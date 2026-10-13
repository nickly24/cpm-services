"""Leave one owned uploading job for a coordinated read-only Uploads proof.

Create:  dev/.venv/bin/python services/homework/scripts/uploads_fixture.py create
Cleanup: dev/.venv/bin/python services/homework/scripts/uploads_fixture.py cleanup

The fixed 0600 handoff contains synthetic identity only. Passwords are obtained
from the existing canonical dev/local/accounts.json; tokens, presigned fields,
storage keys and upload policy never leave memory. No PDF upload, completion,
settings change, service restart or broker mutation is performed.
"""
import argparse
from datetime import datetime, timezone
import json
import os
import re
import urllib.error
import uuid

from http_test import AUTH, ROOT, Suite, mysql_connection


HANDOFF = ROOT / 'dev/local/homework-uploads-fixture.json'
FIELDS = {'synthetic_only', 'organization_id', 'division_id', 'student_login',
          'account_id', 'job_id', 'assignment_id', 'type_id', 'run', 'prefix', 'created_at'}


def logout(s):
    for token in s.sessions:
        s.request('/v1/auth/logout', token, 'POST', origin=AUTH)
    s.sessions.clear()


def write_handoff(data, *, create=False):
    flags = os.O_WRONLY | os.O_CREAT | (os.O_EXCL if create else os.O_TRUNC)
    fd = os.open(HANDOFF, flags, 0o600)
    with os.fdopen(fd, 'w') as stream:
        json.dump(data, stream, ensure_ascii=False, indent=2)
        stream.write('\n')
    HANDOFF.chmod(0o600)


def validate_handoff():
    if HANDOFF.is_symlink() or not HANDOFF.is_file():
        raise AssertionError('Synthetic handoff must be a regular file at the fixed local dev path')
    data = json.loads(HANDOFF.read_text())
    if not isinstance(data, dict) or set(data) != FIELDS:
        raise AssertionError('Synthetic handoff fields do not match the minimal identity contract')
    if data['synthetic_only'] is not True or data['organization_id'] != 1001 or data['division_id'] != 1001:
        raise AssertionError('Synthetic handoff organization/division guard')
    run = data['run']
    if not isinstance(run, str) or not re.fullmatch('[0-9a-f]{12}', run):
        raise AssertionError('Synthetic handoff run guard')
    if data['prefix'] != 'DEV Homework Uploads ' + run or data['student_login'] != 'dev_homework_' + run + '_0':
        raise AssertionError('Synthetic handoff prefix and account guard')
    if any(type(data[field]) is not int or data[field] < 1 for field in ('account_id', 'assignment_id', 'type_id')):
        raise AssertionError('Synthetic handoff positive identity guard')
    if str(uuid.UUID(data['job_id'])) != data['job_id']:
        raise AssertionError('Synthetic handoff job UUID guard')
    created = datetime.fromisoformat(data['created_at'].replace('Z', '+00:00'))
    if created.tzinfo is None or created.utcoffset().total_seconds() != 0:
        raise AssertionError('Synthetic handoff UTC timestamp guard')
    return data


def public_summary(data, state):
    return {'state': state, 'synthetic_only': True, 'handoff': str(HANDOFF),
            'organization_id': data['organization_id'], 'division_id': data['division_id'],
            'account_id': data['account_id'], 'assignment_id': data['assignment_id'],
            'type_id': data['type_id'], 'job_id': data['job_id']}


def create():
    if HANDOFF.exists() or HANDOFF.is_symlink():
        raise AssertionError('Existing handoff must be cleaned before creating another fixture')
    s = Suite('Uploads')
    saved = False
    try:
        s.request('/health/ready')
        s.admin, _ = s.login('dev_admin')
        student, actor, student_login = s.fixture_student()
        kind = s.type()
        assignment, _ = s.assignment(kind['id'], published=True)
        upload = s.api('/assignments/' + str(assignment['id']) + '/uploads', student, 'POST',
                       {'client_upload_id': str(uuid.uuid4())}, 201)
        s.expect(upload['job']['status'] == 'uploading',
                 'interoperability fixture is only uploading and has no compressed file')
        data = {'synthetic_only': True, 'organization_id': 1001, 'division_id': 1001,
                'student_login': student_login, 'account_id': actor['account_id'],
                'job_id': upload['job']['id'], 'assignment_id': assignment['id'],
                'type_id': kind['id'], 'run': s.run, 'prefix': s.prefix,
                'created_at': datetime.now(timezone.utc).isoformat().replace('+00:00', 'Z')}
        logout(s)
        write_handoff(data, create=True)
        saved = True
        print(json.dumps(public_summary(data, 'uploading')))
    finally:
        if not saved:
            s.cleanup()


def cleanup():
    data = validate_handoff()
    s = Suite('Uploads')
    s.run, s.prefix = data['run'], data['prefix']
    conn = mysql_connection(s.env)
    cur = conn.cursor(dictionary=True)
    try:
        cur.execute('SELECT h.name,h.type_id,h.organization_id,h.division_id,j.account_id,j.status '
                    'FROM hw_assignments h JOIN hw_jobs j ON j.assignment_id=h.id '
                    'WHERE h.id=%s AND j.id=%s', (data['assignment_id'], data['job_id']))
        own = cur.fetchone()
        cur.execute('SELECT username FROM auth_users WHERE id=%s', (data['account_id'],))
        account = cur.fetchone()
        cur.execute('SELECT name,organization_id,division_id FROM hw_types WHERE id=%s', (data['type_id'],))
        kind = cur.fetchone()
        if not own or not account or not kind or not own['name'].startswith(s.prefix) or not kind['name'].startswith(s.prefix):
            raise AssertionError('Database fixture prefix guard')
        if own['account_id'] != data['account_id'] or own['type_id'] != data['type_id'] or account['username'] != data['student_login']:
            raise AssertionError('Database fixture identity guard')
        if any(row['organization_id'] != 1001 or row['division_id'] != 1001 for row in (own, kind)):
            raise AssertionError('Database fixture scope guard')
        if own['status'] not in {'uploading', 'cancelled'}:
            raise AssertionError('Read-only peer proof must leave fixture uploading or cancelled')
    finally:
        cur.close()
        conn.close()
    s.users = [data['account_id']]
    s.assignments = [(1001, data['assignment_id'])]
    s.types = [(1001, data['type_id'])]
    s.admin, _ = s.login('dev_admin')
    try:
        student, _ = s.login(data['student_login'])
        cancelled = s.api('/jobs/' + data['job_id'] + '/cancel', student, 'POST', {})
        s.expect(cancelled['status'] == 'cancelled', 'own interoperability job is cancelled before deletion')
    finally:
        s.cleanup()
    conn = mysql_connection(s.env)
    cur = conn.cursor()
    try:
        for table, key in (('hw_jobs', data['job_id']), ('hw_assignments', data['assignment_id']),
                           ('hw_types', data['type_id']), ('auth_users', data['account_id'])):
            cur.execute('SELECT COUNT(*) FROM ' + table + ' WHERE id=%s', (key,))
            s.expect(cur.fetchone()[0] == 0, 'interoperability cleanup leaves no owned ' + table + ' record')
    finally:
        cur.close()
        conn.close()
    HANDOFF.unlink()
    print(json.dumps({**public_summary(data, 'cleaned'), 'synthetic_records_cleaned': s.cleaned,
                      'own_records_absent': True, 'settings_changed': False,
                      'services_paused_or_restarted': False}))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('create', 'cleanup'))
    args = parser.parse_args()
    try:
        create() if args.action == 'create' else cleanup()
    except Exception as exc:
        if isinstance(exc, AssertionError):
            raise SystemExit(str(exc)) from None
        if isinstance(exc, urllib.error.URLError):
            raise SystemExit('URLError: ' + type(exc.reason).__name__) from None
        raise SystemExit(type(exc).__name__) from None
