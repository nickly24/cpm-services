"""Create homework-owned tables and limit settings in fixed local cpm_dev only."""
import argparse
import datetime
import hashlib
import hmac
import json
import re
import sys
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'dev/scripts'))
from bootstrap import mysql_connection, settings
from rabbit_env import read_values

DEFAULT_LIMITS = {
    'max_source_bytes': 10 * 1024 * 1024,
    'max_output_bytes': 10 * 1024 * 1024,
    'max_pages': 35,
    'dpi': 120,
    'jpeg_quality': 75,
    'presign_ttl_seconds': 300,
    'upload_stale_seconds': 1800,
}


def prepare_test_bucket():
    """Create only the isolated localhost fixture bucket using signed S3 PUT."""
    overlay = read_values(ROOT / 'dev/local/homework-compose.env')
    values = read_values(ROOT / 'dev/local/homework-go.env')
    if overlay.get('HOMEWORK_S3_TEST_FIXTURE') != 'true':
        raise SystemExit('--test-s3 requires the explicitly selected local fixture environment.')
    if values.get('S3_ENDPOINT_URL') != 'http://127.0.0.1:19000' or values.get('S3_BUCKET') != 'cpm-homework-test':
        raise SystemExit('Bucket setup permits only localhost:19000/cpm-homework-test.')
    now = datetime.datetime.now(datetime.timezone.utc)
    timestamp, day = now.strftime('%Y%m%dT%H%M%SZ'), now.strftime('%Y%m%d')
    empty_hash = hashlib.sha256(b'').hexdigest()
    canonical_headers = f'host:127.0.0.1:19000\nx-amz-content-sha256:{empty_hash}\nx-amz-date:{timestamp}\n'
    signed_headers = 'host;x-amz-content-sha256;x-amz-date'
    canonical_request = '\n'.join(['PUT', '/cpm-homework-test', '', canonical_headers,
                                  signed_headers, empty_hash])
    scope = f'{day}/us-east-1/s3/aws4_request'
    string_to_sign = '\n'.join(['AWS4-HMAC-SHA256', timestamp, scope,
                               hashlib.sha256(canonical_request.encode()).hexdigest()])
    signing_key = ('AWS4' + values['S3_SECRET_ACCESS_KEY']).encode()
    for part in [day, 'us-east-1', 's3', 'aws4_request']:
        signing_key = hmac.new(signing_key, part.encode(), hashlib.sha256).digest()
    signature = hmac.new(signing_key, string_to_sign.encode(), hashlib.sha256).hexdigest()
    authorization = (f'AWS4-HMAC-SHA256 Credential={values["S3_ACCESS_KEY_ID"]}/{scope}, '
                     f'SignedHeaders={signed_headers}, Signature={signature}')
    request = Request('http://127.0.0.1:19000/cpm-homework-test', data=b'', method='PUT',
                      headers={'X-Amz-Date': timestamp, 'X-Amz-Content-Sha256': empty_hash,
                               'Authorization': authorization})
    try:
        with urlopen(request, timeout=15) as response:
            if response.status != 200:
                raise RuntimeError('Unexpected test bucket creation response.')
    except HTTPError as error:
        if error.code != 409 or b'<Code>BucketAlreadyOwnedByYou</Code>' not in error.read(4096):
            raise RuntimeError(f'Isolated test bucket setup failed with HTTP {error.code}.') from None
    print('Isolated local cpm-homework-test bucket is available; other buckets untouched.')


def main(test_s3=False):
    values = settings()
    connection = mysql_connection(values)
    cursor = connection.cursor()
    try:
        cursor.execute('SELECT DATABASE()')
        if cursor.fetchone()[0] != 'cpm_dev':
            raise RuntimeError('Homework setup is restricted to local cpm_dev.')
        source = (ROOT / 'services/homework/migrations/001_homework.sql').read_text()
        # This migration contains CREATE IF NOT EXISTS statements only. Refuse
        # any future accidental mutation of legacy tables by this dev helper.
        source = re.sub(r'^\s*--[^\n]*', '', source, flags=re.MULTILINE)
        statements = [statement.strip() for statement in source.split(';') if statement.strip()]
        for statement in statements:
            if not re.match(r'^CREATE TABLE IF NOT EXISTS hw_[a-z_]+\s*\(', statement):
                raise RuntimeError('Only additive hw_ table migrations are permitted.')
            cursor.execute(statement)
        # The table may already exist from an interrupted initial setup. This
        # additive patch is restricted to the new domain-owned table.
        cursor.execute("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='cpm_dev' "
                       "AND table_name='hw_jobs' AND column_name='lease_token'")
        if cursor.fetchone()[0] == 0:
            cursor.execute('ALTER TABLE hw_jobs ADD COLUMN lease_token CHAR(36) '
                           'CHARACTER SET ascii COLLATE ascii_bin NULL AFTER lease_until')
        cursor.execute("SELECT data_type FROM information_schema.columns WHERE table_schema='cpm_dev' "
                       "AND table_name='hw_audit' AND column_name='entity_id'")
        audit_type = cursor.fetchone()
        if audit_type is None:
            raise RuntimeError('Homework audit schema is incomplete.')
        if audit_type[0] != 'varchar':
            cursor.execute('ALTER TABLE hw_audit MODIFY COLUMN entity_id VARCHAR(100) NOT NULL')
        encoded = json.dumps(DEFAULT_LIMITS, separators=(',', ':'))
        cursor.execute('SELECT d.id FROM divisions d JOIN organizations o ON o.id=d.organization_id '
                       'WHERE d.is_active=1 AND o.is_active=1 ORDER BY d.id')
        divisions = [row[0] for row in cursor.fetchall()]
        for division in divisions:
            cursor.execute('INSERT IGNORE INTO application_settings(setting_key,setting_value) VALUES(%s,%s)',
                           (f'homework.{division}.limits', encoded))
        connection.commit()
        print(f'Homework-owned schema prepared; existing data and {len(divisions)} division settings retained.')
    except Exception:
        connection.rollback()
        raise
    finally:
        cursor.close()
        connection.close()
    if test_s3:
        prepare_test_bucket()


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--test-s3', action='store_true', help='Also create the explicitly configured local fixture bucket.')
    arguments = parser.parse_args()
    main(arguments.test_s3)
