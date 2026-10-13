"""Real Auth/Core/Cards grant acceptance. Run only in an agreed dev window.

Does not restart services or provision credentials. Supply existing private keys
through environment or --credentials-file (JSON object or KEY=value env file).
Own synthetic role/account/theme/session fixtures only. This is owner acceptance,
not proof of UI confirmation, Uploads persistence or Rabbit delivery.
"""
import argparse
import copy
import hashlib
import json
import os
import secrets
import sys
import urllib.error
import urllib.request
import uuid
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timedelta, timezone
from pathlib import Path

from support import HOSTS, ROOT, api, checks, expect, login, request, save, settings, xlsx
from uploads_contract_test import card, parse, sql_card_count

RUN = secrets.token_hex(8)
RESOURCES = {"run_id": RUN, "sessions": [], "grants": []}


def credentials(path):
    values = dict(os.environ)
    if path:
        p = Path(path)
        if p.stat().st_mode & 0o077:
            raise AssertionError("Credentials file must be private (0600)")
        raw = p.read_text()
        if raw.lstrip().startswith("{"):
            values.update(json.loads(raw))
        else:
            for line in raw.splitlines():
                if not line.strip() or line.lstrip().startswith("#"):
                    continue
                key, sep, value = line.partition("=")
                if sep:
                    values[key.strip()] = value.strip().strip("\"'")
    names = ["AUTH_OPERATIONS_ISSUER_KEY", "CARDS_OPERATION_DELIVERY_KEY"]
    result = [values.get(name, "") for name in names]
    expect(all(len(v) >= 32 for v in result) and result[0] != result[1], "existing distinct credentials are configured")
    return result


def raw_request(service, path, headers, body=None, method="GET", code=200):
    req = urllib.request.Request(HOSTS[service] + path, headers=headers, data=body, method=method)
    try:
        with urllib.request.urlopen(req, timeout=40) as response:
            status, raw = response.status, response.read()
    except urllib.error.HTTPError as error:
        status, raw = error.code, error.read()
    try:
        result = json.loads(raw)
    except ValueError:
        result = {}
    expected = code if isinstance(code, tuple) else (code,)
    if status not in expected:
        # No headers, bodies, bindings or secrets in a failing journal.
        raise AssertionError(f"{service} {method} {path.split('?')[0]} expected {expected}, got {status}; error={result.get('error', 'unknown')}")
    checks.append(f"{service} {method} {path.split('?')[0]} -> {status}")
    return result


def main(args):
    settings()
    issuer, delivery = credentials(args.credentials_file)
    fixture = json.loads((ROOT / "dev/local/cards-fixtures.json").read_text())
    password = json.loads((ROOT / "dev/local/accounts.json").read_text())["password"]
    admin = login("dev_admin", password)
    direction = fixture["direction"]
    permissions = {key: {"view": True, "edit": True} for key in ["train", "upload"]}
    name = "DEV Cards grants " + RUN
    role = api("core", "/v1/core/roles", admin, "POST", {"name": name, "permissions": permissions}, 201)
    actor_name, actor_password = "dev_cards_grants_" + RUN, secrets.token_urlsafe(24)
    actor = api("core", "/v1/core/divisions/1001/users", admin, "POST", {
        "role": "staff_admin", "full_name": name, "login": actor_name,
        "password": actor_password, "role_id": role["id"], "tg_name": "",
    }, 201)
    token = login(actor_name, actor_password)
    RESOURCES.update(role_id=role["id"], account_id=actor["account_id"])
    theme = card("/themes", token, "POST", {"name": name, "direction_id": direction})
    RESOURCES["theme_id"] = theme["id"]

    def auth(path="", body=None, key=None, code=200):
        headers = {"X-Service-Key": issuer}
        if key is not None:
            headers["Idempotency-Key"] = key
        return request("auth", "/internal/auth/operation-grants" + path, token, "POST", body if body is not None else {}, code, headers=headers)

    def plan(label):
        source = xlsx([["Вопрос", "Ответ"], [name + " " + label, "Answer\n\nSecond line"]])
        created = parse(token, source, direction, theme["id"], str(uuid.uuid4()))
        session = card("/sessions/" + created["session_id"], token)
        RESOURCES["sessions"].append(session["session_id"])
        proof = session["authorization_proof"]
        expect(proof["source_sha256"] == hashlib.sha256(source).hexdigest(), "native source digest matches uploaded bytes")
        body = json.dumps({"expected_revision": session["revision"]}, separators=(",", ":")).encode()
        expect(proof["owner_request_sha256"] == hashlib.sha256(body).hexdigest(), "native request digest is published independently of wire")
        binding = {
            "contract_version": 1, "upload_id": secrets.token_hex(16), "upload_session_id": secrets.token_hex(16),
            "job_id": secrets.token_hex(16), "coordinator_command_key": "cards_acceptance_" + secrets.token_hex(8),
            "type": "cards.xlsx", "owner": "cards", "native_command_key": str(uuid.uuid4()),
            "owner_session_id": session["session_id"], "upload_preview_revision": "1",
            "owner_preview_revision": str(session["revision"]), "target_revision": None,
            "target": proof["target"], "source_sha256": proof["source_sha256"],
            "owner_proof": {"scheme": proof["scheme"], "validation_sha256": proof["sha256"], "input_sha256": None, "mapping_sha256": None},
            "owner_request_sha256": proof["owner_request_sha256"], "apply_wire_body_sha256": hashlib.sha256(body).hexdigest(),
            "preview_wire_body_sha256": None,
            "upload_expires_at": (datetime.now(timezone.utc) + timedelta(hours=2)).isoformat().replace("+00:00", "Z"),
            "owner_preview_expires_at": session["expires_at"], "required_division_ids": ["1001"],
        }
        return {"binding": binding, "body": body, "session": session}

    def issue(p):
        key = str(uuid.uuid4())
        out = auth(body=p["binding"], key=key)
        expect(out["status"] and out["mode"] == "apply", "issue returns an apply grant")
        expect(out["actor"]["account_id"] == actor["account_id"], "grant preserves initiator account identity")
        expect(out["actor"]["id"] != out["actor"]["account_id"], "real Auth fixture exercises distinct profile and account IDs")
        RESOURCES["profile_id"] = out["actor"]["id"]
        again = auth(body=p["binding"], key=key)
        expect(again["grant_id"] == out["grant_id"] and again["expires_at"] == out["expires_at"], "issuance replay preserves grant and expiry")
        changed = copy.deepcopy(p["binding"])
        changed["job_id"] = secrets.token_hex(16)
        auth(body=changed, key=key, code=409)
        p["grant"] = out["grant_id"]
        RESOURCES["grants"].append(out["grant_id"])
        return p

    def send(p, stage, code=200, body=None, headers=None, suffix=""):
        b = p["binding"]
        h = {"X-CPM-Operation-Grant": p["grant"], "X-CPM-Operation-Stage": stage,
             "X-CPM-Operation-Job": b["job_id"], "X-CPM-Operation-Service-Key": delivery,
             "Idempotency-Key": b["native_command_key"]}
        h.update(headers or {})
        if stage == "receipt":
            path = "/v1/cards/receipts/" + b["native_command_key"]
        else:
            path = "/v1/cards/sessions/" + b["owner_session_id"] + ("/commit" if stage == "apply" else "")
        if stage == "apply":
            h["Content-Type"] = "application/json"
        return raw_request("cards", path + suffix, h, p["body"] if stage == "apply" and body is None else body,
                           "POST" if stage == "apply" else "GET", code)

    p = issue(plan("concurrent"))
    send(p, "preview")
    send(p, "receipt", 404)
    before = sql_card_count(theme["id"], 1001)
    send(p, "apply", 409, body=p["body"] + b"\n")
    send(p, "apply", 403, headers={"X-CPM-Operation-Service-Key": "invalid"})
    send(p, "apply", (400, 401, 403), headers={"Authorization": "Bearer " + token})
    send(p, "apply", (400, 403), suffix="?")
    send(p, "apply", 409, headers={"X-CPM-Operation-Job": secrets.token_hex(16)})
    send(p, "apply", 409, headers={"Idempotency-Key": str(uuid.uuid4())})
    expect(sql_card_count(theme["id"], 1001) == before, "invalid requests made no domain changes")
    with ThreadPoolExecutor(2) as pool:
        outputs = list(pool.map(lambda _: send(p, "apply")["data"], range(2)))
    expect(outputs[0] == outputs[1], "concurrent deliveries return the same immutable result")
    expect(sql_card_count(theme["id"], 1001) == before + 1, "concurrent delivery inserts exactly one row")
    receipt = send(p, "receipt")["data"]
    expect(receipt["result"] == outputs[0], "lost-response recovery reads original result")
    expect(receipt["request_hash"] == p["binding"]["owner_request_sha256"], "receipt proves the original native request hash")

    # Revoke each live permission on this run's own role, including receipt reads.
    try:
        for permission in ["train", "upload"]:
            restricted = copy.deepcopy(permissions)
            restricted[permission]["edit"] = False
            api("core", "/v1/core/roles/" + str(role["id"]), admin, "PUT", {"name": name, "permissions": restricted})
            send(p, "receipt", 403)
            send(p, "apply", 403)
            api("core", "/v1/core/roles/" + str(role["id"]), admin, "PUT", {"name": name, "permissions": permissions})
    finally:
        api("core", "/v1/core/roles/" + str(role["id"]), admin, "PUT", {"name": name, "permissions": permissions})
    send(p, "receipt")
    auth("/" + p["grant"] + "/revoke")
    send(p, "receipt", 410)
    send(p, "apply", 410)
    recovery_key = str(uuid.uuid4())
    recovered = auth("/" + p["grant"] + "/recover", key=recovery_key)
    expect(recovered["mode"] == "receipt" and recovered["binding"] == p["binding"], "explicit recovery preserves all immutable binding fields")
    recovered_again = auth("/" + p["grant"] + "/recover", key=recovery_key)
    expect(recovered_again["grant_id"] == recovered["grant_id"], "recovery issuance is idempotent")
    q = dict(p, grant=recovered["grant_id"])
    RESOURCES["grants"].append(q["grant"])
    expect(send(q, "receipt")["data"]["result"] == outputs[0], "receipt-only grant and Core dependency recover committed result")
    send(q, "apply", (403, 409))
    send(q, "preview", (403, 409))
    expect(sql_card_count(theme["id"], 1001) == before + 1, "receipt-only recovery creates no additional rows")

    stale = issue(plan("stale"))
    session = stale["session"]
    rows = [{"row": row["row"], "question": row["question"], "answer": row["answer"] + " edited", "skip": False} for row in session["preview"]["cards"]]
    card("/sessions/" + session["session_id"], token, "PUT", {"expected_revision": session["revision"], "cards": rows}, key=str(uuid.uuid4()))
    send(stale, "apply", 409)
    expect(sql_card_count(theme["id"], 1001) == before + 1, "edited preview invalidates previously issued proof before any write")

    # A single browser logout must not revoke an already confirmed background job.
    logout_plan = issue(plan("after logout"))
    request("auth", "/v1/auth/logout", token, "POST", {})
    send(logout_plan, "apply")
    send(logout_plan, "receipt")
    expect(sql_card_count(theme["id"], 1001) == before + 2, "single-session logout preserves confirmed job authorization")
    return {"status": "passed", "checks": len(checks), "details": checks, "resources": RESOURCES,
            "scope": "Native Cards grant HTTP acceptance with real Auth/Core/MySQL; UI/Uploads/Rabbit acceptance is separate",
            "not_covered": ["real TTL expiration", "account epoch and membership changes", "Auth transport outage", "UI confirmation and Rabbit delivery"]}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--credentials-file")
    args = parser.parse_args()
    report = ROOT / "dev/local/cards-operation-grants-http-report.json"
    try:
        result = main(args)
        save(report, result)
        print(f'Cards operation grants: {result["checks"]} checks passed; report {report}')
    except Exception as error:
        save(report, {"status": "failed", "checks": len(checks), "details": checks, "resources": RESOURCES,
                      "error_type": type(error).__name__, "error": str(error) if isinstance(error, AssertionError) else "Local exception; credentials omitted"})
        print("Cards operation grants failed; inspect private report (credentials omitted).", file=sys.stderr)
        sys.exit(1)
