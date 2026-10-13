"""API-only regression of official grading and regrading on fixed local dev.

Run with --run after the coordinated dev READY signal. --self-check uses only
the unmodified legacy Python scorer and local source inspection, without HTTP.
Fixtures are retained and their IDs are recorded. Mongo is read-only and every
query is restricted to this run's API-created test IDs. No SQL, container control
or imported test main/cleanup functions are used. The organization setting is
restored through Core in finally and all newly opened auth sessions are closed.
An existing Tests-owned direction is reused; Core directions are never mutated.
"""
import argparse
import copy
from datetime import datetime, timezone
import json
from pathlib import Path
import secrets
import time
import traceback
from urllib.parse import urlencode

from bson import ObjectId

import grading_test as legacy
import http_test as base
import workflow_test as flow


ROOT = base.ROOT
ORG = 1001
REPORT = ROOT / 'dev/local/tests-grading-regression-http-report.json'
DIRECTION_REPORT = ROOT / 'dev/local/external-tests-integration-summary.json'
FIELDS = ('score', 'earned_points', 'max_points')


def utc_now():
    return datetime.now(timezone.utc).isoformat()


def oracle(questions, answers, original_ids=None, excluded=()):
    allowed = set(original_ids) if original_ids is not None else {q['question_id'] for q in questions}
    effective = [q for q in questions if q['question_id'] in allowed and q['question_id'] not in excluded]
    ids = {q['question_id'] for q in effective}
    return legacy.legacy_grade(effective, [a for a in answers if a['question_id'] in ids])


def equal_grade(actual, expected):
    return (all(actual.get(k) == expected[k] for k in FIELDS)
            and sorted(actual.get('question_results', []), key=lambda q: q['question_id'])
            == sorted(expected['question_results'], key=lambda q: q['question_id']))


def fixtures():
    return [
        ('all-correct', [{'question_id': 1, 'value': 'a'}, {'question_id': 2, 'value': ['y', 'x']}, {'question_id': 3, 'value': '  мОсКвА  '}], 100),
        ('multiple-subset', [{'question_id': 1, 'value': 'a'}, {'question_id': 2, 'value': ['x']}, {'question_id': 3, 'value': 'wrong'}], 20),
        ('multiple-superset', [{'question_id': 1, 'value': 'a'}, {'question_id': 2, 'value': ['x', 'y', 'z']}], 20),
        ('missing-answers', [{'question_id': 3, 'value': ' Moscow '}], 50),
        ('empty-zero', [], 0),
    ]


def self_check():
    qs = flow.questions()
    for label, answers, score in fixtures():
        assert oracle(qs, answers)['score'] == score, label
    decimal = copy.deepcopy(qs[:2])
    decimal[0]['points'], decimal[1]['points'] = 399, 19601
    assert oracle(decimal, [{'question_id': 1, 'value': 'a'}])['score'] == 1
    removed = copy.deepcopy(qs)
    removed[1]['answers'] = [a for a in removed[1]['answers'] if a['id'] != 'y']
    assert oracle(removed, [{'question_id': 2, 'value': ['x', 'y']}])['earned_points'] == 0
    assert oracle(qs, [], {1, 2, 3}, {1, 2, 3}) == {
        'score': 0, 'earned_points': 0, 'max_points': 0, 'question_results': []}
    assert equal_grade(oracle(qs, []), oracle(qs, []))
    print(json.dumps({'status': 'passed', 'mode': 'self-check', 'http_requests': 0,
                      'legacy_oracle': 'cpm-back-main/cpm_back/services/exam/scoring.py'}))


class Regression:
    def __init__(self):
        self.test_ids = set()
        self.direction_ids = []
        self.submissions = []
        self.checks = []
        self.regrades = []
        self.setting_changes = []
        self.sessions = []
        self.original_setting = None
        self.expected_setting_revision = None
        self.last_owned_setting_revision = None
        self.settings_restore_blocked = False
        self.settings_restored = False
        self.admin = None
        self.direction = None
        self.mongo = None
        self.started = utc_now()
        self.stage = 'initializing'
        self.prefix = 'DEV grading regression ' + secrets.token_hex(6)

    def check(self, value, label):
        if not value:
            raise AssertionError(label)
        self.checks.append(label)

    def report(self, status, failure=None):
        value = {'status': status, 'started_at': self.started, 'finished_at': utc_now(),
                 'stage': self.stage, 'target': base.TESTS, 'organization_id': ORG, 'fixture_prefix': self.prefix,
                 'http_checks': len(base.checks), 'checks': len(self.checks), 'scenarios': self.checks,
                 'test_ids': sorted(self.test_ids), 'direction_ids_created': self.direction_ids,
                 'reused_direction_id': self.direction, 'direction_fixture_source': str(DIRECTION_REPORT),
                 'submissions': self.submissions, 'regrades': self.regrades,
                 'original_organization_setting': self.original_setting,
                 'organization_setting_changes': self.setting_changes,
                 'organization_setting_restored': self.settings_restored,
                 'last_owned_setting_revision': self.last_owned_setting_revision,
                 'settings_restore_blocked_by_foreign_revision': self.settings_restore_blocked,
                 'fixtures_retained': True, 'mongo_read_only': True,
                 'direct_sql_used': False, 'containers_changed': False,
                 'production_accessed': False, 'auth_sessions_opened': len(self.sessions),
                 'auth_sessions_closed': sum(session['closed'] for session in self.sessions),
                 'legacy_oracle': 'cpm-back-main/cpm_back/services/exam/scoring.py'}
        if failure is not None:
            value['failure_type'] = type(failure).__name__
            value['failure_location'] = [{'file': Path(frame.filename).name, 'line': frame.lineno, 'function': frame.name}
                                         for frame in traceback.extract_tb(failure.__traceback__)]
            if isinstance(failure, AssertionError):
                value['failure_check'] = str(failure)
        REPORT.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')
        REPORT.chmod(0o600)
        return value

    def login(self, name, division=1001, organization=ORG):
        token = base.login(name, division, organization)
        self.sessions.append({'token': token, 'closed': False})
        return token

    def setting(self, enabled, restoring=False):
        current = base.core('/organization/test-settings', self.admin)
        if restoring and current['recalculate_on_test_change'] == enabled:
            self.settings_restored = True
            return current
        expected = self.last_owned_setting_revision if restoring else self.expected_setting_revision
        if expected is None or current['revision'] != expected:
            if restoring:
                self.settings_restore_blocked = True
            raise AssertionError('organization setting changed outside this run; foreign revision is not overwritten')
        if current['recalculate_on_test_change'] != enabled:
            current = base.core('/organization/test-settings', self.admin, 'PUT', {
                'expected_revision': expected, 'recalculate_on_test_change': enabled})
            self.expected_setting_revision = current['revision']
            self.last_owned_setting_revision = current['revision']
            change = {'changed_at': utc_now(), 'enabled': enabled, 'revision': current['revision'], 'restoring': restoring}
            self.setting_changes.append(change)
            print(json.dumps({'organization_setting_change': change, 'organization_id': ORG}), flush=True)
        self.check(current['recalculate_on_test_change'] == enabled, 'organization setting confirmed through Core API')
        return current

    def create(self, label, questions=None, division=1001):
        doc = base.api(f'/divisions/{division}/tests', self.admin, 'POST', {
            'title': self.prefix + ' ' + label, 'description': 'API-only official grading regression',
            'direction_id': self.direction, 'questions': copy.deepcopy(questions if questions is not None else flow.questions()),
        }, 201, base.key())
        self.test_ids.add(doc['id'])
        self.report('running')
        return self.publish(doc, division)

    def reuse_direction(self):
        previous = json.loads(DIRECTION_REPORT.read_text())
        assert previous['status'] == 'passed' and previous['fixtures']['directions'], 'Tests-owned direction fixture source unavailable'
        # The owner integration report creates the org1001 direction first and
        # the foreign organization direction second. Recheck the scope live.
        direction = previous['fixtures']['directions'][0]
        refs = flow.internal('/internal/core/test-references', 'POST', {
            'organization_id': ORG, 'division_ids': [1001, 1002], 'direction_id': direction}, origin=base.CORE)['data']
        self.check(refs['organization']['id'] == ORG and refs['direction']['id'] == direction
                   and refs['direction']['is_active'] and all(v['is_active'] for v in refs['divisions']),
                   'existing Tests-owned direction and selected divisions remain active in current organization')
        self.direction = direction

    def publish(self, doc, division=1001):
        return base.api(f'/divisions/{division}/tests/{doc["id"]}/settings', self.admin, 'PUT',
                        base.settings_body(doc['revision'], answers=True))

    def record(self, test_id, submission_id):
        assert test_id in self.test_ids
        query = {'organization_id': ORG, 'test_id': ObjectId(test_id), 'submission_id': ObjectId(submission_id)}
        value = self.mongo.cpm_dev.tests_attempts_v1.find_one(query)
        self.check(value is not None and value['organization_id'] == ORG and str(value['test_id']) == test_id,
                   'Mongo read stays inside this run owned test/submission scope')
        return value

    def verify_db(self, fixture, expected):
        doc = self.record(fixture['test_id'], fixture['submission_id'])
        self.check(doc['status'] == 'accepted' and doc['active'] and doc['calculation_status'] == 'ready',
                   'physical submission remains active accepted and ready')
        self.check(legacy.canonical_hash(legacy.immutable(doc)) == fixture['frozen'],
                   'original answers commits version organization division account and deadlines remain immutable')
        self.check(str(doc['_id']) == fixture['attempt_id'] and doc['division_id'] == fixture['division_id'],
                   'same physical attempt and concrete division survive calculation')
        self.check(equal_grade(doc['result'], expected), 'Mongo persisted grade exactly matches old Python scoring')
        query = {'organization_id': ORG, 'test_id': ObjectId(fixture['test_id']), 'submission_id': ObjectId(fixture['submission_id'])}
        self.check(self.mongo.cpm_dev.tests_attempts_v1.count_documents(query) == 1,
                   'calculation overwrites exactly one physical submission')
        job = self.mongo.cpm_dev.tests_calculation_jobs_v1.find_one({
            '_id': doc['calculation_job_id'], 'organization_id': ORG, 'test_id': ObjectId(fixture['test_id'])})
        self.check(job is not None and job['submission_id'] == doc['submission_id']
                   and job['division_id'] == doc['division_id'] and job['account_id'] == doc['account_id']
                   and job['attempt_test_version_id'] == doc['test_version_id']
                   and job['calculation_revision'] == doc['calculation_revision'],
                   'immutable job retains original version and selected student/division identity')
        self.check(job['result'] == doc['result'] and job['result_hash'] == doc['result_hash'],
                   'selected job result and attempt result agree')
        history = doc['calculation_history']
        self.check(len({v['calculation_revision'] for v in history}) == len(history)
                   and history[-1]['calculation_revision'] == doc['calculation_revision']
                   and history[-1]['score'] == doc['result']['score'],
                   'one audit entry per confirmed calculation revision')
        self.check(len(doc['calculation_job_ids']) == len(set(doc['calculation_job_ids'])),
                   'confirmed job references contain no duplicates')
        return doc

    def wait_ready(self, fixture, expected, previous=0):
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            value = base.api('/submissions/' + fixture['submission_id'], fixture['token'])
            if value['calculation_status'] == 'failed':
                raise AssertionError('official calculator reports failed for own submission')
            if value['calculation_status'] == 'ready' and value['calculation_revision'] > previous:
                self.check(all(value['result'].get(k) == expected[k] for k in FIELDS),
                           'official status API aggregate grade matches unmodified old Python scorer')
                self.check(value['result'].get('question_results') is None,
                           'public submission status does not expose per-question grading')
                review = base.api('/submissions/' + fixture['submission_id'] + '/review', fixture['token'])
                self.check(sorted(review['question_results'], key=lambda q: q['question_id'])
                           == sorted(expected['question_results'], key=lambda q: q['question_id']),
                           'authorized review exposes the exact effective per-question grading')
                self.check({q['question_id'] for q in review['questions']}
                           == {q['question_id'] for q in expected['question_results']},
                           'review includes only historical effective grading questions')
                return self.verify_db(fixture, expected)
            time.sleep(.15)
        raise AssertionError('official calculation timed out waiting for ready revision')

    def start(self, token, doc, answers, division=1001, retries=False):
        attempt = base.api(f'/divisions/{division}/tests/{doc["id"]}/attempts', token, 'POST', {'practice': False}, 201)
        self.check(attempt['organization_id'] == ORG and attempt['division_id'] == division and attempt['test_id'] == doc['id'],
                   'official start fixes organization test and concrete division')
        self.check(set(attempt['question_order']) == {q['question_id'] for q in attempt['questions']}
                   and len(attempt['question_order']) == len(attempt['questions']),
                   'randomized order contains each question once')
        self.check('is_correct' not in json.dumps(attempt['questions']) and 'correct_answers' not in json.dumps(attempt['questions']),
                   'official question DTO contains no correct-answer keys')
        resumed = base.api(f'/divisions/{division}/tests/{doc["id"]}/attempts', token, 'POST', {'practice': False})
        self.check(resumed['id'] == attempt['id'] and resumed['question_order'] == attempt['question_order']
                   and resumed['answer_deadline'] == attempt['answer_deadline'] and resumed['test_version_id'] == attempt['test_version_id'],
                   'resume preserves attempt identity random order deadline and version')
        commits = [{'commit_id': secrets.token_hex(16), 'sequence': i + 1, 'question_id': a['question_id'],
                    'value': copy.deepcopy(a['value']), 'answered_at': utc_now()} for i, a in enumerate(answers)]
        if commits:
            path = '/attempts/' + attempt['id'] + '/commits'
            ack = base.api(path, token, 'POST', {'commits': commits})
            replay = base.api(path, token, 'POST', {'commits': commits})
            self.check(ack == replay and ack['acknowledged_commit_ids'] == [v['commit_id'] for v in commits],
                       'offline commit batch replay preserves acknowledgment and revision')
            if retries:
                altered = copy.deepcopy(commits[0])
                altered['value'] = 'b' if altered['value'] == 'a' else 'a'
                base.api(path, token, 'POST', {'commits': [altered]}, 409)
                collision = {**commits[0], 'commit_id': secrets.token_hex(16)}
                base.api(path, token, 'POST', {'commits': [collision]}, 409)
                invalid_time = {**commits[0], 'commit_id': secrets.token_hex(16), 'sequence': len(commits) + 1,
                                'answered_at': '2099-01-01T00:00:00Z'}
                base.api(path, token, 'POST', {'commits': [invalid_time]}, 400)
                retained = base.api('/attempts/' + attempt['id'], token)
                self.check(retained['revision'] == ack['revision'] and len(retained['commits']) == len(commits),
                           'rejected offline retries cannot partly write commits or advance revision')
                self.check(True, 'changed offline commit ID payload and reused sequence are rejected')
        return attempt

    def finalize(self, token, doc, attempt, answers, label, division=1001):
        body = flow.snapshot(attempt, copy.deepcopy(answers))
        accepted = base.api('/attempts/' + attempt['id'] + '/finalize', token, 'POST', body, 202)
        self.check(accepted['submission_status'] == 'accepted' and accepted['calculation_status'] == 'queued'
                   and accepted['result'] is None and accepted['division_id'] == division,
                   'finalize returns 202 queued with null result without waiting for grading')
        fixture = {'label': label, 'test_id': doc['id'], 'attempt_id': attempt['id'], 'submission_id': accepted['submission_id'],
                   'division_id': division, 'token': token, 'body': body}
        frozen = self.record(doc['id'], fixture['submission_id'])
        fixture['frozen'] = legacy.canonical_hash(legacy.immutable(frozen))
        self.submissions.append({k: fixture[k] for k in ['label', 'test_id', 'attempt_id', 'submission_id', 'division_id']})
        self.report('running')
        replay = base.api('/attempts/' + attempt['id'] + '/finalize', token, 'POST', body, 202)
        self.check(replay['submission_id'] == fixture['submission_id'], 'final snapshot replay returns the same submission identity')
        if label == 'all-correct':
            conflicting = copy.deepcopy(body)
            conflicting['answers'][0]['value'] = 'b'
            base.api('/attempts/' + attempt['id'] + '/finalize', token, 'POST', conflicting, 409)
            self.check(self.record(doc['id'], fixture['submission_id'])['snapshot_hash'] == frozen['snapshot_hash'],
                       'changed final snapshot cannot replace accepted answers')
        return fixture

    def grade(self, token, doc, questions, answers, label, division=1001, retries=False):
        attempt = self.start(token, doc, answers, division, retries)
        fixture = self.finalize(token, doc, attempt, answers, label, division)
        expected = oracle(questions, answers)
        self.wait_ready(fixture, expected)
        return fixture, expected

    def callback(self, fixture):
        doc = self.record(fixture['test_id'], fixture['submission_id'])
        pinned = flow.internal('/internal/tests/submissions/' + fixture['submission_id'] + '/input')['data']
        return {**doc['result'], 'calculation_revision': pinned['calculation_revision'], 'input_hash': pinned['input_hash'],
                'grading_test_version_id': pinned['grading_test_version_id'], 'excluded_question_ids': pinned['excluded_question_ids']}

    def verify_callback(self, fixture):
        body = self.callback(fixture)
        path = '/internal/tests/submissions/' + fixture['submission_id'] + '/result'
        before = self.record(fixture['test_id'], fixture['submission_id'])
        flow.internal(path, 'POST', body, authorized=False, code=401)
        flow.internal(path, 'POST', body)
        flow.internal(path, 'POST', body)
        after = self.record(fixture['test_id'], fixture['submission_id'])
        self.check(before['revision'] == after['revision'] and before['calculation_history'] == after['calculation_history'],
                   'identical result callbacks neither advance revision nor duplicate audit')
        flow.internal(path, 'POST', {**body, 'input_hash': '0' * 64}, code=409)
        flow.internal(path, 'POST', {**body, 'grading_test_version_id': '0' * 24}, code=409)
        changed = copy.deepcopy(body)
        changed['score'], changed['earned_points'] = 0, 0
        for item in changed['question_results']:
            item['points'], item['is_correct'] = 0, False
        if body['earned_points'] != 0:
            flow.internal(path, 'POST', changed, code=409)
        self.check(self.record(fixture['test_id'], fixture['submission_id'])['result'] == before['result'],
                   'conflicting callback identity or output cannot overwrite ready result')
        return body

    def edit(self, test_id, mutation):
        assert test_id in self.test_ids
        current = base.api('/' + test_id + '/content', self.admin)
        body = {k: copy.deepcopy(current[k]) for k in ['title', 'description', 'direction_id', 'questions']}
        mutation(body)
        return base.api('/' + test_id + '/content', self.admin, 'PUT', {'expected_revision': current['revision'], **body})

    def wait_content(self, test_id, version_id):
        assert test_id in self.test_ids
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            offset = 0
            while True:
                page = flow.internal('/internal/calculate/messages?' + urlencode({'test_id': test_id, 'limit': 100, 'offset': offset}), origin=flow.CALCULATE)['data']
                for item in page['items']:
                    if item['message_type'] != 'tests.content.changed':
                        continue
                    detail = flow.internal('/internal/calculate/messages/' + item['message_id'], origin=flow.CALCULATE)['data']
                    if detail['payload']['after_version_id'] != version_id:
                        continue
                    if detail['status'] == 'failed':
                        raise AssertionError('own content change failed in Calculate')
                    if detail['status'] in ('ready', 'skipped'):
                        self.check(True, 'own editor change was delivered through Rabbit and processed by Calculate')
                        return detail
                offset += len(page['items'])
                if not page['items'] or offset >= page['total']:
                    break
            time.sleep(.15)
        raise AssertionError('content event timed out waiting for Calculate decision')

    def change(self, label, doc, active_fixtures, mutation, expected, regrade=True):
        previous = [self.record(f['test_id'], f['submission_id']) for f in active_fixtures]
        updated = self.edit(doc['id'], mutation)
        message = self.wait_content(doc['id'], updated['content_version_id'])
        if regrade:
            self.check(message['status'] == 'ready', label + ': scoring change produces regrade jobs')
            for fixture, old in zip(active_fixtures, previous):
                after = self.wait_ready(fixture, expected, old['calculation_revision'])
                self.check(after['calculation_revision'] == old['calculation_revision'] + 1
                           and after['grading_test_version_id'] == ObjectId(updated['content_version_id']),
                           label + ': one new revision selects the committed edited version')
        else:
            self.check(message['status'] == 'skipped', label + ': non-regrade change is explicitly skipped')
            for fixture, old in zip(active_fixtures, previous):
                after = self.verify_db(fixture, expected)
                self.check(after['calculation_revision'] == old['calculation_revision'] and after['result'] == old['result']
                           and after['calculation_history'] == old['calculation_history'],
                           label + ': skipped edit preserves result and audit exactly')
        self.regrades.append({'label': label, 'test_id': doc['id'], 'version_id': updated['content_version_id'],
                              'message_id': message['message_id'], 'status': message['status'],
                              'reason': message.get('last_error', ''), 'expected_score': expected['score']})
        self.report('running')
        # ContentView names the identifier test_id; retain the catalog identity
        # for subsequent independent editor saves and attempt starts.
        return {**updated, 'id': doc['id']}

    def run(self):
        self.stage = 'ready and authorization'
        base.request('/health/ready')
        base.request('/health/ready', origin=base.CORE)
        base.request('/health/ready', origin=flow.CALCULATE)
        self.admin = self.login('dev_admin')
        student = self.login('dev_student_01')
        student_second_division = self.login('dev_student_01', 1002)
        other_student = self.login('dev_student_02')
        foreign = self.login('dev_admin_math', 2001, 2001)
        self.original_setting = base.core('/organization/test-settings', self.admin)
        self.check(self.original_setting['organization_id'] == ORG, 'organization setting belongs to selected dev organization')
        self.expected_setting_revision = self.original_setting['revision']
        foreign_setting = base.core('/organization/test-settings', foreign)
        self.report('running')
        print(json.dumps({'settings_window_started_at': utc_now(), 'organization_id': ORG}), flush=True)
        self.setting(True)
        base.core('/organization/test-settings', student, 'PUT', {
            'expected_revision': self.original_setting['revision'], 'recalculate_on_test_change': False}, 403)
        self.check(base.core('/organization/test-settings', foreign) == foreign_setting,
                   'organization setting changes leave foreign organization unchanged')
        self.reuse_direction()
        self.mongo = base.mongo_client(base.settings())
        self.stage = 'official baseline'
        qs = flow.questions()
        for label, answers, score in fixtures():
            fixture, expected = self.grade(student, self.create(label), qs, answers, label, retries=label == 'all-correct')
            self.check(expected['score'] == score, label + ': explicit legacy boundary verified')
        decimal = copy.deepcopy(qs[:2])
        decimal[0]['points'], decimal[1]['points'] = 399, 19601
        self.grade(student, self.create('decimal-rounding', decimal), decimal, [{'question_id': 1, 'value': 'a'}], 'decimal-rounding')
        unicode_qs = [{'question_id': 1, 'type': 'text', 'text': 'Unicode', 'points': 2, 'answers': [], 'correct_answers': ['İSTANBUL', 'ΟΣ']}]
        self.grade(student, self.create('unicode-text', unicode_qs), unicode_qs, [{'question_id': 1, 'value': '\u001cİSTANBUL\u001f'}], 'unicode-text')

        self.stage = 'same test in two divisions and independent copy'
        source = self.create('shared-regrade')
        source = base.api('/divisions/1002/tests/' + source['id'] + '/attach', self.admin, 'POST', {'expected_revision': source['revision']})
        source = self.publish(source, 1002)
        independent = base.api('/divisions/1002/tests/' + source['id'] + '/copy', self.admin, 'POST', {
            'expected_revision': source['revision'], 'title': self.prefix + ' independent copy'}, 201, base.key())
        self.test_ids.add(independent['id'])
        independent = self.publish(independent, 1002)
        answers = [{'question_id': 1, 'value': 'b'}, {'question_id': 2, 'value': ['x', 'y']}, {'question_id': 3, 'value': 'Moscow'}]
        first, expected = self.grade(student, source, qs, answers, 'shared division 1001')
        second, _ = self.grade(student_second_division, source, qs, answers, 'shared division 1002', 1002)
        copied, copy_expected = self.grade(student_second_division, independent, qs, answers, 'independent copy', 1002)
        self.check(first['submission_id'] != second['submission_id'] and first['attempt_id'] != second['attempt_id'],
                   'same test and account in two divisions produces distinct physical attempts and submissions')
        base.api('/submissions/' + first['submission_id'], student_second_division, code=403)
        base.api('/submissions/' + first['submission_id'], foreign, code=404)
        old_callback = self.verify_callback(first)
        group = [first, second]
        self.stage = 'non-triggering additions'
        source = self.change('text edit', source, group, lambda b: b['questions'][0].update(text='Уточнённый текст'), expected, False)
        added = {'question_id': 4, 'type': 'text', 'text': 'Новый вопрос', 'points': 100, 'answers': [], 'correct_answers': ['новый']}
        source = self.change('new question addition', source, group, lambda b: b['questions'].append(copy.deepcopy(added)), expected, False)
        extra = {'id': 'new_correct', 'text': 'Добавленный верный вариант', 'is_correct': True}
        source = self.change('new correct option addition', source, group, lambda b: b['questions'][1]['answers'].append(copy.deepcopy(extra)), expected, False)
        current = copy.deepcopy(qs)
        current[1]['answers'].append(extra)
        current[0]['points'] = 4
        expected = oracle(current, answers)
        self.stage = 'regrade scoring changes'
        source = self.change('weight change with historical additions excluded', source, group,
                             lambda b: b['questions'][0].update(points=4), expected)
        self.check(expected['max_points'] == 12, 'added question does not inflate historical maximum')
        flow.internal('/internal/tests/submissions/' + first['submission_id'] + '/result', 'POST', old_callback, code=409)
        flow.internal('/internal/tests/submissions/' + first['submission_id'] + '/input?calculation_revision=1', code=409)
        self.verify_db(copied, copy_expected)
        self.verify_callback(first)
        current[1]['answers'] = [a for a in current[1]['answers'] if a['id'] != 'new_correct']
        expected = oracle(current, answers)
        source = self.change('option removal', source, group, lambda b: b['questions'][1].update(answers=copy.deepcopy(current[1]['answers'])), expected)
        current[0]['answers'][0]['is_correct'], current[0]['answers'][1]['is_correct'] = False, True
        expected = oracle(current, answers)
        source = self.change('correct flag change', source, group, lambda b: b['questions'][0].update(answers=copy.deepcopy(current[0]['answers'])), expected)
        current[1]['answers'] = [a for a in current[1]['answers'] if a['id'] != 'y']
        expected = oracle(current, answers)
        source = self.change('selected option removal', source, group, lambda b: b['questions'][1].update(answers=copy.deepcopy(current[1]['answers'])), expected)
        current[2]['correct_answers'] = ['Москва']
        expected = oracle(current, answers)
        source = self.change('text answer alias removal', source, group, lambda b: b['questions'][2].update(correct_answers=['Москва']), expected)
        expected = oracle(current, answers, {1, 2, 3}, {1})
        source = self.change('type change permanently excludes question', source, group,
                             lambda b: b['questions'][0].update(type='text', answers=[], correct_answers=['b']), expected)
        restored = [{**a, 'id': 'restored_' + a['id']} for a in current[0]['answers']]
        source = self.change('restored type cannot revive historical question', source, group,
                             lambda b: b['questions'][0].update(type='single', answers=restored, correct_answers=[]), expected)
        expected = oracle(current, answers, {1, 2, 3}, {1, 3})
        source = self.change('question removal', source, group,
                             lambda b: b.update(questions=[q for q in b['questions'] if q['question_id'] != 3]), expected)
        expected = oracle(current, answers, {1, 2, 3}, {1, 2, 3})
        source = self.change('empty effective historical test scores zero', source, group,
                             lambda b: b.update(questions=[q for q in b['questions'] if q['question_id'] == 4]), expected)
        self.verify_db(copied, copy_expected)
        self.check(self.record(copied['test_id'], copied['submission_id'])['calculation_revision'] == 1,
                   'independent physical copy receives none of source regrades')

        self.stage = 'organization setting false and true'
        disabled = self.create('organization-setting')
        old, old_expected = self.grade(student, disabled, qs, answers, 'setting before disabled')
        self.setting(False)
        changed_qs = copy.deepcopy(qs)
        changed_qs[0]['answers'][0]['is_correct'], changed_qs[0]['answers'][1]['is_correct'] = False, True
        disabled = self.change('organization disabled regrade', disabled, [old],
                               lambda b: b['questions'][0].update(answers=copy.deepcopy(changed_qs[0]['answers'])), old_expected, False)
        new, _ = self.grade(other_student, disabled, changed_qs, answers, 'initial grading while setting disabled')
        before_enabled = self.record(old['test_id'], old['submission_id'])
        self.setting(True)
        self.check(self.record(old['test_id'], old['submission_id'])['calculation_revision'] == before_enabled['calculation_revision'],
                   'enabling setting alone does not retroactively create a regrade')
        changed_qs[0]['points'] = 4
        expected = oracle(changed_qs, answers)
        disabled = self.change('new edit after re-enabling', disabled, [old, new],
                               lambda b: b['questions'][0].update(points=4), expected)
        self.verify_callback(old)
        self.check(base.core('/organization/test-settings', foreign) == foreign_setting,
                   'foreign organization settings remain unchanged after full regression')
        self.stage = 'completed'

    def finish(self):
        failures = []
        try:
            if self.admin is not None and self.original_setting is not None:
                self.setting(self.original_setting['recalculate_on_test_change'], restoring=True)
                current = base.core('/organization/test-settings', self.admin)
                self.settings_restored = current['recalculate_on_test_change'] == self.original_setting['recalculate_on_test_change']
                print(json.dumps({'settings_window_finished_at': utc_now(), 'organization_id': ORG,
                                  'original_flag_restored': self.settings_restored}), flush=True)
        except Exception as failure:
            failures.append(failure)
        finally:
            if self.mongo is not None:
                self.mongo.close()
            for session in self.sessions:
                try:
                    base.request('/v1/auth/logout', session['token'], 'POST', origin=base.AUTH)
                    session['closed'] = True
                except Exception as failure:
                    failures.append(failure)
        if failures:
            raise failures[0]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument('--self-check', action='store_true')
    mode.add_argument('--run', action='store_true')
    args = parser.parse_args()
    if args.self_check:
        self_check()
        return
    if REPORT.exists():
        previous = json.loads(REPORT.read_text())
        prior_run = previous.get('fixture_prefix', '').split()[-1:]
        run_id = prior_run[0] if prior_run and all(c in '0123456789abcdef' for c in prior_run[0]) else secrets.token_hex(6)
        preserved = REPORT.with_name(REPORT.stem + '-' + run_id + '-' + datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%S%f') + '.json')
        preserved.write_bytes(REPORT.read_bytes())
        preserved.chmod(0o600)
        print(json.dumps({'prior_report_preserved': str(preserved)}), flush=True)
    regression = Regression()
    failure = None
    try:
        regression.run()
    except Exception as error:
        failure = error
    finally:
        try:
            regression.finish()
        except Exception as error:
            if failure is None:
                failure = error
    report = regression.report('failed' if failure else 'passed', failure)
    print(json.dumps({k: report[k] for k in ['status', 'stage', 'http_checks', 'checks',
        'organization_setting_restored', 'auth_sessions_closed', 'fixtures_retained']}, ensure_ascii=False))
    if failure is not None:
        raise SystemExit(1)


if __name__ == '__main__':
    main()
