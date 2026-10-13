"""Read-only invariants after integration fixtures; verify.py is bootstrap-only."""
import json
from bson import json_util
from werkzeug.security import check_password_hash
from bootstrap import BASE, settings, mysql_connection, mongo_client
from verify import normalize_ddl, core_school_ddl_005


def main():
    configuration = settings()
    expected = json.loads((BASE / 'schema/mysql.json').read_text())
    declared = set(json.loads((BASE / 'schema/extensions.json').read_text()))
    accounts = json.loads((BASE / 'local/accounts.json').read_text())
    original_logins = {account['login'] for account in accounts['accounts']}
    connection = mysql_connection(configuration)
    cursor = connection.cursor()
    try:
        cursor.execute('SHOW TABLES')
        tables = {row[0] for row in cursor.fetchall()}
        assert set(expected['ddl']) <= tables, 'Original MySQL table missing'
        assert not tables - set(expected['ddl']) - declared, 'Undeclared extension table'
        for name, ddl in expected['ddl'].items():
            cursor.execute('SHOW CREATE TABLE `' + name.replace('`', '``') + '`')
            if name == 'schools' and 'core_legacy_rating_scope' in tables:
                ddl = core_school_ddl_005(ddl)
            assert normalize_ddl(cursor.fetchone()[1]) == normalize_ddl(ddl), f'DDL differs: {name}'
        cursor.execute('SELECT COUNT(*) FROM students WHERE id BETWEEN 1001 AND 1012 AND full_name LIKE %s', ('DEV %',))
        assert cursor.fetchone()[0] == 12, 'Original synthetic student identity missing'
        cursor.execute('SELECT username,password FROM auth_users')
        original = [row for row in cursor.fetchall() if row[0] in original_logins]
        assert {row[0] for row in original} == original_logins, 'Original login missing'
        assert all(check_password_hash(row[1], accounts['password']) for row in original), 'Original password changed'
        cursor.execute('SELECT COUNT(*) FROM students')
        student_count = cursor.fetchone()[0]
        cursor.execute('SELECT COUNT(*) FROM students s LEFT JOIN auth_users a ON a.ref_id=s.id AND a.role=%s WHERE a.id IS NULL', ('student',))
        unlinked_students = cursor.fetchone()[0]
        assert unlinked_students == 0, 'Student has no local Auth account'
    finally:
        cursor.close()
        connection.close()
    metadata = json_util.loads((BASE / 'schema/mongo.json').read_text())
    extension_file = BASE / 'schema/mongo-extensions.json'
    mongo_extensions = set(json.loads(extension_file.read_text())) if extension_file.exists() else set()
    client = mongo_client(configuration)
    extra_indexes = {}
    try:
        db = client.cpm_dev
        originals = {collection['name'] for collection in metadata['collections']}
        actual = set(db.list_collection_names())
        assert originals <= actual, 'Original Mongo collection missing'
        assert not actual - originals - mongo_extensions, 'Undeclared Mongo collection'
        def spec(index):
            return {key: value for key, value in index.items() if key not in {'v', 'ns', 'background'}}
        for collection in metadata['collections']:
            indexes = {index['name']: spec(index) for index in db[collection['name']].list_indexes()}
            baseline = {index['name']: spec(index) for index in collection['indexes']}
            assert all(indexes.get(name) == index for name, index in baseline.items()), 'Original Mongo index differs: ' + collection['name']
            assert db[collection['name']].options() == collection['options'], 'Mongo options differ'
            added = sorted(set(indexes) - set(baseline))
            if added:
                extra_indexes[collection['name']] = added
    finally:
        client.close()
    result = {'status': 'passed', 'read_only': True,
              'original_mysql_ddl_verified': len(expected['ddl']),
              'school_ddl_exception': 'exact Core migration 005',
              'original_logins_and_passwords_verified': len(original),
              'original_synthetic_students_verified': 12,
              'students_with_auth_account': student_count,
              'original_mongo_collections_and_indexes_verified': len(metadata['collections']),
              'additional_mongo_indexes': extra_indexes,
              'limitations': ['Integration fixtures may have non-DEV names and non-dev_ logins; these are not bootstrap invariants.',
                              'Original school updated_at preservation is unproven; migration 005 initially changed four synthetic rows. No guessed restoration was performed.',
                              'This verifies identity, credentials and structure, not preservation of every business field.']}
    (BASE / 'local/integrated-verification.json').write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
    print(json.dumps(result, ensure_ascii=False))


if __name__ == '__main__':
    main()
