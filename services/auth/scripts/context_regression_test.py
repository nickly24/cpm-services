"""Real local HTTP regression. Own sessions only; no SQL, fixtures, or secret output."""
import argparse
import concurrent.futures
import json
import threading
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
parser = argparse.ArgumentParser()
parser.add_argument('--port', type=int, choices=[8002, 18002], default=8002)
parser.add_argument('--core', action='store_true')
args = parser.parse_args()
assert not args.core or args.port == 8002
password = json.loads((ROOT / 'dev/local/accounts.json').read_text())['password']
checks = []
A = {'organization_id': 1001, 'division_id': 1001}
B = {'organization_id': 1001, 'division_id': 1002}
C = {'organization_id': 2001, 'division_id': 2001}


def request(path, body=None, token=None, port=None):
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    req = urllib.request.Request(f'http://127.0.0.1:{port or args.port}' + path,
                                 data=json.dumps(body).encode() if body is not None else None,
                                 headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=15) as res:
            return res.status, json.load(res)
    except urllib.error.HTTPError as err:
        return err.code, json.load(err)


def expect(condition, name):
    assert condition, name
    checks.append(name)


def ok(path, body=None, token=None):
    code, data = request(path, body, token)
    expect(code == 200, f'{path}: expected 200, got {code}')
    return data


def login(name='dev_student_01'):
    return ok('/v1/auth/login', {'login': name, 'password': password, **A})


def denied(token, name):
    code, _ = request('/v1/auth/introspect', {}, token)
    expect(code == 401, name)
    if args.core:
        code, _ = request('/v1/core/me', token=token, port=8003)
        expect(code == 401, name + ' through core')


for name in ['dev_admin', 'dev_student_01', 'dev_proctor_1', 'dev_examiner_1', 'dev_supervisor', 'dev_core_staff']:
    data = login(name)
    try:
        ok('/v1/auth/me', token=data['access_token'])
        data = ok('/v1/auth/refresh', {'refresh_token': data['refresh_token']})
        ok('/v1/auth/me', token=data['access_token'])
    finally:
        ok('/v1/auth/logout', {}, data['access_token'])
    denied(data['access_token'], name + ' logout revokes access')
expect(request('/v1/auth/login', {'login': 'dev_student_01', 'password': 'intentionally-wrong'})[0] == 401, 'wrong password')

current = login()
refresh = current['refresh_token']
old = [current['access_token']]
try:
    for target in [B, A, C, A, A]:
        current = ok('/v1/auth/context', target, current['access_token'])
        expect('refresh_token' not in current, 'context does not replace refresh')
        for token in old:
            denied(token, 'previous generation stays revoked after return/same-context switch')
        ok('/v1/auth/me', token=current['access_token'])
        old.append(current['access_token'])
    code, _ = request('/v1/auth/context', {'organization_id': 999999, 'division_id': 999999}, current['access_token'])
    expect(code == 403, 'foreign context denied')
    ok('/v1/auth/me', token=current['access_token'])
    current = ok('/v1/auth/refresh', {'refresh_token': refresh})
    expect(current['user']['division_id'] == 1001, 'original refresh survives all switches')
    expect(current['refresh_token'] != refresh, 'refresh rotates')
    for token in old[:-1]:
        denied(token, 'refresh cannot revive replaced contexts')
finally:
    ok('/v1/auth/logout', {}, current['access_token'])

# Two requests bearing the same generation: exactly one transition may commit.
for run in range(12):
    current = login()
    barrier = threading.Barrier(2)
    def switch(target):
        barrier.wait()
        return request('/v1/auth/context', target, current['access_token'])
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        results = list(pool.map(switch, [A, B]))
    winners = [d for code, d in results if code == 200]
    expect(sorted(code for code, _ in results) == [200, 401], 'concurrent switch has one winner')
    winner = winners[0]
    try:
        denied(current['access_token'], 'concurrent switch invalidates original')
        ok('/v1/auth/me', token=winner['access_token'])
    finally:
        ok('/v1/auth/logout', {}, winner['access_token'])

# A concurrent refresh never consumes its token without returning the replacement.
# Its access can be superseded by a later switch; the replacement refresh must recover.
for run in range(12):
    current = login()
    barrier = threading.Barrier(2)
    def race(kind):
        barrier.wait()
        if kind == 'switch':
            return request('/v1/auth/context', B, current['access_token'])
        return request('/v1/auth/refresh', {'refresh_token': current['refresh_token']})
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        switched, refreshed = list(pool.map(race, ['switch', 'refresh']))
    expect(switched[0] == 200 and refreshed[0] == 200, 'refresh and switch both complete')
    current = ok('/v1/auth/refresh', {'refresh_token': refreshed[1]['refresh_token']})
    try:
        expect(current['user']['division_id'] == 1002, 'refresh resolves current context after race')
        ok('/v1/auth/me', token=current['access_token'])
    finally:
        ok('/v1/auth/logout', {}, current['access_token'])

current = login()
old_refresh = current['refresh_token']
current = ok('/v1/auth/refresh', {'refresh_token': old_refresh})
expect(request('/v1/auth/refresh', {'refresh_token': old_refresh})[0] == 401, 'refresh replay rejected')
denied(current['access_token'], 'refresh replay revokes session')
expect(request('/v1/auth/refresh', {'refresh_token': current['refresh_token']})[0] == 401, 'replacement refresh revoked after replay')
report = {'status': 'passed', 'port': args.port, 'core': args.core, 'checks': len(checks), 'cases': checks}
(ROOT / 'dev/local/auth-context-regression-report.json').write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
print(json.dumps({k: v for k, v in report.items() if k != 'cases'}))
