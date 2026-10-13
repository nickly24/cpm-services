"""HTTP integration regression using synthetic local cpm_dev fixtures only.

Run with dev/.venv/bin/python. No production URL override is accepted. Credentials
are read from ignored dev/local files and are never printed in failure reports.
"""
from __future__ import annotations

import argparse
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timedelta, timezone
import io
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import time
import traceback
import urllib.error
import urllib.request
from urllib.parse import urlsplit
import uuid
from xml.sax.saxutils import escape
import zipfile

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / "dev/scripts"))
from bootstrap import mysql_connection, settings
from export_structure import read_env


def local_url(value: str) -> str:
    parsed = urlsplit(value)
    if parsed.scheme != "http" or parsed.hostname not in {"127.0.0.1", "localhost"} or parsed.username or parsed.password or parsed.path not in {"", "/"} or parsed.query or parsed.fragment:
        raise argparse.ArgumentTypeError("only loopback HTTP service origins are allowed")
    return value.rstrip("/")


class Regression:
    def __init__(self, args):
        self.args = args
        self.dev = settings()  # fixed cpm_dev and dedicated local DB ports guard
        self.password = json.loads((ROOT / "dev/local/accounts.json").read_text())["password"]
        self.internal_key = read_env(ROOT / "dev/.env")["AUTH_INTERNAL_KEY"]
        self.run = secrets.token_hex(6)
        self.prefix = "dev_exams_http_" + self.run
        self.checks = []
        self.users = []
        self.groups = []
        self.roles = []
        self.divisions = []
        self.exams = []
        self.tokens = []

    def expect(self, condition, name):
        if not condition:
            raise AssertionError(name)
        self.checks.append(name)

    def request(self, origin, path, token=None, method="GET", body=None, code=200, headers=None):
        if path != "/v1/auth/logout":
            self.last_request = method + " " + path.split("?")[0]
        h = dict(headers or {})
        if token:
            h["Authorization"] = "Bearer " + token
        if body is None:
            data = None
        elif isinstance(body, bytes):
            data = body
        else:
            data = json.dumps(body).encode()
            h["Content-Type"] = "application/json"
        req = urllib.request.Request(origin + path, data=data, method=method, headers=h)
        try:
            with urllib.request.urlopen(req, timeout=90) as result:
                status, raw = result.status, result.read()
        except urllib.error.HTTPError as error:
            status, raw = error.code, error.read()
        except urllib.error.URLError:
            raise AssertionError("local HTTP service unavailable: " + origin) from None
        allowed = {code} if isinstance(code, int) else set(code)
        try:
            result = json.loads(raw)
        except (ValueError, UnicodeDecodeError):
            result = {}
        # Error bodies are not echoed: a dependency must never leak credentials
        # or authenticated fixture profile contents into a regression report.
        self.expect(status in allowed, f"{method} {path.split('?')[0]} expected {sorted(allowed)}, got {status}")
        return result

    def core(self, path, token=None, method="GET", body=None, code=200):
        result = self.request(self.args.core_url, "/v1/core" + path, token or self.admin, method, body, code)
        return result.get("data") if isinstance(code, int) and code < 300 else result

    def api(self, path="", token=None, method="GET", body=None, code=200, key=None, headers=None):
        h = dict(headers or {})
        if method not in {"GET", "HEAD"}:
            h["Idempotency-Key"] = key or str(uuid.uuid4())
        result = self.request(self.args.exams_url, "/v1/exams" + path, token or self.admin, method, body, code, h)
        if isinstance(code, int) and code < 300:
            self.expect(result.get("status") is True, "successful exam response has status=true")
            return result.get("data")
        return result

    def login(self, login, division=None, organization=1001):
        body = {"login": login, "password": self.password, "organization_id": organization}
        if division:
            body["division_id"] = division
        result = self.request(self.args.auth_url, "/v1/auth/login", method="POST", body=body)
        self.tokens.append(result["token"])
        return result["token"], result["user"]

    def fixtures(self):
        for origin in [self.args.auth_url, self.args.core_url, self.args.exams_url]:
            self.request(origin, "/health/ready")
        self.admin, self.admin_user = self.login("dev_admin", 1001)
        self.foreign_admin, self.foreign_admin_user = self.login("dev_admin_math", 2001, 2001)
        self.viewer, _ = self.login("dev_core_staff", 1001)
        for name in ["A", "B"]:
            division = self.core("/divisions", method="POST", body={"name": "DEV Exams " + self.run + " " + name}, code=201)["id"]
            self.divisions.append(division)
            group = self.core(f"/divisions/{division}/groups", method="POST", body={"name": "DEV Exams " + self.run + " " + name}, code=201)["id"]
            self.groups.append(group)
        self.a, self.b = self.divisions
        self.admin_a, _ = self.login("dev_admin", self.a)
        self.admin_b, _ = self.login("dev_admin", self.b)
        self.students = [self.create_user("student", shared=True), self.create_user("student", shared=False)]
        self.examiners = [self.create_user("examinator", shared=True) for _ in range(3)]
        self.student_a, _ = self.login(self.students[0][1], self.a)
        self.student_b, _ = self.login(self.students[0][1], self.b)
        self.other_student_a, _ = self.login(self.students[1][1], self.a)
        self.examiner_tokens = [self.login(login, self.a)[0] for _, login in self.examiners]
        role = self.core("/roles", method="POST", body={"name": "DEV Exams scoped " + self.run, "permissions": {"exams": {"view": True, "edit": True}, "upload": {"view": True, "edit": True}}}, code=201)["id"]
        self.roles.append(role)
        login = self.prefix + "_staff"
        staff = self.core(f"/divisions/{self.a}/users", method="POST", body={"role": "staff_admin", "full_name": "DEV Exams scoped " + self.run, "login": login, "password": self.password, "role_id": role}, code=201)["account_id"]
        self.users.append(staff)
        self.scoped_staff, _ = self.login(login, self.a)
        private = ROOT / "dev/local/exams-fixtures.json"
        private.write_text(json.dumps({"synthetic_run": self.run, "division_ids": self.divisions, "group_ids": self.groups, "account_ids": self.users, "exam_ids": self.exams, "cleaned": False}, indent=2) + "\n")
        private.chmod(0o600)

    def create_user(self, role, shared):
        login = self.prefix + "_" + str(len(self.users))
        body = {"role": role, "full_name": "DEV Exams " + role + " " + self.run, "login": login, "password": self.password, "tg_name": "dev_exams"}
        if role == "student":
            body.update({"class": 10, "school_id": 1001, "group_id": self.groups[0]})
        account = self.core(f"/divisions/{self.a}/users", method="POST", body=body, code=201)["account_id"]
        self.users.append(account)
        if shared:
            self.core(f"/divisions/{self.b}/memberships/{account}", method="PUT", body={})
            if role == "student":
                self.core(f"/divisions/{self.b}/groups/{self.groups[1]}/members", method="POST", body={"account_id": account})
        return account, login

    def cleanup(self):
        # Logout first: sessions retain division FKs even after user fixtures are
        # removed. Cleanup SQL is restricted to exact IDs plus this run's prefix.
        for token in self.tokens:
            try:
                self.request(self.args.auth_url, "/v1/auth/logout", token, "POST", code={200, 401})
            except Exception:
                pass
        conn = mysql_connection(self.dev)
        cursor = conn.cursor()
        try:
            for exam in self.exams:
                cursor.execute("SELECT owner_division_id FROM exams_catalog_v1 WHERE id=%s AND organization_id=1001", (exam,))
                row = cursor.fetchone()
                if row and row[0] not in self.divisions:
                    raise AssertionError("exam cleanup fixture scope guard")
                cursor.execute("DELETE FROM exams_catalog_v1 WHERE id=%s AND organization_id=1001", (exam,))
            for user in self.users:
                cursor.execute("SELECT role,ref_id FROM auth_users WHERE id=%s AND username LIKE %s", (user, self.prefix + "%"))
                row = cursor.fetchone()
                if not row:
                    raise AssertionError("account cleanup fixture prefix guard")
                cursor.execute("DELETE t FROM auth_refresh_tokens t JOIN auth_sessions s ON s.id=t.session_id WHERE s.user_id=%s", (user,))
                cursor.execute("DELETE FROM auth_sessions WHERE user_id=%s", (user,))
                for table in ["core_profile_extra", "core_rating_locks", "core_proctor_groups", "auth_division_access", "auth_organization_access", "core_user_owners", "auth_account_state"]:
                    cursor.execute("DELETE FROM " + table + " WHERE user_id=%s", (user,))
                cursor.execute("DELETE FROM auth_users WHERE id=%s", (user,))
                cursor.execute("DELETE FROM " + {"student": "students", "examinator": "examinators", "staff_admin": "admin_role_users"}[row[0]] + " WHERE id=%s", (row[1],))
            for role in self.roles:
                cursor.execute("DELETE FROM auth_scoped_role_permissions WHERE role_id=%s", (role,))
                cursor.execute("DELETE FROM auth_scoped_roles WHERE id=%s AND organization_id=1001 AND name=%s", (role, "DEV Exams scoped " + self.run))
            for group in self.groups:
                cursor.execute("DELETE FROM core_group_scope WHERE group_id=%s", (group,))
                cursor.execute("DELETE FROM `groups` WHERE id=%s AND name LIKE %s", (group, "DEV Exams " + self.run + "%"))
            for division in self.divisions:
                cursor.execute("SELECT id FROM divisions WHERE id=%s AND organization_id=1001 AND name LIKE %s", (division, "DEV Exams " + self.run + "%"))
                if cursor.fetchone() is None:
                    raise AssertionError("division cleanup fixture scope guard")
                cursor.execute("DELETE t FROM auth_refresh_tokens t JOIN auth_sessions s ON s.id=t.session_id WHERE s.division_id=%s", (division,))
                cursor.execute("DELETE FROM auth_sessions WHERE division_id=%s", (division,))
                for table in ["exams_receipts_v1", "exams_source_v1", "exams_outbox_v1"]:
                    cursor.execute("DELETE FROM " + table + " WHERE organization_id=1001 AND division_id=%s", (division,))
                cursor.execute("DELETE FROM divisions WHERE id=%s AND organization_id=1001 AND name LIKE %s", (division, "DEV Exams " + self.run + "%"))
            conn.commit()
        finally:
            cursor.close()
            conn.close()

    def workflow(self):
        self.security()
        exam, part, questions, commission, assignment = self.classic()
        path = f"/{exam}"
        # A deep copy keeps independent bank content and empty operational data.
        original = self.api(path, self.admin_a)
        copied = self.api(path + "/copy", self.admin_a, "POST", {"division_id": self.b, "expected_revision": original["revision"]})
        copy_id = copied["id"]
        self.exams.append(copy_id)
        copy_path = f"/{copy_id}"
        self.expect(copy_id != exam and copied["origin_exam_id"] == exam, "deep copy has independent exam identity and origin")
        self.expect(self.api(copy_path + "/questions", self.admin_b)["total"] == len(questions), "deep copy includes complete question bank")
        for collection in ["commissions", "assignments", "attempts", "results"]:
            self.expect(self.api(copy_path + "/" + collection, self.admin_b)["total"] == 0, "copy starts without " + collection)
        self.api(path + "/connections", self.admin_a, "POST", {"division_id": self.b, "expected_revision": self.api(path, self.admin_a)["revision"]})
        self.expect(self.api(path, self.admin_b)["id"] == exam, "shared connection preserves same exam ID")
        self.expect(self.api(path + "/assignments", self.admin_b)["total"] == 0, "shared content does not copy division assignments")
        self.api(path + "/config", self.admin_b, "PUT", {"expected_revision": self.api(path + "/config", self.admin_b)["config"]["revision"], "fractional_mode": "round_down"})
        self.expect(self.api(path + "/config", self.admin_a)["config"]["fractional_mode"] == "extra_question", "shared exam retains separate division conduct rules")
        self.api(path + "/commissions", self.admin_b, "POST", {"name": "DEV B", "examinator_ids": [self.examiners[0][0], self.examiners[1][0]]})
        bcommission = self.api(path + "/commissions", self.admin_b)["items"][0]
        self.api(path + "/assignments", self.admin_b, "POST", {"student_account_id": self.students[1][0], "commission_id": bcommission["id"]}, code=400)
        self.api(path, self.foreign_admin, code=404)
        self.api(path + "/questions", self.student_a, code=403)

        student = self.students[0][0]
        ensure = path + f"/assignments/{assignment['id']}/attempts/ensure"
        ensure_body = {"expected_history_generation": 1}
        self.expect(self.api(path + f"/readiness?assignment_id={assignment['id']}", self.examiner_tokens[0])["can_prepare"], "assigned examiner can read readiness of its assignment")
        self.api(ensure, self.admin_a, "POST", ensure_body, code=403)
        self.api(ensure, self.examiner_tokens[2], "POST", ensure_body, code={403, 404})
        ensure_key = str(uuid.uuid4())
        prepared = self.api(ensure, self.examiner_tokens[0], "POST", ensure_body, key=ensure_key)
        replay = self.api(ensure, self.examiner_tokens[0], "POST", ensure_body, key=ensure_key)
        self.expect(prepared == replay, "ensure command replay returns durable original receipt")
        attempt = prepared["attempt"]
        aid = attempt["id"]
        apath = path + f"/attempts/{aid}"
        self.api(apath, self.student_a, code=404)
        self.api(apath, self.admin_b, code=404)
        self.api(apath + "/start", self.examiner_tokens[0], "POST", {"expected_state_version": attempt["state_version"]}, code=422)
        with ThreadPoolExecutor(max_workers=2) as pool:
            ready = list(pool.map(lambda token: self.api(apath + "/ready", token, "POST", {}), self.examiner_tokens[:2]))
        attempt = self.api(apath, self.examiner_tokens[0])
        self.expect(attempt["all_members_ready"] and all(m["ready"] for m in attempt["members"]), "concurrent readiness preserves both members")
        start_body = {"expected_state_version": attempt["state_version"]}
        start_key = str(uuid.uuid4())
        with ThreadPoolExecutor(max_workers=2) as pool:
            starts = list(pool.map(lambda _: self.api(apath + "/start", self.examiner_tokens[0], "POST", start_body, key=start_key), range(2)))
        self.expect(starts[0] == starts[1], "concurrent duplicate start applies once and replays identical receipt")
        attempt = starts[0]["attempt"]
        self.expect(attempt["status"] == "in_progress", "ready commission starts exam")
        # Replace before voting, then exclude that question for the whole attempt.
        first = attempt["current_question"]
        replaced = self.api(apath + "/replace-question", self.examiner_tokens[0], "POST", {"presented_question_id": first["id"], "expected_state_version": attempt["state_version"]})["attempt"]
        self.expect(replaced["current_question"]["question_id"] != first["question_id"] and replaced["progress"]["replacement_used"] == 1, "replacement changes question and consumes personal limit")
        attempt = replaced
        active = attempt["current_question"]
        bank_question = next(q for q in questions if q["id"] == active["question_id"])
        self.api(path + f"/questions/{bank_question['id']}", self.admin_a, "PUT", {"expected_revision": bank_question["revision"], "question_text": "DEV edited after snapshot " + self.run})
        copied_question = next(q for q in self.api(copy_path + "/questions", self.admin_b)["items"] if q["id"] == bank_question["id"])
        self.expect(copied_question["question_text"] == bank_question["question_text"], "deep copy bank independent of later original edits")
        self.expect(self.api(apath, self.examiner_tokens[0])["current_question"]["question_text"] == active["question_text"], "started attempt keeps original immutable content snapshot")

        # Opposed votes open a new round; another examiner sees no open votes.
        self.vote(apath, 0, 0)
        other_view = self.api(apath, self.examiner_tokens[1])["current_question"]["round"]
        self.expect("votes" not in other_view and "own_vote" not in other_view, "open votes hidden from other examiner")
        self.api(apath + "/replace-question", self.examiner_tokens[1], "POST", {"presented_question_id": active["id"], "expected_state_version": self.api(apath, self.examiner_tokens[1])["state_version"]}, code=409)
        self.vote(apath, 1, 1)
        disputed = self.api(apath, self.examiner_tokens[0])
        self.expect(disputed["current_question"]["round"]["round_no"] == 2, "disagreement creates next voting round")
        self.consensus(apath, 0.5)
        # A second duplicate vote cannot change a recorded vote or final result.
        values = [1, 1, 0, 0]
        for value in values:
            attempt = self.api(apath, self.examiner_tokens[0])
            current = attempt["current_question"]
            self.api(apath + "/next-question", self.examiner_tokens[0], "POST", {"presented_question_id": current["id"], "expected_state_version": attempt["state_version"]})
            self.consensus(apath, value)
        attempt = self.api(apath, self.examiner_tokens[0])
        self.expect(attempt["phase"] == "tie_breaker", "fractional ordinary sum opens additional question")
        self.consensus(apath, 0.5)
        self.expect(self.api(apath, self.examiner_tokens[0])["phase"] == "tie_breaker", "configured half vote repeats additional question")
        self.consensus(apath, 1)
        result = self.api(path + f"/results/{student}", self.admin_a)
        self.expect(result["raw_total"] == 2.5 and result["rounded_total"] == 3 and result["calculated_grade"] == 3, "exact ordinary 2.5 plus additional positive answer rounds to grade 3")
        published = self.api(path + f"/results/{student}", self.student_a)
        self.expect(published["effective_grade"] == 3 and "calculated_grade" not in published and "result_version" not in published, "student sees safe published result without internal calculation or CAS metadata")
        self.expect(result["division_id"] == self.a and result["attempt_no"] == 1, "published result stays in original division")
        self.api(path + f"/results/{student}", self.student_b, code=404)
        self.api(path + f"/results/{student}", self.other_student_a, code=404)
        protocol = self.api(apath + "/protocol", self.student_a)["items"]
        self.expect(any(q["status"] == "replaced" for q in protocol) and sum(q["purpose"] == "tie_breaker" for q in protocol) == 2, "student protocol retains replaced and additional questions")
        self.expect(all("round" not in q and "rounds" not in q and "votes" not in q for q in protocol), "student protocol never exposes individual votes")
        self.expect(not any(q["question_id"] == first["question_id"] and q["status"] != "replaced" for q in protocol), "replaced question never reappears in same attempt")
        self.rating_scope(exam, student, 3)

        appeal = {"expected_result_version": result["result_version"], "grade": 4}
        self.api(apath + "/appeals", self.student_a, "POST", appeal, code=403)
        self.api(apath + "/appeals", self.admin_a, "POST", appeal)
        self.api(apath + "/appeals", self.admin_a, "POST", appeal, code=409)
        appealed = self.api(path + f"/results/{student}", self.admin_a)
        self.expect(appealed["effective_grade"] == 4 and appealed["calculated_grade"] == 3 and appealed["raw_total"] == 2.5, "appeal changes effective grade while preserving calculated result")
        self.api(path + f"/retakes/{student}", self.admin_a, "POST", {"commission_id": commission["id"], "expected_history_generation": 1}, code=422)
        retake_commission = self.api(path + "/commissions", self.admin_a, "POST", {"name": "DEV retake", "examinator_ids": [self.examiners[0][0], self.examiners[2][0]]})["commission"]
        retake = self.api(path + f"/retakes/{student}", self.admin_a, "POST", {"commission_id": retake_commission["id"], "expected_history_generation": 1})["assignment"]
        self.api(path + f"/retakes/{student}", self.admin_a, "POST", {"commission_id": retake_commission["id"], "expected_history_generation": 1}, code=409)
        self.expect(self.api(path + f"/results/{student}", self.student_a)["effective_grade"] == 4, "unfinished retake preserves first result")
        rattempt = self.api(path + f"/assignments/{retake['id']}/attempts/ensure", self.examiner_tokens[0], "POST", ensure_body)["attempt"]
        rpath = path + f"/attempts/{rattempt['id']}"
        for index in [0, 2]:
            self.api(rpath + "/ready", self.examiner_tokens[index], "POST", {})
        rstate = self.api(rpath, self.examiner_tokens[0])
        self.api(rpath + "/start", self.examiner_tokens[0], "POST", {"expected_state_version": rstate["state_version"]})
        for question in range(5):
            if question:
                state = self.api(rpath, self.examiner_tokens[0])
                self.api(rpath + "/next-question", self.examiner_tokens[0], "POST", {"presented_question_id": state["current_question"]["id"], "expected_state_version": state["state_version"]})
            self.consensus(rpath, 0, [0, 2])
        self.expect(self.api(path + f"/results/{student}", self.student_a)["effective_grade"] == 0, "completed worse retake replaces first result")
        self.rating_scope(exam, student, 0)
        preview = self.api(path + f"/history/{student}/delete-preview", self.admin_a)
        self.api(path + f"/history/{student}", self.admin_a, "DELETE", {"expected_history_generation": 1}, code=409)
        self.api(path + f"/history/{student}", self.admin_a, "DELETE", {"expected_history_generation": 1}, headers={"X-Exam-Confirmation": preview["confirmation"]})
        self.expect(self.api(path + "/attempts", self.admin_a)["total"] == 0, "history cleanup removes first and retake attempts")
        first_assignment = self.api(path + "/assignments", self.admin_a)["items"][0]
        self.expect(first_assignment["history_generation"] == 2 and first_assignment["attempt_no"] == 1, "history cleanup preserves first assignment with new generation")
        self.api(ensure, self.examiner_tokens[0], "POST", ensure_body, code=409)
        self.imports_and_outside(part, exam)
        self.assignment_import(copy_id)
        self.training(exam)
        self.scoped_staff_security(exam, copy_id)
        self.read_smoke(exam)
        self.delete_copy(copy_id)

    def security(self):
        document = self.request(self.args.exams_url, "/openapi.json")
        self.expect(document["openapi"] == "3.1.0" and len(document["paths"]) >= 40, "OpenAPI describes complete registered exam API")
        for path, operations in document["paths"].items():
            if not path.startswith("/v1/exams"):
                continue
            concrete = path
            for parameter in ["exam", "part", "question", "commission", "assignment", "attempt", "student", "result", "presented", "division"]:
                concrete = concrete.replace("{" + parameter + "}", "1")
            concrete = concrete.replace("{kind}", "questions").replace("{session}", str(uuid.uuid4()))
            for method in operations:
                self.request(self.args.exams_url, concrete, method=method.upper(), body={} if method != "get" else None, code=401)
        self.request(self.args.exams_url, "/v1/exams", code=401, headers={"X-Role": "admin", "X-Organization-ID": "1001"})
        self.api("", self.viewer, code=403)
        self.api("?limit=0", self.admin_a, code=400)
        self.api("?limit=1&limit=2", self.admin_a, code=400)
        self.api("?division_id=" + str(self.b), self.student_a, code=400)
        self.api("?organization_id=2001", self.student_a, code=400)
        self.api("", self.student_a, "POST", {"exam_type": "classic", "direction_id": 1001}, code=403)
        self.api("", self.admin_a, "POST", {"exam_type": "classic", "direction_id": 1001, "organization_id": 2001}, code=400)
        self.api("", self.admin_a, "POST", {"exam_type": "classic", "direction_id": 2001}, code=404)
        self.request(self.args.exams_url, "/v1/exams", self.admin_a, "POST", {"exam_type": "classic", "direction_id": 1001}, code=400)
        self.request(self.args.core_url, "/internal/core/exam-references", self.admin_a, "POST", {"organization_id": 1001, "division_ids": [self.a]}, code=401)
        self.request(self.args.core_url, "/internal/core/exam-references", method="POST", body={"organization_id": 1001, "division_ids": [2001]}, code=404, headers={"X-Service-Key": self.internal_key})

    def classic(self):
        key = str(uuid.uuid4())
        body = {"exam_type": "classic", "direction_id": 1001}
        created = self.api("", self.admin_a, "POST", body, code=201, key=key)
        self.exams.append(created["id"])
        self.expect(self.api("", self.admin_a, "POST", body, code=201, key=key) == created, "exam creation replay does not duplicate catalog")
        self.api("", self.admin_a, "POST", {**body, "direction_id": 1002}, code=409, key=key)
        exam = created["id"]
        path = f"/{exam}"
        part = self.api(path + "/parts", self.admin_a, "POST", {"code": "A", "weight": 1, "quota": 5})["part"]
        questions = []
        for n in range(7):
            questions.append(self.api(path + "/questions", self.admin_a, "POST", {"part_id": part["id"], "question_text": f"DEV {self.run} Q{n}", "answer_text": f"DEV {self.run} A{n}"})["question"])
        self.api(path + "/questions", self.admin_a, "POST", {"part_id": part["id"], "question_text": questions[0]["question_text"], "answer_text": questions[0]["answer_text"]}, code=409)
        now = datetime.now(timezone.utc)
        config = {"expected_revision": self.api(path + "/config", self.admin_a)["config"]["revision"], "start_at": (now - timedelta(hours=1)).isoformat(), "end_at": (now + timedelta(hours=3)).isoformat(), "fractional_mode": "extra_question", "tie_breaker_source_mode": "any_part", "tie_breaker_half_mode": "repeat", "thresholds": [{"grade": n, "min_score": n} for n in range(6)]}
        self.api(path + "/config", self.admin_a, "PUT", config)
        self.api(path + "/config", self.admin_a, "PUT", config, code=409)
        current = self.api(path + "/config", self.admin_a)["config"]
        before = self.snapshot_data()["source_revisions"]
        changed_start, changed_end = now - timedelta(minutes=30), now + timedelta(hours=4)
        self.api(path + "/config", self.admin_a, "PUT", {"expected_revision": current["revision"], "start_at": changed_start.isoformat(), "end_at": changed_end.isoformat()})
        updated = self.api(path + "/config", self.admin_a)["config"]
        self.expect(datetime.fromisoformat(updated["start_at"].replace("Z", "+00:00")) == changed_start and datetime.fromisoformat(updated["end_at"].replace("Z", "+00:00")) == changed_end and updated["revision"] == current["revision"] + 1, "editing existing non-null date pointers persists both timestamps and revision")
        after = self.snapshot_data()["source_revisions"]
        self.expect(after[str(self.a)] == before[str(self.a)] + 1 and after[str(self.b)] == before[str(self.b)], "date update invalidates only its own division source revision")
        self.api(path + "/config", self.admin_a, "PUT", {"expected_revision": updated["revision"], "start_at": config["start_at"], "end_at": config["end_at"]})
        commission = self.api(path + "/commissions", self.admin_a, "POST", {"name": "DEV commission " + self.run, "examinator_ids": [self.examiners[0][0], self.examiners[1][0]]})["commission"]
        assignment = self.api(path + "/assignments", self.admin_a, "POST", {"student_account_id": self.students[0][0], "commission_id": commission["id"]})["assignment"]
        self.api(path + f"/privileges/{self.students[0][0]}", self.admin_a, "PUT", {"expected_revision": 0, "replacement_limit": 1})
        self.expect(self.api(path + "/readiness", self.admin_a)["is_configured"], "complete bank settings pass readiness validation")
        return exam, part, questions, commission, assignment

    def vote(self, apath, index, value):
        token = self.examiner_tokens[index]
        question = self.api(apath, token)["current_question"]
        return self.api(apath + "/vote", token, "POST", {"presented_question_id": question["id"], "round_id": question["round"]["id"], "value": value})

    def consensus(self, apath, value, indexes=None):
        indexes = indexes or [0, 1]
        question = self.api(apath, self.examiner_tokens[indexes[0]])["current_question"]
        body = {"presented_question_id": question["id"], "round_id": question["round"]["id"], "value": value}
        with ThreadPoolExecutor(max_workers=len(indexes)) as pool:
            return list(pool.map(lambda index: self.api(apath + "/vote", self.examiner_tokens[index], "POST", body), indexes))

    def rating_scope(self, exam, student, grade):
        now = datetime.now(timezone.utc)
        path = f"/internal/exams/rating-snapshot?organization_id=1001&division_ids={self.a},{self.b}&date_from={(now - timedelta(days=2)).date().isoformat()}&date_to={(now + timedelta(days=2)).date().isoformat()}"
        self.request(self.args.exams_url, path, self.admin_a, code=401)
        snapshot = self.request(self.args.exams_url, path, headers={"X-Service-Key": self.internal_key})["data"]["snapshot"]
        rows = [r for r in snapshot["results"] if r["exam_id"] == exam and r["account_id"] == student]
        self.expect(len(rows) == 1 and rows[0]["division_id"] == self.a and rows[0]["grade"] == grade, "rating snapshot exports only effective subject result in its division")

    def snapshot_data(self):
        now = datetime.now(timezone.utc)
        path = f"/internal/exams/rating-snapshot?organization_id=1001&division_ids={self.a},{self.b}&date_from={(now - timedelta(days=2)).date().isoformat()}&date_to={(now + timedelta(days=2)).date().isoformat()}"
        return self.request(self.args.exams_url, path, headers={"X-Service-Key": self.internal_key})["data"]

    def upload(self, exam, kind, rows, token=None):
        boundary = "cpm_" + self.run
        body = (f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="dev_exams.xlsx"\r\nContent-Type: application/vnd.openxmlformats-officedocument.spreadsheetml.sheet\r\n\r\n').encode() + xlsx(rows) + f"\r\n--{boundary}--\r\n".encode()
        return self.api(f"/{exam}/imports/{kind}", token or self.admin_a, "POST", body, headers={"Content-Type": "multipart/form-data; boundary=" + boundary})

    def imports_and_outside(self, part, exam):
        preview = self.upload(exam, "questions", [["part_code", "question_text", "answer_text"], ["A", "DEV imported " + self.run, "DEV answer"]])
        self.expect(not preview["rows"][0]["errors"], "question spreadsheet resolves part code")
        ipath = f"/{exam}/imports/questions/{preview['id']}"
        self.api(ipath, self.admin_b, code=404)
        self.api(ipath + "/commit", self.admin_a, "POST", {"expected_revision": preview["revision"] + 1}, code=409)
        key = str(uuid.uuid4())
        result = self.api(ipath + "/commit", self.admin_a, "POST", {"expected_revision": preview["revision"]}, key=key)
        self.expect(self.api(ipath + "/commit", self.admin_a, "POST", {"expected_revision": preview["revision"]}, key=key) == result, "question import commit replays without duplicates")
        self.expect(self.api(f"/{exam}/questions", self.admin_a)["total"] == 8, "question import adds exactly one question")
        outside = self.api("", self.admin_a, "POST", {"exam_type": "outside_lms", "direction_id": 1001, "date": datetime.now(timezone.utc).date().isoformat()}, code=201)
        self.exams.append(outside["id"])
        opath = f"/{outside['id']}"
        body = {"student_account_id": self.students[0][0], "points": 123.45, "grade": 2, "examinator": "DEV external"}
        saved = self.api(opath + "/outside-results", self.admin_a, "POST", body)["result"]
        self.api(opath + "/outside-results", self.admin_a, "POST", body, code=409)
        self.api(opath + "/outside-results", self.admin_a, "POST", {**body, "student_account_id": self.students[1][0], "points": 1.234}, code=400)
        read = self.api(opath + f"/results/{self.students[0][0]}", self.student_a)
        self.expect(read["points"] == 123.45 and read["grade"] == 2, "outside points remain independent from grade")
        self.api(opath + f"/outside-results/{saved['id']}", self.admin_a, "PUT", {"expected_revision": saved["revision"], "grade": 5})
        imported = self.upload(outside["id"], "outside", [["student_login", "points", "grade", "examinator"], [self.students[1][1], 54.5, 4, "DEV imported external"]])
        self.expect(not imported["rows"][0]["errors"], "outside import resolves exact synthetic login: " + ",".join(imported["rows"][0]["errors"]))
        self.api(opath + f"/imports/outside/{imported['id']}/commit", self.admin_a, "POST", {"expected_revision": imported["revision"]})
        self.expect(self.api(opath + "/results", self.admin_a)["total"] == 2, "outside import atomically adds second student result")

    def training(self, exam):
        path = f"/{exam}"
        detail = self.api(path, self.admin_a)
        self.api(path + "/training", self.admin_a, "PUT", {"expected_revision": detail["division_revision"], "training_enabled": True})
        view = self.api(path + "/training", self.student_a)
        self.expect(view["stats"]["total"] == 8, "training uses current bank independently of historical snapshots")
        card = view["cards"][0]
        progress = path + f"/training/progress/{card['question_id']}"
        self.api(progress, self.student_a, "PUT", {}, code=400)
        self.api(progress, self.student_a, "PUT", {"content_fingerprint": card["content_fingerprint"]})
        self.expect(self.api(path + "/training", self.student_a)["stats"]["learned"] == 1, "own training progress persisted")
        self.api(path + "/training", self.student_b, code=409)
        question = next(q for q in self.api(path + "/questions", self.admin_a)["items"] if q["id"] == card["question_id"])
        self.api(path + f"/questions/{question['id']}", self.admin_a, "PUT", {"expected_revision": question["revision"], "answer_text": "DEV changed training answer " + self.run})
        self.expect(self.api(path + "/training", self.student_a)["stats"]["answer_changed"] == 1, "bank edit marks prior learned fingerprint stale")
        self.api(progress, self.student_a, "PUT", {"content_fingerprint": card["content_fingerprint"]}, code=409)
        self.api(path + "/training/settings", self.student_a, "PUT", {"batch_size": 11}, code=400)
        saved = self.api(path + "/training/settings", self.student_a, "PUT", {"batch_size": 20, "study_mode": "stale", "last_part_id": card["part_id"], "last_batch_index": 0})
        self.expect(saved["batch_size"] == 20 and self.api(path + "/training", self.student_a)["settings"]["study_mode"] == "stale", "personal training settings persist independently")
        current = next(c for c in self.api(path + "/training", self.student_a)["cards"] if c["question_id"] == card["question_id"])
        self.api(progress, self.student_a, "PUT", {"content_fingerprint": current["content_fingerprint"]})
        self.expect(self.api(path + "/training", self.student_a)["stats"]["answer_changed"] == 0, "updated answer can be reconfirmed with current fingerprint")
        self.api(progress, self.student_a, "DELETE", {})
        self.expect(self.api(path + "/training", self.student_a)["stats"]["learned"] == 0, "student removes own learned mark")

    def assignment_import(self, exam):
        preview = self.upload(exam, "assignments", [["student_login", "examinator_1_login", "examinator_2_login", "replacement_limit"], [self.students[0][1], self.examiners[0][1], self.examiners[2][1], 1], ["no_such_dev_" + self.run, self.examiners[0][1], self.examiners[2][1], 0]], self.admin_b)
        self.expect(not preview["rows"][0]["errors"] and bool(preview["rows"][1]["errors"]), "assignment import exposes invalid reference per row")
        path = f"/{exam}/imports/assignments/{preview['id']}"
        self.api(path + "/commit", self.admin_b, "POST", {"expected_revision": preview["revision"]}, code=409)
        self.expect(self.api(f"/{exam}/assignments", self.admin_b)["total"] == 0, "invalid assignment batch commits no partial data")
        changed = self.api(path, self.admin_b, "PUT", {"expected_revision": preview["revision"], "changes": [{"row_id": preview["rows"][1]["row_id"], "excluded": True}]})
        self.api(path, self.admin_b, "PUT", {"expected_revision": preview["revision"], "changes": []}, code=409)
        self.api(path + "/commit", self.admin_b, "POST", {"expected_revision": changed["revision"]})
        assignments = self.api(f"/{exam}/assignments", self.admin_b)["items"]
        self.expect(len(assignments) == 1 and assignments[0]["student"]["account_id"] == self.students[0][0] and assignments[0]["replacement_limit"] == 1, "assignment import resolves logins and atomically adds personal replacement limit")

    def scoped_staff_security(self, exam, bank_exam):
        path = f"/{exam}"
        question = self.api(path + "/questions", self.scoped_staff)["items"][0]
        self.api(path + f"/questions/{question['id']}", self.scoped_staff, "PUT", {"expected_revision": question["revision"], "answer_text": "DEV forbidden shared change"}, code=403)
        self.api(path + "/parts", self.scoped_staff, "POST", {"code": "B", "weight": 1, "quota": 1}, code=403)
        preview = self.api(path + "/delete-preview", self.admin_a)
        self.api(path, self.scoped_staff, "DELETE", {"expected_revision": preview["revision"]}, code=403, headers={"X-Exam-Confirmation": preview["confirmation"]})
        self.api(path + "/results?division_id=" + str(self.b), self.scoped_staff, code=400)
        bank = f"/{bank_exam}"
        self.api(bank + "/bank", self.admin_b, "PUT", {"expected_revision": self.api(bank, self.admin_b)["revision"], "in_bank": True})
        bank_detail = self.api(bank, self.scoped_staff)
        self.expect(bank_detail["id"] == bank_exam and self.api(bank + "/parts", self.scoped_staff)["total"] == 1 and self.api(bank + "/questions", self.scoped_staff)["total"] == 7, "organization bank grants readable content to staff outside owning division")
        self.api(bank + "/results", self.scoped_staff, code={403, 404})
        self.api(bank + "/assignments", self.scoped_staff, code={403, 404})
        self.api(bank + "/attempts", self.scoped_staff, code={403, 404})
        copied = self.api(bank + "/copy", self.scoped_staff, "POST", {"division_id": self.a, "expected_revision": bank_detail["revision"]})
        self.exams.append(copied["id"])
        self.expect(copied["id"] != bank_exam and copied["owner_division_id"] == self.a, "staff can copy organization bank content into its authorized division")
        self.expect(self.api(f"/{copied['id']}/assignments", self.scoped_staff)["total"] == 0, "staff bank copy excludes source division assignments")

    def read_smoke(self, exam):
        def one(_):
            start = time.perf_counter()
            self.api(f"/{exam}", self.student_a)
            return (time.perf_counter() - start) * 1000
        start = time.perf_counter()
        with ThreadPoolExecutor(max_workers=32) as pool:
            values = sorted(pool.map(one, range(450)))
        self.smoke = {"requests": 450, "workers": 32, "elapsed_seconds": round(time.perf_counter() - start, 3), "p95_ms": round(values[427], 2), "p99_ms": round(values[445], 2), "description": "local concurrent-read smoke; does not claim 450 simultaneous writers"}
        self.expect(len(values) == 450, "450 bounded concurrent read requests succeed")

    def delete_copy(self, exam):
        path = f"/{exam}"
        preview = self.api(path + "/delete-preview", self.admin_b)
        body = {"expected_revision": preview["revision"]}
        self.api(path, self.admin_b, "DELETE", body, code=409)
        key = str(uuid.uuid4())
        deleted = self.api(path, self.admin_b, "DELETE", body, key=key, headers={"X-Exam-Confirmation": preview["confirmation"]})
        self.expect(self.api(path, self.admin_b, "DELETE", body, key=key, headers={"X-Exam-Confirmation": preview["confirmation"]}) == deleted, "physical exam deletion receipt survives missing exam")
        self.api(path, self.admin_b, code=404)


def xlsx(rows):
    """Prefer a standard openpyxl workbook; retain portable OOXML fallback."""
    try:
        from openpyxl import Workbook
    except ImportError:
        runtime = Path(os.environ.get("EXAMS_WORKSPACE_PYTHON", str(Path.home() / ".cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3")))
        if runtime.is_file():
            generator = "import io,json,sys;from openpyxl import Workbook;w=Workbook();s=w.active;rows=json.load(sys.stdin);[s.append(row) for row in rows];b=io.BytesIO();w.save(b);sys.stdout.buffer.write(b.getvalue())"
            result = subprocess.run([str(runtime), "-c", generator], input=json.dumps(rows).encode(), capture_output=True, check=False)
            if result.returncode == 0:
                return result.stdout
    else:
        workbook = Workbook()
        for row in rows:
            workbook.active.append(row)
        stream = io.BytesIO()
        workbook.save(stream)
        return stream.getvalue()
    stream = io.BytesIO()
    with zipfile.ZipFile(stream, "w", compression=zipfile.ZIP_DEFLATED) as archive:
        archive.writestr("[Content_Types].xml", '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>')
        archive.writestr("_rels/.rels", '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>')
        archive.writestr("xl/workbook.xml", '<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Import" sheetId="1" r:id="rId1"/></sheets></workbook>')
        archive.writestr("xl/_rels/workbook.xml.rels", '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>')
        text = '<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>'
        for ri, row in enumerate(rows, 1):
            text += f'<row r="{ri}">'
            for ci, value in enumerate(row):
                if type(value) in {int, float}:
                    text += f'<c r="{chr(65 + ci)}{ri}" t="n"><v>{value}</v></c>'
                else:
                    text += f'<c r="{chr(65 + ci)}{ri}" t="inlineStr"><is><t>{escape(str(value))}</t></is></c>'
            text += "</row>"
        archive.writestr("xl/worksheets/sheet1.xml", text + "</sheetData></worksheet>")
    return stream.getvalue()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--auth-url", type=local_url, default=os.environ.get("AUTH_URL", "http://127.0.0.1:8002"))
    parser.add_argument("--core-url", type=local_url, default=os.environ.get("CORE_URL", "http://127.0.0.1:8003"))
    parser.add_argument("--exams-url", type=local_url, default=os.environ.get("EXAMS_URL", "http://127.0.0.1:8010"))
    parser.add_argument("--keep-fixtures", action="store_true", help="keep this run's synthetic fixtures for debugging")
    args = parser.parse_args()
    for value in [args.auth_url, args.core_url, args.exams_url]:
        local_url(value)
    regression = Regression(args)
    failure = None
    try:
        regression.fixtures()
        regression.workflow()
    except Exception as error:
        failure = error
    finally:
        if not args.keep_fixtures:
            try:
                regression.cleanup()
            except Exception:
                if failure is None:
                    failure = AssertionError("synthetic cleanup failed")
    report = {"status": "passed" if failure is None else "failed", "checks": len(regression.checks), "synthetic_run": regression.run, "fixtures_retained": args.keep_fixtures}
    if hasattr(regression, "smoke"):
        report["read_smoke"] = regression.smoke
    if failure is not None:
        report["error"] = str(failure) if isinstance(failure, AssertionError) else type(failure).__name__
        frames = traceback.extract_tb(failure.__traceback__)
        if frames:
            report["failure_location"] = {"function": frames[-1].name, "line": frames[-1].lineno}
        if hasattr(regression, "last_request"):
            report["last_request"] = regression.last_request
    print(json.dumps(report, ensure_ascii=False))
    private = ROOT / "dev/local/exams-http-report.json"
    private.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    private.chmod(0o600)
    fixtures = ROOT / "dev/local/exams-fixtures.json"
    fixtures.write_text(json.dumps({"synthetic_run": regression.run, "division_ids": regression.divisions, "group_ids": regression.groups, "account_ids": regression.users, "exam_ids": regression.exams, "cleaned": not args.keep_fixtures and failure is None}, indent=2) + "\n")
    fixtures.chmod(0o600)
    return 0 if failure is None else 1


if __name__ == "__main__":
    raise SystemExit(main())
