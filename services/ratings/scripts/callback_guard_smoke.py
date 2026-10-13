"""HTTP guards for cloned results of one completed synthetic B2 job.

Run only after root confirms the final ratings binary is ready. Creates no jobs,
changes no stored/source data, and controls no processes. SQL reads only the
exact completed job, two owned account memberships, outputs, and scope pointer.
Only fixed loopback ratings callbacks use the private cpm_dev service key.
"""
import argparse
import copy
import hashlib
import json
import os
import re
import stat
import sys
import urllib.error
import urllib.request
from pathlib import Path


ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'dev/scripts'))
from bootstrap import settings, mysql_connection

ORIGIN = 'http://127.0.0.1:8012'
JOB = '2c3ae817-0008-4c39-8a40-68de64dcf8a4'
RUN = '10d3deb9e73c'
ORG, DIVISION = 2001, 10191
REPORT = ROOT / 'dev/local/ratings-callback-guard-report.json'


class NoRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, target):
        return None


def private_json(path, value):
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(descriptor, 'w') as stream:
        os.fchmod(stream.fileno(), 0o600)
        json.dump(value, stream, ensure_ascii=False, indent=2, allow_nan=False)
        stream.write('\n')


class Smoke:
    def __init__(self):
        self.env = settings()
        fixture = json.loads((ROOT / f'dev/local/ratings-tenant-{RUN}-fixtures.json').read_text())
        scopes = [s for s in fixture['scopes'] if s['label'] == 'B2']
        if fixture['run_id'] != RUN or len(scopes) != 1 or scopes[0]['organization_id'] != ORG or scopes[0]['division_id'] != DIVISION:
            raise AssertionError('Only the exact completed synthetic B2 fixture may be probed')
        self.students = scopes[0]['students']
        self.accounts = sorted(s['account_id'] for s in self.students)
        self.connection = None
        self.opener = urllib.request.build_opener(NoRedirects())
        self.checks, self.cases = [], []
        self.before = None

    def expect(self, condition, label):
        if not condition:
            raise AssertionError(label)
        self.checks.append(label)

    def sql(self, query, args=()):
        cursor = self.connection.cursor(dictionary=True)
        try:
            cursor.execute(query, args)
            return cursor.fetchall()
        finally:
            cursor.close()

    def state(self):
        jobs = self.sql('SELECT id,organization_id,division_id,revision,status,total_count,processed_count,source_hash '
                        'FROM ratings_jobs_v1 WHERE id=%s AND organization_id=%s AND division_id=%s', (JOB, ORG, DIVISION))
        scopes = self.sql('SELECT revision,active_job_id,published_job_id FROM ratings_scopes_v1 '
                         'WHERE organization_id=%s AND division_id=%s', (ORG, DIVISION))
        items = self.sql('SELECT account_id,status,input_hash,output_hash,output FROM ratings_items_v1 '
                        'WHERE job_id=%s ORDER BY account_id', (JOB,))
        self.expect(len(jobs) == 1 and jobs[0]['status'] == 'completed' and
                    jobs[0]['total_count'] == jobs[0]['processed_count'] == 2 and len(scopes) == 1 and
                    scopes[0]['published_job_id'] == JOB and scopes[0]['active_job_id'] is None and
                    [r['account_id'] for r in items] == self.accounts and all(r['status'] == 'ready' for r in items),
                    'Only the complete current publication and both ready owned rows may be probed')
        signature = {'job': jobs[0], 'scope': scopes[0], 'items': [
            {key: row[key] for key in ('account_id','status','input_hash','output_hash')} |
            {'output_sha256': hashlib.sha256(row['output'].encode() if isinstance(row['output'], str) else row['output']).hexdigest()}
            for row in items]}
        return signature, items

    def callback(self, account, raw, codes, error=None):
        path = f'/internal/ratings/jobs/{JOB}/accounts/{account}/result'
        request = urllib.request.Request(ORIGIN + path, data=raw, method='POST', headers={
            'Content-Type': 'application/json', 'X-Service-Key': self.env['AUTH_INTERNAL_KEY']})
        try:
            with self.opener.open(request, timeout=20) as response:
                code, payload = response.status, response.read(1 << 20)
        except urllib.error.HTTPError as response:
            code, payload = response.code, response.read(1 << 20)
        accepted = (codes,) if isinstance(codes, int) else tuple(codes)
        self.expect(code in accepted, f'Owned callback returns expected status {accepted}; actual {code}')
        response = json.loads(payload)
        if error is not None:
            self.expect(response.get('status') is False and response.get('error') == error,
                        'Malformed nested callback is rejected by its precise wire-contract error')
        elif code == 200:
            self.expect(response.get('status') is True and response.get('data', {}).get('accepted') is True,
                        'An exact stored callback is idempotently acknowledged')
        else:
            self.expect(code == 409 and response.get('error') == 'result_idempotency_conflict',
                        'A valid historical shape reaches idempotency comparison without guessing a status')
        state, _ = self.state()
        self.expect(state == self.before, 'Callback preserves progress, published pointer, output bytes and every stored hash')
        return code

    @staticmethod
    def raw(value):
        return json.dumps(value, ensure_ascii=False, separators=(',', ':'), allow_nan=False).encode()

    def run(self):
        request = urllib.request.Request(ORIGIN + '/health/ready')
        with self.opener.open(request, timeout=20) as response:
            health = json.load(response)
            self.expect(response.status == 200 and health.get('status') is True and
                        health['data']['exams_mode'] == 'required' and health['data']['external_tests_mode'] == 'required',
                        'Final ratings runtime is ready with both real required sources')
        self.connection = mysql_connection(self.env)
        self.connection.autocommit = True
        memberships = self.sql('SELECT a.id,a.username FROM auth_users a JOIN auth_division_access m ON m.user_id=a.id '
            "WHERE a.id IN (%s,%s) AND a.role='student' AND m.organization_id=%s AND m.division_id=%s",
            (*self.accounts, ORG, DIVISION))
        self.expect(len(memberships) == 2 and {r['id'] for r in memberships} == set(self.accounts) and
                    {r['username'] for r in memberships} == {s['login'] for s in self.students} and
                    all(r['username'].startswith('dev_ratings_tenant_' + RUN + '_') for r in memberships),
                    'Exact synthetic accounts and memberships are verified before outputs are read')
        self.before, items = self.state()
        for row in items:
            original = json.loads(row['output'])
            account = row['account_id']
            self.expect(original['job_id'] == JOB and original['organization_id'] == ORG and
                        original['division_id'] == DIVISION and original['account_id'] == account and
                        original['input_hash'] == row['input_hash'] and original['status'] == 'ready' and
                        original['revision'] == self.before['job']['revision'],
                        'Clones retain all metadata from their exact persisted complete result')
            for kind in ('homework', 'tests', 'exams'):
                self.expect(bool(original[kind]['details']), 'Owned completed result has real details for each source')
                for corruption in ('null_detail', 'omitted_score', 'overflow_score'):
                    clone = copy.deepcopy(original)
                    if corruption == 'null_detail':
                        clone[kind]['details'][0] = None
                    elif corruption == 'omitted_score':
                        del clone[kind]['details'][0]['score']
                    else:
                        clone[kind]['details'][0]['score'] = '__own_callback_overflow__'
                    raw = self.raw(clone)
                    if corruption == 'overflow_score':
                        marker = b'"__own_callback_overflow__"'
                        self.expect(raw.count(marker) == 1, 'Only the intended nested score becomes the finite-overflow JSON number')
                        raw = raw.replace(marker, b'2e309')
                    code = self.callback(account, raw, 400, 'incomplete_rating_result')
                    self.cases.append({'account_id':account,'case':kind + '_' + corruption,'http_status':code})
            clone = copy.deepcopy(original)
            direction = next(iter(clone['tests']['directions']))
            clone['tests']['directions'][direction] = 'not-a-number'
            code = self.callback(account, self.raw(clone), 400, 'incomplete_rating_result')
            self.cases.append({'account_id':account,'case':'nonnumeric_direction_mean','http_status':code})
            clone = copy.deepcopy(original)
            clone['exams']['details'][0]['direction_id'] = 'not-an-id'
            code = self.callback(account, self.raw(clone), 400, 'incomplete_rating_result')
            self.cases.append({'account_id':account,'case':'nonnumeric_exam_direction','http_status':code})
            clone = copy.deepcopy(original)
            clone['tests']['details'][0]['calculation_status'] = 'missing'
            clone['tests']['details'][0]['score'] = 5
            code = self.callback(account, self.raw(clone), 400, 'incomplete_rating_result')
            self.cases.append({'account_id':account,'case':'missing_result_with_nonzero_score','http_status':code})
            historical = copy.deepcopy(original)
            for detail in historical['tests']['details']:
                detail.pop('calculation_status', None)
            code = self.callback(account, self.raw(historical), (200,409))
            self.cases.append({'account_id':account,'case':'historical_status_absence','http_status':code})
            code = self.callback(account, self.raw(original), 200)
            self.cases.append({'account_id':account,'case':'exact_original_replay','http_status':code})

    def finish(self, error):
        if self.connection is not None:
            self.connection.close()
        report = {'status':'failed' if error else 'passed','checks':len(self.checks),'scenarios':self.checks,
                  'job_id':JOB,'organization_id':ORG,'division_id':DIVISION,'run_id':RUN,'cases':self.cases,
                  'jobs_created':0,'stored_data_changed':False,'sources_changed':False,'services_restarted':False,
                  'production_accessed':False,'exams_mode':'required','external_tests_mode':'required'}
        if error:
            report['failure'] = str(error) if isinstance(error,AssertionError) else type(error).__name__
        private_json(REPORT, report)
        assert stat.S_IMODE(REPORT.stat().st_mode) == 0o600
        print(json.dumps({'status':report['status'],'checks':report['checks'],'http_callbacks':len(self.cases)}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--allow-callbacks', action='store_true',
                        help='Use only after root confirms the final deployed ratings validator is ready.')
    args = parser.parse_args()
    if not args.allow_callbacks:
        parser.error('--allow-callbacks is required; wait for root final READY before execution')
    smoke, error = None, None
    try:
        smoke = Smoke()
        smoke.run()
    except Exception as failure:
        error = failure
    finally:
        if smoke is not None:
            smoke.finish(error)
    if error:
        raise SystemExit('Ratings callback guard smoke failed; see private local report.')


if __name__ == '__main__':
    main()
