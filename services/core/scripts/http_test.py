"""Exercise real Docker HTTP endpoints against fixed local dev with synthetic data only."""
from concurrent.futures import ThreadPoolExecutor
import base64
import io
import json
import secrets
import sys
import urllib.error
import urllib.request
import zipfile
from pathlib import Path
from xml.sax.saxutils import escape
ROOT=Path(__file__).resolve().parents[3]
sys.path.insert(0,str(ROOT/'dev/scripts'))
from bootstrap import settings,mysql_connection
from export_structure import read_env
AUTH='http://127.0.0.1:8002';CORE='http://127.0.0.1:8003';BASE='/v1/core'
checks=[];users=[];groups=[];divisions=[];schools=[];roles=[];directions=[];lessons=[];days=[];zaps=[];imports=[];sessions=[]
run=secrets.token_hex(5);password=json.loads((ROOT/'dev/local/accounts.json').read_text())['password']

def expect(condition,name):
 if not condition:raise AssertionError(name)
 checks.append(name)

def request(path,token=None,method='GET',body=None,code=200,headers=None,origin=CORE):
 h=dict(headers or {})
 if token:h['Authorization']='Bearer '+token
 if body is not None:
  if isinstance(body,bytes):data=body
  else:data=json.dumps(body).encode();h['Content-Type']='application/json'
 else:data=None
 req=urllib.request.Request(origin+path,data=data,method=method,headers=h)
 try:
  with urllib.request.urlopen(req,timeout=100) as res:status=res.status;raw=res.read()
 except urllib.error.HTTPError as e:status=e.code;raw=e.read()
 expect(status==code,f'{method} {path.split("?")[0]} -> {code} (got {status})')
 try:return json.loads(raw)
 except ValueError:return {}

def api(path,token,method='GET',body=None,code=200,headers=None):
 result=request(BASE+path,token,method,body,code,headers)
 return result.get('data') if code<300 else result

def login(name,division=None,org=1001,pw=None):
 body={'login':name,'password':pw or password,'organization_id':org}
 if division:body['division_id']=division
 result=request('/v1/auth/login',method='POST',body=body,origin=AUTH)
 sessions.append(result['token']);return result['token'],result['user']

def create(role='student',division=1001,group=None):
 body={'role':role,'full_name':'DEV Core HTTP '+role+' '+run,'login':'dev_core_http_'+run+'_'+str(len(users)),'password':password,'tg_name':'dev_test_tg'}
 if role=='student':body['class']=10;body['school_id']=1001
 if group:body['group_id']=group
 result=api(f'/divisions/{division}/users',admin,'POST',body,201);uid=result['account_id'];users.append(uid);return uid,body

def multipart(filename,content):
 boundary='cpm'+run;raw=(f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="{filename}"\r\nContent-Type: application/octet-stream\r\n\r\n').encode()+content+f'\r\n--{boundary}--\r\n'.encode();return raw,{'Content-Type':'multipart/form-data; boundary='+boundary}

def xlsx(rows):
 stream=io.BytesIO()
 with zipfile.ZipFile(stream,'w') as z:
  z.writestr('[Content_Types].xml','<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>')
  z.writestr('_rels/.rels','<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>')
  z.writestr('xl/workbook.xml','<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Users" sheetId="1" r:id="rId1"/></sheets></workbook>')
  z.writestr('xl/_rels/workbook.xml.rels','<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>')
  xml='<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>'
  for ri,row in enumerate(rows,1):
   xml+=f'<row r="{ri}">'
   for ci,value in enumerate(row):xml+=f'<c r="{chr(65+ci)}{ri}" t="inlineStr"><is><t>{escape(str(value))}</t></is></c>'
   xml+='</row>'
  z.writestr('xl/worksheets/sheet1.xml',xml+'</sheetData></worksheet>')
 return stream.getvalue()

def cleanup():
 conn=mysql_connection(settings());c=conn.cursor()
 try:
  for z in zaps:
   c.execute('DELETE FROM core_attendance WHERE zap_id=%s',(z,));c.execute('DELETE FROM core_zap_dates WHERE zap_id=%s',(z,));c.execute('DELETE FROM core_zap_images WHERE zap_id=%s',(z,));c.execute('DELETE FROM core_zaps WHERE id=%s',(z,))
  for day in days:c.execute('DELETE FROM core_attendance WHERE class_day_id=%s',(day,));c.execute('DELETE FROM core_class_days WHERE id=%s',(day,))
  for lesson in lessons:c.execute('DELETE FROM core_lessons WHERE id=%s',(lesson,))
  for n in imports:c.execute('DELETE FROM core_imports WHERE id=%s',(n,))
  for u in users:
   c.execute('SELECT role,ref_id FROM auth_users WHERE id=%s AND username LIKE %s',(u,'dev_core_http_'+run+'%'));row=c.fetchone()
   if not row:raise AssertionError('cleanup fixture guard')
   c.execute('DELETE t FROM auth_refresh_tokens t JOIN auth_sessions s ON s.id=t.session_id WHERE s.user_id=%s',(u,));c.execute('DELETE FROM auth_sessions WHERE user_id=%s',(u,))
   for table in ['core_profile_extra','core_rating_locks','core_proctor_groups','auth_division_access','auth_organization_access','core_user_owners','auth_account_state']:c.execute('DELETE FROM '+table+' WHERE user_id=%s',(u,))
   c.execute('DELETE FROM auth_users WHERE id=%s',(u,));table={'student':'students','proctor':'proctors','supervisor':'supervisors','examinator':'examinators','staff_admin':'admin_role_users'}[row[0]];c.execute('DELETE FROM '+table+' WHERE id=%s',(row[1],))
  for g in groups:c.execute('DELETE FROM core_group_scope WHERE group_id=%s',(g,));c.execute('DELETE FROM `groups` WHERE id=%s',(g,))
  for n in directions:c.execute('DELETE FROM core_direction_scope WHERE direction_id=%s',(n,));c.execute('DELETE FROM directions WHERE id=%s',(n,))
  for n in schools:c.execute('DELETE FROM core_school_scope WHERE school_id=%s',(n,));c.execute('DELETE FROM schools WHERE id=%s',(n,))
  for n in roles:c.execute('DELETE FROM auth_scoped_role_permissions WHERE role_id=%s',(n,));c.execute('DELETE FROM auth_scoped_roles WHERE id=%s',(n,))
  for n in divisions:c.execute('DELETE FROM divisions WHERE id=%s AND organization_id=1001',(n,))
  c.execute("UPDATE auth_scoped_role_permissions SET can_view=1,can_edit=1 WHERE role_id=3002 AND section='groups'")
  conn.commit()
 finally:c.close();conn.close()
 for token in sessions:
  req=urllib.request.Request(AUTH+'/v1/auth/logout',method='POST',headers={'Authorization':'Bearer '+token})
  try:urllib.request.urlopen(req,timeout=5).close()
  except urllib.error.HTTPError:pass

def main():
 global admin
 request('/health/ready');request('/health/ready',origin=AUTH)
 admin,admin_user=login('dev_admin',1001);math,math_user=login('dev_admin_math',2001,2001)
 student,su=login('dev_student_01',1001);other,ou=login('dev_student_02',1001)
 proctor,pu=login('dev_proctor_1',1001);proctor2,p2u=login('dev_proctor_2',1001)
 viewer,vu=login('dev_core_staff',1001);editor,eu=login('dev_core_staff',1002)
 examiner,_=login('dev_examiner_1',1001);supervisor,_=login('dev_supervisor',1001);oldstaff,_=login('dev_staff',1001)
 doc=request('/openapi.json');expect(sum(len(ops) for ops in doc['paths'].values())>=60,'OpenAPI describes all business routes')
 for path,ops in doc['paths'].items():
  concrete=path
  for key in ['division','user','role','school','group','direction','lesson','day','zap','date','image','import']:concrete=concrete.replace('{'+key+'}','1001')
  for method in ops:request(concrete,method=method.upper(),body={} if method in ['post','put'] else None,code=401)
 request(BASE+'/organization',headers={'X-Role':'admin','X-Account-ID':str(admin_user['account_id'])},code=401)
 api('/organization',student);expect(api('/me',student)['account_id']==su['account_id'],'own profile uses unique account id')
 roster=api('/divisions/1001/users',student);expect(len(roster)==1 and roster[0]['account_id']==su['account_id'],'student sees own profile only')
 roster=api('/divisions/1001/users',proctor);expect(all(r['account_id']==pu['account_id'] or r['role']=='student' and r['group_id']==1001 for r in roster),'proctor sees only own group')
 api('/divisions/1001/groups/1002/members',proctor,code=403);api('/divisions/1001/groups/1001/members',student,code=403)
 api('/divisions/1001/users/'+str(ou['account_id']),student,code=403)
 api('/divisions/1002/users',viewer,code=403);api('/divisions/2001/groups',admin,code=404);api('/divisions/1001/groups',math,code=404)
 api('/roles',viewer,code=403);api('/roles',student,code=403)
 api('/divisions/1001/groups',oldstaff,'POST',{'name':'DEV forbidden'},403)
 api('/divisions/1001/groups',student,'POST',{'name':'DEV forbidden'},403,{'X-Role':'admin','X-Organization-ID':'1001'})
 api('/divisions/1001/groups',viewer,'POST',{'name':'DEV readonly'},403)
 g=api('/divisions/1001/groups',admin,'POST',{'name':'DEV HTTP '+run},201)['id'];groups.append(g)
 rg=api('/divisions/1002/groups',editor,'POST',{'name':'DEV Reserve HTTP '+run},201)['id'];groups.append(rg)
 api(f'/divisions/1001/groups/{rg}',admin,'PUT',{'name':'DEV wrong scope'},404)
 api(f'/divisions/1001/groups/{g}',admin,'PUT',{'name':'DEV Renamed '+run})
 api('/divisions/1001/groups',admin,'POST',{'name':'DEV','organization_id':2001},400)
 api('/divisions/1001/groups',admin,'POST',{'name':'x'*51},400)
 api('/divisions/1001/users',admin,'POST',{'role':'admin','full_name':'DEV X','login':'dev_bad','password':password},400)
 api('/divisions/1002/users',editor,'POST',{'role':'staff_admin','full_name':'DEV X','login':'dev_bad','password':password,'role_id':3002},403)
 api('/divisions/1001/groups?limit=0',admin,code=400)
 div=api('/divisions',admin,'POST',{'name':'DEV HTTP Division '+run},201)['id'];divisions.append(div)
 api(f'/divisions/{div}',admin,'PUT',{'name':'DEV HTTP Division renamed '+run})
 api('/divisions',student,'POST',{'name':'DEV X'},403)
 school=api('/schools',admin,'POST',{'name':'DEV HTTP School '+run,'short_name':'DEV S','notes':'DEV internal','is_active':True},201)['id'];schools.append(school)
 api(f'/schools/{school}',math,code=404);expect('notes' not in api(f'/schools/{school}',student),'student does not see administrator notes')
 api('/schools',editor,'POST',{'name':'DEV School','is_active':True},403)
 direction=api('/divisions/1001/directions',admin,'POST',{'name':'DEV HTTP Direction '+run},201)['id'];directions.append(direction)
 api(f'/divisions/1001/directions/{direction}',admin,'PUT',{'name':'DEV Renamed direction'})
 api(f'/divisions/1002/directions/{direction}',editor,'DELETE',code=403)
 expect(any(item['id']==direction for item in api('/divisions/1002/directions',editor)),'direction is shared across organization divisions')
 expect(any(item['id']==direction for item in api('/directions',student)),'organization direction list is available to participant')
 role=api('/roles',admin,'POST',{'name':'DEV HTTP Role '+run,'permissions':{'users':{'view':True,'edit':False}}},201)['id'];roles.append(role)
 api('/roles',admin,'POST',{'name':'DEV Bad role','permissions':{'users':{'view':False,'edit':True}}},400)
 api('/roles',admin,'POST',{'name':'DEV TG','permissions':{'telegram-bot':{'view':True,'edit':True}}},400)
 api('/roles/2001',admin,'PUT',{'name':'DEV Evil','permissions':{}},404)
 u,ub=create(group=g);newstudent,newuser=login(ub['login'],1001)
 expect(api('/me',newstudent)['tg_name']=='dev_test_tg','tg remains a student profile attribute')
 api(f'/divisions/1001/groups/{g}/members',admin,'POST',{'account_id':u})
 api('/divisions/1001/groups/3002/members',admin,'POST',{'account_id':u},404)
 api(f'/divisions/1002/groups/{rg}/members',admin,'POST',{'account_id':u},404)
 api(f'/divisions/1002/memberships/{u}',admin,'PUT',{})
 api(f'/divisions/{div}/memberships/{u}',admin,'PUT',{})
 api(f'/divisions/{div}',admin,'DELETE',code=409)
 api(f'/divisions/{div}/memberships/{u}',admin,'DELETE')
 api(f'/divisions/{div}',admin,'DELETE')
 api(f'/divisions/1002/groups/{rg}/members',admin,'POST',{'account_id':u})
 puid,pbody=create('proctor',group=g);pnew,pn=login(pbody['login'],1001)
 expect(api('/me',pnew)['tg_name']=='dev_test_tg','tg remains a staff profile attribute')
 api(f'/divisions/1001/groups/{g}/members',admin,'POST',{'account_id':pu['account_id']},409)
 api(f'/divisions/1001/groups/{g}/members',pnew,'POST',{'account_id':u},403)
 for role_name in ['examinator','supervisor']:create(role_name)
 staff_body={'role':'staff_admin','full_name':'DEV HTTP staff '+run,'login':'dev_core_http_'+run+'_staff','password':password,'role_id':role,'tg_name':'dev_staff_tg'}
 staff=api('/divisions/1001/users',admin,'POST',staff_body,201)['account_id'];users.append(staff)
 api('/roles/'+str(role),admin,'DELETE',code=409)
 api('/divisions/1002/memberships/'+str(staff),editor,'PUT',{'role_id':3002},403)
 api('/divisions/1002/memberships/'+str(staff),admin,'PUT',{'role_id':2001},404)
 api('/divisions/1002/memberships/'+str(staff),admin,'PUT',{'role_id':3002})
 api('/roles/'+str(role),admin,'PUT',{'name':'DEV HTTP Role updated '+run,'permissions':{'users':{'view':True,'edit':True}}})
 api(f'/divisions/1001/users/{u}',viewer,'PUT',{'full_name':'DEV Evil','class':10,'tg_name':'dev'},403)
 api(f'/divisions/1002/users/{u}',editor,'PUT',{'full_name':'DEV Evil','class':10,'tg_name':'dev'},403)
 api(f'/divisions/1001/users/{u}',admin,'PUT',{'full_name':'DEV Updated HTTP '+run,'class':11,'tg_name':'dev_new_tg','school_id':school})
 api(f'/divisions/1001/users/{u}/credentials',viewer,'PUT',{'login':ub['login'],'password':password},403)
 api(f'/divisions/1001/users/{u}/credentials',admin,'PUT',{'login':ub['login'],'password':password})
 api('/me',newstudent,code=401);newstudent,_=login(ub['login'],1001)
 api(f'/divisions/1001/users/{u}/active',admin,'PUT',{},400)
 api(f'/divisions/1001/users/{u}/active',admin,'PUT',{'is_active':False});api('/me',newstudent,code=401)
 request('/v1/auth/login',method='POST',body={'login':ub['login'],'password':password},code=401,origin=AUTH)
 api(f'/divisions/1001/users/{u}/active',admin,'PUT',{'is_active':True});newstudent,_=login(ub['login'],1001)
 lesson={'date':'2026-10-06','start_time':'10:00','end_time':'11:00','lesson_name':'DEV HTTP lesson','teacher_name':'DEV Teacher','location':'DEV Site','classroom':'301А','color':'#5B8DEF','is_changed':False,'is_in_person':True,'is_for_all':True,'is_public':True}
 lid=api('/divisions/1001/schedule',admin,'POST',lesson,201)['id'];lessons.append(lid)
 api('/divisions/1001/schedule',admin,'POST',lesson,409)
 api('/divisions/1001/schedule',admin,'POST',{**lesson,'start_time':'12:00','end_time':'11:00'},400)
 api('/divisions/1001/schedule/bulk',admin,'POST',{'lessons':[{**lesson,'date':'2026-10-09'},{**lesson,'date':'2026-10-09'}]},409)
 conn=mysql_connection(settings());c=conn.cursor();c.execute("SELECT COUNT(*) FROM core_lessons WHERE date='2026-10-09'");expect(c.fetchone()[0]==0,'conflicting bulk schedule rolls back fully');c.close();conn.close()
 school_lesson={**lesson,'is_public':False,'school_id':school}
 slid=api('/divisions/1001/schedule',admin,'POST',school_lesson,201)['id'];lessons.append(slid)
 srows=api('/divisions/1001/schedule?date_from=2026-10-01&date_to=2026-10-31',student);expect(slid not in [x['id'] for x in srows],'student cannot see another school schedule')
 srows=api('/divisions/1001/schedule?date_from=2026-10-01&date_to=2026-10-31',newstudent);expect(slid in [x['id'] for x in srows],'student sees own school schedule')
 api('/divisions/1001/schedule?date_from=bad&date_to=2026-10-31',admin,code=400)
 api(f'/divisions/1002/schedule/{lid}',editor,'DELETE',code=404)
 api('/divisions/1001/schedule?date_from=2026-10-01&date_to=2026-10-31',examiner)
 day=api('/divisions/1001/class-days',admin,'POST',{'date':'2026-10-06','comment':'DEV HTTP'},201)['id'];days.append(day)
 api('/divisions/1001/class-days',admin,'POST',{'date':'2026-10-06'},409)
 api(f'/divisions/1001/class-days/{day}/attendance',pnew,'POST',{'account_id':u,'attendance_type_id':1})
 api(f'/divisions/1001/class-days/{day}/attendance',proctor2,'POST',{'account_id':u,'attendance_type_id':1},403)
 api(f'/divisions/1001/class-days/{day}/attendance',newstudent,'POST',{'account_id':u,'attendance_type_id':1},403)
 entries=api('/divisions/1001/attendance-report?date_from=2026-10-01&date_to=2026-10-31',newstudent)['entries'];expect(all(x['account_id']==u for x in entries),'student attendance limited to self')
 api('/divisions/1001/attendance-report?date_from=2026-10-01&date_to=2026-10-31',supervisor)
 api('/divisions/1001/attendance-report?date_from=2026-10-01&date_to=2026-10-31&account_id='+str(u),other,code=403)
 api(f'/divisions/1001/class-days/{day}',admin,'DELETE',code=409)
 api(f'/divisions/1001/class-days/{day}',admin,'PUT',{'date':'2026-10-07'},409)
 api(f'/divisions/1001/class-days/{day}/attendance/{u}',pnew,'DELETE')
 png=base64.b64encode(b'\x89PNG\r\n\x1a\n'+b'\0'*24).decode()
 zap=api('/divisions/1001/zaps',newstudent,'POST',{'account_id':u,'text':'DEV sick','dates':['2026-10-06','2026-10-07'],'images':[{'mime':'image/png','data':png}]},201)['id'];zaps.append(zap)
 api('/divisions/1001/zaps',newstudent,'POST',{'account_id':ou['account_id'],'text':'DEV Evil','dates':['2026-10-06']},403)
 api(f'/divisions/1001/zaps/{zap}',other,code=403);api(f'/divisions/1002/zaps/{zap}',editor,code=404)
 api(f'/divisions/1001/zaps/{zap}/process',newstudent,'POST',{'status':'apr'},403)
 api(f'/divisions/1001/zaps/{zap}/process',admin,'POST',{'status':'apr','answer':'DEV approved'})
 detail=api(f'/divisions/1001/zaps/{zap}',newstudent);expect({x['status'] for x in detail['dates']}=={'linked','no_class_day'},'approved zap links existing day and retains future date')
 image=detail['images'][0]['id'];expect(api(f'/divisions/1001/zaps/{zap}/images/{image}',newstudent)['data']==png,'authorized image returned')
 api(f'/divisions/1001/zaps/{zap}/images/{image}',other,code=403)
 api(f'/divisions/1001/class-days/{day}/attendance',admin,'POST',{'account_id':u,'attendance_type_id':1},409)
 api('/divisions/1002/memberships/'+str(u),admin,'DELETE')
 api('/divisions/1001/memberships/'+str(u),admin,'DELETE',code=409)
 api('/divisions/1002/memberships/'+str(u),admin,'PUT',{})
 api('/divisions/1001/memberships/'+str(u),admin,'DELETE',code=409)
 day2=api('/divisions/1001/class-days',admin,'POST',{'date':'2026-10-07'},201)['id'];days.append(day2)
 future=next(x['id'] for x in detail['dates'] if x['status']=='no_class_day')
 api(f'/divisions/1001/zaps/{zap}/dates/{future}/retry',admin,'POST')
 api(f'/divisions/1001/zaps/{zap}/dates/{future}',admin,'DELETE')
 api(f'/divisions/1001/zaps/{zap}/dates/{future}/retry',admin,'POST')
 request('/internal/core/rating-locks',method='POST',body={'account_id':u,'division_id':1002},code=401)
 secret=read_env(ROOT/'dev/.env')['AUTH_INTERNAL_KEY']
 request('/internal/core/rating-locks',method='POST',body={'account_id':u,'division_id':1003},headers={'X-Service-Key':secret},code=400)
 request('/internal/core/rating-locks',method='POST',body={'account_id':u,'division_id':1002},headers={'X-Service-Key':secret})
 api('/divisions/1002/memberships/'+str(u),admin,'DELETE',code=409)
 api(f'/divisions/1001/zaps/{zap}/process',admin,'POST',{'status':'dec','answer':'DEV rejected'})
 expect(not api(f'/divisions/1001/class-days/{day}/attendance',newstudent),'rejected zap removes its generated attendance')
 api(f'/divisions/1001/zaps/{zap}/dates/{future}/retry',admin,'POST',code=409)
 for ext in ['json','csv','xlsx']:
  name='dev_core_http_'+run+'_import_'+ext;rows=[['full_name','login','password','class'],['DEV Import '+ext,name,password,'10']]
  if ext=='json':body={'users':[{'role':'student','full_name':rows[1][0],'login':name,'password':password,'class':10}]};headers=None
  else:body,headers=multipart('users.'+ext,(','.join(rows[0])+'\n'+','.join(rows[1])+'\n').encode() if ext=='csv' else xlsx(rows))
  imp=api('/divisions/1001/imports',admin,'POST',body,201,headers)['id'];imports.append(imp)
  preview=api('/divisions/1001/imports/'+str(imp),admin);expect(password not in json.dumps(preview) and 'pbkdf2' not in json.dumps(preview),'import preview excludes secrets '+ext)
  api('/divisions/1002/imports/'+str(imp),editor,code=404)
  result=api('/divisions/1001/imports/'+str(imp)+'/commit',admin,'POST');users.extend(result['created'])
  expect(api('/divisions/1001/imports/'+str(imp)+'/commit',admin,'POST')==result,'import commit idempotent '+ext)
  login(name,1001)
 api('/divisions/1001/imports',viewer,'POST',{'users':[ub]},403)
 api('/divisions/1001/imports',admin,'POST',{'users':[{**ub,'login':'dev_core_http_'+run+'_bad','group_id':3002}]},400)
 bad_rows=[['full_name','login','password','class','division_id'],['DEV Bad','dev_wrong',password,'10','2001']];body,headers=multipart('users.csv',(','.join(bad_rows[0])+'\n'+','.join(bad_rows[1])+'\n').encode());api('/divisions/1001/imports',admin,'POST',body,400,headers)
 conn=mysql_connection(settings());c=conn.cursor();c.execute("UPDATE auth_scoped_role_permissions SET can_edit=0 WHERE role_id=3002 AND section='groups'");conn.commit();c.close();conn.close()
 api('/divisions/1002/groups',editor,'POST',{'name':'DEV revoked'},403)
 conn=mysql_connection(settings());c=conn.cursor();c.execute("UPDATE auth_scoped_role_permissions SET can_edit=1 WHERE role_id=3002 AND section='groups'");conn.commit();c.close();conn.close()

 # Every mutation checks the target object, including positive delete/archive operations.
 empty_group=api('/divisions/1001/groups',admin,'POST',{'name':'DEV Empty '+run},201)['id'];groups.append(empty_group)
 api(f'/divisions/1001/groups/{g}/members/{u}',admin,'DELETE')
 api(f'/divisions/1001/groups/{g}/members/{u}',admin,'DELETE',code=404)
 api(f'/divisions/1001/groups/{g}/members',admin,'POST',{'account_id':u})
 api(f'/divisions/1001/groups/{empty_group}',admin,'DELETE')
 api(f'/divisions/1001/groups/{empty_group}/members',admin,code=404)
 api(f'/divisions/1001/directions/{direction}',admin,'DELETE')
 api(f'/divisions/1001/directions/{direction}',admin,'PUT',{'name':'DEV cannot revive'},404)
 api(f'/schools/{school}',admin,'PUT',{'name':'DEV HTTP School '+run,'short_name':'DEV S','notes':'DEV internal','is_active':False})
 api(f'/schools/{school}',student,code=404)
 api(f'/schools/{school}',admin,'PUT',{'name':'DEV HTTP School '+run,'is_active':True})
 extra_lessons=api('/divisions/1001/schedule/bulk',admin,'POST',{'lessons':[{**lesson,'date':'2026-10-10'},{**lesson,'date':'2026-10-11'}]},201);lessons.extend(extra_lessons)
 api(f'/divisions/1001/schedule/{extra_lessons[0]}',admin,'PUT',{**lesson,'date':'2026-10-10','lesson_name':'DEV updated'})
 api(f'/divisions/1001/schedule/{extra_lessons[1]}',admin,'DELETE')
 empty_day=api('/divisions/1001/class-days',admin,'POST',{'date':'2026-10-12'},201)['id'];days.append(empty_day)
 api(f'/divisions/1001/class-days/{empty_day}',admin)
 api(f'/divisions/1001/class-days/{empty_day}',admin,'PUT',{'date':'2026-10-13','comment':'DEV renamed'})
 api(f'/divisions/1001/class-days/{empty_day}/attendance',admin,'POST',{'account_id':u,'attendance_type_id':99},400)
 api(f'/divisions/1001/class-days/{empty_day}',admin,'DELETE')
 unused_role=api('/roles',admin,'POST',{'name':'DEV Unused '+run,'permissions':{}},201)['id'];roles.append(unused_role)
 api('/roles/'+str(unused_role),admin,'DELETE')
 api('/divisions/1002/users',editor,'POST',{'role':'supervisor','full_name':'DEV Privilege','login':'dev_core_http_'+run+'_escalation','password':password},403)
 api('/divisions/1001/users/'+str(u)+'/active',student,'PUT',{'is_active':False},403)
 api('/divisions/1001/users/'+str(u)+'/credentials',editor,'PUT',{'login':ub['login'],'password':password},403)
 request('/v1/auth/logout',pnew,'POST',origin=AUTH);api('/divisions/1001/groups/'+str(g)+'/members',pnew,code=401)


 # The rating marker and membership removal serialize on the same account row.
 race_user,race_body=create()
 api('/divisions/1002/memberships/'+str(race_user),admin,'PUT',{})
 def race_call(path,origin,headers,body=None,method='POST'):
  data=json.dumps(body).encode() if body is not None else None
  if data is not None:headers={**headers,'Content-Type':'application/json'}
  req=urllib.request.Request(origin+path,data=data,method=method,headers=headers)
  try:
   with urllib.request.urlopen(req,timeout=20) as res:return res.status
  except urllib.error.HTTPError as e:return e.code
 with ThreadPoolExecutor(max_workers=2) as pool:
  locked=pool.submit(race_call,'/internal/core/rating-locks',CORE,{'X-Service-Key':secret},{'account_id':race_user,'division_id':1002})
  removed=pool.submit(race_call,BASE+'/divisions/1002/memberships/'+str(race_user),CORE,{'Authorization':'Bearer '+admin},None,'DELETE')
  codes=(locked.result(),removed.result())
 expect(codes in [(200,409),(400,200)],'concurrent rating lock and membership removal cannot both succeed')

 api('/audit',viewer,code=403);api('/audit',admin)
 request('/v1/auth/logout',editor,'POST',origin=AUTH);api('/me',editor,code=401)
 request('/internal/auth/password-hash',method='POST',body={'password':password},code=401,origin=AUTH)
 api('/telegram-bot/status',admin,code=404)
 expect('telegram' not in json.dumps(doc).lower(),'no Telegram operations in core')
 report={'status':'passed','checks':len(checks),'scenarios':checks,'target':CORE,'production_accessed':False}
 p=ROOT/'dev/local/core-http-report.json';p.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n');p.chmod(0o600)
 print(json.dumps({'status':'passed','http_checks':len(checks),'synthetic_records_cleaned':True}))

if __name__=='__main__':
 try:main()
 finally:cleanup()
