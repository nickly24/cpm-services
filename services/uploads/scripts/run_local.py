import os
import sys
from pathlib import Path
ROOT=Path(__file__).resolve().parents[3]
sys.path.insert(0,str(ROOT/'dev/scripts'))
from rabbit_env import read_values
values=read_values(ROOT/'dev/local/uploads.env')
assert values['MYSQL_HOST']=='127.0.0.1' and str(values['MYSQL_PORT'])=='13306' and values['MYSQL_DATABASE']=='cpm_dev' and values['UPLOADS_DEV_MODE']=='true'
values['UPLOADS_OPERATIONS_ENABLED']='0'
if sys.argv[1:] == ['--operations']:
    from run_operation_profile import operation_credentials
    try:
        values.update(operation_credentials(ROOT/'dev/local/import-operation-credentials.env'))
    except (OSError, ValueError):
        raise SystemExit('Private operation configuration invalid; no runtime changed')
    values['UPLOADS_OPERATIONS_ENABLED']='1'
elif sys.argv[1:]:
    raise SystemExit('Only optional --operations is supported')
os.execve(ROOT/'dev/local/uploads-run',[str(ROOT/'dev/local/uploads-run')],{**os.environ,**{k:str(v) for k,v in values.items()}})
