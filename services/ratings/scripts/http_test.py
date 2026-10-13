"""End-to-end ratings checks against fixed loopback cpm_dev services.

Creates and retains isolated API fixtures because published ratings permanently
lock their memberships. Never removes history or stops shared services. The only
direct mutation temporarily makes this run's already accepted test pending and
restores its original fields in finally. Reports never include credentials.
"""
import argparse
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timedelta, timezone
import json
import math
import os
from pathlib import Path
import secrets
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from zoneinfo import ZoneInfo

from bson import ObjectId

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'dev/scripts'))
from bootstrap import settings, mysql_connection, mongo_client

AUTH = 'http://127.0.0.1:8002'
CORE = 'http://127.0.0.1:8003'
TESTS = 'http://127.0.0.1:8004'
CALCULATE = 'http://127.0.0.1:8005'
HOMEWORK = 'http://127.0.0.1:8006'
RATINGS = 'http://127.0.0.1:8012'
ORIGINS = {AUTH, CORE, TESTS, CALCULATE, HOMEWORK, RATINGS}


def private_json(path, value):
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(descriptor, 'w') as output:
        os.fchmod(output.fileno(), 0o600)
        json.dump(value, output, ensure_ascii=False, indent=2, allow_nan=False)
        output.write('\n')


class Suite:
    def __init__(self):
        self.env = settings()  # Fixed cpm_dev and dedicated loopback ports.
        self.run = secrets.token_hex(6)
        self.prefix = 'DEV Ratings ' + self.run
        self.password = secrets.token_urlsafe(24)
        self.admin_password = json.loads((ROOT / 'dev/local/accounts.json').read_text())['password']
        self.checks, self.sessions, self.progress = [], [], {}
        self.fixtures = {'run_id': self.run, 'password': self.password, 'organization_id': 1001,
                         'divisions': [], 'users': [], 'groups': [], 'roles': [], 'assignments': [],
                         'types': [], 'tests': [], 'attempts': [], 'jobs': []}
        self.fixture_path = ROOT / 'dev/local/ratings-fixtures.json'
        self.report_path = ROOT / 'dev/local/ratings-http-report.json'
        self.period, self.division, self.secondary = None, None, None
        self.admin = None

    def persist(self):
        private_json(self.fixture_path, self.fixtures)

    def expect(self, condition, label):
        if not condition:
            raise AssertionError(label)
        self.checks.append(label)

    def request(self, origin, path, token=None, method='GET', body=None, codes=200,
                headers=None, record=True):
        if origin not in ORIGINS or not path.startswith('/'):
            raise AssertionError('HTTP tests allow only fixed loopback service origins')
        h = dict(headers or {})
        if token:
            h['Authorization'] = 'Bearer ' + token
        raw = None
        if body is not None:
            raw = json.dumps(body, allow_nan=False).encode()
            h['Content-Type'] = 'application/json'
        req = urllib.request.Request(origin + path, method=method, data=raw, headers=h)
        try:
            with urllib.request.urlopen(req, timeout=45) as response:
                code, payload = response.status, response.read()
        except urllib.error.HTTPError as response:
            code, payload = response.code, response.read()
        accepted = (codes,) if isinstance(codes, int) else tuple(codes)
        label = f'{method} {path.split("?")[0]} expected {accepted}, got {code}'
        if record:
            self.expect(code in accepted, label)
        elif code not in accepted:
            raise AssertionError(label)
        try:
            result = json.loads(payload)
        except ValueError:
            raise AssertionError('HTTP service returned invalid JSON') from None
        return code, result

    def api(self, origin, prefix, path, token=None, method='GET', body=None, codes=200,
            headers=None, record=True):
        code, response = self.request(origin, prefix + path, token, method, body, codes, headers, record)
        if code < 300:
            if response.get('status') is not True or 'data' not in response:
                raise AssertionError('Business response lacks a success envelope')
            return response['data']
        return response

    def core(self, path, token=None, method='GET', body=None, codes=200):
        return self.api(CORE, '/v1/core', path, token or self.admin, method, body, codes)

    def homework(self, path, method='GET', body=None, codes=200):
        return self.api(HOMEWORK, f'/v1/homework/divisions/{self.division}', path,
                        self.admin, method, body, codes)

    def tests(self, path, token=None, method='GET', body=None, codes=200, headers=None, record=True):
        return self.api(TESTS, '/v1/tests', path, token or self.admin, method, body, codes, headers, record)

    def ratings(self, path, token=None, method='GET', body=None, codes=200,
                headers=None, division=None, record=True):
        return self.api(RATINGS, f'/v1/ratings/divisions/{division or self.division}', path,
                        token or self.admin, method, body, codes, headers, record)

    def internal(self, origin, path, method='GET', body=None, codes=200, record=True):
        return self.api(origin, '', path, method=method, body=body, codes=codes,
                        headers={'X-Service-Key': self.env['AUTH_INTERNAL_KEY']}, record=record)

    def login(self, login, division=1001, organization=1001, fixture=True):
        _, response = self.request(AUTH, '/v1/auth/login', method='POST', body={
            'login': login, 'password': self.password if fixture else self.admin_password,
            'organization_id': organization, 'division_id': division})
        self.sessions.append(response['token'])
        return response['token'], response['user']

    def user(self, suffix, role='student', group=None, role_id=None):
        login = f'dev_ratings_{self.run}_{suffix}'
        body = {'role': role, 'full_name': self.prefix + ' ' + suffix, 'login': login,
                'password': self.password, 'tg_name': ''}
        if role == 'student':
            body.update({'class': 10, 'school_id': 1001})
        if group is not None:
            body['group_id'] = group
        if role_id is not None:
            body['role_id'] = role_id
        out = self.core(f'/divisions/{self.division}/users', method='POST', body=body, codes=201)
        self.fixtures['users'].append({'account_id': out['account_id'], 'login': login, 'role': role})
        self.persist()
        token, context = self.login(login, self.division)
        self.expect(context['account_id'] == out['account_id'], 'Profile and account IDs are checked through auth')
        return token, out['account_id']

    def new_job(self, *, token=None, division=None, key=None):
        identity = key or secrets.token_hex(16)
        result = self.ratings('/jobs', token, 'POST', self.period, 202,
                              {'Idempotency-Key': identity}, division)
        job = result['job']
        self.fixtures['jobs'].append({'id': job['id'], 'division_id': division or self.division,
                                     'idempotency_key': identity})
        self.persist()
        self.expect(job['status'] != 'completed', 'Create acknowledges a stored asynchronous job before publication')
        return job, identity

    def wait(self, condition, label, timeout=90):
        until = time.monotonic() + timeout
        while time.monotonic() < until:
            value = condition()
            if value:
                self.expect(True, label)
                return value
            time.sleep(.25)
        raise AssertionError(label + ': timed out')

    def wait_job(self, job, division=None):
        seen = []
        def complete():
            current = self.ratings('/jobs/' + job['id'], division=division, record=False)
            stage = (current['status'], current['processed_count'], current['total_count'])
            if not seen or seen[-1] != stage:
                seen.append(stage)
            if current['status'] in ('failed', 'cancelled', 'superseded'):
                raise AssertionError('Asynchronous rating job failed: ' + current.get('error_code', current['status']))
            return current if current['status'] == 'completed' else None
        out = self.wait(complete, 'Rating job completes through RabbitMQ and calculate')
        self.progress[job['id']] = seen
        self.expect(out['processed_count'] == out['total_count'], 'Publication contains every prepared participant')
        return out

    def setup(self):
        for origin in [AUTH, CORE, TESTS, CALCULATE, HOMEWORK, RATINGS]:
            self.request(origin, '/health/ready')
        self.admin, _ = self.login('dev_admin', fixture=False)
        self.foreign, _ = self.login('dev_admin_math', 2001, 2001, fixture=False)
        self.division = self.core('/divisions', method='POST', body={'name': self.prefix}, codes=201)['id']
        self.secondary = self.core('/divisions', method='POST', body={'name': self.prefix + ' second'}, codes=201)['id']
        self.fixtures['divisions'] = [self.division, self.secondary]
        self.persist()
        groups = []
        for suffix in ['A', 'B']:
            g = self.core(f'/divisions/{self.division}/groups', method='POST',
                          body={'name': self.prefix + ' ' + suffix}, codes=201)['id']
            groups.append(g)
            self.fixtures['groups'].append(g)
        self.student, self.student_id = self.user('student1', group=groups[0])
        self.other, self.other_id = self.user('student2', group=groups[1])
        self.proctor, self.proctor_id = self.user('proctor', 'proctor', group=groups[0])
        self.core(f'/divisions/{self.secondary}/memberships/{self.student_id}', method='PUT', body={})
        for suffix, edit in [('viewer', False), ('editor', True)]:
            rid = self.core('/roles', method='POST', body={'name': self.prefix + ' ' + suffix,
                            'permissions': {'ratings': {'view': True, 'edit': edit}}}, codes=201)['id']
            self.fixtures['roles'].append(rid)
            token, _ = self.user(suffix, 'staff_admin', role_id=rid)
            setattr(self, suffix, token)
        self.persist()
        today = datetime.now(ZoneInfo('Europe/Moscow')).date()
        self.period = {'date_from': (today - timedelta(days=7)).isoformat(),
                       'date_to': (today + timedelta(days=7)).isoformat()}
        self.yesterday = (today - timedelta(days=1)).isoformat()
        self.fixtures.update({'division_id': self.division, 'secondary_division_id': self.secondary,
                              'period': self.period, 'student_id': self.student_id,
                              'other_id': self.other_id, 'proctor_id': self.proctor_id})
        ov = self.homework('/types', 'POST', {'name': 'ОВ'}, 201)['id']
        other_type = self.homework('/types', 'POST', {'name': self.prefix + ' ignored'}, 201)['id']
        self.fixtures['types'] = [ov, other_type]
        self.hw = []
        for index, (type_id, day, score) in enumerate([
                (ov, self.yesterday, 100), (ov, self.yesterday, 60),
                (other_type, self.yesterday, 100), (ov, (today + timedelta(days=1)).isoformat(), 100)]):
            assignment = self.homework('/assignments', 'POST', {
                'name': self.prefix + ' HW ' + str(index), 'type_id': type_id,
                'deadline': day, 'published': False}, 201)['id']
            self.hw.append(assignment)
            self.fixtures['assignments'].append(assignment)
            self.homework(f'/assignments/{assignment}/manual', 'POST', {
                'account_id': self.student_id, 'date_pass': day, 'status': 1, 'result': score})
            self.persist()
        self.direction = self.core('/directions', method='POST', body={'name': self.prefix + ' direction'}, codes=201)['id']
        self.fixtures['direction_id'] = self.direction
        test = self.tests(f'/divisions/{self.division}/tests', method='POST', body={
            'title': self.prefix + ' official', 'description': 'Synthetic rating source',
            'direction_id': self.direction, 'questions': [{
                'question_id': 1, 'type': 'single', 'text': 'Choose A', 'points': 2,
                'answers': [{'id': 'a', 'text': 'A', 'is_correct': True},
                            {'id': 'b', 'text': 'B', 'is_correct': False}], 'correct_answers': []}]},
            codes=201, headers={'Idempotency-Key': secrets.token_hex(16)})
        self.test_id = test['id']
        self.fixtures['tests'].append(self.test_id)
        now = datetime.now(timezone.utc)
        self.tests(f'/divisions/{self.division}/tests/{self.test_id}/settings', method='PUT', body={
            'expected_revision': test['revision'], 'published': True,
            'start_at': (now - timedelta(minutes=10)).isoformat(),
            'end_at': (now + timedelta(hours=1)).isoformat(), 'time_limit_minutes': 60,
            'passing_score': 50, 'answers_visible': True})
        attempt = self.tests(f'/divisions/{self.division}/tests/{self.test_id}/attempts', self.student,
                             'POST', {'practice': False}, 201)
        self.attempt_id = attempt['id']
        self.fixtures['attempts'].append(self.attempt_id)
        accepted = self.tests('/attempts/' + self.attempt_id + '/finalize', self.student, 'POST', {
            'test_version_id': attempt['test_version_id'], 'reason': 'manual',
            'completed_at': datetime.now(timezone.utc).isoformat(),
            'answers': [{'question_id': 1, 'value': 'a'}]}, 202)
        self.submission = accepted['submission_id']
        self.fixtures['submission_id'] = self.submission
        self.persist()
        def ready():
            row = self.tests('/submissions/' + self.submission, self.student, record=False)
            return row if row['calculation_status'] == 'ready' else None
        grade = self.wait(ready, 'Official test grading becomes ready before collecting the rating')
        self.expect(grade['result']['score'] == 100, 'Source test score is computed by calculate')

    def assert_formula(self, rows, expected=65):
        by_account = {r['profile']['account_id']: r for r in rows}
        self.expect(set(by_account) == {self.student_id, self.other_id}, 'Rating includes exactly the current students of its division')
        student = by_account[self.student_id]['rating']
        self.expect(math.isclose(student['homework']['rating'], (expected - 45) / .25, abs_tol=1e-9),
                    'Only past OV homework contributes, including unpublished assignments')
        self.expect(student['tests']['rating'] == 100 and student['exams']['rating'] == 0,
                    'Tests and empty own-division exam source retain component scales')
        self.expect(math.isclose(student['final_rating'], expected, abs_tol=1e-9), 'Legacy hardcoded coefficients produce the expected final rating')
        missing = by_account[self.other_id]['rating']
        self.expect(missing['homework']['rating'] == 0 and missing['tests']['rating'] == 0
                    and missing['final_rating'] == 0, 'A missing official result contributes zero after all real checks are ready')
        return by_account

    def assert_transport(self, job):
        def delivered():
            items = self.internal(CALCULATE, '/internal/calculate/ratings/messages?job_id=' + job['id'] + '&limit=100', record=False)
            return items['items'] if len(items['items']) == 2 and all(m['status'] == 'ready' for m in items['items']) else None
        messages = self.wait(delivered, 'Calculate durable inbox confirms both individual deliveries')
        self.expect({m['account_id'] for m in messages} == {self.student_id, self.other_id}, 'One durable calculation identity per student')
        self.expect(all(m['job_id'] == job['id'] and m['revision'] == job['revision']
                        and m['organization_id'] == 1001 and m['division_id'] == self.division
                        for m in messages), 'Queue identities retain organization, division and revision')
        connection = mysql_connection(self.env)
        try:
            cursor = connection.cursor()
            cursor.execute('SELECT body,delivered_at FROM ratings_outbox_v1 WHERE job_id=%s', (job['id'],))
            events = cursor.fetchall()
            self.expect(len(events) == 2 and all(row[1] is not None for row in events), 'Ratings outbox durably records confirmed RabbitMQ publication')
            for raw, _ in events:
                envelope = json.loads(raw)
                self.expect(set(envelope['payload']) == {'job_id', 'revision', 'organization_id', 'division_id', 'account_id', 'input_hash'},
                            'Broker command contains identifiers and hashes without personal data or raw answers')
            cursor.close()
        finally:
            connection.close()

    def baseline(self):
        path = f'/v1/ratings/divisions/{self.division}/ratings'
        self.request(RATINGS, path, codes=401, headers={'X-Role': 'admin', 'X-Organization-ID': '1001'})
        self.ratings('/jobs', self.student, 'POST', self.period, 403, {'Idempotency-Key': secrets.token_hex(16)})
        self.ratings('/jobs', self.viewer, 'POST', self.period, 403, {'Idempotency-Key': secrets.token_hex(16)})
        self.ratings('/jobs', method='POST', body=self.period, codes=400)
        self.ratings('/jobs', method='POST', body={**self.period, 'organization_id': 2001}, codes=400,
                     headers={'Idempotency-Key': secrets.token_hex(16)})
        job, key = self.new_job()
        repeat = self.ratings('/jobs', method='POST', body=self.period, codes=200,
                              headers={'Idempotency-Key': key})['job']
        self.expect(repeat['id'] == job['id'], 'Repeated create uses one stored job and revision')
        def repeated_create(_):
            return self.ratings('/jobs', method='POST', body=self.period, codes=200,
                                headers={'Idempotency-Key': key}, record=False)['job']['id']
        with ThreadPoolExecutor(max_workers=3) as pool:
            repeats = list(pool.map(repeated_create, range(3)))
        self.expect(set(repeats) == {job['id']}, 'Concurrent create retries retain one stored job')
        self.ratings('/jobs', method='POST', body={**self.period, 'date_from': self.yesterday}, codes=409,
                     headers={'Idempotency-Key': key})
        self.first = self.wait_job(job)
        self.assert_transport(self.first)
        listing = self.ratings('/ratings')
        self.expect(not listing['freshness']['is_stale'], 'Published rating is current against owner snapshots')
        self.initial = self.assert_formula(listing['ratings'])
        self.expect([r['place'] for r in listing['ratings']] == [1, 2], 'Ranking preserves final descending order')
        filtered = self.ratings('/ratings?q=' + urllib.parse.quote(self.prefix + ' student2'))
        self.expect(filtered['total'] == 1 and filtered['ratings'][0]['place'] == 2, 'Search preserves a student rank from the complete division rating')
        own = self.ratings('/me', self.student)['rating']
        self.expect(own['profile']['account_id'] == self.student_id and own['rating']['final_rating'] == 65, 'Student reads their own published details')
        self.ratings('/accounts/' + str(self.other_id), self.student, codes=404)
        student_rows = self.ratings('/ratings', self.student)
        self.expect(student_rows['total'] == 1, 'Student list count excludes other participants')
        proctor_rows = self.ratings('/ratings', self.proctor)
        self.expect(proctor_rows['total'] == 1 and proctor_rows['ratings'][0]['profile']['account_id'] == self.student_id,
                    'Proctor rows and count are filtered by the current group')
        self.ratings('/accounts/' + str(self.other_id), self.proctor, codes=404)
        self.ratings('/ratings', self.foreign, codes=(403, 404))
        self.ratings('/jobs/' + job['id'], self.foreign, codes=(403, 404))
        self.ratings('/ratings', self.viewer, division=self.secondary, codes=(403, 404))
        self.expect(self.ratings('/ratings', self.viewer)['total'] == 2, 'Delegated ratings.view grants only its division')
        report = self.ratings('/report')
        self.expect(len(report['students']) == 2 and report['period'] == self.period, 'Report uses the atomically published version and period')
        self.ratings('/report', self.student, codes=403)
        result = self.initial[self.student_id]['rating']
        internal_path = f'/internal/ratings/jobs/{job["id"]}/accounts/{self.student_id}'
        self.request(RATINGS, internal_path + '/input', self.student, codes=401)
        self.request(RATINGS, internal_path + '/result', self.admin, 'POST', result, 401)
        input_snapshot = self.internal(RATINGS, internal_path + '/input')
        self.expect(input_snapshot['input_hash'] == result['input_hash'] and input_snapshot['account_id'] == self.student_id,
                    'Individual calculate input pins the stored snapshot and account identity')
        replay = self.internal(RATINGS, internal_path + '/result', 'POST', result)
        self.expect(replay['accepted'], 'Identical callback remains idempotent after publication')
        self.internal(RATINGS, internal_path + '/result', 'POST', {**result, 'final_rating': 64}, 409)
        self.internal(RATINGS, internal_path + '/result', 'POST', {**result, 'division_id': self.secondary}, 409)
        self.internal(RATINGS, internal_path + '/result', 'POST', {**result, 'revision': result['revision'] + 1}, 409)
        self.core(f'/divisions/{self.division}/memberships/{self.student_id}', method='DELETE', codes=409)
        self.expect(self.core(f'/divisions/{self.division}/users/{self.student_id}')['account_id'] == self.student_id,
                    'A rating lock preserves participation with published history')
        second, _ = self.new_job(division=self.secondary)
        self.wait_job(second, self.secondary)
        isolated = self.ratings('/accounts/' + str(self.student_id), division=self.secondary)['rating']
        self.expect(isolated['rating']['final_rating'] == 0, 'One account has an independent zero rating in a second division')
        self.expect(self.ratings('/me', self.student)['rating']['rating']['final_rating'] == 65,
                    'Publishing another division leaves the first rating unchanged')

    def source_change(self):
        self.homework(f'/assignments/{self.hw[1]}/manual/{self.student_id}', 'PUT',
                      {'status': 1, 'date_pass': self.yesterday, 'result': 20})
        stale = self.ratings('/ratings')
        self.expect(stale['freshness']['is_stale'] and stale['freshness']['reason'] == 'source_changed',
                    'A confirmed homework grade change marks the published rating stale')
        self.expect(next(r for r in stale['ratings'] if r['profile']['account_id'] == self.student_id)['rating']['final_rating'] == 65,
                    'Source changes preserve the previous publication until a full new version is ready')
        job, _ = self.new_job(token=self.editor)
        self.latest = self.wait_job(job)
        self.assert_formula(self.ratings('/ratings')['ratings'], 60)
        versions = self.ratings('/versions')['versions']
        self.expect(len(versions) == 2 and versions[0]['revision'] > versions[1]['revision'],
                    'Published versions retain immutable earlier revisions')
        previous = self.ratings('/ratings?version=' + self.first['id'])
        self.assert_formula(previous['ratings'], 65)
        replay = self.initial[self.student_id]['rating']
        self.internal(RATINGS, f'/internal/ratings/jobs/{self.first["id"]}/accounts/{self.student_id}/result', 'POST', replay)
        self.expect(self.ratings('/ratings')['freshness']['version_id'] == self.latest['id'],
                    'A late duplicate of the old calculation cannot replace the newer version')

    def pending_and_cancel(self):
        if self.attempt_id not in self.fixtures['attempts'] or self.test_id not in self.fixtures['tests']:
            raise AssertionError('Pending fixture identifiers are not owned by this run')
        client = mongo_client(settings())
        collection = client.cpm_dev.tests_attempts_v1
        scope = {'_id': ObjectId(self.attempt_id), 'test_id': ObjectId(self.test_id),
                 'submission_id': ObjectId(self.submission), 'organization_id': 1001,
                 'division_id': self.division, 'account_id': self.student_id, 'active': True,
                 'practice': False, 'status': 'accepted'}
        saved = collection.find_one({**scope, 'calculation_status': 'ready'})
        self.expect(saved is not None and saved.get('result', {}).get('score') == 100,
                    'Pending simulation is guarded by owned accepted submission and ready score')
        changed = False
        try:
            update = collection.update_one({**scope, 'calculation_status': 'ready',
                                           'calculation_revision': saved['calculation_revision']},
                                           {'$set': {'calculation_status': 'queued', 'result': None}})
            self.expect(update.matched_count == 1, 'Only the owned accepted result is temporarily marked pending')
            changed = True
            waiting, waiting_key = self.new_job()
            def waits_for_source():
                j = self.ratings('/jobs/' + waiting['id'], record=False)
                return j if j['status'] == 'waiting_sources' and j['error_code'] == 'tests_calculation_pending' else None
            self.wait(waits_for_source, 'Pending test prevents the next rating snapshot and publication')
            self.ratings('/jobs', method='POST', body=self.period, codes=409,
                         headers={'Idempotency-Key': secrets.token_hex(16)})
            replay = self.ratings('/jobs', method='POST', body=self.period, codes=200,
                                  headers={'Idempotency-Key': waiting_key})['job']
            self.expect(replay['id'] == waiting['id'], 'The pending job remains idempotent')
            preserved = self.ratings('/ratings')
            self.expect(preserved['freshness']['version_id'] == self.latest['id'], 'Pending test leaves the previous published version available')
            self.assert_formula(preserved['ratings'], 60)
            cancelled = self.ratings('/jobs/' + waiting['id'] + '/cancel', method='POST', body={})
            self.expect(cancelled['status'] == 'cancelled', 'Waiting job cancellation is durable')
            self.ratings('/jobs/' + waiting['id'] + '/cancel', self.student, 'POST', {}, 403)
            self.internal(RATINGS, f'/internal/ratings/jobs/{waiting["id"]}/accounts/{self.student_id}/input', codes=(404, 409))
            self.expect(self.ratings('/ratings')['freshness']['version_id'] == self.latest['id'],
                        'Cancelling a pending job does not affect publication')
        finally:
            if changed:
                restore = collection.update_one({**scope, 'calculation_status': 'queued', 'result': None,
                                                 'calculation_revision': saved['calculation_revision']},
                                                 {'$set': {'calculation_status': saved['calculation_status'],
                                                           'result': saved['result']}})
                if restore.matched_count != 1:
                    raise AssertionError('Owned pending fixture changed concurrently; restore could not be verified')
            client.close()
        restarted, _ = self.new_job()
        self.latest = self.wait_job(restarted)
        self.expect(not self.ratings('/ratings')['freshness']['is_stale'], 'Restored ready input supports a subsequent successful publication')
        self.assert_formula(self.ratings('/ratings')['ratings'], 60)

    def finish(self, error=None):
        # Keep synthetic history/locks. Log out only sessions created by this run.
        for token in self.sessions:
            try:
                self.request(AUTH, '/v1/auth/logout', token, 'POST', codes=(200, 401), record=False)
            except Exception:
                pass
        self.persist()
        report = {'status': 'failed' if error else 'passed', 'http_checks': len(self.checks),
                  'scenarios': self.checks, 'run_id': self.run, 'job_progress': self.progress,
                  'target': RATINGS, 'production_accessed': False, 'fixtures_retained': True,
                  'division_id': self.division, 'secondary_division_id': self.secondary,
                  'job_ids': [j['id'] for j in self.fixtures['jobs']]}
        if error:
            report['failure'] = str(error) if isinstance(error, AssertionError) else type(error).__name__
        private_json(self.report_path, report)
        print(json.dumps({'status': report['status'], 'http_checks': report['http_checks'],
                          'fixtures_retained': True, 'run_id': self.run}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--skip-pending', action='store_true', help='Skip the explicitly guarded temporary pending Mongo fixture.')
    arguments = parser.parse_args()
    suite = Suite()
    error = None
    try:
        suite.setup()
        suite.baseline()
        suite.source_change()
        if not arguments.skip_pending:
            suite.pending_and_cancel()
    except Exception as failure:
        error = failure
    finally:
        suite.finish(error)
    if error:
        raise SystemExit('Ratings HTTP checks failed; see private local report.')


if __name__ == '__main__':
    main()
