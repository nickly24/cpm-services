"""Three read-only native Core student-group privacy scenarios after CoreREADY.

Reuses the retained two-student public API fixture. No source commands, SQL,
roles, runtime, broker, BFF or browser operations. Auth sessions close finally.
"""
from __future__ import annotations

import argparse
import json
from pathlib import Path
import urllib.parse

from legacy_template_http_test import Native, save

ROOT = Path(__file__).resolve().parents[3]
MANIFEST = ROOT / "dev/local/core-external-report-demo.json"
REPORT = ROOT / "dev/local/core-group-privacy-http-report.json"
FIELDS = {"account_id", "profile_id", "role", "full_name", "school_id", "class", "group_id", "is_active"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run", action="store_true")
    if not parser.parse_args().run:
        print(json.dumps({"status": "pending_explicit_CoreprivacyREADY", "scenarios": 0}))
        return
    demo = json.loads(MANIFEST.read_text())
    assert demo["readiness"] == "ready" and demo["prefix"].startswith("DEV Report ")
    assert not any(command["status"] == "pending" for command in demo["commands"])
    password = json.loads((ROOT / "dev/local/accounts.json").read_text())["password"]
    report = {"status": "failed", "run": demo["run"], "scenarios": [], "checks": [],
              "subject_writes": 0, "runtime_control": False, "direct_database_access": False}

    def check(condition, label):
        assert condition, label
        report["checks"].append(label)

    native = Native(check)
    try:
        division, group = demo["division_a"], demo["groups"]["a"]
        own, peer = demo["actors"]["student"], demo["actors"]["peer"]
        admin = native.login("dev_admin", password, division)
        student = native.login(own["login"], password, division)
        tail = f"/divisions/{division}/groups/{group}/members"
        full = native.core(tail + "?limit=200", admin)
        check({row["account_id"] for row in full} == {own["account_id"], peer["account_id"]}, "full admin baseline confirms both current synthetic group participants")
        minimal = native.core(tail + "?limit=200", student)
        check(len(minimal) == 1 and minimal[0]["account_id"] == own["account_id"] and minimal[0]["profile_id"] == own["profile_id"], "student receives only its own distinct account/profile identity")
        check(set(minimal[0]) == FIELDS and minimal[0]["full_name"] == own["full_name"] and minimal[0]["group_id"] == group, "own group DTO contains exactly the minimal display fields without contacts or credentials")
        check(peer["full_name"] not in json.dumps(minimal, ensure_ascii=False), "peer name and account are absent from the student roster")
        report["scenarios"].append("admin complete roster versus student self-only minimal projection")

        filtered = native.core(tail + "?limit=200&q=" + urllib.parse.quote(peer["full_name"]), student)
        check(filtered == [], "peer full-name search cannot recover any other student")
        native.core(f"/divisions/{division}/groups/{demo['groups']['other']}/members", student, expected=403)
        report["scenarios"].append("peer search empty and other own-division group denied")

        paged = native.core(tail + "?limit=1&offset=1", student)
        check(paged == [], "pagination occurs after the student account fence and cannot reveal the peer")
        report["scenarios"].append("offset after own participant cannot expose another group member")
        check(len(report["scenarios"]) == 3, "exactly three narrowly scoped native privacy scenarios")
        report["status"] = "passed"
    except Exception as error:
        report["error"] = type(error).__name__ + ": " + str(error)
    finally:
        for token in native.tokens:
            try:
                native.request("http://127.0.0.1:8002", "/v1/auth/logout", token, method="POST")
            except Exception:
                pass
        report["passed"] = len(report["checks"])
        save(REPORT, report)
        print(json.dumps({"status": report["status"], "scenarios": len(report["scenarios"]), "passed": report["passed"], "report": str(REPORT)}))
    if report["status"] != "passed":
        raise SystemExit(1)


if __name__ == "__main__":
    main()
