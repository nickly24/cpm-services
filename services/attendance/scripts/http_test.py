"""Actual auth/core/attendance HTTP; no fake guard and no direct foreign SQL writes."""
import json
import time
import urllib.error
import urllib.request
from pathlib import Path
ROOT=Path(__file__).resolve().parents[3]
PASSWORD=json.loads((ROOT/'dev/local/accounts.json').read_text())['password']
BASE='http://127.0.0.1:8013'
checks=[]
def expect(ok,label):
    if not ok: raise AssertionError(label)
    checks.append(label)
def request(base,path,method='GET',payload=None,token=None,key=None,code=200):
    headers={'Content-Type':'application/json'}
    if token: headers['Authorization']='Bearer '+token
    if key: headers['Idempotency-Key']=key
    req=urllib.request.Request(base+path,data=json.dumps(payload).encode() if payload is not None else None,headers=headers,method=method)
    try:
        res=urllib.request.urlopen(req,timeout=25);status=res.status;body=json.load(res)
    except urllib.error.HTTPError as e:status=e.code;body=json.load(e)
    expect(status==code,f'{method} {path} → {code}, actual {status}: '+body.get('error',''))
    return body
class Client:
    def __init__(self,login,org=1001,div=1001):
        res=request('http://127.0.0.1:8002','/v1/auth/login','POST',{'login':login,'password':PASSWORD,'organization_id':org,'division_id':div})
        self.token=res['access_token'];self.rev=0
    def get(self,path,code=200):
        r=request(BASE,path,token=self.token,code=code)
        if code==200:self.rev=r['data']['revision'];return r['data']['value']
        return r
    def put(self,path,data,method='POST',code=200,key=None,rev=None):
        r=request(BASE,path,method,{'expected_revision':self.rev if rev is None else rev,'data':data},self.token,key or f'attendance-test-{time.time_ns()}',code)
        if code==200:self.rev=r['data']['revision'];return r['data']['value']
        return r

def lesson(div,start,end,school=None):
    return {'division_id':div,'date':'2026-11-15','start_time':start,'end_time':end,'lesson_name':'DEV Attendance HTTP lesson','teacher_name':'DEV display teacher','location':'DEV site','classroom':'101','color':'#228855','is_changed':False,'is_in_person':True,'is_for_all':True,'is_public':school is None,'school_id':school,'target_group_ids':[]}
def main():
    admin=Client('dev_admin');student=Client('dev_student_01');staff=Client('dev_core_staff');math=Client('dev_admin_math',2001,2001)
    marker=ROOT/'dev/local/attendance-demo.json'
    if marker.exists():demo=json.loads(marker.read_text())
    else:
        divisions=request('http://127.0.0.1:8003','/v1/core/divisions',token=admin.token)['data']
        existing=next((d for d in divisions if d['name']=='DEV Attendance published calendar'),None)
        div=existing['id'] if existing else request('http://127.0.0.1:8003','/v1/core/divisions','POST',{'name':'DEV Attendance published calendar'},admin.token,code=201)['data']['id']
        demo={'foreign_division_id':div}
        marker.write_text(json.dumps(demo,indent=2)+'\n')
    foreign=demo['foreign_division_id']
    admin.get('/v1/attendance/calendars')
    for div in [1001,1002,foreign]:admin.put(f'/v1/attendance/divisions/{div}/calendar',{'visibility':'organization','timezone':'Europe/Moscow'},'PUT')
    math.get('/v1/attendance/calendars');math.put('/v1/attendance/divisions/2001/calendar',{'visibility':'closed','timezone':'Europe/Moscow'},'PUT')
    admin.get('/v1/attendance/calendars');staff.get('/v1/attendance/calendars')
    staff.put('/v1/attendance/divisions/1001/schedule',lesson(1001,'09:00','10:00'),code=403)
    staff.put('/v1/attendance/divisions/1002/schedule',lesson(1002,'09:30','10:30'))
    admin.get('/v1/attendance/calendars')
    batch={'creates':[lesson(1001,'09:00','10:00'),lesson(foreign,'09:15','09:45'),lesson(1002,'10:30','11:30'),lesson(1001,'12:00','13:00',1001)],'updates':[],'deletes':[]}
    key=f'attendance-batch-{time.time_ns()}';rev=admin.rev
    created=admin.put('/v1/attendance/schedule/bulk',batch,key=key);repeat=admin.put('/v1/attendance/schedule/bulk',batch,key=key,rev=rev)
    expect(created==repeat,'unknown-outcome retry returns original IDs')
    admin.put('/v1/attendance/schedule/bulk',{'creates':[lesson(1001,'14:00','15:00')]},key=key,rev=rev,code=409)
    schedule=student.get('/v1/attendance/me/schedule?date_from=2026-11-15&date_to=2026-11-15')
    expect(all(e['division_id'] in [1001,1002] for e in schedule['events']),'personal includes own divisions only')
    expect({e['division_id'] for e in schedule['events']}=={1001,1002},'personal includes inactive-cabinet division')
    expect(bool(schedule['overlap_clusters']),'overlaps preserved for accordion')
    selected=student.get(f'/v1/attendance/schedule?division_ids={foreign},1001&date_from=2026-11-15&date_to=2026-11-15')
    expect(any(e['division_id']==foreign for e in selected['events']),'published foreign calendar visible')
    student.get(f'/v1/attendance/divisions/{foreign}/attendance-report?date_from=2026-11-15&date_to=2026-11-15',403)
    before=[e['id'] for e in schedule['events']]
    res=request('http://127.0.0.1:8002','/v1/auth/context','POST',{'organization_id':1001,'division_id':1002},student.token)
    student.token=res['access_token'];after=student.get('/v1/attendance/me/schedule?date_from=2026-11-15&date_to=2026-11-15')
    expect([e['id'] for e in after['events']]==before,'personal stable across active-division context')
    math.get('/v1/attendance/divisions/1001/schedule?date_from=2026-11-15&date_to=2026-11-15',404)
    admin.get('/v1/attendance/calendars');old=admin.rev
    admin.put('/v1/attendance/schedule/bulk',{'deletes':[{'id':created['created_ids'][0],'division_id':1001}],'creates':[lesson(1002,'16:00','15:00')]},code=400)
    admin.get('/v1/attendance/calendars');expect(old==admin.rev,'failed mixed batch does not commit')
    typ=admin.put('/v1/attendance/divisions/1002/attendance-types',{'code':f'dev_present_{time.time_ns()}','name':'DEV Attendance Present','color':'#00aa00','mark':'П','sort_order':10,'is_active':True,'counts_as_present':True})
    day_date='2030-01-'+str(1+time.time_ns()%28).zfill(2)
    # Resume-safe day listing avoids duplicate-date writes.
    days=admin.get('/v1/attendance/divisions/1002/class-days?date_from=2030-01-01&date_to=2030-01-31')
    day=next((v for v in days if v['date']==day_date),None) or admin.put('/v1/attendance/divisions/1002/class-days',{'date':day_date,'comment':'DEV Attendance guard probe','lesson_ids':[]})
    mark=admin.put(f"/v1/attendance/divisions/1002/class-days/{day['id']}/attendance",{'account_id':9,'attendance_type_id':typ['id']})
    expect(mark['account_id']==9 and mark['type']['id']==typ['id'],'actual permanent guard allows protected mark')
    admin.put(f"/v1/attendance/divisions/1002/attendance-types/{typ['id']}",{},'DELETE')
    report=student.get(f"/v1/attendance/divisions/1002/class-days/{day['id']}/attendance")
    expect(any(e['account_id']==9 and e['type']['name']==typ['name'] for e in report['entries']),'type snapshot survives archive in real journal')
    admin.get('/v1/attendance/calendars')
    admin.put(f"/v1/attendance/divisions/1002/class-days/{day['id']}",{},'DELETE',code=409)
    admin.put(f"/v1/attendance/divisions/1002/class-days/{day['id']}/attendance/9",{},'DELETE')
    admin.put(f"/v1/attendance/divisions/1002/class-days/{day['id']}",{},'DELETE',code=409)
    student.get('/v1/attendance/calendars');zap=student.put('/v1/attendance/divisions/1002/zaps',{'text':'DEV permanent guard probe','dates':[day_date],'images':[]})
    expect(zap['status']=='pending','actual core guard allows student zap')
    admin.get('/v1/attendance/calendars');admin.put(f"/v1/attendance/divisions/1002/zaps/{zap['id']}/process",{'status':'rejected','answer':'DEV probe complete'})
    request(BASE,'/v1/attendance/calendars',token='forged',code=401)
    student.get('/v1/attendance/me/schedule?date_from=2026-01-01&date_to=2027-01-02',400)
    student.get('/v1/attendance/me/schedule?date_from=2026-11-15&date_to=2026-11-15&organization_id=2001',400)
    demo.update(class_day_id=day['id'],lesson_ids=created['created_ids'],checks=len(checks),history_guard='real_core_mysql',fixture_prefix='DEV Attendance')
    marker.write_text(json.dumps(demo,ensure_ascii=False,indent=2)+'\n')
    (ROOT/'dev/local/attendance-http-report.json').write_text(json.dumps({'status':'passed','checks':checks,'real_dependencies':['auth','core','mysql','attendance'],'history_guard':'real_core_mysql'},ensure_ascii=False,indent=2)+'\n')
    print(f'Passed {len(checks)} real HTTP checks. Real permanent core guard and history writes verified.')
if __name__=='__main__':main()
