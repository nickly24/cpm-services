"""Joint Attendance/Uploads acceptance using existing private DEV scope fixtures.

Run only after both owners agree the Uploads runtime is ready:
  dev/.venv/bin/python services/attendance/scripts/uploads_attachment_http_test.py --run

No fixture creation, Core writes, attendance writes, SQL, queue operations or
process restarts. The only domain writes are new actor-private Uploads registry
references/receipts for already existing synthetic Attendance images. Auth
sessions are created by this run and closed afterward. An explicit --run is
required; importing this module or requesting --help performs no HTTP requests.
"""

import argparse
import base64
import json
import secrets
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
FIXTURES = ROOT / "dev/local/attendance-scope-fixtures.json"
REPORT = ROOT / "dev/local/attendance-uploads-attachment-http-report.json"
PNG = base64.b64decode(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lYkAAAAASUVORK5CYII="
)
ORIGINS = {
    "auth": "http://127.0.0.1:8002",
    "attendance": "http://127.0.0.1:8013",
    "uploads": "http://127.0.0.1:8014",
}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class Acceptance:
    def __init__(self):
        self.checks = []
        self.resources = []
        self.commands = []
        self.clients = []
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect)
        self.password = json.loads((ROOT / "dev/local/accounts.json").read_text())["password"]

    def expect(self, condition, label):
        if not condition:
            raise AssertionError(label)
        self.checks.append(label)

    def http(self, service, path, method="GET", data=None, token=None, key=None,
             want=200, binary=False, raw=None):
        headers = {"Content-Type": "application/json"}
        if token:
            headers["Authorization"] = "Bearer " + token
        if key:
            headers["Idempotency-Key"] = key
        encoded = raw if raw is not None else (
            json.dumps(data, ensure_ascii=False, separators=(",", ":")).encode()
            if data is not None else None
        )
        request = urllib.request.Request(
            ORIGINS[service] + path, data=encoded, headers=headers, method=method
        )
        try:
            response = self.opener.open(request, timeout=30)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            status = response.code
            body = response.read((3 << 20) + 1)
            response_headers = response.headers
        # Never include request/response payloads, credentials or bearer in errors.
        self.expect(status == want, f"{service} {method} {path} -> {want}, got {status}")
        self.expect(len(body) <= 3 << 20, service + " response is bounded")
        if binary and status == 200:
            return body, response_headers
        try:
            result = json.loads(body)
        except (UnicodeDecodeError, json.JSONDecodeError):
            raise AssertionError(service + " returned an invalid JSON envelope") from None
        self.expect(isinstance(result, dict), service + " returned an object envelope")
        if service != "auth":
            self.expect(result.get("status") is (status < 400), service + " status envelope matches HTTP")
        return result

    def login(self, fixture, org, division):
        client = Client(self, fixture, org, division)
        self.clients.append(client)
        return client

    def register(self, client, zap_id, image=1, want=201):
        key = "attendance_attachment_" + secrets.token_hex(16)
        payload = {"type": "attendance.zap-images", "target_id": str(zap_id),
                   "owner_reference": str(image)}
        raw = json.dumps(payload, separators=(",", ":")).encode()
        # Preserve the new command before dispatch, including if the response is
        # lost. This is a local acceptance report, not an owner or fixture write.
        self.commands.append({"command_id": key, "organization_id": client.org,
                              "division_id": client.division, "account_id": client.account_id,
                              "payload": payload, "expected_http_status": want})
        checkpoint = REPORT.with_suffix(".tmp")
        checkpoint.write_text(json.dumps({"status": "running", "checks": self.checks,
                                         "registries": self.resources, "commands": self.commands},
                                        ensure_ascii=False, indent=2) + "\n")
        checkpoint.replace(REPORT)
        response = client.uploads("/files/register", "POST", raw=raw, key=key, want=want)
        if want != 201:
            return response
        file = response["data"]
        self.expect(isinstance(file.get("id"), str) and len(file["id"]) == 32,
                    "registration returns a durable registry ID")
        for field, value in {"organization_id": client.org, "division_id": client.division,
                             "actor_account_id": client.account_id, "type": "attendance.zap-images",
                             "owner": "attendance", "target_id": str(zap_id),
                             "owner_reference": str(image)}.items():
            self.expect(file.get(field) == value, "registry preserves " + field)
        self.resources.append({"file_id": file["id"], "command_id": key,
                               "organization_id": client.org, "division_id": client.division,
                               "account_id": client.account_id, "zap_id": zap_id})
        retry = client.uploads("/files/register", "POST", raw=raw, key=key)["data"]
        self.expect(retry["id"] == file["id"], "byte-identical command replay returns the same file")
        receipt = client.uploads("/receipts/" + key)["data"]
        self.expect(receipt.get("resource_id") == file["id"] and
                    receipt.get("action") == "file.register" and receipt.get("state") == "completed",
                    "student can reconcile its own completed registry receipt")
        self.expect(receipt.get("scope") == {"organization_id": client.org,
                    "division_id": client.division, "actor_account_id": client.account_id},
                    "receipt retains exact actor/organization/division scope")
        metadata = client.uploads("/files/" + file["id"])["data"]
        self.expect(metadata.get("file", {}).get("id") == file["id"] and
                    metadata.get("lifecycle_owner") == "attendance",
                    "registry metadata identifies the existing Attendance owner")
        live = metadata.get("owner_live", {})
        self.expect(live.get("image_count", 0) >= image and
                    live.get("zap", {}).get("id") == zap_id and
                    live["zap"].get("account_id") == client.account_id,
                    "metadata contains current protected owner detail, not an empty placeholder")
        self.expect("images" not in live["zap"], "metadata does not expose image base64")
        content, headers = client.uploads("/files/" + file["id"] + "/download", binary=True)
        self.expect(content == PNG and headers.get_content_type() == "image/png",
                    "registry download returns the exact existing PNG bytes")
        self.expect("no-store" in headers.get("Cache-Control", ""),
                    "protected registry download disables caching")
        return file["id"], key


class Client:
    def __init__(self, test, fixture, org, division):
        self.test, self.org, self.division = test, org, division
        self.account_id = fixture["account_id"]
        self.token = test.http("auth", "/v1/auth/login", "POST", {
            "login": fixture["login"], "password": test.password,
            "organization_id": org, "division_id": division,
        })["access_token"]
        self.closed = False

    def context(self, division):
        self.token = self.test.http("auth", "/v1/auth/context", "POST", {
            "organization_id": self.org, "division_id": division,
        }, token=self.token)["access_token"]
        self.division = division

    def uploads(self, suffix, method="GET", division=None, **kwargs):
        target = self.division if division is None else division
        return self.test.http("uploads", f"/v1/uploads/divisions/{target}" + suffix,
                              method, token=self.token, **kwargs)

    def owner(self, zap_id, division=None, image=None, want=200):
        target = self.division if division is None else division
        path = f"/v1/attendance/divisions/{target}/zaps/{zap_id}"
        if image is not None:
            path += "/images/" + str(image)
        return self.test.http("attendance", path, token=self.token, want=want,
                              binary=image is not None)

    def logout(self):
        if not self.closed:
            self.test.http("auth", "/v1/auth/logout", "POST", {}, token=self.token)
            self.closed = True


def verify(test, fixtures, args):
    required = {"run", "d1", "d2", "math_division", "student", "other_student",
                "math_student", "attachment", "zap_id", "second_division_zap_id", "math_zap_id"}
    test.expect(required <= fixtures.keys(), "completed existing Attendance scope fixtures are available")
    for name in ("student", "other_student", "math_student", "staff"):
        if name in fixtures:
            test.expect(fixtures[name]["login"].startswith("dev_att_scope_" + fixtures["run"] + "_"),
                        "account belongs to the private synthetic fixture run")
    d1, d2, md = fixtures["d1"], fixtures["d2"], fixtures["math_division"]
    zap, second_zap, math_zap = (fixtures[name] for name in
                                ("zap_id", "second_division_zap_id", "math_zap_id"))
    test.expect(fixtures["attachment"] == {"organization_id": 1001, "division_id": d1,
                "account_id": fixtures["student"]["account_id"], "zap_id": zap, "image": 1},
                "attachment fixture has the exact expected synthetic owner scope")
    for service in ORIGINS:
        test.http(service, "/health/ready")
    student = test.login(fixtures["student"], 1001, d1)
    other = test.login(fixtures["other_student"], 1001, d1)
    math = test.login(fixtures["math_student"], 2001, md)
    for client, owner_zap in ((student, zap), (math, math_zap)):
        detail = client.owner(owner_zap)["data"]["value"]
        test.expect(detail["zap"]["account_id"] == client.account_id and detail["image_count"] == 1,
                    "both organizations have a real existing owned zap and image")
        content, headers = client.owner(owner_zap, image=1)
        test.expect(content == PNG and headers.get_content_type() == "image/png",
                    "existing owner image matches the known synthetic PNG")
    first_file, first_key = test.register(student, zap)
    math_file, _ = test.register(math, math_zap)
    test.register(student, zap, image=2, want=404)
    test.register(other, zap, want=404)
    for suffix in ("/files/" + first_file, "/files/" + first_file + "/download",
                   "/receipts/" + first_key):
        other.uploads(suffix, want=404)
        math.uploads(suffix, want=404)
        student.uploads(suffix, division=d2, want=403)
        student.uploads(suffix, division=md, want=403)
    student.uploads("/files/" + math_file, want=404)
    denied = student.uploads("/files/register", "POST", division=d2,
                             data={"type": "attendance.zap-images", "target_id": str(second_zap),
                                   "owner_reference": "1"},
                             key="attendance_context_" + secrets.token_hex(16), want=403)
    test.expect(denied.get("error") == "scope_access_denied",
                "Uploads requires the URL division to equal the active auth cabinet")
    # Attendance permits an attached division independently of the active cabinet.
    student.owner(second_zap, division=d2)
    student.context(d2)
    for suffix in ("/files/" + first_file, "/files/" + first_file + "/download",
                   "/receipts/" + first_key):
        student.uploads(suffix, want=404)
    test.register(student, zap, want=404)
    second_file, _ = test.register(student, second_zap)
    student.context(d1)
    student.uploads("/files/" + second_file, want=404)
    student.uploads("/files/" + first_file)
    if args.check_staff_read:
        staff = test.login(fixtures["staff"], 1001, d1)
        staff.owner(zap, image=1)
        test.register(staff, zap, want=403)
        staff.uploads("/files/" + first_file, want=404)
        test.expect(True, "view-only staff reads the owner but cannot register without edit or share an actor-private link")
        staff.context(d2)
        staff.owner(second_zap, image=1, want=404)
        test.register(staff, second_zap, want=403)
        test.expect(True, "existing revoked D2 staff rights deny owner image and registry creation")
    if args.check_session_revocation:
        student.logout()
        student.uploads("/files/" + first_file, want=401)
        student.uploads("/files/" + first_file + "/download", want=401)
        test.expect(True, "logout of this run's own session immediately revokes registry reads/downloads")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run", action="store_true", help="Execute only after owner runtime coordination")
    parser.add_argument("--check-staff-read", action="store_true", help="Read-only existing synthetic staff permissions")
    parser.add_argument("--check-session-revocation", action="store_true", help="Revoke only this run's own Auth session")
    args = parser.parse_args()
    if not args.run:
        parser.error("--run is required after coordinating the Uploads runtime")
    test = Acceptance()
    fixtures = json.loads(FIXTURES.read_text())
    status, failure = "passed", None
    try:
        verify(test, fixtures, args)
    except Exception as error:
        status, failure = "failed", str(error)
    finally:
        for client in test.clients:
            try:
                client.logout()
            except Exception:
                status = "failed"
                failure = failure or "could not close one of this run's private Auth sessions"
        report = {"status": status, "verified_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                  "checks": test.checks, "registries": test.resources, "commands": test.commands,
                  "fixtures": {"run": fixtures.get("run"), "organizations": [1001, 2001],
                               "divisions": [fixtures.get("d1"), fixtures.get("d2"), fixtures.get("math_division")]},
                  "dependencies": ["actual Auth", "actual Core scope read", "actual Attendance",
                                   "actual Uploads registry/MySQL"],
                  "staff_read_checked": args.check_staff_read,
                  "own_session_revocation_checked": args.check_session_revocation,
                  "deferred": ["Core membership/role revocation: separate agreed synthetic-owner test"],
                  "domain_writes": "only new Uploads registry references/receipts/events for existing synthetic images"}
        if failure:
            report["failure"] = failure
        REPORT.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    print(f"{status}: {len(test.checks)} joint Attendance/Uploads checks; report saved.")
    if failure:
        print(failure, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
