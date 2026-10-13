"""HTTP-only period/source regression on a successful retained tenant fixture.

Run only after tenant_test.py succeeds and the source owners are ready. Mutations
to sources are limited to that run's A1 division; the other scopes are controls. New
fixtures and rating history are retained. No DB, Rabbit administration, imports,
process control, or third-party Python packages are used. Credentials stay in
private local files and never enter the report.
"""
import argparse
from datetime import date, datetime, timedelta, timezone
import json
import math
import os
from pathlib import Path
import re
import secrets
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from zoneinfo import ZoneInfo


ROOT = Path(__file__).resolve().parents[3]
LOCAL = ROOT / 'dev/local'
AUTH, CORE, TESTS = ('http://127.0.0.1:' + str(p) for p in (8002, 8003, 8004))
CALCULATE, HOMEWORK = ('http://127.0.0.1:' + str(p) for p in (8005, 8006))
EXAMS, RATINGS = 'http://127.0.0.1:8010', 'http://127.0.0.1:8012'
ORIGINS = frozenset((AUTH, CORE, TESTS, CALCULATE, HOMEWORK, EXAMS, RATINGS))
MOSCOW = ZoneInfo('Europe/Moscow')

# Independent hand-calculated expectations for tenant_test.py's fixed fixtures.
# These are regression examples, not another implementation of the formula.
# Tuple order: H, T, E, final; primary student then peer student.
EXPECTED = {
    'yesterday': {'A1': [(80, 0, 0, 20), (0, 0, 0, 0)],
                  'A2': [(20, 0, 0, 5), (100, 0, 0, 25)],
                  'B1': [(40, 0, 0, 10), (0, 0, 0, 0)],
                  'B2': [(60, 0, 0, 15), (0, 0, 0, 0)]},
    'today': {'A1': [(0, 70, 4, 55.5), (0, 0, 0, 0)],
              'A2': [(0, 40, 2, 30), (0, 100, 5, 75)],
              'B1': [(0, 60, 1, 33), (0, 0, 0, 0)],
              'B2': [(0, 30, 3, 31.5), (0, 0, 0, 0)]},
    'tomorrow': {label: [(0, 0, 0, 0), (0, 0, 0, 0)] for label in ('A1', 'A2', 'B1', 'B2')},
    'full': {'A1': [(80, 70, 4, 75.5), (0, 0, 0, 0)],
             'A2': [(20, 40, 2, 35), (100, 100, 5, 100)],
             'B1': [(40, 60, 1, 43), (0, 0, 0, 0)],
             'B2': [(60, 30, 3, 46.5), (0, 0, 0, 0)]},
}


def private_json(path, value):
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(descriptor, 'w') as output:
        os.fchmod(output.fileno(), 0o600)
        json.dump(value, output, indent=2, ensure_ascii=False, allow_nan=False)
        output.write('\n')


def read_env(path):
    values = {}
    for line in path.read_text().splitlines():
        name, separator, value = line.partition('=')
        if not separator or line.lstrip().startswith('#'):
            continue
        try:
            values[name.strip()] = json.loads(value)
        except ValueError:
            values[name.strip()] = value.strip().strip('\'"')
    return values


class NoRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, target):
        return None


class Suite:
    def __init__(self, fixture_path=None):
        env = read_env(ROOT / 'dev/.env')
        if (env.get('MYSQL_DATABASE') != 'cpm_dev' or env.get('MONGO_DATABASE') != 'cpm_dev'
                or str(env.get('MYSQL_PORT')) != '13306' or str(env.get('MONGO_PORT')) != '27018'
                or len(env.get('AUTH_INTERNAL_KEY', '')) < 32):
            raise AssertionError('Only the fixed cpm_dev loopback configuration is permitted')
        self.key = env['AUTH_INTERNAL_KEY']
        tenant_report = json.loads((LOCAL / 'ratings-tenant-report.json').read_text())
        if tenant_report.get('status') != 'passed' or not re.fullmatch(r'[0-9a-f]{12}', tenant_report.get('run_id', '')):
            raise AssertionError('A successful fresh tenant acceptance report is required')
        self.run = tenant_report['run_id']
        expected_path = LOCAL / f'ratings-tenant-{self.run}-fixtures.json'
        self.source_path = Path(fixture_path).resolve() if fixture_path else expected_path
        if self.source_path != expected_path.resolve():
            raise AssertionError('The fixture must match the latest successful private tenant report')
        self.source = json.loads(self.source_path.read_text())
        if self.source.get('run_id') != self.run or not self.source.get('password'):
            raise AssertionError('The retained private tenant fixture does not match its successful report')
        self.seed_password = json.loads((LOCAL / 'accounts.json').read_text())['password']
        self.password = self.source['password']
        self.nonce = secrets.token_hex(6)
        self.prefix = 'DEV Tenant Ratings ' + self.run
        self.fixture_path = LOCAL / f'ratings-period-{self.run}-{self.nonce}-fixtures.json'
        self.report_path = LOCAL / 'ratings-period-sources-report.json'
        self.opener = urllib.request.build_opener(NoRedirects())
        self.sessions, self.checks, self.scopes, self.progress = [], [], {}, {}
        self.phase = 'preflight'
        self.fixtures = {'tenant_run_id': self.run, 'period_run_id': self.nonce,
                         'source_manifest': self.source_path.name, 'assignments': [],
                         'external_tests': [], 'jobs': [], 'outside_updates': [], 'retained': True,
                         'original_freshness': {}}

    def expect(self, condition, label):
        if not condition:
            raise AssertionError(label)
        self.checks.append(label)

    def persist(self):
        self.fixtures['phase'] = self.phase
        private_json(self.fixture_path, self.fixtures)

    def same_day(self):
        self.expect(datetime.now(MOSCOW).date().isoformat() == self.today,
                    'The period regression stays within the original Moscow calendar date')

    def request(self, origin, path, token=None, method='GET', body=None, codes=200,
                headers=None, record=True):
        if origin not in ORIGINS or not path.startswith('/') or path.startswith('//'):
            raise AssertionError('Only fixed loopback origins and relative owner API paths are permitted')
        h = dict(headers or {})
        if token:
            h['Authorization'] = 'Bearer ' + token
        raw = None
        if body is not None:
            raw = json.dumps(body, allow_nan=False).encode()
            h['Content-Type'] = 'application/json'
        request = urllib.request.Request(origin + path, data=raw, method=method, headers=h)
        try:
            with self.opener.open(request, timeout=45) as response:
                code, payload = response.status, response.read((32 << 20) + 1)
        except urllib.error.HTTPError as response:
            code, payload = response.code, response.read((32 << 20) + 1)
        except urllib.error.URLError:
            raise AssertionError('A fixed local source service is unavailable') from None
        accepted = (codes,) if isinstance(codes, int) else tuple(codes)
        label = f'{method} {path.split("?")[0]} expected {accepted}, got {code}'
        if record:
            self.expect(code in accepted, label)
        elif code not in accepted:
            raise AssertionError(label)
        if len(payload) > 32 << 20:
            raise AssertionError('Owner HTTP response exceeds the bounded snapshot size')
        try:
            result = json.loads(payload)
        except (ValueError, UnicodeDecodeError):
            raise AssertionError('Owner HTTP response is not JSON') from None
        return code, result

    def api(self, origin, path, token=None, method='GET', body=None, codes=200,
            headers=None, record=True):
        code, result = self.request(origin, path, token, method, body, codes, headers, record)
        if code < 300:
            if result.get('status') is not True or 'data' not in result:
                raise AssertionError('Owner business response lacks a success envelope')
            return result['data']
        return result

    def internal(self, origin, path, method='GET', body=None, record=True):
        return self.api(origin, path, method=method, body=body,
                        headers={'X-Service-Key': self.key}, record=record)

    def rating(self, scope, path, method='GET', body=None, headers=None, record=True, codes=200):
        return self.api(RATINGS, f'/v1/ratings/divisions/{scope["division_id"]}' + path,
                        scope['admin'], method, body, codes=codes, headers=headers, record=record)

    def hw(self, scope, path, method='GET', body=None, codes=200):
        return self.api(HOMEWORK, f'/v1/homework/divisions/{scope["division_id"]}' + path,
                        scope['admin'], method, body, codes)

    def external(self, scope, path='', method='GET', body=None, codes=200):
        headers = {'Idempotency-Key': secrets.token_hex(16)} if method != 'GET' else None
        return self.api(TESTS, f'/v1/tests/divisions/{scope["division_id"]}/external-tests' + path,
                        scope['admin'], method, body, codes, headers)

    def exam(self, scope, path, method='GET', body=None):
        # Exams selects division from the scoped auth context and rejects query overrides.
        # This scope's admin was authenticated in its exact retained cabinet.
        headers = {'Idempotency-Key': str(uuid.uuid4())} if method != 'GET' else None
        return self.api(EXAMS, '/v1/exams' + path, scope['admin'], method, body, headers=headers)

    def login(self, login, scope, synthetic=False):
        _, response = self.request(AUTH, '/v1/auth/login', method='POST', body={
            'login': login, 'password': self.password if synthetic else self.seed_password,
            'organization_id': scope['organization_id'], 'division_id': scope['division_id']})
        token = response.get('access_token') or response['token']
        self.sessions.append(token)
        profile = response['user']
        self.expect(profile['organization_id'] == scope['organization_id'] and
                    profile['division_id'] == scope['division_id'], 'Login selects the retained tenant cabinet')
        return token, profile

    def wait(self, predicate, label, timeout=150):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            value = predicate()
            if value:
                self.expect(True, label)
                return value
            time.sleep(.4)
        raise AssertionError(label + ': timed out')

    def snapshots(self, scope, period=None, as_of=None):
        request = {'organization_id': scope['organization_id'], 'division_id': scope['division_id'],
                   **(period or self.full), 'as_of': as_of or datetime.now(timezone.utc).isoformat()}
        tests = self.internal(TESTS, '/internal/tests/ratings/snapshot', 'POST', request)
        homework = self.internal(HOMEWORK, '/internal/homework/ratings/snapshot', 'POST', request)
        query = urllib.parse.urlencode({'organization_id': scope['organization_id'],
                                       'division_ids': scope['division_id'],
                                       'date_from': request['date_from'], 'date_to': request['date_to'],
                                       'as_of': request['as_of']})
        exams = self.internal(EXAMS, '/internal/exams/rating-snapshot?' + query)
        return {'tests': tests, 'homework': homework, 'exams': exams}

    def setup(self):
        for origin in sorted(ORIGINS):
            self.request(origin, '/health/ready')
        health = self.api(RATINGS, '/health/ready')
        self.expect(health.get('exams_mode') == 'required' and health.get('external_tests_mode') == 'required'
                    and health.get('exams_connected'), 'Both real exam and external test domains are required')
        self.today = self.source.get('today') or datetime.now(MOSCOW).date().isoformat()
        self.same_day()
        today = date.fromisoformat(self.today)
        self.yesterday, self.tomorrow = (today - timedelta(days=1)).isoformat(), (today + timedelta(days=1)).isoformat()
        self.full = self.source.get('period') or {'date_from': (today - timedelta(days=7)).isoformat(),
                                               'date_to': (today + timedelta(days=7)).isoformat()}
        entries = self.source.get('scopes') or []
        self.expect({s.get('label') for s in entries} == {'A1', 'A2', 'B1', 'B2'} and len(entries) == 4,
                    'The retained manifest contains exactly its four synthetic tenant scopes')
        for entry in entries:
            scope = {k: v for k, v in entry.items() if k not in ('admin', 'token')}
            label = scope['label']
            self.expect(scope['organization_id'] == (1001 if label.startswith('A') else 2001),
                        'Tenant label belongs to the expected seeded dev organization')
            scope['admin'], profile = self.login('dev_admin' if label.startswith('A') else 'dev_admin_math', scope)
            self.expect(profile.get('organization_admin') and profile['role'] == 'admin',
                        'Only the seed organization owner modifies retained synthetic source fixtures')
            refs = self.internal(CORE, '/internal/core/test-references', 'POST', {
                'organization_id': scope['organization_id'], 'division_ids': [scope['division_id']]})
            self.expect(refs['organization']['id'] == scope['organization_id'] and len(refs['divisions']) == 1
                        and refs['divisions'][0]['id'] == scope['division_id'] and refs['divisions'][0]['is_active']
                        and refs['divisions'][0]['name'] == self.prefix + ' ' + label,
                        'Every retained division is active and carries the exact tenant run label')
            self.restore_scope_metadata(scope)
            report = self.rating(scope, '/report?version=' + scope['original_job_id'])
            self.expect(report['period'] == self.full,
                        'The historical tenant publication retains its inclusive original period')
            self.fixtures['original_freshness'][label] = report['freshness']
            scope['original_report'] = report
            self.assert_rows(scope, report['students'], EXPECTED['full'][label])
            self.scopes[label] = scope
        self.expect(self.scopes['A1']['students'][0]['account_id'] == self.scopes['A2']['students'][0]['account_id'],
                    'A1 and A2 retain the shared account with independent memberships')
        self.fixtures['scopes'] = [{'label': s['label'], 'organization_id': s['organization_id'],
                                   'division_id': s['division_id'], 'original_job_id': s['original_job_id']}
                                  for s in self.scopes.values()]
        self.persist()

    def restore_scope_metadata(self, scope):
        """Recover older minimal manifests using bounded, owner-scoped HTTP reads."""
        request = {'organization_id': scope['organization_id'], 'division_id': scope['division_id'],
                   **self.full, 'as_of': datetime.now(timezone.utc).isoformat()}
        core = self.internal(CORE, '/internal/core/ratings/snapshot', 'POST', request)
        self.expect(core['organization_id'] == scope['organization_id'] and core['division_id'] == scope['division_id']
                    and len(core['accounts']) == 2, 'Core source has exactly two owned retained students')
        by_account = {u['account_id']: u for u in self.source['users'] if u['role'] == 'student'}
        primary_suffix = ('A1' if scope['label'] == 'A2' else scope['label']) + '_primary'
        peer_suffix = scope['label'] + '_peer'
        students = []
        for suffix in (primary_suffix, peer_suffix):
            login = f'dev_ratings_tenant_{self.run}_{suffix}'
            matches = [u for u in by_account.values() if u['login'] == login]
            self.expect(len(matches) == 1 and matches[0]['account_id'] in {a['account_id'] for a in core['accounts']},
                        'Retained student login resolves to this division membership')
            student = {'account_id': matches[0]['account_id'], 'login': login}
            student['token'], context = self.login(login, scope, synthetic=True)
            self.expect(context['account_id'] == student['account_id'], 'Synthetic login preserves the retained account ID')
            students.append(student)
        scope['students'] = students
        source = self.snapshots(scope)
        homework = source['homework']['assignments']
        internal, external = source['tests']['tests'], source['tests']['external_tests']
        exams = source['exams']['snapshot']['exams']
        self.expect(len(homework) == len(internal) == len(external) == len(exams) == 1,
                    'Fresh tenant scope has its original single fixture from every source owner')
        scope.update(assignment_id=homework[0]['assignment_id'], test_id=internal[0]['test_id'],
                     external_test_id=external[0]['test_id'], exam_id=exams[0]['exam_id'],
                     direction_id=internal[0]['direction_id'])
        self.expect(homework[0]['deadline'] == self.yesterday and homework[0]['name'] == self.prefix + ' ' + scope['label'] + ' homework'
                    and internal[0]['title'] == self.prefix + ' ' + scope['label'] + ' official'
                    and external[0]['title'] == self.prefix + ' ' + scope['label'] + ' external'
                    and exams[0]['exam_date'] == self.today
                    and datetime.fromisoformat(internal[0]['start_at'].replace('Z', '+00:00')).astimezone(MOSCOW).date().isoformat() == self.today
                    and datetime.fromisoformat(external[0]['start_at'].replace('Z', '+00:00')).astimezone(MOSCOW).date().isoformat() == self.today,
                    'Source objects and dates belong to this fresh tenant acceptance run')
        jobs = [j['id'] for j in self.source['jobs'] if j['organization_id'] == scope['organization_id']
                and j['division_id'] == scope['division_id']]
        scope['original_job_id'] = scope.get('job_id') or scope.get('original_job_id') or (jobs[0] if len(jobs) == 1 else None)
        self.expect(scope['original_job_id'] is not None, 'Original tenant publication identity is unambiguous')
        owner = self.exam(scope, '/' + str(scope['exam_id']))
        self.expect(owner['name'] == self.prefix + ' ' + scope['label'] + ' exam' and owner['exam_type'] == 'outside_lms'
                    and owner['division_id'] == scope['division_id'] and owner['organization_id'] == scope['organization_id'],
                    'Outside exam belongs to the exact retained run and selected division')

    def assert_rows(self, scope, rows, expected):
        by_account = {r['profile']['account_id']: r for r in rows}
        self.expect(len(rows) == 2 and set(by_account) == {s['account_id'] for s in scope['students']},
                    'Publication contains exactly both owned scope participants')
        for student, values in zip(scope['students'], expected):
            result = by_account[student['account_id']]['rating']
            actual = (result['homework']['rating'], result['tests']['rating'], result['exams']['rating'], result['final_rating'])
            self.expect(all(math.isclose(a, b, rel_tol=0, abs_tol=1e-9) for a, b in zip(actual, values)),
                        'Period components and final match the independent hand-calculated regression example')
            self.expect(result['organization_id'] == scope['organization_id'] and result['division_id'] == scope['division_id']
                        and result['account_id'] == student['account_id'], 'Period result metadata retains the exact tenant scope')
        return by_account

    def publish(self, scope, name, period, expected):
        self.same_day()
        job = self.rating(scope, '/jobs', 'POST', period,
                          {'Idempotency-Key': 'period-' + self.nonce + '-' + scope['label'] + '-' + name}, codes=202)['job']
        self.fixtures['jobs'].append({'id': job['id'], 'organization_id': scope['organization_id'],
                                     'division_id': scope['division_id'], 'scenario': name, 'period': period})
        self.persist()
        stages = []
        def ready():
            current = self.rating(scope, '/jobs/' + job['id'], record=False)
            stage = [current['status'], current['processed_count'], current['total_count']]
            if not stages or stages[-1] != stage:
                stages.append(stage)
            if current['status'] in ('failed', 'cancelled', 'superseded'):
                raise AssertionError('Owned period job entered a terminal failure')
            return current if current['status'] == 'completed' else None
        job = self.wait(ready, 'Period job completes both individual asynchronous calculations')
        self.progress[job['id']] = stages
        self.expect(job['total_count'] == job['processed_count'] == 2 and
                    job['date_from'] == period['date_from'] and job['date_to'] == period['date_to'],
                    'Completed period job retains both endpoints and exactly its two participants')
        captured_at = datetime.fromisoformat(job['as_of'].replace('Z', '+00:00'))
        self.expect(captured_at.utcoffset() == timedelta(0) and captured_at.astimezone(MOSCOW).date().isoformat() == self.today,
                    'Each period job independently captures a fixed UTC as_of in the expected Moscow day')
        report = self.rating(scope, '/report')
        self.expect(report['period'] == period and report['freshness']['version_id'] == job['id']
                    and not report['freshness']['is_stale'], 'Published period and freshness point to the new completed job')
        scope['current_job'], scope['current_report'] = job, report
        self.assert_rows(scope, report['students'], expected)
        self.assert_history(scope)
        def delivered():
            messages = self.internal(CALCULATE, '/internal/calculate/ratings/messages?' + urllib.parse.urlencode({
                'job_id': job['id'], 'limit': 100}), record=False)['items']
            return messages if len(messages) == 2 and all(m['status'] == 'ready' for m in messages) else None
        messages = self.wait(delivered, 'Both period callbacks finish durably in the calculate inbox', 30)
        self.expect(len(messages) == 2 and all(m['status'] == 'ready' and m['job_id'] == job['id']
                    and m['organization_id'] == scope['organization_id'] and m['division_id'] == scope['division_id']
                    for m in messages), 'Calculate exposes two durable ready HTTP callback identities for this period')
        return job, report

    def assert_history(self, scope):
        old = self.rating(scope, '/report?version=' + scope['original_job_id'])
        self.expect(old['period'] == self.full and old['students'] == scope['original_report']['students'],
                    'New periods and source changes leave the original published students and period unchanged')

    def period_matrix(self):
        self.phase = 'four_scope_period_matrix'
        periods = {'yesterday': {'date_from': self.yesterday, 'date_to': self.yesterday},
                   'today': {'date_from': self.today, 'date_to': self.today},
                   'tomorrow': {'date_from': self.tomorrow, 'date_to': self.tomorrow}, 'full': self.full}
        for name, period in periods.items():
            for label, scope in self.scopes.items():
                _, report = self.publish(scope, name, period, EXPECTED[name][label])
                for row in report['students']:
                    result = row['rating']
                    hw_ids = {d['homework_id'] for d in result['homework']['details']}
                    test_ids = {d['test_id'] for d in result['tests']['details']}
                    exam_ids = {d['exam_id'] for d in result['exams']['details']}
                    self.expect(hw_ids == ({scope['assignment_id']} if name in ('yesterday', 'full') else set())
                                and test_ids == ({scope['test_id'], scope['external_test_id']} if name in ('today', 'full') else set())
                                and exam_ids == ({str(scope['exam_id'])} if name in ('today', 'full') else set()),
                                'Inclusive single-day period selects exactly its owner homework, tests and exams')

    def future_homework(self):
        self.phase = 'future_homework_calendar'
        scope = self.scopes['A1']
        types = self.hw(scope, '/types')['items']
        ov = [t['id'] for t in types if t['name'] == 'ОВ' and not t.get('archived')]
        self.expect(len(ov) == 1, 'Retained A1 has its exact OV homework type')
        for day, label in [(self.today, 'today'), (self.tomorrow, 'tomorrow')]:
            assignment = self.hw(scope, '/assignments', 'POST', {
                'name': self.prefix + ' period ' + self.nonce + ' ' + label,
                'type_id': ov[0], 'deadline': day, 'published': False}, 201)['id']
            self.fixtures['assignments'].append({'id': assignment, 'division_id': scope['division_id'], 'deadline': day})
            self.persist()
            for student in scope['students']:
                self.hw(scope, f'/assignments/{assignment}/manual', 'POST', {
                    'account_id': student['account_id'], 'date_pass': self.today, 'status': 1, 'result': 100})
        job, report = self.publish(scope, 'future-hw-full', self.full, EXPECTED['full']['A1'])
        source = self.snapshots(scope, as_of=job['as_of'])['homework']
        cutoff = datetime.combine(date.fromisoformat(self.tomorrow), datetime.min.time(), MOSCOW).astimezone(timezone.utc)
        self.expect(datetime.fromisoformat(source['next_transition_at'].replace('Z', '+00:00')) == cutoff,
                    'Next OV eligibility boundary is exactly the following Moscow midnight')
        expected_ids = {scope['assignment_id']} | {a['id'] for a in self.fixtures['assignments']}
        for student in scope['students']:
            raw = self.internal(RATINGS, f'/internal/ratings/jobs/{job["id"]}/accounts/{student["account_id"]}/input')
            self.expect({h['assignment_id'] for h in raw['homework']} == expected_ids and
                        all(h['result'] == 100 and h['status'] == 1 for h in raw['homework'] if h['assignment_id'] != scope['assignment_id']),
                        'Frozen input retains already graded today/future OV while calculate excludes them')
        tomorrow_period = {'date_from': self.tomorrow, 'date_to': self.tomorrow}
        self.publish(scope, 'future-hw-tomorrow', tomorrow_period, EXPECTED['tomorrow']['A1'])
        self.assert_controls_unchanged()

    def write_external_score(self, scope, test_id, account, score):
        view = self.external(scope, '/' + test_id)
        rows = self.external(scope, '/' + test_id + '/results')['items']
        previous = next((r for r in rows if r['account_id'] == account), None)
        body = {'expected_revision': view['revision'], 'expected_result_revision': previous['revision'] if previous else 0,
                'score': score}
        result = self.external(scope, f'/{test_id}/results/{account}', 'PUT', body)
        self.expect(result['revision'] == view['revision'] + 1, 'External owner advances the revision for an accepted manual result write')
        return previous, result

    def external_missing_zero_future(self):
        self.phase = 'external_zero_null_absent_future'
        scope = self.scopes['A1']
        primary, peer = scope['students']
        view = self.external(scope, method='POST', body={
            'name': self.prefix + ' period ' + self.nonce + ' zero-null',
            'direction_id': scope['direction_id'], 'date': self.today}, codes=201)
        zero_id = view['id']
        self.fixtures['external_tests'].append({'id': zero_id, 'division_id': scope['division_id'], 'date': self.today})
        self.persist()
        self.write_external_score(scope, zero_id, primary['account_id'], 0)
        self.write_external_score(scope, zero_id, peer['account_id'], None)
        rows = self.external(scope, '/' + zero_id + '/results')['items']
        by_account = {r['account_id']: r for r in rows}
        self.expect(by_account[primary['account_id']]['score'] == 0 and by_account[peer['account_id']]['score'] is None,
                    'Owner HTTP result rows distinguish an explicit zero grade from a saved unknown grade')
        job, report = self.publish(scope, 'external-zero-null', self.full, [(80, 140 / 3, 4, 65), (0, 0, 0, 0)])
        published = {r['profile']['account_id']: r['rating'] for r in report['students']}
        primary_details = {d['test_id']: d for d in published[primary['account_id']]['tests']['details']}
        peer_details = {d['test_id']: d for d in published[peer['account_id']]['tests']['details']}
        self.expect(primary_details[zero_id]['calculation_status'] == 'ready' and primary_details[zero_id]['score'] == 0
                    and peer_details[zero_id]['calculation_status'] == 'missing' and peer_details[zero_id]['score'] == 0
                    and peer_details[scope['external_test_id']]['calculation_status'] == 'missing',
                    'Published details distinguish ready-zero, saved-null and absent results while retaining legacy zero arithmetic')
        source = self.snapshots(scope, as_of=job['as_of'])['tests']
        records = {t['test_id']: t for t in source['external_tests']}
        zero_rows = {r['account_id']: r for r in records[zero_id]['results']}
        self.expect(zero_rows[primary['account_id']]['calculation_status'] == 'ready' and
                    zero_rows[primary['account_id']]['score'] == 0 and
                    zero_rows[peer['account_id']]['calculation_status'] == 'missing' and
                    zero_rows[peer['account_id']]['score'] is None and zero_rows[peer['account_id']]['submission_id']
                    and all(r['account_id'] != peer['account_id'] for r in records[scope['external_test_id']]['results']),
                    'Ratings source keeps saved null identity/version distinct from a wholly absent external result')
        for student in scope['students']:
            raw = self.internal(RATINGS, f'/internal/ratings/jobs/{job["id"]}/accounts/{student["account_id"]}/input')
            external = {t['test_id']: t for t in raw['tests'] if t['source'] == 'external'}
            self.expect(external[zero_id]['score'] == (0 if student is primary else None) and
                        external[zero_id]['calculation_status'] == ('ready' if student is primary else 'missing'),
                        'Personal immutable input preserves external known-zero and unknown states')
        future = self.external(scope, method='POST', body={
            'name': self.prefix + ' period ' + self.nonce + ' future external',
            'direction_id': scope['direction_id'], 'date': self.tomorrow}, codes=201)
        self.future_external_id = future['id']
        self.fixtures['external_tests'].append({'id': future['id'], 'division_id': scope['division_id'], 'date': self.tomorrow})
        self.persist()
        self.write_external_score(scope, future['id'], primary['account_id'], 100)
        self.publish(scope, 'future-external-tomorrow', {'date_from': self.tomorrow, 'date_to': self.tomorrow},
                     [(0, 100, 0, 45), (0, 0, 0, 0)])
        job, report = self.publish(scope, 'future-external-full', self.full, [(80, 60, 4, 71), (0, 0, 0, 0)])
        raw = self.internal(RATINGS, f'/internal/ratings/jobs/{job["id"]}/accounts/{primary["account_id"]}/input')
        self.expect(any(t['test_id'] == future['id'] and t['source'] == 'external' and t['score'] == 100 for t in raw['tests'])
                    and self.future_external_id in {d['test_id'] for d in report['students'][0]['rating']['tests']['details']},
                    'An external calendar date inside the inclusive period counts before that future day under legacy rules')
        self.zero_external_id = zero_id
        self.assert_controls_unchanged()

    def assert_controls_unchanged(self):
        for label in ('A2', 'B1', 'B2'):
            scope = self.scopes[label]
            after = self.rating(scope, '/report')
            before = scope['current_report']
            self.expect(after['period'] == self.full and after['students'] == before['students']
                        and after['freshness']['version_id'] == scope['current_job']['id'] and not after['freshness']['is_stale'],
                        'A1 source changes leave control publications and freshness unchanged in the other three scopes')

    def assert_old_pointer_stale(self, scope, old):
        def changed():
            after = self.rating(scope, '/report', record=False)
            return after if after['freshness']['is_stale'] else None
        after = self.wait(changed, 'Changed owner input marks the current A1 publication stale without replacing it', 30)
        self.expect(after['freshness']['reason'] == 'source_changed' and
                    after['freshness']['version_id'] == scope['current_job']['id'] and after['students'] == old['students'],
                    'Source invalidation preserves the last complete published rows until explicit recalculation')
        self.assert_controls_unchanged()

    def same_grade_external_revision(self):
        self.phase = 'same_grade_external_revision'
        scope = self.scopes['A1']
        primary = scope['students'][0]
        before, old = self.snapshots(scope)['tests'], scope['current_report']
        previous, _ = self.write_external_score(scope, self.zero_external_id, primary['account_id'], 0)
        rows = self.external(scope, '/' + self.zero_external_id + '/results')['items']
        current = next(r for r in rows if r['account_id'] == primary['account_id'])
        after = self.snapshots(scope)['tests']
        self.expect(current['score'] == previous['score'] == 0 and current['revision'] == previous['revision'] + 1
                    and after['external_source_version'] != before['external_source_version']
                    and after['input_hash'] != before['input_hash'],
                    'A same-grade owner correction changes source versions without fabricating a changed score')
        self.assert_old_pointer_stale(scope, old)
        self.publish(scope, 'same-grade-manual-recalculation', self.full, [(80, 60, 4, 71), (0, 0, 0, 0)])

    def outside_exam_scoped_change(self):
        self.phase = 'outside_exam_source_change'
        scope = self.scopes['A1']
        primary = scope['students'][0]
        before = {label: self.snapshots(s)['exams']['source_revisions'] for label, s in self.scopes.items()}
        old = scope['current_report']
        result = self.exam(scope, f'/{scope["exam_id"]}/results/{primary["account_id"]}')
        self.expect(result['grade'] == 4 and result['student']['account_id'] == primary['account_id'],
                    'The updated outside result belongs to the retained A1 primary student')
        changed = self.exam(scope, f'/{scope["exam_id"]}/outside-results/{result["id"]}', 'PUT', {
            'expected_revision': result['revision'], 'grade': 3})
        self.expect(changed.get('changed') is True and changed.get('rating_changed') is True and
                    changed['result']['grade'] == 3 and changed['result']['revision'] == result['revision'] + 1,
                    'The outside owner commits a scoped effective-grade change and declares rating invalidation')
        self.fixtures['outside_updates'].append({'exam_id': scope['exam_id'], 'result_id': result['id'],
                                                'division_id': scope['division_id'], 'previous_grade': 4, 'grade': 3})
        self.persist()
        after = {label: self.snapshots(s)['exams']['source_revisions'] for label, s in self.scopes.items()}
        self.expect(after['A1'][str(scope['division_id'])] > before['A1'][str(scope['division_id'])]
                    and all(after[label] == before[label] for label in ('A2', 'B1', 'B2')),
                    'Exam owner advances only A1 source revision and leaves every control division counter unchanged')
        self.assert_old_pointer_stale(scope, old)
        self.publish(scope, 'outside-manual-recalculation', self.full, [(80, 60, 3, 65), (0, 0, 0, 0)])

    def audit_finished_jobs(self):
        self.phase = 'completed_http_audit'
        self.expect(len(self.fixtures['jobs']) == len(self.progress) == 23,
                    'Exactly 23 period/source jobs were created and observed completing independently')
        verified = []
        for captured in self.fixtures['jobs']:
            scope = next(s for s in self.scopes.values() if s['division_id'] == captured['division_id']
                         and s['organization_id'] == captured['organization_id'])
            job = self.rating(scope, '/jobs/' + captured['id'])
            self.expect(job['status'] == 'completed' and job['total_count'] == job['processed_count'] == 2
                        and job['organization_id'] == captured['organization_id'] and job['division_id'] == captured['division_id']
                        and {'date_from': job['date_from'], 'date_to': job['date_to']} == captured['period'],
                        'Independent final HTTP lookup confirms a complete scoped job, period and two-of-two progress')
            captured_at = datetime.fromisoformat(job['as_of'].replace('Z', '+00:00'))
            self.expect(captured_at.utcoffset() == timedelta(0) and captured_at.astimezone(MOSCOW).date().isoformat() == self.today,
                        'Completed job retains its original fixed UTC as_of independently of later source changes')
            for student in scope['students']:
                raw = self.internal(RATINGS, f'/internal/ratings/jobs/{job["id"]}/accounts/{student["account_id"]}/input')
                self.expect(raw['job_id'] == job['id'] and raw['revision'] == job['revision']
                            and raw['organization_id'] == scope['organization_id'] and raw['division_id'] == scope['division_id']
                            and raw['account_id'] == student['account_id'] and raw['as_of'] == job['as_of']
                            and raw['date_from'] == job['date_from'] and raw['date_to'] == job['date_to']
                            and re.fullmatch(r'[0-9a-f]{64}', raw['input_hash']) is not None,
                            'Each retained immutable input independently matches its completed job scope, as_of and period')
            verified.append({'job_id': job['id'], 'organization_id': job['organization_id'], 'division_id': job['division_id'],
                             'status': job['status'], 'processed_count': job['processed_count'], 'total_count': job['total_count'],
                             'as_of': job['as_of'], 'period': captured['period'], 'scenario': captured['scenario']})
        self.fixtures['verified_jobs'] = verified
        publications = []
        for scope in self.scopes.values():
            report = self.rating(scope, '/report')
            latest = next(j for j in reversed(self.fixtures['jobs']) if j['division_id'] == scope['division_id'])
            self.expect(report['period'] == self.full and report['freshness']['version_id'] == latest['id']
                        and not report['freshness']['is_stale'],
                        'Every final tenant publication independently points to its complete fresh full-period job')
            expected = [(80, 60, 3, 65), (0, 0, 0, 0)] if scope['label'] == 'A1' else EXPECTED['full'][scope['label']]
            self.assert_rows(scope, report['students'], expected)
            publications.append({'label': scope['label'], 'organization_id': scope['organization_id'],
                                 'division_id': scope['division_id'], 'period': report['period'],
                                 'freshness': report['freshness']})
        self.fixtures['final_publications'] = publications

    def finish(self, error=None):
        for token in self.sessions:
            try:
                self.request(AUTH, '/v1/auth/logout', token, 'POST', {}, (200, 401), record=False)
            except Exception:
                pass
        self.fixtures['status'] = 'failed' if error else 'passed'
        self.persist()
        report = {'status': self.fixtures['status'], 'tenant_run_id': self.run, 'period_run_id': self.nonce,
                  'http_checks': len(self.checks), 'scenarios': self.checks, 'job_progress': self.progress,
                  'scopes': self.fixtures.get('scopes', []), 'job_ids': [j['id'] for j in self.fixtures['jobs']],
                  'original_freshness': self.fixtures['original_freshness'],
                  'verified_jobs': self.fixtures.get('verified_jobs', []),
                  'final_publications': self.fixtures.get('final_publications', []),
                  'phase': self.phase, 'database_accessed': False, 'production_accessed': False,
                  'rabbit_administration_used': False, 'services_restarted': False, 'fixtures_retained': True,
                  'source_event_evidence': 'Owner rating_changed and scoped source counters; HTTP publication freshness. Broker delivery is tested separately.'}
        if error:
            report['failure'] = str(error) if isinstance(error, AssertionError) else type(error).__name__
        private_json(self.report_path, report)
        print(json.dumps({'status': report['status'], 'http_checks': report['http_checks'],
                          'tenant_run_id': self.run, 'fixtures_retained': True}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--allow-source-mutations', action='store_true',
                        help='Retain HTTP fixtures and publications inside the latest successful synthetic tenant A1 scope.')
    parser.add_argument('--fixtures', type=Path, help='Exact private fixture matching the latest successful tenant report.')
    arguments = parser.parse_args()
    if not arguments.allow_source_mutations:
        parser.error('Specify --allow-source-mutations only after the tenant fixture and source owners are ready.')
    suite, failure = None, None
    try:
        suite = Suite(arguments.fixtures)
        suite.setup()
        suite.period_matrix()
        suite.future_homework()
        suite.external_missing_zero_future()
        suite.same_grade_external_revision()
        suite.outside_exam_scoped_change()
        suite.audit_finished_jobs()
    except Exception as error:
        failure = error
    finally:
        if suite is not None:
            suite.finish(failure)
    if failure:
        raise SystemExit('Ratings period/source acceptance failed; inspect the private local report.')


if __name__ == '__main__':
    main()
