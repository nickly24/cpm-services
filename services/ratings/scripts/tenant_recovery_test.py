"""Coordinated B2 cancellation, public retry, and ratings-only crash acceptance.

Requires root's quiet restart window and --allow-ratings-restart. Reuses the
successful tenant fixture without changing source records, directions, settings,
or its manifest. SQL only reads and locks its two auth rows; Mongo only reads
calculate's durable inbox. Only cpm-dev-ratings-1 may be killed or started.
"""
import argparse
import importlib.util
import json
import re
import secrets
import shutil
import subprocess
import urllib.error
import urllib.request
from pathlib import Path


HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('ratings_recovery_helpers', HERE / 'recovery_test.py')
owner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(owner)
ORIGINS = frozenset('http://127.0.0.1:' + str(port) for port in (8002, 8003, 8004, 8005, 8006, 8010, 8012))
CALCULATE = 'http://127.0.0.1:8005'


class NoRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, target):
        return None


class Recovery(owner.Recovery):
    def __init__(self, run):
        if not re.fullmatch('[a-f0-9]{12}', run):
            raise AssertionError('Only an exact successful synthetic tenant run is allowed')
        self.env = owner.settings()
        self.fixture_path = owner.ROOT / f'dev/local/ratings-tenant-{run}-fixtures.json'
        self.fixture = json.loads(self.fixture_path.read_text())
        candidates = [s for s in self.fixture['scopes'] if s['label'] == 'B2']
        if self.fixture['run_id'] != run or len(candidates) != 1:
            raise AssertionError('The retained B2 fixture identity is invalid')
        self.tenant = candidates[0]
        self.run, self.org, self.division = run, self.tenant['organization_id'], self.tenant['division_id']
        self.accounts = sorted(s['account_id'] for s in self.tenant['students'])
        if self.org != 2001 or self.division < 1 or len(self.accounts) != 2 or len(set(self.accounts)) != 2:
            raise AssertionError('Only the two fresh B2 students in dev organization 2001 may be used')
        self.prefix = 'DEV Tenant Ratings ' + run
        self.docker = shutil.which('docker') or '/Applications/Docker.app/Contents/Resources/bin/docker'
        self.opener = urllib.request.build_opener(NoRedirects())
        self.checks, self.jobs = [], []
        self.token = self.lock_connection = self.read_connection = self.mongo = None
        self.job = self.previous = self.before = self.cached = self.recovery_elapsed = None
        self.killed, self.restarted = False, False
        self.initial_publication = self.cancelled_job = self.retry_job = self.initial_rows = None
        self.watchdog = None
        self.attempt = secrets.token_hex(6)
        self.watchdog_marker = owner.ROOT / f'dev/local/ratings-tenant-recovery-{run}-{self.attempt}.watchdog'
        self.report_path = owner.ROOT / 'dev/local/ratings-tenant-recovery-report.json'
        self.manifest_path = owner.ROOT / f'dev/local/ratings-tenant-recovery-{run}-{self.attempt}-manifest.json'

    def http(self, origin, path, method='GET', body=None, token=None, codes=200, headers=None):
        if origin not in ORIGINS or not path.startswith('/') or path.startswith('//'):
            raise AssertionError('Recovery HTTP is restricted to fixed loopback service origins')
        h = dict(headers or {})
        if token:
            h['Authorization'] = 'Bearer ' + token
        raw = None
        if body is not None:
            raw = json.dumps(body, allow_nan=False).encode()
            h['Content-Type'] = 'application/json'
        if method == 'POST' and (path.endswith('/jobs') or path.endswith('/retry')):
            h['Idempotency-Key'] = self.request_key
        request = urllib.request.Request(origin + path, data=raw, method=method, headers=h)
        try:
            with self.opener.open(request, timeout=20) as response:
                code, payload = response.status, response.read()
        except urllib.error.HTTPError as response:
            code, payload = response.code, response.read()
        accepted = (codes,) if isinstance(codes, int) else tuple(codes)
        if code not in accepted:
            raise AssertionError(f'Recovery HTTP {method} {path.split("?")[0]} expected {accepted}, got {code}')
        try:
            return json.loads(payload)
        except ValueError:
            raise AssertionError('Recovery service returned invalid JSON') from None

    def rating(self, path, method='GET', body=None, codes=200):
        response = self.http(owner.RATINGS, f'/v1/ratings/divisions/{self.division}' + path,
                             method, body, self.token, codes)
        if response.get('status') is True:
            return response['data']
        return response

    def guard_container(self):
        info = json.loads(subprocess.check_output([self.docker, 'inspect', owner.CONTAINER], text=True))[0]
        labels = info['Config']['Labels']
        self.expect(info['Name'] == '/' + owner.CONTAINER and
                    labels.get('com.docker.compose.project') == 'cpm-dev' and
                    labels.get('com.docker.compose.service') == 'ratings' and info['State']['Running'],
                    'Crash target is exactly the currently running ratings container in project cpm-dev')
        return info['Id']

    def guard_quiet_jobs(self, allowed=None):
        jobs = self.sql("SELECT id FROM ratings_jobs_v1 WHERE status IN "
                        "('preparing','waiting_sources','queued','running','validating')")
        actual = {j['id'] for j in jobs}
        self.expect(actual == (set() if allowed is None else {allowed}),
                    'The coordinated restart window has no competing active ratings jobs')

    def prepare(self):
        self.guard_container()
        for origin in sorted(ORIGINS):
            self.expect(self.http(origin, '/health/ready').get('status') is True,
                        'Every live source and worker is ready before the recovery window')
        health = self.http(owner.RATINGS, '/health/ready')['data']
        self.expect(health.get('exams_mode') == 'required' and health.get('external_tests_mode') == 'required',
                    'Recovery exercises both real required rating sources')
        self.read_connection = owner.mysql_connection(self.env)
        self.read_connection.autocommit = True
        self.mongo = owner.mongo_client(self.env)
        rows = self.sql('SELECT a.id,a.username,m.organization_id,m.division_id FROM auth_users a '
                        'JOIN auth_division_access m ON m.user_id=a.id WHERE a.id IN (%s,%s) '
                        "AND a.role='student' AND m.organization_id=%s AND m.division_id=%s",
                        (*self.accounts, self.org, self.division))
        expected_logins = {s['login'] for s in self.tenant['students']}
        self.expect(len(rows) == 2 and {r['id'] for r in rows} == set(self.accounts) and
                    {r['username'] for r in rows} == expected_logins and
                    all(r['username'].startswith('dev_ratings_tenant_' + self.run + '_') for r in rows),
                    'Only both exact synthetic B2 auth accounts and memberships may be held')
        divisions = self.sql('SELECT d.id,d.name FROM divisions d JOIN organizations o ON o.id=d.organization_id '
                             'WHERE d.id=%s AND d.organization_id=%s AND d.is_active=1 AND o.is_active=1',
                             (self.division, self.org))
        self.expect(len(divisions) == 1 and divisions[0]['name'] == self.prefix + ' B2',
                    'The exact retained B2 division belongs to its active dev organization')
        self.guard_quiet_jobs()
        scope = self.scope()
        self.expect(scope['active_job_id'] is None and scope['published_job_id'] is not None,
                    'B2 starts with a complete publication and no active calculation')
        self.initial_publication = self.previous = scope['published_job_id']
        jobs = self.sql("SELECT id,revision,DATE_FORMAT(date_from,'%Y-%m-%d') date_from,"
                       "DATE_FORMAT(date_to,'%Y-%m-%d') date_to,snapshot FROM ratings_jobs_v1 "
                       "WHERE id=%s AND organization_id=%s AND division_id=%s AND status='completed'",
                       (self.previous, self.org, self.division))
        self.expect(len(jobs) == 1, 'The initial publication is a completed version in this exact B2 scope')
        snapshot = json.loads(jobs[0]['snapshot'])
        self.expect(snapshot['exams_mode'] == 'required' and snapshot['external_tests_mode'] == 'required',
                    'The retained baseline was calculated using both required owners')
        self.period = {key: jobs[0][key] for key in ('date_from', 'date_to')}
        password = json.loads((owner.ROOT / 'dev/local/accounts.json').read_text())['password']
        response = self.http(owner.AUTH, '/v1/auth/login', 'POST', {
            'login': 'dev_admin_math', 'password': password,
            'organization_id': self.org, 'division_id': self.division})
        self.token = response['token']
        self.expect(response['user']['organization_id'] == self.org and
                    response['user']['division_id'] == self.division and response['user'].get('organization_admin'),
                    'Only the verified organization 2001 owner creates recovery jobs')
        listing = self.rating('/ratings')
        self.expect(listing['freshness'].get('reason', '') in ('', 'source_changed') and
                    listing['freshness']['version_id'] == self.previous and
                    {r['profile']['account_id'] for r in listing['ratings']} == set(self.accounts),
                    'Recovery starts from a complete owned publication; catalog additions may mark its immutable history stale')
        self.baseline_was_stale = listing['freshness']['is_stale']
        self.initial_rows = self.business_rows(listing['ratings'])
        self.persist()

    @staticmethod
    def business_rows(rows):
        return {r['profile']['account_id']: {'profile': r['profile'], 'place': r['place'],
                **{key: r['rating'][key] for key in ('final_rating', 'homework', 'tests', 'exams')}} for r in rows}

    def persist(self):
        owner.private_json(self.manifest_path, {'run_id': self.run, 'organization_id': self.org,
            'division_id': self.division, 'accounts': self.accounts, 'period': getattr(self, 'period', None),
            'jobs': self.jobs, 'initial_publication': self.initial_publication})

    def hold_accounts(self):
        self.expect(self.lock_connection is None, 'No second holding transaction is allowed')
        self.lock_connection = owner.mysql_connection(self.env)
        self.lock_connection.start_transaction(isolation_level='READ COMMITTED')
        cursor = self.lock_connection.cursor()
        try:
            cursor.execute('SELECT DATABASE()')
            self.expect(cursor.fetchone()[0] == 'cpm_dev', 'The held transaction can target only the private cpm_dev database')
            cursor.execute("SELECT id,username FROM auth_users WHERE id IN (%s,%s) AND role='student' ORDER BY id FOR UPDATE",
                           tuple(self.accounts))
            rows = cursor.fetchall()
            self.expect([r[0] for r in rows] == self.accounts and
                        {r[1] for r in rows} == {s['login'] for s in self.tenant['students']},
                        'Only the two exact owned B2 rows block callback history locks')
        finally:
            cursor.close()

    def release_accounts(self):
        if self.lock_connection is not None:
            try:
                self.lock_connection.rollback()
            finally:
                self.lock_connection.close()
                self.lock_connection = None

    def create_job(self, scenario):
        self.request_key = secrets.token_hex(16)
        self.job = self.rating('/jobs', 'POST', self.period, 202)['job']
        self.expect(self.job['status'] != 'completed', 'The request stores a new asynchronous B2 job')
        self.jobs.append({'id': self.job['id'], 'revision': self.job['revision'],
                          'idempotency_key': self.request_key, 'scenario': scenario})
        self.persist()

    def wait_completed(self):
        def complete():
            current = self.job_state()
            if current['status'] in ('failed', 'cancelled', 'superseded'):
                raise AssertionError('Owned retry job became terminal: ' + current['error_code'])
            return current if current['status'] == 'completed' else None
        job = self.wait(complete, 'Public retry fully publishes its new independent revision', 120)
        self.expect(job['processed_count'] == job['total_count'] == 2,
                    'The retry contains exactly both complete student results')
        return job

    def cancel_and_retry(self):
        self.hold_accounts()
        self.create_job('cancel_cached_callbacks')
        job, _, docs = self.wait(self.staged_and_cached,
            'Confirmed commands cache both outputs before either callback can be accepted', 50)
        cancelled_output = {d['account_id']: owner.output_bytes(d['output']) for d in docs}
        cancelled = self.rating('/jobs/' + self.job['id'] + '/cancel', 'POST', {})
        self.cancelled_job = self.job['id']
        self.expect(cancelled['status'] == 'cancelled' and cancelled['processed_count'] == 0,
                    'Public cancellation commits while both cached callbacks are held')
        scope = self.scope()
        self.expect(scope['active_job_id'] is None and scope['published_job_id'] == self.initial_publication,
                    'Cancellation clears the active job and preserves the previous publication')
        self.release_accounts()
        for account, raw in cancelled_output.items():
            response = self.http(owner.RATINGS,
                f'/internal/ratings/jobs/{self.cancelled_job}/accounts/{account}/result', 'POST', json.loads(raw),
                codes=409, headers={'X-Service-Key': self.env['AUTH_INTERNAL_KEY']})
            self.expect(response.get('error') == 'stale_rating_job',
                        'A late exact cached callback for the cancelled job is rejected as stale')
        def discarded():
            inbox = self.inbox()
            return inbox if len(inbox) == 2 and all(d['status'] == 'skipped' for d in inbox) else None
        skipped = self.wait(discarded, 'Both actual cancelled deliveries become skipped in durable calculate inbox', 60)
        self.expect(all(d.get('last_error') == 'rating_job_cancelled_or_superseded' for d in skipped) and
                    self.job_state()['processed_count'] == 0,
                    'The real worker rejects cancellation without recording accepted progress')
        self.expect(self.business_rows(self.rating('/ratings')['ratings']) == self.initial_rows,
                    'Cancellation and late deliveries leave the previous visible scores unchanged')
        self.request_key = secrets.token_hex(16)
        retried = self.rating('/jobs/' + self.cancelled_job + '/retry', 'POST', {}, 202)
        replay = self.rating('/jobs/' + self.cancelled_job + '/retry', 'POST', {})
        self.expect(replay['id'] == retried['id'] and retried['id'] != self.cancelled_job and
                    retried['revision'] > job['revision'], 'Public retry is idempotent and creates a strictly newer job revision')
        self.job = retried
        self.retry_job = retried['id']
        self.jobs.append({'id': retried['id'], 'revision': retried['revision'],
                          'idempotency_key': self.request_key, 'scenario': 'public_retry'})
        self.persist()
        self.wait_completed()
        listing = self.rating('/ratings')
        self.expect(listing['freshness']['version_id'] == self.retry_job and
                    listing['freshness']['is_stale'] is False and
                    self.business_rows(listing['ratings']) == self.initial_rows,
                    'Public retry advances publication without changing any source-derived scores')
        versions = {j['id'] for j in self.rating('/versions')['versions']}
        self.expect(self.initial_publication in versions and self.retry_job in versions and
                    self.cancelled_job not in versions, 'The retry preserves old history and excludes cancelled staging from versions')
        self.previous = self.retry_job

    def arm_watchdog(self, container_id):
        # This separate process can restore only the inspected container after
        # 60 seconds even if the test process is killed. It has no credentials.
        self.watchdog_marker.write_text('armed\n')
        self.watchdog_marker.chmod(0o600)
        source = """import json, pathlib, subprocess, sys, time
docker, name, expected, marker = sys.argv[1:]
time.sleep(60)
if pathlib.Path(marker).exists():
    try:
        info = json.loads(subprocess.check_output([docker,'inspect',name], text=True))[0]
        labels = info['Config']['Labels']
        if info['Id'] == expected and info['Name'] == '/' + name and labels.get('com.docker.compose.project') == 'cpm-dev' and labels.get('com.docker.compose.service') == 'ratings' and not info['State']['Running']:
            subprocess.run([docker,'start',name], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    except Exception:
        pass
"""
        self.watchdog = subprocess.Popen([str(owner.ROOT / 'dev/.venv/bin/python'), '-c', source,
            self.docker, owner.CONTAINER, container_id, str(self.watchdog_marker)],
            stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)

    def crash(self):
        self.guard_quiet_jobs()
        self.hold_accounts()
        self.create_job('real_ratings_crash')
        self.wait(self.staged_and_cached, 'Crash begins only after durable outputs and zero accepted callbacks', 50)
        self.guard_quiet_jobs(self.job['id'])
        container_id = self.guard_container()
        self.arm_watchdog(container_id)
        # Existing reviewed helper kills/starts only ratings, releases our held
        # transaction, observes real callback retries, and compares immutable
        # inputs and byte-identical cached outputs across the process crash.
        super().crash_and_resume()
        listing = self.rating('/ratings')
        self.expect(listing['freshness']['version_id'] == self.job['id'] and
                    listing['freshness']['is_stale'] is False and
                    self.business_rows(listing['ratings']) == self.initial_rows,
                    'The recovered publication retains both original business results')
        versions = {v['id'] for v in self.rating('/versions')['versions']}
        self.expect({self.initial_publication, self.retry_job, self.job['id']} <= versions and
                    self.cancelled_job not in versions, 'Crash recovery preserves both prior complete versions and excludes cancellation')

    def finish(self, error):
        try:
            self.release_accounts()
        except Exception:
            error = AssertionError('Owned held SQL transaction could not be released')
        if self.killed and not self.restarted:
            try:
                subprocess.run([self.docker, 'start', owner.CONTAINER], check=True, capture_output=True, text=True)
                self.restarted = True
            except Exception:
                error = AssertionError('Only ratings could not be restarted; watchdog remains armed')
        if not self.killed or self.restarted:
            self.watchdog_marker.unlink(missing_ok=True)
            if self.watchdog is not None and self.watchdog.poll() is None:
                self.watchdog.terminate()
        if self.token:
            try:
                self.http(owner.AUTH, '/v1/auth/logout', 'POST', token=self.token, codes=(200, 401))
            except Exception:
                pass
        if self.mongo is not None:
            self.mongo.close()
        if self.read_connection is not None:
            self.read_connection.close()
        self.persist()
        report = {'status': 'failed' if error else 'passed', 'checks': len(self.checks), 'scenarios': self.checks,
            'run_id': self.run, 'organization_id': self.org, 'division_id': self.division,
            'container': owner.CONTAINER, 'only_ratings_restarted': True,
            'ratings_restart_completed': self.restarted, 'production_accessed': False,
            'source_records_changed': False, 'source_settings_changed': False,
            'artificial_lease_expiry': False, 'fixtures_retained': True,
            'initial_publication': self.initial_publication, 'cancelled_job': self.cancelled_job,
            'public_retry_job': self.retry_job, 'recovery_job': self.job['id'] if self.killed and self.job else None,
            'recovery_seconds': self.recovery_elapsed, 'jobs': self.jobs,
            'exams_mode': 'required', 'external_tests_mode': 'required',
            'initial_publication_was_stale': getattr(self, 'baseline_was_stale', None),
            'cancelled_calculate_status': 'skipped', 'watchdog_seconds': 60}
        if self.before:
            report['source_hash'] = self.before['source_hash']
            report['input_hashes'] = {str(a): v['input_hash'] for a, v in self.before['inputs'].items()}
        if error:
            report['failure'] = str(error) if isinstance(error, AssertionError) else type(error).__name__
        owner.private_json(self.report_path, report)
        print(json.dumps({key: report[key] for key in ('status','checks','ratings_restart_completed','recovery_seconds')}))
        return error


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--fixture-run-id', default='10d3deb9e73c')
    parser.add_argument('--allow-ratings-restart', action='store_true',
                        help='Use only after root approves the quiet window for cpm-dev-ratings-1.')
    args = parser.parse_args()
    if not args.allow_ratings_restart:
        parser.error('--allow-ratings-restart is required; coordinate the quiet window with root first')
    recovery, error = None, None
    try:
        recovery = Recovery(args.fixture_run_id)
        recovery.prepare()
        recovery.cancel_and_retry()
        recovery.crash()
    except Exception as failure:
        error = failure
    finally:
        if recovery is not None:
            error = recovery.finish(error)
    if error:
        raise SystemExit('Tenant recovery acceptance failed; see private local report.')


if __name__ == '__main__':
    main()
