"""API-only loopback exam regression through cookie BFF.

Reuses the owner's public HTTP workflows, never its SQL fixture cleanup.
Fresh DEV divisions/accounts/exams are created by public APIs and retained for
browser checks. Tokens are used only by fixture Core calls and are not printed.
"""
from __future__ import annotations

from datetime import datetime, timedelta, timezone
import http.cookiejar
import importlib.util
import json
from pathlib import Path
import secrets
from types import SimpleNamespace
import traceback
import urllib.error
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[3]
spec = importlib.util.spec_from_file_location("exam_owner_public_regression", ROOT / "services/exams/scripts/http_test.py")
owner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(owner)


class BrowserSession:
    def __init__(self):
        self.http = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        self.revision = ""
        self.fixture_token = ""


class BFFRegression(owner.Regression):
    def __init__(self):
        # Deliberately skip the owner's DB configuration and cleanup lifecycle.
        self.args = SimpleNamespace(auth_url="http://127.0.0.1:8002", core_url="http://127.0.0.1:8003", exams_url="http://127.0.0.1:8010")
        self.password = json.loads((ROOT / "dev/local/accounts.json").read_text())["password"]
        self.internal_key = owner.read_env(ROOT / "dev/.env")["AUTH_INTERNAL_KEY"]
        self.run = secrets.token_hex(4)
        self.prefix = "dev_bff_exams_" + self.run
        self.checks, self.users, self.groups, self.roles, self.divisions, self.exams, self.tokens = [], [], [], [], [], [], []
        self.logins = {}
        self.demo = {}

    def request(self, origin, path, token=None, method="GET", body=None, code=200, headers=None):
        # Existing public owner scenarios are routed through BFF, including raw
        # missing-key and unauthenticated requests. Internal snapshot reads remain
        # explicitly service-to-service and can never be routed by the browser.
        if origin != self.args.exams_url or path.startswith("/internal/"):
            direct = token.fixture_token if isinstance(token, BrowserSession) else token
            return super().request(origin, path, direct, method, body, code, headers)
        session = token if isinstance(token, BrowserSession) else BrowserSession()
        target = "/bff/exams" + path.removeprefix("/v1/exams") if path.startswith("/v1/exams") else path
        return self.browser_request(session, target, method, body, code, headers)

    def browser_request(self, session, path, method="GET", body=None, code=200, headers=None):
        self.last_request = method + " " + path.split("?")[0]
        h = {"Origin": "http://127.0.0.1:3001", "X-CPM-Request": "1", "X-CPM-Context": session.revision}
        h.update(headers or {})
        if body is None:
            raw = None
        elif isinstance(body, bytes):
            raw = body
        else:
            raw = json.dumps(body).encode()
            h["Content-Type"] = "application/json"
        req = urllib.request.Request("http://127.0.0.1:8001" + path, data=raw, method=method, headers=h)
        try:
            with session.http.open(req, timeout=40) as response:
                status, result = response.status, json.load(response)
        except urllib.error.HTTPError as response:
            status, result = response.code, json.load(response)
        allowed = {code} if isinstance(code, int) else set(code)
        self.expect(status in allowed, f"{method} {path.split('?')[0]} expected {sorted(allowed)}, got {status} {result.get('error', '')}")
        self.expect(not any(k in result for k in ["access_token", "refresh_token", "token"]), "BFF never returns auth tokens")
        if "context_revision" in result:
            session.revision = result["context_revision"]
        return result

    def login(self, login, division=None, organization=1001):
        session = BrowserSession()
        body = {"login": login, "password": self.password, "organization_id": organization}
        if division:
            body["division_id"] = division
        identity = self.browser_request(session, "/bff/auth/login", "POST", body)
        direct = super().request(self.args.auth_url, "/v1/auth/login", method="POST", body=body)
        session.fixture_token = direct.get("access_token", direct.get("token"))
        self.tokens.append(session)
        self.logins[login] = identity["user"]["account_id"]
        return session, identity["user"]

    def api(self, path="", token=None, method="GET", body=None, code=200, key=None, headers=None):
        if path == "" and method == "POST" and isinstance(body, dict) and "name" not in body:
            body = {**body, "name": "DEV BFF Exams " + self.run + " · " + body.get("exam_type", "Проверка")}
        return super().api(path, token, method, body, code, key, headers)

    def fixtures(self):
        for origin in [self.args.auth_url, self.args.core_url, self.args.exams_url]:
            super().request(origin, "/health/ready")
        self.admin, self.admin_user = self.login("dev_admin", 1001)
        self.foreign_admin, _ = self.login("dev_admin_math", 2001, 2001)
        self.viewer, _ = self.login("dev_core_staff", 1001)
        for name in ["A", "B"]:
            division = self.core("/divisions", method="POST", body={"name": "DEV BFF Exams " + self.run + " " + name}, code=201)["id"]
            self.divisions.append(division)
            group = self.core(f"/divisions/{division}/groups", method="POST", body={"name": "DEV BFF Exams " + self.run + " " + name}, code=201)["id"]
            self.groups.append(group)
        self.a, self.b = self.divisions
        self.admin_a, _ = self.login("dev_admin", self.a)
        self.admin_b, _ = self.login("dev_admin", self.b)
        self.students = [self.create_user("student", True), self.create_user("student", False)]
        self.examiners = [self.create_user("examinator", True) for _ in range(3)]
        self.student_a, _ = self.login(self.students[0][1], self.a)
        self.student_b, _ = self.login(self.students[0][1], self.b)
        self.other_student_a, _ = self.login(self.students[1][1], self.a)
        self.examiner_tokens = [self.login(login, self.a)[0] for _, login in self.examiners]
        role = self.core("/roles", method="POST", body={"name": "DEV BFF Exams scoped " + self.run, "permissions": {"exams": {"view": True, "edit": True}, "upload": {"view": True, "edit": True}, "users": {"view": True}}}, code=201)["id"]
        self.roles.append(role)
        login = self.prefix + "_staff"
        account = self.core(f"/divisions/{self.a}/users", method="POST", body={"role": "staff_admin", "full_name": "DEV BFF Exams scoped " + self.run, "login": login, "password": self.password, "role_id": role}, code=201)["account_id"]
        self.users.append(account)
        self.scoped_staff, _ = self.login(login, self.a)
        self.demo.update(scoped_staff_login=login)
        view_role = self.core("/roles", method="POST", body={"name": "DEV BFF Exams view " + self.run, "permissions": {"exams": {"view": True, "edit": False}}}, code=201)["id"]
        view_login = self.prefix + "_viewer"
        account = self.core(f"/divisions/{self.a}/users", method="POST", body={"role": "staff_admin", "full_name": "DEV BFF Exams viewer " + self.run, "login": view_login, "password": self.password, "role_id": view_role}, code=201)["account_id"]
        self.users.append(account)
        self.view_staff, _ = self.login(view_login, self.a)
        self.demo.update(view_staff_login=view_login)

    def cleanup(self):
        raise RuntimeError("This BFF suite retains API fixtures; SQL cleanup is forbidden")

    def security(self):
        # BFF exposes the public contract and no owner internal routes.
        document = self.browser_request(BrowserSession(), "/openapi.json")
        self.expect(document["openapi"] == "3.1.0", "BFF embeds public OpenAPI")
        operations = [(path, method) for path, methods in document["paths"].items() if path.startswith("/bff/exams") and path != "/bff/exams/capabilities" for method in methods if method in {"get", "post", "put", "patch", "delete"}]
        self.expect(len(operations) == 61, "BFF covers all 61 public owner exam operations")
        for path, method in operations:
            concrete = path
            for name in ["exam", "part", "question", "commission", "assignment", "attempt", "student", "result", "presented", "division"]:
                concrete = concrete.replace("{" + name + "}", "1")
            concrete = concrete.replace("{kind}", "questions").replace("{session}", str(uuid.uuid4()))
            self.browser_request(BrowserSession(), concrete, method.upper(), {} if method != "get" else None, 401)
        self.api("", self.viewer, code=403)
        self.api("?limit=0", self.admin_a, code=400)
        self.api("?limit=1&limit=2", self.admin_a, code=400)
        self.api("?division_id=" + str(self.b), self.student_a, code=400)
        self.api("?organization_id=2001", self.student_a, code=400)
        self.api("", self.student_a, "POST", {"exam_type": "classic", "direction_id": 1001}, code=403)
        self.api("", self.admin_a, "POST", {"exam_type": "classic", "direction_id": 1001, "organization_id": 2001}, code=400)
        self.api("", self.admin_a, "POST", {"exam_type": "classic", "direction_id": 2001}, code=404)
        self.request(self.args.exams_url, "/v1/exams", self.admin_a, "POST", {"exam_type": "classic", "direction_id": 1001}, code=400)
        self.browser_request(self.admin_a, "/bff/internal/exams/rating-snapshot", code=501)

    def read_smoke(self, exam):
        # Meaningful context fence rather than a repeated read load test.
        original = self.student_a.revision
        self.browser_request(self.student_a, "/bff/auth/context", "POST", {"organization_id": 1001, "division_id": self.b})
        latest = self.student_a.revision
        self.student_a.revision = original
        self.api(f"/{exam}/results", self.student_a, code=409)
        self.student_a.revision = latest
        self.browser_request(self.student_a, "/bff/auth/context", "POST", {"organization_id": 1001, "division_id": self.a})
        self.api(f"/{exam}/parts", self.view_staff)
        self.api(f"/{exam}/parts", self.view_staff, "POST", {"code": "V", "quota": 1, "weight": 1}, code=403)
        self.api(f"/{exam}/results", self.view_staff)
        self.browser_request(self.scoped_staff, "/bff/auth/context", "POST", {"organization_id": 1001, "division_id": self.b}, code=403)

    def auxiliary_contracts(self):
        path = f"/{self.exams[0]}"
        preview = self.api(path + "/delete-preview", self.admin_a)
        self.expect(preview["counts"]["divisions"] == 2 and preview["counts"]["commissions"] == 3 and preview["counts"]["questions"] == 8, "shared exam deletion preview counts all divisions and their commissions")
        part = self.api(path + "/parts", self.admin_a, "POST", {"code": "B", "weight": 2, "quota": 1})["part"]
        question = self.api(path + "/questions", self.admin_a, "POST", {"part_id": part["id"], "question_text": "DEV temp question " + self.run, "answer_text": "DEV initial"})["question"]
        changed = self.api(path + f"/questions/{question['id']}", self.admin_a, "PUT", {"expected_revision": question["revision"], "answer_text": "DEV revised"})["question"]
        self.api(path + f"/questions/{question['id']}", self.admin_a, "DELETE", {"expected_revision": question["revision"]}, code=409)
        self.api(path + f"/parts/{part['id']}", self.admin_a, "PUT", {"expected_revision": part["revision"], "code": "C"})
        self.expect(self.api(path + f"/questions?part_id={part['id']}", self.admin_a)["items"][0]["answer_text"] == "DEV revised", "part rename preserves question identity and revised answer")
        self.api(path + f"/questions/{question['id']}", self.admin_a, "DELETE", {"expected_revision": changed["revision"]})
        current = next(p for p in self.api(path + "/parts", self.admin_a)["items"] if p["id"] == part["id"])
        self.api(path + f"/parts/{part['id']}", self.admin_a, "DELETE", {"expected_revision": current["revision"]})
        commission = self.api(path + "/commissions", self.admin_a, "POST", {"name": "DEV disposable " + self.run, "examinator_ids": [self.examiners[0][0]]})["commission"]
        commission = self.api(path + f"/commissions/{commission['id']}", self.admin_a, "PUT", {"expected_revision": commission["revision"], "name": "DEV renamed " + self.run})["commission"]
        assignment = self.api(path + "/assignments", self.admin_a, "POST", {"student_account_id": self.students[1][0], "commission_id": commission["id"]})["assignment"]
        self.api(path + f"/commissions/{commission['id']}", self.admin_a, "DELETE", {"expected_revision": commission["revision"]})
        after = next(a for a in self.api(path + "/assignments", self.admin_a)["items"] if a["id"] == assignment["id"])
        self.expect(after["commission_id"] == 0 and after["revision"] == assignment["revision"] + 1 and after["members"] == assignment["members"], "commission deletion invalidates assignment CAS while preserving pinned members")
        replacement = self.api(path + "/commissions", self.admin_a)["items"][0]
        self.api(path + f"/assignments/{assignment['id']}", self.admin_a, "PUT", {"expected_revision": assignment["revision"], "commission_id": replacement["id"]}, code=409)
        after = self.api(path + f"/assignments/{assignment['id']}", self.admin_a, "PUT", {"expected_revision": after["revision"], "commission_id": replacement["id"]})["assignment"]
        self.api(path + f"/assignments/{assignment['id']}", self.admin_a, "DELETE", {"expected_revision": after["revision"]})
        privilege = self.api(path + f"/privileges/{self.students[1][0]}", self.admin_a, "PUT", {"expected_revision": 0, "replacement_limit": 2})["privilege"]
        self.api(path + f"/privileges/{self.students[1][0]}", self.admin_a, "DELETE", {"expected_revision": privilege["revision"]})
        self.expect(not any(p["student_account_id"] == self.students[1][0] for p in self.api(path + "/privileges", self.admin_a)["items"]), "privilege deletion removes only the chosen student's allowance")
        outside = next(exam for exam in self.exams if self.api(f"/{exam}", self.admin_a, code={200, 404}).get("data", {}).get("exam_type") == "outside_lms")
        outside_path = f"/{outside}"
        existing = self.api(outside_path + "/results", self.admin_a)["items"][0]
        self.api(outside_path + f"/outside-results/{existing['id']}", self.admin_a, "DELETE", {"expected_revision": existing["revision"] - 1}, code=409)
        self.api(outside_path + f"/outside-results/{existing['id']}", self.admin_a, "DELETE", {"expected_revision": existing["revision"]})
        zero = self.api(outside_path + "/outside-results", self.admin_a, "POST", {"student_account_id": existing["student"]["account_id"], "points": 0, "grade": 0, "examinator": "DEV zero"})["result"]
        row = self.api(outside_path + f"/results/{existing['student']['account_id']}", self.admin_a)
        self.expect(row["points"] == 0 and row["grade"] == 0 and zero["id"] != existing["id"], "published external zero survives delete/create and differs from missing result")

    def browser_fixtures(self):
        private = self.api("", self.admin_b, "POST", {"exam_type": "classic", "direction_id": 1001, "name": "DEV BFF Exams " + self.run + " · Закрытый экзамен другого подразделения"}, code=201)
        self.exams.append(private["id"])
        private_path = f"/{private['id']}"
        private_part = self.api(private_path + "/parts", self.admin_b, "POST", {"code": "A", "weight": 1, "quota": 1})["part"]
        self.api(private_path + "/questions", self.admin_b, "POST", {"part_id": private_part["id"], "question_text": "DEV private question", "answer_text": "DEV private answer"})
        disconnected = self.api(private_path, self.admin_a)
        self.expect(disconnected["connected"] is False and "config" not in disconnected and self.api(private_path + "/questions", self.admin_a)["total"] == 1, "organization admin can inspect private disconnected catalogue content without operational fields")
        self.api(private_path + "/config", self.admin_a, code=404)
        self.api(private_path, self.scoped_staff, code=404)
        self.api(private_path, self.student_a, code=404)
        self.api(private_path, self.foreign_admin, code=404)
        self.expect(any(exam["id"] == private["id"] for exam in self.api("?scope=organization", self.admin_a)["items"]), "organization catalogue includes the readable disconnected private exam")
        self.demo.update(private_division_b_exam_id=private["id"])
        exam, part, questions, commission, assignment = self.classic()
        path = f"/{exam}"
        detail = self.api(path, self.admin_a)
        self.api(path + "/metadata", self.admin_a, "PUT", {"expected_revision": detail["revision"], "name": "DEV BFF Exams " + self.run + " · Устный экзамен UI"})
        self.api(path + "/training", self.admin_a, "PUT", {"expected_revision": self.api(path, self.admin_a)["division_revision"], "training_enabled": True})
        for actor in [self.student_a, self.examiner_tokens[0]]:
            safe = self.api(path, actor)
            self.expect(safe["exam_window"]["start_at"] is not None and safe["exam_window"]["end_at"] is not None and "config" not in safe and "parts" not in safe, "student and assigned examiner receive safe exam dates without bank/config")
        attempt = self.api(path + f"/assignments/{assignment['id']}/attempts/ensure", self.examiner_tokens[0], "POST", {"expected_history_generation": 1})["attempt"]
        apath = path + f"/attempts/{attempt['id']}"
        for index in [0, 1]:
            self.api(apath + "/ready", self.examiner_tokens[index], "POST", {})
        state = self.api(apath, self.examiner_tokens[0])
        self.api(apath + "/start", self.examiner_tokens[0], "POST", {"expected_state_version": state["state_version"]})
        for index in range(5):
            if index:
                state = self.api(apath, self.examiner_tokens[0])
                self.api(apath + "/next-question", self.examiner_tokens[0], "POST", {"presented_question_id": state["current_question"]["id"], "expected_state_version": state["state_version"]})
            self.consensus(apath, 1)
        first_result = self.api(path + f"/results/{self.students[0][0]}", self.admin_a)
        self.api(apath + "/appeals", self.admin_a, "POST", {"expected_result_version": first_result["result_version"], "grade": 4})
        changed_commission = self.api(path + "/commissions", self.admin_a, "POST", {"name": "DEV UI пересдача " + self.run, "examinator_ids": [self.examiners[0][0], self.examiners[2][0]]})["commission"]
        retake = self.api(path + f"/retakes/{self.students[0][0]}", self.admin_a, "POST", {"commission_id": changed_commission["id"], "expected_history_generation": 1})["assignment"]
        second = self.api(path + f"/assignments/{retake['id']}/attempts/ensure", self.examiner_tokens[0], "POST", {"expected_history_generation": 1})["attempt"]
        second_path = path + f"/attempts/{second['id']}"
        for index in [0, 2]:
            self.api(second_path + "/ready", self.examiner_tokens[index], "POST", {})
        second_state = self.api(second_path, self.examiner_tokens[0])
        self.api(second_path + "/start", self.examiner_tokens[0], "POST", {"expected_state_version": second_state["state_version"]})
        for index in range(5):
            if index:
                second_state = self.api(second_path, self.examiner_tokens[0])
                self.api(second_path + "/next-question", self.examiner_tokens[0], "POST", {"presented_question_id": second_state["current_question"]["id"], "expected_state_version": second_state["state_version"]})
            self.consensus(second_path, 0, [0, 2])
        current = self.api(path + f"/results/{self.students[0][0]}", self.student_a)
        self.expect(current["effective_grade"] == 0 and len(current["history"]) == 1 and current["history"][0]["effective_grade"] == 4, "browser fixture has effective zero retake and preserved first appealed grade four")
        pending = self.api(path + "/assignments", self.admin_a, "POST", {"student_account_id": self.students[1][0], "commission_id": commission["id"]})["assignment"]
        outside = self.api("", self.admin_a, "POST", {"exam_type": "outside_lms", "direction_id": 1001, "date": datetime.now(timezone.utc).date().isoformat(), "name": "DEV BFF Exams " + self.run + " · Внешний экзамен UI"}, code=201)
        self.exams.append(outside["id"])
        result = self.api(f"/{outside['id']}/outside-results", self.admin_a, "POST", {"student_account_id": self.students[0][0], "points": 123.45, "grade": 2, "examinator": "DEV Внешний преподаватель"})["result"]
        self.demo.update(classic_exam_id=exam, outside_exam_id=outside["id"], first_completed_attempt_id=attempt["id"], completed_attempt_id=second["id"], completed_assignment_id=retake["id"], first_assignment_id=assignment["id"], pending_assignment_id=pending["id"], commission_id=commission["id"], part_id=part["id"], question_ids=[question["id"] for question in questions], outside_result_id=result["id"], classic_current_grade=0, classic_first_appealed_grade=4, first_attempt_id=attempt["id"], retake_attempt_id=second["id"])

    def save_demo(self):
        data = {"run": self.run, "organization_id": 1001, "division_id": self.a, "second_division_id": self.b, "division_a": self.a, "division_b": self.b, "direction_id": 1001, "student_login": self.students[0][1], "student_account_id": self.students[0][0], "student2_login": self.students[1][1], "student2_account_id": self.students[1][0], "students": [{"account_id": account, "login": login} for account, login in self.students], "examiners": [{"account_id": account, "login": login} for account, login in self.examiners], "exam_ids": self.exams, **self.demo}
        target = ROOT / "dev/local/bff-exams-demo.json"
        target.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n")
        target.chmod(0o600)
        return data


def main():
    regression = BFFRegression()
    error = None
    location = None
    try:
        regression.fixtures()
        regression.workflow()
        regression.auxiliary_contracts()
        regression.browser_fixtures()
    except Exception as failure:
        error = str(failure) if isinstance(failure, AssertionError) else type(failure).__name__
        frames = traceback.extract_tb(failure.__traceback__)
        if frames:
            location = {"function": frames[-1].name, "line": frames[-1].lineno}
    demo = regression.save_demo() if regression.divisions and hasattr(regression, "students") else {}
    report = {"status": "passed" if error is None else "failed", "checks": len(regression.checks), "check_names": regression.checks, "run": regression.run, "last_request": getattr(regression, "last_request", ""), "fixtures_retained": True, "fixture_writes": "public API only", "demo": demo}
    if error:
        report["error"] = error
        report["failure_location"] = location
    target = ROOT / "dev/local/bff-exams-http-report.json"
    target.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    target.chmod(0o600)
    print(json.dumps({key: report[key] for key in ["status", "checks", "run", "last_request"]} | ({"error": error} if error else {})))
    return 1 if error else 0


if __name__ == "__main__":
    raise SystemExit(main())
