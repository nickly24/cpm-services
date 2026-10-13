"""Real ratings crash and cached-callback recovery on retained local fixtures.

Requires --allow-ratings-restart. Only cpm-dev-ratings-1 is killed/restarted.
Owner services, calculate, RabbitMQ and databases keep running. No lease expiry,
fake clock, result mutation, cleanup or deletion of permanent rating locks.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'dev/scripts'))
from bootstrap import settings, mysql_connection, mongo_client

AUTH = 'http://127.0.0.1:8002'
RATINGS = 'http://127.0.0.1:8012'
CONTAINER = 'cpm-dev-ratings-1'


def private_json(path, value):
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(descriptor, 'w') as output:
        os.fchmod(output.fileno(), 0o600)
        json.dump(value, output, ensure_ascii=False, indent=2, allow_nan=False)
        output.write('\n')


def json_hash(value):
    if isinstance(value, (bytes, str)):
        value = json.loads(value)
    encoded = json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=False, allow_nan=False).encode()
    return hashlib.sha256(encoded).hexdigest()


def output_bytes(value):
    if isinstance(value, str):
        return value.encode()
    if isinstance(value, (bytes, bytearray)):
        return bytes(value)
    raise AssertionError('Calculate cached output must be persisted JSON bytes')


class Recovery:
    def __init__(self):
        self.env = settings()
        self.fixture_path = ROOT / 'dev/local/ratings-fixtures.json'
        self.fixture = json.loads(self.fixture_path.read_text())
        self.run = self.fixture['run_id']
        self.org, self.division = self.fixture['organization_id'], self.fixture['division_id']
        self.accounts = sorted([self.fixture['student_id'], self.fixture['other_id']])
        if not re.fullmatch('[a-f0-9]{12}', self.run) or self.org != 1001 or self.division < 1 or len(set(self.accounts)) != 2:
            raise AssertionError('Retained fixture identity is invalid')
        self.docker = shutil.which('docker') or '/Applications/Docker.app/Contents/Resources/bin/docker'
        self.checks = []
        self.token = None
        self.lock_connection = None
        self.read_connection = None
        self.mongo = None
        self.killed = False
        self.restarted = False
        self.job = None
        self.previous = None
        self.before = None
        self.cached = None
        self.recovery_elapsed = None

    def expect(self, condition, label):
        if not condition:
            raise AssertionError(label)
        self.checks.append(label)

    def http(self, origin, path, method='GET', body=None, token=None, codes=200):
        if origin not in {AUTH, RATINGS}:
            raise AssertionError('Recovery API origin must be fixed loopback')
        headers = {}
        if token:
            headers['Authorization'] = 'Bearer ' + token
        raw = None
        if body is not None:
            raw = json.dumps(body, allow_nan=False).encode()
            headers['Content-Type'] = 'application/json'
        if path.endswith('/jobs') and method == 'POST':
            headers['Idempotency-Key'] = self.request_key
        req = urllib.request.Request(origin + path, data=raw, method=method, headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=20) as response:
                code, raw = response.status, response.read()
        except urllib.error.HTTPError as response:
            code, raw = response.code, response.read()
        accepted = (codes,) if isinstance(codes, int) else tuple(codes)
        if code not in accepted:
            raise AssertionError(f'Recovery HTTP {method} {path} expected {accepted}, got {code}')
        return json.loads(raw)

    def wait(self, condition, label, timeout):
        until = time.monotonic() + timeout
        while time.monotonic() < until:
            result = condition()
            if result:
                self.expect(True, label)
                return result
            time.sleep(.2)
        raise AssertionError(label + ': timed out')

    def sql(self, query, args=()):
        cursor = self.read_connection.cursor(dictionary=True)
        try:
            cursor.execute(query, args)
            return cursor.fetchall()
        finally:
            cursor.close()

    def scope(self):
        rows = self.sql('SELECT revision,active_job_id,published_job_id FROM ratings_scopes_v1 '
                        'WHERE organization_id=%s AND division_id=%s', (self.org, self.division))
        if len(rows) != 1:
            raise AssertionError('Retained division has no ratings scope')
        return rows[0]

    def job_state(self):
        rows = self.sql('SELECT id,revision,status,source_hash,snapshot,total_count,processed_count,error_code '
                        'FROM ratings_jobs_v1 WHERE id=%s AND organization_id=%s AND division_id=%s',
                        (self.job['id'], self.org, self.division))
        if len(rows) != 1:
            raise AssertionError('Recovery job disappeared or left its scope')
        return rows[0]

    def inputs(self):
        return self.sql('SELECT account_id,input_hash,input,status,output_hash,output FROM ratings_items_v1 '
                        'WHERE job_id=%s ORDER BY account_id', (self.job['id'],))

    def inbox(self):
        return list(self.mongo.cpm_dev.calculate_ratings_inbox_v1.find({
            'job_id': self.job['id'], 'organization_id': self.org, 'division_id': self.division,
            'account_id': {'$in': self.accounts}}, {'body': 0}))

    def prepare(self):
        info = json.loads(subprocess.check_output([self.docker, 'inspect', CONTAINER], text=True))[0]
        self.expect(info['Name'] == '/' + CONTAINER and info['Config']['Labels'].get('com.docker.compose.project') == 'cpm-dev'
                    and info['Config']['Labels'].get('com.docker.compose.service') == 'ratings'
                    and info['State']['Running'], 'Crash target is exactly the running dev ratings container')
        self.read_connection = mysql_connection(self.env)
        self.read_connection.autocommit = True  # Every observation must see current commits.
        self.mongo = mongo_client(self.env)
        rows = self.sql('SELECT a.id,a.username,m.organization_id,m.division_id FROM auth_users a '
                        'JOIN auth_division_access m ON m.user_id=a.id '
                        "WHERE a.id IN (%s,%s) AND a.role='student' AND m.organization_id=%s AND m.division_id=%s",
                        (*self.accounts, self.org, self.division))
        self.expect(len(rows) == 2 and {r['id'] for r in rows} == set(self.accounts)
                    and all(r['username'].startswith('dev_ratings_' + self.run + '_') for r in rows),
                    'Row locks are limited to this run synthetic student accounts and memberships')
        divisions = self.sql('SELECT d.id,d.name FROM divisions d JOIN organizations o ON o.id=d.organization_id '
                             'WHERE d.id=%s AND d.organization_id=%s AND d.is_active=1 AND o.is_active=1',
                             (self.division, self.org))
        self.expect(len(divisions) == 1 and divisions[0]['name'] == 'DEV Ratings ' + self.run,
                    'Retained run-labelled division belongs to the expected active organization')
        current = self.scope()
        self.expect(current['published_job_id'] is not None and current['active_job_id'] is None,
                    'Recovery starts from a published version with no competing job')
        self.previous = current['published_job_id']
        old = self.sql("SELECT id FROM ratings_jobs_v1 WHERE id=%s AND organization_id=%s AND division_id=%s AND status='completed'",
                       (self.previous, self.org, self.division))
        self.expect(len(old) == 1, 'Earlier publication is a complete scoped version')
        password = json.loads((ROOT / 'dev/local/accounts.json').read_text())['password']
        login = self.http(AUTH, '/v1/auth/login', 'POST', {'login': 'dev_admin', 'password': password,
                          'organization_id': self.org, 'division_id': 1001})
        self.token = login['token']
        self.expect(login['user']['role'] == 'admin' and login['user']['organization_id'] == self.org,
                    'Recovery request uses a verified organization admin')
        self.lock_connection = mysql_connection(self.env)
        self.lock_connection.start_transaction(isolation_level='READ COMMITTED')
        cursor = self.lock_connection.cursor()
        try:
            cursor.execute('SELECT DATABASE()')
            self.expect(cursor.fetchone()[0] == 'cpm_dev', 'Held transaction is confined to cpm_dev')
            cursor.execute("SELECT id,username FROM auth_users WHERE id IN (%s,%s) AND role='student' AND username LIKE %s ORDER BY id FOR UPDATE",
                           (*self.accounts, 'dev_ratings_' + self.run + '_%'))
            locked = cursor.fetchall()
            self.expect(len(locked) == 2 and [r[0] for r in locked] == self.accounts,
                        'Both owned accounts are locked before the first result can be stored')
        finally:
            cursor.close()
        self.request_key = secrets.token_hex(16)
        created = self.http(RATINGS, f'/v1/ratings/divisions/{self.division}/jobs', 'POST',
                            self.fixture['period'], self.token, 202)['data']['job']
        self.job = created
        self.expect(created['status'] != 'completed', 'Recovery creates a stored asynchronous job while callbacks are held')
        self.fixture['jobs'].append({'id': created['id'], 'division_id': self.division,
                                     'idempotency_key': self.request_key, 'scenario': 'real_recovery'})
        private_json(self.fixture_path, self.fixture)

    def staged_and_cached(self):
        job = self.job_state()
        if job['status'] in ('failed', 'cancelled', 'superseded'):
            raise AssertionError('Held recovery job became terminal: ' + job['error_code'])
        rows = self.inputs()
        outbox = self.sql('SELECT message_id,delivered_at FROM ratings_outbox_v1 WHERE job_id=%s', (self.job['id'],))
        docs = self.inbox()
        if (len(rows) != 2 or len(outbox) != 2 or not all(r['delivered_at'] for r in outbox)
                or len(docs) != 2 or not all(d.get('output') for d in docs)):
            return None
        self.expect({r['account_id'] for r in rows} == set(self.accounts) and all(r['status'] == 'queued' for r in rows),
                    'Held core authorization locks prevent either staged result from being saved')
        self.expect(self.scope()['published_job_id'] == self.previous and job['processed_count'] == 0,
                    'The prior publication remains visible while both callbacks are held')
        return job, rows, docs

    def crash_and_resume(self):
        job, rows, docs = self.wait(self.staged_and_cached,
                                   'Both confirmed broker commands have durable calculate output before callbacks', 50)
        self.before = {'source_hash': job['source_hash'], 'snapshot_hash': json_hash(job['snapshot']),
                       'inputs': {r['account_id']: {'input_hash': r['input_hash'], 'body_hash': json_hash(r['input'])} for r in rows}}
        self.cached = {d['account_id']: {'message_id': d['_id'], 'output': output_bytes(d['output']),
                                       'input_hash': d['input_hash']} for d in docs}
        self.expect(all(self.cached[a]['input_hash'] == self.before['inputs'][a]['input_hash'] for a in self.accounts),
                    'Durable output is tied to the immutable input hash for each student')
        self.killed = True
        subprocess.run([self.docker, 'kill', CONTAINER], check=True, capture_output=True, text=True)
        self.lock_connection.rollback()
        self.lock_connection.close()
        self.lock_connection = None
        self.expect(self.scope()['published_job_id'] == self.previous,
                    'Killing only ratings before releasing locks preserves the published pointer')
        def retrying_cached():
            retrying = self.inbox()
            if len(retrying) != 2 or not all(d.get('status') == 'retrying' for d in retrying):
                return None
            return retrying if all(output_bytes(d.get('output')) == self.cached[d['account_id']]['output'] for d in retrying) else None
        self.wait(retrying_cached, 'Calculate retains the same cached outputs while the ratings callback is unavailable', 20)
        self.expect(self.scope()['published_job_id'] == self.previous,
                    'Offline callback retries cannot partially publish a rating')
        resumed = time.monotonic()
        subprocess.run([self.docker, 'start', CONTAINER], check=True, capture_output=True, text=True)
        self.restarted = True
        def healthy():
            try:
                response = self.http(RATINGS, '/health/ready')
                return response if response.get('status') is True else None
            except (OSError, urllib.error.URLError, AssertionError):
                return None
        self.wait(healthy, 'The restarted ratings service becomes ready', 30)
        def completed():
            current = self.job_state()
            if current['status'] in ('failed', 'cancelled', 'superseded'):
                raise AssertionError('Recovery failed: ' + current['error_code'])
            return current if current['status'] == 'completed' else None
        after = self.wait(completed, 'The same stored job resumes and publishes after a real process crash', 120)
        self.recovery_elapsed = round(time.monotonic() - resumed, 3)
        self.expect(after['id'] == self.job['id'] and after['total_count'] == after['processed_count'] == 2,
                    'Recovery completes the original job and all of its participants')
        self.expect(after['source_hash'] == self.before['source_hash'] and json_hash(after['snapshot']) == self.before['snapshot_hash'],
                    'Recovery uses the original owner snapshot without recollecting or changing it')
        for row in self.inputs():
            before = self.before['inputs'][row['account_id']]
            self.expect(row['input_hash'] == before['input_hash'] and json_hash(row['input']) == before['body_hash']
                        and row['status'] == 'ready', 'Staged immutable input survives the crash and receives its cached result')
            self.expect(json_hash(row['output']) == json_hash(self.cached[row['account_id']]['output']),
                        'Ratings stores the same complete result that calculate cached before the crash')
        def all_delivered():
            records = self.inbox()
            return records if len(records) == 2 and all(d['status'] == 'ready' for d in records) else None
        delivered = self.wait(all_delivered, 'Both cached callbacks become durably accepted', 30)
        self.expect(all(d['processing_attempts'] >= 2 for d in delivered), 'Real failed deliveries produce retry attempts')
        for d in delivered:
            cached = self.cached[d['account_id']]
            self.expect(d['_id'] == cached['message_id'] and output_bytes(d['output']) == cached['output']
                        and d['input_hash'] == cached['input_hash'], 'Retry reuses the exact saved output and message identity')
        self.expect(self.scope()['published_job_id'] == self.job['id'], 'Publication atomically advances to the recovered version')
        self.expect(len(self.sql("SELECT id FROM ratings_jobs_v1 WHERE id=%s AND organization_id=%s AND division_id=%s AND status='completed'",
                                 (self.previous, self.org, self.division))) == 1,
                    'The earlier complete version remains available as history')

    def finish(self, error):
        if self.lock_connection is not None:
            try:
                self.lock_connection.rollback()
            except Exception:
                self.checks.append('Held SQL transaction rollback could not be acknowledged')
            try:
                self.lock_connection.close()
            except Exception:
                self.checks.append('Held SQL connection closure could not be acknowledged')
            self.lock_connection = None
        if self.killed and not self.restarted:
            try:
                subprocess.run([self.docker, 'start', CONTAINER], check=True, capture_output=True, text=True)
                self.restarted = True
            except Exception:
                self.checks.append('Ratings restart failed in finally; user action is required')
        if self.token:
            try:
                self.http(AUTH, '/v1/auth/logout', 'POST', token=self.token, codes=(200, 401))
            except Exception:
                pass
        if self.mongo is not None:
            self.mongo.close()
        if self.read_connection is not None:
            self.read_connection.close()
        report = {'status': 'failed' if error else 'passed', 'checks': len(self.checks), 'scenarios': self.checks,
                  'run_id': self.run, 'container': CONTAINER, 'only_ratings_restarted': True,
                  'ratings_restart_completed': self.restarted, 'production_accessed': False,
                  'artificial_lease_expiry': False, 'fixtures_retained': True,
                  'job_id': self.job['id'] if self.job else None, 'previous_version_id': self.previous,
                  'recovery_seconds': self.recovery_elapsed}
        if self.before:
            report['source_hash'] = self.before['source_hash']
            report['input_hashes'] = {str(k): v['input_hash'] for k, v in self.before['inputs'].items()}
        if error:
            report['failure'] = str(error) if isinstance(error, AssertionError) else type(error).__name__
        private_json(ROOT / 'dev/local/ratings-recovery-report.json', report)
        print(json.dumps({k: report[k] for k in ['status', 'checks', 'ratings_restart_completed', 'recovery_seconds']}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--allow-ratings-restart', action='store_true',
                        help='Explicitly allow kill/start of only cpm-dev-ratings-1.')
    if not parser.parse_args().allow_ratings_restart:
        parser.error('--allow-ratings-restart is required for this real crash test')
    recovery = Recovery()
    error = None
    try:
        recovery.prepare()
        recovery.crash_and_resume()
    except Exception as failure:
        error = failure
    finally:
        recovery.finish(error)
    if error:
        raise SystemExit('Ratings recovery checks failed; see private local report.')


if __name__ == '__main__':
    main()
