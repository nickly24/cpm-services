"""Verify the local structure and synthetic fixtures without reading production."""
import json
import re
from bson import json_util
from werkzeug.security import check_password_hash
from bootstrap import BASE, settings, mysql_connection, mongo_client


def normalize_ddl(ddl):
    ddl = re.sub(r' AUTO_INCREMENT=\d+', '', ddl)
    # SHOW CREATE formatting changed between MySQL 8.0 patch releases.
    ddl = re.sub(r'\bCHARSET=utf8\b', 'CHARSET=utf8mb3', ddl)
    ddl = ddl.replace(' CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci',
                      ' COLLATE utf8mb4_unicode_ci')
    return ddl


def core_school_ddl_005(original):
    """Exact approved change; all original school columns remain verified."""
    legacy_index = '  UNIQUE KEY `uq_schools_name` (`name`)'
    if original.count(legacy_index) != 1 or '`organization_id`' in original:
        raise AssertionError('Unknown baseline school DDL; no exception applied')
    original = original.replace('  PRIMARY KEY (`id`),',
        '  `organization_id` bigint DEFAULT NULL,\n  PRIMARY KEY (`id`),')
    return original.replace(legacy_index,
        '  UNIQUE KEY `uq_schools_organization_name` (`organization_id`,`name`),\n'
        '  CONSTRAINT `fk_core_school_organization` FOREIGN KEY (`organization_id`) REFERENCES `organizations` (`id`)')


def main():
    v = settings()
    expected = json.loads((BASE / 'schema/mysql.json').read_text())
    conn = mysql_connection(v)
    cur = conn.cursor()
    try:
        cur.execute('SHOW TABLES')
        actual_tables={r[0] for r in cur.fetchall()}
        extensions=set(json.loads((BASE/'schema/extensions.json').read_text()))
        assert set(expected['ddl']) <= actual_tables, 'Original MySQL tables missing'
        assert not actual_tables-set(expected['ddl'])-extensions, 'Unexpected MySQL tables'
        for name, ddl in expected['ddl'].items():
            cur.execute('SHOW CREATE TABLE `' + name.replace('`', '``') + '`')
            actual = cur.fetchone()[1]
            if name == 'schools' and 'core_legacy_rating_scope' in actual_tables:
                ddl = core_school_ddl_005(ddl)
            assert normalize_ddl(actual) == normalize_ddl(ddl), f'DDL differs: {name}'
        cur.execute('SELECT COUNT(*) FROM students')
        students = cur.fetchone()[0]
        cur.execute('SELECT COUNT(*) FROM students WHERE id BETWEEN 1001 AND 1012')
        assert cur.fetchone()[0] == 12, 'Original synthetic students missing'
        cur.execute("SELECT COUNT(*) FROM students WHERE full_name NOT LIKE 'DEV %'")
        assert cur.fetchone()[0] == 0, 'Unexpected student names'
        accounts = json.loads((BASE / 'local/accounts.json').read_text())
        cur.execute('SELECT username,password FROM auth_users')
        all_users = cur.fetchall()
        original_logins={a['login'] for a in accounts['accounts']}
        users=[row for row in all_users if row[0] in original_logins]
        assert {row[0] for row in users} == original_logins
        assert all(row[0].startswith('dev_') for row in all_users), 'Unexpected non-dev account'
        assert all(check_password_hash(row[1], accounts['password']) for row in users)
        cur.execute('SELECT VERSION()')
        mysql_version = cur.fetchone()[0]
    finally:
        cur.close()
        conn.close()
    meta = json_util.loads((BASE / 'schema/mongo.json').read_text())
    client = mongo_client(v)
    try:
        db = client.cpm_dev
        originals={c['name'] for c in meta['collections']}
        extension_file=BASE/'schema/mongo-extensions.json'
        extensions=set(json.loads(extension_file.read_text())) if extension_file.exists() else set()
        actual_collections=set(db.list_collection_names())
        assert originals <= actual_collections, 'Original Mongo collections missing'
        assert not actual_collections-originals-extensions, 'Unexpected Mongo collections'
        def index_spec(index):
            return {k: val for k, val in index.items() if k not in {'v', 'ns', 'background'}}
        for coll in meta['collections']:
            actual = {i['name']: index_spec(i) for i in db[coll['name']].list_indexes()}
            wanted = {i['name']: index_spec(i) for i in coll['indexes']}
            assert actual == wanted, 'Mongo indexes differ: ' + coll['name']
            assert db[coll['name']].options() == coll['options']
        assert db.tests.count_documents({}) == 1
        assert db.test_versions.count_documents({}) == 1
        assert db.schedule.count_documents({}) == 1
        for coll in meta['collections']:
            if coll['name'] not in {'tests', 'test_versions', 'schedule'}:
                assert db[coll['name']].count_documents({}) == 0
        mongo_version = client.server_info()['version']
    finally:
        client.close()
    result = {'mysql_version': mysql_version, 'mysql_tables_ddl_verified': len(expected['ddl']),
              'school_ddl_policy': 'scoped Core migration 005' if 'core_legacy_rating_scope' in actual_tables else 'original',
              'mongo_version': mongo_version, 'mongo_collections_and_indexes_verified': len(meta['collections']),
              'synthetic_students': students, 'passwords_verified': len(users), 'status': 'passed'}
    (BASE / 'local/verification.json').write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result))


if __name__ == '__main__':
    main()
