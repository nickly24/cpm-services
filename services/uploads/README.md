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
Правильный ответ, optional Примечание к ответу. Empty formatted cells are ignored;
archive/formula/external-content/expanded-byte guards remain. At most 200 actual
questions; source question numbers and physical row numbers are preserved.
Uploads sends raw cells as `importFormat=question_table` to Tests. Tests owns
answer matching, type inference, aliases, warnings and normal domain validation.

JSON/JS/XLSX preview uses the existing authenticated owner endpoint
`/v1/tests/divisions/{division}/imports/json/preview` with the original bearer,
not the unrelated Uploads internal credential. Confirmation and worker grant,
idempotency, owner receipt and tenant fences remain unchanged.
