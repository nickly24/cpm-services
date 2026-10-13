"""Prepare only the new tests collection on fixed local dev; preserve legacy schemas."""
import fcntl
import json
import os
import sys
from pathlib import Path

ROOT=Path(__file__).resolve().parents[3]
sys.path.insert(0,str(ROOT/'dev/scripts'))
from bootstrap import settings,mongo_client
from rabbit_env import read_values

TESTS_MONGO_COLLECTIONS = (
    'tests_catalog_v1', 'tests_content_versions_v1', 'tests_attempts_v1',
    'tests_calculation_jobs_v1', 'tests_drafts_v1', 'tests_external_catalog_v1',
    'tests_external_domain_v1', 'tests_external_versions_v1',
)

def register_mongo_extensions():
    """Read under the shared file lock and append only this owner's names."""
    path = ROOT/'dev/schema/mongo-extensions.json'
    with path.open('r+') as stream:
        fcntl.flock(stream, fcntl.LOCK_EX)
        current = json.load(stream)
        if not isinstance(current, list) or any(not isinstance(name, str) for name in current):
            raise ValueError('Invalid Mongo extension manifest; retained unchanged')
        merged = current + [name for name in TESTS_MONGO_COLLECTIONS if name not in current]
        if merged != current:
            stream.seek(0)
            stream.write(json.dumps(merged, ensure_ascii=False, indent=2)+'\n')
            stream.truncate()
            stream.flush()
            os.fsync(stream.fileno())

def main():
    v=settings()
    rabbit=read_values(ROOT/'dev/.env')
    if not rabbit.get('RABBITMQ_URL'):
        raise SystemExit('Run ./dev/dev rabbit-prepare first.')
    client=mongo_client(v)
    try:
        db=client.cpm_dev
        name='tests_catalog_v1'
        if name not in db.list_collection_names():
            db.create_collection(name)
        collection=db[name]
        collection.create_index([('organization_id',1),('is_archived',1),('created_at',-1),('_id',-1)],name='organization_catalog')
        collection.create_index([('organization_id',1),('placements.division_id',1),('created_at',-1)],name='division_catalog')
        collection.create_index([('organization_id',1),('direction_id',1),('owner_division_id',1)],name='catalog_filters')
        for name in TESTS_MONGO_COLLECTIONS:
            if name not in db.list_collection_names():
                db.create_collection(name)
        db.tests_content_versions_v1.create_index([('organization_id',1),('test_id',1),('created_at',-1),('_id',-1)], name='test_versions')
        db.tests_attempts_v1.create_index([('organization_id',1),('division_id',1),('account_id',1),('test_id',1),('practice',1)], unique=True, partialFilterExpression={'active':True}, name='active_attempt_scope')
        db.tests_attempts_v1.create_index([('submission_id',1)], unique=True, partialFilterExpression={'submission_id':{'$type':'objectId'}}, name='submission_identity')
        db.tests_attempts_v1.create_index([('organization_id',1),('division_id',1),('accepted_at',-1),('_id',-1)], name='division_submissions')
        db.tests_drafts_v1.create_index([('organization_id',1),('division_id',1),('is_archived',1),('updated_at',-1),('_id',-1)], name='division_drafts')
    finally:
        client.close()
    register_mongo_extensions()
    p=ROOT/'dev/local/tests.env'
    env={
        'MONGO_HOST':'127.0.0.1','MONGO_PORT':'27018','MONGO_DATABASE':'cpm_dev',
        'MONGO_USER':v['MONGO_USER'],'MONGO_PASSWORD':v['MONGO_PASSWORD'],
        'TESTS_LISTEN_ADDR':'127.0.0.1:8004','TESTS_AUTH_URL':'http://127.0.0.1:8002',
        'TESTS_CORE_URL':'http://127.0.0.1:8003','AUTH_INTERNAL_KEY':v['AUTH_INTERNAL_KEY'],
        'TESTS_CALCULATE_URL':'http://127.0.0.1:8005','RABBITMQ_URL':rabbit['RABBITMQ_URL'],
    }
    p.write_text('\n'.join(f'{k}={value}' for k,value in env.items())+'\n')
    p.chmod(0o600)
    print('Local tests collections and environment prepared. Legacy data retained.')

if __name__=='__main__':
    main()
