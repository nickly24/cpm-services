"""Add only attendance tables and private config in fixed loopback cpm_dev."""
import json
import secrets
import sys
import fcntl
from pathlib import Path
ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'dev/scripts'))
from bootstrap import settings, mysql_connection
from export_structure import read_env

def main():
    values = settings()
    conn = mysql_connection(values)
    try:
        cur = conn.cursor()
        cur.execute('SELECT DATABASE()')
        assert cur.fetchone()[0] == 'cpm_dev'
        for sql in (ROOT/'services/attendance/migrations/001_attendance.sql').read_text().split(';'):
            if sql.strip(): cur.execute(sql)
        conn.commit()
    finally: conn.close()
    # Lock/read/merge the current allowlist so neighbours' additions are retained.
    with (ROOT/'dev/schema/extensions.json').open('r+') as f:
        fcntl.flock(f, fcntl.LOCK_EX)
        tables = set(json.load(f))
        tables.update(['attendance_state_v1','attendance_audit_v1','attendance_receipts_v1'])
        f.seek(0); json.dump(sorted(tables), f, indent=2); f.write('\n'); f.truncate()
    path = ROOT/'dev/local/attendance.env'
    old = read_env(path) if path.exists() else {}
    env = {key:values[key] for key in ['MYSQL_USER','MYSQL_PASSWORD','AUTH_INTERNAL_KEY']}
    env.update(MYSQL_HOST='127.0.0.1',MYSQL_PORT='13306',MYSQL_DATABASE='cpm_dev',ATTENDANCE_LISTEN_ADDR='127.0.0.1:8013',ATTENDANCE_AUTH_URL='http://127.0.0.1:8002',ATTENDANCE_CORE_URL='http://127.0.0.1:8003',ATTENDANCE_QR_KEY=old.get('ATTENDANCE_QR_KEY',secrets.token_hex(32)))
    path.touch(mode=0o600, exist_ok=True);path.chmod(0o600)
    path.write_text('\n'.join(f'{k}={v}' for k,v in env.items())+'\n')
    print('Only attendance tables migrated; private dev config prepared. No foreign data modified.')
if __name__ == '__main__':main()
