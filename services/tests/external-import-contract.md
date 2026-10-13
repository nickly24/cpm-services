# External result import: uploads → Tests

Uploads owns the original file, file metadata, parser transport, preview session,
preview revision, workflow job, progress and report. Tests owns external tests,
confirmed results, result history and the durable domain command receipt. Tests
does not create upload sessions, accept file URLs or read the uploads database.

The external test and all of its results belong to one `organization_id` and one
`division_id`. Results are never shared automatically across divisions. The same
pupil can have independent results in multiple divisions. This domain does not
create an internal question attempt or invoke question grading in Calculate.

## Authentication and permissions

The existing direct-user lane requires `X-Service-Key` **and** a live user `Authorization: Bearer`
token. Tests introspects the actor through Auth on every request, including a
replay, and confirms the current organization/division relationship through Core.
Request JSON cannot set the actor or replace this authorization.

A full organization administrator can operate within their own organization.
A delegated administrator needs both `upload` and `test-results` permissions:
view for validation; edit for commit. The selected division must be in the live
actor scope. Students cannot call these domain import endpoints. The additional
`test-results` capability is an explicit policy in the Go implementation; legacy
imports used the `upload` section alone.

A service key is transport authentication, not blanket permission to write
grades. Background workers must not persist a user Bearer token as their
authorization strategy. A durable scoped operation grant uses the frozen Auth
Rev2 contract and source candidate described in
[background-import-contract.md](docs/background-import-contract.md). Its complete
carrier is an alternative to the bearer lane; mixed credentials and unverified
actor claims are rejected. Source readiness does not imply that new credentials,
runtime or the Uploads/Rabbit/UI chain have been deployed.

## Validate domain rows

`POST /internal/tests/external-results/validate`

```json
{
  "organization_id": 1001,
  "division_id": 1002,
  "test_id": "0123456789abcdef01234567",
  "expected_test_revision": 3,
  "rows": [
    {
      "row_id": "sheet-1-row-3",
      "row": 3,
      "full_name": "Иванов Иван Иванович",
      "account_id": 10001,
      "legacy_student_id": 101,
      "percent": 87.5,
      "correct_count": 14,
      "completed_at": "2026-10-09 12:30:00",
      "time_spent": "00:15:00",
      "login": "ivan"
    }
  ]
}
```

`test_id` is the Go external test ObjectID, also accepted with an `external_`
prefix. A legacy numeric external test ID is not a Go ObjectID. Organizations
and divisions must be positive IDs. There must be 1–5000 raw rows; the public server
also limits the JSON body to 1 MiB. `expected_test_revision` may be omitted or
zero for an initial validation against the latest test. A positive value must
match the current revision. Every source row needs a nonempty unique `row_id`
and a positive source `row` number.

The Core projection supports up to 2000 scoped pupils, and the external aggregate
limits current confirmed results accordingly. The larger raw-row bound permits
uploads to receive duplicate/unmatched-row diagnostics; it does not allow an
incomplete people projection or bypass result uniqueness and storage limits.

`account_id` and `legacy_student_id` are separate identifiers. Account ID is the
subject of a result; legacy student ID identifies its Core profile. Either may
be omitted for automatic mapping. An explicitly supplied account is looked up
only among the pupils provided by Core for the requested organization/division.
An explicitly supplied profile is resolved through that same projection. When
both are present they must identify the same pupil.

Automatic mapping follows the legacy rule: split the name by whitespace,
compare lowercase surname and given name, ignore the patronymic. It does not
substitute `ё` with `е`. At least two name parts are required. Unknown or
ambiguous names are errors; ambiguity returns candidates within the authorized
scope so uploads can offer an explicit selection. Blocked/inactive pupils in
the current scoped Core projection remain available for historical results.
Pupils outside that projection cannot be imported.

Every row sharing a duplicate surname/given-name key is invalid, including
rows with explicit mappings. Duplicate mapped accounts and duplicate source
row identifiers are also invalid. An existing result, including a zero or an
unknown manual score, blocks a new import for that pupil/test/division.

`percent` must be a finite JSON number between **0 and 100 inclusive**. Null,
omission, booleans and strings are invalid. Zero is a confirmed score, distinct
from no result. Decimals are retained without rounding or scaling: `0.9` means
`0.9%`, not `90%`. `correct_count` is an optional integer metadata value; it does
not participate in the score formula. Completion time, duration and login are
optional text metadata, also unused in scoring. The catalog test date controls
the rating period.

The response is `{status:true,data:...}` with:

```json
{
  "organization_id": 1001,
  "division_id": 1002,
  "test_id": "0123456789abcdef01234567",
  "revision": 3,
  "preview_hash": "<sha256>",
  "rows": [
    {
      "row_id": "sheet-1-row-3",
      "row": 3,
      "full_name": "Иванов Иван Иванович",
      "account_id": 10001,
      "legacy_student_id": 101,
      "percent": 87.5,
      "student_full_name": "Иванов Иван Иванович",
      "action": "import",
      "errors": []
    }
  ],
  "summary": {
    "total_rows": 1,
    "import_rows": 1,
    "row_errors": 0,
    "matched_students": 1,
    "duplicate_rows": 0,
    "existing_results": 0
  },
  "can_commit": true
}
```

Invalid domain rows return a successful validation response with
`can_commit:false`, `action:"error"` and structured
`errors:[{code,field,message}]`. They write nothing. Malformed JSON or scope and
revision failures return an HTTP error. Derived fields such as `errors`,
`action`, `student_full_name`, candidates and summary must not be copied into
the commit request; uploads keeps them in its own preview state.

## Confirm the batch

`POST /internal/tests/external-results/commit`

Use the same row DTO and scope, plus:

```json
{
  "command_id": "595e3e03-fce4-4eaf-8455-857b7a3a24fc",
  "organization_id": 1001,
  "division_id": 1002,
  "test_id": "0123456789abcdef01234567",
  "expected_test_revision": 3,
  "preview_hash": "<validated sha256>",
  "upload_id": "opaque-upload-id",
  "upload_session_id": "opaque-upload-session-id",
  "upload_preview_revision": 2,
  "rows": [
    {
      "row_id": "sheet-1-row-3",
      "row": 3,
      "full_name": "Иванов Иван Иванович",
      "account_id": 10001,
      "legacy_student_id": 101,
      "percent": 87.5
    }
  ]
}
```

The command ID is a nonzero UUID generated and durably retained by uploads.
The upload and session identifiers are opaque audit references; Tests does not
dereference them. Upload preview revision and expected test revision must both
be positive. Freeze the complete DTO, actor and command ID before dispatching
and reuse them on retry.

Tests rechecks authorization, the external test revision, scoped pupils,
mapping, every row and the domain preview hash. Every row must remain valid.
A changed pupil mapping or display name changes the preview hash and requires
another validation. A source result added after preview changes the test
revision and prevents applying the stale batch.

All confirmed results, one command receipt and the committed history reference
are written in one external test aggregate CAS. Mongo transactions are not
required. A rejected row or CAS failure cannot create a subset of official
results. History documents prepared before a failed CAS are not committed
history until their ID is linked by the aggregate.

The first successful request returns **201**. An identical confirmed retry
returns **200** with the original receipt, even after the test revision has
advanced or the test has been logically deleted. Such a replay only confirms
the historical operation; it never restores deleted catalogue/results. New
commands against a deleted test return 404. Idempotency is scoped to `(organization_id,division_id,test_id,
command_id)`. Actor, normalized rows, test revision, preview hash and upload
audit references are bound to the command input hash. Reusing the command with
different data or actor returns 409. Concurrent identical requests return the
winning durable receipt rather than inserting duplicate results.

```json
{
  "status": true,
  "data": {
    "command_id": "595e3e03-fce4-4eaf-8455-857b7a3a24fc",
    "status": "committed",
    "source_revision": 4,
    "results": [
      {
        "row_id": "sheet-1-row-3",
        "result_id": "0123456789abcdef01234568",
        "account_id": 10001,
        "revision": 1,
        "percent": 87.5
      }
    ]
  }
}
```

The receipt retains upload/session/revision audit references and the resulting
result IDs. Import inserts new results; it does not overwrite an existing
official result. Administrative corrections use the separate public result
API with its own permissions, revision checks and history.

## Errors and retries

| HTTP | Meaning |
| --- | --- |
| 400 | Invalid JSON/DTO, missing confirmation data, invalid row batch |
| 401 | Missing/invalid service key or live user token |
| 403 | Current organization/division/method permission denied |
| 404 | External test absent, deleted or outside the requested scope |
| 409 | Stale test revision, changed preview, conflicting command payload, concurrent different update |
| 415 | JSON content type required |
| 503 | Auth/Core temporarily unavailable |

After a network error, uploads retries the frozen command. A 409 requiring new
preview or revision is shown for review and revalidation; it must not be
silently changed under the same confirmed command ID. Failures never turn a
missing external result into a zero. Uploads reports per-row validation errors
and whole-batch outcomes; Tests does not own the general import job lifecycle.
