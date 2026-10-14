#!/usr/bin/env python3
"""Check every app readiness, then BFF authorization rejection and exact CORS."""
import argparse,json,subprocess,time,urllib.request,urllib.error
from pathlib import Path

def container(service):
 ids=subprocess.check_output(['docker','ps','--filter','label=com.docker.compose.project=cpm-prod','--filter','label=com.docker.compose.service='+service,'--format','{{.ID}}'],text=True).split()
 if len(ids)!=1:raise ValueError('missing container '+service)
 return json.loads(subprocess.check_output(['docker','inspect',ids[0]],text=True))[0]

def request(url,method='GET',headers=None):
 try:
  with urllib.request.urlopen(urllib.request.Request(url,method=method,headers=headers or {}),timeout=8) as r:return r.status,dict(r.headers),r.read(1024)
 except urllib.error.HTTPError as e:return e.code,dict(e.headers),b''

def ready(ports):
 for name,port in ports.items():
  c=container(name);ip=c['NetworkSettings']['Networks']['cpm-prod-backend']['IPAddress'];path='/readyz' if name=='file-work-service' else '/health/ready'
  if request('http://'+ip+':'+str(port)+path)[0]!=200:raise ValueError('not ready '+name)
 bff=container('bff');ip=bff['NetworkSettings']['Networks']['cpm-prod-backend']['IPAddress'];base='http://'+ip+':8001'
 for origin in ['https://cpm-lms.ru','http://localhost:3000','http://185.239.51.138','https://back1.cpm-lms.ru']:
  status,headers,_=request(base+'/bff/auth/login','OPTIONS',{'Origin':origin,'Access-Control-Request-Method':'POST','Access-Control-Request-Headers':'Content-Type,X-CPM-Request'})
  if status!=204 or headers.get('Access-Control-Allow-Origin')!=origin:raise ValueError('BFF CORS failed')
 status,_,_=request(base+'/bff/core/students',headers={'X-CPM-Request':'1'})
 if status not in [401,403]:raise ValueError('BFF unauthorized smoke failed')

if __name__=='__main__':
 p=argparse.ArgumentParser();p.add_argument('--timeout',type=int,default=240);p.add_argument('--ports',default=str(Path(__file__).resolve().parents[1]/'service-ports.json'));a=p.parse_args();ports=json.loads(Path(a.ports).read_text());end=time.monotonic()+a.timeout;failure=''
 while time.monotonic()<end:
  try:ready(ports);print('PASS: twelve readiness checks, BFF CORS and unauthorized access');break
  except (ValueError,subprocess.SubprocessError,OSError) as e:failure=type(e).__name__+': '+str(e) if isinstance(e,ValueError) else type(e).__name__;time.sleep(3)
 else:raise SystemExit('readiness timeout: '+failure)
