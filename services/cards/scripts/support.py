"""HTTP helpers restricted to the loopback dev contour. Never log credentials."""
import json,sys,urllib.request,urllib.error,secrets
from pathlib import Path
ROOT=Path(__file__).resolve().parents[3]
sys.path.insert(0,str(ROOT/'dev/scripts'))
from bootstrap import settings,mysql_connection
HOSTS={k:f'http://127.0.0.1:{v}' for k,v in [('auth',8002),('core',8003),('tests',8004),('cards',8008)]}
checks=[]
def request(service,path,token=None,method='GET',body=None,code=200,headers=None,files=None,data=None):
 assert service in HOSTS and path.startswith('/')
 h=dict(headers or {})
 if token:h['Authorization']='Bearer '+token
 payload=None
 if body is not None:payload=json.dumps(body).encode();h['Content-Type']='application/json'
 if files:
  boundary='cards_'+secrets.token_hex(12);parts=[]
  for name,value in (data or {}).items():parts.append((f'--{boundary}\r\nContent-Disposition: form-data; name="{name}"\r\n\r\n{value}\r\n').encode())
  for name,(filename,content) in files.items():parts.append((f'--{boundary}\r\nContent-Disposition: form-data; name="{name}"; filename="{filename}"\r\nContent-Type: application/octet-stream\r\n\r\n').encode()+content+b'\r\n')
  payload=b''.join(parts)+f'--{boundary}--\r\n'.encode();h['Content-Type']='multipart/form-data; boundary='+boundary
 req=urllib.request.Request(HOSTS[service]+path,data=payload,method=method,headers=h)
 try:
  with urllib.request.urlopen(req,timeout=40) as r:status=r.status;raw=r.read()
 except urllib.error.HTTPError as e:status=e.code;raw=e.read()
 try:out=json.loads(raw)
 except ValueError:out={}
 if status not in (code if isinstance(code,tuple) else (code,)):
  raise AssertionError(f'{service} {method} {path.split("?")[0]} expected {code}, got {status}; error={out.get("error","unknown")}')
 checks.append(f'{service} {method} {path.split("?")[0]} -> {status}')
 return out

def api(service,path,token=None,method='GET',body=None,code=200,**kw):
 out=request(service,path,token,method,body,code,**kw)
 return out.get('data') if isinstance(code,tuple) or code<300 else out

def login(name,password,org=1001,division=1001):
 return request('auth','/v1/auth/login',method='POST',body={'login':name,'password':password,'organization_id':org,'division_id':division})['token']
def save(path,data):
 path.write_text(json.dumps(data,ensure_ascii=False,indent=2)+'\n');path.chmod(0o600)
def expect(ok,label):
 if not ok:raise AssertionError(label)
 checks.append(label)

import io,zipfile
from xml.sax.saxutils import escape
def xlsx(rows):
 stream=io.BytesIO()
 with zipfile.ZipFile(stream,'w') as z:
  z.writestr('[Content_Types].xml','<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>')
  z.writestr('_rels/.rels','<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>')
  z.writestr('xl/workbook.xml','<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Users" sheetId="1" r:id="rId1"/></sheets></workbook>')
  z.writestr('xl/_rels/workbook.xml.rels','<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>')
  xml='<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>'
  for ri,row in enumerate(rows,1):
   xml+=f'<row r="{ri}">'
   for ci,value in enumerate(row):xml+=f'<c r="{chr(65+ci)}{ri}" t="inlineStr"><is><t>{escape(str(value))}</t></is></c>'
   xml+='</row>'
  z.writestr('xl/worksheets/sheet1.xml',xml+'</sheetData></worksheet>')
 return stream.getvalue()
