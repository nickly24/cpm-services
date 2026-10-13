"""Uploads public BFF regression on retained, isolated synthetic fixtures.

Run --prepare-only before the uploads owner is ready. The default run reuses
that manifest, holds all credential CSV bytes in memory and writes no databases,
configuration, broker state or unrelated fixtures. Credentials/tokens are never
included in reports. Unknown commands are recovered by their original receipt.
"""
from __future__ import annotations

import argparse
import csv
from datetime import date
import io
import json
from pathlib import Path
import secrets
import time
import urllib.error
import urllib.request
import uuid
import zipfile
from xml.sax.saxutils import escape

from homework_settings_audit_http_test import NoRedirects, Session as AuthSession, request as fixture_request

ROOT = Path(__file__).resolve().parents[3]
MANIFEST = ROOT / "dev/local/bff-uploads-demo.json"
REPORT = ROOT / "dev/local/bff-uploads-http-report.json"
BASE = "http://127.0.0.1:8001"
ORIGIN = "http://127.0.0.1:3001"


def save(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")
    path.chmod(0o600)


def xlsx(rows):
    """Small in-memory standards-compliant inline-string workbook."""
    buf = io.BytesIO()
    ns = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
    xml_rows = []
    for number, cells in enumerate(rows, 1):
        contents = "".join(f'<c r="{chr(65 + index)}{number}" t="inlineStr"><is><t xml:space="preserve">{escape(str(value))}</t></is></c>' for index, value in enumerate(cells))
        xml_rows.append(f'<row r="{number}">{contents}</row>')
    parts = {
        "[Content_Types].xml": '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>',
        "_rels/.rels": '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>',
        "xl/workbook.xml": f'<workbook xmlns="{ns}" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Upload" sheetId="1" r:id="rId1"/></sheets></workbook>',
        "xl/_rels/workbook.xml.rels": '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>',
        "xl/worksheets/sheet1.xml": f'<worksheet xmlns="{ns}"><sheetData>{"".join(xml_rows)}</sheetData></worksheet>',
    }
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as book:
        for name, content in parts.items():
            book.writestr(name, content)
    return buf.getvalue()


def multipart(kind, content, extension, **fields):
    boundary = "uploads_test_" + uuid.uuid4().hex
    pieces = []
    for name, value in {"type": kind, **fields}.items():
        pieces.append(f'--{boundary}\r\nContent-Disposition: form-data; name="{name}"\r\n\r\n{value}\r\n'.encode())
    pieces.append(f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="synthetic.{extension}"\r\nContent-Type: application/octet-stream\r\n\r\n'.encode() + content + b"\r\n")
    pieces.append(f"--{boundary}--\r\n".encode())
    return b"".join(pieces), "multipart/form-data; boundary=" + boundary


def no_secrets(value, password):
    assert password not in json.dumps(value), "Secret appeared in a protected GET response"
    if isinstance(value, dict):
        assert not any("password" in key.lower() or key in {"access_token", "refresh_token", "token", "payload"} for key in value), "Sensitive field appeared in protected response"
        for child in value.values():
            no_secrets(child, password)
    elif isinstance(value, list):
        for child in value:
            no_secrets(child, password)


class Session(AuthSession):
    def call(self, path, method="GET", body=None, expected=200, revision=None, key=None, content_type=None, binary=False):
        assert path.startswith("/bff/") or path == "/openapi.json"
        assert method == "GET" or path.startswith(("/bff/auth/", "/bff/uploads/", "/bff/cards/", "/bff/exams"))
        assert method != "DELETE", "Destructive calls are forbidden"
        headers = {"Origin": ORIGIN, "X-CPM-Request": "1", "X-CPM-Context": self.revision if revision is None else revision}
        if key:
            headers["Idempotency-Key"] = key
        raw = body if isinstance(body, bytes) else json.dumps(body).encode() if body is not None else None
        if raw is not None:
            headers["Content-Type"] = content_type or "application/json"
        req = urllib.request.Request(BASE + path, method=method, data=raw, headers=headers)
        try:
            with self.http.open(req, timeout=100) as response:
                status, content = response.status, response.read(64 * 1024 * 1024 + 1)
        except urllib.error.HTTPError as response:
            status, content = response.code, response.read(64 * 1024 * 1024 + 1)
        allowed = {expected} if isinstance(expected, int) else set(expected)
        payload = None if binary and status == 200 else json.loads(content)
        assert status in allowed, f"{method} {path.split('?')[0]} expected {sorted(allowed)}, got {status} {(payload or {}).get('error', '')}"
        if payload is None:
            return content
        assert not {"access_token", "refresh_token", "token"} & payload.keys(), "BFF leaked a bearer token"
        if "context_revision" in payload:
            self.revision = payload["context_revision"]
        return payload.get("data", payload)

    def uploads(self, suffix, **options):
        return self.call("/bff/uploads/" + suffix, **options)


class Regression:
    def __init__(self):
        self.password = json.loads((ROOT / "dev/local/accounts.json").read_text())["password"]
        self.direct = urllib.request.build_opener(NoRedirects())
        self.tokens, self.sessions, self.checks = [], [], []
        self.demo = {}

    def check(self, value, name):
        assert value, name
        self.checks.append(name)

    def owner_token(self, login="dev_admin", organization=1001, division=1001):
        value = fixture_request(self.direct, "http://127.0.0.1:8002", "/v1/auth/login", "POST", {"login": login, "password": self.password, "organization_id": organization, "division_id": division})
        token = value.get("access_token", value.get("token"))
        assert token
        self.tokens.append(token)
        return token

    def core(self, token, path, body, method="POST", expected=201):
        assert path.startswith(("/divisions", "/roles", "/directions")), "Only fresh Core fixture catalog/user writes"
        assert method in {"POST", "PUT"}
        return fixture_request(self.direct, "http://127.0.0.1:8003", "/v1/core" + path, method, body, {"Authorization": "Bearer " + token}, expected).get("data")

    def login(self, name="dev_admin", division=None, organization=None):
        session = Session()
        self.sessions.append(session)
        session.identity = session.login(name, self.password, organization or self.demo["organization_id"], division or self.demo["division_a"])
        return session

    def prepare(self):
        run = secrets.token_hex(5)
        self.demo = {"run": run, "prefix": "DEV Uploads " + run, "organization_id": 1001, "foreign_organization_id": 2001, "sessions": {}, "jobs": {}}
        token = self.owner_token()
        foreign = self.owner_token("dev_admin_math", 2001, 2001)
        for label in ("a", "b"):
            self.demo["division_" + label] = self.core(token, "/divisions", {"name": self.demo["prefix"] + " " + label.upper()})["id"]
        self.demo["foreign_division"] = self.core(foreign, "/divisions", {"name": self.demo["prefix"] + " foreign"})["id"]
        self.demo["card_direction_id"] = self.core(token, "/directions", {"name": self.demo["prefix"] + " direction"})["id"]
        for label in ("editor", "viewer", "upload_only"):
            sections = ["upload"] if label == "upload_only" else ["upload", "users", "train", "exams"]
            permissions = {section: {"view": True, "edit": label != "viewer"} for section in sections}
            role = self.core(token, "/roles", {"name": self.demo["prefix"] + " " + label, "permissions": permissions})["id"]
            login = f"uploads_{run}_{label}"
            user = self.core(token, f"/divisions/{self.demo['division_a']}/users", {"role": "staff_admin", "full_name": self.demo["prefix"] + " " + label, "login": login, "password": self.password, "role_id": role})
            self.demo[label] = {"login": login, "account_id": user["account_id"], "role_id": role, "permissions": permissions}
        login = f"uploads_{run}_student"
        user = self.core(token, f"/divisions/{self.demo['division_a']}/users", {"role": "student", "full_name": self.demo["prefix"] + " student", "login": login, "password": self.password, "class": 9})
        self.demo["student"] = {"login": login, "account_id": user["account_id"]}
        admin = self.login()
        self.demo["outside_exam_name"] = self.demo["prefix"] + " outside"
        self.demo["outside_exam_command"] = str(uuid.uuid4())
        save(MANIFEST, self.demo)
        exam = admin.call("/bff/exams", "POST", {"name": self.demo["outside_exam_name"], "exam_type": "outside_lms", "direction_id": self.demo["card_direction_id"], "date": date.today().isoformat()}, expected={201, 503}, key=self.demo["outside_exam_command"])
        if "id" in exam:
            self.demo["outside_exam_id"] = exam["id"]
        else:
            self.demo["outside_exam_dependency"] = "native_exams_unavailable"
            self.demo["outside_exam_creation_unknown"] = True
        self.check(True, "fresh divisions, canonical roles and student created through public Core APIs; optional exam availability is explicit")
        save(MANIFEST, self.demo)

    def upload(self, actor, kind, content, extension, **fields):
        entry = self.reserve_command("create", kind)
        key = entry["id"]
        body, content_type = multipart(kind, content, extension, **fields)
        session = actor.uploads("sessions", method="POST", body=body, content_type=content_type, key=key, expected=201)
        entry.update(state="completed", resource_id=session["id"])
        save(MANIFEST, self.demo)
        no_secrets(session, self.password)
        receipt = actor.uploads("receipts/" + key)
        self.check(receipt["resource_id"] == session["id"] and receipt["command_id"] == key, "create receipt recovers the original scoped session")
        return session

    def commit(self, actor, session):
        entry = self.reserve_command("commit", session["type"], session["id"])
        key = entry["id"]
        body = {"expected_revision": session["revision"]}
        job = actor.uploads(f"sessions/{session['id']}/commit", method="POST", body=body, key=key, expected=202)
        entry.update(state="completed", resource_id=job["id"])
        save(MANIFEST, self.demo)
        # Deliberately recover by GET first: unknown outcomes must retain the key.
        receipt = actor.uploads("receipts/" + key)
        self.check(receipt["resource_id"] == job["id"] and receipt["command_id"] == key, "commit outcome recovers with its original receipt and no new command")
        replay = actor.uploads(f"sessions/{session['id']}/commit", method="POST", body=body, key=key, expected=202)
        self.check(replay["id"] == job["id"], "explicit same-command commit replay returns the same durable SQL job")
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            current = actor.uploads("jobs/" + job["id"])
            no_secrets(current, self.password)
            if current["status"] == "completed":
                self.check(current["processed"] == current["total"] and current["total"] > 0, "SQL worker reports owner-confirmed terminal progress")
                return current
            assert current["status"] not in {"failed", "awaiting_authorization"}, "Own job requires owner/runtime correction: " + current.get("error", current["status"])
            time.sleep(0.3)
        raise AssertionError("Own upload job did not complete within bounded polling")

    def reserve_command(self, action, kind, resource=""):
        journal = self.demo.setdefault("commands", {})
        name = action + ":" + kind + ":" + resource
        prior = journal.get(name)
        assert not prior or prior["state"] != "pending", "Unknown command retained: recover its original receipt before any new POST"
        entry = {"id": str(uuid.uuid4()), "action": action, "type": kind, "resource": resource, "state": "pending"}
        journal[name] = entry
        save(MANIFEST, self.demo)
        return entry

    def run(self):
        self.demo = json.loads(MANIFEST.read_text())
        admin = self.login()
        editor = self.login(self.demo["editor"]["login"])
        viewer = self.login(self.demo["viewer"]["login"])
        upload_only = self.login(self.demo["upload_only"]["login"])
        student = self.login(self.demo["student"]["login"])
        catalog = editor.uploads("catalog")
        kinds = {row["type"]: row for row in catalog["types"]}
        # Never weaken this to the obsolete, unissuable `cards` permission.
        self.check(kinds["cards.xlsx"]["permission"] == "train" and kinds["cards.xlsx"]["create"], "canonical upload.edit plus train.edit grants card import")
        self.check(kinds["users.credentials"]["create"] and kinds["exams.outside"]["create"], "canonical users and exams subject editors have live import capability")
        for kind in ("users.legacy", "tests.json", "tests.external-results"):
            self.check(not kinds[kind]["create"] and kinds[kind]["mode"] == "unavailable" and bool(kinds[kind].get("dependency")), "disabled import honestly advertises its missing native contract")
            admin.uploads("sessions", method="POST", body={"type": kind, "owner_payload": {}}, key=str(uuid.uuid4()), expected=501)
        self.check(not any(row["create"] for row in viewer.uploads("catalog")["types"] if row["mode"] == "owner_session"), "viewer catalog never grants subject writes")
        csv_buf = io.StringIO()
        writer = csv.writer(csv_buf)
        writer.writerow(["full_name", "login", "password", "class"])
        imported_logins = [f"uploads_{self.demo['run']}_imported_{index}" for index in (1, 2)]
        for index, login in enumerate(imported_logins, 1):
            writer.writerow([self.demo["prefix"] + f" imported {index}", login, self.password, "9"])
        credential_bytes = csv_buf.getvalue().encode()
        body, ct = multipart("users.credentials", credential_bytes, "csv")
        for actor in (viewer, upload_only, student):
            actor.uploads("sessions", method="POST", body=body, content_type=ct, key=str(uuid.uuid4()), expected=403)
        credentials = self.upload(editor, "users.credentials", credential_bytes, "csv")
        self.demo["sessions"]["credentials"] = credentials["id"]
        preview = editor.uploads("sessions/" + credentials["id"])
        no_secrets(preview, self.password)
        self.check(len(preview["preview"]["users"]) == 2, "credential parse delegates a complete live native Core preview without password fields")
        editor.uploads(f"files/{credentials['file_id']}/download", expected=410)
        job = self.commit(editor, credentials)
        self.demo["jobs"]["credentials"] = job["id"]
        report = editor.uploads(f"jobs/{job['id']}/report")
        no_secrets(report, self.password)
        roster = editor.call("/bff/core/users?role=student&limit=200&offset=0")
        matches = [row for row in roster if row["login"] in imported_logins]
        self.check(len(matches) == 2 and len({row["account_id"] for row in matches}) == 2, "credential commit creates exactly two real Core accounts despite commit replay")
        self.demo["imported_students"] = [{"login": row["login"], "account_id": row["account_id"]} for row in matches]
        self.cards(editor)
        self.exams(editor)
        # Coordinator logs and receipts are actor-private, even to a full admin.
        for actor in (admin, viewer):
            actor.uploads("sessions/" + credentials["id"], expected=404)
            actor.uploads("jobs/" + job["id"], expected=404)
        self.check(True, "different actors cannot read another actor's imports or receipts")
        viewer.uploads("sessions/" + credentials["id"], method="PUT", body={"expected_revision": 1, "owner_payload": {}}, key=str(uuid.uuid4()), expected=404)
        previous = editor.revision
        admin.call("/bff/auth/context", "POST", {"organization_id": 1001, "division_id": self.demo["division_b"]})
        self.check(admin.uploads("sessions?limit=200") == [], "fresh division B has an independent empty upload journal")
        editor.uploads("catalog", expected=409, revision="stale-" + previous)
        editor.uploads("catalog?division_id=" + str(self.demo["division_b"]), expected=400)
        foreign = self.login("dev_admin_math", self.demo["foreign_division"], 2001)
        foreign.uploads("sessions/" + credentials["id"], expected=404)
        self.check(True, "division and foreign organization fences reject old or forged scope")
        self.withdraw(editor, credentials)
        for route in ("sessions?limit=200", "jobs?limit=200", "events?limit=200"):
            no_secrets(editor.uploads(route), self.password)
        self.check(True, "all protected GET logs, jobs and events remain credential free")
        save(MANIFEST, self.demo)

    def cards(self, actor):
        source = xlsx([["Вопрос", "Ответ"], ["Keep", "First\n\nSecond"], ["Fix", ""], ["Skip", "Answer"]])
        ss = self.upload(actor, "cards.xlsx", source, "xlsx", direction_id=self.demo["card_direction_id"], create_new_theme="true", new_theme_name=self.demo["prefix"] + " cards")
        self.demo["sessions"]["cards"] = ss["id"]
        rows = ss["preview"]["preview"]["cards"]
        self.check(len(rows) == 3 and rows[1]["action"] == "error", "cards native preview retains an invalid row until explicit correction")
        edits = [{"row": row["row"], "question": row["question"], "answer": "One\r\n\r\nTwo\r\n" if index == 1 else row["answer"], "skip": index == 2} for index, row in enumerate(rows)]
        command = self.reserve_command("edit", ss["type"], ss["id"])
        changed = actor.uploads("sessions/" + ss["id"], method="PUT", body={"expected_revision": ss["revision"], "owner_payload": {"cards": edits}}, key=command["id"])
        command.update(state="completed", resource_id=ss["id"])
        save(MANIFEST, self.demo)
        self.check(changed["revision"] == ss["revision"] + 1 and changed["preview"]["preview"]["cards"][1]["answer"] == "One\n\nTwo\n", "card edit preserves multiline answers and advances both native and coordinator revisions")
        actor.uploads("sessions/" + ss["id"], method="PUT", body={"expected_revision": ss["revision"], "owner_payload": {"cards": edits}}, key=str(uuid.uuid4()), expected=409)
        self.check(actor.uploads(f"files/{ss['file_id']}/download", binary=True) == source, "protected source download returns the original bounded workbook bytes")
        job = self.commit(actor, changed)
        self.demo["jobs"]["cards"] = job["id"]
        result = actor.uploads(f"jobs/{job['id']}/report")["report"]["owner_result"]
        self.check(result["created"] == 2 and result["skipped"] == 1, "cards owner confirms exactly two cards and one deliberate skip")
        self.demo["card_theme_id"] = result["theme_id"]
        current = actor.call(f"/bff/cards/sections/manual/{result['theme_id']}")
        self.check(len(current["cards"]) == 2 and any(row["answer"] == "One\n\nTwo" for row in current["cards"]), "real Cards owner contains the confirmed count and multiline answer")

    def exams(self, actor):
        if not self.demo.get("outside_exam_id"):
            name = self.demo.get("outside_exam_name", self.demo["prefix"] + " outside")
            items = actor.call("/bff/exams?scope=division&limit=200")["items"]
            candidates = [item for item in items if item.get("name") == name]
            assert len(candidates) == 1, "Optional outside fixture requires explicit recovery/preparation; no automatic new POST after an unknown outcome"
            self.demo["outside_exam_id"] = candidates[0]["id"]
            self.demo.pop("outside_exam_dependency", None)
            self.demo.pop("outside_exam_creation_unknown", None)
        source = xlsx([["student_login", "points", "grade", "examinator"], [self.demo["student"]["login"], "12.50", "99", "DEV Uploads"]])
        ss = self.upload(actor, "exams.outside", source, "xlsx", target_id=self.demo["outside_exam_id"])
        self.demo["sessions"]["exam"] = ss["id"]
        row = ss["preview"]["rows"][0]
        self.check(bool(row["errors"]), "outside exam native preview exposes an invalid grade without applying a result")
        corrected = {**row["input"], "grade": "2"}
        command = self.reserve_command("edit", ss["type"], ss["id"])
        changed = actor.uploads("sessions/" + ss["id"], method="PUT", body={"expected_revision": ss["revision"], "owner_payload": {"changes": [{"row_id": row["row_id"], "input": corrected, "excluded": False}]}}, key=command["id"])
        command.update(state="completed", resource_id=ss["id"])
        save(MANIFEST, self.demo)
        self.check(not changed["preview"]["rows"][0]["errors"], "explicit outside row correction is revalidated by native exams")
        job = self.commit(actor, changed)
        self.demo["jobs"]["exam"] = job["id"]
        result = actor.call(f"/bff/exams/{self.demo['outside_exam_id']}/results/{self.demo['student']['account_id']}")["current"]
        self.check(result["grade"] == 2 and float(result["points"]) == 12.5, "real outside exam result keeps decimal points independent of the corrected grade")

    def withdraw(self, actor, ss):
        token = self.owner_token()
        role = self.demo["editor"]
        original = {"name": self.demo["prefix"] + " editor", "permissions": role["permissions"]}
        try:
            self.core(token, "/roles/" + str(role["role_id"]), {"name": original["name"], "permissions": {"upload": {"view": True, "edit": True}}}, method="PUT", expected=200)
            actor.uploads("sessions/" + ss["id"], expected=403)
            actor.uploads("files/" + ss["file_id"], expected=403)
            self.check(True, "live subject withdrawal immediately blocks preview and source metadata in the existing cookie session")
        finally:
            self.core(token, "/roles/" + str(role["role_id"]), original, method="PUT", expected=200)

    def close(self):
        for session in self.sessions:
            try:
                session.close()
            except Exception:
                pass
        for token in self.tokens:
            try:
                fixture_request(self.direct, "http://127.0.0.1:8002", "/v1/auth/logout", "POST", {}, {"Authorization": "Bearer " + token})
            except Exception:
                pass


def main():
    args = argparse.ArgumentParser()
    args.add_argument("--prepare-only", action="store_true")
    options = args.parse_args()
    regression = Regression()
    status = "failed"
    failure = None
    try:
        if options.prepare_only:
            regression.prepare()
            status = "prepared_pending_owner"
        else:
            regression.run()
            status = "passed"
    except Exception as error:
        failure = str(error)
        raise
    finally:
        regression.close()
        if regression.demo:
            save(MANIFEST, regression.demo)
        save(REPORT, {"status": status, "prepare_only": options.prepare_only, "run": regression.demo.get("run"), "checks": regression.checks, "check_count": len(regression.checks), **({"error": failure} if failure else {})})
        print(json.dumps({"status": status, "checks": len(regression.checks), "prepare_only": options.prepare_only}))


if __name__ == "__main__":
    main()
