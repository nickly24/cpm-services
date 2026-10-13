"""HTTP tenant acceptance for the fixed loopback cpm_dev services.

Creates fresh divisions and synthetic accounts in the two seeded dev organizations.
All source fixtures are created through their owners' public APIs. No database,
Rabbit administration, process restart, or production access is used. Published
history and its membership locks are retained. Tokens never enter reports.
"""
import argparse
from datetime import datetime, timedelta, timezone
import importlib.util
import json
import math
import secrets
import urllib.error
import urllib.parse
import urllib.request
import uuid
from pathlib import Path
from zoneinfo import ZoneInfo


HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('ratings_http_fixture_helpers', HERE / 'http_test.py')
owner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(owner)
EXAMS = 'http://127.0.0.1:8010'
ALLOWED_ORIGINS = frozenset(owner.ORIGINS | {EXAMS})


class NoRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, target):
        return None


class Suite(owner.Suite):
    def __init__(self):
        super().__init__()  # Fixed cpm_dev settings and private seed credentials only.
        self.prefix = 'DEV Tenant Ratings ' + self.run
        self.fixture_path = owner.ROOT / f'dev/local/ratings-tenant-{self.run}-fixtures.json'
        self.report_path = owner.ROOT / 'dev/local/ratings-tenant-report.json'
        self.fixtures.update({'organizations': [1001, 2001], 'schools': [], 'directions': [],
                              'external_tests': [], 'exams': [], 'scopes': []})
        self.scopes, self.organization_admins, self.students = {}, {}, {}
        self.opener = urllib.request.build_opener(NoRedirects())
        self.role_id, self.staff = None, None
        self.restore_role = False

    def persist(self):
        # Later acceptance scripts may reuse our retained synthetic scopes. Build
        # their manifest by an explicit field allowlist; never serialize tokens.
        if hasattr(self, 'scopes'):
            scopes = []
            for scope in self.scopes.values():
                item = {key: scope[key] for key in (
                    'label', 'organization_id', 'division_id', 'direction_id', 'school_id',
                    'assignment_id', 'test_id', 'external_test_id', 'exam_id') if key in scope}
                item['students'] = [{key: student[key] for key in ('account_id', 'login', 'name')}
                                    for student in scope['students']]
                if scope.get('job'):
                    item['job_id'] = scope['job']['id']
                scopes.append(item)
            self.fixtures['scopes'] = scopes
        for name in ('today', 'yesterday', 'period'):
            if getattr(self, name, None) is not None:
                self.fixtures[name] = getattr(self, name)
        self.fixtures['prefix'] = self.prefix
        super().persist()

    def request(self, origin, path, token=None, method='GET', body=None, codes=200,
                headers=None, record=True):
        if origin not in ALLOWED_ORIGINS or not path.startswith('/') or path.startswith('//'):
            raise AssertionError('Only fixed loopback service origins and relative API paths are allowed')
        h = dict(headers or {})
        if token:
            h['Authorization'] = 'Bearer ' + token
        raw = None
        if body is not None:
            raw = json.dumps(body, allow_nan=False).encode()
            h['Content-Type'] = 'application/json'
        request = urllib.request.Request(origin + path, method=method, data=raw, headers=h)
        try:
            with self.opener.open(request, timeout=45) as response:
                code, payload = response.status, response.read()
        except urllib.error.HTTPError as response:
            code, payload = response.code, response.read()
        accepted = (codes,) if isinstance(codes, int) else tuple(codes)
        label = f'{method} {path.split("?")[0]} expected {accepted}, got {code}'
        if record:
            self.expect(code in accepted, label)
        elif code not in accepted:
            raise AssertionError(label)
        if code == 204 and not payload:
            return code, {}
        try:
            result = json.loads(payload)
        except ValueError:
            raise AssertionError('HTTP service returned invalid JSON') from None
        return code, result

    def rating(self, scope, path, token=None, method='GET', body=None, codes=200,
               headers=None, record=True):
        return self.api(owner.RATINGS, f'/v1/ratings/divisions/{scope["division_id"]}', path,
                        token or scope['admin'], method, body, codes, headers, record)

    def hw(self, scope, path, method='GET', body=None, codes=200):
        return self.api(owner.HOMEWORK, f'/v1/homework/divisions/{scope["division_id"]}', path,
                        scope['admin'], method, body, codes)

    def exam(self, scope, path='', method='GET', body=None, codes=200):
        headers = {'Idempotency-Key': str(uuid.uuid4())} if method != 'GET' else None
        return self.api(EXAMS, '/v1/exams', path, scope['admin'], method, body, codes, headers)

    def preflight(self):
        for origin in sorted(ALLOWED_ORIGINS):
            self.request(origin, '/health/ready')
        health = self.api(owner.RATINGS, '', '/health/ready')
        self.expect(health.get('exams_mode') == 'required' and
                    health.get('external_tests_mode') == 'required' and health.get('exams_connected'),
                    'Tenant acceptance requires real exams and external tests, including explicit empty catalogs')
        now = datetime.now(timezone.utc)
        today = now.astimezone(ZoneInfo('Europe/Moscow')).date()
        self.today, self.yesterday = today.isoformat(), (today - timedelta(days=1)).isoformat()
        self.period = {'date_from': (today - timedelta(days=7)).isoformat(),
                       'date_to': (today + timedelta(days=7)).isoformat()}
        # Confirm the external owner has completed initialization before creating fixtures.
        snapshot = self.internal(owner.TESTS, '/internal/tests/ratings/snapshot', 'POST', {
            'organization_id': 1001, 'division_id': 1001, **self.period,
            'as_of': now.isoformat().replace('+00:00', 'Z')})
        domain = snapshot.get('external_domain') or {}
        self.expect(domain.get('schema_version') == 1 and domain.get('status') == 'ready' and
                    domain.get('organization_id') == 1001 and domain.get('division_id') == 1001 and
                    isinstance(snapshot.get('external_tests'), list) and
                    len(snapshot.get('external_source_version', '')) == 64,
                    'External tests owner reports an initialized scoped domain and source version')
        for organization, login, division in [(1001, 'dev_admin', 1001), (2001, 'dev_admin_math', 2001)]:
            token, profile = self.login(login, division, organization, fixture=False)
            self.expect(profile['organization_id'] == organization and profile.get('organization_admin'),
                        'Seed administrator is an organization owner in the expected dev cabinet')
            self.organization_admins[organization] = token

    def create_user(self, scope, suffix, role='student', group=None, role_id=None):
        login = f'dev_ratings_tenant_{self.run}_{suffix}'
        name = self.prefix + ' ' + suffix
        body = {'role': role, 'full_name': name, 'login': login,
                'password': self.password, 'tg_name': ''}
        if role == 'student':
            body.update({'class': 10, 'school_id': scope['school_id']})
        if group is not None:
            body['group_id'] = group
        if role_id is not None:
            body['role_id'] = role_id
        profile = self.core(f'/divisions/{scope["division_id"]}/users', scope['admin'],
                            'POST', body, 201)
        account = profile['account_id']
        self.fixtures['users'].append({'account_id': account, 'login': login, 'role': role,
                                      'organization_id': scope['organization_id'],
                                      'division_id': scope['division_id'], 'full_name': name})
        self.persist()
        token, context = self.login(login, scope['division_id'], scope['organization_id'])
        self.expect(context['account_id'] == account and context['division_id'] == scope['division_id'] and
                    context['organization_id'] == scope['organization_id'],
                    'Fresh account is authenticated in exactly its owning scope')
        return {'account_id': account, 'login': login, 'name': name, 'token': token}

    def setup(self):
        self.preflight()
        dictionaries = {}
        for organization, admin in self.organization_admins.items():
            school = self.core('/schools', admin, 'POST', {
                'name': self.prefix + f' school {organization}', 'short_name': 'Tenant ' + self.run,
                'notes': 'Synthetic isolated HTTP acceptance', 'is_active': True}, 201)['id']
            direction = self.core('/directions', admin, 'POST', {
                'name': self.prefix + f' direction {organization}'}, 201)['id']
            self.fixtures['schools'].append(school)
            self.fixtures['directions'].append(direction)
            dictionaries[organization] = {'school_id': school, 'direction_id': direction}
            self.persist()
        for label, organization in [('A1', 1001), ('A2', 1001), ('B1', 2001), ('B2', 2001)]:
            bootstrap_admin = self.organization_admins[organization]
            division = self.core('/divisions', bootstrap_admin, 'POST', {
                'name': self.prefix + ' ' + label}, 201)['id']
            self.fixtures['divisions'].append(division)
            scope = {'label': label, 'organization_id': organization, 'division_id': division,
                     **dictionaries[organization], 'students': [], 'groups': []}
            self.scopes[label] = scope
            self.persist()
            scope['admin'], profile = self.login('dev_admin' if organization == 1001 else 'dev_admin_math',
                                                  division, organization, fixture=False)
            self.expect(profile['division_id'] == division, 'Source owners receive an admin token in the new division')
            self.verify_empty_sources(scope)
            for group_suffix in ['first', 'second']:
                group = self.core(f'/divisions/{division}/groups', scope['admin'], 'POST', {
                    'name': self.prefix + ' ' + label + ' ' + group_suffix}, 201)['id']
                self.fixtures['groups'].append(group)
                scope['groups'].append(group)
            self.persist()
        self.division, self.secondary = self.scopes['A1']['division_id'], self.scopes['A2']['division_id']
        for label, scope in self.scopes.items():
            if label == 'A2':
                shared = self.scopes['A1']['students'][0]
                self.core(f'/divisions/{scope["division_id"]}/memberships/{shared["account_id"]}',
                          scope['admin'], 'PUT', {})
                self.core(f'/divisions/{scope["division_id"]}/groups/{scope["groups"][0]}/members',
                          scope['admin'], 'POST', {'account_id': shared['account_id']})
                token, profile = self.login(shared['login'], scope['division_id'], scope['organization_id'])
                self.expect(profile['account_id'] == shared['account_id'],
                            'One fresh account owns independent memberships in A1 and A2')
                scope['students'].append({**shared, 'token': token})
            else:
                scope['students'].append(self.create_user(scope, label + '_primary', group=scope['groups'][0]))
            scope['students'].append(self.create_user(scope, label + '_peer', group=scope['groups'][1]))
            self.persist()
        a1, a2 = self.scopes['A1'], self.scopes['A2']
        self.role_name = self.prefix + ' delegated ratings'
        self.role_id = self.core('/roles', a1['admin'], 'POST', {
            'name': self.role_name, 'permissions': {'ratings': {'view': True, 'edit': True}}}, 201)['id']
        self.fixtures['roles'].append(self.role_id)
        self.staff = self.create_user(a1, 'delegated_admin', 'staff_admin', role_id=self.role_id)
        self.proctor = self.create_user(a1, 'proctor', 'proctor', group=a1['groups'][0])
        self.core(f'/divisions/{a2["division_id"]}/memberships/{self.staff["account_id"]}', a2['admin'],
                  'PUT', {'role_id': self.role_id})
        self.staff_secondary, _ = self.login(self.staff['login'], a2['division_id'], a2['organization_id'])
        self.persist()
        # Different source values expose any accidental cross-tenant/source join.
        values = {'A1': [(80, 100, 40, 4), (0, None, None, None)],
                  'A2': [(20, 0, 80, 2), (100, 100, 100, 5)],
                  'B1': [(40, 100, 20, 1), (0, None, None, None)],
                  'B2': [(60, 0, 60, 3), (0, None, None, None)]}
        for label, scope in self.scopes.items():
            scope['expected'] = values[label]
            self.create_sources(scope)

    def verify_empty_sources(self, scope):
        request = {'organization_id': scope['organization_id'], 'division_id': scope['division_id'],
                   **self.period, 'as_of': datetime.now(timezone.utc).isoformat().replace('+00:00', 'Z')}
        tests = self.internal(owner.TESTS, '/internal/tests/ratings/snapshot', 'POST', request)
        version = tests.get('external_source_version', '')
        self.expect(tests.get('tests') == [] and tests.get('external_tests') == [] and
                    (tests.get('external_domain') or {}).get('schema_version') == 1 and
                    (tests.get('external_domain') or {}).get('organization_id') == scope['organization_id'] and
                    (tests.get('external_domain') or {}).get('division_id') == scope['division_id'] and
                    (tests.get('external_domain') or {}).get('status') == 'ready' and
                    len(version) == 64 and all(c in '0123456789abcdefABCDEF' for c in version),
                    'A genuinely empty new test catalog is explicit and scoped in the initialized owner domain')
        homework = self.internal(owner.HOMEWORK, '/internal/homework/ratings/snapshot', 'POST', request)
        self.expect(homework['assignments'] == [], 'The new division starts with no inherited homework sources')
        query = urllib.parse.urlencode({'organization_id': scope['organization_id'],
                                       'division_ids': scope['division_id'], **self.period, 'as_of': request['as_of']})
        exams = self.internal(EXAMS, '/internal/exams/rating-snapshot?' + query)['snapshot']
        self.expect(exams['exams'] == [] and exams['results'] == [],
                    'The new division starts with no inherited exam catalog or results')

    def create_sources(self, scope):
        ov = self.hw(scope, '/types', 'POST', {'name': 'ОВ'}, 201)['id']
        self.fixtures['types'].append(ov)
        assignment = self.hw(scope, '/assignments', 'POST', {
            'name': self.prefix + ' ' + scope['label'] + ' homework', 'type_id': ov,
            'deadline': self.yesterday, 'published': False}, 201)['id']
        self.fixtures['assignments'].append(assignment)
        scope['assignment_id'] = assignment
        self.persist()
        for student, (homework, _, _, _) in zip(scope['students'], scope['expected']):
            self.hw(scope, f'/assignments/{assignment}/manual', 'POST', {
                'account_id': student['account_id'], 'date_pass': self.yesterday,
                'status': 1, 'result': homework})
        test = self.tests(f'/divisions/{scope["division_id"]}/tests', scope['admin'], 'POST', {
            'title': self.prefix + ' ' + scope['label'] + ' official',
            'description': 'Synthetic tenant acceptance', 'direction_id': scope['direction_id'],
            'questions': [{'question_id': 1, 'type': 'single', 'text': 'Choose A', 'points': 2,
                           'answers': [{'id': 'a', 'text': 'A', 'is_correct': True},
                                       {'id': 'b', 'text': 'B', 'is_correct': False}],
                           'correct_answers': []}]}, 201, {'Idempotency-Key': secrets.token_hex(16)})
        scope['test_id'] = test['id']
        self.fixtures['tests'].append(test['id'])
        self.persist()
        now = datetime.now(timezone.utc)
        self.tests(f'/divisions/{scope["division_id"]}/tests/{test["id"]}/settings', scope['admin'], 'PUT', {
            'expected_revision': test['revision'], 'published': True,
            'start_at': (now - timedelta(minutes=10)).isoformat(),
            'end_at': (now + timedelta(hours=2)).isoformat(), 'time_limit_minutes': 60,
            'passing_score': 50, 'answers_visible': True})
        for student, (_, score, _, _) in zip(scope['students'], scope['expected']):
            if score is None:
                continue
            attempt = self.tests(f'/divisions/{scope["division_id"]}/tests/{test["id"]}/attempts',
                                 student['token'], 'POST', {'practice': False}, 201)
            self.fixtures['attempts'].append(attempt['id'])
            self.persist()
            accepted = self.tests('/attempts/' + attempt['id'] + '/finalize', student['token'], 'POST', {
                'test_version_id': attempt['test_version_id'], 'reason': 'manual',
                'completed_at': datetime.now(timezone.utc).isoformat(),
                'answers': [{'question_id': 1, 'value': 'a' if score == 100 else 'b'}]}, 202)
            submission = accepted['submission_id']
            def ready():
                row = self.tests('/submissions/' + submission, student['token'], record=False)
                if row['calculation_status'] == 'failed':
                    raise AssertionError('Owned official test calculation failed')
                return row if row['calculation_status'] == 'ready' else None
            result = self.wait(ready, 'Owned official result is ready through calculate before rating capture')
            self.expect(result['result']['score'] == score, 'Official owner score matches this tenant fixture')
        external = self.tests(f'/divisions/{scope["division_id"]}/external-tests', scope['admin'], 'POST', {
            'name': self.prefix + ' ' + scope['label'] + ' external',
            'direction_id': scope['direction_id'], 'date': self.today}, 201,
            {'Idempotency-Key': secrets.token_hex(16)})
        scope['external_test_id'] = external['id']
        self.fixtures['external_tests'].append(external['id'])
        self.persist()
        for student, (_, _, score, _) in zip(scope['students'], scope['expected']):
            if score is not None:
                external = self.tests(f'/divisions/{scope["division_id"]}/external-tests/{external["id"]}/results/{student["account_id"]}',
                                      scope['admin'], 'PUT', {'expected_revision': external['revision'],
                                      'expected_result_revision': 0, 'score': score}, 200,
                                      {'Idempotency-Key': secrets.token_hex(16)})
        exam = self.exam(scope, method='POST', body={'exam_type': 'outside_lms',
                         'name': self.prefix + ' ' + scope['label'] + ' exam',
                         'direction_id': scope['direction_id'], 'date': self.today}, codes=201)
        scope['exam_id'] = exam['id']
        self.fixtures['exams'].append(exam['id'])
        self.persist()
        for student, (_, _, _, grade) in zip(scope['students'], scope['expected']):
            if grade is not None:
                self.exam(scope, f'/{exam["id"]}/outside-results', 'POST', {
                    'student_account_id': student['account_id'], 'grade': grade,
                    'points': 923.45, 'examinator': self.prefix})
        self.persist()

    def publish(self):
        common_key = secrets.token_hex(16)
        for scope in self.scopes.values():
            created = self.rating(scope, '/jobs', method='POST', body=self.period, codes=202,
                                  headers={'Idempotency-Key': common_key})['job']
            scope['job'] = created
            self.fixtures['jobs'].append({'id': created['id'], 'organization_id': scope['organization_id'],
                                         'division_id': scope['division_id'], 'idempotency_key': common_key})
            self.persist()
            replay = self.rating(scope, '/jobs', method='POST', body=self.period,
                                 headers={'Idempotency-Key': common_key})['job']
            self.expect(replay['id'] == created['id'], 'Idempotency identity is stable within its tenant scope')
        self.expect(len({scope['job']['id'] for scope in self.scopes.values()}) == 4,
                    'The same idempotency key creates independent jobs in all four tenant scopes')
        for scope in self.scopes.values():
            stages = []
            def completed():
                job = self.rating(scope, '/jobs/' + scope['job']['id'], record=False)
                stage = (job['status'], job['processed_count'], job['total_count'])
                if not stages or stages[-1] != stage:
                    stages.append(stage)
                if job['status'] in ('failed', 'cancelled', 'superseded'):
                    raise AssertionError('Owned tenant rating job entered terminal failure: ' + job.get('error_code', ''))
                return job if job['status'] == 'completed' else None
            scope['job'] = self.wait(completed, 'Tenant job publishes all participants asynchronously', timeout=150)
            self.persist()
            self.progress[scope['job']['id']] = stages
            self.expect(scope['job']['total_count'] == 2 and scope['job']['processed_count'] == 2,
                        'Tenant publication contains exactly its two current participants')
            self.verify_scope(scope)

    def verify_scope(self, scope):
        job, students = scope['job'], scope['students']
        listing = self.rating(scope, '/ratings')
        rows = {r['profile']['account_id']: r for r in listing['ratings']}
        scope['rows'] = rows
        accounts = {s['account_id'] for s in students}
        self.expect(listing['total'] == 2 and set(rows) == accounts,
                    'Admin rating list contains exactly the selected tenant accounts')
        self.expect(listing['freshness']['version_id'] == job['id'] and not listing['freshness']['is_stale'],
                    'Tenant sources match the completed publication fingerprint')
        for student, (homework, internal, external, exam) in zip(students, scope['expected']):
            row, result = rows[student['account_id']], rows[student['account_id']]['rating']
            tests = ((internal or 0) + (external or 0)) / 2
            expected = homework * .25 + (exam or 0) * 6 + tests * .45
            self.expect(math.isclose(result['homework']['rating'], homework, abs_tol=1e-9) and
                        math.isclose(result['tests']['rating'], tests, abs_tol=1e-9) and
                        math.isclose(result['exams']['rating'], exam or 0, abs_tol=1e-9) and
                        math.isclose(result['final_rating'], expected, abs_tol=1e-9),
                        'Distinct tenant source values produce the manual legacy formula')
            self.expect(result['organization_id'] == scope['organization_id'] and
                        result['division_id'] == scope['division_id'] and result['job_id'] == job['id'] and
                        result['account_id'] == student['account_id'], 'Published result metadata is scoped to its own job')
            self.expect({v['homework_id'] for v in result['homework']['details']} == {scope['assignment_id']} and
                        {v['test_id'] for v in result['tests']['details']} == {scope['test_id'], scope['external_test_id']} and
                        {v['exam_id'] for v in result['exams']['details']} == {str(scope['exam_id'])},
                        'Published details contain only source objects of this tenant')
            own = self.rating(scope, '/me', student['token'])['rating']
            self.expect(own == row, 'Student cabinet returns only their own scoped published rating')
        def delivered():
            items = self.internal(owner.CALCULATE, '/internal/calculate/ratings/messages?' + urllib.parse.urlencode({
                'job_id': job['id'], 'limit': 100}), record=False)['items']
            return items if len(items) == 2 and all(m['status'] == 'ready' for m in items) else None
        messages = self.wait(delivered, 'Both asynchronous tenant callbacks become durable in calculate inbox')
        self.expect(len(messages) == 2 and {m['account_id'] for m in messages} == accounts and
                    all(m['status'] == 'ready' and m['organization_id'] == scope['organization_id'] and
                        m['division_id'] == scope['division_id'] and m['revision'] == job['revision'] for m in messages),
                    'Durable calculate inbox confirms two ready scoped Rabbit deliveries')
        report = self.rating(scope, '/report?version=' + job['id'])
        self.expect(report['period'] == self.period and report['students'] == listing['ratings'],
                    'Export JSON contains the exact selected publication and period')
        versions = self.rating(scope, '/versions')['versions']
        jobs = self.rating(scope, '/jobs?limit=1&offset=0')['jobs']
        self.expect(len(versions) == 1 and versions[0]['id'] == job['id'] and len(jobs) == 1 and
                    all(v['organization_id'] == scope['organization_id'] and v['division_id'] == scope['division_id']
                        for v in versions + jobs), 'Job and version catalogs never combine neighboring scopes')
        filtered = self.rating(scope, '/ratings?q=' + urllib.parse.quote(students[1]['name']))
        self.expect(filtered['total'] == 1 and filtered['ratings'][0] == rows[students[1]['account_id']],
                    'Admin search filters rows while retaining their full division rank')

    def authorization(self):
        a1, a2, b1, b2 = (self.scopes[k] for k in ('A1', 'A2', 'B1', 'B2'))
        # Organization owners may open another division in their organization using the same token.
        self.expect(self.rating(a2, '/ratings', a1['admin'])['total'] == 2,
                    'Full organization admin can explicitly select another owned division')
        self.expect(self.rating(b2, '/ratings', b1['admin'])['total'] == 2,
                    'The second organization admin can select its other owned division')
        for local, foreign in [(a1, b1), (b1, a1), (a2, b2), (b2, a2)]:
            for path in ['/ratings', '/report', '/versions', '/jobs', '/jobs/' + foreign['job']['id'],
                         '/accounts/' + str(foreign['students'][0]['account_id'])]:
                self.rating(foreign, path, local['admin'], codes=(403, 404))
            self.rating(foreign, '/jobs', local['admin'], 'POST', self.period, (403, 404),
                        {'Idempotency-Key': secrets.token_hex(16)})
            self.assert_foreign_objects(local, foreign, local['admin'])
        self.assert_foreign_objects(a1, a2, a1['admin'])
        self.assert_foreign_objects(a2, a1, a2['admin'])
        for path in ['/ratings', '/report', '/versions', '/jobs']:
            self.rating(a2, path, self.staff['token'], codes=403)
            self.rating(b1, path, self.staff['token'], codes=403)
        own_staff = self.rating(a1, '/ratings', self.staff['token'])
        self.expect(own_staff['total'] == 2, 'Delegated admin view is limited to the selected membership')
        self.expect(self.rating(a2, '/ratings', self.staff_secondary)['total'] == 2,
                    'A delegated admin can view its second participation after selecting that cabinet')
        self.rating(a1, '/report', self.staff_secondary, codes=403)
        self.rating(b1, '/report', self.staff_secondary, codes=403)
        filtered = self.rating(a1, '/ratings?q=' + urllib.parse.quote(b1['students'][0]['name']), self.staff['token'])
        self.expect(filtered['total'] == 0 and filtered['ratings'] == [], 'Delegated search cannot discover a foreign organization account')
        for scope in self.scopes.values():
            first, second = scope['students']
            own = self.rating(scope, '/ratings', first['token'])
            self.expect(own['total'] == 1 and len(own['ratings']) == 1 and
                        own['ratings'][0]['profile']['account_id'] == first['account_id'],
                        'Student list totals exclude every other participant')
            self.rating(scope, '/accounts/' + str(second['account_id']), first['token'], codes=404)
            for path in ['/jobs', '/jobs/' + scope['job']['id'], '/versions', '/report']:
                self.rating(scope, path, first['token'], codes=403)
            self.rating(scope, '/jobs', first['token'], 'POST', self.period, 403,
                        {'Idempotency-Key': secrets.token_hex(16)})
        shared_a1, shared_a2 = a1['students'][0], a2['students'][0]
        self.expect(shared_a1['account_id'] == shared_a2['account_id'] and
                    a1['rows'][shared_a1['account_id']]['rating']['final_rating'] !=
                    a2['rows'][shared_a2['account_id']]['rating']['final_rating'],
                    'The same account retains different grades and places across memberships')
        self.rating(a2, '/me', shared_a1['token'], codes=403)
        self.rating(a1, '/me', shared_a2['token'], codes=403)
        proctor = self.rating(a1, '/ratings', self.proctor['token'])
        self.expect(proctor['total'] == 1 and proctor['ratings'][0]['profile']['account_id'] == shared_a1['account_id'],
                    'Proctor rows and totals are restricted to the current own group')
        self.rating(a1, '/accounts/' + str(a1['students'][1]['account_id']), self.proctor['token'], codes=404)
        self.rating(a1, '/report', self.proctor['token'], codes=403)
        self.rating(a1, '/jobs', method='POST', body={**self.period, 'organization_id': 2001}, codes=400,
                    headers={'Idempotency-Key': secrets.token_hex(16)})
        self.rating(a1, '/jobs', method='POST', body={**self.period, 'division_id': b1['division_id']}, codes=400,
                    headers={'Idempotency-Key': secrets.token_hex(16)})
        path = f'/v1/ratings/divisions/{a1["division_id"]}/report'
        self.request(owner.RATINGS, path, codes=401, headers={'X-Role': 'admin', 'X-Organization-ID': '1001'})
        self.request(owner.RATINGS, path, a1['students'][0]['token'], codes=403,
                     headers={'X-Role': 'admin', 'X-Organization-ID': '1001', 'X-Division-ID': str(a1['division_id'])})
        spoofed_query = self.rating(a1, '/report?' + urllib.parse.urlencode({
            'organization_id': 2001, 'division_id': b1['division_id']}))
        self.expect({r['profile']['account_id'] for r in spoofed_query['students']} ==
                    {s['account_id'] for s in a1['students']},
                    'Untrusted scope headers and query fields cannot select foreign organization data')

    def assert_foreign_objects(self, local, foreign, token):
        foreign_job = foreign['job']['id']
        self.rating(local, '/jobs/' + foreign_job, token, codes=404)
        for endpoint in ['/ratings', '/report', '/accounts/' + str(local['students'][0]['account_id'])]:
            self.rating(local, endpoint + '?version=' + foreign_job, token, codes=404)
        for action in ['cancel', 'retry']:
            self.rating(local, '/jobs/' + foreign_job + '/' + action, token, 'POST', {}, 404,
                        {'Idempotency-Key': secrets.token_hex(16)})
        foreign_account = foreign['students'][-1]['account_id']
        self.rating(local, '/accounts/' + str(foreign_account), token, codes=404)
        self.expect(all(j['id'] != foreign_job for j in self.rating(local, '/jobs', token)['jobs']),
                    'Foreign job identifiers are excluded from the local catalog')

    def membership_locks(self):
        a1, a2 = self.scopes['A1'], self.scopes['A2']
        shared = a1['students'][0]['account_id']
        memberships_path = f'/organization/users/{shared}/memberships'
        before_memberships = self.core(memberships_path, a1['admin'])
        owned = {a1['division_id'], a2['division_id']}
        self.expect({m['division_id'] for m in before_memberships['memberships']} == owned,
                    'The owned shared student has exactly both rated memberships before deletion attempts')
        snapshots = {}
        for scope in [a1, a2]:
            account = scope['rows'][shared]['profile']
            group = account['group_id']
            self.expect(scope['job']['status'] == 'completed' and isinstance(group, int) and
                        group in scope['groups'], 'Membership lock probe is restricted to completed owned synthetic scopes')
            request = {'organization_id': scope['organization_id'], 'division_id': scope['division_id'],
                       **self.period, 'as_of': datetime.now(timezone.utc).isoformat().replace('+00:00', 'Z')}
            snapshots[scope['division_id']] = (request, self.internal(
                owner.CORE, '/internal/core/ratings/snapshot', 'POST', request))
            code = None
            try:
                code, response = self.request(owner.CORE,
                    f'/v1/core/divisions/{scope["division_id"]}/memberships/{shared}', scope['admin'],
                    'DELETE', body=None, codes=(409, 200, 201, 202, 204))
                self.expect(code == 409 and response.get('error') == 'rating_exists',
                            'A published rating permanently blocks removal of its owned division participation')
            finally:
                if code is None or 200 <= code < 300:
                    # Current revokeGrant removes auth_division_access, including
                    # its group_id. Restore only our own lost participation and
                    # the original group if a regression unexpectedly accepts it
                    # (or loses the response after a possible commit).
                    self.core(f'/divisions/{scope["division_id"]}/memberships/{shared}',
                              scope['admin'], 'PUT', {})
                    self.core(f'/divisions/{scope["division_id"]}/groups/{group}/members',
                              scope['admin'], 'POST', {'account_id': shared})
        after_memberships = self.core(memberships_path, a1['admin'])
        self.expect(after_memberships == before_memberships,
                    'Denied removal preserves both scoped memberships and their original groups')
        for scope in [a1, a2]:
            request, before = snapshots[scope['division_id']]
            self.expect(self.internal(owner.CORE, '/internal/core/ratings/snapshot', 'POST', request) == before,
                        'Denied removal preserves the exact Core rating input snapshot')
            listing = self.rating(scope, '/ratings')
            self.expect(listing['freshness']['version_id'] == scope['job']['id'] and
                        {r['profile']['account_id']: r for r in listing['ratings']} == scope['rows'],
                        'Denied removal preserves both published rating results and selected versions')

    def revoke_permissions(self):
        scope = self.scopes['A1']
        old = self.staff['token']
        self.restore_role = True
        self.set_permissions(view=True, edit=False)
        self.expect(self.rating(scope, '/report', old)['period'] == self.period,
                    'A live permission downgrade keeps permitted report access')
        for path, body in [('/jobs', self.period), ('/jobs/' + scope['job']['id'] + '/cancel', {}),
                           ('/jobs/' + scope['job']['id'] + '/retry', {})]:
            self.rating(scope, path, old, 'POST', body, 403, {'Idempotency-Key': secrets.token_hex(16)})
        self.set_permissions(view=False, edit=False)
        for path in ['/ratings', '/accounts/' + str(scope['students'][0]['account_id']), '/report',
                     '/report?version=' + scope['job']['id'], '/versions', '/jobs', '/jobs/' + scope['job']['id']]:
            self.rating(scope, path, old, codes=403)
        self.rating(self.scopes['A2'], '/report', self.staff_secondary, codes=403)
        self.rating(scope, '/jobs', old, 'POST', self.period, 403, {'Idempotency-Key': secrets.token_hex(16)})
        self.expect(True, 'Existing token immediately loses read, export, version, job and write access after role revocation')
        self.set_permissions(view=True, edit=True)
        self.restore_role = False
        self.expect(self.rating(scope, '/ratings', old)['total'] == 2,
                    'Restoring the owned role restores rights through fresh auth introspection')
        self.expect(self.rating(self.scopes['A2'], '/ratings', self.staff_secondary)['total'] == 2,
                    'Restored live role applies independently to both delegated memberships')

    def set_permissions(self, view, edit):
        self.core('/roles/' + str(self.role_id), self.scopes['A1']['admin'], 'PUT', {
            'name': self.role_name, 'permissions': {'ratings': {'view': view, 'edit': edit}}})

    def switch_contexts(self):
        a1, a2 = self.scopes['A1'], self.scopes['A2']
        original = a1['students'][0]
        token, profile = self.login(original['login'], a1['division_id'], 1001)
        current = token
        for scope in [a2, a1]:
            _, response = self.request(owner.AUTH, '/v1/auth/context', current, 'POST', {
                'organization_id': scope['organization_id'], 'division_id': scope['division_id']})
            next_token = response.get('access_token') or response['token']
            self.sessions.append(next_token)
            self.expect(response['user']['account_id'] == original['account_id'] and
                        response['user']['division_id'] == scope['division_id'],
                        'Context change preserves account identity and selects the owned membership')
            self.rating(a1, '/me', current, codes=401)
            self.expect(self.rating(scope, '/me', next_token)['rating'] == scope['rows'][original['account_id']],
                        'Changed cabinet reads the matching published membership')
            current = next_token
        # The public grant API deliberately cannot create a fresh cross-organization account.
        # The existing multi-organization seed is read only; only our new login session changes.
        current, profile = self.login('dev_student_01', 1001, 1001, fixture=False)
        account = profile['account_id']
        old = []
        for organization, division in [(2001, 2001), (1001, 1002), (1001, 1001)]:
            old.append(current)
            _, response = self.request(owner.AUTH, '/v1/auth/context', current, 'POST', {
                'organization_id': organization, 'division_id': division})
            current = response.get('access_token') or response['token']
            self.sessions.append(current)
            self.expect(response['user']['account_id'] == account and
                        response['user']['organization_id'] == organization and
                        response['user']['division_id'] == division,
                        'Synthetic multi-organization account switches among its existing memberships')
            for replaced in old:
                self.rating(a1, '/report', replaced, codes=401)
            self.rating(a1, '/report', current, codes=403)
            self.rating(self.scopes['B1'], '/report', current, codes=403)

    def callbacks(self):
        a1, b1 = self.scopes['A1'], self.scopes['B1']
        student = a1['students'][0]
        job = a1['job']
        result = a1['rows'][student['account_id']]['rating']
        base = f'/internal/ratings/jobs/{job["id"]}/accounts/{student["account_id"]}'
        self.request(owner.RATINGS, base + '/input', a1['admin'], codes=401)
        self.request(owner.RATINGS, base + '/result', a1['admin'], 'POST', result, 401)
        self.request(owner.RATINGS, base + '/result', student['token'], 'POST', result, 401)
        for field, value in [('organization_id', 2001), ('division_id', b1['division_id']),
                             ('job_id', b1['job']['id']), ('revision', result['revision'] + 1),
                             ('account_id', b1['students'][0]['account_id']), ('input_hash', '0' * 64)]:
            self.internal(owner.RATINGS, base + '/result', 'POST', {**result, field: value}, 409)
        foreign_path = f'/internal/ratings/jobs/{b1["job"]["id"]}/accounts/{b1["students"][0]["account_id"]}/result'
        self.internal(owner.RATINGS, foreign_path, 'POST', result, 409)
        missing_account = b1['students'][0]['account_id']
        missing_base = f'/internal/ratings/jobs/{job["id"]}/accounts/{missing_account}'
        self.internal(owner.RATINGS, missing_base + '/input', codes=404)
        self.internal(owner.RATINGS, missing_base + '/result', 'POST', {**result, 'account_id': missing_account}, 404)
        self.internal(owner.RATINGS, base + '/result', 'POST', {**result, 'final_rating': result['final_rating'] + 1}, 409)
        accepted = self.internal(owner.RATINGS, base + '/result', 'POST', result)
        self.expect(accepted['accepted'], 'An exact completed callback remains idempotently acknowledged')
        for scope in [a1, b1]:
            after = self.rating(scope, '/jobs/' + scope['job']['id'])
            listing = self.rating(scope, '/ratings')
            self.expect(after['status'] == 'completed' and after['processed_count'] == 2 and
                        after['total_count'] == 2 and listing['freshness']['version_id'] == scope['job']['id'] and
                        {r['profile']['account_id']: r for r in listing['ratings']} == scope['rows'],
                        'Rejected and repeated callbacks preserve both tenant publications and progress')

    def finish(self, error=None):
        if self.restore_role and self.role_id is not None:
            try:
                self.set_permissions(view=True, edit=True)
                self.restore_role = False
            except Exception:
                error = AssertionError('Owned synthetic staff role could not be restored')
        for token in self.sessions:
            try:
                self.request(owner.AUTH, '/v1/auth/logout', token, 'POST', {}, (200, 401), record=False)
            except Exception:
                pass
        self.persist()
        report = {'status': 'failed' if error else 'passed', 'run_id': self.run,
                  'http_checks': len(self.checks), 'scenarios': self.checks, 'job_progress': self.progress,
                  'organizations': [1001, 2001],
                  'scopes': [{key: scope[key] for key in ('label', 'organization_id', 'division_id')}
                             for scope in self.fixtures['scopes']],
                  'fixture_manifest': str(self.fixture_path.relative_to(owner.ROOT)),
                  'job_ids': [j['id'] for j in self.fixtures['jobs']],
                  'target': owner.RATINGS, 'exams_mode': 'required', 'external_tests_mode': 'required',
                  'production_accessed': False, 'database_accessed': False,
                  'rabbit_administration_used': False, 'services_restarted': False,
                  'fixtures_retained': True, 'role_restored': not self.restore_role,
                  'export_format': 'owner JSON report; client generates export',
                  'cross_organization_membership': 'existing synthetic seed, session changes only'}
        if error:
            report['failure'] = str(error) if isinstance(error, AssertionError) else type(error).__name__
        owner.private_json(self.report_path, report)
        print(json.dumps({'status': report['status'], 'http_checks': report['http_checks'],
                          'run_id': self.run, 'fixtures_retained': True}))
        return error


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--allow-fixtures', action='store_true',
                        help='Create and retain synthetic fixtures in both fixed cpm_dev organizations.')
    arguments = parser.parse_args()
    if not arguments.allow_fixtures:
        parser.error('Specify --allow-fixtures to run the HTTP acceptance against the private local dev stack.')
    suite, error = None, None
    try:
        suite = Suite()
        suite.setup()
        suite.publish()
        suite.authorization()
        suite.membership_locks()
        suite.revoke_permissions()
        suite.switch_contexts()
        suite.callbacks()
    except Exception as failure:
        error = failure
    finally:
        if suite is not None:
            error = suite.finish(error)
    if error:
        raise SystemExit('Ratings tenant HTTP acceptance failed; see the private local report.')


if __name__ == '__main__':
    main()
