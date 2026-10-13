"""Apply Auth migration 003 only in fixed local Dev, in a coordinated window."""
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
        sql = (ROOT / 'services/auth/migrations/003_import_operation_grants.sql').read_text()
        # This frozen migration contains one CREATE. Semicolons in comments
        # are not statement boundaries; send the faithful SQL as one command.
        cur.execute(sql)
        cur.execute("SELECT column_name,data_type,is_nullable FROM information_schema.columns "
                    "WHERE table_schema=DATABASE() AND table_name='auth_operation_grants' ORDER BY ordinal_position")
        assert cur.fetchall() == [
            ('id', 'char', 'NO'), ('issuance_key', 'char', 'NO'), ('input_hash', 'char', 'NO'),
            ('mode', 'enum', 'NO'), ('parent_id', 'char', 'YES'), ('account_id', 'int', 'NO'),
            ('organization_id', 'bigint', 'NO'), ('division_id', 'bigint', 'NO'),
            ('profile_id', 'bigint', 'NO'), ('auth_version', 'bigint', 'NO'), ('role', 'varchar', 'NO'),
            ('binding', 'json', 'NO'), ('issued_at', 'datetime', 'NO'), ('expires_at', 'datetime', 'NO'),
            ('revoked', 'tinyint', 'NO')], 'Unexpected operation grant schema; runtime must remain disabled'
        cur.execute('SHOW INDEX FROM auth_operation_grants')
        indices = {}
        for row in cur.fetchall():
            name, sequence, column, non_unique = row[2], row[3], row[4], row[1]
            indices.setdefault(name, {'non_unique': non_unique, 'columns': {}})['columns'][sequence] = column
        expected = {
            'PRIMARY': (0, ['id']), 'uq_operation_issuance': (0, ['account_id', 'issuance_key']),
            'ix_operation_actor_scope': (1, ['account_id', 'organization_id', 'division_id']),
            'ix_operation_expiry': (1, ['expires_at'])}
        assert set(indices) == set(expected), 'Unexpected grant indices'
        for name, (non_unique, columns) in expected.items():
            actual = indices[name]
            assert actual['non_unique'] == non_unique and [actual['columns'][n] for n in sorted(actual['columns'])] == columns, 'Grant issuance uniqueness/index definition differs'
        cur.execute("SELECT column_name,character_set_name,collation_name,datetime_precision "
                    "FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='auth_operation_grants'")
        metadata = {row[0]: row[1:] for row in cur.fetchall()}
        for name in ['id', 'issuance_key', 'input_hash', 'parent_id', 'role']:
            assert metadata[name][:2] == ('ascii', 'ascii_bin'), 'Grant credential metadata collation differs'
        for name in ['issued_at', 'expires_at']:
            assert metadata[name][2] == 6, 'Grant timestamp precision differs'
        cur.execute("SELECT constraint_type,COUNT(*) FROM information_schema.table_constraints "
                    "WHERE table_schema=DATABASE() AND table_name='auth_operation_grants' GROUP BY constraint_type")
        assert dict(cur.fetchall()) == {'PRIMARY KEY': 1, 'UNIQUE': 1, 'CHECK': 2}, 'Grant constraints differ; no cascading foreign keys permitted'
        conn.commit()
    finally:
        cur.close()


if __name__ == '__main__':
    values = settings()
    assert str(values['MYSQL_PORT']) == '13306' and values['MYSQL_DATABASE'] == 'cpm_dev'
    conn = mysql_connection(values)
    try:
        migrate(conn)
        print('Auth operation grant metadata schema verified in local Dev.')
    finally:
        conn.close()
