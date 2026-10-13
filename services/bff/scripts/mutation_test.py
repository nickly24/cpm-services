"""Local BFF mutation contracts using isolated API-created homework; no direct DB writes."""
import json, uuid
from http_test import Client, ROOT, checks, expect
admin=Client();admin.login('dev_admin')
proctor=Client();identity=proctor.login('dev_proctor_1')
student=Client();learner=student.login('dev_student_01')
t=admin.request('homework/types',{'name':'DEV BFF mutations '+uuid.uuid4().hex[:8]},code=201)
assignment=admin.request('homework/assignments',{'name':'DEV BFF mutation fixture','type_id':t['id'],'published':True,'deadline':'2026-10-20'},code=201)
key=assignment['id'];aid=learner['user']['account_id']
try:
 admin.request(f'homework/assignments/{key}',{'name':'DEV BFF updated fixture','type_id':t['id'],'published':True,'deadline':'2026-10-21'},method='PUT')
 expect(student.request(f'homework/assignments/{key}')['name']=='DEV BFF updated fixture','updated assignment visible')
 proctor.request(f'homework/assignments/{key}/manual',{'account_id':aid,'date_pass':'2026-10-08','result':0})
 expect(student.request(f'homework/assignments/{key}/workspace')['legacy_result']['result']==0,'zero grade persisted')
 proctor.request(f'homework/assignments/{key}/manual',{'account_id':aid,'status':0})
 expect(student.request(f'homework/assignments/{key}/workspace')['legacy_result']['status']==0,'manual grade cleared')
 bulk=proctor.request(f'homework/assignments/{key}/manual/bulk',{'group_id':identity['user']['group_id'],'date_pass':'2026-10-08','result':78})
 expect(bulk['passed']>0 and not bulk['errors'],'bulk marks group')
 expect(student.request(f'homework/assignments/{key}/workspace')['legacy_result']['result']==78,'bulk grade visible')
 # Clear manual result before a fresh file upload; cancel the untransferred draft.
 proctor.request(f'homework/assignments/{key}/manual',{'account_id':aid,'status':0})
 upload=student.request(f'homework/assignments/{key}/uploads',{'client_upload_id':str(uuid.uuid4())},code=201)
 student.request(f"homework/jobs/{upload['job']['id']}/cancel",{})
 expect(student.request(f"homework/jobs/{upload['job']['id']}")['status']=='cancelled','upload cancellation persisted')
finally:
 admin.request(f'homework/assignments/{key}',method='DELETE')
 admin.request(f"homework/types/{t['id']}",method='DELETE')
 for c in [admin,student,proctor]:c.logout()
(ROOT/'dev/local/bff-mutations-report.json').write_text(json.dumps({'status':'passed','checks':checks},ensure_ascii=False,indent=2))
print(json.dumps({'status':'passed','checks':len(checks)}))
