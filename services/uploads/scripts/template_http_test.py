"""Focused real owner-template acceptance on an existing private cpm_dev fixture graph.

Download -> inspect -> fill own synthetic data -> protected preview only. This
script never commits an import, creates a catalog fixture, restarts a service,
or overwrites the previous full acceptance/Homework evidence.
"""
from __future__ import annotations

import argparse
from datetime import datetime, timezone
import hashlib
import io
import json
from pathlib import Path
import re
import secrets
import sys
import urllib.parse
from xml.etree import ElementTree as ET
import zipfile

from http_test import Acceptance, ROOT, column, local_url


XLSX_MIME = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
SHEET_NS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
REL_NS = "http://schemas.openxmlformats.org/package/2006/relationships"
DOC_REL_NS = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"


def workbook(content):
    """Read a real downloaded workbook without running formulas or external links."""
    with zipfile.ZipFile(io.BytesIO(content)) as archive:
        names = archive.namelist()
        if len(names) != len(set(names)) or sum(x.file_size for x in archive.infolist()) > 32 << 20:
            raise AssertionError("template workbook has duplicate entries or excessive expanded size")
        for name in names:
            if name.startswith("/") or ".." in Path(name).parts or "\\" in name:
                raise AssertionError("template workbook has an unsafe archive path")
            if any(part in name.lower() for part in ("vbaproject", "externallinks/", "embeddings/")):
                raise AssertionError("template workbook contains executable or external material")
        files = {name: archive.read(name) for name in names}
    for name, raw in files.items():
        if name.endswith((".xml", ".rels")):
            if b"<!DOCTYPE" in raw.upper() or b"<!ENTITY" in raw.upper():
                raise AssertionError("template workbook contains XML external entity declarations")
            root = ET.fromstring(raw)
            if name.endswith(".rels") and any(e.attrib.get("TargetMode") == "External" for e in root):
                raise AssertionError("template workbook contains an external relationship")
    shared = []
    if "xl/sharedStrings.xml" in files:
        strings = ET.fromstring(files["xl/sharedStrings.xml"])
        shared = ["".join(si.itertext()) for si in strings]
    book = ET.fromstring(files["xl/workbook.xml"])
    sheet = book.find(f"{{{SHEET_NS}}}sheets/{{{SHEET_NS}}}sheet")
    if sheet is None:
        raise AssertionError("template workbook has no worksheet")
    relation_id = sheet.attrib[f"{{{DOC_REL_NS}}}id"]
    relations = ET.fromstring(files["xl/_rels/workbook.xml.rels"])
    target = next(e.attrib["Target"] for e in relations if e.attrib["Id"] == relation_id)
    sheet_path = target.lstrip("/") if target.startswith("/") else "xl/" + target
    tree = ET.fromstring(files[sheet_path])
    if tree.find(f".//{{{SHEET_NS}}}f") is not None:
        raise AssertionError("template workbook contains a formula")
    data = tree.find(f"{{{SHEET_NS}}}sheetData")
    if data is None or not list(data):
        raise AssertionError("template workbook has no header row")
    headers = []
    for cell in data[0]:
        text = cell.find(f"{{{SHEET_NS}}}v")
        if cell.attrib.get("t") == "s":
            value = shared[int(text.text)]
        elif cell.attrib.get("t") == "inlineStr":
            value = "".join(cell.find(f"{{{SHEET_NS}}}is").itertext())
        else:
            value = text.text if text is not None else ""
        headers.append((cell.attrib["r"], value))
    return files, sheet_path, tree, data, headers


def fill_workbook(content, row, header_changes=None):
    """Retain the owner's actual OOXML/header, replacing only example data rows."""
    files, sheet_path, tree, data, headers = workbook(content)
    header = data[0]
    for index, value in (header_changes or {}).items():
        cell = header[index]
        for child in list(cell):
            cell.remove(child)
        cell.attrib["t"] = "inlineStr"
        ET.SubElement(ET.SubElement(cell, f"{{{SHEET_NS}}}is"), f"{{{SHEET_NS}}}t").text = value
    for old in list(data)[1:]:
        data.remove(old)
    number = int(header.attrib.get("r", "1")) + 1
    new = ET.SubElement(data, f"{{{SHEET_NS}}}row", {"r": str(number)})
    for index, value in enumerate(row, 1):
        cell = ET.SubElement(new, f"{{{SHEET_NS}}}c", {"r": column(index) + str(number)})
        if isinstance(value, (int, float)):
            ET.SubElement(cell, f"{{{SHEET_NS}}}v").text = str(value)
        else:
            cell.attrib["t"] = "inlineStr"
            text = ET.SubElement(ET.SubElement(cell, f"{{{SHEET_NS}}}is"), f"{{{SHEET_NS}}}t")
            text.text = str(value)
    dimension = tree.find(f"{{{SHEET_NS}}}dimension")
    if dimension is not None:
        dimension.attrib["ref"] = f"A1:{column(max(len(row), len(headers)))}{number}"
    files[sheet_path] = ET.tostring(tree, encoding="utf-8", xml_declaration=True)
    stream = io.BytesIO()
    with zipfile.ZipFile(stream, "w", zipfile.ZIP_DEFLATED) as archive:
        for name, raw in files.items():
            archive.writestr(name, raw)
    return stream.getvalue(), number


class TemplateAcceptance(Acceptance):
    def __init__(self, args):
        super().__init__(args)
        self.template_run = secrets.token_hex(6)
        self.created = {"sessions": [], "files": [], "owner_previews": []}
        self.template_evidence = []
        self.source_fixture_path = Path(args.resume_fixtures).resolve()
        self.source_fixture_hash = hashlib.sha256(self.source_fixture_path.read_bytes()).hexdigest()
        self.prior_evidence_hashes = {
            path: hashlib.sha256(path.read_bytes()).hexdigest()
            for path in [ROOT / "dev/local/uploads-http-report.json", ROOT / "dev/local/uploads-homework-http-proof.json"]
        }

    def save_fixtures(self):
        # Acceptance.resume_fixtures/create_session call this method. Keep their
        # modifications only in memory and record newly created preview IDs in a
        # separate manifest; the previous fixture/evidence files stay untouched.
        self.save(ROOT / "dev/local/uploads-template-http-fixtures.json", {
            "synthetic_only": True, "synthetic_run": self.run, "template_run": self.template_run,
            "created": self.created, "direction_ids": [getattr(self, "direction", None), getattr(self, "foreign_direction", None)],
            "subject_apply": False, "catalog_mutations": False})

    def request(self, service, path, token=None, method="GET", *args, **kwargs):
        if method != "GET":
            allowed = ((service == "auth" and path in {"/v1/auth/login", "/v1/auth/logout"})
                       or (service == "uploads" and re.fullmatch(r"/v1/uploads/divisions/\d+/sessions", path))
                       or (service == "core" and re.fullmatch(r"/v1/core/divisions/\d+/legacy-imports", path))
                       or (service == "tests" and re.fullmatch(r"/v1/tests/divisions/\d+/imports/json/preview", path))
                       or (service == "tests" and path == "/internal/tests/external-results/validate"))
            if method != "POST" or not allowed:
                raise AssertionError("focused template suite attempted an unauthorized mutation")
        return super().request(service, path, token, method, *args, **kwargs)

    def create_session(self, *args, **kwargs):
        session = super().create_session(*args, **kwargs)
        if (kwargs.get("expected", 201)) < 300:
            self.created["sessions"].append(session["id"])
            if session.get("file_id"):
                self.created["files"].append(session["file_id"])
            self.save_fixtures()
        return session

    def download(self, kind, owner_service, owner_path, query="", extension="xlsx", mime=XLSX_MIME, token=None):
        path = f"/v1/uploads/divisions/{self.a}/templates/{kind}" + query
        _, content, headers = self.request("uploads", path, token or self.admin_a, binary=True)
        _, native, owner_headers = self.request(owner_service, owner_path, token or self.admin_a, binary=True)
        self.expect(0 < len(content) <= 8 << 20, kind + " template obeys source byte limits")
        self.expect(content == native, kind + " Uploads forwards exact protected native template bytes")
        self.expect(headers.get("Content-Type", "").split(";")[0] == mime, kind + " template has its source MIME")
        self.expect(owner_headers.get("Content-Type", "").split(";")[0] == mime,
                    kind + " native owner advertises the same source MIME")
        self.expect("attachment" in headers.get("Content-Disposition", "") and ("." + extension) in headers.get("Content-Disposition", ""),
                    kind + " template has a usable attachment filename")
        self.expect("no-store" in headers.get("Cache-Control", ""), kind + " template prevents shared caching")
        self.expect("no-store" in owner_headers.get("Cache-Control", ""), kind + " native template prevents shared caching")
        if extension == "xlsx":
            files, _, _, _, columns = workbook(content)
            inspected = "\n".join(raw.decode("utf-8", "replace") for name, raw in files.items() if name.endswith((".xml", ".rels")))
            self.expect(bool(columns), kind + " owner workbook has readable actual headers")
        else:
            inspected = content.decode("utf-8")
        self.no_credentials(inspected)
        self.expect(all(login not in inspected for _, login in self.students), kind + " template contains no real pupil login")
        self.template_evidence.append({"type": kind, "extension": extension, "bytes": len(content),
                                       "sha256": hashlib.sha256(content).hexdigest(), "owner_passthrough": True})
        return content

    def protected_downloads(self, kind, owner_service, owner_path, query="", allowed_staff="tests", deny_staff=None):
        path = f"/v1/uploads/divisions/{self.a}/templates/{kind}" + query
        for token in [None, self.student, self.staff["upload_only"]["token"], self.staff[deny_staff or "cards"]["token"]]:
            for service, route in [("uploads", path), (owner_service, owner_path)]:
                _, raw, headers = self.request(service, route, token, expected=401 if token is None else 403, binary=True)
                self.expect("Content-Disposition" not in headers and json.loads(raw).get("status") is False,
                            service + " protected template rejection remains a JSON error without attachment headers")
        # Existing foreign/other cabinet actors cannot choose this division path.
        for token in [self.admin_b, self.foreign_c]:
            self.request("uploads", path, token, expected={403, 404}, binary=True)
        self.request("uploads", path, self.staff[allowed_staff]["token"], binary=True)
        self.request(owner_service, owner_path, self.staff[allowed_staff]["token"], binary=True)

    def prepare(self):
        self.resume_fixtures()
        self.expect((self.direction, self.foreign_direction) == (1058, 1059),
                    "focused suite reuses frozen directions 1058/1059 without mutations")
        self.own_suffix = "Template" + self.template_run

    def run_templates(self):
        catalog = self.uploads("/catalog")
        types = {item["type"]: item for item in catalog["types"]}
        for kind in ["users.legacy", "tests.json", "tests.external-results"]:
            self.expect(types[kind]["template"] is True, kind + " catalog advertises its protected native template")
        for kind in ["cards.transform", "homework.pdf", "attendance.zap-images"]:
            self.expect(types[kind]["template"] is False, kind + " catalog explicitly has no file template")
            result = self.uploads("/templates/" + kind, expected=400)
            self.expect(result["error"] == "template_not_applicable", kind + " reports an inapplicable template")
        print("Uploads focused templates: Core legacy workbook", flush=True)
        self.legacy_template()
        path = f"/v1/tests/divisions/{self.a}/imports/json/template"
        for format in ["cpm_json", "online_test_pad"]:
            print("Uploads focused templates: Tests " + format, flush=True)
            self.json_template(path, format)
        self.expect(bool(self.fixtures.get("external_tests")), "focused external template uses an existing own synthetic native target")
        target = self.fixtures["external_tests"][-1]
        path = f"/v1/tests/divisions/{self.a}/external-tests/{target}/imports/results/template"
        print("Uploads focused templates: Tests external workbook and exact identity columns", flush=True)
        self.external_template(path, target)

    def legacy_template(self):
        path = f"/v1/core/divisions/{self.a}/legacy-imports/template"
        content = self.download("users.legacy", "core", path)
        self.protected_downloads("users.legacy", "core", path, allowed_staff="users")
        headers = [value for _, value in workbook(content)[4]]
        self.expect(headers == ["ФИО", "Класс", "Школа", "Проктор", "Telegram"],
                    "Core legacy template uses its five native source columns without credentials or IDs")
        name = self.own_suffix + " Learner"
        row = [name, 7, "", "", "@synthetic"]
        filled, physical_row = fill_workbook(content, row)
        query = urllib.parse.quote(name)
        self.expect(not self.core(f"/divisions/{self.a}/users?q={query}"), "own template pupil does not exist before preview")
        session = self.create_session("users.legacy", filled, token=self.staff["users"]["token"])
        self.scoped(session, actor=self.staff["users"]["account"])
        domain = session["preview"]["preview"]
        self.expect(domain["can_commit"] and domain["summary"]["students_create"] == 1,
                    "filled downloaded legacy template produces one valid native proposed pupil")
        self.expect(domain["students"][0]["row"] == physical_row and domain["students"][0]["full_name"] == name,
                    "filled legacy template preserves physical source row and own synthetic name")
        native = self.owner("core", f"/divisions/{self.a}/legacy-imports", token=self.staff["users"]["token"],
                            method="POST", body={"rows": [{"row": physical_row, "full_name": name, "class_raw": "7",
                            "school_name": "", "proctor_name": "", "tg_name": "@synthetic"}]}, expected=201)
        self.created["owner_previews"].append({"owner": "core", "id": native["id"]})
        self.save_fixtures()
        self.expect(native["preview"] == domain, "filled Core workbook and explicit native preview have identical domain diagnostics")
        self.expect(not self.core(f"/divisions/{self.a}/users?q={query}"), "template previews create no subject pupil")
        self.request("uploads", f"/v1/uploads/divisions/{self.a}/templates/users.legacy?target_id=1", self.admin_a,
                     expected=400, binary=True)
        self.request("core", path + "?unexpected=true", self.admin_a, expected=400, binary=True)
        self.create_session("users.legacy", filled, filename="synthetic.csv", expected=415)
        self.create_session("users.legacy", b"0" * ((8 << 20) + 1), expected=413)

    def json_template(self, owner_path, format):
        query = "?" + urllib.parse.urlencode({"direction_id": self.direction, "format": format})
        otp = format == "online_test_pad"
        extension, mime = ("js", "text/javascript") if otp else ("json", "application/json")
        content = self.download("tests.json", "tests", owner_path + query, query, extension, mime,
                                token=self.staff["tests"]["token"])
        self.protected_downloads("tests.json", "tests", owner_path + query, query)
        schema = self.api("tests", owner_path.removesuffix("template") + "schema" + query, self.staff["tests"]["token"])
        self.expect(schema["capability"] == "supported" and schema["format"] == format
                    and schema["organization_id"] == 1001 and schema["division_id"] == self.a
                    and schema["direction_id"] == self.direction and schema["filename"].endswith("." + extension)
                    and schema["mime"].split(";")[0] == mime,
                    format + " native schema describes the same scoped downloadable source contract")
        before = self.owner("tests", f"/divisions/{self.a}/tests")["total"]
        fields = {"direction_id": str(self.direction)}
        if otp:
            text = content.decode("utf-8").strip()
            self.expect(text.startswith("var test = ") and text.endswith(";"),
                        "native OnlineTestPad template contains one JSON object wrapper without executable logic")
            payload = json.loads(text[len("var test = "):-1])
            payload["Name"] = self.own_suffix + " OTP"
            payload["Questions"][0]["Text"] = self.own_suffix + " own question"
            filled = ("var test = " + json.dumps(payload, ensure_ascii=False) + ";\n").encode()
            fields.update(title=payload["Name"], direction="DEV Uploads " + self.run,
                          startDate="2099-01-01T10:00", endDate="2099-01-01T11:00",
                          published="false", visible="false", timeLimitMinutes="30")
            native_payload = {"importFormat": "online_test_pad", "sourceText": filled.decode(),
                              "metadata": {"title": payload["Name"], "direction": fields["direction"],
                                           "startDate": fields["startDate"], "endDate": fields["endDate"],
                                           "published": False, "visible": False, "timeLimitMinutes": 30}}
        else:
            payload = json.loads(content)
            self.expect(payload["direction"] == "DEV Uploads " + self.run
                        and {q["type"] for q in payload["questions"]} == {"single", "multiple", "text"},
                        "native CPM JSON template names the selected direction and demonstrates all supported question types")
            payload["title"] = self.own_suffix + " JSON"
            payload["questions"][0]["text"] = self.own_suffix + " own question"
            filled = json.dumps(payload, ensure_ascii=False).encode()
            native_payload = payload
        session = self.create_session("tests.json", filled, fields, filename="synthetic." + extension,
                                      token=self.staff["tests"]["token"])
        self.scoped(session, actor=self.staff["tests"]["account"])
        domain = session["preview"]["preview"]
        self.expect(domain["valid"] and domain["summary"]["questions_total"] == 3,
                    "filled downloaded " + format + " template validates all three native question examples")
        native = self.owner("tests", f"/divisions/{self.a}/imports/json/preview", token=self.staff["tests"]["token"],
                            method="POST", body={"organization_id": 1001, "division_id": self.a,
                            "direction_id": self.direction, "payload": native_payload})
        self.expect(domain == native, format + " file preview exactly matches protected native Tests preview")
        self.expect(self.owner("tests", f"/divisions/{self.a}/tests")["total"] == before,
                    format + " template previews create no subject test")
        self.create_session("tests.json", filled, fields, filename="synthetic.txt", expected=415)
        self.request("tests", owner_path + "?direction_id=0", self.admin_a, expected=400, binary=True)
        self.request("uploads", f"/v1/uploads/divisions/{self.a}/templates/tests.json?direction_id={self.direction}&format=invalid",
                     self.admin_a, expected=400, binary=True)
        self.request("tests", owner_path + f"?direction_id={self.foreign_direction}", self.admin_a,
                     expected={403, 404}, binary=True)

    def external_template(self, owner_path, target):
        base = f"/divisions/{self.a}/external-tests/{target}"
        metadata = self.owner("tests", base)
        query = "?target_id=" + target
        token = self.staff["test_results"]["token"]
        content = self.download("tests.external-results", "tests", owner_path, query, token=token)
        self.protected_downloads("tests.external-results", "tests", owner_path, query,
                                 allowed_staff="test_results", deny_staff="tests")
        schema = self.api("tests", owner_path.removesuffix("template") + "schema", token)
        self.expect(schema["capability"] == "supported" and schema["test_id"] == target
                    and schema["organization_id"] == 1001 and schema["division_id"] == self.a
                    and schema["mime"] == XLSX_MIME,
                    "external native schema belongs to this exact live target and source MIME")
        _, sample_bytes, sample_headers = self.request("tests", owner_path.removesuffix("template") + "sample", token, binary=True)
        sample = json.loads(sample_bytes)
        self.no_credentials(sample)
        self.expect(sample["test_id"] == target and sample["expected_test_revision"] == metadata["revision"]
                    and sample["organization_id"] == 1001 and sample["division_id"] == self.a
                    and all(not row.get("account_id") and not row.get("legacy_student_id") for row in sample["rows"]),
                    "external native sample pins scope/revision and exports only fictional rows without pupil IDs")
        self.expect(sample_headers.get("Content-Type", "").startswith("application/json")
                    and "attachment" in sample_headers.get("Content-Disposition", ""),
                    "external native JSON sample stays a protected file rather than an envelope")
        headers = [value for _, value in workbook(content)[4]]
        self.expect(headers == ["ФИО", "Процент правильных ответов (%)", "Количество правильных ответов",
                                "Дата завершения", "Потрачено времени", "Логин", "account_id", "legacy_student_id"],
                    "external results template documents native scores, matching fields and distinct identities")
        before = self.owner("tests", base + "/results")["total"]
        account, login = self.students[0]
        name = self.person_names[account]
        profile = self.core(f"/divisions/{self.a}/users/{account}")["profile_id"]
        self.expect(account != profile, "own pupil has distinct Auth account and Core legacy profile identities")
        own_row = [name, 0, "", "", "", login, str(account), str(profile)]
        filled, physical_row = fill_workbook(content, own_row)
        session = self.create_session("tests.external-results", filled, {"target_id": target}, token=token)
        self.scoped(session, actor=self.staff["test_results"]["account"])
        domain = session["preview"]["preview"]
        self.expect(domain["can_commit"] and domain["summary"]["import_rows"] == 1
                    and domain["rows"][0]["account_id"] == account and domain["rows"][0]["legacy_student_id"] == profile
                    and domain["rows"][0]["percent"] == 0,
                    "filled external template retains distinct explicit owner identities and a zero percentage")
        self.expect(domain["rows"][0]["row"] == physical_row and domain["revision"] == metadata["revision"],
                    "external template preview preserves physical row and current native target revision")
        row = domain["rows"][0]
        native_row = {key: value for key, value in row.items() if key in
                      {"row_id", "row", "full_name", "account_id", "legacy_student_id", "percent", "correct_count", "completed_at", "time_spent", "login"}}
        body = {"organization_id": 1001, "division_id": self.a, "test_id": target,
                "expected_test_revision": metadata["revision"], "rows": [native_row]}
        key = self.env["AUTH_INTERNAL_KEY"]
        native = self.api("tests", "/internal/tests/external-results/validate", token, "POST", body,
                          headers={"X-Service-Key": key})
        self.expect(native == domain, "filled external workbook equals existing protected native owner validation")
        self.request("tests", "/internal/tests/external-results/validate", token, "POST", body, expected=401)
        self.request("tests", "/internal/tests/external-results/validate", None, "POST", body,
                     expected=401, headers={"X-Service-Key": key})
        for denied in [self.student, self.staff["tests"]["token"], self.staff["upload_only"]["token"]]:
            self.request("tests", "/internal/tests/external-results/validate", denied, "POST", body,
                         expected=403, headers={"X-Service-Key": key})
        self.expect(self.owner("tests", base + "/results")["total"] == before,
                    "external template preview/validation writes no pupil result")
        fractional_row = own_row.copy()
        fractional_row[1] = 87.125
        fractional, _ = fill_workbook(content, fractional_row)
        fractional_preview = self.create_session("tests.external-results", fractional, {"target_id": target}, token=token)["preview"]["preview"]
        self.expect(fractional_preview["can_commit"] and fractional_preview["rows"][0]["percent"] == 87.125,
                    "explicit identity columns retain a fractional owner percentage without rounding or scaling")
        self.request("uploads", f"/v1/uploads/divisions/{self.a}/templates/tests.external-results", self.admin_a,
                     expected=400, binary=True)
        self.request("uploads", f"/v1/uploads/divisions/{self.c}/templates/tests.external-results" + query,
                     self.foreign_c, expected={403, 404}, binary=True)
        mismatched, _ = fill_workbook(content, own_row[:7] + [str(profile + 1000000)])
        mismatch = self.create_session("tests.external-results", mismatched, {"target_id": target}, token=token)["preview"]["preview"]
        self.expect(not mismatch["can_commit"] and any(error["code"] == "student_account_mismatch"
                    and error["field"] == "legacy_student_id" for error in mismatch["rows"][0]["errors"]),
                    "conflicting explicit IDs remain inspectable and owner blocks the complete import")
        large = 9007199254740993
        huge_row = own_row[:6] + [str(large), ""]
        huge, _ = fill_workbook(content, huge_row)
        precision_session = self.create_session("tests.external-results", huge, {"target_id": target}, token=token)
        precision = precision_session["preview"]["preview"]
        self.expect(not precision["can_commit"] and precision["rows"][0]["account_id"] == large
                    and any(error["code"] == "student_not_in_scope" for error in precision["rows"][0]["errors"]),
                    "large text ID reaches owner exactly above 2^53 and cannot fall back to matching name/login")
        reloaded = self.uploads("/sessions/" + precision_session["id"], token)["preview"]["preview"]
        self.expect(reloaded == precision and reloaded["rows"][0]["account_id"] == large,
                    "large exact source ID and validation fingerprint survive durable preview reload")
        for invalid in ["0", "7.5"]:
            bad, _ = fill_workbook(content, own_row[:6] + [invalid, ""])
            result = self.create_session("tests.external-results", bad, {"target_id": target}, token=token, expected=422)
            self.expect(result["error"] == "invalid_source_cell_2_account_id", "explicit source ID rejects zero/fraction: " + invalid)
        unsafe_numeric, _ = fill_workbook(content, own_row[:6] + [large, ""])
        result = self.create_session("tests.external-results", unsafe_numeric, {"target_id": target}, token=token, expected=422)
        self.expect(result["error"] == "invalid_source_cell_2_account_id_use_text",
                    "Excel numeric ID above 2^53 requires a text cell rather than accepting lost precision")
        duplicate, _ = fill_workbook(content, own_row, header_changes={7: "account_id"})
        result = self.create_session("tests.external-results", duplicate, {"target_id": target}, token=token, expected=422)
        self.expect(result["error"] == "duplicate_source_header_1_account_id",
                    "duplicate explicit ID columns are rejected before fallback matching")
        self.expect(self.owner("tests", base + "/results")["total"] == before,
                    "valid, mismatched and malformed identity template sources write no subject grades")

    def report(self, status, **extra):
        return {"status": status, "checked_at": datetime.now(timezone.utc).isoformat(),
                "synthetic_only": True, "synthetic_run": self.run, "template_run": self.template_run,
                "checks": len(self.checks), "cases": self.checks, "templates": self.template_evidence,
                "direction_ids": [getattr(self, "direction", None), getattr(self, "foreign_direction", None)],
                "subject_apply_tested": False, "import_commit_sent": False, "catalog_mutations": False,
                "sql_mutations": [], "browser_or_bff_tested": False, **extra}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for service, port in [("uploads", 8014), ("auth", 8002), ("core", 8003), ("cards", 8008), ("exams", 8010), ("tests", 8004)]:
        parser.add_argument("--" + service + "-url", type=local_url, default=f"http://127.0.0.1:{port}")
    parser.add_argument("--resume-fixtures", default=str(ROOT / "dev/local/uploads-http-fixtures.json"))
    parser.set_defaults(homework_fixture=None, homework_proof=None, background_mode="pending")
    args = parser.parse_args()
    suite = TemplateAcceptance(args)
    report = ROOT / "dev/local/uploads-template-http-report.json"
    try:
        suite.prepare()
        suite.run_templates()
        suite.expect(hashlib.sha256(suite.source_fixture_path.read_bytes()).hexdigest() == suite.source_fixture_hash,
                     "previous full acceptance fixtures remain byte-for-byte unchanged")
        suite.expect(all(hashlib.sha256(path.read_bytes()).hexdigest() == digest
                         for path, digest in suite.prior_evidence_hashes.items()),
                     "839-check full report and prior 57-check Homework proof remain byte-for-byte unchanged")
        suite.save(report, suite.report("passed"))
        print(f"Uploads focused real template HTTP acceptance: {len(suite.checks)} checks passed; report dev/local/uploads-template-http-report.json")
    except Exception as error:
        suite.save(report, suite.report("failed", last_request=suite.last_request, error_type=type(error).__name__,
                   error=str(error) if isinstance(error, AssertionError) else "Local execution failure; response bodies and credentials omitted"))
        print("Uploads focused template acceptance FAILED; see dev/local/uploads-template-http-report.json", file=sys.stderr)
        return 1
    finally:
        suite.logout()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
