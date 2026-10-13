"""Real HTTP acceptance for Cards' Uploads owner contract; synthetic dev only.

Run only after the matching Cards container has been rebuilt. This script never
restarts services, changes shared roles/fixtures, or calls Uploads' worker.
"""
import copy
import hashlib
import io
import json
import re
import secrets
import uuid
import urllib.error
import urllib.request
import xml.etree.ElementTree as ET
import zipfile
from concurrent.futures import ThreadPoolExecutor

from support import HOSTS, ROOT, api, checks, expect, login, mysql_connection, request, save, settings, xlsx


XLSX_TYPE = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
NS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
RUN = secrets.token_hex(8)


def card(path, token, method="GET", body=None, code=200, key=None, **kwargs):
    headers = {"Idempotency-Key": key} if key is not None else {}
    return api("cards", "/v1/cards" + path, token, method, body, code, headers=headers, **kwargs)


def parse(token, content, direction, theme, key, code=200):
    return card(
        "/imports/parse", token, "POST", code=code, key=key,
        files={"file": ("synthetic-owner-contract.xlsx", content)},
        data={"direction_id": str(direction), "theme_id": str(theme)},
    )


def receipt(token, key, original, session_id=None):
    out = card("/receipts/" + key, token)
    expect(out["state"] == "completed", "receipt records a completed command")
    expect(bool(out["operation"]), "receipt identifies its owner operation")
    expect(bool(re.fullmatch(r"[a-f0-9]{64}", out["request_hash"])), "receipt has a SHA256 request digest")
    expect(out["result"] == original, "receipt returns the exact original response DTO")
    if session_id is not None:
        expect(out.get("session_id", out.get("resource_id")) == session_id, "receipt identifies the original owner session")
    return out


def download_template(token):
    req = urllib.request.Request(
        HOSTS["cards"] + "/v1/cards/imports/template",
        headers={"Authorization": "Bearer " + token},
    )
    with urllib.request.urlopen(req, timeout=40) as response:
        expect(response.status == 200, "owner XLSX template download succeeds")
        expect(response.headers.get_content_type() == XLSX_TYPE, "template has the XLSX content type")
        expect("attachment" in response.headers.get("Content-Disposition", ""), "template is an attachment")
        content = response.read((10 << 20) + 1)
        expect(len(content) <= 10 << 20, "template download is bounded")
    return content


def fill_template(content):
    """Inspect the returned workbook and add only this run's synthetic data."""
    with zipfile.ZipFile(io.BytesIO(content)) as source:
        entries = {name: source.read(name) for name in source.namelist()}
    strings = []
    if "xl/sharedStrings.xml" in entries:
        for item in ET.fromstring(entries["xl/sharedStrings.xml"]).findall("{%s}si" % NS):
            strings.append("".join(t.text or "" for t in item.iter("{%s}t" % NS)))
    root = ET.fromstring(entries["xl/worksheets/sheet1.xml"])
    data = root.find("{%s}sheetData" % NS)
    expect(data is not None, "template contains worksheet rows")
    header = data.find("{%s}row" % NS)
    labels = []
    for cell in header.findall("{%s}c" % NS):
        value = cell.find("{%s}v" % NS)
        if cell.get("t") == "s":
            labels.append(strings[int(value.text)])
        else:
            labels.append("".join(t.text or "" for t in cell.iter("{%s}t" % NS)))
    expect(labels == ["Вопрос", "Ответ"], "template has the authoritative Russian headers")
    expect(len(data.findall("{%s}row" % NS)) == 1, "template contains no organization or personal rows")
    row = ET.SubElement(data, "{%s}row" % NS, {"r": "2"})
    for column, text in [("A", "Template question " + RUN), ("B", "Template answer\n\nSecond line")]:
        cell = ET.SubElement(row, "{%s}c" % NS, {"r": column + "2", "t": "inlineStr"})
        ET.SubElement(ET.SubElement(cell, "{%s}is" % NS), "{%s}t" % NS).text = text
    entries["xl/worksheets/sheet1.xml"] = ET.tostring(root, encoding="utf-8", xml_declaration=True)
    stream = io.BytesIO()
    with zipfile.ZipFile(stream, "w", zipfile.ZIP_DEFLATED) as output:
        for name, value in entries.items():
            output.writestr(name, value)
    return stream.getvalue()


def sql_card_count(theme_id, organization_id):
    # Read only: prove that repeats did not create hidden duplicate rows.
    conn = mysql_connection(settings())
    try:
        cur = conn.cursor()
        cur.execute("SELECT DATABASE()")
        expect(cur.fetchone()[0] == "cpm_dev", "SQL proof is restricted to cpm_dev")
        cur.execute(
            "SELECT COUNT(*) FROM cards_items_v1 WHERE organization_id=%s AND theme_id=%s",
            (organization_id, theme_id),
        )
        return cur.fetchone()[0]
    finally:
        conn.close()


def main():
    settings()  # Validate fixed dev host/database before any write.
    fixture = json.loads((ROOT / "dev/local/cards-fixtures.json").read_text())
    password = json.loads((ROOT / "dev/local/accounts.json").read_text())["password"]
    admin = login("dev_admin", password)
    reserve = login("dev_admin", password, 1001, 1002)
    other = login("dev_admin_math", password, 2001, 2001)
    editor = login("dev_cards_editor", fixture["password"])
    viewer = login("dev_cards_viewer", fixture["password"])
    student = login("dev_cards_student", fixture["password"])
    direction, other_direction = fixture["direction"], fixture["other_direction"]
    own = {"run_id": RUN, "themes": [], "sessions": [], "drafts": [], "permission_fixtures": []}

    request("cards", "/health/ready")
    contract = card("/imports/contract", viewer)
    expect(contract["type"] == "cards.xlsx" and contract["permission"] == "train", "contract uses canonical train permission")
    expect(contract["scope"] == "organization", "import contract declares organization ownership")
    expect(contract["columns"] == ["Вопрос", "Ответ"], "contract describes native Cards columns")
    expect(contract["source_storage"] == "uploads" and contract["retains_source_bytes"] is False, "source lifecycle remains in Uploads")
    for path in ["/imports/contract", "/imports/targets", "/imports/template"]:
        card(path, student, code=403)
        request("cards", "/v1/cards" + path, code=401)
    template = fill_template(download_template(viewer))

    theme = card("/themes", admin, "POST", {"name": "DEV owner contract " + RUN, "direction_id": direction})
    alternate = card("/themes", admin, "POST", {"name": "DEV owner contract alternate " + RUN, "direction_id": direction})
    foreign_theme = card("/themes", other, "POST", {"name": "DEV owner contract foreign " + RUN, "direction_id": other_direction})
    own["themes"] = [theme["id"], alternate["id"], foreign_theme["id"]]
    tid, ftid = theme["id"], foreign_theme["id"]
    targets = card("/imports/targets", viewer)
    expect(direction in {x["id"] for x in targets["directions"]}, "targets contain the permitted organization direction")
    expect(other_direction not in {x["id"] for x in targets["directions"]}, "targets exclude the foreign direction")
    expect(tid in {x["id"] for x in targets["themes"]}, "targets contain own manual theme")
    expect(ftid not in {x["id"] for x in targets["themes"]}, "targets exclude foreign themes")
    expect(all(x["direction_id"] in {d["id"] for d in targets["directions"]} for x in targets["themes"]), "each target theme belongs to an allowed direction")
    expect(not any("test_id" in x for x in targets["themes"]), "import targets are manual themes only")
    foreign_targets = card("/imports/targets", other)
    expect(ftid in {x["id"] for x in foreign_targets["themes"]} and tid not in {x["id"] for x in foreign_targets["themes"]}, "target catalogs are isolated in both directions")

    key = "uploads_parse_" + RUN
    raw = xlsx([["Вопрос", "Ответ"], ["Owner Q " + RUN, "Owner A\n\nLine 2"], ["Editable Q", "Original A"]])
    with ThreadPoolExecutor(2) as pool:
        results = list(pool.map(lambda _: parse(admin, raw, direction, tid, key), range(2)))
    parsed = results[0]
    expect(results[0] == results[1], "concurrent parse retries return one exact original preview")
    sid = parsed["session_id"]
    own["sessions"].append(sid)
    initial_receipt = receipt(admin, key, parsed, sid)
    expect(initial_receipt["operation"] == "import:parse", "parse receipt identifies native parse")
    expect(parse(reserve, raw, direction, tid, key) == parsed, "same organization division switch preserves import command identity")
    expect(card("/sessions/" + sid, reserve)["session_id"] == sid, "manual import preview is shared between the actor's divisions")
    source = card("/sessions/" + sid + "/source", admin)
    expect(source["source_sha256"] == hashlib.sha256(raw).hexdigest(), "source metadata is bound to the exact uploaded bytes")
    expect(source["source_filename"] == "synthetic-owner-contract.xlsx" and source["source_sheet"] == "Users", "source metadata retains safe filename and active sheet")
    expect(source["organization_id"] == 1001 and source["direction_id"] == direction, "source metadata carries verified organization and direction")
    expect(source["content_type"] == XLSX_TYPE and source["retains_source_bytes"] is False, "owner source contract returns metadata only")
    expect(not {"content", "bytes", "download_url", "storage_key"}.intersection(source), "source metadata exposes no file content or storage credential")
    for token in [other, editor]:
        card("/sessions/" + sid, token, code=404)
        card("/sessions/" + sid + "/source", token, code=404)
        card("/receipts/" + key, token, code=404)
    card("/sessions/" + sid + "/source", viewer, code=403)
    changed_raw = xlsx([["Вопрос", "Ответ"], ["Changed file " + RUN, "A"]])
    parse(admin, changed_raw, direction, tid, key, 409)
    parse(admin, raw, direction, alternate["id"], key, 409)
    expect(receipt(admin, key, parsed, sid)["request_hash"] == initial_receipt["request_hash"], "conflicting requests cannot overwrite the parse receipt hash")
    for invalid_key in ["short", "x" * 129]:
        parse(admin, raw, direction, tid, invalid_key, 400)
    parse(admin, raw, other_direction, ftid, "foreign_scope_" + RUN, 404)

    # Identical opaque keys are private to each authenticated account/organization.
    account_preview = parse(editor, raw, direction, tid, key)
    org_preview = parse(other, raw, other_direction, ftid, key)
    own["sessions"].extend([account_preview["session_id"], org_preview["session_id"]])
    expect(len({sid, account_preview["session_id"], org_preview["session_id"]}) == 3, "same command key is isolated by actor and organization")
    receipt(editor, key, account_preview, account_preview["session_id"])
    receipt(other, key, org_preview, org_preview["session_id"])
    card("/sessions/" + account_preview["session_id"] + "/source", admin, code=404)
    template_preview = parse(admin, template, direction, alternate["id"], str(uuid.uuid4()))
    own["sessions"].append(template_preview["session_id"])
    expect(template_preview["preview"]["summary"]["cards_to_import"] == 1, "downloaded template round-trips through native parser")

    edit_key = str(uuid.uuid4())
    rows = [{"row": x["row"], "question": x["question"], "answer": x["answer"], "skip": False} for x in parsed["preview"]["cards"]]
    rows[1]["answer"] = "First corrected answer"
    first_body = {"expected_revision": 1, "cards": rows}
    edited = card("/sessions/" + sid, admin, "PUT", first_body, key=edit_key)
    expect(edited["revision"] == 2, "first edit increments owner revision once")
    first_edit_receipt = receipt(admin, edit_key, edited, sid)
    newer_rows = copy.deepcopy(rows)
    newer_rows[1]["answer"] = "Newest corrected answer"
    second_key = "uploads_edit_" + RUN
    newer = card("/sessions/" + sid, admin, "PUT", {"expected_revision": 2, "cards": newer_rows}, key=second_key)
    expect(newer["revision"] == 3, "separate edit advances preview to revision three")
    expect(card("/sessions/" + sid, admin, "PUT", first_body, key=edit_key) == edited, "lost edit reply recovers its original revision after a newer edit")
    expect(receipt(admin, edit_key, edited, sid)["request_hash"] == first_edit_receipt["request_hash"], "original edit receipt digest survives later edits")
    expect(receipt(admin, second_key, newer, sid)["request_hash"] != first_edit_receipt["request_hash"], "different edit payloads have distinct request digests")
    bad_edit = copy.deepcopy(first_body)
    bad_edit["cards"][1]["answer"] = "Conflicting payload"
    card("/sessions/" + sid, admin, "PUT", bad_edit, code=409, key=edit_key)
    expect(card("/sessions/" + sid, admin)["preview"]["cards"][1]["answer"] == "Newest corrected answer", "replayed or rejected edits cannot revert live preview")
    expect(parse(admin, raw, direction, tid, key) == parsed, "parse replay returns original preview after editing")

    commit_key = "uploads_commit_" + RUN
    commit_body = {"expected_revision": 3}
    with ThreadPoolExecutor(2) as pool:
        commits = list(pool.map(lambda _: card("/sessions/" + sid + "/commit", admin, "POST", commit_body, key=commit_key), range(2)))
    committed = commits[0]
    expect(commits[0] == commits[1] and committed["created"] == 2, "concurrent commit creates each imported row once")
    receipt(admin, commit_key, committed, sid)
    expect(card("/sessions/" + sid + "/commit", reserve, "POST", commit_body, key=commit_key) == committed, "commit replay survives same-organization cabinet switch")
    expect(card("/sessions/" + sid + "/commit", admin, "POST", commit_body, key=str(uuid.uuid4())) == committed, "another commit command returns the same session result")
    card("/sessions/" + sid + "/commit", admin, "POST", {"expected_revision": 2}, code=409, key=commit_key)
    expect(sql_card_count(tid, 1001) == 2, "SQL proof: only two cards exist after all commit repeats")
    expect(card("/sessions/" + sid, admin)["state"] == "completed", "live preview exposes completed state")
    receipt(admin, edit_key, edited, sid)
    expect(card("/sessions/" + sid, admin, "PUT", first_body, key=edit_key) == edited, "completed import still allows exact historical edit recovery")
    expect(card("/sessions/" + sid + "/source", admin)["source_sha256"] == source["source_sha256"], "commit preserves original source metadata")
    for token in [other, editor]:
        card("/sessions/" + sid + "/commit", token, "POST", commit_body, code=404, key=commit_key)
        card("/receipts/" + commit_key, token, code=404)

    # The transform destination remains a verified division, while Cards' source is organization-owned.
    transform_key = "uploads_transform_" + RUN
    transform_body = {"theme_id": tid, "card_ids": committed["card_ids"]}
    transformed = card("/transforms", editor, "POST", transform_body, key=transform_key)
    tsid = transformed["session_id"]
    own["sessions"].append(tsid)
    expect(card("/transforms", editor, "POST", transform_body, key=transform_key) == transformed, "opaque Uploads key works for native transform replay")
    receipt(editor, transform_key, transformed, tsid)
    card("/sessions/" + tsid + "/source", editor, code=404)
    draft_key = "uploads_transform_commit_" + RUN
    result = card("/sessions/" + tsid + "/commit", editor, "POST", {"expected_revision": 1}, key=draft_key)
    own["drafts"].append(result["draft_id"])
    expect(card("/sessions/" + tsid + "/commit", editor, "POST", {"expected_revision": 1}, key=draft_key) == result, "transform retry does not duplicate Tests draft")
    receipt(editor, draft_key, result, tsid)
    draft = api("tests", "/v1/tests/divisions/1001/drafts/" + result["draft_id"], editor)
    expect(draft["state"] == "draft" and len(draft["questions"]) == 2, "real Tests owns one unpublished draft with imported questions")
    expect(draft["questions"][1]["correct_answers"] == ["Newest corrected answer"], "transform uses the latest committed manual answer")

    pinned_key = "uploads_pinned_transform_" + RUN
    pinned = card("/transforms", admin, "POST", transform_body, key=pinned_key)
    own["sessions"].append(pinned["session_id"])
    expect(pinned["division_id"] == 1001, "transform records its verified Tests destination division")
    card("/sessions/" + pinned["session_id"], reserve, code=403)
    card("/receipts/" + pinned_key, reserve, code=403)
    card("/sessions/" + pinned["session_id"] + "/commit", reserve, "POST", {"expected_revision": 1}, code=403, key="wrong_division_" + RUN)
    expect(card("/sessions/" + pinned["session_id"], admin)["state"] == "preview", "cabinet switch cannot redirect or apply the pinned transform")

    # These are NEW fixtures for this run. Never mutate the shared editor/viewer roles.
    permissions = {"train": {"view": True, "edit": True}, "tests": {"view": True, "edit": True}}
    role_name = "DEV Cards contract rights " + RUN
    role = api("core", "/v1/core/roles", admin, "POST", {"name": role_name, "permissions": permissions}, 201)
    actor_login = "dev_cards_contract_" + RUN
    actor_password = secrets.token_urlsafe(24)  # In memory only; never journaled.
    actor = api("core", "/v1/core/divisions/1001/users", admin, "POST", {
        "role": "staff_admin", "full_name": "DEV Cards contract " + RUN,
        "login": actor_login, "password": actor_password, "role_id": role["id"], "tg_name": "",
    }, 201)
    own["permission_fixtures"].append({"role_id": role["id"], "account_id": actor["account_id"]})
    actor_token = login(actor_login, actor_password)
    rights_key = "uploads_rights_parse_" + RUN
    rights_preview = parse(actor_token, raw, direction, tid, rights_key)
    rights_id = rights_preview["session_id"]
    own["sessions"].append(rights_id)
    receipt(actor_token, rights_key, rights_preview, rights_id)
    card("/sessions/" + rights_id + "/source", actor_token)
    try:
        restricted = copy.deepcopy(permissions)
        restricted["train"]["edit"] = False
        api("core", "/v1/core/roles/" + str(role["id"]), admin, "PUT", {"name": role_name, "permissions": restricted})
        card("/receipts/" + rights_key, actor_token, code=403)
        card("/sessions/" + rights_id + "/source", actor_token, code=403)
        parse(actor_token, raw, direction, tid, rights_key, 403)
        expect(card("/imports/contract", actor_token)["permission"] == "train", "revoking train.edit retains authorized read-only contract access")
        api("core", "/v1/core/roles/" + str(role["id"]), admin, "PUT", {"name": role_name, "permissions": permissions})
        receipt(actor_token, rights_key, rights_preview, rights_id)
        scoped_transform_key = "uploads_rights_transform_" + RUN
        scoped_transform = card("/transforms", actor_token, "POST", transform_body, key=scoped_transform_key)
        scoped_transform_id = scoped_transform["session_id"]
        own["sessions"].append(scoped_transform_id)
        receipt(actor_token, scoped_transform_key, scoped_transform, scoped_transform_id)
        restricted = copy.deepcopy(permissions)
        restricted["tests"]["edit"] = False
        api("core", "/v1/core/roles/" + str(role["id"]), admin, "PUT", {"name": role_name, "permissions": restricted})
        card("/sessions/" + scoped_transform_id, actor_token, code=403)
        card("/receipts/" + scoped_transform_key, actor_token, code=403)
        card("/sessions/" + scoped_transform_id + "/commit", actor_token, "POST", {"expected_revision": 1}, code=403, key="revoked_tests_commit_" + RUN)
        card("/transforms", actor_token, "POST", transform_body, code=403, key=scoped_transform_key)
        expect(card("/sessions/" + rights_id + "/source", actor_token)["source_sha256"] == source["source_sha256"], "revoking tests.edit does not remove independent manual import access")
    finally:
        api("core", "/v1/core/roles/" + str(role["id"]), admin, "PUT", {"name": role_name, "permissions": permissions})
    expect(card("/sessions/" + scoped_transform_id, actor_token)["state"] == "preview", "permission revocation prevented creation of a Tests draft")

    return {
        "status": "passed", "checks": len(checks), "details": checks,
        "real_dependencies": ["Docker cards", "auth", "core", "tests", "dev MySQL read-only proof"],
        "synthetic_only": True, "resources": own,
        "scope": "Native owner contract; Uploads queue/BFF acceptance runs separately",
    }


if __name__ == "__main__":
    report = ROOT / "dev/local/cards-uploads-contract-http-report.json"
    try:
        result = main()
        save(report, result)
        print(f'Cards Uploads owner contract: {result["checks"]} checks passed. Report: dev/local/cards-uploads-contract-http-report.json')
    except Exception as error:
        save(report, {
            "status": "failed", "checks": len(checks), "details": checks,
            "error_type": type(error).__name__,
            "error": str(error) if isinstance(error, AssertionError) else "Local exception; credentials omitted",
        })
        raise
