"""Additional local two-organization, live-auth and paged-import regression.

Uses only synthetic fixtures in seeded cpm_dev organizations 1001 and 2001.
Run after the exams owner explicitly confirms the current runtime is ready.
Tokens, workbook contents and profile data never enter the public report.
"""
from __future__ import annotations

import argparse
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timedelta, timezone
import http.cookiejar
import json
import os
import time
import traceback
import urllib.error
import urllib.request
from urllib.parse import urlencode
import uuid

from http_test import ROOT, Regression, local_url
from bootstrap import mysql_connection


class BrowserSession:
    """Isolated BFF cookie jar; no existing browser/user sessions are touched."""

    def __init__(self, owner, login, division, organization=1001):
        self.owner = owner
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        self.revision = None
        data = self.call("/auth/login", "POST", {"login": login, "password": owner.password, "organization_id": organization, "division_id": division})
        self.revision = data["context_revision"]

    def call(self, path, method="GET", body=None, code=200, revision=None, key=None):
        headers = {"X-CPM-Request": "1", "Origin": self.owner.args.browser_origin}
        if self.revision:
            headers["X-CPM-Context"] = revision or self.revision
        if method not in {"GET", "HEAD"}:
            headers["Content-Type"] = "application/json"
            headers["Idempotency-Key"] = key or str(uuid.uuid4())
        request = urllib.request.Request(self.owner.args.bff_url + "/bff" + path, method=method, data=None if body is None else json.dumps(body).encode(), headers=headers)
        self.owner.last_request = "BFF " + method + " " + path.split("?")[0]
        try:
            with self.opener.open(request, timeout=90) as response:
                status, raw = response.status, response.read()
        except urllib.error.HTTPError as error:
            status, raw = error.code, error.read()
        allowed = {code} if isinstance(code, int) else set(code)
        self.owner.expect(status in allowed, f"BFF {method} {path.split('?')[0]} expected {sorted(allowed)}, got {status}")
        return json.loads(raw)

    def close(self):
        try:
            self.call("/auth/logout", "POST", {}, code={200, 401})
        except Exception:
            pass


class AuditRegression(Regression):
    def __init__(self, args):
        super().__init__(args)
        self.foreign_divisions = []
        self.foreign_exams = []
        self.browsers = []
        self.phases = {}
        self.catalog_directions = []

    def phase(self, name, operation):
        before = len(self.checks)
        started = time.monotonic()
        operation()
        self.phases[name] = {"checks": len(self.checks) - before, "seconds": round(time.monotonic() - started, 3)}

    def foreign_fixtures(self):
        for name in ["F", "G"]:
            division = self.core("/divisions", self.foreign_admin, "POST", {"name": "DEV Exams " + self.run + " " + name}, code=201)["id"]
            self.foreign_divisions.append(division)
        self.f, self.g = self.foreign_divisions
        self.foreign_f, _ = self.login("dev_admin_math", self.f, 2001)
        self.foreign_g, _ = self.login("dev_admin_math", self.g, 2001)
        directions = self.core("/directions", self.foreign_f)
        active = [row["id"] for row in directions if row.get("is_active", True)]
        self.expect(bool(active), "seeded foreign organization has an active direction")
        self.foreign_direction = active[0]
        self.foreign_student_login = self.prefix + "_foreign_student"
        self.foreign_student_id = self.core(f"/divisions/{self.f}/users", self.foreign_f, "POST", {"role": "student", "full_name": "DEV Exams foreign " + self.run, "login": self.foreign_student_login, "password": self.password, "class": 10}, code=201)["account_id"]
        self.users.append(self.foreign_student_id)
        self.core(f"/divisions/{self.g}/memberships/{self.foreign_student_id}", self.foreign_f, "PUT", {})
        self.foreign_student, _ = self.login(self.foreign_student_login, self.f, 2001)
        exam = self.api("", self.foreign_f, "POST", {"exam_type": "outside_lms", "direction_id": self.foreign_direction, "date": "2026-10-09", "name": "DEV foreign " + self.run}, code=201)
        self.foreign_exam = exam["id"]
        self.foreign_exams.append(self.foreign_exam)
        self.api(f"/{self.foreign_exam}/outside-results", self.foreign_f, "POST", {"student_account_id": self.foreign_student_id, "points": 7.5, "grade": 0, "examinator": "DEV foreign"})

    def cleanup(self):
        for browser in self.browsers:
            browser.close()
        # Foreign documents use separate tracked IDs and an explicit org/name
        # guard; the base cleanup then removes exact prefixed accounts in both
        # organizations before deleting org1001 divisions.
        conn = mysql_connection(self.dev)
        cursor = conn.cursor()
        try:
            for exam in self.foreign_exams:
                cursor.execute("SELECT owner_division_id FROM exams_catalog_v1 WHERE id=%s AND organization_id=2001", (exam,))
                row = cursor.fetchone()
                if row and row[0] not in self.foreign_divisions:
                    raise AssertionError("foreign exam cleanup fixture scope guard")
                cursor.execute("DELETE FROM exams_catalog_v1 WHERE id=%s AND organization_id=2001", (exam,))
            conn.commit()
        finally:
            cursor.close()
            conn.close()
        super().cleanup()
        conn = mysql_connection(self.dev)
        cursor = conn.cursor()
        try:
            for direction, name in self.catalog_directions:
                cursor.execute("SELECT d.id FROM directions d JOIN core_direction_scope s ON s.direction_id=d.id WHERE d.id=%s AND s.organization_id=1001 AND d.name=%s", (direction, name))
                if cursor.fetchone() is None:
                    raise AssertionError("catalogue direction cleanup fixture scope guard")
                cursor.execute("DELETE FROM core_direction_scope WHERE direction_id=%s AND organization_id=1001", (direction,))
                cursor.execute("DELETE FROM directions WHERE id=%s AND name=%s", (direction, name))
            for division in self.foreign_divisions:
                cursor.execute("SELECT id FROM divisions WHERE id=%s AND organization_id=2001 AND name LIKE %s", (division, "DEV Exams " + self.run + "%"))
                if cursor.fetchone() is None:
                    raise AssertionError("foreign division cleanup fixture scope guard")
                cursor.execute("DELETE t FROM auth_refresh_tokens t JOIN auth_sessions s ON s.id=t.session_id WHERE s.division_id=%s", (division,))
                cursor.execute("DELETE FROM auth_sessions WHERE division_id=%s", (division,))
                for table in ["exams_receipts_v1", "exams_source_v1", "exams_outbox_v1"]:
                    cursor.execute("DELETE FROM " + table + " WHERE organization_id=2001 AND division_id=%s", (division,))
                cursor.execute("DELETE FROM divisions WHERE id=%s AND organization_id=2001 AND name LIKE %s", (division, "DEV Exams " + self.run + "%"))
            conn.commit()
        finally:
            cursor.close()
            conn.close()

    def catalogue_privacy_and_sort(self):
        marker = self.prefix + "_catalog"
        direction_names = {}
        for label in ["ZZZ", "AAA", "MMM"]:
            name = label + " DEV Catalog " + self.run
            direction = self.core("/directions", self.admin_a, "POST", {"name": name}, code=201)["id"]
            self.catalog_directions.append((direction, name))
            direction_names[direction] = name
        zeta, alpha, archived = [direction for direction, _ in self.catalog_directions]
        self.expect(zeta < alpha, "fixture direction ID order differs from name order")
        created = []
        for direction, name in [(zeta, "Z " + marker), (alpha, "Z " + marker), (alpha, "A " + marker), (zeta, "A " + marker), (archived, "MMM " + marker + " 2"), (archived, "MMM " + marker + " 1")]:
            exam = self.api("", self.admin_a, "POST", {"exam_type": "classic", "direction_id": direction, "name": name}, code=201)
            self.exams.append(exam["id"])
            created.append({"id": exam["id"], "direction": direction, "name": name})
            path = f"/{exam['id']}"
            commission = self.api(path + "/commissions", self.admin_a, "POST", {"examinator_ids": [self.examiners[0][0], self.examiners[1][0]]})["commission"]
            self.api(path + "/assignments", self.admin_a, "POST", {"student_account_id": self.students[0][0], "commission_id": commission["id"]})
        # Archived direction is absent from active Core names; catalogue must
        # use the exam name fallback instead of numeric direction identity.
        self.core("/directions/" + str(archived), self.admin_a, "DELETE")
        direction_names.pop(archived)
        expected = [row["id"] for row in sorted(created, key=lambda row: (direction_names.get(row["direction"], row["name"]).lower(), row["id"]))]
        hidden = {"connected_division_ids", "owner_division_id", "origin_exam_id", "in_bank"}
        for actor, token in [("administrator", self.admin_a), ("student", self.student_a), ("examiner", self.examiner_tokens[0])]:
            ids = []
            for offset in [0, 2, 4]:
                page = self.api("?" + urlencode({"q": marker, "sort": "direction_asc", "scope": "division", "limit": 2, "offset": offset}), token)
                self.expect(page["total"] == 6 and len(page["items"]) == 2, actor + " catalogue sorts before bounded pagination")
                for row in page["items"]:
                    self.expect((hidden <= row.keys()) if actor == "administrator" else hidden.isdisjoint(row), actor + " catalogue respects administrative bank metadata privacy")
                    self.expect({"id", "name", "direction_id", "exam_type", "exam_date", "connected"} <= row.keys(), actor + " catalogue retains public screen fields")
                ids.extend(row["id"] for row in page["items"])
            self.expect(ids == expected, actor + " direction name order uses stable exam IDs and name fallback across pages")
        full = self.api("?" + urlencode({"q": marker, "sort": "direction_asc", "limit": 200}), self.admin_a)
        self.expect([row["id"] for row in full["items"]] == expected, "unpaged catalogue agrees with concatenated sorted pages")

    def new_classic(self, name, start=None):
        created = self.api("", self.admin_a, "POST", {"exam_type": "classic", "direction_id": 1001, "name": "DEV " + name + " " + self.run}, code=201)
        self.exams.append(created["id"])
        path = f"/{created['id']}"
        part = self.api(path + "/parts", self.admin_a, "POST", {"code": "A", "weight": 5, "quota": 1})["part"]
        question = self.api(path + "/questions", self.admin_a, "POST", {"part_id": part["id"], "question_text": "DEV audit question " + self.run, "answer_text": "DEV audit answer"})["question"]
        now = datetime.now(timezone.utc)
        self.api(path + "/config", self.admin_a, "PUT", {"expected_revision": self.api(path + "/config", self.admin_a)["config"]["revision"], "start_at": (start or now - timedelta(hours=1)).isoformat(), "end_at": (now + timedelta(days=30)).isoformat(), "fractional_mode": "round_down", "thresholds": [{"grade": n, "min_score": n} for n in range(6)]})
        return created["id"], part, question

    def catalog_ids(self, token, query=None):
        values, offset = set(), 0
        while True:
            page = self.api("?" + urlencode({**(query or {}), "limit": 200, "offset": offset}), token)
            values.update(row["id"] for row in page["items"])
            offset += len(page["items"])
            if offset >= page["total"]:
                return values
            self.expect(bool(page["items"]), "catalogue pagination makes progress")

    def source_delta(self, before, after, affected, name):
        for division in [self.a, self.b]:
            expected = before["source_revisions"][str(division)] + (1 if division in affected else 0)
            self.expect(after["source_revisions"][str(division)] == expected, name + " source revision follows exact division scope")
        if not affected:
            self.expect(before["snapshot"]["source_revision"] == after["snapshot"]["source_revision"], name + " preserves semantic snapshot source hash")

    def isolation_and_history(self):
        exam, part, question = self.new_classic("audit history")
        self.audit_exam, self.audit_part = exam, part
        path = f"/{exam}"
        commission = self.api(path + "/commissions", self.admin_a, "POST", {"examinator_ids": [self.examiners[0][0], self.examiners[1][0]]})["commission"]
        assignment = self.api(path + "/assignments", self.admin_a, "POST", {"student_account_id": self.students[0][0], "commission_id": commission["id"]})["assignment"]
        before = self.snapshot_data()
        self.api(path + "/questions", self.admin_a, "POST", {"part_id": part["id"], "question_text": "DEV spare replacement " + self.run, "answer_text": "DEV spare"})
        self.source_delta(before, self.snapshot_data(), [], "question bank content update")
        self.api(path + f"/privileges/{self.students[0][0]}", self.admin_a, "PUT", {"expected_revision": 0, "replacement_limit": 1})
        self.source_delta(before, self.snapshot_data(), [], "personal replacement privilege")
        self.api(path + "/assignments", self.admin_a, "POST", {"student_account_id": self.foreign_student_id, "commission_id": commission["id"]}, code={400, 404})
        prepared = self.api(path + f"/assignments/{assignment['id']}/attempts/ensure", self.examiner_tokens[0], "POST", {"expected_history_generation": 1})["attempt"]
        apath = path + f"/attempts/{prepared['id']}"
        for token in self.examiner_tokens[:2]:
            self.api(apath + "/ready", token, "POST", {})
        started = self.api(apath + "/start", self.examiner_tokens[0], "POST", {"expected_state_version": self.api(apath, self.examiner_tokens[0])["state_version"]})["attempt"]
        self.vote(apath, 0, 1)
        self.source_delta(before, self.snapshot_data(), [], "unfinished prepare/start/vote")
        self.vote(apath, 1, 1)
        if self.api(apath, self.examiner_tokens[0])["status"] != "completed":
            self.api(apath + "/next-question", self.examiner_tokens[0], "POST", {"presented_question_id": started["current_question"]["id"], "expected_state_version": self.api(apath, self.examiner_tokens[0])["state_version"]})
        completed_source = self.snapshot_data()
        self.source_delta(before, completed_source, [self.a], "completed current result")
        result = self.api(path + f"/results/{self.students[0][0]}", self.admin_a)
        current = result["current"]
        self.expect(current["effective_grade"] == 5, "completed current result applies configured weight and thresholds")
        self.api(apath + "/appeals", self.admin_a, "POST", {"grade": 4, "expected_result_version": current["result_version"]})
        appeal_source = self.snapshot_data()
        self.source_delta(completed_source, appeal_source, [self.a], "effective current appeal")
        next_commission = self.api(path + "/commissions", self.admin_a, "POST", {"examinator_ids": [self.examiners[0][0], self.examiners[2][0]]})["commission"]
        self.api(path + f"/retakes/{self.students[0][0]}", self.admin_a, "POST", {"commission_id": next_commission["id"], "expected_history_generation": 1})
        pending_source = self.snapshot_data()
        self.source_delta(appeal_source, pending_source, [], "pending retake assignment")
        self.expect([row for row in pending_source["snapshot"]["results"] if row["exam_id"] == exam] == [row for row in appeal_source["snapshot"]["results"] if row["exam_id"] == exam], "pending retake preserves effective rating result rows")
        own = self.api(path + f"/results/{self.students[0][0]}", self.student_a)
        self.expect(own["current"]["effective_grade"] == 4 and own["current"]["has_appeal"] and own["current"]["id"] == current["id"], "student current DTO keeps published appeal result during an unfinished retake")
        self.expect(own["current"]["has_active_retake"] and own["current"]["retake_status"] == "not_started", "student safe DTO identifies pending retake without replacing published current result")
        self.expect("calculated_grade" not in own["current"] and "result_version" not in own["current"], "student current DTO omits examiner/admin score and CAS version")
        for tail in ["", "/parts", "/questions", "/assignments", "/attempts", "/results", f"/results/{self.students[0][0]}", f"/attempts/{prepared['id']}/protocol", "/delete-preview", "/training", "/training?preview=true"]:
            self.api(path + tail, self.foreign_f, code=404)
        for tail, body in [("/metadata", {"name": "DEV forbidden", "expected_revision": self.api(path, self.admin_a)["revision"]}), (f"/outside-results/{self.foreign_student_id}", {"expected_revision": 1, "grade": 5}), (f"/training/progress/{question['id']}", {"content_fingerprint": "0" * 64})]:
            self.api(path + tail, self.foreign_f, "PUT", body, code=404)
        self.api(path + "/copy", self.admin_a, "POST", {"division_id": self.f, "expected_revision": self.api(path, self.admin_a)["revision"]}, code=404)
        self.api(path + "/connections", self.admin_a, "POST", {"division_id": self.f, "expected_revision": self.api(path, self.admin_a)["revision"]}, code=404)
        self.api(f"/{self.foreign_exam}", self.admin_a, code=404)
        foreign = self.api(f"/{self.foreign_exam}/results/{self.foreign_student_id}", self.foreign_student)
        self.expect(foreign["grade"] == 0 and foreign["points"] == 7.5, "foreign organization retains its own explicit zero result")
        # Every catalogue scope remains organization bounded, including full admin.
        for token, forbidden in [(self.admin_a, self.foreign_exam), (self.foreign_f, exam)]:
            for scope in ["division", "bank", "organization"]:
                ids = self.catalog_ids(token, {"scope": scope})
                self.expect(forbidden not in ids, "catalogue " + scope + " does not disclose foreign organization")
        self.api("?scope=organization", self.scoped_staff, code=403)
        self.api(path + "/training", self.admin_a, "PUT", {"expected_revision": self.api(path, self.admin_a)["division_revision"], "training_enabled": True})
        card = self.api(path + "/training", self.student_a)["cards"][0]
        self.api(path + f"/training/progress/{card['question_id']}", self.student_a, "PUT", {"content_fingerprint": card["content_fingerprint"]})
        self.api(path + "/connections", self.admin_a, "POST", {"division_id": self.b, "expected_revision": self.api(path, self.admin_a)["revision"]})
        self.api(path + "/training", self.admin_b, "PUT", {"expected_revision": self.api(path, self.admin_b)["division_revision"], "training_enabled": True})
        self.expect(self.api(path + "/training", self.student_b)["stats"]["learned"] == 0, "shared exam training progress is isolated by division")
        self.expect(self.api(path + "/training", self.student_a)["stats"]["learned"] == 1, "other division training does not reset original personal progress")
        shared_source = self.snapshot_data()
        name = "DEV shared renamed " + self.run
        self.api(path + "/metadata", self.admin_a, "PUT", {"name": name, "expected_revision": self.api(path, self.admin_a)["revision"]})
        renamed_source = self.snapshot_data()
        self.source_delta(shared_source, renamed_source, [self.a, self.b], "actual shared exam name update")
        self.api(path + "/metadata", self.admin_a, "PUT", {"name": name, "expected_revision": self.api(path, self.admin_a)["revision"]})
        self.source_delta(renamed_source, self.snapshot_data(), [], "identical shared name no-op")
        config = self.api(path + "/config", self.admin_a)["config"]
        self.api(path + "/config", self.admin_a, "PUT", {"expected_revision": config["revision"], "start_at": config["start_at"], "end_at": config["end_at"]})
        self.source_delta(renamed_source, self.snapshot_data(), [], "identical selected division dates no-op")
        self.api(path + "/config", self.admin_a, "PUT", {"expected_revision": config["revision"], "start_at": "2026-10-07T21:00:00Z"})
        self.source_delta(renamed_source, self.snapshot_data(), [self.a], "actual selected division date update")
        dated = self.api(path + f"/results/{self.students[0][0]}", self.student_a)["current"]
        self.expect(dated["exam_date"] == "2026-10-08" and dated["exam_window_snapshot"] == own["current"]["exam_window_snapshot"], "result date follows current division settings while historical attempt window remains immutable")
        matching = self.api(path + "/results?date_from=2026-10-08&date_to=2026-10-08&limit=1", self.student_a)
        self.expect(matching["total"] == 1 and matching["items"][0]["effective_grade"] == 4, "result date filter is inclusive and applied before pagination")
        self.expect(self.api(path + "/results?from_date=2026-10-09&to_date=2026-10-09", self.student_a)["total"] == 0, "result legacy date aliases exclude other current dates")
        self.api(path + "/results?date_from=2026-10-10&date_to=2026-10-09", self.student_a, code=400)

    def download_template(self, exam, kind, token, code=200):
        self.last_request = f"GET /v1/exams/{exam}/imports/{kind}/template"
        headers = {"Authorization": "Bearer " + token} if token else {}
        request = urllib.request.Request(self.args.exams_url + f"/v1/exams/{exam}/imports/{kind}/template", headers=headers)
        try:
            with urllib.request.urlopen(request, timeout=90) as response:
                status, raw, content_type = response.status, response.read(), response.headers.get("Content-Type", "")
        except urllib.error.HTTPError as error:
            status, raw, content_type = error.code, error.read(), error.headers.get("Content-Type", "")
        self.expect(status == code, f"template {kind} expected {code}, got {status}")
        if code == 200:
            self.expect(raw.startswith(b"PK") and "spreadsheetml" in content_type, "native import template is a real XLSX document")

    def paged_import(self):
        exam, _, _ = self.new_classic("audit import")
        self.import_exam = exam
        for kind in ["questions", "assignments"]:
            self.download_template(exam, kind, self.admin_a)
            self.download_template(exam, kind, None, code=401)
            self.download_template(exam, kind, self.foreign_f, code=404)
        self.download_template(self.foreign_exam, "outside", self.foreign_f)
        rows = [["part_code", "question_text", "answer_text"]]
        rows.extend(["A", f"DEV {self.run} bulk {n}", f"DEV bulk answer {n}"] for n in range(self.args.import_rows))
        rows[-1][0] = "INVALID"
        first = self.upload(exam, "questions", rows)
        self.expect(first["total"] == self.args.import_rows and len(first["rows"]) <= 200 and first["has_more"], "large import upload returns bounded preview with whole-file count")
        self.expect(len(first["source_hash"]) == 64 and len(first["validation_hash"]) == 64, "import preview exposes stable source and validation hashes")
        path = f"/{exam}/imports/questions/{first['id']}"
        self.api(path, self.foreign_f, code=404)
        self.api(path + "/commit", self.foreign_f, "POST", {"expected_revision": first["revision"]}, code=404)
        collected, source_rows, offset = [], set(), 0
        while offset < first["total"]:
            page = self.api(path + "?" + urlencode({"limit": 200, "offset": offset, "expected_revision": first["revision"]}), self.admin_a)
            self.expect(page["revision"] == first["revision"] and page["source_hash"] == first["source_hash"] and page["total"] == first["total"], "all preview pages share source identity and revision")
            self.expect(len(page["rows"]) <= 200 and bool(page["rows"]), "preview page is bounded and progresses")
            for row in page["rows"]:
                self.expect("privilege_baseline_version" not in row and "privilege_account_id" not in row, "preview row does not leak hidden commit baseline")
                self.expect(row["source_row"] not in source_rows, "preview keeps unique original workbook row numbers")
                source_rows.add(row["source_row"])
            collected.extend(page["rows"])
            offset += len(page["rows"])
            self.expect(page["has_more"] == (offset < first["total"]), "preview has_more agrees with full-file count")
        self.expect(len({row["row_id"] for row in collected}) == self.args.import_rows, "complete preview recovers all rows beyond the first 200")
        invalid = collected[-1]
        self.expect(bool(invalid["errors"]) and first["summary"]["error_rows"] == 1, "whole-file summary includes invalid row beyond first preview page")
        self.api(path + "/commit", self.admin_a, "POST", {"expected_revision": first["revision"]}, code=409)
        self.expect(self.api(f"/{exam}/questions", self.admin_a)["total"] == 1, "failed large import remains atomic with no partial rows")
        revised = self.api(path, self.admin_a, "PUT", {"expected_revision": first["revision"], "changes": [{"row_id": invalid["row_id"], "excluded": True}]})
        self.expect(revised["source_hash"] == first["source_hash"] and revised["validation_hash"] != first["validation_hash"], "row edit preserves workbook identity and advances validation hash")
        self.api(path + "?expected_revision=" + str(first["revision"]), self.admin_a, code=409)
        self.api(path, self.admin_a, "PUT", {"expected_revision": first["revision"], "changes": []}, code=409)
        # A reconstructed client with only session ID/revision can recover pages.
        recovered = self.api(path + "?" + urlencode({"limit": 200, "offset": self.args.import_rows - 1, "expected_revision": revised["revision"]}), self.admin_a)
        self.expect(recovered["rows"][0]["row_id"] == invalid["row_id"] and recovered["rows"][0]["excluded"], "new client recovers edited last page from saved session")
        key, body = str(uuid.uuid4()), {"expected_revision": revised["revision"]}
        saved = self.api(path + "/commit", self.admin_a, "POST", body, key=key)
        self.expect(self.api(path + "/commit", self.admin_a, "POST", body, key=key) == saved, "lost commit reply replay returns exact saved import outcome")
        self.expect(self.api(path + "/commit", self.admin_a, "POST", body) == saved, "new command key for committed session returns saved outcome without second apply")
        self.expect(self.api(f"/{exam}/questions", self.admin_a)["total"] == self.args.import_rows, "large import applies included rows once and preserves pre-existing question")
        self.expect(self.api(path, self.admin_a)["state"] == "committed", "committed import session remains recoverable")

    def auth_and_bff(self):
        path = f"/{self.import_exam}"
        staff_login = self.prefix + "_staff"
        browser = BrowserSession(self, staff_login, self.a)
        admin_browser = BrowserSession(self, "dev_admin", self.a)
        foreign_browser = BrowserSession(self, "dev_admin_math", self.f, 2001)
        self.browsers.extend([browser, admin_browser, foreign_browser])
        browser.call("/exams" + path)
        browser.call("/exams/" + str(self.foreign_exam), code=404)
        foreign_browser.call("/exams" + path, code=404)
        foreign_browser.call("/exams" + path + "/results", code=404)
        old_revision = admin_browser.revision
        switched = admin_browser.call("/auth/context", "POST", {"organization_id": 1001, "division_id": self.b})
        admin_browser.revision = switched["context_revision"]
        self.expect(admin_browser.revision != old_revision, "BFF context switch rotates browser context revision")
        admin_browser.call("/exams", revision=old_revision, code=409)
        browser.call("/auth/context", "POST", {"organization_id": 1001, "division_id": self.b}, code={403, 404})
        self.api(path + "/parts", self.scoped_staff)
        key = str(uuid.uuid4())
        original = self.api(path, self.scoped_staff)
        body = {"name": "DEV scoped update " + self.run, "expected_revision": original["revision"]}
        self.api(path + "/metadata", self.scoped_staff, "PUT", body, key=key)
        # Update only this run's role. Existing bearer and BFF cookie must see it
        # immediately; persisted receipts cannot bypass current permissions.
        role_path = "/roles/" + str(self.roles[0])
        role_name = "DEV Exams scoped " + self.run
        self.core(role_path, self.admin_a, "PUT", {"name": role_name, "permissions": {"exams": {"view": True, "edit": False}, "upload": {"view": True, "edit": False}}})
        self.api(path, self.scoped_staff)
        self.api(path + "/metadata", self.scoped_staff, "PUT", body, key=key, code=403)
        browser.call("/exams" + path + "/metadata", "PUT", body, code=403)
        capabilities = browser.call("/exams/capabilities")["data"]
        self.expect(not capabilities["canCreateClassic"] and not capabilities["canImportExams"], "BFF capabilities reflect live permission edit revoke")
        self.core(role_path, self.admin_a, "PUT", {"name": role_name, "permissions": {}})
        self.api(path, self.scoped_staff, code=403)
        browser.call("/exams" + path, code=403)
        self.download_template(self.import_exam, "questions", self.scoped_staff, code=403)
        self.core(role_path, self.admin_a, "PUT", {"name": role_name, "permissions": {"exams": {"view": True, "edit": True}, "upload": {"view": True, "edit": True}}})
        self.api(path, self.scoped_staff)
        browser.call("/exams" + path)
        # A spare student has two memberships and no exam history, so only this
        # synthetic selected-division membership is revoked through Core.
        account, login = self.create_user("student", shared=True)
        student, _ = self.login(login, self.b)
        self.api("", student)
        self.core(f"/divisions/{self.b}/memberships/{account}", self.admin_a, "DELETE")
        self.api("", student, code={401, 403})

    def dates_and_snapshot(self):
        early, _, _ = self.new_classic("Moscow early", datetime(2026, 10, 8, 20, 59, 59, tzinfo=timezone.utc))
        midnight, _, _ = self.new_classic("Moscow midnight", datetime(2026, 10, 8, 21, 0, tzinfo=timezone.utc))
        for date, selected, excluded in [("2026-10-08", early, midnight), ("2026-10-09", midnight, early)]:
            ids = self.catalog_ids(self.admin_a, {"date_from": date, "date_to": date})
            self.expect(selected in ids and excluded not in ids, "catalogue inclusive date filters use Moscow midnight boundary " + date)
            aliases = self.catalog_ids(self.admin_a, {"from_date": date, "to_date": date})
            self.expect(ids == aliases, "legacy date aliases match canonical catalogue filtering")
        for query in ["date_from=2026-02-30", "date_from=2026-10-10&date_to=2026-10-09", "date_from=2026-10-09&from_date=2026-10-08"]:
            self.api("?" + query, self.admin_a, code=400)
        params = {"organization_id": 1001, "division_ids": str(self.a), "date_from": "2026-10-09", "date_to": "2026-10-09"}
        def capture(as_of):
            return self.request(self.args.exams_url, "/internal/exams/rating-snapshot?" + urlencode({**params, "as_of": as_of}), headers={"X-Service-Key": self.internal_key})["data"]["snapshot"]
        before = capture("2026-10-08T20:59:59.999999999Z")
        exact = capture("2026-10-08T21:00:00Z")
        self.expect(midnight not in {row["exam_id"] for row in before["exams"]} and before["next_exam_start_at"] == "2026-10-08T21:00:00Z", "rating as_of excludes future start and reports next invalidation instant")
        self.expect(midnight in {row["exam_id"] for row in exact["exams"]}, "rating as_of includes exact start instant")
        self.request(self.args.exams_url, "/internal/exams/rating-snapshot?" + urlencode({**params, "division_ids": str(self.f)}), headers={"X-Service-Key": self.internal_key}, code=404)

    def source_events(self):
        conn = mysql_connection(self.dev)
        cursor = conn.cursor()
        self.event_ids, scopes = [], []
        try:
            for organization, divisions, exam_ids in [(1001, self.divisions, self.exams), (2001, self.foreign_divisions, self.foreign_exams)]:
                for division in divisions:
                    cursor.execute("SELECT id FROM divisions WHERE id=%s AND organization_id=%s AND name LIKE %s", (division, organization, "DEV Exams " + self.run + "%"))
                    self.expect(cursor.fetchone() is not None, "outbox inspection is restricted to this run's synthetic division")
                    query = "SELECT id,source_revision,published_at,JSON_EXTRACT(envelope,'$.payload.exam_id'),JSON_EXTRACT(envelope,'$.payload.organization_id'),JSON_EXTRACT(envelope,'$.payload.division_id'),JSON_UNQUOTE(JSON_EXTRACT(envelope,'$.message_type')) FROM exams_outbox_v1 WHERE organization_id=%s AND division_id=%s ORDER BY source_revision"
                    deadline = time.monotonic() + 5
                    while True:
                        conn.commit()  # refresh repeatable-read visibility
                        cursor.execute(query, (organization, division))
                        rows = cursor.fetchall()
                        if all(row[2] is not None for row in rows) or time.monotonic() >= deadline:
                            break
                        time.sleep(0.1)
                    for row in rows:
                        self.expect(int(row[3]) in exam_ids and int(row[4]) == organization and int(row[5]) == division and row[6] == "exams.source.changed", "outbox envelope retains owner exam/org/division scope")
                    self.event_ids.extend(row[0] for row in rows)
                    scopes.append({"organization_id": organization, "division_id": division, "emitted": len(rows), "published": sum(row[2] is not None for row in rows), "last_source_revision": max((row[1] for row in rows), default=0)})
            self.events = {"scopes": scopes, "emitted": len(self.event_ids), "published": sum(scope["published"] for scope in scopes)}
            # Ratings may still be awaiting its own coordinated rebuild. Read
            # available owner evidence without making consumer readiness a
            # prerequisite for Exams HTTP acceptance or deleting Ratings data.
            cursor.execute("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='ratings_source_events_v1'")
            if cursor.fetchone()[0] and self.event_ids:
                placeholders = ",".join(["%s"] * len(self.event_ids))
                deadline = time.monotonic() + 5
                while True:
                    conn.commit()
                    cursor.execute("SELECT COUNT(*) FROM ratings_source_events_v1 WHERE message_id IN (" + placeholders + ")", tuple(self.event_ids))
                    observed = cursor.fetchone()[0]
                    if observed >= self.events["published"] or time.monotonic() >= deadline:
                        break
                    time.sleep(0.1)
                self.events["ratings_observed"] = observed
        finally:
            cursor.close()
            conn.close()

    def concurrent_delete_replay(self):
        for number in range(8):
            exam = self.api("", self.admin_a, "POST", {"exam_type": "outside_lms", "direction_id": 1001, "date": "2026-10-09", "name": f"DEV concurrent delete {self.run} {number}"}, code=201)["id"]
            self.exams.append(exam)
            path = f"/{exam}"
            preview = self.api(path + "/delete-preview", self.admin_a)
            body, key = {"expected_revision": preview["revision"]}, str(uuid.uuid4())
            headers = {"X-Exam-Confirmation": preview["confirmation"]}
            with ThreadPoolExecutor(max_workers=2) as pool:
                responses = list(pool.map(lambda _: self.api(path, self.admin_a, "DELETE", body, key=key, headers=headers), range(2)))
            self.expect(responses[0] == responses[1] and responses[0]["deleted"], "concurrent identical physical DELETE applies once and returns exact receipt to both callers")
            self.api(path, self.admin_a, code=404)
            self.expect(self.api(path, self.admin_a, "DELETE", body, key=key, headers=headers) == responses[0], "DELETE receipt remains replayable after the object is physically absent")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name, port in [("auth", 8002), ("core", 8003), ("exams", 8010), ("bff", 8001)]:
        parser.add_argument("--" + name + "-url", type=local_url, default=os.environ.get(name.upper() + "_URL", f"http://127.0.0.1:{port}"))
    parser.add_argument("--browser-origin", type=local_url, default="http://127.0.0.1:3001")
    parser.add_argument("--import-rows", type=int, default=251, choices=[251, 5000])
    parser.add_argument("--base", action="store_true", help="also run the comprehensive baseline workflow")
    parser.add_argument("--catalog-only", action="store_true", help="only target catalogue privacy, named direction sorting and pagination")
    parser.add_argument("--keep-fixtures", action="store_true")
    args = parser.parse_args()
    for value in [args.auth_url, args.core_url, args.exams_url, args.bff_url, args.browser_origin]:
        local_url(value)
    regression = AuditRegression(args)
    started, failure, cleaned = time.monotonic(), None, False
    try:
        regression.phase("fixtures", regression.fixtures)
        if args.catalog_only:
            regression.phase("catalogue_privacy_sort_paging", regression.catalogue_privacy_and_sort)
        else:
            regression.phase("second_organization", regression.foreign_fixtures)
            if args.base:
                regression.phase("baseline", regression.workflow)
            regression.phase("scope_history_training", regression.isolation_and_history)
            regression.phase("large_import_recovery", regression.paged_import)
            regression.phase("live_auth_bff_context", regression.auth_and_bff)
            regression.phase("moscow_dates_as_of", regression.dates_and_snapshot)
            regression.phase("concurrent_delete_replay", regression.concurrent_delete_replay)
            regression.phase("source_outbox_evidence", regression.source_events)
    except Exception as error:
        failure = error
    finally:
        if not args.keep_fixtures:
            try:
                regression.cleanup()
                cleaned = True
            except Exception:
                if failure is None:
                    failure = AssertionError("synthetic audit cleanup failed")
    report = {"status": "passed" if failure is None else "failed", "mode": "catalog_only" if args.catalog_only else "audit", "checks": len(regression.checks), "synthetic_run": regression.run, "seconds": round(time.monotonic() - started, 3), "phases": regression.phases, "import_rows": args.import_rows, "fixtures_retained": args.keep_fixtures, "cleaned": cleaned}
    if hasattr(regression, "events"):
        report["source_events"] = regression.events
    if failure is not None:
        report["error"] = str(failure) if isinstance(failure, AssertionError) else type(failure).__name__
        frames = traceback.extract_tb(failure.__traceback__)
        if frames:
            report["failure_location"] = {"function": frames[-1].name, "line": frames[-1].lineno}
        report["last_request"] = getattr(regression, "last_request", "setup")
    print(json.dumps(report, ensure_ascii=False))
    for name, data in [("exams-audit-http-report.json", report), ("exams-audit-fixtures.json", {"synthetic_run": regression.run, "division_ids": regression.divisions, "foreign_division_ids": regression.foreign_divisions, "account_ids": regression.users, "exam_ids": regression.exams, "foreign_exam_ids": regression.foreign_exams, "event_ids": getattr(regression, "event_ids", []), "cleaned": cleaned})]:
        path = ROOT / "dev/local" / name
        path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n")
        path.chmod(0o600)
        if name in {"exams-audit-fixtures.json", "exams-audit-http-report.json"}:
            archive = ROOT / "dev/local" / (name.removesuffix(".json") + "-" + regression.run + ".json")
            archive.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n")
            archive.chmod(0o600)
    return 0 if failure is None else 1


if __name__ == "__main__":
    raise SystemExit(main())
