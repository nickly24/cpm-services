"""Run the Go executable with the explicit local dev environment."""
import os
import sys
from pathlib import Path

ROOT=Path(__file__).resolve().parents[3]
sys.path.insert(0,str(ROOT/'dev/scripts'))
from export_structure import read_env

values=read_env(ROOT/'dev/local/auth.env')
assert values['MYSQL_HOST']=='127.0.0.1' and str(values['MYSQL_PORT'])=='13306' and values['MYSQL_DATABASE']=='cpm_dev'
environment={**os.environ,**{key:str(value) for key,value in values.items()}}
os.execve(ROOT/'dev/local/auth-run',[str(ROOT/'dev/local/auth-run')],environment)
