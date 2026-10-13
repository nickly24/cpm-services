"""Prepare shared local RabbitMQ settings. No Docker, network or DB operations."""
import os
import re
import secrets
from pathlib import Path
from urllib.parse import quote

BASE = Path(__file__).resolve().parents[1]


def read_values(path):
    values = {}
    for line in path.read_text().splitlines():
        if line.lstrip().startswith('#'):
            continue
        key, sep, value = line.partition('=')
        if sep:
            values[key.strip()] = value.strip().strip('\"\'')
    return values


def prepare():
    path = BASE / '.env'
    if not path.exists():
        raise SystemExit('Prepare dev/.env with dev/scripts/init_env.py first.')
    values = read_values(path)
    if values.get('MYSQL_DATABASE') != 'cpm_dev' or values.get('MONGO_DATABASE') != 'cpm_dev':
        raise SystemExit('RabbitMQ preparation is restricted to the local cpm_dev environment.')
    defaults = {
        'RABBITMQ_IMAGE': 'rabbitmq:4.3.6-management',
        'RABBITMQ_HOST': '127.0.0.1',
        'RABBITMQ_PORT': '5672',
        'RABBITMQ_MANAGEMENT_PORT': '15672',
        'RABBITMQ_USER': 'cpm_dev',
        'RABBITMQ_VHOST': 'cpm_dev',
    }
    added = dict(defaults)
    for key in ['RABBITMQ_PASSWORD', 'RABBITMQ_ERLANG_COOKIE']:
        current = values.get(key, '')
        if current and not current.startswith('replace-with-'):
            added[key] = current
        else:
            added[key] = secrets.token_hex(32)
    for key, default in defaults.items():
        added[key] = values.get(key) or default
    if added['RABBITMQ_HOST'] != '127.0.0.1' or added['RABBITMQ_VHOST'] != 'cpm_dev':
        raise SystemExit('Only loopback RabbitMQ and the cpm_dev vhost are supported.')
    for key in ['RABBITMQ_PORT', 'RABBITMQ_MANAGEMENT_PORT']:
        try:
            port = int(added[key])
        except ValueError:
            raise SystemExit('Invalid RabbitMQ local port.')
        if not 1024 <= port <= 65535:
            raise SystemExit('Invalid RabbitMQ local port.')
        added[key] = str(port)
    if added['RABBITMQ_PORT'] == added['RABBITMQ_MANAGEMENT_PORT']:
        raise SystemExit('AMQP and management must use different local ports.')
    for key in ['RABBITMQ_USER', 'RABBITMQ_PASSWORD', 'RABBITMQ_ERLANG_COOKIE']:
        if not re.fullmatch(r'[A-Za-z0-9_.-]+', added[key]):
            raise SystemExit('RabbitMQ credentials must be safe unquoted environment values.')
    if len(added['RABBITMQ_PASSWORD']) < 32 or len(added['RABBITMQ_ERLANG_COOKIE']) < 32:
        raise SystemExit('RabbitMQ password and Erlang cookie must contain at least 32 characters.')
    user, password, vhost = [quote(added[key], safe='') for key in
                             ['RABBITMQ_USER', 'RABBITMQ_PASSWORD', 'RABBITMQ_VHOST']]
    credentials = f'{user}:{password}'
    added['RABBITMQ_URL'] = f'amqp://{credentials}@127.0.0.1:{added["RABBITMQ_PORT"]}/{vhost}'
    added['RABBITMQ_DOCKER_URL'] = f'amqp://{credentials}@rabbitmq:5672/{vhost}'
    added['RABBITMQ_MANAGEMENT_URL'] = f'http://127.0.0.1:{added["RABBITMQ_MANAGEMENT_PORT"]}'

    # Preserve other services' settings and existing real credentials.
    content = []
    written = set()
    for line in path.read_text().splitlines():
        key, sep, _ = line.partition('=')
        if sep and not line.lstrip().startswith('#') and key.strip() in added:
            key = key.strip()
            if key not in written:
                content.append(f'{key}={added[key]}')
                written.add(key)
        else:
            content.append(line)
    if written != set(added):
        content.extend(['', '# Shared local RabbitMQ settings (generated; do not commit).'])
        content.extend(f'{key}={value}' for key, value in added.items() if key not in written)
    with path.open('w') as output:
        os.chmod(path, 0o600)
        output.write('\n'.join(content) + '\n')

    local = BASE / 'local'
    local.mkdir(mode=0o700, parents=True, exist_ok=True)
    env = local / 'rabbitmq.env'
    # The common .env remains authoritative; this is a generated Mac overlay.
    with env.open('w') as output:
        os.chmod(env, 0o600)
        output.write('\n'.join(f'{key}={value}' for key, value in added.items()) + '\n')
    print('Shared RabbitMQ settings prepared. Broker not started; no queues created.')


if __name__ == '__main__':
    prepare()
