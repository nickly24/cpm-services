"""Produce the versioned public contract embedded in the Go executable."""
import json
from pathlib import Path

def ref(name):return {'$ref':'#/components/schemas/'+name}
def obj(properties,required=None):return {'type':'object','properties':properties,'required':required or list(properties),'additionalProperties':False}
ID={'type':'integer','minimum':1}
NULL_ID={'type':['integer','null'],'minimum':1}
TEXT={'type':'string'}
BOOL={'type':'boolean'}
schemas={
 'Permission':obj({'view':BOOL,'edit':BOOL}),
 'Login':obj({'login':{'type':'string','minLength':1,'maxLength':50},'password':{'type':'string','minLength':1,'maxLength':1024},'organization_id':ID,'division_id':ID},['login','password']),
 'Context':obj({'organization_id':ID,'division_id':NULL_ID},['organization_id']),
 'Refresh':obj({'refresh_token':{'type':'string','minLength':64,'maxLength':64}}),
 'Error':obj({'status':{'const':False},'error':TEXT}),
 'Status':obj({'status':BOOL}),
 'PermissionMap':{'type':'object','additionalProperties':ref('Permission')},
 'Division':obj({'id':ID,'name':TEXT,'group_id':NULL_ID,'role_id':NULL_ID,'role_name':TEXT,'permissions':ref('PermissionMap')},['id','name','group_id','role_id','permissions']),
 'Organization':obj({'id':ID,'name':TEXT,'divisions':{'type':'array','items':ref('Division')}}),
 'User':obj({'id':ID,'account_id':ID,'role':{'enum':['student','proctor','examinator','supervisor','admin','staff_admin']},'full_name':TEXT,'group_id':NULL_ID,'organization_id':ID,'division_id':NULL_ID,'division_ids':{'type':'array','items':ID},'organization_admin':BOOL,'role_id':NULL_ID,'role_name':TEXT,'permissions':ref('PermissionMap')},['id','account_id','role','full_name','group_id','organization_id','division_id','division_ids','organization_admin','role_id','permissions']),
}
orgs={'type':'array','items':ref('Organization')}
schemas['TokenResponse']=obj({'status':{'const':True},'token':TEXT,'access_token':TEXT,'token_type':{'const':'Bearer'},'expires_in':{'type':'integer'},'refresh_token':TEXT,'user':ref('User'),'organizations':orgs},['status','token','access_token','token_type','expires_in','user','organizations'])
schemas['UserResponse']=obj({'status':{'const':True},'active':{'const':True},'user':ref('User'),'organizations':orgs,'role':TEXT,'entity_id':ID,'full_name':TEXT,'group_id':NULL_ID,'organization_id':ID,'division_id':NULL_ID,'division_ids':{'type':'array','items':ID},'role_id':NULL_ID,'role_name':TEXT,'permissions':ref('PermissionMap')})
schemas['JWKS']=obj({'keys':{'type':'array','items':obj({'kty':{'const':'RSA'},'use':{'const':'sig'},'alg':{'const':'RS256'},'kid':TEXT,'n':TEXT,'e':TEXT})}})
paths={}
def endpoint(path,method,summary,response,body=None,protected=False,alias=False):
    responses={'200':{'description':'Success','content':{'application/json':{'schema':ref(response)}}}}
    for status in [400,401,403,415,503]:responses[str(status)]={'description':'Request failed','content':{'application/json':{'schema':ref('Error')}}}
    op={'summary':summary,'responses':responses,'security':[{'bearerAuth':[]}] if protected else []}
    if body:op['requestBody']={'required':True,'content':{'application/json':{'schema':ref(body)}}}
    if alias:op['deprecated']=True
    paths.setdefault(path,{})[method]=op

endpoint('/v1/auth/login','post','Login and choose an available context','TokenResponse','Login')
endpoint('/v1/auth/refresh','post','Rotate the refresh token','TokenResponse','Refresh')
endpoint('/v1/auth/context','post','Select organization and division','TokenResponse','Context',True)
paths['/v1/auth/context']['post']['description'] = 'Each successful selection increments session context_version, including selecting the current context. All previous access generations remain invalid after returning to a previous context. Concurrent selections with the same access have one winner; the other returns 401. Refresh token is preserved and is not returned here.'
endpoint('/v1/auth/me','get','Get current identity and available contexts','UserResponse',protected=True)
endpoint('/v1/auth/introspect','post','Validate session and return current permissions','UserResponse',protected=True)
endpoint('/v1/auth/logout','post','Revoke current session','Status',protected=True)
endpoint('/.well-known/jwks.json','get','Public JWT verification key','JWKS')
endpoint('/health/live','get','Process liveness','Status')
endpoint('/health/ready','get','Database readiness','Status')
endpoint('/api/auth','post','Legacy login address','TokenResponse','Login',alias=True)
endpoint('/api/aun','post','Legacy identity address','UserResponse',protected=True,alias=True)
endpoint('/api/logout','post','Legacy logout address','Status',protected=True,alias=True)
document={'openapi':'3.1.0','info':{'title':'CPM Auth','version':'0.1.0','description':'Go auth service. JWT RS256, organization/division contexts. No rate limits. Cookie and CORS handling belongs to the future BFF.'},'servers':[{'url':'http://127.0.0.1:8002','description':'Local dev only'}],'paths':paths,'components':{'securitySchemes':{'bearerAuth':{'type':'http','scheme':'bearer','bearerFormat':'JWT'}},'schemas':schemas}}
from operation_openapi import extend_operations
extend_operations(document)
target=Path(__file__).resolve().parents[1]/'internal/auth/openapi.json'
target.write_text(json.dumps(document,ensure_ascii=False,indent=2)+'\n')
print('OpenAPI contract written.')
