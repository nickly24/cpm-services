"""Add auth context version in fixed local dev only; no fixtures are changed."""
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'dev/scripts'))
from bootstrap import settings, mysql_connection


def migrate(conn):
    cur = conn.cursor()
    try:
        cur.execute('SELECT DATABASE()')
        assert cur.fetchone()[0] == 'cpm_dev', 'Only cpm_dev allowed'
        cur.execute("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='auth_sessions' AND column_name='context_version'")
        if cur.fetchone()[0] == 0:
            cur.execute((ROOT / 'services/auth/migrations/002_session_context_version.sql').read_text())
        cur.execute("SELECT data_type,is_nullable,column_default FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='auth_sessions' AND column_name='context_version'")
        assert cur.fetchone() == ('bigint', 'NO', '1'), 'Unexpected context_version definition'
        conn.commit()
    finally:
        cur.close()


if __name__ == '__main__':
    # bootstrap.mysql_connection uses only 127.0.0.1 and the configured local dev port.
    values = settings()
    assert str(values['MYSQL_PORT']) == '13306' and values['MYSQL_DATABASE'] == 'cpm_dev'
    conn = mysql_connection(values)
    try:
        migrate(conn)
        print('auth context_version migration verified')
    finally:
        conn.close()
