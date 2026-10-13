"""Permissions/revocation through BFF with isolated accounts created via core API."""
from http_test import Client, ROOT, pw, expect, checks
import json, urllib.request, urllib.error, uuid

def owner(port,path,token=None,body=None,method=None,code=200):
 h={'Content-Type':'application/json'}
 if token:h['Authorization']='Bearer '+token
 req=urllib.request.Request(f'http://127.0.0.1:{port}'+path,data=json.dumps(body).encode() if body is not None else None,headers=h,method=method or ('POST' if body is not None else 'GET'))
 try:
  with urllib.request.urlopen(req,timeout=20) as r:status=r.status;data=json.load(r)
 except urllib.error.HTTPError as e:status=e.code;data=json.load(e)
 expect(status==code,f'owner {path}: {status} expected {code}')
 return data.get('data',data)
run=uuid.uuid4().hex[:8]
a=owner(8002,'/v1/auth/login',body={'login':'dev_admin','password':pw});token=a['access_token']
sections=['assignments','review-queue','homework-archive','monitoring']
roles=[]
try:
 for edit in [False,True]:
  roles.append(owner(8003,'/v1/core/roles',token,{'name':f'DEV BFF {run} {edit}','permissions':{s:{'view':True,'edit':edit} for s in sections}},code=201)['id'])
 login='dev_bff_staff_'+run
 uid=owner(8003,'/v1/core/divisions/1001/users',token,{'role':'staff_admin','full_name':'DEV BFF сотрудник '+run,'login':login,'password':pw,'role_id':roles[0]},code=201)['account_id']
 owner(8003,f'/v1/core/divisions/1002/memberships/{uid}',token,{'role_id':roles[1]},'PUT')
 c=Client();c.login(login);c.request('homework/assignments');c.request('homework/types',{'name':'must-not-create'},code=403)
 changed=c.context(1001,1002);expect(changed['user']['permissions']['assignments']['edit'],'editor permission after switch')
 type_id=c.request('homework/types',{'name':'DEV BFF role check '+run},code=201)['id'];c.request(f'homework/types/{type_id}',method='DELETE')
 c.context(1001,1001);c.request('homework/settings',{},'PUT',code=403);c.request('auth/context',{'organization_id':2001,'division_id':2001},code=403)
 # Updating just our new role removes access immediately through live introspection.
 owner(8003,f'/v1/core/roles/{roles[0]}',token,{'name':f'DEV BFF {run} False','permissions':{}},'PUT')
 c.request('homework/assignments',code=403)
 owner(8003,f'/v1/core/roles/{roles[0]}',token,{'name':f'DEV BFF {run} False','permissions':{s:{'view':True,'edit':False} for s in sections}},'PUT')
 c.request('homework/assignments');c.logout()
 blocked_login='dev_bff_disabled_'+run
 student=owner(8003,'/v1/core/divisions/1001/users',token,{'role':'student','class':10,'full_name':'DEV BFF отзыв '+run,'login':blocked_login,'password':pw},code=201)['account_id']
 blocked=Client();blocked.login(blocked_login);owner(8003,f'/v1/core/divisions/1001/users/{student}/active',token,{'is_active':False},'PUT');blocked.request('auth/me',code=401);blocked.request('homework/assignments',code=401)
 fixture={'staff_login':login,'staff_account_id':uid,'roles':roles,'disabled_account_id':student}
 (ROOT/'dev/local/bff-access-fixtures.json').write_text(json.dumps(fixture,ensure_ascii=False,indent=2)+'\n')
 (ROOT/'dev/local/bff-access-report.json').write_text(json.dumps({'status':'passed','checks':checks},ensure_ascii=False,indent=2)+'\n')
 print(json.dumps({'status':'passed','checks':len(checks),'staff_login':login}))
finally:owner(8002,'/v1/auth/logout',token,{})
