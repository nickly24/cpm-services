import importlib.util,tempfile,unittest
import hashlib
from pathlib import Path
from cryptography.exceptions import InvalidTag
P=Path(__file__).resolve().parents[1]/'scripts/mongo_backup.py';spec=importlib.util.spec_from_file_location('mongo_backup',P);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
class BackupEncryptionTest(unittest.TestCase):
 def test_streaming_roundtrip_and_tamper(self):
  with tempfile.TemporaryDirectory() as tmp:
   p=Path(tmp);source=p/'plain';enc=p/'encrypted';out=p/'restored';key=b'x'*32;data=b'private fixture'*90000;source.write_bytes(data);m.encrypt(source,enc,key);self.assertNotIn(data[:128],enc.read_bytes());m.decrypt(enc,out,key);self.assertEqual(out.read_bytes(),data)
   damaged=bytearray(enc.read_bytes());damaged[len(m.MAGIC)+20]^=1;enc.write_bytes(damaged)
   with self.assertRaises(InvalidTag):m.decrypt(enc,out,key)
   self.assertFalse(out.exists())
 def test_wrong_key_leaves_no_plaintext(self):
  with tempfile.TemporaryDirectory() as tmp:
   p=Path(tmp);(p/'source').write_bytes(b'fixture');m.encrypt(p/'source',p/'enc',b'x'*32)
   with self.assertRaises(InvalidTag):m.decrypt(p/'enc',p/'out',b'y'*32)
   self.assertFalse((p/'out').exists())

class BackupRetentionTest(unittest.TestCase):
 def test_third_verified_backup_rotates_only_oldest_archive(self):
  with tempfile.TemporaryDirectory() as tmp:
   root=Path(tmp);prefix='private/mongo/';objects={}
   for day in (12,13,14):
    name=f'202610{day}T000000000000Z.archive.gz.enc';data=str(day).encode();(root/name).write_bytes(data)
    objects[prefix+name]={'ContentLength':len(data),'Metadata':{'format':'cpm-aes256-gcm-v1','sha256':hashlib.sha256(data).hexdigest()}}
   (root/'initial-source.archive.gz').write_bytes(b'keep source')
   objects[prefix+'unrelated.archive.gz.enc']={}
   class S3:
    def get_paginator(self,*args):return self
    def paginate(self,**kwargs):return [{'Contents':[{'Key':key} for key in objects]}]
    def head_object(self,**kwargs):return objects[kwargs['Key']]
    def delete_objects(self,**kwargs):
     for item in kwargs['Delete']['Objects']:del objects[item['Key']]
     return {}
   m.retain_two(S3(),{'bucket':'fixture','prefix':prefix},root)
   self.assertFalse((root/'20261012T000000000000Z.archive.gz.enc').exists())
   self.assertEqual(len(list(root.glob('*.enc'))),2)
   self.assertEqual(len(objects),3)
   self.assertTrue((root/'initial-source.archive.gz').exists())
   self.assertIn(prefix+'unrelated.archive.gz.enc',objects)

 def test_remote_rotation_failure_preserves_local_backups(self):
  with tempfile.TemporaryDirectory() as tmp:
   root=Path(tmp);objects={}
   for day in (12,13,14):
    name=f'202610{day}T000000000000Z.archive.gz.enc';(root/name).write_bytes(b'fixture')
    objects[name]={'ContentLength':7,'Metadata':{'format':'cpm-aes256-gcm-v1','sha256':hashlib.sha256(b'fixture').hexdigest()}}
   class S3:
    def get_paginator(self,*args):return self
    def paginate(self,**kwargs):return [{'Contents':[{'Key':key} for key in objects]}]
    def head_object(self,**kwargs):return objects[kwargs['Key']]
    def delete_objects(self,**kwargs):return {'Errors':[{'Code':'AccessDenied'}]}
   with self.assertRaises(RuntimeError):m.retain_two(S3(),{'bucket':'fixture','prefix':''},root)
   self.assertEqual(len(list(root.glob('*.enc'))),3)

 def test_schedule_is_daily_at_three_moscow(self):
  timer=(P.parents[1]/'cpm-mongo-backup.timer').read_text()
  self.assertIn('OnCalendar=*-*-* 03:00:00 Europe/Moscow',timer)
  self.assertIn('RandomizedDelaySec=0',timer)
  self.assertNotIn('OnCalendar=hourly',timer)
