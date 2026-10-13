"""Public loopback homework settings/audit regression with fresh retained fixtures.

Core fixture creation uses authenticated public owner APIs. All homework calls
use opaque BFF cookies. No SQL, broker, remote service, cleanup or runtime writes.
Passwords and auth tokens are never saved in the manifest or report.
"""
from __future__ import annotations

import http.cookiejar
import json
from pathlib import Path
import secrets
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[3]
BFF = "http://127.0.0.1:8001"
ORIGIN = "http://127.0.0.1:3001"
DEFAULTS = {"max_source_bytes": 10485760, "max_output_bytes": 10485760, "max_pages": 35,
            "dpi": 120, "jpeg_quality": 75, "presign_ttl_seconds": 300, "upload_stale_seconds": 1800}


class NoRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise RuntimeError("Unexpected redirect from fixed loopback API")


def request(http, origin, path, method="GET", body=None, headers=None, expected=200):
    assert origin in {BFF, "http://127.0.0.1:8002", "http://127.0.0.1:8003"}
    h = dict(headers or {})
    raw = None
    if body is not None:
        raw = json.dumps(body).encode()
        h["Content-Type"] = "application/json"
    req = urllib.request.Request(origin + path, method=method, data=raw, headers=h)
    try:
        with http.open(req, timeout=25) as response:
            code, value = response.status, json.load(response)
    except urllib.error.HTTPError as response:
        code, value = response.code, json.load(response)
    allowed = {expected} if isinstance(expected, int) else set(expected)
    assert code in allowed, f"{method} {path.split('?')[0]} expected {sorted(allowed)}, got {code} {value.get('error', '')}"
    return value


class Session:
    def __init__(self):
        self.jar = http.cookiejar.CookieJar()
        self.http = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar), NoRedirects())
        self.revision = ""

    def call(self, path, method="GET", body=None, expected=200, revision=None):
        assert path.startswith("/bff/") or path == "/openapi.json"
        assert method == "GET" or path in {"/bff/auth/login", "/bff/auth/context", "/bff/auth/logout", "/bff/homework/settings"}
        value = request(self.http, BFF, path, method, body, {"Origin": ORIGIN, "X-CPM-Request": "1", "X-CPM-Context": self.revision if revision is None else revision}, expected)
        assert not {"access_token", "refresh_token", "token"} & value.keys(), "BFF leaked auth token"
        if "context_revision" in value:
            self.revision = value["context_revision"]
        return value.get("data", value)

    def login(self, name, password, organization, division):
        identity = self.call("/bff/auth/login", "POST", {"login": name, "password": password, "organization_id": organization, "division_id": division})
        assert identity["user"]["division_id"] == division
        cookie = next(cookie for cookie in self.jar if cookie.name == "cpm_bff")
        assert cookie.has_nonstandard_attr("HttpOnly") and cookie.get_nonstandard_attr("SameSite") == "Strict"
        return identity

    def close(self):
        if self.revision:
            self.call("/bff/auth/logout", "POST", {})
            self.revision = ""


def main():
    run = secrets.token_hex(5)
    password = json.loads((ROOT / "dev/local/accounts.json").read_text())["password"]
    checks, sessions, owner_tokens = [], [], []
    demo = {"run": run, "organization_id": 1001, "foreign_organization_id": 2001, "users": {}}
    report = {"status": "failed", "checks": checks, "run": run}
    direct = urllib.request.build_opener(NoRedirects())

    def check(value, name):
        assert value, name
        checks.append(name)

    def token(login, organization, division):
        auth = request(direct, "http://127.0.0.1:8002", "/v1/auth/login", "POST", {"login": login, "password": password, "organization_id": organization, "division_id": division})
        value = auth.get("access_token", auth.get("token"))
        assert value
        owner_tokens.append(value)
        return value

    def core(access, path, body):
        return request(direct, "http://127.0.0.1:8003", "/v1/core" + path, "POST", body, {"Authorization": "Bearer " + access}, 201).get("data")

    def login(name, organization=1001, division=None):
        session = Session()
        sessions.append(session)
        session.login(name, password, organization, division or demo["division_a"])
        return session

    def settings(session):
        value = session.call("/bff/homework/settings")
        check(set(value) == set(DEFAULTS) and all(type(x) is int for x in value.values()), "settings use complete integer owner DTO")
        return value

    try:
        public = Session()
        public.call("/bff/homework/settings", expected=401)
        public.call("/bff/homework/audit", expected=401)
        check(True, "settings and audit require fresh cookie sessions")
        paths = public.call("/openapi.json")["paths"]
        check(set(paths["/bff/homework/settings"]) == {"get", "put"} and "get" in paths["/bff/homework/audit"], "all three existing public operations are documented")
        access = token("dev_admin", 1001, 1001)
        foreign_access = token("dev_admin_math", 2001, 2001)
        for label in ("a", "b"):
            name = f"DEV HW Settings {run} {label.upper()}"
            demo["division_" + label] = core(access, "/divisions", {"name": name})["id"]
            demo["division_" + label + "_name"] = name
        demo["foreign_division"] = core(foreign_access, "/divisions", {"name": f"DEV HW Settings {run} foreign"})["id"]
        admin = login("dev_admin")
        admin_b = login("dev_admin", division=demo["division_b"])
        foreign = login("dev_admin_math", 2001, demo["foreign_division"])
        check(settings(admin) == DEFAULTS and settings(admin_b) == DEFAULTS and settings(foreign) == DEFAULTS, "fresh settings fallback defaults are independent across divisions and organizations")
        check(admin.call("/bff/homework/audit?limit=200&after=0")["items"] == [], "fresh division starts with empty audit")

        grants = {"viewer": {"assignments": {"view": True, "edit": False}}, "editor": {"assignments": {"view": True, "edit": True}}, "monitor": {"monitoring": {"view": True, "edit": False}}, "denied": {"dashboard": {"view": True, "edit": False}}}
        for label, permissions in grants.items():
            role = core(access, "/roles", {"name": f"DEV HW Settings {run} {label}", "permissions": permissions})["id"]
            name = f"hwsettings_{run}_{label}"
            user = core(access, f"/divisions/{demo['division_a']}/users", {"role": "staff_admin", "full_name": f"DEV HW {run} {label}", "login": name, "password": password, "role_id": role})
            demo["users"][label] = {"login": name, "account_id": user["account_id"], "role": "staff_admin"}
        for label in ("student", "proctor"):
            name = f"hwsettings_{run}_{label}"
            user = core(access, f"/divisions/{demo['division_a']}/users", {"role": label, "full_name": f"DEV HW {run} {label}", "login": name, "password": password, **({"class": 9} if label == "student" else {})})
            demo["users"][label] = {"login": name, "account_id": user["account_id"], "role": label}
        for label in ("viewer", "editor", "monitor", "student", "proctor"):
            actor = login(demo["users"][label]["login"])
            check(settings(actor) == DEFAULTS, f"{label} can read allowed settings")
            actor.call("/bff/homework/settings", "PUT", DEFAULTS, expected=403)
            actor.call("/bff/homework/audit?limit=1&after=0", expected=403)
            check(True, f"{label} cannot change settings or read administrator audit")
        denied = login(demo["users"]["denied"]["login"])
        denied.call("/bff/homework/settings", expected=403)
        check(True, "unrelated delegated permissions do not grant settings reads")

        for patch in ({"max_source_bytes": 0}, {"max_output_bytes": 104857601}, {"max_pages": 201}, {"dpi": 71}, {"jpeg_quality": 96}, {"presign_ttl_seconds": 59}, {"upload_stale_seconds": 599}, {"dpi": 120.5}, {"unexpected": 1}):
            admin.call("/bff/homework/settings", "PUT", {**DEFAULTS, **patch}, expected=400)
        check(admin.call("/bff/homework/audit?limit=200&after=0")["items"] == [], "invalid settings create neither changes nor audit records")
        for query in ("limit=0", "limit=201", "after=-1", "after=broken"):
            admin.call("/bff/homework/audit?" + query, expected=400)
        for query in ("division_id=" + str(demo["division_b"]), "organization_id=2001", "url=http://invalid"):
            admin.call("/bff/homework/settings?" + query, expected=400)
            admin.call("/bff/homework/audit?" + query, expected=400)
        check(True, "pagination ranges and browser supplied organization/division/upstream are rejected")

        # 55 valid synthetic updates provide a real second audit page for the UI.
        for index in range(55):
            desired = {**DEFAULTS, "max_pages": 36 + index % 2}
            check(admin.call("/bff/homework/settings", "PUT", desired) == desired, "synthetic own-division settings update returns complete canonical values")
        demo["settings_a"] = desired
        demo["settings_b"] = {**DEFAULTS, "max_pages": 42}
        demo["settings_foreign"] = {**DEFAULTS, "max_pages": 43}
        admin_b.call("/bff/homework/settings", "PUT", demo["settings_b"])
        foreign.call("/bff/homework/settings", "PUT", demo["settings_foreign"])
        audit = admin.call("/bff/homework/audit?limit=200&after=0")["items"]
        check(len(audit) == 55 and all(row["action"] == "settings.update" and row["entity_id"] == str(demo["division_a"]) for row in audit), "audit contains only 55 own-division settings writes")
        check(all(audit[i]["id"] < audit[i+1]["id"] for i in range(len(audit)-1)), "audit owner cursor order is strictly increasing")
        first = admin.call("/bff/homework/audit?limit=51&after=0")["items"]
        second = admin.call(f"/bff/homework/audit?limit=51&after={first[49]['id']}")["items"]
        check(len(first) == 51 and second == audit[50:], "extra-row pagination proves a second page without duplicates or skipped rows")
        demo["audit_count"] = len(audit)
        demo["first_audit_id"], demo["last_audit_id"] = audit[0]["id"], audit[-1]["id"]
        check(settings(admin_b) == demo["settings_b"] and len(admin_b.call("/bff/homework/audit?limit=200")["items"]) == 1, "division B keeps independent settings and audit")
        check(settings(foreign) == demo["settings_foreign"] and len(foreign.call("/bff/homework/audit?limit=200")["items"]) == 1, "foreign organization keeps independent settings and audit")
        previous = admin.revision
        admin.call("/bff/auth/context", "POST", {"organization_id": 1001, "division_id": demo["division_b"]})
        admin.call("/bff/homework/settings", expected=409, revision=previous)
        admin.call("/bff/homework/settings", "PUT", demo["settings_a"], expected=409, revision=previous)
        admin.call("/bff/homework/audit", expected=409, revision=previous)
        check(settings(admin) == demo["settings_b"], "cabinet switch changes actual settings and rejects stale reads and writes")
        admin.call("/bff/auth/context", "POST", {"organization_id": 2001, "division_id": demo["foreign_division"]}, expected=403)
        check(True, "full administrator cannot switch to unauthorized foreign organization")
        admin.call("/bff/auth/context", "POST", {"organization_id": 1001, "division_id": demo["division_a"]})
        check(settings(admin) == demo["settings_a"], "returning to division A restores its own settings")
        report["status"] = "passed"
    except Exception as failure:
        report["error"] = str(failure)
        raise
    finally:
        for session in sessions:
            try:
                session.close()
            except Exception:
                pass
        for access in owner_tokens:
            try:
                request(direct, "http://127.0.0.1:8002", "/v1/auth/logout", "POST", {}, {"Authorization": "Bearer " + access})
            except Exception:
                pass
        for path, data in (("bff-homework-settings-audit-demo.json", demo), ("bff-homework-settings-audit-http-report.json", {**report, "check_count": len(checks)})):
            destination = ROOT / "dev/local" / path
            destination.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n")
            destination.chmod(0o600)
        print(json.dumps({"status": report["status"], "checks": len(checks), "run": run}))


if __name__ == "__main__":
    main()
