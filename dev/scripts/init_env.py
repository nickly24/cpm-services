"""Create local credentials once; never replace an existing .env."""
import secrets
from pathlib import Path

base = Path(__file__).resolve().parents[1]
path = base / '.env'
if path.exists():
    print('Existing .env retained.')
else:
    template = (base / '.env.example').read_text()
    lines = []
    for line in template.splitlines():
        key, sep, value = line.partition('=')
        if sep and ('PASSWORD' in key or key in {'JWT_SECRET_KEY', 'RABBITMQ_ERLANG_COOKIE'}):
            line = key + '=' + secrets.token_hex(32)
        lines.append(line)
    with path.open('x') as output:
        path.chmod(0o600)
        output.write('\n'.join(lines) + '\n')
    print('Created local .env with random credentials.')
