"""Two read-only native/BFF canonical dashboard scenarios after new CoreREADY.

Uses retained U07 actor and source fixture. No users, roles, memberships, tests,
SQL, runtime, browser or capability consumers are changed. Capability wire name
remains test_results; legacy dual-key precedence is unit evidence only.
"""
from __future__ import annotations

import argparse
import json
from pathlib import Path

from legacy_template_http_test import Native, no_secrets, save
from scope_guards_http_test import Session

ROOT = Path(__file__).resolve().parents[3]
REPORT = ROOT / "dev/local/core-dashboard-results-http-report.json"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run", action="store_true", help="Only after actual CoreREADY for canonical dashboard capability fix")
    if not parser.parse_args().run:
        print(json.dumps({"status": "pending_new_CoreREADY_isolated_ACK", "scenarios": 0}))
        return
    demo = json.loads((ROOT / "dev/local/bff-tests-results-access-demo.json").read_text())
    source = json.loads((ROOT / "dev/local/core-external-report-demo.json").read_text())
    assert demo["readiness"] == "ready" and demo["prefix"].startswith("DEV Results access ")
    assert demo["source_fixture_run"] == source["run"] and demo["division_a"] == source["division_a"]
    assert len(demo["commands"]) == 5 and all(command["status"] == "confirmed" for command in demo["commands"])
    password = json.loads((ROOT / "dev/local/accounts.json").read_text())["password"]
    report = {"status": "failed", "fixture_run": demo["run"], "checks": [], "scenarios": [], "subject_writes": 0,
              "runtime_control": False, "direct_database_access": False, "auth_cleanup_failures": 0,
              "limitations": ["legacy-only/dual-alias permission combinations are covered by the five-case unit matrix; public role API accepts canonical keys only",
                              "current React dashboard consumes totals and does not consume capabilities; this runner does not claim new UI wiring"]}

    def check(condition, label):
        assert condition, label
        report["checks"].append(label)

    native, session = Native(check), Session(check)
    query = "?date_from=" + source["period"]["date_from"] + "&date_to=" + source["period"]["date_to"]
    try:
        user = session.login(demo["actor"]["login"], password, demo["organization_id"], demo["division_a"])
        for name, permitted in (("a", True), ("b", False)):
            division = demo["division_" + name]
            if name == "b":
                user = session.switch(demo["organization_id"], division)["user"]
            token = native.login(demo["actor"]["login"], password, division, demo["organization_id"])
            direct_user = native.request("http://127.0.0.1:8002", "/v1/auth/me", token)["user"]
            check(user["account_id"] == direct_user["account_id"] == demo["actor"]["account_id"] and user["organization_id"] == direct_user["organization_id"] == demo["organization_id"] and user["division_id"] == direct_user["division_id"] == division, "same actual actor retains exact selected org/div in native and BFF identities")
            check(user["permissions"]["test-results"] == direct_user["permissions"]["test-results"] == {"view": permitted, "edit": False} and "test_results" not in user["permissions"] and "test_results" not in direct_user["permissions"], "live canonical permission is authoritative without an invented legacy alias")
            direct = native.core(f"/divisions/{division}/dashboard", token)
            proxied = session.core("dashboard")
            check(direct == proxied, "native and BFF dashboard DTOs agree exactly")
            check(direct["organization_id"] == demo["organization_id"] and direct["division_id"] == division and direct["role"] == "staff_admin" and isinstance(direct["division_name"], str) and bool(direct["division_name"]), "dashboard preserves role and selected division label without cross-scope identity")
            check(direct["totals"] == {"students": None, "staff": None, "groups": None, "schools": None}, "private user/group/school counts remain null rather than read or invented zero")
            expected_capabilities = ["test_results"] if permitted else ["tests"]
            check(direct["capabilities"] == expected_capabilities, "stable wire capability follows canonical result permission and keeps unrelated test access separate")
            no_secrets(direct)
            if permitted:
                direct_report = native.core(f"/divisions/{division}/reports/tests" + query, token)
                bff_report = session.core("reports/tests" + query)
                check(direct_report["items"] == bff_report["items"] and direct_report["cells"] == bff_report["cells"], "advertised result capability corresponds to an actually authorized same-scope report")
                check({item["target_id"] for item in direct_report["items"]} == {test["id"] for test in demo["external_tests"].values()} and all(cell["account_id"] in {demo["student"]["account_id"], demo["peer"]["account_id"]} for cell in direct_report["cells"]), "allowed report stays on retained own targets and current student identities")
                no_secrets(direct_report)
            else:
                native.core(f"/divisions/{division}/reports/tests" + query, token, expected=403)
                session.core("reports/tests" + query, expected=403)
                check(True, "explicit canonical result denial blocks both native and BFF reports despite tests capability")
            report["scenarios"].append({"division_id": division, "canonical_result_view": permitted,
                                        "capabilities": direct["capabilities"], "private_totals": direct["totals"],
                                        "report_http_status": 200 if permitted else 403})
        check(len(report["scenarios"]) == 2, "exactly two retained-scope read-only dashboard scenarios")
        report["status"] = "passed"
    except Exception as error:
        report["error"] = type(error).__name__ + ": " + str(error)
    finally:
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
        report["passed"] = len(report["checks"])
        save(REPORT, report)
        print(json.dumps({"status": report["status"], "scenarios": len(report["scenarios"]), "passed": report["passed"], "auth_cleanup_failures": report["auth_cleanup_failures"], "report": str(REPORT)}))
    if report["status"] != "passed":
        raise SystemExit(1)


if __name__ == "__main__":
    main()
