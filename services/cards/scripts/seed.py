"""Repeatable synthetic cards fixtures; own credentials/records only."""
import io,json,secrets
from datetime import datetime,timedelta,timezone
from support import *

def main():
 settings() # Fixed DB name/ports before any write, including HTTP seed.
 marker=ROOT/'dev/local/cards-fixtures.json'
 f=json.loads(marker.read_text()) if marker.exists() else {'password':secrets.token_urlsafe(24)}
 save(marker,f)
 adminpw=json.loads((ROOT/'dev/local/accounts.json').read_text())['password']
 admin=login('dev_admin',adminpw);other=login('dev_admin_math',adminpw,2001,2001)
 for key,token,name in [('direction',admin,'DEV Cards synthetic'),('other_direction',other,'DEV Cards other organization')]:
  if key not in f:
   existing=api('core','/v1/core/directions',token)
   found=next((x for x in existing if x['name']==name),None)
   f[key]=(found or api('core','/v1/core/directions',token,'POST',{'name':name},201))['id'];save(marker,f)
 for key,permission in [('viewer_role',{'train':{'view':True,'edit':False}}),('editor_role',{'train':{'view':True,'edit':True},'tests':{'view':True,'edit':True}})]:
  if key not in f:f[key]=api('core','/v1/core/roles',admin,'POST',{'name':'DEV Cards '+key,'permissions':permission},201)['id'];save(marker,f)
 for key,role,roleid in [('student','student',None),('viewer','staff_admin',f['viewer_role']),('editor','staff_admin',f['editor_role'])]:
  if key not in f:
   body={'role':role,'full_name':'DEV Cards '+key,'login':'dev_cards_'+key,'password':f['password'],'tg_name':''}
   if role=='student':body['class']=10
   else:body['role_id']=roleid
   f[key]=api('core','/v1/core/divisions/1001/users',admin,'POST',body,201)['account_id'];save(marker,f)
 # Core's cross-organization enrollment API is intentionally unavailable.
 # Only this dedicated synthetic student gets additional dev memberships.
 conn=mysql_connection(settings())
 try:
  c=conn.cursor();c.execute('SELECT DATABASE()');assert c.fetchone()[0]=='cpm_dev'
  c.execute('SELECT username FROM auth_users WHERE id=%s',(f['student'],));assert c.fetchone()[0]=='dev_cards_student'
  c.execute('INSERT IGNORE INTO auth_organization_access(user_id,organization_id) VALUES(%s,2001)',(f['student'],))
  for org,div in [(1001,1002),(2001,2001)]:c.execute('INSERT IGNORE INTO auth_division_access(user_id,organization_id,division_id) VALUES(%s,%s,%s)',(f['student'],org,div))
  conn.commit()
 finally:conn.close()
 for key,token,direction in [('theme',admin,f['direction']),('other_theme',other,f['other_direction'])]:
  if key not in f:
   qs=[{'question':f'Синтетический вопрос {i}','answer':f'Ответ {i}'} for i in range(1,26)]
   f[key]=api('cards','/v1/cards/themes',token,'POST',{'name':'DEV Cards '+key,'direction_id':direction,'questions':qs})['id'];save(marker,f)
 qs=[{'question_id':1,'type':'single','text':'Один вариант','points':1,'answers':[{'id':'a','text':'Первый','is_correct':True},{'id':'b','text':'Второй','is_correct':False}],'correct_answers':[]},
 {'question_id':2,'type':'multiple','text':'Несколько вариантов','points':1,'answers':[{'id':'a','text':'Красный','is_correct':True},{'id':'b','text':'Синий','is_correct':True}],'correct_answers':[]},
 {'question_id':3,'type':'text','text':'Текстовый ответ','points':1,'answers':[],'correct_answers':['Вариант 1','Вариант 2']}]
 for key,visible in [('test',True),('hidden_test',False)]:
  if key not in f:
   t=api('tests','/v1/tests/divisions/1001/tests',admin,'POST',{'title':'DEV Cards '+key,'description':'Synthetic fixture','direction_id':f['direction'],'questions':qs},(200,201),headers={'Idempotency-Key':'dev_cards_seed_v1_'+key})
   f[key]=t['id'];save(marker,f)
  t=api('tests','/v1/tests/'+f[key],admin)
  now=datetime.now(timezone.utc)
  cfg={'expected_revision':t['revision'],'published':True,'start_at':(now-timedelta(days=3)).isoformat(),'end_at':(now-timedelta(days=1)).isoformat(),'time_limit_minutes':30,'passing_score':None,'answers_visible':visible}
  api('tests',f'/v1/tests/divisions/1001/tests/{f[key]}/settings',admin,'PUT',cfg)
 student=login('dev_cards_student',f['password'])
 view=api('cards',f'/v1/cards/sections/manual/{f["theme"]}',student)
 if 'progress_seeded' not in f:
  for c in view['cards'][:2]:api('cards',f'/v1/cards/sections/manual/{f["theme"]}/learned',student,'POST',{'card_ref':c['card_ref'],'content_fingerprint':c['content_fingerprint']})
  c=view['cards'][1];api('cards',f'/v1/cards/themes/{f["theme"]}/cards/{c["card_id"]}',admin,'PUT',{'expected_revision':c['revision'],'question':c['question'],'answer':'Изменённый синтетический ответ'})
  f['progress_seeded']=True;save(marker,f)
 if 'import_session' not in f:
  content=xlsx([['Вопрос','Ответ'],['Импортированный вопрос','Многострочный\n\nответ'],['Нужно исправить','']])
  ss=api('cards','/v1/cards/imports/parse',admin,'POST',files={'file':('synthetic.xlsx',content)},data={'direction_id':str(f['direction']),'theme_id':str(f['theme'])})
  f['import_session']=ss['session_id'];save(marker,f)
 print('Repeatable cards fixtures ready: two organizations, multiple divisions, three dedicated accounts, manual/test cards, progress and import preview. Credentials only in dev/local/cards-fixtures.json.')
if __name__=='__main__':main()
