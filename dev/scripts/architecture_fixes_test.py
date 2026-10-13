"""Explicit local Dev acceptance and bounded load run, using public BFF APIs.

Never imported by unittest discovery; run with --run. New DEV ARCH fixtures are
kept for browser QA. The load represents submissions, not distinct real users.
"""
import argparse
import concurrent.futures
from datetime import datetime, timezone
import json
from pathlib import Path
import statistics
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'services/bff/scripts'))
from tests_http_test import TestsClient, questions, settings

DOCKER = '/Applications/Docker.app/Contents/Resources/bin/docker'
RUN = datetime.now().strftime('%Y%m%d-%H%M%S') + '-' + uuid.uuid4().hex[:6]
PREFIX = 'DEV ARCH ' + RUN
REPORT = ROOT / 'dev/local/architecture-fixes' / (RUN + '.json')
LOCK = threading.Lock()
TIMINGS = {}
CHECKS = []


class Client(TestsClient):
    def request(self, path, body=None, method=None, code=200, key=None):
        started = time.monotonic()
        try:
            return super().request(path, body, method, code, key)
        finally:
            operation = ('POST' if body is not None else 'GET') if method is None else method
            # Aggregate labels only; neither user identifiers nor cookies enter the report.
            route = 'catalog' if path.startswith('tests/student-catalog') else path.rsplit('/', 1)[-1].split('?')[0]
            route = route if route in {'catalog', 'commits', 'finalize', 'settings', 'attempts', 'login', 'me'} else 'other'
            with LOCK:
                TIMINGS.setdefault(operation + ' ' + route, []).append(time.monotonic() - started)


def check(value, label):
    if not value:
        raise AssertionError(label)
    CHECKS.append(label)


def metrics():
    values = {}
    for port, service in [(8001, 'bff'), (8004, 'tests'), (8005, 'calculate')]:
        with urllib.request.urlopen(f'http://127.0.0.1:{port}/metrics', timeout=8) as response:
            lines = response.read().decode().splitlines()
        values[service] = {line.split(' ')[0]: float(line.rsplit(' ', 1)[1]) for line in lines
                           if line.startswith('cpm_') and not line.startswith('cpm_http')
                           and not line.startswith('cpm_tests_result_latency')}
    return values


def create(admin, label):
    row = admin.request('tests/catalog', {'title': PREFIX + ' · ' + label,
        'description': 'Проверка архитектурных исправлений на Dev', 'direction_id': 1001,
        'questions': questions()}, code=201, key=uuid.uuid4().hex)
    return admin.request('tests/' + row['id'] + '/settings', settings(row['revision']), method='PUT')


def start(student, test):
    return student.request('tests/' + test['id'] + '/attempts', {'practice': False}, code=201)


def commit(student, attempt):
    body = {'commits': [{'commit_id': uuid.uuid4().hex, 'sequence': 1, 'question_id': 1,
                        'value': 'a', 'answered_at': datetime.now(timezone.utc).isoformat()}]}
    ack = student.request('tests/attempts/' + attempt['id'] + '/commits', body)
    check(ack.get('server_answer_count') == 1 and bool(ack.get('server_time')), 'compact commit acknowledgement')
    return body


def final_body(attempt):
    return {'test_version_id': attempt['test_version_id'], 'completed_at': datetime.now(timezone.utc).isoformat(),
            'reason': 'manual', 'answers': [{'question_id': 1, 'value': 'a'},
                {'question_id': 2, 'value': ['x', 'y']}, {'question_id': 3, 'value': 'Москва'}]}


def wait_ready(client, submission, deadline):
    while time.monotonic() < deadline:
        row = client.request('tests/submissions/' + submission)
        if row['calculation_status'] == 'ready':
            check(row['result']['score'] == 100, 'correct grade is 100')
            return row
        if row['calculation_status'] == 'failed':
            raise AssertionError('grade failed')
        time.sleep(.4)
    raise AssertionError('grade timed out')


def main(count, concurrency, restart_bff):
    report = {'run': RUN, 'status': 'running', 'load': {'submissions': count,
        'http_concurrency_limit': concurrency, 'student_accounts': 12}, 'fixtures': []}
    clients = []
    paused = False
    try:
        admin = Client(); admin.login('dev_admin'); clients.append(admin)
        students = []
        for i in range(1, 13):
            client = Client(); client.login(f'dev_student_{i:02}'); client.context(1001, 1001)
            clients.append(client); students.append(client)
        student = students[0]
        report['metrics_before'] = metrics()
        smoke = create(admin, 'Сетевой сбой и повторная отправка')
        manual = create(admin, 'Ручная проверка браузером')
        report['fixtures'].extend([smoke['id'], manual['id']]); report['manual_test_id'] = manual['id']
        attempt = start(student, smoke)
        commit_body = commit(student, attempt)
        student.request('tests/attempts/' + attempt['id'] + '/commits', commit_body)
        student.context(1001, 1002)
        student.request('tests/attempts/' + attempt['id'], code=403)
        student.context(1001, 1001)
        admin.context(1001, 1002)
        admin.request('tests/attempts/' + attempt['id'], code=404)
        admin.context(1001, 1001)
        check(True, 'student and administrator cannot escape selected cabinet')
        body = final_body(attempt)
        wrong = {**body, 'answers': [{**body['answers'][0], 'value': 'b'}, *body['answers'][1:]]}
        student.request('tests/attempts/' + attempt['id'] + '/finalize', wrong, code=409)
        check(True, 'committed answer remains immutable')
        state = subprocess.run([DOCKER, 'inspect', '--format', '{{json .State}}', 'cpm-dev-calculate-1'],
                               check=True, capture_output=True, text=True)
        state = json.loads(state.stdout)
        check(state['Running'] and not state['Paused'], 'local calculate is running before outage test')
        subprocess.run([DOCKER, 'pause', 'cpm-dev-calculate-1'], check=True, capture_output=True); paused = True
        accepted = student.request('tests/attempts/' + attempt['id'] + '/finalize', body, code=202)
        replay = student.request('tests/attempts/' + attempt['id'] + '/finalize', body, code=202)
        check(accepted['submission_id'] == replay['submission_id'], 'duplicate final produces one submission')
        row = student.request('tests/submissions/' + accepted['submission_id'])
        check(row['calculation_status'] == 'queued', 'accepted submission survives calculate outage')
        subprocess.run([DOCKER, 'unpause', 'cpm-dev-calculate-1'], check=True, capture_output=True); paused = False
        wait_ready(student, accepted['submission_id'], time.monotonic() + 90)
        check(True, 'outage recovery delivers and grades accepted submission')
        page = student.request('tests/student-catalog?' + urllib.parse.urlencode({'q': RUN, 'limit': 1}))
        second = student.request('tests/student-catalog?' + urllib.parse.urlencode({'q': RUN, 'limit': 1, 'offset': 1}))
        check(page['total'] == 2 and len(page['items']) == 1 and page['items'][0]['id'] != second['items'][0]['id'], 'server pagination and total')
        compact = json.dumps(page)
        check(all('"' + name + '"' not in compact for name in ['questions', 'answers', 'commits', 'correct_answers']), 'catalog has no question or answer history')
        completed = student.request('tests/student-catalog?' + urllib.parse.urlencode({'q': RUN, 'status': 'completed'}))
        check(completed['total'] == 1 and completed['items'][0]['session']['score'] == 100, 'status filter and score reach catalog')
        # A forged gateway assertion cannot turn a direct owner call into a trusted request.
        request = urllib.request.Request('http://127.0.0.1:8004/v1/tests/divisions/1001/student-catalog',
            headers={'X-CPM-Gateway-Context': 'forged', 'X-CPM-Gateway-Signature': 'forged'})
        try:
            urllib.request.urlopen(request, timeout=5)
            raise AssertionError('forged gateway identity accepted')
        except urllib.error.HTTPError as error:
            check(error.code == 401, 'forged gateway identity is rejected')
        if restart_bff:
            subprocess.run([DOCKER, 'restart', 'cpm-bff-bff-1'], check=True, capture_output=True)
            deadline = time.monotonic() + 40
            while time.monotonic() < deadline:
                try:
                    urllib.request.urlopen('http://127.0.0.1:8001/health/ready', timeout=2).close(); break
                except (urllib.error.URLError, OSError):
                    time.sleep(.5)
            check(student.request('auth/me')['user']['account_id'] > 0, 'same session cookie survives BFF restart')
        print(json.dumps({'stage': 'smoke_passed', 'run': RUN, 'manual_test_id': manual['id']}), flush=True)
        if count:
            with concurrent.futures.ThreadPoolExecutor(max_workers=min(12, concurrency)) as pool:
                tests = list(pool.map(lambda i: create(admin, f'Массовая сдача {i+1:03}'), range(count)))
            report['fixtures'].extend(row['id'] for row in tests)
            with concurrent.futures.ThreadPoolExecutor(max_workers=min(24, concurrency)) as pool:
                attempts = list(pool.map(lambda item: start(students[item[0] % 12], item[1]), enumerate(tests)))
            print(json.dumps({'stage': 'load_fixtures_ready', 'attempts': len(attempts)}), flush=True)
            def submit(item):
                i, attempt = item
                client = students[i % 12]
                commit(client, attempt)
                accepted = client.request('tests/attempts/' + attempt['id'] + '/finalize', final_body(attempt), code=202)
                return i, accepted['submission_id'], time.monotonic()
            started = time.monotonic()
            with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:
                submissions = list(pool.map(submit, enumerate(attempts)))
            report['load']['accepted_seconds'] = round(time.monotonic() - started, 3)
            report['load']['accepted'] = len(submissions)
            report['metrics_after_acceptance'] = metrics()
            def ready(item):
                i, submission, accepted_at = item
                wait_ready(students[i % 12], submission, time.monotonic() + 180)
                return time.monotonic() - accepted_at
            with concurrent.futures.ThreadPoolExecutor(max_workers=min(24, concurrency)) as pool:
                grade_times = list(pool.map(ready, submissions))
            report['load']['ready'] = len(grade_times)
            report['load']['all_ready_seconds'] = round(time.monotonic() - started, 3)
            check(len(grade_times) == count, 'every load submission has a real correct result')
            page = student.request('tests/student-catalog?' + urllib.parse.urlencode({'q': RUN, 'limit': 20}))
            report['catalog_page_bytes'] = len(json.dumps(page, ensure_ascii=False).encode())
            check(page['total'] == count + 2 and len(page['items']) == 20, 'catalog remains paginated after load')
        report['metrics_after'] = metrics()
        report['status'] = 'passed'
    except Exception as error:
        report['status'] = 'failed'
        # Test errors contain only status/error codes, never auth tokens or passwords.
        report['error'] = str(error)
        raise
    finally:
        if paused:
            subprocess.run([DOCKER, 'unpause', 'cpm-dev-calculate-1'], check=True, capture_output=True)
        for client in clients:
            try: client.logout()
            except Exception: pass
        report['checks'] = list(dict.fromkeys(CHECKS))
        for route, times in TIMINGS.items():
            ordered = sorted(times)
            report.setdefault('http_latency_seconds', {})[route] = {
                'count': len(times), 'p50': round(statistics.median(times), 4),
                'p95': round(ordered[max(0, int(.95 * len(ordered)) - 1)], 4),
                'p99': round(ordered[max(0, int(.99 * len(ordered)) - 1)], 4)}
        REPORT.parent.mkdir(parents=True, exist_ok=True)
        REPORT.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
        print(json.dumps({'status': report['status'], 'report': str(REPORT), 'load': report['load']}, ensure_ascii=False), flush=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run', action='store_true', required=True)
    parser.add_argument('--submissions', type=int, default=100)
    parser.add_argument('--concurrency', type=int, default=50)
    parser.add_argument('--restart-bff', action='store_true')
    arguments = parser.parse_args()
    if not 0 <= arguments.submissions <= 450 or not 1 <= arguments.concurrency <= 100:
        parser.error('use 0..450 submissions and 1..100 concurrent HTTP workers')
    main(arguments.submissions, arguments.concurrency, arguments.restart_bff)
