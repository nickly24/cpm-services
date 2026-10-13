"""Additive local ratings setup. Never seeds records or operates shared services."""
import argparse
import os
from pathlib import Path
import shutil
import subprocess
from urllib.parse import unquote, urlparse

from bootstrap import settings, mysql_connection, mongo_client
from rabbit_env import read_values

BASE = Path(__file__).resolve().parents[1]
ROOT = BASE.parent


def guarded_settings():
    values = settings()
    if values.get('MYSQL_DATABASE') != 'cpm_dev' or values.get('MONGO_DATABASE') != 'cpm_dev':
        raise SystemExit('Ratings setup supports only cpm_dev.')
    for name in ['MYSQL_DATABASE', 'MONGO_DATABASE', 'RABBITMQ_DOCKER_URL']:
        if name in os.environ and os.environ[name] != str(values[name]):
            raise SystemExit('Conflicting shell configuration would override the guarded dev settings.')
    for name, hostname, port in [('RABBITMQ_URL', '127.0.0.1', 5672),
                                 ('RABBITMQ_DOCKER_URL', 'rabbitmq', 5672)]:
        address = urlparse(values.get(name, ''))
        expected_port = int(values.get('RABBITMQ_PORT', 5672)) if name == 'RABBITMQ_URL' else port
        if (address.scheme != 'amqp' or address.hostname != hostname or address.port != expected_port
                or unquote(address.path) != '/cpm_dev' or not address.username or not address.password
                or address.query or address.fragment):
            raise SystemExit('Prepare shared local RabbitMQ cpm_dev settings first.')
    if len(values.get('AUTH_INTERNAL_KEY', '')) < 32:
        raise SystemExit('An existing AUTH_INTERNAL_KEY of at least 32 characters is required.')
    for name in ['MYSQL_USER', 'MYSQL_PASSWORD', 'MONGO_USER', 'MONGO_PASSWORD']:
        if not values.get(name):
            raise SystemExit('Required local database configuration is missing.')
    return values


def write_private(path, values):
    for value in values.values():
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


def configure(values):
    local = BASE / 'local'
    local.mkdir(mode=0o700, parents=True, exist_ok=True)
    previous_path = local / 'ratings-compose.env'
    previous = read_values(previous_path) if previous_path.exists() else {}
    mode = previous.get('RATINGS_EXAMS_MODE', 'dev_empty')
    if mode not in ('dev_empty', 'required'):
        raise SystemExit('Invalid ratings exams mode.')
    external_mode = previous.get('RATINGS_EXTERNAL_TESTS_MODE', 'dev_internal_only')
    if external_mode not in ('dev_internal_only', 'required'):
        raise SystemExit('Invalid ratings external tests mode.')
    # Preserve an explicit future exams endpoint when re-running preparation.
    docker_exams = previous.get('RATINGS_EXAMS_URL', '')
    ratings = {key: values[key] for key in ['MYSQL_USER', 'MYSQL_PASSWORD', 'AUTH_INTERNAL_KEY']}
    ratings.update(MYSQL_HOST='127.0.0.1', MYSQL_PORT='13306', MYSQL_DATABASE='cpm_dev',
                   RATINGS_LISTEN_ADDR='127.0.0.1:8012', RATINGS_AUTH_URL='http://127.0.0.1:8002',
                   RATINGS_CORE_URL='http://127.0.0.1:8003', RATINGS_HOMEWORK_URL='http://127.0.0.1:8006',
                   RATINGS_TESTS_URL='http://127.0.0.1:8004', RATINGS_CALCULATE_URL='http://127.0.0.1:8005',
                   RATINGS_EXAMS_MODE=mode, RATINGS_EXTERNAL_TESTS_MODE=external_mode, RATINGS_EXAMS_URL=previous.get('RATINGS_EXAMS_HOST_URL', ''),
                   RABBITMQ_URL=values['RABBITMQ_URL'])
    calculate = {key: values[key] for key in ['MONGO_USER', 'MONGO_PASSWORD', 'AUTH_INTERNAL_KEY']}
    calculate.update(MONGO_HOST='127.0.0.1', MONGO_PORT='27018', MONGO_DATABASE='cpm_dev',
                     CALCULATE_LISTEN_ADDR='127.0.0.1:8005', CALCULATE_TESTS_URL='http://127.0.0.1:8004',
                     CALCULATE_RATINGS_URL='http://127.0.0.1:8012', RABBITMQ_URL=values['RABBITMQ_URL'])
    write_private(local / 'ratings.env', ratings)
    write_private(local / 'calculate-ratings.env', calculate)
    write_private(previous_path, {'RATINGS_EXAMS_MODE': mode, 'RATINGS_EXTERNAL_TESTS_MODE': external_mode, 'RATINGS_EXAMS_URL': docker_exams,
                                 'RATINGS_EXAMS_HOST_URL': ratings['RATINGS_EXAMS_URL']})


def migrate(values):
    connection = mysql_connection(values)
    try:
        cursor = connection.cursor()
        try:
            cursor.execute('SELECT DATABASE()')
            if cursor.fetchone()[0] != 'cpm_dev':
                raise SystemExit('Migration refused: unexpected database.')
            for migration in sorted((ROOT / 'services/ratings/migrations').glob('*.sql')):
                for statement in migration.read_text().split(';'):
                    if statement.strip():
                        cursor.execute(statement)
            connection.commit()
        finally:
            cursor.close()
    finally:
        connection.close()
    client = mongo_client(values)
    try:
        database = client.cpm_dev
        name = 'calculate_ratings_inbox_v1'
        if name not in database.list_collection_names():
            database.create_collection(name)
        database[name].create_index([('job_id', 1), ('account_id', 1), ('received_at', -1)],
                                    name='rating_job_messages')
        database[name].create_index([('status', 1), ('next_attempt_at', 1), ('lease_until', 1),
                                     ('received_at', 1)], name='inbox_processing')
    finally:
        client.close()


def offline_build(include_sources=False, ratings_only=False, calculate_only=False):
    docker = shutil.which('docker') or '/Applications/Docker.app/Contents/Resources/bin/docker'
    go = shutil.which('go') or str(Path.home() / '.local/bin/go')
    services = ['calculate'] if calculate_only else (['ratings'] if ratings_only else (['core', 'tests', 'homework', 'calculate', 'ratings'] if include_sources else ['calculate', 'ratings']))
    for service in services:
        # Ratings can reuse the cached calculate runtime before its first image
        # exists. No registry download or application process is started.
        runtime = 'cpm-calculate:dev' if service == 'ratings' else f'cpm-{service}:dev'
        architecture = subprocess.check_output([docker, 'image', 'inspect', runtime, '--format',
                                                '{{.Architecture}}'], text=True).strip()
        if architecture not in ('arm64', 'amd64'):
            raise SystemExit('Unsupported cached image architecture.')
        target = BASE / 'local/ratings-offline' / service
        target.mkdir(mode=0o700, parents=True, exist_ok=True)
        environment = dict(os.environ, GOOS='linux', GOARCH=architecture, CGO_ENABLED='0', GOPROXY='off')
        subprocess.run([go, 'build', '-trimpath', '-ldflags=-s -w', '-o', str(target / service),
                        './cmd/' + service], cwd=ROOT / 'services' / service, env=environment, check=True)
        (target / 'Dockerfile').write_text(f'FROM {runtime}\nCOPY {service} /{service}\nENTRYPOINT ["/{service}"]\n')
        subprocess.run([docker, 'build', '--pull=false', '-t', f'cpm-{service}:dev', str(target)],
                       env=dict(os.environ, DOCKER_BUILDKIT='0'), check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['prepare', 'config', 'migrate', 'check', 'offline-build'],
                        nargs='?', default='prepare')
    parser.add_argument('--sources', action='store_true', help='Also rebuild the cached core/tests/homework runtime images.')
    parser.add_argument('--ratings-only', action='store_true', help='Rebuild only ratings without changing shared calculate.')
    parser.add_argument('--calculate-only', action='store_true', help='Rebuild only calculate after coordination with its Tests owner.')
    arguments = parser.parse_args()
    if sum([arguments.sources, arguments.ratings_only, arguments.calculate_only]) > 1:
        parser.error('Select only one rebuild scope')
    action = arguments.action
    values = guarded_settings()
    if action in ('prepare', 'config'):
        configure(values)
    if action in ('prepare', 'migrate'):
        migrate(values)
    if action == 'offline-build':
        offline_build(arguments.sources, arguments.ratings_only, arguments.calculate_only)
    if action != 'check':
        print('Ratings local dev preparation completed. Credentials retained in private files.')


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        # Connection errors must not expose credentials or complete URLs.
        raise SystemExit('Ratings dev operation failed (' + type(error).__name__ + ').') from None
