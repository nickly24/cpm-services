"""Fresh Core/BFF scope acceptance against fixed loopback synthetic Dev only.

Requires explicit --run after the coordinated Core migration/rebuild. Creates
only uniquely named synthetic records through public APIs; retains fixtures.
Internal APIs read references and reserve a rating lock for this run's student.
No SQL, migration, process, broker or unrelated subject operations are performed.
Passwords, cookies, tokens and full response bodies never enter reports.
"""
from __future__ import annotations

import argparse
import http.cookiejar
import json
from pathlib import Path
import secrets
import sys
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / "dev/scripts"))
from export_structure import read_env

BFF = "http://127.0.0.1:8001"
AUTH = "http://127.0.0.1:8002"
CORE = "http://127.0.0.1:8003"
ORIGIN = "http://127.0.0.1:3001"
MANIFEST = ROOT / "dev/local/bff-core-scope-demo.json"
REPORT = ROOT / "dev/local/bff-core-scope-http-report.json"
PRIVATE_KEYS = {"password", "password_hash", "hash", "credentials", "token", "access_token", "refresh_token"}
ROSTER_PRIVATE_KEYS = PRIVATE_KEYS | {"login", "username", "tg_name", "role_id"}


class NoRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise RuntimeError("Unexpected redirect from fixed loopback API")


def private_keys(value, forbidden=PRIVATE_KEYS):
    if isinstance(value, dict):
        return bool(forbidden & value.keys()) or any(private_keys(child, forbidden) for child in value.values())
    if isinstance(value, list):
        return any(private_keys(child, forbidden) for child in value)
    return False


def request(http, base, path, method="GET", body=None, headers=None, expected=200):
    assert base in {BFF, AUTH, CORE} and path.startswith("/") and not path.startswith("//")
    raw, hdr = None, dict(headers or {})
    if body is not None:
        raw = json.dumps(body).encode()
        hdr["Content-Type"] = "application/json"
    req = urllib.request.Request(base + path, method=method, data=raw, headers=hdr)
    try:
        with http.open(req, timeout=25) as res:
            code, value = res.status, json.load(res)
    except urllib.error.HTTPError as res:
        code, value = res.code, json.load(res)
    allowed = {expected} if isinstance(expected, int) else set(expected)
    # Never include returned bodies, submitted profiles, credentials or errors
    # that could embed source data in assertion/exception reports.
    assert code in allowed, f"{method} {path.split('?')[0]}: expected {sorted(allowed)}, got {code}"
    return value.get("data", value), value


class Session:
    def __init__(self, check):
        self.jar = http.cookiejar.CookieJar()
        self.http = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar), NoRedirects())
        self.revision, self.check = "", check

    def call(self, path, method="GET", body=None, expected=200, revision=None):
        assert path.startswith("/bff/core/") or path in {"/bff/auth/login", "/bff/auth/context", "/bff/auth/logout"} or path.startswith("/bff/internal/")
        value, envelope = request(self.http, BFF, path, method, body, {
            "Origin": ORIGIN, "X-CPM-Request": "1",
            "X-CPM-Context": self.revision if revision is None else revision,
        }, expected)
        assert not (PRIVATE_KEYS & envelope.keys()), "BFF leaked authentication material"
        if "context_revision" in envelope:
            self.revision = envelope["context_revision"]
        return value

    def login(self, login, password, organization, division):
        identity = self.call("/bff/auth/login", "POST", {"login": login, "password": password, "organization_id": organization, "division_id": division})
        self.check(identity["user"]["organization_id"] == organization and identity["user"]["division_id"] == division, "login selects actual organization/division")
        cookie = next(item for item in self.jar if item.name == "cpm_bff")
        self.check(cookie.has_nonstandard_attr("HttpOnly") and cookie.get_nonstandard_attr("SameSite") == "Strict", "BFF cookie is HttpOnly/SameSite Strict")
        return identity["user"]

    def switch(self, organization, division):
        return self.call("/bff/auth/context", "POST", {"organization_id": organization, "division_id": division})

    def core(self, tail, method="GET", body=None, expected=200, **kwargs):
        return self.call("/bff/core/" + tail, method, body, expected, **kwargs)

    def all(self, tail, query=""):
        rows = []
        for offset in range(0, 100001, 200):
            page = self.core(tail + f"?limit=200&offset={offset}" + ("&" + query if query else ""))
            assert isinstance(page, list), "Invalid list response"
            rows.extend(page)
            if len(page) < 200:
                return rows
        raise AssertionError("Directory exceeds supported pagination")

    def close(self):
        if self.revision:
            self.call("/bff/auth/logout", "POST", {})
            self.revision = ""


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run", action="store_true", help="Run only after coordinated migration/rebuild readiness")
    args = parser.parse_args()
    if not args.run:
        print(json.dumps({"status": "pending_runtime_handoff", "live_checks": 0, "required": "--run after migration 005 and Core/BFF readiness"}))
        return
    if MANIFEST.exists():
        previous = json.loads(MANIFEST.read_text())
        if any(command.get("status") == "pending" for command in previous.get("commands", [])):
            raise SystemExit("Previous fixture has an uncertain command; preserve its manifest and inspect canonical state before a new run")

    password = json.loads((ROOT / "dev/local/accounts.json").read_text())["password"]
    internal_key = read_env(ROOT / "dev/local/core.env")["AUTH_INTERNAL_KEY"]
    run = secrets.token_hex(5)
    prefix = "DEV Core scope " + run
    demo = {"run": run, "prefix": prefix, "organization_id": 1001, "foreign_organization_id": 2001,
            "readiness": "preparing", "users": {}, "groups": {}, "commands": []}
    report = {"status": "failed", "run": run, "checks": [], "limitations": [
        "organization creation API absent: fresh divisions/accounts inside synthetic organizations 1001/2001",
        "unknown student class cannot be created by public user/import contracts; NULLIF(class,0) is unit-covered",
        "legacy Allratings has no rows in the coordinated Dev preflight; scoped legacy history is unit-covered only",
    ]}
    sessions = []
    direct = urllib.request.build_opener(NoRedirects())

    def check(value, label):
        assert value, label
        report["checks"].append(label)

    def save():
        MANIFEST.parent.mkdir(parents=True, exist_ok=True)
        MANIFEST.write_text(json.dumps(demo, ensure_ascii=False, indent=2) + "\n")
        MANIFEST.chmod(0o600)

    def login(name, org=1001, div=None):
        session = Session(check)
        sessions.append(session)
        session.login(name, password, org, div or demo["division_a"])
        return session

    def command(session, tail, method, body, expected=200):
        receipt = {"route": tail.split("?")[0], "method": method, "status": "pending"}
        demo["commands"].append(receipt)
        save()
        # One dispatch only. If the connection fails, preserve this command in
        # the manifest; this runner never automatically retries the mutation.
        out = session.core(tail, method, body, expected)
        receipt["status"] = "confirmed" if expected < 400 else "rejected"
        save()
        return out

    def service(path, body, expected=200, key=internal_key):
        value, _ = request(direct, CORE, path, "POST", body, {"X-Service-Key": key}, expected)
        return value

    def roster(rows, expected_accounts):
        check({row["account_id"] for row in rows} == set(expected_accounts), "roster preserves complete scoped membership")
        check(all(set(row) == {"account_id", "profile_id", "role", "full_name", "school_id", "class", "group_id", "is_active"} for row in rows), "restricted roster contains exactly the minimal display DTO")
        check(not private_keys(rows, ROSTER_PRIVATE_KEYS), "restricted roster never contains login, Telegram, role grants or credentials")
        check(all(type(row["account_id"]) is int and type(row["profile_id"]) is int for row in rows), "account and profile identities are explicit integer fields")

    try:
        admin = login("dev_admin", div=1001)
        math = login("dev_admin_math", org=2001, div=2001)
        for name in ("division_a", "division_b", "empty_division"):
            demo[name] = command(admin, "divisions", "POST", {"name": prefix + " " + name}, 201)["id"]
        demo["foreign_division"] = command(math, "divisions", "POST", {"name": prefix + " foreign"}, 201)["id"]
        admin.switch(1001, demo["division_a"])
        math.switch(2001, demo["foreign_division"])
        check(admin.core("organization")["id"] == 1001 and math.core("organization")["id"] == 2001, "each full admin reads only its current organization")
        own_divisions = {row["id"] for row in admin.core("divisions")}
        check({demo["division_a"], demo["division_b"]} <= own_divisions and demo["foreign_division"] not in own_divisions, "organization division directory excludes foreign division")
        command(admin, f"divisions/{demo['division_b']}", "PUT", {"name": prefix + " B renamed"})
        command(admin, f"divisions/{demo['foreign_division']}", "PUT", {"name": prefix + " forbidden"}, 404)

        school_name = prefix + " same school"
        demo["school_a"] = command(admin, "schools", "POST", {"name": school_name, "short_name": "DEV-A", "notes": "Synthetic only", "is_active": True}, 201)["id"]
        demo["school_foreign"] = command(math, "schools", "POST", {"name": school_name, "short_name": "DEV-B", "notes": "Synthetic only", "is_active": True}, 201)["id"]
        check(demo["school_a"] != demo["school_foreign"], "same school name in two organizations creates independent preserved IDs")
        duplicate = command(admin, "schools", "POST", {"name": school_name, "is_active": True}, 409)
        check(duplicate["error"] == "school_name_exists", "duplicate school in its own organization has a precise conflict")
        check({row["id"] for row in admin.all("schools", "q=" + urllib.parse.quote(school_name))} == {demo["school_a"]}, "school directory search is organization scoped")
        admin.core(f"schools/{demo['school_foreign']}", expected=404)
        command(admin, f"schools/{demo['school_foreign']}", "PUT", {"name": prefix + " foreign update", "is_active": True}, 404)
        admin.core("schools?organization_id=2001", expected=400)
        command(admin, "schools", "POST", {"name": prefix + " forbidden scope", "organization_id": 2001, "is_active": True}, 400)
        check(True, "foreign school reads/writes and claimed organization substitutions are rejected")

        direction_name = prefix + " direction"
        demo["direction_a"] = command(admin, "directions", "POST", {"name": direction_name}, 201)["id"]
        demo["direction_foreign"] = command(math, "directions", "POST", {"name": direction_name}, 201)["id"]
        command(admin, f"directions/{demo['direction_a']}", "PUT", {"name": direction_name + " renamed"})
        command(admin, f"directions/{demo['direction_foreign']}", "PUT", {"name": prefix + " forbidden"}, 404)
        check(demo["direction_a"] in {row["id"] for row in admin.core("directions")} and demo["direction_foreign"] not in {row["id"] for row in admin.core("directions")}, "organization direction CRUD preserves foreign scope")

        for label in ("group_a", "group_other"):
            demo["groups"][label] = command(admin, "groups", "POST", {"name": prefix + " " + label}, 201)["id"]
        admin.switch(1001, demo["division_b"])
        demo["groups"]["group_b"] = command(admin, "groups", "POST", {"name": prefix + " group B"}, 201)["id"]
        admin.switch(1001, demo["division_a"])
        role_permissions = {
            "groups_view": {"groups": {"view": True, "edit": False}},
            "groups_edit": {"groups": {"view": True, "edit": True}},
            "users_view": {"users": {"view": True, "edit": False}},
            "users_edit": {"users": {"view": True, "edit": True}, "groups": {"view": True, "edit": True}},
        }
        demo["roles"] = {name: command(admin, "roles", "POST", {"name": prefix + " " + name, "permissions": permissions}, 201)["id"] for name, permissions in role_permissions.items()}

        def create(label, role="student", group=None, role_id=None, owner=admin, cls=7, school=True):
            login_name = "dev_scope_" + run + "_" + label
            out = command(owner, "users", "POST", {"role": role, "full_name": prefix + " " + label,
                "class": cls if role == "student" else 0, "tg_name": ("scope_" + run + "_" + label)[:30],
                "school_id": demo["school_a"] if role == "student" and school else None,
                "group_id": group, "role_id": role_id, "login": login_name, "password": password}, 201)
            uid = out["account_id"]
            profile = owner.core(f"users/{uid}")
            demo["users"][label] = {"account_id": uid, "profile_id": profile["profile_id"], "login": login_name, "role": role}
            save()
            return uid

        student = create("student", group=demo["groups"]["group_a"])
        second = create("student2", group=demo["groups"]["group_other"], cls=8)
        proctor = create("proctor", "proctor", demo["groups"]["group_a"])
        for name in role_permissions:
            create(name, "staff_admin", role_id=demo["roles"][name])
        foreign = create("foreign_student", owner=math, school=False)
        blocked = create("blocked", cls=9)
        command(admin, f"users/{blocked}/active", "PUT", {"is_active": False})

        expected_group = {student, proctor}
        for label in ("groups_view", "groups_edit", "proctor", "student"):
            reader = login(demo["users"][label]["login"])
            roster(reader.all(f"groups/{demo['groups']['group_a']}/members"), {student} if label == "student" else expected_group)
            if label in {"proctor", "student"}:
                reader.core(f"groups/{demo['groups']['group_other']}/members", expected=403)
                check(True, "ordinary group member cannot read another own-division group")
            reader.core(f"groups/{demo['groups']['group_b']}/members", expected=404)
            reader.core("organization/users", expected=403)
            reader.core(f"organization/users/{student}/memberships", expected=403)
            check(True, "restricted actors cannot read foreign-division group or organization membership directory")
            if label == "groups_view":
                command(reader, f"groups/{demo['groups']['group_a']}/members", "POST", {"account_id": second}, 403)
                command(reader, "schools", "POST", {"name": prefix + " denied", "is_active": True}, 403)
            if label == "groups_edit":
                # Independent groups.edit does not imply users.view/edit.
                command(reader, f"groups/{demo['groups']['group_a']}/members", "POST", {"account_id": second})
                roster(reader.all(f"groups/{demo['groups']['group_a']}/members"), {student, second, proctor})
                command(reader, f"groups/{demo['groups']['group_other']}/members", "POST", {"account_id": second})
                check(True, "groups-only editor moves a known account without expanding user directory access")

        full = login(demo["users"]["users_view"]["login"])
        full_roster = full.all(f"groups/{demo['groups']['group_a']}/members")
        check({row["account_id"] for row in full_roster} == expected_group and all("login" in row and "tg_name" in row and "role_id" in row for row in full_roster), "users.view retains authorized contact and role DTO")
        check(not private_keys(full_roster), "authorized user DTO still never contains passwords or hashes")
        first_page = admin.core(f"groups/{demo['groups']['group_a']}/members?limit=1&offset=0")
        next_page = admin.core(f"groups/{demo['groups']['group_a']}/members?limit=1&offset=1")
        check(len(first_page) == len(next_page) == 1 and first_page[0]["account_id"] != next_page[0]["account_id"], "group members have real distinct pagination")
        admin.core(f"groups/{demo['groups']['group_a']}/members?limit=201", expected=400)

        changing_view = login(demo["users"]["groups_view"]["login"])
        command(admin, f"roles/{demo['roles']['groups_view']}", "PUT", {"name": prefix + " groups_view", "permissions": {}})
        changing_view.core(f"groups/{demo['groups']['group_a']}/members", expected=403)
        command(admin, f"roles/{demo['roles']['groups_view']}", "PUT", {"name": prefix + " groups_view", "permissions": role_permissions["groups_view"]})
        roster(changing_view.all(f"groups/{demo['groups']['group_a']}/members"), expected_group)
        check(True, "live withdrawal and restoration of group permission affects an existing cookie session")
        for label in ("proctor", "student"):
            changing_member = login(demo["users"][label]["login"])
            account = demo["users"][label]["account_id"]
            command(admin, f"groups/{demo['groups']['group_other']}/members", "POST", {"account_id": account})
            changing_member.core(f"groups/{demo['groups']['group_a']}/members", expected=403)
            command(admin, f"groups/{demo['groups']['group_a']}/members", "POST", {"account_id": account})
            roster(changing_member.all(f"groups/{demo['groups']['group_a']}/members"), {student} if label == "student" else expected_group)
        check(True, "live student/proctor group reassignment closes the previous own-group roster")

        membership = admin.core(f"organization/users/{student}/memberships")
        check(membership["owner_organization_id"] == 1001 and membership["can_assign"] and membership["can_revoke"] and [m["division_id"] for m in membership["memberships"]] == [demo["division_a"]], "own account membership DTO exposes owner and independent command capabilities")
        command(admin, f"divisions/{demo['division_b']}/memberships/{student}", "PUT", {})
        membership = admin.core(f"organization/users/{student}/memberships")
        check({row["division_id"] for row in membership["memberships"]} == {demo["division_a"], demo["division_b"]} and all(row["can_revoke"] for row in membership["memberships"]), "full admin explicitly grants another own division without cabinet switch")
        check({row["account_id"] for row in admin.all("organization/users", "q=" + urllib.parse.quote(demo["users"]["student"]["login"]))} == {student, second}, "organization directory substring query finds both matching owned synthetic students")
        admin.core(f"organization/users/{foreign}/memberships", expected=404)
        check(foreign not in {row["account_id"] for row in admin.all("organization/users")}, "foreign-owned unrelated account never leaks into organization catalog")
        command(admin, f"divisions/{demo['foreign_division']}/memberships/{student}", "PUT", {}, 404)
        command(admin, f"divisions/{demo['division_b']}/memberships/{foreign}", "PUT", {}, 403)
        delegated = login(demo["users"]["users_edit"]["login"])
        command(delegated, f"divisions/{demo['division_b']}/memberships/{student}", "PUT", {}, 403)
        command(delegated, f"memberships/{demo['users']['users_view']['account_id']}", "PUT", {"role_id": demo["roles"]["users_edit"]}, 403)
        check(True, "explicit target membership cannot cross organization or escalate delegated staff roles")

        # The initial synthetic cross-organization student is read-only here.
        shared_login, _ = request(direct, AUTH, "/v1/auth/login", "POST", {"login": "dev_student_01", "password": password, "organization_id": 2001, "division_id": 2001})
        shared_token = shared_login["access_token"]
        shared_identity, _ = request(direct, AUTH, "/v1/auth/introspect", "POST", headers={"Authorization": "Bearer " + shared_token})
        shared_id = shared_identity["user"]["account_id"]
        shared = math.core(f"organization/users/{shared_id}/memberships")
        math_divisions = {row["id"] for row in math.core("divisions")}
        check(shared["owner_organization_id"] == 1001 and not shared["can_assign"] and shared["can_revoke"] and {row["division_id"] for row in shared["memberships"]} <= math_divisions and all(row["can_revoke"] for row in shared["memberships"]), "shared foreign-owned account exposes only current-org participation with revoke allowed and grant denied")
        command(math, f"divisions/{demo['foreign_division']}/memberships/{shared_id}", "PUT", {}, 403)

        # BFF/native projections agree, and service keys never become browser APIs.
        auth, _ = request(direct, AUTH, "/v1/auth/login", "POST", {"login": demo["users"]["groups_view"]["login"], "password": password, "organization_id": 1001, "division_id": demo["division_a"]})
        native, _ = request(direct, CORE, f"/v1/core/divisions/{demo['division_a']}/groups/{demo['groups']['group_a']}/members", headers={"Authorization": "Bearer " + auth["access_token"]})
        roster(native, expected_group)
        service("/internal/core/test-references", {"organization_id": 1001, "division_ids": [demo["division_a"]], "include_students": True}, 401, "wrong")
        admin.call("/bff/internal/core/test-references", expected=501)
        refs = service("/internal/core/test-references", {"organization_id": 1001, "division_ids": [demo["division_a"]], "include_students": True})
        accounts = {row["account_id"]: row for row in refs["accounts"]}
        check(refs["accounts_complete"] is True and refs["account_projection_version"] == 1 and set(accounts) == {student, second, blocked}, "include_students returns complete bounded current-division student projection")
        check(accounts[blocked]["is_active"] is False and accounts[blocked]["class"] == 9 and accounts[student]["class"] == 7, "blocked students remain in historical source references with actual integer classes")
        check(not private_keys(refs["accounts"], ROSTER_PRIVATE_KEYS) and proctor not in accounts and foreign not in accounts, "internal student references omit staff, foreign students and contacts")
        no_students = service("/internal/core/test-references", {"organization_id": 1001, "division_ids": [demo["division_a"]]})
        check("accounts" not in no_students and "accounts_complete" not in no_students, "student projection is explicit opt-in")
        for divisions in ([], [demo["division_a"], demo["division_b"]], [demo["division_a"], demo["division_a"]]):
            service("/internal/core/test-references", {"organization_id": 1001, "division_ids": divisions, "include_students": True}, 400)
        service("/internal/core/test-references", {"organization_id": 1001, "division_ids": [demo["foreign_division"]], "include_students": True}, 404)
        check(True, "student projection rejects ambiguous and foreign division scope")

        homework = service("/internal/core/homework-references", {"organization_id": 1001, "division_id": demo["division_a"]})
        members = {row["account_id"]: row for row in homework["members"]}
        own = members[student]
        check(homework["organization_id"] == 1001 and homework["division_id"] == demo["division_a"] and own["class"] == 7 and own["school_id"] == demo["school_a"] and own["school_name"] == school_name and own["school_short_name"] == "DEV-A", "homework references carry real class and own organization school display fields")
        check(members[proctor]["class"] is None and members[proctor]["school_id"] is None and members[proctor]["school_name"] is None and members[proctor]["school_short_name"] is None, "homework staff class and school remain explicit nulls")
        check(blocked not in members and foreign not in members and not private_keys(homework, ROSTER_PRIVATE_KEYS), "homework references retain active/member scope and exclude contacts")
        service("/internal/core/homework-references", {"organization_id": 1001, "division_id": demo["division_a"], "account_ids": [blocked]}, 404)
        service("/internal/core/homework-references", {"organization_id": 1001, "division_id": demo["division_a"], "account_ids": [foreign]}, 404)

        rating_receipt = {"route": "/internal/core/rating-locks", "method": "POST", "account_id": student,
                          "division_id": demo["division_b"], "status": "pending"}
        demo["commands"].append(rating_receipt)
        save()
        service("/internal/core/rating-locks", {"account_id": student, "division_id": demo["division_b"]})
        rating_receipt["status"] = "confirmed"
        save()
        blocked_rating = command(admin, f"divisions/{demo['division_b']}/memberships/{student}", "DELETE", None, 409)
        check(blocked_rating["error"] == "rating_exists", "actual permanent rating reservation blocks only its own division")
        command(admin, f"memberships/{student}", "DELETE", None)
        membership = admin.core(f"organization/users/{student}/memberships")
        check([m["division_id"] for m in membership["memberships"]] == [demo["division_b"]], "another unrated participation is removable despite historical rating elsewhere")
        command(admin, f"memberships/{student}", "PUT", {})
        command(admin, f"groups/{demo['groups']['group_a']}/members", "POST", {"account_id": student})
        check(True, "new participation can be re-added without copying or removing the historical rating")
        last = command(admin, f"memberships/{second}", "DELETE", None, 409)
        check(last["error"] == "last_membership", "last global participation remains protected")
        archive = command(admin, f"divisions/{demo['division_a']}", "DELETE", None, 409)
        check(archive["error"] == "division_has_members", "division with members cannot be archived")
        command(admin, f"divisions/{demo['empty_division']}", "DELETE", None)
        check(demo["empty_division"] not in {row["id"] for row in admin.core("divisions")}, "empty own division archive preserves remaining scopes")
        command(admin, f"directions/{demo['direction_a']}", "DELETE", None)
        demo["direction_a_archived"] = True
        check(demo["direction_a"] not in {row["id"] for row in admin.core("directions")} and demo["direction_foreign"] in {row["id"] for row in math.core("directions")}, "direction archive retains the independent foreign organization direction")

        report["legacy_http_verified"] = False

        previous = admin.revision
        admin.switch(1001, demo["division_b"])
        admin.core(f"groups/{demo['groups']['group_a']}/members", revision=previous, expected=409)
        admin.core(f"groups/{demo['groups']['group_a']}/members", expected=404)
        check(True, "cabinet switch invalidates old context and confines subsequent group reads")
        admin.switch(1001, demo["division_a"])
        demo["readiness"] = "ready"
        save()
        report["status"] = "passed"
    except Exception as exc:
        report["error_type"] = type(exc).__name__
        # Our assertion labels never include profile bodies or credentials.
        report["error"] = str(exc) if isinstance(exc, AssertionError) else "transport_or_runtime_error"
        raise
    finally:
        for session in sessions:
            try:
                session.close()
            except Exception:
                pass
        report["count"] = len(report["checks"])
        REPORT.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
        REPORT.chmod(0o600)
        print(json.dumps({"status": report["status"], "count": report["count"], "legacy_http_verified": report.get("legacy_http_verified", False)}))


if __name__ == "__main__":
    main()
