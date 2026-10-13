"""Provision fixtures only in the fixed local cpm_dev MySQL; never import production."""
import json
import os
import sys
from pathlib import Path
from werkzeug.security import generate_password_hash

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'dev/scripts'))
from bootstrap import settings, mysql_connection
from migrate_context_version import migrate


def main():
    v = settings()
    conn = mysql_connection(v)
    cur = conn.cursor(dictionary=True)
    try:
        cur.execute('SELECT DATABASE() AS name, @@hostname AS host')
        assert cur.fetchone()['name'] == 'cpm_dev'
        migration = (ROOT / 'services/auth/migrations/001_auth_context.sql').read_text()
        for statement in migration.split(';'):
            if statement.strip():
                cur.execute(statement)
        conn.commit()
        migrate(conn)
        marker = ROOT / 'dev/local/auth-seed-complete.json'
        if not marker.exists():
            password = json.loads((ROOT / 'dev/local/accounts.json').read_text())['password']
            cur.execute('SELECT id,username,role,ref_id FROM auth_users ORDER BY id')
            existing = cur.fetchall()
            assert existing and all(a['username'].startswith('dev_') for a in existing), 'Only synthetic dev accounts allowed'
            cur.executemany('INSERT INTO organizations (id,name) VALUES (%s,%s)', [(1001, 'DEV Олимпиады по праву'), (2001, 'DEV Олимпиады по математике')])
            cur.executemany('INSERT INTO divisions (id,organization_id,name) VALUES (%s,%s,%s)', [(1001,1001,'DEV Основная сборная'), (1002,1001,'DEV Резерв'), (1003,1001,'DEV Профильные школы'), (2001,2001,'DEV Математика')])
            cur.executemany('INSERT INTO auth_scoped_roles (id,organization_id,name) VALUES (%s,%s,%s)', [(1001,1001,'DEV Просмотр'), (1002,1001,'DEV Редактор'), (2001,2001,'DEV Математика — просмотр')])
            cur.executemany('INSERT INTO auth_scoped_role_permissions (role_id,section,can_view,can_edit) VALUES (%s,%s,%s,%s)', [(1001,'tests',1,0), (1001,'ratings',1,0), (1002,'tests',1,1), (1002,'exams',1,1), (2001,'tests',1,0)])
            fixtures = []
            for a in existing:
                uid = a['id']
                cur.execute('INSERT INTO auth_account_state (user_id) VALUES (%s)', (uid,))
                cur.execute('INSERT INTO auth_organization_access VALUES (%s,1001)', (uid,))
                if a['role'] != 'admin':
                    group_id = None
                    if a['role'] in {'student', 'proctor'}:
                        table = 'students' if a['role'] == 'student' else 'proctors'
                        cur.execute(f'SELECT group_id FROM {table} WHERE id=%s', (a['ref_id'],))
                        group_id = cur.fetchone()['group_id']
                    cur.execute('INSERT INTO auth_division_access VALUES (%s,1001,1001,%s,%s)', (uid, 1001 if a['role']=='staff_admin' else None, group_id))
                fixtures.append({'login':a['username'],'role':a['role'],'expected_login':200})
                if a['username'] in {'dev_student_01', 'dev_proctor_1', 'dev_examiner_1', 'dev_staff'}:
                    cur.execute('INSERT INTO auth_division_access VALUES (%s,1001,1002,%s,NULL)', (uid,1002 if a['role']=='staff_admin' else None))
                if a['username'] == 'dev_student_01':
                    cur.execute('INSERT INTO auth_organization_access VALUES (%s,2001)', (uid,))
                    cur.execute('INSERT INTO auth_division_access VALUES (%s,2001,2001,NULL,NULL)', (uid,))
            extras = [('dev_auth_scrypt','student',2001,200), ('dev_auth_disabled','student',2002,401), ('dev_auth_unscoped','student',2003,403), ('dev_auth_plaintext','student',2004,401), ('dev_auth_version','student',2005,200), ('dev_auth_membership','student',2006,200), ('dev_auth_orphan','student',2999,401), ('dev_admin_math','admin',2001,200), ('dev_staff_disabled','staff_admin',2001,401)]
            for login, role, entity_id, expected in extras:
                if role == 'student' and entity_id != 2999:
                    cur.execute("INSERT INTO students (id,full_name,`class`,group_id,school_id,tg_name) VALUES (%s,%s,10,NULL,NULL,'')", (entity_id, 'DEV Auth '+login))
                elif role == 'admin':
                    cur.execute('INSERT INTO admins (id,full_name) VALUES (%s,%s)', (entity_id,'DEV Администратор математики'))
                elif role == 'staff_admin':
                    cur.execute('INSERT INTO admin_role_users (id,full_name,role_id,is_active) VALUES (%s,%s,1001,0)', (entity_id,'DEV Отключённый сотрудник'))
                hashed = generate_password_hash(password, method='scrypt:32768:8:1' if login=='dev_auth_scrypt' else 'pbkdf2:sha256:600000')
                if login == 'dev_auth_plaintext':
                    hashed = password  # Deliberate negative fixture: rejected by the new service.
                cur.execute('INSERT INTO auth_users (username,password,role,ref_id) VALUES (%s,%s,%s,%s)', (login,hashed,role,entity_id))
                uid = cur.lastrowid
                cur.execute('INSERT INTO auth_account_state (user_id,is_active) VALUES (%s,%s)', (uid,login!='dev_auth_disabled'))
                if login != 'dev_auth_unscoped':
                    org = 2001 if role=='admin' else 1001
                    cur.execute('INSERT INTO auth_organization_access VALUES (%s,%s)', (uid,org))
                    if role!='admin':
                        cur.execute('INSERT INTO auth_division_access VALUES (%s,%s,1001,%s,NULL)', (uid,org,1001 if role=='staff_admin' else None))
                fixtures.append({'login':login,'role':role,'expected_login':expected})
            conn.commit()
            marker.write_text(json.dumps({'organizations':2,'divisions':4,'scoped_roles':3,'accounts':len(fixtures),'fixtures':fixtures},ensure_ascii=False,indent=2)+'\n')
            marker.chmod(0o600)
            print('Auth fixtures created: 2 organizations, 4 divisions, 3 scoped roles, 29 accounts including negative cases.')
        else:
            print('Existing auth fixtures retained.')
    except Exception:
        conn.rollback()
        raise
    finally:
        cur.close()
        conn.close()
    common = {'MYSQL_HOST':'127.0.0.1','MYSQL_PORT':'13306','MYSQL_DATABASE':'cpm_dev','MYSQL_USER':v['MYSQL_USER'],'MYSQL_PASSWORD':v['MYSQL_PASSWORD'],
              'AUTH_LISTEN_ADDR':'127.0.0.1:8002','AUTH_PRIVATE_KEY_FILE':str(ROOT/'dev/local/auth-private.pem'),'AUTH_ISSUER':'cpm-auth-dev','AUTH_AUDIENCE':'cpm-services'}
    if v.get('AUTH_INTERNAL_KEY'):
        common['AUTH_INTERNAL_KEY']=v['AUTH_INTERNAL_KEY']
    p=ROOT/'dev/local/auth.env'
    p.write_text('\n'.join(f'{k}={value}' for k,value in common.items())+'\n');p.chmod(0o600)
    env_path=ROOT/'dev/.env'
    content=env_path.read_text()
    for key,value in {'AUTH_UID':os.getuid(),'AUTH_GID':os.getgid()}.items():
        if key+'=' not in content:
            content+=f'{key}={value}\n'
    env_path.write_text(content);env_path.chmod(0o600)


if __name__=='__main__':
    main()
