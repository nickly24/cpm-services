import json,unittest
from pathlib import Path
PROD=Path(__file__).resolve().parents[1]
class ProductionConfigTest(unittest.TestCase):
 def test_all_applications_have_ports(self):self.assertEqual(len(json.loads((PROD/'service-ports.json').read_text())),12)
 def test_runtime_is_external(self):
  text=(PROD/'compose.yaml').read_text();self.assertNotIn('host.docker.internal',text);self.assertNotIn('cpm_dev',text);self.assertNotIn('build:',text);self.assertNotIn('ports:',text);self.assertNotIn('depends_on:',text)
 def test_exact_origins_and_live_sources(self):
  text=(PROD/'compose.yaml').read_text()
  for value in ['RATINGS_EXAMS_MODE: required','RATINGS_EXTERNAL_TESTS_MODE: required','http://exams:8010']:self.assertIn(value,text)
 def test_private_management_and_shared_budget(self):
  text=(PROD/'infra.compose.yaml').read_text()
  for port in ['127.0.0.1:9443:9443','127.0.0.1:9090:9090','127.0.0.1:15672:15672']:self.assertIn(port,text)
  self.assertIn('https://cpm-lms.ru,http://localhost:3000,http://185.239.51.138',(PROD/'.env.example').read_text())
  self.assertIn('MemoryMax=4608M',(PROD/'cpm.slice').read_text())
 def test_tmpfs_mount_paths_are_absolute(self):
  import yaml
  for service in yaml.safe_load((PROD/'infra.compose.yaml').read_text())['services'].values():
   for mount in service.get('tmpfs',[]):self.assertTrue(mount.startswith('/'),mount)
 def test_auth_memory_policy_survives_deploy_and_rollback(self):
  import yaml
  base=yaml.safe_load((PROD/'compose.yaml').read_text())['services']['auth']
  policy=yaml.safe_load((PROD/'resources.compose.yaml').read_text())['services']['auth']
  for item in (base,policy):
   self.assertEqual(item['mem_limit'],'1g')
   self.assertEqual(item['memswap_limit'],'1g')
   self.assertEqual(item['environment']['GOMEMLIMIT'],'768MiB')
  deploy=(PROD/'scripts/deploy.sh').read_text()
  self.assertIn('resources=(-f /etc/cpm/prod/resources.compose.yaml)',deploy)
  self.assertEqual(deploy.count('"${resources[@]}"'),2)
if __name__=='__main__':unittest.main()
