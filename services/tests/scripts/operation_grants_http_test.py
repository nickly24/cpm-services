"""Targeted Rev2 Auth -> Tests HTTP proof. No DB writes outside owner APIs.

Default invocation is source preflight only: no credentials, HTTP or DB access.
Execute only in a coordinated dev window after Auth/Tests credentials and schema
are provisioned. This is a native harness, not an Uploads/Rabbit/UI E2E claim.
Fixtures and owner audit are retained; bearer tokens stay in request memory.
"""
import argparse
from datetime import datetime, timedelta, timezone
import hashlib
import json
from pathlib import Path
import secrets
import stat
import sys
import urllib.error
import urllib.request
from uuid import uuid4
from zoneinfo import ZoneInfo

ROOT = Path(__file__).resolve().parents[3]
CONTRACT_SHA = '4aeeccb8497777f1b0fac218bebc52f69dcd162b36683fc3a7756ef5e7bc92bd'
AUTH = 'http://127.0.0.1:8002'
TESTS = 'http://127.0.0.1:8004'


def encoded(value):
    return json.dumps(value, ensure_ascii=False, separators=(',', ':')).encode()


def digest(body):
    return hashlib.sha256(body).hexdigest()


def source_preflight():
    contract = ROOT / 'services/auth/docs/import-operation-grants-v1.md'
    if digest(contract.read_bytes()) != CONTRACT_SHA:
        raise RuntimeError('Rev2 contract changed; obtain affected owner ACK before runtime')
    return {'state': 'NOTRUN', 'contract_revision': 2, 'contract_sha256': CONTRACT_SHA,
            'scope': 'native Auth -> Tests JSON/OTP/external prepare, apply, receipt',
            'runtime_changed': False, 'credentials_read': False,
            'uploads_rabbit_ui_e2e': 'NOTRUN',
            'prerequisites': ['Auth schema/runtime provisioned by its owner',
                              'distinct private credentials provisioned by sole writer',
                              'Tests frozen candidate deployed in ACKed window',
                              'live fixture actor with different account/profile IDs',
                              'active scoped direction and synthetic pupil fixture']}


class Harness:
    def __init__(self, args):
        # Importing dev helpers is deliberately restricted to an actual run.
        sys.path.insert(0, str(ROOT / 'dev/scripts'))
        from bootstrap import settings, mongo_client
        from export_structure import read_env
        from bson import ObjectId, BSON
        key_file = ROOT / 'dev/local/import-operation-credentials.env'
        if stat.S_IMODE(key_file.stat().st_mode) != 0o600:
            raise RuntimeError('Operation credential file must be private mode 0600')
        credentials = read_env(key_file)
        config = settings()
        self.issuer = credentials['AUTH_OPERATIONS_ISSUER_KEY']
        self.delivery = credentials['TESTS_OPERATION_DELIVERY_KEY']
        self.owner_key = credentials['AUTH_OPERATIONS_TESTS_KEY']
        self.legacy = config['AUTH_INTERNAL_KEY']
        if any(len(v) < 32 for v in (self.issuer, self.delivery, self.owner_key, self.legacy)) or len({self.issuer, self.delivery, self.owner_key, self.legacy}) != 4:
            raise RuntimeError('Missing/short/shared operation credentials')
        self.args = args
        self.password = json.loads((ROOT / 'dev/local/accounts.json').read_text())['password']
        self.sessions = []
        self.client = mongo_client(config)
        self.db = self.client.cpm_dev
        self.object_id, self.bson = ObjectId, BSON
        self.checks, self.requests, self.fixtures = [], [], []
        self.prefix = 'DEV Tests Rev2 ' + secrets.token_hex(6)
        self.actor = None
        self.token = None

    def expect(self, condition, label):
        if not condition:
            raise AssertionError(label)
        self.checks.append(label)

    def call(self, origin, path, method='POST', body=None, headers=None, expected=(200,)):
        h = dict(headers or {})
        if body is not None:
            h['Content-Type'] = 'application/json'
        req = urllib.request.Request(origin + path, data=body, headers=h, method=method)
        try:
            with urllib.request.urlopen(req, timeout=30) as response:
                code, raw = response.status, response.read()
        except urllib.error.HTTPError as error:
            code, raw = error.code, error.read()
        self.requests.append({'method': method, 'path': path, 'status': code})
        self.expect(code in expected, f'{method} {path}: unexpected HTTP {code}')
        out = json.loads(raw)
        if code < 300:
            self.expect(out.get('status') is True, 'successful response envelope')
        return out

    def login(self):
        out = self.call(AUTH, '/v1/auth/login', body=encoded({'login': self.args.login,
            'password': self.password, 'organization_id': self.args.organization,
            'division_id': self.args.division}))
        self.token = out['token']
        self.sessions.append(self.token)
        actor = out['user']
        self.expect(actor['id'] != actor['account_id'], 'live actor profile/account IDs differ')
        self.expect(actor['organization_id'] == self.args.organization and actor['division_id'] == self.args.division,
                    'live actor has exact original context')
        if self.actor is not None:
            self.expect(actor['id'] == self.actor['id'] and actor['account_id'] == self.actor['account_id'], 'fresh login retains original profile/account')
        self.actor = actor

    def bearer(self):
        return {'Authorization': 'Bearer ' + self.token}

    def reject_foreign_recovery(self, grant):
        out = self.call(AUTH, '/v1/auth/login', body=encoded({'login': self.args.foreign_login,
            'password': self.password, 'organization_id': self.args.organization,
            'division_id': self.args.division}))
        token = out['token']
        self.sessions.append(token)
        actor = out['user']
        self.expect(actor['account_id'] != self.actor['account_id'], 'foreign recovery caller is a different real account')
        full_admin = actor['role'] == 'admin' and actor['organization_admin']
        section = 'test-results' if self.active_kind == 'tests.external-results' else 'tests'
        rights = actor.get('permissions', {})
        self.expect(full_admin or actor['role'] == 'staff_admin' and all(rights.get(key, {}).get('view') and rights.get(key, {}).get('edit') for key in ('upload', section)),
                    'foreign caller has otherwise sufficient current rights')
        self.call(AUTH, f'/internal/auth/operation-grants/{grant}/recover', body=b'{}',
            headers={'Authorization': 'Bearer '+token, 'X-Service-Key': self.issuer, 'Idempotency-Key': str(uuid4())}, expected=(404,))
        self.call(AUTH, '/v1/auth/logout', headers={'Authorization': 'Bearer '+token})
        self.sessions.remove(token)

    def native_headers(self, grant, stage, command, job):
        return {'X-CPM-Operation-Grant': grant, 'X-CPM-Operation-Stage': stage,
                'X-CPM-Operation-Job': job, 'Idempotency-Key': command,
                'X-CPM-Operation-Service-Key': self.delivery}

    def issue(self, kind, target, target_revision, command, job, upload, session, preview_raw, apply_raw, quote, source):
        proof = dict(quote['owner_proof'])
        self.expect(proof.pop('schema_version') == 1, 'native schema1 proof normalized explicitly')
        deadline = (datetime.now(timezone.utc) + timedelta(hours=2)).isoformat().replace('+00:00', 'Z')
        binding = {'contract_version': 1, 'upload_id': upload, 'upload_session_id': session,
            'job_id': job, 'coordinator_command_key': 'tests-native:' + secrets.token_hex(16),
            'type': kind, 'owner': 'tests', 'native_command_key': command,
            'owner_session_id': None, 'upload_preview_revision': '1',
            'owner_preview_revision': None, 'target_revision': target_revision,
            'target': target, 'source_sha256': digest(source), 'owner_proof': proof,
            'owner_request_sha256': quote['owner_request_sha256'],
            'apply_wire_body_sha256': digest(apply_raw), 'preview_wire_body_sha256': digest(preview_raw),
            'upload_expires_at': deadline, 'owner_preview_expires_at': None,
            'required_division_ids': [str(self.args.division)]}
        result = self.call(AUTH, '/internal/auth/operation-grants', body=encoded(binding),
            headers={**self.bearer(), 'X-Service-Key': self.issuer, 'Idempotency-Key': str(uuid4())})
        self.expect(result['binding'] == binding and result['mode'] == 'apply', 'Auth stores immutable original native binding')
        self.expect(result['actor']['id'] == self.actor['id'] and result['actor']['account_id'] == self.actor['account_id'], 'Auth grant preserves distinct live identity')
        return result['grant_id']

    def snapshot(self, kind, test_id):
        oid = self.object_id(test_id)
        collection = 'tests_external_catalog_v1' if kind == 'tests.external-results' else 'tests_catalog_v1'
        doc = self.db[collection].find_one({'_id': oid, 'organization_id': self.args.organization})
        self.expect(doc is not None, 'owner document exists in exact organization')
        division = doc.get('division_id', doc.get('owner_division_id'))
        self.expect(division == self.args.division, 'owner document belongs to original division')
        if kind == 'tests.external-results':
            results = doc['results']
            self.expect(len(results) == 1 and results[0]['account_id'] == self.args.student_account and results[0]['student_id'] == self.args.student_profile and results[0]['score'] == 0,
                        'Mongo external result preserves distinct pupil IDs and zero score')
            self.expect(len(doc['commands']) == 1 and doc['commands'][0]['actor_id'] == self.actor['account_id'], 'one account-bound durable external receipt')
        else:
            self.expect(doc['created_by'] == self.actor['account_id'] and len(doc['history_version_ids']) == 1, 'one selected version and real account writer')
            version = self.db.tests_content_versions_v1.find_one({'_id': doc['content_version_id'], 'organization_id': self.args.organization, 'test_id': oid})
            self.expect(version is not None and version['questions'] == doc['questions'], 'immutable version matches selected catalog content')
        return digest(self.bson.encode(doc))

    def exercise(self, kind, payload):
        self.active_kind = kind
        command, job, upload, session = str(uuid4()), secrets.token_hex(16), secrets.token_hex(16), secrets.token_hex(16)
        base = '/internal/tests/external-results' if kind == 'tests.external-results' else '/internal/tests/imports/json'
        if kind == 'tests.external-results':
            created = self.call(TESTS, f'/v1/tests/divisions/{self.args.division}/external-tests', body=encoded({'name': self.prefix+' external', 'direction_id': self.args.direction, 'date': datetime.now(ZoneInfo('Europe/Moscow')).date().isoformat()}),
                headers={**self.bearer(), 'Idempotency-Key': str(uuid4())}, expected=(200,201))['data']
            validate = {'organization_id': self.args.organization, 'division_id': self.args.division,
                'test_id': created['id'], 'expected_test_revision': created['revision'],
                'rows': [{'row_id': 'r2', 'row': 2, 'full_name': self.args.student_name,
                          'account_id': self.args.student_account, 'legacy_student_id': self.args.student_profile, 'percent': 0}]}
            target = {'kind': 'external_test', 'test_id': created['id']}
            target_revision = str(created['revision'])
        else:
            validate = {'organization_id': self.args.organization, 'division_id': self.args.division,
                        'direction_id': self.args.direction, 'payload': payload}
            target, target_revision = {'kind': 'create_tests', 'direction_id': str(self.args.direction)}, None
        preview_raw = encoded(validate)
        preview = self.call(TESTS, base+'/validate', body=preview_raw,
                            headers={**self.bearer(), 'X-Service-Key': self.legacy})['data']
        self.expect(preview.get('valid', preview.get('can_commit')) is True, 'native preview can apply')
        commit = {**validate, 'command_id': command, 'preview_hash': preview['preview_hash']}
        if kind == 'tests.external-results':
            commit.update(upload_id=upload, upload_session_id=session, upload_preview_revision=1)
        apply_raw = encoded(commit) + b'\n'
        if kind == 'tests.external-results':
            scope = {'_id': self.object_id(validate['test_id']), 'organization_id': self.args.organization}
            prepare_before = digest(self.bson.encode(self.db.tests_external_catalog_v1.find_one(scope)))
        else:
            scope = {'organization_id': self.args.organization, 'owner_division_id': self.args.division, 'title': preview['preview']['title']}
            prepare_before = self.db.tests_catalog_v1.count_documents(scope)
            self.expect(prepare_before == 0, 'JSON fixture absent before preparation')
        quote = self.call(TESTS, base+'/prepare', body=apply_raw,
            headers={**self.bearer(), 'Idempotency-Key': command, 'X-CPM-Import-Prepare-Key': self.delivery})['data']
        self.expect(quote['native_command_key'] == command and quote['owner_proof'] == preview['owner_proof'], 'readonly prepare copies current native proof and original command')
        prepare_after = (digest(self.bson.encode(self.db.tests_external_catalog_v1.find_one(scope)))
                         if kind == 'tests.external-results' else self.db.tests_catalog_v1.count_documents(scope))
        self.expect(prepare_before == prepare_after, 'readonly prepare makes no business mutation in Mongo')
        grant = self.issue(kind,target,target_revision,command,job,upload,session,preview_raw,apply_raw,quote,encoded(payload or validate))
        # Positive grant preview uses exact originally bound Validate bytes.
        self.call(TESTS,base+'/validate',body=preview_raw,headers=self.native_headers(grant,'preview',command,job))
        # Logout closes the browser session, not the approved account-bound job.
        self.call(AUTH,'/v1/auth/logout',headers=self.bearer())
        self.sessions.remove(self.token)
        result = self.call(TESTS,base+'/commit',body=apply_raw,headers=self.native_headers(grant,'apply',command,job),expected=(201,))['data']
        test_id = validate['test_id'] if kind == 'tests.external-results' else result['test']['id']
        fixture = {'type':kind,'test_id':test_id,'command_id':command,'job_id':job,
                   'grant_id':grant,'actor_profile_id':self.actor['id'],'actor_account_id':self.actor['account_id'],
                   'student_account_id':self.args.student_account if kind=='tests.external-results' else None,
                   'student_profile_id':self.args.student_profile if kind=='tests.external-results' else None}
        self.fixtures.append(fixture)
        before = self.snapshot(kind,test_id)
        # Confirmed command redelivery replays the same owner receipt.
        self.call(TESTS,base+'/commit',body=apply_raw,headers=self.native_headers(grant,'apply',command,job))
        self.call(TESTS,base+'/receipt',body=apply_raw,headers=self.native_headers(grant,'receipt',command,job))
        self.expect(before == self.snapshot(kind,test_id),'matching redelivery and receipt make zero owner changes')
        self.call(TESTS,base+'/commit',body=apply_raw+b' ',headers=self.native_headers(grant,'apply',command,job),expected=(409,))
        mixed = {**self.native_headers(grant,'apply',command,job),'Authorization':'Bearer deliberately-invalid'}
        self.call(TESTS,base+'/commit',body=apply_raw,headers=mixed,expected=(409,))
        self.login()
        self.call(AUTH,f'/internal/auth/operation-grants/{grant}/revoke',body=b'{}',headers={**self.bearer(),'X-Service-Key':self.issuer})
        self.reject_foreign_recovery(grant)
        self.call(TESTS,base+'/receipt',body=apply_raw,headers=self.native_headers(grant,'receipt',command,job),expected=(410,))
        recovered = self.call(AUTH,f'/internal/auth/operation-grants/{grant}/recover',body=b'{}',headers={**self.bearer(),'X-Service-Key':self.issuer,'Idempotency-Key':str(uuid4())})
        self.expect(recovered['mode']=='receipt' and recovered['binding']['native_command_key']==command,'explicit recovery preserves command and only grants receipt')
        fixture['receipt_grant_id'] = recovered['grant_id']
        self.call(TESTS,base+'/receipt',body=apply_raw,headers=self.native_headers(recovered['grant_id'],'receipt',command,job))
        self.call(TESTS,base+'/commit',body=apply_raw,headers=self.native_headers(recovered['grant_id'],'apply',command,job),expected=(403,409))
        self.expect(before == self.snapshot(kind,test_id),'revoke/recovery/denied second apply leave owner state unchanged')
        self.call(AUTH,f'/internal/auth/operation-grants/{recovered["grant_id"]}/revoke',body=b'{}',headers={**self.bearer(),'X-Service-Key':self.issuer})

    def run(self):
        self.login()
        metadata={'startDate':'2099-01-01T10:00','endDate':'2099-01-01T12:00','timeLimitMinutes':30,'published':False,'visible':False}
        payload={**metadata,'title':self.prefix+' JSON','questions':[{'questionId':1,'type':'single','text':'Synthetic?','points':2,'answers':[{'id':'a','text':'Yes','isCorrect':True},{'id':'b','text':'No','isCorrect':False}]}]}
        self.exercise('tests.json',payload)
        source={'Name':self.prefix+' OTP','Questions':[{'Number':1,'AnswerType':10,'Text':'<p>Synthetic OTP?</p>','maxScore':2,'AnswerTemplates':[{'Number':1,'Text':'Yes','IsRightAnswer':True},{'Number':2,'Text':'No','IsRightAnswer':False}]}]}
        self.exercise('tests.json',{'importFormat':'online_test_pad','sourceText':'var test = '+json.dumps(source,ensure_ascii=False)+';','metadata':metadata})
        self.exercise('tests.external-results',None)

    def close(self):
        for token in self.sessions[:]:
            try:
                self.call(AUTH,'/v1/auth/logout',headers={'Authorization':'Bearer '+token})
                self.sessions.remove(token)
            except Exception:
                self.checks.append('logout_cleanup_failed')
        self.client.close()


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--execute',action='store_true',help='Run only after coordinated dev provisioning/deployment')
    parser.add_argument('--login');parser.add_argument('--foreign-login');parser.add_argument('--organization',type=int);parser.add_argument('--division',type=int);parser.add_argument('--direction',type=int)
    parser.add_argument('--student-account',type=int);parser.add_argument('--student-profile',type=int);parser.add_argument('--student-name')
    args=parser.parse_args()
    report=source_preflight()
    if not args.execute:
        print(json.dumps(report,ensure_ascii=False,indent=2));return
    if not all((args.login,args.foreign_login,args.organization,args.division,args.direction,args.student_account,args.student_profile,args.student_name)) or args.student_account==args.student_profile or min(args.organization,args.division,args.direction,args.student_account,args.student_profile)<1:
        parser.error('Execute requires an existing scoped actor, direction and synthetic pupil with distinct IDs')
    harness=Harness(args)
    report.update(state='RUNNING',credentials_read=True,organization_id=args.organization,division_id=args.division)
    failure=None
    try:
        harness.run();report['state']='PASS'
    except Exception as error:
        # Never include response bodies, raw rows, tokens or credentials in logs.
        report['state']='FAIL';report['failure_type']=type(error).__name__;failure=error
    finally:
        harness.close()
        if harness.sessions:
            report['state'] = 'FAIL'
            report['failure_type'] = 'SessionCleanupIncomplete'
            failure = failure or RuntimeError('Session cleanup incomplete')
        report.update(check_count=len(harness.checks),http_count=len(harness.requests),checks=harness.checks,
                      requests=harness.requests,fixtures=harness.fixtures,db_access='scoped reads only',
                      fixtures_retained=True,tokens_remaining=len(harness.sessions),
                      uploads_rabbit_ui_e2e='NOTRUN')
        path=ROOT/'dev/local/tests-operation-grants-http-report.json'
        path.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n')
        print(json.dumps({'state':report['state'],'checks':report['check_count'],'http':report['http_count'],'report':str(path)}))
    if failure is not None:
        raise SystemExit(1)


if __name__=='__main__':
    main()
