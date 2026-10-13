"""Repeatable read-only public BFF exam checks with fresh cookie sessions.

Uses only the final API demo. Business data is never written: the sole POSTs
create/select/delete this script's own BFF auth sessions. No direct owner, DB,
broker or remote requests are made. Credentials and cookies are never printed.
"""
from __future__ import annotations

import http.cookiejar
import json
from pathlib import Path
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[3]
BASE = "http://127.0.0.1:8001"
ORIGIN = "http://127.0.0.1:3001"


class NoRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise RuntimeError("Unexpected redirect from the fixed local BFF")


class Session:
    def __init__(self, check):
        self.jar = http.cookiejar.CookieJar()
        self.http = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar), NoRedirects())
        self.revision = ""
        self.check = check

    def request(self, path, method="GET", body=None, expected=200, revision=None):
        assert path.startswith("/bff/") or path == "/openapi.json"
        assert method == "GET" or path in {"/bff/auth/login", "/bff/auth/context", "/bff/auth/logout"}, "Business writes are forbidden"
        headers = {"X-CPM-Request": "1", "X-CPM-Context": self.revision if revision is None else revision}
        raw = None
        if body is not None:
            headers.update({"Content-Type": "application/json", "Origin": ORIGIN})
            raw = json.dumps(body).encode()
        request = urllib.request.Request(BASE + path, method=method, data=raw, headers=headers)
        try:
            with self.http.open(request, timeout=20) as response:
                status, payload = response.status, json.load(response)
        except urllib.error.HTTPError as response:
            status, payload = response.code, json.load(response)
        allowed = {expected} if isinstance(expected, int) else set(expected)
        assert status in allowed, f"{method} {path.split('?')[0]} expected {sorted(allowed)}, got {status} {payload.get('error', '')}"
        assert not any(key in payload for key in ("token", "access_token", "refresh_token")), "BFF exposed an auth token"
        if "context_revision" in payload:
            self.revision = payload["context_revision"]
        return payload.get("data", payload)

    def login(self, login, password, organization, division):
        identity = self.request("/bff/auth/login", "POST", {"login": login, "password": password, "organization_id": organization, "division_id": division})
        assert identity["user"]["division_id"] == division
        cookie = next(cookie for cookie in self.jar if cookie.name == "cpm_bff")
        assert cookie.has_nonstandard_attr("HttpOnly") and cookie.get_nonstandard_attr("SameSite") == "Strict"
        return identity

    def exams(self, suffix="", **kwargs):
        return self.request("/bff/exams" + suffix, **kwargs)

    def close(self):
        if self.revision:
            self.request("/bff/auth/logout", "POST", {})
            self.revision = ""


def main():
    demo = json.loads((ROOT / "dev/local/bff-exams-demo.json").read_text())
    password = json.loads((ROOT / "dev/local/accounts.json").read_text())["password"]
    checks, sessions = [], []
    status, failure = "failed", None

    def check(value, name):
        assert value, name
        checks.append(name)

    def login(name):
        session = Session(check)
        sessions.append(session)
        session.login(name, password, demo["organization_id"], demo["division_a"])
        return session

    classic, outside = demo["classic_exam_id"], demo["outside_exam_id"]
    try:
        public = Session(check)
        public.exams("/capabilities", expected=401)
        check(True, "exam capabilities require a cookie session")
        spec = public.request("/openapi.json")
        operations = sum(len(methods) for path, methods in spec["paths"].items() if path.startswith("/bff/exams"))
        check(operations == 62, "embedded public contract contains 61 owner operations plus capabilities")

        admin = login("dev_admin")
        caps = admin.exams("/capabilities")
        check(caps["canReadAdminExams"] and caps["canCreateClassic"] and caps["canManageOutside"] and caps["canImportExams"], "full administrator capabilities, imports and HttpOnly session")
        catalog = admin.exams("?scope=division&limit=200")["items"]
        check({classic, outside} <= {row["id"] for row in catalog} and demo["private_division_b_exam_id"] not in {row["id"] for row in catalog}, "selected division catalog contains both demo models and excludes the disconnected private exam")
        organization = admin.exams("?scope=organization&limit=200")["items"]
        check(demo["private_division_b_exam_id"] in {row["id"] for row in organization}, "organization administrator can read all organization exam definitions")
        bank = admin.exams("?scope=bank&limit=200")["items"]
        check(all(row["in_bank"] for row in bank), "organization bank returns published definitions only")
        detail = admin.exams(f"/{classic}")
        check(detail["connected"] and detail["exam_window"]["start_at"] and detail.get("config"), "connected classic detail has current configuration and a safe exam window")
        private = admin.exams(f"/{demo['private_division_b_exam_id']}")
        check(not private["connected"] and "config" not in private and "exam_window" not in private, "disconnected private definition is read-only with no operational config")
        admin.request("/bff/internal/exams/rating-snapshot", expected=501)
        check(True, "the internal rating snapshot is not a browser BFF route")
        previous = admin.revision
        admin.request("/bff/auth/context", "POST", {"organization_id": demo["organization_id"], "division_id": demo["division_b"]})
        admin.exams(f"/{classic}", expected=409, revision=previous)
        check(admin.revision != previous, "a selected cabinet change rejects the previous context revision")
        connected_b = admin.exams("?scope=division&limit=200")["items"]
        check(classic not in {row["id"] for row in connected_b}, "division B has an independent catalog")
        admin.request("/bff/auth/context", "POST", {"organization_id": demo["organization_id"], "division_id": demo["division_a"]})
        check(admin.exams(f"/{classic}")["connected"], "returning to division A restores its operational context")

        staff = login(demo["scoped_staff_login"])
        caps = staff.exams("/capabilities")
        check(caps["canReadAdminExams"] and caps["canCreateClassic"] and caps["canImportExams"], "division-scoped editor retains its granted exam and upload capabilities")
        staff.exams("?scope=organization", expected=403)
        check(True, "division-scoped editor cannot select the organization catalog")
        staff.exams(f"/{demo['private_division_b_exam_id']}", expected=404)
        check(True, "division-scoped editor cannot read another division's private exam")

        viewer = login(demo["view_staff_login"])
        caps = viewer.exams("/capabilities")
        check(caps["canReadAdminExams"] and not caps["canCreateClassic"] and not caps["canManageOutside"] and not caps["canImportExams"], "view-only staff receives no write or import capabilities")
        check(viewer.exams(f"/{classic}")["id"] == classic, "view-only staff can read its exam")
        directory = viewer.request("/bff/core/users?role=student&limit=1")
        check(directory == [], "exam view rights do not reveal the student directory")

        student = login(demo["student_login"])
        caps = student.exams("/capabilities")
        check(caps["canReadStudentResults"] and not caps["canReadAdminExams"], "student capabilities are separate from administration")
        result = student.exams(f"/{classic}/results/{demo['student_account_id']}")
        current, history = result["current"], result["history"]
        check(current["id"] == demo["retake_attempt_id"] and current["status"] == "completed" and current["effective_grade"] == 0, "the completed worse zero retake remains current")
        check(any(row["id"] == demo["first_attempt_id"] and row["effective_grade"] == 4 and row["has_appeal"] for row in history), "student history preserves the appealed first grade")
        outside_result = student.exams(f"/{outside}/results/{demo['student_account_id']}")["current"]
        check(float(outside_result["points"]) == 123.45 and outside_result["grade"] == 2, "outside decimal score is independent of grade")
        own_protocol = student.exams(f"/{classic}/attempts/{demo['retake_attempt_id']}/protocol?limit=200")["items"]
        check(bool(own_protocol) and all("rounds" not in row and "round" not in row for row in own_protocol), "student reads its frozen protocol without examiner votes")
        student.exams(f"/{classic}/results/{demo['student2_account_id']}", expected=(403, 404))
        check(True, "student cannot read another student's result")
        training = student.exams(f"/{classic}/training")
        check(training["stats"]["total"] == 7 and detail["name"], "the final classic demo still has seven live training cards")

        examiner = login(demo["examiners"][0]["login"])
        caps = examiner.exams("/capabilities")
        check(caps["canConductClassic"] and not caps["canReadAdminExams"], "assigned examiner has conduct capabilities only")
        own = examiner.exams(f"/{classic}/attempts/{demo['retake_attempt_id']}")
        check(own["exam_id"] == classic and own["id"] == demo["retake_attempt_id"] and own["result"]["effective_grade"] == 0, "exam-scoped completed attempt is available to its pinned examiner")
        examiner_detail = examiner.exams(f"/{classic}")
        check(examiner_detail["exam_window"] == detail["exam_window"] and "config" not in examiner_detail, "examiner receives dates without administration config")
        examiner_protocol = examiner.exams(f"/{classic}/attempts/{demo['retake_attempt_id']}/protocol?limit=200")["items"]
        check(len(examiner_protocol) == len(own_protocol), "examiner protocol and student protocol refer to the same selected attempt")
        status = "passed"
    except Exception as error:
        failure = str(error)
        raise
    finally:
        for session in sessions:
            session.close()
        report = {"status": status, "checks": len(checks), "check_names": checks, "fixture_run": demo["run"], "business_writes": 0, "fresh_cookie_sessions": len(sessions), **({"error": failure} if failure else {})}
        target = ROOT / "dev/local/bff-exams-http-smoke-report.json"
        target.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
        target.chmod(0o600)
        print(json.dumps({"status": status, "checks": len(checks), "business_writes": 0}))


if __name__ == "__main__":
    main()
