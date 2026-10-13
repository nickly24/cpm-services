#!/usr/bin/env python3
"""Generate dev-only upload specimens; does not read env, contact services or seed DBs.

XLSX authoring uses the bundled @oai/artifact-tool writer. Fixture meaning is
stable; ZIP metadata is canonicalized. Runtime credentials and tenant/object IDs
must be bound by the HTTP test harness and must never be saved into these files.
"""
from __future__ import annotations

import argparse
import base64
import copy
import csv
import hashlib
import json
from pathlib import Path, PurePosixPath
import re
import shutil
import struct
import subprocess
import tempfile
import zipfile
import zlib
import xml.etree.ElementTree as ET

SERVICE = Path(__file__).resolve().parents[1]
RUNTIME = Path.home() / ".cache/codex-runtimes/codex-primary-runtime/dependencies"
PERSON_A = "Тестов УченикА"
PERSON_B = "Примеров УченикБ"
AMBIGUOUS = "Дублев УченикВ"
PROCTOR = "Синтетиков ПрокторА"
DIRECTION = "Uploads synthetic direction"
PASSWORD_MARKER = "<runtime-generated-password>"


def write_json(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def canonical_xlsx(path: Path) -> None:
    # Artifact export can timestamp package entries. Keep source fixtures stable
    # and remove author identities; no spreadsheet cell content is altered.
    with zipfile.ZipFile(path) as source:
        entries = {name: source.read(name) for name in source.namelist()}
    # The artifact exporter assigns fresh OPC relationship IDs. Canonicalize
    # only package relationship IDs and their r:id references, never cell text.
    package_ns = "http://schemas.openxmlformats.org/package/2006/relationships"
    office_ns = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
    ET.register_namespace("", package_ns)
    ET.register_namespace("x", "http://schemas.openxmlformats.org/spreadsheetml/2006/main")
    ET.register_namespace("r", office_ns)
    for name in sorted(entries):
        if not name.endswith(".rels"):
            continue
        root = ET.fromstring(entries[name])
        relationships = sorted(list(root), key=lambda node: (node.get("Target", ""), node.get("Type", ""), node.get("TargetMode", "")))
        mapping = {}
        for index, relationship in enumerate(relationships, 1):
            mapping[relationship.get("Id")] = f"rId{index}"
            relationship.set("Id", f"rId{index}")
        root[:] = relationships
        entries[name] = ET.tostring(root, encoding="utf-8", xml_declaration=True)
        relation_path = PurePosixPath(name)
        source_name = str(relation_path.parent.parent / relation_path.name[:-5])
        if source_name in entries:
            owner = ET.fromstring(entries[source_name])
            for node in owner.iter():
                for attribute in list(node.attrib):
                    if attribute.startswith("{" + office_ns + "}") and node.attrib[attribute] in mapping:
                        node.set(attribute, mapping[node.attrib[attribute]])
            entries[source_name] = ET.tostring(owner, encoding="utf-8", xml_declaration=True)
    if "docProps/core.xml" in entries:
        data = entries["docProps/core.xml"].decode("utf-8")
        data = re.sub(r"(<dcterms:(?:created|modified)[^>]*>).*?(</dcterms:(?:created|modified)>)", r"\g<1>2026-10-09T00:00:00Z\2", data)
        data = re.sub(r"(<dc:creator>).*?(</dc:creator>)", r"\g<1>CPM synthetic fixtures\2", data)
        data = re.sub(r"(<cp:lastModifiedBy>).*?(</cp:lastModifiedBy>)", r"\g<1>CPM synthetic fixtures\2", data)
        entries["docProps/core.xml"] = data.encode("utf-8")
    with zipfile.ZipFile(path, "w", compression=zipfile.ZIP_DEFLATED) as output:
        for name, data in sorted(entries.items()):
            output.writestr(zipfile.ZipInfo(name, (2026, 10, 9, 0, 0, 0)), data, compress_type=zipfile.ZIP_DEFLATED)


def pdf_bytes() -> bytes:
    # Minimal genuine one-page PDF without actions, attachments, URLs or scripts.
    content = b"BT /F1 18 Tf 60 760 Td (CPM synthetic homework fixture) Tj ET\n"
    objects = [
        b"<< /Type /Catalog /Pages 2 0 R >>",
        b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
        b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
        b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
        b"<< /Length " + str(len(content)).encode() + b" >>\nstream\n" + content + b"endstream",
    ]
    data = bytearray(b"%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
    offsets = [0]
    for number, obj in enumerate(objects, 1):
        offsets.append(len(data))
        data.extend(f"{number} 0 obj\n".encode() + obj + b"\nendobj\n")
    xref = len(data)
    data.extend(f"xref\n0 {len(objects)+1}\n0000000000 65535 f \n".encode())
    for offset in offsets[1:]:
        data.extend(f"{offset:010d} 00000 n \n".encode())
    data.extend(f"trailer\n<< /Size {len(objects)+1} /Root 1 0 R >>\nstartxref\n{xref}\n%%EOF\n".encode())
    return bytes(data)


def png_bytes() -> bytes:
    def chunk(kind: bytes, data: bytes) -> bytes:
        return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data) & 0xFFFFFFFF)
    width, height = 160, 100
    rows = b"".join(b"\x00" + bytes((230, 240, 250)) * width for _ in range(height))
    return b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0)) + chunk(b"IDAT", zlib.compress(rows)) + chunk(b"IEND", b"")


def specifications() -> list[dict]:
    specs = []
    def add(path, rows, **extra):
        specs.append({"path": path, "rows": rows, **extra})
    users_header = ["ФИО", "Класс", "Школа", "Проктор", "Telegram"]
    add("users/legacy-success.xlsx", [users_header, [PERSON_A, 7, "Uploads synthetic school", PROCTOR, ""], [PERSON_B, 8, "Uploads synthetic school", "", ""]], sheet="Ученики")
    add("users/legacy-duplicate-names.xlsx", [users_header, [PERSON_A, 7, "", "", ""], [PERSON_A + " Синтетический", 7, "", "", ""]])
    add("users/legacy-ambiguous-owner.xlsx", [users_header, [AMBIGUOUS, 7, "", PROCTOR, ""]])
    add("users/legacy-invalid.xlsx", [users_header, ["Однослово", 7, "", "", ""], [PERSON_A, 7.5, "", "", ""], [PERSON_B, -1, "", "", ""]])
    add("users/legacy-missing-header.xlsx", [["Имя", "Школа"], [PERSON_A, "Uploads synthetic school"]])
    credential_header = ["role", "full_name", "class", "tg_name", "login", "password"]
    credential_rows = [credential_header, ["student", PERSON_A, 7, "", "uploads-fixture-new-a", PASSWORD_MARKER], ["student", PERSON_B, 8, "", "uploads-fixture-new-b", PASSWORD_MARKER]]
    add("users/credentials-placeholder.xlsx", credential_rows)
    add("users/credentials-duplicate-login.xlsx", [credential_header, *[row[:4] + ["uploads-fixture-duplicate", PASSWORD_MARKER] for row in credential_rows[1:]]])
    add("users/credentials-101-rows.xlsx", [credential_header, *[["student", f"Фикстуров Ученик{n:03}", 7, "", f"uploads-fixture-limit-{n:03}", PASSWORD_MARKER] for n in range(101)]])
    cards_header = ["Вопрос", "Ответ"]
    add("cards/success.xlsx", [cards_header, ["Сколько будет 2 + 2?", "4"], ["Типы коллекций?", "list; tuple; dict"]])
    add("cards/duplicates.xlsx", [cards_header, ["Сколько будет 2 + 2?", "4"], ["Сколько будет 2 + 2?", "4"]])
    add("cards/multiline.xlsx", [cards_header, ["Вопрос\nсо второй строкой", "Первая строка\n\nТретья строка\nвариант1 / вариант2, вариант3; вариант4"]], multiline=["A2:B2"])
    add("cards/invalid-empty-row.xlsx", [cards_header, ["Вопрос А", "Ответ А"], [None, None], ["Вопрос Б", "Ответ Б"]])
    add("cards/invalid-missing-answer.xlsx", [cards_header, ["Вопрос без ответа", None]])
    add("cards/formula.xlsx", [cards_header, ["Формула должна быть отклонена", 2]], formulas={"B2": "=1+1"})
    add("cards/missing-header.xlsx", [["question", "answer"], ["Synthetic question", "Synthetic answer"]])
    results_header = ["№", "Пользователь", "Дата завершения", "Потрачено времени", "Логин", "ФИО", "Количество правильных ответов", "Процент правильных ответов (%)"]
    add("external-results/success.xlsx", [results_header, [1, "synthetic-platform-a", "2026-10-09 12:00:00", "00:05:00", "uploads-fixture-a1-student", PERSON_A, 7, 87.5], [2, "synthetic-platform-b", "2026-10-09 12:05:00", "00:04:00", "uploads-fixture-a1-student-b", PERSON_B, 8, "100%"]])
    add("external-results/header-row-10.xlsx", [[f"Synthetic export metadata {n}"] for n in range(1, 10)] + [["ФИО", "Процент"], [PERSON_A, "87,5%"]], header_row=10)
    add("external-results/header-row-11.xlsx", [[f"Synthetic export metadata {n}"] for n in range(1, 11)] + [["ФИО", "Процент"], [PERSON_A, 75]], header_row=11)
    add("external-results/duplicate-names.xlsx", [["ФИО", "Процент"], [PERSON_A, 75], [PERSON_A + " Другой", 80]])
    add("external-results/ambiguous-and-unknown.xlsx", [["ФИО", "Процент"], [AMBIGUOUS, 75], ["Неизвестнов УченикГ", 80]])
    add("external-results/invalid-percent.xlsx", [["ФИО", "Процент"], [PERSON_A, True], [PERSON_B, "не число"], [AMBIGUOUS, 101], ["Фикстуров УченикД", -1]])
    add("external-results/already-existing.xlsx", [["ФИО", "Процент"], ["Готовов УченикЕ", 80]])
    add("external-results/zero-and-fraction.xlsx", [["ФИО", "Процент", "Верных ответов"], [PERSON_A, 0, 0], [PERSON_B, 0.875, 1]])
    add("external-results/5001-rows.xlsx", [["ФИО", "Процент"], *[[f"Фикстуров Ученик{n:04}", 75] for n in range(1, 5002)]])
    questions_header = ["Часть", "Вопрос", "Эталонный ответ"]
    add("exams/questions-success.xlsx", [questions_header, ["A", "Сколько будет 3 + 4?", "7"], ["B", "Синтетический вопрос\nВторая строка", "Синтетический ответ"]], multiline=["B3"])
    add("exams/questions-duplicates.xlsx", [questions_header, ["A", "Синтетический вопрос", "Ответ"], ["A", "Синтетический вопрос", "Ответ"]])
    add("exams/questions-205-rows.xlsx", [questions_header, *[["A", f"Синтетический вопрос {n:03}", f"Ответ {n:03}"] for n in range(1, 206)]])
    add("exams/questions-5001-rows.xlsx", [questions_header, *[["A", f"Синтетический вопрос {n:04}", f"Ответ {n:04}"] for n in range(1, 5002)]])
    add("exams/questions-formula.xlsx", [questions_header, ["A", "Формула в ответе", 2]], formulas={"C2": "=1+1"})
    assignments_header = ["student_login", "examinator_1_login", "examinator_2_login", "replacement_limit"]
    add("exams/assignments-success.xlsx", [assignments_header, ["uploads-fixture-a1-student", "uploads-fixture-a1-examinator", "uploads-fixture-a1-examinator-b", 2]])
    add("exams/assignments-duplicate-examinator.xlsx", [assignments_header, ["uploads-fixture-a1-student", "uploads-fixture-a1-examinator", "uploads-fixture-a1-examinator", 2]])
    add("exams/assignments-foreign-division.xlsx", [assignments_header, ["uploads-fixture-a2-student", "uploads-fixture-a1-examinator", "", 2]])
    outside_header = ["student_login", "points", "grade", "examinator"]
    add("exams/outside-success.xlsx", [outside_header, ["uploads-fixture-a1-student", 17.5, 4, "Синтетиков ЭкзаменаторА"]])
    add("exams/outside-invalid.xlsx", [outside_header, ["uploads-fixture-a1-student", "17.5", 6, "Синтетиков ЭкзаменаторА"]])
    add("exams/outside-foreign-org.xlsx", [outside_header, ["uploads-fixture-b1-student", 17.5, 4, "Синтетиков ЭкзаменаторА"]])
    return specs


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, default=SERVICE / "fixtures")
    parser.add_argument("--previews", type=Path, default=Path(tempfile.gettempdir()) / "cpm-upload-fixture-previews")
    parser.add_argument("--runtime", type=Path, default=RUNTIME)
    args = parser.parse_args()
    output = args.output.resolve()
    if "production" in str(output).lower() or output == SERVICE:
        parser.error("fixture output must be a dedicated dev/test directory")
    output.mkdir(parents=True, exist_ok=True)
    specs = specifications()
    node = args.runtime / "node/bin/node"
    packages = args.runtime / "node/node_modules"
    if not node.is_file() or not (packages / "@oai/artifact-tool").exists():
        parser.error("bundled artifact-tool runtime missing; pass --runtime to load_workspace_dependencies root")
    with tempfile.TemporaryDirectory(prefix="cpm-uploads-fixtures-") as task_dir:
        task = Path(task_dir)
        (task / "node_modules").symlink_to(packages, target_is_directory=True)
        builder = task / "generate_workbooks.mjs"
        shutil.copyfile(Path(__file__).with_name(builder.name), builder)
        write_json(task / "workbooks.json", specs)
        subprocess.run([str(node), str(builder), str(task / "workbooks.json"), str(output), str(args.previews.resolve())], check=True)
    for spec in specs:
        canonical_xlsx(output / spec["path"])
        # XLSX export emits its own inspection sidecar. Fixtures should contain
        # only input/expected-data files; previews stay in the temporary folder.
        inspector = output / (spec["path"] + ".inspect.ndjson")
        if inspector.is_file():
            inspector.unlink()
    credential_rows = specs[5]["rows"]
    with (output / "users/credentials-placeholder.csv").open("w", encoding="utf-8", newline="") as handle:
        csv.writer(handle, lineterminator="\n").writerows(credential_rows)
    native = {"title": "Uploads synthetic internal test", "direction": DIRECTION, "startDate": "2026-10-09T10:00", "endDate": "2026-12-31T23:59", "timeLimitMinutes": 30, "published": False, "visible": False, "questions": [
        {"questionId": 1, "type": "single", "text": "Сколько будет 2 + 2?", "points": 1, "answers": [{"id": "a", "text": "3", "isCorrect": False}, {"id": "b", "text": "4", "isCorrect": True}]},
        {"questionId": 2, "type": "multiple", "text": "Какие числа чётные?", "points": 2, "answers": [{"id": "a", "text": "2", "isCorrect": True}, {"id": "b", "text": "4", "isCorrect": True}, {"id": "c", "text": "3", "isCorrect": False}]},
        {"questionId": 3, "type": "text", "text": "Текстовый ответ", "points": 1, "answers": [], "correctAnswers": ["синтетический", "пример"]},
    ]}
    write_json(output / "tests/native-success.json", native)
    invalid = copy.deepcopy(native)
    invalid["questions"][1]["questionId"] = 1
    invalid["questions"][0]["answers"][0]["isCorrect"] = True
    invalid["questions"][2]["correctAnswers"] = []
    write_json(output / "tests/native-invalid.json", invalid)
    online = {"Name": "Uploads synthetic OnlineTestPad", "Params": {"timelimited": True, "timelimitminutes": 20}, "IsLimitedQuestions": True, "LimitedQuestionsCount": 2, "IsRandomQuestions": True, "IsRandomAnswersInAllQuestions": True, "Questions": [
        {"Number": 1, "AnswerType": 10, "Text": "<p>Сколько будет 2 + 2?</p>", "RightScore": 1, "AnswerTemplates": [{"Number": 1, "Text": "3", "Score": 0}, {"Number": 2, "Text": "4", "Score": 1}]},
        {"Number": 2, "AnswerType": 210, "Text": "<p>Чётные числа</p>", "maxScore": 2, "AnswerTemplates": [{"Number": 1, "Text": "2", "IsRightAnswer": True}, {"Number": 2, "Text": "4", "IsRightAnswer": True}, {"Number": 3, "Text": "3", "IsRightAnswer": False}]},
        {"Number": 3, "AnswerType": 410, "Text": "<p>Число четыре</p>", "AnswerTemplates": [{"Number": 1, "ValueInt": 4, "Score": 1}]},
        {"Number": 4, "AnswerType": 420, "Text": "<p>Текстовый ответ<br>Вторая строка</p>", "AnswerTemplates": [{"Number": 1, "ValueText": "синтетический <или> пример", "IsRightAnswer": True}]},
    ]}
    source_text = "var test = " + json.dumps(online, ensure_ascii=False) + ";\n"
    (output / "tests/online-test-pad.js").write_text(source_text, encoding="utf-8")
    write_json(output / "tests/online-test-pad-request.json", {"importFormat": "online_test_pad", "sourceText": source_text, "metadata": {key: value for key, value in native.items() if key != "questions"}})
    unsupported = copy.deepcopy(online)
    unsupported["Questions"][0]["AnswerType"] = 999
    (output / "tests/online-test-pad-unsupported.js").write_text("var test = " + json.dumps(unsupported, ensure_ascii=False) + ";\n", encoding="utf-8")
    (output / "tests/invalid-json.json").write_text('{"questions": [}', encoding="utf-8")
    write_json(output / "cards/transform-request.json", {"theme_id": "<runtime-theme-id>", "card_ids": ["<runtime-card-id-1>", "<runtime-card-id-2>"]})
    (output / "owner-references").mkdir(exist_ok=True)
    (output / "owner-references/homework.pdf").write_bytes(pdf_bytes())
    png = png_bytes()
    (output / "owner-references/attendance.png").write_bytes(png)
    write_json(output / "owner-references/attendance-image.json", {"mime": "image/png", "data": base64.b64encode(png).decode()})
    (output / "security").mkdir(exist_ok=True)
    (output / "security/not-xlsx.xlsx").write_bytes(b"CPM synthetic fixture: this is not a ZIP/XLSX file\n")
    (output / "security/not-pdf.pdf").write_bytes(b"CPM synthetic fixture: this is not a PDF file\n")
    shutil.copyfile(output / "cards/success.xlsx", output / "security/expanded-limit.xlsx")
    with zipfile.ZipFile(output / "security/expanded-limit.xlsx", "a", compression=zipfile.ZIP_DEFLATED) as archive:
        archive.writestr("xl/synthetic-expanded-limit.xml", b"0" * (65 * 1024 * 1024))
    canonical_xlsx(output / "security/expanded-limit.xlsx")
    scenarios = {
        "dev_only": True, "database_seeded": False, "runtime_binding_required": True,
        "credential_placeholder": PASSWORD_MARKER,
        "organizations": [{"alias": "A", "divisions": ["A1", "A2"]}, {"alias": "B", "divisions": ["B1"]}],
        "direction_name": DIRECTION,
        "school_name": "Uploads synthetic school",
        "names": {"new_a": PERSON_A, "new_b": PERSON_B, "ambiguous_same_division": AMBIGUOUS, "existing_result": "Готовов УченикЕ", "proctor": PROCTOR},
        "owner_dependencies": ["Seed equivalent surname/given name in A1, A2 and B1 to prove isolation", "Seed TWO ambiguous-name student participations in A1 for ambiguity fixture", "Seed existing external result for Готовов УченикЕ", "Bind fixture logins to fresh dev Auth accounts and Core participations", "Generate credential passwords only in memory before sending directly to Core; never save substituted file"],
        "required_access_scenarios": ["admin_A cannot access B", "staff_A1 cannot access A2", "upload_edit_without_subject_edit cannot commit", "student cannot create administrative import", "switching cabinet preserves original session/job division", "revoke membership after preview and before apply", "guessed foreign session/job/file/receipt/report IDs", "owner expected_revision conflict", "lost commit response: repeat exact command", "worker restart and Rabbit redelivery: one owner commit"],
        "scope_notes": "Files/session/jobs/events/receipts remain pinned to verified org+division+actor. Cards/themes/progress and school/direction catalogs are organization targets. Account ID is distinct from profile/student ID.",
    }
    write_json(output / "scenarios.json", scenarios)
    write_json(output / "expectations.json", {
        "scope": "These assertions are for dev HTTP harnesses after their own owner fixtures are bound. No domain validation is implemented by this generator.",
        "users/legacy-success.xlsx": {"source_rows": [2, 3], "owner_expected": "two valid synthetic student rows; one without group", "requires": ["fresh synthetic school/proctor target"]},
        "users/legacy-duplicate-names.xlsx": {"owner_expected": "actual Core duplicate policy must be exercised; historical preview did not deduplicate new input names"},
        "users/legacy-ambiguous-owner.xlsx": {"owner_expected": "ambiguity must not select a foreign/same-name account", "requires": ["two same-name students in target division"]},
        "users/legacy-invalid.xlsx": {"owner_expected": "row errors; no subject writes"},
        "users/legacy-missing-header.xlsx": {"source_expected": "missing_source_headers"},
        "users/credentials-placeholder.csv": {"credentials": "replace marker with generated password in memory only; never use/import the marker", "source_download": "disabled"},
        "users/credentials-placeholder.xlsx": {"credentials": "replace marker with generated password in memory only; never use/import the marker", "source_download": "disabled"},
        "users/credentials-duplicate-login.xlsx": {"owner_expected": "duplicate login blocks import"},
        "users/credentials-101-rows.xlsx": {"owner_expected": "credential row limit exceeded"},
        "cards/success.xlsx": {"owner_expected": "two rows create"},
        "cards/duplicates.xlsx": {"owner_expected": "duplicate warning; included warning rows are created unless explicitly excluded"},
        "cards/multiline.xlsx": {"owner_expected": "one card; embedded blank line and separators stay in single answer"},
        "cards/invalid-empty-row.xlsx": {"owner_expected": "empty_row_3"},
        "cards/invalid-missing-answer.xlsx": {"owner_expected": "preview row error"},
        "cards/formula.xlsx": {"source_expected": "formulas_forbidden"},
        "external-results/success.xlsx": {"source_rows": [2, 3], "percent": [87.5, 100], "owner_expected": "two results atomically applied only after matching target participants"},
        "external-results/header-row-10.xlsx": {"source_rows": [11], "percent": [87.5]},
        "external-results/header-row-11.xlsx": {"source_expected": "missing_source_headers"},
        "external-results/zero-and-fraction.xlsx": {"source_rows": [2, 3], "percent": [0, 0.875], "correct_count": [0, 1], "number_format_scaling": False},
        "external-results/duplicate-names.xlsx": {"owner_expected": "duplicate name blocks entire batch; patronymic ignored"},
        "external-results/ambiguous-and-unknown.xlsx": {"owner_expected": "ambiguity/unmatched blocks entire batch"},
        "external-results/invalid-percent.xlsx": {"source_expected": "invalid_source_cell_2_percent", "owner_expected": "invalid/null/out-of-range scores prevent apply"},
        "external-results/already-existing.xlsx": {"owner_expected": "existing result prevents batch/overwrite", "requires": ["seed existing result"]},
        "external-results/5001-rows.xlsx": {"source_expected": "row_limit_exceeded"},
        "tests/native-success.json": {"owner_expected": "single/multiple/text questions validated by Tests", "requires": ["existing synthetic direction"]},
        "tests/native-invalid.json": {"owner_expected": "duplicate questionId, multiple correct single answers and empty text answer rejected"},
        "tests/online-test-pad.js": {"source_expected": "exact sourceText wrapped without execution", "owner_expected": "types10/210/410/420 conversion; warnings retained"},
        "tests/online-test-pad-unsupported.js": {"owner_expected": "unsupported answer type rejected by Tests"},
        "tests/invalid-json.json": {"source_expected": "invalid_json"},
        "exams/questions-success.xlsx": {"owner_expected": "questions added by Exams"},
        "exams/questions-duplicates.xlsx": {"owner_expected": "duplicate question row errors"},
        "exams/questions-205-rows.xlsx": {"owner_expected": "205 preview rows delivered; no truncation at100/200", "commit_scope": "fresh synthetic exam only"},
        "exams/questions-5001-rows.xlsx": {"owner_expected": "row limit exceeded"},
        "exams/questions-formula.xlsx": {"source_expected": "formulas_forbidden"},
        "exams/assignments-success.xlsx": {"owner_expected": "student assignment and valid two-person commission", "requires": ["bind dev logins in one target division"]},
        "exams/assignments-duplicate-examinator.xlsx": {"owner_expected": "duplicate examinator rejected"},
        "exams/assignments-foreign-division.xlsx": {"owner_expected": "foreign target participation rejected"},
        "exams/outside-success.xlsx": {"owner_expected": "numeric17.5 points and grade4 add result"},
        "exams/outside-invalid.xlsx": {"owner_expected": "text points/grade6 rejected"},
        "exams/outside-foreign-org.xlsx": {"owner_expected": "foreign organization student rejected"},
        "owner-references/homework.pdf": {"owner": "homework", "bytes": "direct S3", "uploads_import": False},
        "owner-references/attendance.png": {"owner": "attendance", "uploads_import": False},
        "security/not-xlsx.xlsx": {"source_expected": "invalid_xlsx"},
        "security/expanded-limit.xlsx": {"source_expected": "decompression_limit", "expanded_bytes": 65*1024*1024},
    })
    fixture_entries = []
    for path in sorted(output.rglob("*")):
        if path.is_file() and path.name != "manifest.json":
            content = path.read_bytes()
            fixture_entries.append({"path": path.relative_to(output).as_posix(), "bytes": len(content), "sha256": hashlib.sha256(content).hexdigest()})
    write_json(output / "manifest.json", {"schema_version": 1, "synthetic_only": True, "writes_to_services": False, "files": fixture_entries})
    print(json.dumps({"fixtures": len(fixture_entries), "xlsx": len(specs) + 1, "output": str(output), "seeds_performed": 0}))


if __name__ == "__main__":
    main()
