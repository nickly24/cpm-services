#!/usr/bin/env python3
"""Run the repository's legacy rating functions with synthetic in-memory owners.

No application configuration, databases, network, or credentials are loaded.
The output is checked into internal/calculate/testdata for ordinary Go tests.
"""

import ast
from datetime import date, datetime, timezone
import json
from pathlib import Path
import runpy
import sys
from zoneinfo import ZoneInfo


ROOT = Path(__file__).resolve().parents[3]
LEGACY = ROOT / "cpm-back-main/cpm_back/services/exam/calculate_ratings.py"
EXAMS = ROOT / "cpm-back-main/cpm_back/services/exams/rating.py"
OUTPUT = ROOT / "services/calculate/internal/calculate/testdata/ratings_legacy_golden.json"
MOSCOW = ZoneInfo("Europe/Moscow")


class Collection:
    def __init__(self, rows):
        self.rows = rows

    def find(self, *args):
        return self.rows


class Cursor:
    def __init__(self, fixture):
        self.fixture = fixture
        self.rows = None

    def execute(self, sql, params=None):
        if "FROM directions" in sql:
            self.rows = self.fixture["directions"]
        elif "FROM tests_out" in sql:
            self.rows = self.fixture["external"]
        elif "FROM homework h" in sql:
            self.rows = self.fixture["homework"]
        else:
            raise AssertionError("unexpected legacy query")

    def fetchall(self):
        return self.rows

    def close(self):
        pass


class Connection:
    def __init__(self, fixture):
        self.fixture = fixture

    def cursor(self, **kwargs):
        return Cursor(self.fixture)


def personal_input():
    return dict(job_id="ae095746-6c38-4b77-bc40-c6d0e62d1b2d", revision=1,
                organization_id=1, division_id=2, account_id=3,
                input_hash="a" * 64, algorithm_version="legacy-rating-v1",
                date_from="2026-10-01", date_to="2026-10-31",
                as_of="2026-11-01T00:00:00Z", homework=[], tests=[], exams=[])


def test(identifier, score, direction="Математика", source="external"):
    return dict(test_id=str(identifier), title="Синтетический тест", direction=direction,
                source=source, calculation_status="missing" if score is None else "ready",
                score=score)


def evaluate(ns, exam_fn, item):
    today = datetime.fromisoformat(item["as_of"].replace("Z", "+00:00")).astimezone(MOSCOW).date()
    fixture = dict(directions=[], external=[], homework=[])
    for hw in item["homework"]:
        if hw["type_name"] == "ОВ" and item["date_from"] <= hw["deadline"] <= item["date_to"] and date.fromisoformat(hw["deadline"]) < today:
            fixture["homework"].append(dict(id=hw["assignment_id"], name=hw["name"], type=hw["type_name"],
                                             deadline=hw["deadline"], status=hw["status"],
                                             result=hw["result"], date_pass=hw["date_pass"]))
    internal, sessions = [], []
    for index, entry in enumerate(item["tests"], 1):
        if entry["source"] == "internal":
            internal.append(dict(_id=entry["test_id"], title=entry["title"], direction=entry["direction"],
                                 published=True, isActive=True, startDate=item["date_from"]))
            if entry["calculation_status"] == "ready":
                sessions.append(dict(testId=entry["test_id"], studentId=item["account_id"], score=entry["score"]))
        else:
            fixture["directions"].append(dict(id=index, name=entry["direction"]))
            fixture["external"].append(dict(id=entry["test_id"], name=entry["title"], direction_id=index,
                                             date=item["date_from"], rate=entry["score"]))
    mongo = type("Mongo", (), dict(tests=Collection(internal), test_sessions=Collection(sessions)))()
    conn = Connection(fixture)
    hw = ns["calculate_homework_rating"](conn, item["account_id"], item["date_from"], item["date_to"])
    tests = ns["calculate_tests_rating"](conn, mongo, item["account_id"], item["date_from"], item["date_to"])
    snapshot = dict(exams=[], values={})
    for exam in item["exams"]:
        snapshot["exams"].append(dict(id=exam["exam_id"], exam_name=exam["exam_name"],
                                       exam_type=exam["exam_type"], direction_id=exam["direction_id"],
                                       date=date.fromisoformat(exam["exam_date"])))
        if not exam["missing_result"]:
            snapshot["values"][(exam["exam_id"], item["account_id"])] = dict(
                grade=exam["score"], attempt_no=exam["attempt_no"], has_appeal=exam["has_appeal"])
    exams = exam_fn(snapshot, item["account_id"])
    return dict(homework=hw["average"], exams=exams["average"], tests=tests["average"],
                directions=tests["directions"], final_rating=ns["calculate_final_rating"](hw, exams, tests))


def main():
    if sys.version_info < (3, 12):
        raise SystemExit("These golden vectors use the dev legacy Python 3.12+ float sum behavior")
    ns = runpy.run_path(str(LEGACY))
    # Compile the actual pure exam function without importing its application/DB modules.
    tree = ast.parse(EXAMS.read_text())
    selected = [node for node in tree.body if isinstance(node, ast.FunctionDef) and node.name == "calculate_from_snapshot"]
    if len(selected) != 1:
        raise AssertionError("legacy exam function not found")
    exam_ns = dict(UTC=timezone.utc, MOSCOW=MOSCOW)
    exec(compile(ast.Module(body=selected, type_ignores=[]), str(EXAMS), "exec"), exam_ns)
    cases = []
    item = personal_input()
    for n in range(10):
        item["homework"].append(dict(assignment_id=n+1, name="Синтетическая ДЗ", type_name="ОВ",
                                      deadline="2026-10-01", status=1, result=0.1, date_pass=None))
        item["exams"].append(dict(exam_id=str(n+1), exam_name="Синтетический экзамен", exam_date="2026-10-01",
                                   exam_type="outside_lms", direction_id=None, attempt_no=None,
                                   has_appeal=False, missing_result=False, score=0.1))
        item["tests"].append(test(n+1, 0.1))
    cases.append(("fractional-components-homework-keeps-ordinary-sum", item))
    item = personal_input()
    item["tests"] = [test(n+1, 0.1, "Направление " + str(n+1)) for n in range(10)]
    cases.append(("fractional-equal-direction-sum", item))
    item = personal_input()
    item["tests"] = [test(1, 0.3), test(2, None), test(3, 0.0)]
    cases.append(("external-fractional-null-and-completed-zero", item))
    item = personal_input()
    item["tests"] = [test(1, 90.0, "\x1fStraße\x1c", "internal"), test(2, 0.0, "STRASSE"), test(3, None, " ")]
    cases.append(("mixed-source-casefold-and-missing-direction", item))
    out = dict(runtime=f"CPython {sys.version_info.major}.{sys.version_info.minor}.{sys.version_info.micro}",
               source_files=[str(LEGACY.relative_to(ROOT)), str(EXAMS.relative_to(ROOT))],
               cases=[dict(name=name, input=item, expected=evaluate(ns, exam_ns["calculate_from_snapshot"], item)) for name, item in cases])
    OUTPUT.parent.mkdir(exist_ok=True)
    OUTPUT.write_text(json.dumps(out, ensure_ascii=False, indent=2) + "\n")
    print(f"Wrote {len(cases)} synthetic vectors using {out['runtime']}")


if __name__ == "__main__":
    main()
