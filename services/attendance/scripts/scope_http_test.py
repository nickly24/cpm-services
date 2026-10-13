"""Fresh org/div/live-access acceptance. Own synthetic fixtures; real auth/core/attendance.

--prepare creates only private DEV scope fixtures through Core APIs.
--verify uses them and writes a token-free report; it never changes shared fixture roles.
No broker, shared runtime, production credentials or foreign SQL operations.
"""
import argparse,base64,json,secrets,time,urllib.request,urllib.error
from pathlib import Path
ROOT=Path(__file__).resolve().parents[3]
PWD=json.loads((ROOT/'dev/local/accounts.json').read_text())['password']
FIXTURES=ROOT/'dev/local/attendance-scope-fixtures.json'
checks=[]
PNG='iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lYkAAAAASUVORK5CYII='
def expect(ok,label):
    assert ok,label
    checks.append(label)
def http(port,path,method='GET',data=None,token=None,key=None,want=200,binary=False):
    h={'Content-Type':'application/json'}
    if token:h['Authorization']='Bearer '+token
    if key:h['Idempotency-Key']=key
    req=urllib.request.Request(f'http://127.0.0.1:{port}'+path,data=None if data is None else json.dumps(data).encode(),headers=h,method=method)
    try:
        with urllib.request.urlopen(req,timeout=30) as r:status=r.status;body=r.read();ct=r.headers.get('Content-Type','')
    except urllib.error.HTTPError as e:status=e.code;body=e.read();ct=e.headers.get('Content-Type','')
    expect(status==want,f'{method} {path.split("?")[0]} -> {want}, got {status}')
    if binary and want==200:return body,ct
    return json.loads(body)
class Client:
    def __init__(self,login,org,div):
        self.token=http(8002,'/v1/auth/login','POST',{'login':login,'password':PWD,'organization_id':org,'division_id':div})['access_token'];self.rev=0
    def core(self,path,data=None,method=None,want=200):return http(8003,'/v1/core'+path,method or ('POST' if data is not None else 'GET'),data,self.token,want=want).get('data')
    def context(self,org,div):self.token=http(8002,'/v1/auth/context','POST',{'organization_id':org,'division_id':div},self.token)['access_token']
    def read(self,path,want=200):
        out=http(8013,'/v1/attendance'+path,token=self.token,want=want)
        if want==200:self.rev=out['data']['revision'];return out['data']['value']
        return out
    def write(self,path,data,method='POST',want=200,key=None,revision=None):
        # Refresh only before a new test command; explicit receipt replays preserve the original bytes.
        if revision is None:self.read('/resources/rooms')
        revision=self.rev if revision is None else revision
        out=http(8013,'/v1/attendance'+path,method,{'expected_revision':revision,'data':data},self.token,key or secrets.token_hex(16),want)
        if want==200:self.rev=out['data']['revision'];return out['data']['value']
        return out

def prepare():
    run=secrets.token_hex(5);a=Client('dev_admin',1001,1001);m=Client('dev_admin_math',2001,2001)
    f={'run':run,'prepared_at':time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()),'organizations':[1001,2001]}
    f['d1']=a.core('/divisions',{'name':'DEV Attendance scope A '+run},want=201)['id']
    f['d2']=a.core('/divisions',{'name':'DEV Attendance scope B '+run},want=201)['id']
    f['overlay']=a.core('/divisions',{'name':'DEV Attendance overlay '+run},want=201)['id']
    f['math_division']=m.core('/divisions',{'name':'DEV Attendance other org '+run},want=201)['id']
    perms={k:{'view':True,'edit':True} for k in ['schedule','attendance','scan','zaps','upload']}
    view={k:{'view':True,'edit':False} for k in ['schedule','attendance','scan','zaps','upload']}
    f['role_edit']=a.core('/roles',{'name':'DEV Attendance editor '+run,'permissions':perms},want=201)['id']
    f['role_view']=a.core('/roles',{'name':'DEV Attendance viewer '+run,'permissions':view},want=201)['id']
    def user(c,label,div,role='student',role_id=None):
        login='dev_att_scope_'+run+'_'+label;data={'role':role,'full_name':'DEV Attendance scope '+label+' '+run,'login':login,'password':PWD}
        if role=='student':data['class']=10
        if role_id:data['role_id']=role_id
        u=c.core(f'/divisions/{div}/users',data,want=201)
        f[label]={'account_id':u['account_id'],'login':login}
        return u['account_id']
    s=user(a,'student',f['d1']);a.core(f'/divisions/{f["d2"]}/memberships/{s}',{},'PUT')
    user(a,'other_student',f['d1']);user(m,'math_student',f['math_division'])
    staff=user(a,'staff',f['d1'],'staff_admin',f['role_view']);a.core(f'/divisions/{f["d2"]}/memberships/{staff}',{'role_id':f['role_edit']},'PUT')
    p=user(a,'proctor',f['d1'],'proctor');g=a.core(f'/divisions/{f["d1"]}/groups',{'name':'DEV Attendance scope group '+run},want=201)['id'];f['group']=g
    a.core(f'/divisions/{f["d1"]}/groups/{g}/members',{'account_id':s});a.core(f'/divisions/{f["d1"]}/groups/{g}/members',{'account_id':p})
    FIXTURES.write_text(json.dumps(f,ensure_ascii=False,indent=2)+'\n')
    print('Own synthetic Core scope fixtures prepared; no shared runtime restarted.')

def verify():
    f=json.loads(FIXTURES.read_text());d1,d2,ov,md=f['d1'],f['d2'],f['overlay'],f['math_division'];run=f['run'];u=f['student']['account_id'];mu=f['math_student']['account_id']
    a=Client('dev_admin',1001,1001);m=Client('dev_admin_math',2001,2001);s=Client(f['student']['login'],1001,d1);other=Client(f['other_student']['login'],1001,d1);ms=Client(f['math_student']['login'],2001,md);staff=Client(f['staff']['login'],1001,d1);p=Client(f['proctor']['login'],1001,d1)
    for div,zone,vis in [(d1,'Europe/Moscow','closed'),(d2,'UTC','closed'),(ov,'Europe/Moscow','organization')]:a.write(f'/divisions/{div}/calendar',{'visibility':vis,'timezone':zone},'PUT')
    m.write(f'/divisions/{md}/calendar',{'visibility':'closed','timezone':'Europe/Moscow'},'PUT')
    def lesson(div,start,end):return {'division_id':div,'date':'2036-01-01','start_time':start,'end_time':end,'lesson_name':'DEV Attendance scope lesson '+run,'teacher_name':'DEV Teacher','location':'DEV Site','classroom':'A','color':'#228855','is_public':True,'is_for_all':True,'target_group_ids':[]}
    batch={'creates':[lesson(d1,'09:00','10:00'),lesson(d2,'06:30','07:30'),lesson(ov,'09:15','09:45')],'updates':[],'deletes':[]}
    a.write(f'/divisions/{ov}/calendar',{'visibility':'organization','timezone':'Europe/Berlin'},'PUT')
    import calendar
    for month,code in [(3,'nonexistent_local_time'),(10,'ambiguous_local_time')]:
        last_sunday=max(week[calendar.SUNDAY] for week in calendar.monthcalendar(2036,month));date=f'2036-{month:02d}-{last_sunday:02d}'
        invalid=lesson(ov,'02:30','03:30');invalid['date']=date
        result=a.write(f'/divisions/{ov}/schedule',invalid,want=400)
        expect(result['error']==code,'actual owner rejects '+code)
    a.write(f'/divisions/{ov}/calendar',{'visibility':'organization','timezone':'Europe/Moscow'},'PUT')
    wrong_group=lesson(d2,'16:00','17:00');wrong_group['target_group_ids']=[f['group']];wrong_group['is_for_all']=False
    a.write(f'/divisions/{d2}/schedule',wrong_group,want=404)
    made=a.write('/schedule/bulk',batch)['created_ids'];m.write(f'/divisions/{md}/schedule',lesson(md,'09:00','10:00'))
    path='?date_from=2036-01-01&date_to=2036-01-01';personal=s.read('/me/schedule'+path)
    expect({e['division_id'] for e in personal['events']}=={d1,d2},'own schedule only attached divisions of selected org')
    expect(set(personal['overlap_clusters'][0]['lesson_ids'])==set(made[:2]),'UTC overlay detects mixed-timezone overlap and excludes unselected calendar')
    expect(next(e for e in personal['events'] if e['division_id']==d1)['starts_at'].endswith('06:00:00Z'),'Moscow localdate/time becomes correct UTC')
    ids=[e['id'] for e in personal['events']];s.context(1001,d2);expect([e['id'] for e in s.read('/me/schedule'+path)['events']]==ids,'active cabinet does not alter own schedule')
    selected=s.read(f'/schedule{path}&division_ids={ov}');expect([e['id'] for e in selected['events']]==[made[2]],'published nonmember overlay visible')
    s.read(f'/divisions/{ov}/attendance-report'+path,403);s.read(f'/divisions/{ov}/zaps',403)
    a.read(f'/divisions/{md}/schedule'+path,404);m.read(f'/divisions/{d1}/schedule'+path,404);s.read('/me/schedule'+path+'&organization_id=2001',400)
    s.read('/me/schedule?date_from=2036-02-30&date_to=2036-03-01',400);s.read('/me/schedule?date_from=2036-01-02&date_to=2036-01-01',400);s.read('/me/schedule?date_from=2036-01-01&date_to=2037-01-01',400)
    expect(s.read('/me/schedule?date_from=2035-12-31&date_to=2035-12-31')['events']==[],'local date boundaries do not leak neighbouring day')
    staff.write(f'/divisions/{d1}/schedule',lesson(d1,'14:00','15:00'),want=403)
    own_lesson=staff.write(f'/divisions/{d2}/schedule',lesson(d2,'14:00','15:00'))['created_ids'][0]
    expect(own_lesson>0,'filtered staff edits granted division with read-only active cabinet')
    present={'code':'scoped_present','name':'DEV scope presence '+run,'mark':'П','color':'#00aa00','is_active':True,'counts_as_present':True,'scan_default':True}
    excuse={'code':'scoped_excuse','name':'DEV scope excuse '+run,'mark':'У','color':'#aaaa00','is_active':True,'counts_as_absent':True,'is_excused':True,'zap_default':True}
    ts={};days={}
    for c,div in [(a,d1),(a,d2),(m,md)]:
        ts[div]=c.write(f'/divisions/{div}/attendance-types',present);c.write(f'/divisions/{div}/attendance-types',excuse)
        days[div]=c.write(f'/divisions/{div}/class-days',{'date':'2036-01-01','comment':'DEV Attendance scope '+run,'lesson_ids':[]})
    f['days']=days;f['types']=ts;FIXTURES.write_text(json.dumps(f,ensure_ascii=False,indent=2)+'\n')
    dp=f'/divisions/{d1}/class-days/{days[d1]["id"]}'
    mark=p.write(dp+'/attendance',{'account_id':u,'attendance_type_id':ts[d1]['id']})
    mq=ms.read(f'/divisions/{md}/students');expect(all(x['account_id']==mu for x in mq),'other org roster is populated and isolated')
    m.write(f'/divisions/{md}/class-days/{days[md]["id"]}/attendance',{'account_id':mu,'attendance_type_id':ts[md]['id']})
    s.read(dp+'/attendance?account_id='+str(f['other_student']['account_id']),403)
    a.read(f'/divisions/{d2}/class-days/{days[d1]["id"]}',404);a.write(f'/divisions/{d2}/attendance-types/{ts[d1]["id"]}',present,'PUT',404)
    a.write(dp+'/attendance',{'account_id':mu,'attendance_type_id':ts[d1]['id']},want=404)
    q=http(8013,'/v1/attendance'+dp+'/qr','POST',{},s.token)['data']['value']['qr']
    a.write(f'/divisions/{d2}/class-days/{days[d2]["id"]}/scan',{'qr':q},want=400);m.write(f'/divisions/{md}/class-days/{days[md]["id"]}/scan',{'qr':q},want=400);a.write(dp+'/scan',{'qr':q+'x'},want=400);a.write(dp+'/scan',{'qr':str(u)},want=400)
    scanned=p.write(dp+'/scan',{'qr':q});expect(scanned['id']==mark['id'],'real signed scoped QR records one attendance entry')
    # Live proctor assignment and exact receipt replay must both observe current grants.
    p.read('/resources/rooms');rev=p.rev;key=secrets.token_hex(16);data={'account_id':u,'attendance_type_id':ts[d1]['id']}
    receipt=p.write(dp+'/attendance',data,key=key,revision=rev);expect(p.write(dp+'/attendance',data,key=key,revision=rev)==receipt,'unchanged proctor assignment exact replay succeeds')
    a.core(f'/divisions/{d1}/groups/{f["group"]}/members/{f["proctor"]["account_id"]}',method='DELETE')
    p.write(dp+'/attendance',data,key=key,revision=rev,want=403);p.read(dp+'/attendance',403)
    # Restore only this synthetic proctor, preserving other chats' assignments.
    a.core(f'/divisions/{d1}/groups/{f["group"]}/members',{'account_id':f['proctor']['account_id']})
    changed={**present,'name':'DEV scope renamed '+run,'is_active':False}
    a.write(f'/divisions/{d1}/attendance-types/{ts[d1]["id"]}',changed,'PUT')
    history=s.read(dp+'/attendance')['entries'][0]['type']
    expect(history['name']==present['name'] and history['version']==1 and history['is_active'],'recorded type snapshot survives version change and archive')
    # Empty answer, duplicate/trimmed dates and manual replacement preserve legacy semantics.
    z=s.write(f'/divisions/{d1}/zaps',{'text':'DEV Attendance scope zap '+run,'dates':[' 2036-01-01 ','2036-01-01','2036-01-02'],'images':[{'mime':'image/png','data':PNG}]})
    expect(len(z['dates'])==2,'legacy dates trim and deduplicate')
    zp=f'/divisions/{d1}/zaps/{z["id"]}';b,ct=http(8013,'/v1/attendance'+zp+'/images/1',token=s.token,binary=True)
    expect(b==base64.b64decode(PNG) and ct=='image/png','real owner attachment bytes protected')
    other.read(zp,404);http(8013,'/v1/attendance'+zp+'/images/1',token=other.token,want=404);http(8013,f'/v1/attendance/divisions/{d2}/zaps/{z["id"]}/images/1',token=s.token,want=404);http(8013,f'/v1/attendance/divisions/{md}/zaps/{z["id"]}/images/1',token=m.token,want=404)
    approved=a.write(zp+'/process',{'status':'approved','answer':''});expect([d['status'] for d in approved['dates']]==['linked','no_class_day'],'approve replaces manual mark and preserves missing class date')
    linked=s.read(dp+'/attendance')['entries'][0];expect(linked['id']==mark['id'] and linked['zap_id']==z['id'],'manual replacement retains entry ID and links own zap')
    a.write(zp+'/process',{'status':'rejected','answer':''});expect(s.read(dp+'/attendance')['entries']==[],'corrected rejection removes own zap live marks')
    a.write(zp+'/process',{'status':'approved','answer':'restored'});expect(s.read(dp+'/attendance')['entries'][0]['zap_id']==z['id'],'decision can be corrected back to approved')
    a.write(zp+'/dates/2036-01-01',{},'DELETE');expect(s.read(dp+'/attendance')['entries']==[],'unlink removes owned live mark only')
    a.write(f'/divisions/{d1}/class-days/{days[d1]["id"]}',{},'DELETE',409)
    blocked=http(8003,f'/v1/core/divisions/{d1}/memberships/{u}','DELETE',token=a.token,want=409);expect(blocked['error']=='membership_has_history','permanent Core marker survives unlinked/rejected marks')
    mz=ms.write(f'/divisions/{md}/zaps',{'text':'DEV other org zap '+run,'dates':['2036-01-01'],'images':[{'mime':'image/png','data':PNG}]})
    a.read(f'/divisions/{d1}/zaps/{mz["id"]}',404) if mz['id']!=z['id'] else expect(a.read(zp)['zap']['account_id']==u,'same relative ID resolves only selected organization')
    z2=s.write(f'/divisions/{d2}/zaps',{'text':'DEV Attendance scope second division '+run,'dates':['2036-01-01'],'images':[{'mime':'image/png','data':PNG}]})
    zp2=f'/divisions/{d2}/zaps/{z2["id"]}'
    http(8013,'/v1/attendance'+zp2+'/images/1',token=staff.token,binary=True)
    # Downgrade only own role. The same JWT must lose edit and view immediately.
    staff.read('/resources/rooms');sr=staff.rev;sk=secrets.token_hex(16);body={'division_id':d2,'date':'2036-01-02','start_time':'08:00','end_time':'09:00','lesson_name':'DEV role replay '+run,'teacher_name':'DEV','location':'DEV','classroom':'A','color':'#228855','is_public':True,'is_for_all':True}
    saved=staff.write(f'/divisions/{d2}/schedule',body,key=sk,revision=sr)
    a.core(f'/roles/{f["role_edit"]}',{'name':'DEV Attendance revoked '+run,'permissions':{}},'PUT')
    staff.write(f'/divisions/{d2}/schedule',body,key=sk,revision=sr,want=403);staff.read(f'/divisions/{d2}/attendance-report'+path,403);http(8013,'/v1/attendance'+zp2+'/images/1',token=staff.token,want=404);http(8013,'/v1/attendance'+zp+'/images/1',token=staff.token,binary=True)
    # Archived, unrelated published calendar must not poison active calendars.
    a.write(f'/divisions/{ov}/schedule/{made[2]}',{},'DELETE');a.core(f'/divisions/{ov}',method='DELETE')
    expect(any(c['division_id']==d1 for c in s.read('/calendars')),'archived nonmember calendar does not poison calendar list')
    expect({e['division_id'] for e in s.read('/me/schedule'+path)['events']}=={d1,d2},'archived calendar does not poison personal schedule');s.read(f'/schedule{path}&division_ids={ov}',404)
    f['zap_id']=z['id'];f['second_division_zap_id']=z2['id'];f['math_zap_id']=mz['id'];f['attachment']={'organization_id':1001,'division_id':d1,'account_id':u,'zap_id':z['id'],'image':1};FIXTURES.write_text(json.dumps(f,ensure_ascii=False,indent=2)+'\n')
    report={'status':'passed','verified_at':time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()),'checks':checks,'fixtures':{'run':run,'organizations':[1001,2001],'divisions':[d1,d2,md]},'dependencies':['actual auth','actual core permanent guard','actual attendance','actual MySQL'],'camera':'deferred_by_user','uploads':'separate_joint_acceptance'}
    (ROOT/'dev/local/attendance-scope-http-report.json').write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n');print(f'Passed {len(checks)} fresh scope/live-access/date/legacy HTTP checks.')
if __name__=='__main__':
    parser=argparse.ArgumentParser();parser.add_argument('--prepare',action='store_true');parser.add_argument('--verify',action='store_true');args=parser.parse_args()
    if args.prepare:prepare()
    if args.verify:verify()
    if not(args.prepare or args.verify):parser.error('choose --prepare or --verify')
