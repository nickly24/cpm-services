import hashlib,importlib.util,json,tempfile,unittest
from pathlib import Path
PROD=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('release',PROD/'scripts/release.py');r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)
class ReleaseTest(unittest.TestCase):
 def setUp(self):
  self.tmp=tempfile.TemporaryDirectory();self.p=Path(self.tmp.name);(self.p/'prod/scripts').mkdir(parents=True)
  for n in ['prod/compose.yaml','prod/service-ports.json','prod/scripts/readiness.py','prod/nginx.conf']:(self.p/n).write_text('fixture')
  self.m={'schema':1,'branch':'prod','commit':'a'*40,'id':'prod-123-'+('a'*12),'images':{s:'ghcr.io/nickly24/cpm-services/'+s+'@sha256:'+('b'*64) for s in r.SERVICES},'files':{str(p.relative_to(self.p)):hashlib.sha256(p.read_bytes()).hexdigest() for p in (self.p/'prod').rglob('*') if p.is_file()}}
 def tearDown(self):self.tmp.cleanup()
 def save(self):(self.p/'release.json').write_text(json.dumps(self.m))
 def test_complete_frozen_release(self):self.save();self.assertEqual(r.image_environment(self.p)['id'],self.m['id']);self.assertIn('@sha256:',(self.p/'images.env').read_text())
 def test_reject_nonprod(self):self.m['branch']='dev';self.save();self.assertRaises(ValueError,r.validate,self.p)
 def test_reject_missing_service(self):del self.m['images']['uploads'];self.save();self.assertRaises(ValueError,r.validate,self.p)
 def test_reject_mutable_image(self):self.m['images']['auth']='ghcr.io/nickly24/cpm-services/auth:latest';self.save();self.assertRaises(ValueError,r.validate,self.p)
 def test_reject_tampered_configuration(self):self.save();(self.p/'prod/compose.yaml').write_text('tamper');self.assertRaises(ValueError,r.validate,self.p)
 def test_reject_unlisted_script(self):self.save();(self.p/'prod/scripts/extra.py').write_text('untrusted');self.assertRaises(ValueError,r.validate,self.p)
 def test_reject_traversal(self):self.m['files']['prod/../../untrusted']='b'*64;self.save();self.assertRaises(ValueError,r.validate,self.p)
if __name__=='__main__':unittest.main()
