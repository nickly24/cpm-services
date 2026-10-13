#!/usr/bin/env python3
"""Run broker tests in a disposable local dev vhost, then delete only that vhost."""
import base64
import json
import os
from pathlib import Path
import shlex
import shutil
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request
import uuid


def main():
    service = Path(__file__).resolve().parents[1]
    workspace = service.parents[1]
    values = {}
    for line in (workspace / "dev/.env").read_text().splitlines():
        if not line.strip() or line.lstrip().startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        parsed = shlex.split(value, comments=True)
        values[key.strip()] = parsed[0] if parsed else ""
    management = urllib.parse.urlsplit(values["RABBITMQ_MANAGEMENT_URL"])
    broker = urllib.parse.urlsplit(values["RABBITMQ_URL"])
    if management.hostname not in {"127.0.0.1", "localhost"} or broker.hostname not in {"127.0.0.1", "localhost"}:
        raise RuntimeError("broker fixture must use localhost dev endpoints")
    user, password = values["RABBITMQ_USER"], values["RABBITMQ_PASSWORD"]
    token = base64.b64encode(f"{user}:{password}".encode()).decode()
    management_base = urllib.parse.urlunsplit((management.scheme, management.netloc, management.path.rstrip("/"), "", ""))
    vhost = "filework-tests-" + str(uuid.uuid4())
    encoded_vhost = urllib.parse.quote(vhost, safe="")

    def request(method, path, body=None):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(management_base + path, data=data, method=method,
                                     headers={"Authorization": "Basic " + token, "Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=10) as response:
            response.read()

    created = False
    try:
        request("PUT", "/api/vhosts/" + encoded_vhost, {})
        created = True
        request("PUT", "/api/permissions/" + encoded_vhost + "/" + urllib.parse.quote(user, safe=""),
                {"configure": ".*", "write": ".*", "read": ".*"})
        host = broker.hostname + ":" + str(broker.port or 5672)
        auth = urllib.parse.quote(user, safe="") + ":" + urllib.parse.quote(password, safe="") + "@"
        env = dict(os.environ)
        env["RABBITMQ_TEST_URL"] = urllib.parse.urlunsplit((broker.scheme, auth + host, "/" + encoded_vhost, "", ""))
        go = shutil.which("go") or str(Path.home() / ".local/bin/go")
        result = subprocess.run([go, "test", "-race", "-count=1", "-v", "-run", "TestRealBroker", "./internal/filework"],
                                cwd=service, env=env, check=False)
        return result.returncode
    finally:
        if created:
            request("DELETE", "/api/vhosts/" + encoded_vhost)
            print("Disposable RabbitMQ test vhost removed.")


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, KeyError, RuntimeError, urllib.error.URLError):
        print("Local RabbitMQ fixture setup or cleanup failed; credentials were not printed.", file=sys.stderr)
        sys.exit(1)
