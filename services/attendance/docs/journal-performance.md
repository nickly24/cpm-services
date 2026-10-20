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
