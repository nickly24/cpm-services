"""Cards acceptance through public BFF; only fresh DEV business fixtures are written.

No SQL, service keys, dependency pauses, imports or existing fixture writes.
Credentials are read from ignored local files and never enter the output manifest.
"""
from __future__ import annotations

import copy
import hashlib
import json
from datetime import datetime, timedelta, timezone
from pathlib import Path
import urllib.error
import urllib.request
import uuid

from exams_http_smoke import BASE, ORIGIN, Session

ROOT = Path(__file__).resolve().parents[3]


class CardsSession(Session):
    def request(self, path, method="GET", body=None, expected=200, revision=None, key=None):
        assert path.startswith("/bff/")
        assert method == "GET" or path.startswith(("/bff/cards/", "/bff/tests/", "/bff/auth/"))
        headers = {"X-CPM-Request": "1", "X-CPM-Context": self.revision if revision is None else revision,
                   "Origin": ORIGIN, "Content-Type": "application/json"}
        if key:
            headers["Idempotency-Key"] = key
        request = urllib.request.Request(BASE + path, method=method, headers=headers,
                                         data=json.dumps(body).encode() if body is not None else None)
        try:
            with self.http.open(request, timeout=35) as response:
                status, payload = response.status, json.load(response)
        except urllib.error.HTTPError as response:
            status, payload = response.code, json.load(response)
        allowed = {expected} if isinstance(expected, int) else set(expected)
        assert status in allowed, f"{method} {path.split('?')[0]} expected {sorted(allowed)}, got {status} {payload.get('error', '')}"
        assert not any(k in payload for k in ("token", "access_token", "refresh_token")), "BFF exposed auth tokens"
        if "context_revision" in payload:
            self.revision = payload["context_revision"]
        return payload.get("data", payload)


def questions():
    return [
        {"question_id": 1, "type": "single", "text": "Первый вариант правильный", "points": 2,
         "answers": [{"id": "a", "text": "Первый", "is_correct": True}, {"id": "b", "text": "Второй", "is_correct": False}], "correct_answers": []},
        {"question_id": 2, "type": "multiple", "text": "Выберите X и Y", "points": 3,
         "answers": [{"id": "x", "text": "X", "is_correct": True}, {"id": "y", "text": "Y", "is_correct": True}, {"id": "z", "text": "Z", "is_correct": False}], "correct_answers": []},
        {"question_id": 3, "type": "text", "text": "Столица России", "points": 5, "answers": [], "correct_answers": ["Москва", "Moscow"]},
    ]


def main():
    fixtures = json.loads((ROOT / "dev/local/cards-fixtures.json").read_text())
    common = json.loads((ROOT / "dev/local/accounts.json").read_text())["password"]
    run = uuid.uuid4().hex[:8]
    checks, sessions = [], []
    status, failure = "failed", None
    manifest = {"run": run, "organization_id": 1001, "division_a": 1001, "division_b": 1002,
                "direction_id": fixtures["direction"], "student_login": "dev_cards_student",
                "viewer_login": "dev_cards_viewer", "editor_login": "dev_cards_editor", "admin_login": "dev_admin",
                "credential_file": "dev/local/cards-fixtures.json"}

    def check(value, label):
        assert value, label
        checks.append(label)

    def login(name, organization=1001, division=1001):
        client = CardsSession(check)
        sessions.append(client)
        client.login(name, fixtures["password"] if name.startswith("dev_cards_") else common, organization, division)
        return client

    def switch(client, division, organization=1001):
        return client.request("/bff/auth/context", "POST", {"organization_id": organization, "division_id": division})

    def write(client, path, method, body, expected=200, key=None):
        return client.request(path, method, body, expected, key=key or str(uuid.uuid4()))

    def test_settings(client, test, visible=True, future=False):
        meta = client.request("/bff/tests/" + test)
        now = datetime.now(timezone.utc)
        return write(client, f"/bff/tests/{test}/settings", "PUT", {
            "expected_revision": meta["revision"], "published": True,
            "start_at": (now - timedelta(days=2)).isoformat(),
            "end_at": (now + timedelta(days=1) if future else now - timedelta(days=1)).isoformat(),
            "time_limit_minutes": 30, "passing_score": None, "answers_visible": visible})

    try:
        admin, student, editor, viewer = [login(n) for n in ("dev_admin", "dev_cards_student", "dev_cards_editor", "dev_cards_viewer")]
        foreign = login("dev_cards_student", 2001, 2001)
        other_admin = login("dev_admin_math", 2001, 2001)
        direction = fixtures["direction"]
        catalog = viewer.request("/bff/cards/admin/catalog")
        manifest["direction_name"] = next(d["name"] for d in catalog["directions"] if d["id"] == direction)
        body = {"name": "DEV BFF Cards " + run, "direction_id": direction,
                "questions": [{"question": f"Карточка {i + 1}", "answer": f"Ответ {i + 1}"} for i in range(25)]}
        key = str(uuid.uuid4())
        theme = write(editor, "/bff/cards/themes", "POST", body, key=key)
        check(theme == write(editor, "/bff/cards/themes", "POST", body, key=key), "lost create response repeats as the same theme")
        write(editor, "/bff/cards/themes", "POST", {**body, "name": body["name"] + " conflict"}, 409, key)
        check(True, "same create key with another payload conflicts")
        receipt = hashlib.sha256(json.dumps(["cards-command-v1", 1001, fixtures["editor"], key], separators=(",", ":")).encode()).hexdigest()[:32]
        editor.request("/bff/cards/sessions/" + receipt, expected=404)
        check(True, "idempotency receipts remain private")
        for client in (student, viewer):
            write(client, "/bff/cards/themes", "POST", {"name": "Forbidden " + run, "direction_id": direction}, 403)
        write(admin, "/bff/cards/themes", "POST", {"name": "Foreign " + run, "direction_id": fixtures["other_direction"]}, 400)
        write(admin, "/bff/cards/themes", "POST", {"name": "Owner " + run, "direction_id": direction, "organization_id": 2001}, 400)
        check(True, "view-only users cannot write and browser cannot supply another organization")
        tid = theme["id"]
        manifest.update(theme_id=tid, theme_name=theme["name"])
        section = f"/bff/cards/sections/manual/{tid}"
        foreign.request(section, expected=404)
        other_admin.request(f"/bff/cards/themes/{tid}/cards", expected=404)
        check(True, "manual themes are isolated by organization")
        view = student.request(section)
        check(len(view["cards"]) == 25 and view["settings"]["batch_size"] == 10 and view["settings"]["study_mode"] == "unlearned", "fresh manual section has default learning settings")
        check([b["size"] for b in view["batches"]] == [10, 10, 5], "stable manual batches contain 10, 10, 5 cards")
        first, second = view["cards"][:2]
        fp = hashlib.sha256(json.dumps({"q": first["question"].strip(), "a": first["answer"].strip()}, ensure_ascii=False, sort_keys=True).encode()).hexdigest()
        check(first["content_fingerprint"] == fp, "content fingerprints preserve the existing learning contract")
        mark = {"card_ref": first["card_ref"], "content_fingerprint": fp}
        write(student, section + "/learned", "POST", {**mark, "account_id": fixtures["student"]}, 400)
        write(admin, section + "/learned", "POST", mark, 403)
        write(student, section + "/learned", "POST", {**mark, "content_fingerprint": "0" * 64}, 409)
        write(student, section + "/learned", "POST", mark)
        write(student, section + "/learned", "POST", mark)
        switch(student, 1002)
        check(student.request(section)["cards"][0]["status"] == "learned", "manual learning progress survives cabinet switch within the organization")
        switch(student, 1001)
        edit = {"expected_revision": first["revision"], "question": first["question"], "answer": "Изменённый\n\nответ"}
        editkey = str(uuid.uuid4())
        changed = write(editor, f"/bff/cards/themes/{tid}/cards/{first['card_id']}", "PUT", edit, key=editkey)
        check(changed == write(editor, f"/bff/cards/themes/{tid}/cards/{first['card_id']}", "PUT", edit, key=editkey), "lost card update response replays despite the old expected revision")
        write(editor, f"/bff/cards/themes/{tid}/cards/{first['card_id']}", "PUT", edit, 409)
        stale = student.request(section)
        check(stale["cards"][0]["card_ref"] == first["card_ref"] and stale["stats"]["answer_changed"] == 1, "changed answers keep card identity and invalidate old progress")
        write(student, section + "/learned", "POST", mark, 409)
        for mode, count in (("all", 10), ("unlearned", 10), ("learned", 0), ("stale", 1)):
            check(student.request(section + "/batches/0?study_mode=" + mode)["count"] == count, "batch filter " + mode)
        for size in (10, 20, 30):
            write(student, section + "/settings", "PUT", {"batch_size": size, "study_mode": "all", "last_batch_index": 0})
            switch(student, 1002)
            check(student.request(section)["settings"]["batch_size"] == size, f"batch size {size} follows account and organization")
            switch(student, 1001)
        write(student, section + "/settings", "PUT", {"batch_size": 11, "study_mode": "all"}, 400)
        write(student, section + "/settings", "PUT", {"batch_size": 10, "study_mode": "all", "last_batch_index": -1}, 400)
        write(student, section + "/learned", "DELETE", mark)
        write(student, section + "/learned", "DELETE", mark)
        check(student.request(section)["stats"]["learned"] == 0, "unmark repeats safely after an answer changed")
        listing = student.request(f"/bff/cards/directions/{direction}/sections?kind=manual&search={run}&page=99&limit=1")
        check(listing["pagination"]["total"] == 1 and listing["pagination"]["page"] == 1, "manual catalog filtering and pagination clamp correctly")
        check(student.request(f"/bff/cards/directions/{direction}/sections?progress=learned&search={run}")["pagination"]["total"] == 0, "unfinished theme does not match learned filter")

        def create_test(label):
            return write(admin, "/bff/tests/catalog", "POST", {"title": "DEV BFF Cards " + run + " " + label,
                         "description": "Карточки через BFF", "direction_id": direction, "questions": questions()}, 201)["id"]

        test = create_test("Из теста")
        tsec = "/bff/cards/sections/test/" + test
        student.request(tsec, expected=403)
        test_settings(admin, test, future=True)
        student.request(tsec, expected=403)
        check(True, "test answer keys are hidden before allowed submission or test end")
        test_settings(admin, test)
        projection = student.request(tsec)
        check([c["answer"] for c in projection["cards"]] == ["Первый", "X; Y", "Москва / Moscow"], "single, multiple and text test questions project as real cards")
        tcard = projection["cards"][0]
        tmark = {"card_ref": tcard["card_ref"], "content_fingerprint": tcard["content_fingerprint"]}
        write(student, tsec + "/learned", "POST", tmark)
        switch(student, 1002)
        check(student.request(tsec)["cards"][0]["status"] == "learned", "test eligibility and progress use membership union across organization divisions")
        switch(student, 1001)
        content = admin.request("/bff/tests/" + test + "/content")
        edited = copy.deepcopy(content["questions"])
        edited[0]["text"] += " — новая версия"
        write(admin, "/bff/tests/" + test + "/content", "PUT", {
            "expected_revision": content["revision"], "title": content["title"], "description": content["description"],
            "direction_id": content["direction_id"], "questions": edited})
        live = student.request(tsec)
        check(live["cards"][0]["card_ref"] == tcard["card_ref"] and live["cards"][0]["status"] == "answer_changed" and live["content_version_id"] != projection["content_version_id"], "test version change keeps stable identity and invalidates obsolete progress")
        write(student, tsec + "/learned", "POST", tmark, 409)
        write(student, tsec + "/learned", "DELETE", tmark)
        hidden = create_test("Скрытые ответы")
        test_settings(admin, hidden, visible=False)
        for client in (student, admin):
            client.request("/bff/cards/sections/test/" + hidden, expected=403)
        catalog = viewer.request("/bff/cards/admin/catalog")
        metadata = next(s for d in catalog["directions"] for s in d["sections"] if s.get("test_id") == hidden)
        check(metadata["visible"] is False and metadata["cards_count"] == 3 and "questions" not in metadata, "admin hidden-test metadata never includes answer keys")
        check(not any(s["refId"] == hidden for d in student.request("/bff/cards/tree")["directions"] for s in d["sections"]), "hidden test is absent from learner catalog")
        foreign.request(tsec, expected=404)
        check(True, "test card lookup respects organization isolation")

        transform_body = {"theme_id": tid, "card_ids": [second["card_id"], first["card_id"]]}
        transformkey = str(uuid.uuid4())
        transform = write(editor, "/bff/cards/transforms", "POST", transform_body, key=transformkey)
        check(transform == write(editor, "/bff/cards/transforms", "POST", transform_body, key=transformkey), "transform creation replays the same snapshot")
        sid = transform["session_id"]
        admin.request("/bff/cards/sessions/" + sid, expected=404)
        write(editor, "/bff/cards/transforms", "POST", {"theme_id": tid, "card_ids": [first["card_id"], first["card_id"]]}, 400)
        editor.request("/bff/auth/context", "POST", {"organization_id": 1001, "division_id": 1002}, expected=403)
        check(True, "division-scoped card editor cannot switch to an unassigned cabinet")
        pinned = write(admin, "/bff/cards/transforms", "POST", transform_body)
        switch(admin, 1002)
        write(admin, "/bff/cards/sessions/" + pinned["session_id"] + "/commit", "POST", {"expected_revision": pinned["revision"]}, 403)
        switch(admin, 1001)
        check(True, "transform commit is pinned to its original selected division")
        commit = {"expected_revision": transform["revision"]}
        result = write(editor, "/bff/cards/sessions/" + sid + "/commit", "POST", commit)
        check(result == write(editor, "/bff/cards/sessions/" + sid + "/commit", "POST", commit), "transformation commit repeats as exactly one tests draft")
        draft = editor.request("/bff/tests/drafts/" + result["draft_id"])
        check(draft["state"] == "draft" and [q["correct_answers"] for q in draft["questions"]] == [[second["answer"]], [changed["answer"]]] and all(q["points"] == 1 for q in draft["questions"]), "actual test draft preserves ordered multiline answers and point weights")
        check([p["x"] for p in draft["layout"]] == [120, 480], "transformation preserves visual selection order")
        extra = write(editor, f"/bff/cards/themes/{tid}/cards", "POST", {"question": "DEV Delete " + run, "answer": "Delete"})
        deletekey = str(uuid.uuid4())
        deleted = write(editor, f"/bff/cards/themes/{tid}/cards/{extra['card_id']}", "DELETE", {"expected_revision": extra["revision"]}, key=deletekey)
        check(deleted == write(editor, f"/bff/cards/themes/{tid}/cards/{extra['card_id']}", "DELETE", {"expected_revision": extra["revision"]}, key=deletekey), "deleted card command replays its original result")
        trash = write(editor, "/bff/cards/themes", "POST", {"name": "DEV Delete theme " + run, "direction_id": direction})
        deletekey = str(uuid.uuid4())
        deleted = write(editor, f"/bff/cards/themes/{trash['id']}", "DELETE", {"expected_revision": trash["revision"]}, key=deletekey)
        check(deleted == write(editor, f"/bff/cards/themes/{trash['id']}", "DELETE", {"expected_revision": trash["revision"]}, key=deletekey), "deleted theme command replays its original result")
        student.request(section + "?organization_id=2001", expected=400)
        student.request("/bff/cards/imports/parse", expected=501)
        check(True, "global imports and caller-supplied scope are excluded")
        write(student, section + "/settings", "PUT", {"batch_size": 10, "study_mode": "unlearned", "last_batch_index": 0})
        examdemo = json.loads((ROOT / "dev/local/bff-exams-demo.json").read_text())
        manifest.update(manual_card_ids=[c["card_id"] for c in student.request(section)["cards"]],
                        eligible_test_id=test, hidden_test_id=hidden, draft_id=result["draft_id"], exam_id=examdemo["classic_exam_id"])
        (ROOT / "dev/local/bff-cards-demo.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n")
        status = "passed"
    except Exception as error:
        failure = str(error) if isinstance(error, AssertionError) else type(error).__name__
        raise
    finally:
        for client in sessions:
            try:
                client.close()
            except Exception:
                pass
        (ROOT / "dev/local/bff-cards-http-report.json").write_text(json.dumps({"status": status, "check_count": len(checks), "checks": checks,
                  "failure": failure, "run": run, "public_bff_only": True}, ensure_ascii=False, indent=2) + "\n")
        print(json.dumps({"status": status, "checks": len(checks)}, ensure_ascii=False))


if __name__ == "__main__":
    main()
