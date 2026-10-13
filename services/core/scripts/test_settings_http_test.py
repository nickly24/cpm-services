"""Verify organization settings and their SQL writes against guarded local dev."""
from concurrent.futures import ThreadPoolExecutor
import json
import sys
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'dev/scripts'))
from bootstrap import settings, mysql_connection
from export_structure import read_env

AUTH = 'http://127.0.0.1:8002'
CORE = 'http://127.0.0.1:8003'
PATH = '/v1/core/organization/test-settings'
checks = []
sessions = []


def expect(condition, name):
    if not condition:
        raise AssertionError(name)
    checks.append(name)


def request(path, token=None, method='GET', body=None, code=200, headers=None, origin=CORE):
    h = dict(headers or {})
    if token:
        h['Authorization'] = 'Bearer ' + token
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        h['Content-Type'] = 'application/json'
    req = urllib.request.Request(origin + path, data=data, method=method, headers=h)
    try:
        with urllib.request.urlopen(req, timeout=20) as response:
            status, raw = response.status, response.read()
    except urllib.error.HTTPError as error:
        status, raw = error.code, error.read()
    expect(status == code, f'{method} {path.split("?")[0]} -> {code} (got {status})')
    result = json.loads(raw)
    return result['data'] if result.get('status') and 'data' in result else result


def login(name, org=1001, division=1001):
    password = json.loads((ROOT / 'dev/local/accounts.json').read_text())['password']
    result = request('/v1/auth/login', method='POST', body={'login': name, 'password': password, 'organization_id': org, 'division_id': division}, origin=AUTH)
    sessions.append(result['token'])
    return result['token'], result['user']


def sql_state(org):
    conn = mysql_connection(settings())
    cur = conn.cursor(dictionary=True)
    try:
        cur.execute('SELECT DATABASE() name')
        expect(cur.fetchone()['name'] == 'cpm_dev', 'guarded local database')
        cur.execute('SELECT organization_id,recalculate_on_test_change,revision,updated_by FROM core_organization_test_settings WHERE organization_id=%s', (org,))
        row = cur.fetchone()
        cur.execute("SELECT COUNT(*) n FROM core_audit WHERE organization_id=%s AND action LIKE 'organization.test-recalculation.%%'", (org,))
        return row, cur.fetchone()['n']
    finally:
        cur.close()
        conn.close()


def payload(current, value):
    return {'expected_revision': current['revision'], 'recalculate_on_test_change': value}


def main():
    admin = None
    original = None
    try:
        request('/health/ready')
        admin, user = login('dev_admin')
        math, _ = login('dev_admin_math', 2001, 2001)
        student, _ = login('dev_student_01')
        viewer, _ = login('dev_core_staff')
        editor, _ = login('dev_core_staff', division=1002)
        original = request(PATH, admin)
        foreign_before = request(PATH, math)
        initial_row, initial_audits = sql_state(1001)
        foreign_row, foreign_audits = sql_state(2001)
        expect(original['organization_id'] == 1001 and foreign_before['organization_id'] == 2001, 'organization follows authenticated context')
        expect(type(original['recalculate_on_test_change']) is bool, 'setting uses boolean DTO')
        if initial_row is None:
            expect(original['revision'] == 0 and original['recalculate_on_test_change'], 'missing row has legacy-compatible true default and revision zero')
        else:
            expect(original['revision'] == initial_row['revision'] and original['recalculate_on_test_change'] == bool(initial_row['recalculate_on_test_change']), 'GET reflects SQL persisted value')
        request(PATH, code=401)
        request(PATH, method='PUT', body=payload(original, False), code=401)
        for actor in [student, viewer, editor]:
            expect(request(PATH, actor) == original, 'organization participants can read setting')
            request(PATH, actor, 'PUT', payload(original, False), 403)
        expect(request(PATH + '?organization_id=2001', admin) == original, 'query cannot select another organization')
        for body in [{}, {'expected_revision': original['revision']}, {'recalculate_on_test_change': False}, {'expected_revision': -1, 'recalculate_on_test_change': False}, {'expected_revision': original['revision'], 'recalculate_on_test_change': None}, {'expected_revision': original['revision'], 'recalculate_on_test_change': 'false'}, {**payload(original, False), 'organization_id': 2001}]:
            request(PATH, admin, 'PUT', body, 400)
        expect(sql_state(1001) == (initial_row, initial_audits), 'denied and invalid requests leave value revision and audit unchanged')
        unchanged = request(PATH, admin, 'PUT', payload(original, original['recalculate_on_test_change']))
        expect(unchanged == original and sql_state(1001) == (initial_row, initial_audits), 'no-op creates neither revision nor audit record')
        changed = request(PATH, admin, 'PUT', payload(original, not original['recalculate_on_test_change']))
        expect(changed['revision'] == original['revision'] + 1 and changed['recalculate_on_test_change'] != original['recalculate_on_test_change'], 'toggle increments revision once')
        row, audits = sql_state(1001)
        expect(row['revision'] == changed['revision'] and bool(row['recalculate_on_test_change']) == changed['recalculate_on_test_change'] and row['updated_by'] == user['account_id'], 'SQL value revision and actor committed together')
        expect(audits == initial_audits + 1, 'toggle and audit committed in same transaction')
        request(PATH, admin, 'PUT', payload(original, original['recalculate_on_test_change']), 409)
        expect(sql_state(1001) == (row, audits), 'stale request does not overwrite database')
        key = read_env(ROOT / 'dev/.env')['AUTH_INTERNAL_KEY']
        internal = '/internal/core/organizations/1001/test-settings'
        request(internal, code=401)
        request(internal, admin, code=401)
        request(internal, headers={'X-Service-Key': 'wrong'}, code=401)
        expect(request(internal, headers={'X-Service-Key': key}) == changed, 'service reads matching frozen value and revision')
        expect(request('/internal/core/organizations/2001/test-settings', headers={'X-Service-Key': key}) == foreign_before, 'service reads requested organization only')
        request('/internal/core/organizations/999999999/test-settings', headers={'X-Service-Key': key}, code=404)
        request('/internal/core/organizations/zero/test-settings', headers={'X-Service-Key': key}, code=400)

        def race():
            h = {'Authorization': 'Bearer ' + admin, 'Content-Type': 'application/json'}
            req = urllib.request.Request(CORE + PATH, data=json.dumps(payload(changed, original['recalculate_on_test_change'])).encode(), method='PUT', headers=h)
            try:
                with urllib.request.urlopen(req, timeout=20) as response:
                    return response.status
            except urllib.error.HTTPError as error:
                return error.code

        with ThreadPoolExecutor(max_workers=2) as pool:
            results = list(pool.map(lambda _: race(), range(2)))
        expect(sorted(results) == [200, 409], 'concurrent first matching revision has exactly one winner')
        restored = request(PATH, admin)
        restored_row, restored_audits = sql_state(1001)
        expect(restored['revision'] == changed['revision'] + 1 and restored['recalculate_on_test_change'] == original['recalculate_on_test_change'], 'race restores original flag with monotonic revision')
        expect(restored_row['revision'] == restored['revision'] and restored_audits == audits + 1, 'race commits one SQL update and one audit only')
        expect(request(PATH, math) == foreign_before and sql_state(2001) == (foreign_row, foreign_audits), 'other organization value revision and audit unchanged')
        report = {'status': 'passed', 'checks': len(checks), 'verification': checks, 'organization_id': 1001, 'final_revision': restored['revision'], 'original_flag_restored': True}
        output = ROOT / 'dev/local/core-test-settings-http-report.json'
        output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
        output.chmod(0o600)
        print(f'Passed {len(checks)} organization-setting HTTP and database checks.')
    finally:
        if admin and original:
            current = request(PATH, admin)
            if current['recalculate_on_test_change'] != original['recalculate_on_test_change']:
                request(PATH, admin, 'PUT', payload(current, original['recalculate_on_test_change']))
        for token in sessions:
            try:
                req = urllib.request.Request(AUTH + '/v1/auth/logout', method='POST', headers={'Authorization': 'Bearer ' + token})
                urllib.request.urlopen(req, timeout=5).close()
            except urllib.error.HTTPError:
                pass


if __name__ == '__main__':
    main()
