#!/usr/bin/env python3
"""Validate immutable CPM releases; never evaluates manifest content as shell code."""
import hashlib,json,re,sys
from pathlib import Path
SERVICES={'auth','core','bff','tests','calculate','cards','exams','homework','file-work-service','ratings','attendance','uploads'}

def validate(directory):
 p=Path(directory); m=json.loads((p/'release.json').read_text())
 if m.get('schema')!=1 or m.get('branch')!='prod' or not re.fullmatch(r'[0-9a-f]{40}',m.get('commit','')):raise ValueError('only frozen prod commits may deploy')
 if set(m.get('images',{}))!=SERVICES:raise ValueError('release must contain all twelve services')
 for s,image in m['images'].items():
  if not re.fullmatch(r'ghcr\.io/nickly24/cpm-services/'+re.escape(s)+r'@sha256:[0-9a-f]{64}',image):raise ValueError('untrusted image reference')
 if not re.fullmatch(r'prod-[0-9]+-[0-9a-f]{12}',m.get('id','')):raise ValueError('invalid release identifier')
 files=m.get('files',{})
 required={'prod/compose.yaml','prod/service-ports.json','prod/scripts/readiness.py','prod/nginx.conf'}
 if not required<=set(files):raise ValueError('incomplete configuration manifest')
 for name,digest in files.items():
  path=p/name
  if not name.startswith('prod/') or '..' in Path(name).parts or path.is_symlink() or not re.fullmatch(r'[0-9a-f]{64}',digest) or hashlib.sha256(path.read_bytes()).hexdigest()!=digest:raise ValueError('release artifact checksum mismatch')
 for file in (p/'prod').rglob('*'):
  if file.is_file() and str(file.relative_to(p)) not in files:raise ValueError('unlisted release file')
 return m

def image_environment(directory):
 m=validate(directory)
 text=''.join('IMAGE_'+s.upper().replace('-','_')+'='+image+'\n' for s,image in sorted(m['images'].items()))
 (Path(directory)/'images.env').write_text(text)
 return m

if __name__=='__main__':
 try:
  m=image_environment(sys.argv[1]);print(m['id'])
 except (ValueError,OSError,KeyError,json.JSONDecodeError) as e:
  print('invalid release: '+str(e),file=sys.stderr);sys.exit(1)
