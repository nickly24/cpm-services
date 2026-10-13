"""Real HTTP acceptance against Docker cards/auth/core/tests; synthetic data only."""
import copy,hashlib,subprocess,time,threading
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime,timedelta,timezone
from support import *

run=secrets.token_hex(6)
def main():
 settings();f=json.loads((ROOT/'dev/local/cards-fixtures.json').read_text());pw=json.loads((ROOT/'dev/local/accounts.json').read_text())['password']
 admin=login('dev_admin',pw);other=login('dev_admin_math',pw,2001,2001)
 student=login('dev_cards_student',f['password']);reserve=login('dev_cards_student',f['password'],1001,1002);foreign=login('dev_cards_student',f['password'],2001,2001)
 viewer=login('dev_cards_viewer',f['password']);editor=login('dev_cards_editor',f['password']);noaccess=login('dev_core_staff',pw)
 c=lambda path,token=student,method='GET',body=None,code=200,**kw:api('cards','/v1/cards'+path,token,method,body,code,**kw)
 request('cards','/health/ready');request('cards','/openapi.json')
 request('cards','/v1/cards/tree',code=401,headers={'X-Role':'admin'})
 c('/tree',noaccess,code=403);c('/admin/catalog',student,code=403)
 for token in [student,viewer]:c('/themes',token,'POST',{'name':'Forbidden','direction_id':f['direction']},403)
 c('/themes',admin,'POST',{'name':'Foreign direction','direction_id':f['other_direction']},400)
 c('/themes',admin,'POST',{'name':'Invalid owner','direction_id':f['direction'],'organization_id':2001},400)
 theme=c('/themes',editor,'POST',{'name':'HTTP Cards '+run,'direction_id':f['direction'],'questions':[{'question':f'Q{i}','answer':f'A{i}'} for i in range(25)]})
 tid=theme['id'];sec=f'/sections/manual/{tid}'
 c('/themes',admin,'POST',{'name':theme['name'],'direction_id':f['direction']},409)
 c(sec,foreign,code=404);c(f'/themes/{tid}/cards',other,code=404)
 v=c(sec);expect(len(v['cards'])==25 and v['settings']['batch_size']==10 and v['settings']['study_mode']=='unlearned','default study settings and 25 cards')
 expect([b['size'] for b in v['batches']]==[10,10,5],'stable batch boundaries 10/10/5')
 first=v['cards'][0];second=v['cards'][1];fp=lambda c:hashlib.sha256(json.dumps({'q':c['question'].strip(),'a':c['answer'].strip()},ensure_ascii=False,sort_keys=True).encode()).hexdigest()
 expect(first['content_fingerprint']==fp(first),'fingerprint exactly matches Python AS IS')
 mark={'card_ref':first['card_ref'],'content_fingerprint':first['content_fingerprint']}
 c(sec+'/learned',student,'POST',{**mark,'account_id':f['student']},400)
 c(sec+'/learned',admin,'POST',mark,403)
 c(sec+'/learned',student,'POST',{**mark,'card_ref':'card:999999999'},404)
 c(sec+'/learned',student,'POST',{**mark,'content_fingerprint':'0'*64},409)
 c(sec+'/learned',student,'POST',mark);c(sec+'/learned',student,'POST',mark)
 expect(c(sec,reserve)['cards'][0]['status']=='learned','manual progress shared across divisions')
 changed=c(f'/themes/{tid}/cards/{first["card_id"]}',editor,'PUT',{'expected_revision':first['revision'],'question':first['question'],'answer':'Changed\n\nanswer'})
 stale=c(sec);expect(stale['cards'][0]['status']=='answer_changed' and stale['stats']['answer_changed']==1,'changed answer retains identity and becomes stale')
 c(sec+'/learned',student,'POST',mark,409)
 for mode,count in [('all',10),('unlearned',10),('learned',0),('stale',1)]:expect(c(sec+'/batches/0?study_mode='+mode)['count']==count,'batch mode '+mode)
 c(sec+'/batches/0?study_mode=bad',code=400);c(sec+'/batches/-1',code=404)
 for size in [10,20,30]:
  c(sec+'/settings',student,'PUT',{'batch_size':size,'study_mode':'all','last_batch_index':0});expect(c(sec,reserve)['settings']['batch_size']==size,'settings shared divisions '+str(size))
 c(sec+'/settings',student,'PUT',{'batch_size':11,'study_mode':'all'},400)
 c(sec+'/settings',student,'PUT',{'batch_size':10,'study_mode':'all','last_batch_index':-1},400)
 c(sec+'/learned',student,'POST',{'card_ref':changed['card_ref'],'content_fingerprint':changed['content_fingerprint']})
 c(sec+'/learned',student,'DELETE',mark);c(sec+'/learned',student,'DELETE',mark)
 expect(c(sec)['stats']['learned']==0,'idempotent unmark')
 # Conditional writes: exactly one concurrent edit wins.
 def edit(answer):return c(f'/themes/{tid}/cards/{second["card_id"]}',editor,'PUT',{'expected_revision':second['revision'],'question':second['question'],'answer':answer},(200,409))
 with ThreadPoolExecutor(2) as pool:out=list(pool.map(edit,['Concurrent A','Concurrent B']))
 expect(sum(isinstance(x,dict) and x.get('card_id')==second['card_id'] for x in out)==1,'concurrent card update has one winner')
 c(f'/themes/{tid}',editor,'PUT',{'expected_revision':999,'name':'stale','direction_id':f['direction']},409)
 tree=c('/tree');expect(any(d['id']==f['direction'] for d in tree['directions']),'organization tree contains permitted direction')
 c(f'/directions/{f["other_direction"]}/sections',code=404)
 sections=c(f'/directions/{f["direction"]}/sections?kind=manual&search={run}&page=99&limit=1')
 expect(sections['pagination']['total']==1 and sections['pagination']['page']==1,'section filters pagination clamps page')
 expect(c(f'/directions/{f["direction"]}/sections?progress=learned&search={run}')['pagination']['total']==0,'unfinished section excluded from learned filter')
 # Student's other organization has its own cards/progress/settings.
 ov=c('/sections/manual/'+str(f['other_theme']),foreign);expect(ov['stats']['learned']==0,'other organization progress isolated')
 expect(all(d['id']!=f['direction'] for d in c('/tree',foreign)['directions']),'organization catalog isolation')
 # Excel errors, warnings, trailing newline edits, ordered transactional commit.
 c('/imports/parse',student,'POST',code=403,files={'file':('x.xlsx',b'x')},data={'direction_id':str(f['direction']),'theme_id':str(tid)})
 for rows in [[['wrong','headers'],['Q','A']],[['Вопрос','Ответ'],['Q','A'],['',''],['Q','A']]]:
  c('/imports/parse',admin,'POST',code=400,files={'file':('x.xlsx',xlsx(rows))},data={'direction_id':str(f['direction']),'theme_id':str(tid)})
 raw=xlsx([['Вопрос','Ответ'],['Q0','Changed\n\nanswer'],['Q0','Changed answer'],['Fix me',''],['Skip me','Answer']])
 imp=c('/imports/parse',admin,'POST',files={'file':('synthetic.xlsx',raw)},data={'direction_id':str(f['direction']),'theme_id':str(tid)})
 sid=imp['session_id'];rows=imp['preview']['cards'];expect(rows[0]['action']=='warning' and len(rows[1]['warnings'])==2 and rows[2]['action']=='error','duplicates are warnings; empty answer blocks commit')
 c('/sessions/'+sid,editor,code=404);c('/sessions/'+sid,other,code=404)
 c('/sessions/'+sid+'/commit',admin,'POST',{'expected_revision':1},400)
 edits=[{'row':row['row'],'question':row['question'],'answer':row['answer'],'skip':False} for row in rows];edits[2]['answer']='One\r\n\r\nTwo\r\n';edits[3]['skip']=True
 imp=c('/sessions/'+sid,admin,'PUT',{'expected_revision':1,'cards':edits});expect(imp['preview']['cards'][2]['answer']=='One\n\nTwo\n','preview preserves trailing Enter and internal blank lines')
 c('/sessions/'+sid,admin,'PUT',{'expected_revision':1,'cards':edits},409)
 with ThreadPoolExecutor(2) as pool:results=list(pool.map(lambda _:c('/sessions/'+sid+'/commit',admin,'POST',{'expected_revision':imp['revision']}),range(2)))
 expect(results[0]==results[1] and results[0]['created']==3 and results[0]['skipped']==1,'concurrent import commits create once')
 now=c(sec);expect(len(now['cards'])==28,'atomic import exact final count');imported=next(x for x in now['cards'] if x['question']=='Fix me');expect(imported['answer']=='One\n\nTwo','committed import trims only outer whitespace')
 collision=c('/imports/parse',admin,'POST',files={'file':('x.xlsx',xlsx([['Вопрос','Ответ'],['Collision','A']]))},data={'direction_id':str(f['direction']),'create_new_theme':'true','new_theme_name':theme['name']})
 expect(collision['preview']['theme_name_collision'],'import new theme name collision reuses theme')
 cres=c('/sessions/'+collision['session_id']+'/commit',admin,'POST',{'expected_revision':1});expect(cres['theme_id']==tid,'collision committed into existing theme')
 # Real tests projections, visibility, versions, removed questions.
 seedtest=api('tests','/v1/tests/'+f['test']+'/content',admin)
 t=api('tests','/v1/tests/divisions/1001/tests',admin,'POST',{'title':'HTTP Cards '+run,'description':'','direction_id':f['direction'],'questions':seedtest['questions']},201,headers={'Idempotency-Key':'cards_http_'+run})
 testid=t['id'];testsec='/sections/test/'+testid
 c(testsec,code=403)
 nowtime=datetime.now(timezone.utc)
 def settings_test(visible=True,published=True,future=False):
  meta=api('tests','/v1/tests/'+testid,admin)
  body={'expected_revision':meta['revision'],'published':published,'start_at':(nowtime-timedelta(days=2)).isoformat(),'end_at':(nowtime+timedelta(days=1) if future else nowtime-timedelta(days=1)).isoformat(),'time_limit_minutes':30,'passing_score':None,'answers_visible':visible}
  return api('tests',f'/v1/tests/divisions/1001/tests/{testid}/settings',admin,'PUT',body)
 settings_test(future=True);c(testsec,code=403) # visible, but cannot leak before submission/end
 settings_test();tv=c(testsec);expect([x['answer'] for x in tv['cards']]==['Первый','Красный; Синий','Вариант 1 / Вариант 2'],'all test question types project with legacy formatting')
 tc=tv['cards'][0];c(testsec+'/learned',student,'POST',{'card_ref':tc['card_ref'],'content_fingerprint':tc['content_fingerprint']})
 expect(c(testsec,reserve)['cards'][0]['status']=='learned','test access union and progress survive division switch')
 oldversion=tv['content_version_id'];content=api('tests','/v1/tests/'+testid+'/content',admin);qs=copy.deepcopy(content['questions']);qs[0]['text']='Changed question'
 api('tests','/v1/tests/'+testid+'/content',admin,'PUT',{'expected_revision':content['revision'],'title':content['title'],'description':content['description'],'direction_id':content['direction_id'],'questions':qs})
 updated=c(testsec);expect(updated['cards'][0]['card_ref']==tc['card_ref'] and updated['cards'][0]['status']=='answer_changed' and updated['content_version_id']!=oldversion,'live version change stable identity stale progress')
 c(testsec+'/learned',student,'POST',{'card_ref':tc['card_ref'],'content_fingerprint':tc['content_fingerprint']},409)
 content=api('tests','/v1/tests/'+testid+'/content',admin);api('tests','/v1/tests/'+testid+'/content',admin,'PUT',{'expected_revision':content['revision'],'title':content['title'],'description':content['description'],'direction_id':content['direction_id'],'questions':qs[1:]})
 expect(c(testsec)['stats']['total']==2,'deleted question absent from projection and denominator')
 c(testsec+'/learned',student,'POST',{'card_ref':tc['card_ref'],'content_fingerprint':tc['content_fingerprint']},404)
 settings_test(False);c(testsec,code=403);c(testsec,admin,code=403)
 expect(not any(n['refId']==testid for d in c('/tree')['directions'] for n in d['sections']),'hidden answer test absent from student tree')
 cat=c('/admin/catalog',viewer);hidden=next(n for d in cat['directions'] for n in d['sections'] if n.get('test_id')==testid)
 expect(hidden['visible']==False and hidden['cards_count']==2 and 'questions' not in hidden,'admin catalog shows hidden metadata without keys')
 c(testsec,foreign,code=404)
 request('tests','/internal/tests/cards/catalog',student,code=401)
 key=settings()['AUTH_INTERNAL_KEY'];request('tests','/internal/tests/cards/catalog',headers={'X-Service-Key':key},code=401)
 settings_test(True,False);c(testsec,code=403);settings_test()
 # Transform snapshot remains editable in tests, full multiline answer is one text alias.
 tr=c('/transforms',editor,'POST',{'theme_id':tid,'card_ids':[imported['card_id'],first['card_id']]});trid=tr['session_id']
 c('/transforms',editor,'POST',{'theme_id':tid,'card_ids':[first['card_id'],first['card_id']]},400)
 with ThreadPoolExecutor(2) as pool:trs=list(pool.map(lambda _:c('/sessions/'+trid+'/commit',editor,'POST',{'expected_revision':1}),range(2)))
 expect(trs[0]==trs[1],'concurrent transform retry returns same draft')
 draft=api('tests','/v1/tests/divisions/1001/drafts/'+trs[0]['draft_id'],editor)
 expect(draft['state']=='draft' and draft['questions'][0]['correct_answers']==['One\n\nTwo'] and draft['questions'][0]['points']==1,'real tests draft preserves full answer and weight')
 expect([p['x'] for p in draft['layout']]==[120,480],'transform selection order and three-column layout')
 # Rehearse crash after remote success before the local commit by restoring only
 # this synthetic session to its durable pre-commit state.
 conn=mysql_connection(settings())
 try:
  cur=conn.cursor();cur.execute('SELECT DATABASE()');expect(cur.fetchone()[0]=='cpm_dev','recovery injection target is dev')
  cur.execute("UPDATE cards_sessions_v1 SET state='preview',result=NULL WHERE id=%s AND account_id=%s AND organization_id=1001",(trid,f['editor']));expect(cur.rowcount==1,'only own transform recovery state changed');conn.commit()
 finally:conn.close()
 expect(c('/sessions/'+trid+'/commit',editor,'POST',{'expected_revision':1})==trs[0],'retry after remote-success local-commit loss reuses same tests draft')
 # Expired preview and archived direction are denied on every method.
 exp=c('/imports/parse',admin,'POST',files={'file':('x.xlsx',xlsx([['Вопрос','Ответ'],['Expired','A']]))},data={'direction_id':str(f['direction']),'theme_id':str(tid)})
 conn=mysql_connection(settings())
 try:
  cur=conn.cursor();cur.execute("UPDATE cards_sessions_v1 SET expires_at=UTC_TIMESTAMP()-INTERVAL 1 HOUR WHERE id=%s AND organization_id=1001",(exp['session_id'],));conn.commit()
 finally:conn.close()
 c('/sessions/'+exp['session_id'],admin,code=404);c('/sessions/'+exp['session_id']+'/commit',admin,'POST',{'expected_revision':1},404)
 direction=api('core','/v1/core/directions',admin,'POST',{'name':'DEV Cards archive '+run},201)['id']
 at=c('/themes',admin,'POST',{'name':'Archived direction '+run,'direction_id':direction,'questions':[{'question':'Q','answer':'A'}]})
 api('core','/v1/core/directions/'+str(direction),admin,'DELETE')
 c('/sections/manual/'+str(at['id']),code=404);c('/themes/'+str(at['id'])+'/cards',admin,'POST',{'question':'Q','answer':'A'},404)
 # Revoke an isolated run-specific role: shared fixtures may serve other chats.
 role_name='DEV Cards revoke '+run
 perms={'train':{'view':True,'edit':False}}
 revoke_role=api('core','/v1/core/roles',admin,'POST',{'name':role_name,'permissions':perms},201)['id']
 revoke_login='dev_cards_revoke_'+run;revoke_password=secrets.token_urlsafe(24)
 api('core','/v1/core/divisions/1001/users',admin,'POST',{'role':'staff_admin','full_name':role_name,'login':revoke_login,'password':revoke_password,'tg_name':'','role_id':revoke_role},201)
 revoke_viewer=login(revoke_login,revoke_password)
 c('/admin/catalog',revoke_viewer)
 try:
  api('core','/v1/core/roles/'+str(revoke_role),admin,'PUT',{'name':role_name,'permissions':{}})
  c('/admin/catalog',revoke_viewer,code=403)
  restricted=login(revoke_login,revoke_password);c('/admin/catalog',restricted,code=403)
 finally:
  api('core','/v1/core/roles/'+str(revoke_role),admin,'PUT',{'name':role_name,'permissions':perms})
 # Live auth revocation is effective immediately for cards.
 revoke=login('dev_cards_student',f['password']);request('auth','/v1/auth/logout',revoke,'POST',{})
 c(sec,revoke,code=401)
 # Delete progress and cards atomically, enforce read-only staff.
 last=c(sec)['cards'][2];c(sec+'/learned',student,'POST',{'card_ref':last['card_ref'],'content_fingerprint':last['content_fingerprint']})
 c(f'/themes/{tid}/cards/{last["card_id"]}',viewer,'DELETE',{'expected_revision':last['revision']},403)
 c(f'/themes/{tid}/cards/{last["card_id"]}',editor,'DELETE',{'expected_revision':last['revision']});expect(c(sec)['stats']['learned']==0,'deleted manual card removed from progress')
 outage_test(student,sec,testsec)
 c(f'/themes/{tid}',editor,'DELETE',{'expected_revision':theme['revision']});c(sec,code=404)
 meta=api('tests','/v1/tests/'+testid,admin);api('tests','/v1/tests/'+testid+'/archive',admin,'PUT',{'expected_revision':meta['revision'],'is_archived':True})
 c(testsec,code=403)
 return {'status':'passed','checks':len(checks),'details':checks,'real_dependencies':['Docker cards','auth','core','tests'],'synthetic_only':True}

def outage_test(student,sec,testsec):
 # Isolated same-image container. Shared tests/core/auth are not stopped.
 docker='/Applications/Docker.app/Contents/Resources/bin/docker';name='cpm-cards-http-outage-'+run
 env=settings();p=ROOT/'dev/local/cards-outage.env'
 values={k:env[k] for k in ['MYSQL_USER','MYSQL_PASSWORD','AUTH_INTERNAL_KEY']};values.update(MYSQL_HOST='mysql',MYSQL_PORT='3306',MYSQL_DATABASE='cpm_dev',CARDS_LISTEN_ADDR='0.0.0.0:8008',CARDS_AUTH_URL='http://auth:8002',CARDS_CORE_URL='http://core:8003',CARDS_TESTS_URL='http://tests:1')
 p.write_text('\n'.join(f'{k}={v}' for k,v in values.items())+'\n');p.chmod(0o600)
 original=HOSTS['cards']
 try:
  subprocess.run([docker,'run','--rm','-d','--name',name,'--network','cpm-dev_default','--env-file',str(p),'-p','127.0.0.1:18008:8008','--read-only','--cap-drop','ALL','--memory','512m','cpm-cards:dev'],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.PIPE)
  HOSTS['cards']='http://127.0.0.1:18008'
  for _ in range(30):
   try:request('cards','/health/live');break
   except (OSError,urllib.error.URLError):time.sleep(.2)
  request('cards','/health/ready',code=503)
  api('cards','/v1/cards'+sec,student)
  api('cards','/v1/cards'+testsec,student,code=503)
  api('cards','/v1/cards/tree',student,code=503)
  checks.append('Isolated Docker same-image tests outage: manual works; test projection/tree/readiness return 503; shared services untouched')
 finally:
  HOSTS['cards']=original
  subprocess.run([docker,'rm','-f',name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL);p.unlink(missing_ok=True)

if __name__=='__main__':
 report=ROOT/'dev/local/cards-http-report.json'
 try:
  result=main();save(report,result);print(f'Cards real HTTP acceptance: {result["checks"]} checks passed. Report: dev/local/cards-http-report.json')
 except Exception as e:
  save(report,{'status':'failed','checks':len(checks),'details':checks,'error_type':type(e).__name__,'error':str(e) if isinstance(e,AssertionError) else 'See local exception; credentials omitted'})
  raise
