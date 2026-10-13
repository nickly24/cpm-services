"""Real HTTP regression tests against fixed local dev; never targets production.

Run with dev/.venv/bin/python services/homework/scripts/http_test.py.
Fixtures use generated names and are removed only from the local cpm_dev DB.
No credential, signed URL, password hash or token is written to the report.
"""
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timedelta
import json
from pathlib import Path
import secrets
import sys
from threading import Barrier
import urllib.error
import urllib.parse
import urllib.request
from zoneinfo import ZoneInfo

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'dev/scripts'))
from bootstrap import settings, mysql_connection

AUTH = 'http://127.0.0.1:8002'
CORE = 'http://127.0.0.1:8003'
HOMEWORK = 'http://127.0.0.1:8006'
FILEWORK = 'http://127.0.0.1:8007'
BASE = '/v1/homework'


class Suite:
    def __init__(self, label='HTTP'):
        self.run = secrets.token_hex(6)
        self.prefix = 'DEV Homework ' + label + ' ' + self.run
        self.checks, self.sessions, self.users, self.roles, self.groups = [], [], [], [], []
        self.assignments, self.types, self.saved_settings = [], [], {}
        self.cleaned = False
        self.metrics = {}
        self.password = json.loads((ROOT / 'dev/local/accounts.json').read_text())['password']
        # Validates the loopback dev database before creating any fixture.
        self.env = settings()

    def expect(self, condition, name):
        if not condition:
            raise AssertionError(name)
        self.checks.append(name)

    def request(self, path, token=None, method='GET', body=None, code=200,
                headers=None, origin=HOMEWORK, record=True):
        if origin not in {AUTH, CORE, HOMEWORK, FILEWORK}:
            raise AssertionError('HTTP fixture origin must be a fixed loopback service')
        h = dict(headers or {})
        if token:
            h['Authorization'] = 'Bearer ' + token
        if body is None:
            data = None
        elif isinstance(body, bytes):
            data = body
        else:
            data = json.dumps(body, allow_nan=False).encode()
            h['Content-Type'] = 'application/json'
        req = urllib.request.Request(origin + path, method=method, data=data, headers=h)
        try:
            with urllib.request.urlopen(req, timeout=40) as response:
                status, raw = response.status, response.read()
        except urllib.error.HTTPError as error:
            status, raw = error.code, error.read()
        allowed = code if isinstance(code, (tuple, list, set)) else (code,)
        label = f'{method} {path.split("?")[0]}: expected {"/".join(map(str, allowed))}, got {status}'
        if record:
            self.expect(status in allowed, label)
        elif status not in allowed:
            raise AssertionError(label)
        try:
            result = json.loads(raw)
        except ValueError:
            raise AssertionError('Service response must be JSON') from None
        return result

    def api(self, path, token, method='GET', body=None, code=200, headers=None,
            division=1001, record=True):
        result = self.request(f'{BASE}/divisions/{division}' + path, token, method,
                              body, code, headers, record=record)
        success = isinstance(code, int) and code < 300
        if success:
            condition = result.get('status') is True and 'data' in result
            if record:
                self.expect(condition, 'business response has success envelope')
            elif not condition:
                raise AssertionError('business response has success envelope')
            return result['data']
        self.expect(result.get('status') is False and bool(result.get('error')),
                    'business rejection has error envelope')
        return result

    def core(self, path, token, method='GET', body=None, code=200):
        result = self.request('/v1/core' + path, token, method, body, code, origin=CORE)
        return result['data'] if code < 300 else result

    def login(self, name, division=1001, organization=1001):
        result = self.request('/v1/auth/login', method='POST', body={
            'login': name, 'password': self.password,
            'organization_id': organization, 'division_id': division,
        }, origin=AUTH)
        self.sessions.append(result['token'])
        return result['token'], result['user']

    @staticmethod
    def items(data):
        return data['items'] if isinstance(data, dict) else data

    def assignment(self, type_id, *, division=1001, published=True, deadline=None, token=None, name=None):
        body = {'name': name or self.prefix + ' assignment ' + str(len(self.assignments)),
                'type_id': type_id, 'published': published,
                'deadline': deadline or (datetime.now(ZoneInfo('Europe/Moscow')).date() + timedelta(days=7)).isoformat()}
        doc = self.api('/assignments', token or self.admin, 'POST', body, 201, division=division)
        self.assignments.append((division, doc['id']))
        return doc, body

    def type(self, *, division=1001, name=None, token=None):
        name = name or self.prefix + ' type ' + str(len(self.types))
        doc = self.api('/types', token or self.admin, 'POST',
                       {'name': name}, 201, division=division)
        self.types.append((division, doc['id']))
        doc['name'] = name
        return doc

    def fixture_student(self, group=1001):
        login = 'dev_homework_' + self.run + '_' + str(len(self.users))
        data = self.core('/divisions/1001/users', self.admin, 'POST', {
            'role': 'student', 'full_name': self.prefix + ' student', 'class': 10,
            'login': login, 'password': self.password, 'school_id': 1001, 'group_id': group,
        }, 201)
        self.users.append(data['account_id'])
        token, actor = self.login(login)
        return token, actor, login

    def fixture_staff(self):
        sections = ['assignments', 'review-queue', 'homework-archive', 'monitoring']
        role_ids = []
        for label, edit in [('viewer', False), ('editor', True)]:
            role = self.core('/roles', self.admin, 'POST', {
                'name': self.prefix + ' ' + label,
                'permissions': {section: {'view': True, 'edit': edit} for section in sections},
            }, 201)
            self.roles.append(role['id'])
            role_ids.append(role['id'])
        login = 'dev_homework_' + self.run + '_staff'
        account = self.core('/divisions/1001/users', self.admin, 'POST', {
            'role': 'staff_admin', 'full_name': self.prefix + ' staff',
            'login': login, 'password': self.password, 'role_id': role_ids[0],
        }, 201)
        self.users.append(account['account_id'])
        self.core(f'/divisions/1002/memberships/{account["account_id"]}', self.admin,
                  'PUT', {'role_id': role_ids[1]})
        viewer, _ = self.login(login)
        editor, _ = self.login(login, 1002)
        return viewer, editor, role_ids

    def fixture_proctor(self):
        group = self.core('/divisions/1001/groups', self.admin, 'POST',
                          {'name': self.prefix + ' proctor group'}, 201)
        self.groups.append(group['id'])
        login = 'dev_homework_' + self.run + '_' + str(len(self.users))
        account = self.core('/divisions/1001/users', self.admin, 'POST', {
            'role': 'proctor', 'full_name': self.prefix + ' proctor',
            'login': login, 'password': self.password, 'group_id': group['id'],
        }, 201)
        self.users.append(account['account_id'])
        return account['account_id'], group['id']

    def save_settings(self, division=1001):
        current = self.api('/settings', self.admin, division=division)
        fields = ['max_source_bytes', 'max_output_bytes', 'max_pages', 'dpi', 'jpeg_quality',
                  'presign_ttl_seconds', 'upload_stale_seconds']
        self.saved_settings[division] = {field: current[field] for field in fields}
        return dict(self.saved_settings[division])

    def cleanup(self):
        # Restore policy before removing fixtures, including when assertions fail.
        for division, body in self.saved_settings.items():
            self.api('/settings', self.admin, 'PUT', body, division=division)
        for division, assignment_id in self.assignments:
            try:
                self.api(f'/assignments/{assignment_id}', self.admin, 'DELETE',
                         code=200, division=division)
            except AssertionError:
                # A fixture may already have been deleted by the regression scenario.
                pass
        conn = mysql_connection(self.env)
        cur = conn.cursor(dictionary=True)
        try:
            assignment_ids = [item[1] for item in self.assignments]
            if assignment_ids:
                marks = ','.join(['%s'] * len(assignment_ids))
                cur.execute('SELECT id,name FROM hw_assignments WHERE id IN (' + marks + ')', assignment_ids)
                rows = cur.fetchall()
                if len(rows) != len(assignment_ids) or any(not row['name'].startswith(self.prefix) for row in rows):
                    raise AssertionError('assignment cleanup fixture guard')
                cur.execute('SELECT id,source_key,output_key FROM hw_jobs WHERE assignment_id IN (' + marks + ')', assignment_ids)
                jobs = cur.fetchall()
                for job in jobs:
                    cur.execute("DELETE FROM hw_outbox WHERE JSON_UNQUOTE(JSON_EXTRACT(body_json,'$.payload.job_id'))=%s", (job['id'],))
                    # Delete bookkeeping after the normal service has queued object cleanup.
                    # The S3 deletion job is retained until it succeeds.
                cur.execute('DELETE f FROM hw_files f JOIN hw_submissions s ON s.id=f.submission_id WHERE s.assignment_id IN (' + marks + ')', assignment_ids)
                cur.execute('DELETE FROM hw_jobs WHERE assignment_id IN (' + marks + ')', assignment_ids)
                cur.execute('DELETE FROM hw_submissions WHERE assignment_id IN (' + marks + ')', assignment_ids)
                cur.execute('DELETE FROM hw_results WHERE assignment_id IN (' + marks + ')', assignment_ids)
                cur.execute('DELETE FROM hw_assignments WHERE id IN (' + marks + ')', assignment_ids)
            for _, type_id in self.types:
                cur.execute('DELETE FROM hw_types WHERE id=%s AND name LIKE %s', (type_id, self.prefix + '%'))
            for account_id in self.users:
                cur.execute('SELECT role,ref_id,username FROM auth_users WHERE id=%s', (account_id,))
                row = cur.fetchone()
                if not row or not row['username'].startswith('dev_homework_' + self.run + '_'):
                    raise AssertionError('account cleanup fixture guard')
                cur.execute('DELETE t FROM auth_refresh_tokens t JOIN auth_sessions s ON s.id=t.session_id WHERE s.user_id=%s', (account_id,))
                cur.execute('DELETE FROM auth_sessions WHERE user_id=%s', (account_id,))
                for table in ['core_profile_extra', 'core_proctor_groups', 'auth_division_access',
                              'auth_organization_access', 'core_user_owners', 'auth_account_state']:
                    cur.execute('DELETE FROM ' + table + ' WHERE user_id=%s', (account_id,))
                cur.execute('DELETE FROM auth_users WHERE id=%s', (account_id,))
                table = {'student': 'students', 'staff_admin': 'admin_role_users', 'proctor': 'proctors'}[row['role']]
                cur.execute('DELETE FROM ' + table + ' WHERE id=%s', (row['ref_id'],))
            for group_id in self.groups:
                cur.execute('DELETE FROM core_group_scope WHERE group_id=%s', (group_id,))
                cur.execute('DELETE FROM `groups` WHERE id=%s AND name LIKE %s', (group_id, self.prefix + '%'))
            for role_id in self.roles:
                cur.execute('DELETE FROM auth_scoped_role_permissions WHERE role_id=%s', (role_id,))
                cur.execute('DELETE FROM auth_scoped_roles WHERE id=%s AND name LIKE %s', (role_id, self.prefix + '%'))
            conn.commit()
        finally:
            cur.close()
            conn.close()
        for token in self.sessions:
            req = urllib.request.Request(AUTH + '/v1/auth/logout', method='POST',
                                         headers={'Authorization': 'Bearer ' + token})
            try:
                urllib.request.urlopen(req, timeout=5).close()
            except urllib.error.HTTPError:
                pass
        self.cleaned = True

    def report(self, filename, status='passed', failure=None):
        report = {'status': status, 'http_checks': len(self.checks), 'scenarios': self.checks,
                  'target': HOMEWORK, 'synthetic_records_cleaned': self.cleaned,
                  'production_accessed': False}
        if failure:
            report['failure'] = failure
        if self.metrics:
            report['metrics'] = self.metrics
        path = ROOT / 'dev/local' / filename
        path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
        path.chmod(0o600)
        print(json.dumps({key: report[key] for key in ['status', 'http_checks', 'synthetic_records_cleaned']}))


def main(s):
    s.request('/health/ready')
    spec = s.request('/openapi.json')
    s.expect(sum(len(ops) for ops in spec['paths'].values()) >= 30, 'OpenAPI describes public homework operations')
    replacements = {'division': '1001', 'assignment': '1', 'homework': '1', 'type': '1',
                    'submission': '1', 'job': '11111111-1111-4111-8111-111111111111', 'upload': '11111111-1111-4111-8111-111111111111', 'account': '1'}
    for path, ops in spec['paths'].items():
        if not path.startswith(BASE):
            continue
        concrete = path
        for name, value in replacements.items():
            concrete = concrete.replace('{' + name + '}', value)
        for method in ops:
            s.request(concrete, method=method.upper(), body={} if method in ['post', 'put', 'delete'] else None, code=401)
    s.request(BASE + '/divisions/1001/assignments', headers={'X-Role': 'admin', 'X-Account-ID': '1'}, code=401)
    s.admin, _ = s.login('dev_admin')
    math, math_actor = s.login('dev_admin_math', 2001, 2001)
    proctor, _ = s.login('dev_proctor_1')
    proctor2, _ = s.login('dev_proctor_2')
    examiner, _ = s.login('dev_examiner_1')
    supervisor, _ = s.login('dev_supervisor')
    unprivileged, _ = s.login('dev_staff')
    student, actor, student_login = s.fixture_student()
    other, other_actor, _ = s.fixture_student(1002)
    viewer, editor, role_ids = s.fixture_staff()
    s.core(f'/divisions/1002/memberships/{actor["account_id"]}', s.admin, 'PUT', {})
    reserve_student, reserve_actor = s.login(student_login, 1002)
    s.expect(reserve_actor['account_id'] == actor['account_id'], 'same student keeps unique account ID while switching division')
    for token in [examiner, supervisor, unprivileged]:
        s.api('/assignments', token, code=403)
    s.api('/assignments', math, code=404)
    s.api('/assignments', student, division=1002, code=404)
    s.api('/assignments', viewer, division=1002, code=404)
    s.api('/types', student, 'POST', {'name': s.prefix}, 403)
    s.api('/types', viewer, 'POST', {'name': s.prefix}, 403)
    s.api('/types', editor, 'POST', {'name': s.prefix}, 404)  # selected division is 1002
    first_type = s.type()
    second_type = s.type(name=s.prefix + ' another arbitrary name')
    reserve_type = s.type(division=1002, token=editor, name=first_type['name'])
    s.expect(first_type['id'] != reserve_type['id'], 'same type name is independent in another division')
    s.api('/types', s.admin, 'POST', {'name': first_type['name']}, 409)
    for bad in ['', ' ', 'x' * 101]:
        s.api('/types', s.admin, 'POST', {'name': bad}, 400)
    s.api('/types', s.admin, 'POST', {'name': 'DEV invalid', 'division_id': 2001}, 400)
    s.api(f'/types/{first_type["id"]}', math, 'PUT', {'name': s.prefix + ' evil'}, 404)
    s.api(f'/types/{first_type["id"]}', editor, 'PUT', {'name': s.prefix + ' evil'}, 404, division=1002)
    s.api(f'/types/{second_type["id"]}', s.admin, 'PUT', {'name': s.prefix + ' renamed type'})
    visible, visible_body = s.assignment(first_type['id'])
    hidden, hidden_body = s.assignment(second_type['id'], published=False)
    reserve, reserve_body = s.assignment(reserve_type['id'], division=1002, token=editor)
    visible_id, hidden_id, reserve_id = visible['id'], hidden['id'], reserve['id']
    ids = lambda data: {item['id'] for item in s.items(data)}
    s.expect(visible_id in ids(s.api('/assignments', student)), 'student sees published assignment of own division')
    s.expect(hidden_id not in ids(s.api('/assignments', student)), 'student cannot list unpublished assignment')
    s.expect(hidden_id in ids(s.api('/assignments', viewer)), 'authorized staff lists unpublished assignment')
    s.expect(reserve_id not in ids(s.api('/assignments', s.admin)), 'division catalogue excludes sibling assignments')
    s.expect(reserve_id in ids(s.api('/assignments', reserve_student, division=1002)), 'same student sees independent assignments in selected second division')
    s.api(f'/assignments/{visible_id}/workspace', reserve_student, division=1002, code=404)
    s.api(f'/assignments/{reserve_id}/workspace', student, code=404)
    s.api(f'/assignments/{hidden_id}', student, code=403)
    s.api(f'/assignments/{visible_id}', math, code=404)
    s.api(f'/assignments/{visible_id}', s.admin, code=404, division=1002)
    s.api('/assignments', student, 'POST', visible_body, 403)
    s.api('/assignments', viewer, 'POST', visible_body, 403)
    s.api('/assignments', s.admin, 'POST', {**visible_body, 'type_id': reserve_type['id']}, 404)
    for bad in ['2026-02-30', 'bad', '']:
        s.api('/assignments', s.admin, 'POST', {**visible_body, 'deadline': bad}, 400)
    for field, value in [('name', ''), ('name', 'x' * 201), ('published', 'yes'), ('type_id', -1), ('organization_id', 2001)]:
        s.api('/assignments', s.admin, 'POST', {**visible_body, field: value}, 400)
    s.api(f'/assignments/{visible_id}', viewer, 'PUT', visible_body, 403)
    s.api(f'/assignments/{visible_id}', s.admin, 'PUT', {**visible_body, 'name': s.prefix + ' assignment renamed'})
    visible_body['name'] = s.prefix + ' assignment renamed'
    s.api(f'/assignments/{hidden_id}', s.admin, 'PUT', {**hidden_body, 'published': True})
    s.expect(hidden_id in ids(s.api('/assignments', student)), 'publishing immediately exposes assignment')
    s.api(f'/assignments/{hidden_id}', s.admin, 'PUT', hidden_body)
    s.expect(hidden_id not in ids(s.api('/assignments', student)), 'unpublishing immediately hides assignment')
    for query in [f'type_id={first_type["id"]}', 'published=true', 'search=' + urllib.parse.quote('renamed')]:
        rows = s.items(s.api('/assignments?' + query, s.admin))
        s.expect(visible_id in {row['id'] for row in rows}, 'assignment filter returns matching fixture ' + query.split('=')[0])
        if query == 'published=true':
            s.expect(all(bool(row['published']) for row in rows), 'published filter excludes every unpublished assignment')
    unpublished = s.items(s.api('/assignments?published=false', s.admin))
    s.expect(hidden_id in {row['id'] for row in unpublished} and all(not row['published'] for row in unpublished),
             'unpublished filter returns only hidden assignments to authorized staff')
    student_filtered = s.items(s.api('/assignments?published=false', student))
    s.expect(hidden_id not in {row['id'] for row in student_filtered} and all(bool(row['published']) for row in student_filtered),
             'student cannot override publication visibility by filter')
    for query in ['limit=0', 'limit=201', 'after=-1', 'type_id=-1', 'published=garbage']:
        s.api('/assignments?' + query, s.admin, code=400)
    s.api(f'/types/{first_type["id"]}', s.admin, 'DELETE')
    s.expect(s.api(f'/assignments/{visible_id}', s.admin)['type_id'] == first_type['id'], 'type archive preserves existing assignment')
    s.api(f'/assignments/{visible_id}', s.admin, 'PUT', visible_body)
    s.expect(first_type['id'] not in ids(s.api('/types', s.admin))
             and first_type['id'] in ids(s.api('/types?archived=true', s.admin)),
             'type catalogue hides archived type by default and exposes it explicitly to staff')
    s.expect(first_type['id'] not in ids(s.api('/types?archived=true', student)),
             'student cannot expand active type catalogue by archived filter')
    s.api('/assignments', s.admin, 'POST', visible_body, 404)
    # Workspace access uses account IDs and current core group membership.
    own = s.api(f'/assignments/{visible_id}/workspace', student)
    s.expect(own['homework']['id'] == visible_id, 'workspace belongs to requested assignment')
    s.api(f'/assignments/{visible_id}/workspace?account_id={other_actor["account_id"]}', student, code=403)
    s.api(f'/assignments/{visible_id}/workspace?account_id={actor["account_id"]}', proctor)
    s.api(f'/assignments/{visible_id}/workspace?account_id={other_actor["account_id"]}', proctor, code=403)
    s.api(f'/assignments/{visible_id}/workspace?account_id={actor["account_id"]}', proctor2, code=403)
    s.api(f'/assignments/{visible_id}/students', student, code=403)
    rows = s.items(s.api(f'/assignments/{visible_id}/students', proctor))
    s.expect(all(row.get('group_id') == 1001 for row in rows), 'proctor roster includes only assigned group')
    s.api('/review-queue', student, code=403)
    s.api('/archive', student, code=403)
    s.api('/monitoring', student, code=403)
    s.api('/review-queue', viewer)
    s.api('/archive', viewer)
    s.api('/monitoring', viewer)
    for path in ['/review-queue?limit=0', '/archive?after=-1']:
        s.api(path, s.admin, code=400)
    # Limits are persisted per division, validated strictly, and changed by full admin.
    defaults = s.save_settings()
    reserve_defaults = s.save_settings(1002)
    s.expect(s.api('/settings', student)['max_source_bytes'] == defaults['max_source_bytes'], 'student can read upload policy')
    changed = {**defaults, 'max_source_bytes': 4 * 1024 * 1024, 'max_output_bytes': 1024 * 1024,
               'max_pages': 12, 'dpi': 100, 'jpeg_quality': 60, 'presign_ttl_seconds': 60, 'upload_stale_seconds': 120}
    s.api('/settings', viewer, 'PUT', changed, 403)
    s.api('/settings', editor, 'PUT', changed, 403, division=1002)
    s.api('/settings', proctor, 'PUT', changed, 403)
    s.api('/settings', s.admin, 'PUT', changed)
    actual = s.api('/settings', student)
    s.expect(all(actual[key] == value for key, value in changed.items()), 'settings round trip in database')
    sibling = s.api('/settings', s.admin, division=1002)
    s.expect(all(sibling[key] == value for key, value in reserve_defaults.items()), 'settings change isolated from sibling division')
    invalid = [('max_source_bytes', 0), ('max_source_bytes', 100 * 1024 * 1024 + 1),
               ('max_output_bytes', 0), ('max_output_bytes', 100 * 1024 * 1024 + 1),
               ('max_pages', 0), ('max_pages', 201), ('dpi', 71), ('dpi', 301),
               ('jpeg_quality', 34), ('jpeg_quality', 96), ('presign_ttl_seconds', 59),
               ('presign_ttl_seconds', 901), ('upload_stale_seconds', 119), ('upload_stale_seconds', 86401)]
    for field, value in invalid:
        s.api('/settings', s.admin, 'PUT', {**changed, field: value}, 400)
    s.api('/settings', s.admin, 'PUT', {**changed, 'max_pages': '12'}, 400)
    s.api('/settings', s.admin, 'PUT', {**changed, 'unknown': True}, 400)
    after_bad = s.api('/settings', s.admin)
    s.expect(all(after_bad[key] == value for key, value in changed.items()), 'invalid settings never partially mutate stored policy')
    s.api('/settings', s.admin, 'PUT', defaults)
    # Manual journal remains independent until a file workflow exists.
    date = datetime.now(ZoneInfo('Europe/Moscow')).date().isoformat()
    manual = {'account_id': actor['account_id'], 'date_pass': date, 'result': 87}
    s.api(f'/assignments/{visible_id}/manual', student, 'POST', manual, 403)
    s.api(f'/assignments/{visible_id}/manual', viewer, 'POST', manual, 403)
    s.api(f'/assignments/{visible_id}/manual', proctor2, 'POST', manual, 403)
    s.api(f'/assignments/{visible_id}/manual', proctor, 'POST', manual)
    legacy = s.api(f'/assignments/{visible_id}/workspace', student)['legacy_result']
    s.expect(legacy['result'] == 87 and bool(legacy['status']), 'manual grade visible to student')
    for bad in [True, 80.5, 'abc']:
        s.api(f'/assignments/{visible_id}/manual/{actor["account_id"]}', s.admin, 'PUT', {'date_pass': date, 'result': bad}, 400)
    for value, expected in [(-1, 0), (101, 100)]:
        answer = s.api(f'/assignments/{visible_id}/manual/{actor["account_id"]}', s.admin, 'PUT', {'date_pass': date, 'result': value})
        s.expect(answer['result'] == expected, 'legacy manual score clamps to0..100')
    s.api(f'/assignments/{visible_id}/manual/{actor["account_id"]}', s.admin, 'PUT', {'date_pass': date, 'result': 91})
    s.expect(s.api(f'/assignments/{visible_id}/workspace', student)['legacy_result']['result'] == 91, 'manual grade edit persisted')
    s.api(f'/assignments/{reserve_id}/manual', s.admin, 'POST',
          {'account_id': actor['account_id'], 'date_pass': date, 'result': 33}, division=1002)
    s.expect(s.api(f'/assignments/{reserve_id}/workspace', reserve_student, division=1002)['legacy_result']['result'] == 33
             and s.api(f'/assignments/{visible_id}/workspace', student)['legacy_result']['result'] == 91,
             'same account has independent homework grades in each division')
    s.api(f'/assignments/{visible_id}/manual/bulk', proctor, 'POST', {'group_id': 1002, 'date_pass': date, 'result': 70}, 403)
    bulk = s.api(f'/assignments/{visible_id}/manual/bulk', proctor, 'POST', {'group_id': 1001, 'date_pass': date, 'result': 70})
    s.expect(isinstance(bulk, dict), 'bulk manual operation returns explicit result')
    report = s.api('/report?date_from=' + date + '&date_to=' + date, s.admin)
    s.expect(bool(report), 'report returns journal records in requested date period')
    s.api('/report?date_from=bad&date_to=' + date, s.admin, code=400)
    s.api('/report?date_from=' + date + '&date_to=' + date + '&account_id=' + str(other_actor['account_id']), student, code=403)
    student_report = s.api('/report?date_from=' + date + '&date_to=' + (datetime.now(ZoneInfo('Europe/Moscow')).date() + timedelta(days=14)).isoformat(), student)
    s.expect(hidden_id not in {row['id'] for row in student_report['assignments']}, 'student report cannot disclose unpublished assignment')
    s.expect(all(row['account_id'] == actor['account_id'] for row in student_report['students']), 'student report contains only own results')
    report_marks = {row['assignment_id']: row['result'] for row in student_report['students'][0]['results']}
    s.expect(report_marks[visible_id]['result'] == 91, 'student report includes persisted grade for its visible assignment')
    # Auth introspection observes live permission change without refreshing token.
    s.core('/roles/' + str(role_ids[1]), s.admin, 'PUT', {
        'name': s.prefix + ' editor',
        'permissions': {section: {'view': True, 'edit': False} for section in
                        ['assignments', 'review-queue', 'homework-archive', 'monitoring']},
    })
    s.api(f'/assignments/{reserve_id}', editor, 'PUT', reserve_body, 403, division=1002)
    s.request('/v1/auth/logout', viewer, 'POST', origin=AUTH)
    s.api('/assignments', viewer, code=401)
    # Concurrent grading cannot create duplicate journal rows.
    with ThreadPoolExecutor(max_workers=6) as pool:
        list(pool.map(lambda _: s.api(f'/assignments/{visible_id}/manual/{actor["account_id"]}',
                                      s.admin, 'PUT', {'result': 92, 'date_pass': date}), range(6)))
    conn = mysql_connection(s.env)
    cur = conn.cursor()
    try:
        cur.execute('SELECT COUNT(*),MIN(result),MAX(result) FROM hw_results WHERE assignment_id=%s AND account_id=%s', (visible_id, actor['account_id']))
        row = cur.fetchone()
        s.expect(row == (1, 92, 92), 'concurrent manual edits preserve one journal row')
    finally:
        cur.close()
        conn.close()
    # Bulk marking must skip a grade committed while it was waiting for the lock.
    # For either ordering, the explicit individual87 must remain the final grade.
    for attempt in range(6):
        s.api(f'/assignments/{visible_id}/manual/{actor["account_id"]}', s.admin, 'PUT', {'status': 0})
        start = Barrier(2)
        def racing_mark(bulk):
            start.wait(timeout=5)
            if bulk:
                return s.api(f'/assignments/{visible_id}/manual/bulk', proctor, 'POST',
                             {'group_id': 1001, 'date_pass': date, 'result': 100})
            return s.api(f'/assignments/{visible_id}/manual', s.admin, 'POST',
                         {'account_id': actor['account_id'], 'date_pass': date, 'result': 87})
        with ThreadPoolExecutor(max_workers=2) as pool:
            list(pool.map(racing_mark, [True, False]))
        mark = s.api(f'/assignments/{visible_id}/workspace', student)['legacy_result']
        s.expect(mark['result'] == 87 and bool(mark['status']),
                 'racing bulk never overwrites explicit individual grade, iteration' + str(attempt + 1))
    s.api(f'/assignments/{hidden_id}', s.admin, 'DELETE')
    s.api(f'/assignments/{hidden_id}', s.admin, code=404)
    s.expect(hidden_id not in ids(s.api('/assignments', s.admin)), 'deleted assignment disappears from catalogue')
    # Read-only core contract returns current membership projections, not business rights.
    service_key = s.env['AUTH_INTERNAL_KEY']
    ref_path = '/internal/core/homework-references'
    ref = {'organization_id': 1001, 'division_id': 1001}
    s.request(ref_path, s.admin, 'POST', ref, 401, origin=CORE)
    s.request(ref_path, method='POST', body=ref, code=401, headers={'X-Service-Key': 'wrong-key'}, origin=CORE)
    for bad in [{**ref, 'organization_id': 2001}, {**ref, 'division_id': 2001},
                {**ref, 'account_ids': [math_actor['account_id']]}, {**ref, 'group_id': 999999999}]:
        s.request(ref_path, method='POST', body=bad, code=404, headers={'X-Service-Key': service_key}, origin=CORE)
    for bad in [{**ref, 'account_ids': [actor['account_id'], actor['account_id']]},
                {**ref, 'account_ids': list(range(1, 1002))}, {**ref, 'account_ids': [-1]},
                {**ref, 'group_id': 0}, {**ref, 'unknown': 1}]:
        s.request(ref_path, method='POST', body=bad, code=400, headers={'X-Service-Key': service_key}, origin=CORE)
    scoped = s.request(ref_path, method='POST', body={**ref, 'account_ids': [actor['account_id']]},
                       headers={'X-Service-Key': service_key}, origin=CORE)['data']
    s.expect(len(scoped['members']) == 1 and scoped['members'][0]['account_id'] == actor['account_id'],
             'core internal reference is bounded to requested active account')
    s.request(ref_path, method='POST', body={**ref, 'account_ids': [actor['account_id']], 'group_id': 1002},
              code=404, headers={'X-Service-Key': service_key}, origin=CORE)
    s.core(f'/divisions/1001/users/{actor["account_id"]}/active', s.admin, 'PUT', {'is_active': False})
    s.request(ref_path, method='POST', body={**ref, 'account_ids': [actor['account_id']]},
              code=404, headers={'X-Service-Key': service_key}, origin=CORE)
    disabled_proctor, group_id = s.fixture_proctor()
    s.core(f'/divisions/1001/users/{disabled_proctor}/active', s.admin, 'PUT', {'is_active': False})
    projection = s.request(ref_path, method='POST', body={**ref, 'group_id': group_id},
                           headers={'X-Service-Key': service_key}, origin=CORE)['data']
    s.expect(projection['groups'][0]['proctor_account_id'] is None and not projection['members'],
             'disabled proctor removed from active members and group assignment projection')


if __name__ == '__main__':
    suite = Suite()
    error = None
    try:
        main(suite)
    except Exception as exc:
        # Do not serialize raw response bodies, tokens or dependency URLs.
        error = str(exc) if isinstance(exc, AssertionError) else type(exc).__name__
    finally:
        try:
            suite.cleanup()
        except Exception as exc:
            error = error or ('cleanup failed: ' + type(exc).__name__)
    suite.report('homework-http-report.json', 'failed' if error else 'passed', error)
    if error:
        raise SystemExit(error)
