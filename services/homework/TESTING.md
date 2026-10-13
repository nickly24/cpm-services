# Homework integration verification

These scripts target only local `cpm_dev`, auth `8002`, core `8003`, homework
`8006`, file-work-service `8007`, Rabbit management `15672`, and the isolated
MinIO fixture `19000` / `cpm-homework-test`. They never accept a production
S3 endpoint. Passwords and service keys are read from ignored dev files and
are never copied into reports.

Run after both services are built, migrations are applied, and local auth/core,
MySQL/Rabbit and the isolated S3 fixture are ready:

```sh
./dev/.venv/bin/python services/homework/scripts/http_test.py
./dev/.venv/bin/python services/homework/scripts/workflow_test.py
./dev/.venv/bin/python services/homework/scripts/workflow_test.py --chaos
./dev/.venv/bin/python services/homework/scripts/group_move_test.py
./dev/.venv/bin/python services/homework/scripts/audit_test.py
```

Run `--chaos` in a serial verification window. It temporarily pauses and always
unpauses only `file-work-service`, `rabbitmq`, or `s3-test` in the local Compose
project. A broker pause also interrupts other local consumers. It does not
stop production or change broker users, permissions, topology, or credentials.

The HTTP suite checks:

- Authentication on the OpenAPI public methods, forged role headers, live
  delegated permission changes and revoked sessions.
- Organization/division/group/account boundaries, publication visibility,
  dynamic types per division, duplicate names and type archival.
- Assignment CRUD and filtering, workspace/roster access, review/archive/
  monitoring permissions, settings persistence and independent division limits.
- Strict typed inputs, settings ceilings and invalid policy rollback.
- Manual marks, score clamping inherited from the old manual journal, bulk
  marking, reports and concurrent edits without duplicate result rows.

The workflow suite checks real presigned S3 POST, actual Rabbit delivery and
actual QPDF/Ghostscript processing rather than a replacement compressor:

- Upload UUID idempotency, active upload exclusivity, wrong object/owner POST
  policy, missing object completion, forged object owner and settings snapshots.
- Real scan compression ratio, page count, persisted size/SHA-256, signed
  downloads, retained selectable PDF text and private metadata removal. The
  password-protected fixture uses valid PDF encryption and is independently
  readable with its synthetic fixture password.
- Staff draft privacy and the manual journal/bulk prohibition against bypassing
  the file workflow.
- Draft/current separation, replacement cancellation, concurrent/idempotent
  submit, claim/takeover/release, revision comments, revision uploads/draft
  deletion, grade validation, Moscow midnight auto score, grade edits and clean
  resubmission; queue-only and archive-only delegated rights remain independent
  after a live role update without token refresh.
- Invalid signature, malformed/empty/encrypted/active PDF, page/source/output
  limits, bounded manual retries with immutable policy, and absence of a grade
  or draft after rejection.
- Optional controlled failures: ETag change after completion, Rabbit outage and
  transactional outbox recovery, duplicate result delivery, abandoned-upload
  expiry, late worker success after cancellation, expired lease generation
  recovery with a stale callback, and repeatable object deletion after S3 outage.

The narrow `group_move_test.py` suite moves only its synthetic student's current
division participation. It verifies release of an old proctor claim, immediate
loss of old group file/grade/archive access, successful review by the new
proctor, and preservation of an organization admin claim during queue fetches.
It uses no container pauses and restores its own membership before cleanup.

The October 9 `audit_test.py` suite checks nullable Core/report student class,
immutable public job limits, fresh worker/S3 monitoring and organization/division
isolation through real HTTP/PDF processing. It does not pause shared resources.
Coordinate its temporary changes of dev division limits and owner rebuilds with
other service owners before running. Its report is
`dev/local/homework-audit-report.json`.

Fixtures are generated with a unique prefix. Assignment removal uses the
business API to enqueue object deletion before guarded physical cleanup in the
fixed dev DB. Normal S3 deletion jobs remain until the service finishes them.
Audit rows remain. A failed assertion still restores settings, removes fixture
accounts/roles/catalogue rows and unpauses containers.

Reports are `dev/local/homework-http-report.json` and
`dev/local/homework-workflow-report.json`; the narrow group case writes
`dev/local/homework-group-move-report.json`. A report says `passed` only after
all assertions and cleanup finish. A failure includes a sanitized assertion
label or exception type, never a raw response body or signed URL. These suites
are functional/concurrency verification, not a load-test claim.
