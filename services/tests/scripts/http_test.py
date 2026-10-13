"""Real HTTP integration against fixed local dev. Isolated synthetic fixtures only."""
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timedelta, timezone
import json
from pathlib import Path
import secrets
import sys
import urllib.error
import urllib.request
from bson import ObjectId

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'dev/scripts'))
from bootstrap import settings, mongo_client, mysql_connection
from export_structure import read_env

TESTS = 'http://127.0.0.1:8004'
AUTH = 'http://127.0.0.1:8002'
CORE = 'http://127.0.0.1:8003'
run = secrets.token_hex(6)
prefix = 'DEV Tests HTTP ' + run
password = json.loads((ROOT / 'dev/local/accounts.json').read_text())['password']
checks, sessions, ids, roles, directions, users = [], [], set(), [], [], []


def expect(condition, label):
    if not condition:
        raise AssertionError(label)
    checks.append(label)


def request(path, token=None, method='GET', body=None, code=200, headers=None, origin=TESTS):
    h = dict(headers or {})
    if token:
        h['Authorization'] = 'Bearer ' + token
    if body is None:
        data = None
    elif isinstance(body, bytes):
        data = body
    else:
        data = json.dumps(body).encode()
        h['Content-Type'] = 'application/json'
    req = urllib.request.Request(origin + path, method=method, data=data, headers=h)
    try:
        with urllib.request.urlopen(req, timeout=40) as res:
            status, raw = res.status, res.read()
    except urllib.error.HTTPError as e:
        status, raw = e.code, e.read()
    expect(status == code, f'{method} {path.split("?")[0]}: expected {code}, got {status}')
    return json.loads(raw)


def api(path, token, method='GET', body=None, code=200, headers=None):
    result = request('/v1/tests' + path, token, method, body, code, headers)
    return result['data'] if code < 300 else result


def core(path, token, method='GET', body=None, code=200):
    result = request('/v1/core' + path, token, method, body, code, origin=CORE)
    return result['data'] if code < 300 else result


def login(name, division=1001, organization=1001):
    result = request('/v1/auth/login', method='POST', body={
        'login': name, 'password': password, 'organization_id': organization, 'division_id': division,
    }, origin=AUTH)
    sessions.append(result['token'])
    return result['token']


def key():
    return {'Idempotency-Key': secrets.token_hex(16)}


def keep(doc):
    ids.add(doc['id'])
    return doc


def questions():
    return [{'question_id': 1, 'type': 'single', 'text': 'Выберите первый вариант', 'points': 2,
             'answers': [{'id': 'a', 'text': 'Первый', 'is_correct': True},
                         {'id': 'b', 'text': 'Второй', 'is_correct': False}], 'correct_answers': []}]


def settings_body(revision, *, published=True, answers=False, delta=0):
    now = datetime.now(timezone.utc)
    return {'expected_revision': revision, 'published': published,
            'start_at': (now - timedelta(hours=1) + timedelta(days=delta)).isoformat(),
            'end_at': (now + timedelta(hours=1) + timedelta(days=delta)).isoformat(),
            'time_limit_minutes': 60, 'passing_score': 50, 'answers_visible': answers}


def cleanup():
    client = mongo_client(settings())
    try:
        client.cpm_dev.tests_catalog_v1.delete_many({'organization_id': 1001, '_id': {'$in': [ObjectId(x) for x in ids]}})
        for name in ['tests_content_versions_v1', 'tests_drafts_v1', 'tests_attempts_v1', 'tests_calculation_jobs_v1']:
            client.cpm_dev[name].delete_many({'test_id': {'$in': [ObjectId(x) for x in ids]}})
        client.cpm_dev.calculate_inbox_v1.delete_many({'test_id': {'$in': list(ids) + [ObjectId(x) for x in ids]}})
    finally:
        client.close()
    conn = mysql_connection(settings())
    cur = conn.cursor(dictionary=True)
    try:
        for uid in users:
            cur.execute('SELECT ref_id,username FROM auth_users WHERE id=%s', (uid,))
            row = cur.fetchone()
            assert row and row['username'] == 'dev_tests_' + run, 'cleanup fixture guard'
            cur.execute('DELETE t FROM auth_refresh_tokens t JOIN auth_sessions s ON s.id=t.session_id WHERE s.user_id=%s', (uid,))
            cur.execute('DELETE FROM auth_sessions WHERE user_id=%s', (uid,))
            for table in ['core_profile_extra', 'core_proctor_groups', 'auth_division_access', 'auth_organization_access', 'core_user_owners', 'auth_account_state']:
                cur.execute('DELETE FROM ' + table + ' WHERE user_id=%s', (uid,))
            cur.execute('DELETE FROM auth_users WHERE id=%s', (uid,))
            cur.execute('DELETE FROM admin_role_users WHERE id=%s', (row['ref_id'],))
        for rid in roles:
            cur.execute('DELETE FROM auth_scoped_role_permissions WHERE role_id=%s', (rid,))
            cur.execute('DELETE FROM auth_scoped_roles WHERE id=%s AND name LIKE %s', (rid, prefix + '%'))
        for did in directions:
            cur.execute('DELETE FROM core_direction_scope WHERE direction_id=%s', (did,))
            cur.execute('DELETE FROM directions WHERE id=%s AND name LIKE %s', (did, prefix + '%'))
        conn.commit()
    finally:
        cur.close()
        conn.close()
    for token in sessions:
        req = urllib.request.Request(AUTH + '/v1/auth/logout', method='POST', headers={'Authorization': 'Bearer ' + token})
        try:
            urllib.request.urlopen(req, timeout=5).close()
        except urllib.error.HTTPError:
            pass


def main():
    request('/health/ready')
    spec = request('/openapi.json')
    expect(sum(len(x) for x in spec['paths'].values()) >= 12, 'public catalogue and workflow operations described')
    for path, ops in spec['paths'].items():
        concrete = path.replace('{division}', '1001').replace('{test}', '0' * 24)
        for method in ops:
            request(concrete, method=method.upper(), body={} if method in ['post', 'put', 'delete'] else None, code=401)
    admin = login('dev_admin')
    math = login('dev_admin_math', 2001, 2001)
    student = login('dev_student_01')
    unprivileged = login('dev_core_staff')
    for name in ['dev_proctor_1', 'dev_examiner_1', 'dev_supervisor']:
        api('/bank', login(name), code=403)
    api('/bank', student, code=403)
    api('/bank', unprivileged, code=403)
    request('/v1/tests/bank', headers={'X-Role': 'admin', 'X-Organization-ID': '1001'}, code=401)

    role_ids = []
    for name, edit in [('viewer', False), ('editor', True)]:
        rid = core('/roles', admin, 'POST', {'name': prefix + ' ' + name,
                    'permissions': {'tests': {'view': True, 'edit': edit}}}, 201)['id']
        roles.append(rid)
        role_ids.append(rid)
    uid = core('/divisions/1001/users', admin, 'POST', {'role': 'staff_admin', 'full_name': 'DEV Tests ' + run,
               'login': 'dev_tests_' + run, 'password': password, 'role_id': role_ids[0]}, 201)['account_id']
    users.append(uid)
    core(f'/divisions/1002/memberships/{uid}', admin, 'PUT', {'role_id': role_ids[1]})
    viewer, editor = login('dev_tests_' + run), login('dev_tests_' + run, 1002)
    direction = core('/directions', admin, 'POST', {'name': prefix + ' direction'}, 201)['id']
    directions.append(direction)
    foreign = core('/directions', math, 'POST', {'name': prefix + ' foreign'}, 201)['id']
    directions.append(foreign)
    expect(direction in [x['id'] for x in core('/divisions/1002/directions', editor)], 'directions shared across organization')
    core('/directions', editor, 'POST', {'name': prefix + ' forbidden'}, 403)
    expect(foreign not in [x['id'] for x in core('/directions', admin)], 'direction organization isolation')

    body = {'title': prefix + ' source', 'description': 'Описание общего теста', 'direction_id': direction}
    path = '/divisions/1001/tests'
    create_key = key()
    create_body = {**body, 'questions': questions()}
    source = keep(api(path, admin, 'POST', create_body, 201, create_key))
    sid = source['id']
    expect(source['revision'] == 1 and not source['bank_available'] and not source['placements'][0]['settings']['published'], 'create is private and unpublished')
    replay = api(path, admin, 'POST', create_body, headers=create_key)
    expect(replay['id'] == sid, 'create replay has same physical id')
    api(path, admin, 'POST', {**create_body, 'title': prefix + ' changed'}, 409, create_key)
    api(path, admin, 'POST', body, 400)
    api(path, admin, 'POST', {**body, 'organization_id': 2001}, 400, key())
    api(path, admin, 'POST', {**body, 'direction_id': foreign}, 404, key())
    api('/divisions/2001/tests', admin, 'POST', body, 404, key())
    api(path, viewer, 'POST', body, 403, key())
    api(path, editor, 'POST', body, 403, key())
    api('/' + sid, math, code=404)
    api('/' + sid, editor, code=404)
    api('/' + sid, student, code=404)
    expect(not api('/bank?q=' + run, editor)['items'], 'private source absent from foreign bank')
    visible = api('/' + sid + '/bank', admin, 'PUT', {'expected_revision': 1, 'available': True})
    rev = visible['revision']
    bank = api('/bank?q=' + run + '&direction_id=' + str(direction), editor)
    expect(len(bank['items']) == 1 and bank['items'][0]['placements'] == [], 'open bank shows metadata without owner settings')
    expect(bank['items'][0]['actions']['can_attach'] and bank['items'][0]['actions']['can_copy'] and not bank['items'][0]['actions']['can_edit_metadata'], 'bank action permissions')
    api('/bank?target_division_id=1001', editor, code=403)
    api('/bank?limit=101', admin, code=400)
    api('/bank?relation=invalid', admin, code=400)
    api('/bank?is_archived=bad', admin, code=400)
    expect(not api('/bank?q=' + run + '&owner_division_id=1001&relation=owned&target_division_id=1002', admin)['items'], 'owner filter and relation are intersected')

    attached_path = '/divisions/1002/tests/' + sid
    attached = api(attached_path + '/attach', editor, 'POST', {'expected_revision': rev})
    rev = attached['revision']
    expect(attached['id'] == sid and len(attached['placements']) == 1 and not attached['placements'][0]['settings']['published'], 'attach same id with only visible local settings')
    expect(api(attached_path + '/attach', editor, 'POST', {'expected_revision': 1})['revision'] == rev, 'repeat attachment does not increment revision')
    api('/' + sid, editor, 'PUT', {'expected_revision': rev, **body}, 403)
    api('/' + sid + '/bank', editor, 'PUT', {'expected_revision': rev, 'available': False}, 403)
    api('/' + sid + '/archive', editor, 'PUT', {'expected_revision': rev, 'is_archived': True}, 403)
    configured = api(attached_path + '/settings', editor, 'PUT', settings_body(rev, answers=True))
    rev = configured['revision']
    full = api('/' + sid, admin)
    expect(len(full['placements']) == 2 and not next(p for p in full['placements'] if p['division_id'] == 1001)['settings']['published'], 'settings do not publish another division')
    api(attached_path + '/settings', viewer, 'PUT', settings_body(rev), 403)
    api('/divisions/1001/tests/' + sid + '/settings', admin, 'PUT', {'expected_revision': rev, 'published': True}, 400)
    api(attached_path + '/settings', editor, 'PUT', settings_body(rev - 1), 409)

    copy_key = key()
    copy_body = {'expected_revision': rev, 'title': prefix + ' copy'}
    copy = keep(api(attached_path + '/copy', editor, 'POST', copy_body, 201, copy_key))
    cid = copy['id']
    expect(cid != sid and copy['owner_division_id'] == 1002 and len(copy['placements']) == 1 and copy['origin']['test_id'] == sid and copy['origin']['source_revision'] == rev, 'independent physical copy with provenance')
    expect(not copy['placements'][0]['settings']['published'] and not copy['placements'][0]['settings']['answers_visible'] and not copy['bank_available'], 'copy resets publication, reviews and bank')
    expect(api(attached_path + '/copy', editor, 'POST', copy_body, headers=copy_key)['id'] == cid, 'copy replay creates no duplicate')
    api(attached_path + '/copy', editor, 'POST', {**copy_body, 'title': prefix + ' wrong'}, 409, copy_key)
    changed = api('/' + sid, admin, 'PUT', {'expected_revision': rev, **body, 'title': prefix + ' updated source'})
    rev = changed['revision']
    expect(api('/' + cid, editor)['title'] == prefix + ' copy', 'source editing does not alter independent copy')
    expect(api(attached_path + '/copy', editor, 'POST', copy_body, headers=copy_key)['id'] == cid, 'copy replay survives source revision change')
    closed = api('/' + sid + '/bank', admin, 'PUT', {'expected_revision': rev, 'available': False})
    rev = closed['revision']
    expect(api('/' + sid, editor)['is_attached'], 'closing bank keeps existing attachment')
    expect(api('/bank?q=' + run + '&relation=attached', editor)['total'] == 1, 'attached filter')
    expect(api('/bank?q=' + run + '&relation=copy', editor)['total'] == 1, 'copy filter')

    owner_path = '/divisions/1001/tests/' + sid
    access = api(owner_path + '/access', student)
    expect(not access['official_access_allowed'] and access['official_reason'] == 'unpublished', 'student access respects own unpublished placement')
    configured = api(owner_path + '/settings', admin, 'PUT', settings_body(rev))
    rev = configured['revision']
    access = api(owner_path + '/access', student)
    expect(access['official_access_allowed'] and not access['review_access_allowed'] and access['scope'] == 'test_content_and_attempt' and access['attempt_state_checked'] and access['content_checked'], 'access checks content, current division and official attempt state')
    student_doc = api('/' + sid, student)
    expect(len(student_doc['placements']) == 1 and not any(student_doc['actions'].values()) and 'questions' not in student_doc and 'answers' not in student_doc, 'student metadata has no foreign placements, actions or content')
    expect(sid in [x['id'] for x in api('/divisions/1001/tests', student)['items']], 'published student catalogue')
    api('/divisions/1002/tests/' + sid + '/access', student, code=403)
    configured = api(owner_path + '/settings', admin, 'PUT', settings_body(rev, answers=True, delta=-2))
    rev = configured['revision']
    access = api(owner_path + '/access', student)
    expect(not access['official_access_allowed'] and access['official_reason'] == 'test_ended' and not access['review_access_allowed'] and access['practice_access_allowed'], 'ended window enables practice but review still requires a submission')

    # Both edits read the same revision. Atomic compare-and-swap must allow exactly one.
    def race_metadata(title):
        req = urllib.request.Request(TESTS + '/v1/tests/' + sid, method='PUT',
            data=json.dumps({'expected_revision': rev, **body, 'title': prefix + title}).encode(),
            headers={'Authorization': 'Bearer ' + admin, 'Content-Type': 'application/json'})
        try:
            with urllib.request.urlopen(req, timeout=30) as res:
                return res.status
        except urllib.error.HTTPError as e:
            return e.code
    with ThreadPoolExecutor(max_workers=2) as pool:
        codes = list(pool.map(race_metadata, [' race A', ' race B']))
    expect(sorted(codes) == [200, 409], 'concurrent edits cannot silently overwrite each other')
    rev = api('/' + sid, admin)['revision']
    concurrent_key = key()
    concurrent_body = {**create_body, 'title': prefix + ' concurrent create'}
    def race_create(_):
        req = urllib.request.Request(TESTS + '/v1/tests' + path, method='POST', data=json.dumps(concurrent_body).encode(),
                headers={'Authorization': 'Bearer ' + admin, 'Content-Type': 'application/json', **concurrent_key})
        with urllib.request.urlopen(req, timeout=30) as res:
            result = json.load(res)['data']
            ids.add(result['id'])
            return res.status, result['id']
    with ThreadPoolExecutor(max_workers=4) as pool:
        results = list(pool.map(race_create, range(4)))
    expect(sorted(x[0] for x in results) == [200, 200, 200, 201] and len({x[1] for x in results}) == 1, 'concurrent create replay stores one document')

    core('/directions/' + str(direction), admin, 'DELETE')
    access = api(owner_path + '/access', student)
    expect(not access['official_access_allowed'] and not access['review_access_allowed'] and access['review_reason'] == 'direction_archived', 'archived direction blocks both modes')
    expect(not api('/' + cid, editor)['actions']['can_copy'], 'inactive direction disables copy action')
    api(attached_path + '/copy', editor, 'POST', {'expected_revision': rev}, 409, key())
    archived = api('/' + sid + '/archive', admin, 'PUT', {'expected_revision': rev, 'is_archived': True})
    rev = archived['revision']
    expect(not api('/' + cid, editor)['is_archived'], 'source archive does not archive copy')
    expect(api(owner_path + '/access', student)['official_reason'] == 'test_archived', 'archived test blocks access')
    api('/' + sid, student, code=404)
    api(owner_path, admin, 'DELETE', {'expected_revision': rev}, 409)
    detached = api(attached_path, editor, 'DELETE', {'expected_revision': rev})
    rev = detached['revision']
    api('/' + sid, editor, code=404)
    expect(api('/' + cid, editor)['id'] == cid, 'detachment preserves independent copy')
    api('/' + sid + '/archive', admin, 'PUT', {'expected_revision': rev, 'is_archived': False})

    service_key = read_env(ROOT / 'dev/.env')['AUTH_INTERNAL_KEY']
    ref_path = '/internal/core/test-references'
    request(ref_path, admin, 'POST', {'organization_id': 1001}, 401, origin=CORE)
    request(ref_path, method='POST', body={'organization_id': 1001, 'division_ids': [1001, 1001]}, code=400, headers={'X-Service-Key': service_key}, origin=CORE)
    request(ref_path, method='POST', body={'organization_id': 1001, 'division_ids': [2001]}, code=404, headers={'X-Service-Key': service_key}, origin=CORE)
    request(ref_path, method='POST', body={'organization_id': 1001, 'direction_id': foreign}, code=404, headers={'X-Service-Key': service_key}, origin=CORE)

    # A live permission change is observed without a token refresh.
    core('/roles/' + str(role_ids[1]), admin, 'PUT', {'name': prefix + ' editor', 'permissions': {'tests': {'view': True, 'edit': False}}})
    api('/' + cid, editor, 'PUT', {'expected_revision': 1, **body}, 403)
    request('/v1/auth/logout', editor, 'POST', origin=AUTH)
    api('/bank', editor, code=401)
    client = mongo_client(settings())
    try:
        indexes = client.cpm_dev.tests_catalog_v1.index_information()
        expect({'organization_catalog', 'division_catalog', 'catalog_filters'} <= set(indexes), 'catalogue indexes installed')
        expect(client.cpm_dev.tests_catalog_v1.count_documents({'_id': {'$in': [ObjectId(x) for x in ids]}}) == len(ids), 'only expected physical documents created')
    finally:
        client.close()


if __name__ == '__main__':
    try:
        main()
    finally:
        cleanup()
    report = {'status': 'passed', 'http_checks': len(checks), 'scenarios': checks, 'target': TESTS,
              'synthetic_records_cleaned': True, 'production_accessed': False}
    p = ROOT / 'dev/local/tests-http-report.json'
    p.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
    p.chmod(0o600)
    print(json.dumps({k: report[k] for k in ['status', 'http_checks', 'synthetic_records_cleaned']}))
