"""Local tests integration through BFF. Fixtures and cleanup use public APIs only.

--queue-outage additionally pauses only the local calculate/Rabbit containers,
with state guards and unpause in finally. Run that variant without other writers.
All fixtures have a fresh DEV prefix and remain available for UI inspection.
"""
import argparse
import copy
from datetime import datetime, timedelta, timezone
import json
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

from http_test import Client, ROOT, pw, expect, checks

DOCKER = '/Applications/Docker.app/Contents/Resources/bin/docker'
RUN = uuid.uuid4().hex[:8]
PREFIX = 'DEV BFF Tests ' + RUN


class TestsClient(Client):
    def request(self, path, body=None, method=None, code=200, key=None):
        headers = {'X-CPM-Request': '1', 'X-CPM-Context': self.rev,
                   'Origin': 'http://127.0.0.1:3001', 'Content-Type': 'application/json'}
        if key:
            headers['Idempotency-Key'] = key
        request = urllib.request.Request('http://127.0.0.1:8001/bff/' + path,
                                        data=json.dumps(body).encode() if body is not None else None,
                                        headers=headers, method=method or ('POST' if body is not None else 'GET'))
        try:
            with self.http.open(request, timeout=35) as response:
                status, data = response.status, json.load(response)
        except urllib.error.HTTPError as response:
            status, data = response.code, json.load(response)
        expect(status == code, f'{path.split("?")[0]} expected {code}, got {status} {data.get("error", "")}')
        if 'context_revision' in data:
            self.rev = data['context_revision']
        expect(not any(k in data for k in ['access_token', 'refresh_token', 'token']), 'BFF never returns auth tokens')
        return data.get('data', data)


def owner(port, path, token=None, body=None, method=None, code=200, internal=False):
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    if internal:
        env = {}
        for line in (ROOT / 'dev/.env').read_text().splitlines():
            if '=' in line and not line.startswith('#'):
                k, v = line.split('=', 1)
                env[k] = v.strip().strip('"').strip("'")
        headers['X-Service-Key'] = env['AUTH_INTERNAL_KEY']
    request = urllib.request.Request(f'http://127.0.0.1:{port}' + path,
                                     data=json.dumps(body).encode() if body is not None else None,
                                     headers=headers, method=method or ('POST' if body is not None else 'GET'))
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            status, data = response.status, json.load(response)
    except urllib.error.HTTPError as response:
        status, data = response.code, json.load(response)
    expect(status == code, f'local owner {path.split("?")[0]} expected {code}, got {status} {data.get("error", "")}')
    return data.get('data', data)


def questions():
    return [
        {'question_id': 1, 'type': 'single', 'text': 'Первый вариант правильный', 'points': 2,
         'answers': [{'id': 'a', 'text': 'Первый', 'is_correct': True}, {'id': 'b', 'text': 'Второй', 'is_correct': False}], 'correct_answers': []},
        {'question_id': 2, 'type': 'multiple', 'text': 'Выберите X и Y', 'points': 3,
         'answers': [{'id': 'x', 'text': 'X', 'is_correct': True}, {'id': 'y', 'text': 'Y', 'is_correct': True}, {'id': 'z', 'text': 'Z', 'is_correct': False}], 'correct_answers': []},
        {'question_id': 3, 'type': 'text', 'text': 'Столица России', 'points': 5,
         'answers': [], 'correct_answers': ['Москва', 'Moscow']},
    ]


def settings(revision, published=True, answers=True):
    now = datetime.now(timezone.utc)
    return {'expected_revision': revision, 'published': published,
            'start_at': (now - timedelta(days=1)).isoformat(), 'end_at': (now + timedelta(days=7)).isoformat(),
            'time_limit_minutes': 60, 'passing_score': 50, 'answers_visible': answers}


def create(client, direction, label, content=None, key=None):
    body = {'title': PREFIX + ' · ' + label, 'description': 'Локальная API/BFF проверка',
            'direction_id': direction, 'questions': questions() if content is None else content}
    return client.request('tests/catalog', body, code=201, key=key or uuid.uuid4().hex), body


def finalize(client, attempt, correct=True):
    values = [{'question_id': 1, 'value': 'b'},
              {'question_id': 2, 'value': ['y', 'x'] if correct else ['z']},
              {'question_id': 3, 'value': ' Москва ' if correct else 'Неверно'}]
    body = {'test_version_id': attempt['test_version_id'], 'completed_at': datetime.now(timezone.utc).isoformat(),
            'reason': 'manual', 'answers': values}
    data = client.request('tests/attempts/' + attempt['id'] + '/finalize', body, code=202)
    expect(data['submission_status'] == 'accepted' and data['calculation_status'] == 'queued'
           and data['result'] is None, 'official finalize accepts asynchronously without a fabricated score')
    return data, body


def wait_for(check, label, timeout=65):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        result = check()
        if result:
            expect(True, label)
            return result
        time.sleep(.4)
    raise AssertionError(label + ': timed out')


def ready(client, submission, score=None):
    def check():
        row = client.request('tests/submissions/' + submission)
        return row if row['calculation_status'] == 'ready' and (score is None or row['result']['score'] == score) else None
    return wait_for(check, 'real Rabbit/calculate callback produces ready' + (f' score {score}' if score is not None else ''))


class PausedContainer:
    def __init__(self, service):
        self.name = 'cpm-dev-' + service + '-1'
        self.paused = False

    def __enter__(self):
        result = subprocess.run([DOCKER, 'inspect', '--format', '{{json .State}}', self.name], check=True, capture_output=True, text=True)
        state = json.loads(result.stdout)
        expect(state['Running'] and not state['Paused'], 'only a running unpaused local dev container may be paused')
        subprocess.run([DOCKER, 'pause', self.name], check=True, capture_output=True)
        self.paused = True
        return self

    def __exit__(self, *_):
        if self.paused:
            subprocess.run([DOCKER, 'unpause', self.name], check=True, capture_output=True)
            self.paused = False


def main(queue_outage=False):
    clients = []
    direct = owner(8002, '/v1/auth/login', body={'login': 'dev_admin', 'password': pw})
    token = direct['access_token']
    def logged(name):
        client = TestsClient()
        identity = client.login(name)
        clients.append(client)
        return client, identity
    admin, _ = logged('dev_admin')
    student, identity = logged('dev_student_01')
    zero, zero_identity = logged('dev_student_02')
    direction = owner(8003, '/v1/core/directions', token, {'name': PREFIX + ' Направление'}, code=201)['id']
    original_settings = admin.request('core/organization/test-settings')
    fixtures = {'run': RUN, 'direction_id': direction, 'student_account_id': identity['user']['account_id'],
                'zero_account_id': zero_identity['user']['account_id'], 'queue_outage': queue_outage}
    try:
        roles = {}
        for name, permissions in {
            'view': {'tests': {'view': True, 'edit': False}, 'test-results': {'view': True, 'edit': False}},
            'edit': {'tests': {'view': True, 'edit': True}, 'test-results': {'view': True, 'edit': True}},
            'results': {'test-results': {'view': True, 'edit': False}},
        }.items():
            roles[name] = owner(8003, '/v1/core/roles', token, {'name': PREFIX + ' ' + name, 'permissions': permissions}, code=201)['id']
        for name in ['staff', 'editor', 'results']:
            login = 'dev_bff_tests_' + name + '_' + RUN
            account = owner(8003, '/v1/core/divisions/1001/users', token,
                            {'role': 'staff_admin', 'full_name': PREFIX + ' ' + name, 'login': login, 'password': pw,
                             'role_id': roles['view' if name == 'staff' else 'edit' if name == 'editor' else 'results']}, code=201)['account_id']
            if name == 'staff':
                owner(8003, f'/v1/core/divisions/1002/memberships/{account}', token, {'role_id': roles['edit']}, 'PUT')
            fixtures[name + '_login'] = login
        staff, _ = logged(fixtures['staff_login'])
        editor, _ = logged(fixtures['editor_login'])
        results_only, _ = logged(fixtures['results_login'])
        key = uuid.uuid4().hex
        source, source_body = create(admin, direction, 'Общий тест', key=key)
        sid = source['id']; fixtures['source_test_id'] = sid
        same = admin.request('tests/catalog', source_body, code=200, key=key)
        expect(same['id'] == sid, 'create replay preserves one test identity')
        admin.request('tests/catalog', {**source_body, 'title': 'Changed key body'}, code=409, key=key)
        staff.request('tests/catalog', source_body, code=403, key=uuid.uuid4().hex)
        staff.request('tests/' + sid + '/content')
        results_only.request('tests/catalog', code=403)
        results_only.request('tests/' + sid, code=404)
        student.request('tests/' + sid + '/content', code=404)
        source = admin.request('tests/' + sid + '/bank', {'expected_revision': source['revision'], 'available': True}, 'PUT')
        staff.context(1001, 1002)
        expect(any(t['id'] == sid for t in staff.request('tests/bank?q=' + RUN)['items']), 'open bank exposes a foreign division test to authorized staff')
        source = admin.request('tests/' + sid + '/bank', {'expected_revision': source['revision'], 'available': False}, 'PUT')
        expect(staff.request('tests/bank?q=' + RUN)['total'] == 0, 'closed private bank excludes unattached foreign division staff')
        staff.request('tests/' + sid, code=404)
        source = admin.request('tests/' + sid + '/bank', {'expected_revision': source['revision'], 'available': True}, 'PUT')
        staff.context(1001, 1001)
        source = admin.request('tests/' + sid + '/settings', settings(source['revision']), 'PUT')
        admin.context(1001, 1002)
        source = admin.request('tests/' + sid + '/attach', {'expected_revision': source['revision']})
        repeated = admin.request('tests/' + sid + '/attach', {'expected_revision': 1})
        expect(sum(p['division_id'] == 1002 for p in repeated['placements']) == 1, 'attachment replay never duplicates placement')
        source = admin.request('tests/' + sid + '/settings', settings(source['revision']), 'PUT')
        clone = admin.request('tests/' + sid + '/copy', {'expected_revision': source['revision'], 'title': PREFIX + ' · Независимая копия'}, code=201, key=uuid.uuid4().hex)
        cloneid = clone['id']; fixtures['reserve_copy_test_id'] = cloneid
        clone_content = admin.request('tests/' + cloneid + '/content')
        expect(clone['revision'] == 1 and not clone['bank_available'] and len(clone['placements']) == 1
               and not clone['placements'][0]['settings']['published'] and clone_content['questions'] == questions()
               and clone['content_version_id'] != source['content_version_id'], 'deep copy has independent owner, version, keys, questions and hidden settings')
        clone = admin.request('tests/' + cloneid + '/settings', settings(clone['revision']), 'PUT')
        staff.context(1001, 1002)
        staff.request('tests/' + sid + '/content', {'expected_revision': source['revision'], **source_body}, 'PUT', code=403)
        source = staff.request('tests/' + sid + '/settings', settings(source['revision']), 'PUT')
        staffcopy = staff.request('tests/' + sid + '/copy', {'expected_revision': source['revision'], 'title': PREFIX + ' · Копия сотрудника'}, code=201, key=uuid.uuid4().hex)
        fixtures['staff_copy_test_id'] = staffcopy['id']
        staff.request('tests/' + sid + '/copy', {'expected_revision': 1, 'title': ''}, code=409, key=uuid.uuid4().hex)
        admin.request('tests/' + sid + '/attachment', {'expected_revision': source['revision']}, 'DELETE')
        source = admin.request('tests/' + sid + '/attach', {'expected_revision': source['revision'] + 1})
        source = admin.request('tests/' + sid + '/settings', settings(source['revision']), 'PUT')
        q = urllib.parse.urlencode({'q': RUN, 'limit': 1})
        page1 = admin.request('tests/catalog?' + q)
        page2 = admin.request('tests/catalog?' + q + '&offset=1')
        expect(page1['total'] >= 3 and page1['total'] == page2['total'] and page1['items'][0]['id'] != page2['items'][0]['id'], 'division filtering and total precede pagination')
        intersection = admin.request('tests/catalog?' + urllib.parse.urlencode({'q': RUN, 'relation': 'owned', 'owner_division_id': 1001}))
        expect(intersection['total'] == 0, 'owner and relation filters intersect without overwriting one another')
        copies = admin.request('tests/catalog?' + urllib.parse.urlencode({'q': RUN, 'relation': 'copy', 'direction_id': direction}))
        expect(copies['total'] == 2, 'copy and direction filters return independent copies only')
        admin.context(1001, 1001)
        staff.context(1001, 1001)
        content = admin.request('tests/' + sid + '/content')
        changed = copy.deepcopy(questions()); changed[0]['text'] += ' — обновлённая формулировка'
        content = admin.request('tests/' + sid + '/content', {**source_body, 'questions': changed, 'expected_revision': content['revision']}, 'PUT')
        admin.request('tests/' + sid + '/content', {**source_body, 'questions': changed, 'expected_revision': content['revision'] - 1}, 'PUT', code=409)
        admin.context(1001, 1002)
        expect(admin.request('tests/' + cloneid + '/content')['questions'] == clone_content['questions'], 'source content changes do not mutate independent copy')
        admin.context(1001, 1001)
        source = admin.request('tests/' + sid)
        source = admin.request('tests/' + sid + '/archive', {'expected_revision': source['revision'], 'is_archived': True}, 'PUT')
        expect(sid not in [t['id'] for t in student.request('tests/catalog')['items']], 'archive closes student catalog')
        student.request('tests/' + sid + '/attempts', {'practice': False}, code=409)
        admin.context(1001, 1002)
        expect(not admin.request('tests/' + cloneid)['is_archived'], 'source archive leaves copy usable')
        admin.context(1001, 1001)
        source = admin.request('tests/' + sid + '/archive', {'expected_revision': source['revision'], 'is_archived': False}, 'PUT')
        admin.request('tests/' + sid + '/attachment', {'expected_revision': source['revision']}, 'DELETE', code=409)
        foreign, _ = logged('dev_admin_math')
        foreign.request('tests/' + sid, code=404)
        foreign_bank = foreign.request('tests/bank?q=' + RUN)
        expect(foreign_bank['total'] == 0, 'organization switch isolates bank and test lookup')
        student.context(2001, 2001)
        student.request('tests/' + sid, code=404)
        student.context(1001, 1001)
        staff.request('auth/context', {'organization_id': 2001, 'division_id': 2001}, code=403)
        admin.request('tests/catalog?division_id=1002', code=400)
        admin.request('tests/bank?target_division_id=1002', code=400)
        admin.request('tests/divisions/1002/tests', code=501)
        # Draft revision/lock/publish uses public APIs, including a second editor.
        draft_body = {'title': '', 'description': '', 'direction_id': 0, 'questions': [], 'layout': []}
        draft = admin.request('tests/drafts', draft_body, code=201, key=uuid.uuid4().hex)
        did = draft['id']; fixtures['draft_id'] = did
        admin.request('tests/drafts/' + did, {**draft_body, 'expected_revision': draft['revision']}, 'PUT', code=409)
        draft = admin.request('tests/drafts/' + did + '/lock', {'expected_revision': draft['revision']})
        editor.request('tests/drafts/' + did + '/lock', {'expected_revision': draft['revision']}, code=409)
        full_draft = {'title': PREFIX + ' · Из черновика', 'description': 'Layout сохранён', 'direction_id': direction,
                      'questions': questions(), 'layout': [{'question_id': 1, 'x': 10, 'y': 20}]}
        draft = admin.request('tests/drafts/' + did, {**full_draft, 'expected_revision': draft['revision']}, 'PUT')
        expect(draft['layout'] == full_draft['layout'], 'draft persists canvas layout')
        publication_revision = draft['revision']
        draft_test = admin.request('tests/drafts/' + did + '/publish', {'expected_revision': publication_revision}, code=201)
        replay = admin.request('tests/drafts/' + did + '/publish', {'expected_revision': publication_revision})
        expect(replay['id'] == draft_test['id'] and not replay['placements'][0]['settings']['published'], 'draft publication repeats as the same independent hidden test')
        fixtures['published_draft_test_id'] = draft_test['id']
        from_test = admin.request('tests/' + sid + '/draft', {}, code=201, key=uuid.uuid4().hex)
        expect(from_test['questions'] == changed, 'new draft from test contains full independent editor content')
        admin.request('tests/drafts/' + from_test['id'], {'expected_revision': from_test['revision']}, 'DELETE')
        # Main official attempts and both zero/nonzero scores.
        attempt = student.request('tests/' + sid + '/attempts', {'practice': False}, code=201)
        resumed = student.request('tests/' + sid + '/attempts', {'practice': False})
        expect(resumed['id'] == attempt['id'] and resumed['question_order'] == attempt['question_order']
               and resumed['answer_deadline'] == attempt['answer_deadline'], 'resume preserves attempt identity, order and deadline')
        expect('is_correct' not in json.dumps(attempt['questions']) and 'correct_answers' not in json.dumps(attempt['questions']), 'student questions never include answer keys')
        student.request('tests/attempts?practice=garbage', code=400)
        commit = {'commit_id': uuid.uuid4().hex, 'sequence': 1, 'question_id': 1, 'value': 'b', 'answered_at': datetime.now(timezone.utc).isoformat()}
        ack = student.request('tests/attempts/' + attempt['id'] + '/commits', {'commits': [commit]})
        student.request('tests/attempts/' + attempt['id'] + '/commits', {'commits': [commit]})
        expect(ack['acknowledged_commit_ids'] == [commit['commit_id']], 'offline commit acknowledgement is repeatable')
        student.request('tests/attempts/' + attempt['id'] + '/commits', {'commits': [{**commit, 'value': 'a'}]}, code=409)
        zero_attempt = zero.request('tests/' + sid + '/attempts', {'practice': False}, code=201)
        if queue_outage:
            with PausedContainer('calculate'):
                submission, snapshot = finalize(student, attempt)
                zero_sub, _ = finalize(zero, zero_attempt, False)
                pending = student.request('tests/submissions/' + submission['submission_id'])
                expect(pending['calculation_status'] == 'queued' and pending['result'] is None, 'calculate outage retains durable accepted pending status')
                student.request('tests/submissions/' + submission['submission_id'] + '/review', code=409)
                retried = admin.request('tests/submissions/' + submission['submission_id'] + '/retry-calculation', {'expected_revision': pending['revision']}, code=202)
                expect(retried['calculation_revision'] == pending['calculation_revision'] + 1, 'retry creates a fresh calculation revision while pending')
        else:
            submission, snapshot = finalize(student, attempt)
            zero_sub, _ = finalize(zero, zero_attempt, False)
        subid = submission['submission_id']; zeroid = zero_sub['submission_id']
        fixtures.update(submission_id=subid, zero_submission_id=zeroid, attempt_id=attempt['id'])
        replay = student.request('tests/attempts/' + attempt['id'] + '/finalize', snapshot, code=202)
        expect(replay['submission_id'] == subid, 'same final snapshot never creates a second submission')
        student.request('tests/attempts/' + attempt['id'] + '/finalize', {**snapshot, 'answers': []}, code=409)
        graded = ready(student, subid, 80); ready(zero, zeroid, 0)
        summary = admin.request('tests/submissions/stats?' + urllib.parse.urlencode({'test_id': sid, 'account_id': identity['user']['account_id'], 'calculation_status': 'ready'}))
        expect(summary['accepted'] == summary['ready'] == 1 and summary['average_score'] == 80, 'stats applies account/test/status before averaging instead of mixing another student')
        summary = admin.request('tests/submissions/stats?test_id=' + sid + '&calculation_status=queued')
        expect(summary['accepted'] == summary['ready'] == 0 and summary['average_score'] is None, 'queued filter never fabricates zero or includes ready grades')
        own = student.request('tests/submissions?account_id=' + str(zero_identity['user']['account_id']) + '&test_id=' + sid)
        expect(len(own['items']) == 1 and own['items'][0]['account_id'] == identity['user']['account_id'], 'student cannot select another account in result list')
        result_rows = results_only.request('tests/submissions?test_id=' + sid)
        expect(result_rows['total'] == 2, 'results-only staff reads scoped submission list without catalog rights')
        results_only.request('tests/attempts/' + attempt['id'] + '/force-submit', {}, code=403)
        staff.request('tests/attempts/' + attempt['id'], {'expected_revision': graded['revision']}, 'DELETE', code=403)
        student.context(1001, 1002)
        student.request('tests/submissions/' + subid, code=403)
        expect(student.request('tests/submissions?test_id=' + sid)['total'] == 0, 'attached division has its own independent submissions')
        student.context(1001, 1001)
        admin.context(1001, 1002)
        admin.request('tests/submissions/' + subid, code=404)
        admin.context(1001, 1001)
        student.request('tests/submissions/' + subid + '/review')
        source = admin.request('tests/' + sid)
        source = admin.request('tests/' + sid + '/settings', settings(source['revision'], answers=False), 'PUT')
        student.request('tests/submissions/' + subid + '/review', code=403)
        source = admin.request('tests/' + sid + '/settings', settings(source['revision']), 'PUT')
        admin.request('tests/submissions/' + subid + '/retry-calculation', {'expected_revision': student.request('tests/submissions/' + subid)['revision']}, code=409)
        history = admin.request('tests/submissions/' + subid + '/calculations')
        expect(history['total'] == 1, 'duplicate finalize and queue delivery retain one confirmed calculation')
        # Policy disabled then enabled: original answers remain unchanged.
        config = admin.request('core/organization/test-settings')
        admin.request('core/organization/test-settings', {'expected_revision': config['revision'], 'recalculate_on_test_change': False}, 'PUT')
        content = admin.request('tests/' + sid + '/content')
        disabled_questions = copy.deepcopy(content['questions']); disabled_questions[0]['points'] = 3
        admin.request('tests/' + sid + '/content', {**source_body, 'questions': disabled_questions, 'expected_revision': content['revision']}, 'PUT')
        time.sleep(1)
        expect(student.request('tests/submissions/' + subid)['result']['score'] == 80, 'organization disabled policy preserves prior grade')
        config = admin.request('core/organization/test-settings')
        admin.request('core/organization/test-settings', {'expected_revision': config['revision'], 'recalculate_on_test_change': True}, 'PUT')
        content = admin.request('tests/' + sid + '/content')
        enabled_questions = copy.deepcopy(content['questions']); enabled_questions[0]['points'] = 8
        admin.request('tests/' + sid + '/content', {**source_body, 'questions': enabled_questions, 'expected_revision': content['revision']}, 'PUT')
        recalculated = ready(student, subid, 50); ready(zero, zeroid, 0)
        history = admin.request('tests/submissions/' + subid + '/calculations')
        expect(history['total'] == 2 and {row['calculation']['score'] for row in history['items']} == {50, 80}, 'scoring edit asynchronously adds history without overwriting original grade')
        frozen = student.request('tests/attempts/' + attempt['id'])
        expect(frozen['test_version_id'] == attempt['test_version_id'] and frozen['division_id'] == 1001 and frozen['answers'][0]['value'] == 'b', 'recalculation preserves attempt version, division and raw answers')
        # An end-to-end training uses the public owner route, with no official submission.
        practice = student.request('tests/' + sid + '/attempts', {'practice': True}, code=201)
        feedback = student.request('tests/attempts/' + practice['id'] + '/practice-answer', {'question_id': 1, 'value': 'a'})
        expect(feedback['feedback']['is_correct'] and feedback['feedback']['points'] == 8, 'practice feedback is immediate and uses current version')
        practice_done = student.request('tests/attempts/' + practice['id'] + '/submit', {})
        expect(practice_done['status'] == 'practice_completed' and practice_done['submission_id'] is None, 'practice completion has no official submission identity')
        # Fresh active demo for browser finishing; dev_student_03 has never started it.
        demo, _ = create(admin, direction, 'Для прохождения в UI')
        demo = admin.request('tests/' + demo['id'] + '/settings', settings(demo['revision']), 'PUT')
        fixtures['browser_test_id'] = demo['id']; fixtures['browser_student_login'] = 'dev_student_03'
        if queue_outage:
            pending_student, _ = logged('dev_student_04')
            pending_attempt = pending_student.request('tests/' + demo['id'] + '/attempts', {'practice': False}, code=201)
            with PausedContainer('rabbitmq'):
                broker_pending, _ = finalize(pending_student, pending_attempt)
                retained = pending_student.request('tests/submissions/' + broker_pending['submission_id'])
                expect(retained['result'] is None, 'Rabbit outage never rejects already durable accepted submission')
            ready(pending_student, broker_pending['submission_id'], 80)
            reset_student, _ = logged('dev_student_05')
            reset_attempt = reset_student.request('tests/' + demo['id'] + '/attempts', {'practice': False}, code=201)
            with PausedContainer('calculate'):
                reset_sub, _ = finalize(reset_student, reset_attempt)
                admin.request('tests/attempts/' + reset_attempt['id'], {'expected_revision': reset_sub['revision']}, 'DELETE')
            def skipped():
                inbox = owner(8005, '/internal/calculate/messages?submission_id=' + reset_sub['submission_id'], internal=True)
                return inbox if any(r['status'] == 'skipped' for r in inbox['items']) else None
            wait_for(skipped, 'late queued calculation for reset attempt is skipped')
            reset_student.request('tests/submissions/' + reset_sub['submission_id'], code=404)
        # Explicit live permission revocation uses only our isolated role.
        owner(8003, '/v1/core/roles/' + str(roles['view']), token, {'name': PREFIX + ' view', 'permissions': {}}, 'PUT')
        staff.request('tests/catalog', code=403)
        owner(8003, '/v1/core/roles/' + str(roles['view']), token,
              {'name': PREFIX + ' view', 'permissions': {'tests': {'view': True, 'edit': False}, 'test-results': {'view': True, 'edit': False}}}, 'PUT')
        staff.request('tests/catalog')
        fixtures['roles'] = roles
        fixtures['source_score'] = recalculated['result']['score']
        (ROOT / 'dev/local/bff-tests-demo.json').write_text(json.dumps(fixtures, ensure_ascii=False, indent=2) + '\n')
        report = {'status': 'passed', 'checks': checks, 'demo': fixtures}
        (ROOT / 'dev/local/bff-tests-http-report.json').write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
        print(json.dumps({'status': 'passed', 'checks': len(checks), 'demo': fixtures}, ensure_ascii=False))
    finally:
        try:
            current = admin.request('core/organization/test-settings')
            if current['recalculate_on_test_change'] != original_settings['recalculate_on_test_change']:
                admin.request('core/organization/test-settings', {'expected_revision': current['revision'], 'recalculate_on_test_change': original_settings['recalculate_on_test_change']}, 'PUT')
        finally:
            for client in clients:
                try:
                    client.logout()
                except Exception:
                    pass
            owner(8002, '/v1/auth/logout', token, {})


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--queue-outage', action='store_true')
    main(parser.parse_args().queue_outage)
