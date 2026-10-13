"""Read-only checks of the connected Core/Cards browser boundary.

All business calls are GET. The only mutations create, switch and close this
script's auth sessions; passwords/cookies never enter reports or console output.
"""
from __future__ import annotations

import json
from pathlib import Path

from exams_http_smoke import Session

ROOT = Path(__file__).resolve().parents[3]


def main():
    demo = json.loads((ROOT / "dev/local/bff-ratings-demo.json").read_text())
    password = json.loads((ROOT / "dev/local/accounts.json").read_text())["password"]
    checks, sessions = [], []
    status, failure = "failed", None

    def check(value, name):
        assert value, name
        checks.append(name)

    def login(name, division=None):
        client = Session(check)
        sessions.append(client)
        client.login(name, password, demo["organization_id"], division or demo["division_a"])
        return client

    def read_all(client, path):
        rows = []
        for offset in range(0, 100001, 200):
            page = client.request(f"{path}{'&' if '?' in path else '?'}limit=200&offset={offset}")
            assert isinstance(page, list), "Invalid public list"
            rows.extend(page)
            if len(page) < 200:
                return rows
        raise AssertionError("Public list exceeded its paging guard")

    try:
        public = Session(check)
        public.request("/bff/cards/tree", expected=401)
        public.request("/bff/core/users", expected=401)
        check(True, "cards and core require a cookie session")
        contract = public.request("/openapi.json")["paths"]
        check(sum(len(v) for p, v in contract.items() if p.startswith("/bff/cards/")) == 19,
              "public Cards contract covers 19 operations with excluded import parse")
        check(all(path in contract for path in ("/bff/core/dashboard", "/bff/core/students",
                                                "/bff/core/reports/tests", "/bff/core/reports/exams", "/bff/core/reports/attendance")),
              "Core dashboard, students and separate reports are documented")
        check("/bff/cards/imports/parse" not in contract and "/bff/core/schedule" not in contract,
              "global imports and unrelated Core write modules stay outside this integration")

        admin = login("dev_admin")
        roster = read_all(admin, "/bff/core/users?role=student")
        identities = {row["account_id"] for row in roster}
        check({demo["student_account_id"], demo["student2_account_id"]} <= identities,
              "management roster uses the selected division's account identities")
        check(all("profile_id" in row and not {"password", "password_hash", "token"} & row.keys() for row in roster),
              "roster preserves profile IDs separately and contains no passwords or hashes")
        students = read_all(admin, "/bff/core/students")
        check({row["account_id"] for row in students} == identities and all(row["role"] == "student" for row in students),
              "dedicated students route has the same complete scoped roster")
        dashboard = admin.request("/bff/core/dashboard")
        check(dashboard["division_id"] == demo["division_a"] and dashboard["organization_id"] == demo["organization_id"],
              "dashboard identifies the selected live organization and division")
        check(dashboard["totals"]["students"] == len(roster), "dashboard student count agrees with full roster")
        check(isinstance(admin.request("/bff/core/roles")["roles"], list), "role directory keeps its real object envelope")
        check(isinstance(read_all(admin, "/bff/core/schools"), list), "organization school directory is readable")
        groups = read_all(admin, "/bff/core/groups")
        check(isinstance(groups, list), "selected division group directory is readable")
        if groups:
            members = read_all(admin, f"/bff/core/groups/{groups[0]['id']}/members")
            check(all(row["group_id"] == groups[0]["id"] for row in members), "group members support complete scoped pagination")
        check(isinstance(admin.request("/bff/cards/admin/catalog")["directions"], list), "Cards organization admin catalog is connected")
        check(isinstance(admin.request("/bff/cards/tree")["directions"], list), "Cards learner tree is connected")

        for path in ("/bff/core/users?division_id=2", "/bff/core/users?organization_id=2",
                     "/bff/core/users?role=student&role=admin", "/bff/core/dashboard?url=http://invalid",
                     "/bff/cards/tree?account_id=999", "/bff/cards/tree?organization_id=2",
                     "/bff/cards/tree?division_id=2", "/bff/cards/directions/1/sections?page=1&page=2"):
            admin.request(path, expected=400)
        check(True, "business scopes, arbitrary upstreams and duplicate query values are rejected")
        for path in ("/bff/core/organization/users", "/bff/core/users/1/credentials", "/bff/core/imports",
                     "/bff/cards/internal", "/bff/cards/imports/parse", "/bff/cards/sections/exam/1"):
            admin.request(path, expected=501)
        check(True, "private APIs, credential reads and excluded global imports are unavailable")

        period = demo["period"]
        query = f"date_from={period['date_from']}&date_to={period['date_to']}"
        reports = {}
        for source in ("tests", "exams", "attendance"):
            report = admin.request(f"/bff/core/reports/{source}?{query}")
            reports[source] = report
            check(report["source"] == source and report["division_id"] == demo["division_a"],
                  f"{source} report preserves actual source and current division")
            check({row["account_id"] for row in report["students"]} == identities,
                  f"{source} report uses the complete Core student roster")
            check(all(cell["account_id"] in identities for cell in report["cells"]),
                  f"{source} report has no foreign roster rows")
        check(any(cell.get("score") == 0 for cell in reports["tests"]["cells"]), "completed test zero stays numeric zero")
        own_exams = [cell for cell in reports["exams"]["cells"] if cell["account_id"] == demo["student_account_id"]]
        check(any(cell.get("score") == 0 for cell in own_exams) and any(cell.get("score") == 2 for cell in own_exams),
              "exam report combines effective zero retake and outside-system grade two")
        check(all(cell.get("score") is None for cell in reports["tests"]["cells"] if cell["status"] != "ready"),
              "missing or pending test results stay without a numeric result")

        student = login(demo["student_login"])
        own = read_all(student, "/bff/core/students")
        check([row["account_id"] for row in own] == [demo["student_account_id"]], "student roster access remains private")
        student.request(f"/bff/core/users/{demo['student2_account_id']}", expected=403)
        for source in ("tests", "exams", "attendance"):
            student.request(f"/bff/core/reports/{source}?{query}", expected=403)
        student.request("/bff/cards/admin/catalog", expected=403)
        check(True, "student cannot read administrative cards or supervisor report data")
        check(isinstance(student.request("/bff/cards/tree")["directions"], list), "student can read their authorized training tree")

        proctor = login(demo["proctor_login"])
        own_group = read_all(proctor, "/bff/core/students")
        check(demo["student_account_id"] in {row["account_id"] for row in own_group} and demo["student2_account_id"] not in {row["account_id"] for row in own_group},
              "proctor student list is restricted to the current own group")
        supervisor = login(demo["supervisor_login"])
        for source in ("tests", "exams", "attendance"):
            report = supervisor.request(f"/bff/core/reports/{source}?{query}")
            check(report["division_id"] == demo["division_a"], f"supervisor can read separate {source} report")

        previous = admin.revision
        admin.request("/bff/auth/context", "POST", {"organization_id": demo["organization_id"], "division_id": demo["division_b"]})
        admin.request("/bff/core/dashboard", expected=409, revision=previous)
        admin.request("/bff/cards/tree", expected=409, revision=previous)
        check(True, "both Core and Cards reject stale cabinet requests")
        other = admin.request("/bff/core/dashboard")
        check(other["division_id"] == demo["division_b"], "dashboard switches to another selected division")
        status = "passed"
    except Exception as error:
        failure = str(error)
        raise
    finally:
        for client in sessions:
            try:
                client.close()
            except Exception:
                pass
        report = {"status": status, "checks": checks, "check_count": len(checks), "failure": failure}
        (ROOT / "dev/local/bff-core-cards-http-smoke-report.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
        print(json.dumps({"status": status, "checks": len(checks)}, ensure_ascii=False))


if __name__ == "__main__":
    main()
