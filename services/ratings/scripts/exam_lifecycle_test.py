"""HTTP-only classic exam lifecycle -> asynchronous ratings acceptance.

Run after the latest tenant_test.py passes and both owners confirm readiness.
Only this run's new exam/examinators and A1/A2 rating publications are written.
No existing account membership, source fixture, database or process is changed.
"""
import argparse
import copy
from datetime import datetime, timedelta, timezone
import importlib.util
import json
import math
from pathlib import Path
import secrets
import urllib.parse
import uuid


HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('ratings_period_http_helpers', HERE / 'period_sources_test.py')
http = importlib.util.module_from_spec(spec)
spec.loader.exec_module(http)  # Standard-library HTTP helpers; no DB modules/imports.


class Suite(http.Suite):
    def __init__(self, fixture_path=None):
        super().__init__(fixture_path)  # Latest successful private tenant manifest + cpm_dev guard.
        self.started = datetime.now(timezone.utc)
        self.as_of = self.started.isoformat().replace('+00:00', 'Z')
        self.exam_name = 'DEV Ratings Lifecycle ' + self.nonce
        self.own_password = secrets.token_urlsafe(24)
        self.fixture_path = http.LOCAL / f'ratings-exam-lifecycle-{self.run}-{self.nonce}-fixtures.json'
        self.report_path = http.LOCAL / 'ratings-exam-lifecycle-report.json'
        self.fixtures = {'tenant_run_id': self.run, 'lifecycle_run_id': self.nonce,
                         'source_manifest': self.source_path.name, 'password': self.own_password,
                         'started_at': self.as_of, 'users': [], 'exams': [], 'commissions': [],
                         'assignments': [], 'attempts': [], 'jobs': [], 'event_expectations': [],
                         'retained': True}
        self.history, self.examiners = {}, []
        self.exam_id = None

    def core(self, scope, path, method='GET', body=None, codes=200):
        return self.api(http.CORE, '/v1/core' + path, scope['admin'], method, body, codes)

    def exam(self, scope, path='', method='GET', body=None, token=None, codes=200):
        # Public Exams selects division from JWT; its strict query contract
        # does not accept a division_id override.
        headers = {'Idempotency-Key': str(uuid.uuid4())} if method != 'GET' else None
        return self.api(http.EXAMS, '/v1/exams' + path, token or scope['admin'], method, body,
                        codes=codes, headers=headers)

    def path(self, tail=''):
        return '/' + str(self.exam_id) + tail

    def setup(self):
        for origin in sorted(http.ORIGINS):
            self.request(origin, '/health/ready')
        health = self.api(http.RATINGS, '/health/ready')
        self.expect(health.get('exams_mode') == health.get('external_tests_mode') == 'required'
                    and health.get('exams_connected'), 'Lifecycle requires both real rating source domains')
        self.today = datetime.now(http.MOSCOW).date().isoformat()
        self.full = self.source['period']
        self.expect(self.full['date_from'] <= self.today <= self.full['date_to'],
                    'Retained full tenant period contains this lifecycle calendar day')
        entries = self.source.get('scopes') or []
        self.expect(len(entries) == 4 and {s['label'] for s in entries} == {'A1', 'A2', 'B1', 'B2'},
                    'The successful tenant manifest has its four distinct synthetic scopes')
        users = {u['login']: u for u in self.source['users'] if u['role'] == 'student'}
        for entry in entries:
            scope = copy.deepcopy(entry)
            label = scope['label']
            self.expect(scope['organization_id'] == (1001 if label.startswith('A') else 2001),
                        'Tenant scope belongs to the expected seeded development organization')
            scope['admin'], profile = self.login('dev_admin' if label.startswith('A') else 'dev_admin_math', scope)
            self.expect(profile.get('organization_admin') and profile['role'] == 'admin',
                        'Lifecycle source mutations use the selected organization owner')
            refs = self.internal(http.CORE, '/internal/core/test-references', 'POST', {
                'organization_id': scope['organization_id'], 'division_ids': [scope['division_id']]})
            self.expect(len(refs['divisions']) == 1 and refs['divisions'][0]['id'] == scope['division_id']
                        and refs['divisions'][0]['name'] == self.prefix + ' ' + label
                        and refs['divisions'][0]['is_active'], 'Exact active division name guards the retained tenant run')
            if 'direction_id' not in scope:
                snapshot = self.internal(http.TESTS, '/internal/tests/ratings/snapshot', 'POST', {
                    'organization_id': scope['organization_id'], 'division_id': scope['division_id'],
                    **self.full, 'as_of': self.as_of})
                own = [t for t in snapshot['tests'] if t['title'] == self.prefix + ' ' + label + ' official']
                self.expect(len(own) == 1, 'Original owned internal test identifies this scope direction')
                scope['direction_id'] = own[0]['direction_id']
            scope['students'] = []
            for suffix in [('A1' if label == 'A2' else label) + '_primary', label + '_peer']:
                login = f'dev_ratings_tenant_{self.run}_{suffix}'
                self.expect(login in users, 'Only original synthetic tenant students are used')
                student = {'account_id': users[login]['account_id'], 'login': login}
                student['token'], context = self.login(login, scope, synthetic=True)
                self.expect(context['account_id'] == student['account_id'], 'Student login retains its original account ID')
                scope['students'].append(student)
            baseline = self.rating(scope, '/report')
            self.expect(baseline['period'] == self.full and not baseline['freshness']['is_stale'],
                        'Each tenant scope starts with a fresh complete publication for the full period')
            expected_ids = {s['account_id'] for s in scope['students']}
            self.expect(len(baseline['students']) == 2 and {r['profile']['account_id'] for r in baseline['students']} == expected_ids,
                        'Baseline includes exactly the two original scope participants')
            scope['baseline_report'] = copy.deepcopy(baseline)
            scope['current_report'] = baseline
            scope['current_job_id'] = baseline['freshness']['version_id']
            self.scopes[label] = scope
            self.history[scope['current_job_id']] = {'label': label, 'period': baseline['period'],
                                                   'students': copy.deepcopy(baseline['students'])}
        self.a, self.b = self.scopes['A1'], self.scopes['A2']
        self.student_id = self.a['students'][0]['account_id']
        self.expect(self.student_id == self.b['students'][0]['account_id'],
                    'The same synthetic student has separate A1 and A2 memberships')
        self.fixtures['scopes'] = [{'label': s['label'], 'organization_id': s['organization_id'],
                                   'division_id': s['division_id'], 'initial_job_id': s['current_job_id']}
                                  for s in self.scopes.values()]
        self.fixtures['period'] = self.full
        self.persist()

    def create_examiners(self):
        self.phase = 'own_examiners'
        for n in range(3):
            login = f'dev_rl_{self.nonce}_x{n+1}'
            body = {'role': 'examinator', 'full_name': f'DEV Ratings Lifecycle {self.nonce} X{n+1}',
                    'login': login, 'password': self.own_password, 'tg_name': ''}
            account = self.core(self.a, f'/divisions/{self.a["division_id"]}/users', 'POST', body, 201)['account_id']
            self.fixtures['users'].append({'account_id': account, 'login': login, 'role': 'examinator',
                                          'organization_id': 1001, 'division_ids': [self.a['division_id']]})
            self.persist()
            self.core(self.b, f'/divisions/{self.b["division_id"]}/memberships/{account}', 'PUT', {})
            self.fixtures['users'][-1]['division_ids'].append(self.b['division_id'])
            examiner = {'account_id': account, 'tokens': {}}
            for scope in (self.a, self.b):
                _, response = self.request(http.AUTH, '/v1/auth/login', method='POST', body={
                    'login': login, 'password': self.own_password, 'organization_id': 1001,
                    'division_id': scope['division_id']})
                token = response.get('access_token') or response['token']
                self.sessions.append(token)
                user = response['user']
                self.expect(user['account_id'] == account and user['role'] == 'examinator'
                            and user['division_id'] == scope['division_id'], 'New examiner authenticates in its own conduct scope')
                examiner['tokens'][scope['label']] = token
            self.examiners.append(examiner)
            self.persist()

    def create_exam(self):
        self.phase = 'own_shared_classic'
        created = self.exam(self.a, method='POST', body={'exam_type': 'classic',
                            'direction_id': self.a['direction_id'], 'name': self.exam_name}, codes=201)
        self.exam_id = created['id']
        self.fixtures['exams'].append(self.exam_id)
        self.persist()
        part = self.exam(self.a, self.path('/parts'), 'POST', {'code': 'A', 'weight': 5, 'quota': 1})['part']
        self.exam(self.a, self.path('/questions'), 'POST', {'part_id': part['id'],
                  'question_text': 'DEV lifecycle question ' + self.nonce, 'answer_text': 'DEV lifecycle answer'})
        self.exam(self.a, self.path('/connections'), 'POST', {'division_id': self.b['division_id'],
                  'expected_revision': self.exam(self.a, self.path())['revision']})
        for scope, mode in ((self.a, 'round_up'), (self.b, 'round_down')):
            config = self.exam(scope, self.path('/config'))['config']
            self.exam(scope, self.path('/config'), 'PUT', {'expected_revision': config['revision'],
                      'start_at': (self.started - timedelta(minutes=5)).isoformat(),
                      'end_at': (self.started + timedelta(hours=4)).isoformat(), 'fractional_mode': mode,
                      'thresholds': [{'grade': n, 'min_score': n} for n in range(6)]})
            commission = self.commission(scope, [0, 1], 'first')
            scope['assignment'] = self.exam(scope, self.path('/assignments'), 'POST', {
                'student_account_id': self.student_id, 'commission_id': commission['id']})['assignment']
            self.fixtures['assignments'].append({'id': scope['assignment']['id'], 'division_id': scope['division_id'], 'attempt_no': 1})
            self.expect(self.exam(scope, self.path('/readiness'))['is_configured'], 'Real owner readiness validates the complete scoped classic exam')
        self.expect(self.exam(self.a, self.path('/config'))['config']['fractional_mode'] == 'round_up'
                    and self.exam(self.b, self.path('/config'))['config']['fractional_mode'] == 'round_down',
                    'A shared question bank retains independent division conduct rules')
        self.persist()
        self.selected(self.snapshot_all(), self.a, None)
        self.selected(self.snapshot_all(), self.b, None)
        self.publish(self.a, 'classic-missing', None)
        self.publish(self.b, 'classic-missing', None)

    def commission(self, scope, indexes, name):
        commission = self.exam(scope, self.path('/commissions'), 'POST', {
            'name': f'DEV lifecycle {self.nonce} {name}',
            'examinator_ids': [self.examiners[i]['account_id'] for i in indexes]})['commission']
        self.fixtures['commissions'].append({'id': commission['id'], 'division_id': scope['division_id']})
        self.persist()
        return commission

    def snapshot_all(self):
        snapshots = {}
        for label, scope in self.scopes.items():
            query = urllib.parse.urlencode({'organization_id': scope['organization_id'],
                                           'division_ids': scope['division_id'], **self.full, 'as_of': self.as_of})
            row = self.internal(http.EXAMS, '/internal/exams/rating-snapshot?' + query, record=False)
            self.expect(row['snapshot']['organization_id'] == scope['organization_id']
                        and row['snapshot']['division_ids'] == [scope['division_id']], 'Internal exam snapshot echoes the exact tenant scope')
            snapshots[label] = row
        return snapshots

    def source_delta(self, before, after, affected, name):
        evidence = {'phase': name, 'affected_division_ids': [self.scopes[x]['division_id'] for x in affected],
                    'organization_id': 1001, 'source_revisions_before': {}, 'source_revisions_after': {},
                    'source_hashes_before': {}, 'source_hashes_after': {},
                    'selected_results_before': {}, 'selected_results_after': {}}
        for label, scope in self.scopes.items():
            key = str(scope['division_id'])
            old, new = before[label]['source_revisions'][key], after[label]['source_revisions'][key]
            self.expect(new == old + (1 if label in affected else 0), name + ': exact source counter delta stays in affected divisions')
            old_hash, new_hash = before[label]['snapshot']['source_revision'], after[label]['snapshot']['source_revision']
            self.expect((old_hash != new_hash) == (label in affected), name + ': semantic source hash follows effective rating inputs')
            if label not in affected:
                self.expect(before[label]['snapshot'] == after[label]['snapshot'], name + ': unmodified division snapshot remains byte-semantic identical')
            evidence['source_revisions_before'][label], evidence['source_revisions_after'][label] = old, new
            evidence['source_hashes_before'][label], evidence['source_hashes_after'][label] = old_hash, new_hash
            for source, key in ((before, 'selected_results_before'), (after, 'selected_results_after')):
                evidence[key][label] = [
                    {k: row[k] for k in ('exam_id', 'division_id', 'account_id', 'grade', 'result_id',
                                        'attempt_no', 'result_version', 'history_generation', 'has_appeal') if k in row}
                    for row in source[label]['snapshot']['results']
                    if row['exam_id'] == self.exam_id and row['account_id'] == self.student_id]
        self.fixtures['event_expectations'].append(evidence)
        self.persist()

    def selected(self, snapshots, scope, grade, attempt=None, appealed=False, result_id=None, version=None):
        snap = snapshots[scope['label']]['snapshot']
        catalog = [e for e in snap['exams'] if e['exam_id'] == self.exam_id]
        self.expect(len(catalog) == 1 and catalog[0]['exam_name'] == self.exam_name,
                    'Shared classic exam appears once with its current name in the selected division')
        rows = [r for r in snap['results'] if r['exam_id'] == self.exam_id and r['account_id'] == self.student_id]
        if grade is None:
            self.expect(rows == [], 'An unfinished first attempt supplies no completed exam result row')
            return None
        self.expect(len(rows) == 1 and rows[0]['grade'] == grade and not rows[0]['missing_result']
                    and rows[0]['attempt_no'] == attempt and rows[0]['has_appeal'] == appealed,
                    'Owner snapshot selects the exact effective attempt and appeal grade')
        if result_id is not None:
            self.expect(rows[0]['result_id'] == result_id, 'Selected owner result identity belongs to the expected attempt')
        if version is not None:
            self.expect(rows[0]['result_version'] == version, 'Selected result version matches the committed appeal history')
        return rows[0]

    def assert_history(self):
        for job_id, historical in self.history.items():
            scope = self.scopes[historical['label']]
            old = self.rating(scope, '/report?version=' + job_id, record=False)
            self.expect(old['period'] == historical['period'] and old['students'] == historical['students'],
                        'Later exam lifecycle transitions preserve immutable published history')

    def assert_pointers(self, stale, name):
        for label, scope in self.scopes.items():
            def reached():
                report = self.rating(scope, '/report', record=False)
                return report if report['freshness']['is_stale'] == (label in stale) else None
            report = self.wait(reached, name + ': current publication has the expected scoped freshness', 30)
            self.expect(report['freshness']['version_id'] == scope['current_job_id']
                        and report['students'] == scope['current_report']['students'],
                        name + ': source events retain the last complete publication pointer and rows')
            if label in stale:
                self.expect(report['freshness']['reason'] == 'source_changed', name + ': staleness identifies changed owner input')
        self.assert_history()

    def publish(self, scope, scenario, grade, attempt=None, appealed=False):
        self.same_day()
        job = self.rating(scope, '/jobs', 'POST', self.full,
                          {'Idempotency-Key': str(uuid.uuid4())}, codes=202)['job']
        self.fixtures['jobs'].append({'id': job['id'], 'organization_id': scope['organization_id'],
                                     'division_id': scope['division_id'], 'scenario': scenario})
        self.persist()
        stages = []
        def completed():
            current = self.rating(scope, '/jobs/' + job['id'], record=False)
            stage = [current['status'], current['processed_count'], current['total_count']]
            if not stages or stages[-1] != stage:
                stages.append(stage)
            if current['status'] in ('failed', 'superseded', 'cancelled'):
                raise AssertionError('Owned lifecycle rating job entered a terminal failure')
            return current if current['status'] == 'completed' else None
        job = self.wait(completed, 'Lifecycle rating job completes both personal asynchronous calculations')
        self.progress[job['id']] = stages
        self.expect(job['processed_count'] == job['total_count'] == 2, 'The new publication includes exactly both tenant students')
        report = self.rating(scope, '/report')
        self.expect(report['period'] == self.full and report['freshness']['version_id'] == job['id']
                    and not report['freshness']['is_stale'], 'Explicit recalculation atomically publishes a fresh lifecycle version')
        baseline = {r['profile']['account_id']: r['rating'] for r in scope['baseline_report']['students']}
        self.expect({r['profile']['account_id'] for r in report['students']} == set(baseline), 'Exam helpers do not become rating participants')
        for row in report['students']:
            account, result = row['profile']['account_id'], row['rating']
            old = baseline[account]
            clean_tests = lambda component: {**component, 'details': [{k: v for k, v in d.items() if k != 'calculation_status'} for d in component['details']]}
            self.expect(result['homework'] == old['homework'] and clean_tests(result['tests']) == clean_tests(old['tests']),
                        'Exam lifecycle preserves every homework/test contribution and source detail')
            prior = {str(e['exam_id']): e for e in old['exams']['details']}
            details = {str(e['exam_id']): e for e in result['exams']['details']}
            self.expect(len(details) == len(prior) + 1 and all(details[k] == v for k, v in prior.items()),
                        'Only the owned classic exam is added; prior exam details remain unchanged')
            detail = details[str(self.exam_id)]
            effective = grade if account == self.student_id else None
            score = effective if effective is not None else 0
            self.expect(detail['score'] == score and detail['missing_result'] == (effective is None)
                        and detail['exam_name'] == self.exam_name and detail['exam_type'] == 'classic'
                        and detail['attempt_no'] == (attempt if effective is not None else None)
                        and detail['has_appeal'] == (appealed if effective is not None else False),
                        'Published classic detail retains completed-zero, missing, attempt and appeal semantics')
            # Independent arithmetic on the baseline plus ONE new known exam,
            # without reproducing grading, question selection, or result choice.
            exam_mean = (sum(d['score'] for d in prior.values()) + score) / (len(prior) + 1)
            final = (old['homework']['rating'] * 25) / 100 + exam_mean * 6 + (old['tests']['rating'] * 45) / 100
            self.expect(math.isclose(result['exams']['rating'], exam_mean, rel_tol=0, abs_tol=1e-9)
                        and math.isclose(result['final_rating'], final, rel_tol=0, abs_tol=1e-9),
                        'Manual baseline H/T plus the known effective exam score matches the complete legacy formula')
            self.expect(result['organization_id'] == scope['organization_id'] and result['division_id'] == scope['division_id']
                        and result['account_id'] == account and result['job_id'] == job['id'], 'New callback and publication metadata stay in their exact personal scope')
            personal = self.internal(http.RATINGS, f'/internal/ratings/jobs/{job["id"]}/accounts/{account}/input')
            own = [e for e in personal['exams'] if e['exam_id'] == str(self.exam_id)]
            self.expect(len(own) == 1 and own[0]['score'] == effective and own[0]['missing_result'] == (effective is None),
                        'Immutable personal input distinguishes absent exam score from a completed zero')
        def delivered():
            messages = self.internal(http.CALCULATE, '/internal/calculate/ratings/messages?' + urllib.parse.urlencode({
                'job_id': job['id'], 'limit': 100}), record=False)['items']
            return messages if len(messages) == 2 and all(m['status'] == 'ready' for m in messages) else None
        messages = self.wait(delivered, 'Both lifecycle callbacks finish durably in the calculate inbox', 30)
        self.expect({m['account_id'] for m in messages} == set(baseline) and all(
            m['job_id'] == job['id'] and m['organization_id'] == scope['organization_id']
            and m['division_id'] == scope['division_id'] and m['revision'] == job['revision'] for m in messages),
            'Calculate diagnostics retain exactly two correctly scoped durable callback identities')
        scope['current_report'], scope['current_job_id'] = report, job['id']
        self.history[job['id']] = {'label': scope['label'], 'period': report['period'], 'students': copy.deepcopy(report['students'])}
        self.assert_history()

    def begin(self, scope, assignment, indexes):
        token = self.examiners[indexes[0]]['tokens'][scope['label']]
        attempt = self.exam(scope, self.path(f'/assignments/{assignment["id"]}/attempts/ensure'), 'POST',
                            {'expected_history_generation': assignment['history_generation']}, token)['attempt']
        apath = self.path('/attempts/' + str(attempt['id']))
        self.fixtures['attempts'].append({'id': attempt['id'], 'division_id': scope['division_id'], 'attempt_no': attempt['attempt_no']})
        self.persist()
        for i in indexes:
            self.exam(scope, apath + '/ready', 'POST', {}, self.examiners[i]['tokens'][scope['label']])
        current = self.exam(scope, apath, token=token)
        self.expect(current['all_members_ready'], 'Both real commission members become ready')
        started = self.exam(scope, apath + '/start', 'POST', {'expected_state_version': current['state_version']}, token)
        self.expect(started['attempt']['status'] == 'in_progress' and not started['rating_changed'],
                    'Public commission workflow starts an unfinished attempt without rating invalidation')
        return apath, attempt['id']

    def vote(self, scope, apath, index, value):
        token = self.examiners[index]['tokens'][scope['label']]
        question = self.exam(scope, apath, token=token)['current_question']
        return self.exam(scope, apath + '/vote', 'POST', {'presented_question_id': question['id'],
                        'round_id': question['round']['id'], 'value': value}, token)

    def first_attempt(self, scope, value, grade):
        self.phase = 'first_attempt_' + scope['label']
        before = self.snapshot_all()
        apath, attempt = self.begin(scope, scope['assignment'], [0, 1])
        self.exam(self.b if scope['label'] == 'A1' else self.a, apath, codes=404)
        first = self.vote(scope, apath, 0, value)
        self.expect(not first['rating_changed'] and first['attempt']['status'] == 'in_progress', 'One vote cannot publish a grade or change rating input')
        self.source_delta(before, self.snapshot_all(), [], self.phase + '_pending')
        self.assert_pointers([], self.phase + '_pending')
        final = self.vote(scope, apath, 1, value)
        self.expect(final['rating_changed'] and final['attempt']['status'] == 'completed', 'Last unanimous vote completes the real owner calculation')
        after = self.snapshot_all()
        self.source_delta(before, after, [scope['label']], self.phase + '_completed')
        selected = self.selected(after, scope, grade, 1, result_id=attempt, version=1)
        self.assert_pointers([scope['label']], self.phase + '_completed')
        scope['first_attempt_path'], scope['first_attempt_id'] = apath, selected['result_id']
        if scope['label'] == 'A2':
            self.expect(scope['first_attempt_id'] != self.a['first_attempt_id'],
                        'Shared student and exam IDs keep distinct completed attempt identities in A1 and A2')
        self.publish(scope, self.phase, grade, 1)

    def retake(self):
        self.phase = 'worse_retake_A1'
        before = self.snapshot_all()
        old = self.selected(before, self.a, 5, 1, result_id=self.a['first_attempt_id'], version=1)
        commission = self.commission(self.a, [0, 2], 'retake')
        assignment = self.exam(self.a, self.path('/retakes/' + str(self.student_id)), 'POST', {
            'commission_id': commission['id'], 'expected_history_generation': self.a['assignment']['history_generation']})['assignment']
        self.fixtures['assignments'].append({'id': assignment['id'], 'division_id': self.a['division_id'], 'attempt_no': 2})
        self.persist()
        apath, attempt = self.begin(self.a, assignment, [0, 2])
        self.vote(self.a, apath, 0, 0)
        pending = self.snapshot_all()
        self.source_delta(before, pending, [], 'unfinished_retake')
        self.expect(self.selected(pending, self.a, 5, 1, result_id=self.a['first_attempt_id'], version=1) == old,
                    'An unfinished retake preserves the entire effective first result row')
        student = self.exam(self.a, self.path('/results/' + str(self.student_id)), token=self.a['students'][0]['token'])['current']
        self.expect(student['effective_grade'] == 5 and student['has_active_retake'] and student['retake_status'] == 'in_progress'
                    and 'result_version' not in student and 'calculated_grade' not in student,
                    'Student safe DTO retains the published first grade during the unfinished retake')
        self.assert_pointers([], 'unfinished_retake')
        final = self.vote(self.a, apath, 2, 0)
        self.expect(final['attempt']['status'] == 'completed' and final['rating_changed'], 'Worse retake is a real completed result')
        after = self.snapshot_all()
        self.source_delta(before, after, ['A1'], 'completed_worse_retake')
        self.selected(after, self.a, 0, 2, result_id=attempt, version=1)
        self.selected(after, self.b, 2, 1, result_id=self.b['first_attempt_id'], version=1)
        self.assert_pointers(['A1'], 'completed_worse_retake')
        self.a['retake_path'], self.a['retake_id'] = apath, attempt
        self.publish(self.a, 'completed-worse-retake', 0, 2)

    def appeals(self):
        self.phase = 'appeals'
        before = self.snapshot_all()
        current = self.exam(self.a, self.path('/results/' + str(self.student_id)))['current']
        self.expect(current['id'] == self.a['retake_id'] and current['effective_grade'] == 0,
                    'Appeal CAS targets the selected current worse retake rather than the best old grade')
        changed = self.exam(self.a, self.a['retake_path'] + '/appeals', 'POST', {
            'expected_result_version': current['result_version'], 'grade': 3})
        self.expect(changed['rating_changed'], 'Appealing the effective attempt declares rating invalidation')
        after = self.snapshot_all()
        self.source_delta(before, after, ['A1'], 'current_attempt_appeal')
        self.selected(after, self.a, 3, 2, True, self.a['retake_id'], current['result_version'] + 1)
        self.assert_pointers(['A1'], 'current_attempt_appeal')
        self.publish(self.a, 'current-attempt-appeal', 3, 2, True)
        before = self.snapshot_all()
        first = self.exam(self.a, self.a['first_attempt_path'])
        changed = self.exam(self.a, self.a['first_attempt_path'] + '/appeals', 'POST', {
            'expected_result_version': first['result_version'], 'grade': 4})
        self.expect(not changed['rating_changed'], 'Correcting historical first-attempt appeal does not replace the current retake')
        after = self.snapshot_all()
        self.source_delta(before, after, [], 'historical_attempt_appeal')
        self.selected(after, self.a, 3, 2, True, self.a['retake_id'], current['result_version'] + 1)
        self.assert_pointers([], 'historical_attempt_appeal')

    def rename_shared(self):
        self.phase = 'shared_name'
        before = self.snapshot_all()
        revision = self.exam(self.a, self.path())['revision']
        self.exam_name += ' renamed'
        self.exam(self.a, self.path('/metadata'), 'PUT', {'expected_revision': revision, 'name': self.exam_name})
        after = self.snapshot_all()
        self.source_delta(before, after, ['A1', 'A2'], 'shared_exam_name')
        self.selected(after, self.a, 3, 2, True, self.a['retake_id'], 2)
        self.selected(after, self.b, 2, 1, False, self.b['first_attempt_id'], 1)
        self.assert_pointers(['A1', 'A2'], 'shared_exam_name')
        self.publish(self.a, 'shared-exam-rename', 3, 2, True)
        self.publish(self.b, 'shared-exam-rename', 2, 1)
        self.assert_pointers([], 'shared_exam_name_recalculated')

    def finish(self, error=None):
        for token in self.sessions:
            try:
                self.request(http.AUTH, '/v1/auth/logout', token, 'POST', {}, (200, 401), record=False)
            except Exception:
                pass
        self.fixtures['status'] = 'failed' if error else 'passed'
        self.fixtures['finished_at'] = datetime.now(timezone.utc).isoformat()
        self.persist()
        report = {'status': self.fixtures['status'], 'tenant_run_id': self.run, 'lifecycle_run_id': self.nonce,
                  'http_checks': len(self.checks), 'scenarios': self.checks, 'phase': self.phase,
                  'exam_id': self.exam_id, 'job_progress': self.progress,
                  'job_ids': [j['id'] for j in self.fixtures['jobs']], 'scopes': self.fixtures.get('scopes', []),
                  'event_expectations': self.fixtures['event_expectations'],
                  'fixture_manifest': str(self.fixture_path.relative_to(http.ROOT)),
                  'started_at': self.fixtures['started_at'], 'finished_at': self.fixtures['finished_at'],
                  'database_accessed': False, 'production_accessed': False, 'rabbit_administration_used': False,
                  'services_restarted': False, 'fixtures_retained': True,
                  'transport_evidence': 'HTTP owner counters, scoped snapshot hashes, publication freshness and durable calculate diagnostics; raw source-event inbox delivery is verified separately by root.'}
        if error:
            report['failure'] = str(error) if isinstance(error, AssertionError) else type(error).__name__
        http.private_json(self.report_path, report)
        print(json.dumps({'status': report['status'], 'http_checks': report['http_checks'],
                          'tenant_run_id': self.run, 'lifecycle_run_id': self.nonce, 'fixtures_retained': True}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--allow-source-mutations', action='store_true',
                        help='Create and retain this run\'s synthetic shared exam/examiners and A1/A2 publications.')
    parser.add_argument('--fixtures', type=Path, help='Exact private manifest matching the latest successful tenant report.')
    args = parser.parse_args()
    if not args.allow_source_mutations:
        parser.error('Run with --allow-source-mutations only after tenant acceptance and source owner readiness.')
    suite, error = None, None
    try:
        suite = Suite(args.fixtures)
        suite.setup()
        suite.create_examiners()
        suite.create_exam()
        suite.first_attempt(suite.a, 1, 5)
        suite.first_attempt(suite.b, .5, 2)
        suite.retake()
        suite.appeals()
        suite.rename_shared()
    except Exception as failure:
        error = failure
    finally:
        if suite is not None:
            suite.finish(error)
    if error:
        raise SystemExit('Ratings exam lifecycle acceptance failed; inspect its private local report.')


if __name__ == '__main__':
    main()
