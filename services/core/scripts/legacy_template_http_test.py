"""Targeted native Core legacy-template acceptance on fresh synthetic scopes.

Requires --run after explicit coordinator CoreREADY. No SQL, runtime control,
import commit, account deletion, automatic command replay or existing-role edits.
All tokens/passwords remain in memory. Unknown commands retain their original
private manifest entry and prevent starting another run.
"""
from __future__ import annotations

import argparse
import hashlib
import io
import json
from pathlib import Path
import secrets
import urllib.error
import urllib.request
import uuid
import xml.etree.ElementTree as ET
import zipfile

ROOT = Path(__file__).resolve().parents[3]
CORE, AUTH = "http://127.0.0.1:8003", "http://127.0.0.1:8002"
PATH = "/v1/core/divisions/{division}/legacy-imports/template"
MIME = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
HEADERS = ["ФИО", "Класс", "Школа", "Проктор", "Telegram"]
MANIFEST = ROOT / "dev/local/core-legacy-template-demo.json"
REPORT = ROOT / "dev/local/core-legacy-template-http-report.json"
FILE = ROOT / "dev/local/core-users-legacy-template.xlsx"
NS = {"m": "http://schemas.openxmlformats.org/spreadsheetml/2006/main"}


class NoRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *_args, **_kwargs):
        raise AssertionError("Fixed loopback API attempted a redirect")


def save(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")
    path.chmod(0o600)


def workbook_rows(file):
    """Read the actual downloaded active sheet, including Excelize shared strings."""
    with zipfile.ZipFile(io.BytesIO(file)) as archive:
        assert len(archive.infolist()) < 40 and sum(part.file_size for part in archive.infolist()) < 1 << 20, "Oversized template archive"
        assert all("vba" not in name.lower() and "externalLink" not in name for name in archive.namelist()), "Unexpected active workbook contents"
        book = ET.fromstring(archive.read("xl/workbook.xml"))
        sheets = book.findall("m:sheets/m:sheet", NS)
        assert len(sheets) == 1 and sheets[0].get("name") == "Ученики", "Unexpected template sheet"
        view = book.find("m:bookViews/m:workbookView", NS)
        assert view is None or view.get("activeTab", "0") == "0", "Wrong active template sheet"
        relation = sheets[0].get("{http://schemas.openxmlformats.org/officeDocument/2006/relationships}id")
        relationships = ET.fromstring(archive.read("xl/_rels/workbook.xml.rels"))
        matching = [item for item in relationships if item.get("Id") == relation]
        assert len(matching) == 1 and matching[0].get("TargetMode") != "External", "Invalid sheet relationship"
        target = matching[0].get("Target", "")
        assert ".." not in target and target.endswith(".xml"), "Invalid template sheet target"
        sheet_path = target.lstrip("/") if target.startswith("/") else "xl/" + target
        strings = []
        if "xl/sharedStrings.xml" in archive.namelist():
            strings = ["".join(item.itertext()) for item in ET.fromstring(archive.read("xl/sharedStrings.xml")).findall("m:si", NS)]
        sheet = ET.fromstring(archive.read(sheet_path))
        assert not sheet.findall(".//m:f", NS), "Formula in template"
        rows = []
        for row in sheet.findall("m:sheetData/m:row", NS):
            cells = {}
            for cell in row.findall("m:c", NS):
                reference = cell.get("r", "")
                assert reference[:1] in "ABCDE" and reference[1:].isdigit(), "Unexpected template column"
                index = ord(reference[0]) - ord("A")
                assert index not in cells, "Duplicate template cell"
                value = cell.find("m:v", NS)
                text = "" if value is None else value.text or ""
                if cell.get("t") == "s":
                    text = strings[int(text)]
                elif cell.get("t") == "inlineStr":
                    inline = cell.find("m:is", NS)
                    text = "" if inline is None else "".join(inline.itertext())
                cells[index] = text
            rows.append([cells.get(index, "") for index in range(5)])
        assert len(rows) == 2 and rows[0] == HEADERS, "Template does not match the actual old five-column format"
        return rows


def no_secrets(value):
    if isinstance(value, dict):
        assert not {"password", "password_hash", "hash", "token", "access_token", "refresh_token", "login", "authorization"} & value.keys(), "Credentials in public preview DTO"
        for child in value.values():
            no_secrets(child)
    elif isinstance(value, list):
        for child in value:
            no_secrets(child)


class Native:
    def __init__(self, check):
        self.http = urllib.request.build_opener(NoRedirects())
        self.check = check
        self.last_status = None
        self.tokens = []

    def request(self, base, path, token=None, method="GET", body=None, expected=200, key=None, binary=False):
        assert base in {CORE, AUTH} and path.startswith("/") and not path.startswith("//")
        headers = {"Authorization": "Bearer " + token} if token else {}
        raw = None if body is None else json.dumps(body, ensure_ascii=False).encode()
        if raw is not None:
            headers["Content-Type"] = "application/json"
        if key:
            headers["Idempotency-Key"] = key
        self.last_status = None
        req = urllib.request.Request(base + path, method=method, data=raw, headers=headers)
        try:
            response = self.http.open(req, timeout=30)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            status, data, headers = response.code, response.read((1 << 20) + 1), response.headers
        self.last_status = status
        self.check(status == expected, f"{method} {path.split('?')[0]} expected{expected} got{status}")
        assert len(data) <= 1 << 20, "Oversized native response"
        if binary and status == 200:
            return data, headers
        value = json.loads(data)
        if status < 300 and base == CORE and path.startswith("/v1/core/"):
            self.check(value.get("status") is True, "native Core success envelope")
        return value.get("data", value)

    def core(self, path, token=None, **options):
        return self.request(CORE, "/v1/core" + path, token, **options)

    def login(self, name, password, division=1001, organization=1001):
        result = self.request(AUTH, "/v1/auth/login", method="POST", body={"login": name, "password": password, "organization_id": organization, "division_id": division})
        token = result["token"]
        self.tokens.append(token)
        return token


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run", action="store_true")
    if not parser.parse_args().run:
        print(json.dumps({"status": "pending_explicit_CoreREADY", "live_checks": 0}))
        return
    if MANIFEST.exists() and any(item["status"] == "pending" for item in json.loads(MANIFEST.read_text()).get("commands", [])):
        raise SystemExit("Resolve the original uncertain command before creating another synthetic run")
    checks = []
    report = {"status": "failed", "checks": checks, "transport": "public native HTTP", "synthetic_only": True, "direct_database_access": False, "runtime_control": False, "import_commit": False}
    demo = {"run": secrets.token_hex(5), "organization_id": 1001, "foreign_organization_id": 2001, "readiness": "preparing", "commands": [], "actors": {}}
    demo["prefix"] = "DEV Core template " + demo["run"]

    def check(condition, label):
        assert condition, label
        checks.append(label)

    native = Native(check)

    def command(path, token, body, method="POST", expected=201, idempotent=False):
        item = {"route": path, "method": method, "status": "pending", "request_sha256": hashlib.sha256(json.dumps(body, ensure_ascii=False).encode()).hexdigest()}
        if idempotent:
            item["key"] = str(uuid.uuid4())
        demo["commands"].append(item)
        save(MANIFEST, demo)
        try:
            out = native.core(path, token, method=method, body=body, expected=expected, key=item.get("key"))
        except Exception:
            if native.last_status is not None and 400 <= native.last_status < 500:
                item["status"], item["http_status"] = "rejected", native.last_status
                save(MANIFEST, demo)
            raise
        item["status"], item["http_status"] = "confirmed", native.last_status
        save(MANIFEST, demo)
        return out

    try:
        spec = native.request(CORE, "/openapi.json")
        check(MIME in spec["paths"][PATH]["get"]["responses"]["200"]["content"], "deployed native OpenAPI exposes real XLSX template before fixture writes")
        password = json.loads((ROOT / "dev/local/accounts.json").read_text())["password"]
        admin = native.login("dev_admin", password)
        foreign = native.login("dev_admin_math", password, 2001, 2001)
        for label in ("a", "b"):
            demo["division_" + label] = command("/divisions", admin, {"name": demo["prefix"] + " " + label.upper()})["id"]
        admin = native.login("dev_admin", password, demo["division_a"])
        for label in ("editor", "viewer", "upload_only"):
            permissions = {section: {"view": True, "edit": label != "viewer"} for section in (["upload"] if label == "upload_only" else ["upload", "users"])}
            name = demo["prefix"] + " " + label
            role = command("/roles", admin, {"name": name, "permissions": permissions})["id"]
            login = "template_" + demo["run"] + "_" + label
            user = command(f"/divisions/{demo['division_a']}/users", admin, {"role": "staff_admin", "full_name": name, "role_id": role, "login": login, "password": password})
            demo["actors"][label] = {"account_id": user["account_id"], "login": login, "role_id": role, "role_name": name, "permissions": permissions}
        student_login = "template_" + demo["run"] + "_student"
        student_id = command(f"/divisions/{demo['division_a']}/users", admin, {"role": "student", "full_name": demo["prefix"] + " student", "class": 8, "login": student_login, "password": password})["account_id"]
        demo["actors"]["student"] = {"account_id": student_id, "login": student_login}
        tokens = {label: native.login(actor["login"], password, demo["division_a"]) for label, actor in demo["actors"].items()}
        tail = f"/divisions/{demo['division_a']}/legacy-imports/template"
        file, headers = native.core(tail, admin, binary=True)
        check(headers.get("Content-Type") == MIME and headers.get("Content-Disposition") == 'attachment; filename="users-legacy-template.xlsx"', "native response has exact XLSX MIME and filename")
        check(headers.get("Cache-Control") == "no-store" and headers.get("X-Content-Type-Options") == "nosniff", "template download is private and cannot be interpreted as JSON")
        rows = workbook_rows(file)
        check(rows[1] == ["Примеров Ученик", "7", "Пример школы", "Примеров Проктор", "@example_student"], "actual downloaded active sheet roundtrips exact five headers and fixed synthetic sample")
        FILE.write_bytes(file)
        FILE.chmod(0o600)
        demo["template_sha256"] = hashlib.sha256(file).hexdigest()
        source = {"row": 2, "full_name": rows[1][0], "class_raw": rows[1][1], "school_name": rows[1][2], "proctor_name": rows[1][3], "tg_name": rows[1][4]}
        base = tail.removesuffix("/template")
        preview = command(base, admin, {"rows": [source]}, idempotent=True)
        demo["admin_preview_id"] = preview["id"]
        summary = preview["preview"]["summary"]
        check(preview["status"] == "preview" and preview["division_id"] == demo["division_a"] and preview["organization_id"] == 1001 and preview["preview"]["can_commit"], "actual sample enters native actor/org/div-bound preview without applying subjects")
        check(summary["students_create"] == 1 and summary["proctors_create"] == 1 and summary["groups_create"] == 1 and summary["schools_create"] == 1 and preview["preview"]["students"][0]["class"] == 7, "sample maps real native class/school/proctor-derived group plan")
        no_secrets(preview)
        editor_file, _ = native.core(tail, tokens["editor"], binary=True)
        check(workbook_rows(editor_file) == rows, "canonical scoped users/upload editor can download the same safe source format")
        plain = {**source, "full_name": "Template" + demo["run"] + " Student", "school_name": "", "proctor_name": ""}
        scoped = command(base, tokens["editor"], {"rows": [plain]}, idempotent=True)
        demo["editor_preview_id"] = scoped["id"]
        check(scoped["actor_id"] == demo["actors"]["editor"]["account_id"] and scoped["preview"]["can_commit"] and scoped["preview"]["summary"]["students_without_group"] == 1, "scoped plain sample preview keeps native rights and actor/account mapping")
        no_secrets(scoped)
        for label in ("viewer", "upload_only", "student"):
            native.core(tail, tokens[label], expected=403)
        native.core(tail, foreign, expected=404)
        native.core(f"/divisions/{demo['division_b']}/legacy-imports/template", tokens["editor"], expected=403)
        native.core("/divisions/2001/legacy-imports/template", admin, expected=404)
        for query in ("organization_id=2001", f"division_id={demo['division_b']}", "limit=200"):
            native.core(tail + "?" + query, admin, expected=400)
        native.core(tail, expected=401)
        native.core("/divisions/0/legacy-imports/template", admin, expected=400)
        actor = demo["actors"]["editor"]
        for section in ("users", "upload"):
            revoked = {key: dict(value) for key, value in actor["permissions"].items()}
            revoked[section]["edit"] = False
            command(f"/roles/{actor['role_id']}", admin, {"name": actor["role_name"], "permissions": revoked}, method="PUT", expected=200)
            native.core(tail, tokens["editor"], expected=403)
            command(f"/roles/{actor['role_id']}", admin, {"name": actor["role_name"], "permissions": actor["permissions"]}, method="PUT", expected=200)
            restored, _ = native.core(tail, tokens["editor"], binary=True)
            check(workbook_rows(restored) == rows, f"same live actor token respects {section}.edit withdrawal and restoration")
        roster = native.core(f"/divisions/{demo['division_a']}/users?limit=200", admin)
        check({row["account_id"] for row in roster} == {actor["account_id"] for actor in demo["actors"].values()}, "preview never commits sample people or changes the fresh four-account roster")
        native.core(base + "/" + str(scoped["id"]), admin, expected=404)
        native.core(base + "/" + str(preview["id"]), tokens["editor"], expected=404)
        check(not any(item["status"] == "pending" for item in demo["commands"]), "all single-dispatch fixture commands have confirmed outcomes")
        demo["readiness"], report["status"] = "ready", "passed"
    except Exception as error:
        report["error"] = type(error).__name__ + ": " + str(error)
    finally:
        for token in native.tokens:
            try:
                native.request(AUTH, "/v1/auth/logout", token, method="POST")
            except Exception:
                pass
        if demo["commands"]:
            save(MANIFEST, demo)
        report["run"], report["passed"] = demo["run"], len(checks)
        save(REPORT, report)
        print(json.dumps({"status": report["status"], "passed": len(checks), "report": str(REPORT)}))
    if report["status"] != "passed":
        raise SystemExit(1)


if __name__ == "__main__":
    main()
