#!/usr/bin/env python3
"""Bounded read-only production connectivity/schema probe; outputs no credentials."""
import json,sys,time
from pathlib import Path
import mysql.connector,pymongo,boto3
from botocore.config import Config
v=json.loads(Path(sys.argv[1]).read_text());result={}
try:
 c=mysql.connector.connect(host=v['MYSQL_HOST'],port=int(v['MYSQL_PORT']),user=v['MYSQL_USER'],password=v['MYSQL_PASSWORD'],database=v['MYSQL_DATABASE'],autocommit=True,connection_timeout=8,read_timeout=8,write_timeout=8,ssl_disabled=False)
 cur=c.cursor();cur.execute("SHOW STATUS LIKE 'Ssl_cipher'");cipher=cur.fetchone()[1];result['mysql']={'connected':True,'database':v['MYSQL_DATABASE'],'tls_cipher':cipher}
 cur.execute('SELECT @@max_connections');result['mysql']['max_connections']=cur.fetchone()[0]
 cur.execute('SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=%s',(v['MYSQL_DATABASE'],));result['mysql']['table_count']=cur.fetchone()[0]
 cur.execute('SELECT id FROM organizations WHERE id=1001');result['mysql']['organization_present']=cur.fetchone() is not None
 cur.execute('SELECT id FROM divisions WHERE id=1001 AND organization_id=1001');result['mysql']['division_present']=cur.fetchone() is not None
 cur.close();c.close()
except Exception as e:result['mysql']={'connected':False,'error_type':type(e).__name__}
try:
 client=pymongo.MongoClient(v['MONGODB_URI'],serverSelectionTimeoutMS=8000,connectTimeoutMS=8000,socketTimeoutMS=8000,maxPoolSize=2);client.admin.command('ping');db=client[v['MONGODB_DB_NAME']]
 result['mongo']={'connected':True,'database':v['MONGODB_DB_NAME'],'collections':db.list_collection_names()}
 # Pending job collections are small metadata-only counts, not scans of test answers.
 result['mongo']['pending_jobs']={}
 for name in ['calculation_jobs','calculation_outbox','test_outbox','grading_jobs','ratings_jobs']:
  if name in result['mongo']['collections']:result['mongo']['pending_jobs'][name]=db[name].count_documents({},maxTimeMS=4000)
 client.close()
except Exception as e:result['mongo']={'connected':False,'error_type':type(e).__name__}
try:
 s3=boto3.client('s3',endpoint_url=v['S3_ENDPOINT_URL'],region_name=v['S3_REGION'],aws_access_key_id=v['S3_ACCESS_KEY_ID'],aws_secret_access_key=v['S3_SECRET_ACCESS_KEY'],config=Config(connect_timeout=8,read_timeout=8,retries={'max_attempts':0}));s3.head_bucket(Bucket=v['S3_BUCKET']);result['s3']={'connected':True,'external':True}
except Exception as e:result['s3']={'connected':False,'error_type':type(e).__name__}
print(json.dumps(result,ensure_ascii=False,indent=2))
if not all(result[x].get('connected') for x in ['mysql','mongo','s3']):sys.exit(1)
