"""Real HTTP integration tests against the running dev auth container. No production access."""
import concurrent.futures
import datetime as dt
import json
import sys
import time
import urllib.request
import urllib.error
from pathlib import Path
import jwt

ROOT=Path(__file__).resolve().parents[3]
sys.path.insert(0,str(ROOT/'dev/scripts'))
from bootstrap import settings,mysql_connection

BASE='http://127.0.0.1:8002'
PASSWORD=json.loads((ROOT/'dev/local/accounts.json').read_text())['password']
RESULTS=[]
SESSIONS=set()
PUBLIC=None


def request(path,body=None,token=None,method=None,raw=None):
    data=raw if raw is not None else (json.dumps(body).encode() if body is not None else None)
    headers={'Content-Type':'application/json'}
    if token:headers['Authorization']='Bearer '+token
    req=urllib.request.Request(BASE+path,data=data,headers=headers,method=method or ('POST' if data is not None else 'GET'))
    try:
        with urllib.request.urlopen(req,timeout=25) as res:
            code=res.status;result=json.load(res)
    except urllib.error.HTTPError as err:
        code=err.code;result=json.load(err)
    if code==200 and result.get('token'):
        claims=jwt.decode(result['token'],PUBLIC,algorithms=['RS256'],audience='cpm-services',issuer='cpm-auth-dev')
        SESSIONS.add(claims['sid'])
        assert claims['account_id']==result['user']['account_id']
        assert claims['organization_id']==result['user']['organization_id']
        assert claims['division_id']==result['user']['division_id']
        assert claims['role']==result['user']['role']
        assert not {'password','login','refresh_token'} & claims.keys()
    return code,result


def expect(name,path,code,body=None,token=None,method=None,raw=None):
    actual,result=request(path,body,token,method,raw)
    assert actual==code,f'{name}: expected {code}, received {actual}: {result.get("error")}'
    RESULTS.append({'case':name,'status':'passed','http_status':actual})
    return result


def login(name,**kwargs):
    return expect('login '+name,'/v1/auth/login',200,{'login':name,'password':PASSWORD,**kwargs})


def sql(statement,params=()):
    conn=mysql_connection(settings());cur=conn.cursor()
    try:
        cur.execute(statement,params)
        rows=cur.fetchall() if cur.with_rows else []
        conn.commit();return rows
    finally:cur.close();conn.close()


def main():
    global PUBLIC
    expect('ready','/health/ready',200)
    contract=expect('OpenAPI contract','/openapi.json',200)
    assert contract['openapi']=='3.1.0' and '/v1/auth/context' in contract['paths']
    keys=expect('public JWKS','/.well-known/jwks.json',200)
    assert len(keys['keys'])==1 and 'd' not in keys['keys'][0]
    PUBLIC=jwt.PyJWK.from_dict(keys['keys'][0]).key
    fixtures=json.loads((ROOT/'dev/local/auth-seed-complete.json').read_text())['fixtures']
    for f in fixtures:
        data=expect('account '+f['login'],'/v1/auth/login',f['expected_login'],{'login':f['login'],'password':PASSWORD})
        if f['expected_login']==200:
            assert data['user']['role']==f['role']
            expect('current user '+f['login'],'/v1/auth/me',200,token=data['token'])
    expect('bad password','/v1/auth/login',401,{'login':'dev_admin','password':'wrong'})
    expect('missing account','/v1/auth/login',401,{'login':'dev_missing_account','password':PASSWORD})
    expect('SQL injection login','/v1/auth/login',401,{'login':"' OR 1=1 --",'password':PASSWORD})
    expect('empty JSON','/v1/auth/login',400,{})
    expect('null JSON','/v1/auth/login',400,raw=b'null')
    expect('array JSON','/v1/auth/login',400,raw=b'[]')
    expect('role injection','/v1/auth/login',400,{'login':'dev_student_01','password':PASSWORD,'role':'admin'})
    expect('trailing JSON','/v1/auth/login',400,raw=b'{} {}')
    expect('division without organization','/v1/auth/login',400,{'login':'dev_student_01','password':PASSWORD,'division_id':1002})
    expect('body size','/v1/auth/login',400,raw=json.dumps({'login':'dev_admin','password':'x'*17000}).encode())
    expect('missing token','/v1/auth/me',401)
    expect('malformed token','/v1/auth/me',401,token='broken')

    student=login('dev_student_01')
    assert student['user']['division_ids']==[1001,1002]
    assert [o['id'] for o in student['organizations']]==[1001,2001]
    token=student['token']
    claims=jwt.decode(token,PUBLIC,algorithms=['RS256'],audience='cpm-services',issuer='cpm-auth-dev')
    assert claims['exp']-claims['iat']==900
    expect('denied division','/v1/auth/context',403,{'organization_id':1001,'division_id':1003},token)
    expect('organization division mismatch','/v1/auth/context',403,{'organization_id':2001,'division_id':1001},token)
    expect('student organization-wide denied','/v1/auth/context',403,{'organization_id':1001},token)
    reserve=expect('student switches division','/v1/auth/context',200,{'organization_id':1001,'division_id':1002},token)
    expect('previous context token rejected','/v1/auth/me',401,token=token)
    expect('same context switch','/v1/auth/context',200,{'organization_id':1001,'division_id':1002},reserve['token'])
    math=expect('student switches organization','/v1/auth/context',200,{'organization_id':2001,'division_id':2001},reserve['token'])
    assert math['user']['division_ids']==[2001]
    expect('token introspection','/v1/auth/introspect',200,{},math['token'])

    staff=login('dev_staff')
    assert staff['user']['permissions']['tests']=={'view':True,'edit':False}
    editor=expect('staff different division rights','/v1/auth/context',200,{'organization_id':1001,'division_id':1002},staff['token'])
    assert editor['user']['permissions']['tests']=={'view':True,'edit':True}
    expect('staff cannot select unrestricted division','/v1/auth/context',403,{'organization_id':1001,'division_id':1003},editor['token'])
    expect('staff cannot select foreign organization','/v1/auth/context',403,{'organization_id':2001,'division_id':2001},editor['token'])
    for name in ['dev_proctor_1','dev_examiner_1']:
        actor=login(name)
        expect(name+' switches division','/v1/auth/context',200,{'organization_id':1001,'division_id':1002},actor['token'])
    admin=login('dev_admin')
    assert admin['user']['organization_admin'] and admin['user']['division_ids']==[1001,1002,1003]
    assert all(p['edit'] for p in admin['user']['permissions'].values())
    expect('admin foreign organization denied','/v1/auth/context',403,{'organization_id':2001,'division_id':2001},admin['token'])
    organization=expect('admin organization-wide context','/v1/auth/context',200,{'organization_id':1001},admin['token'])
    assert organization['user']['division_id'] is None
    expect('login denied context','/v1/auth/login',403,{'login':'dev_admin','password':PASSWORD,'organization_id':2001,'division_id':2001})
    expect('login chosen context','/v1/auth/login',200,{'login':'dev_student_01','password':PASSWORD,'organization_id':2001,'division_id':2001})
    # Foreign organization roles and division links are rejected by the database too.
    for name,statement,params in [
        ('foreign role constraint','UPDATE auth_division_access SET role_id=2001 WHERE user_id=%s AND division_id=1001',(staff['user']['account_id'],)),
        ('foreign division constraint','UPDATE auth_division_access SET organization_id=2001 WHERE user_id=%s AND division_id=1001',(student['user']['account_id'],)),
    ]:
        try:
            sql(statement,params)
        except Exception as error:
            assert getattr(error,'errno',None)==1452,name
        else:
            raise AssertionError(name+' accepted invalid data')
        RESULTS.append({'case':name,'status':'passed','database_error':1452})

    private=(ROOT/'dev/local/auth-private.pem').read_bytes()
    kid=keys['keys'][0]['kid']
    for name,change in [('expired',{'exp':int(time.time())-60}),('wrong issuer',{'iss':'foreign'}),('wrong audience',{'aud':'foreign'}),('missing expiration',{'exp':None})]:
        altered={**claims,**change}
        if altered.get('exp') is None:altered.pop('exp',None)
        forged=jwt.encode(altered,private,algorithm='RS256',headers={'kid':kid})
        expect(name,'/v1/auth/me',401,token=forged)
    forged=jwt.encode(claims,'a-different-test-secret-of-sufficient-length',algorithm='HS256',headers={'kid':kid})
    expect('wrong algorithm','/v1/auth/me',401,token=forged)
    pieces=token.split('.');pieces[1]=pieces[1][:-1]+('A' if pieces[1][-1]!='A' else 'B')
    expect('tampered payload','/v1/auth/me',401,token='.'.join(pieces))

    current=login('dev_student_02')
    rotated=expect('refresh rotates','/v1/auth/refresh',200,{'refresh_token':current['refresh_token']})
    assert current['refresh_token']!=rotated['refresh_token']
    expect('replayed refresh rejected','/v1/auth/refresh',401,{'refresh_token':current['refresh_token']})
    expect('replay revokes session','/v1/auth/me',401,token=rotated['token'])
    current=login('dev_student_02')
    expect('logout','/v1/auth/logout',200,{},current['token'])
    expect('logout rejects access','/v1/auth/me',401,token=current['token'])
    expect('logout rejects refresh','/v1/auth/refresh',401,{'refresh_token':current['refresh_token']})

    actor=login('dev_auth_version')
    uid=actor['user']['account_id']
    try:
        sql('UPDATE auth_account_state SET auth_version=auth_version+1 WHERE user_id=%s',(uid,))
        expect('version change rejects access','/v1/auth/me',401,token=actor['token'])
        expect('version change rejects refresh','/v1/auth/refresh',401,{'refresh_token':actor['refresh_token']})
    finally:sql('UPDATE auth_account_state SET auth_version=auth_version-1 WHERE user_id=%s',(uid,))
    actor=login('dev_auth_membership');uid=actor['user']['account_id']
    try:
        sql('DELETE FROM auth_division_access WHERE user_id=%s AND division_id=1001',(uid,))
        expect('removed membership rejects access','/v1/auth/me',401,token=actor['token'])
        expect('removed membership rejects refresh','/v1/auth/refresh',401,{'refresh_token':actor['refresh_token']})
    finally:sql('INSERT INTO auth_division_access VALUES (%s,1001,1001,NULL,NULL)',(uid,))
    actor=login('dev_student_03');uid=actor['user']['account_id']
    try:
        sql('UPDATE auth_account_state SET is_active=0 WHERE user_id=%s',(uid,))
        expect('account disabled after login','/v1/auth/me',401,token=actor['token'])
        expect('disabled refresh','/v1/auth/refresh',401,{'refresh_token':actor['refresh_token']})
    finally:sql('UPDATE auth_account_state SET is_active=1 WHERE user_id=%s',(uid,))
    actor=login('dev_staff')
    try:
        sql('UPDATE admin_role_users SET session_version=session_version+1 WHERE id=1001')
        expect('legacy staff version rejects access','/v1/auth/me',401,token=actor['token'])
        expect('legacy staff version rejects refresh','/v1/auth/refresh',401,{'refresh_token':actor['refresh_token']})
    finally:sql('UPDATE admin_role_users SET session_version=session_version-1 WHERE id=1001')

    actor=login('dev_student_04')
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        replies=list(pool.map(lambda _: request('/v1/auth/refresh',{'refresh_token':actor['refresh_token']}),range(2)))
    assert sorted(code for code,_ in replies)==[200,401]
    RESULTS.append({'case':'concurrent refresh single-use','status':'passed','http_status':[200,401]})
    success=next(data for code,data in replies if code==200)
    expect('concurrent replay revokes session','/v1/auth/me',401,token=success['token'])
    legacy=expect('legacy login alias','/api/auth',200,{'login':'dev_student_05','password':PASSWORD})
    expect('legacy user alias','/api/aun',200,{},legacy['token'])
    expect('legacy logout alias','/api/logout',200,{},legacy['token'])
    for i in range(25):
        expect(f'unlimited wrong-password attempt {i+1}','/v1/auth/login',401,{'login':'dev_student_06','password':'wrong'})
    for i in range(25):
        expect(f'unlimited successful login {i+1}','/v1/auth/login',200,{'login':'dev_student_06','password':PASSWORD})
    report={'checked_at_utc':dt.datetime.now(dt.timezone.utc).isoformat(),'base_url':BASE,'status':'passed','checks':len(RESULTS),'accounts':len(fixtures),'rate_limit':'absent: 25 failed and 25 successful repeated attempts verified','cases':RESULTS}
    (ROOT/'dev/local/auth-http-report.json').write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n')
    print(json.dumps({k:v for k,v in report.items() if k!='cases'},ensure_ascii=False))


if __name__=='__main__':
    try:main()
    finally:
        # Remove only sessions issued by this run. Test tokens never go to reports.
        for sid in SESSIONS:
            sql('DELETE FROM auth_refresh_tokens WHERE session_id=%s',(sid,))
            sql('DELETE FROM auth_sessions WHERE id=%s',(sid,))
