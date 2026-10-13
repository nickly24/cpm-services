"""Run race/vet and actual MySQL integration without exposing private DSN."""
import os
import subprocess
import sys
from pathlib import Path
ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0,str(ROOT/'dev/scripts'))
from bootstrap import settings
v=settings()
env=os.environ.copy()
# Dev user/password are generated hex/alphanumeric; enforce Go DSN-safe characters.
assert all(c.isalnum() or c=='_' for c in v['MYSQL_USER'])
assert all(c.isalnum() for c in v['MYSQL_PASSWORD'])
env['ATTENDANCE_TEST_DSN']=f"{v['MYSQL_USER']}:{v['MYSQL_PASSWORD']}@tcp(127.0.0.1:13306)/cpm_dev?parseTime=true&charset=utf8mb4"
for args in [['test','-race','./...'],['vet','./...']]:
    subprocess.run([str(Path.home()/'.local/bin/go'),*args],cwd=ROOT/'services/attendance',env=env,check=True)
