"""Refresh the embedded local BFF contract from locally running owner APIs."""
import copy,json,re,urllib.request,argparse
from pathlib import Path
ROOT=Path(__file__).resolve().parents[3]
file=ROOT/'services/bff/internal/bff/openapi.json'
spec=json.loads(file.read_text())
schemas=spec.setdefault('components',{}).setdefault('schemas',{})
parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument('--owners',nargs='+',choices=['tests','core','exams','ratings','cards','uploads'],default=['tests','core','exams','ratings','cards','uploads'])
owners=set(parser.parse_args().owners)
def read(port):
 with urllib.request.urlopen(f'http://127.0.0.1:{port}/openapi.json',timeout=10) as response:return json.load(response)
def operation(value, omitted=('division', 'target_division_id')):
 value=copy.deepcopy(value)
 value['security']=[{'sessionCookie':[]}]
 value['parameters']=[p for p in value.get('parameters',[]) if p['name'] not in omitted]
 value['parameters'] += [{'name':'X-CPM-Request','in':'header','required':True,'schema':{'const':'1'}},{'name':'X-CPM-Context','in':'header','required':True,'schema':{'type':'string'}}]
 value['description']=value.get('description','')+' Организация и подразделение берутся из BFF session. Изменения требуют Origin http://127.0.0.1:3001; устаревший контекст получает409.'
 return value
if 'tests' in owners:
 for path in list(spec['paths']):
  if path.startswith('/bff/tests/'):del spec['paths'][path]
 for path,methods in read(8004)['paths'].items():
  # Owner import DTOs and binary templates are exposed through Uploads only.
  if not path.startswith('/v1/tests/') or '/imports/' in path:continue
  browser=path.replace('/v1/tests','/bff/tests')
  browser=browser.replace('/divisions/{division}/tests/{test}','/{test}')
  browser=browser.replace('/divisions/{division}/tests','/catalog')
  browser=browser.replace('/divisions/{division}','')
  for method,value in methods.items():
   target=browser
   if path=='/v1/tests/divisions/{division}/tests/{test}' and method=='delete':target+='/attachment'
   spec['paths'].setdefault(target,{})[method]=operation(value)
if 'core' in owners:
 core=read(8003)
 core_public={
  '/me':['get'], '/organization':['get','put'], '/organization/users':['get'], '/organization/users/{user}/memberships':['get'],
  '/audit':['get'], '/directions':['get','post'], '/directions/{direction}':['put','delete'],
  '/divisions':['get','post'], '/divisions/{division}':['put','delete'], '/roles':['get','post'], '/roles/{role}':['put','delete'],
  '/organization/test-settings':['get','put'], '/schools':['get','post'], '/schools/{school}':['get','put'],
  '/divisions/{division}/users':['get','post'], '/divisions/{division}/users/{user}':['get','put'],
  '/divisions/{division}/users/{user}/credentials':['put'], '/divisions/{division}/users/{user}/active':['put'],
  '/divisions/{division}/memberships/{user}':['put','delete'],
  '/divisions/{division}/groups':['get','post'], '/divisions/{division}/groups/{group}':['put','delete'],
  '/divisions/{division}/groups/{group}/members':['get','post'],
  '/divisions/{division}/groups/{group}/members/{user}':['delete'],
  '/divisions/{division}/students':['get'], '/divisions/{division}/dashboard':['get'],
  '/divisions/{division}/reports/tests':['get'], '/divisions/{division}/reports/exams':['get'],
  '/divisions/{division}/reports/attendance':['get'], '/divisions/{division}/attendance-report':['get'],
 }
 for path in list(spec['paths']):
  if path.startswith('/bff/core/'):del spec['paths'][path]
 for tail, allowed in core_public.items():
  path='/v1/core'+tail
  selected=tail.startswith('/divisions/{division}/')
  browser='/bff/core'+(tail.removeprefix('/divisions/{division}') if selected else tail)
  methods={}
  for method in allowed:
   value=operation(core['paths'][path][method],omitted=('division',) if selected else ())
   queries=set()
   leaf=browser.removeprefix('/bff/core/')
   if method=='get':
    if leaf in {'users','students','groups','schools','organization/users'}:queries={'limit','offset','q'}
    if leaf=='organization/users':queries.add('role')
    if leaf=='audit':queries={'limit','offset'}
    if leaf=='users':queries|={'role','unassigned'}
    if leaf=='students':queries.add('unassigned')
    if leaf.startswith('groups/') and leaf.endswith('/members'):queries={'limit','offset','q'}
    if leaf.startswith('reports/') or leaf=='attendance-report':queries={'date_from','date_to'}
    if leaf=='attendance-report':queries.add('account_id')
   value['parameters']=[p for p in value['parameters'] if p.get('in')!='query' or p['name'] in queries]
   methods[method]=value
  spec['paths'][browser]=methods
 # Explicit membership targets are owner-authorized within the actor organization.
 for tail,allowed in {'/divisions/{division}/users':['get'],'/divisions/{division}/memberships/{user}':['put','delete']}.items():
  methods={}
  for method in allowed:
   value=operation(core['paths']['/v1/core'+tail][method],omitted=())
   queries={'limit','offset','q','role','unassigned'} if method=='get' else set()
   value['parameters']=[p for p in value['parameters'] if p.get('in')!='query' or p['name'] in queries]
   value['description']+=' Целевое подразделение в path проверяет Core: полный администратор только своей организации; делегированный — только выбранного кабинета.'
   methods[method]=value
  spec['paths']['/bff/core'+tail]=methods
if 'exams' in owners:
 # Exams use referenced DTO schemas; namespace them to avoid collisions with other owners.
 exams=read(8010)
 def exam_refs(value):
  if isinstance(value,dict):
   return {k: ('#/components/schemas/Exams_'+v.split('/')[-1] if k=='$ref' and isinstance(v,str) and v.startswith('#/components/schemas/') else exam_refs(v)) for k,v in value.items()}
  if isinstance(value,list):return [exam_refs(v) for v in value]
  return value
 for key in list(schemas):
  if key.startswith('Exams_'):del schemas[key]
 for key,value in exams.get('components',{}).get('schemas',{}).items():schemas['Exams_'+key]=exam_refs(value)
 for path in list(spec['paths']):
  if path.startswith('/bff/exams'):del spec['paths'][path]
 for path,methods in exams['paths'].items():
  if not path.startswith('/v1/exams'):continue
  spec['paths'][path.replace('/v1/exams','/bff/exams')]={m:operation(exam_refs(v), omitted=()) for m,v in methods.items()}
 spec['paths']['/bff/exams/capabilities']={'get':operation({'summary':'Capabilities of the current exam cabinet','responses':{'200':{'description':'Live auth role/permission projection'}}})}
if 'ratings' in owners:
 ratings=read(8012)
 def rating_refs(value):
  if isinstance(value,dict):
   return {k: ('#/components/schemas/Ratings_'+v.split('/')[-1] if k=='$ref' and isinstance(v,str) and v.startswith('#/components/schemas/') else rating_refs(v)) for k,v in value.items()}
  if isinstance(value,list):return [rating_refs(v) for v in value]
  return value
 for key in list(schemas):
  if key.startswith('Ratings_'):del schemas[key]
 for key,value in ratings.get('components',{}).get('schemas',{}).items():schemas['Ratings_'+key]=rating_refs(value)
 for path in list(spec['paths']):
  if path.startswith('/bff/ratings/'):del spec['paths'][path]
 for path,methods in ratings['paths'].items():
  if not path.startswith('/v1/ratings/divisions/{division}/'):continue
  browser=path.replace('/v1/ratings/divisions/{division}', '/bff/ratings')
  spec['paths'][browser]={m:operation(rating_refs(v), omitted=('division',)) for m,v in methods.items()}
if 'cards' in owners:
 cards=read(8008)
 def card_refs(value):
  if isinstance(value,dict):
   return {k: ('#/components/schemas/Cards_'+v.split('/')[-1] if k=='$ref' and isinstance(v,str) and v.startswith('#/components/schemas/') else card_refs(v)) for k,v in value.items()}
  if isinstance(value,list):return [card_refs(v) for v in value]
  return value
 for key in list(schemas):
  if key.startswith('Cards_'):del schemas[key]
 for key,value in cards.get('components',{}).get('schemas',{}).items():schemas['Cards_'+key]=card_refs(value)
 for path in list(spec['paths']):
  if path.startswith('/bff/cards/'):del spec['paths'][path]
 for path,methods in cards['paths'].items():
  if not path.startswith('/v1/cards/') or '/imports/' in path or '/receipts/' in path or path.endswith('/source'):continue
  spec['paths'][path.replace('/v1/cards','/bff/cards')]={m:operation(card_refs(v),omitted=()) for m,v in methods.items()}
if 'uploads' in owners:
 uploads=read(8014)
 def upload_refs(value):
  if isinstance(value,dict):
   return {k: ('#/components/schemas/Uploads_'+v.split('/')[-1] if k=='$ref' and isinstance(v,str) and v.startswith('#/components/schemas/') else upload_refs(v)) for k,v in value.items()}
  if isinstance(value,list):return [upload_refs(v) for v in value]
  return value
 for key in list(schemas):
  if key.startswith('Uploads_'):del schemas[key]
 for key,value in uploads.get('components',{}).get('schemas',{}).items():schemas['Uploads_'+key]=upload_refs(value)
 for path in list(spec['paths']):
  if path.startswith('/bff/uploads/'):del spec['paths'][path]
 for path,methods in uploads['paths'].items():
  if not path.startswith('/v1/uploads/divisions/{division}/'):continue
  browser=path.replace('/v1/uploads/divisions/{division}','/bff/uploads')
  spec['paths'][browser]={m:operation(upload_refs(v),omitted=('division',)) for m,v in methods.items()}
spec['info']['title']='Local CPM BFF: auth, core, homework, tests, exams, ratings, cards and uploads'
# A retained business path parameter (e.g. target division on disconnect) is
# different from an omitted selected-cabinet parameter. Validate the final paths.
for path, methods in spec['paths'].items():
 for method, value in methods.items():
  if method not in {'get','post','put','patch','delete'}:continue
  expected=set(re.findall(r'\{([^}]+)\}',path))
  declared={p['name'] for p in value.get('parameters',[]) if p.get('in')=='path' and p.get('required') is True}
  if expected != declared:raise ValueError(f'{method} {path}: invalid path parameters {expected ^ declared}')
file.write_text(json.dumps(spec,ensure_ascii=False,indent=2)+'\n')
print('Embedded BFF operations:',sum(len(methods) for methods in spec['paths'].values()))
