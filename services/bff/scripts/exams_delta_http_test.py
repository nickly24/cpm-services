"""Fresh public BFF fixtures for canonical Exams dates, pending retake and 5000-row preview.

Explicit --run only after Core/BFF/Exams readiness and coordinated Ratings hold
release. No SQL, cleanup, restarts, shared settings or previous fixtures touched.
Every business command is journaled before one dispatch; unknown outcomes keep
the original key and prevent an automatic new run. Credentials remain in memory.
"""
from __future__ import annotations

import argparse
from datetime import datetime, timedelta, timezone
import hashlib
import http.cookiejar
import io
import json
from pathlib import Path
import secrets
import urllib.error
import urllib.request
import uuid
from xml.sax.saxutils import escape
import zipfile

ROOT = Path(__file__).resolve().parents[3]
BFF = "http://127.0.0.1:8001"
ORIGIN = "http://127.0.0.1:3001"
MANIFEST = ROOT / "dev/local/bff-exams-delta-demo.json"
REPORT = ROOT / "dev/local/bff-exams-delta-http-report.json"


class NoRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise RuntimeError("Unexpected redirect from loopback BFF")


class Session:
    def __init__(self):
        self.jar = http.cookiejar.CookieJar()
        self.http = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar), NoRedirects())
        self.revision = ""

    def call(self, path, method="GET", body=None, expected=200, headers=None):
        self.last_status = None
        assert path.startswith("/bff/core/") or path.startswith("/bff/exams") or path in {"/bff/auth/login", "/bff/auth/context", "/bff/auth/logout"}
        hdr = {"Origin": ORIGIN, "X-CPM-Request": "1", "X-CPM-Context": self.revision, **(headers or {})}
        raw = body if isinstance(body, bytes) else None
        if body is not None and raw is None:
            raw = json.dumps(body).encode()
            hdr["Content-Type"] = "application/json"
        req = urllib.request.Request(BFF + path, data=raw, headers=hdr, method=method)
        try:
            with self.http.open(req, timeout=40) as response:
                code, envelope = response.status, json.load(response)
        except urllib.error.HTTPError as response:
            code, envelope = response.code, json.load(response)
        self.last_status = code
        assert code == expected, f"{method} {path.split('?')[0]} expected {expected}, got {code}"
        assert not {"access_token", "refresh_token", "token"} & envelope.keys(), "BFF leaked tokens"
        if "context_revision" in envelope:
            self.revision = envelope["context_revision"]
        return envelope.get("data", envelope)

    def login(self, login, password, division):
        return self.call("/bff/auth/login", "POST", {"login": login, "password": password, "organization_id": 1001, "division_id": division})

    def switch(self, division):
        self.call("/bff/auth/context", "POST", {"organization_id": 1001, "division_id": division})

    def close(self):
        if self.revision:
            self.call("/bff/auth/logout", "POST", {})


def workbook(rows):
    """Small portable OOXML workbook, generated entirely in memory."""
    xml_rows = []
    for row_no, values in enumerate(rows, 1):
        cells = []
        for index, value in enumerate(values):
            col = chr(ord("A") + index)
            cells.append(f'<c r="{col}{row_no}" t="inlineStr"><is><t>{escape(str(value))}</t></is></c>')
        xml_rows.append(f'<row r="{row_no}">{"".join(cells)}</row>')
    stream = io.BytesIO()
    with zipfile.ZipFile(stream, "w", compression=zipfile.ZIP_DEFLATED) as archive:
        archive.writestr("[Content_Types].xml", '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>')
        archive.writestr("_rels/.rels", '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>')
        archive.writestr("xl/workbook.xml", '<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Questions" sheetId="1" r:id="rId1"/></sheets></workbook>')
        archive.writestr("xl/_rels/workbook.xml.rels", '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>')
        archive.writestr("xl/worksheets/sheet1.xml", '<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>' + "".join(xml_rows) + '</sheetData></worksheet>')
    return stream.getvalue()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run", action="store_true")
    args = parser.parse_args()
    if not args.run:
        print(json.dumps({"status": "pending_runtime_handoff", "checks": 0}))
        return
    if MANIFEST.exists():
        old = json.loads(MANIFEST.read_text())
        if any(c["status"] == "pending" for c in old.get("commands", [])):
            raise SystemExit("Uncertain original command exists; inspect canonical state with its preserved key before any new run")
        archived = MANIFEST.with_name("bff-exams-delta-demo-" + old["run"] + ".json")
        archived.write_bytes(MANIFEST.read_bytes()); archived.chmod(0o600)
    password = json.loads((ROOT / "dev/local/accounts.json").read_text())["password"]
    run = secrets.token_hex(5)
    demo = {"run": run, "prefix": "DEV Exams delta " + run, "organization_id": 1001, "direction_id": 1001, "commands": [], "readiness": "preparing"}
    report = {"status": "failed", "run": run, "checks": []}
    sessions = []

    def check(value, label):
        assert value, label
        report["checks"].append(label)

    def save():
        MANIFEST.write_text(json.dumps(demo, ensure_ascii=False, indent=2) + "\n"); MANIFEST.chmod(0o600)

    def login(name, division):
        actor = Session(); sessions.append(actor); actor.login(name, password, division)
        return actor

    def command(actor, path, method="POST", body=None, expected=200, headers=None, key=None):
        key = key or str(uuid.uuid4())
        raw = body if isinstance(body, bytes) else json.dumps(body, sort_keys=True).encode()
        journal = {"path": path, "method": method, "key": key, "request_sha256": hashlib.sha256(raw).hexdigest(), "status": "pending"}
        demo["commands"].append(journal); save()
        try:
            value = actor.call(path, method, body, expected, {"Idempotency-Key": key, **(headers or {})})
        except Exception:
            if actor.last_status is not None and 400 <= actor.last_status < 500:
                journal.update(status="rejected", response_status=actor.last_status); save()
            raise
        journal["status"] = "confirmed" if expected < 400 else "rejected"; save()
        return value

    try:
        admin = login("dev_admin", 1001)
        check(1001 in {d["id"] for d in admin.call("/bff/core/directions")}, "existing synthetic direction is available without changing shared catalog")
        for name in ["a", "b"]:
            demo["division_" + name] = command(admin, "/bff/core/divisions", body={"name": demo["prefix"] + " " + name}, expected=201)["id"]
        admin.switch(demo["division_a"])
        for alias in ["student", "examinator", "examinator2"]:
            role = "student" if alias == "student" else "examinator"
            login_name = "dev_exams_delta_" + run + "_" + alias
            account = command(admin, "/bff/core/users", body={"role": role, "full_name": demo["prefix"] + " " + alias, "login": login_name, "password": password, "class": 9 if role == "student" else 0}, expected=201)["account_id"]
            demo[alias + "_account_id"], demo[alias + "_login"] = account, login_name; save()
        student = login(demo["student_login"], demo["division_a"])
        examiner = login(demo["examinator_login"], demo["division_a"])
        retake_examiner = login(demo["examinator2_login"], demo["division_a"])
        classic = command(admin, "/bff/exams", body={"exam_type": "classic", "direction_id": 1001, "name": demo["prefix"] + " Pending retake"}, expected=201)
        eid = demo["classic_exam_id"] = classic["id"]; save()
        base = f"/bff/exams/{eid}"
        part = command(admin, base + "/parts", body={"code": "A", "weight": 6, "quota": 1})["part"]
        command(admin, base + "/questions", body={"part_id": part["id"], "question_text": demo["prefix"] + " First question", "answer_text": "Synthetic answer"})
        now = datetime.now(timezone.utc)
        config = admin.call(base + "/config")["config"]
        command(admin, base + "/config", "PUT", {"expected_revision": config["revision"], "start_at": (now - timedelta(hours=1)).isoformat(), "end_at": (now + timedelta(hours=8)).isoformat(), "fractional_mode": "round_down", "thresholds": [{"grade": n, "min_score": n} for n in range(6)]})
        commission = command(admin, base + "/commissions", body={"name": demo["prefix"] + " Commission", "examinator_ids": [demo["examinator_account_id"]]})["commission"]
        assignment = command(admin, base + "/assignments", body={"student_account_id": demo["student_account_id"], "commission_id": commission["id"]})["assignment"]
        attempt = command(examiner, base + f"/assignments/{assignment['id']}/attempts/ensure", body={"expected_history_generation": assignment["history_generation"]})["attempt"]
        apath = base + f"/attempts/{attempt['id']}"
        command(examiner, apath + "/ready", body={})
        state = examiner.call(apath)
        command(examiner, apath + "/start", body={"expected_state_version": state["state_version"]})
        state = examiner.call(apath); question = state["current_question"]
        command(examiner, apath + "/vote", body={"presented_question_id": question["id"], "round_id": question["round"]["id"], "value": 0})
        first = student.call(base + f"/results/{demo['student_account_id']}")
        check(first["current"]["status"] == "completed" and first["current"]["effective_grade"] == 0, "completed synthetic first attempt publishes actual zero")
        retake_commission = command(admin, base + "/commissions", body={"name": demo["prefix"] + " Retake commission", "examinator_ids": [demo["examinator2_account_id"]]})["commission"]
        retake = command(admin, base + f"/retakes/{demo['student_account_id']}", body={"commission_id": retake_commission["id"], "expected_history_generation": assignment["history_generation"]})["assignment"]
        pending = command(retake_examiner, base + f"/assignments/{retake['id']}/attempts/ensure", body={"expected_history_generation": retake["history_generation"]})["attempt"]
        current = student.call(base + f"/results/{demo['student_account_id']}")
        check(current["current"]["id"] == attempt["id"] and current["current"]["effective_grade"] == 0 and current["current"]["has_active_retake"] is True and current["current"]["retake_status"] == "pending_ready", "student keeps completed zero while real retake awaits commission readiness")
        check(not current["history"] and not {"calculated_grade", "votes", "round", "result_version"} & current["current"].keys(), "pending retake does not publish private scores/votes/CAS or unfinished history")
        demo.update(first_attempt_id=attempt["id"], pending_attempt_id=pending["id"], pending_assignment_id=retake["id"], classic_current_grade=0, retake_status="pending_ready"); save()
        summary = next(e for e in student.call("/bff/exams?scope=division&limit=200&sort=date_asc")["items"] if e["id"] == eid)
        expected_date = (now - timedelta(hours=1) + timedelta(hours=3)).date().isoformat()
        check(summary["exam_date"] == expected_date and summary["start_at"] and summary["end_at"], "student canonical exam date and operational period are available in summary")
        filtered = student.call(f"/bff/exams?scope=division&date_from={expected_date}&date_to={expected_date}&limit=1")
        check(filtered["total"] == 1 and filtered["items"][0]["id"] == eid, "inclusive owner date filter is applied before one-row pagination")
        check(not student.call("/bff/exams?scope=division&date_from=2099-01-01&limit=1")["items"], "owner excludes current exam before pagination outside requested period")
        import_exam = command(admin, "/bff/exams", body={"exam_type": "classic", "direction_id": 1001, "name": demo["prefix"] + " Import 5000"}, expected=201)["id"]
        demo["import_exam_id"] = import_exam; save()
        command(admin, f"/bff/exams/{import_exam}/parts", body={"code": "A", "weight": 6, "quota": 1})
        rows = [["part_code", "question_text", "answer_text"]] + [["A" if i < 4999 else "Missing", f"DEV {run} question {i}", "Synthetic answer"] for i in range(5000)]
        file = workbook(rows); boundary = "cpm_delta_" + run
        body = f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="delta5000.xlsx"\r\nContent-Type: application/vnd.openxmlformats-officedocument.spreadsheetml.sheet\r\n\r\n'.encode() + file + f"\r\n--{boundary}--\r\n".encode()
        parsed = command(admin, f"/bff/exams/{import_exam}/imports/questions", body=body, headers={"Content-Type": "multipart/form-data; boundary=" + boundary})
        demo.update(import_session_id=parsed["id"], import_revision=parsed["revision"], import_source_hash=parsed["source_hash"], import_validation_hash=parsed["validation_hash"]); save()
        ipath = f"/bff/exams/{import_exam}/imports/questions/{parsed['id']}"
        check(parsed["total"] == 5000 and parsed["summary"]["error_rows"] == 1 and parsed["source_hash"] == hashlib.sha256(file).hexdigest(), "5000-row parse retains actual source hash and late-row validation error")
        all_rows = []
        for offset in range(0, 5000, 200):
            page = admin.call(ipath + f"?limit=200&offset={offset}&expected_revision={parsed['revision']}")
            check(page["revision"] == parsed["revision"] and page["total"] == 5000 and page["source_hash"] == parsed["source_hash"] and page["validation_hash"] == parsed["validation_hash"] and page["summary"] == parsed["summary"], "pinned real preview page preserves revision, total, hashes and canonical summary")
            all_rows.extend(page["rows"])
        check(len(all_rows) == 5000 and len({r["row_id"] for r in all_rows}) == 5000 and all_rows[-1]["errors"], "all25 BFF preview pages include exact5000 unique rows and the final error")
        changed = command(admin, ipath, "PUT", {"expected_revision": parsed["revision"], "changes": [{"row_id": all_rows[-1]["row_id"], "excluded": True}]})
        conflict = admin.call(ipath + f"?limit=200&offset=200&expected_revision={parsed['revision']}", expected=409)
        check(conflict["error"] == "preview_modified" and changed["revision"] == parsed["revision"] + 1 and changed["summary"]["error_rows"] == 0, "editing the last row produces real409 for a stale pinned later page")
        demo.update(import_revision=changed["revision"], import_validation_hash=changed["validation_hash"], import_last_row_id=all_rows[-1]["row_id"], import_rows=5000, import_included=4999); save()
        admin.switch(demo["division_b"])
        admin.call(base + f"/results/{demo['student_account_id']}", expected=404)
        admin.call(ipath, expected=404)
        check(True, "foreign selected division cannot read operational result or import session")
        admin.switch(demo["division_a"])
        demo["readiness"] = "ready"; save(); report["status"] = "passed"
    except Exception as error:
        report["error_type"] = type(error).__name__
        report["error"] = str(error) if isinstance(error, AssertionError) else "transport_or_runtime_error"
        raise
    finally:
        for actor in sessions:
            try: actor.close()
            except Exception: pass
        REPORT.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n"); REPORT.chmod(0o600)
        print(json.dumps({"status": report["status"], "checks": len(report["checks"])}))


if __name__ == "__main__":
    main()
