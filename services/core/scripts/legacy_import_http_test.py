"""Native Core users.legacy/credentials acceptance, fresh synthetic HTTP data only.

Run only after the coordinator deploys Core and migrations 005/006. No SQL,
Docker, Rabbit or runtime control; the run leaves its synthetic demo intact.
"""
from concurrent.futures import ThreadPoolExecutor
import http.client
import json
from pathlib import Path
import secrets
import urllib.error
import urllib.parse
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[3]
AUTH = "http://127.0.0.1:8002"
CORE = "http://127.0.0.1:8003"
BASE = "/v1/core"
checks, tokens = [], []
run = secrets.token_hex(5)
password = json.loads((ROOT / "dev/local/accounts.json").read_text())["password"]


def expect(condition, name):
    if not condition:
        raise AssertionError(name)
    checks.append(name)


def request(path, token=None, method="GET", body=None, code=200, key=None, origin=CORE):
    headers = {}
    if token:
        headers["Authorization"] = "Bearer " + token
    if key:
        headers["Idempotency-Key"] = key
    data = None
    if body is not None:
        data = body if isinstance(body, bytes) else json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(origin + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=100) as response:
            status, raw = response.status, response.read()
    except urllib.error.HTTPError as error:
        status, raw = error.code, error.read()
    expect(status == code, f"{method} {path.split('?')[0]} -> {code} (got {status})")
    value = json.loads(raw)
    if code < 300 and path.startswith(BASE):
        expect(value.get("status") is True, "native owner success envelope")
        return value["data"]
    return value


def api(path, token, method="GET", body=None, code=200, key=None):
    return request(BASE + path, token, method, body, code, key)


def login(name, division=1001, org=1001):
    result = request("/v1/auth/login", method="POST", origin=AUTH,
                     body={"login": name, "password": password,
                           "organization_id": org, "division_id": division})
    tokens.append(result["token"])
    return result["token"]


def fresh_key():
    return str(uuid.uuid4())


def discard_commit_reply(path, token, revision, key):
    """Receive successful headers, deliberately discard the committed body."""
    connection = http.client.HTTPConnection("127.0.0.1", 8003, timeout=100)
    try:
        connection.request("POST", BASE + path, json.dumps({"expected_revision": revision}),
                           {"Authorization": "Bearer " + token, "Content-Type": "application/json",
                            "Idempotency-Key": key})
        response = connection.getresponse()
        expect(response.status == 200, "native commit accepted before deliberate lost response body")
        # Do not consume/store the response body. Recovery must use the owner
        # receipt under the original UUID, not another business command.
    finally:
        connection.close()


def raw_row(number, name, **fields):
    return {"row": number, "full_name": name, "class_raw": "7.0",
            "school_name": "", "proctor_name": "", "tg_name": "", **fields}


def no_secrets(value):
    if isinstance(value, dict):
        expect(not set(value).intersection({"password", "hash", "token", "authorization", "command_hash"}),
               "owner public DTO has no credential material")
        for item in value.values():
            no_secrets(item)
    elif isinstance(value, list):
        for item in value:
            no_secrets(item)


def main():
    request("/health/ready")
    admin = login("dev_admin")
    foreign = login("dev_admin_math", 2001, 2001)
    division = api("/divisions", admin, "POST", {"name": "DEV Legacy " + run}, 201)["id"]
    other_division = api("/divisions", admin, "POST", {"name": "DEV Legacy Other " + run}, 201)["id"]
    path = f"/divisions/{division}/legacy-imports"
    permissions = {"upload": {"view": True, "edit": True}, "users": {"view": True, "edit": True}}
    role_name = "DEV Legacy editor " + run
    role = api("/roles", admin, "POST", {"name": role_name, "permissions": permissions}, 201)["id"]
    staff_login = "dev_legacy_" + run
    staff_id = api(f"/divisions/{division}/users", admin, "POST",
                   {"role": "staff_admin", "full_name": "DEV Import Editor " + run,
                    "role_id": role, "login": staff_login, "password": password}, 201)["account_id"]
    staff = login(staff_login, division)
    rows = [raw_row(2, "Legacy" + run + " Alpha", school_name="DEV Import school " + run,
                    proctor_name="LegacyProctor" + run + " First", tg_name="dev_legacy"),
            raw_row(3, "Legacy" + run + " Beta", school_name="DEV Import school " + run,
                    proctor_name="LegacyProctor" + run + " First"),
            raw_row(4, "Legacy" + run + " Gamma", class_raw="0")]
    preview_key = fresh_key()
    initial = api(path, admin, "POST", {"rows": rows}, 201, preview_key)
    expect(initial["max_rows"] == 100 and initial["revision"] == 1 and
           initial["preview"]["summary"]["row_errors"] == 1 and not initial["preview"]["can_commit"],
           "row errors/max_rows are authoritative")
    expect(api(path, admin, "POST", {"rows": rows}, 201, preview_key)["id"] == initial["id"],
           "lost preview reply recovers same session")
    altered = [dict(row) for row in rows]
    altered[0]["full_name"] += " Changed"
    expect(api(path, admin, "POST", {"rows": altered}, 409, preview_key)["error"] == "idempotency_key_reused",
           "preview UUID cannot be overwritten")
    session = path + "/" + str(initial["id"])
    api(session, foreign, code=404)
    api(session, staff, code=404)
    api(f"/divisions/{other_division}/legacy-imports/{initial['id']}", admin, code=404)
    api(session + "/commit", admin, "POST", {"expected_revision": 1}, 409, fresh_key())
    corrected = [dict(row) for row in rows]
    corrected[2]["class_raw"] = "٧"
    edit_key = fresh_key()
    edited = api(session, admin, "PUT", {"expected_revision": 1, "rows": corrected}, 200, edit_key)
    expect(edited["revision"] == 2 and edited["preview"]["can_commit"], "edit preserves spreadsheet rows and Unicode class")
    expect(api(session, admin, "PUT", {"expected_revision": 1, "rows": corrected}, 200, edit_key)["revision"] == 2,
           "lost edit reply bypasses stale CAS with accepted same command")
    api(session, admin, "PUT", {"expected_revision": 1, "rows": corrected}, 409, fresh_key())
    commit_key = fresh_key()
    # Two deliveries of one command serialize to one subject transaction.
    with ThreadPoolExecutor(max_workers=2) as pool:
        replies = list(pool.map(lambda _: api(session + "/commit", admin, "POST",
                                             {"expected_revision": 2}, 200, commit_key), range(2)))
    completed = replies[0]
    expect(replies[1]["report"] == completed["report"] and completed["status"] == "completed" and
           completed["revision"] == 3, "concurrent duplicate commit returns one durable receipt")
    report = completed["report"]
    expect(report["created_students"] == 3 and report["created_proctors"] == 1 and
           report["created_groups"] == 1 and report["created_schools"] == 1 and
           report["credentials_policy"] == "reset_required", "atomic mapping graph and honest credentials policy")
    no_secrets(completed)
    get = api(session, admin)
    expect(get["report"] == report and get["status"] == "completed" and not get["preview"]["can_commit"],
           "durable completed GET has same report")
    members = api(f"/divisions/{division}/users?limit=100", admin)
    expected_ids = {row["account_id"] for row in report["created"]}
    actual = {row["account_id"]: row for row in members if row["account_id"] in expected_ids}
    expect(set(actual) == expected_ids and all(actual[row["account_id"]]["profile_id"] == row["profile_id"]
                                              for row in report["created"]), "account/profile IDs map to actual Core subjects")
    groups = api(f"/divisions/{division}/groups?limit=100", admin)
    imported_group = next(group for group in groups if group["name"] == corrected[0]["proctor_name"])
    expect(imported_group["student_count"] == 2 and imported_group["proctor_account_id"] in expected_ids,
           "proctor and students belong to one current division group")
    skip = api(path, admin, "POST", {"rows": corrected}, 201, fresh_key())
    expect(skip["preview"]["summary"]["students_skip"] == 3 and not skip["preview"]["can_commit"] and
           skip["preview"]["summary"]["schools_create"] == 0 and skip["preview"]["summary"]["proctors_create"] == 0,
           "existing pupils are skipped without history/mapping rewrites")
    other = api(f"/divisions/{other_division}/legacy-imports", admin, "POST", {"rows": corrected}, 201, fresh_key())
    expect(other["preview"]["summary"]["row_errors"] == 3, "other division match never creates or silently grants membership")
    duplicate = api(path, admin, "POST", {"rows": [raw_row(2, "Duplicate" + run + " Person"),
                                                  raw_row(3, "Duplicate" + run + " Person Other")]}, 201, fresh_key())
    expect(duplicate["preview"]["summary"]["row_errors"] == 2, "same first/last names in workbook are ambiguous")
    ambiguous_name = "Ambiguous" + run + " Person"
    for index in range(2):
        api(f"/divisions/{division}/users", admin, "POST",
            {"role": "student", "full_name": ambiguous_name + " " + str(index), "class": 7,
             "login": "dev_legacy_amb_" + run + str(index), "password": password}, 201)
    ambiguous = api(path, admin, "POST", {"rows": [raw_row(2, ambiguous_name)]}, 201, fresh_key())
    expect(ambiguous["preview"]["summary"]["row_errors"] == 1 and not ambiguous["preview"]["can_commit"],
           "ambiguous persisted people never use first match")
    plan_name = "PlanChanged" + run + " Person"
    plan = api(path, admin, "POST", {"rows": [raw_row(2, plan_name)]}, 201, fresh_key())
    plan_path = path + "/" + str(plan["id"])
    existing = api(f"/divisions/{division}/users", admin, "POST",
                   {"role": "student", "full_name": plan_name, "class": 8,
                    "login": "dev_legacy_plan_" + run, "password": password}, 201)["account_id"]
    plan_conflict = api(plan_path + "/commit", admin, "POST", {"expected_revision": 1}, 409, fresh_key())
    expect(plan_conflict["error"] == "legacy_import_preview_changed" and
           api(f"/divisions/{division}/users/{existing}", admin)["class"] == 8,
           "changed owner matching blocks stale apply and preserves existing profile")
    refreshed = api(plan_path, admin, "PUT", {"expected_revision": 1, "rows": [raw_row(2, plan_name)]},
                    200, fresh_key())
    expect(refreshed["revision"] == 2 and refreshed["preview"]["summary"]["students_skip"] == 1 and
           not refreshed["preview"]["can_commit"], "explicit snapshot refresh resolves changed plan as zero creates")
    scoped = api(path, staff, "POST", {"rows": [raw_row(2, "Scoped" + run + " Person")]}, 201, fresh_key())
    expect(scoped["preview"]["can_commit"], "users/upload editor can prepare plain student")
    scoped_path = path + "/" + str(scoped["id"])
    revoke = {"name": role_name, "permissions": {"upload": {"view": True, "edit": True},
                                                "users": {"view": True, "edit": False}}}
    api(f"/roles/{role}", admin, "PUT", revoke)
    scoped_commit_key = fresh_key()
    api(scoped_path + "/commit", staff, "POST", {"expected_revision": 1}, 403, scoped_commit_key)
    api(f"/roles/{role}", admin, "PUT", {"name": role_name, "permissions": permissions})
    discard_commit_reply(scoped_path + "/commit", staff, 1, scoped_commit_key)
    scoped_completed = api(scoped_path, staff)
    expect(scoped_completed["report"]["created_students"] == 1, "live rights restored permits original UUID command")
    api(f"/roles/{role}", admin, "PUT", revoke)
    api(scoped_path + "/commit", staff, "POST", {"expected_revision": 1}, 403, scoped_commit_key)
    api(f"/roles/{role}", admin, "PUT", {"name": role_name, "permissions": permissions})
    expect(api(scoped_path + "/commit", staff, "POST", {"expected_revision": 1}, 200,
               scoped_commit_key)["report"] == scoped_completed["report"],
           "lost completed reply survives permission rejection and restores exact account")
    restricted = api(path, staff, "POST", {"rows": [raw_row(2, "Restricted" + run + " Person",
                                                            school_name="Forbidden school " + run,
                                                            proctor_name="Forbidden" + run + " Proctor")]}, 201, fresh_key())
    expect(not restricted["preview"]["can_commit"] and restricted["preview"]["summary"]["row_errors"] == 1,
           "scoped editor cannot escalate school/proctor/group writes")
    api(path, admin, "POST", {"rows": [raw_row(i + 2, "Limit" + run + " Person " + str(i)) for i in range(101)]}, 400, fresh_key())
    api(path, admin, "POST", b'{"rows":[],"rows":[]}', 400, fresh_key())
    api(path, admin, "POST", {"rows": [raw_row(2, "Invalid" + run + " Person")], "organization_id": 2001}, 400, fresh_key())
    api(path, admin, "POST", {"rows": [raw_row(2, "Invalid" + run + " Person")]}, 400)
    # Distinct credential import stays compatible and can recover preview after
    # its accounts have already been applied (no plaintext/hash in GET receipt).
    credentials = f"/divisions/{division}/imports"
    credential_key = fresh_key()
    credential_input = {"users": [{"role": "student", "full_name": "Credential" + run + " Person", "class": 9,
                                   "login": "dev_legacy_cred_" + run, "password": password}]}
    cred = api(credentials, admin, "POST", credential_input, 201, credential_key)
    expect(api(credentials, admin, "POST", credential_input, 201, credential_key)["id"] == cred["id"],
           "credential preview same key recovers original session")
    api(credentials + "/" + str(cred["id"]) + "/commit", admin, "POST")
    expect(api(credentials, admin, "POST", credential_input, 201, credential_key)["id"] == cred["id"],
           "credential preview replay after commit does not reject existing login or duplicate")
    receipt = api(credentials + "/receipts/" + credential_key, admin)
    expect(receipt["session_id"] == cred["id"] and set(receipt) == {"session_id", "status", "created_at"},
           "credential recovery endpoint publishes only session reference")
    api(credentials + "/receipts/" + credential_key, staff, code=404)
    api(credentials + "/receipts/" + credential_key, foreign, code=404)
    no_secrets(receipt)
    changed = {"users": [{**credential_input["users"][0], "password": password + "changed"}]}
    api(credentials, admin, "POST", changed, 409, credential_key)
    legacy_credential = {"users": [{"role": "student", "full_name": "OldCredential" + run + " Person", "class": 8,
                                    "login": "dev_legacy_oldcred_" + run, "password": password}]}
    old = api(credentials, admin, "POST", legacy_credential, 201)
    expect(old["id"] != cred["id"], "credential preview without optional UUID retains original contract")
    manifest = {"run": run, "organization_id": 1001, "division_id": division, "other_division_id": other_division,
                "session_id": initial["id"], "credential_session_id": cred["id"], "credential_receipt_key": credential_key,
                "created": report["created"], "group_id": imported_group["id"], "staff_login": staff_login,
                "staff_account_id": staff_id, "role_id": role, "password_file": "dev/local/accounts.json"}
    (ROOT / "dev/local/core-legacy-import-demo.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2))
    return manifest


if __name__ == "__main__":
    status, error = "passed", None
    try:
        manifest = main()
    except Exception as exception:
        status, error = "failed", type(exception).__name__ + ": " + str(exception)
    finally:
        for token in tokens:
            try:
                request("/v1/auth/logout", token, "POST", origin=AUTH)
            except Exception:
                pass
        report = {"status": status, "run": run, "passed": len(checks), "checks": checks,
                  "error": error, "transport": "public native HTTP", "synthetic_only": True,
                  "direct_database_access": False, "runtime_control": False}
        output = ROOT / "dev/local/core-legacy-import-http-report.json"
        output.write_text(json.dumps(report, ensure_ascii=False, indent=2))
        print(json.dumps({"status": status, "passed": len(checks), "report": str(output), "error": error}))
    if status != "passed":
        raise SystemExit(1)
