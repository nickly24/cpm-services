"""Repeatable Access API boundary smoke. Only auth/context writes; no business mutation."""
import json
import urllib.error
import urllib.request
import http.cookiejar
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
BASE = "http://127.0.0.1:8001/bff"
password = json.loads((ROOT / "dev/local/accounts.json").read_text())["password"]
demo = json.loads((ROOT / "dev/local/bff-core-access-demo.json").read_text())
checks = []


def request(opener, path, method="GET", body=None, revision=""):
    req = urllib.request.Request(BASE + path, method=method, headers={
        "X-CPM-Request": "1", "X-CPM-Context": revision,
        "Content-Type": "application/json", "Origin": "http://127.0.0.1:3001",
    }, data=json.dumps(body).encode() if body is not None else None)
    try:
        with opener.open(req) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as response:
        return response.code, json.load(response)


def client(login):
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
    status, data = request(opener, "/auth/login", "POST", {"login": login, "password": password})
    assert status == 200 and data["status"], "BFF login failed"
    return opener, data["context_revision"]


def check(condition, label):
    assert condition, label
    checks.append(label)


admin, revision = client("dev_admin")
status, state = request(admin, "/auth/context", "POST", {"organization_id": demo["organization_id"], "division_id": demo["division_a"]}, revision)
check(status == 200, "admin selects owned division A using session context")
revision = state["context_revision"]
status, catalog = request(admin, "/core/roles", revision=revision)
check(status == 200 and isinstance(catalog["data"]["roles"], list) and isinstance(catalog["data"]["sections"], list), "live GETroles has flat roles+sections owner envelope")
check(any(role["id"] == demo["bootstrap_role_id"] for role in catalog["data"]["roles"]), "organization catalogue includes owned bootstrap role")
check(all(set(role) == {"id", "name", "permissions"} for role in catalog["data"]["roles"]), "roles do not invent descriptions, global counts or revisions")
status, roster = request(admin, "/core/users?role=staff_admin&limit=200&offset=0", revision=revision)
check(status == 200 and {row["account_id"] for row in roster["data"]} == {demo["viewer"]["account_id"]}, "A staff roster uses auth account ID and excludes B memberships")
check(all(not any("password" in key or "token" in key or "hash" in key for key in row) for row in roster["data"]), "staff read contains no credential secrets")
status, state = request(admin, "/auth/context", "POST", {"organization_id": demo["organization_id"], "division_id": demo["division_b"]}, revision)
check(status == 200, "admin changes to owned B context")
revision = state["context_revision"]
status, other = request(admin, "/core/roles", revision=revision)
check(status == 200 and other["data"] == catalog["data"], "role catalogue remains organization-wide after division change")
status, roster = request(admin, "/core/users?role=staff_admin&limit=200&offset=0", revision=revision)
expected_b = {demo["foreign"]["account_id"]}
if demo.get("employee"):
    expected_b.add(demo["employee"]["account_id"])
check(status == 200 and {row["account_id"] for row in roster["data"]} == expected_b, "B roster shows only B assignments")
for query in ["organization_id=1002", "division_id=" + str(demo["division_a"]), "role=staff_admin"]:
    status, _ = request(admin, "/core/roles?" + query, revision=revision)
    check(status == 400, "role query scope override rejected: " + query.split("=")[0])
viewer, revision = client(demo["viewer"]["login"])
status, identity = request(viewer, "/auth/me", revision=revision)
check(status == 200 and identity["user"]["role"] == "staff_admin" and not identity["user"]["organization_admin"], "filtered staff cannot acquire organization_admin")
status, _ = request(viewer, "/core/roles", revision=revision)
check(status == 403, "filtered staff roles read denied by public owner")
status, roster = request(viewer, "/core/users?role=staff_admin&limit=200&offset=0", revision=revision)
check(status == 200 and {row["account_id"] for row in roster["data"]} == {demo["viewer"]["account_id"]}, "authorized staff users.view stays confined to own division")
report = {"run": demo["run"], "checks": checks, "business_writes": 0}
(ROOT / "dev/local/bff-core-access-readonly-api-report.json").write_text(json.dumps(report, ensure_ascii=False, indent=2))
print(json.dumps({"run": demo["run"], "checks": len(checks), "business_writes": 0, "status": "passed"}))
