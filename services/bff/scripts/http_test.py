"""Synthetic local BFF integration; API only. Keeps isolated demo assignments for UI."""
import concurrent.futures, http.cookiejar, json, time, urllib.request, urllib.error, urllib.parse, uuid, sys
from pathlib import Path
ROOT=Path(__file__).resolve().parents[3]
pw=json.loads((ROOT/'dev/local/accounts.json').read_text())['password']
checks=[]
def expect(ok,name):
 assert ok,name
 checks.append(name)
class Client:
 def __init__(self):
  self.jar=http.cookiejar.CookieJar();self.http=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar));self.rev=''
 def request(self,path,body=None,method=None,code=200):
  headers={'X-CPM-Request':'1','X-CPM-Context':self.rev,'Origin':'http://127.0.0.1:3001','Content-Type':'application/json'}
  request=urllib.request.Request('http://127.0.0.1:8001/bff/'+path,data=json.dumps(body).encode() if body is not None else None,headers=headers,method=method or ('POST' if body is not None else 'GET'))
  try:
   with self.http.open(request,timeout=30) as res:status=res.status;data=json.load(res)
  except urllib.error.HTTPError as err:status=err.code;data=json.load(err)
  expect(status==code,f'{path.split("?")[0]} expected {code}, got {status} {data.get("error","")}')
  if 'context_revision' in data:self.rev=data['context_revision']
  expect(not any(k in data for k in ['access_token','refresh_token','token']),'no browser tokens')
  return data.get('data',data)
 def login(self,name):return self.request('auth/login',{'login':name,'password':pw})
 def context(self,org,division):return self.request('auth/context',{'organization_id':org,'division_id':division})
 def logout(self):self.request('auth/logout',{})

if __name__=='__main__':
 for name in ['dev_admin','dev_student_01','dev_proctor_1','dev_examiner_1','dev_supervisor','dev_core_staff']:
  c=Client();c.login(name);c.request('auth/me');c.request('auth/refresh',{});c.request('auth/me');c.logout();c.request('auth/me',code=401)
 c=Client();c.request('auth/login',{'login':'dev_student_01','password':'wrong'},code=401)
 admin=Client();admin.login('dev_admin');student=Client();identity=student.login('dev_student_01');proctor=Client();proctor.login('dev_proctor_1')
 run=uuid.uuid4().hex[:8]
 def create(client,name,published=True):
  types=client.request('homework/types')['items'];t=next((t for t in types if t['name']=='ОВ'),None)
  if not t:t=client.request('homework/types',{'name':'ОВ'},code=201)
  return client.request('homework/assignments',{'type_id':t['id'],'name':f'DEV BFF {run} · {name}','deadline':'2026-10-20','published':published},code=201)['id']
 active=create(admin,'Конституционное право');graded=create(admin,'Проверенная работа');hidden=create(admin,'Черновик преподавателя',False)
 got=student.request('homework/assignments')['items'];expect(active in [r['id'] for r in got] and hidden not in [r['id'] for r in got],'student sees published only')
 student.request('homework/assignments',{'name':'forbidden'},code=403)
 staff=Client();staff.login('dev_core_staff');staff.request('homework/assignments',code=403);staff.logout()
 old=student.rev;student.context(1001,1002);new=student.rev;student.rev=old;student.request('homework/assignments',code=409);student.rev=new
 student.request(f'homework/assignments/{active}',code=404)
 admin.context(1001,1002);reserve=create(admin,'Работа резервного кабинета');admin.context(1001,1001)
 student.context(2001,2001);student.request(f'homework/assignments/{active}',code=404);student.context(1001,1001)
 student.request('auth/context',{'organization_id':999999,'division_id':999999},code=403)
 student.request('homework/assignments?division_id=2001',code=400)
 student.request('internal/auth/password-hash',{},code=501)
 # Real PDF goes directly to local S3, then through existing worker/RabbitMQ.
 sys.path.insert(0,str(ROOT/'services/homework/scripts'))
 from workflow_test import pdf
 content=pdf(label='DEV BFF integration homework')
 (ROOT/'dev/local/bff-demo.pdf').write_bytes(content)
 init=student.request(f'homework/assignments/{graded}/uploads',{'client_upload_id':str(uuid.uuid4())},code=201)
 upload=init['upload'];expect(urllib.parse.urlparse(upload['url']).netloc=='127.0.0.1:19000','local S3 only')
 boundary='bff'+uuid.uuid4().hex
 parts=[]
 for key,value in upload['fields'].items():parts.append(f'--{boundary}\r\nContent-Disposition: form-data; name="{key}"\r\n\r\n{value}\r\n'.encode())
 parts.append(f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="homework.pdf"\r\nContent-Type: application/pdf\r\n\r\n'.encode()+content+b'\r\n');parts.append(f'--{boundary}--\r\n'.encode())
 with urllib.request.urlopen(urllib.request.Request(upload['url'],data=b''.join(parts),headers={'Content-Type':f'multipart/form-data; boundary={boundary}'}),timeout=30) as r:expect(r.status in [200,201,204],'S3 upload')
 job=student.request(f"homework/uploads/{init['job']['id']}/complete",{},code=202)
 for _ in range(50):
  job=student.request(f"homework/jobs/{init['job']['id']}")
  if job['status'] in ['ready','failed']:break
  time.sleep(1)
 expect(job['status']=='ready','real PDF processing complete')
 student.request(f'homework/assignments/{graded}/submit',{})
 queue=proctor.request('homework/review-queue')['items'];submission=next(r['id'] for r in queue if r['assignment_id']==graded)
 proctor.request(f'homework/submissions/{submission}/claim',{})
 proctor.request(f'homework/submissions/{submission}/request-revision',{'message':'DEV: добавьте обоснование'})
 expect(student.request(f'homework/assignments/{graded}/workspace')['submission']['state']=='revision_requested','revision visible to student')
 # Returned existing file is retained; submit without a new processed draft is refused.
 student.request(f'homework/assignments/{graded}/submit',{},code=409)
 # Separate manual result to exercise journal without bypassing PDF review.
 proctor.request(f'homework/assignments/{active}/manual',{'account_id':identity['user']['account_id'],'date_pass':'2026-10-08','result':87})
 expect(student.request(f'homework/assignments/{active}/workspace')['legacy_result']['result']==87,'manual grade visible')
 supervisor=Client();supervisor.login('dev_supervisor');report=supervisor.request('homework/report?date_from=2026-10-01&date_to=2026-10-31');expect(any(h['id']==active for h in report['assignments']),'supervisor report');supervisor.logout()
 demo={'run':run,'active':active,'file_homework':graded,'hidden':hidden,'reserve':reserve,'submission':submission,'student_account_id':identity['user']['account_id']}
 (ROOT/'dev/local/bff-demo.json').write_text(json.dumps(demo,ensure_ascii=False,indent=2)+'\n')
 for client in [student,proctor,admin]:client.logout()
 (ROOT/'dev/local/bff-http-report.json').write_text(json.dumps({'status':'passed','checks':checks,'demo':demo},ensure_ascii=False,indent=2)+'\n')
 print(json.dumps({'status':'passed','checks':len(checks),'demo':demo}))
