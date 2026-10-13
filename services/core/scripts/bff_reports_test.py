"""Read-only live Core source reports through the local public BFF.

No subject writes, direct DB access, owner service keys or broker operations.
Fixtures were created through public APIs by bff_integration_test.py (ratings).
Only this script's cookie sessions are created/switched/closed. Never log secrets.
"""
from __future__ import annotations

import json
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / "services/bff/scripts"))
from exams_http_smoke import Session


def main():
    demo = json.loads((ROOT / "dev/local/bff-ratings-demo.json").read_text())
    password = json.loads((ROOT / "dev/local/accounts.json").read_text())["password"]
    checks, sessions, reports = [], [], {}
    status, failure = "failed", None

    def check(value, label):
        assert value, label
        checks.append(label)

    def login(name):
        session = Session(check)
        sessions.append(session)
        session.login(name, password, demo["organization_id"], demo["division_a"])
        return session

    def report(session, source, suffix="", **options):
        period = demo["period"]
        return session.request(f"/bff/core/reports/{source}?date_from={period['date_from']}&date_to={period['date_to']}" + suffix, **options)

    def validate(data, source, division):
        check(data["source"] == source and data["organization_id"] == demo["organization_id"] and data["division_id"] == division, source + " report retains selected organization/division")
        check(data["date_from"] == demo["period"]["date_from"] and data["date_to"] == demo["period"]["date_to"], source + " report honors the independent requested period")
        accounts = {row["account_id"] for row in data["students"]}
        expected_accounts = {demo["student_account_id"], demo["student2_account_id"]} if division == demo["division_a"] else {demo["student_account_id"]}
        check(accounts == expected_accounts, source + " report roster uses current scoped students")
        check(all(row["profile_id"] != row["account_id"] for row in data["students"]), source + " report preserves separate account/profile identities")
        check(len(data["cells"]) == len(data["students"]) * len(data["items"]), source + " report has a complete bounded matrix")
        check(all(row["account_id"] in accounts for row in data["cells"]), source + " cells never contain a foreign student")
        raw = json.dumps(data)
        check(not any('"' + key + '"' in raw for key in ("password", "login", "tg_name", "password_hash", "answers", "question_text", "votes")), source + " DTO contains no credentials, answers or examination votes")

    try:
        anonymous = Session(check)
        for source in ("tests", "exams", "attendance"):
            report(anonymous, source, expected=401)
        check(True, "all source reports require authenticated HttpOnly BFF sessions")
        spec = anonymous.request("/openapi.json")
        for source in ("tests", "exams", "attendance"):
            operation = spec["paths"]["/bff/core/reports/" + source]["get"]
            check("content" in operation["responses"]["200"], source + " public contract declares the real nullable report DTO")

        supervisor = login(demo["supervisor_login"])
        tests = report(supervisor, "tests")
        validate(tests, "tests", demo["division_a"])
        reports["tests_a"] = tests
        check(len(tests["items"]) == 3 and "external_legacy_tests_not_connected" in tests["limitations"], "report uses all three real published internal tests and declares legacy external limitation")
        own_tests = [cell for cell in tests["cells"] if cell["account_id"] == demo["student_account_id"]]
        check(sorted(cell["score"] for cell in own_tests if cell["status"] == "ready") == [0, 100], "real official test zero and 100 are preserved independently")
        check(sum(cell["status"] == "missing" and cell["score"] is None for cell in own_tests) == 1, "missing official test remains null rather than a fabricated zero")
        check(all(cell["score"] is None and cell["status"] == "missing" for cell in tests["cells"] if cell["account_id"] == demo["student2_account_id"]), "unsubmitted second student's tests remain missing")

        exams = report(supervisor, "exams")
        validate(exams, "exams", demo["division_a"])
        reports["exams_a"] = exams
        items = {int(item["id"]): item for item in exams["items"]}
        check(set(items) == {demo["classic_exam_id"], demo["outside_exam_id"]}, "classic and outside exams are selected from the live current owner")
        check(all(item["name"] and item["name"] != "Экзамен " + item["id"] for item in items.values()), "actual owner exam names reach Core and UI DTO")
        own_exams = {int(cell["item_id"]): cell for cell in exams["cells"] if cell["account_id"] == demo["student_account_id"]}
        check(own_exams[demo["classic_exam_id"]]["score"] == 0 and own_exams[demo["classic_exam_id"]]["attempt_no"] == 2, "effective worse completed retake zero wins over the old appealed grade4")
        check(own_exams[demo["outside_exam_id"]]["score"] == 2, "outside owner grade2 is reported instead of raw decimal points123.45")
        check(all(cell["score"] is None for cell in exams["cells"] if cell["account_id"] == demo["student2_account_id"]), "second student's uncompleted exam results are not synthesized")

        attendance = report(supervisor, "attendance")
        validate(attendance, "attendance", demo["division_a"])
        reports["attendance_a"] = attendance
        check(attendance["unmarked_is_absent"] is False and "current_attendance_owner" in attendance["limitations"], "attendance report delegates new owner and keeps unmarked distinct from absence")
        check("source_revision" in attendance, "attendance report preserves current owner revision")
        direct_attendance = supervisor.request(f"/bff/attendance/divisions/{demo['division_a']}/attendance-report?date_from={demo['period']['date_from']}&date_to={demo['period']['date_to']}")
        check(direct_attendance["revision"] == attendance["source_revision"] and {str(row["id"]) for row in direct_attendance["value"]["days"]} == {row["id"] for row in attendance["items"]}, "Core attendance report matches current public owner revision and days, independently of old Core attendance tables")

        for source in ("tests", "exams", "attendance"):
            report(supervisor, source, "&organization_id=2", expected=400)
            report(supervisor, source, "&division_id=2", expected=400)
            report(supervisor, source, "&account_id=1", expected=400)
            report(supervisor, source, "&date_from=2026-10-02", expected=400)
        check(True, "source reports reject claimed scopes, account substitutions and duplicate period parameters")
        supervisor.request("/bff/core/reports/tests?date_from=2026-02-30&date_to=2026-10-15", expected=400)
        supervisor.request("/bff/core/reports/exams?date_from=2025-01-01&date_to=2026-10-15", expected=400)
        empty = supervisor.request("/bff/core/reports/tests?date_from=2026-01-01&date_to=2026-01-02")
        check(empty["items"] == [] and empty["cells"] == [] and len(empty["students"]) == 2, "empty independent period has no fabricated work or grade")

        # The fixture supervisor intentionally belongs only to A; switch the
        # organization administrator rather than granting a new participation.
        supervisor.request("/bff/auth/context", "POST", {"organization_id": demo["organization_id"], "division_id": demo["division_b"]}, expected=403)
        check(True, "supervisor cannot select a division without participation")
        switcher = login("dev_admin")
        old = switcher.revision
        switcher.request("/bff/auth/context", "POST", {"organization_id": demo["organization_id"], "division_id": demo["division_b"]})
        report(switcher, "exams", revision=old, expected=409)
        check(True, "previous context cannot read reports after cabinet switch")
        exams_b = report(switcher, "exams")
        validate(exams_b, "exams", demo["division_b"])
        reports["exams_b"] = exams_b
        check({int(item["id"]) for item in exams_b["items"]} == {demo["outside_exam_id"], demo["copy_exam_id"]}, "division B has its own outside exam and deep copy catalogue")
        cells_b = {int(cell["item_id"]): cell for cell in exams_b["cells"] if cell["account_id"] == demo["student_account_id"]}
        check(cells_b[demo["outside_exam_id"]]["score"] == 5 and cells_b[demo["copy_exam_id"]]["score"] is None, "outside result differs per division; copied exam without results stays missing")

        for login_key in ("student_login", "proctor_login", "scoped_staff_view_login"):
            reader = login(demo[login_key])
            for source in ("tests", "exams", "attendance"):
                report(reader, source, expected=403)
        check(True, "student, proctor and ratings-only staff cannot read full source reports")

        proctor = login(demo["proctor_login"])
        students = proctor.request("/bff/core/students?limit=200")
        check([row["account_id"] for row in students] == [demo["student_account_id"]], "proctor roster contains only own currently assigned group")
        dashboard = proctor.request("/bff/core/dashboard")
        check(dashboard["totals"] == {"students": 1, "staff": None, "groups": 1, "schools": None}, "proctor dashboard counts own group while unauthorized totals remain null")
        admin = login("dev_admin")
        dashboard = admin.request("/bff/core/dashboard")
        check(dashboard["totals"]["students"] == 2 and dashboard["division_id"] == demo["division_a"], "administrator dashboard counts selected division students")
        members = admin.request(f"/bff/core/groups/{demo['group_a']}/members?limit=1&offset=0")
        next_members = admin.request(f"/bff/core/groups/{demo['group_a']}/members?limit=1&offset=1")
        check(len(members) == 1 and len(next_members) == 1 and members[0]["account_id"] != next_members[0]["account_id"], "group members support actual distinct limit/offset pages")
        admin.request("/bff/internal/tests/ratings/snapshot", expected=501)
        admin.request("/bff/internal/exams/rating-snapshot", expected=501)
        check(True, "source service-key snapshots are never public BFF routes")
        status = "passed"
    except Exception as exc:
        failure = str(exc)
        raise
    finally:
        for session in sessions:
            try:
                session.close()
            except Exception:
                pass
        result = {"status": status, "count": len(checks), "checks": checks, "error": failure, "reports": reports}
        (ROOT / "dev/local/bff-core-reports-http-report.json").write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
        print(json.dumps({"status": status, "count": len(checks)}))


if __name__ == "__main__":
    main()
