"""Real local-dev external catalogue/result/import checks with API-owned fixtures.

No production calls, foreign database writes or legacy cleanup helper. Fixtures
are retained and listed in the report; all creation and mutations use owners'
HTTP APIs. Mongo inspection is read-only and limited to this run's Tests IDs.
Run only after Tests and the Core scoped student projection are rebuilt.
"""
from concurrent.futures import ThreadPoolExecutor
import copy
from datetime import datetime, timezone
import json
from pathlib import Path
import secrets
import urllib.error
import urllib.request
from urllib.parse import urlencode
import uuid

import http_test as base

RUN = secrets.token_hex(6)
PREFIX = "DEV External " + RUN
DAY = "2041-05-06"  # Legacy includes future tests selected by calendar date.
fixtures = {"run": RUN, "prefix": PREFIX, "tests": [], "users": [], "roles": [], "directions": []}
report_path = base.ROOT / "dev/local/external-tests-integration-summary.json"


def path(test, section=""):
    return f'/divisions/{test["division_id"]}/external-tests/{test["id"]}' + section


def detail(test, token, code=200):
    return base.api(path(test), token, code=code)


def internal(route, token=None, body=None, code=200, service=True):
    headers = {"X-Service-Key": base.read_env(base.ROOT / "dev/.env")["AUTH_INTERNAL_KEY"]} if service else {}
    answer = base.request("/internal/tests/" + route, token, "POST", body, code, headers)
    return answer["data"] if code < 300 else answer


def raw_commit(body, token, allowed=(200, 201)):
    headers = {"Authorization": "Bearer " + token, "Content-Type": "application/json",
               "X-Service-Key": base.read_env(base.ROOT / "dev/.env")["AUTH_INTERNAL_KEY"]}
    req = urllib.request.Request(base.TESTS + "/internal/tests/external-results/commit",
                                 method="POST", data=json.dumps(body).encode(), headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=40) as response:
            status, answer = response.status, json.load(response)
    except urllib.error.HTTPError as response:
        status, answer = response.code, json.load(response)
    base.expect(status in allowed, "commit HTTP outcome remains within the authorized expected states")
    return status, answer


def role(admin, label, permissions):
    value = base.core("/roles", admin, "POST", {"name": PREFIX + " " + label, "permissions": permissions}, 201)
    fixtures["roles"].append(value["id"])
    return value["id"]


def user(admin, label, division=1001, organization=1001, role_id=None, name=None):
    login = "dev_ext_" + RUN + "_" + label
    payload = {"role": "staff_admin" if role_id else "student", "full_name": name or "Экстерн" + RUN + " " + label,
               "login": login, "password": base.password}
    if role_id:
        payload["role_id"] = role_id
    else:
        payload["class"] = 10
    account_id = base.core(f"/divisions/{division}/users", admin, "POST", payload, 201)["account_id"]
    profile = base.core(f"/divisions/{division}/users/{account_id}", admin)
    value = {"account_id": account_id, "student_id": profile["profile_id"], "full_name": payload["full_name"],
             "login": login, "organization_id": organization, "division_id": division}
    fixtures["users"].append(value)
    return value


def create(admin, direction, label, division=1001, date=DAY, organization=1001):
    body = {"name": PREFIX + " " + label, "direction_id": direction, "date": date}
    headers = base.key()
    test = base.api(f"/divisions/{division}/external-tests", admin, "POST", body, 201, headers)
    fixtures["tests"].append({"id": test["id"], "organization_id": organization, "division_id": division})
    replay = base.api(f"/divisions/{division}/external-tests", admin, "POST", body, headers=headers)
    base.expect(replay["id"] == test["id"] and replay["revision"] == test["revision"], "catalogue create replay keeps identity/revision")
    base.api(f"/divisions/{division}/external-tests", admin, "POST", {**body, "name": body["name"] + " changed"}, 409, headers)
    return test


def results(test, token, code=200, query=None):
    suffix = "/results" + ("?" + urlencode(query) if query else "")
    return base.api(path(test, suffix), token, code=code)


def manual(test, admin, account, score, result_revision=0, headers=None, code=200):
    body = {"expected_revision": test["revision"], "expected_result_revision": result_revision, "score": score}
    return base.api(path(test, "/results/" + str(account)), admin, "PUT", body, code, headers or base.key())


def rows(*people):
    return [{"row_id": "row-" + str(index + 3), "row": index + 3, "full_name": person["full_name"],
             "account_id": person["account_id"], "legacy_student_id": person["student_id"], "percent": score}
            for index, (person, score) in enumerate(people)]


def validate(test, token, source, code=200):
    body = {"organization_id": test["organization_id"], "division_id": test["division_id"], "test_id": test["id"],
            "expected_test_revision": test["revision"], "rows": source}
    return internal("external-results/validate", token, body, code), body


def command(test, token, source):
    preview, body = validate(test, token, source)
    base.expect(preview["can_commit"], "valid domain rows can be committed")
    return {**body, "command_id": str(uuid.uuid4()), "preview_hash": preview["preview_hash"],
            "upload_id": "ext-upload-" + RUN, "upload_session_id": "ext-session-" + RUN,
            "upload_preview_revision": 1}


def snapshot(admin, organization=1001, division=1001, day=DAY):
    body = {"organization_id": organization, "division_id": division, "date_from": day, "date_to": day,
            "as_of": datetime.now(timezone.utc).isoformat()}
    return internal("ratings/snapshot", admin, body)


def own_snapshot(snapshot_value, test):
    return next(item for item in snapshot_value["external_tests"] if item["test_id"] == test["id"])


def check_database(test, receipt=None):
    client = base.mongo_client(base.settings())
    try:
        doc = client.cpm_dev.tests_external_catalog_v1.find_one({"_id": base.ObjectId(test["id"]),
                                                                "organization_id": test["organization_id"],
                                                                "division_id": test["division_id"]})
        base.expect(bool(doc), "API-created external aggregate exists in Tests database")
        base.expect(len({row["account_id"] for row in doc["results"]}) == len(doc["results"]), "one official external result per account/test/division")
        for history_id in doc["history_ids"]:
            history = client.cpm_dev.tests_external_versions_v1.find_one({"_id": history_id, "test_id": doc["_id"],
                                                                         "organization_id": doc["organization_id"],
                                                                         "division_id": doc["division_id"]})
            base.expect(bool(history) and history["revision"] <= doc["revision"], "committed history reference belongs to aggregate and scope")
        if receipt:
            saved = [item for item in doc["commands"] if item["command_id"] == receipt["command_id"]]
            base.expect(len(saved) == 1 and saved[0]["source_revision"] == receipt["source_revision"], "one durable import receipt persisted at committed revision")
            base.expect(saved[0]["upload_id"] == "ext-upload-" + RUN and saved[0]["upload_session_id"] == "ext-session-" + RUN,
                        "domain receipt retains upload audit references")
            by_account = {item["account_id"]: item for item in doc["results"]}
            for row in receipt["results"]:
                actual = by_account[row["account_id"]]
                base.expect(str(actual["_id"]) == row["result_id"] and actual["score"] == row["percent"], "stored grade and result ID exactly match import receipt")
        return doc
    finally:
        client.close()


def main():
    base.request("/health/ready")
    base.request("/health/ready", origin=base.CORE)
    admin = base.login("dev_admin")
    foreign_admin = base.login("dev_admin_math", 2001, 2001)
    direction = base.core("/directions", admin, "POST", {"name": PREFIX + " direction"}, 201)["id"]
    foreign_direction = base.core("/directions", foreign_admin, "POST", {"name": PREFIX + " other org"}, 201)["id"]
    fixtures["directions"] += [direction, foreign_direction]

    all_edit = {section: {"view": True, "edit": True} for section in ["tests", "test-results", "upload"]}
    all_view = {section: {"view": True, "edit": False} for section in ["tests", "test-results", "upload"]}
    editor_role = role(admin, "editor", all_edit)
    viewer_role = role(admin, "viewer", all_view)
    catalog_role = role(admin, "catalog", {"tests": {"view": True, "edit": True}})
    upload_role = role(admin, "upload", {"upload": {"view": True, "edit": True}})
    editor_person = user(admin, "editor", role_id=editor_role)
    viewer_person = user(admin, "viewer", role_id=viewer_role)
    catalog_person = user(admin, "catalog", role_id=catalog_role)
    upload_person = user(admin, "upload", role_id=upload_role)
    base.core(f'/divisions/1002/memberships/{editor_person["account_id"]}', admin, "PUT", {"role_id": viewer_role})
    editor, viewer = base.login(editor_person["login"]), base.login(viewer_person["login"])
    editor_second_div = base.login(editor_person["login"], 1002)
    catalog, uploader = base.login(catalog_person["login"]), base.login(upload_person["login"])
    first = user(admin, "student_a", name="Экстерн" + RUN + " Первый Первый")
    second = user(admin, "student_b", name="Экстерн" + RUN + " Второй Второй")
    ambiguous = user(admin, "student_c", name="Экстерн" + RUN + " Первый Другой")
    foreign_student = user(foreign_admin, "foreign", 2001, 2001, name="Чужой" + RUN + " Ученик")
    base.core(f'/divisions/1002/memberships/{first["account_id"]}', admin, "PUT", {})
    student, student_second_div = base.login(first["login"]), base.login(first["login"], 1002)
    other_student = base.login(second["login"])
    base.core(f'/divisions/1001/users/{ambiguous["account_id"]}/active', admin, "PUT", {"is_active": False})

    # Concrete catalogue scopes and absent/zero/unknown result semantics.
    source = create(admin, direction, "source")
    other_div = create(admin, direction, "other division", 1002)
    other_org = create(foreign_admin, foreign_direction, "other organization", 2001, organization=2001)
    outside = create(admin, direction, "outside date", date="2041-05-07")
    base.expect(detail(source, student)["result"] is None, "absence is null before any grade")
    detail(source, foreign_admin, 404)
    base.api(path(source).replace("1001", "1002", 1), editor, code=403)
    base.api(path(source).replace("1001", "1002", 1), admin, code=404)
    base.api(path(other_org), admin, code=404)
    base.api("/divisions/1001/external-tests", student, "POST", {}, 403, base.key())
    base.api("/divisions/1001/external-tests", viewer, "POST", {}, 403, base.key())
    base.api("/divisions/1001/external-tests", admin, "POST", {"name": PREFIX, "direction_id": foreign_direction, "date": DAY}, 404, base.key())
    base.api("/divisions/1001/external-tests", admin, "POST", {"name": PREFIX, "direction_id": direction, "date": "2041-02-30"}, 400, base.key())

    page = base.api("/divisions/1001/external-tests?" + urlencode({"q": RUN, "limit": 1, "offset": 0}), admin)
    second_page = base.api("/divisions/1001/external-tests?" + urlencode({"q": RUN, "limit": 1, "offset": 1}), admin)
    base.expect(page["total"] == 2 and second_page["total"] == 2 and page["items"][0]["id"] != second_page["items"][0]["id"],
                "selected division controls both catalogue pagination and total")
    second_div_page = base.api("/divisions/1002/external-tests?" + urlencode({"q": RUN}), student_second_div)
    base.expect(second_div_page["total"] == 1 and second_div_page["items"][0]["id"] == other_div["id"], "student in multiple divisions gets independent catalogue")
    initial_snapshot = snapshot(admin)
    base.expect(own_snapshot(initial_snapshot, source)["results"] == [] and outside["id"] not in [item["test_id"] for item in initial_snapshot["external_tests"]],
                "rating source explicitly includes missing grade and excludes other calendar date")

    # Manual correction is an explicit Go extension; importing still cannot overwrite.
    write_headers = base.key()
    zero_body = {"expected_revision": source["revision"], "expected_result_revision": 0, "score": 0}
    source = base.api(path(source, "/results/" + str(first["account_id"])), editor, "PUT", zero_body, headers=write_headers)
    base.api(path(source, "/results/" + str(first["account_id"])), editor, "PUT", zero_body, headers=write_headers)
    base.api(path(source, "/results/" + str(first["account_id"])), editor, "PUT", {**zero_body, "score": 1}, 409, write_headers)
    own = detail(source, student)["result"]
    base.expect(own["score"] == 0 and own["student_id"] == first["student_id"] and own["account_id"] == first["account_id"], "confirmed zero keeps separate account/profile IDs")
    base.expect(detail(source, other_student)["result"] is None and detail(other_div, student_second_div)["result"] is None,
                "one student's zero never expands to another pupil or division")
    results(source, student, 403, {"account_id": second["account_id"]})
    base.expect(results(source, student)["total"] == 1 and results(source, viewer)["total"] == 1, "student self and results viewer receive allowed result count")
    results(source, catalog, 403)
    manual(source, viewer, second["account_id"], 80, code=403)
    manual(source, catalog, second["account_id"], 80, code=403)
    manual(source, editor_second_div, second["account_id"], 80, code=403)
    manual(source, editor, second["account_id"], -1, code=400)
    manual(source, editor, second["account_id"], 100.1, code=400)
    base.api(path(source, "/results/" + str(second["account_id"])), editor, "PUT",
             {"expected_revision": source["revision"], "expected_result_revision": 0}, 400, base.key())
    manual(source, editor, foreign_student["account_id"], 80, code=404)
    source = manual(source, editor, first["account_id"], 87.125, 1)
    base.expect(detail(source, student)["result"]["score"] == 87.125, "manual fractional score is retained without integer rounding")
    manual(source, editor, first["account_id"], 70, 1, code=409)
    source = manual(source, editor, second["account_id"], None)
    base.expect(detail(source, other_student)["result"]["score"] is None and detail(source, other_student)["result"] is not None,
                "unknown manual grade remains a result distinct from absence")
    source = base.api(path(source, "/results/" + str(second["account_id"])), editor, "DELETE",
                      {"expected_revision": source["revision"], "expected_result_revision": 1, "score": None}, headers=base.key())
    base.expect(detail(source, other_student)["result"] is None, "result deletion restores explicit absence")
    history = base.api(path(source, "/history"), viewer)
    base.expect(any(item["action"] == "result_deleted" for item in history["items"]) and any(item["result_changes"] for item in history["items"]), "authorized history retains result overwrites and deletions")
    hidden_history = base.api(path(source, "/history"), catalog)
    base.expect(all(not item["result_changes"] for item in hidden_history["items"]), "catalogue-only staff cannot read historical scores")
    changed_snapshot = snapshot(admin)
    base.expect(changed_snapshot["input_hash"] != initial_snapshot["input_hash"] and own_snapshot(changed_snapshot, source)["results"][0]["score"] == 87.125,
                "rating source hash changes with persisted external grade")

    # Domain imports are independent of the general uploads workflow.
    imported = create(admin, direction, "import")
    source_rows = rows((first, 0), (second, 0.9))
    preview, request = validate(imported, viewer, source_rows)
    base.expect(preview["can_commit"] and preview["summary"]["import_rows"] == 2, "view-only staff can validate domain rows")
    internal("external-results/validate", editor, request, 401, service=False)
    internal("external-results/validate", None, request, 401)
    internal("external-results/validate", student, request, 403)
    internal("external-results/validate", uploader, request, 403)
    internal("external-results/validate", foreign_admin, request, 403)
    internal("external-results/validate", editor_second_div, request, 403)
    ambiguous_row = {"row_id": "ambiguous", "row": 3, "full_name": first["full_name"], "percent": 10}
    bad_preview, _ = validate(imported, editor, [ambiguous_row])
    base.expect(not bad_preview["can_commit"] and len(bad_preview["rows"][0]["candidates"]) == 2, "automatic name match includes inactive historical pupil and exposes ambiguity")
    for bad_rows in [
        [source_rows[0], {**source_rows[1], "full_name": first["full_name"]}],
        [source_rows[0], {**source_rows[1], "account_id": first["account_id"], "legacy_student_id": first["student_id"]}],
        [source_rows[0], {**source_rows[1], "row_id": source_rows[0]["row_id"]}],
        [{**source_rows[0], "percent": None}],
        [{**source_rows[0], "percent": -1}],
        [{**source_rows[0], "percent": 100.1}],
        [{**source_rows[0], "legacy_student_id": second["student_id"]}],
        [{**source_rows[0], "account_id": foreign_student["account_id"]}],
    ]:
        failed_preview, _ = validate(imported, editor, bad_rows)
        base.expect(not failed_preview["can_commit"] and failed_preview["summary"]["row_errors"] > 0, "invalid mapping/score/duplicate cannot be committed")
    invalid_request = {**request, "rows": [{**source_rows[0], "percent": True}]}
    internal("external-results/validate", editor, invalid_request, 400)
    commit = command(imported, editor, source_rows)
    internal("external-results/commit", viewer, commit, 403)
    bad_commit = {**commit, "command_id": str(uuid.uuid4()), "rows": [source_rows[0], {**source_rows[1], "percent": None}]}
    internal("external-results/commit", editor, bad_commit, 400)
    base.expect(results(imported, viewer)["total"] == 0 and detail(imported, editor)["revision"] == 1, "one bad row produces no partial official results or revision")

    with ThreadPoolExecutor(max_workers=2) as pool:
        raced = list(pool.map(lambda _: raw_commit(commit, editor), range(2)))
    receipt = raced[0][1]["data"]
    base.expect(receipt == raced[1][1]["data"] and any(status == 201 for status, _ in raced), "concurrent identical commits share one original receipt")
    replay = internal("external-results/commit", editor, commit)
    base.expect(replay == receipt and results(imported, viewer)["total"] == 2, "retry neither duplicates results nor changes receipt")
    internal("external-results/commit", editor, {**commit, "upload_preview_revision": 2}, 409)
    imported = detail(imported, editor)
    existing_preview, _ = validate(imported, editor, source_rows)
    base.expect(not existing_preview["can_commit"] and existing_preview["summary"]["existing_results"] == 2, "import never overwrites existing zero or decimal score")
    base.expect(detail(imported, student)["result"]["score"] == 0 and detail(imported, other_student)["result"]["score"] == 0.9,
                "imported zero/fractional scores reach own pupil DTOs")
    check_database(imported, receipt)

    # Frozen preview becomes stale after a concurrent catalogue edit.
    stale = create(admin, direction, "stale import")
    stale_commit = command(stale, editor, source_rows)
    stale = base.api(path(stale), editor, "PUT", {"expected_revision": stale["revision"], "name": stale["name"] + " renamed",
                                                  "direction_id": direction, "date": DAY})
    internal("external-results/commit", editor, stale_commit, 409)
    base.expect(results(stale, viewer)["total"] == 0, "stale preview does not write results")

    changed_person_test = create(admin, direction, "changed person preview")
    changed_person_commit = command(changed_person_test, editor, source_rows[:1])
    base.core(f'/divisions/1001/users/{first["account_id"]}', admin, "PUT",
              {"full_name": first["full_name"] + " Новый", "class": 10, "tg_name": ""})
    internal("external-results/commit", editor, changed_person_commit, 409)
    base.expect(results(changed_person_test, viewer)["total"] == 0, "changed live pupil projection invalidates preview without partial write")
    base.core(f'/divisions/1001/users/{first["account_id"]}', admin, "PUT",
              {"full_name": first["full_name"], "class": 10, "tg_name": ""})

    # An already confirmed command is still checked against live permissions.
    base.core("/roles/" + str(editor_role), admin, "PUT", {"name": PREFIX + " revoked", "permissions": all_view})
    raw_commit(commit, editor, (401, 403))
    readonly_editor = base.login(editor_person["login"])
    internal("external-results/commit", readonly_editor, commit, 403)
    base.core("/roles/" + str(editor_role), admin, "PUT", {"name": PREFIX + " restored", "permissions": all_edit})
    editor = base.login(editor_person["login"])
    base.expect(internal("external-results/commit", editor, commit) == receipt, "restored actor replays original confirmed command")

    # Preview-bound deletion and durable history survive logical removal.
    old_delete = base.api(path(source, "/delete-preview"), admin)
    source = manual(source, editor, first["account_id"], 90, 2)
    base.api(path(source), admin, "DELETE", {"expected_revision": old_delete["test"]["revision"],
                                            "confirmation_token": old_delete["confirmation_token"]}, 409)
    base.api(path(source, "/delete-preview"), catalog, code=403)
    fresh_delete = base.api(path(source, "/delete-preview"), admin)
    delete_body = {"expected_revision": fresh_delete["test"]["revision"], "confirmation_token": fresh_delete["confirmation_token"]}
    base.expect(fresh_delete["results_count"] == 1, "delete preview reports current official result count")
    base.api(path(source), admin, "DELETE", delete_body)
    base.api(path(source), admin, "DELETE", delete_body)
    detail(source, student, 404)
    after_delete = snapshot(admin)
    base.expect(source["id"] not in [item["test_id"] for item in after_delete["external_tests"]], "deleted external test disappears from rating inputs")
    check_database(source)
    base.expect(any(item["action"] == "deleted" for item in base.api(path(source, "/history"), admin)["items"]), "deleted test history remains available to authorized admin")

    tombstone = create(admin, direction, "receipt after deletion")
    tombstone_command = command(tombstone, editor, source_rows[:1])
    tombstone_receipt = internal("external-results/commit", editor, tombstone_command, 201)
    tombstone = detail(tombstone, editor)
    tombstone_preview = base.api(path(tombstone, "/delete-preview"), admin)
    base.api(path(tombstone), admin, "DELETE", {"expected_revision": tombstone_preview["test"]["revision"],
                                              "confirmation_token": tombstone_preview["confirmation_token"]})
    base.expect(internal("external-results/commit", editor, tombstone_command) == tombstone_receipt,
                "confirmed import can be reconciled after catalogue deletion without resurrection")
    internal("external-results/commit", editor, {**tombstone_command, "command_id": str(uuid.uuid4())}, 404)
    detail(tombstone, student, 404)
    check_database(tombstone, tombstone_receipt)

    for token in base.sessions:
        logout = urllib.request.Request(base.AUTH + "/v1/auth/logout", method="POST",
                                        headers={"Authorization": "Bearer " + token})
        try:
            with urllib.request.urlopen(logout, timeout=10) as response:
                base.expect(response.status == 200, "owned test session logged out")
        except urllib.error.HTTPError as response:
            base.expect(response.code == 401, "previously revoked test session remains invalid")
    summary = {"status": "passed", "checks": len(base.checks), "checked_at": datetime.now(timezone.utc).isoformat(),
               "fixtures_retained": True, "fixtures": fixtures, "production_calls": False,
               "foreign_database_writes": False, "tests_database_inspection": "read_only_own_fixture_ids"}
    report_path.write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps({"status": summary["status"], "checks": summary["checks"], "report": str(report_path)}, ensure_ascii=False))


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        # Record identities for owner-API recovery without exposing any token,
        # password, raw request body or authentication configuration.
        report_path.write_text(json.dumps({"status": "failed", "checks": len(base.checks), "fixtures_retained": True,
                                           "fixtures": fixtures, "failure_type": type(exc).__name__}, ensure_ascii=False, indent=2) + "\n")
        raise
