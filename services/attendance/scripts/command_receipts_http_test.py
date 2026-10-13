"""Targeted actual Attendance command-receipt acceptance; source-only until READY.

--plan/--help never read credentials, start HTTP or change fixtures. --run requires
an agreed Attendance/Auth/Core owner window. --allow-core-writes is a separate,
later-approved option for own temporary staff grants and proctor assignment.
No SQL, fake peers, runtime changes, QR persistence or request/response body logs.
"""
import argparse
import datetime as dt
import json
import sys
import time
import urllib.error
import urllib.request
import uuid
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
REPORT = ROOT / "dev/local/attendance-command-receipts-http-report.json"
FIXTURES = ROOT / "dev/local/attendance-scope-fixtures.json"
ORIGINS = {"auth": "http://127.0.0.1:8002", "core": "http://127.0.0.1:8003",
           "attendance": "http://127.0.0.1:8013"}
PLAN = {
    "baseline": {"new_attendance_commands": 11, "core_writes": 0,
        "scope": "existing own org1001 divisions10150/10151, student391; new DEV UUID resources on unused 2041 dates",
        "operations": ["fresh nondefault type", "empty day create/delete", "separate class day/manual mark391",
                       "archive the fresh type", "student own pending zap without images", "organization room",
                       "new D1/D2 lessons", "bulk noop updateD1 and deleteD2 with complete original scope"],
        "reads": ["own metadata receipts", "byte-exact public replay without private scope metadata",
                  "foreign actor/org and missing UUID404", "invalid UUID/query/body/HEAD",
                  "deleted day and archived type receipts", "current cabinet independent of explicit scope",
                  "GET/replay leave revision and public audit unchanged"]},
    "optional_allow_core_writes": {"new_attendance_commands": 4, "core_writes": 10,
        "operations": ["new dedicated schedule role temporarily bound only to staff394 D1/D2",
                       "own staff bulk; restore D2 revoked binding:403; regrant:200",
                       "restore both original staff bindings and delete unused new role",
                       "own proctor395 mark; unassign group3065:403; restore:200"],
        "precondition": "own proctor is already assigned; staff bindings match existing fixture roles; explicit later approval required"},
    "legacy_deferred": "known canonical BFF delta receipt belongs to another owner's actor; no login or receipt probe",
    "go_only_not_actual_http": ["legacy division200/public replay: original actor and original bytes unavailable",
                                "legacy bulk409 if no own existing canonical UUID", "late original request before Mutate",
                                "QR expiry/rotation", "corrupt/missing audit/private scope", "historical proctor group move"],
    "forbidden": ["shared role edits", "foreign SQL", "fake Core/test hooks", "QR/body/credential persistence",
                  "runtime/restarts", "automatic retry with a new revision or UUID"],
}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class Acceptance:
    def __init__(self, allow_core):
        self.stage, self.failure, self.clients = "read-private-fixtures", None, []
        self.checks, self.cases, self.commands, self.resources = [], [], [], []
        self.native_writes, self.core_writes, self.http_calls = 0, 0, 0
        self.allow_core, self.cleanup_errors, self.core_commands = allow_core, [], []
        self.run = uuid.uuid4().hex[:10]
        self.password = json.loads((ROOT / "dev/local/accounts.json").read_text())["password"]
        self.f = json.loads(FIXTURES.read_text())
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect)

    def expect(self, condition, label):
        if not condition:
            raise AssertionError(label)
        self.checks.append(label)

    def checkpoint(self, status="running"):
        value = {"status": status, "checked_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "run": self.run, "fixture_run": self.f.get("run"), "checks": self.checks, "case_results": self.cases,
            "actual_http_case_count": sum(case["status"] == "actual_http" for case in self.cases),
            "deferred_case_count": sum(case["status"] != "actual_http" for case in self.cases),
            "commands": self.commands, "resources": self.resources, "native_committed_commands": self.native_writes,
            "core_write_attempts": self.core_writes, "core_commands": self.core_commands,
            "http_calls": self.http_calls, "core_option_enabled": self.allow_core,
            "cleanup_errors": self.cleanup_errors, "go_only_not_actual_http": PLAN["go_only_not_actual_http"],
            "scope": "own synthetic fixtures; fresh Auth sessions; no SQL/runtime/QR/body/credentials in report"}
        if self.failure:
            value["failure"] = self.failure
        temp = REPORT.with_suffix(".tmp")
        temp.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")
        temp.replace(REPORT)

    def http(self, service, path, method="GET", data=None, raw=None, token=None,
             key=None, want=200, error=None, json_response=True, extra_headers=None):
        headers = {"Content-Type": "application/json"}
        if token:
            headers["Authorization"] = "Bearer " + token
        if key:
            headers["Idempotency-Key"] = key
        headers.update(extra_headers or {})
        encoded = raw if raw is not None else (json.dumps(data, ensure_ascii=False, separators=(",", ":")).encode()
                  if data is not None else None)
        request = urllib.request.Request(ORIGINS[service] + path, data=encoded, headers=headers, method=method)
        self.http_calls += 1
        try:
            response = self.opener.open(request, timeout=30)
        except urllib.error.HTTPError as failure:
            response = failure
        with response:
            status, body, received = response.code, response.read((4 << 20) + 1), response.headers
        self.expect(status == want, f"{service} {method} status is {want}")
        self.expect(len(body) <= 4 << 20, service + " response is bounded")
        if service == "attendance":
            self.expect("no-store" in received.get("Cache-Control", ""), "Attendance response disables caching")
            self.expect(received.get("X-Content-Type-Options") == "nosniff", "Attendance response has nosniff")
        if not json_response:
            return None
        try:
            result = json.loads(body)
        except (ValueError, UnicodeDecodeError):
            raise AssertionError("invalid response JSON") from None
        self.expect(isinstance(result, dict), "response is an object envelope")
        if service != "auth":
            self.expect(result.get("status") is (status < 400), "HTTP status matches response envelope")
        if error:
            self.expect(result.get("error") == error, "response has the exact contracted error")
        return result

    def login(self, login, org, division):
        client = Client(self, login, org, division)
        return client

    def command(self, client, path, data, method="POST", divisions=None, scope="division"):
        # Fresh command only. Preserve its identity before dispatch; its original
        # bytes remain RAM-only for exact replay, never written to the report.
        revision = client.read("/resources/rooms")["revision"]
        key = str(uuid.uuid4())
        raw = json.dumps({"expected_revision": revision, "data": data}, ensure_ascii=False, separators=(",", ":")).encode()
        record = {"command_id": key, "organization_id": client.org, "actor_account_id": client.account,
                  "division_ids": sorted(divisions or []), "scope": scope, "method": method, "path": path,
                  "state": "unknown"}
        self.commands.append(record)
        self.checkpoint()
        result = client.native(path, method, raw=raw, key=key)["data"]
        self.expect(set(result) == {"revision", "value"}, "public command response contains only original Result")
        self.expect("scope_metadata" not in json.dumps(result), "public response excludes private receipt scope")
        record.update(state="committed", revision=result["revision"])
        self.native_writes += 1
        self.checkpoint()
        return {"record": record, "raw": raw, "result": result, "client": client}

    def receipt(self, client, command, want=200, error=None):
        record = command["record"] if "record" in command else command
        response = client.native("/command-receipts/" + record["command_id"], want=want, error=error)
        if want != 200:
            return response
        result = response["data"]
        self.expect(set(result) == {"revision", "value"}, "receipt uses standard revision/value envelope")
        self.expect(result["revision"] == record["revision"], "receipt retains original committed revision")
        expected = {"contract_version": 1, "command_id": record["command_id"], "state": "committed",
                    "organization_id": record["organization_id"], "scope": record["scope"],
                    "division_ids": record["division_ids"], "method": record["method"], "path": record["path"]}
        self.expect(result["value"] == expected, "receipt contains exactly metadata-only original command identity")
        self.expect(not result["value"]["path"].startswith("/v1/"), "receipt path remains command-relative")
        return result

    def readonly(self, admin, label, operation):
        before = admin.read("/resources/rooms")["revision"]
        audit_before = admin.read("/audit?after_revision=" + str(before))["value"]
        result = operation()
        after = admin.read("/resources/rooms")["revision"]
        audit_after = admin.read("/audit?after_revision=" + str(before))["value"]
        self.expect(before == after and audit_before == audit_after, label + " leaves native revision/public audit unchanged")
        return result

    def replay(self, admin, command):
        def operation():
            row = command["record"]
            result = command["client"].native(row["path"], row["method"], raw=command["raw"], key=row["command_id"])["data"]
            self.expect(result == command["result"], "byte-exact replay retains original public Result")
            self.expect(set(result) == {"revision", "value"} and "scope_metadata" not in json.dumps(result),
                        "exact public replay cannot leak stored private scope metadata")
        self.readonly(admin, "exact replay", operation)

    def core(self, admin, path, data=None, method="GET", want=200, effect=None):
        record = None
        if method != "GET":
            self.expect(self.allow_core, "Core mutation requires the separately approved option")
            self.core_writes += 1
            record = {"method": method, "path": path, "organization_id": admin.org, "state": "unknown"}
            if effect is not None:
                record["expected_effect"] = effect
            self.core_commands.append(record)
            self.checkpoint()
        result = self.http("core", "/v1/core" + path, method, data=data, token=admin.token, want=want)["data"]
        if record is not None:
            record["state"] = "confirmed"
            self.checkpoint()
        return result

    def reconcile_core(self, path, observed_effect, final_state=None):
        # An observed requested effect can reconcile a lost response. Seeing
        # the original state cannot prove a delayed request will never commit.
        # Preserve unknown in that case, record the observation and require
        # human review before a fresh acceptance run.
        for record in self.core_commands:
            if record["path"] != path or record["state"] not in ("unknown", "reconciled_effect_observed"):
                continue
            record["last_observation"] = observed_effect
            if record["state"] == "unknown" and observed_effect == record.get("expected_effect"):
                record["state"] = "reconciled_effect_observed"
                record["effect_observed"] = observed_effect
            if final_state is not None:
                record["cleanup_observation"] = final_state
        self.checkpoint()


class Client:
    def __init__(self, test, login, org, division):
        self.test, self.org, self.division, self.closed = test, org, division, False
        self.token, self.account = None, None
        test.clients.append(self)
        result = test.http("auth", "/v1/auth/login", "POST", data={"login": login, "password": test.password,
                           "organization_id": org, "division_id": division})
        self.token = result.get("access_token")
        test.expect(isinstance(self.token, str) and bool(self.token), "fresh Auth returned a session token")
        self.account = result["user"]["account_id"]

    def native(self, path, method="GET", **kwargs):
        return self.test.http("attendance", "/v1/attendance" + path, method, token=self.token, **kwargs)

    def read(self, path):
        return self.native(path)["data"]

    def context(self, division):
        result = self.test.http("auth", "/v1/auth/context", "POST", data={"organization_id": self.org,
                                "division_id": division}, token=self.token)
        self.token, self.division = result["access_token"], division

    def logout(self):
        if not self.closed and self.token:
            self.test.http("auth", "/v1/auth/logout", "POST", data={}, token=self.token)
            self.closed = True


def lesson(division, date, tag):
    return {"division_id": division, "date": date, "start_time": "12:00", "end_time": "13:00",
            "lesson_name": "DEV U04 receipt " + tag, "teacher_name": "DEV receipt teacher",
            "location": "DEV receipt site", "classroom": "U04", "color": "#228855",
            "is_public": True, "is_for_all": True, "target_group_ids": []}


def new_bulk(test, client, date, tag):
    f = test.f
    first_input, second_input = lesson(f["d1"], date, tag + " first"), lesson(f["d2"], date, tag + " deleted")
    first = test.command(client, f'/divisions/{f["d1"]}/schedule', first_input, divisions=[f["d1"]])
    second = test.command(client, f'/divisions/{f["d2"]}/schedule', second_input, divisions=[f["d2"]])
    first_id, second_id = first["result"]["value"]["created_ids"][0], second["result"]["value"]["created_ids"][0]
    payload = {"creates": [], "updates": [{**first_input, "id": first_id}],
               "deletes": [{"id": second_id, "division_id": f["d2"]}]}
    bulk = test.command(client, "/schedule/bulk", payload, divisions=[f["d1"], f["d2"]], scope="divisions")
    test.expect(bulk["result"]["value"] == {"created_ids": [], "updated_count": 1, "deleted_count": 1},
                "new bulk is a genuine noop update plus delete, with no created IDs to infer scope from")
    test.resources += [{"kind": "lesson", "division_id": f["d1"], "id": first_id},
                       {"kind": "deleted_lesson", "division_id": f["d2"], "id": second_id}]
    return bulk


def optional_live_access(test, admin, mark_day, date, active_type):
    f, d1, d2 = test.f, test.f["d1"], test.f["d2"]
    test.stage = "optional-own-staff-grants"
    rows = test.core(admin, f'/organization/users/{f["staff"]["account_id"]}/memberships')["memberships"]
    original = {row["division_id"]: row["role_id"] for row in rows}
    test.expect(original.get(d1) == f["role_view"] and original.get(d2) == f["role_edit"],
                "only expected own staff fixture bindings may be temporarily changed")
    role = test.core(admin, "/roles", {"name": "DEV U04 receipts " + test.run,
                "permissions": {"schedule": {"view": True, "edit": True}}}, "POST", 201)["id"]
    role_resource = {"kind": "temporary_core_role", "organization_id": admin.org, "id": role, "state": "created"}
    test.resources.append(role_resource)
    bindings_changed = []
    try:
        for division in [d1, d2]:
            bindings_changed.append(division)
            test.core(admin, f'/divisions/{division}/memberships/{f["staff"]["account_id"]}', {"role_id": role}, "PUT",
                      effect={"role_id": role})
        staff = test.login(f["staff"]["login"], 1001, d1)
        bulk = new_bulk(test, staff, date, test.run + " staff")
        test.readonly(admin, "staff receipt GET", lambda: test.receipt(staff, bulk))
        test.core(admin, f'/divisions/{d2}/memberships/{f["staff"]["account_id"]}', {"role_id": original[d2]}, "PUT",
                  effect={"role_id": original[d2]})
        test.readonly(admin, "revoked original bulk target", lambda: test.receipt(staff, bulk, 403, "forbidden"))
        test.core(admin, f'/divisions/{d2}/memberships/{f["staff"]["account_id"]}', {"role_id": role}, "PUT",
                  effect={"role_id": role})
        test.readonly(admin, "restored original bulk target", lambda: test.receipt(staff, bulk))
        test.replay(admin, bulk)
        test.cases.append({"case": "own staff bulk original division grant revoke/restore", "status": "actual_http"})
    finally:
        # Restore only our own temporary binding. Skip detected third-party
        # changes; never edit an existing role's permissions. The agreed owner
        # window remains necessary because Core grant has no conditional CAS.
        current = None
        try:
            current = {row["division_id"]: row["role_id"] for row in
                       test.core(admin, f'/organization/users/{f["staff"]["account_id"]}/memberships')["memberships"]}
        except Exception as error:
            test.cleanup_errors.append({"stage": "read-own-staff-bindings-for-restore", "type": type(error).__name__})
        for division in bindings_changed:
            if current is None:
                continue
            if current.get(division) not in (role, original[division]):
                test.cleanup_errors.append({"stage": "restore-own-staff-binding", "type": "ConcurrentChange"})
                continue
            binding_path = f'/divisions/{division}/memberships/{f["staff"]["account_id"]}'
            test.reconcile_core(binding_path, {"role_id": current[division]})
            try:
                test.core(admin, binding_path, {"role_id": original[division]}, "PUT",
                          effect={"role_id": original[division]})
            except Exception as error:
                test.cleanup_errors.append({"stage": "restore-own-staff-binding", "type": type(error).__name__})
        if not test.cleanup_errors:
            try:
                restored = {row["division_id"]: row["role_id"] for row in
                            test.core(admin, f'/organization/users/{f["staff"]["account_id"]}/memberships')["memberships"]}
                test.expect(all(restored.get(division) == original[division] for division in [d1, d2]),
                            "both own staff bindings returned to their exact original roles")
                for division in [d1, d2]:
                    test.reconcile_core(f'/divisions/{division}/memberships/{f["staff"]["account_id"]}',
                                        {"role_id": restored[division]}, {"role_id": original[division]})
                test.core(admin, f"/roles/{role}", method="DELETE")
                role_resource["state"] = "deleted"
            except Exception as error:
                test.cleanup_errors.append({"stage": "verify-staff-restore-and-delete-own-role", "type": type(error).__name__})

    test.stage = "optional-own-proctor-assignment"
    group = next(row for row in test.core(admin, f"/divisions/{d1}/groups") if row["id"] == f["group"])
    test.expect(group.get("proctor_account_id") == f["proctor"]["account_id"],
                "optional proctor proof requires its unchanged own existing assignment")
    proctor = test.login(f["proctor"]["login"], 1001, d1)
    mark = test.command(proctor, f'/divisions/{d1}/class-days/{mark_day}/attendance',
                        {"account_id": f["student"]["account_id"], "attendance_type_id": active_type}, divisions=[d1])
    test.readonly(admin, "proctor receipt GET", lambda: test.receipt(proctor, mark))
    unassigned = False
    unassign_path = f'/divisions/{d1}/groups/{f["group"]}/members/{f["proctor"]["account_id"]}'
    assign_path = f'/divisions/{d1}/groups/{f["group"]}/members'
    try:
        unassigned = True
        test.core(admin, unassign_path, method="DELETE", effect={"proctor_account_id": None})
        test.readonly(admin, "unassigned proctor receipt", lambda: test.receipt(proctor, mark, 403, "forbidden"))
        test.readonly(admin, "unassigned proctor exact replay", lambda:
                      proctor.native(mark["record"]["path"], "POST", raw=mark["raw"], key=mark["record"]["command_id"], want=403, error="forbidden"))
    finally:
        if unassigned:
            try:
                current = next(row for row in test.core(admin, f"/divisions/{d1}/groups") if row["id"] == f["group"])
                current_proctor = current.get("proctor_account_id")
                test.reconcile_core(unassign_path, {"proctor_account_id": current_proctor})
                if current_proctor not in (None, f["proctor"]["account_id"]):
                    test.cleanup_errors.append({"stage": "restore-own-proctor-assignment", "type": "ConcurrentChange"})
                else:
                    if current_proctor is None:
                        test.core(admin, assign_path, {"account_id": f["proctor"]["account_id"]}, "POST",
                                  effect={"proctor_account_id": f["proctor"]["account_id"]})
                    restored = next(row for row in test.core(admin, f"/divisions/{d1}/groups") if row["id"] == f["group"])
                    test.expect(restored.get("proctor_account_id") == f["proctor"]["account_id"],
                                "own proctor assignment returned to its exact original owner")
                    test.reconcile_core(unassign_path, {"proctor_account_id": current_proctor},
                                        {"proctor_account_id": restored["proctor_account_id"]})
                    test.reconcile_core(assign_path, {"proctor_account_id": restored["proctor_account_id"]})
            except Exception as error:
                test.cleanup_errors.append({"stage": "restore-own-proctor-assignment", "type": type(error).__name__})
    test.readonly(admin, "restored proctor receipt", lambda: test.receipt(proctor, mark))
    test.replay(admin, mark)
    test.cases.append({"case": "own proctor unassign/restore with same session and exact receipt", "status": "actual_http"})


def verify(test):
    f = test.f
    test.expect(f["run"] == "0a242b5545" and f["d1"] == 10150 and f["student"]["account_id"] == 391,
                "acceptance uses the approved own scope fixture run")
    for name in ["student", "other_student", "math_student", "staff", "proctor"]:
        test.expect(f[name]["login"].startswith("dev_att_scope_" + f["run"] + "_"), "account belongs to own synthetic fixture run")
    for service in ORIGINS:
        test.http(service, "/health/ready")
    test.stage = "fresh-own-auth-sessions"
    admin = test.login("dev_admin", 1001, f["d1"])
    math = test.login("dev_admin_math", 2001, f["math_division"])
    student = test.login(f["student"]["login"], 1001, f["d1"])
    other = test.login(f["other_student"]["login"], 1001, f["d1"])
    staff = test.login(f["staff"]["login"], 1001, f["d1"])
    test.stage = "contract-negative-reads"
    missing = str(uuid.uuid4())
    def negatives():
        for client in [admin, student, math]:
            client.native("/command-receipts/" + missing, want=404, error="not_found")
        admin.native("/command-receipts/" + uuid.uuid4().hex, want=400, error="invalid_command_id")
        admin.native("/command-receipts/AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", want=400, error="invalid_command_id")
        for query in ["?actor_account_id=391", "?organization_id=2001", "?division_id=10151", "?"]:
            admin.native("/command-receipts/" + missing + query, want=400, error="invalid_receipt_request")
        admin.native("/command-receipts/" + missing, raw=b"{}", want=400, error="invalid_receipt_request")
        admin.native("/command-receipts/" + missing, "HEAD", want=405, json_response=False)
        admin.native("/command-receipts", want=404, json_response=False)
        test.http("attendance", "/v1/attendance/command-receipts/" + missing,
                  extra_headers={"X-Service-Key": "fixture-not-a-credential"}, want=401, error="invalid_token")
    test.readonly(admin, "negative receipt reads", negatives)
    test.cases.append({"case": "missing and invalid receipt contract reads", "status": "actual_http",
                       "absence_semantics": "404 remains unknown; no claim of rollback or expiration"})

    test.stage = "choose-unused-own-class-dates"
    used = {day["date"] for day in admin.read(f'/divisions/{f["d1"]}/class-days?date_from=2041-01-01&date_to=2041-12-31')["value"]}
    candidates = [(dt.date(2041, 1, 1) + dt.timedelta(days=i)).isoformat() for i in range(365)]
    empty_date, mark_date = [date for date in candidates if date not in used][:2]
    tag, d1 = test.run, f["d1"]
    test.stage = "fresh-type-and-deleted-day"
    typ = test.command(admin, f"/divisions/{d1}/attendance-types", {"code": "u04_" + tag,
                      "name": "DEV U04 receipt type " + tag, "color": "#336699", "mark": "R",
                      "is_active": True, "counts_as_present": True}, divisions=[d1])
    type_resource = {"kind": "attendance_type", "division_id": d1, "id": typ["result"]["value"]["id"], "state": "created"}
    test.resources.append(type_resource)
    empty = test.command(admin, f"/divisions/{d1}/class-days", {"date": empty_date,
                        "comment": "DEV U04 empty " + tag, "lesson_ids": []}, divisions=[d1])
    deleted = test.command(admin, f'/divisions/{d1}/class-days/{empty["result"]["value"]["id"]}', {}, "DELETE", [d1])
    test.resources.append({"kind": "deleted_day", "division_id": d1, "id": empty["result"]["value"]["id"], "date": empty_date})
    test.readonly(admin, "deleted day receipts", lambda: [test.receipt(admin, empty), test.receipt(admin, deleted)])
    test.replay(admin, deleted)
    admin.native(deleted["record"]["path"], want=404, error="not_found")
    test.cases.append({"case": "receipt for a genuinely deleted own day while division remains live", "status": "actual_http"})

    test.stage = "manual-mark-and-archived-type"
    day = test.command(admin, f"/divisions/{d1}/class-days", {"date": mark_date,
                      "comment": "DEV U04 mark " + tag, "lesson_ids": []}, divisions=[d1])
    mark_day, type_id = day["result"]["value"]["id"], typ["result"]["value"]["id"]
    test.resources.append({"kind": "class_day", "division_id": d1, "id": mark_day, "date": mark_date})
    mark = test.command(admin, f"/divisions/{d1}/class-days/{mark_day}/attendance",
                       {"account_id": f["student"]["account_id"], "attendance_type_id": type_id}, divisions=[d1])
    test.resources.append({"kind": "entry", "division_id": d1, "id": mark["result"]["value"]["id"],
                           "class_day_id": mark_day, "account_id": f["student"]["account_id"]})
    archived = test.command(admin, f"/divisions/{d1}/attendance-types/{type_id}", {}, "DELETE", [d1])
    type_resource["state"] = "archived"
    test.readonly(admin, "archived type and mark receipts", lambda: [test.receipt(admin, typ), test.receipt(admin, mark), test.receipt(admin, archived)])
    test.replay(admin, typ)
    test.replay(admin, mark)
    changed = json.loads(typ["raw"])
    changed["data"]["name"] += " changed"
    changed_raw = json.dumps(changed, ensure_ascii=False, separators=(",", ":")).encode()
    test.readonly(admin, "changed same-key command", lambda: admin.native(typ["record"]["path"], "POST",
                  raw=changed_raw, key=typ["record"]["command_id"], want=409, error="idempotency_conflict"))
    test.cases.append({"case": "archived own type preserves committed receipt and public historical replay", "status": "actual_http"})

    test.stage = "student-own-receipt-and-foreign-fences"
    zap = test.command(student, f"/divisions/{d1}/zaps", {"text": "DEV U04 private reason " + tag,
                       "dates": [mark_date], "images": []}, divisions=[d1])
    test.resources.append({"kind": "pending_zap", "division_id": d1, "id": zap["result"]["value"]["id"],
                           "account_id": f["student"]["account_id"]})
    test.readonly(admin, "student receipt", lambda: test.receipt(student, zap))
    test.replay(admin, zap)
    def foreign():
        for client in [other, math, staff, admin]:
            test.receipt(client, zap, 404, "not_found")
        for client in [student, math, staff]:
            test.receipt(client, mark, 404, "not_found")
    test.readonly(admin, "foreign actor/org receipts", foreign)
    student.context(f["d2"])
    test.readonly(admin, "other available active cabinet", lambda: test.receipt(student, zap))
    student.context(d1)
    test.cases.append({"case": "student own metadata-only receipt; actor/org privacy and independent active cabinet", "status": "actual_http"})

    test.stage = "organization-room-and-new-bulk"
    room = test.command(admin, "/resources/rooms", {"code": "u04_" + tag,
                        "name": "DEV U04 receipt room " + tag}, divisions=[], scope="organization")
    test.resources.append({"kind": "room", "organization_id": admin.org, "id": room["result"]["value"]["id"]})
    bulk = new_bulk(test, admin, mark_date, tag + " admin")
    test.readonly(admin, "room and complete new bulk scope receipts", lambda: [test.receipt(admin, room), test.receipt(admin, bulk)])
    test.replay(admin, room)
    test.replay(admin, bulk)
    test.readonly(admin, "foreign organization/bulk receipt privacy", lambda:
                  [test.receipt(client, command, 404, "not_found") for client in [student, staff, math] for command in [room, bulk]])
    test.cases.append({"case": "organization room and new bulk noop/deleted original divisions", "status": "actual_http"})
    test.cases.append({"case": "legacy canonical division receipt and old bulk409", "status": "deferred_go_only",
                       "reason": "known old canonical receipt belongs to another owner's actor; no own old bulk fixture"})
    if test.allow_core:
        active = [typ for typ in admin.read(f"/divisions/{d1}/attendance-types")["value"] if typ["code"] == "scoped_excuse" and typ["is_active"]]
        test.expect(len(active) == 1, "existing own active non-scan type is available for optional proctor proof")
        optional_live_access(test, admin, mark_day, mark_date, active[0]["id"])
    else:
        test.cases.append({"case": "live staff/proctor revoke and restore", "status": "not_run_without_later_Core_permission"})
    test.expect(not test.cleanup_errors, "own temporary grants were restored without overwriting concurrent changes")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--plan", action="store_true", help="Print mutation/proof plan without private reads or HTTP")
    parser.add_argument("--run", action="store_true", help="Run only after owner READY and a reviewed execution window")
    parser.add_argument("--allow-core-writes", action="store_true", help="Requires separate later approval for own live-grant proof")
    args = parser.parse_args()
    if args.plan:
        print(json.dumps(PLAN, indent=2))
        return 0
    if not args.run:
        parser.error("choose --plan or --run after the owner window is agreed")
    test = None
    try:
        if REPORT.exists():
            previous = json.loads(REPORT.read_text())
            if any(command.get("state") == "unknown" for command in previous.get("commands", []) + previous.get("core_commands", [])):
                raise RuntimeError("unresolved preceding acceptance command")
        test = Acceptance(args.allow_core_writes)
        verify(test)
    except Exception as error:
        if test:
            test.failure = {"stage": test.stage, "type": type(error).__name__}
        else:
            print("Receipt acceptance failed before HTTP; private error details suppressed.", file=sys.stderr)
            return 1
    finally:
        if test:
            for client in reversed(test.clients):
                try:
                    client.logout()
                except Exception as error:
                    test.cleanup_errors.append({"stage": "logout-own-session", "type": type(error).__name__})
                    test.failure = test.failure or {"stage": "logout-own-session", "type": type(error).__name__}
            try:
                test.checkpoint("failed" if test.failure else "passed")
            except Exception:
                test.failure = test.failure or {"stage": "write-token-free-report", "type": "Error"}
    if test.failure:
        print("Receipt acceptance failed at " + test.failure["stage"] + " (" + test.failure["type"] + "); raw errors suppressed.", file=sys.stderr)
        return 1
    actual = sum(case["status"] == "actual_http" for case in test.cases)
    deferred = len(test.cases) - actual
    print(f"Passed {actual} actual HTTP cases, {len(test.checks)} assertions; {deferred} cases explicitly deferred; token-free report saved.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
