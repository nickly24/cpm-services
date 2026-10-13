"""Prepare U07 canonical test-results access fixtures after isolated ACK.

Exactly five public Core commands create one new division, two new roles, one
new staff actor and its second membership. Existing Tests/report subjects and
roles are read-only. Core generic CRUD has no idempotency receipt contract;
local original UUID/digest is pinned before one dispatch and is never replayed.
"""
from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import secrets
import sys
import uuid

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / "services/core/scripts"))
from legacy_template_http_test import Native, no_secrets, save
from scope_guards_http_test import Session, request, BFF, ORIGIN

MANIFEST = ROOT / "dev/local/bff-tests-results-access-demo.json"
REPORT = ROOT / "dev/local/bff-tests-results-access-http-report.json"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--prepare", action="store_true", help="Dispatch only after the coordinator isolated-acceptance ACK")
    if not parser.parse_args().prepare:
        print(json.dumps({"status": "pending_isolated_acceptance_ACK", "live_checks": 0, "planned_core_commands": 5}))
        return
    if MANIFEST.exists():
        previous = json.loads(MANIFEST.read_text())
        if any(command["status"] == "pending" for command in previous.get("commands", [])):
            raise SystemExit("Preserve original uncertain command UUID/body digest; resolve canonical state manually without replay")
        raise SystemExit("Retain the existing fixture; no automatic new roles, scopes or command keys")
    source = json.loads((ROOT / "dev/local/core-external-report-demo.json").read_text())
    assert source["readiness"] == "ready" and source["prefix"].startswith("DEV Report ")
    assert not any(command["status"] == "pending" for command in source["commands"])
    password = json.loads((ROOT / "dev/local/accounts.json").read_text())["password"]
    demo = {"run": secrets.token_hex(5), "readiness": "preparing", "organization_id": source["organization_id"],
            "division_a": source["division_a"], "commands": [], "roles": {}, "source_fixture_run": source["run"],
            "student": source["actors"]["student"], "peer": source["actors"]["peer"], "external_tests": source["tests"]}
    demo["prefix"] = "DEV Results access " + demo["run"]
    report = {"status": "failed", "run": demo["run"], "checks": [], "core_commands": 0, "test_source_writes": 0,
              "existing_role_writes": 0, "runtime_control": False, "direct_database_access": False, "auth_cleanup_failures": 0,
              "limitations": ["dual legacy-alias/canonical-key combination cannot be created through public CoreRole API; unit8 only",
                              "Core generic CRUD does not promise idempotency receipts; pinned original UUID is local command metadata"]}

    def check(condition, label):
        assert condition, label
        report["checks"].append(label)

    native, sessions = Native(check), []

    def command(path, token, body, method="POST", expected=201):
        receipt = {"route": path, "method": method, "key": str(uuid.uuid4()), "status": "pending", "idempotency_supported": False,
                   "request_sha256": hashlib.sha256(json.dumps(body, ensure_ascii=False).encode()).hexdigest()}
        demo["commands"].append(receipt)
        save(MANIFEST, demo)
        try:
            result = native.core(path, token, method=method, body=body, expected=expected, key=receipt["key"])
        except Exception:
            if native.last_status is not None and 400 <= native.last_status < 500:
                receipt["status"], receipt["http_status"] = "rejected", native.last_status
                save(MANIFEST, demo)
            raise
        receipt["status"], receipt["http_status"] = "confirmed", native.last_status
        save(MANIFEST, demo)
        return result

    def bff_tests(session, tail, expected=200, revision=None):
        value, _ = request(session.http, BFF, "/bff/tests/" + tail, headers={"Origin": ORIGIN, "X-CPM-Request": "1",
                           "X-CPM-Context": session.revision if revision is None else revision}, expected=expected)
        check(True, "public BFF Tests scoped read expected" + str(expected))
        return value

    try:
        admin = native.login("dev_admin", password, demo["division_a"], demo["organization_id"])
        demo["division_b"] = command("/divisions", admin, {"name": demo["prefix"] + " denied"})["id"]
        for name, tests_view, results_view in (("readonly", False, True), ("denied", True, False)):
            permissions = {"tests": {"view": tests_view, "edit": False}, "test-results": {"view": results_view, "edit": False}}
            role = command("/roles", admin, {"name": demo["prefix"] + " " + name, "permissions": permissions})
            demo["roles"][name] = {"id": role["id"], "permissions": permissions}
            save(MANIFEST, demo)
        login = "results_access_" + demo["run"]
        account = command(f"/divisions/{demo['division_a']}/users", admin, {"role": "staff_admin", "role_id": demo["roles"]["readonly"]["id"],
                          "full_name": demo["prefix"] + " actor", "login": login, "password": password})["account_id"]
        demo["actor"] = {"account_id": account, "login": login, "full_name": demo["prefix"] + " actor"}
        save(MANIFEST, demo)
        command(f"/divisions/{demo['division_b']}/memberships/{account}", admin, {"role_id": demo["roles"]["denied"]["id"]}, method="PUT", expected=200)
        check(len(demo["commands"]) == 5 and all(command["status"] == "confirmed" for command in demo["commands"]), "exactly five isolated new Core commands confirmed without changing existing actors/roles")
        memberships = native.core(f"/organization/users/{account}/memberships", admin)
        check({item["division_id"]: item["role_id"] for item in memberships["memberships"]} == {demo["division_a"]: demo["roles"]["readonly"]["id"], demo["division_b"]: demo["roles"]["denied"]["id"]}, "same fresh actor has exact independent canonical roles in its two own scopes")
        session = Session(check)
        sessions.append(session)
        identity = session.login(login, password, demo["organization_id"], demo["division_a"])
        check(identity["account_id"] == account and identity["permissions"]["test-results"] == {"view": True, "edit": False} and "test_results" not in identity["permissions"], "actual Auth grants canonical result view only with no fabricated legacy alias")
        tail = f"external-results?account_id={demo['student']['account_id']}&limit=100&offset=0"
        allowed = bff_tests(session, tail)
        check(allowed["total"] == 2 and {item["test"]["id"] for item in allowed["items"]} == {demo["external_tests"][name]["id"] for name in ("zero", "ungraded")}, "canonical-only result viewer reads exact two retained external results without test catalogue rights")
        check({item["result"]["score"] for item in allowed["items"]} == {0, None} and all(item["result"]["account_id"] == demo["student"]["account_id"] and item["test"]["division_id"] == demo["division_a"] for item in allowed["items"]), "native zero/null results preserve selected account and division")
        no_secrets(allowed)
        bff_tests(session, "catalog", expected=403)
        revision_a = session.revision
        denied_identity = session.switch(demo["organization_id"], demo["division_b"])["user"]
        check(denied_identity["account_id"] == account and denied_identity["permissions"]["test-results"] == {"view": False, "edit": False}, "same actor selected division changes live canonical permission to explicit false")
        bff_tests(session, tail, expected=403)
        bff_tests(session, "submissions?limit=100&offset=0", expected=403)
        bff_tests(session, tail, expected=409, revision=revision_a)
        bff_tests(session, "external-tests/" + demo["external_tests"]["zero"]["id"], expected=404)
        session.switch(demo["organization_id"], demo["division_a"])
        check(bff_tests(session, tail)["items"] == allowed["items"], "explicit switch back restores only the same allowed source rows")
        demo["readiness"] = "ready"
        report["status"] = "passed"
    except Exception as error:
        report["error"] = type(error).__name__ + ": " + str(error)
    finally:
        for session in sessions:
            try:
                session.close()
            except Exception:
                report["auth_cleanup_failures"] += 1
                report["status"] = "failed"
        for token in native.tokens:
            try:
                native.request("http://127.0.0.1:8002", "/v1/auth/logout", token, method="POST")
            except Exception:
                report["auth_cleanup_failures"] += 1
                report["status"] = "failed"
        save(MANIFEST, demo)
        report["passed"] = len(report["checks"])
        report["core_commands"] = len(demo["commands"])
        report["pending_commands"] = sum(command["status"] == "pending" for command in demo["commands"])
        save(REPORT, report)
        print(json.dumps({"status": report["status"], "passed": report["passed"], "core_commands": report["core_commands"], "pending_commands": report["pending_commands"], "report": str(REPORT)}))
    if report["status"] != "passed":
        raise SystemExit(1)


if __name__ == "__main__":
    main()
