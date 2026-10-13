"""Real cookie-BFF ratings, public source fixtures and Rabbit/calculate checks.

Only the audited public fixture setup from http_test.py is reused. Its direct
Mongo pending simulation and SQL outbox inspection are never called. This suite
has no database client operations, cleanup or service/process control.
"""
from __future__ import annotations

from datetime import datetime, timedelta, timezone
import base64
import http.cookiejar
import importlib.util
import json
import math
from pathlib import Path
import re
import secrets
import urllib.error
import urllib.parse
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[3]
spec = importlib.util.spec_from_file_location("ratings_public_fixture_helpers", ROOT / "services/ratings/scripts/http_test.py")
owner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(owner)
EXAMS, BFF = "http://127.0.0.1:8010", "http://127.0.0.1:8001"
owner.ORIGINS.add(EXAMS)


class NoRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


class Suite(owner.Suite):
    def __init__(self):
        super().__init__()
        self.prefix = "DEV BFF Ratings " + self.run
        self.password = self.admin_password
        self.browser = {}
        self.expected_a = {"homework": 40, "tests": 25, "exams": 1, "final": 27.25, "place": 1}
        self.expected_first = {**self.expected_a, "exams": 3, "final": 39.25}
        self.expected_b = {"homework": 0, "tests": 0, "exams": 2.5, "final": 15, "place": 1}
        self.fixture_path = ROOT / "dev/local/bff-ratings-fixtures.json"
        self.report_path = ROOT / "dev/local/bff-ratings-http-report.json"
        self.events, self.inbox, self.completed_a = [], [], []
        self.rabbit_queue = None

    def persist(self):
        public = {key: value for key, value in self.fixtures.items() if key != "password"}
        owner.private_json(self.fixture_path, public)
        users = {row["login"].rsplit("_", 1)[-1]: row for row in self.fixtures["users"]}
        demo = {"run": self.run, "organization_id": 1001, "division_a": self.division, "division_b": self.secondary,
                "period": self.period, "expected_a": self.expected_a, "expected_first_a": self.expected_first,
                "expected_b": self.expected_b, "jobs_a": self.completed_a,
                "external_tests_mode": "dev_internal_only", "exams_mode": "required"}
        for key, suffix in (("student", "student1"), ("student2", "student2"), ("proctor", "proctor"), ("supervisor", "supervisor")):
            if suffix in users:
                demo[key + "_login"] = users[suffix]["login"]
                demo[key + "_account_id"] = users[suffix]["account_id"]
        for key, suffix in (("scoped_staff_view_login", "viewer"), ("scoped_staff_editor_login", "editor")):
            if suffix in users:
                demo[key] = users[suffix]["login"]
        for key, value in (("group_a", self.fixtures["groups"][0] if self.fixtures["groups"] else None),
                           ("group_b", self.fixtures["groups"][1] if len(self.fixtures["groups"]) > 1 else None),
                           ("classic_exam_id", getattr(self, "classic_id", None)), ("outside_exam_id", getattr(self, "outside_id", None)),
                           ("copy_exam_id", getattr(self, "copy_id", None)), ("first_attempt_id", getattr(self, "first_attempt", {}).get("id")),
                           ("retake_attempt_id", getattr(self, "retake_attempt", {}).get("id")), ("job_b_id", getattr(self, "job_b", {}).get("id"))):
            if value is not None:
                demo[key] = value
        owner.private_json(ROOT / "dev/local/bff-ratings-demo.json", demo)

    def bff(self, session, path, method="GET", body=None, codes=200, headers=None, record=True):
        outbound = {"X-CPM-Request": "1", "X-CPM-Context": session["revision"], "Origin": "http://127.0.0.1:3001", **(headers or {})}
        outbound.pop("Authorization", None)
        raw = None
        if body is not None:
            raw = json.dumps(body, allow_nan=False).encode()
            outbound["Content-Type"] = "application/json"
        request = urllib.request.Request(BFF + path, method=method, data=raw, headers=outbound)
        try:
            with session["http"].open(request, timeout=45) as response:
                code, result = response.status, json.load(response)
        except urllib.error.HTTPError as response:
            code, result = response.code, json.load(response)
        allowed = (codes,) if isinstance(codes, int) else tuple(codes)
        assert code in allowed, f"{method} {path.split('?')[0]} expected {allowed}, got {code} {result.get('error', '')}"
        assert not any(key in result for key in ("token", "access_token", "refresh_token")), "Browser received an auth token"
        if record:
            self.expect(True, f"{method} {path.split('?')[0]} HTTP {code}, no browser token")
        if "context_revision" in result:
            session["revision"] = result["context_revision"]
            session["division"] = result["user"].get("division_id")
        return code, result

    def login(self, login, division=1001, organization=1001, fixture=True):
        token, user = super().login(login, division, organization, fixture)
        jar = http.cookiejar.CookieJar()
        session = {"http": urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar), NoRedirects()), "revision": "", "division": None, "organization": organization}
        self.bff(session, "/bff/auth/login", "POST", {"login": login, "password": self.password if fixture else self.admin_password, "organization_id": organization, "division_id": division})
        cookie = next(cookie for cookie in jar if cookie.name == "cpm_bff")
        self.expect(cookie.has_nonstandard_attr("HttpOnly") and cookie.get_nonstandard_attr("SameSite") == "Strict", "BFF auth uses an HttpOnly Strict cookie")
        self.browser[token] = session
        return token, user

    def request(self, origin, path, token=None, method="GET", body=None, codes=200, headers=None, record=True):
        match = re.fullmatch(r"/v1/ratings/divisions/(\d+)/(.*)", path) if origin == owner.RATINGS else None
        if not match:
            return super().request(origin, path, token, method, body, codes, headers, record)
        division, tail = int(match[1]), match[2]
        session = self.browser.get(token) or {"http": urllib.request.build_opener(NoRedirects()), "revision": "", "division": None}
        if token and session["division"] != division:
            status, response = self.bff(session, "/bff/auth/context", "POST", {"organization_id": session["organization"], "division_id": division}, codes=(200, 403, 404), record=record)
            if status != 200:
                allowed = (codes,) if isinstance(codes, int) else tuple(codes)
                assert status in allowed, "Cabinet selection rejected an expected authorized ratings request"
                return status, response
        return self.bff(session, "/bff/ratings/" + tail, method, body, codes, headers, record)

    def exam(self, path="", token=None, method="GET", body=None, codes=200):
        return self.api(EXAMS, "/v1/exams", path, token or self.admin_a, method, body, codes,
                        {"Idempotency-Key": str(uuid.uuid4())} if method != "GET" else None)

    def extra_test(self, direction, score=None):
        test = self.tests(f"/divisions/{self.division}/tests", method="POST", body={"title": self.prefix + (" missing" if score is None else " zero"), "description": "Public API rating fixture", "direction_id": direction,
            "questions": [{"question_id": 1, "type": "single", "text": "Choose A", "points": 2, "answers": [{"id": "a", "text": "A", "is_correct": True}, {"id": "b", "text": "B", "is_correct": False}], "correct_answers": []}]}, codes=201, headers={"Idempotency-Key": uuid.uuid4().hex})
        self.fixtures["tests"].append(test["id"])
        now = datetime.now(timezone.utc)
        self.tests(f"/divisions/{self.division}/tests/{test['id']}/settings", method="PUT", body={"expected_revision": test["revision"], "published": True, "start_at": (now-timedelta(minutes=10)).isoformat(), "end_at": (now+timedelta(hours=2)).isoformat(), "time_limit_minutes": 60, "passing_score": 50, "answers_visible": True})
        if score is not None:
            attempt = self.tests(f"/divisions/{self.division}/tests/{test['id']}/attempts", self.student, "POST", {"practice": False}, 201)
            accepted = self.tests(f"/attempts/{attempt['id']}/finalize", self.student, "POST", {"test_version_id": attempt["test_version_id"], "reason": "manual", "completed_at": datetime.now(timezone.utc).isoformat(), "answers": [{"question_id": 1, "value": "b"}]}, 202)
            self.expect(accepted["calculation_status"] == "queued", "Accepted source test is queued rather than showing an immediate result")
            result = self.wait(lambda: (row if (row := self.tests('/submissions/'+accepted['submission_id'], self.student, record=False))["calculation_status"] == "ready" else None), "Official zero test becomes ready through calculate")
            self.expect(result["result"]["score"] == 0, "Official source preserves a completed zero score")
        self.persist()

    def conduct(self, assignment, examiner, value):
        path = f"/{self.classic_id}"
        state = self.exam(path+f"/assignments/{assignment['id']}/attempts/ensure", examiner, "POST", {"expected_history_generation": assignment["history_generation"]})["attempt"]
        state = self.exam(path+f"/attempts/{state['id']}/ready", examiner, "POST", {})["attempt"]
        state = self.exam(path+f"/attempts/{state['id']}/start", examiner, "POST", {"expected_state_version": state["state_version"]})["attempt"]
        question = state["current_question"]
        state = self.exam(path+f"/attempts/{state['id']}/vote", examiner, "POST", {"presented_question_id": question["id"], "round_id": question["round"]["id"], "value": value})["attempt"]
        self.expect(state["status"] == "completed", "Single-commission oral result completes in its owner")
        return state

    def setup_inputs(self):
        super().setup()  # Audited public core/homework/test fixture creation only.
        self.supervisor, _ = self.user("supervisor", "supervisor")
        self.examiner1, examiner1_id = self.user("examiner1", "examinator")
        self.examiner2, examiner2_id = self.user("examiner2", "examinator")
        self.admin_a, _ = self.login("dev_admin", self.division, fixture=False)
        self.admin_b, _ = self.login("dev_admin", self.secondary, fixture=False)
        for name, score in (("ready zero", 0), ("missing", None)):
            assignment = self.homework("/assignments", "POST", {"name": self.prefix+" "+name, "type_id": self.fixtures["types"][0], "deadline": self.yesterday, "published": False}, 201)["id"]
            self.fixtures["assignments"].append(assignment)
            if score is not None:
                self.homework(f"/assignments/{assignment}/manual", "POST", {"account_id": self.student_id, "date_pass": self.yesterday, "status": 1, "result": score})
        self.extra_test(self.direction)
        other_direction = self.core("/directions", method="POST", body={"name": self.prefix+" second direction"}, codes=201)["id"]
        self.extra_test(other_direction, 0)
        self.classic_id = self.exam(method="POST", body={"exam_type": "classic", "direction_id": self.direction, "name": self.prefix+" oral"}, codes=201)["id"]
        path = f"/{self.classic_id}"
        part = self.exam(path+"/parts", method="POST", body={"code": "A", "weight": 5, "quota": 1})["part"]
        for number in (1, 2):
            self.exam(path+"/questions", method="POST", body={"part_id": part["id"], "question_text": self.prefix+f" Q{number}", "answer_text": f"A{number}"})
        now = datetime.now(timezone.utc)
        self.exam(path+"/config", method="PUT", body={"expected_revision": self.exam(path+"/config")["config"]["revision"], "start_at": (now-timedelta(hours=1)).isoformat(), "end_at": (now+timedelta(hours=3)).isoformat(), "fractional_mode": "round_down", "thresholds": [{"grade": n, "min_score": n} for n in range(6)]})
        commission = self.exam(path+"/commissions", method="POST", body={"name": self.prefix+" first", "examinator_ids": [examiner1_id]})["commission"]
        assignment = self.exam(path+"/assignments", method="POST", body={"student_account_id": self.student_id, "commission_id": commission["id"]})["assignment"]
        self.first_attempt = self.conduct(assignment, self.examiner1, 1)
        self.exam(path+f"/attempts/{self.first_attempt['id']}/appeals", method="POST", body={"expected_result_version": self.first_attempt["result_version"], "grade": 4})
        second_commission = self.exam(path+"/commissions", method="POST", body={"name": self.prefix+" retake", "examinator_ids": [examiner2_id]})["commission"]
        self.retake_assignment = self.exam(path+f"/retakes/{self.student_id}", method="POST", body={"commission_id": second_commission["id"], "expected_history_generation": assignment["history_generation"]})["assignment"]
        self.retake_attempt = self.exam(path+f"/assignments/{self.retake_assignment['id']}/attempts/ensure", self.examiner2, "POST", {"expected_history_generation": self.retake_assignment["history_generation"]})["attempt"]
        self.outside_id = self.exam(method="POST", body={"exam_type": "outside_lms", "direction_id": self.direction, "name": self.prefix+" outside", "date": now.date().isoformat()}, codes=201)["id"]
        outside = f"/{self.outside_id}"
        self.exam(outside+"/outside-results", method="POST", body={"student_account_id": self.student_id, "grade": 2, "points": 123.45, "examinator": "Synthetic"})
        self.exam(outside+"/connections", method="POST", body={"expected_revision": self.exam(outside)["revision"], "division_id": self.secondary})
        self.exam(outside+"/outside-results", self.admin_b, "POST", {"student_account_id": self.student_id, "grade": 5, "points": 0, "examinator": "Synthetic B"})
        self.copy_id = self.exam(outside+"/copy", self.admin_b, "POST", {"expected_revision": self.exam(outside, self.admin_b)["revision"], "division_id": self.secondary})["id"]
        self.persist()

    def assert_formula(self, listing, expected, accounts):
        rows = {row["profile"]["account_id"]: row for row in listing["ratings"]}
        self.expect(set(rows) == set(accounts), "Published scope contains exactly its current participant accounts")
        result = rows[self.student_id]["rating"]
        for name in ("homework", "tests", "exams"):
            self.expect(math.isclose(result[name]["rating"], expected[name], abs_tol=1e-9), "Manual expected "+name+" component")
        self.expect(math.isclose(result["final_rating"], expected["final"], abs_tol=1e-9), "Manual unrounded H*.25 + E*6 + T*.45 rating")
        self.expect(rows[self.student_id]["place"] == expected["place"], "Global division place is stable")
        if self.other_id in rows:
            self.expect(rows[self.other_id]["rating"]["final_rating"] == 0 and rows[self.other_id]["place"] == 2, "Missing results publish an actual zero rating in second place")
        return rows

    def rabbit(self, path, method="GET", body=None, codes=(200,)):
        broker = urllib.parse.urlsplit(self.env["RABBITMQ_URL"])
        assert broker.hostname in {"localhost", "127.0.0.1"} and urllib.parse.unquote(broker.path[1:]) == "cpm_dev"
        auth = base64.b64encode((urllib.parse.unquote(broker.username)+":"+urllib.parse.unquote(broker.password)).encode()).decode()
        request = urllib.request.Request("http://127.0.0.1:15672"+path, method=method, data=json.dumps(body).encode() if body is not None else None, headers={"Content-Type": "application/json", "Authorization": "Basic "+auth})
        with urllib.request.build_opener(NoRedirects()).open(request, timeout=20) as response:
            assert response.status in codes
            raw = response.read()
            return json.loads(raw) if raw else None

    def start_probe(self):
        self.rabbit_queue = "dev_bff_ratings_proof_"+uuid.uuid4().hex
        self.rabbit("/api/queues/cpm_dev/"+self.rabbit_queue, "PUT", {"durable": True, "auto_delete": False, "arguments": {"x-queue-type": "classic", "x-expires": 120000, "x-message-ttl": 120000, "x-max-length": 1000}}, (201, 204))
        self.rabbit("/api/bindings/cpm_dev/e/cpm.events/q/"+self.rabbit_queue, "POST", {"routing_key": "ratings.calculate.requested.v1", "arguments": {}}, (201,))

    def verify_transport(self, job):
        messages = self.wait(lambda: (rows if len(rows := self.internal(owner.CALCULATE, '/internal/calculate/ratings/messages?job_id='+job['id']+'&limit=100', record=False)["items"]) == 2 and all(row["status"] == "ready" for row in rows) else None), "Real calculate durable inbox confirms both personal results")
        self.expect({row["account_id"] for row in messages} == {self.student_id, self.other_id}, "One calculate identity per prepared student")
        self.inbox = [{key: row.get(key) for key in ("message_id", "job_id", "revision", "account_id", "organization_id", "division_id", "input_hash", "status")} for row in messages]
        deliveries = self.rabbit("/api/queues/cpm_dev/"+self.rabbit_queue+"/get", "POST", {"count": 1000, "ackmode": "ack_requeue_false", "encoding": "auto", "truncate": 65536})
        for delivery in deliveries:
            envelope = json.loads(delivery["payload"])
            if envelope.get("payload", {}).get("job_id") != job["id"]:
                continue
            payload = envelope["payload"]
            self.expect(set(payload) == {"job_id", "revision", "organization_id", "division_id", "account_id", "input_hash"}, "Rabbit command contains only scoped identities and hash")
            self.expect(envelope["message_type"] == "ratings.calculate.requested" and envelope["producer"] == "ratings-service" and envelope["schema_version"] == 1, "Exact ratings command envelope")
            self.expect(delivery["exchange"] == "cpm.events" and delivery["routing_key"] == "ratings.calculate.requested.v1" and delivery["properties"]["delivery_mode"] == 2, "Real Rabbit exact persistent ratings route")
            self.events.append({"message_id": envelope["message_id"], "payload": payload, "routing_key": delivery["routing_key"]})
        self.expect(len(self.events) == 2 and {row["payload"]["account_id"] for row in self.events} == {self.student_id, self.other_id}, "Owned temporary binding receives one command for each account")

    def run_ratings(self):
        self.ratings("/ratings?version=invalid", codes=400)
        self.ratings("/ratings?version="+str(uuid.uuid4()), codes=404)
        self.ratings("/jobs", self.student, "POST", self.period, 403, {"Idempotency-Key": uuid.uuid4().hex})
        self.ratings("/jobs", self.viewer, "POST", self.period, 403, {"Idempotency-Key": uuid.uuid4().hex})
        self.start_probe()
        first, key = self.new_job()
        replay = self.ratings("/jobs", method="POST", body=self.period, codes=200, headers={"Idempotency-Key": key})["job"]
        self.expect(replay["id"] == first["id"], "Cookie BFF retries retain the stored asynchronous job")
        self.first_job = self.wait_job(first)
        self.verify_transport(self.first_job)
        listing = self.ratings("/ratings")
        self.assert_formula(listing, self.expected_first, [self.student_id, self.other_id])
        result = next(row for row in listing["ratings"] if row["profile"]["account_id"] == self.student_id)["rating"]
        self.expect(result["exams"]["details"][0]["score"] == 4 and result["exams"]["details"][0]["attempt_no"] == 1 and result["exams"]["details"][0]["has_appeal"], "Pending retake keeps effective first appeal grade rather than maximum calculated grade")
        self.expect(all(row["exam_name"].startswith(self.prefix) for row in result["exams"]["details"]), "Real owner exam names survive source adapter and calculate output")
        self.expect(any(row["status"] == "Сдано" and row["score"] == 0 for row in result["homework"]["details"]), "Completed zero homework remains completed in rating details")
        self.completed_a.append({"id": first["id"], "revision": first["revision"], "status": "completed", "expected_components": self.expected_first})
        self.retake_attempt = self.conduct(self.retake_assignment, self.examiner2, 0)
        stale = self.ratings("/ratings")
        self.expect(stale["freshness"]["is_stale"] and stale["freshness"]["reason"] == "source_changed", "Completed worse retake invalidates prior publication without replacing it early")
        self.assert_formula(stale, self.expected_first, [self.student_id, self.other_id])
        latest, _ = self.new_job(token=self.editor)
        self.latest = self.wait_job(latest)
        self.assert_formula(self.ratings("/ratings"), self.expected_a, [self.student_id, self.other_id])
        self.completed_a.append({"id": latest["id"], "revision": latest["revision"], "status": "completed", "expected_components": self.expected_a})
        self.assert_formula(self.ratings("/ratings?version="+first["id"]), self.expected_first, [self.student_id, self.other_id])
        self.job_b, _ = self.new_job(division=self.secondary)
        self.wait_job(self.job_b, self.secondary)
        self.assert_formula(self.ratings("/ratings", division=self.secondary), self.expected_b, [self.student_id])
        self.assert_formula(self.ratings("/ratings"), self.expected_a, [self.student_id, self.other_id])
        self.ratings("/ratings?version="+self.job_b["id"], codes=404)
        self.permissions()
        self.cancel_and_retry()
        self.persist()

    def permissions(self):
        own = self.ratings("/me", self.student)["rating"]
        self.expect(own["profile"]["account_id"] == self.student_id and own["rating"]["final_rating"] == self.expected_a["final"], "Student reads only its own current division result")
        self.ratings("/accounts/"+str(self.other_id), self.student, codes=404)
        self.expect(self.ratings("/ratings", self.student)["total"] == 1, "Student list total excludes other accounts")
        proctor = self.ratings("/ratings", self.proctor)
        self.expect(proctor["total"] == 1 and proctor["ratings"][0]["profile"]["account_id"] == self.student_id, "Proctor gets only its live group")
        self.ratings("/accounts/"+str(self.other_id), self.proctor, codes=404)
        for token in (self.student, self.proctor):
            self.ratings("/jobs", token, codes=403)
            self.ratings("/report", token, codes=403)
        self.expect(self.ratings("/ratings", self.viewer)["total"] == 2, "Scoped view permission reads its entire selected division")
        self.ratings("/ratings", self.viewer, division=self.secondary, codes=(403, 404))
        self.expect(len(self.ratings("/report", self.supervisor)["students"]) == 2, "Supervisor can read complete division report")
        self.ratings("/jobs", self.supervisor, "POST", self.period, 403, {"Idempotency-Key": uuid.uuid4().hex})
        searched = self.ratings("/ratings?q="+urllib.parse.quote(self.prefix+" student2"))
        self.expect(searched["total"] == 1 and searched["ratings"][0]["place"] == 2, "Search preserves the unfiltered global rank")
        self.ratings("/jobs/"+self.first_job["id"], self.foreign, codes=(403, 404))
        session = self.browser[self.admin]
        self.bff(session, "/bff/ratings/ratings?organization_id=2001", codes=400)
        self.bff(session, "/bff/internal/ratings/jobs/"+self.first_job["id"]+"/accounts/"+str(self.student_id)+"/input", codes=501)
        self.core(f"/divisions/{self.division}/memberships/{self.student_id}", method="DELETE", codes=409)
        self.core(f"/divisions/{self.division}/memberships/{self.other_id}", method="DELETE", codes=409)
        self.expect(True, "Published nonzero and zero ratings permanently preserve core participation")

    def cancel_and_retry(self):
        job, _ = self.new_job()
        cancelled = self.ratings('/jobs/'+job['id']+'/cancel', method='POST', body={})
        self.expect(cancelled["status"] == "cancelled", "Own new uncompleted job can be cancelled through BFF")
        self.expect(self.ratings('/ratings')["freshness"]["version_id"] == self.latest["id"], "Cancellation preserves the previous published version")
        retried = self.ratings('/jobs/'+job['id']+'/retry', self.editor, 'POST', {}, 202, {'Idempotency-Key': uuid.uuid4().hex})
        self.expect(retried['id'] != job['id'] and retried['revision'] > job['revision'], "Explicit retry creates a new revision and immutable job identity")
        self.latest = self.wait_job(retried)
        self.assert_formula(self.ratings('/ratings'), self.expected_a, [self.student_id, self.other_id])
        self.completed_a.append({"id": retried["id"], "revision": retried["revision"], "status": "completed", "expected_components": self.expected_a})
        self.ratings('/jobs/'+retried['id']+'/cancel', method='POST', body={}, codes=409)
        self.ratings('/jobs/'+retried['id']+'/retry', method='POST', body={}, codes=409, headers={'Idempotency-Key': uuid.uuid4().hex})
        self.expect(len(self.ratings('/versions')['versions']) == 3, "Versions contain completed publications and exclude cancelled jobs")

    def finish(self, error=None):
        if self.rabbit_queue:
            self.rabbit('/api/queues/cpm_dev/'+self.rabbit_queue, 'DELETE', codes=(204,))
        for session in self.browser.values():
            try:
                self.bff(session, '/bff/auth/logout', 'POST', {}, record=False)
            except Exception:
                pass
        for token in self.sessions:
            try:
                super().request(owner.AUTH, '/v1/auth/logout', token, 'POST', codes=(200, 401), record=False)
            except Exception:
                pass
        self.persist()
        report = {"status": "failed" if error else "passed", "checks": len(self.checks), "check_names": self.checks,
                  "run": self.run, "division_a": self.division, "division_b": self.secondary, "job_progress": self.progress,
                  "events": self.events, "calculate_inbox": self.inbox, "expected_first_a": self.expected_first,
                  "expected_a": self.expected_a, "expected_b": self.expected_b, "direct_database_writes": 0,
                  "rabbit_queue_removed": bool(self.rabbit_queue), "external_tests_mode": "dev_internal_only", "full_external_tests_verified": False,
                  **({"error": str(error)} if error else {})}
        owner.private_json(self.report_path, report)
        print(json.dumps({"status": report["status"], "checks": len(self.checks), "run": self.run, "division_a": self.division, "division_b": self.secondary}))


def main():
    suite, error = Suite(), None
    try:
        suite.setup_inputs()
        suite.run_ratings()
    except Exception as failure:
        error = failure
    finally:
        suite.finish(error)
    if error:
        raise SystemExit("BFF ratings checks failed; see the non-secret local report")


if __name__ == "__main__":
    main()
