"""Isolated HTTP BFF -> real Homework routes/SQL test with synthetic dependencies.

Starts and removes its own tmpfs MySQL container; never reads dev credentials,
uses shared fixtures, starts background workers, or connects to production.
Auth/Core/S3 HEAD are loopback mocks. No PDF processing or browser UI is tested.
"""
import base64
import hashlib
import hmac
import json
import os
import re
import http.client
from pathlib import Path
import secrets
import shutil
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ROOT = Path(__file__).resolve().parents[3]
KEY = 'synthetic-homework-gateway-key-32-bytes'
CORE_KEY = 'synthetic-core-key-for-gateway-test-32'
WORKER_KEY = 'synthetic-worker-key-for-gateway-test'
TESTS_KEY = 'synthetic-tests-key-for-gateway-test-32'
SECTIONS = ('assignments', 'review-queue', 'homework-archive', 'monitoring')
COUNTS = {'me': 0, 'introspect': 0, 'references': 0}
CHECKS = []


def actor(role, account, group=None, permissions=None, org=1, div=2):
    return dict(account_id=account, id=account + 1000, role=role, full_name='Synthetic',
                organization_id=org, division_id=div, division_ids=[2, 3], group_id=group,
                organization_admin=role == 'admin', role_id=None,
                permissions=permissions or {})


USERS = {
    'student': actor('student', 7, 3), 'other': actor('student', 8, 4),
    'proctor': actor('proctor', 9, 3), 'proctor2': actor('proctor', 10, 4),
    'admin': actor('admin', 11), 'restricted': actor('staff_admin', 12,
        permissions={s: {'view': True, 'edit': True} for s in SECTIONS}),
    'read': actor('staff_admin', 13, permissions={s: {'view': True, 'edit': False} for s in SECTIONS}),
    'empty': actor('staff_admin', 14), 'supervisor': actor('supervisor', 15),
    'examinator': actor('examinator', 16), 'foreign': actor('admin', 17, org=99),
}
BLOCKED = set()
MEMBERS = [dict(account_id=u['account_id'], profile_id=u['id'], role=u['role'],
                full_name='Synthetic', group_id=u['group_id'], group_name='Synthetic group',
                **{'class': 10}) for u in USERS.values() if u['organization_id'] == 1]


class Dependencies(BaseHTTPRequestHandler):
    def log_message(self, *_): pass
    def reply(self, code, data):
        raw = json.dumps(data).encode()
        self.send_response(code); self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(raw))); self.end_headers(); self.wfile.write(raw)
    def do_HEAD(self):
        self.send_response(200); self.send_header('Content-Length', '100')
        self.send_header('Content-Type', 'application/pdf'); self.send_header('ETag', '"synthetic-etag"')
        self.send_header('Last-Modified', 'Wed, 14 Oct 2026 00:00:00 GMT')
        self.send_header('X-Amz-Meta-Account-Id', '7'); self.end_headers()
    def do_GET(self): self.handle_api()
    def do_POST(self): self.handle_api()
    def handle_api(self):
        raw = self.rfile.read(int(self.headers.get('Content-Length', '0')))
        body = json.loads(raw) if raw else {}
        token = self.headers.get('Authorization', '').removeprefix('Bearer ')
        if self.path == '/v1/auth/login':
            name = body['username']; u = USERS[name]
            self.reply(200, dict(status=True, user=u, organizations=[], access_token=name,
                                 refresh_token='refresh-' + name, expires_in=900)); return
        if self.path in ('/v1/auth/me', '/v1/auth/introspect'):
            kind = 'me' if self.path.endswith('/me') else 'introspect'; COUNTS[kind] += 1
            if token in BLOCKED or token not in USERS: self.reply(401, dict(status=False)); return
            self.reply(200, dict(status=True, active=True, user=USERS[token], organizations=[])); return
        if self.path == '/v1/auth/context':
            USERS[token]['division_id'] = body['division_id']
            self.reply(200, dict(status=True, user=USERS[token], organizations=[], access_token=token,
                                 refresh_token='refresh-' + token, expires_in=900)); return
        if self.path == '/internal/core/homework-references':
            COUNTS['references'] += 1
            if self.headers.get('X-Service-Key') != CORE_KEY: self.reply(403, {}); return
            if body['organization_id'] != 1 or body['division_id'] not in (2, 3):
                self.reply(404, {}); return
            self.reply(200, dict(status=True, data=dict(organization_id=1,
                       division_id=body['division_id'], members=MEMBERS,
                       groups=[dict(id=3, name='Synthetic 3', proctor_account_id=9),
                               dict(id=4, name='Synthetic 4', proctor_account_id=10)]))); return
        self.reply(404, {})


BFF_SOURCE = '''package main
import("cpm/bff/internal/bff";"net";"net/http";"os";"fmt")
func main(){ l,e:=net.Listen("tcp","127.0.0.1:0");if e!=nil{panic(e)}
s:=bff.New(bff.Config{Auth:os.Getenv("FIXTURE_DEPENDENCIES"),Homework:os.Getenv("FIXTURE_HOMEWORK"),Origin:"http://synthetic.local",HomeworkGatewayKey:os.Getenv("FIXTURE_GATEWAY_KEY"),GatewayKey:os.Getenv("FIXTURE_TESTS_KEY")})
fmt.Println("http://"+l.Addr().String());http.Serve(l,s)}
'''
HOMEWORK_SOURCE = '''package main
import("cpm/homework/internal/homework";"database/sql";_ "github.com/go-sql-driver/mysql";"net";"net/http";"os";"fmt")
func main(){ db,e:=sql.Open("mysql",os.Getenv("FIXTURE_DSN"));if e!=nil{panic(e)};if e=db.Ping();e!=nil{panic(e)}
c:=homework.Config{AuthURL:os.Getenv("FIXTURE_DEPENDENCIES"),CoreURL:os.Getenv("FIXTURE_DEPENDENCIES"),CoreKey:os.Getenv("FIXTURE_CORE_KEY"),Key:os.Getenv("FIXTURE_WORKER_KEY"),GatewayKey:os.Getenv("FIXTURE_GATEWAY_KEY"),S3Endpoint:os.Getenv("FIXTURE_DEPENDENCIES"),S3PublicEndpoint:os.Getenv("FIXTURE_DEPENDENCIES"),S3Bucket:"synthetic",S3Region:"us-east-1",S3Access:"synthetic-access",S3Secret:"synthetic-secret"}
storage,e:=homework.NewStorage(c);if e!=nil{panic(e)}
s:=&homework.Server{DB:db,Deps:homework.NewDependencies(c),Storage:storage,Config:c}
l,e:=net.Listen("tcp","127.0.0.1:0");if e!=nil{panic(e)};fmt.Println("http://"+l.Addr().String());http.Serve(l,s.Routes())}
'''


def request(url, method='GET', body=None, headers=None):
    h = dict(headers or {})
    data = json.dumps(body).encode() if isinstance(body, (dict, list)) else body
    if data is not None: h.setdefault('Content-Type', 'application/json')
    req = urllib.request.Request(url, method=method, data=data, headers=h)
    try:
        res = urllib.request.urlopen(req, timeout=15)
    except urllib.error.HTTPError as e: res = e
    with res:
        raw = res.read()
        return res.status, json.loads(raw) if raw else {}, res.headers


class Suite:
    def __init__(self, bff, homework, sql):
        self.bff, self.homework, self.sql = bff, homework, sql
        self.sessions = {}
    def check(self, condition, label):
        if not condition: raise AssertionError(label)
        CHECKS.append(label)
    def login(self, name):
        code, data, headers = request(self.bff + '/bff/auth/login', 'POST',
            {'username': name, 'password': 'synthetic'}, {'Origin': 'http://synthetic.local', 'X-CPM-Request': '1'})
        self.check(code == 200, 'login ' + name)
        self.sessions[name] = {'Cookie': headers['Set-Cookie'].split(';')[0],
            'X-CPM-Context': data['context_revision'], 'X-CPM-Request': '1', 'Origin': 'http://synthetic.local'}
    def api(self, name, tail, method='GET', body=None, code=200, count=True):
        if name not in self.sessions: self.login(name)
        before = COUNTS.copy()
        h = self.sessions[name] | {'X-CPM-Gateway-Context': 'forged-browser-admin',
                                  'X-CPM-Gateway-Signature': 'forged', 'X-Role': 'admin', 'X-User-Id': '11'}
        got, data, _ = request(self.bff + '/bff/homework/' + tail, method, body, h)
        self.check(got == code, f'{name} {method} {tail.split("?")[0]} expected {code}, got {got} ({data.get("error", "")})')
        if count:
            self.check(COUNTS['me'] - before['me'] == 1 and COUNTS['introspect'] == before['introspect'],
                       f'one BFF Auth / zero Homework Auth: {name} {method} {tail.split("?")[0]}')
            self.check(COUNTS['references'] - before['references'] == (0 if code == 401 else 1),
                       'Core References retained: ' + name + ' ' + tail.split('?')[0])
        return data.get('data', {})
    def seed_draft(self, assignment, account=7):
        self.sql(f"INSERT INTO hw_submissions(assignment_id,account_id,state,revision_comment) VALUES({assignment},{account},'draft','') ON DUPLICATE KEY UPDATE state='draft';")
        sid = int(self.sql(f'SELECT id FROM hw_submissions WHERE assignment_id={assignment} AND account_id={account};').strip())
        self.sql(f"INSERT INTO hw_files(submission_id,object_key,status,size_bytes,page_count,sha256) VALUES({sid},'synthetic/{uuid.uuid4()}.pdf','draft',100,1,REPEAT('a',64)); UPDATE hw_submissions SET draft_file_id=LAST_INSERT_ID() WHERE id={sid};")
        return sid
    def run(self):
        for name in USERS: self.login(name)
        kind = self.api('admin', 'types', 'POST', {'name': 'Synthetic type'}, 201)['id']
        self.api('admin', f'types/{kind}', 'PUT', {'name': 'Synthetic type renamed'})
        h = self.api('admin', 'assignments', 'POST', {'name': 'Synthetic assignment', 'type_id': kind, 'deadline': '2026-10-14', 'published': True}, 201)['id']
        for name in ('student', 'proctor', 'admin', 'restricted', 'read'):
            for tail in ('types', 'assignments', f'assignments/{h}', 'settings', f'assignments/{h}/workspace?account_id=7'):
                self.api(name, tail)
        for name in ('empty', 'supervisor', 'examinator'):
            self.api(name, 'types', code=403)
            self.api(name, 'assignments', code=403)
            self.api(name, 'review-queue', code=403)
            self.api(name, 'archive', code=403)
        self.api('supervisor', 'report?date_from=2026-10-01&date_to=2026-10-31')
        self.api('examinator', 'report', code=403)
        self.api('foreign', f'assignments/{h}', code=404)
        for name in ('student', 'proctor', 'restricted', 'read'):
            self.api(name, 'audit', code=403)
        self.api('admin', 'audit'); self.api('admin', 'monitoring')
        self.api('restricted', 'monitoring'); self.api('read', 'monitoring')
        self.api('read', 'types', 'POST', {'name': 'Forbidden'}, 403)
        self.api('read', f'assignments/{h}', 'PUT', {'name': 'Forbidden'}, 403)
        self.api('restricted', f'assignments/{h}', 'PUT', {'name': 'Renamed', 'type_id': kind, 'deadline': '2026-10-14', 'published': True})
        limits = self.api('admin', 'settings')
        limits['max_source_bytes'] = 100 << 20
        self.api('admin', 'settings', 'PUT', limits)
        self.api('restricted', 'settings', 'PUT', limits, 403)
        self.api('admin', f'assignments/{h}/students')
        self.api('proctor', f'assignments/{h}/students')
        self.api('student', f'assignments/{h}/students', code=403)
        self.api('other', f'assignments/{h}/workspace?account_id=7', code=403)
        self.api('proctor2', f'assignments/{h}/workspace?account_id=7', code=403)
        # Exercise direct S3 upload metadata; API body stays small for a 100MiB allowed PDF.
        upload = self.api('student', f'assignments/{h}/uploads', 'POST', {'client_upload_id': str(uuid.uuid4())}, 201)
        self.check(upload['max_bytes'] == 100 << 20 and upload['upload']['method'] == 'POST', '100MiB PDF uses presigned S3, not gateway buffering')
        job = upload['job']['id']
        self.api('student', 'jobs/active'); self.api('student', 'jobs/' + job)
        self.api('other', 'jobs/' + job, code=404)
        self.api('other', 'uploads/' + job + '/complete', 'POST', {}, 403)
        self.api('student', 'uploads/' + job + '/complete', 'POST', {}, 202)
        self.api('student', 'jobs/' + job + '/cancel', 'POST', {})
        # Synthetic SQL state substitutes for PDF processing; no broker/worker is running.
        sid = self.seed_draft(h)
        self.api('student', f'submissions/{sid}/file-url?draft=1&download=1')
        self.api('admin', f'submissions/{sid}/file-url?draft=1', code=403)
        self.api('student', f'assignments/{h}/submit', 'POST', {})
        self.api('student', f'submissions/{sid}/file-url')
        self.api('other', f'submissions/{sid}/file-url', code=403)
        self.api('proctor2', f'submissions/{sid}/file-url', code=403)
        self.api('proctor', 'review-queue'); self.api('restricted', 'review-queue')
        self.api('read', f'submissions/{sid}/claim', 'POST', {}, 403)
        self.api('proctor', f'submissions/{sid}/claim', 'POST', {})
        self.api('restricted', f'submissions/{sid}/takeover', 'POST', {})
        self.api('restricted', f'submissions/{sid}/release', 'POST', {})
        self.api('proctor', f'submissions/{sid}/claim', 'POST', {})
        self.api('proctor', f'submissions/{sid}/request-revision', 'POST', {'message': 'Synthetic revision'})
        self.seed_draft(h)
        self.api('student', f'assignments/{h}/submit', 'POST', {})
        self.api('proctor', f'submissions/{sid}/claim', 'POST', {})
        self.api('proctor', f'submissions/{sid}/grade', 'POST', {'result': 87})
        for name in ('admin', 'proctor', 'restricted', 'read'):
            self.api(name, 'archive'); self.api(name, f'submissions/{sid}/file-url?download=1')
        self.api('student', f'assignments/{h}/workspace')
        self.api('restricted', f'submissions/{sid}/edit-grade', 'POST', {'result': 90})
        self.api('read', f'submissions/{sid}/resubmit', 'POST', {}, 403)
        self.api('restricted', f'submissions/{sid}/resubmit', 'POST', {})
        self.seed_draft(h)
        self.api('student', f'assignments/{h}/draft', 'DELETE')
        self.api('student', f'submissions/{sid}/file-url?draft=1', code=404)
        # Failed job fixture exercises existing manual retry without a real processor.
        self.sql(f"UPDATE hw_jobs SET status='failed',error_code='processing_failed' WHERE id='{job}'; UPDATE hw_submissions SET state='none',generation=1 WHERE id={sid};")
        self.api('student', 'jobs/' + job + '/retry', 'POST', {}, 202)
        self.api('student', 'jobs/' + job + '/cancel', 'POST', {})
        self.seed_draft(h)
        self.api('student', f'assignments/{h}/submit', 'POST', {})
        self.api('proctor', f'submissions/{sid}/claim', 'POST', {})
        member = next(m for m in MEMBERS if m['account_id'] == 7)
        member['group_id'] = 4
        queue = self.api('proctor2', 'review-queue')
        self.check(any(s['id'] == sid and s['state'] == 'submitted' for s in queue['items']), 'Core current group releases former proctor claim')
        self.api('proctor', f'submissions/{sid}/file-url', code=403)
        self.api('proctor', f'submissions/{sid}/grade', 'POST', {'result': 80}, 403)
        self.api('proctor2', f'submissions/{sid}/claim', 'POST', {})
        self.api('proctor2', f'submissions/{sid}/grade', 'POST', {'result': 80})
        member['group_id'] = 3
        for name in ('student', 'proctor', 'restricted', 'read', 'admin', 'supervisor'):
            self.api(name, 'report?date_from=2026-10-01&date_to=2026-10-31')
        # Separate manual assignment avoids file-review constraints.
        manual = self.api('admin', 'assignments', 'POST', {'name': 'Manual', 'type_id': kind, 'deadline': '2026-10-14', 'published': True}, 201)['id']
        self.api('proctor', f'assignments/{manual}/manual', 'POST', {'account_id': 7, 'result': 75, 'status': 1, 'date_pass': '2026-10-14'})
        self.api('admin', f'assignments/{manual}/manual/7', 'PUT', {'result': 80, 'status': 1, 'date_pass': '2026-10-14'})
        self.api('proctor', f'assignments/{manual}/manual/bulk', 'POST', {'group_id': 3, 'result': 85, 'status': 1, 'date_pass': '2026-10-14'})
        self.api('proctor', f'assignments/{manual}/manual/bulk', 'POST', {'group_id': 4, 'result': 85, 'status': 1}, 403)
        # Current Core group is fetched on the next request, not cached in Homework.
        USERS['proctor']['group_id'] = 4
        self.api('proctor', f'assignments/{h}/workspace?account_id=7', code=403)
        USERS['proctor']['group_id'] = 3
        # Revocation/permission edits become effective on the next browser request.
        USERS['restricted']['permissions'] = {}
        self.api('restricted', 'assignments', code=403)
        BLOCKED.add('student'); self.api('student', 'assignments', code=401)
        BLOCKED.remove('student'); self.login('student')
        code, data, _ = request(self.bff + '/bff/auth/context', 'POST', {'division_id': 3}, self.sessions['student'])
        self.check(code == 200, 'cabinet switch through Auth-owned context')
        old_headers = self.sessions['student'].copy()
        self.sessions['student']['X-CPM-Context'] = data['context_revision']
        self.api('student', f'assignments/{h}', code=404)
        got, _, _ = request(self.bff + '/bff/homework/assignments', headers=old_headers)
        self.check(got == 409, 'old cabinet revision rejected')
        # Full and restricted admins retain the selected cabinet fence through BFF.
        def switch(name, division):
            code, data, _ = request(self.bff + '/bff/auth/context', 'POST', {'division_id': division}, self.sessions[name])
            self.check(code == 200, 'admin cabinet switch: ' + name)
            self.sessions[name]['X-CPM-Context'] = data['context_revision']
        switch('admin', 3)
        self.api('admin', f'assignments/{h}', code=404)
        self.check(self.api('admin', 'assignments')['items'] == [], 'admin sees only newly selected cabinet')
        switch('admin', 2)
        self.api('admin', f'assignments/{h}')
        switch('read', 3)
        self.api('read', f'assignments/{h}', code=404)
        switch('read', 2)
        self.api('read', f'assignments/{h}')
        self.api('admin', f'assignments/{manual}', 'DELETE')
        self.api('admin', f'types/{kind}', 'DELETE')
        self.direct_security(h)

    def direct_security(self, h):
        before = COUNTS.copy()
        code, _, _ = request(self.homework + '/v1/homework/divisions/2/assignments', headers={'Authorization': 'Bearer admin'})
        self.check(code == 200 and COUNTS['introspect'] - before['introspect'] == 1 and COUNTS['me'] == before['me'], 'direct bearer Homework makes exactly one introspect')
        uri = f'/v1/homework/divisions/2/assignments/{h}/workspace?account_id=7'
        now = int(time.time())
        claims = dict(version=1, issuer='cpm-bff', audience='homework', issued_at=now,
                      expires_at=now+30, method='GET', uri=uri,
                      body_sha256=hashlib.sha256(b'').hexdigest(), user=USERS['admin'])
        def signed(c):
            encoded = base64.urlsafe_b64encode(json.dumps(c).encode()).rstrip(b'=')
            return {'X-CPM-Gateway-Context': encoded.decode(), 'X-CPM-Gateway-Signature': base64.urlsafe_b64encode(hmac.new(KEY.encode(), encoded, hashlib.sha256).digest()).rstrip(b'=').decode(), 'Authorization': 'Bearer admin'}
        for key, value in (('audience', 'tests'), ('issuer', 'client'), ('expires_at', now-1), ('method', 'POST'), ('uri', uri+'&other=1'), ('body_sha256', '0'*64)):
            before = COUNTS.copy(); headers = signed(claims | {key: value})
            code, _, _ = request(self.homework+uri, headers=headers)
            self.check(code == 401 and COUNTS == before, 'HTTP invalid assertion fails before Auth/Core: ' + key)
        for headers in ({'X-CPM-Gateway-Context': 'forged'}, {'X-CPM-Gateway-Signature': 'forged'}, signed(claims) | {'X-CPM-Gateway-Signature': 'forged'}):
            before = COUNTS.copy(); code, _, _ = request(self.homework+uri, headers=headers | {'Authorization': 'Bearer admin'})
            self.check(code == 401 and COUNTS == before, 'HTTP partial/forged assertion has no bearer fallback')
        # Duplicate headers through actual HTTP, not collapsed Python dictionaries.
        parsed = urllib.parse.urlsplit(self.homework)
        for name in ('X-CPM-Gateway-Context', 'X-CPM-Gateway-Signature'):
            before = COUNTS.copy(); headers = signed(claims)
            connection = http.client.HTTPConnection(parsed.hostname, parsed.port, timeout=10)
            connection.putrequest('GET', uri)
            for key, value in headers.items(): connection.putheader(key, value)
            connection.putheader(name, headers[name]); connection.endheaders()
            res = connection.getresponse(); res.read()
            self.check(res.status == 401 and COUNTS == before, 'HTTP duplicate header rejected: ' + name)
            connection.close()
        # Every registered public method, including all review actions, uses the same fail-closed entry.
        source = (ROOT / 'services/homework/internal/homework/http.go').read_text()
        patterns = re.findall(r'\{"((?:GET|POST|PUT|DELETE) /[^" ]+)"', source)
        patterns += [('POST /submissions/{submission}/' + action) for action in ('claim', 'takeover', 'release', 'request-revision', 'grade', 'edit-grade', 'resubmit')]
        for pattern in patterns:
            method, path = pattern.split(' ', 1)
            path = re.sub(r'\{[^}]+\}', '1', path)
            before = COUNTS.copy()
            code, _, _ = request(self.homework + '/v1/homework/divisions/2' + path, method,
                {} if method in ('POST', 'PUT') else None,
                {'X-CPM-Gateway-Context': 'partial', 'Authorization': 'Bearer admin'})
            self.check(code == 401 and COUNTS == before, 'all-route invalid assertion fence: ' + pattern)
        # Gateway/service/worker keys do not grant browser identity by themselves.
        for key in (CORE_KEY, WORKER_KEY, KEY):
            code, _, _ = request(self.homework+uri, headers={'X-Service-Key': key})
            self.check(code == 401, 'service credential is not browser authorization')


def main():
    go = shutil.which('go') or str(Path.home() / '.local/bin/go')
    container = 'cpm-homework-auth-synthetic-' + secrets.token_hex(5)
    processes, temporary = [], []
    mock = ThreadingHTTPServer(('127.0.0.1', 0), Dependencies)
    threading.Thread(target=mock.serve_forever, daemon=True).start()
    dep = 'http://127.0.0.1:' + str(mock.server_port)
    def sql(text):
        res = subprocess.run(['docker', 'exec', '-i', container, 'mysql', '--default-character-set=utf8mb4', '-uroot', '-psynthetic-local-only', '-N', 'synthetic_homework'], input=text, text=True, capture_output=True)
        if res.returncode: raise RuntimeError('synthetic SQL failed: ' + res.stderr.replace('mysql: [Warning] Using a password on the command line interface can be insecure.\n', ''))
        return res.stdout
    try:
        subprocess.run(['docker', 'run', '-d', '--name', container, '--memory=512m', '--cpus=1',
            '--tmpfs', '/var/lib/mysql:rw,size=384m', '-p', '127.0.0.1::3306',
            '-e', 'MYSQL_ROOT_PASSWORD=synthetic-local-only', '-e', 'MYSQL_DATABASE=synthetic_homework',
            'mysql:8.0.46', '--innodb-buffer-pool-size=64M', '--performance-schema=OFF'], check=True, capture_output=True)
        port = subprocess.check_output(['docker', 'port', container, '3306/tcp'], text=True).strip().split(':')[-1]
        for _ in range(80):
            try: sql('SELECT 1;'); break
            except RuntimeError: time.sleep(.5)
        else: raise RuntimeError('isolated MySQL readiness timeout')
        sql((ROOT / 'services/homework/migrations/001_homework.sql').read_text())
        sql('CREATE TABLE application_settings(setting_key VARCHAR(200) PRIMARY KEY,setting_value TEXT,updated_by BIGINT);')
        env = os.environ.copy()
        # Helpers use only explicit synthetic configuration and do not call LoadConfig.
        env.update(FIXTURE_DEPENDENCIES=dep, FIXTURE_GATEWAY_KEY=KEY, FIXTURE_CORE_KEY=CORE_KEY,
                   FIXTURE_WORKER_KEY=WORKER_KEY, FIXTURE_TESTS_KEY=TESTS_KEY,
                   FIXTURE_DSN=f'root:synthetic-local-only@tcp(127.0.0.1:{port})/synthetic_homework?parseTime=true&loc=UTC')
        with tempfile.TemporaryDirectory(prefix='cpm-homework-auth-binaries-') as bins:
            for module, source in (('homework', HOMEWORK_SOURCE), ('bff', BFF_SOURCE)):
                directory = Path(tempfile.mkdtemp(prefix='gatewayfixture_', dir=ROOT / 'services' / module / 'cmd'))
                temporary.append(directory); (directory / 'main.go').write_text(source)
                binary = str(Path(bins) / module)
                subprocess.run([go, 'build', '-o', binary, './cmd/' + directory.name], cwd=ROOT / 'services' / module, check=True)
                p = subprocess.Popen([binary], env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
                processes.append(p); address = p.stdout.readline().strip()
                if not address.startswith('http://127.0.0.1:'): raise RuntimeError(module+' fixture failed')
                if module == 'homework': homework = address; env['FIXTURE_HOMEWORK'] = address
                else: bff = address
            Suite(bff, homework, sql).run()
        print(json.dumps({'result': 'PASS', 'checks': len(CHECKS), 'call_counts': COUNTS,
                          'scope': 'real loopback BFF/Homework HTTP and isolated MySQL; synthetic Auth/Core/S3 HEAD; no broker/PDF/UI'}, indent=2))
    finally:
        for p in reversed(processes):
            p.terminate()
            try: p.wait(timeout=5)
            except subprocess.TimeoutExpired: p.kill(); p.wait()
        for directory in temporary: shutil.rmtree(directory)
        mock.shutdown(); mock.server_close()
        subprocess.run(['docker', 'rm', '-f', container], capture_output=True)


if __name__ == '__main__': main()
