"""Merge only the new attendance contract; preserve all neighbouring BFF paths."""
import copy,json,urllib.request
from pathlib import Path
ROOT=Path(__file__).resolve().parents[3]
with urllib.request.urlopen('http://127.0.0.1:8013/openapi.json',timeout=10) as r:owner=json.load(r)
path=ROOT/'services/bff/internal/bff/openapi.json'
spec=json.loads(path.read_text())
for key in list(spec['paths']):
    if key.startswith('/bff/attendance/'):del spec['paths'][key]
for source,methods in owner['paths'].items():
    if not source.startswith('/v1/attendance/'):continue
    target=source.replace('/v1/attendance/','/bff/attendance/')
    mapped=copy.deepcopy(methods)
    for operation in mapped.values():
        operation['security']=[{'sessionCookie':[]}]
        operation['parameters']+= [{'name':'X-CPM-Request','in':'header','required':True,'schema':{'const':'1'}},{'name':'X-CPM-Context','in':'header','required':True,'schema':{'type':'string'}}]
        operation['description']='Organization comes from the live BFF session. Calendar filters and permitted division IDs do not switch auth context. Owner authorizes every object. Mutations require exact Origin and preserve CAS/Idempotency-Key without retry.'
    if '/images/{image}' in target:
        mapped['get']['responses']['200']={'description':'Protected image as JSON after opaque-cookie/context checks','content':{'application/json':{'schema':{'type':'object','required':['status','data'],'properties':{'status':{'const':True},'data':{'type':'object','required':['mime','data'],'properties':{'mime':{'enum':['image/png','image/jpeg']},'data':{'type':'string','contentEncoding':'base64'}}}}}}}}
    spec['paths'][target]=mapped
if 'attendance' not in spec['info']['title'].lower():spec['info']['title']+=' and attendance'
path.write_text(json.dumps(spec,ensure_ascii=False,indent=2)+'\n')
print('Attendance operations merged:',sum(len(m) for p,m in spec['paths'].items() if p.startswith('/bff/attendance/')))
