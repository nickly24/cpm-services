"""Run the optional frozen Auth Dev profile; never prints credential values."""
import argparse
import os
import re
import stat
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'dev/scripts'))
from export_structure import read_env

KEY_NAMES = ('AUTH_OPERATIONS_ISSUER_KEY', 'AUTH_OPERATIONS_CORE_KEY',
    'AUTH_OPERATIONS_CARDS_KEY', 'AUTH_OPERATIONS_TESTS_KEY', 'AUTH_OPERATIONS_EXAMS_KEY',
    'CORE_OPERATION_DELIVERY_KEY', 'CARDS_OPERATION_DELIVERY_KEY',
    'TESTS_OPERATION_DELIVERY_KEY', 'EXAMS_OPERATIONS_DELIVERY_KEY', 'CARDS_CORE_OPERATION_KEY')


def credentials(path):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o600:
        raise ValueError('operation credentials must be a regular file with mode 0600')
    seen = set()
    for line in path.read_text().splitlines():
        if not line.strip() or line.lstrip().startswith('#'):
            continue
        name, sep, _ = line.partition('=')
        if not sep or name.strip() in seen:
            raise ValueError('operation credential file has malformed or duplicate fields')
        seen.add(name.strip())
    values = read_env(path)
    keys = []
    for name in KEY_NAMES:
        value = values.get(name, '')
        if not isinstance(value, str) or not 32 <= len(value) <= 256 or value != value.strip():
            raise ValueError('operation credentials are incomplete or malformed')
        if name in os.environ and os.environ[name] != value:
            raise ValueError('shell operation credential override is forbidden')
        keys.append(value)
    if len(set(keys)) != len(keys):
        raise ValueError('operation credential audiences must be distinct')
    if values.get('CORE_CARDS_OPERATION_KEY', keys[-1]) != keys[-1]:
        raise ValueError('Cards/Core dependency aliases differ')
    if 'CORE_CARDS_OPERATION_KEY' in os.environ and os.environ['CORE_CARDS_OPERATION_KEY'] != keys[-1]:
        raise ValueError('shell dependency credential override is forbidden')
    base = read_env(ROOT / 'dev/.env')
    if base.get('AUTH_INTERNAL_KEY') in keys:
        raise ValueError('operation credentials cannot reuse the existing internal key')
    return {name: values[name] for name in KEY_NAMES}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', required=True, help='Frozen immutable Auth image reference')
    parser.add_argument('--check', action='store_true', help='Validate configuration without docker or mutation')
    args = parser.parse_args()
    if not re.fullmatch(r'sha256:[0-9a-f]{64}', args.image):
        parser.error('an immutable frozen Auth sha256 image ID is required')
    try:
        values = credentials(ROOT / 'dev/local/import-operation-credentials.env')
    except (OSError, ValueError):
        parser.error('private operation configuration is unavailable or invalid; no runtime changed')
    if args.check:
        print('Auth optional operation profile configuration valid; no runtime changed.')
        return
    environment = {**os.environ, **values, 'AUTH_OPERATIONS_IMAGE': args.image}
    subprocess.run(['/Applications/Docker.app/Contents/Resources/bin/docker',
        'compose', '--env-file', str(ROOT / 'dev/.env'), '-f', str(ROOT / 'dev/compose.yaml'),
        '-f', str(ROOT / 'dev/compose.auth-operations.yaml'), 'up', '-d', '--no-deps',
        '--no-build', '--pull', 'never', 'auth'], cwd=ROOT, env=environment, check=True)


if __name__ == '__main__':
    main()
