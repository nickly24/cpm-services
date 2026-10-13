#!/usr/bin/env bash
set -euo pipefail
[[ $# == 2 && $1 =~ ^(deploy|rollback)$ && $2 =~ ^prod-[0-9]+-[0-9a-f]{12}$ ]] || { echo 'usage: deploy.sh deploy|rollback RELEASE_ID' >&2; exit 2; }
mode=$1
release=/opt/cpm/releases/$2
exec 9>/run/lock/cpm-deploy.lock
flock -n 9 || { echo 'Another deployment is active' >&2; exit 1; }
# Files are from the verified workflow artifact; secrets and data stay outside releases.
python3 /opt/cpm/infra/scripts/release.py "$release" >/dev/null
previous=$(readlink -f /opt/cpm/current || true)
compose=(docker compose --project-name cpm-prod --env-file "$release/images.env" -f "$release/prod/compose.yaml")
"${compose[@]}" config --quiet
# Registry login uses the caller's short-lived token, never a permanent broad PAT.
if [[ $mode == deploy ]]; then
  export DOCKER_CONFIG
  DOCKER_CONFIG=$(mktemp -d)
  trap 'rm -rf "$DOCKER_CONFIG"' EXIT
  docker login ghcr.io --username nickly24 --password-stdin >/dev/null
  "${compose[@]}" pull --quiet
else
  python3 - "$release" <<'PY'
import json,subprocess,sys
from pathlib import Path
for image in json.loads((Path(sys.argv[1])/'release.json').read_text())['images'].values():
 subprocess.run(['docker','image','inspect',image],check=True,stdout=subprocess.DEVNULL)
PY
fi
systemctl stop cpm-watchdog.timer || true
finish() { systemctl start cpm-watchdog.timer || true; }
trap 'finish; if [[ -n ${DOCKER_CONFIG:-} ]]; then rm -rf "$DOCKER_CONFIG"; fi' EXIT
activate_ingress() {
  cp "$release/prod/nginx.conf" /opt/cpm/infra/nginx.conf
  docker compose --project-name cpm-infra --env-file /opt/cpm/infra/images.env -f /opt/cpm/infra/infra.compose.yaml --profile ingress up -d --no-deps nginx || return 1
  for attempt in {1..10}; do
    if curl --fail --silent --max-time 5 http://127.0.0.1/health/ready >/dev/null; then return 0; fi
    sleep 2
  done
  return 1
}
if "${compose[@]}" up -d --no-build --pull never && python3 "$release/prod/scripts/readiness.py" && activate_ingress; then
  if [[ -n $previous && $previous != "$release" ]]; then ln -sfn "$previous" /opt/cpm/previous; fi
  ln -sfn "$release" /opt/cpm/current
  echo "DEPLOYED $2"
else
  echo 'Release verification failed; restoring previous images' >&2
  if [[ -n $previous && -f $previous/release.json ]]; then
    docker compose --project-name cpm-prod --env-file "$previous/images.env" -f "$previous/prod/compose.yaml" up -d --no-build --pull never
    python3 "$previous/prod/scripts/readiness.py"
    cp "$previous/prod/nginx.conf" /opt/cpm/infra/nginx.conf
    docker compose --project-name cpm-infra --env-file /opt/cpm/infra/images.env -f /opt/cpm/infra/infra.compose.yaml --profile ingress up -d --no-deps --force-recreate nginx
    echo 'Previous release restored' >&2
  else
    docker compose --project-name cpm-infra --env-file /opt/cpm/infra/images.env -f /opt/cpm/infra/infra.compose.yaml --profile ingress stop nginx || true
    "${compose[@]}" stop
    echo 'First release failed; ingress and apps stopped, databases and volumes retained' >&2
  fi
  exit 1
fi
