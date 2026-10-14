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
   self.assertEqual(item['mem_limit'],'1536m')
   self.assertEqual(item['memswap_limit'],'1536m')
   self.assertEqual(item['environment']['GOMEMLIMIT'],'1152MiB')
  deploy=(PROD/'scripts/deploy.sh').read_text()
  self.assertIn('resources=(-f /etc/cpm/prod/resources.compose.yaml)',deploy)
  self.assertEqual(deploy.count('"${resources[@]}"'),2)

class HTTPSPolicyTest(unittest.TestCase):
 def test_tls_policy_survives_older_release_rollback(self):
  import subprocess,tempfile
  script=(PROD/'scripts/deploy.sh').read_text()
  self.assertEqual(script.count('install_ingress_config "'),2)
  start=script.index('install_ingress_config() {')
  stop=script.index('\nactivate_ingress()',start)
  helper=script[start:stop]
  with tempfile.TemporaryDirectory() as directory:
   root=Path(directory);(root/'release/prod').mkdir(parents=True)
   (root/'release/prod/nginx.conf').write_text('old HTTP release')
   helper=helper.replace('/etc/cpm/prod/nginx.conf',str(root/'host.conf')).replace('/opt/cpm/infra/nginx.conf',str(root/'active.conf'))
   def activate():subprocess.run(['bash','-euc',helper+'\ninstall_ingress_config "$1"','test',str(root/'release')],check=True)
   activate();self.assertEqual((root/'active.conf').read_text(),'old HTTP release')
   (root/'host.conf').write_text('durable HTTPS policy')
   activate();self.assertEqual((root/'active.conf').read_text(),'durable HTTPS policy')
 def test_certificates_webroot_and_renewal_share_paths(self):
  import yaml
  nginx=yaml.safe_load((PROD/'infra.compose.yaml').read_text())['services']['nginx']
  self.assertIn('443:443',nginx['ports'])
  for volume in ['/etc/letsencrypt:/etc/letsencrypt:ro','/var/lib/letsencrypt:/var/www/acme:ro']:self.assertIn(volume,nginx['volumes'])
  config=(PROD/'nginx.tls.conf').read_text()
  self.assertIn('root /var/www/acme;',config)
  self.assertIn('/etc/letsencrypt/live/back1.cpm-lms.ru/fullchain.pem',config)
  self.assertIn('return 308 https://back1.cpm-lms.ru$request_uri;',config)
  self.assertIn('listen 80 default_server;',config)
  self.assertIn('ssl_protocols TLSv1.2 TLSv1.3;',config)
  hook=(PROD/'scripts/certbot-nginx-reload').read_text()
  self.assertLess(hook.index('nginx -t'),hook.index('nginx -s reload'))
  env=(PROD/'.env.example').read_text()
  self.assertIn('BFF_COOKIE_SECURE=true',env)
  self.assertIn('https://back1.cpm-lms.ru',env)

if __name__=='__main__':unittest.main()
