"""Rebuild against existing local runtime images when the Docker registry is down."""
import os,subprocess,sys
from pathlib import Path
ROOT=Path(__file__).resolve().parents[3]
DOCKER='/Applications/Docker.app/Contents/Resources/bin/docker'
GO=str(Path.home()/'.local/bin/go')
def main():
 for service in (sys.argv[1:] or ['core','tests','cards']):
  assert service in ['core','tests','cards']
  arch=subprocess.check_output([DOCKER,'image','inspect','cpm-'+service+':dev','--format','{{.Architecture}}'],text=True).strip()
  assert arch in ['arm64','amd64']
  target=ROOT/'dev/local/cards-offline'/service;target.mkdir(parents=True,exist_ok=True)
  env=dict(os.environ,GOOS='linux',GOARCH=arch,CGO_ENABLED='0')
  subprocess.run([GO,'build','-trimpath','-ldflags=-s -w','-o',str(target/service),'./cmd/'+service],cwd=ROOT/'services'/service,env=env,check=True)
  (target/'Dockerfile').write_text(f'FROM cpm-{service}:dev\nCOPY {service} /{service}\n')
  subprocess.run([DOCKER,'build','--pull=false','-t','cpm-'+service+':dev',str(target)],env=dict(os.environ,DOCKER_BUILDKIT='0'),check=True)
if __name__=='__main__':main()
