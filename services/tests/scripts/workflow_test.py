"""Exercise the local tests API end to end with synthetic, API-created fixtures.

The normal run cleans its fixtures. --demo retains two tests and a graded
submission for inspection. --skip-faults skips Docker stop/start scenarios.
"""
import argparse
from concurrent.futures import ThreadPoolExecutor
import copy
import hashlib
from datetime import datetime, timedelta, timezone
import json
from pathlib import Path
import subprocess
import time
import urllib.error
import urllib.request

import http_test as base

ROOT = base.ROOT
CALCULATE = 'http://127.0.0.1:8005'
DOCKER = '/Applications/Docker.app/Contents/Resources/bin/docker'
attempt_ids, draft_ids = set(), set()


def now():
    return datetime.now(timezone.utc).isoformat()


def client_hash(body):
    return hashlib.sha256(json.dumps(body, sort_keys=True, separators=(',', ':'), ensure_ascii=False).encode()).hexdigest()


def race_request(path, token, body, method='POST', service=False):
    headers = {'Content-Type': 'application/json'}
    if service:
        headers['X-Service-Key'] = base.read_env(ROOT / 'dev/.env')['AUTH_INTERNAL_KEY']
    else:
        headers['Authorization'] = 'Bearer ' + token
    req = urllib.request.Request(base.TESTS + path, data=json.dumps(body).encode(), headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=40) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as response:
        return response.code, json.load(response)


def questions():
    return [
        {'question_id': 1, 'type': 'single', 'text': 'Выберите первый вариант', 'points': 2,
         'answers': [{'id': 'a', 'text': 'Первый', 'is_correct': True},
                     {'id': 'b', 'text': 'Второй', 'is_correct': False}], 'correct_answers': []},
        {'question_id': 2, 'type': 'multiple', 'text': 'Выберите оба верных варианта', 'points': 3,
         'answers': [{'id': 'x', 'text': 'X', 'is_correct': True},
                     {'id': 'y', 'text': 'Y', 'is_correct': True},
                     {'id': 'z', 'text': 'Z', 'is_correct': False}], 'correct_answers': []},
        {'question_id': 3, 'type': 'text', 'text': 'Столица России', 'points': 5,
         'answers': [], 'correct_answers': ['Москва', 'Moscow']},
    ]


def create(admin, direction, suffix, division=1001):
    return base.keep(base.api(f'/divisions/{division}/tests', admin, 'POST', {
        'title': base.prefix + suffix, 'description': 'Полный тест через новый API',
        'direction_id': direction, 'questions': questions(),
    }, 201, base.key()))


def publish(admin, test, division=1001):
    return base.api(f'/divisions/{division}/tests/{test["id"]}/settings', admin, 'PUT',
                    base.settings_body(test['revision'], answers=True))


def start(student, test, division=1001, practice=False):
    data = base.api(f'/divisions/{division}/tests/{test}/attempts', student, 'POST', {'practice': practice}, 201)
    attempt_ids.add(data['id'])
    return data


def snapshot(attempt, answers=None):
    return {'test_version_id': attempt['test_version_id'], 'completed_at': now(), 'reason': 'manual',
            'answers': answers if answers is not None else [
                {'question_id': 1, 'value': 'b'},
                {'question_id': 2, 'value': ['x', 'y']},
                {'question_id': 3, 'value': ' Москва '},
            ]}


def internal(path, method='GET', body=None, code=200, origin=base.TESTS, authorized=True):
    headers = {'X-Service-Key': base.read_env(ROOT / 'dev/.env')['AUTH_INTERNAL_KEY']} if authorized else {}
    return base.request(path, method=method, body=body, code=code, headers=headers, origin=origin)


def inbox(test_id=None, submission_id=None):
    query = 'test_id=' + test_id if test_id else 'submission_id=' + submission_id
    return internal('/internal/calculate/messages?' + query + '&limit=100', origin=CALCULATE)['data']


def wait_for(check, label, timeout=30):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            value = check()
            if value:
                base.expect(True, label)
                return value
        except (OSError, urllib.error.URLError, AssertionError):
            pass
        time.sleep(.25)
    raise AssertionError(label + ': timed out')


def wait_ready(submission, token, score=None, timeout=40):
    def ready():
        value = base.api('/submissions/' + submission, token)
        return value if value['calculation_status'] == 'ready' and (score is None or value['result']['score'] == score) else None
    return wait_for(ready, 'official calculation becomes ready' + (f' with score {score}' if score is not None else ''), timeout)


def reset(attempt_id, admin):
    for _ in range(5):
        state = base.api('/attempts/' + attempt_id, admin)
        outcome = race_request('/v1/tests/attempts/' + attempt_id, admin, {'expected_revision': state['revision']}, 'DELETE')
        if outcome[0] == 200:
            base.expect(True, 'reset succeeds against the current asynchronous calculation revision')
            return outcome[1]['data']
        base.expect(outcome[0] == 409, 'concurrent calculator result only causes a retriable revision conflict')
    raise AssertionError('reset did not obtain a stable revision')


def container(action, name):
    subprocess.run([DOCKER, action, 'cpm-dev-' + name + '-1'], check=True, capture_output=True, text=True)


def clock_fixture(attempt, expired_upload=False):
    """Shift only this run's API-created attempt to exercise server time bounds."""
    assert attempt['id'] in attempt_ids and attempt['test_id'] in base.ids
    current = datetime.now(timezone.utc)
    answer_deadline = current - (timedelta(hours=25) if expired_upload else timedelta(minutes=1))
    upload_deadline = answer_deadline + timedelta(hours=24)
    client = base.mongo_client(base.settings())
    try:
        result = client.cpm_dev.tests_attempts_v1.update_one({
            '_id': base.ObjectId(attempt['id']), 'test_id': base.ObjectId(attempt['test_id']),
            'organization_id': 1001, 'status': 'in_progress', 'active': True,
        }, {'$set': {'started_at': answer_deadline - timedelta(minutes=1),
                     'answer_deadline': answer_deadline, 'upload_deadline': upload_deadline}})
        assert result.matched_count == 1, 'synthetic clock fixture guard'
    finally:
        client.close()
    return answer_deadline.isoformat()


def cleanup():
    client = base.mongo_client(base.settings())
    try:
        db = client.cpm_dev
        physical = [base.ObjectId(value) for value in base.ids]
        for name in ['tests_content_versions_v1', 'tests_attempts_v1', 'tests_calculation_jobs_v1']:
            db[name].delete_many({'organization_id': 1001, 'test_id': {'$in': physical}})
        db.tests_drafts_v1.delete_many({'organization_id': 1001, '_id': {'$in': [base.ObjectId(value) for value in draft_ids]}})
        db.calculate_inbox_v1.delete_many({'test_id': {'$in': physical + list(base.ids)}})
    finally:
        client.close()
    base.cleanup()


def main(faults=True):
    base.request('/health/ready')
    base.request('/health/ready', origin=CALCULATE)
    admin = base.login('dev_admin')
    student = base.login('dev_student_01')
    student2 = base.login('dev_student_01', 1002)
    other = base.login('dev_student_02')
    foreign = base.login('dev_admin_math', 2001, 2001)
    staff = base.login('dev_staff', 1002)
    role = base.core('/roles', admin, 'POST', {'name': base.prefix + ' results viewer', 'permissions': {
        'tests': {'view': True, 'edit': False}, 'test-results': {'view': True, 'edit': False},
    }}, 201)['id']
    base.roles.append(role)
    viewer_id = base.core('/divisions/1001/users', admin, 'POST', {
        'role': 'staff_admin', 'full_name': 'DEV Workflow ' + base.run,
        'login': 'dev_tests_' + base.run, 'password': base.password, 'role_id': role,
    }, 201)['account_id']
    base.users.append(viewer_id)
    viewer = base.login('dev_tests_' + base.run)
    direction = base.core('/directions', admin, 'POST', {'name': base.prefix + ' workflow'}, 201)['id']
    base.directions.append(direction)
    source = create(admin, direction, ' workflow source')
    tid = source['id']
    content = base.api('/' + tid + '/content', admin)
    base.expect(content['questions'] == questions(), 'all three question types survive API creation')
    base.expect(bool(content['content_version_id']), 'create pins an immutable content version')
    base.api('/' + tid + '/content', student, code=404)
    base.api('/' + tid + '/content', foreign, code=404)

    source = base.api('/' + tid + '/bank', admin, 'PUT', {'expected_revision': source['revision'], 'available': True})
    source = base.api(f'/divisions/1002/tests/{tid}/attach', staff, 'POST', {'expected_revision': source['revision']})
    source = publish(admin, source, 1001)
    source = publish(admin, source, 1002)
    independent = base.keep(base.api(f'/divisions/1002/tests/{tid}/copy', staff, 'POST',
        {'expected_revision': source['revision'], 'title': base.prefix + ' independent'}, 201, base.key()))
    copied = base.api('/' + independent['id'] + '/content', staff)
    base.expect(copied['questions'] == content['questions'] and copied['content_version_id'] != content['content_version_id'],
                'physical copy has full content and an independent version')
    independent = publish(admin, independent, 1002)
    listed1 = base.api('/divisions/1001/tests?q=' + base.run + '&limit=1&offset=0', student)
    listed2 = base.api('/divisions/1002/tests?q=' + base.run + '&limit=1&offset=0', student2)
    page2 = base.api('/divisions/1002/tests?q=' + base.run + '&limit=1&offset=1', student2)
    base.expect(listed1['total'] == 1 and listed2['total'] == 2 and page2['total'] == 2,
                'selected division filters both page items and total count before pagination')
    base.expect(len(listed2['items']) == len(page2['items']) == 1 and listed2['items'][0]['id'] != page2['items'][0]['id'],
                'second division has two disjoint pages')
    base.api('/divisions/1002/tests', other, code=403)

    attempt = start(student, tid)
    aid = attempt['id']
    base.expect(attempt['division_id'] == 1001 and set(attempt['question_order']) == {1, 2, 3},
                'attempt records selected division and a complete randomized question order')
    base.expect('is_correct' not in json.dumps(attempt['questions']) and 'correct_answers' not in json.dumps(attempt['questions']),
                'student question DTO never exposes the answer key')
    resumed = base.api(f'/divisions/1001/tests/{tid}/attempts', student, 'POST', {'practice': False})
    base.expect(resumed['id'] == aid and resumed['question_order'] == attempt['question_order']
                and resumed['answer_deadline'] == attempt['answer_deadline'], 'resume preserves order, version and deadline')
    base.api('/attempts/' + aid, other, code=403)
    base.api('/attempts/' + aid, student2, code=403)
    base.api('/attempts/' + aid + '/answer', student, 'PATCH', {'question_id': 1, 'value': 'unknown'}, 400)
    commit = {'commit_id': base.secrets.token_hex(16), 'sequence': 1, 'question_id': 1, 'value': 'a', 'answered_at': now()}
    commits = base.api('/attempts/' + aid + '/commits', student, 'POST', {'commits': [commit]})
    base.api('/attempts/' + aid + '/commits', student, 'POST', {'commits': [commit]})
    base.api('/attempts/' + aid + '/commits', student, 'POST', {'commits': [{**commit, 'value': 'b'}]}, 409)
    base.api('/attempts/' + aid + '/answer', student, 'PATCH', {'question_id': 1, 'value': 'b'}, 409)
    base.expect(bool(commits), 'commits acknowledged; same-id changed payload and mirrored answer rewrite rejected')
    new_settings = base.settings_body(source['revision'], answers=True)
    new_settings.update(time_limit_minutes=5, passing_score=90)
    source = base.api(f'/divisions/1001/tests/{tid}/settings', admin, 'PUT', new_settings)
    frozen = base.api('/attempts/' + aid, student)
    base.expect(frozen['settings_snapshot'] == attempt['settings_snapshot']
                and frozen['answer_deadline'] == attempt['answer_deadline']
                and frozen['settings_snapshot']['passing_score'] == 50,
                'editing placement settings preserves the started attempt threshold, schedule and deadline')

    edit = {key: content[key] for key in ['title', 'description', 'direction_id', 'questions']}
    edit = copy.deepcopy(edit)
    edit['questions'][0]['points'] = 4
    edit['questions'][1]['text'] += ' (уточнение)'
    updated = base.api('/' + tid + '/content', admin, 'PUT', {'expected_revision': source['revision'], **edit})
    history = base.api('/' + tid + '/history?limit=1&offset=0', admin)
    batch = history['items'][0]['change_batch']
    base.expect(batch and len(batch['changes']) >= 2 and batch['before']['questions'][0]['points'] == 2
                and batch['after']['questions'][0]['points'] == 4, 'one save records a complete before/after change batch')
    base.expect(base.api('/attempts/' + aid, student)['test_version_id'] == attempt['test_version_id'],
                'editing current test never changes the running attempt version')
    no_op = base.api('/' + tid + '/content', admin, 'PUT', {'expected_revision': updated['revision'], **edit})
    base.expect(no_op['revision'] == updated['revision'], 'no-op content save creates no revision or logical event')
    base.api('/' + tid + '/content', admin, 'PUT', {'expected_revision': source['revision'], **edit}, 409)
    base.expect(base.api('/' + independent['id'] + '/content', staff)['questions'][0]['points'] == 2,
                'content edits leave independent copy unchanged')

    final = snapshot(attempt)
    client_time = datetime.now(timezone.utc)
    client_time += timedelta(microseconds=100000 - client_time.microsecond % 100000)
    time.sleep(.1)
    final['completed_at'] = client_time.isoformat(timespec='milliseconds').replace('+00:00', 'Z')
    base.api('/attempts/' + aid + '/finalize', student, 'POST', {**final, 'test_version_id': '0' * 24}, 409)
    base.api('/attempts/' + aid + '/finalize', student, 'POST', {**final, 'snapshot_hash': '0' * 64}, 400)
    final['snapshot_hash'] = client_hash(final)
    with ThreadPoolExecutor(max_workers=2) as pool:
        same = list(pool.map(lambda _: race_request('/v1/tests/attempts/' + aid + '/finalize', student, final), range(2)))
    base.expect([item[0] for item in same] == [202, 202]
                and len({item[1]['data']['submission_id'] for item in same}) == 1,
                'concurrent identical finalizations both acknowledge one physical submission; responses=' +
                str([(item[0], item[1].get('error')) for item in same]))
    accepted = same[0][1]['data']
    submission = accepted['submission_id']
    base.expect(accepted['division_id'] == 1001 and accepted['submission_status'] == 'accepted'
                and accepted['calculation_status'] in ['queued', 'processing', 'retrying', 'ready']
                and (accepted['result'] is None if accepted['calculation_status'] != 'ready' else accepted['result']['score'] == 80),
                'submission accepts raw snapshot immediately in checking state without a fake score')
    replay = base.api('/attempts/' + aid + '/finalize', student, 'POST', final, 202)
    base.expect(replay['submission_id'] == submission, 'retrying sealed snapshot returns the same submission')
    altered = copy.deepcopy(final)
    altered['answers'][0]['value'] = 'a'
    altered.pop('snapshot_hash')
    base.api('/attempts/' + aid + '/finalize', student, 'POST', altered, 409)
    base.api(f'/divisions/1001/tests/{tid}/attempts', student, 'POST', {'practice': False}, 409)
    base.api('/submissions/' + submission, other, code=403)
    base.api('/submissions/' + submission, student2, code=403)
    l1 = base.api('/divisions/1001/submissions?test_id=' + tid, student)
    l2 = base.api('/divisions/1002/submissions?test_id=' + tid, student2)
    base.expect(l1['total'] == 1 and l2['total'] == 0, 'submission pagination filters the original selected division')
    stats = base.api('/divisions/1001/submissions/stats?test_id=' + tid, student)
    base.expect(stats['accepted'] == 1 and stats['queued'] + stats['ready'] == 1
                and (stats['average_score'] == 80 if stats['ready'] else stats['average_score'] is None),
                'statistics distinguish asynchronous checks from graded scores')
    base.api('/divisions/1002/submissions', staff, code=403)
    base.expect(base.api('/divisions/1001/submissions?test_id=' + tid, viewer)['total'] == 1,
                'filtered results viewer can inspect submissions only in assigned division')
    base.api('/divisions/1002/submissions', viewer, code=403)
    base.api('/attempts/' + aid + '/force-submit', viewer, 'POST', {}, 403)
    base.api('/attempts/' + aid, viewer, 'DELETE', {'expected_revision': 1}, 403)
    internal('/internal/tests/submissions/' + submission + '/input', authorized=False, code=401)
    original = internal('/internal/tests/submissions/' + submission + '/input')['data']
    base.expect(original['submission']['division_id'] == 1001 and original['submission']['test_version_id'] == attempt['test_version_id'],
                'calculation input pins submission division and original content version')
    receipt = wait_for(lambda: inbox(submission_id=submission)['items'], 'calculate durably receives accepted submission')
    base.expect(len(receipt) == 1, 'one logical grade event is stored for repeated finalize')
    receipt_detail = internal('/internal/calculate/messages/' + receipt[0]['message_id'], origin=CALCULATE)['data']
    base.expect(receipt_detail['payload']['division_id'] == 1001
                and original['settings_snapshot']['passing_score'] == 50
                and receipt_detail['payload']['grading_test_version_id'] == attempt['test_version_id'],
                'grade message records original selected division and frozen placement settings')
    changes = wait_for(lambda: [item for item in inbox(test_id=tid)['items']
                              if item['message_type'] == 'tests.content.changed'], 'calculate receives whole edit batch')
    base.expect(len(changes) == 1, 'no-op and stale saves create no duplicate change batches')

    internal('/internal/tests/submissions/' + submission + '/result', 'POST', {}, authorized=False, code=401)
    ready = wait_ready(submission, student, 80)
    base.expect(ready['result']['earned_points'] == 8 and ready['result']['max_points'] == 10,
                'official worker grades immutable original content even when editor has saved a newer version')
    base.expect(base.api('/divisions/1001/submissions/stats?test_id=' + tid, student)['average_score'] == 80,
                'statistics include the official calculator result')
    review = base.api('/submissions/' + submission + '/review', student)
    base.expect(bool(review), 'ready result with answers enabled supports student review')

    second = start(student2, tid, 1002)
    base.expect(second['id'] != aid and second['division_id'] == 1002,
                'same physical test has a separate official attempt in another selected division')
    final2 = snapshot(second)
    accepted2 = base.api('/attempts/' + second['id'] + '/finalize', student2, 'POST', final2, 202)
    base.expect(accepted2['division_id'] == 1002 and accepted2['submission_id'] != submission,
                'official submission remains attached to the division where it was taken')
    reset(second['id'], admin)
    internal('/internal/tests/submissions/' + accepted2['submission_id'] + '/input', code=409)
    replacement = start(student2, tid, 1002)
    base.expect(replacement['id'] != second['id'], 'reset creates a new attempt and rejects stale calculator writes')
    base.api('/attempts/' + replacement['id'] + '/answer', student2, 'PATCH', {'question_id': 1, 'value': 'a'})
    forced = base.api('/attempts/' + replacement['id'] + '/force-submit', admin, 'POST', {}, 202)
    base.expect(forced['division_id'] == 1002 and forced['calculation_status'] == 'queued',
                'admin force-submit follows the same asynchronous pipeline and original division')

    practice = start(student, tid, practice=True)
    feedback = base.api('/attempts/' + practice['id'] + '/practice-answer', student, 'POST', {'question_id': 1, 'value': 'a'})
    base.expect(feedback['feedback']['is_correct'], 'training still returns immediate answer feedback')
    completed_practice = base.api('/attempts/' + practice['id'] + '/submit', student, 'POST', {})
    base.expect(completed_practice['status'] == 'practice_completed' and completed_practice['submission_id'] is None
                and completed_practice['result']['score'] == 33, 'training completion reports its own score without an official submission')
    next_practice = start(student, tid, practice=True)
    base.expect(next_practice['id'] != practice['id'], 'completed training permits a fresh training attempt')
    base.expect(base.api('/divisions/1001/submissions?test_id=' + tid, student)['total'] == 1,
                'training does not create an official submission or rating input')

    draft = base.api('/divisions/1001/tests/' + tid + '/draft', admin, 'POST', {}, 201, base.key())
    draft_ids.add(draft['id'])
    draft_body = {key: draft[key] for key in ['title', 'description', 'direction_id', 'questions', 'layout']}
    base.api('/divisions/1001/drafts/' + draft['id'], admin, 'PUT', {'expected_revision': draft['revision'], **draft_body}, 409)
    base.api('/divisions/1001/drafts/' + draft['id'] + '/lock', viewer, 'POST', {'expected_revision': draft['revision']}, 403)
    base.api('/divisions/1002/drafts/' + draft['id'], viewer, code=403)
    draft = base.api('/divisions/1001/drafts/' + draft['id'] + '/lock', admin, 'POST', {'expected_revision': draft['revision']})
    draft_body = {key: draft[key] for key in ['title', 'description', 'direction_id', 'questions', 'layout']}
    draft_body['title'] = base.prefix + ' published draft'
    draft = base.api('/divisions/1001/drafts/' + draft['id'], admin, 'PUT', {'expected_revision': draft['revision'], **draft_body})
    published = base.keep(base.api('/divisions/1001/drafts/' + draft['id'] + '/publish', admin, 'POST', {'expected_revision': draft['revision']}, 201))
    base.expect(published['id'] != tid and not published['placements'][0]['settings']['published'],
                'draft publish creates an independent hidden test')
    repeated_publish = base.api('/divisions/1001/drafts/' + draft['id'] + '/publish', admin, 'POST', {'expected_revision': draft['revision']})
    base.expect(repeated_publish['id'] == published['id'], 'draft publication retry returns the same physical test')

    stable = create(admin, direction, ' stable question IDs')
    stable_id = stable['id']
    stable_content = base.api('/' + stable_id + '/content', admin)
    reduced = {key: copy.deepcopy(stable_content[key]) for key in ['title', 'description', 'direction_id', 'questions']}
    reduced['questions'] = reduced['questions'][:2]
    reduced['questions'][1]['answers'] = reduced['questions'][1]['answers'][:2]
    stable = base.api('/' + stable_id + '/content', admin, 'PUT', {'expected_revision': stable['revision'], **reduced})
    restored_question = copy.deepcopy(reduced)
    restored_question['questions'].append(questions()[2])
    base.api('/' + stable_id + '/content', admin, 'PUT', {'expected_revision': stable['revision'], **restored_question}, 409)
    restored_option = copy.deepcopy(reduced)
    restored_option['questions'][1]['answers'].append(questions()[1]['answers'][2])
    base.api('/' + stable_id + '/content', admin, 'PUT', {'expected_revision': stable['revision'], **restored_option}, 409)
    base.expect(True, 'deleted question IDs and deleted option IDs cannot be reused by later saves')

    racing_test = publish(admin, create(admin, direction, ' concurrent submission'))
    racing = start(other, racing_test['id'])
    commit_time = now()
    parallel_commits = [
        {'commits': [{'commit_id': base.secrets.token_hex(16), 'sequence': 1, 'question_id': 1, 'value': 'a', 'answered_at': commit_time}]},
        {'commits': [{'commit_id': base.secrets.token_hex(16), 'sequence': 2, 'question_id': 2, 'value': ['x', 'y'], 'answered_at': commit_time}]},
    ]
    with ThreadPoolExecutor(max_workers=2) as pool:
        committed = list(pool.map(lambda body: race_request('/v1/tests/attempts/' + racing['id'] + '/commits', other, body), parallel_commits))
    base.expect([item[0] for item in committed] == [200, 200]
                and len(base.api('/attempts/' + racing['id'], other)['answers']) == 2,
                'concurrent commits for different questions both persist without lost answers')
    candidate_a = snapshot(racing)
    candidate_b = copy.deepcopy(candidate_a)
    candidate_b['answers'][0]['value'] = 'a'
    with ThreadPoolExecutor(max_workers=2) as pool:
        contested = list(pool.map(lambda body: race_request('/v1/tests/attempts/' + racing['id'] + '/finalize', other, body), [candidate_a, candidate_b]))
    base.expect(sorted(item[0] for item in contested) == [202, 409],
                'concurrent different final snapshots accept exactly one immutable submission')
    won = next(item[1]['data'] for item in contested if item[0] == 202)
    for _ in range(5):
        state = base.api('/attempts/' + racing['id'], admin)
        outcome = race_request('/v1/tests/attempts/' + racing['id'], admin, {'expected_revision': state['revision']}, 'DELETE')
        if outcome[0] == 200:
            break
        base.expect(outcome[0] == 409, 'concurrent calculator write only produces a revision conflict')
    else:
        raise AssertionError('reset could not obtain a stable revision')
    internal('/internal/tests/submissions/' + won['submission_id'] + '/input', code=409)
    base.expect(True, 'reset during automatic calculation leaves an inactive immutable submission')

    timed_test = publish(admin, create(admin, direction, ' offline deadline'))
    timed = start(other, timed_test['id'])
    completed_at = clock_fixture(timed)
    base.expect(base.api('/attempts/' + timed['id'], other)['status'] == 'expired_pending_upload',
                'answer deadline closes editing while preserving pending offline upload')
    base.api('/attempts/' + timed['id'] + '/answer', other, 'PATCH', {'question_id': 1, 'value': 'a'}, 409)
    late_commit = {'commit_id': base.secrets.token_hex(16), 'sequence': 1, 'question_id': 1,
                   'value': 'a', 'answered_at': completed_at}
    base.api('/attempts/' + timed['id'] + '/commits', other, 'POST', {'commits': [late_commit]})
    late_snapshot = {**snapshot(timed), 'completed_at': completed_at, 'reason': 'timeout'}
    base.api('/attempts/' + timed['id'] + '/finalize', other, 'POST', late_snapshot, 202)
    base.expect(True, 'offline commits and frozen final snapshot upload after answer deadline within 24 hours')
    expired_test = publish(admin, create(admin, direction, ' expired upload'))
    expired = start(other, expired_test['id'])
    expired_completed = clock_fixture(expired, expired_upload=True)
    base.api('/attempts/' + expired['id'] + '/finalize', other, 'POST', {
        **snapshot(expired), 'completed_at': expired_completed, 'reason': 'timeout',
    }, 409)
    base.api('/attempts/' + expired['id'] + '/commits', other, 'POST', {'commits': [
        {**late_commit, 'commit_id': base.secrets.token_hex(16), 'answered_at': expired_completed},
    ]}, 409)

    if faults:
        pending = start(other, tid)
        pending_snapshot = snapshot(pending)
        container('stop', 'rabbitmq')
        try:
            base.request('/health/ready')
            accepted_pending = base.api('/attempts/' + pending['id'] + '/finalize', other, 'POST', pending_snapshot, 202)
            base.expect(accepted_pending['result'] is None, 'broker outage does not reject an atomically persisted submission')
            base.expect(base.api('/submissions/' + accepted_pending['submission_id'], other)['submission_status'] == 'accepted',
                        'accepted snapshot remains readable while RabbitMQ is stopped')
        finally:
            container('start', 'rabbitmq')
        wait_for(lambda: base.request('/health/ready', origin=CALCULATE)['status'], 'calculate reconnects after broker restart', 60)
        wait_for(lambda: inbox(submission_id=accepted_pending['submission_id'])['items'], 'outbox delivers accepted submission after broker recovery', 60)
        wait_ready(accepted_pending['submission_id'], other, 66, 60)
        worker_test = publish(admin, create(admin, direction, ' consumer stopped'))
        worker_attempt = start(other, worker_test['id'])
        container('stop', 'calculate')
        try:
            worker_accepted = base.api('/attempts/' + worker_attempt['id'] + '/finalize', other, 'POST', snapshot(worker_attempt), 202)
            base.expect(worker_accepted['calculation_status'] == 'queued',
                        'durable broker accepts a submission while calculate consumer is stopped')
        finally:
            container('start', 'calculate')
        wait_for(lambda: base.request('/health/ready', origin=CALCULATE)['status'], 'calculate restarts with its durable queue', 60)
        wait_for(lambda: inbox(submission_id=worker_accepted['submission_id'])['items'], 'stored queue delivers after consumer restart', 60)
        wait_ready(worker_accepted['submission_id'], other, 80, 60)

    return None


def create_demo():
    """Retain a small API-created example; do not run or retain test-suite fixtures."""
    base.request('/health/ready')
    base.request('/health/ready', origin=CALCULATE)
    admin = base.login('dev_admin')
    student = base.login('dev_student_01')
    direction = base.core('/directions', admin, 'POST', {'name': base.prefix + ' demo direction'}, 201)['id']
    base.directions.append(direction)
    source = create(admin, direction, ' demo source')
    source = base.api('/' + source['id'] + '/bank', admin, 'PUT', {'expected_revision': source['revision'], 'available': True})
    source = base.api('/divisions/1002/tests/' + source['id'] + '/attach', admin, 'POST', {'expected_revision': source['revision']})
    source = publish(admin, source)
    source = publish(admin, source, 1002)
    independent = base.keep(base.api('/divisions/1002/tests/' + source['id'] + '/copy', admin, 'POST', {
        'expected_revision': source['revision'], 'title': base.prefix + ' demo independent copy',
    }, 201, base.key()))
    independent = publish(admin, independent, 1002)
    attempt = start(student, source['id'])
    accepted = base.api('/attempts/' + attempt['id'] + '/finalize', student, 'POST', snapshot(attempt), 202)
    wait_for(lambda: inbox(submission_id=accepted['submission_id'])['items'], 'demo submission durably received by calculate')
    ready = wait_ready(accepted['submission_id'], student, 80)
    for token in base.sessions:
        base.request('/v1/auth/logout', token, 'POST', origin=base.AUTH)
    return {'organization_id': 1001, 'direction_id': direction, 'division_id': 1001,
            'attached_division_ids': [1001, 1002], 'test_id': source['id'], 'copied_test_id': independent['id'],
            'attempt_id': attempt['id'], 'submission_id': accepted['submission_id'],
            'status': 'ready', 'score': ready['result']['score'], 'calculate_mode': 'grading', 'created_through_api': True}


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--skip-faults', action='store_true')
    parser.add_argument('--demo', action='store_true')
    args = parser.parse_args()
    success = False
    try:
        demo = create_demo() if args.demo else main(faults=not args.skip_faults)
        success = True
    finally:
        if not args.demo or not success:
            cleanup()
    report = {'status': 'passed', 'http_checks': len(base.checks), 'scenarios': base.checks,
              'synthetic_records_cleaned': not args.demo, 'broker_outage_checked': not args.skip_faults and not args.demo,
              'production_accessed': False}
    path = ROOT / 'dev/local' / ('tests-demo-report.json' if args.demo else 'tests-workflow-report.json')
    path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
    path.chmod(0o600)
    if demo:
        path = ROOT / 'dev/local/tests-demo.json'
        path.write_text(json.dumps(demo, ensure_ascii=False, indent=2) + '\n')
        path.chmod(0o600)
    print(json.dumps({key: report[key] for key in ['status', 'http_checks', 'synthetic_records_cleaned', 'broker_outage_checked']}))
