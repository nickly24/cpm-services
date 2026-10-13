"""Explicit optional Uploads profile; provisioned by the sole Auth credential writer."""
import argparse
import os
import re
import stat
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'dev/scripts'))
from rabbit_env import read_values

KEY_NAMES = ('AUTH_OPERATIONS_ISSUER_KEY', 'AUTH_OPERATIONS_CORE_KEY',
    'AUTH_OPERATIONS_CARDS_KEY', 'AUTH_OPERATIONS_TESTS_KEY', 'AUTH_OPERATIONS_EXAMS_KEY',
    'CORE_OPERATION_DELIVERY_KEY', 'CARDS_OPERATION_DELIVERY_KEY',
    'TESTS_OPERATION_DELIVERY_KEY', 'EXAMS_OPERATIONS_DELIVERY_KEY', 'CARDS_CORE_OPERATION_KEY')
UPLOADS_KEYS = (KEY_NAMES[0], *KEY_NAMES[5:9])


def operation_credentials(path):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o600:
        raise ValueError('private credentials must be a regular file with mode 0600')
    seen = set()
    for line in path.read_text().splitlines():
        if not line.strip() or line.lstrip().startswith('#'):
            continue
        name, sep, _ = line.partition('=')
        if not sep or name.strip() in seen:
            raise ValueError('duplicate or malformed credential fields')
        seen.add(name.strip())
    values = read_values(path)
    keys = []
    for name in KEY_NAMES:
        value = values.get(name, '')
        if not isinstance(value, str) or not 32 <= len(value) <= 256 or value != value.strip():
            raise ValueError('operation credentials are incomplete or malformed')
        if name in os.environ and os.environ[name] != value:
            raise ValueError('conflicting shell credentials are forbidden')
        keys.append(value)
    if len(set(keys)) != len(keys):
        raise ValueError('operation audiences must use distinct credentials')
    if values.get('CORE_CARDS_OPERATION_KEY', keys[-1]) != keys[-1]:
        raise ValueError('Cards/Core dependency aliases differ')
    base = read_values(ROOT / 'dev/.env')
    if base.get('AUTH_INTERNAL_KEY') in keys:
        raise ValueError('existing internal key cannot authorize operations')
    return {name: values[name] for name in UPLOADS_KEYS}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', required=True)
    parser.add_argument('--check', action='store_true')
    args = parser.parse_args()
    if not re.fullmatch(r'sha256:[0-9a-f]{64}', args.image):
        parser.error('immutable frozen Uploads sha256 image ID required')
    try:
        values = operation_credentials(ROOT / 'dev/local/import-operation-credentials.env')
    except (OSError, ValueError):
        parser.error('private operation configuration invalid; no runtime changed')
    if args.check:
        print('Uploads optional profile valid; no runtime changed.')
        return
    environment = {**os.environ, **values, 'UPLOADS_OPERATIONS_ENABLED': '1',
        'UPLOADS_OPERATIONS_IMAGE': args.image}
    subprocess.run(['/Applications/Docker.app/Contents/Resources/bin/docker', 'compose',
        '--project-directory', str(ROOT / 'dev'), '--env-file', str(ROOT / 'dev/.env'),
        '-f', str(ROOT / 'dev/compose.yaml'), '-f', str(ROOT / 'dev/compose.uploads.yaml'),
        '--profile', 'uploads', 'up', '-d', '--no-deps', '--no-build', '--pull', 'never',
        'uploads'], cwd=ROOT, env=environment, check=True)


if __name__ == '__main__':
    main()
