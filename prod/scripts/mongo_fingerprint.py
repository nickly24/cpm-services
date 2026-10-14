#!/usr/bin/env python3
"""Bounded full-fidelity read-only fingerprints. Never prints source rows or URIs."""
import argparse,hashlib,json,struct,time
from pathlib import Path
from bson import json_util
from bson.raw_bson import RawBSONDocument
from bson.codec_options import CodecOptions
from pymongo import MongoClient

def canonical(value):return json.dumps(json.loads(json_util.dumps(value)),ensure_ascii=False,sort_keys=True,separators=(',',':'))
def fingerprint(uri,database):
 client=MongoClient(uri,serverSelectionTimeoutMS=10000,connectTimeoutMS=10000,socketTimeoutMS=60000,maxPoolSize=2)
 db=client[database];result={'database':database,'collections':{}}
 try:
  for meta in sorted(db.list_collections(),key=lambda v:v['name']):
   name=meta['name']
   if name.startswith('system.'):continue
   options=meta.get('options',{});item={'type':meta['type'],'options':json.loads(json_util.dumps(options))}
   if meta['type']=='view':result['collections'][name]=item;continue
   col=db.get_collection(name,codec_options=CodecOptions(document_class=RawBSONDocument))
   digest=hashlib.sha256();count=0
   with col.find({},batch_size=32).sort('_id',1).max_time_ms(120000) as cursor:
    for doc in cursor:
     raw=doc.raw;digest.update(struct.pack('>I',len(raw)));digest.update(raw);count+=1
     if count%128==0:time.sleep(.005)
   indexes=[]
   for idx in db[name].list_indexes():
    idx=dict(idx);idx['key']=[[key,value] for key,value in idx['key'].items()]
    for key in ['v','ns','background']:idx.pop(key,None)
    for key in ['unique','sparse','hidden']:
     if idx.get(key) is False:idx.pop(key)
    indexes.append(idx)
   item.update(count=count,sha256=digest.hexdigest(),indexes=sorted(indexes,key=lambda x:x['name']))
   result['collections'][name]=item
  result['total_documents']=sum(c.get('count',0) for c in result['collections'].values())
  result['combined_sha256']=hashlib.sha256(canonical(result['collections']).encode()).hexdigest()
  return result
 finally:client.close()

if __name__=='__main__':
 p=argparse.ArgumentParser();p.add_argument('--connection',required=True);p.add_argument('--output',required=True);a=p.parse_args();v=json.loads(Path(a.connection).read_text());uri=v.get('MONGODB_URI') or v['uri'];database=v.get('MONGODB_DB_NAME') or v['database'];r=fingerprint(uri,database);out=Path(a.output);out.touch(mode=0o600,exist_ok=True);out.chmod(0o600);out.write_text(json.dumps(r,indent=2,ensure_ascii=False)+'\n');print(json.dumps({'collections':len(r['collections']),'documents':r['total_documents'],'combined_sha256':r['combined_sha256']}))
