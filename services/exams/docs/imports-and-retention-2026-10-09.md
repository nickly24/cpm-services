# Native imports, templates and retention — 2026-10-09

Exams owns XLSX parsing, row validation, editing and atomic application for its three formats. Uploads coordinates the workflow and gathers native pages; it must preserve Exams errors rather than implementing another validator.

## Routes and scope

All public routes use the current auth session. Organization, selected division and account come from auth, never the file or client query. A preview belongs to its creator and exact organization/division/exam/kind; another account cannot read or commit it, including another organization administrator. Current permissions are checked again before replaying a receipt.

Staff need both canonical `exams` and `upload` capabilities: view or edit for GET/template, edit for upload/edit/commit. Organization administrators have their normal organization authority. Students and examiners cannot use imports or download these administrative templates. Question writes additionally require the owner division and access to every connected division for staff; assignments and outside results remain in the selected division. The exam must actually be connected to that division.

| Method | Route | Meaning |
| --- | --- | --- |
| GET | `/v1/exams/{exam}/imports/{kind}/template` | Native XLSX headers; authenticated download |
| POST | `/v1/exams/{exam}/imports/{kind}` | Multipart `file`; create validated preview |
| GET | `/v1/exams/{exam}/imports/{kind}/{session}` | Read a page and current native validation |
| PUT | `/v1/exams/{exam}/imports/{kind}/{session}` | Apply row edits/exclusions using body CAS |
| POST | `/v1/exams/{exam}/imports/{kind}/{session}/commit` | Atomically apply included valid rows |

`kind` is `questions`, `assignments` or `outside`. The first two require a classic exam; `outside` requires an outside LMS exam. Every mutation requires a UUID `Idempotency-Key`.

## Files and templates

Limits: `.xlsx`, at most 10 MiB compressed / 64 MiB expanded, one populated sheet, 5000 data rows and 32 columns. ZIP members and XML are bounded; duplicate entries, DTD/entities and invalid workbook structures are rejected. External references are not followed. Formula/date/bool cells are not calculated and become native row errors.

Templates are header-only valid Office ZIP packages, with a frozen header row and safe attachment filename `exams-{kind}.xlsx`. Fill at least one data row before uploading. The template headers use the same alias map as the parser:

| Kind | Headers |
| --- | --- |
| questions | `part_code`, `question_text`, `answer_text` |
| assignments | `student_id`, `student_login`, six pairs `examinator_1_id` / `examinator_1_login` through `examinator_6_id` / `examinator_6_login`, `replacement_limit` |
| outside | `student_id`, `student_login`, `points`, `grade`, `examinator` |

Numeric student/examiner IDs are **auth account IDs**, not monolith profile IDs. Either ID or login may identify a person; if both are supplied they must resolve to the same active account with the required role and membership in the selected division. Commissions contain 1–6 examiners. Outside points use the existing numeric rule and grade is explicitly 0–5; no automatic grade conversion is introduced.

## Complete paginated preview

POST, GET and PUT return the same `data` DTO and at most 200 rows (`limit` default 100, range 1–200; `offset` default 0). POST/PUT also honor this page size. A 5000-row preview needs 25 pages at limit 200. Its stable row identity is `row_id`; `source_row` retains the spreadsheet row number.

DTO fields are `id`, `revision`, `state`, `kind`, `exam_id`, `organization_id`, `division_id`, `account_id`, `filename`, `source_hash`, `validation_hash`, `rows`, `total`, `limit`, `offset`, `has_more`, `next_offset`, `summary`, `error_report`, `expires_at`, `result`. Each row has `row_id`, `source_row`, `excluded`, canonical camelCase `input` and `errors`. Private privilege baseline/CAS data is never returned.

`source_hash` is SHA256 of the original file and survives row edits. `validation_hash` covers the complete public validated row list, canonicalizing JSON object order and preserving numeric precision; it is identical across pages and SQL persistence. Pre-migration sessions have empty filename/source hash, since their original files were not retained.

`summary` contains global `total_rows`, `included_rows`, `excluded_rows`, `error_rows`, `valid_rows`. `error_report` contains **only this page's** rows with errors (`row_id`, `source_row`, `errors`), so an error report remains bounded. Error counts exclude excluded rows; an excluded row may still retain an identity error in its page report.

Uploads/BFF must fetch GET pages until `has_more=false`, using `next_offset` and `expected_revision` from the first response, and verify the same ID, revision, state, total, source hash and validation hash. A concurrent row edit yields `409 preview_modified`. Revalidation may change when underlying domain data changes without a preview edit; a changed validation hash means discard the partial collection and start again. A commit between pages changes `state` and must also trigger a fresh collection. Never infer a complete preview from POST's first page or `summary` alone.

`expected_revision` query is for GET only. Writes use the body version; providing this query on POST/PUT returns 400. A row edit has this shape:

```json
{"expected_revision":1,"changes":[{"row_id":"row-2","input":{"partCode":"A","questionText":"Question","answerText":"Answer"}},{"row_id":"row-3","excluded":true}]}
```

The whole row input is replaced, not merged. A successful PUT increments preview revision. Unknown/repeated row IDs, duplicate JSON fields and unknown body fields are rejected. A new input resets its private privilege baseline. Exclusion omits application but does not discard the source row.

Commit body is `{"expected_revision":2}`; row changes in commit are rejected. Included rows are revalidated against fresh scoped references and the locked exam, then all apply or none do. Duplicate imported rows and existing assignments/results do not silently overwrite data. An excluded-only commit succeeds with `changed=false` and does not advance subject/rating source revisions. A successful outside-result import advances only that division's rating revision and writes the source event in the same transaction; question imports do not change rating revisions.

Commit response preserves native `committed`, `changed`, `rating_changed`, `counts`, and adds `id`, `session_id`, `revision`, `state`, `total`, `source_hash`, `expires_at`. Committed GET returns the saved validated rows and `result`, without revalidating them as duplicates against already-created entities. Commit changes state while preserving its final preview revision.

## Expiry, receipt recovery and cleanup

An editable preview expires 72 hours after creation; edits do not extend it. Commit changes its expiry to 7 days after completion. Every session route checks logical expiry before receipt replay, returning `410 import_expired` while the row still exists. After physical deletion the session is 404. Lost commit responses are recovered by repeating the original method/path/query/body and key while the session is live. Repeating commit with another key on the same committed session returns its stored result and does not apply rows again.

All command receipt response bodies have a 7-day logical lifetime from receipt creation. Root HTTP guards return `410 idempotency_expired` once that lifetime ends, even before cleanup. The worker retires old successful response bodies to SQL NULL / status 410, retaining organization/account/key/request hash and scope as a permanent compact tombstone. The tombstone is deliberately not deleted: deleting it would let an old key become a new write after restart. A different request using that key remains 409.

Maintenance runs once per minute, under a 5-second context, deleting at most 1000 expired preview records and retiring at most 1000 old receipt responses per pass. Larger backlogs drain over subsequent passes. Receipt index `(status_code,created_at)` and preview expiry index bound eligible lookups. Cleanup never deletes subject exam data. Migration 002 adds filename/source hash and receipt retention index; dev schema application checks existing columns/indexes explicitly and is idempotent on the fixed local `cpm_dev` database.

## Verification

`aux_import_audit_test.go` checks all 5000 rows over 25 pages, first-page limits, canonical hashes including integers above 2^53, native template/parser headers, private-baseline omission, global counts/page errors, canonical dual capabilities and excluded-only no-op.

Opt-in `CPM_EXAMS_AUX_INTEGRATION=1 go test -race ./internal/exams -run '^TestImportHTTPAndRetentionLocalIntegration$' -count=1 -v` exercises actual HTTP/SQL transactions on connection-local temporary copies of migrated schemas. It accepts only loopback MySQL 13306 / `cpm_dev`, guards against connection replacement, supplies identity/references in-process and writes no durable shared data. It verifies 5000-row paging, edits/CAS/exclusion, org/div/account isolation, template permissions, unknown upload/commit response recovery, logical session expiry and bounded receipt retirement with no old-key reapplication. The final 2026-10-09 run passed with race in 73 seconds, also checking revoked permissions, training isolation, root last-part selection, disabled-training replay, semantic rating no-ops and the outside-import source event. Full regular Go tests passed with race and vet; the isolated Rabbit/MySQL outbox run passed with race, and ZIP parser fuzz passed 67,318 executions in 11.5 seconds. Regular unit tests skip it without the flag.

Training remains scoped by organization/division/account/exam. Live-card deletion validates the current usable card; persisted settings reject corrupt/unknown/duplicate JSON instead of silently falling back. Per-part settings use the root selection's last-part value, matching the existing study navigation rule. No training operation changes semantic rating versions.
