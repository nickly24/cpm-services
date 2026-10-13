"""Actual local BFF/auth/core/attendance/MySQL. History writes must fail closed."""
import http.cookiejar,json,time,uuid,urllib.request,urllib.error
from pathlib import Path
ROOT=Path(__file__).resolve().parents[3]
password=json.loads((ROOT/'dev/local/accounts.json').read_text())['password']
checks=[]
def expect(value,label):
    assert value,label
    checks.append(label)
class Client:
    def __init__(self,name):
        self.http=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        self.context='';self.revision=0
        self.request('auth/login',{'login':name,'password':password})
    def request(self,path,body=None,method=None,code=200,key=None):
        headers={'X-CPM-Request':'1','X-CPM-Context':self.context,'Origin':'http://127.0.0.1:3001','Content-Type':'application/json'}
        if key:headers['Idempotency-Key']=key
        req=urllib.request.Request('http://127.0.0.1:8001/bff/'+path,data=json.dumps(body).encode() if body is not None else None,headers=headers,method=method or ('POST' if body is not None else 'GET'))
        try:
            with self.http.open(req,timeout=30) as res:status=res.status;data=json.load(res)
        except urllib.error.HTTPError as e:status=e.code;data=json.load(e)
        expect(status==code,f'{path.split("?")[0]} → {code}, got {status} {data.get("error","")}')
        expect(not any(k in data for k in ['token','access_token','refresh_token']),'no browser token')
        if 'context_revision' in data:self.context=data['context_revision']
        result=data.get('data',data)
        if 'revision' in result:self.revision=result['revision']
        return result
    def read(self,path,code=200):
        result=self.request('attendance'+path,code=code)
        return result['value'] if code==200 else result
    def write(self,path,data,method='POST',code=200,key=None,revision=None):
        return self.request('attendance'+path,{'expected_revision':self.revision if revision is None else revision,'data':data},method,code,key or str(uuid.uuid4()))

def main():
    demo=json.loads((ROOT/'dev/local/attendance-demo.json').read_text());foreign=demo['foreign_division_id']
    admin=Client('dev_admin');student=Client('dev_student_01');staff=Client('dev_core_staff');proctor=Client('dev_proctor_1');supervisor=Client('dev_supervisor')
    schedule=student.read('/me/schedule?date_from=2026-11-15&date_to=2026-11-15')
    expect({e['division_id'] for e in schedule['events']}=={1001,1002},'personal all assigned divisions within selected organization')
    expect(bool(schedule['overlap_clusters']),'overlap metadata reaches BFF')
    ids=[e['id'] for e in schedule['events']]
    selected=student.read(f'/schedule?division_ids={foreign}&date_from=2026-11-15&date_to=2026-11-15')
    expect(any(e['division_id']==foreign for e in selected['events']),'published calendar outside membership readable')
    student.read(f'/divisions/{foreign}/attendance-report?date_from=2026-11-15&date_to=2026-11-15',403)
    student.read('/me/schedule?organization_id=2001&date_from=2026-11-15&date_to=2026-11-15',400)
    old_context=student.context;student.request('auth/context',{'organization_id':1001,'division_id':1002});new_context=student.context
    expect([e['id'] for e in student.read('/me/schedule?date_from=2026-11-15&date_to=2026-11-15')['events']]==ids,'calendar filters independent of active division')
    student.context=old_context;student.read('/calendars',409);student.context=new_context
    student.request('auth/context',{'organization_id':2001,'division_id':2001});student.read('/divisions/1001/schedule?date_from=2026-11-15&date_to=2026-11-15',404)
    student.request('auth/context',{'organization_id':1001,'division_id':1001})
    staff.read('/calendars');staff.write('/divisions/1001/calendar',{'visibility':'closed','timezone':'UTC'},'PUT',403)
    # Staff can edit the permitted division while another division is active.
    key=str(uuid.uuid4());rev=staff.revision;data={'visibility':'organization','timezone':'Europe/Moscow'}
    saved=staff.write('/divisions/1002/calendar',data,'PUT',key=key,revision=rev)
    expect(staff.write('/divisions/1002/calendar',data,'PUT',key=key,revision=rev)==saved,'BFF preserves exact receipt retry')
    staff.write('/divisions/1002/calendar',data,'PUT',409,revision=rev)
    proctor.read('/divisions/1001/students');proctor.read(f'/divisions/{foreign}/students',403)
    supervisor.read('/divisions/1001/attendance-report?date_from=2026-11-15&date_to=2026-11-15')
    supervisor.write('/divisions/1001/class-days',{'date':'2030-01-01','lesson_ids':[]},code=403)
    defaults=admin.read('/divisions/1002/attendance-types')
    needs_default=not any(t['is_active'] and t['scan_default'] for t in defaults)
    typ=admin.write('/divisions/1002/attendance-types',{'code':'bff_'+uuid.uuid4().hex[:12],'name':'DEV BFF attendance present','mark':'П','color':'#228855','is_active':True,'counts_as_present':True,'scan_default':needs_default})['value']
    days=admin.read('/divisions/1002/class-days?date_from=2033-01-01&date_to=2033-01-31')
    day_date=next('2033-01-'+str(i).zfill(2) for i in range(1,32) if not any(d['date']=='2033-01-'+str(i).zfill(2) for d in days))
    day=admin.write('/divisions/1002/class-days',{'date':day_date,'comment':'DEV BFF full attendance workflow','lesson_ids':[]})['value']
    mark=admin.write(f"/divisions/1002/class-days/{day['id']}/attendance",{'account_id':9,'attendance_type_id':typ['id']})['value']
    expect(mark['account_id']==9,'real guard permits manual attendance through BFF')
    qr=student.request(f"attendance/divisions/1002/class-days/{day['id']}/qr",{})['value']
    expect(qr['qr'].startswith('attendance-qr-v1.'),'signed day/division-bound QR through cookie BFF')
    admin.read('/calendars')
    scanned=admin.write(f"/divisions/1002/class-days/{day['id']}/scan",{'qr':qr['qr']})['value']
    expect(scanned['id']==mark['id'] and scanned['type']['scan_default'],'signed scan updates same mark using configured default')
    admin.write(f"/divisions/1002/class-days/{day['id']}/scan",{'qr':qr['qr']+'x'},code=400)
    admin.write(f"/divisions/1001/class-days/{day['id']}/scan",{'qr':qr['qr']},code=400)
    admin.write(f"/divisions/1002/attendance-types/{typ['id']}",{},'DELETE')
    report=student.read(f"/divisions/1002/class-days/{day['id']}/attendance")
    expect(report['entries'][0]['type']==scanned['type'],'recorded type snapshot survives archive')
    admin.read('/calendars')
    admin.write(f"/divisions/1002/class-days/{day['id']}",{'date':'2033-02-01','comment':'blocked','lesson_ids':[]},'PUT',409)
    defaults=admin.read('/divisions/1002/attendance-types');zap_type=next((t for t in defaults if t['is_active'] and t['zap_default']),None);created_zap_type=zap_type is None
    if zap_type is None:zap_type=admin.write('/divisions/1002/attendance-types',{'code':'bff_zap_'+uuid.uuid4().hex[:12],'name':'DEV BFF excused absence','mark':'У','color':'#aa8800','is_active':True,'counts_as_absent':True,'is_excused':True,'zap_default':True})['value']
    missing_days=admin.read('/divisions/1002/class-days?date_from=2033-02-01&date_to=2033-02-28')
    missing_date=next('2033-02-'+str(i).zfill(2) for i in range(1,29) if not any(d['date']=='2033-02-'+str(i).zfill(2) for d in missing_days))
    png='iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lYkAAAAASUVORK5CYII='
    student.read('/calendars');zap=student.write('/divisions/1002/zaps',{'text':'DEV BFF full zap workflow','dates':[day_date,missing_date],'images':[{'mime':'image/png','data':png}]})['value']
    expect(zap['status']=='pending' and not zap.get('images'),'real zap created without leaking attachment bytes')
    detail=student.read(f"/divisions/1002/zaps/{zap['id']}");expect(detail['image_count']==1,'protected image metadata returned')
    image=student.request(f"attendance/divisions/1002/zaps/{zap['id']}/images/1");expect(image['mime']=='image/png' and image['data']==png,'actual protected owner image adapted to authenticated JSON')
    proctor.request(f"attendance/divisions/1002/zaps/{zap['id']}/images/1",code=404)
    other=Client('dev_student_02');other.request(f"attendance/divisions/1002/zaps/{zap['id']}/images/1",code=404);other.request('auth/logout',{})
    staff.read('/calendars')
    approved=staff.write(f"/divisions/1002/zaps/{zap['id']}/process",{'status':'approved','answer':''})['value']
    entries=student.read(f"/divisions/1002/class-days/{day['id']}/attendance")['entries']
    linked=next(e for e in entries if e['account_id']==9)
    expect(linked['id']==scanned['id'] and linked['zap_id']==zap['id'] and linked['type']['zap_default'],'approval replaces manual/QR mark in place using ZapDefault')
    staff.write(f"/divisions/1002/zaps/{zap['id']}/process",{'status':'rejected','answer':''})
    expect(not any(e['account_id']==9 for e in student.read(f"/divisions/1002/class-days/{day['id']}/attendance")['entries']),'approved decision can be corrected to rejected with empty answer')
    approved=staff.write(f"/divisions/1002/zaps/{zap['id']}/process",{'status':'approved','answer':'DEV accepted'})['value']
    expect([d['status'] for d in approved['dates']]==['linked','no_class_day'],'approval links available day and retains missing-day date')
    admin.read('/calendars');admin.write(f"/divisions/1002/class-days/{day['id']}/attendance/9",{},'DELETE',409)
    future=admin.write('/divisions/1002/class-days',{'date':missing_date,'comment':'DEV BFF retry day','lesson_ids':[]})['value']
    staff.read('/calendars');retried=staff.write(f"/divisions/1002/zaps/{zap['id']}/dates/{missing_date}/retry",{})['value'];expect(all(d['status']=='linked' for d in retried['dates']),'retry links newly created real class day')
    staff.write(f"/divisions/1002/zaps/{zap['id']}/dates/{day_date}",{},'DELETE');staff.write(f"/divisions/1002/zaps/{zap['id']}/dates/{missing_date}",{},'DELETE')
    report=student.read('/divisions/1002/attendance-report?date_from=2033-01-01&date_to=2033-02-28');expect(not any(e['class_day_id'] in [day['id'],future['id']] for e in report['entries']),'unlink removes only zap-owned marks while retaining history')
    admin.read('/calendars');admin.write(f"/divisions/1002/class-days/{day['id']}",{},'DELETE',409)
    student.read('/calendars');rejected=student.write('/divisions/1002/zaps',{'text':'DEV BFF rejection workflow','dates':[day_date],'images':[]})['value']
    staff.read('/calendars');decision=staff.write(f"/divisions/1002/zaps/{rejected['id']}/process",{'status':'rejected','answer':'DEV rejected'})['value'];expect(decision['status']=='rejected','rejection stored and visible to student')
    admin.read('/calendars')
    if created_zap_type:admin.write(f"/divisions/1002/attendance-types/{zap_type['id']}",{},'DELETE')
    admin.request('auth/context',{'organization_id':1001,'division_id':1002})
    blocked=admin.request('core/memberships/9',method='DELETE',code=409);expect(blocked['error']=='membership_has_history','core still protects membership after every mark is unlinked/deleted')
    admin.request('auth/context',{'organization_id':1001,'division_id':1001})
    (ROOT/'dev/local/attendance-workflow-demo.json').write_text(json.dumps({'division_id':1002,'day_id':day['id'],'day_date':day_date,'retry_day_id':future['id'],'retry_date':missing_date,'zap_id':zap['id']},indent=2)+'\n')
    student.request('attendance/internal/core/history-reservations',code=501)
    # Read-only regressions for already connected neighbours, no new domain writes.
    student.request('homework/assignments');student.request('tests/catalog')
    for client in [admin,student,staff,proctor,supervisor]:client.request('auth/logout',{})
    report={'status':'passed','checks':checks,'history_guard':'real_core_mysql'}
    (ROOT/'dev/local/attendance-bff-http-report.json').write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n')
    print(f'Passed {len(checks)} actual BFF HTTP checks; full workflow with real permanent guard.')
if __name__=='__main__':main()
