"""Copy the local dev signing key to a named volume without macOS bind mounts."""
import os
import shutil
import subprocess
from pathlib import Path

ROOT=Path(__file__).resolve().parents[3]
docker=shutil.which('docker') or '/Applications/Docker.app/Contents/Resources/bin/docker'
key=ROOT/'dev/local/auth-private.pem'
if not key.is_file():raise SystemExit('Generate dev/local/auth-private.pem first')
name='cpm-dev-auth-key-init'
def run(*args):
    return subprocess.run([docker,*args],check=True,capture_output=True,text=True)
run('create','--name',name,'--user','0:0','--entrypoint','/bin/sh','--mount','type=volume,source=cpm-dev_auth-keys,target=/keys','cpm-auth:dev','-c',f'chown {os.getuid()}:{os.getgid()} /keys/auth-private.pem && chmod 0400 /keys/auth-private.pem')
try:
    run('cp',str(key),name+':/keys/auth-private.pem')
    run('start','--attach',name)
finally:
    run('rm',name)
print('Dev signing key provisioned in cpm-dev_auth-keys.')
