"""Owned public-API fixture for coordinated bounded Calculate downtime.

prepare leaves one actual zero publication and one open official attempt in a
separate division. finalize must run only during the root's authorized bounded
pause; it creates a real queued official submission, never changes a database or
controls a process. Credentials are read only from ignored dev/local/accounts.
"""
from __future__ import annotations
import argparse
from datetime import datetime, timedelta, timezone
import importlib.util
import json
from pathlib import Path
import uuid

ROOT = Path(__file__).resolve().parents[3]
spec = importlib.util.spec_from_file_location('ratings_bff_public_helpers', ROOT/'services/ratings/scripts/bff_integration_test.py')
helpers = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helpers)
WAITING = ROOT/'dev/local/bff-ratings-waiting-fixtures.json'
DEMO = ROOT/'dev/local/bff-ratings-demo.json'


class Suite(helpers.Suite):
    def persist(self):
        public = {key: value for key, value in self.fixtures.items() if key != 'password'}
        helpers.owner.private_json(WAITING, public)

    def close(self, error=None):
        for session in self.browser.values():
            try:
                self.bff(session, '/bff/auth/logout', 'POST', {}, record=False)
            except Exception:
                pass
        for token in self.sessions:
            try:
                super(helpers.Suite, self).request(helpers.owner.AUTH, '/v1/auth/logout', token, 'POST', codes=(200, 401), record=False)
            except Exception:
                pass
        report = {'status': 'failed' if error else 'passed', 'checks': len(self.checks), 'check_names': self.checks,
                  'division_id': self.division, 'run': self.run, 'direct_database_writes': 0,
                  **({'error': str(error)} if error else {})}
        helpers.owner.private_json(ROOT/f'dev/local/bff-ratings-waiting-{self.mode}-report.json', report)
        print(json.dumps({key: report[key] for key in ('status','checks','division_id','run')}))

    def prepare(self):
        self.admin, _ = self.login('dev_admin', fixture=False)
        self.division = self.core('/divisions', method='POST', body={'name': self.prefix+' waiting'}, codes=201)['id']
        self.fixtures['divisions'].append(self.division)
        group = self.core(f'/divisions/{self.division}/groups', method='POST', body={'name': self.prefix+' waiting group'}, codes=201)['id']
        self.fixtures['groups'].append(group)
        self.student, self.student_id = self.user('waitingstudent', group=group)
        self.admin, _ = self.login('dev_admin', self.division, fixture=False)
        self.period = json.loads(DEMO.read_text())['period']
        direction = self.core('/directions', method='POST', body={'name': self.prefix+' waiting direction'}, codes=201)['id']
        test = self.tests(f'/divisions/{self.division}/tests', method='POST', body={
            'title': self.prefix+' waiting official', 'description': 'Synthetic public queued source', 'direction_id': direction,
            'questions': [{'question_id': 1,'type':'single','text':'Choose A','points':2,
                'answers':[{'id':'a','text':'A','is_correct':True},{'id':'b','text':'B','is_correct':False}], 'correct_answers':[]}]},
            codes=201, headers={'Idempotency-Key':uuid.uuid4().hex})
        now = datetime.now(timezone.utc)
        self.tests(f"/divisions/{self.division}/tests/{test['id']}/settings", method='PUT', body={
            'expected_revision':test['revision'], 'published':True, 'start_at':(now-timedelta(minutes=10)).isoformat(),
            'end_at':(now+timedelta(hours=6)).isoformat(),'time_limit_minutes':360,'passing_score':50,'answers_visible':True})
        job, _ = self.new_job()
        baseline = self.wait_job(job)
        row = self.ratings('/me', self.student)['rating']
        self.expect(row['rating']['final_rating'] == 0, 'Waiting scope retains a completed actual zero baseline')
        attempt = self.tests(f"/divisions/{self.division}/tests/{test['id']}/attempts", self.student, 'POST', {'practice':False}, 201)
        student_login = next(row['login'] for row in self.fixtures['users'] if row['account_id'] == self.student_id)
        self.fixtures.update({'division_id':self.division,'student_id':self.student_id,'student_login':student_login,
            'period':self.period,'test_id':test['id'],'attempt_id':attempt['id'],'test_version_id':attempt['test_version_id'],
            'baseline_job_id':baseline['id'],'baseline_revision':baseline['revision']})
        self.persist()
        demo = json.loads(DEMO.read_text())
        demo.update({'waiting_division_id':self.division,'waiting_student_login':student_login,'waiting_student_account_id':self.student_id,
            'waiting_test_id':test['id'],'waiting_attempt_id':attempt['id'],'waiting_baseline_job_id':baseline['id'],
            'waiting_baseline_revision':baseline['revision'],'waiting_expected_final':0})
        helpers.owner.private_json(DEMO, demo)

    def finalize(self):
        fixture = json.loads(WAITING.read_text())
        self.run = fixture['run_id']
        self.division = fixture['division_id']
        self.student, _ = self.login(fixture['student_login'], self.division)
        accepted = self.tests('/attempts/'+fixture['attempt_id']+'/finalize', self.student, 'POST', {
            'test_version_id':fixture['test_version_id'],'reason':'manual','completed_at':datetime.now(timezone.utc).isoformat(),
            'answers':[{'question_id':1,'value':'b'}]}, 202)
        self.expect(accepted['calculation_status'] == 'queued', 'Real official source is queued during coordinated Calculate pause')
        state = self.tests('/submissions/'+accepted['submission_id'], self.student)
        self.expect(state['calculation_status'] == 'queued' and state.get('result') is None, 'Queued source exposes no immediate score')
        fixture['submission_id'] = accepted['submission_id']
        helpers.owner.private_json(WAITING, fixture)
        demo = json.loads(DEMO.read_text())
        demo['waiting_submission_id'] = accepted['submission_id']
        helpers.owner.private_json(DEMO, demo)

    def verify(self):
        fixture = json.loads(WAITING.read_text())
        self.run, self.division = fixture['run_id'], fixture['division_id']
        self.student, _ = self.login(fixture['student_login'], self.division)
        self.admin, _ = self.login('dev_admin', self.division, fixture=False)
        source = self.tests('/submissions/'+fixture['submission_id'], self.student)
        self.expect(source['calculation_status'] == 'ready' and source['result']['score'] == 0,
                    'Resumed actual Calculate grades the queued official source to ready zero')
        jobs = self.ratings('/jobs')['jobs']
        baseline = next(row for row in jobs if row['id'] == fixture['baseline_job_id'])
        cancelled = [row for row in jobs if row['status'] == 'cancelled']
        completed = [row for row in jobs if row['status'] == 'completed']
        self.expect(len(jobs) == 3 and len(cancelled) == 1 and len(completed) == 2,
                    'Owned waiting scope retains baseline, cancelled job and one completed retry')
        latest = max(completed, key=lambda row: row['revision'])
        self.expect(latest['revision'] > cancelled[0]['revision'] > baseline['revision'],
                    'Cancel and retry preserve independent increasing revisions')
        self.expect(latest['processed_count'] == latest['total_count'] == 1,
                    'Completed retry atomically publishes its one prepared participant')
        current = self.ratings('/me', self.student)
        self.expect(current['rating']['rating']['final_rating'] == 0 and current['freshness']['version_id'] == latest['id'],
                    'Student current publication is the resumed retry and preserves a real zero')
        previous = self.ratings('/ratings?version='+baseline['id'])
        self.expect(previous['ratings'][0]['rating']['final_rating'] == 0 and previous['freshness']['version_id'] == baseline['id'],
                    'Baseline zero publication remains available as immutable historical version')
        versions = self.ratings('/versions')['versions']
        self.expect({row['id'] for row in versions} == {baseline['id'], latest['id']},
                    'Publication history excludes cancelled waiting job')
        inbox = self.internal(helpers.owner.CALCULATE, '/internal/calculate/ratings/messages?job_id='+latest['id']+'&limit=10')['items']
        self.expect(len(inbox) == 1 and inbox[0]['status'] == 'ready' and inbox[0]['account_id'] == fixture['student_id']
                    and inbox[0]['organization_id'] == 1001 and inbox[0]['division_id'] == self.division,
                    'Retry has one real ready Calculate durable inbox record with exact participant scope')
        self.ratings('/ratings?version='+cancelled[0]['id'], codes=404)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('mode', choices=('prepare','finalize','verify'))
    args = parser.parse_args()
    suite, error = Suite(), None
    suite.mode = args.mode
    try:
        getattr(suite,args.mode)()
    except Exception as failure:
        error = failure
    finally:
        suite.close(error)
    if error:
        raise SystemExit('Waiting fixture failed; see the non-secret local report')

if __name__ == '__main__':
    main()
