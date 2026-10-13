"""Read-only Core/native/BFF external-test report acceptance after CoreREADY.

Uses retained API-owned Tests synthetic fixtures, never writes source subjects.
Only Auth login/context/logout and the read-only service snapshot POST are used.
No SQL, Uploads requests, source commit, migration or runtime control.
"""
from __future__ import annotations

import argparse
from collections import Counter
import json
from pathlib import Path
import sys
import urllib.error
import urllib.request
from zoneinfo import ZoneInfo
from datetime import datetime

from legacy_template_http_test import Native, NoRedirects, no_secrets, save
from scope_guards_http_test import Session

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / "dev/scripts"))
from export_structure import read_env

TESTS = "http://127.0.0.1:8004"
CORE = "http://127.0.0.1:8003"
REPORT = ROOT / "dev/local/core-external-report-http-report.json"
DAY = "2041-05-06"


def owner_snapshot(http, key, organization, division, day):
    body = {"organization_id": organization, "division_id": division, "date_from": day, "date_to": day, "as_of": datetime.now(ZoneInfo("UTC")).isoformat().replace("+00:00", "Z")}
    request = urllib.request.Request(TESTS + "/internal/tests/ratings/snapshot", data=json.dumps(body).encode(), method="POST", headers={"Content-Type": "application/json", "X-Service-Key": key})
    with http.open(request, timeout=30) as response:
        raw = response.read((32 << 20) + 1)
        assert response.status == 200 and len(raw) <= 32 << 20, "Bounded Tests snapshot unavailable"
    envelope = json.loads(raw)
    assert envelope.get("status") is True, "Owner snapshot success envelope missing"
    return envelope["data"]


def compare(report, source, organization, division, day, check):
    check(report["source"] == "tests" and report["organization_id"] == organization and report["division_id"] == division and report["date_from"] == day and report["date_to"] == day, "Core report preserves exact selected org/div and requested calendar period")
    check(report["source_input_hash"] == source["input_hash"] and report["external_source_version"] == source["external_source_version"], "Core exposes exact live scoped source hash and external catalogue/results version")
    check(source["external_domain"] == {"schema_version": 1, "status": "ready", "organization_id": organization, "division_id": division}, "actual owner proves external domain readiness for the exact scope")
    expected_items, expected_cells = {}, {}
    roster = {student["account_id"] for student in report["students"]}
    for kind, tests in (("test", source["tests"]), ("external_test", source["external_tests"])):
        for test in tests:
            identifier = ("external:" if kind == "external_test" else "") + test["test_id"]
            date = datetime.fromisoformat(test["start_at"].replace("Z", "+00:00")).astimezone(ZoneInfo("Europe/Moscow")).date().isoformat()
            expected_items[identifier] = (kind, test["test_id"], test["title"], date, test.get("direction_id"), test.get("direction_name", ""), test.get("source_revision", 0))
            for result in test["results"]:
                if result["account_id"] in roster:
                    expected_cells[(result["account_id"], identifier)] = (kind, result)
    check(len(expected_items) == len(report["items"]) and len(report["cells"]) == len(roster) * len(expected_items), "full bounded internal/external catalogue and matrix arrive without paging truncation")
    order = []
    for item in report["items"]:
        actual = (item["kind"], item["target_id"], item["name"], item["date"], item.get("direction_id"), item.get("direction_name", ""), item.get("source_revision", 0))
        assert expected_items[item["id"]] == actual, "Core changed native target/name/date/direction/revision"
        order.append((item["date"], item["id"]))
    check(order == sorted(order), "canonical MSK date and namespaced stable IDs order internal and external work")
    zero, unknown, absent = 0, 0, 0
    for cell in report["cells"]:
        expected = expected_cells.get((cell["account_id"], cell["item_id"]))
        assert cell["account_id"] in roster, "Owner-only or profile-ID account leaked into current roster"
        if expected is None:
            assert cell["status"] == "missing" and cell["score"] is None and not cell.get("recorded") and not cell.get("external_result_id"), "Absent source result was fabricated"
            absent += 1
            continue
        kind, result = expected
        assert cell["status"] == result["calculation_status"] and cell["submission_id"] == result["submission_id"] and cell["calculation_revision"] == result["calculation_revision"], "Source result identity/status/version changed"
        assert cell["score"] == (result["score"] if result["calculation_status"] == "ready" else None), "Pending/null result acquired an old or invented score"
        if kind == "external_test":
            assert cell["recorded"] is True and cell["external_result_id"] == result["submission_id"] and cell["result_version"] == result["calculation_revision"], "Recorded external result identity is missing"
            zero += cell["score"] == 0
            unknown += result["calculation_status"] == "missing" and cell["score"] is None
    check(True, "actual external zero/nullable/missing results retain account identity and native result revision")
    check("scoped_external_tests" in report["limitations"] and "external_legacy_tests_not_connected" not in report["limitations"], "Core declares the genuinely connected external source")
    no_secrets(report)
    return {"organization_id": organization, "division_id": division, "day": day, "student_count": len(roster), "items": dict(Counter(item["kind"] for item in report["items"])), "external_zero_cells": zero, "external_recorded_null_cells": unknown, "absent_cells": absent, "source_input_hash": report["source_input_hash"], "external_source_version": report["external_source_version"]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run", action="store_true")
    if not parser.parse_args().run:
        print(json.dumps({"status": "pending_explicit_CoreREADY", "live_checks": 0}))
        return
    fixtures = json.loads((ROOT / "dev/local/external-tests-integration-summary.json").read_text())
    assert fixtures["status"] == "passed" and fixtures["fixtures"]["prefix"].startswith("DEV External "), "Retained native synthetic fixtures are required"
    password = json.loads((ROOT / "dev/local/accounts.json").read_text())["password"]
    key = read_env(ROOT / "dev/local/core.env")["AUTH_INTERNAL_KEY"]
    report = {"status": "failed", "checks": [], "scopes": [], "subject_writes": 0, "uploads_requests": 0, "direct_database_access": False, "runtime_control": False, "native_fixture_run": fixtures["fixtures"]["run"]}

    def check(condition, label):
        assert condition, label
        report["checks"].append(label)

    native = Native(check)
    http, sessions = urllib.request.build_opener(NoRedirects()), []
    try:
        for organization, division, admin_name in ((1001, 1001, "dev_admin"), (1001, 1002, "dev_admin"), (2001, 2001, "dev_admin_math")):
            token = native.login(admin_name, password, division, organization)
            source = owner_snapshot(http, key, organization, division, DAY)
            query = f"?date_from={DAY}&date_to={DAY}"
            direct = native.core(f"/divisions/{division}/reports/tests" + query, token)
            summary = compare(direct, source, organization, division, DAY, check)
            bff = Session(check)
            sessions.append(bff)
            bff.login(admin_name, password, organization, division)
            proxied = bff.core("reports/tests" + query)
            compare(proxied, source, organization, division, DAY, check)
            check({(row["account_id"], row["item_id"]): row for row in direct["cells"]} == {(row["account_id"], row["item_id"]): row for row in proxied["cells"]}, "public BFF and native Core expose the same complete result cells")
            report["scopes"].append(summary)
            for extra in ("&organization_id=999", "&division_id=999", "&limit=1", "&offset=1", "&date_from=2041-05-05"):
                bff.core("reports/tests" + query + extra, expected=400)
            native.core(f"/divisions/{2001 if organization == 1001 else 1001}/reports/tests" + query, token, expected=404)
        check(sum(scope["items"].get("external_test", 0) for scope in report["scopes"]) > 0, "actual retained native external fixtures appear in live Core columns")
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
                native.request("http://127.0.0.1:8002", "/v1/auth/logout", token, method="POST")
            except Exception:
                pass
        report["passed"] = len(report["checks"])
        save(REPORT, report)
        print(json.dumps({"status": report["status"], "passed": report["passed"], "report": str(REPORT)}))
    if report["status"] != "passed":
        raise SystemExit(1)


if __name__ == "__main__":
    main()
