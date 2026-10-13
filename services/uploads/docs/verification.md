# Uploads verification — local dev, 2026-10-09

Evidence applies to loopback `cpm_dev` and synthetic fixtures. Owner services retain their domain data and lifecycles. A health check, source test or accepted queue command is not proof of a completed domain import.

## Completed checks

- Whole Uploads Go tests with race detection and `go vet ./...` pass. Source parser cases include physical row numbers, zero and fractional raw percentages, safe OnlineTestPad extraction, exact source preservation, formulas and archive bounds. Native adapter tests cover complete 5000-row Exams previews, errors on later pages, changed validation fingerprints, malformed owner responses, stable UUID commands, scope checks and recovery after uncertain responses.
- Live Rabbit transport tests passed with race detection (6.51 seconds). Isolated synthetic deliveries covered duplicate/ACK loss, persisted-outcome response loss, expired leases, restart recovery, forged scope/source rejection and sanitized poison diagnostics. The actual executor persisted `awaiting_authorization` without calling a domain owner.
- The Attendance owner ran the real joint Uploads registry/download acceptance: 290 assertions passed. Evidence: `dev/local/attendance-uploads-attachment-http-report.json`; script: `services/attendance/scripts/uploads_attachment_http_test.py`. It covered selected division, two organizations, actor-private references and receipts, live owner access, PNG download, revoked access and logout. Attachment lifecycle stayed in Attendance.
- 51 synthetic source manifest entries have verified size/SHA256. Repeated generation after normalizing XLSX relationship IDs changed zero file hashes. Fixture existence alone does not establish HTTP readiness.

## Baseline runtime and full HTTP acceptance

Final source tests passed with `go test -race ./... -count=1` (5.020 seconds) and `go vet ./...`. Only Uploads was rebuilt/recreated at 20:11 Moscow; `/health/ready` returned 200. Image: `sha256:f4bfe9f32fc9b5f4446f876e4109325f04ff8a8058ad5e3d66a2199e0ae60dcb`; container binary SHA256: `7587eac6ecfbd844dc14999d4f1a22d224af80444e59d480fd27806225e9b142`.

Own migrations 001–004 applied, and a second `./dev/uploads prepare` also passed. Live MySQL JSON changed numeric lexemes (`1e2` to `100.0`); `uploads_previews_v1` now stores exact validated bytes instead. Its row count was zero before migration. A regression checks exact binary scan, exponent/precision/large IDs and honest integrity failure for normalized old bytes; hashes are never silently rewritten.

Real pending-authorization HTTP acceptance found and fixed an Uploads response defect: native Exams PUT changed row 237 but returned only its first 200 rows. Uploads now reads all pages after a successful edit and verifies that the returned owner revision is the accepted one. Unit regressions cover all 237 rows/errors and changes during reading without contradicting the durable receipt.

Final direct HTTP acceptance passed: **839 real checks**, exit 0, using the same synthetic fixture graph and two directions (1058/1059). Report: `dev/local/uploads-http-report.json` (mode 0600). It covers Core credentials/legacy preview, Cards, all Exams import kinds, Tests JSON/OnlineTestPad/external results, current rights/revocation, actor/org/division isolation, source privacy, malformed sources and protected templates. No new direction was created during resumption. Test client recovery discards the response body only after receiving the accepted HTTP202 headers, then recovers the original receipt/job.

For 11 current jobs, read-only SQL inspection confirmed a published outbox and completed inbox with the original job/session/scope/source/revisions. Rabbit payloads contained references only; no domain rows, source bytes or authorization tokens were persisted in broker commands. The same job and receipt survived an actual restart of only `cpm-dev-uploads-1` at 20:11:56 Moscow; health returned 200 afterward. Shared Rabbit/Auth/Core and the domain owners were never restarted by this suite. Current-run direct SQL mutations are empty; synthetic fixture setup and owner changes use native HTTP APIs.

The final run was `pending`: `subject_apply_tested:false`, `browser_or_bff_tested:false`, with explicit `background_authorization_pending`. The public catalog exposed all required preview capabilities and disabled asynchronous apply. This is delivery/preview acceptance, not a completed domain import.

Reproduce against the retained private synthetic fixture graph without another directory setup:

```sh
dev/.venv/bin/python services/uploads/scripts/http_test.py \
  --background-mode pending \
  --resume-fixtures dev/local/uploads-http-fixtures.json \
  --homework-proof dev/local/uploads-homework-http-proof.json \
  --restart-container cpm-dev-uploads-1
```

This command deliberately restarts only Uploads; coordinate a window with other clients before running it again. It does not rerun the cleaned native Homework fixture.

Homework completed separately: `dev/local/uploads-homework-http-proof.json` records 57 real HTTP checks passed at 20:08 Moscow. It covered register/replay/metadata/receipt, the explicit protected owner-download instruction, denial for another student in the same organization/division, admin and foreign organization, and absence from admin events/sessions/jobs. The temporary Uploads file/receipt/event and own negative student's added membership were removed. Homework then removed its native synthetic job/account/type/assignment and private handoff file through its own cleanup. Uploads never invoked Homework/S3 upload, complete, submit, retry or cancel. Later runs refer to this prior proof explicitly instead of pretending to rerun a deleted fixture.

## Current template, precision and recovery runtime

The later code delta passed the whole `go test -race ./... -count=1` suite (3.658 seconds) and `go vet ./...`. Only Uploads was recreated inside its acknowledged 20:51–20:53 Moscow window, starting at 20:51:31. Health returned 200 and Docker reported healthy. Current image: `sha256:da982c220ceae7183ee7164f5f317d52e771d5a928a43fabb3eb18d293f6d4a9`; actual container binary SHA256: `f372cdfd7193b717e7fac15e01d964dcbe38e3820007fb1fb340c440f5e4f043`. No migrations, Auth keys or other containers changed in this update. The focused template report records 130 HTTP requests within its 284 total request/semantic checks; logout cleanup occurs after report creation.

The catalog now exposes template applicability and the native 200-question Tests limit. Four new source outputs passed actual download→inspect→fill→protected preview acceptance: Core five-column XLSX, CPM JSON, OnlineTestPad JavaScript and Tests eight-column external-results XLSX. The first focused run passed **284 checks**, exit 0, at 20:52:30 Moscow. Report: `dev/local/uploads-template-http-report.json`; separate retained preview manifest: `dev/local/uploads-template-http-fixtures.json`, both mode 0600. Script: `services/uploads/scripts/template_http_test.py`. It reused directions 1058/1059 and verified exact native bytes/MIME, fixed download headers, protected errors and scopes, native preview agreement, zero/fractional scores, distinct explicit account/profile IDs, mismatch rejection, malformed IDs and duplicate headers. Text ID `9007199254740993` reached the owner and survived a durable preview reload exactly; an unsafe numeric cell required text. No import commit, new catalog object, direct SQL mutation or service restart occurred in this focused suite. Seven own Uploads sessions/files and one additional native Core preview remain as scoped synthetic preview fixtures. The previous 839-check report, source fixture graph and Homework 57-check proof stayed byte-for-byte unchanged. This run did not test BFF or browser behavior.

Meaningful unit regressions additionally cover pure literal downloadable JavaScript, malformed/wrong-MIME/unsafe owner files, exact-number redaction/report assembly, browser cancellation before/after owner response, malformed successful references, original receipt lookup, young versus orphaned staging, scope/permission failures and late responses after queued/completed state. Only SQL outcome persistence can outlive cancellation, bounded to five seconds. Native owner calls use the live request context; credential recovery never resends bytes.

BFF independently verified protected recovery of the original browser interruption on this runtime: five checks passed using a fresh live bearer and the same original command/resource/actor/org/division. GET changed aged staging to `outcome_unknown`; the exact native Core receipt returned 404, and reconcile retained uncertainty and the original receipt binding. Report: `dev/local/bff-uploads-original-recovery-http-report.json` (mode 0600); script: `services/bff/scripts/uploads_original_recovery_readonly.py`. No credential source was resent, no new command key or business write was issued, and the fresh Auth session was closed. Missing receipt is not proof that the original operation did not run. This is protected native HTTP recovery evidence; the original UI journal remains bound to its key and fresh browser verification is a separate BFF/QA step.

Protected credential CSV, Core legacy XLSX, Cards XLSX, Tests JSON/OnlineTestPad/external-results and three Exams templates are now connected. Transform, Homework and Attendance expose `template:false` and return `400/template_not_applicable`. Core legacy requires edit rights; Tests template downloads require their view rights and current native target/direction. OnlineTestPad metadata must contain the required direction and dates; the native JSON sample supplies hidden demonstration dates in 2099 that users should replace.

## Addressed BFF/browser preview acceptance

BFF independently completed the addressed Uploads browser delta: **21 checks passed**, with zero reported errors or cleanup errors. Report: `dev/local/bff-uploads-delta-independent-remaining-report.json` (mode 0600); final environment handoff: `dev/local/bff-final-runtime-proof.json` (mode 0600). It covered real JSON/OnlineTestPad template round trips, scoped eight-column external sources with zero/null, two accepted preview CAS edits, outside-exam zero points/grade, current protected template downloads, cabinet/actor/readonly/student isolation, honest pending journals and 390-pixel mobile layouts. Unsafe numeric IDs were displayed as unsafe and prevented rounded structured edits; source parsing remained exact. No apply/retry business call, remote legacy fallback or browser JavaScript error was reported.

The cumulative acknowledged budget was six confirmed new preview POSTs and two confirmed session CAS PUTs on BFF's retained synthetic scope. A seven-cell harness fixture had placed the large ID in the wrong column; that confirmed preview remained intact, and the corrected eight-cell source used a separate authorized intent/key. This was a fixture correction, not a product failure or replay of an unknown command. Earlier seven browser scenarios were retained without repeating their mutations. The original uncertain credential operation remained untouched. Pending commands were zero, and all browser/Auth/held-request contexts were closed at handoff.

The final handoff records Core image `sha256:f3f43d337a2ef4ba00c08cda3d85674f64c1af74cad752ad145fb09e8db9202d`, BFF image `sha256:853b0eb50398b6ac1ed3562feb3f99662f9a4f2c1f926da598f9ea53c2bd2473` with 296 OpenAPI operations, and optimized UI build SHA256 `5da08f648ab9dda7d7b82e69774d5d01b61944b8e4c4b1202346806d76582386` after 535 tests. Uploads remains on the current image/binary above. This is addressed browser preview/access acceptance; general three-chat QA release remains controlled by the main coordinator, and full background application is still unavailable.

## Historical disabled-runtime limits

The evidence above predates the later direct human consent `01a12221-7d06-73f3-8f27-8c05f352977a`. That consent supersedes the earlier approval hold. The frozen [Auth protocol revision 2](../../auth/docs/import-operation-grants-v1.md) and Uploads source implementation now exist, while shared operation credentials, permanent migration 005 and enabled runtime remain pending coordinated deployment. The [public API](background-public-api.md) records the chronology and current source behavior. `cards.transform` remains excluded from the initial background allowlist until the separate Cards→Tests authorization chain is agreed; its existing native bearer flow remains available.

Every asynchronous import currently has zero domain writes, zero progress and no final report; its durable outcome is `awaiting_authorization/background_authorization_pending`. Public catalog exposes `commit:false`; the technical enqueue route is used only to verify delivery. Full asynchronous owner apply, its final reports and the complete browser apply/recovery flow remain unverified until the approved Auth contract is implemented and tested. The separate browser preview/access checks above do not claim that full flow.

The 839-check baseline served credential CSV, Cards and three Exams templates. At that time Core legacy, Tests JSON/OnlineTestPad, external-results and transform returned `501/owner_template_pending`. The later adapters and template applicability are covered by the separate 284-check evidence above; they are not retroactively covered by the 839 checks.

Load testing, CI/CD and production/server migration are outside the current code task by the user's explicit instruction. These checks did not inject a shared broker outage or restart domain owners. BFF/UI changes and browser verification belong to the BFF owner chat.


## Confirmed background source verification (before deployment)

Direct human consent and frozen Auth revision 2 are implemented. Full Go race
suite with the opted-in connection-owned temporary SQL fixture: PASS, 80 top-level
tests / 358 total cases, package elapsed 3.584 seconds. Go vet exit 0; candidate
Docker build passed. Eight hermetic private profile guard checks passed.

Eight-owner binding/receipt matrices check exact source/body/native key, explicit
null fields, current source proof, full connected exam union and native completion.
Ambiguous JSON, changed scope/routes/body, mixed legacy prepare credentials,
mutable current Tests direction and changed known result IDs are covered.

Real temporary MySQL proof uses migration-faithful owned tables on one connection:
lost issuance reply -> restart -> same original confirmation/issuance key and
binding -> one bound outbox; lost apply reply -> matching readonly receipt ->
completion. Simulated restart/redelivery after the durable marker sends only
receipts (one native apply / five receipts). Missing receipt stays UNKNOWN,
foreign account cannot load private plan, changed stored bytes fail integrity.
Profile=999 and account=3 are deliberately distinct. Temporary tables disappear
on connection close; permanent DDL/shared rows/runtime/operation keys remain zero.
This uses HTTP Auth/native fixtures and a persisted envelope broker claim; it does
not prove real Auth authorization, Rabbit delivery, multi-connection SQL contention
or UI. The full enabled chain remains for the agreed minimal deployment window.

Independent receipt interoperability review found and fixed job recover/revoke
action receipt classification, late old-worker errors erasing newly queued
recovery, missing/null/case-aliased zero percent or skipped fields, swapped
student account and duplicate result IDs. Targeted real temporary SQL regression
queues a newer readonly recovery before the old grant returns 403: new job/grant/
generation remain queued and the new receipt completes it, with native apply
count still one. The negative write is fenced by generation/status/lease/grant.
Receipt scalar checks allow legitimate unrelated native fields; explicit zero
remains valid. External students are pinned to the retained confirmed mapping,
without a fresh lookup. All four action receipts resolve the same job for both
reserved and completed states; foreign account reads return no row.
