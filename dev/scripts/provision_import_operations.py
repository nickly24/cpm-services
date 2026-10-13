"""Sole-provisioner tool for ten distinct local Dev operation credentials."""
import argparse
import os
import secrets
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'services/auth/scripts'))
from run_operation_profile import KEY_NAMES, credentials
sys.path.insert(0, str(ROOT / 'dev/scripts'))
from export_structure import read_env


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run', action='store_true', help='Provision only after frozen-source checks and a newly ACKed window')
    args = parser.parse_args()
    if not args.run:
        print('Operation keys NOT provisioned; coordinated Dev window required.')
        return
    path = ROOT / 'dev/local/import-operation-credentials.env'
    if path.exists() or path.is_symlink():
        credentials(path)
        print('Existing valid operation credentials retained; zero rotations.')
        return
    previous = {read_env(ROOT / p).get('AUTH_INTERNAL_KEY') for p in ['dev/.env', 'dev/local/auth.env']}
    values = {}
    while len(values) < len(KEY_NAMES):
        value = secrets.token_hex(32)
        if value not in previous and value not in values.values():
            values[KEY_NAMES[len(values)]] = value
    text = '\n'.join(f'{name}={values[name]}' for name in KEY_NAMES)
    text += '\nCORE_CARDS_OPERATION_KEY=' + values['CARDS_CORE_OPERATION_KEY'] + '\n'
    temporary = path.with_name('.import-operations-' + secrets.token_hex(12) + '.tmp')
    descriptor = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    try:
        with os.fdopen(descriptor, 'w') as output:
            output.write(text)
            output.flush()
            os.fsync(output.fileno())
        # Atomic publication without replacing another writer's existing file.
        os.link(temporary, path, follow_symlinks=False)
        credentials(path)
    finally:
        temporary.unlink(missing_ok=True)
    print('Ten distinct operation credentials provisioned in fixed mode0600 file; existing internal key retained.')


if __name__ == '__main__':
    main()
