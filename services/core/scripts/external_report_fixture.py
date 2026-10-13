"""Create an isolated public-API Core/external-Tests report fixture.

Run only after the coordinator authorizes fresh synthetic fixtures. No existing
subject, direction, rating, import, SQL or runtime changes. Mutations dispatch
once and retain uncertain original command metadata before any network call.
"""
from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import secrets
import urllib.error
import urllib.request
import uuid

from legacy_template_http_test import Native, NoRedirects, no_secrets, save
from scope_guards_http_test import Session

ROOT = Path(__file__).resolve().parents[3]
AUTH, TESTS = "http://127.0.0.1:8002", "http://127.0.0.1:8004"
MANIFEST = ROOT / "dev/local/core-external-report-demo.json"
REPORT = ROOT / "dev/local/core-external-report-fixture-report.json"
DAY = "2031-04-05"


class Tests:
    def __init__(self, check):
        self.http = urllib.request.build_opener(NoRedirects())
        self.last_status, self.check = None, check

    def request(self, path, token, method="GET", body=None, expected=200, key=None):
        assert path.startswith("/v1/tests/divisions/") and not path.startswith("//")
        headers = {"Authorization": "Bearer " + token}
        data = None if body is None else json.dumps(body, ensure_ascii=False).encode()
        if data is not None:
            headers["Content-Type"] = "application/json"
        if key:
            headers["Idempotency-Key"] = key
        self.last_status = None
        request = urllib.request.Request(TESTS + path, method=method, data=data, headers=headers)
        try:
            response = self.http.open(request, timeout=30)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            self.last_status = response.code
            raw = response.read((1 << 20) + 1)
        self.check(self.last_status == expected, f"Tests {method} {path} expected{expected} got{self.last_status}")
        assert len(raw) <= 1 << 20, "Oversized public Tests response"
        envelope = json.loads(raw)
        self.check(envelope.get("status") is True, "native Tests success envelope")
        return envelope["data"]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run", action="store_true")
    parser.add_argument("--resume", action="store_true", help="Continue only confirmed partial fixtures without replaying commands")
    args = parser.parse_args()
    if not args.run:
        print(json.dumps({"status": "pending_explicit_fresh_fixture_authorization", "live_checks": 0}))
        return
    previous = None
    if MANIFEST.exists():
        previous = json.loads(MANIFEST.read_text())
        if any(command["status"] == "pending" for command in previous.get("commands", [])):
            raise SystemExit("Resolve the original uncertain command; a new fixture cannot replace it")
        assert previous.get("readiness") != "ready", "Use the retained ready fixture rather than creating duplicates"
        assert args.resume, "Retain partial fixture; explicit --resume is required"
    else:
        assert not args.resume, "No partial fixture is available"

    report = {"status": "failed", "checks": [], "public_api_only": True, "direct_database_access": False,
              "runtime_control": False, "ratings_writes": 0, "direction_writes": 0, "import_commits": 0}
    demo = {"run": secrets.token_hex(5), "organization_id": 1001, "direction_id": 1001,
            "readiness": "preparing", "commands": [], "actors": {}, "tests": {}, "groups": {},
            "period": {"date_from": DAY, "date_to": DAY}}
    demo["prefix"] = "DEV Report " + demo["run"]
    if previous is not None:
        demo = previous

    def check(condition, label):
        assert condition, label
        report["checks"].append(label)

    native, tests, sessions = Native(check), Tests(check), []

    def command(owner, path, token, body, method="POST", expected=201, idempotent=False):
        receipt = {"owner": owner, "route": path, "method": method, "status": "pending",
                   "request_sha256": hashlib.sha256(json.dumps(body, ensure_ascii=False).encode()).hexdigest()}
        if idempotent:
            receipt["key"] = str(uuid.uuid4())
        demo["commands"].append(receipt)
        save(MANIFEST, demo)
        client = native if owner == "core" else tests
        try:
            if owner == "core":
                result = native.core(path, token, method=method, body=body, expected=expected, key=receipt.get("key"))
            else:
                result = tests.request(path, token, method=method, body=body, expected=expected, key=receipt.get("key"))
        except Exception:
            if client.last_status is not None and 400 <= client.last_status < 500:
                receipt["status"], receipt["http_status"] = "rejected", client.last_status
                save(MANIFEST, demo)
            raise
        receipt["status"], receipt["http_status"] = "confirmed", client.last_status
        save(MANIFEST, demo)
        return result

    try:
        password = json.loads((ROOT / "dev/local/accounts.json").read_text())["password"]
        admin = native.login("dev_admin", password)
        directions = native.core("/directions", admin)
        check(any(row["id"] == demo["direction_id"] for row in directions), "existing own organization direction1001 is read-only and available")
        if "division_a" not in demo:
            demo["division_a"] = command("core", "/divisions", admin, {"name": demo["prefix"] + " isolated"})["id"]
        admin = native.login("dev_admin", password, demo["division_a"])
        for name in ("a", "other"):
            if name not in demo["groups"]:
                demo["groups"][name] = command("core", f"/divisions/{demo['division_a']}/groups", admin, {"name": demo["prefix"] + " " + name})["id"]
        for name, role, cls in (("student", "student", 7), ("peer", "student", 8), ("supervisor", "supervisor", 0)):
            if name in demo["actors"]:
                continue
            login = "report_" + demo["run"] + "_" + name
            body = {"role": role, "full_name": demo["prefix"] + " " + name, "class": cls,
                    "group_id": demo["groups"]["a"] if role == "student" else None,
                    "login": login, "password": password, "tg_name": "report_" + demo["run"] + "_" + name}
            account = command("core", f"/divisions/{demo['division_a']}/users", admin, body)["account_id"]
            profile = native.core(f"/divisions/{demo['division_a']}/users/{account}", admin)
            demo["actors"][name] = {"account_id": account, "profile_id": profile["profile_id"],
                                    "login": login, "full_name": body["full_name"], "class": cls, "role": role}
            save(MANIFEST, demo)
        check(demo["actors"]["student"]["account_id"] != demo["actors"]["student"]["profile_id"], "student account/profile identifiers are distinct")
        base = f"/v1/tests/divisions/{demo['division_a']}/external-tests"
        for name in ("zero", "ungraded", "absent"):
            if name in demo["tests"]:
                target = demo["tests"][name]
            else:
                target = command("tests", base, admin, {"name": demo["prefix"] + " " + name,
                                "direction_id": demo["direction_id"], "date": DAY}, idempotent=True)
                demo["tests"][name] = {key: target[key] for key in ("id", "revision", "name", "date", "direction_id", "direction_name")}
                save(MANIFEST, demo)
            if name != "absent":
                if target["revision"] == 1:
                    updated = command("tests", base + "/" + target["id"] + "/results/" + str(demo["actors"]["student"]["account_id"]), admin,
                                  {"expected_revision": target["revision"], "expected_result_revision": 0,
                                   "score": 0 if name == "zero" else None}, method="PUT", expected=200, idempotent=True)
                    demo["tests"][name]["revision"] = updated["revision"]
                    save(MANIFEST, demo)
                rows = tests.request(base + "/" + target["id"] + "/results?limit=100", admin)
                check(rows["total"] == 1 and len(rows["items"]) == 1, "own external target has exactly one explicit result")
                result = rows["items"][0]
                check(result["account_id"] == demo["actors"]["student"]["account_id"] and result["student_id"] == demo["actors"]["student"]["profile_id"] and result["score"] == (0 if name == "zero" else None), "public owner retains exact zero or explicit null and separate student identity")
                demo["tests"][name]["result_id"], demo["tests"][name]["result_revision"] = result["id"], result["revision"]
                save(MANIFEST, demo)
        supervisor = native.login(demo["actors"]["supervisor"]["login"], password, demo["division_a"])
        student = native.login(demo["actors"]["student"]["login"], password, demo["division_a"])
        tail = f"/divisions/{demo['division_a']}/reports/tests?date_from={DAY}&date_to={DAY}"
        direct = native.core(tail, supervisor)
        session = Session(check)
        sessions.append(session)
        session.login(demo["actors"]["supervisor"]["login"], password, 1001, demo["division_a"])
        proxied = session.core(f"reports/tests?date_from={DAY}&date_to={DAY}")
        check(direct["students"] == proxied["students"] and direct["items"] == proxied["items"] and direct["cells"] == proxied["cells"], "native and BFF supervisor reports expose identical complete fresh matrix")
        check({row["account_id"] for row in direct["students"]} == {demo["actors"][name]["account_id"] for name in ("student", "peer")} and len(direct["items"]) == 3 and len(direct["cells"]) == 6, "isolated report has exactly two students and three external targets")
        for name, target in demo["tests"].items():
            item = next(row for row in direct["items"] if row["target_id"] == target["id"])
            check(item["id"] == "external:" + target["id"] and item["kind"] == "external_test" and item["date"] == DAY and item["source_revision"] == target["revision"], "report preserves external target kind, native identity, MSK date and revision")
            own = next(row for row in direct["cells"] if row["item_id"] == item["id"] and row["account_id"] == demo["actors"]["student"]["account_id"])
            other = next(row for row in direct["cells"] if row["item_id"] == item["id"] and row["account_id"] == demo["actors"]["peer"]["account_id"])
            check(other["status"] == "missing" and other["score"] is None and not other.get("recorded"), "peer absence is never synthesized as a grade or recorded null")
            if name == "zero":
                check(own["status"] == "ready" and own["score"] == 0 and own["recorded"] is True, "actual zero is a numeric ready result")
            elif name == "ungraded":
                check(own["status"] == "missing" and own["score"] is None and own["recorded"] is True and own["external_result_id"] == target["result_id"], "actual explicit null is recorded without a fabricated numeric grade")
            else:
                check(own["status"] == "missing" and own["score"] is None and not own.get("recorded") and not own.get("external_result_id"), "no result remains absent rather than an explicit null record")
        no_secrets(direct)
        native.core(tail, student, expected=403)
        native.core("/divisions/1001/reports/tests?date_from=" + DAY + "&date_to=" + DAY, supervisor, expected=403)
        for query in ("organization_id=2001", "division_id=1001", "limit=1", "offset=1"):
            session.core(f"reports/tests?date_from={DAY}&date_to={DAY}&{query}", expected=400)
        session.call("/bff/auth/context", "POST", {"organization_id": 1001, "division_id": 1001}, expected=403)
        check(len(direct["source_input_hash"]) == 64 and len(direct["external_source_version"]) == 64, "live report exposes genuine bounded owner source hashes")
        demo["report"] = direct
        demo["readiness"] = "ready"
        report["status"] = "passed"
    except Exception as error:
        report["error"] = type(error).__name__ + ": " + str(error)
    finally:
        for session in sessions:
            try:
                session.close()
            except Exception:
                pass
        for token in native.tokens:
            try:
                native.request(AUTH, "/v1/auth/logout", token, method="POST")
            except Exception:
                pass
        save(MANIFEST, demo)
        report["passed"] = len(report["checks"])
        report["run"] = demo["run"]
        report["pending_commands"] = sum(item["status"] == "pending" for item in demo["commands"])
        save(REPORT, report)
        print(json.dumps({"status": report["status"], "passed": report["passed"], "pending_commands": report["pending_commands"], "report": str(REPORT)}))
    if report["status"] != "passed":
        raise SystemExit(1)


if __name__ == "__main__":
    main()
