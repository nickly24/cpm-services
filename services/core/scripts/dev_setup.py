"""Only local dev migrations and synthetic fixtures. Does not access production."""
import json
import secrets
import sys
from pathlib import Path
ROOT=Path(__file__).resolve().parents[3]
sys.path.insert(0,str(ROOT/'dev/scripts'))
from bootstrap import settings,mysql_connection
from export_structure import read_env
from werkzeug.security import generate_password_hash

def main():
 v=settings();conn=mysql_connection(v);cur=conn.cursor(dictionary=True)
 try:
  cur.execute('SELECT DATABASE() name');assert cur.fetchone()['name']=='cpm_dev'
  for sql in (ROOT/'services/core/migrations/001_core.sql').read_text().split(';'):
   if sql.strip():cur.execute(sql)
  for sql in (ROOT/'services/core/migrations/003_organization_test_settings.sql').read_text().split(';'):
   if sql.strip():cur.execute(sql)
  for sql in (ROOT/'services/core/migrations/004_attendance_history_reservations.sql').read_text().split(';'):
   if sql.strip():cur.execute(sql)
  cur.execute("SELECT COUNT(*) n FROM information_schema.columns WHERE table_schema='cpm_dev' AND table_name='core_direction_scope' AND column_name='division_id'")
  if cur.fetchone()['n']:
   # Resume after any individual DDL statement; MySQL DDL is not transactional.
   cur.execute("SELECT DISTINCT constraint_name AS name FROM information_schema.key_column_usage WHERE table_schema='cpm_dev' AND table_name='core_direction_scope' AND referenced_table_name='divisions'")
   for row in cur.fetchall():
    name=row['name']
    assert name.replace('_','').isalnum()
    cur.execute('ALTER TABLE core_direction_scope DROP FOREIGN KEY `'+name+'`')
   cur.execute("SELECT COUNT(*) n FROM information_schema.key_column_usage WHERE table_schema='cpm_dev' AND table_name='core_direction_scope' AND referenced_table_name='organizations'")
   if not cur.fetchone()['n']:
    cur.execute('ALTER TABLE core_direction_scope ADD CONSTRAINT fk_core_direction_organization FOREIGN KEY (organization_id) REFERENCES organizations(id)')
   cur.execute('ALTER TABLE core_direction_scope DROP COLUMN division_id')
  cur.execute('INSERT IGNORE INTO core_id_allocator(id) SELECT GREATEST((SELECT COALESCE(MAX(id),0) FROM divisions),(SELECT COALESCE(MAX(id),0) FROM auth_scoped_roles),9999)')
  conn.commit()
  marker=ROOT/'dev/local/core-seed-complete.json'
  if not marker.exists():
   cur.execute('SELECT id,username,role FROM auth_users ORDER BY id');accounts=cur.fetchall()
   assert all(a['username'].startswith('dev_') for a in accounts)
   for a in accounts:
    cur.execute('INSERT IGNORE INTO core_user_owners VALUES(%s,%s)',(a['id'],2001 if a['username']=='dev_admin_math' else 1001))
   for group in (1001,1002):cur.execute('INSERT IGNORE INTO core_group_scope(group_id,organization_id,division_id) VALUES(%s,1001,1001)',(group,))
   for group,org,div,name in [(3001,1001,1002,'DEV Резерв — группа'),(3002,2001,2001,'DEV Математика — группа')]:
    cur.execute('INSERT INTO `groups`(id,name) VALUES(%s,%s)',(group,name));cur.execute('INSERT INTO core_group_scope(group_id,organization_id,division_id) VALUES(%s,%s,%s)',(group,org,div))
   for school in (1001,1002):cur.execute('INSERT IGNORE INTO core_school_scope VALUES(%s,1001)',(school,))
   for direction in (1001,1002):cur.execute('INSERT IGNORE INTO core_direction_scope(direction_id,organization_id) VALUES(%s,1001)',(direction,))
   cur.execute("INSERT INTO core_proctor_groups(user_id,division_id,group_id) SELECT a.id,x.division_id,x.group_id FROM auth_users a JOIN auth_division_access x ON x.user_id=a.id JOIN core_group_scope g ON g.group_id=x.group_id AND g.division_id=x.division_id WHERE a.role='proctor'")
   password=json.loads((ROOT/'dev/local/accounts.json').read_text())['password']
   for role,name,edit in [(3001,'DEV Core просмотр',False),(3002,'DEV Core редактор',True)]:
    cur.execute('INSERT INTO auth_scoped_roles(id,organization_id,name) VALUES(%s,1001,%s)',(role,name))
    for section in ['users','groups','directions','schedule','attendance','zaps','upload','schools']:
     cur.execute('INSERT INTO auth_scoped_role_permissions VALUES(%s,%s,1,%s)',(role,section,edit))
   cur.execute("INSERT INTO admin_role_users(full_name,role_id) VALUES('DEV Core сотрудник',1001)");ref=cur.lastrowid
   cur.execute("INSERT INTO auth_users(username,password,role,ref_id) VALUES('dev_core_staff',%s,'staff_admin',%s)",(generate_password_hash(password,method='pbkdf2:sha256:600000'),ref));uid=cur.lastrowid
   cur.execute('INSERT INTO auth_account_state(user_id) VALUES(%s)',(uid,));cur.execute('INSERT INTO core_user_owners VALUES(%s,1001)',(uid,));cur.execute('INSERT INTO auth_organization_access VALUES(%s,1001)',(uid,))
   for div,role in [(1001,3001),(1002,3002)]:cur.execute('INSERT INTO auth_division_access(user_id,organization_id,division_id,role_id) VALUES(%s,1001,%s,%s)',(uid,div,role))
   conn.commit();marker.write_text(json.dumps({'fixtures':['dev_core_staff'],'roles':[3001,3002],'groups':[3001,3002]},indent=2)+'\n');marker.chmod(0o600)
   print('Synthetic core fixtures created: scope mappings, reserve/math groups and a delegated account.')
  else:print('Existing core fixtures retained.')
 except Exception:
  conn.rollback();raise
 finally:cur.close();conn.close()
 env_path=ROOT/'dev/.env';v=read_env(env_path)
 if 'AUTH_INTERNAL_KEY' not in v:
  with env_path.open('a') as f:f.write('AUTH_INTERNAL_KEY='+secrets.token_hex(32)+'\n')
 env_path.chmod(0o600);v=read_env(env_path)
 p=ROOT/'dev/local/core.env';env={'MYSQL_HOST':'127.0.0.1','MYSQL_PORT':'13306','MYSQL_DATABASE':'cpm_dev','MYSQL_USER':v['MYSQL_USER'],'MYSQL_PASSWORD':v['MYSQL_PASSWORD'],'CORE_LISTEN_ADDR':'127.0.0.1:8003','CORE_AUTH_URL':'http://127.0.0.1:8002','AUTH_INTERNAL_KEY':v['AUTH_INTERNAL_KEY']}
 p.write_text('\n'.join(f'{k}={value}' for k,value in env.items())+'\n');p.chmod(0o600)
 p=ROOT/'dev/local/auth.env'
 content=p.read_text() if p.exists() else ''
 if 'AUTH_INTERNAL_KEY=' not in content:
  p.write_text(content+'AUTH_INTERNAL_KEY='+v['AUTH_INTERNAL_KEY']+'\n');p.chmod(0o600)
 print('Local core environment prepared; credentials kept in ignored files.')
if __name__=='__main__':main()
