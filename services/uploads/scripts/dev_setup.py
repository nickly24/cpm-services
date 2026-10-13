"""Add uploads-owned tables in guarded local cpm_dev; never change existing seeds."""
import json
import sys
from pathlib import Path
ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'dev/scripts'))
from bootstrap import settings, mysql_connection
from rabbit_env import read_values
from homework_env import write_private

def prepare():
    values = read_values(ROOT / "dev/.env")
    values.setdefault("MYSQL_HOST", "127.0.0.1")
    if values['MYSQL_HOST'] != '127.0.0.1' or str(values['MYSQL_PORT']) != '13306' or values['MYSQL_DATABASE'] != 'cpm_dev':
        raise SystemExit('Only isolated local cpm_dev supported')
    conn = mysql_connection(settings())
    try:
        with conn.cursor() as cursor:
            cursor.execute('SELECT DATABASE()')
            assert cursor.fetchone()[0] == 'cpm_dev'
            for migration in sorted((ROOT / 'services/uploads/migrations').glob('*.sql')):
                # These owned migrations contain plain statements, with full-line
                # comments; punctuation in comments must not become SQL fragments.
                source = '\n'.join(line for line in migration.read_text().splitlines()
                                   if not line.lstrip().startswith('--'))
                for statement in source.split(';'):
                    if statement.strip():
                        cursor.execute(statement)
                        if cursor.with_rows: cursor.fetchall()
        conn.commit()
    finally: conn.close()
    # Merge the latest allowlist; preserve additions from other service chats.
    allow = ROOT / 'dev/schema/extensions.json'
    names = set(json.loads(allow.read_text()))
    names.update('uploads_' + name + '_v1' for name in ['sessions', 'files', 'jobs', 'receipts', 'events', 'outbox', 'inbox', 'previews', 'execution_plans'])
    allow.write_text(json.dumps(sorted(names), indent=2) + '\n')
    write_private(ROOT / 'dev/local/uploads.env', {
        'UPLOADS_DEV_MODE': 'true', 'UPLOADS_LISTEN_ADDR': '127.0.0.1:8014',
        **{k: str(values[k]) for k in ['MYSQL_HOST','MYSQL_PORT','MYSQL_DATABASE','MYSQL_USER','MYSQL_PASSWORD']},
        'UPLOADS_RABBITMQ_URL': values['RABBITMQ_URL'],
        'UPLOADS_INTERNAL_KEY': values['AUTH_INTERNAL_KEY'],
    })
    print('Uploads additive schema and private local environment ready; no owner data changed.')
if __name__ == '__main__': prepare()
