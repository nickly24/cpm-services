"""Generate ignored dev configuration; no network, DB or Docker operations."""
import argparse
import os
import secrets
from pathlib import Path
from urllib.parse import urlparse

from rabbit_env import read_values

BASE = Path(__file__).resolve().parents[1]


def write_private(path, values):
    # Values are dotenv data, never interpreted by a shell in this helper.
    for key, value in values.items():
        if any(character in str(value) for character in '\r\n\x00'):
            raise SystemExit('Environment values must not contain line breaks.')
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    try:
        os.fchmod(descriptor, 0o600)
        with os.fdopen(descriptor, 'w') as output:
            descriptor = None
            output.write('\n'.join(f'{key}={value}' for key, value in values.items()) + '\n')
    finally:
        if descriptor is not None:
            os.close(descriptor)


def worker_key(values, previous):
    core_key = values.get('AUTH_INTERNAL_KEY', '')
    if len(core_key) < 32:
        raise SystemExit('An existing dev AUTH_INTERNAL_KEY of at least 32 bytes is required.')
    supplied = values.get('HOMEWORK_SERVICE_KEY')
    if supplied:
        if len(supplied) < 32 or supplied == core_key:
            raise SystemExit('HOMEWORK_SERVICE_KEY must be at least 32 bytes and distinct from AUTH_INTERNAL_KEY.')
        return supplied
    saved = previous.get('HOMEWORK_SERVICE_KEY', '')
    if len(saved) >= 32 and saved != core_key:
        return saved
    # First setup and the old shared-key setup get a dedicated worker key.
    # Subsequent runs reuse it; regenerating dev env must not invalidate jobs.
    return secrets.token_hex(32)


def prepare(test_s3=False):
    values = read_values(BASE / '.env')
    if values.get('MYSQL_DATABASE') != 'cpm_dev' or values.get('MYSQL_PORT') != '13306':
        raise SystemExit('Homework configuration supports only local MySQL :13306/cpm_dev.')
    if values.get('RABBITMQ_HOST') != '127.0.0.1' or values.get('RABBITMQ_VHOST') != 'cpm_dev':
        raise SystemExit('Run ./dev/dev rabbit-prepare first with local cpm_dev settings.')
    if not values.get('RABBITMQ_URL') or not values.get('RABBITMQ_DOCKER_URL'):
        raise SystemExit('Run ./dev/dev rabbit-prepare first.')
    local = BASE / 'local'
    local.mkdir(mode=0o700, parents=True, exist_ok=True)
    previous_path = local / 'homework-compose.env'
    previous = read_values(previous_path) if previous_path.exists() else {}
    key = worker_key(values, previous)
    if test_s3:
        access = previous.get('MINIO_ROOT_USER') or 'cpm_homework_fixture'
        secret = previous.get('MINIO_ROOT_PASSWORD') or secrets.token_hex(32)
        endpoint = 'http://127.0.0.1:19000'
        docker_endpoint = 'http://s3-test:9000'
        public_endpoint = endpoint
        bucket = 'cpm-homework-test'
        region = 'us-east-1'
    else:
        endpoint = values.get('HOMEWORK_S3_ENDPOINT_URL') or 'http://127.0.0.1:19000'
        docker_endpoint = values.get('HOMEWORK_S3_DOCKER_ENDPOINT_URL') or endpoint
        public_endpoint = values.get('HOMEWORK_S3_PUBLIC_ENDPOINT_URL') or endpoint
        bucket = values.get('HOMEWORK_S3_BUCKET') or 'cpm-homework-dev-unconfigured'
        access = values.get('HOMEWORK_S3_ACCESS_KEY_ID') or 'dev-unconfigured'
        secret = values.get('HOMEWORK_S3_SECRET_ACCESS_KEY') or 'dev-unconfigured'
        region = values.get('HOMEWORK_S3_REGION') or 'us-east-1'
        if bucket == 'cpm-homework-test':
            raise SystemExit('Use --test-s3 explicitly for the local fixture bucket.')
    for address in [endpoint, docker_endpoint, public_endpoint]:
        parsed = urlparse(address)
        if (parsed.scheme not in ('http', 'https') or not parsed.hostname or parsed.username or parsed.password
                or parsed.path or parsed.query or parsed.fragment):
            raise SystemExit('S3 endpoints must be HTTP(S) origins without credentials, paths or query parameters.')
    s3 = {'S3_ENDPOINT_URL': endpoint, 'S3_PUBLIC_ENDPOINT_URL': public_endpoint,
          'S3_REGION': region, 'S3_BUCKET': bucket, 'S3_ACCESS_KEY_ID': access,
          'S3_SECRET_ACCESS_KEY': secret}
    homework = {
        'MYSQL_HOST': '127.0.0.1', 'MYSQL_PORT': '13306', 'MYSQL_DATABASE': 'cpm_dev',
        'MYSQL_USER': values['MYSQL_USER'], 'MYSQL_PASSWORD': values['MYSQL_PASSWORD'],
        'HOMEWORK_LISTEN_ADDR': '127.0.0.1:8006',
        'HOMEWORK_AUTH_URL': 'http://127.0.0.1:8002',
        'HOMEWORK_CORE_URL': 'http://127.0.0.1:8003',
        'AUTH_INTERNAL_KEY': values['AUTH_INTERNAL_KEY'], 'HOMEWORK_SERVICE_KEY': key,
        'RABBITMQ_URL': values['RABBITMQ_URL'], **s3,
    }
    worker = {'LISTEN_ADDR': '127.0.0.1:8007', 'RABBITMQ_URL': values['RABBITMQ_URL'],
              'HOMEWORK_URL': 'http://127.0.0.1:8006', 'HOMEWORK_SERVICE_KEY': key,
              'PDF_TEMP_DIR': '/tmp', 'PDF_PROCESS_TIMEOUT_SECONDS': '120', **s3}
    compose = {
        'HOMEWORK_SERVICE_KEY': key,
        'HOMEWORK_S3_ENDPOINT_URL': docker_endpoint,
        'HOMEWORK_S3_PUBLIC_ENDPOINT_URL': public_endpoint,
        'HOMEWORK_S3_REGION': region, 'HOMEWORK_S3_BUCKET': bucket,
        'HOMEWORK_S3_ACCESS_KEY_ID': access, 'HOMEWORK_S3_SECRET_ACCESS_KEY': secret,
        'MINIO_ROOT_USER': access if test_s3 else previous.get('MINIO_ROOT_USER', 'cpm_homework_fixture'),
        'MINIO_ROOT_PASSWORD': secret if test_s3 else previous.get('MINIO_ROOT_PASSWORD', secrets.token_hex(32)),
        'HOMEWORK_S3_TEST_FIXTURE': 'true' if test_s3 else 'false',
    }
    write_private(local / 'homework-go.env', homework)
    write_private(local / 'file-work.env', worker)
    write_private(previous_path, compose)
    print('Private homework/file-work dev environments prepared. No services started.')
    print('S3 mode: local isolated fixture.' if test_s3 else 'S3 mode: supplied dev settings or unconfigured placeholder.')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--test-s3', action='store_true', help='Configure only the optional isolated local S3 fixture.')
    arguments = parser.parse_args()
    prepare(arguments.test_s3)
