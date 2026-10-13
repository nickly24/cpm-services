"""Repeatable, read-only BFF ratings checks after fixture creation and rebuild.

Business writes, service-key routes, DB edits and broker operations are forbidden.
Credentials are read from the ignored local accounts file and never logged.
"""
from __future__ import annotations

import json
import math
from pathlib import Path

from exams_http_smoke import Session

ROOT = Path(__file__).resolve().parents[3]


def main():
    demo = json.loads((ROOT / "dev/local/bff-ratings-demo.json").read_text())
    password = json.loads((ROOT / "dev/local/accounts.json").read_text())["password"]
    checks, sessions = [], []
    outcome, error = "failed", None

    def check(value, label):
        assert value, label
        checks.append(label)

    def login(name, division=None):
        session = Session(check)
        sessions.append(session)
        session.login(name, password, demo["organization_id"], division or demo["division_a"])
        return session

    def rating(session, suffix="ratings", **options):
        return session.request("/bff/ratings/" + suffix, **options)

    def expected(row, expected):
        actual = row["rating"]
        for component in ("homework", "tests", "exams"):
            check(math.isclose(actual[component]["rating"], expected[component], abs_tol=1e-9), component + " agrees with independently expected mean")
        check(math.isclose(actual["final_rating"], expected["final"], abs_tol=1e-9), "weighted final agrees with independently expected formula")
        check(row["place"] == expected["place"], "owner global place is preserved")

    try:
        anonymous = Session(check)
        anonymous.request("/bff/ratings/ratings", expected=401)
        check(True, "ratings require an opaque authenticated cookie")
        spec = anonymous.request("/openapi.json")
        check(sum(len(methods) for path, methods in spec["paths"].items() if path.startswith("/bff/ratings/")) == 10, "embedded browser contract contains all ten public owner operations")
        check(not any(path.startswith("/internal/ratings") for path in spec["paths"]), "service-key calculation inputs and callbacks are absent from the browser contract")

        admin = login("dev_admin")
        current = rating(admin)
        rows = {row["profile"]["account_id"]: row for row in current["ratings"]}
        check(current["total"] == len(rows) and demo["student_account_id"] in rows, "selected division publication contains the scoped student profiles")
        expected(rows[demo["student_account_id"]], demo["expected_a"])
        check(current["freshness"]["exams_mode"] == "required", "published rating used the real exams owner")
        check(current["freshness"]["external_tests_mode"] == "dev_internal_only", "external-test dev limitation remains explicit in the published version")
        report = rating(admin, "report")
        check(report["period"] == demo["period"] and len(report["students"]) == len(rows), "JSON report uses the published period and all authorized rows")
        detail = rating(admin, "accounts/" + str(demo["student_account_id"]))
        check(detail["rating"]["profile"]["account_id"] == demo["student_account_id"], "published details use account identity instead of profile identity")
        jobs = rating(admin, "jobs?limit=200")["jobs"]
        versions = rating(admin, "versions")["versions"]
        check(all(row["division_id"] == demo["division_a"] for row in jobs + versions), "job history and published versions are scoped to the selected division")
        check(all(row["status"] == "completed" for row in versions), "version selector exposes complete atomic publications only")
        first = min(versions, key=lambda row: row["revision"])
        historical = rating(admin, "report?version=" + first["id"])
        check(historical["freshness"]["version_id"] == first["id"], "historical report retains the requested immutable publication identity")
        history_row = next(row for row in historical["students"] if row["profile"]["account_id"] == demo["student_account_id"])
        check(math.isclose(history_row["rating"]["exams"]["rating"], 3, abs_tol=1e-9), "old version retains the appealed exam grade before the worse retake")

        for suffix in ("ratings?organization_id=2", "ratings?division_id=2", "ratings?division_ids=2", "ratings?target_division_id=2", "ratings?url=http://evil.example"):
            rating(admin, suffix, expected=400)
        check(True, "browser scope and upstream substitutions are rejected")
        for suffix in ("internal/input", "jobs/" + first["id"] + "/input", "divisions/2/ratings"):
            rating(admin, suffix, expected=501)
        check(True, "private and explicit-division paths are outside the allowlist")
        old = admin.revision
        admin.request("/bff/auth/context", "POST", {"organization_id": demo["organization_id"], "division_id": demo["division_b"]})
        rating(admin, revision=old, expected=409)
        check(True, "switching cabinets fences every previous ratings request")
        other = rating(admin)
        row_b = next(row for row in other["ratings"] if row["profile"]["account_id"] == demo["student_account_id"])
        expected(row_b, demo["expected_b"])
        rating(admin, "jobs/" + first["id"], expected=404)
        check(True, "an organization administrator cannot read a job from an unselected division")

        student = login(demo["student_login"])
        mine = rating(student, "me")
        expected(mine["rating"], demo["expected_a"])
        check(rating(student)["total"] == 1, "student list contains only their own account")
        rating(student, "accounts/" + str(demo["student2_account_id"]), expected=404)
        for suffix in ("jobs", "versions", "report"):
            rating(student, suffix, expected=403)
        check(True, "student cannot read peers, administrative jobs or the full report")

        proctor = login(demo["proctor_login"])
        group_rows = rating(proctor)
        check(bool(group_rows["ratings"]) and all(row["profile"]["group_id"] == demo["group_a"] for row in group_rows["ratings"]), "proctor sees only students currently belonging to their group")
        rating(proctor, "accounts/" + str(demo["student2_account_id"]), expected=404)
        for suffix in ("jobs", "versions", "report"):
            rating(proctor, suffix, expected=403)
        check(True, "proctor cannot read the other group or administrator report/job history")

        supervisor = login(demo["supervisor_login"])
        check(bool(rating(supervisor, "jobs")["jobs"]) and rating(supervisor, "report")["period"] == demo["period"], "supervisor can read scoped job progress and the published report")
        viewer = login(demo["scoped_staff_view_login"])
        check(rating(viewer)["total"] == len(rows) and bool(rating(viewer, "versions")["versions"]), "ratings view-only employee reads their authorized published division")
        outcome = "passed"
    except Exception as exc:
        error = str(exc)
        raise
    finally:
        for session in sessions:
            try:
                session.close()
            except Exception:
                pass
        result = {"status": outcome, "checks": checks, "count": len(checks), "error": error}
        (ROOT / "dev/local/bff-ratings-http-smoke-report.json").write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
        print(json.dumps({"status": outcome, "count": len(checks)}, ensure_ascii=False))


if __name__ == "__main__":
    main()
