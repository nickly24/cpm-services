"""Live maintenance regression checks using only owned synthetic fixtures.

Only fixed local cpm_dev and the explicitly configured isolated S3 fixture are
accepted. Global settings and service states are never modified. The narrow
lease checks use deletion jobs only. Two additional scenarios create an owned
student/type/assignment, process a real PDF, then verify reference protection,
reuse of a completed deletion tombstone and cancellation's actual upload grace.
--business-only completes those scenarios without rerunning earlier lease tests.
"""
import argparse
from datetime import datetime, timedelta, timezone
import json
from pathlib import Path
import sys
import time
import urllib.error
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'dev/scripts'))
from bootstrap import mysql_connection, settings
from export_structure import read_env


class MaintenanceSuite:
    def __init__(self, business_only=False):
        self.env = settings()
        local = read_env(ROOT / 'dev/local/homework-compose.env')
        fixture_flag = local.get('HOMEWORK_S3_TEST_FIXTURE')
        if (not (fixture_flag is True or fixture_flag == 'true')
                or local.get('HOMEWORK_S3_PUBLIC_ENDPOINT_URL') != 'http://127.0.0.1:19000'
                or local.get('HOMEWORK_S3_BUCKET') != 'cpm-homework-test'):
            raise AssertionError('Maintenance checks require the isolated local S3 fixture.')
        self.connection = mysql_connection(self.env)
        self.connection.autocommit = True
        self.cursor = self.connection.cursor(dictionary=True)
        self.cursor.execute('SELECT DATABASE() AS name')
        if self.cursor.fetchone()['name'] != 'cpm_dev':
            raise AssertionError('Only the fixed local cpm_dev database is permitted.')
        self.run = str(uuid.uuid4())
        self.owned = []
        self.cases = []
        self.business = None
        self.business_keys = set()
        self.business_only = business_only
        self.prior_cases = []
        if business_only:
            prior_path = ROOT / 'dev/local/homework-maintenance-report.json'
            if prior_path.exists():
                prior = json.loads(prior_path.read_text())
                # Keep the genuinely executed initial lease/scheduler results
                # with explicit provenance, never count a skip as a pass.
                if prior.get('success') and prior.get('scope') == 'Only three owned hw_delete_jobs rows; fictitious non-existing S3 keys.':
                    self.prior_cases = [{**case, 'carried_forward': True,
                                         'executed_at_utc': prior.get('finished_at_utc')}
                                        for case in prior.get('cases', []) if case.get('passed')]
        self.started = datetime.now(timezone.utc)
        self.ttl = None

    def business_checks(self):
        # Import helper classes only; neither module's complete suite is run.
        from workflow_test import WorkflowSuite, pdf
        self.wait_http('http://127.0.0.1:8006/health/ready')
        self.wait_http('http://127.0.0.1:19000/minio/health/ready')
        s = self.business = WorkflowSuite()
        s.prefix = 'DEV Homework Maint ' + s.run
        s.admin, _ = s.login('dev_admin')
        student, actor, _ = s.fixture_student()
        kind = s.type()
        assignment, _ = s.assignment(kind['id'])
        assignment_id = assignment['id']
        print('Owned student/assignment ready; processing one PDF for reference protection.', flush=True)
        upload, workspace = s.process(assignment_id, student, pdf(label='Maintenance owned PDF'))
        draft = workspace['submission']['draft_file']
        self.check('real PDF processing attaches an owned draft', workspace['submission']['state'] == 'draft' and draft is not None)
        job = s.row('SELECT * FROM hw_jobs WHERE id=%s', (upload['job']['id'],))
        file = s.row('SELECT * FROM hw_files WHERE id=%s', (draft['id'],))
        self.check('reference fixture belongs to own assignment and student',
                   job['assignment_id'] == assignment_id and job['account_id'] == actor['account_id']
                   and file['submission_id'] == job['submission_id'])
        self.business_keys.update([job['source_key'], job['output_key']])
        s.signed_s3('HEAD', file['object_key'])
        self.cursor.execute("SELECT COUNT(*) AS count FROM hw_delete_jobs WHERE object_key=%s", (file['object_key'],))
        self.check('live PDF has no previous deletion task', self.cursor.fetchone()['count'] == 0)
        self.cursor.execute("INSERT INTO hw_delete_jobs(object_key,status,attempts,available_at) "
                            "VALUES(%s,'queued',0,DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 1 SECOND))", (file['object_key'],))
        referenced = {'id': self.cursor.lastrowid, 'key': file['object_key']}
        self.owned.append(referenced)
        retained = self.wait_status(referenced, 'done', timeout=40)
        self.check('referenced live PDF deletion is marked done without storage removal', retained['status'] == 'done')
        self.check('reference protection performs no S3 deletion attempt', retained['attempts'] == 0,
                   attempts=retained['attempts'])
        self.check('reference protection retains the homework file row',
                   s.row('SELECT id FROM hw_files WHERE id=%s AND object_key=%s', (file['id'], file['object_key'])) is not None)
        s.signed_s3('HEAD', file['object_key'])
        self.check('referenced PDF is physically retained in S3', True)

        s.api(f'/assignments/{assignment_id}/draft', student, 'DELETE')
        revived = self.row(referenced)
        self.check('draft removal revives the same completed tombstone',
                   revived['status'] in ('queued', 'running', 'done'), observed_status=revived['status'])
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline:
            deleted = self.row(referenced)
            if deleted['status'] == 'done' and deleted['attempts'] >= 1:
                break
            time.sleep(1)
        self.check('revived completed tombstone makes a real deletion attempt',
                   deleted['status'] == 'done' and deleted['attempts'] >= 1, attempts=deleted['attempts'])
        s.signed_s3('HEAD', file['object_key'], expected=(404,))
        self.check('removing draft physically deletes the formerly protected PDF', True)
        self.check('draft file row is removed after explicit API removal',
                   s.row('SELECT id FROM hw_files WHERE id=%s', (file['id'],)) is None)

        print('Reference/tombstone checks passed; checking actual cancel grace without complete.', flush=True)
        cancelled = s.new_upload(assignment_id, student)
        before = s.row('SELECT * FROM hw_jobs WHERE id=%s', (cancelled['job']['id'],))
        self.business_keys.update([before['source_key'], before['output_key']])
        self.check('cancel fixture is still uploading and has never completed',
                   before['status'] == 'uploading' and before['source_size'] == 0)
        policy = json.loads(before['settings_json'])
        self.ttl = int(policy['presign_ttl_seconds'])
        s.api('/jobs/' + before['id'] + '/cancel', student, 'POST', {})
        after = s.row('SELECT * FROM hw_jobs WHERE id=%s', (before['id'],))
        tombstone = s.row('SELECT * FROM hw_delete_jobs WHERE object_key=%s', (before['source_key'],))
        self.check('cancel without complete sets a terminal cancelled job', after['status'] == 'cancelled')
        self.check('actual cancel queues a source cleanup tombstone', tombstone is not None and tombstone['status'] == 'queued')
        self.check('cancel cleanup waits at least the original signed upload lifetime',
                   tombstone['available_at'] >= before['updated_at'] + timedelta(seconds=self.ttl),
                   presign_ttl_seconds=self.ttl)
        self.check('cancel cleanup preserves the lifetime from cancellation as well',
                   tombstone['available_at'] >= after['updated_at'] + timedelta(seconds=self.ttl))
        self.check('cancel staging tombstone has no premature attempt or lease',
                   tombstone['attempts'] == 0 and tombstone['lease_until'] is None)

    def check(self, name, condition, **observations):
        self.cases.append({'name': name, 'passed': bool(condition), **observations})
        if not condition:
            raise AssertionError(name)

    @staticmethod
    def wait_http(url, timeout=120):
        if url not in {'http://127.0.0.1:8006/health/ready',
                       'http://127.0.0.1:19000/minio/health/ready'}:
            raise AssertionError('Only fixed local readiness endpoints are permitted.')
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                with urllib.request.urlopen(url, timeout=2) as response:
                    if response.status == 200:
                        response.read(4096)
                        return
            except (OSError, urllib.error.URLError):
                pass
            time.sleep(1)
        raise AssertionError('Local readiness did not recover within the bounded wait.')

    def insert(self, label, status, attempts, future_seconds=0):
        if label == 'staging-presign-grace':
            key = 'homework/1001/1001/staging/maintenance-' + self.run + '.pdf'
        else:
            key = 'homework/maintenance/' + self.run + '/' + label + '.pdf'
        self.cursor.execute(
            "INSERT INTO hw_delete_jobs(object_key,status,attempts,available_at,lease_until) "
            "VALUES(%s,%s,%s,DATE_ADD(UTC_TIMESTAMP(6),INTERVAL %s SECOND),"
            "IF(%s='running',DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 5 SECOND),NULL))",
            (key, status, attempts, future_seconds, status),
        )
        fixture = {'id': self.cursor.lastrowid, 'key': key}
        self.owned.append(fixture)
        return fixture

    def row(self, fixture):
        self.cursor.execute(
            'SELECT status,attempts,lease_until,error_code,available_at '
            'FROM hw_delete_jobs WHERE id=%s AND object_key=%s',
            (fixture['id'], fixture['key']),
        )
        row = self.cursor.fetchone()
        if row is None:
            raise AssertionError('An owned deletion fixture unexpectedly disappeared.')
        return row

    def wait_status(self, fixture, wanted, timeout=180):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            row = self.row(fixture)
            if row['status'] == wanted:
                return row
            if row['status'] == 'failed' and wanted != 'failed':
                raise AssertionError('An owned deletion exhausted retries before S3 recovered.')
            time.sleep(1)
        raise AssertionError('Owned deletion did not reach its expected terminal state.')

    def run_checks(self):
        self.wait_http('http://127.0.0.1:8006/health/ready')
        print('Maintenance API ready; checking expired final deletion lease.', flush=True)
        exhausted = self.insert('exhausted-expired-lease', 'running', 10)
        failed = self.wait_status(exhausted, 'failed', timeout=45)
        self.check('expired final deletion attempt becomes failed', failed['status'] == 'failed')
        self.check('exhausted deletion keeps its attempt count', failed['attempts'] == 10,
                   attempts=failed['attempts'])
        self.check('exhausted deletion clears lease', failed['lease_until'] is None)
        self.check('exhausted deletion has explicit error', failed['error_code'] == 'delete_retry_exhausted',
                   error_code=failed['error_code'])

        self.wait_http('http://127.0.0.1:19000/minio/health/ready')
        self.cursor.execute('SELECT setting_value FROM application_settings WHERE setting_key=%s',
                            ('homework.1001.limits',))
        setting = self.cursor.fetchone()
        self.ttl = int(json.loads(setting['setting_value'])['presign_ttl_seconds']) if setting else 300
        if not 60 <= self.ttl <= 1800:
            raise AssertionError('Unexpected presigned lifetime in local settings.')
        retry = self.insert('recoverable-expired-lease', 'running', 1)
        staging = self.insert('staging-presign-grace', 'queued', 0, future_seconds=self.ttl + 10)
        print('S3 ready; checking delayed tombstone without changing global time or settings.', flush=True)
        # Cross at least two 5-second maintenance ticks. The fixture's deadline
        # remains safely beyond this interval even at the smallest allowed TTL.
        deadline = time.monotonic() + 12
        while time.monotonic() < deadline:
            delayed = self.row(staging)
            if delayed['status'] != 'queued' or delayed['attempts'] != 0:
                self.check('staging tombstone is not consumed before expiry', False)
            time.sleep(1)
        self.check('staging tombstone waits beyond two maintenance ticks', delayed['status'] == 'queued')
        self.check('staging tombstone has no early S3 deletion attempt', delayed['attempts'] == 0,
                   attempts=delayed['attempts'])
        self.check('staging tombstone remains unleased before deadline', delayed['lease_until'] is None)
        self.cursor.execute(
            'UPDATE hw_delete_jobs SET available_at=DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 1 SECOND) '
            'WHERE id=%s AND object_key=%s AND status=%s AND attempts=0',
            (staging['id'], staging['key'], 'queued'),
        )
        self.check('only owned staging fixture models expiry', self.cursor.rowcount == 1)
        done_staging = self.wait_status(staging, 'done')
        self.check('staging tombstone is consumed after modeled expiry', done_staging['status'] == 'done')
        self.check('staging cleanup records an actual deletion attempt', done_staging['attempts'] >= 1,
                   attempts=done_staging['attempts'])

        done_retry = self.wait_status(retry, 'done')
        self.check('expired recoverable lease completes through real S3 deletion', done_retry['status'] == 'done')
        self.check('lease recovery increments deletion attempts', 2 <= done_retry['attempts'] <= 10,
                   attempts=done_retry['attempts'])
        self.check('successful recovery clears lease and storage error',
                   done_retry['lease_until'] is None and done_retry['error_code'] is None)
        self.check('successful staging cleanup clears lease and storage error',
                   done_staging['lease_until'] is None and done_staging['error_code'] is None)

    def cleanup(self):
        try:
            if self.business is not None:
                self.cleanup_business()
            for fixture in self.owned:
                if self.run not in fixture['key'] and fixture['key'] not in self.business_keys:
                    raise AssertionError('Cleanup refuses a foreign key.')
                self.cursor.execute('DELETE FROM hw_delete_jobs WHERE id=%s AND object_key=%s',
                                    (fixture['id'], fixture['key']))
            self.check('all owned deletion rows cleaned', all(self.absent(fixture) for fixture in self.owned))
        finally:
            self.cursor.close()
            self.connection.close()

    def cleanup_business(self):
        s = self.business
        assignment_ids = [assignment_id for _, assignment_id in s.assignments]
        for assignment_id in assignment_ids:
            owned_assignment = s.row('SELECT name FROM hw_assignments WHERE id=%s', (assignment_id,))
            if not owned_assignment or not owned_assignment['name'].startswith(s.prefix):
                raise AssertionError('Business cleanup refuses an unowned assignment.')
            self.cursor.execute('SELECT id,source_key,output_key FROM hw_jobs WHERE assignment_id=%s', (assignment_id,))
            for job in self.cursor.fetchall():
                for key in [job['source_key'], job['output_key']]:
                    if not key.startswith('homework/1001/1001/') or job['id'] not in key:
                        raise AssertionError('Business cleanup refuses an unowned S3 key.')
                    self.business_keys.add(key)
        s.cleanup()
        # Only the script's upload credentials existed, so no client can use
        # them after this test. Physically clean its exact keys and bookkeeping.
        for key in self.business_keys:
            s.signed_s3('DELETE', key, expected=(200, 204))
            self.cursor.execute('DELETE FROM hw_delete_jobs WHERE object_key=%s', (key,))
        self.check('owned business fixtures and exact S3 keys cleaned', s.cleaned)
        self.cases.extend({'name': name, 'passed': True, 'group': 'business HTTP/S3 helper'} for name in s.checks)

    def absent(self, fixture):
        self.cursor.execute('SELECT COUNT(*) AS count FROM hw_delete_jobs WHERE id=%s AND object_key=%s',
                            (fixture['id'], fixture['key']))
        return self.cursor.fetchone()['count'] == 0

    def report(self, error=None):
        cases = self.prior_cases + self.cases
        result = {
            'started_at_utc': self.started.isoformat(),
            'finished_at_utc': datetime.now(timezone.utc).isoformat(),
            'passed': sum(case['passed'] for case in cases),
            'total': len(cases), 'cases': cases, 'skipped': [],
            'executed_this_run': len(self.cases), 'carried_forward_checks': len(self.prior_cases),
            'scope': 'Owned maintenance rows plus one owned student/type/assignment, one real processed draft and one cancelled upload.',
            'staging_scope': 'Actual cancel-to-presign-TTL calculation verified; initial scheduler checks retained with provenance.',
            'presign_ttl_snapshot_seconds': self.ttl,
            'success': error is None and all(case['passed'] for case in self.cases),
        }
        if error:
            result['error'] = error
        path = ROOT / 'dev/local/homework-maintenance-report.json'
        path.write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
        path.chmod(0o600)
        print(json.dumps({'passed': result['passed'], 'total': result['total'],
                          'executed_this_run': result['executed_this_run'],
                          'carried_forward_checks': result['carried_forward_checks'],
                          'skipped': 0, 'success': result['success']}), flush=True)
        return result['success']


def main(business_only=False):
    suite = MaintenanceSuite(business_only)
    error = None
    try:
        if not business_only:
            suite.run_checks()
        suite.business_checks()
    except Exception as failure:
        error = type(failure).__name__ + ': ' + str(failure)
    finally:
        try:
            suite.cleanup()
        except Exception as failure:
            error = error or (type(failure).__name__ + ': ' + str(failure))
    return 0 if suite.report(error) else 1


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--business-only', action='store_true', help='Complete only the two business reference/cancel scenarios.')
    args = parser.parse_args()
    raise SystemExit(main(args.business_only))
