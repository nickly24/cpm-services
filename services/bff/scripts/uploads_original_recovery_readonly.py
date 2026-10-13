"""Read the original uncertain credential command; never send its source again."""
import argparse
import json
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
KEY = "44083500-b582-4c23-bd1c-67c2e9e367a7"
SESSION = "e5b5dd865773e7f45538d8f35c185d60"
OWNER_KEY = "58dc2fb7-1ccd-5ba8-b343-43e67358a1f9"
SCOPE = {"organization_id": 1001, "division_id": 10235, "actor_account_id": 3}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run", action="store_true")
    if not parser.parse_args().run:
        print(json.dumps({"status": "pending_explicit_UploadsREADY", "live_checks": 0}))
        return
    token = None
    requests, checks = [], []

    def request(port, path, method="GET", body=None, expected=200):
        # Exact route allowlist prohibits create/edit/commit/retry and new UUIDs.
        allowed = {
            (8002, "/v1/auth/login", "POST"), (8002, "/v1/auth/logout", "POST"),
            (8003, f"/v1/core/divisions/10235/imports/receipts/{OWNER_KEY}", "GET"),
            (8014, f"/v1/uploads/divisions/10235/receipts/{KEY}", "GET"),
            (8014, f"/v1/uploads/divisions/10235/sessions/{SESSION}", "GET"),
            (8014, f"/v1/uploads/divisions/10235/sessions/{SESSION}/reconcile", "POST"),
        }
        assert (port, path, method) in allowed
        headers = {"Content-Type": "application/json"}
        if token:
            headers["Authorization"] = "Bearer " + token
        payload = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(f"http://127.0.0.1:{port}{path}", data=payload, headers=headers, method=method)
        try:
            response = urllib.request.urlopen(req, timeout=30)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            status, raw = response.status, response.read((1 << 20) + 1)
        assert len(raw) <= 1 << 20 and status == expected, f"Unexpected status for {method} {path}: {status}"
        requests.append({"port": port, "method": method, "path": path, "status": status})
        value = json.loads(raw)
        if status < 300:
            assert value.get("status") is True
        return value.get("data", value)

    def scope(value):
        assert all(value.get(name) == expected for name, expected in SCOPE.items())

    try:
        password = json.loads((ROOT / "dev/local/accounts.json").read_text())["password"]
        login = request(8002, "/v1/auth/login", "POST", {"login": "dev_admin", "password": password,
                         "organization_id": 1001, "division_id": 10235})
        token = login["token"]
        del password, login
        receipt = request(8014, f"/v1/uploads/divisions/10235/receipts/{KEY}")
        scope(receipt["scope"])
        assert receipt["command_id"] == KEY and receipt["resource_id"] == SESSION and receipt["type"] == "users.credentials"
        checks.append("original receipt identity, resource and actor/org/division remain bound")
        session = request(8014, f"/v1/uploads/divisions/10235/sessions/{SESSION}")
        scope(session)
        assert session["id"] == SESSION and session["type"] == "users.credentials"
        assert session["state"] == "outcome_unknown" and not session.get("owner_session_id")
        checks.append("aged staging becomes honest outcome_unknown, without inventing an accepted preview")
        request(8003, f"/v1/core/divisions/10235/imports/receipts/{OWNER_KEY}", expected=404)
        checks.append("missing exact native receipt remains uncertainty, never proof of no execution")
        reconciled = request(8014, f"/v1/uploads/divisions/10235/sessions/{SESSION}/reconcile", "POST")
        scope(reconciled)
        assert reconciled["id"] == SESSION and reconciled["state"] == "outcome_unknown"
        assert not reconciled.get("owner_session_id")
        checks.append("reconcile reads the original command only and retains uncertainty")
        final = request(8014, f"/v1/uploads/divisions/10235/receipts/{KEY}")
        scope(final["scope"])
        assert final["command_id"] == KEY and final["resource_id"] == SESSION
        checks.append("same receipt/key retained; no create, source, password, commit or retry sent")
        report = {"status": "passed", "command_id": KEY, "session_id": SESSION, "scope": SCOPE,
                  "outcome": "outcome_unknown", "native_receipt_status": 404, "business_writes": 0,
                  "source_resends": 0, "new_command_keys": 0, "checks": checks, "requests": requests}
    finally:
        if token:
            request(8002, "/v1/auth/logout", "POST")
    report["auth_session_closed"] = True
    path = ROOT / "dev/local/bff-uploads-original-recovery-http-report.json"
    path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    path.chmod(0o600)
    print(json.dumps({"status": "passed", "checks": len(checks), "outcome": report["outcome"],
                      "source_resends": 0, "business_writes": 0, "auth_session_closed": True}))


if __name__ == "__main__":
    main()
