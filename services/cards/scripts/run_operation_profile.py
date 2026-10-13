"""Deploy the frozen Cards image only after coordinator GO in an exact UTC window.

Default is a read-only source/private-configuration check. No keys are generated,
no build/pull/migration or other-service recreation is allowed by this runner.
"""
import argparse
from datetime import datetime, timezone
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[3]
DOCKER = '/Applications/Docker.app/Contents/Resources/bin/docker'
NAMES = ('AUTH_OPERATIONS_CARDS_KEY', 'CARDS_OPERATION_DELIVERY_KEY', 'CARDS_CORE_OPERATION_KEY')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run', action='store_true')
    parser.add_argument('--not-before')
    parser.add_argument('--deadline')
    args = parser.parse_args()
    freeze = json.loads((ROOT / 'services/cards/docs/operation-grants-source-freeze.json').read_text())
    manifest = ROOT / 'services/cards/docs' / freeze['source_manifest']
    if hashlib.sha256(manifest.read_bytes()).hexdigest() != freeze['source_manifest_sha256']:
        raise ValueError('Cards source manifest differs')
    for line in manifest.read_text().splitlines():
        digest, relative = line.split('  ', 1)
        path = ROOT / 'services/cards' / relative
        if hashlib.sha256(path.read_bytes()).hexdigest() != digest:
            raise ValueError('Cards frozen source differs')
    # Reuse Auth's already reviewed regular-0600/distinct/no-override key guard.
    guard_path = ROOT / 'services/auth/scripts/run_operation_profile.py'
    auth_freeze = json.loads((ROOT / 'services/auth/docs/operation-grants-source-freeze.json').read_text())
    guard_hash = next(f['sha256'] for f in auth_freeze['files'] if f['path'] == str(guard_path.relative_to(ROOT)))
    if hashlib.sha256(guard_path.read_bytes()).hexdigest() != guard_hash:
        raise ValueError('Auth private configuration guard differs')
    spec = importlib.util.spec_from_file_location('auth_operation_private_guard', guard_path)
    guard = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(guard)
    values = guard.credentials(ROOT / 'dev/local/import-operation-credentials.env')
    if not args.run:
        print('Cards frozen source and private configuration valid; no runtime changed.')
        return
    if not args.not_before or not args.deadline:
        raise ValueError('Explicit approved UTC deployment window is required')
    start = datetime.fromisoformat(args.not_before.replace('Z', '+00:00'))
    end = datetime.fromisoformat(args.deadline.replace('Z', '+00:00'))
    now = datetime.now(timezone.utc)
    if start.utcoffset().total_seconds() != 0 or end.utcoffset().total_seconds() != 0 or not start <= now < end:
        raise ValueError('Outside approved deployment window')
    image = freeze['candidate_image_id']
    inspected = subprocess.run([DOCKER, 'image', 'inspect', image, '--format', '{{.Id}}'], capture_output=True, text=True, check=True)
    if inspected.stdout.strip() != image:
        raise ValueError('Frozen immutable Cards image is unavailable')
    environment = dict(os.environ)
    environment.update({name: values[name] for name in NAMES})
    overlay = json.dumps({'services': {'cards': {'image': image}}})
    command = [DOCKER, 'compose', '--env-file', str(ROOT / 'dev/.env'), '-f', str(ROOT / 'dev/compose.yaml'),
               '-f', str(ROOT / 'dev/compose.cards.yaml'), '-f', '-', '--profile', 'cards',
               'up', '-d', '--no-deps', '--no-build', '--pull', 'never', 'cards']
    timeout = max(1, min(90, int((end - datetime.now(timezone.utc)).total_seconds())))
    result = subprocess.run(command, cwd=ROOT, env=environment, input=overlay, text=True, capture_output=True, timeout=timeout)
    if result.returncode:
        raise ValueError('Cards-only compose failed; inspect container metadata before retry')
    print('Cards-only immutable image recreation finished; readiness verification remains required.')


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print('Cards deployment guard/command failed (' + type(error).__name__ + '); credential values omitted.', file=sys.stderr)
        sys.exit(1)
