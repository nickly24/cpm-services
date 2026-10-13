"""Prove local exams outbox delivery via a temporary exact Rabbit binding.

Uses Rabbit's management basic.get on an owned, short-lived queue. Existing
queues/consumers are untouched. The only fixture write is public BFF exam create;
there are no direct SQL writes or broker/container pauses. Secrets stay private.
"""
from datetime import datetime, timezone
import base64
import http.cookiejar
import json
from pathlib import Path
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / "dev/scripts"))
from export_structure import read_env


def main():
    completed = json.loads((ROOT / "dev/local/bff-exams-http-report.json").read_text())
    if completed["status"] != "passed":
        raise RuntimeError("successful BFF fixtures are required before broker verification")
    demo = json.loads((ROOT / "dev/local/bff-exams-demo.json").read_text())
    password = json.loads((ROOT / "dev/local/accounts.json").read_text())["password"]
    env = read_env(ROOT / "dev/local/exams.env")
    broker = urllib.parse.urlsplit(env["RABBITMQ_URL"])
    if broker.scheme != "amqp" or broker.hostname not in {"127.0.0.1", "localhost"} or broker.port not in {None, 5672}:
        raise RuntimeError("only the existing loopback DevRabbit is allowed")
    vhost = urllib.parse.unquote(broker.path[1:])
    if vhost != "cpm_dev":
        raise RuntimeError("only the local cpm_dev Rabbit vhost is allowed")
    authorization = base64.b64encode((urllib.parse.unquote(broker.username) + ":" + urllib.parse.unquote(broker.password)).encode()).decode()
    queue = "dev_bff_exams_proof_" + uuid.uuid4().hex
    quote = lambda value: urllib.parse.quote(value, safe="")
    queue_path = "/api/queues/" + quote(vhost) + "/" + quote(queue)
    jar = http.cookiejar.CookieJar()
    browser = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
    revision = ""
    checks = []

    def expect(ok, name):
        if not ok:
            raise AssertionError(name)
        checks.append(name)

    def request(origin, path, method="GET", body=None, headers=None, expected=(200,)):
        raw = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(origin + path, data=raw, method=method, headers={"Content-Type": "application/json", **(headers or {})})
        transport = browser if origin.endswith(":8001") else urllib.request
        try:
            with (transport.open(req, timeout=20) if transport is browser else transport.urlopen(req, timeout=20)) as response:
                status, raw = response.status, response.read()
        except urllib.error.HTTPError as response:
            status, raw = response.code, response.read()
        expect(status in expected, method + " " + path.split("?")[0] + " returned " + str(status))
        return json.loads(raw) if raw else {}

    def rabbit(path, method="GET", body=None, expected=(200,)):
        return request("http://127.0.0.1:15672", path, method, body, {"Authorization": "Basic " + authorization}, expected)

    def bff(path, method="GET", body=None):
        nonlocal revision
        headers = {"Origin": "http://127.0.0.1:3001", "X-CPM-Request": "1", "X-CPM-Context": revision}
        if method != "GET":
            headers["Idempotency-Key"] = str(uuid.uuid4())
        data = request("http://127.0.0.1:8001", "/bff/" + path, method, body, headers, (200, 201))
        expect(not any(key in data for key in ["token", "access_token", "refresh_token"]), "no browser tokens")
        revision = data.get("context_revision", revision)
        return data.get("data", data)

    created_queue = False
    fixture = None
    delivery = None
    try:
        # Rabbit 4.3 rejects non-durable non-exclusive queues. This owned queue
        # expires automatically as a fallback and is deleted in finally.
        rabbit(queue_path, "PUT", {"durable": True, "auto_delete": False, "arguments": {"x-queue-type": "classic", "x-expires": 120000, "x-message-ttl": 60000, "x-max-length": 1000}}, (201, 204))
        created_queue = True
        rabbit("/api/bindings/" + quote(vhost) + "/e/cpm.events/q/" + quote(queue), "POST", {"routing_key": "exams.source.changed", "arguments": {}}, (201,))
        bff("auth/login", "POST", {"login": "dev_admin", "password": password, "organization_id": 1001, "division_id": demo["division_a"]})
        fixture = bff("exams", "POST", {"exam_type": "outside_lms", "direction_id": demo["direction_id"], "date": datetime.now(timezone.utc).date().isoformat(), "name": "DEV BFF Exams " + demo["run"] + " · Rabbit outbox"})
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline and delivery is None:
            messages = rabbit(queue_path + "/get", "POST", {"count": 100, "ackmode": "ack_requeue_false", "encoding": "auto", "truncate": 65536})
            for item in messages:
                envelope = json.loads(item["payload"])
                payload = envelope.get("payload", {})
                if payload.get("exam_id") == fixture["id"]:
                    delivery = {"message_id": envelope["message_id"], "message_type": envelope["message_type"], "schema_version": envelope["schema_version"], "producer": envelope["producer"], "payload": payload, "exchange": item["exchange"], "routing_key": item["routing_key"], "delivery_mode": item["properties"].get("delivery_mode"), "property_message_id": item["properties"].get("message_id")}
                    break
            if delivery is None:
                time.sleep(.4)
        expect(delivery is not None, "public BFF mutation reaches the real Rabbit outbox route")
        expect(delivery["exchange"] == "cpm.events" and delivery["routing_key"] == "exams.source.changed", "exact exchange and routing key")
        expect(delivery["message_type"] == "exams.source.changed" and delivery["schema_version"] == 1 and delivery["producer"] == "exams-service", "owner event envelope contract")
        expect(delivery["delivery_mode"] == 2 and delivery["property_message_id"] == delivery["message_id"] and str(uuid.UUID(delivery["message_id"])) == delivery["message_id"], "persistent message with a stable UUID for consumer deduplication")
        payload = delivery["payload"]
        expect(payload["organization_id"] == 1001 and payload["division_id"] == demo["division_a"] and payload["exam_revision"] == fixture["revision"] and payload["source_revision"] > 0 and payload["reason"] == "created", "event identity, source revision and creation reason match the public receipt")
        report = {"status": "passed", "checks": len(checks), "check_names": checks, "fixture_exam_id": fixture["id"], "event": delivery, "queue_removed": False, "consumer": "Rabbit management basic.get on an owned temporary queue", "ratings_job_run": False}
    finally:
        if created_queue:
            rabbit(queue_path, "DELETE", expected=(204,))
        if revision:
            bff("auth/logout", "POST", {})
    report["queue_removed"] = True
    report["checks"] = len(checks)
    target = ROOT / "dev/local/bff-exams-rabbit-report.json"
    target.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    target.chmod(0o600)
    print(json.dumps({"status": report["status"], "checks": report["checks"], "fixture_exam_id": fixture["id"], "queue_removed": True}))


if __name__ == "__main__":
    main()
