"""Real core/auth HTTP/MySQL races. Only own synthetic users are created via core API."""
import concurrent.futures,json,secrets,sys,threading,urllib.request,urllib.error
from pathlib import Path
ROOT=Path(__file__).resolve().parents[3]
sys.path.insert(0,str(ROOT/'dev/scripts'))
from export_structure import read_env
key=read_env(ROOT/'dev/local/core.env')['AUTH_INTERNAL_KEY']
password=json.loads((ROOT/'dev/local/accounts.json').read_text())['password']
checks=[];run=secrets.token_hex(5)
def expect(value,name):
    assert value,name
    checks.append(name)
def request(path,body=None,method=None,token=None,service=False):
    headers={'Content-Type':'application/json'}
    if token:headers['Authorization']='Bearer '+token
    if service:headers['X-Service-Key']=key
    req=urllib.request.Request('http://127.0.0.1:8003'+path,data=json.dumps(body).encode() if body is not None else None,headers=headers,method=method or ('POST' if body is not None else 'GET'))
    try:
        with urllib.request.urlopen(req,timeout=30) as res:return res.status,json.load(res)
    except urllib.error.HTTPError as e:return e.code,json.load(e)
def core(path,body=None,method=None,want=200):
    status,out=request('/v1/core'+path,body,method,admin)
    expect(status==want,f'core {method or "GET"} {path} -> {want}, got {status}')
    return out.get('data',out)
def guard(account,division=1002,org=1001):
    return request('/internal/core/attendance/history-reservations',{'organization_id':org,'division_id':division,'account_id':account,'operation_key':'race-'+secrets.token_hex(8)},service=True)
def make_student(index):
    result=core('/divisions/1001/users',{'role':'student','full_name':f'DEV Attendance guard {run} {index}','login':f'dev_att_guard_{run}_{index}','password':password,'class':10,'school_id':1001},'POST',201)
    uid=result['account_id'];core(f'/divisions/1002/memberships/{uid}',{},'PUT');return uid

def main():
    global admin
    req=urllib.request.Request('http://127.0.0.1:8002/v1/auth/login',data=json.dumps({'login':'dev_admin','password':password,'organization_id':1001,'division_id':1001}).encode(),headers={'Content-Type':'application/json'})
    with urllib.request.urlopen(req,timeout=30) as r:admin=json.load(r)['access_token']
    path='/internal/core/attendance/history-reservations'
    status,_=request(path,{'organization_id':1001,'division_id':1002,'account_id':9,'operation_key':'valid-key'},token=admin)
    expect(status==401,'public Bearer cannot reserve history')
    for account,division,org in [(3,1002,1001),(9,2001,1001),(9,1002,2001),(999999999,1002,1001)]:
        status,_=guard(account,division,org);expect(status==404,'wrong role/account/scope cannot reserve')
    first=make_student('first');status,a=guard(first);expect(status==200 and a['data']['permanent'],'real permanent marker committed')
    status,b=guard(first);expect(status==200 and a['data']['reservation_id']==b['data']['reservation_id'],'repeat with another operation key retains original marker')
    blocked=core(f'/divisions/1002/memberships/{first}',method='DELETE',want=409);expect(blocked['error']=='membership_has_history','real revoke blocked by permanent marker')
    # Disabling an account does not remove its marker; inactive students cannot reserve.
    core(f'/divisions/1001/users/{first}/active',{'is_active':False},'PUT');status,_=guard(first);expect(status==404,'inactive account cannot reserve')
    core(f'/divisions/1001/users/{first}/active',{'is_active':True},'PUT');status,c=guard(first);expect(c['data']['reservation_id']==a['data']['reservation_id'],'marker retained after deactivate/reactivate')
    removed=make_student('removed');core(f'/divisions/1002/memberships/{removed}',method='DELETE');status,_=guard(removed);expect(status==404,'revoke winner prevents later reservation')
    # A successful guard followed by caller rollback must still protect membership.
    status,_=guard(first);core(f'/divisions/1002/memberships/{first}',method='DELETE',want=409);expect(status==200,'no release for caller without attendance commit')
    outcomes=[]
    for i in range(8):
        uid=make_student(i);start=threading.Barrier(2)
        def reserve():start.wait();return guard(uid)
        def revoke():start.wait();return request(f'/v1/core/divisions/1002/memberships/{uid}',method='DELETE',token=admin)
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            g=pool.submit(reserve);d=pool.submit(revoke);gs,gb=g.result();ds,db=d.result()
        expect((gs,ds) in [(200,409),(404,200)],f'race {i}: exactly one admissible winner {(gs,ds)}')
        if gs==200:expect(db['error']=='membership_has_history','guard winner gives history-specific revoke refusal')
        else:expect(guard(uid)[0]==404,'revoke winner remains unreserved')
        outcomes.append({'guard':gs,'revoke':ds})
    report={'status':'passed','checks':checks,'races':outcomes,'synthetic_user_prefix':'dev_att_guard_'+run,'guard':'real_core_mysql'}
    (ROOT/'dev/local/attendance-core-history-report.json').write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n')
    print(f'Passed {len(checks)} real guard checks including 8 guard/revoke races.')
if __name__=='__main__':main()
