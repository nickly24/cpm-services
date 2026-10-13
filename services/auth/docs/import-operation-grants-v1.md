# Background import wire contract v1 — frozen revision 2

9 October 2026. Direct human implementation consent: main userMessage `01a12221-7d06-73f3-8f27-8c05f352977a`. Revision 2 has explicit Core/Cards/Tests/Exams/Uploads ACK and coordinator consistency ACK. This freezes the wire protocol, not implementation/runtime readiness or general QA RELEASE. Any wire change requires a new revision and affected owner ACK. Auth writer is BFF chat; Core, Uploads, Cards, Tests and Exams each have one owner writer. Existing bearer routes remain unchanged. No credentials, schema or shared runtime changes precede freeze/checks/new window/affected owner ACK.

## Transport and credentials

Native delivery requires exactly one of each header:

- `X-CPM-Operation-Grant`: 32 lowercase hex.
- `X-CPM-Operation-Stage`: `apply`, `preview`, `receipt`, or `core-direction`.
- `X-CPM-Operation-Job`: 32 lowercase hex.
- `X-CPM-Operation-Service-Key`: the distinct trusted caller→owner delivery credential.
- `Idempotency-Key`: original canonical lowercase UUID, including reads; Tests also matches its unchanged commit DTO `command_id`.

Bearer plus any grant carrier is rejected. Partial/duplicate carriers, encoded/dot-segment paths, scope/selector headers, GET bodies and bare `?` are rejected. Browser/BFF never forwards carrier or internal credentials. A grant ID alone is not a credential.

Auth issue/recover/revoke use `X-Service-Key` = `AUTH_OPERATIONS_ISSUER_KEY` plus the current initiator bearer. Authorize uses `X-Service-Key` = the calling owner's `AUTH_OPERATIONS_CORE_KEY`, `AUTH_OPERATIONS_CARDS_KEY`, `AUTH_OPERATIONS_TESTS_KEY`, or `AUTH_OPERATIONS_EXAMS_KEY`, and NO bearer. Issuer cannot authorize; an owner cannot issue. Auth keys are pairwise distinct and differ from existing `AUTH_INTERNAL_KEY`. Native incoming keys are `CORE_OPERATION_DELIVERY_KEY`, `CARDS_OPERATION_DELIVERY_KEY`, `TESTS_OPERATION_DELIVERY_KEY`, `EXAMS_OPERATIONS_DELIVERY_KEY`. Cards→Core uses separate `CARDS_CORE_OPERATION_KEY` / `CORE_CARDS_OPERATION_KEY` (same value only for this dependency). No delivery credential equals any Auth issuer/owner credential. A partially configured enabled feature fails startup; absent configuration keeps it disabled.

Auth feature configuration: `AUTH_OPERATIONS_ENABLED=1`; TTL default six hours, configurable `AUTH_OPERATIONS_TTL_SECONDS` from 300 to 86400. Credential provisioning has one writer (BFF/Auth), private files, no printed secrets or existing key rotation.

## Primitive types and strict JSON

Metadata numeric IDs/revisions are canonical positive decimal **strings**, `[1-9][0-9]*`, at most signed int64. Native business DTO integers stay integers. UUID, 24hex external test ID, 32hex session/job/grant ID and 64hex SHA256 are distinct validated types. Nullable fields below are explicitly present with JSON null, not a fabricated revision/session. All objects reject unknown/wrong-case/duplicate/escaped duplicate keys recursively. Arrays preserve order except explicitly sorted division IDs. Limits: issue JSON 32 KiB, authorize 4 KiB, at most 1000 sorted unique required division IDs. Timestamps are RFC3339 UTC; persisted timestamps normalize to microseconds before hashing/storage.

## Issue: POST /internal/auth/operation-grants

Live bearer + issuer credential + canonical UUID `Idempotency-Key`. Issuer reserves the job and immutable plan before issue; no Rabbit enqueue until the grant is durably bound. Auth derives initiator/org/selected division from live bearer; issuer does not choose actor/scope. Only admin with actual organization_admin or staff_admin with live permissions may use this lane.

Exact request shape (nulls included; sample uses users.legacy):

```json
{
  "contract_version":1,
  "upload_id":"<32hex>",
  "upload_session_id":"<32hex>",
  "job_id":"<32hex>",
  "coordinator_command_key":"<original Uploads HTTP command key>",
  "type":"users.legacy",
  "owner":"core",
  "native_command_key":"<UUID>",
  "owner_session_id":"123",
  "upload_preview_revision":"3",
  "owner_preview_revision":"2",
  "target_revision":null,
  "target":{"kind":"division_users","division_id":"1001"},
  "source_sha256":"<64hex original upload bytes>",
  "owner_proof":{
    "scheme":"core.legacy.snapshot.v1",
    "validation_sha256":"<64hex owner snapshot>",
    "input_sha256":"<64hex native stored input>",
    "mapping_sha256":null
  },
  "owner_request_sha256":"<64hex native request/receipt hash>",
  "apply_wire_body_sha256":"<64hex exact actual commit bytes>",
  "preview_wire_body_sha256":null,
  "upload_expires_at":"2030-01-01T12:00:00Z",
  "owner_preview_expires_at":"2030-01-01T12:00:00Z",
  "required_division_ids":["1001"]
}
```

Target is a strict discriminated object; irrelevant properties are forbidden:

- Core: `{"kind":"division_users","division_id":"<origin>"}`.
- Cards: `{"kind":"existing_theme","direction_id":"<decimal>","theme_id":"<decimal>"}` or `{"kind":"create_theme","direction_id":"<decimal>","name":"<exact trimmed native name>","collision_policy":"reuse_existing"}`. No fabricated future theme ID.
- Tests JSON: `{"kind":"create_tests","direction_id":"<decimal>"}`; native target/session/revision are absent/null, direction remains bound.
- External results: `{"kind":"external_test","test_id":"<24hex>"}`.
- Exams: `{"kind":"exam","exam_id":"<decimal>"}`.

Proof scheme mapping: Core `core.credentials.snapshot.v1` / `core.legacy.snapshot.v1`; Cards `cards.xlsx.v1`; Tests `tests.json.v1` / `tests.external-results.v1`; Exams `exams.import.v1` (owner must ACK exact scheme). `validation_sha256` stores Cards native authorization_proof.sha256, Core owner-computed snapshot SHA, Tests existing preview_hash, or Exams full validation_hash. It is never an Uploads wrapper digest. `input_sha256` is nonnull for Core/Tests, nullable Cards/Exams; `mapping_sha256` is nonnull only Tests external. Owners publish their complete canonical recipes separately; Auth does not reproduce their business hashes.

Original `source_sha256` is Uploads' immutable original file digest. For normalized Core legacy and stateless Tests inputs it is not claimed to be independently reconstructed by the owner. Core legacy creation uses trusted source attestation (`X-CPM-Import-Source-Key` = Core delivery credential and `X-CPM-Import-Source-SHA256`) alongside ordinary bearer; Core stores provenance `uploads-file-v1`, computes its own normalized input/plan SHA, and includes both in its snapshot. Unknown/old source is not backfilled or guessed. Credentials/Cards/Exams can independently hash their received file bytes. Native proof/expiry is read from current owner, never stale masked creation receipt.

`owner_request_sha256` is the owner's existing/versioned native request/receipt hash, distinct from wire SHA. Exact commit bytes are formed once and durably retained before enqueue. Credentials body is exactly `{}` (two bytes). No trailing LF unless that exact representation was initially bound. Do not decode/reencode during delivery. Native key remains unchanged across retries, restart, reauthorization or receipt recovery.

Issue expiry = min(now+configured TTL, upload_expires_at, nonnull owner_preview_expires_at). It is never prolonged by matching issuance replay. Unique issuance key binds issuer+initiator; same canonical issue input returns same grant/expiry, changed input 409. Issue output is the envelope below with mode `apply`.

## Registered native routes

`coordinator_command_key` preserves the original Uploads HTTP command key (`^[a-zA-Z0-9:_-]{8,128}$`); it is not falsely relabelled as a UUID. Auth issuance and native command keys remain canonical UUIDs. Tests `preview_wire_body_sha256` is required nonnull; other types require null. Core/Cards publish owner_request_sha256 with their owner proof/GET; Tests obtains it through the readonly prepare step below.

Only these paths are derived by Auth from type/native IDs/target; issuer supplies no arbitrary method/path/host/headers:

| Type | Native nullable metadata | Apply | Preview | Receipt |
| --- | --- | --- | --- | --- |
| users.credentials | decimal session, owner revision null, target revision null; owner deadline null | POST `/v1/core/divisions/{origin}/imports/{session}/commit` | GET `/v1/core/divisions/{origin}/imports/{session}` | GET `/v1/core/divisions/{origin}/imports/{session}/receipts/{nativeKey}` |
| users.legacy | decimal session, owner revision positive, target revision null | POST `/v1/core/divisions/{origin}/legacy-imports/{session}/commit` | GET same session | GET same session + `/receipts/{nativeKey}` |
| cards.xlsx | 32hex session, owner revision positive, target revision null | POST `/v1/cards/sessions/{session}/commit` | GET same session | GET `/v1/cards/receipts/{nativeKey}` |
| tests.json | owner session/revision/target revision null; owner deadline null | POST `/internal/tests/imports/json/commit` | POST `/internal/tests/imports/json/validate` | POST `/internal/tests/imports/json/receipt` |
| tests.external-results | owner session/revision null, target revision = expected_test_revision | POST `/internal/tests/external-results/commit` | POST `/internal/tests/external-results/validate` | POST `/internal/tests/external-results/receipt` |
| exams.questions / assignments / outside | UUID session, owner revision and exam target revision positive | POST `/v1/exams/{exam}/imports/{kind}/{session}/commit` | GET same session | GET same session + `/receipts/{nativeKey}` |

Core/Cards/Exams receipt endpoints above are readonly. Tests receipt POST uses identical original commit DTO bytes but only looks up historical matching receipt, never falls into apply. All stages carry original native UUID header. Auth receipt stage expects empty body SHA for GET; Tests expects original apply byte SHA. Native owner matches stored receipt hash against owner_request_sha256 plus original identity/session/target/proof. Core credentials' existing creation receipt is NOT an apply receipt and cannot prove completion of this new key.

All routes reject any query including bare `?`, except Exams preview: required canonical `expected_revision={ownerRev}&limit={1..200}&offset={0..5000}`, no duplicates/unknown fields/alternate encodings. URL-encoded canonical sort order exactly as shown; pages must retain same session/target/revision/full hash/state/total, <=25 pages/5000 rows.

Cards dependency: stage `core-direction`, only Cards grant + Auth Core owner credential, native GET `/internal/core/import-directions/{pinnedDirection}`; no body/query. Cards sends grant/job/original native UUID and separate Cards→Core delivery credential. Core independently verifies live original upload.edit+train.edit and exact target direction/org. This narrowly registered dependency is also permitted for Cards receipt-mode grants because native receipt visibility already requires current direction access; failure preserves UNKNOWN, never applies. It does not require editable preview/source lifetime and never reads the full catalog. Response `{status:true,data:{account_id:<int64>,organization_id:<int64>,division_id:<int64>,job_id:"<32hex>",owner_session_id:"<32hex>",direction_id:"<decimal>",active:true}}`. No full catalog/roster/universal Core permission. Existing bearer cards-context stays intact.

## Authorize: POST /internal/auth/operation-grants/{grant}/authorize

Own distinct owner credential, no bearer, no query. Exact request:

```json
{
  "contract_version":1,
  "job_id":"<32hex actual carrier>",
  "stage":"apply",
  "method":"POST",
  "path":"<actual EscapedPath>",
  "raw_query":"",
  "force_query":false,
  "wire_body_sha256":"<SHA256 actual bytes before decode>",
  "native_command_key":"<actual Idempotency-Key UUID>"
}
```

Exact positive response (issue/recovery also return this envelope; actor current, binding immutable):

```json
{
  "status":true,
  "contract_version":1,
  "grant_id":"<32hex>",
  "mode":"apply",
  "expires_at":"<UTC RFC3339>",
  "actor":{
    "id":123,"account_id":123,"role":"staff_admin",
    "organization_id":1001,"division_id":1002,
    "division_ids":[1002],"organization_admin":false,
    "permissions":{"upload":{"view":true,"edit":true},"users":{"view":true,"edit":true}}
  },
  "division_permissions":[{"division_id":1002,"permissions":{"users":{"view":true,"edit":true}}}],
  "binding":{
    "contract_version":1,
    "upload_id":"<32hex>","upload_session_id":"<32hex>","job_id":"<32hex>",
    "coordinator_command_key":"<original Uploads HTTP command key>","type":"users.legacy","owner":"core",
    "native_command_key":"<UUID>","owner_session_id":"123",
    "upload_preview_revision":"3","owner_preview_revision":"2","target_revision":null,
    "target":{"kind":"division_users","division_id":"1002"},
    "source_sha256":"<64hex>",
    "owner_proof":{"scheme":"core.legacy.snapshot.v1","validation_sha256":"<64hex>","input_sha256":"<64hex>","mapping_sha256":null},
    "owner_request_sha256":"<64hex>","apply_wire_body_sha256":"<64hex>","preview_wire_body_sha256":null,
    "upload_expires_at":"<UTC RFC3339>","owner_preview_expires_at":"<UTC RFC3339>",
    "required_division_ids":["1002"]
  }
}
```

Actor `id` is the actual native profile/entity ID, never derived from account_id. Actor ID/account/org/div and division_ids are native JSON int64 values (internal, never browser-transformed). No full_name/login/password/bearer/private rows. Actual Account.LoadAccount + User(originalContext) determine role/admin/permissions. Return current active memberships in original organization as division_permissions, not other organizations. Pin issuance auth_version + original role/profile identity as the operation account-state epoch: Core already increments auth_version for credentials and every block/reactivation in the same transaction, while Auth single logout/context switch/refresh reuse only affect session rows. No new operation_version column is required if Core ACKs this policy. New revoke-all/security reset must bump auth_version. Native owners still enforce business permissions such as Core groups.edit/full organization admin.

Every stage requires current upload.edit plus users.edit/train.edit/tests.edit/test-results.edit/exams.edit. Questions first apply/preview requires exams.edit in every pinned required division; owner compares full current connected union under commit lock. For receipt/replay, owner enforces all CURRENT connected divisions using current returned contexts; old disconnected union does not authorize new writes. Preserve canonical test-results key precedence. Full admin is only actual role=admin + organization_admin, never synthesized.

Authorization checks owner audience/stage, original actor/org/div, job/nativekey, state/TTL/account epoch/current membership/current permissions, derived method/path/query and actual wire SHA. Auth unavailable denies apply. Native owner verifies returned complete binding and its computed current proof under its commit locks. Order is live auth/scope → matching historical receipt → first-apply current source/revisions/full owner proof/domain guards → atomic mutation+receipt+audit. Owner never forwards grant to unrelated dependency routes.

## Readonly preparation before issuance

For stateless Tests only, after explicit user confirmation and durable reservation of original native key/exact commit bytes, Uploads calls bearer-only POST `/internal/tests/imports/json/prepare` or `/internal/tests/external-results/prepare`. These accept SAME exact immutable commit DTO bytes and original UUID header, current origin bearer and distinct `X-CPM-Import-Prepare-Key` = Tests delivery credential; no query, grant carrier or writes. Owner checks current preview/mapping/target revision and returns `{owner_request_sha256:<native commit hash>,owner_proof:<native proof schema>,native_command_key:<same UUID>}`. Uploads copies/normalizes the native quote into issue; it does not duplicate Tests business hashing. These are not grant stages, public BFF routes or arbitrary internal proxies. Lost preparation can be retried read-only; no business receipt is created. Grant worker remains restricted to validate/apply/receipt routes above. Core/Cards owner-published request hash is copied from current native proof instead of reimplemented in Uploads.

## Explicit recovery / revoke

`POST /internal/auth/operation-grants/{grant}/recover`, live origin initiator bearer + issuer key + new issuance UUID, exact body `{}`. It copies original binding/native keys/bytes/proofs without an editable-source prerequisite, produces mode `receipt`, and allows ONLY registered receipt stage (plus the exact `core-direction` dependency for Cards receipts, never apply/preview). TTL configured bounded window; native receipt retention/tombstone/deletion still governs visibility. Can recover an expired/revoked/account-epoch-invalid original only with fresh current permitted initiator; cannot apply, validate preview, renew original or issue arbitrary target. Same recovery issuance returns same new grant/expiry. No automatic recovery issuance or new native key.

`POST /internal/auth/operation-grants/{grant}/revoke`, live origin initiator bearer + issuer key, exact `{}`; repeated revoke returns200, does not undo mutations/delete native receipts. Creator-only visibility for initial release; no extra admin audit override invented.

Expired/revoked/missing grants after possible apply are UNKNOWN, never proof of failure/no changes. Receipt missing/expiry/tombstone also remain UNKNOWN. Active grant redelivery may only replay exact original key/bytes; Uploads delivery with previous executing/outcome_unknown selects receipt-only, never calls apply again. If a future explicit apply reauthorization is needed, it is a separate contracted action with original keys/bytes/source/preview still valid; it is NOT included in this initial recovery endpoint.

Auth errors:401 invalid_operation_credential;403 operation_access_denied;404 operation_not_found (foreign/missing uniform);409 operation_request_mismatch or operation_issuance_conflict;410 operation_expired or operation_revoked;503 authorization_unavailable. All use existing `{status:false,error:"..."}` envelope, no leaked binding. Owners preserve unknown in worker/UI rather than translating authorization failures into no-apply.

Initial allowlist is eight types above. `cards.transform`, Homework/PDF/Attendance lifecycle, arbitrary routes, production, load/CI-CD/camera and refactoring existing Ratings/Homework/Calculate execution mechanisms are excluded.

## Revision 2 ACK matrix

- Core: exact carrier/derived routes, actual profile identity, existing auth_version security epoch coverage, own source attestation/proof/request hash and readonly apply receipts; Core writer in BFF chat.
- Cards: all-stage UUID, normalized owner proof/native published request hash, exact Core dependency including receipt-mode visibility.
- Tests: exact stateless nulls, readonly native prepare quotes, strict unchanged native DTO/bytes, receipt-only lookup and fresh live permissions.
- Exams: plural EXAMS_OPERATIONS_DELIVERY_KEY, UUID on every read, exact canonical pagination/hash/target revision/receipt path and current connected scopes.
- Uploads: immutable plan before issue/queue, actual coordinator HTTP key, eight native type bindings, copied owner request hash and explicit receipt-only recovery.
- Main coordinator: consistency ACK only; native ACKs above remain separate from source checks/runtime/E2E/general QA.

Original user consent is current. Uploads briefly retransmitted a historical stop; it corrected the chronology and all owners resumed. There is no new human pause. Existing Cards/Tests/Exams/BFF/Core runtimes, secrets and schema remain unchanged at protocol freeze.
