#!/usr/bin/env python3
"""Consistent replica-set dump, authenticated encryption, scoped S3 retention."""
import argparse,datetime,fcntl,hashlib,json,os,re,subprocess,tempfile
from pathlib import Path
from cryptography.hazmat.primitives.ciphers import Cipher,algorithms,modes
MAGIC=b'CPMMONGO1'
CHUNK=1024*1024
BACKUP_NAME=re.compile(r'^\d{8}T\d{12}Z\.archive\.gz\.enc$')

def retain_two(s3,cfg,root):
 # Count complete, verified archives, not elapsed days. Failed runs never reach here.
 verified=[]
 for page in s3.get_paginator('list_objects_v2').paginate(Bucket=cfg['bucket'],Prefix=cfg['prefix']):
  for obj in page.get('Contents',[]):
   name=obj['Key'].removeprefix(cfg['prefix'])
   if not obj['Key'].startswith(cfg['prefix']) or not BACKUP_NAME.fullmatch(name):continue
   head=s3.head_object(Bucket=cfg['bucket'],Key=obj['Key'])
   metadata=head.get('Metadata',{})
   if metadata.get('format')=='cpm-aes256-gcm-v1' and re.fullmatch(r'[0-9a-f]{64}',metadata.get('sha256','')):
    verified.append((obj['Key'],name,head['ContentLength'],metadata['sha256']))
 verified.sort(reverse=True)
 local=[item for item in verified if (root/item[1]).is_file() and (root/item[1]).stat().st_size==item[2] and sha256(root/item[1])==item[3]]
 for start in range(2,len(verified),1000):
  response=s3.delete_objects(Bucket=cfg['bucket'],Delete={'Objects':[{'Key':item[0]} for item in verified[start:start+1000]],'Quiet':True})
  if response.get('Errors'):raise RuntimeError('S3 backup retention deletion failed')
 # Preserve two validated local archives; never touch source dumps, reports or PDF files.
 if len(local)>=2:
  keep={item[1] for item in local[:2]}
  for old in root.iterdir():
   if old.is_file() and BACKUP_NAME.fullmatch(old.name) and old.name not in keep:old.unlink()

def encrypt(source,target,key):
 nonce=os.urandom(12);cipher=Cipher(algorithms.AES(key),modes.GCM(nonce)).encryptor();cipher.authenticate_additional_data(MAGIC)
 with open(source,'rb') as inp,open(target,'wb') as out:
  os.chmod(target,0o600);out.write(MAGIC+nonce)
  while data:=inp.read(CHUNK):out.write(cipher.update(data))
  out.write(cipher.finalize());out.write(cipher.tag)

def decrypt(source,target,key):
 try:
  with open(source,'rb') as inp,open(target,'wb') as out:
   os.chmod(target,0o600)
   if inp.read(len(MAGIC))!=MAGIC:raise ValueError('invalid encrypted backup header')
   nonce=inp.read(12);inp.seek(-16,2);tag=inp.read(16);remaining=inp.tell()-16-len(MAGIC)-12;inp.seek(len(MAGIC)+12)
   c=Cipher(algorithms.AES(key),modes.GCM(nonce,tag)).decryptor();c.authenticate_additional_data(MAGIC)
   while remaining:
    data=inp.read(min(CHUNK,remaining))
    if not data:raise ValueError('truncated encrypted backup')
    out.write(c.update(data));remaining-=len(data)
   out.write(c.finalize())
 except Exception:
  Path(target).unlink(missing_ok=True);raise

def sha256(path):
 h=hashlib.sha256()
 with open(path,'rb') as f:
  while data:=f.read(CHUNK):h.update(data)
 return h.hexdigest()

def run():
 import boto3
 from botocore.config import Config
 cfg=json.loads(Path('/etc/cpm/prod/mongo-backup-config.json').read_text());key=bytes.fromhex(Path('/etc/cpm/prod/mongo-backup.key').read_text().strip())
 root=Path('/srv/cpm/backups/mongo');root.mkdir(mode=0o700,parents=True,exist_ok=True)
 with open('/run/lock/cpm-mongo-backup.lock','w') as lock:
  fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
  now=datetime.datetime.now(datetime.timezone.utc);stamp=now.strftime('%Y%m%dT%H%M%S%fZ');target=root/(stamp+'.archive.gz.enc')
  with tempfile.TemporaryDirectory(prefix='.backup-',dir=root) as tmp:
   plain=Path(tmp)/'mongo.archive.gz';log=Path(tmp)/'mongodump.log'
   with open(plain,'wb') as out,open(log,'wb') as errors:
    os.chmod(plain,0o600);os.chmod(log,0o600)
    r=subprocess.run(['docker','exec','cpm-infra-mongo-1','mongodump','--config=/run/mongo-tools.yaml','--archive','--gzip','--oplog','--numParallelCollections=1'],stdout=out,stderr=errors,timeout=900)
   if r.returncode:raise RuntimeError('consistent mongodump failed; backup was not uploaded')
   encrypt(plain,target,key)
  digest=sha256(target);object_key=cfg['prefix']+target.name
  s3=boto3.client('s3',endpoint_url=cfg['endpoint'],region_name=cfg['region'],aws_access_key_id=cfg['access_key'],aws_secret_access_key=cfg['secret_key'],config=Config(connect_timeout=10,read_timeout=60,retries={'max_attempts':3}))
  s3.upload_file(str(target),cfg['bucket'],object_key,ExtraArgs={'ACL':'private','Metadata':{'sha256':digest,'format':'cpm-aes256-gcm-v1'}})
  head=s3.head_object(Bucket=cfg['bucket'],Key=object_key)
  if head['ContentLength']!=target.stat().st_size or head['Metadata'].get('sha256')!=digest:raise RuntimeError('S3 backup verification failed')
  record={'timestamp':now.isoformat(),'object_key':object_key,'file':str(target),'sha256':digest,'bytes':target.stat().st_size,'s3_verified':True,'consistent_oplog':True}
  (root/'latest.json').write_text(json.dumps(record,indent=2)+'\n');os.chmod(root/'latest.json',0o600)
  retain_two(s3,cfg,root)
  print(json.dumps(record))

if __name__=='__main__':
 p=argparse.ArgumentParser();p.add_argument('--decrypt',nargs=2,metavar=('ENCRYPTED','OUTPUT'));a=p.parse_args()
 if a.decrypt:decrypt(a.decrypt[0],a.decrypt[1],bytes.fromhex(Path('/etc/cpm/prod/mongo-backup.key').read_text().strip()));print('Backup decrypted and authenticated')
 else:run()
