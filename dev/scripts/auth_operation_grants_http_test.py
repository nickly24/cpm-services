"""Opt-in Auth-only Dev HTTP proof; no native apply, account edits or broker calls."""
import argparse
import datetime as dt
import hashlib
import http.client
import json
import sys
import time
import uuid
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'services/auth/scripts'))
from run_operation_profile import credentials
sys.path.insert(0, str(ROOT / 'dev/scripts'))
from export_structure import read_env


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run', action='store_true', help='Run only in a newly ACKed Auth Dev window')
    args = parser.parse_args()
    if not args.run:
        print('Auth grant HTTP proof NOT RUN; frozen Auth deployment and coordinated window required.')
        return
    keys = credentials(ROOT / 'dev/local/import-operation-credentials.env')
    password = json.loads((ROOT / 'dev/local/accounts.json').read_text())['password']
    legacy_key = read_env(ROOT / 'dev/local/auth.env')['AUTH_INTERNAL_KEY']
    checks, grants, tokens = [], [], []
    actor = None
    active = None

    def request(path, expected, body=None, key=None, bearer=None, issuance=None, raw=None, extra=()):
        data = raw if raw is not None else json.dumps(body, separators=(',', ':')).encode() if body is not None else None
        headers = [('Content-Type', 'application/json')]
        if key:
            headers.append(('X-Service-Key', key))
        if bearer:
            headers.append(('Authorization', 'Bearer ' + bearer))
        if issuance:
            headers.append(('Idempotency-Key', issuance))
        connection = http.client.HTTPConnection('127.0.0.1', 8002, timeout=10)
        try:
            connection.putrequest('POST' if data is not None else 'GET', path)
            for name, value in headers + list(extra):
                connection.putheader(name, value)
            if data is not None:
                connection.putheader('Content-Length', str(len(data)))
            connection.endheaders(data)
            response = connection.getresponse()
            raw_response = response.read()
            if response.status != expected:
                raise AssertionError('unexpected Auth HTTP status')
            value = json.loads(raw_response)
            checks.append({'path': path.split('?')[0].replace(next((g for g in grants if g in path), '<none>'), '{grant}'), 'http_status': response.status})
            return value
        finally:
            connection.close()

    def login():
        value = request('/v1/auth/login', 200, {'login': 'dev_admin', 'password': password,
                       'organization_id': 1001, 'division_id': 1001})
        tokens.append(value['access_token'])
        return value

    def digest(data):
        return hashlib.sha256(data).hexdigest()

    def binding(deadline):
        return {'contract_version': 1, 'upload_id': uuid.uuid4().hex, 'upload_session_id': uuid.uuid4().hex,
            'job_id': uuid.uuid4().hex, 'coordinator_command_key': str(uuid.uuid4()),
            'type': 'users.credentials', 'owner': 'core', 'native_command_key': str(uuid.uuid4()),
            'owner_session_id': '9000000000000000001', 'upload_preview_revision': '1',
            'owner_preview_revision': None, 'target_revision': None,
            'target': {'kind': 'division_users', 'division_id': '1001'},
            'source_sha256': digest(b'synthetic Auth-only test; never native source'),
            'owner_proof': {'scheme': 'core.credentials.snapshot.v1', 'validation_sha256': digest(b'Auth-only validation'),
                            'input_sha256': digest(b'Auth-only input'), 'mapping_sha256': None},
            'owner_request_sha256': digest(b'Auth-only receipt'), 'apply_wire_body_sha256': digest(b'{}'),
            'preview_wire_body_sha256': None, 'upload_expires_at': deadline,
            'owner_preview_expires_at': None, 'required_division_ids': ['1001']}

    def issue(value, issuance):
        result = request('/internal/auth/operation-grants', 200, value,
                         keys['AUTH_OPERATIONS_ISSUER_KEY'], active, issuance)
        if result['grant_id'] not in grants:
            grants.append(result['grant_id'])
        if result['actor']['id'] != actor['id'] or result['actor']['account_id'] != actor['account_id']:
            raise AssertionError('profile/account identity changed')
        if result['binding'] != value:
            raise AssertionError('immutable binding changed')
        return result

    def stage(value, kind):
        base = '/v1/core/divisions/1001/imports/' + value['owner_session_id']
        return {'contract_version': 1, 'job_id': value['job_id'], 'stage': kind,
                'method': 'POST' if kind == 'apply' else 'GET',
                'path': base + '/commit' if kind == 'apply' else base + '/receipts/' + value['native_command_key'],
                'raw_query': '', 'force_query': False, 'wire_body_sha256': digest(b'{}' if kind == 'apply' else b''),
                'native_command_key': value['native_command_key']}

    result = {'status': 'not_completed', 'checks': checks, 'native_apply_calls': 0, 'broker_calls': 0,
              'account_permission_mutations': 0, 'scope': 'Auth HTTP only; synthetic grant metadata; own sessions'}
    try:
        request('/health/ready', 200)
        contract = request('/openapi.json', 200)
        expected_contract = json.loads((ROOT / 'services/auth/internal/auth/openapi.json').read_text())
        if contract != expected_contract:
            raise AssertionError('served Auth source contract differs from frozen source')
        current = login()
        active, actor = current['access_token'], current['user']
        deadline = (dt.datetime.now(dt.timezone.utc) + dt.timedelta(minutes=10)).isoformat(timespec='microseconds').replace('+00:00', 'Z')
        value, issuance = binding(deadline), str(uuid.uuid4())
        original = issue(value, issuance)
        replay = issue(value, issuance)
        if (replay['grant_id'], replay['expires_at']) != (original['grant_id'], original['expires_at']):
            raise AssertionError('lost reply issuance identity changed')
        changed = {**value, 'source_sha256': digest(b'changed')}
        request('/internal/auth/operation-grants', 409, changed, keys['AUTH_OPERATIONS_ISSUER_KEY'], active, issuance)
        path = '/internal/auth/operation-grants/' + original['grant_id']
        apply = stage(value, 'apply')
        owner = keys['AUTH_OPERATIONS_CORE_KEY']
        request(path + '/authorize', 200, apply, owner)
        request(path + '/authorize', 401, apply, keys['AUTH_OPERATIONS_ISSUER_KEY'])
        request(path + '/authorize', 401, apply, legacy_key)
        request(path + '/authorize', 404, apply, keys['AUTH_OPERATIONS_TESTS_KEY'])
        request(path + '/authorize', 401, apply, owner, active)
        request(path + '/authorize', 401, apply, owner, extra=[('X-Service-Key', owner)])
        request(path + '/authorize?', 400, apply, owner)
        request(path + '/authorize', 409, {**apply, 'wire_body_sha256': digest(b'{}\n')}, owner)
        request(path + '/authorize', 409, {**apply, 'raw_query': 'division_id=1002'}, owner)
        request(path + '/authorize', 400, {**apply, 'Force_query': False}, owner)
        # Switching a browser context never retargets the original grant.
        switched = request('/v1/auth/context', 200, {'organization_id': 1001, 'division_id': 1002}, bearer=active)
        active = switched['access_token']; tokens.append(active)
        request(path + '/authorize', 200, apply, owner)
        request(path + '/recover', 404, key=keys['AUTH_OPERATIONS_ISSUER_KEY'], bearer=active,
                issuance=str(uuid.uuid4()), raw=b'{}')
        request('/v1/auth/logout', 200, {}, bearer=active)
        request(path + '/authorize', 200, apply, owner)
        current = login(); active = current['access_token']
        request(path + '/revoke', 200, key=keys['AUTH_OPERATIONS_ISSUER_KEY'], bearer=active, raw=b'{}')
        request(path + '/revoke', 200, key=keys['AUTH_OPERATIONS_ISSUER_KEY'], bearer=active, raw=b'{}')
        request(path + '/authorize', 410, apply, owner)
        recover_key = str(uuid.uuid4())
        recovered = request(path + '/recover', 200, key=keys['AUTH_OPERATIONS_ISSUER_KEY'], bearer=active,
                            issuance=recover_key, raw=b'{}')
        grants.append(recovered['grant_id'])
        again = request(path + '/recover', 200, key=keys['AUTH_OPERATIONS_ISSUER_KEY'], bearer=active,
                        issuance=recover_key, raw=b'{}')
        if recovered['mode'] != 'receipt' or recovered['binding'] != value or again != recovered:
            raise AssertionError('receipt recovery binding/identity changed')
        recovery_path = '/internal/auth/operation-grants/' + recovered['grant_id']
        request(recovery_path + '/authorize', 403, apply, owner)
        request(recovery_path + '/authorize', 200, stage(value, 'receipt'), owner)
        short = binding((dt.datetime.now(dt.timezone.utc) + dt.timedelta(seconds=1)).isoformat(timespec='microseconds').replace('+00:00', 'Z'))
        expired = issue(short, str(uuid.uuid4()))
        time.sleep(1.2)
        request('/internal/auth/operation-grants/' + expired['grant_id'] + '/authorize', 410, stage(short, 'apply'), owner)
        result['status'] = 'passed'
    finally:
        if active:
            for grant in grants:
                request('/internal/auth/operation-grants/' + grant + '/revoke', 200,
                        key=keys['AUTH_OPERATIONS_ISSUER_KEY'], bearer=active, raw=b'{}')
            request('/v1/auth/logout', 200, {}, bearer=active)
        result['checks_count'] = len(checks)
        output = ROOT / 'dev/local/auth-operation-grants-http-report.json'
        output.write_text(json.dumps(result, indent=2) + '\n'); output.chmod(0o600)
    print(json.dumps({'status': result['status'], 'checks': len(checks), 'native_apply_calls': 0}))


if __name__ == '__main__':
    main()
