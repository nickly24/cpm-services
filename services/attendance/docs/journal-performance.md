# Journal loading and cell writes

`GET /v1/attendance/divisions/{division}/journal-workspace?date_from=YYYY-MM-DD&date_to=YYYY-MM-DD`
returns one `Result` with a shared organization `revision` and
`value: {journal, students, types}`. It reuses the existing report, roster and type
permissions and live Core checks. Student reports/rosters stay personal; proctors
stay within their live assigned group; a scan-only grant cannot read this view.
Only date_from/date_to are accepted. Existing endpoints remain supported.

Manual mark/unmark commands keep the same envelope, idempotency keys, CAS,
canonical responses, permanent Core history guard and receipts. Their SQL read
projects just next_id, the target day, type and previous entry, under the existing
organization row lock. JSON_SET writes only the affected JSON paths. Immutable
full audit snapshots are copied inside MySQL with INSERT SELECT, in the same
transaction as state/revision/receipt; no full audit payload is transmitted back
from the service. Replay authorization continues to read the full original audit.
No schema migration, new cache or environment setting is required.

The organization lock, Core checks inside it and full audit storage remain.
This is not normalized per-entry persistence, and SQL may still rewrite a binary
JSON value internally. Signed/profile scanning retains its existing aggregate
mutation path. The 32 MiB size limit on a projected write is checked against the
stored MySQL JSON representation; it can be more conservative than the old
compact Go JSON limit near capacity.

Frontend opens the fullscreen report only after preparing its workspace. A
successful manual cell response is applied to that exact cell with its immutable
type snapshot and next revision; it does not trigger three full GETs. UNKNOWN
keeps the original command UUID. Queued unsent brush intents remain paused in the
mounted editor; recovery/reload and explicit continuation are required after an
error. They are not a durable offline draft: navigation is guarded and a force
close can discard unsent intentions.

Release order for this additive contract: Attendance, BFF, frontend. Old clients
can continue using the original reads. No production migration or data replay is
part of this change.

## Request context and atomic cell batches

BFF still calls live Auth `/me` on every browser operation. With distinct optional
`BFF_ATTENDANCE_CONTEXT_KEY` configured in both BFF and Attendance, it signs that
request's full relevant identity (including target-division grants and group),
audience `attendance`, method, exact URI and body hash, with a 30-second lifetime.
Attendance validates the assertion and reuses this live result. No permissions
are cached between requests. Missing assertions use direct bearer introspection;
invalid/partial assertions fail closed. Oversized identity scopes use the bearer
path. Browser context/signature headers are never forwarded as trusted values.

Manual mark/erase requests use Core `/internal/core/attendance/cell-targets` once.
It locks the active organization/division, validates all current active student
memberships, checks live proctor assignment/group when required and commits
permanent history reservations. Full editors preserve historical erasure and
receipt replay for inactive students: explicit scope-only validation checks the
active organization/division without creating guards or requiring live students.
Proctor erasures/replays still validate current membership/assignment and the
original historical group. Set-based SQL bounds query count independently
of the number of students. Revocation uses the same division/account locks.
Existing scanner/profile resolution and zap APIs retain their existing Core
paths; they also benefit from the single Auth request when context is enabled.

`POST /v1/attendance/divisions/{division}/attendance-cells/batch` takes the usual
CAS/idempotency envelope with `data.cells` (1–100 unique day/account pairs):
`{class_day_id,account_id,attendance_type_id}`. Explicit type `0` erases an existing
mark. Every day/type/target is scoped; zap-linked entries remain protected. All
cells commit atomically under one organization revision, receipt and full audit.
The response is `{revision,value:{cells:[Entry,...]}}` in request order, including
canonical tombstones for erasures. Existing single-cell APIs are unchanged.

Excel starts the first cell immediately; accumulated cells drain in batches of
up to 100, serially against the last confirmed revision. It validates the whole
canonical batch before updating the grid. Unknown outcomes preserve the exact
UUID and batch, pause the queue, and never submit a new UUID automatically.
Positive receipt recovery reloads the workspace and consumes that exact whole
batch; the unsent tail needs explicit acknowledgement/continuation. Full inputs
remain only in RAM; durable browser storage contains command metadata only.

Rollout: Core/Attendance/BFF backend first; configure a new independent context
key in BFF and Attendance runtime env files (no Auth/Tests/Homework/QR key rotation).
Then publish frontend. Old frontend works with new backend. Roll back frontend
before backend if reverting the new batch endpoint. No database migrations.
