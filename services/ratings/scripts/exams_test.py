"""Real Go exams -> ratings -> RabbitMQ -> calculate regression on cpm_dev.

Reuses retained synthetic ratings participants and creates its own outside exams
through their public API. Exam and rating history is retained. No direct SQL,
Mongo operations, production endpoints or service/process control is performed.
Credentials are read locally and never written into fixtures or reports.
"""
import argparse
from datetime import datetime, timezone
import json
import math
import os
from pathlib import Path
import re
import secrets
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from zoneinfo import ZoneInfo

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'dev/scripts'))
from bootstrap import settings

AUTH = 'http://127.0.0.1:8002'
EXAMS = 'http://127.0.0.1:8010'
RATINGS = 'http://127.0.0.1:8012'
ORIGINS = {AUTH, EXAMS, RATINGS}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


def private_json(path, value):
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(descriptor, 'w') as output:
        os.fchmod(output.fileno(), 0o600)
        json.dump(value, output, ensure_ascii=False, indent=2, allow_nan=False)
        output.write('\n')


class Suite:
    def __init__(self, expected_run):
        self.env = settings()  # Enforces fixed loopback cpm_dev dependencies.
        self.source = json.loads((ROOT / 'dev/local/ratings-fixtures.json').read_text())
        if not re.fullmatch(r'[0-9a-f]{12}', expected_run) or self.source.get('run_id') != expected_run:
            raise AssertionError('retained_synthetic_fixture_run_mismatch')
        if self.source.get('organization_id') != 1001:
            raise AssertionError('synthetic_organization_guard')
        self.source_run = expected_run
        self.division = int(self.source['division_id'])
        self.secondary = int(self.source['secondary_division_id'])
        self.student = int(self.source['student_id'])
        self.other = int(self.source['other_id'])
        if self.division == self.secondary or min(self.division, self.secondary) <= 1001:
            raise AssertionError('synthetic_division_guard')
        self.period = self.source['period']
        self.today = datetime.now(ZoneInfo('Europe/Moscow')).date().isoformat()
        if not self.period['date_from'] <= self.today <= self.period['date_to']:
            raise AssertionError('exam_date_outside_retained_test_period')
        self.users = {int(row['account_id']): row for row in self.source['users']}
        for account, suffix in [(self.student, 'student1'), (self.other, 'student2')]:
            if self.users.get(account, {}).get('login') != f'dev_ratings_{expected_run}_{suffix}':
                raise AssertionError('synthetic_student_login_guard')
        self.admin_password = json.loads((ROOT / 'dev/local/accounts.json').read_text())['password']
        self.run = secrets.token_hex(6)
        self.prefix = 'DEV Ratings Exams ' + self.source_run + ' ' + self.run
        self.tokens, self.checks, self.progress = [], [], {}
        self.opener = urllib.request.build_opener(NoRedirect())
        self.fixture_path = ROOT / 'dev/local/ratings-exams-fixtures.json'
        self.report_path = ROOT / 'dev/local/ratings-exams-report.json'
        self.fixtures = {'run_id': self.run, 'ratings_fixture_run_id': self.source_run,
                         'organization_id': 1001, 'division_ids': [self.division, self.secondary],
                         'student_account_id': self.student, 'exam_ids': [], 'jobs': [],
                         'fixtures_retained': True, 'prior_runs': []}
        if self.fixture_path.exists():
            previous = json.loads(self.fixture_path.read_text())
            self.fixtures['prior_runs'] = previous.get('prior_runs', []) + [
                {key: value for key, value in previous.items() if key != 'prior_runs'}]
        self.persist()

    def persist(self):
        private_json(self.fixture_path, self.fixtures)

    def expect(self, condition, label):
        if not condition:
            raise AssertionError(label)
        self.checks.append(label)

    def request(self, origin, path, *, token=None, method='GET', body=None, headers=None,
                codes=(200,), record=True):
        if origin not in ORIGINS or not path.startswith('/'):
            raise AssertionError('fixed_loopback_origin_guard')
        outbound = dict(headers or {})
        if token:
            outbound['Authorization'] = 'Bearer ' + token
        raw = None
        if body is not None:
            raw = json.dumps(body, allow_nan=False).encode()
            outbound['Content-Type'] = 'application/json'
        req = urllib.request.Request(origin + path, data=raw, method=method, headers=outbound)
        try:
            with self.opener.open(req, timeout=45) as response:
                code, payload = response.status, response.read()
        except urllib.error.HTTPError as response:
            code, payload = response.code, response.read()
        except (urllib.error.URLError, TimeoutError):
            raise AssertionError('loopback_http_unavailable') from None
        if code not in codes:
            raise AssertionError(f'{method} {path.split("?")[0]} unexpected_http_{code}')
        try:
            result = json.loads(payload)
        except ValueError:
            raise AssertionError('invalid_response_json') from None
        if record:
            self.expect(True, f'{method} {path.split("?")[0]} HTTP {code}')
        return code, result

    def api(self, origin, path, **kwargs):
        _, response = self.request(origin, path, **kwargs)
        self.expect(response.get('status') is True and 'data' in response, 'business_success_envelope')
        return response['data']

    def login(self, login, division, *, fixture=False):
        _, response = self.request(AUTH, '/v1/auth/login', method='POST', body={
            'login': login, 'password': self.source['password'] if fixture else self.admin_password,
            'organization_id': 1001, 'division_id': division})
        token, user = response['token'], response['user']
        self.tokens.append(token)
        self.expect(user.get('organization_id') == 1001 and user.get('division_id') == division,
                    'auth_selected_organization_and_division')
        return token, user

    def exam(self, path='', *, token, method='GET', body=None, codes=(200,)):
        headers = {'Idempotency-Key': str(uuid.uuid4())} if method != 'GET' else {}
        return self.api(EXAMS, '/v1/exams' + path, token=token, method=method,
                        body=body, headers=headers, codes=codes)

    def rating(self, division, path, *, token, method='GET', body=None, headers=None,
               codes=(200,), record=True):
        return self.api(RATINGS, f'/v1/ratings/divisions/{division}' + path, token=token,
                        method=method, body=body, headers=headers, codes=codes, record=record)

    def snapshot(self, division):
        query = urllib.parse.urlencode({'organization_id': 1001, 'division_ids': division,
                                        **self.period, 'as_of': datetime.now(timezone.utc).isoformat()})
        data = self.api(EXAMS, '/internal/exams/rating-snapshot?' + query,
                        headers={'X-Service-Key': self.env['AUTH_INTERNAL_KEY']})
        snapshot = data['snapshot']
        self.expect(snapshot['organization_id'] == 1001 and snapshot['division_ids'] == [division],
                    'real_exam_snapshot_has_exact_scope')
        self.expect(str(division) in data['source_revisions'] and
                    re.fullmatch(r'[0-9a-f]{64}', snapshot['source_revision']) is not None,
                    'real_exam_snapshot_has_revisions')
        return data

    @staticmethod
    def subject_result(snapshot, exam, student):
        matches = [row for row in snapshot['results']
                   if row['exam_id'] == exam and row['account_id'] == student]
        if len(matches) != 1:
            raise AssertionError('effective_exam_result_not_unique')
        return matches[0]

    def create_job(self, division, token):
        key = secrets.token_hex(16)
        data = self.rating(division, '/jobs', token=token, method='POST', body=self.period,
                           headers={'Idempotency-Key': key}, codes=(202,))
        job = data['job']
        self.fixtures['jobs'].append({'id': job['id'], 'division_id': division,
                                      'idempotency_key': key})
        self.persist()
        self.expect(job['status'] != 'completed', 'job_acknowledges_before_atomic_publication')
        until, seen = time.monotonic() + 120, []
        while time.monotonic() < until:
            current = self.rating(division, '/jobs/' + job['id'], token=token, record=False)
            state = [current['status'], current['processed_count'], current['total_count']]
            if not seen or seen[-1] != state:
                seen.append(state)
            if current['status'] in ('failed', 'cancelled', 'superseded'):
                code = current.get('error_code', 'unknown')
                code = code if re.fullmatch(r'[a-z0-9_]{0,100}', code) else 'unknown'
                raise AssertionError('rating_job_terminal_' + code)
            if current['status'] == 'completed':
                self.progress[job['id']] = seen
                self.expect(current['processed_count'] == current['total_count'],
                            'all_personal_results_accepted_before_publication')
                return current
            time.sleep(.25)
        raise AssertionError('rating_job_timeout')

    def run_checks(self):
        health = self.api(RATINGS, '/health/ready')
        self.expect(health.get('exams_mode') == 'required' and health.get('exams_connected') is True,
                    'ratings_uses_real_required_exam_source')
        self.expect(health.get('external_tests_mode') == 'dev_internal_only',
                    'external_legacy_test_gap_is_explicit_dev_mode')
        self.api(EXAMS, '/health/ready')
        admin_a, _ = self.login('dev_admin', self.division)
        admin_b, _ = self.login('dev_admin', self.secondary)
        student_a, profile = self.login(self.users[self.student]['login'], self.division, fixture=True)
        self.expect(profile.get('account_id') == self.student and
                    profile.get('full_name', '').startswith('DEV Ratings ' + self.source_run),
                    'retained_primary_student_is_synthetic')
        other_a, profile = self.login(self.users[self.other]['login'], self.division, fixture=True)
        self.expect(profile.get('account_id') == self.other and
                    profile.get('full_name', '').startswith('DEV Ratings ' + self.source_run),
                    'retained_second_student_is_synthetic')
        student_b, profile = self.login(self.users[self.student]['login'], self.secondary, fixture=True)
        self.expect(profile['account_id'] == self.student, 'same_auth_account_has_second_division_context')

        before_a, before_b = self.snapshot(self.division), self.snapshot(self.secondary)
        self.expect(not before_a['snapshot']['exams'] and not before_b['snapshot']['exams'],
                    'retained_scopes_have_no_unrelated_exams_before_test')
        baselines = {}
        for division, token in [(self.division, admin_a), (self.secondary, admin_b)]:
            baselines[division] = self.rating(division, '/accounts/' + str(self.student), token=token)['rating']['rating']

        shared = self.exam(token=admin_a, method='POST', body={
            'exam_type': 'outside_lms',
            'direction_id': 1001, 'date': self.today}, codes=(201,))
        shared_id = shared['id']
        self.fixtures['exam_ids'].append(shared_id)
        self.fixtures['shared_exam_id'] = shared_id
        self.persist()
        outside_a = self.exam(f'/{shared_id}/outside-results', token=admin_a, method='POST', body={
            'student_account_id': self.student, 'points': 123.45, 'grade': 4,
            'examinator': self.prefix})['result']
        self.expect(outside_a['grade'] == 4, 'owner_stores_grade_separately_from_external_points')
        current = self.exam(f'/{shared_id}', token=admin_a)
        self.exam(f'/{shared_id}/connections', token=admin_a, method='POST', body={
            'division_id': self.secondary, 'expected_revision': current['revision']})
        self.exam(f'/{shared_id}/outside-results', token=admin_b, method='POST', body={
            'student_account_id': self.student, 'points': 987.65, 'grade': 2,
            'examinator': self.prefix})
        current = self.exam(f'/{shared_id}', token=admin_b)
        copied = self.exam(f'/{shared_id}/copy', token=admin_b, method='POST', body={
            'division_id': self.secondary, 'expected_revision': current['revision']})
        copied_id = copied['id']
        self.fixtures['exam_ids'].append(copied_id)
        self.fixtures['copied_exam_id'] = copied_id
        self.persist()
        self.expect(copied_id != shared_id and copied['origin_exam_id'] == shared_id,
                    'deep_copy_has_independent_exam_identity')
        empty_copy = self.snapshot(self.secondary)['snapshot']
        self.expect(any(row['exam_id'] == copied_id for row in empty_copy['exams']) and
                    not any(row['exam_id'] == copied_id for row in empty_copy['results']),
                    'deep_copy_does_not_inherit_original_results')
        self.exam(f'/{copied_id}/outside-results', token=admin_b, method='POST', body={
            'student_account_id': self.student, 'points': 0, 'grade': 5,
            'examinator': self.prefix})

        source_a, source_b = self.snapshot(self.division), self.snapshot(self.secondary)
        snap_a, snap_b = source_a['snapshot'], source_b['snapshot']
        self.expect({row['exam_id'] for row in snap_a['exams']} == {shared_id} and
                    {row['exam_id'] for row in snap_b['exams']} == {shared_id, copied_id},
                    'catalogue_contains_shared_exam_only_in_connected_scopes_and_copy_only_in_B')
        self.expect(self.subject_result(snap_a, shared_id, self.student)['grade'] == 4 and
                    self.subject_result(snap_b, shared_id, self.student)['grade'] == 2,
                    'shared_content_preserves_independent_results_for_same_account')
        self.expect(self.subject_result(snap_b, copied_id, self.student)['grade'] == 5,
                    'copied_exam_owns_its_own_grade')
        self.expect(source_a['source_revisions'][str(self.division)] > before_a['source_revisions'][str(self.division)] and
                    source_b['source_revisions'][str(self.secondary)] > before_b['source_revisions'][str(self.secondary)],
                    'owner_scope_revisions_change_after_api_mutations')
        self.expect(not any(row['account_id'] == self.other for row in snap_a['results']),
                    'missing_second_student_remains_absent_result')

        self.create_job(self.division, admin_a)
        self.create_job(self.secondary, admin_b)
        finals = {}
        for division, token, mean, expected_ids in [
            (self.division, student_a, 4.0, {str(shared_id)}),
            (self.secondary, student_b, 3.5, {str(shared_id), str(copied_id)}),
        ]:
            data = self.rating(division, '/me', token=token)
            row, fresh = data['rating']['rating'], data['freshness']
            baseline = baselines[division]
            expected = (baseline['homework']['rating'] * 25) / 100 + mean * 6 + (baseline['tests']['rating'] * 45) / 100
            self.expect(math.isclose(row['exams']['rating'], mean, rel_tol=0, abs_tol=1e-9),
                        'published_exam_mean_matches_real_owner_snapshot')
            self.expect(math.isclose(row['final_rating'], expected, rel_tol=0, abs_tol=1e-9),
                        'full_rating_preserves_prior_homework_tests_and_adds_exact_exam_component')
            self.expect(row['homework']['rating'] == baseline['homework']['rating'] and
                        row['tests']['rating'] == baseline['tests']['rating'],
                        'exam_integration_does_not_modify_other_components')
            self.expect({entry['exam_id'] for entry in row['exams']['details']} == expected_ids,
                        'published_exam_details_preserve_original_and_copy_ids')
            self.expect(fresh['is_stale'] is False and fresh['exams_mode'] == 'required' and
                        fresh['external_tests_mode'] == 'dev_internal_only',
                        'publication_has_fresh_real_exam_source_and_explicit_external_gap')
            finals[str(division)] = {'exam_mean': mean, 'final_rating': row['final_rating'], 'expected_final': expected}
        missing = self.rating(self.division, '/me', token=other_a)['rating']['rating']
        self.expect(missing['exams']['rating'] == 0 and len(missing['exams']['details']) == 1 and
                    missing['exams']['details'][0]['missing_result'] is True and
                    missing['exams']['details'][0]['score'] == 0,
                    'missing_exam_is_zero_in_denominator_for_second_student')
        self.fixtures['expected_results'] = finals
        self.persist()
        return finals

    def logout(self):
        for token in self.tokens:
            try:
                self.request(AUTH, '/v1/auth/logout', token=token, method='POST', body={},
                             codes=(200, 401), record=False)
            except AssertionError:
                pass


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--fixture-run-id', default='c10d133ac44b',
                        help='Exact retained synthetic ratings run required by scope guard.')
    args = parser.parse_args()
    suite, error, finals = None, None, None
    try:
        suite = Suite(args.fixture_run_id)
        finals = suite.run_checks()
    except (AssertionError, KeyError, ValueError, OSError) as failure:
        # Never serialize raw transport errors, request bodies or credentials.
        error = str(failure) if isinstance(failure, AssertionError) else 'local_test_configuration_error'
    finally:
        if suite is not None:
            suite.logout()
    report = {'status': 'passed' if error is None else 'failed',
              'checks': len(suite.checks) if suite else 0,
              'production_accessed': False, 'direct_database_access': False,
              'fixtures_retained': True, 'ratings_fixture_run_id': args.fixture_run_id,
              'run_id': suite.run if suite else None,
              'progress': suite.progress if suite else {}, 'results': finals,
              'error': error}
    private_json(ROOT / 'dev/local/ratings-exams-report.json', report)
    print(json.dumps({key: value for key, value in report.items() if key != 'progress'}, ensure_ascii=False))
    return 0 if error is None else 1


if __name__ == '__main__':
    raise SystemExit(main())
