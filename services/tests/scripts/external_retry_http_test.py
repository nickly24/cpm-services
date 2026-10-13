"""Narrow concurrent retry contract over loopback dev; API-owned fixtures only.

Run only after the owner announces EDGE FINAL READY for the Tests image with
manual-result and catalogue-delete CAS replay reconciliation. Reuse the active
org1001 direction created by the completed broad suite, without changing that
direction. Mongo queries are read-only and limited to this run's external
aggregate and selected history IDs.
"""
from concurrent.futures import ThreadPoolExecutor
from copy import deepcopy
from datetime import datetime, timezone
import json
import secrets
from threading import Barrier
import urllib.error
import urllib.request

from bson import ObjectId

import http_test as base


RUN = secrets.token_hex(6)
PREFIX = "DEV External Retry " + RUN
REPORT = base.ROOT / "dev/local" / ("external-retry-http-" + RUN + ".json")
fixtures = {"run": RUN, "prefix": PREFIX, "organization_id": 1001,
            "division_id": 1001, "direction_id": None, "student": None,
            "test_id": None}
audit = []
ABSENT = object()


def route(suffix=""):
    return "/divisions/1001/external-tests/" + fixtures["test_id"] + suffix


def race(path, token, method, body, headers=None):
    """Release four identical deliveries together, retaining each HTTP result."""
    barrier = Barrier(4)

    def deliver(_):
        h = {"Authorization": "Bearer " + token, "Content-Type": "application/json", **(headers or {})}
        req = urllib.request.Request(base.TESTS + "/v1/tests" + path,
                                     method=method, data=json.dumps(body).encode(), headers=h)
        barrier.wait(timeout=20)
        try:
            with urllib.request.urlopen(req, timeout=40) as response:
                status, value = response.status, json.load(response)
        except urllib.error.HTTPError as response:
            status, value = response.code, json.load(response)
        return status, value

    with ThreadPoolExecutor(max_workers=4) as executor:
        outcomes = list(executor.map(deliver, range(4)))
    base.expect(all(status == 200 and value.get("status") is True for status, value in outcomes),
                method + " concurrent identical deliveries all replay HTTP 200")
    return [value["data"] for _, value in outcomes]


def inspect(revision, expected_actions, receipts, result_revision=None, score=ABSENT, result_id=None, deleted=False):
    client = base.mongo_client(base.settings())
    try:
        db = client.cpm_dev
        test_id = ObjectId(fixtures["test_id"])
        scope = {"_id": test_id, "organization_id": 1001, "division_id": 1001}
        doc = db.tests_external_catalog_v1.find_one(scope)
        base.expect(doc is not None and doc["name"].startswith(PREFIX), "inspection targets only this synthetic scoped aggregate")
        base.expect(doc["revision"] == revision and doc["is_deleted"] is deleted,
                    "one aggregate revision selected for concurrent mutation")
        rows = doc["results"]
        if score is ABSENT:
            base.expect(rows == [], "deleted current result remains absent")
        else:
            base.expect(len(rows) == 1 and rows[0]["account_id"] == fixtures["student"]["account_id"]
                        and rows[0]["student_id"] == fixtures["student"]["profile_id"],
                        "one current result preserves distinct account/profile identity")
            base.expect(rows[0]["revision"] == result_revision and rows[0]["score"] == score,
                        "one result revision preserves exact zero or decimal score")
            if result_id is not None:
                base.expect(rows[0]["_id"] == result_id, "result update preserves physical result identity")
        commands = doc["commands"]
        base.expect(len(commands) == len(receipts), "concurrent manual delivery selects only one receipt per command")
        for key, source_revision in receipts.items():
            selected = [row for row in commands if row["command_id"] == "manual:" + key]
            base.expect(len(selected) == 1 and selected[0]["source_revision"] == source_revision
                        and selected[0]["actor_id"] == doc["created_by"] and bool(selected[0]["input_hash"]),
                        "durable manual receipt binds actor, hash and committed revision")
        history_ids = doc["history_ids"]
        base.expect(len(history_ids) == len(expected_actions) and len(set(history_ids)) == len(history_ids),
                    "aggregate selects one committed history ID per mutation")
        history = list(db.tests_external_versions_v1.find({"_id": {"$in": history_ids},
                    "test_id": test_id, "organization_id": 1001, "division_id": 1001}).sort("revision", 1))
        base.expect([row["revision"] for row in history] == list(range(1, revision + 1))
                    and [row["action"] for row in history] == expected_actions,
                    "committed history contains exactly one ordered mutation per revision")
        for item in history:
            base.expect(item["actor_id"] == doc["created_by"], "committed audit retains authorized actor")
        if revision >= 2:
            first = history[1]["result_changes"]
            base.expect(len(first) == 1 and first[0]["before"] is None
                        and first[0]["after"]["revision"] == 1 and first[0]["after"]["score"] == 0,
                        "creation audit preserves confirmed zero and original row revision")
        if revision >= 3:
            changed = history[2]["result_changes"]
            base.expect(len(changed) == 1 and changed[0]["before"]["revision"] == 1
                        and changed[0]["before"]["score"] == 0
                        and changed[0]["after"]["revision"] == 2
                        and changed[0]["after"]["score"] == 87.125
                        and changed[0]["before"]["_id"] == changed[0]["after"]["_id"],
                        "update audit records one exact before/after result transition")
        if revision >= 4:
            removed = history[3]["result_changes"]
            base.expect(len(removed) == 1 and removed[0]["before"]["revision"] == 2
                        and removed[0]["after"] is None,
                        "result deletion audit selects one removal without duplicate transitions")
        if deleted:
            base.expect(history[-1]["metadata_before"]["is_deleted"] is False
                        and history[-1]["metadata_after"]["is_deleted"] is True,
                        "catalogue deletion selects one logical tombstone audit")
        # Failed CAS can leave unselected immutable audit documents; these are
        # intentionally excluded from the public history by the aggregate IDs.
        stored_history = db.tests_external_versions_v1.count_documents({"test_id": test_id,
                         "organization_id": 1001, "division_id": 1001})
        audit.append({"revision": revision, "selected_history": len(history_ids),
                      "unselected_history": stored_history - len(history_ids), "receipts": len(commands)})
        return doc
    finally:
        client.close()


def main():
    base.settings()  # Refuses any contour other than fixed cpm_dev loopback ports.
    base.request("/health/ready")
    admin = base.login("dev_admin")
    previous = json.loads((base.ROOT / "dev/local/external-tests-integration-summary.json").read_text())
    old_fixtures = previous["fixtures"]
    base.expect(previous["status"] == "passed" and old_fixtures["prefix"].startswith("DEV External "),
                "reuse direction only from the completed owned broad fixture run")
    direction_id = old_fixtures["directions"][0]  # Broad suite records org1001 before org2001.
    active = base.core("/directions", admin)
    base.expect(any(row["id"] == direction_id and row["name"] == old_fixtures["prefix"] + " direction"
                    for row in active), "owned broad direction is still active in organization1001")
    fixtures["direction_id"] = direction_id
    fixtures["direction_reused"] = True
    fixtures["direction_source_run"] = old_fixtures["run"]
    login = "dev_ext_retry_" + RUN
    created = base.core("/divisions/1001/users", admin, "POST", {
        "role": "student", "full_name": "Ретрай" + RUN + " Ученик", "login": login,
        "password": base.password, "class": 10}, 201)
    profile = base.core("/divisions/1001/users/" + str(created["account_id"]), admin)
    fixtures["student"] = {"account_id": created["account_id"], "profile_id": profile["profile_id"], "login": login}
    test = base.api("/divisions/1001/external-tests", admin, "POST", {
        "name": PREFIX + " test", "direction_id": direction_id, "date": "2041-05-06"}, 201, base.key())
    fixtures["test_id"] = test["id"]
    account = fixtures["student"]["account_id"]
    result_path = route("/results/" + str(account))
    actions = ["created"]
    receipts = {}
    inspect(1, actions, receipts)

    # New row: a confirmed zero must not become a missing result on any replay.
    create_key = base.key()
    create_body = {"expected_revision": 1, "expected_result_revision": 0, "score": 0}
    values = race(result_path, admin, "PUT", create_body, create_key)
    base.expect(all(row["id"] == test["id"] for row in values), "new-row retries return the original test identity")
    receipts[create_key["Idempotency-Key"]] = 2
    actions.append("result_updated")
    created_row = inspect(2, actions, receipts, 1, 0)["results"][0]
    base.api(result_path, admin, "PUT", {**create_body, "score": 42}, 409, create_key)
    inspect(2, actions, receipts, 1, 0, created_row["_id"])

    # Update: all duplicate requests must select only one new row revision.
    update_key = base.key()
    update_body = {"expected_revision": 2, "expected_result_revision": 1, "score": 87.125}
    race(result_path, admin, "PUT", update_body, update_key)
    receipts[update_key["Idempotency-Key"]] = 3
    actions.append("result_updated")
    inspect(3, actions, receipts, 2, 87.125, created_row["_id"])
    changed = deepcopy(update_body)
    changed["score"] = None
    base.api(result_path, admin, "PUT", changed, 409, update_key)
    inspect(3, actions, receipts, 2, 87.125, created_row["_id"])

    # Result deletion: replay has to survive the current row becoming absent.
    delete_key = base.key()
    delete_body = {"expected_revision": 3, "expected_result_revision": 2}
    race(result_path, admin, "DELETE", delete_body, delete_key)
    receipts[delete_key["Idempotency-Key"]] = 4
    actions.append("result_deleted")
    inspect(4, actions, receipts)
    base.api(result_path, admin, "DELETE", {**delete_body, "expected_result_revision": 1}, 409, delete_key)
    inspect(4, actions, receipts)

    # Confirmation-bound catalogue deletion has the same concurrent retry rule.
    preview = base.api(route("/delete-preview"), admin)
    body = {"expected_revision": 4, "confirmation_token": preview["confirmation_token"]}
    values = race(route(), admin, "DELETE", body)
    base.expect(all(row == {"test_id": test["id"], "results_deleted": 0} for row in values),
                "catalogue delete duplicates return the same tombstone outcome")
    actions.append("deleted")
    inspect(5, actions, receipts, deleted=True)
    base.api(route(), admin, "DELETE", body)
    inspect(5, actions, receipts, deleted=True)
    base.api(route(), admin, code=404)


def logout():
    for token in base.sessions:
        req = urllib.request.Request(base.AUTH + "/v1/auth/logout", method="POST",
                                     headers={"Authorization": "Bearer " + token})
        try:
            with urllib.request.urlopen(req, timeout=10) as response:
                base.expect(response.status == 200, "own fixture session logged out")
        except urllib.error.HTTPError as response:
            base.expect(response.code == 401, "own fixture session already revoked")


if __name__ == "__main__":
    status, failure = "failed", None
    try:
        main()
        status = "passed"
    except Exception as error:
        failure = type(error).__name__
        raise
    finally:
        logout()
        report = {"status": status, "checks": len(base.checks), "scenarios": base.checks,
                  "fixtures": fixtures, "audit": audit, "fixtures_retained": True,
                  "foreign_database_writes": False, "production_calls": False,
                  "tests_database_inspection": "read_only_own_fixture_ids",
                  "checked_at": datetime.now(timezone.utc).isoformat()}
        if failure is not None:
            report["error_type"] = failure
        REPORT.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
        REPORT.chmod(0o600)
        print(json.dumps({"status": status, "checks": len(base.checks), "report": str(REPORT)}))
