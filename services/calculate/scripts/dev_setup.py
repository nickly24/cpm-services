"""Prepare calculate's isolated collection and environment on fixed local dev."""
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'dev/scripts'))
from bootstrap import settings, mongo_client
from rabbit_env import read_values


def main():
    values = settings()
    rabbit = read_values(ROOT / 'dev/.env')
    if not rabbit.get('RABBITMQ_URL'):
        raise SystemExit('Run ./dev/dev rabbit-prepare first.')
    client = mongo_client(values)
    try:
        db = client.cpm_dev
        name = 'calculate_inbox_v1'
        if name not in db.list_collection_names():
            db.create_collection(name)
        db[name].create_index([('submission_id', 1), ('received_at', -1), ('_id', -1)], name='submission_messages')
        db[name].create_index([('test_id', 1), ('received_at', -1), ('_id', -1)], name='test_messages')
        db[name].create_index([('status', 1), ('next_attempt_at', 1), ('lease_until', 1), ('received_at', 1)], name='inbox_processing')
    finally:
        client.close()
    env = {
        'MONGO_HOST': '127.0.0.1', 'MONGO_PORT': '27018', 'MONGO_DATABASE': 'cpm_dev',
        'MONGO_USER': values['MONGO_USER'], 'MONGO_PASSWORD': values['MONGO_PASSWORD'],
        'CALCULATE_LISTEN_ADDR': '127.0.0.1:8005',
        'CALCULATE_TESTS_URL': 'http://127.0.0.1:8004',
        'AUTH_INTERNAL_KEY': values['AUTH_INTERNAL_KEY'], 'RABBITMQ_URL': rabbit['RABBITMQ_URL'],
    }
    path = ROOT / 'dev/local/calculate.env'
    path.write_text('\n'.join(f'{key}={value}' for key, value in env.items()) + '\n')
    path.chmod(0o600)
    print('Local calculate inbox and environment prepared. Legacy data retained.')


if __name__ == '__main__':
    main()
