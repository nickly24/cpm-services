# CPM Uploads — local Go coordinator

Port 8014. RabbitMQ delivers administrative import jobs. SQL stores scoped input metadata, references to native owner previews, the job journal, command receipts, outbox and inbox. Uploads never writes another owner's business tables.

```sh
./dev/uploads prepare
./dev/uploads docker-up
./dev/uploads status
./dev/uploads test --background-mode pending
```

`prepare` applies only additive uploads migrations to loopback MySQL `cpm_dev`, merges current uploads table names into the shared dev allowlist, and writes its private environment mode 0600. Existing Rabbit/Auth credentials are reused; no shared keys are generated or services restarted. `docker-up` builds and starts only uploads with `--no-deps`. `run` starts a local binary as an alternative; do not run both listeners.

Native Core credential and legacy-user, Cards and Exams previews are connected. Tests validates JSON/OnlineTestPad and external-result input; Uploads stores their scoped input and validated preview, then rechecks the owner when reading or confirming it. Core legacy previews accept at most 100 rows. Protected templates are deployed for credential CSV, Core legacy XLSX, Cards XLSX, Tests JSON/OnlineTestPad, external-results XLSX and three Exams XLSX. The catalog exposes applicability; Transform uses existing cards and has no file template. Actual runtime and targeted proof are in docs/verification.md. Homework and Attendance retain their own file lifecycles; Uploads can register protected existing references. Source credentials are never stored or downloaded.

**Confirmed background imports are implemented in source, with execution disabled by default.** Human consent was received; the frozen Auth protocol revision 2 binds the original actor/scope, preview, exact native bytes and stable native command UUID. The worker uses Rabbit, current native-owner authorization and readonly receipts; uncertain outcomes never cause a second apply. Private execution plans and migration 005 are source artifacts, not evidence of deployment. The unchanged disabled runtime reports `awaiting_authorization/background_authorization_pending`. See [public protocol](docs/background-public-api.md) and source/runtime evidence in [verification](docs/verification.md).

The optional `./dev/uploads operations --image sha256:<frozen-image> [--check]`
loads only the issuer and four delivery credentials from the sole Auth writer's
regular mode-0600 `dev/local/import-operation-credentials.env`. It does not create
keys, migrate SQL or rebuild owners. Execute only in a newly agreed runtime window
after matching source freeze and affected-owner acknowledgments. Ordinary
`docker-up` and `run` keep operation execution disabled; explicit local
`run --operations` is available for the same approved profile.

See [contract](docs/contracts.md), [process matrix](docs/matrix.md), [Rabbit delivery](docs/rabbit-delivery.md), and [verification](docs/verification.md). Local acceptance and availability are not production deployment or a capacity guarantee.

## Test question tables and live preview access

`tests.json` also accepts `.xlsx` question tables: header within the first ten
rows, columns Название теста, Номер, Категория вопроса, Вопрос, Варианты ответов,
Правильный ответ; Примечание к ответу is ignored. Empty formatted cells are ignored;
archive/formula/external-content/expanded-byte guards remain. No question-count limit; source question numbers and physical row numbers are preserved.
Uploads sends raw cells as `importFormat=question_table` to Tests. Tests owns
answer matching, type inference, aliases, warnings and normal domain validation.

JSON/JS/XLSX preview uses the existing authenticated owner endpoint
`/v1/tests/divisions/{division}/imports/json/preview` with the original bearer,
not the unrelated Uploads internal credential. Confirmation and worker grant,
idempotency, owner receipt and tenant fences remain unchanged.

### Student draft bridge

`POST /v1/uploads/divisions/{division}/student-imports/{draft}/batches/{index}` accepts `expected_revision` and a stable Idempotency-Key. Only the live full organization administrator may bridge their own frozen Core draft. A full admin may choose another active division in the same current organization; Core revalidates it, and Auth derives the grant's target from its live account catalog. Delegated actors remain confined to the selected division.

The bridge reserves one normal `users.credentials` session, asks Core to prepare the immutable native child, and stores only native ID/source hash, not input or credentials. The existing session commit, issuer grant, Rabbit outbox, native UUID/proof and receipt-only UNKNOWN recovery are reused. `GET .../sessions/{session}/job` exposes only the initiator's credential job/capabilities for reload recovery. BFF's explicit-division surface is limited to the bridge, individual session/job reads, confirmation and receipt recovery; catalog/history/file registration are not widened.

If the process stops after the reservation receipt but before session creation, the same command recovers its reserved session ID. A receipt row lock serializes creation/recovery, and session plus file metadata commit together before any native owner request. Replays cannot allocate a second session or file; changed payloads still conflict.

A single file has no 100-person product limit: the UI dispatches groups of 25 serially and stops on unresolved outcomes. Each group's existing atomic native transaction remains independent; prior completed groups are retained. Native preparation conflicts cross Core→Uploads→BFF only as allowlisted physical row/field/reason, without login/password values, arbitrary native body or SQL. Old formats/other owner contracts remain available and their queue semantics are unchanged.
