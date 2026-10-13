"""JSON/Online Test Pad contract over fixed loopback dev HTTP; DB reads only.

The full run creates isolated synthetic directions/tests through their owner APIs
and archives them through the same APIs. The focused negative-zero run reuses an
active Tests fixture direction and archives only its own new test. Keeps the
fixtures and audit history for inspection.
Never writes to Mongo/MySQL directly, and never prints credentials/tokens.
"""
from concurrent.futures import ThreadPoolExecutor
from copy import deepcopy
from datetime import datetime, timedelta
import argparse
import json
from pathlib import Path
import secrets
import sys
import urllib.error
import urllib.request
from uuid import uuid4
from zoneinfo import ZoneInfo
from bson import ObjectId

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'dev/scripts'))
from bootstrap import settings, mongo_client
from export_structure import read_env

TESTS = 'http://127.0.0.1:8004'
AUTH = 'http://127.0.0.1:8002'
CORE = 'http://127.0.0.1:8003'
run = secrets.token_hex(6)
prefix = 'DEV JSON Import ' + run
checks, sessions, test_ids = [], [], set()
direction_id, admin = None, None
owns_direction = True
private_headers = {}


def expect(condition, label):
    if not condition:
        raise AssertionError(label)
    checks.append(label)


def request(path, token=None, method='GET', body=None, code=200, headers=None, origin=TESTS):
    h = dict(headers or {})
    if token:
        h['Authorization'] = 'Bearer ' + token
    data = None
    if body is not None:
        data = json.dumps(body, ensure_ascii=False).encode()
        h['Content-Type'] = 'application/json'
    req = urllib.request.Request(origin + path, method=method, data=data, headers=h)
    try:
        with urllib.request.urlopen(req, timeout=40) as response:
            status, raw = response.status, response.read()
    except urllib.error.HTTPError as error:
        status, raw = error.code, error.read()
    acceptable = set(code) if isinstance(code, tuple) else {code}
    expect(status in acceptable, f'{method} {path}: HTTP {status}, expected {sorted(acceptable)}')
    envelope = json.loads(raw)
    if status < 300:
        expect(envelope.get('status') is True and 'data' in envelope, 'successful owner envelope')
        return envelope['data'], status
    return envelope, status


def login(name, division=1001, organization=1001):
    password = json.loads((ROOT / 'dev/local/accounts.json').read_text())['password']
    # Login has the auth envelope rather than the Tests/Core data envelope.
    payload = json.dumps({'login': name, 'password': password,
                          'organization_id': organization, 'division_id': division}).encode()
    req = urllib.request.Request(AUTH + '/v1/auth/login', data=payload,
                                 headers={'Content-Type': 'application/json'}, method='POST')
    with urllib.request.urlopen(req, timeout=40) as response:
        result = json.loads(response.read())
    expect(result.get('status') is True and bool(result.get('token')), 'live dev login')
    sessions.append(result['token'])
    return result['token']


def core(path, method='GET', body=None, code=200):
    return request('/v1/core' + path, admin, method, body, code, origin=CORE)[0]


def preview(body, token=None, private=False, code=200):
    path = '/internal/tests/imports/json/validate' if private else '/v1/tests/divisions/1001/imports/json/preview'
    return request(path, token or admin, 'POST', body, code,
                   private_headers if private else None)[0]


def commit(body, private=False, code=(200, 201)):
    path = '/internal/tests/imports/json/commit' if private else '/v1/tests/divisions/1001/imports/json/commit'
    out, status = request(path, admin, 'POST', body, code, private_headers if private else None)
    if status < 300:
        test_ids.add(out['test']['id'])
    return out, status


def persisted(test_id):
    client = mongo_client(settings())
    try:
        db = client.cpm_dev
        doc = db.tests_catalog_v1.find_one({'_id': ObjectId(test_id), 'organization_id': 1001})
        expect(doc is not None and doc['owner_division_id'] == 1001, 'real catalog document scoped to organization/division')
        expect(len(doc['placements']) == 1 and doc['placements'][0]['division_id'] == 1001,
               'one final placement persisted with catalog')
        version = db.tests_content_versions_v1.find_one({'_id': doc['content_version_id'], 'test_id': doc['_id'], 'organization_id': 1001})
        expect(version is not None and version['questions'] == doc['questions'], 'immutable initial questions match catalog')
        expect(doc['history_version_ids'] == [version['_id']] and bool(doc.get('operation_hash')),
               'history reference and durable command hash persisted')
        expect(db.tests_drafts_v1.count_documents({'test_id': doc['_id']}) == 0, 'JSON import created no draft')
        return doc, db.tests_content_versions_v1.count_documents({'test_id': doc['_id']})
    finally:
        client.close()


def main():
    global admin, direction_id, private_headers
    settings()  # Fixed cpm_dev, loopback ports 13306/27018; reject other contours.
    private_headers = {'X-Service-Key': read_env(ROOT / 'dev/.env')['AUTH_INTERNAL_KEY']}
    with urllib.request.urlopen(TESTS + '/health/ready', timeout=10) as response:
        expect(response.status == 200, 'current dev Tests ready')
    admin = login('dev_admin')
    student = login('dev_student_01')
    staff = login('dev_core_staff')
    foreign = login('dev_admin_math', 2001, 2001)
    direction_name = prefix + ' direction'
    direction_id = core('/directions', 'POST', {'name': direction_name}, 201)['id']
    now = datetime.now(ZoneInfo('Europe/Moscow'))
    payload = {'title': prefix + ' native', 'direction': direction_name,
               'startDate': (now - timedelta(hours=1)).strftime('%Y-%m-%dT%H:%M'),
               'endDate': (now + timedelta(hours=1)).strftime('%Y-%m-%dT%H:%M'),
               'timeLimitMinutes': 30, 'questions': [
        {'questionId': 7, 'type': 'single', 'text': 'Один?', 'points': 2,
         'answers': [{'id': 'a', 'text': 'Да', 'isCorrect': True}, {'id': 'b', 'text': 'Нет', 'isCorrect': False}]},
        {'questionId': 8, 'type': 'multiple', 'text': 'Два?', 'points': 3,
         'answers': [{'id': 'a', 'text': '1', 'isCorrect': True}, {'id': 'b', 'text': '2', 'isCorrect': True}]},
        {'questionId': 9, 'type': 'text', 'text': 'Текст?', 'points': 4, 'correctAnswers': [' ответ ', 42, 'второй']},
    ]}
    body = {'organization_id': 1001, 'division_id': 1001, 'direction_id': direction_id, 'payload': payload}
    public_path = '/v1/tests/divisions/1001/imports/json/preview'
    private_path = '/internal/tests/imports/json/validate'
    request(public_path, method='POST', body=body, code=401)
    request(private_path, method='POST', body=body, code=401, headers=private_headers)
    request(private_path, admin, 'POST', body, 401)
    request('/internal/tests/imports/json/commit', method='POST', body=body, code=401, headers=private_headers)
    request('/internal/tests/imports/json/commit', admin, 'POST', body, 401)
    for token in (student, staff):
        request(public_path, token, 'POST', body, 403)
    request(public_path, foreign, 'POST', body, 404)
    request('/v1/tests/divisions/1002/imports/json/preview', admin, 'POST', body, 403)
    request(public_path, admin, 'POST', {**body, 'division_id': 2001}, 403)
    before = preview(body)
    internal = preview(body, private=True)
    expect(before == internal and before['valid'], 'private and public preview have identical normalized contract')
    expect(before['summary'] == {'questions_total': 3, 'total_points': 9, 'single_count': 1,
                                 'multiple_count': 1, 'text_count': 1, 'errors_count': 0}, 'native summary retains all types and weights')
    expect(before['preview']['settings']['published'] and not before['preview']['settings']['answers_visible'],
           'native published/answers defaults match legacy')
    command = {**body, 'command_id': str(uuid4()), 'preview_hash': before['preview_hash']}
    created, _ = commit(command, private=True, code=201)
    test_id = created['test']['id']
    expect(created['test']['revision'] == 1 and created['test']['question_count'] == 3, 'commit creates complete internal test revision one')
    doc, count = persisted(test_id)
    expect(doc['placements'][0]['settings']['published'] and doc['placements'][0]['settings']['time_limit_minutes'] == 30,
           'final native publication settings persisted on first insert')
    content, _ = request('/v1/tests/' + test_id + '/content', admin)
    expect([q['question_id'] for q in content['questions']] == [7, 8, 9]
           and content['questions'][2]['correct_answers'] == ['ответ', 'второй'], 'content API returns normalized native IDs and aliases')
    replay, _ = commit(command, code=200)
    expect(replay['test']['id'] == test_id and replay['test']['content_version_id'] == created['test']['content_version_id'],
           'public replay of private commit keeps durable catalog and immutable version')
    changed = deepcopy(command)
    changed['payload'] = {'title': False}
    commit(changed, private=True, code=409)
    _, after_count = persisted(test_id)
    expect(after_count == count, 'replay and invalid changed replay create no immutable versions')
    invalid = deepcopy(body)
    invalid['payload']['title'] = ''
    invalid['payload']['published'] = 'yes'
    invalid['payload']['questions'][0]['text'] = ''
    invalid['payload']['direction'] = 'Wrong direction'
    invalid_preview = preview(invalid)
    paths = {x['path'] for x in invalid_preview['errors']}
    expect(not invalid_preview['valid'] and {'title', 'published', 'questions[0].text', 'direction'} <= paths,
           'preview reports all field errors rather than first error')
    commit({**invalid, 'command_id': str(uuid4()), 'preview_hash': invalid_preview['preview_hash']}, code=400)
    source = {'Name': prefix + ' OTP source', 'IsRandomQuestions': True, 'Questions': [
        {'Number': 2, 'AnswerType': 420, 'Text': '<p>Текст &amp; ответ</p>', 'RightScore': 3.5,
         'AnswerTemplates': [{'Number': 1, 'ValueText': 'один <ИЛИ> второй', 'IsRightAnswer': True}]},
        {'Number': 1, 'AnswerType': 10, 'Text': '<b>Первый</b>', 'maxScore': 2.5,
         'AnswerTemplates': [{'Number': 2, 'Text': '<i>Нет</i>', 'Score': 0, 'IsRightAnswer': True},
                             {'Number': 1, 'TextWOHtml': '<b>Да</b>', 'Score': 1, 'IsRightAnswer': False}]},
    ]}
    otp = {**body, 'payload': {'importFormat': 'online_test_pad', 'sourceText': 'var test = ' + json.dumps(source) + ';',
                              'metadata': {'direction': direction_name, 'startDate': payload['startDate'], 'endDate': payload['endDate']}}}
    otp_preview = preview(otp, private=True)
    expect(otp_preview['valid'] and otp_preview['source'] == 'online_test_pad' and len(otp_preview['warnings']) == 1,
           'real test.js conversion retains warnings')
    qs = otp_preview['preview']['questions']
    expect([q['points'] for q in qs] == [2, 4] and qs[0]['text'] == 'Первый'
           and qs[0]['answers'][0]['is_correct'] and not qs[0]['answers'][1]['is_correct']
           and qs[1]['correct_answers'] == ['один', 'второй'], 'OTP sort/HTML/Score priority/bankers weights/text variants match legacy')
    otp_command = {**otp, 'command_id': str(uuid4()), 'preview_hash': otp_preview['preview_hash']}
    with ThreadPoolExecutor(max_workers=2) as executor:
        race = list(executor.map(lambda _: commit(otp_command, private=True), range(2)))
    expect({x[1] for x in race} == {200, 201} and len({x[0]['test']['id'] for x in race}) == 1,
           'concurrent first commit creates one test and one replay')
    otp_doc, otp_count = persisted(race[0][0]['test']['id'])
    expect(not otp_doc['placements'][0]['settings']['published'], 'OTP default creates real unpublished test')
    commit(otp_command, code=200)
    _, later_otp_count = persisted(race[0][0]['test']['id'])
    expect(later_otp_count == otp_count, 'OTP durable replay does not add another content version')
    # A rename between preview and first commit changes the owner-normalized input.
    no_name = deepcopy(body)
    no_name['payload'].pop('direction')
    no_name['payload']['title'] = prefix + ' direction change'
    prior = preview(no_name)
    pending = {**no_name, 'command_id': str(uuid4()), 'preview_hash': prior['preview_hash']}
    core('/directions/' + str(direction_id), 'PUT', {'name': direction_name + ' renamed'})
    commit(pending, code=409)
    refreshed = preview(no_name)
    expect(refreshed['valid'] and refreshed['preview_hash'] != prior['preview_hash'], 'live direction rename invalidates preview hash')
    commit({**pending, 'preview_hash': refreshed['preview_hash']}, private=True, code=201)
    old_receipt, _ = commit(command, private=True, code=200)
    expect(old_receipt['test']['id'] == test_id and old_receipt['test']['direction_name'] == direction_name + ' renamed',
           'durable receipt returns existing test with current core direction name')


def negative_zero():
    """One test proving the legacy negative-zero fix on a rebuilt live service."""
    global admin, direction_id, private_headers, owns_direction
    settings()
    private_headers = {'X-Service-Key': read_env(ROOT / 'dev/.env')['AUTH_INTERNAL_KEY']}
    with urllib.request.urlopen(TESTS + '/health/ready', timeout=10) as response:
        expect(response.status == 200, 'current dev Tests ready for negative-zero regression')
    admin = login('dev_admin')
    owners = json.loads((ROOT / 'dev/local/external-tests-integration-summary.json').read_text())['fixtures']['directions']
    allowed = set(owners)
    active = [item for item in core('/directions') if item['id'] in allowed]
    expect(bool(active), 'reuse an active Tests-owned fixture direction in organization 1001')
    direction_id, direction_name = active[0]['id'], active[0]['name']
    owns_direction = False
    source = {'Name': prefix + ' negative zero', 'Questions': [
        {'Number': 1, 'AnswerType': 410, 'Text': 'Integer negative zero', 'maxScore': 1,
         'AnswerTemplates': [{'ValueInt': '__integer_negative_zero__', 'ValueText': 'integer fallback', 'IsRightAnswer': True}]},
        {'Number': 2, 'AnswerType': 410, 'Text': 'Float negative zero', 'maxScore': 1,
         'AnswerTemplates': [{'ValueFloat': '__float_negative_zero__', 'Text': '<b>0</b>', 'IsRightAnswer': True}]},
        {'Number': 3, 'AnswerType': 410, 'Text': 'Literal string negative zero', 'maxScore': 1,
         'AnswerTemplates': [{'ValueInt': '-0', 'ValueText': 'unused fallback', 'IsRightAnswer': True}]},
    ]}
    # Preserve both original JSON numeric spellings inside test.js. Loading this
    # into Python first would normalize the integer -0 before the HTTP request.
    source_text = json.dumps(source).replace('"__integer_negative_zero__"', '-0').replace('"__float_negative_zero__"', '-0.0')
    now = datetime.now(ZoneInfo('Europe/Moscow'))
    body = {'organization_id': 1001, 'division_id': 1001, 'direction_id': direction_id,
            'payload': {'importFormat': 'online_test_pad', 'sourceText': 'var test = ' + source_text + ';',
                        'metadata': {'direction': direction_name,
                                     'startDate': (now - timedelta(hours=1)).strftime('%Y-%m-%dT%H:%M'),
                                     'endDate': (now + timedelta(hours=1)).strftime('%Y-%m-%dT%H:%M')}}}
    out = preview(body, private=True)
    expected = [['integer fallback'], ['0'], ['-0']]
    expect(out['valid'] and [q['correct_answers'] for q in out['preview']['questions']] == expected,
           'live OTP numeric -0/-0.0 discard zero and use ValueText/HTML; literal string -0 retained')
    result, _ = commit({**body, 'command_id': str(uuid4()), 'preview_hash': out['preview_hash']}, private=True, code=201)
    test_id = result['test']['id']
    doc, _ = persisted(test_id)
    expect([q['correct_answers'] for q in doc['questions']] == expected,
           'numeric negative-zero fallbacks persisted exactly in real catalog')
    content, _ = request('/v1/tests/' + test_id + '/content', admin)
    expect([q['correct_answers'] for q in content['questions']] == expected,
           'content API and immutable initial version expose legacy negative-zero aliases')


def cleanup():
    # Archive through owner APIs; retain isolated audit/history for inspection.
    if admin:
        for test_id in sorted(test_ids):
            doc, _ = request('/v1/tests/' + test_id, admin)
            expect(doc['title'].startswith(prefix), 'cleanup only this synthetic import run')
            if not doc['is_archived']:
                request('/v1/tests/' + test_id + '/archive', admin, 'PUT',
                        {'expected_revision': doc['revision'], 'is_archived': True})
        if direction_id and owns_direction:
            core('/directions/' + str(direction_id), 'DELETE')
    for token in sessions:
        req = urllib.request.Request(AUTH + '/v1/auth/logout', method='POST',
                                     headers={'Authorization': 'Bearer ' + token})
        try:
            urllib.request.urlopen(req, timeout=10).close()
        except urllib.error.HTTPError:
            pass


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--negative-zero-only', action='store_true')
    args = parser.parse_args()
    try:
        negative_zero() if args.negative_zero_only else main()
    finally:
        cleanup()
    report = {'status': 'passed', 'http_checks': len(checks), 'scenarios': checks, 'target': TESTS,
              'test_ids': sorted(test_ids), 'direction_id': direction_id, 'fixtures_archived': True,
              'database_direct_writes': 0, 'production_accessed': False}
    report['negative_zero_only'] = args.negative_zero_only
    report['direction_reused_without_mutation'] = not owns_direction
    filename = 'tests-json-import-negative-zero-http-report.json' if args.negative_zero_only else 'tests-json-import-http-report.json'
    path = ROOT / 'dev/local' / filename
    path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
    path.chmod(0o600)
    print(json.dumps({key: report[key] for key in ('status', 'http_checks', 'fixtures_archived', 'database_direct_writes')}))
