"""Official grading/regrading against guarded local dev, using API-created tests.

Every expected grade is computed by the unmodified legacy Python scorer. Read-only
Mongo inspection verifies the accepted snapshot and physical submission identities.
Fault scenarios stop only calculate or RabbitMQ and always restore the containers.
"""
import argparse
import base64
from concurrent.futures import ThreadPoolExecutor
import copy
from datetime import datetime, timezone
import hashlib
import importlib.util
import json
from pathlib import Path
import time
import uuid

from bson import json_util
import http_test as base
import workflow_test as flow

ROOT = base.ROOT
bulk_users = []


def legacy_module(name):
    path = ROOT / 'cpm-back-main/cpm_back/services/exam' / (name + '.py')
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


SCORING = legacy_module('scoring')


def legacy_questions(questions):
    return [{'questionId': q['question_id'], 'type': q['type'], 'text': q['text'], 'points': q['points'],
             'answers': [{'id': a['id'], 'text': a['text'], 'isCorrect': a['is_correct']} for a in q['answers']],
             'correctAnswers': q['correct_answers']} for q in questions]


def legacy_answers(questions, answers):
    types = {q['question_id']: q['type'] for q in questions}
    return {a['question_id']: {'questionId': a['question_id'], 'type': types[a['question_id']],
            {'single': 'selectedAnswer', 'multiple': 'selectedAnswers', 'text': 'textAnswer'}[types[a['question_id']]]: a['value']}
            for a in answers}


def legacy_grade(questions, answers):
    old_questions = legacy_questions(questions)
    raw = legacy_answers(questions, answers)
    scored, score = SCORING.score_attempt_answers(old_questions, [q['question_id'] for q in questions], raw)
    return {'score': score, 'earned_points': sum(a['points'] for a in scored),
            'max_points': sum(q['points'] for q in questions), 'question_results': [
                {'question_id': a['questionId'], 'points': a['points'], 'is_correct': a['isCorrect']} for a in scored]}


def canonical_hash(value):
    return hashlib.sha256(json_util.dumps(value, sort_keys=True, separators=(',', ':'),
                                         json_options=json_util.CANONICAL_JSON_OPTIONS).encode()).hexdigest()


def immutable(doc):
    return {k: doc.get(k) for k in ['_id', 'organization_id', 'division_id', 'account_id', 'test_id',
        'test_version_id', 'settings_snapshot', 'question_order', 'answers', 'commits', 'submission_id',
        'accepted_at', 'completed_at', 'reason', 'snapshot_hash', 'started_at', 'answer_deadline', 'upload_deadline']}


def record(submission):
    client = base.mongo_client(base.settings())
    try:
        value = client.cpm_dev.tests_attempts_v1.find_one({'submission_id': base.ObjectId(submission)})
        assert value and str(value['test_id']) in base.ids and value['organization_id'] == 1001
        return value
    finally:
        client.close()


def assert_db(submission, frozen, expected, division=1001):
    doc = record(submission)
    base.expect(doc['active'] and doc['status'] == 'accepted' and doc['calculation_status'] == 'ready',
                'Mongo keeps one active accepted ready submission')
    base.expect(canonical_hash(immutable(doc)) == frozen, 'Mongo preserves every accepted input field across calculation')
    base.expect(doc['division_id'] == division, 'Mongo stores the concrete selected division after grading')
    actual = doc['result']
    base.expect({k: actual[k] for k in ['score', 'earned_points', 'max_points']} ==
                {k: expected[k] for k in ['score', 'earned_points', 'max_points']}, 'Mongo persists legacy score and points consistently')
    base.expect(sorted(actual['question_results'], key=lambda q: q['question_id']) ==
                sorted(expected['question_results'], key=lambda q: q['question_id']),
                'Mongo persists all per-question results, including unanswered zero points')
    client = base.mongo_client(base.settings())
    try:
        base.expect(client.cpm_dev.tests_attempts_v1.count_documents({'submission_id': base.ObjectId(submission)}) == 1,
                    'a regrade overwrites the same physical submission rather than inserting another')
        job_ids = doc['calculation_job_ids']
        base.expect(len(job_ids) == len(set(job_ids)) and doc['calculation_job_id'] in job_ids,
                    'calculation history references each immutable job exactly once')
        job = client.cpm_dev.tests_calculation_jobs_v1.find_one({'_id': doc['calculation_job_id']})
        base.expect(job and job['submission_id'] == doc['submission_id'] and job['division_id'] == doc['division_id']
                    and job['calculation_revision'] == doc['calculation_revision'],
                    'current immutable calculation job belongs to the same submission division and revision')
        base.expect(job['result'] == doc['result'] and job['result_hash'] == doc['result_hash'],
                    'job output and the selected physical session result agree exactly')
        history = doc['calculation_history']
        base.expect(len({item['calculation_revision'] for item in history}) == len(history)
                    and history[-1]['calculation_revision'] == doc['calculation_revision']
                    and history[-1]['score'] == doc['result']['score'],
                    'confirmed calculation audit records the current score once per revision')
    finally:
        client.close()
    return doc


def callback(submission, expected):
    pinned = flow.internal('/internal/tests/submissions/' + submission + '/input')['data']
    return {**expected, 'calculation_revision': pinned['calculation_revision'], 'input_hash': pinned['input_hash'],
            'grading_test_version_id': pinned['grading_test_version_id'],
            'excluded_question_ids': pinned['excluded_question_ids']}


def check_callback(submission, expected):
    payload = callback(submission, expected)
    # The worker's stored result defines its stable result-array ordering.
    payload['question_results'] = record(submission)['result']['question_results']
    path = '/internal/tests/submissions/' + submission + '/result'
    flow.internal(path, 'POST', payload, authorized=False, code=401)
    before = record(submission)
    flow.internal(path, 'POST', payload)
    flow.internal(path, 'POST', payload)
    base.expect(record(submission)['revision'] == before['revision'], 'identical callbacks cannot rewrite or advance the ready session')
    altered = copy.deepcopy(payload)
    altered['earned_points'], altered['score'] = 0, 0
    for question in altered['question_results']:
        question.update(points=0, is_correct=False)
    flow.internal(path, 'POST', altered, code=409)
    flow.internal(path, 'POST', {**payload, 'input_hash': '0' * 64}, code=409)
    flow.internal(path, 'POST', {**payload, 'grading_test_version_id': '0' * 24}, code=409)
    base.expect(record(submission)['result'] == before['result'], 'conflicting calculator identity or output cannot overwrite a ready score')
    return payload


def create(admin, direction, label, questions=None, division=1001):
    return base.keep(base.api(f'/divisions/{division}/tests', admin, 'POST', {
        'title': base.prefix + ' grading ' + label, 'description': 'Legacy scorer integration fixture',
        'direction_id': direction, 'questions': questions if questions is not None else flow.questions(),
    }, 201, base.key()))


def accepted(token, test, answers, division=1001):
    attempt = flow.start(token, test['id'], division)
    sealed = flow.snapshot(attempt, answers)
    value = base.api('/attempts/' + attempt['id'] + '/finalize', token, 'POST', sealed, 202)
    base.expect(value['submission_status'] == 'accepted' and value['division_id'] == division,
                'finalize acknowledges accepted answers in the selected division')
    submission = value['submission_id']
    return submission, canonical_hash(immutable(record(submission))), attempt, sealed


def grade(token, test, questions, answers, division=1001):
    submission, frozen, attempt, sealed = accepted(token, test, answers, division)
    expected = legacy_grade(questions, answers)
    ready = flow.wait_ready(submission, token, expected['score'])
    base.expect(ready['result']['earned_points'] == expected['earned_points'] and ready['result']['max_points'] == expected['max_points'],
                'official result matches the legacy Python scorer')
    assert_db(submission, frozen, expected, division)
    return submission, frozen, expected, attempt, sealed


def edit(admin, test_id, mutation):
    current = base.api('/' + test_id + '/content', admin)
    body = {k: copy.deepcopy(current[k]) for k in ['title', 'description', 'direction_id', 'questions']}
    mutation(body)
    return base.api('/' + test_id + '/content', admin, 'PUT', {'expected_revision': current['revision'], **body})


def wait_change(test_id, batch_id=None):
    def done():
        offset = 0
        while True:
            page = flow.internal('/internal/calculate/messages?test_id=' + test_id + '&limit=100&offset=' + str(offset), origin=flow.CALCULATE)['data']
            for value in page['items']:
                if value['message_type'] != 'tests.content.changed':
                    continue
                detail = flow.internal('/internal/calculate/messages/' + value['message_id'], origin=flow.CALCULATE)['data']
                if batch_id and detail['payload']['after_version_id'] != batch_id:
                    continue
                if detail['status'] in ['ready', 'skipped']:
                    return detail
                if detail['status'] == 'failed':
                    raise RuntimeError('Content worker failed: ' + detail.get('last_error', 'unknown'))
            offset += len(page['items'])
            if not page['items'] or offset >= page['total']:
                break
        return None
    return flow.wait_for(done, 'content event finishes its regrade decision', 60)


def regrade(admin, token, tid, submission, frozen, expected, mutation):
    previous = record(submission)['calculation_revision']
    updated = edit(admin, tid, mutation)
    wait_change(tid, updated['content_version_id'])
    flow.wait_for(lambda: (v := base.api('/submissions/' + submission, token))
                  and v['calculation_status'] == 'ready' and v['calculation_revision'] > previous
                  and v['result']['score'] == expected['score'] and v,
                  'qualifying content edit overwrites the latest calculation revision', 60)
    doc = assert_db(submission, frozen, expected)
    base.expect(doc['calculation_revision'] > previous, 'regrade advances calculation revision monotonically')
    return updated, doc


def no_regrade(admin, tid, submission, mutation):
    before = record(submission)
    updated = edit(admin, tid, mutation)
    wait_change(tid, updated['content_version_id'])
    time.sleep(.8)
    after = record(submission)
    base.expect(after['calculation_revision'] == before['calculation_revision'] and after['result'] == before['result'],
                'nonqualifying content save preserves the current result and calculation revision')
    return updated


def org_setting(admin, enabled):
    current = base.core('/organization/test-settings', admin)
    return base.core('/organization/test-settings', admin, 'PUT', {
        'expected_revision': current['revision'], 'recalculate_on_test_change': enabled})


def publish_envelope(message):
    config = base.settings()
    assert config.get('RABBITMQ_HOST') == '127.0.0.1' and config.get('RABBITMQ_VHOST') == 'cpm_dev'
    authorization = base64.b64encode((config['RABBITMQ_USER'] + ':' + config['RABBITMQ_PASSWORD']).encode()).decode()
    result = base.request('/api/exchanges/cpm_dev/cpm.events/publish', method='POST', body={
        'properties': {'delivery_mode': 2, 'content_type': 'application/json', 'message_id': message['message_id']},
        'routing_key': message['message_type'] + '.v1',
        'payload': json.dumps(message, ensure_ascii=False, separators=(',', ':')), 'payload_encoding': 'string',
    }, headers={'Authorization': 'Basic ' + authorization}, origin='http://127.0.0.1:15672')
    base.expect(result['routed'], 'real test event routes durably through RabbitMQ')


def replay_message(message):
    # Deliberately a fresh transport ID: logical job identity must still deduplicate.
    envelope = {key: message[key] for key in ['message_type', 'schema_version', 'occurred_at', 'producer', 'correlation_id', 'payload']}
    envelope['message_id'] = str(uuid.uuid4())
    publish_envelope(envelope)
    return envelope['message_id']


def original_hashes():
    client = base.mongo_client(base.settings())
    try:
        names = [c['name'] for c in json.loads((ROOT / 'dev/schema/mongo.json').read_text())['collections']]
        return {name: canonical_hash(list(client.cpm_dev[name].find({}).sort('_id', 1))) for name in names}
    finally:
        client.close()


def cleanup_bulk_users():
    if not bulk_users:
        return
    conn = base.mysql_connection(base.settings())
    cur = conn.cursor(dictionary=True)
    try:
        for fixture in bulk_users:
            cur.execute('SELECT ref_id,username,role FROM auth_users WHERE id=%s', (fixture['account_id'],))
            row = cur.fetchone()
            assert row and row['username'] == fixture['login'] and row['username'].startswith('dev_grade_' + base.run + '_') and row['role'] == 'student'
            cur.execute('DELETE t FROM auth_refresh_tokens t JOIN auth_sessions s ON s.id=t.session_id WHERE s.user_id=%s', (fixture['account_id'],))
            cur.execute('DELETE FROM auth_sessions WHERE user_id=%s', (fixture['account_id'],))
            for table in ['core_profile_extra', 'core_proctor_groups', 'auth_division_access', 'auth_organization_access', 'core_user_owners', 'auth_account_state']:
                cur.execute('DELETE FROM ' + table + ' WHERE user_id=%s', (fixture['account_id'],))
            cur.execute('DELETE FROM auth_users WHERE id=%s', (fixture['account_id'],))
            cur.execute('DELETE FROM students WHERE id=%s AND full_name=%s', (row['ref_id'], fixture['full_name']))
            assert cur.rowcount == 1
        conn.commit()
    finally:
        cur.close()
        conn.close()


def paginated_regrade(admin, direction, qs, answers):
    test = flow.publish(admin, create(admin, direction, '52-student regrade pagination'))
    submissions = []
    for index in range(52):
        login = 'dev_grade_' + base.run + '_' + str(index)
        full_name = 'DEV Grade ' + base.run + ' ' + str(index)
        result = base.core('/divisions/1001/users', admin, 'POST', {
            'role': 'student', 'full_name': full_name, 'login': login, 'password': base.password,
            'class': 10, 'school_id': 1001,
        }, 201)
        bulk_users.append({'account_id': result['account_id'], 'login': login, 'full_name': full_name})
        token = base.login(login)
        sid, frozen, _, _ = accepted(token, test, answers)
        submissions.append((sid, frozen))
    initial = legacy_grade(qs, answers)
    physical = [base.ObjectId(sid) for sid, _ in submissions]

    def all_scores(expected):
        client = base.mongo_client(base.settings())
        try:
            count = client.cpm_dev.tests_attempts_v1.count_documents({
                'submission_id': {'$in': physical}, 'test_id': base.ObjectId(test['id']), 'active': True,
                'calculation_status': 'ready', 'result.score': expected['score'],
                'result.max_points': expected['max_points'], 'result.earned_points': expected['earned_points'],
            })
            return count == len(submissions)
        finally:
            client.close()

    flow.wait_for(lambda: all_scores(initial), 'all 52 API-created submissions initially grade correctly', 90)
    expected_qs = copy.deepcopy(qs)
    expected_qs[0]['points'] = 4
    expected = legacy_grade(expected_qs, answers)
    updated = edit(admin, test['id'], lambda b: b['questions'][0].update(points=4))
    event = wait_change(test['id'], updated['content_version_id'])
    base.expect(event['processing_attempts'] >= 2, 'regrade consumes more than one page with a durable inbox checkpoint')
    flow.wait_for(lambda: all_scores(expected), 'all 52 submissions are overwritten after paginated regrade', 90)
    for sid, frozen in submissions:
        assert_db(sid, frozen, expected)
    page1 = base.api('/divisions/1001/submissions?test_id=' + test['id'] + '&limit=50&offset=0', admin)
    page2 = base.api('/divisions/1001/submissions?test_id=' + test['id'] + '&limit=50&offset=50', admin)
    base.expect(page1['total'] == page2['total'] == 52 and len(page1['items']) == 50 and len(page2['items']) == 2,
                'submission pagination reports the selected-division count before taking each page')
    base.expect({v['submission_id'] for v in page1['items']}.isdisjoint({v['submission_id'] for v in page2['items']}),
                'paginated submission pages contain disjoint physical sessions')


def main(faults):
    base.request('/health/ready')
    base.request('/health/ready', origin=flow.CALCULATE)
    baseline = original_hashes()
    admin = base.login('dev_admin')
    student = base.login('dev_student_01')
    student2 = base.login('dev_student_01', 1002)
    other = base.login('dev_student_02')
    foreign = base.login('dev_admin_math', 2001, 2001)
    staff = base.login('dev_staff', 1002)
    original_setting = base.core('/organization/test-settings', admin)['recalculate_on_test_change']
    foreign_setting = base.core('/organization/test-settings', foreign)
    try:
        base.expect(base.core('/organization/test-settings', student)['organization_id'] == 1001,
                    'participant reads only the current organization setting')
        base.core('/organization/test-settings', student, 'PUT', {'expected_revision': 0, 'recalculate_on_test_change': False}, 403)
        base.core('/organization/test-settings', staff, 'PUT', {'expected_revision': 0, 'recalculate_on_test_change': False}, 403)
        org_setting(admin, True)
        base.expect(base.core('/organization/test-settings', foreign) == foreign_setting,
                    'organization-specific setting cannot change another organization')
        direction = base.core('/directions', admin, 'POST', {'name': base.prefix + ' grading direction'}, 201)['id']
        base.directions.append(direction)
        qs = flow.questions()
        fixtures = [
            ('all correct', [{'question_id': 1, 'value': 'a'}, {'question_id': 2, 'value': ['y', 'x']}, {'question_id': 3, 'value': '  мОсКвА  '}]),
            ('wrong single', [{'question_id': 1, 'value': 'b'}, {'question_id': 2, 'value': ['x', 'y']}, {'question_id': 3, 'value': 'MOSCOW'}]),
            ('multiple subset', [{'question_id': 1, 'value': 'a'}, {'question_id': 2, 'value': ['x']}, {'question_id': 3, 'value': 'wrong'}]),
            ('multiple superset', [{'question_id': 1, 'value': 'a'}, {'question_id': 2, 'value': ['x', 'y', 'z']}]),
            ('missing questions', [{'question_id': 3, 'value': ' Moscow '}]),
            ('empty submission', []),
        ]
        for label, answers in fixtures:
            test = flow.publish(admin, create(admin, direction, label))
            grade(student, test, qs, answers)
        decimal_qs = copy.deepcopy(qs[:2])
        decimal_qs[0]['points'], decimal_qs[1]['points'] = 399, 19601
        decimal_test = flow.publish(admin, create(admin, direction, 'decimal tie', decimal_qs))
        decimal = grade(student, decimal_test, decimal_qs, [{'question_id': 1, 'value': 'a'}])
        base.expect(decimal[2]['score'] == 1, 'decimal boundary reproduces int(round(399/20000*100,2)) exactly')
        unicode_qs = [{'question_id': 1, 'type': 'text', 'text': 'Unicode псевдоним', 'points': 2,
                       'answers': [], 'correct_answers': ['İSTANBUL', 'ΟΣ']}]
        unicode_test = flow.publish(admin, create(admin, direction, 'Unicode alias', unicode_qs))
        grade(student, unicode_test, unicode_qs, [{'question_id': 1, 'value': '\u001cİSTANBUL\u001f'}])

        # Legacy addition of even a correct option does not itself trigger a regrade.
        added_correct = flow.publish(admin, create(admin, direction, 'added correct option'))
        all_correct = fixtures[0][1]
        added_sub, added_hash, _, _, _ = grade(student, added_correct, qs, all_correct)
        no_regrade(admin, added_correct['id'], added_sub, lambda b: b['questions'][1]['answers'].append(
            {'id': 'new_correct', 'text': 'Добавленный верный', 'is_correct': True}))
        added_qs = copy.deepcopy(qs)
        added_qs[0]['points'] = 4
        added_qs[1]['answers'].append({'id': 'new_correct', 'text': 'Добавленный верный', 'is_correct': True})
        regrade(admin, student, added_correct['id'], added_sub, added_hash, legacy_grade(added_qs, all_correct),
                lambda b: b['questions'][0].update(points=4))

        source = flow.publish(admin, create(admin, direction, 'regrade policy'))
        tid = source['id']
        source = base.api('/' + tid + '/bank', admin, 'PUT', {'expected_revision': source['revision'], 'available': True})
        source = base.api('/divisions/1002/tests/' + tid + '/attach', admin, 'POST', {'expected_revision': source['revision']})
        source = flow.publish(admin, source, 1002)
        independent = base.keep(base.api('/divisions/1002/tests/' + tid + '/copy', admin, 'POST',
            {'expected_revision': source['revision'], 'title': base.prefix + ' no shared regrades'}, 201, base.key()))
        independent = flow.publish(admin, independent, 1002)
        answers = [{'question_id': 1, 'value': 'b'}, {'question_id': 2, 'value': ['x', 'y']}, {'question_id': 3, 'value': 'Moscow'}]
        submission, frozen, expected, _, _ = grade(student, source, qs, answers)
        original_callback = check_callback(submission, expected)
        history = base.api('/submissions/' + submission + '/calculations?limit=1', admin)
        base.expect(history['total'] == 1 and history['items'][0]['result']['score'] == expected['score'],
                    'administrator reads the confirmed initial result history')
        base.api('/submissions/' + submission + '/calculations', student, code=403)
        base.api('/submissions/' + submission + '/calculations', foreign, code=404)
        submission2, frozen2, _, _, _ = grade(student2, source, qs, answers, 1002)
        copied_sub, copied_hash, copied_expected, _, _ = grade(student2, independent, qs, answers, 1002)

        # Every legacy non-trigger is checked separately rather than hiding one in a large save.
        no_regrade(admin, tid, submission, lambda b: b.update(title=b['title'] + ' renamed'))
        no_regrade(admin, tid, submission, lambda b: b['questions'][0].update(text='Уточнённый текст'))
        no_regrade(admin, tid, submission, lambda b: b['questions'][0]['answers'][0].update(text='Уточнённый вариант'))
        no_regrade(admin, tid, submission, lambda b: b['questions'].reverse())
        no_regrade(admin, tid, submission, lambda b: next(q for q in b['questions'] if q['question_id'] == 3)['correct_answers'].append('Столица РФ'))
        new_question = {'question_id': 4, 'type': 'text', 'text': 'Новый вопрос', 'points': 100,
                        'answers': [], 'correct_answers': ['новый']}
        no_regrade(admin, tid, submission, lambda b: b['questions'].append(copy.deepcopy(new_question)))
        no_regrade(admin, tid, submission, lambda b: next(q for q in b['questions'] if q['question_id'] == 2)['answers'].append(
            {'id': 'extra', 'text': 'Новый неверный', 'is_correct': False}))

        current = copy.deepcopy(qs)
        current[0]['points'] = 4
        expected = legacy_grade(current, answers)
        regrade(admin, student, tid, submission, frozen, expected, lambda b: next(q for q in b['questions'] if q['question_id'] == 1).update(points=4))
        flow.internal('/internal/tests/submissions/' + submission + '/result', 'POST', original_callback, code=409)
        base.expect(record(submission)['result']['max_points'] == 12, 'late initial callback cannot overwrite an already regraded score')
        flow.wait_ready(submission2, student2, expected['score'])
        assert_db(submission2, frozen2, expected, 1002)
        assert_db(copied_sub, copied_hash, copied_expected, 1002)
        base.expect(expected['max_points'] == 12, 'newly added questions never inflate an older submission denominator')
        review = base.api('/submissions/' + submission + '/review', student)
        base.expect({q['question_id'] for q in review['questions']} == {1, 2, 3}
                    and next(q for q in review['questions'] if q['question_id'] == 1)['points'] == 4,
                    'review shows the effective grading version and excludes added unanswered questions')
        regrade(admin, student, tid, submission, frozen, expected,
                lambda b: next(q for q in b['questions'] if q['question_id'] == 2).update(answers=copy.deepcopy(current[1]['answers'])))
        base.expect(True, 'removing an unselected incorrect option also triggers a regrade')

        def key_change(body):
            question = next(q for q in body['questions'] if q['question_id'] == 1)
            question['answers'][0]['is_correct'], question['answers'][1]['is_correct'] = False, True
        current[0]['answers'][0]['is_correct'], current[0]['answers'][1]['is_correct'] = False, True
        expected = legacy_grade(current, answers)
        regrade(admin, student, tid, submission, frozen, expected, key_change)

        # Remove a selected option: its original raw ID must remain, while its points become zero.
        current[1]['answers'] = [a for a in current[1]['answers'] if a['id'] != 'y']
        expected = legacy_grade(current, answers)
        regrade(admin, student, tid, submission, frozen, expected,
                lambda b: next(q for q in b['questions'] if q['question_id'] == 2).update(answers=copy.deepcopy(current[1]['answers'])))
        base.expect(record(submission)['answers'][1]['value'] == ['x', 'y'], 'removed-option regrade preserves the submitted unknown-now option')

        current[2]['correct_answers'] = ['Москва']
        expected = legacy_grade(current, answers)
        regrade(admin, student, tid, submission, frozen, expected,
                lambda b: next(q for q in b['questions'] if q['question_id'] == 3).update(correct_answers=['Москва']))

        # Changed type excludes the old question forever, including a later return to the old type.
        surviving = current[1:]
        expected = legacy_grade(surviving, answers[1:])
        regrade(admin, student, tid, submission, frozen, expected,
                lambda b: next(q for q in b['questions'] if q['question_id'] == 1).update(type='text', answers=[], correct_answers=['b']))
        restored_choices = copy.deepcopy(current[0]['answers'])
        for option in restored_choices:
            option['id'] = 'restored_' + option['id']
        regrade(admin, student, tid, submission, frozen, expected,
                lambda b: next(q for q in b['questions'] if q['question_id'] == 1).update(type='single', answers=restored_choices, correct_answers=[]))
        surviving = current[1:2]
        expected = legacy_grade(surviving, answers[1:2])
        regrade(admin, student, tid, submission, frozen, expected,
                lambda b: b.update(questions=[q for q in b['questions'] if q['question_id'] != 3]))
        expected = {'score': 0, 'earned_points': 0, 'max_points': 0, 'question_results': []}
        regrade(admin, student, tid, submission, frozen, expected,
                lambda b: b.update(questions=[q for q in b['questions'] if q['question_id'] not in [1, 2]]))
        base.expect(record(submission)['result']['max_points'] == 0, 'fully excluded historical submission has a valid zero result')
        history = base.api('/submissions/' + submission + '/calculations?limit=1&offset=0', admin)
        history2 = base.api('/submissions/' + submission + '/calculations?limit=1&offset=1', admin)
        base.expect(history['total'] == history2['total'] and history['total'] > 5
                    and history['items'][0]['calculation']['calculation_revision'] > history2['items'][0]['calculation']['calculation_revision'],
                    'calculation audit paginates confirmed overwrites in descending revision order')

        paginated_regrade(admin, direction, qs, answers)

        if faults:
            # Decisions use the organization flag saved with the edit, even if consumption is delayed.
            disabled_test = flow.publish(admin, create(admin, direction, 'disabled freeze'))
            disabled_sub, disabled_hash, disabled_expected, _, _ = grade(student, disabled_test, qs, answers)
            flow.container('stop', 'calculate')
            try:
                org_setting(admin, False)
                disabled_edit = edit(admin, disabled_test['id'], lambda b: b['questions'][0].update(points=20))
                org_setting(admin, True)
            finally:
                flow.container('start', 'calculate')
            flow.wait_for(lambda: base.request('/health/ready', origin=flow.CALCULATE)['status'], 'calculate restarts after deferred disabled edit', 60)
            wait_change(disabled_test['id'], disabled_edit['content_version_id'])
            assert_db(disabled_sub, disabled_hash, disabled_expected)
            current_disabled = copy.deepcopy(qs)
            current_disabled[0]['points'] = 21
            regrade(admin, student, disabled_test['id'], disabled_sub, disabled_hash, legacy_grade(current_disabled, answers),
                    lambda b: b['questions'][0].update(points=21))

            # Multiple saved revisions wait in Rabbit, then must replay in causal content order.
            burst_test = flow.publish(admin, create(admin, direction, 'deferred ordered edits'))
            burst_sub, burst_hash, _, _, _ = grade(student, burst_test, qs, answers)
            flow.container('stop', 'calculate')
            try:
                edit(admin, burst_test['id'], lambda b: b['questions'][0].update(points=8))
                edit(admin, burst_test['id'], key_change)
                last = edit(admin, burst_test['id'], lambda b: b['questions'][1].update(points=9))
                new_attempt = flow.start(other, burst_test['id'])
                new_accepted = base.api('/attempts/' + new_attempt['id'] + '/finalize', other, 'POST', flow.snapshot(new_attempt, answers), 202)
                base.expect(new_accepted['calculation_status'] == 'queued' and new_accepted['result'] is None,
                            'stopped calculate leaves durable acceptance with no fabricated score')
            finally:
                flow.container('start', 'calculate')
            flow.wait_for(lambda: base.request('/health/ready', origin=flow.CALCULATE)['status'], 'worker resumes durable backlog', 60)
            wait_change(burst_test['id'], last['content_version_id'])
            final_qs = copy.deepcopy(qs)
            final_qs[0]['points'], final_qs[1]['points'] = 8, 9
            final_qs[0]['answers'][0]['is_correct'], final_qs[0]['answers'][1]['is_correct'] = False, True
            final_expected = legacy_grade(final_qs, answers)
            flow.wait_ready(burst_sub, student, final_expected['score'])
            stable = assert_db(burst_sub, burst_hash, final_expected)
            flow.wait_ready(new_accepted['submission_id'], other, final_expected['score'])
            events = [m for m in flow.inbox(test_id=burst_test['id'])['items'] if m['message_type'] == 'tests.content.changed']
            original_event = flow.internal('/internal/calculate/messages/' + events[-1]['message_id'], origin=flow.CALCULATE)['data']
            transport = replay_message(original_event)
            transport_result = flow.wait_for(lambda: (v := flow.internal('/internal/calculate/messages/' + transport, origin=flow.CALCULATE)['data'])
                          and v['status'] in ['ready', 'skipped', 'failed'] and v, 'out-of-order duplicate content body completes safely', 60)
            base.expect(transport_result['status'] == 'failed', 'changing transport identity cannot forge a committed content event')
            time.sleep(1)
            base.expect(record(burst_sub)['calculation_revision'] == stable['calculation_revision'],
                        'old logical content event cannot regress or duplicate a later result')
            assert_db(burst_sub, burst_hash, final_expected)

            # Hold the worker so reset and result completion race on one pending revision.
            reset_test = flow.publish(admin, create(admin, direction, 'reset callback race'))
            flow.container('stop', 'calculate')
            try:
                reset_sub, _, reset_attempt, _ = accepted(other, reset_test, answers)
                reset_payload = callback(reset_sub, legacy_grade(qs, answers))
                state = base.api('/attempts/' + reset_attempt['id'], admin)
                with ThreadPoolExecutor(max_workers=2) as pool:
                    deletion = pool.submit(flow.race_request, '/v1/tests/attempts/' + reset_attempt['id'], admin,
                                           {'expected_revision': state['revision']}, 'DELETE')
                    writing = pool.submit(flow.race_request, '/internal/tests/submissions/' + reset_sub + '/result', None,
                                          reset_payload, 'POST', True)
                    outcomes = [deletion.result(), writing.result()]
                base.expect(sorted(v[0] for v in outcomes) == [200, 409], 'reset and result callback permit exactly one durable revision winner')
                if outcomes[0][0] == 409:
                    state = base.api('/attempts/' + reset_attempt['id'], admin)
                    base.api('/attempts/' + reset_attempt['id'], admin, 'DELETE', {'expected_revision': state['revision']})
                flow.internal('/internal/tests/submissions/' + reset_sub + '/result', 'POST', reset_payload, code=409)
                base.expect(not record(reset_sub)['active'], 'late calculator writes cannot resurrect the reset physical session')
            finally:
                flow.container('start', 'calculate')
            flow.wait_for(lambda: base.request('/health/ready', origin=flow.CALCULATE)['status'], 'calculate restored after reset race', 60)

        base.expect(original_hashes() == baseline, 'all original legacy Mongo documents remain byte-equivalent')
        return {'original_mongo_documents_unchanged': True, 'legacy_oracle': 'cpm_back/services/exam/scoring.py',
                'official_grading_verified': True, 'content_regrades_verified': True,
                'selected_division_verified': True, 'database_overwrites_verified': True}
    finally:
        org_setting(admin, original_setting)


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--skip-faults', action='store_true')
    args = parser.parse_args()
    try:
        summary = main(not args.skip_faults)
    finally:
        try:
            cleanup_bulk_users()
        finally:
            flow.cleanup()
    report = {'status': 'passed', 'checks': len(base.checks), 'scenarios': base.checks,
              'faults_checked': not args.skip_faults, 'production_accessed': False, **summary}
    destination = ROOT / 'dev/local/tests-grading-report.json'
    destination.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
    destination.chmod(0o600)
    print(json.dumps({k: v for k, v in report.items() if k != 'scenarios'}, ensure_ascii=False))
