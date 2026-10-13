"""Real Uploads/Auth/Core/Cards/Exams/Tests/Rabbit acceptance, synthetic cpm_dev only.

Run with dev/.venv/bin/python. No production origin or database is accepted.
Every run creates its own HTTP fixtures; existing demo users are only used to
authenticate the two dev organization administrators. Fixture/report files omit
passwords, bearer tokens, owner preview contents and source rows. SQL audits are
read-only; optional Homework checking deletes only its own Uploads registry
fixture rows. The optional restart touches only an explicitly selected uploads
container.
"""
from __future__ import annotations

import argparse
import base64
from concurrent.futures import ThreadPoolExecutor
import hashlib
import io
import json
from pathlib import Path
import re
import secrets
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from xml.sax.saxutils import escape
import zipfile

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / "dev/scripts"))
from bootstrap import mysql_connection, settings
from export_structure import read_env

DOCKER = "/Applications/Docker.app/Contents/Resources/bin/docker"


def local_url(value):
    parsed = urllib.parse.urlsplit(value)
    if (parsed.scheme != "http" or parsed.hostname not in {"127.0.0.1", "localhost"}
            or parsed.username or parsed.password or parsed.path not in {"", "/"}
            or parsed.query or parsed.fragment):
        raise argparse.ArgumentTypeError("only loopback HTTP origins are allowed")
    return value.rstrip("/")


def multipart(fields, content, filename="synthetic.xlsx", boundary=None):
    boundary = boundary or "uploads_acceptance_" + secrets.token_hex(12)
    parts = []
    for key, value in fields.items():
        parts.append((f'--{boundary}\r\nContent-Disposition: form-data; name="{key}"\r\n\r\n{value}\r\n').encode())
    parts.append((f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="{filename}"\r\nContent-Type: application/octet-stream\r\n\r\n').encode() + content + b"\r\n")
    return b"".join(parts) + f"--{boundary}--\r\n".encode(), {"Content-Type": "multipart/form-data; boundary=" + boundary}


def column(index):
    result = ""
    while index:
        index, remainder = divmod(index - 1, 26)
        result = chr(65 + remainder) + result
    return result


def xlsx(rows, *, formula=False, extra=None):
    """Minimal real OOXML; deterministic bytes keep semantic retry hashes stable."""
    stream = io.BytesIO()
    files = {
        "[Content_Types].xml": '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>',
        "_rels/.rels": '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>',
        "xl/workbook.xml": '<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Synthetic" sheetId="1" r:id="rId1"/></sheets></workbook>',
        "xl/_rels/workbook.xml.rels": '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>',
    }
    xml = '<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>'
    for ri, row in enumerate(rows, 1):
        xml += f'<row r="{ri}">'
        for ci, value in enumerate(row, 1):
            ref = column(ci) + str(ri)
            if formula and ri == 2 and ci == 1:
                xml += f'<c r="{ref}"><f>1+1</f><v>2</v></c>'
            elif isinstance(value, (int, float)):
                xml += f'<c r="{ref}"><v>{value}</v></c>'
            else:
                xml += f'<c r="{ref}" t="inlineStr"><is><t xml:space="preserve">{escape(str(value))}</t></is></c>'
        xml += "</row>"
    files["xl/worksheets/sheet1.xml"] = xml + "</sheetData></worksheet>"
    files.update(extra or {})
    with zipfile.ZipFile(stream, "w", zipfile.ZIP_DEFLATED) as archive:
        for name, contents in files.items():
            info = zipfile.ZipInfo(name, (2026, 10, 9, 0, 0, 0))
            info.compress_type = zipfile.ZIP_DEFLATED
            archive.writestr(info, contents)
    return stream.getvalue()


class Acceptance:
    def __init__(self, args):
        self.args = args
        self.dev = settings()  # canonical loopback13306/cpm_dev guard
        self.env = read_env(ROOT / "dev/.env")
        self.password = json.loads((ROOT / "dev/local/accounts.json").read_text())["password"]
        self.import_password = "DevUploads!" + secrets.token_urlsafe(18)
        self.run = secrets.token_hex(6)
        self.prefix = "dev_uploads_" + self.run
        self.checks = []
        self.tokens = []
        self.fixtures = {"run": self.run, "synthetic_only": True, "organizations": [1001, 2001],
                         "divisions": [], "roles": [], "accounts": [], "sessions": [], "jobs": [], "files": [], "exams": []}
        self.last_request = ""
        self.observed_jobs = []
        self.unsupported_types = []
        self.exam_targets = {}
        self.person_names = {}
        self.pending_template_types = []
        self.homework_prior_evidence = None
        self.sql_mutations = []
        self.permission_sets = {
            "upload_only": {"upload": {"view": True, "edit": True}},
            "cards": {"upload": {"view": True, "edit": True}, "train": {"view": True, "edit": True}},
            "viewer": {"upload": {"view": True, "edit": False}, "train": {"view": True, "edit": False}},
            "users": {"upload": {"view": True, "edit": True}, "users": {"view": True, "edit": True}},
            "exams": {"upload": {"view": True, "edit": True}, "exams": {"view": True, "edit": True}},
            "tests": {"upload": {"view": True, "edit": True}, "tests": {"view": True, "edit": True}},
            "test_results": {"upload": {"view": True, "edit": True}, "tests": {"view": True, "edit": True}, "test-results": {"view": True, "edit": True}},
        }

    def expect(self, condition, message):
        if not condition:
            raise AssertionError(message)
        self.checks.append(message)

    def request(self, service, path, token=None, method="GET", body=None, expected=200,
                headers=None, binary=False, record=True):
        origin = getattr(self.args, service + "_url")
        self.last_request = service + " " + method + " " + path.split("?")[0]
        h = dict(headers or {})
        if token:
            h["Authorization"] = "Bearer " + token
        if body is None or isinstance(body, bytes):
            payload = body
        else:
            payload = json.dumps(body, ensure_ascii=False).encode()
            h["Content-Type"] = "application/json"
        req = urllib.request.Request(origin + path, data=payload, method=method, headers=h)
        try:
            with urllib.request.urlopen(req, timeout=110) as result:
                code, raw, response_headers = result.status, result.read(), dict(result.headers)
        except urllib.error.HTTPError as error:
            code, raw, response_headers = error.code, error.read(), dict(error.headers)
        except (urllib.error.URLError, OSError):
            raise AssertionError("loopback dependency unavailable: " + service) from None
        allowed = {expected} if isinstance(expected, int) else set(expected)
        # Never echo request/response bodies, because they may contain credentials.
        message = f"{self.last_request}: expected {sorted(allowed)}, got {code}"
        if code not in allowed:
            raise AssertionError(message)
        if record:
            self.checks.append(message)
        if binary:
            return code, raw, response_headers
        try:
            result = json.loads(raw)
        except (ValueError, UnicodeDecodeError):
            raise AssertionError("expected JSON envelope: " + self.last_request) from None
        self.no_credentials(result)
        return result

    def no_credentials(self, value):
        serialized = json.dumps(value, ensure_ascii=False)
        internal_keys = [value for name, value in self.env.items() if "INTERNAL" in name and "KEY" in name and len(value) >= 16]
        for secret in [self.password, self.import_password, *internal_keys]:
            if secret and secret in serialized:
                raise AssertionError("plaintext credential or internal service key leaked in protected data")
        # Auth responses legitimately contain current access/refresh tokens.
        if self.last_request.startswith("auth "):
            return
        if "pbkdf2:sha256:" in serialized or "$2b$" in serialized:
            raise AssertionError("password hash leaked in HTTP response")
        for token in self.tokens:
            if token and token in serialized:
                raise AssertionError("bearer leaked in HTTP response")

    def api(self, service, path, token=None, method="GET", body=None, expected=200, key=None, **kwargs):
        headers = dict(kwargs.pop("headers", {}) or {})
        if key:
            headers["Idempotency-Key"] = key
        result = self.request(service, path, token, method, body, expected, headers, **kwargs)
        allowed = {expected} if isinstance(expected, int) else set(expected)
        if max(allowed) < 300:
            self.expect(result.get("status") is True, service + " success envelope")
            return result.get("data")
        return result

    def core(self, path, token=None, method="GET", body=None, expected=200):
        return self.api("core", "/v1/core" + path, token or self.admin, method, body, expected)

    def uploads(self, path, token=None, division=None, method="GET", body=None, expected=200, key=None, **kwargs):
        return self.api("uploads", f"/v1/uploads/divisions/{division or self.a}" + path,
                        token or self.admin_a, method, body, expected, key, **kwargs)

    def owner(self, service, path, token=None, method="GET", body=None, expected=200, key=None, **kwargs):
        return self.api(service, "/v1/" + service + path, token or self.admin_a, method, body,
                        expected, key or (str(uuid.uuid4()) if method != "GET" else None), **kwargs)

    def login(self, login, division=1001, organization=1001, password=None):
        result = self.request("auth", "/v1/auth/login", method="POST", body={
            "login": login, "password": password or self.password,
            "organization_id": organization, "division_id": division})
        token = result.get("access_token") or result["token"]
        self.tokens.append(token)
        return token, result["user"]

    def fixtures_setup(self):
        if self.args.resume_fixtures:
            return self.resume_fixtures()
        for service in ["auth", "core", "uploads", "cards", "exams", "tests"]:
            self.request(service, "/health/ready")
        self.openapi = self.request("uploads", "/openapi.json")
        self.admin, self.admin_user = self.login("dev_admin")
        self.foreign, _ = self.login("dev_admin_math", 2001, 2001)
        for suffix in ["A", "B"]:
            division = self.core("/divisions", method="POST", body={"name": "DEV Uploads " + self.run + " " + suffix}, expected=201)["id"]
            self.fixtures["divisions"].append(division)
        self.a, self.b = self.fixtures["divisions"]
        self.c = self.core("/divisions", self.foreign, "POST", {"name": "DEV Uploads " + self.run + " C"}, 201)["id"]
        self.fixtures["divisions"].append(self.c)
        self.admin_a, self.admin_user_a = self.login("dev_admin", self.a)
        self.admin_b, _ = self.login("dev_admin", self.b)
        self.foreign_c, _ = self.login("dev_admin_math", self.c, 2001)
        self.direction = self.core("/directions", method="POST", body={"name": "DEV Uploads " + self.run}, expected=201)["id"]
        self.foreign_direction = self.core("/directions", self.foreign, "POST", {"name": "DEV Uploads " + self.run}, 201)["id"]
        self.school = self.core("/schools", method="POST", body={"name": "DEV Uploads School " + self.run,
                            "short_name": "DEV " + self.run, "notes": "Synthetic only", "is_active": True}, expected=201)["id"]
        self.group_a = self.core(f"/divisions/{self.a}/groups", method="POST", body={"name": "DEV Uploads " + self.run}, expected=201)["id"]
        self.group_b = self.core(f"/divisions/{self.b}/groups", method="POST", body={"name": "DEV Uploads " + self.run}, expected=201)["id"]
        self.staff = {}
        for name, permissions in self.permission_sets.items():
            role = self.core("/roles", method="POST", body={"name": "DEV Uploads " + self.run + " " + name,
                              "permissions": permissions}, expected=201)["id"]
            login = self.prefix + "_" + name
            account = self.core(f"/divisions/{self.a}/users", method="POST", body={"role": "staff_admin",
                                   "full_name": "DEV Uploads " + self.run + " " + name,
                                   "login": login, "password": self.password, "role_id": role}, expected=201)["account_id"]
            token, _ = self.login(login, self.a)
            self.staff[name] = {"token": token, "role": role, "account": account, "login": login}
            self.fixtures["roles"].append(role)
            self.fixtures["accounts"].append(account)
        self.students = []
        for name in ["Student A", "Student B"]:
            account, login = self.create_user("student", name)
            self.students.append((account, login))
        self.student, self.student_user = self.login(self.students[0][1], self.a)
        self.examiners = [self.create_user("examinator", "Examiner " + str(i)) for i in range(2)]
        self.fixtures.update(direction=self.direction, foreign_direction=self.foreign_direction, school=self.school,
                             groups=[self.group_a, self.group_b], fixture_setup_complete=True,
                             staff={name: {key: value for key, value in record.items() if key != "token"} for name, record in self.staff.items()},
                             fixture_students=self.students, fixture_examiners=self.examiners)
        self.save_fixtures()
        if self.args.homework_proof:
            self.load_homework_proof()

    def resume_fixtures(self):
        path = Path(self.args.resume_fixtures).resolve()
        self.expect(path.is_relative_to(ROOT / "dev/local") and path.stat().st_mode & 0o077 == 0,
                    "resumed fixtures are a private local synthetic handoff")
        fixtures = json.loads(path.read_text())
        self.expect(fixtures.get("synthetic_only") is True and fixtures.get("organizations") == [1001, 2001]
                    and re.fullmatch(r"[0-9a-f]{12}", fixtures.get("run", "")) and len(fixtures.get("divisions", [])) == 3
                    and len(fixtures.get("roles", [])) == len(self.permission_sets) and len(fixtures.get("accounts", [])) >= 11,
                    "resumed fixture graph has complete own organization/division/actor identities")
        self.fixtures = fixtures
        self.run = fixtures["run"]
        self.prefix = "dev_uploads_" + self.run
        self.a, self.b, self.c = fixtures["divisions"]
        self.direction, self.foreign_direction, self.school = fixtures["direction"], fixtures["foreign_direction"], fixtures["school"]
        self.group_a, self.group_b = fixtures["groups"]
        for service in ["auth", "core", "uploads", "cards", "exams", "tests"]:
            self.request(service, "/health/ready")
        self.openapi = self.request("uploads", "/openapi.json")
        self.admin, self.admin_user = self.login("dev_admin")
        self.foreign, _ = self.login("dev_admin_math", 2001, 2001)
        self.admin_a, self.admin_user_a = self.login("dev_admin", self.a)
        self.admin_b, _ = self.login("dev_admin", self.b)
        self.foreign_c, _ = self.login("dev_admin_math", self.c, 2001)
        self.staff = {}
        for index, name in enumerate(self.permission_sets):
            record = fixtures.get("staff", {}).get(name) or {"login": self.prefix + "_" + name,
                         "role": fixtures["roles"][index], "account": fixtures["accounts"][index]}
            token, actor = self.login(record["login"], self.a)
            self.expect(actor["account_id"] == record["account"], "resumed staff identity is reverified by live Auth")
            self.staff[name] = {**record, "token": token}
        self.students = fixtures.get("fixture_students") or [(fixtures["accounts"][i], self.prefix + "_" + str(i)) for i in [7, 8]]
        self.examiners = fixtures.get("fixture_examiners") or [(fixtures["accounts"][i], self.prefix + "_" + str(i)) for i in [9, 10]]
        self.student, self.student_user = self.login(self.students[0][1], self.a)
        for index, (account, _) in enumerate(self.students):
            self.person_names[account] = "DEV" + self.run + " Student" + ("A" if index == 0 else "B")
        self.fixtures.update(fixture_setup_complete=True,
                             staff={name: {key: value for key, value in record.items() if key != "token"} for name, record in self.staff.items()},
                             fixture_students=self.students, fixture_examiners=self.examiners)
        self.save_fixtures()
        self.expect(True, "resuming own fixtures performs no direction/catalog/account mutations")
        if self.args.homework_proof:
            self.load_homework_proof()

    def load_homework_proof(self):
        path = Path(self.args.homework_proof).resolve()
        self.expect(path.is_relative_to(ROOT / "dev/local") and path.stat().st_mode & 0o077 == 0,
                    "prior Homework proof is a private local synthetic report")
        proof = json.loads(path.read_text())
        self.expect(proof.get("status") == "passed" and proof.get("synthetic_only") is True
                    and proof.get("synthetic_run") == self.run and proof.get("homework_lifecycle_mutated") is False
                    and proof.get("registry", {}).get("cleaned") is True
                    and proof.get("negative_student_membership", {}).get("removed") is True,
                    "prior real Homework checks passed before owner cleanup and all own link fixtures were removed")
        self.homework_prior_evidence = {"path": str(path.relative_to(ROOT)), "checked_at": proof["checked_at"],
            "checks": proof["checks"], "coverage": proof["coverage"], "status": "passed",
            "source": "earlier real HTTP checks before native owner fixture cleanup; not rerun in this suite"}

    def create_user(self, role, suffix):
        login = self.prefix + "_" + str(len(self.fixtures["accounts"]))
        body = {"role": role, "full_name": "DEV" + self.run + " " + suffix.replace(" ", ""),
                "login": login, "password": self.password}
        if role == "student":
            body.update({"class": 10, "school_id": self.school, "group_id": self.group_a})
        account = self.core(f"/divisions/{self.a}/users", method="POST", body=body, expected=201)["account_id"]
        self.fixtures["accounts"].append(account)
        self.person_names[account] = body["full_name"]
        return account, login

    def save_fixtures(self):
        self.save(ROOT / "dev/local/uploads-http-fixtures.json", self.fixtures)

    @staticmethod
    def save(path, data):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n")
        path.chmod(0o600)

    def scoped(self, resource, organization=1001, division=None, actor=None):
        self.expect(resource["organization_id"] == organization and resource["division_id"] == (division or self.a)
                    and resource["actor_account_id"] == (actor or self.admin_user_a["account_id"]),
                    "resource retains verified organization/division/account scope")

    def create_session(self, kind, content, fields=None, filename="synthetic.xlsx", token=None,
                       division=None, key=None, expected=201):
        payload, headers = multipart({"type": kind, **(fields or {})}, content, filename)
        session = self.uploads("/sessions", token, division, "POST", payload, expected, key or str(uuid.uuid4()), headers=headers)
        if expected < 300:
            self.fixtures["sessions"].append(session["id"])
            if session.get("file_id"):
                self.fixtures["files"].append(session["file_id"])
            self.save_fixtures()
        return session

    def job(self, session, *, expected_status=None, key=None):
        key = key or str(uuid.uuid4())
        job = self.uploads("/sessions/" + session["id"] + "/commit", method="POST",
                           body={"expected_revision": session["revision"]}, expected=202, key=key)
        self.fixtures["jobs"].append(job["id"])
        self.save_fixtures()
        expected_status = "awaiting_authorization" if self.args.background_mode == "pending" else (expected_status or "completed")
        job = self.wait_job(job["id"], expected_status)
        self.scoped(job, actor=session["actor_account_id"])
        if self.args.background_mode == "pending":
            self.expect(job.get("error") == "background_authorization_pending" and job["processed"] == 0 and not job.get("report"),
                        "pending background authorization is explicit and claims no owner apply/progress/report")
            self.uploads("/jobs/" + job["id"] + "/report", expected=409)
        self.observed_jobs.append((session, job, key))
        return job

    def wait_job(self, job_id, expected=None, token=None, division=None, timeout=90):
        expected = expected or ("awaiting_authorization" if self.args.background_mode == "pending" else "completed")
        deadline = time.monotonic() + timeout
        terminal = {"completed", "failed", "awaiting_authorization"}
        while time.monotonic() < deadline:
            job = self.uploads("/jobs/" + job_id, token, division, record=False)
            if job["status"] == expected:
                self.expect(True, "Rabbit owner job reached " + expected)
                return job
            if job["status"] in terminal:
                raise AssertionError("job reached unexpected terminal status: " + job["status"] + "; code=" + job.get("error", ""))
            time.sleep(.25)
        raise AssertionError("Rabbit job did not reach expected status within bounded wait")

    def catalog_and_security(self):
        catalog = self.uploads("/catalog")
        self.scoped(catalog["scope"])
        self.types = {entry["type"]: entry for entry in catalog["types"]}
        self.unsupported_types = [entry["type"] for entry in catalog["types"] if entry["mode"] == "unavailable"]
        for kind in self.args.require_types:
            self.expect(kind in self.types and self.types[kind]["create"], "required catalog type is live: " + kind)
        self.expect(self.types["cards.xlsx"]["permission"] == "train", "Cards uses canonical train subject permission")
        if "tests.external-results" in self.types:
            self.expect(self.types["tests.external-results"]["permission"] == "test-results", "external results use canonical test-results subject permission")
        self.expect(self.types["cards.xlsx"]["target_scope"] == "organization", "Cards target remains organization-wide")
        if self.args.background_mode == "pending":
            self.expect(catalog["delivery"] == "rabbit" and catalog["job_execution"] == "awaiting_authorization"
                        and catalog["job_authorization"] == "background_authorization_pending",
                        "catalog truthfully advertises Rabbit delivery and pending background authorization")
            self.expect(all(not entry["commit"] for entry in catalog["types"]), "catalog does not advertise owner apply before authorization approval")
        self.request("uploads", f"/v1/uploads/divisions/{self.a}/catalog", expected=401)
        self.uploads("/catalog", self.admin_a, self.b, expected=403)
        self.uploads("/catalog", self.admin_a, self.c, expected=403)
        self.uploads("/catalog", self.foreign_c, self.a, expected=403)
        self.uploads("/catalog", self.staff["cards"]["token"], self.b, expected=403)
        for name in ["upload_only", "viewer"]:
            values = self.uploads("/catalog", self.staff[name]["token"])["types"]
            if name == "upload_only":
                self.expect(not any(x["create"] for x in values), "upload.edit alone grants no subject import")
            else:
                self.expect(all(not x["create"] and not x["commit"] for x in values), "read-only upload/subject role cannot mutate")
        student_catalog = self.uploads("/catalog", self.student)["types"]
        self.expect(all(x["type"] in {"homework.pdf", "attendance.zap-images"} for x in student_catalog), "student sees only personal owner files")
        # The owner workflow is not duplicated in uploads.
        self.request("uploads", f"/v1/uploads/divisions/{self.a}/homework/1/create", self.student,
                     method="POST", body={}, expected=404, binary=True, headers={"Idempotency-Key": str(uuid.uuid4())})
        self.uploads("/sessions", method="POST", body={"type": "cards.transform", "organization_id": 2001}, expected=400, key=str(uuid.uuid4()))
        self.uploads("/sessions", method="POST", body={"type": "cards.transform", "division_id": self.b}, expected=400, key=str(uuid.uuid4()))
        for limit in ["0", "201", "bad"]:
            self.uploads("/sessions?limit=" + limit, expected=400)
        self.uploads("/receipts/unknown_acceptance_command", expected=404)

    def credentials(self):
        login = self.prefix + "_credential"
        csv = ("full_name,login,password,class,school_id,group_id\n"
               + f"DEV Uploads imported {self.run},{login},{self.import_password},10,{self.school},{self.group_a}\n").encode()
        for token in [self.student, self.staff["upload_only"]["token"], self.staff["cards"]["token"]]:
            self.create_session("users.credentials", csv, filename="synthetic.csv", token=token, expected=403)
        for field, value in [("organization_id", 2001), ("division_id", self.b), ("actor_account_id", 1)]:
            self.create_session("users.credentials", csv, {field: value}, filename="synthetic.csv", expected=400)
        key = str(uuid.uuid4())
        session = self.create_session("users.credentials", csv, filename="synthetic.csv", key=key)
        self.scoped(session)
        self.expect(session["source_hash"] == hashlib.sha256(csv).hexdigest(), "credential source has stable byte hash")
        repeated = self.create_session("users.credentials", csv, filename="synthetic.csv", key=key, expected=200)
        self.expect(repeated["id"] == session["id"] and repeated["owner_session_id"] == session["owner_session_id"], "same create command/multipart boundary reuses same owner preview")
        self.create_session("users.credentials", csv.replace(login.encode(), (login + "_new").encode()), filename="synthetic.csv", key=key, expected=409)
        self.source_isolation(session, credential=True)
        receipt = self.uploads("/receipts/" + key)
        self.expect(receipt["resource_id"] == session["id"], "lost create response recovered by scoped receipt")
        # Preview itself never creates users.
        rows = self.core(f"/divisions/{self.a}/users?q={urllib.parse.quote('DEV Uploads imported ' + self.run)}")
        self.expect(not rows, "credential preview writes no subject users")
        job = self.job(session)
        if self.args.background_mode == "live":
            report = self.uploads("/jobs/" + job["id"] + "/report")
            self.expect(report["report"]["atomicity"] == "owner_transaction_only", "report states owner transaction boundary")
            _, actor = self.login(login, self.a, password=self.import_password)
            account = actor["account_id"]
            self.fixtures["accounts"].append(account)
            self.expect(actor["role"] == "student" and actor["division_id"] == self.a, "committed credential account assigned verified target division")
            self.core(f"/divisions/{self.b}/users/{account}", self.admin_b, expected=404)
            self.core(f"/divisions/{self.c}/users/{account}", self.foreign_c, expected=404)
        else:
            self.expect(not self.core(f"/divisions/{self.a}/users?q={urllib.parse.quote('DEV Uploads imported ' + self.run)}"),
                        "pending Rabbit job creates no account before background authorization is approved")
        self.job_idempotency(session, job)
        # Credentials are also denied through the source metadata/read/report family.
        self.private_storage(session, csv)
        duplicate = csv + csv.splitlines(keepends=True)[1]
        self.create_session("users.credentials", duplicate.replace(login.encode(), (login + "_dup").encode()), filename="synthetic.csv", expected=400)
        self.create_session("users.credentials", csv.replace(b"class,school_id", b"class,wrong_school"), filename="synthetic.csv", expected=400)
        elevation = ("role,full_name,login,password\nadmin,DEV Uploads Admin," + login + "_admin," + self.import_password + "\n").encode()
        self.create_session("users.credentials", elevation, filename="synthetic.csv", expected=400)
        elevation = ("role,full_name,login,password,role_id\nstaff_admin,DEV Uploads Staff," + login + "_staff," + self.import_password + "," + str(self.staff["users"]["role"]) + "\n").encode()
        self.create_session("users.credentials", elevation, filename="synthetic.csv", token=self.staff["users"]["token"], expected=400)

    def source_isolation(self, session, credential=False):
        self.uploads("/sessions/" + session["id"])
        self.uploads("/files/" + session["file_id"])
        for token, division in [(self.admin_b, self.b), (self.foreign_c, self.c), (self.staff["upload_only"]["token"], self.a)]:
            for path in ["/sessions/" + session["id"], "/files/" + session["file_id"], "/files/" + session["file_id"] + "/download"]:
                self.uploads(path, token, division, expected={403, 404})
        if credential:
            self.uploads("/files/" + session["file_id"] + "/download", expected=410)
        else:
            _, raw, headers = self.request("uploads", f"/v1/uploads/divisions/{self.a}/files/{session['file_id']}/download", self.admin_a, binary=True)
            self.expect(hashlib.sha256(raw).hexdigest() == session["source_hash"], "authorized download matches immutable source SHA256")
            self.expect("no-store" in headers.get("Cache-Control", ""), "source download disables shared caching")

    def legacy_users(self):
        name = "Legacy" + self.run + " Learner"
        proctor = "Proctor" + self.run + " Person"
        rows = [["ФИО", "Класс", "Школа", "Проктор", "Telegram"],
                [self.person_names[self.students[0][0]], "10", "", "", ""],
                [name, "7.5", "DEV Uploads School " + self.run, proctor, "@synthetic"]]
        source = xlsx(rows)
        for token in [self.student, self.staff["upload_only"]["token"], self.staff["cards"]["token"]]:
            self.create_session("users.legacy", source, token=token, expected=403)
        session = self.create_session("users.legacy", source)
        domain = session["preview"]["preview"]
        self.expect(not domain["can_commit"] and domain["summary"]["students_skip"] == 1
                    and bool(domain["students"][1]["errors"]),
                    "legacy Core resolves existing pupils and reports noninteger class in owner preview")
        self.expect(domain["students"][0]["existing_account_id"] == self.students[0][0],
                    "legacy name matching exposes account separately from legacy profile identity")
        self.source_isolation(session)
        edits = [{key: value for key, value in row.items() if key in {"row", "full_name", "class_raw", "school_name", "proctor_name", "tg_name"}}
                 for row in domain["students"]]
        edits[1]["class_raw"] = "7.0"
        changed = self.uploads("/sessions/" + session["id"], method="PUT", body={"expected_revision": 1,
                               "owner_payload": {"rows": edits}}, key=str(uuid.uuid4()))
        corrected = changed["preview"]["preview"]
        self.expect(corrected["can_commit"] and corrected["students"][1]["class"] == 7
                    and corrected["summary"]["students_create"] == 1 and corrected["summary"]["proctors_create"] == 1,
                    "legacy Core owns class coercion and proposed pupil/proctor/group graph")
        self.expect(not self.core(f"/divisions/{self.a}/users?q={urllib.parse.quote(name)}"),
                    "legacy preview and edit create no subject pupil")
        job = self.job(changed)
        self.job_idempotency(changed, job)
        if self.args.background_mode == "pending":
            self.expect(not self.core(f"/divisions/{self.a}/users?q={urllib.parse.quote(name)}")
                        and not self.core(f"/divisions/{self.a}/users?q={urllib.parse.quote(proctor)}"),
                        "pending legacy job creates no pupil or proctor")

    def private_storage(self, session, original):
        conn = mysql_connection(self.dev)
        try:
            with conn.cursor() as cursor:
                cursor.execute("SELECT DATABASE()")
                self.expect(cursor.fetchone()[0] == "cpm_dev", "read-only privacy audit uses guarded cpm_dev")
                cursor.execute("SELECT content,organization_id,division_id,actor_account_id FROM uploads_files_v1 WHERE id=%s", (session["file_id"],))
                row = cursor.fetchone()
                self.expect(row and row[0] is None and row[1:] == (1001, self.a, session["actor_account_id"]), "credential input bytes absent from scoped uploads storage")
                cursor.execute("SELECT payload FROM core_imports WHERE id=%s AND organization_id=1001 AND division_id=%s", (session["owner_session_id"], self.a))
                payload = cursor.fetchone()[0]
                payload = payload.decode() if isinstance(payload, bytes) else payload
                self.expect(self.import_password not in payload and "pbkdf2:sha256:" in payload, "Core preview stores only password hash; uploads owns no credential copy")
        finally:
            conn.close()

    def job_idempotency(self, session, job):
        replay_key = str(uuid.uuid4())
        replay = self.uploads("/sessions/" + session["id"] + "/commit", method="POST", body={"expected_revision": session["revision"]}, expected=202, key=replay_key)
        self.expect(replay["id"] == job["id"], "new confirm command reuses already committed job")
        receipt = self.uploads("/receipts/" + replay_key)
        self.expect(receipt["resource_id"] == job["id"] and receipt["state"] == "completed",
                    "confirm replay with new command ID has a durable original job receipt")
        concurrent_key = str(uuid.uuid4())
        with ThreadPoolExecutor(max_workers=2) as pool:
            replies = list(pool.map(lambda _: self.uploads("/sessions/" + session["id"] + "/commit", method="POST",
                                body={"expected_revision": session["revision"]}, expected=202, key=concurrent_key), range(2)))
        self.expect(all(reply["id"] == job["id"] for reply in replies),
                    "concurrent duplicate confirmations recover one persisted original job")
        retried = self.uploads("/jobs/" + job["id"] + "/retry", method="POST", body={}, key=str(uuid.uuid4()))
        self.expect(retried["id"] == job["id"], "retry reuses same original job and scope")
        if self.args.background_mode == "live":
            self.expect(retried["status"] == "completed", "retry completed job does not reapply subject data")
        else:
            waiting = self.wait_job(job["id"])
            self.expect(waiting.get("error") == "background_authorization_pending" and waiting["processed"] == 0,
                        "Rabbit retry preserves explicit pending authorization instead of applying data")
        for token, division in [(self.admin_b, self.b), (self.foreign_c, self.c), (self.staff["upload_only"]["token"], self.a)]:
            self.uploads("/jobs/" + job["id"], token, division, expected={403, 404})
            self.uploads("/jobs/" + job["id"] + "/report", token, division, expected={403, 404})
            self.uploads("/jobs/" + job["id"] + "/retry", token, division, "POST", {}, {403, 404}, str(uuid.uuid4()))
        self.uploads("/sessions/" + session["id"], method="PUT", body={"expected_revision": session["revision"], "owner_payload": {}}, expected={405, 409}, key=str(uuid.uuid4()))

    def cards(self):
        theme = self.owner("cards", "/themes", method="POST", body={"name": "DEV Uploads Cards " + self.run + " " + secrets.token_hex(3),
                           "direction_id": self.direction, "questions": []})
        self.theme = theme["id"]
        self.fixtures.setdefault("themes", []).append(self.theme)
        fields = {"direction_id": str(self.direction), "theme_id": str(self.theme)}
        source = xlsx([["Вопрос", "Ответ"], ["DEV Question " + self.run, "First\n\nSecond\n"], ["Fix empty answer", ""], ["Exclude row", "Answer"]])
        for token in [self.student, self.staff["upload_only"]["token"], self.staff["viewer"]["token"]]:
            self.create_session("cards.xlsx", source, fields, token=token, expected=403)
        key = str(uuid.uuid4())
        session = self.create_session("cards.xlsx", source, fields, key=key)
        self.scoped(session)
        preview = session["preview"]["preview"]
        rows = preview["cards"]
        self.expect(len(rows) == 3 and rows[1]["action"] == "error", "Cards owner validates every row before commit")
        self.expect(not self.owner("cards", f"/themes/{self.theme}/cards"), "invalid Cards preview has no subject side effects")
        # Validate source bytes and current source access before confirmation.
        self.source_isolation(session)
        repeated = self.create_session("cards.xlsx", source, fields, key=key, expected=200)
        self.expect(repeated["owner_session_id"] == session["owner_session_id"], "Cards parse response loss does not create another owner preview")
        edits = [{"row": x["row"], "question": x["question"], "answer": x["answer"], "skip": False} for x in rows]
        edits[1]["answer"] = "DEV repaired answer"
        edits[2]["skip"] = True
        body = {"expected_revision": session["revision"], "owner_payload": {"cards": edits}}
        edit_key = str(uuid.uuid4())
        changed = self.uploads("/sessions/" + session["id"], method="PUT", body=body, key=edit_key)
        self.expect(changed["revision"] == session["revision"] + 1 and changed["owner_revision"] == session["owner_revision"] + 1, "coordinator and native owner preview revisions advance together")
        edit_replay = self.uploads("/sessions/" + session["id"], method="PUT", body=body, key=edit_key)
        self.expect(edit_replay["revision"] == changed["revision"], "lost preview edit response reuses same revision")
        self.uploads("/sessions/" + session["id"], method="PUT", body=body, expected=409, key=str(uuid.uuid4()))
        self.uploads("/sessions/" + session["id"] + "/commit", method="POST", body={"expected_revision": session["revision"]}, expected=409, key=str(uuid.uuid4()))
        job = self.job(changed)
        cards = self.owner("cards", f"/themes/{self.theme}/cards")
        if self.args.background_mode == "live":
            self.expect(len(cards) == 2 and any(x["question"] == "Fix empty answer" for x in cards), "owner commit creates exactly included repaired cards")
        else:
            self.expect(not cards, "pending background authorization leaves Cards subject data unchanged")
        other_division_cards = self.owner("cards", f"/themes/{self.theme}/cards", self.admin_b)
        self.expect(other_division_cards == cards, "manual Cards data is shared inside organization across divisions")
        self.owner("cards", f"/themes/{self.theme}/cards", self.foreign_c, expected=404)
        if self.args.background_mode == "live":
            report = self.uploads("/jobs/" + job["id"] + "/report")
            self.expect(len(report["report"]["rows"]) == 3 and sum(x["state"] == "skipped" for x in report["report"]["rows"]) == 1, "report keeps stable original row mapping including excluded row")
        self.job_idempotency(changed, job)
        # Explicitly exercise canonical train edit without any fictitious cards permission.
        scoped = self.create_session("cards.xlsx", xlsx([["Вопрос", "Ответ"], ["Scoped Q", "Scoped A"]]), fields, token=self.staff["cards"]["token"])
        self.expect(scoped["actor_account_id"] == self.staff["cards"]["account"], "scoped upload+train editor can use Cards adapter")
        self.live_revoke(scoped, self.staff["cards"])
        self.response_loss_cards(fields)
        self.owner_revision_conflict(fields)

    def live_revoke(self, session, staff):
        self.core("/roles/" + str(staff["role"]), method="PUT", body={"name": "DEV Uploads " + self.run + " cards",
                  "permissions": {"upload": {"view": True, "edit": True}}})
        try:
            for suffix in ["/sessions/" + session["id"], "/files/" + session["file_id"], "/files/" + session["file_id"] + "/download"]:
                self.uploads(suffix, staff["token"], expected=403)
            self.uploads("/sessions/" + session["id"] + "/commit", staff["token"], method="POST", body={"expected_revision": session["revision"]}, expected=403, key=str(uuid.uuid4()))
        finally:
            self.core("/roles/" + str(staff["role"]), method="PUT", body={"name": "DEV Uploads " + self.run + " cards", "permissions": self.permission_sets["cards"]})
        self.expect(True, "current subject permission is rechecked for preview/source/download/commit")

    def response_loss_cards(self, fields):
        session = self.create_session("cards.xlsx", xlsx([["Вопрос", "Ответ"], ["DEV lost reply " + self.run, "Answer"]]), fields)
        key = str(uuid.uuid4())
        path = f"/v1/uploads/divisions/{self.a}/sessions/{session['id']}/commit"
        self.drop_response(path, self.admin_a, {"expected_revision": session["revision"]}, key)
        receipt = None
        for _ in range(80):
            response = self.request("uploads", f"/v1/uploads/divisions/{self.a}/receipts/{key}", self.admin_a, expected={200, 404}, record=False)
            if response.get("status") is True:
                receipt = response["data"]
                break
            time.sleep(.1)
        self.expect(receipt is not None and receipt["action"].startswith("session.commit:"), "dropped actual TCP response recovered with same persisted command receipt")
        job_id = receipt["resource_id"]
        replay = self.uploads("/sessions/" + session["id"] + "/commit", method="POST", body={"expected_revision": session["revision"]}, expected=202, key=key)
        self.expect(replay["id"] == job_id, "resend after transport loss returns original job ID")
        job = self.wait_job(job_id)
        self.fixtures["jobs"].append(job_id)
        self.observed_jobs.append((session, job, key))
        if self.args.background_mode == "pending":
            self.expect(job["error"] == "background_authorization_pending" and job["processed"] == 0,
                        "response-loss command is durably awaiting authorization without subject progress")
        count = len(self.owner("cards", f"/themes/{self.theme}/cards"))
        self.uploads("/jobs/" + job_id + "/retry", method="POST", body={}, key=str(uuid.uuid4()))
        self.expect(len(self.owner("cards", f"/themes/{self.theme}/cards")) == count, "response-loss retry never duplicates native Cards apply")

    def owner_revision_conflict(self, fields):
        session = self.create_session("cards.xlsx", xlsx([["Вопрос", "Ответ"], ["DEV owner revision " + self.run, "Original"]]), fields)
        row = session["preview"]["preview"]["cards"][0]
        before = len(self.owner("cards", f"/themes/{self.theme}/cards"))
        native = self.owner("cards", "/sessions/" + session["owner_session_id"], method="PUT", body={
            "expected_revision": session["owner_revision"],
            "cards": [{"row": row["row"], "question": row["question"], "answer": "Owner changed", "skip": False}]})
        self.expect(native["revision"] == session["owner_revision"] + 1, "native owner changed preview independently of coordinator")
        failed = self.job(session, expected_status="failed")
        if self.args.background_mode == "live":
            self.expect(failed["error"] == "owner_preview_modified" and failed["processed"] == 0,
                        "worker rejects stale owner revision and never claims progress")
        else:
            self.expect(failed["error"] == "background_authorization_pending" and failed["processed"] == 0,
                        "authorization gate prevents owner apply even after native preview revision changed")
        self.expect(len(self.owner("cards", f"/themes/{self.theme}/cards")) == before,
                    "stale owner revision does not apply changed preview")

    def drop_response(self, path, token, body, key):
        origin = urllib.parse.urlsplit(self.args.uploads_url)
        payload = json.dumps(body).encode()
        headers = (f"POST {path} HTTP/1.1\r\nHost: {origin.hostname}:{origin.port or 80}\r\n"
                   + f"Authorization: Bearer {token}\r\nIdempotency-Key: {key}\r\nContent-Type: application/json\r\n"
                   + f"Content-Length: {len(payload)}\r\nConnection: close\r\n\r\n").encode()
        with socket.create_connection((origin.hostname, origin.port or 80), timeout=10) as connection:
            connection.sendall(headers + payload)
            # Content-Length delimits this complete request. Half-closing would
            # cancel Go's request before a receipt can commit. Wait for response
            # progress, then close without receiving the original job body.
            connection.settimeout(20)
            response_headers = bytearray()
            while not response_headers.endswith(b"\r\n\r\n"):
                byte = connection.recv(1)
                if not byte or len(response_headers) >= 65536:
                    raise AssertionError("discarded commit response did not contain bounded complete HTTP headers")
                response_headers.extend(byte)
            status = bytes(response_headers).split(b"\r\n", 1)[0].split(b" ")
            self.expect(len(status) >= 2 and status[1] == b"202",
                        "actual discarded commit response headers confirmed202 without reading original job body")
        self.checks.append("client deliberately discarded a real commit response")

    def exams(self):
        created = self.owner("exams", "", method="POST", body={"exam_type": "classic", "direction_id": self.direction}, expected=201)
        exam = created["id"]
        self.exam_targets.update({"exams.questions": exam, "exams.assignments": exam})
        self.fixtures["exams"].append(exam)
        self.owner("exams", f"/{exam}/parts", method="POST", body={"code": "A", "weight": 1, "quota": 5})
        rows = [["part_code", "question_text", "answer_text"]] + [["A", f"DEV Uploads {self.run} question {i}", "DEV answer"] for i in range(237)]
        session = self.create_session("exams.questions", xlsx(rows), {"target_id": str(exam)})
        preview = self.uploads("/sessions/" + session["id"])["preview"]
        self.expect(preview["total"] == 237 and len(preview["rows"]) == 237, "Exams coordinator aggregates every native preview page beyond100/200rows")
        self.expect(len({row["row_id"] for row in preview["rows"]}) == 237 and not any(row["errors"] for row in preview["rows"]), "full Exams preview has stable unique row IDs and owner validations")
        last = preview["rows"][-1]
        changed = self.uploads("/sessions/" + session["id"], method="PUT", body={"expected_revision": session["revision"],
                               "owner_payload": {"changes": [{"row_id": last["row_id"], "excluded": True}]}}, key=str(uuid.uuid4()))
        self.expect(changed["preview"]["rows"][-1]["excluded"] is True, "edit on final native preview page is preserved")
        job = self.job(changed)
        if self.args.background_mode == "live":
            self.expect(self.owner("exams", f"/{exam}/questions?limit=200")["total"] == 236, "Exams atomic commit includes all pages except explicit excluded row")
            report = self.uploads("/jobs/" + job["id"] + "/report")["report"]
            self.expect(len(report["rows"]) == 237 and sum(r["state"] == "skipped" for r in report["rows"]) == 1, "Exams report covers full input instead of first100rows")
        else:
            self.expect(self.owner("exams", f"/{exam}/questions?limit=200")["total"] == 0,
                        "full immutable Exams preview waits without applying questions")
        self.source_isolation(changed)
        self.job_idempotency(changed, job)
        # Assignments owner matching/errors/atomicity and protected reference IDs.
        assignments = [["student_login", "examinator_1_login", "examinator_2_login", "replacement_limit"],
                       [self.students[0][1], self.examiners[0][1], self.examiners[1][1], 1],
                       ["no_such_dev_" + self.run, self.examiners[0][1], self.examiners[1][1], 0]]
        assignment_session = self.create_session("exams.assignments", xlsx(assignments), {"target_id": str(exam)})
        p = assignment_session["preview"]
        self.expect(not p["rows"][0]["errors"] and bool(p["rows"][1]["errors"]), "Exams assignments use owner exact login matching and report unresolved row")
        failed = self.job(assignment_session, expected_status="failed")
        self.expect(self.owner("exams", f"/{exam}/assignments")["total"] == 0, "invalid assignment batch leaves no partially committed assignments")
        self.expect(failed["processed"] == 0, "unapplied owner transaction records no confirmed progress")
        fixed = self.create_session("exams.assignments", xlsx(assignments[:2]), {"target_id": str(exam)})
        self.job(fixed)
        self.expect(self.owner("exams", f"/{exam}/assignments")["total"] == (1 if self.args.background_mode == "live" else 0),
                    "assignment data follows the explicitly selected authorization mode")
        outside = self.owner("exams", "", method="POST", body={"exam_type": "outside_lms", "direction_id": self.direction,
                              "date": "2026-10-09"}, expected=201)["id"]
        self.fixtures["exams"].append(outside)
        self.exam_targets["exams.outside"] = outside
        result_session = self.create_session("exams.outside", xlsx([["student_login", "points", "grade", "examinator"],
                                                [self.students[1][1], 54.5, 4, "DEV Uploads external"]]), {"target_id": str(outside)})
        self.job(result_session)
        if self.args.background_mode == "live":
            result = self.owner("exams", f"/{outside}/results/{self.students[1][0]}")
            self.expect(result["points"] == 54.5 and result["grade"] == 4, "outside Exams preserves independent fractional points and grade")
        else:
            self.owner("exams", f"/{outside}/results/{self.students[1][0]}", expected=404)
            self.expect(True, "pending external Exams import creates no result")
        self.create_session("exams.questions", xlsx(rows[:2]), {"target_id": str(exam)}, token=self.foreign_c, division=self.c, expected=404)

    def tests_imports(self):
        payload = {"title": "DEV Uploads JSON " + self.run, "direction": "DEV Uploads " + self.run,
                   "startDate": "2026-10-09T10:00", "endDate": "2026-10-09T12:00", "timeLimitMinutes": 30,
                   "questions": [{"questionId": 7, "type": "single", "text": "DEV choice", "points": 2,
                                  "answers": [{"id": "a", "text": "Yes", "isCorrect": True},
                                              {"id": "b", "text": "No", "isCorrect": False}]},
                                 {"questionId": 9, "type": "text", "text": "DEV free answer", "points": 4,
                                  "correctAnswers": [" First ", "Second"]}]}
        source = json.dumps(payload, ensure_ascii=False).encode()
        fields = {"direction_id": str(self.direction)}
        for token in [self.student, self.staff["upload_only"]["token"], self.staff["cards"]["token"]]:
            self.create_session("tests.json", source, fields, filename="synthetic.json", token=token, expected=403)
        initial_count = self.owner("tests", f"/divisions/{self.a}/tests")["total"]
        key = str(uuid.uuid4())
        session = self.create_session("tests.json", source, fields, filename="synthetic.json", key=key)
        domain = session["preview"]["preview"]
        self.expect(domain["valid"] and domain["summary"]["questions_total"] == 2 and domain["summary"]["total_points"] == 6,
                    "Tests owner validates native JSON question types and points without creating draft")
        self.expect(domain["preview"]["questions"][1]["correct_answers"] == ["First", "Second"],
                    "Tests normalized text answers reach common preview intact")
        replay = self.create_session("tests.json", source, fields, filename="synthetic.json", key=key, expected=200)
        self.expect(replay["id"] == session["id"] and replay["owner_session_id"] == session["id"],
                    "stateless Tests validation has one durable coordinator preview identity")
        self.source_isolation(session)
        body = {"expected_revision": session["revision"], "owner_payload": {"payload": {**payload, "title": payload["title"] + " edited"}}}
        edit_key = str(uuid.uuid4())
        changed = self.uploads("/sessions/" + session["id"], method="PUT", body=body, key=edit_key)
        self.expect(changed["revision"] == 2 and changed["owner_revision"] == 2,
                    "Tests stateless source edit advances persistent preview revision once")
        repeated = self.uploads("/sessions/" + session["id"], method="PUT", body=body, key=edit_key)
        self.expect(repeated["revision"] == 2, "same Tests edit receipt does not validate into another revision")
        self.uploads("/sessions/" + session["id"], method="PUT", body=body, key=str(uuid.uuid4()), expected=409)
        self.uploads("/sessions/" + session["id"] + "/commit", method="POST", body={"expected_revision": 1}, key=str(uuid.uuid4()), expected=409)
        self.uploads("/sessions/" + session["id"], method="PUT", body={"expected_revision": 2,
                     "owner_payload": {"payload": payload, "direction_id": self.foreign_direction}}, key=str(uuid.uuid4()), expected=400)
        job = self.job(changed)
        self.job_idempotency(changed, job)
        if self.args.background_mode == "pending":
            self.expect(self.owner("tests", f"/divisions/{self.a}/tests")["total"] == initial_count,
                        "pending Tests JSON command creates no subject test or draft")
        invalid = self.create_session("tests.json", json.dumps({**payload, "questions": []}).encode(), fields, filename="synthetic.json")
        self.expect(invalid["preview"]["preview"]["valid"] is False, "domain-invalid JSON remains inspectable with owner diagnostics")
        self.uploads("/sessions/" + invalid["id"] + "/commit", method="POST", body={"expected_revision": invalid["revision"]}, key=str(uuid.uuid4()), expected=409)
        otp = {"Name": "DEV Uploads OTP " + self.run, "Questions": [{"Number": 1, "AnswerType": 10,
               "Text": "<p>DEV HTML &amp; content</p>", "maxScore": 2.5,
               "AnswerTemplates": [{"Number": 1, "Text": "Yes", "IsRightAnswer": True},
                                   {"Number": 2, "Text": "No", "IsRightAnswer": False}]}]}
        otp_fields = {**fields, "direction": "DEV Uploads " + self.run,
                      "startDate": "2026-10-09T10:00", "endDate": "2026-10-09T12:00"}
        otp_session = self.create_session("tests.json", ("var test = " + json.dumps(otp) + ";\nwindow.trailer = true;").encode(), otp_fields, filename="test.js")
        otp_preview = otp_session["preview"]["preview"]
        self.expect(otp_preview["valid"] and otp_preview["source"] == "online_test_pad"
                    and otp_preview["preview"]["questions"][0]["points"] == 2,
                    "legacy OnlineTestPad source is parsed without executing JavaScript and validated by Tests")
        self.external_results()

    def external_results(self):
        base = f"/divisions/{self.a}/external-tests"
        test = self.owner("tests", base, method="POST", body={"name": "DEV Uploads external " + self.run,
                          "direction_id": self.direction, "date": "2026-10-09"}, expected=201)
        self.fixtures.setdefault("external_tests", []).append(test["id"])
        names = [self.person_names[account] for account, _ in self.students]
        source_rows = [["Synthetic online test"], ["ФИО", "Процент правильных ответов (%)", "Логин"],
                       [names[0], 0, self.students[0][1]], [names[1], "87,5%", self.students[1][1]]]
        source = xlsx(source_rows)
        fields = {"target_id": test["id"]}
        for token in [self.student, self.staff["upload_only"]["token"], self.staff["tests"]["token"]]:
            self.create_session("tests.external-results", source, fields, token=token, expected=403)
        session = self.create_session("tests.external-results", source, fields)
        domain = session["preview"]["preview"]
        self.expect(domain["can_commit"] and domain["summary"]["import_rows"] == 2,
                    "Tests owner matches external results only against selected division pupils")
        self.expect([row["row"] for row in domain["rows"]] == [3, 4]
                    and [row["percent"] for row in domain["rows"]] == [0, 87.5],
                    "external source retains physical sheet rows, zero and fractional percentages")
        self.expect([row["account_id"] for row in domain["rows"]] == [x[0] for x in self.students],
                    "external preview carries authoritative account IDs rather than guessed legacy profile IDs")
        self.source_isolation(session)
        malformed = self.create_session("tests.external-results", xlsx(source_rows[:3] + [["Absent" + self.run + " Student", "", "no_login"]]), fields)
        bad = malformed["preview"]["preview"]
        self.expect(not bad["can_commit"] and bool(bad["rows"][1]["errors"]), "external unresolved/blank score diagnostics block the whole confirmation")
        self.uploads("/sessions/" + malformed["id"] + "/commit", method="POST", body={"expected_revision": 1}, key=str(uuid.uuid4()), expected=409)
        edits = [{key: value for key, value in row.items() if key in {"row_id", "row", "full_name", "account_id", "legacy_student_id", "percent", "correct_count", "completed_at", "time_spent", "login"}}
                 for row in domain["rows"]]
        edits[1]["percent"] = 88.125
        changed = self.uploads("/sessions/" + session["id"], method="PUT", body={"expected_revision": 1,
                               "owner_payload": {"rows": edits}}, key=str(uuid.uuid4()))
        self.expect(changed["preview"]["preview"]["rows"][1]["percent"] == 88.125 and changed["owner_revision"] == 2,
                    "external correction is revalidated and pinned to original test revision")
        job = self.job(changed)
        self.job_idempotency(changed, job)
        if self.args.background_mode == "pending":
            self.expect(self.owner("tests", base + "/" + test["id"] + "/results")["total"] == 0,
                        "pending external-results job cannot write a pupil grade")
        stale = self.create_session("tests.external-results", source, fields)
        self.owner("tests", base + "/" + test["id"], method="PUT", body={"expected_revision": test["revision"],
                   "name": test["name"] + " changed", "direction_id": self.direction, "date": "2026-10-09"})
        self.uploads("/sessions/" + stale["id"], expected=409)
        self.uploads("/sessions/" + stale["id"] + "/commit", method="POST", body={"expected_revision": 1}, key=str(uuid.uuid4()), expected=409)
        self.expect(True, "native external test revision change conflicts instead of silently replacing frozen preview")
        self.create_session("tests.external-results", source, fields, token=self.foreign_c, division=self.c, expected=404)

    def malformed_sources(self):
        fields = {"direction_id": str(self.direction), "theme_id": str(self.theme)}
        rows = [["Вопрос", "Ответ"], ["DEV Safety", "Answer"]]
        tests = [
            ("bad.xlsx", b"not a ZIP", 400),
            ("synthetic.csv", b"wrong format", 415),
            ("synthetic.xlsm", xlsx(rows), 415),
            ("synthetic.xlsx", xlsx(rows, formula=True), 400),
            ("synthetic.xlsx", xlsx(rows, extra={"xl/vbaProject.bin": b"synthetic"}), 400),
            ("synthetic.xlsx", xlsx(rows, extra={"xl/externalLinks/link.xml": "<x/>"}), 400),
            ("synthetic.xlsx", xlsx(rows, extra={"xl/embeddings/ole.bin": b"synthetic"}), 400),
            ("synthetic.xlsx", xlsx(rows, extra={"../outside.xml": "<x/>"}), 400),
            ("synthetic.xlsx", xlsx(rows, extra={"xl/unsafe.xml": '<!DOCTYPE x [<!ENTITY a "synthetic">]><x/>'}), 400),
            ("synthetic.xlsx", xlsx(rows, extra={"xl/unsafe.rels": '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="x" Target="https://example.invalid/file" TargetMode="External"/></Relationships>'}), 400),
            ("synthetic.xlsx", xlsx(rows, extra={"xl/large.xml": "<x>" + "0" * (33 << 20) + "</x>"}), 413),
            ("synthetic.xlsx", b"0" * ((8 << 20) + 1), 413),
        ]
        before = len(self.owner("cards", f"/themes/{self.theme}/cards"))
        for filename, content, expected in tests:
            self.create_session("cards.xlsx", content, fields, filename=filename, expected=expected)
        csv = ("full_name,login,password,class\n" + f"DEV Invalid,{self.prefix}_large,{self.import_password},10\n" * 101).encode()
        self.create_session("users.credentials", csv, filename="synthetic.csv", expected=413)
        self.expect(len(self.owner("cards", f"/themes/{self.theme}/cards")) == before, "all malformed/formula/macro/external/decompression rejections leave domain unchanged")

    def templates(self):
        path = "/v1/uploads/divisions/{division}/templates/{type}"
        self.expect(path in self.openapi["paths"], "protected common template route appears in OpenAPI")
        downloads = [(kind, "?target_id=" + str(self.exam_targets[kind]) if kind.startswith("exams.") else "")
                     for kind in ["users.credentials", "users.legacy", "cards.xlsx", "exams.questions", "exams.assignments", "exams.outside"]]
        downloads += [("tests.json", f"?direction_id={self.direction}&format={format}")
                      for format in ["cpm_json", "online_test_pad"]]
        downloads += [("tests.external-results", "?target_id=" + self.fixtures["external_tests"][-1])]
        for kind, query in downloads:
            self.expect(self.types[kind]["template"] is True, "catalog advertises an applicable native template: " + kind)
            _, content, headers = self.request("uploads", f"/v1/uploads/divisions/{self.a}/templates/{kind}" + query, self.admin_a, binary=True)
            self.expect(bool(content) and self.import_password.encode() not in content and self.password.encode() not in content,
                        "template has usable data and no credentials: " + kind)
            self.expect("no-store" in headers.get("Cache-Control", ""), "template response is private: " + kind)
            self.uploads("/templates/" + kind + query, self.staff["upload_only"]["token"], expected=403)
            self.uploads("/templates/" + kind + query, self.student, expected=403)
        for kind in ["cards.transform", "homework.pdf", "attendance.zap-images"]:
            self.expect(self.types[kind]["template"] is False, "catalog hides file templates outside file import: " + kind)
            response = self.uploads("/templates/" + kind, expected=400)
            self.expect(response["error"] == "template_not_applicable", "non-file input has an explicit template capability: " + kind)

    def attachment_references(self):
        """Real owner attachment; uploads registers a reference and owns no zap."""
        calendar = self.api("attendance", "/v1/attendance/calendars", self.student)
        png = base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGNQaFAAAAGEAMFhTfg1AAAAAElFTkSuQmCC")
        created = self.api("attendance", f"/v1/attendance/divisions/{self.a}/zaps", self.student, "POST", {
            "expected_revision": calendar["revision"],
            "data": {"text": "DEV Uploads image reference " + self.run, "dates": ["2026-10-09"],
                     "images": [{"mime": "image/png", "data": base64.b64encode(png).decode()}]}},
            key=str(uuid.uuid4()))
        zap = created["value"]
        owner_id = str(zap["id"])
        body = {"type": "attendance.zap-images", "target_id": owner_id, "owner_reference": "1"}
        key = str(uuid.uuid4())
        registered = self.uploads("/files/register", self.student, method="POST", body=body, expected=201, key=key)
        self.fixtures["files"].append(registered["id"])
        self.scoped(registered, actor=self.student_user["account_id"])
        replay = self.uploads("/files/register", self.student, method="POST", body=body, key=key)
        self.expect(replay["id"] == registered["id"], "repeated attachment registration uses one registry reference")
        metadata = self.uploads("/files/" + registered["id"], self.student)
        self.expect(metadata["lifecycle_owner"] == "attendance" and metadata["owner_live"]["image_count"] == 1,
                    "Attendance revision.value envelope is adapted to real image metadata")
        _, downloaded, headers = self.request("uploads", f"/v1/uploads/divisions/{self.a}/files/{registered['id']}/download", self.student, binary=True)
        self.expect(downloaded == png and headers.get("Content-Type", "").startswith("image/png"),
                    "registry download returns owner's exact protected image bytes")
        receipt = self.uploads("/receipts/" + key, self.student)
        self.expect(receipt["resource_id"] == registered["id"], "student's own attachment registration receipt is recoverable")
        other_student, _ = self.login(self.students[1][1], self.a)
        for token, division in [(other_student, self.a), (self.admin_b, self.b), (self.foreign_c, self.c)]:
            self.uploads("/files/" + registered["id"], token, division, expected={403, 404})
            self.uploads("/files/" + registered["id"] + "/download", token, division, expected={403, 404})
            self.uploads("/receipts/" + key, token, division, expected={403, 404})
        self.uploads("/files/register", self.student, method="POST", body={**body, "owner_reference": "2"}, expected=404, key=str(uuid.uuid4()))
        self.uploads("/files/register", other_student, method="POST", body=body, expected={403, 404}, key=str(uuid.uuid4()))
        self.uploads("/files/register", self.student, method="POST", body={**body, "organization_id": 2001}, expected=400, key=str(uuid.uuid4()))
        # Read-only Homework link contract: no upload/complete/submit is sent.
        homework_type = self.types["homework.pdf"]
        self.expect(homework_type["mode"] == "owner_direct_s3" and not homework_type["create"] and not homework_type["commit"],
                    "Homework capability links to its owner and exposes no duplicate lifecycle")

    def homework_reference(self):
        path = Path(self.args.homework_fixture).resolve()
        self.expect(path.is_relative_to(ROOT / "dev/local"), "Homework metadata fixture is a local synthetic handoff")
        handoff = json.loads(path.read_text())
        self.expect(handoff.get("synthetic_only") is True and handoff.get("organization_id") in {1001, 2001},
                    "Homework metadata handoff explicitly identifies synthetic scope")
        self.expect(path.stat().st_mode & 0o077 == 0, "Homework handoff is private to the local user")
        token, actor = self.login(handoff.get("student_login") or handoff["login"], handoff["division_id"], handoff["organization_id"], self.password)
        division = handoff["division_id"]
        self.expect(actor["account_id"] == handoff["account_id"] and actor["role"] == "student"
                    and actor["division_id"] == division and actor["organization_id"] == handoff["organization_id"],
                    "Homework linked actor comes from live Auth and canonical dev credential lookup")
        owner = self.api("homework", f"/v1/homework/divisions/{division}/jobs/{handoff['job_id']}", token)
        body = {"type": "homework.pdf", "owner_reference": handoff["job_id"]}
        key = str(uuid.uuid4())
        registered = self.uploads("/files/register", token, division, "POST", body, 201, key)
        self.fixtures["homework_registry"] = {"id": registered["id"], "command_id": key,
            "organization_id": handoff["organization_id"], "division_id": division,
            "actor_account_id": actor["account_id"], "job_id": handoff["job_id"], "cleaned": False}
        self.save_fixtures()
        membership_fixture = None
        try:
            self.scoped(registered, handoff["organization_id"], division, actor["account_id"])
            replay = self.uploads("/files/register", token, division, "POST", body, 200, key)
            self.expect(replay["id"] == registered["id"], "Homework registration replay retains one original protected reference")
            metadata = self.uploads("/files/" + registered["id"], token, division)
            self.expect(metadata["lifecycle_owner"] == "homework" and metadata["owner_live"] == owner,
                        "Homework registry reads actual authorized owner job without copying lifecycle")
            receipt = self.uploads("/receipts/" + key, token, division)
            self.expect(receipt["resource_id"] == registered["id"] and receipt["state"] == "completed",
                        "Homework student's own reference receipt recovers its original registered file")
            response = self.uploads("/files/" + registered["id"] + "/download", token, division, expected=409)
            self.expect(response["error"] == "use_homework_submission_file_url",
                        "Homework download explicitly routes to owner's protected file URL workflow")
            wrong_actor, _ = self.login("dev_admin", division, handoff["organization_id"])
            student_account, student_login = self.students[1]
            memberships = self.core(f"/organization/users/{student_account}/memberships", wrong_actor)["memberships"]
            if not any(member["division_id"] == division for member in memberships):
                self.core(f"/divisions/{division}/memberships/{student_account}", wrong_actor, "PUT", {})
                membership_fixture = {"account_id": student_account, "organization_id": handoff["organization_id"],
                                      "division_id": division, "newly_created": True, "removed": False}
                self.fixtures["homework_negative_student_membership"] = membership_fixture
                self.save_fixtures()
            same_division_student, same_actor = self.login(student_login, division, handoff["organization_id"])
            self.expect(same_actor["role"] == "student" and same_actor["account_id"] != actor["account_id"],
                        "same-organization Homework negative actor is another real student in the original division")
            for path in ["/files/" + registered["id"], "/files/" + registered["id"] + "/download", "/receipts/" + key]:
                self.uploads(path, wrong_actor, division, expected={403, 404})
                self.uploads(path, same_division_student, division, expected={403, 404})
                self.uploads(path, self.foreign_c, self.c, expected={403, 404})
            for path in ["/events?limit=200", "/sessions?limit=200", "/jobs?limit=200"]:
                visible = self.uploads(path, wrong_actor, division)
                serialized = json.dumps(visible)
                self.expect(registered["id"] not in serialized and handoff["job_id"] not in serialized,
                            "organization admin journal cannot expose another student's private Homework reference/progress")
            self.uploads("/files/register", token, division, "POST", {**body, "actor_account_id": self.admin_user_a["account_id"]}, 400, str(uuid.uuid4()))
        finally:
            self.cleanup_homework_registry(self.fixtures["homework_registry"])
            if membership_fixture:
                response = self.core(f"/divisions/{division}/memberships/{membership_fixture['account_id']}",
                                     wrong_actor, "DELETE", expected={200, 409})
                membership_fixture["removed"] = response.get("status") is True
                if not membership_fixture["removed"]:
                    membership_fixture["retained_reason"] = response.get("error", "owner_retained_membership")
                self.expect(True, "only newly-added own student membership is removed or explicitly recorded as retained by Core")
            self.save_fixtures()
        self.uploads("/files/" + registered["id"], token, division, expected=404)
        self.uploads("/receipts/" + key, token, division, expected=404)
        after = self.api("homework", f"/v1/homework/divisions/{division}/jobs/{handoff['job_id']}", token)
        self.expect(after == owner, "cleaning only Uploads registry leaves the Homework owner job unchanged")

    def cleanup_homework_registry(self, fixture):
        conn = mysql_connection(self.dev)
        scope = (fixture["id"], fixture["organization_id"], fixture["division_id"], fixture["actor_account_id"])
        try:
            with conn.cursor() as cursor:
                cursor.execute("SELECT type,owner,owner_reference,target_id,source_hash,content FROM uploads_files_v1 "
                               "WHERE id=%s AND organization_id=%s AND division_id=%s AND actor_account_id=%s FOR UPDATE", scope)
                row = cursor.fetchone()
                self.expect(row == ("homework.pdf", "homework", fixture["job_id"], "", "", None),
                            "cleanup guard matches only this run's unmaterialized Homework reference")
                cursor.execute("SELECT COUNT(*) FROM uploads_sessions_v1 WHERE file_id=%s", (fixture["id"],))
                self.expect(cursor.fetchone()[0] == 0, "Homework fixture cleanup cannot delete an import source")
                cursor.execute("DELETE FROM uploads_receipts_v1 WHERE command_id=%s AND resource_id=%s AND action='file.register' "
                               "AND organization_id=%s AND division_id=%s AND actor_account_id=%s", (fixture["command_id"], *scope))
                cursor.execute("DELETE FROM uploads_events_v1 WHERE resource_id=%s AND organization_id=%s AND division_id=%s "
                               "AND actor_account_id=%s AND type='homework.pdf' AND owner='homework' AND action='file.registered'", scope)
                cursor.execute("DELETE FROM uploads_files_v1 WHERE id=%s AND organization_id=%s AND division_id=%s AND actor_account_id=%s "
                               "AND type='homework.pdf' AND owner='homework' AND owner_reference=%s AND content IS NULL", (*scope, fixture["job_id"]))
                self.expect(cursor.rowcount == 1, "cleanup removes exactly one own Uploads Homework registry fixture")
            conn.commit()
            fixture["cleaned"] = True
            self.sql_mutations.append("guarded cleanup of this run's own Uploads Homework registry file/receipt/event")
        except BaseException:
            conn.rollback()
            raise
        finally:
            conn.close()

    def journal_scope_and_context(self):
        for list_path in ["/sessions?limit=200", "/jobs?limit=200"]:
            rows = self.uploads(list_path)
            self.expect(all(x["division_id"] == self.a and x["organization_id"] == 1001
                            and x["actor_account_id"] == self.admin_user_a["account_id"] for x in rows), "journal list scope is original division and actor")
            rows = self.uploads(list_path, self.admin_b, self.b)
            self.expect(all(x["id"] not in self.fixtures["sessions"] + self.fixtures["jobs"] for x in rows), "different cabinet journal cannot inherit original imports")
        events = self.uploads("/events?limit=200")
        self.expect(all(x["scope"]["organization_id"] == 1001 and x["scope"]["division_id"] == self.a for x in events), "every service event retains original organization/division")
        # One real session context transition invalidates its original access token.
        token, _ = self.login("dev_admin", self.a)
        switched = self.request("auth", "/v1/auth/context", token, "POST", {"organization_id": 1001, "division_id": self.b})
        new_token = switched.get("access_token") or switched["token"]
        self.tokens.append(new_token)
        original = self.observed_jobs[0][0]
        self.uploads("/sessions/" + original["id"], token, expected=401)
        self.uploads("/sessions/" + original["id"], new_token, self.b, expected=404)
        self.uploads("/sessions/" + original["id"], new_token, self.a, expected=403)
        returned = self.request("auth", "/v1/auth/context", new_token, "POST", {"organization_id": 1001, "division_id": self.a})
        returned_token = returned.get("access_token") or returned["token"]
        self.tokens.append(returned_token)
        self.expect(self.uploads("/sessions/" + original["id"], returned_token)["id"] == original["id"], "returning to original verified cabinet recovers pinned session")
        revoked, _ = self.login("dev_admin", self.a)
        self.request("auth", "/v1/auth/logout", revoked, "POST", {})
        self.uploads("/jobs/" + self.observed_jobs[0][1]["id"] + "/report", revoked, expected=401)

    def rabbit_topology(self):
        required = ["RABBITMQ_USER", "RABBITMQ_PASSWORD", "RABBITMQ_VHOST", "RABBITMQ_MANAGEMENT_PORT"]
        self.expect(all(k in self.env for k in required), "canonical dev Rabbit management configuration exists")
        self.expect(self.env["RABBITMQ_VHOST"] == "cpm_dev" and str(self.env["RABBITMQ_MANAGEMENT_PORT"]) == "15672", "Rabbit inspection restricted to isolated cpm_dev vhost")
        origin = "http://127.0.0.1:15672"
        path = "/api/queues/" + urllib.parse.quote(self.env["RABBITMQ_VHOST"], safe="")
        authorization = base64.b64encode((self.env["RABBITMQ_USER"] + ":" + self.env["RABBITMQ_PASSWORD"]).encode()).decode()
        request = urllib.request.Request(origin + path, headers={"Authorization": "Basic " + authorization})
        try:
            with urllib.request.urlopen(request, timeout=10) as response:
                queues = json.load(response)
        except (urllib.error.URLError, OSError):
            raise AssertionError("isolated Rabbit management unavailable") from None
        names = {q["name"]: q for q in queues}
        required_queues = self.args.rabbit_queues
        for name in required_queues:
            self.expect(name in names and names[name]["durable"] and not names[name]["auto_delete"], "durable Rabbit queue exists: " + name)
        self.rabbit_evidence = [{"name": name, "durable": names[name]["durable"],
                                 "consumers": names[name].get("consumers", 0), "messages": names[name].get("messages", 0)} for name in required_queues]

    def delivery_storage(self):
        """Read own SQL delivery proofs; never insert jobs or broker messages."""
        conn = mysql_connection(self.dev)
        conn.autocommit = True
        verified = 0
        try:
            for session, job, _ in self.observed_jobs:
                deadline = time.monotonic() + 5
                while True:
                    with conn.cursor() as cursor:
                        cursor.execute("SELECT o.body,o.published_at,o.generation,o.organization_id,o.division_id,o.actor_account_id,"
                                       "i.state,i.completed_at FROM uploads_outbox_v1 o LEFT JOIN uploads_inbox_v1 i ON i.id=o.id AND i.job_id=o.job_id "
                                       "WHERE o.job_id=%s AND o.organization_id=%s AND o.division_id=%s AND o.actor_account_id=%s "
                                       "ORDER BY o.generation DESC,o.created_at DESC LIMIT 1",
                                       (job["id"], session["organization_id"], session["division_id"], session["actor_account_id"]))
                        row = cursor.fetchone()
                    if row and row[1] is not None and row[6] == "done" and row[7] is not None:
                        break
                    if time.monotonic() >= deadline:
                        raise AssertionError("own Rabbit delivery has no durable published outbox/completed inbox proof")
                    time.sleep(.1)
                envelope = json.loads(row[0])
                self.no_credentials(envelope)
                payload = envelope["payload"]
                self.expect(set(payload) == {"organization_id", "division_id", "actor_account_id", "job_id", "session_id",
                                            "generation", "type", "owner", "owner_session_id", "source_hash", "preview_revision", "owner_revision"},
                            "Rabbit command contains only references, pinned scope and revisions")
                self.expect((payload["organization_id"], payload["division_id"], payload["actor_account_id"]) == row[3:6]
                            and payload["job_id"] == job["id"] and payload["session_id"] == session["id"]
                            and payload["source_hash"] == session["source_hash"] and payload["preview_revision"] == session["revision"]
                            and payload["owner_revision"] == session["owner_revision"] and payload["generation"] == row[2],
                            "durable Rabbit receipt preserves the confirmed original source and scope")
                verified += 1
            with conn.cursor() as cursor:
                cursor.execute("SELECT input,preview FROM uploads_previews_v1 WHERE organization_id=%s AND division_id=%s AND actor_account_id=%s",
                               (1001, self.a, self.admin_user_a["account_id"]))
                for raw_input, raw_preview in cursor.fetchall():
                    self.no_credentials(json.loads(raw_input))
                    self.no_credentials(json.loads(raw_preview))
            self.expect(True, "staged Tests storage contains no persisted bearer/service key or credential password")
        finally:
            conn.close()
        self.delivery_evidence = {"own_jobs_with_published_outbox_and_done_inbox": verified,
                                  "broker_payload_contains_domain_data": False, "authorization_tokens_persisted": False}

    def restart_uploads(self):
        name = self.args.restart_container
        if not name:
            return
        self.expect(bool(re.fullmatch(r"[A-Za-z0-9_.-]+", name)), "restart target has safe container name")
        result = subprocess.run([DOCKER, "inspect", name], check=True, capture_output=True, text=True)
        metadata = json.loads(result.stdout)[0]
        self.expect(metadata["Config"].get("Labels", {}).get("com.docker.compose.service") == "uploads", "restart target is only uploads, never broker/owner service")
        self.expect(metadata["Config"].get("Labels", {}).get("com.docker.compose.project") == "cpm-dev",
                    "restart target belongs to this isolated development compose project")
        # Verify durable outcome and original receipt survive the actual process exit.
        session, job, key = self.observed_jobs[-1]
        suffix = "/report" if self.args.background_mode == "live" else ""
        before = self.uploads("/jobs/" + job["id"] + suffix)
        subprocess.run([DOCKER, "restart", name], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline:
            try:
                self.request("uploads", "/health/ready", record=False)
                break
            except AssertionError:
                time.sleep(.3)
        else:
            raise AssertionError("uploads readiness did not recover after own process restart")
        after = self.uploads("/jobs/" + job["id"] + suffix)
        self.expect(before == after, "persisted job outcome survives real uploads restart")
        receipt = self.uploads("/receipts/" + key)
        self.expect(receipt["resource_id"] == job["id"], "original commit receipt survives uploads restart")
        self.job_idempotency(session, job)
        self.checks.append("shared Rabbit/Auth/Core/Cards/Exams were never stopped")

    def logout(self):
        for token in self.tokens:
            try:
                self.request("auth", "/v1/auth/logout", token, "POST", {}, expected={200, 401}, record=False)
            except AssertionError:
                pass

    def run_all(self):
        steps = [("create own synthetic HTTP fixtures", self.fixtures_setup),
                 ("catalog and selected cabinet permissions", self.catalog_and_security),
                 *(([("existing protected Homework registry and cleanup", self.homework_reference)]) if self.args.homework_fixture else []),
                 ("native credential privacy and pending queue", self.credentials),
                 ("legacy users preview and graph", self.legacy_users),
                 ("Cards preview, edit, retry and response loss", self.cards),
                 ("full Exams preview pagination and row diagnostics", self.exams),
                 ("native Tests JSON, OnlineTestPad and external results", self.tests_imports),
                 ("malformed source rejection", self.malformed_sources),
                 ("protected native templates", self.templates),
                 ("real Attendance and optional Homework references", self.attachment_references),
                 ("journal scope and live context revocation", self.journal_scope_and_context),
                 ("isolated Rabbit topology", self.rabbit_topology),
                 ("durable own delivery and credential storage proofs", self.delivery_storage),
                 ("optional uploads process restart", self.restart_uploads)]
        for name, step in steps:
            print("Uploads HTTP acceptance: " + name, flush=True)
            step()
        self.save_fixtures()
        return {"status": "passed", "synthetic_run": self.run, "synthetic_only": True,
                "checks": len(self.checks), "cases": self.checks, "unavailable_capabilities": self.unsupported_types,
                "real_dependencies": ["uploads HTTP", "auth HTTP", "core HTTP", "cards HTTP", "exams HTTP", "tests HTTP", "attendance HTTP", "MySQL read-only privacy audit", "Rabbit management read-only"],
                "rabbit": self.rabbit_evidence,
                "delivery_storage": self.delivery_evidence,
                "process_restart_tested": bool(self.args.restart_container),
                "homework_reference_tested": bool(self.args.homework_fixture),
                "homework_reference_prior_proof": self.homework_prior_evidence,
                "homework_lifecycle_mutated": False,
                "sql_audits_read_only": True,
                "sql_mutations": self.sql_mutations,
                "background_mode": self.args.background_mode,
                "subject_apply_tested": self.args.background_mode == "live",
                "pending_dependencies": ["background_authorization_pending"] if self.args.background_mode == "pending" else [],
                "pending_template_types": self.pending_template_types,
                "browser_or_bff_tested": False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for service, port in [("uploads", 8014), ("auth", 8002), ("core", 8003), ("cards", 8008), ("exams", 8010), ("tests", 8004), ("attendance", 8013), ("homework", 8006)]:
        parser.add_argument("--" + service + "-url", type=local_url, default=f"http://127.0.0.1:{port}")
    parser.add_argument("--require-types", nargs="*", default=["users.credentials", "users.legacy", "cards.xlsx", "exams.questions", "exams.assignments", "exams.outside", "tests.json", "tests.external-results"])
    parser.add_argument("--rabbit-queues", nargs="+", default=["uploads.import.execute.v1", "uploads.import.retry.v1", "uploads.import.dead.v1"])
    parser.add_argument("--restart-container", help="explicit uploads-only compose container; never stops shared broker/owners")
    parser.add_argument("--background-mode", choices=["pending", "live"], default="pending",
                        help="pending tests the durable authorization gate; live requires approved implemented owner apply")
    homework = parser.add_mutually_exclusive_group()
    homework.add_argument("--homework-fixture", help="dev/local synthetic owner job/actor handoff for optional read-only reference check")
    homework.add_argument("--homework-proof", help="private earlier real HTTP Homework proof captured before owner fixture cleanup; clearly reported as prior coverage")
    parser.add_argument("--resume-fixtures", help="reuse this script's private complete fixture graph without catalog/direction mutations")
    args = parser.parse_args()
    suite = Acceptance(args)
    report = ROOT / "dev/local/uploads-http-report.json"
    try:
        result = suite.run_all()
        suite.save(report, result)
        print(f"Uploads real HTTP acceptance: {result['checks']} checks passed; report dev/local/uploads-http-report.json")
    except Exception as error:
        suite.save_fixtures()
        suite.save(report, {"status": "failed", "synthetic_run": suite.run, "checks": len(suite.checks), "cases": suite.checks,
                            "last_request": suite.last_request, "error_type": type(error).__name__,
                            "error": str(error) if isinstance(error, AssertionError) else "Local execution failure; response bodies and credentials omitted",
                            "background_mode": args.background_mode, "subject_apply_tested": False,
                            "homework_lifecycle_mutated": False, "sql_audits_read_only": True,
                            "homework_reference_tested": bool(args.homework_fixture), "homework_reference_prior_proof": suite.homework_prior_evidence,
                            "sql_mutations": suite.sql_mutations,
                            "browser_or_bff_tested": False})
        # A sanitized failure, without traceback locals or owner/body content.
        print(f"Uploads acceptance FAILED ({type(error).__name__}); see dev/local/uploads-http-report.json", file=sys.stderr)
        return 1
    finally:
        suite.logout()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
