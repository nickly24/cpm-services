#!/usr/bin/env python3
"""Only liveness failures restart a running app; readiness/DB outage never does."""
import json,subprocess,time,urllib.request
from pathlib import Path
statefile=Path('/var/lib/cpm/watchdog.json');statefile.parent.mkdir(parents=True,exist_ok=True)
try:state=json.loads(statefile.read_text())
except (OSError,ValueError):state={}
ids=subprocess.check_output(['docker','ps','--filter','label=cpm.watchdog=true','--format','{{.ID}}'],text=True).split()
now=time.time()
for ident in ids:
 c=json.loads(subprocess.check_output(['docker','inspect',ident],text=True))[0];labels=c['Config']['Labels'];name=labels['com.docker.compose.service'];rec=state.setdefault(name,{'failures':0,'restarts':[]})
 if now-__import__('datetime').datetime.fromisoformat(c['State']['StartedAt'].replace('Z','+00:00')).timestamp()<120:continue
 ip=c['NetworkSettings']['Networks']['cpm-prod-backend']['IPAddress']
 try:
  with urllib.request.urlopen('http://'+ip+':'+labels['cpm.port']+labels['cpm.liveness'],timeout=5) as r:healthy=r.status==200
 except OSError:healthy=False
 rec['failures']=0 if healthy else rec['failures']+1;rec['restarts']=[t for t in rec['restarts'] if now-t<900]
 if rec['failures']>=3 and len(rec['restarts'])<3:
  subprocess.run(['docker','restart','--time','60',ident],check=True,stdout=subprocess.DEVNULL);rec['restarts'].append(now);rec['failures']=0;print('Liveness restart: '+name)
 elif rec['failures']>=3:print('Liveness restart limit reached: '+name)
statefile.write_text(json.dumps(state))
