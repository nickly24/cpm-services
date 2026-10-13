# Core owner contract: import operation grants v1

Source implementation against [frozen Auth revision 2](../../auth/docs/import-operation-grants-v1.md), 9 October 2026. This document is not runtime/DDL readiness or acceptance evidence. Migration `007_core_import_operation_proofs.sql` is additive, has no source backfill, and must be applied in the coordinated deployment window before the new Core image. Existing ordinary bearer routes keep their native permissions and recovery semantics.

## Published proof and request hash

Current native credentials session GET returns positive numeric `id`, `organization_id`, `division_id` and original account `actor_id` from its scoped stored row. These are additive explicit origin fields, never invented from the caller's profile or another creator. Current native session GET adds these proof fields:

```json
{
  "authorization_proof": {
    "scheme": "core.credentials.snapshot.v1",
    "validation_sha256": "<64 lowercase hex>",
    "input_sha256": "<64 lowercase hex>",
    "mapping_sha256": null
  },
  "source_sha256": "<64 lowercase hex>",
  "source_provenance": "core-file-v1",
  "owner_request_sha256": "<64 lowercase hex>"
}
```

Credentials additionally publishes `owner_preview_expires_at:null`; its existing native session has neither revision nor deadline. Legacy keeps its native positive revision and original 24-hour `expires_at`; proof scheme is `core.legacy.snapshot.v1`, provenance `uploads-file-v1`. An old/unknown source or non-editable session publishes null authorization proof; no SHA or source is guessed/backfilled. Credential GET calculates private input digest before removing passwords and never returns password hashes.

Credentials hashes the received CSV/XLSX file-part bytes before parsing: this SHA is independent of multipart boundary, filename and parsed rows. The file is bounded at 8 MiB and remains in memory. Legacy receives normalized JSON rather than the original workbook, so creation requires ordinary live bearer plus exactly one `X-CPM-Import-Source-Key` matching `CORE_OPERATION_DELIVERY_KEY` and one lowercase `X-CPM-Import-Source-SHA256`. This is authenticated Uploads source attestation, not a claim that Core reconstructed original XLSX bytes. Source metadata is inserted in the same native preview transaction. Ordinary legacy creation without this attestation still works, but cannot enable a new grant.

Creation receipt replay preserves original source. Changed known source/provenance conflicts with the same command key. A pre-migration credential creation receipt still recovers its ordinary bearer session without source backfill; the recovered session has no grant proof. Attestation cannot retroactively relabel a legacy session with unknown source.

All proof hashes use SHA256, lowercase hex, and Go `encoding/json.Marshal` on the exact typed structures below, without LF. This is a versioned Go-JSON recipe, not RFC8785. Struct field order is declaration order, map keys are sorted by Go JSON, UTF-8 is not normalized to NFC, HTML `<`, `>` and `&` are escaped, optional pointer values remain null, and arrays preserve order; null and empty collections are distinct. Persisted MySQL JSON key ordering is irrelevant because native input is decoded to its typed DTO before hashing.

`input_sha256`:

- Credentials: `SHA256(json.Marshal(ImportDTO{Users:storedUsers}))`, including the stored salted PBKDF2 password hashes. This is not the plaintext credential-preview receipt digest, the original file digest or the masked public GET.
- Legacy: `SHA256(json.Marshal(LegacyImportDTO{Rows:storedNormalizedRows}))`, including source row order and all five normalized cell fields.

Internal plan SHA:

- Credentials: the same private input SHA; existing native user/group/school/role/login guards are rerun in the application transaction.
- Legacy: unchanged `legacyCommandHash("plan",session,storedPreview)`; current matching plan is recomputed and compared under the existing division/organization/session locks.

`validation_sha256` is SHA256 of the following ordered snapshot structure; every field is present:

```json
{
  "contract_version":1,
  "scheme":"core.credentials.snapshot.v1",
  "type":"users.credentials",
  "account_id":"<original account ID>",
  "organization_id":"<original organization ID>",
  "division_id":"<original division ID>",
  "owner_session_id":"<numeric native session ID>",
  "owner_preview_revision":null,
  "source_sha256":"<original source SHA>",
  "source_provenance":"core-file-v1",
  "input_sha256":"<typed private input SHA>",
  "plan_sha256":"<native plan SHA>",
  "owner_preview_expires_at":null
}
```

For legacy, scheme/type/provenance change as described above; revision is a canonical positive decimal string and deadline is exactly UTC `YYYY-MM-DDTHH:MM:SS.ffffffZ`, normalized to microseconds before storage/hash. All identity/session IDs are positive signed-int64 decimal strings. Account ID is distinct from profile ID. The validation snapshot does not contain plaintext/password hashes or private rows; it contains their owner-computed input digest only.

Published `owner_request_sha256` uses the existing native wrapper order and capitalization:

- Credentials: `SHA256(json.Marshal({Operation:"credential_commit",Session:n,Payload:{}}))`.
- Legacy: `SHA256(json.Marshal({Operation:"commit",Session:n,Payload:{expected_revision:N}}))`.

Native `Session` and `expected_revision` remain JSON integers. Request hashes are distinct from actual wire SHA. Credentials grant body is exactly `{}`; legacy exact original bytes are retained by the coordinator. Completed legacy revision becomes `N+1`, but native request hash and original command body retain `N`.

## Carrier and permissions

Core uses `AUTH_OPERATIONS_CORE_KEY` only for its direct Auth authorize request. Incoming Uploads delivery uses separate `CORE_OPERATION_DELIVERY_KEY`; Cards dependency uses separate `CORE_CARDS_OPERATION_KEY`. All three must be at least 32 bytes, distinct from each other and existing `AUTH_INTERNAL_KEY`; partial configuration fails startup. Absent configuration leaves the carrier disabled. Auth/provisioning owns cross-service key distinctness and does not rotate existing credentials.

All native operation stages require exactly one grant/stage/job/delivery-key header and original UUID `Idempotency-Key`, including reads. Mixed bearer/grant identities, partial/duplicate carriers, browser cookies/origin, selector headers, unknown operation headers, encoded/dot paths, any query including bare `?`, and GET bodies are rejected. The ordinary bearer source-attestation headers belong only to creation, not grant delivery.

Core captures actual bytes before decode and sends exact method/EscapedPath/raw_query/force_query/wire SHA/native UUID to Auth. Strict response decoding rejects wrong-case, unknown/missing and recursively duplicate/escaped duplicate fields. Auth failure never falls back to user bearer, service admin or cached permissions. The actual profile ID stays `actor.id`; SQL ownership uses `actor.account_id`. Only actual role `admin` plus `organization_admin:true` gains full administrator behavior. Native upload.edit/users.edit and group/full-admin domain checks remain in effect.

Legacy reauthorizes the same saved request after potentially slow password hashing and immediately before its transaction. Immutable binding and original identity must remain equal; fresh permissions replace earlier permissions. First apply compares native revision/deadline, exact native request hash, original source and full current proof under native locks. Current preview reads also compare their full native proof; historical receipt reads do not load/revalidate editable previews.

## Read-only receipt routes

- `GET /v1/core/divisions/{division}/imports/{session}/receipts/{nativeUUID}`
- `GET /v1/core/divisions/{division}/legacy-imports/{session}/receipts/{nativeUUID}`

Both return the same successful `data` as the original commit, with the ordinary `{status:true,data:...}` envelope. The new routes require current upload.edit/users.edit and original initiator ownership. They return 404 when absent/foreign or for another type/session, and never call apply, password hashing, current preview/domain validation, audit or any SQL write. Grant reads additionally compare stored native request hash, source SHA and complete historical proof with the immutable authorized binding. Current editable-preview expiration/revision/source deletion does not change an already stored receipt.

The existing `/imports/receipts/{creationUUID}` remains a separate credential creation receipt. It is not an apply receipt or proof. New historical apply receipts store only the non-secret native response, native request hash, source hash and proof, atomically with native application/session completion/audit. Grant application cannot claim another command's already-completed credential session as its own receipt. A missing receipt remains UNKNOWN to the coordinator; no new native UUID or apply fallback is allowed.

Legacy lock order remains division then active organization then native session; credentials locks division then native session. Matching historical receipt precedes new-apply revision/source/plan/domain checks. Native bearer matching legacy receipts retain their existing early-replay semantics; new grant replay also checks the computed original commit hash before reading historical receipt.

## Cards dependency

`GET /internal/core/import-directions/{pinnedDirection}` accepts only stage `core-direction`, a Cards grant, original native UUID and Cards→Core delivery credential. Core independently authorizes with its own Auth credential, verifies current upload.edit/train.edit and the exact native Cards target, then reads only that active direction in the original active org/div. Receipt-mode grants may use this dependency without reviving expired source or editable preview.

Exact response:

```json
{"status":true,"data":{"account_id":123,"organization_id":1001,"division_id":1002,"job_id":"<32hex>","owner_session_id":"<32hex>","direction_id":"<canonical decimal>","active":true}}
```

It exposes no full direction catalog, names, users or memberships. Existing bearer `/internal/core/cards-context` is unchanged. `cards.transform` and unrelated dependency routes are excluded.

## Security epoch and evidence

Background grants reuse existing `auth_account_state.auth_version` plus original role/profile identity. Credentials/login/password changes and every block/reactivation increment auth_version in the same transaction. Staff active-state changes also preserve the legacy profile session_version increment. Future security-reset/revoke-all/identity-changing handlers must use `bumpAccountAuthVersion` in their own atomic mutation; no new endpoint or epoch column is introduced. Single-session logout, refresh reuse and cabinet switching remain session-only. Membership/permissions and active domain scope are checked live on every Auth delivery.

Meaningful tests live in `import_operation_proofs_test.go` and `operation_grants_test.go`: source/hash distinctions, private input masking, row order/null/large IDs/microseconds, old bearer recovery, source mismatch/no backfill, strict carriers/Auth contexts, native request mismatch, historical read-only receipts, separate Cards dependency, authorization withdrawal during hashing, source mismatch under native locks and apply+receipt+audit rollback. Actual HTTP/Rabbit/restart acceptance remains pending the coordinated runtime window.

## Coordinated Dev enablement helpers

`dev/compose.core-operations.yaml` is an optional Core-only overlay. It passes exactly `AUTH_OPERATIONS_CORE_KEY`, `CORE_OPERATION_DELIVERY_KEY`, and `CORE_CARDS_OPERATION_KEY` to Core. It does not enable Auth or alter another service. `dev/core-operations up --image sha256:<frozen-image-id>` prepares without reading private files, contacting Docker, or changing runtime. The coordinator adds `--run` only after the Core window ACKs and migration readiness. Apply uses the pinned local immutable image, `--no-deps --no-build --pull never`, and targets only `core`; there is no build, key generation, configuration dump, or other-service lifecycle command.

The sole provisioner owns `dev/local/import-operation-credentials.env`. The reader requires a regular non-symlink file, current-user ownership, exact mode0600, bounded size, strict dotenv names without interpolation, no duplicate/unknown keys and no environment override. Its ten canonical credential values must be distinct: five Auth issuer/owner keys, four native delivery keys (including plural `EXAMS_OPERATIONS_DELIVERY_KEY`), and `CARDS_CORE_OPERATION_KEY`. Optional `CORE_CARDS_OPERATION_KEY` must equal this last dependency credential; when absent the reader derives this alias for Core. None may equal existing `AUTH_INTERNAL_KEY`. Only Core's three values reach its deployment subprocess. Error output contains codes, never raw Docker/configuration output or credential values. The helper does not provision or rotate any key.

`services/core/scripts/import_operations_setup.py check|apply` also prepares without DB/private-file access unless explicitly passed `--run`. Only fixed loopback port13306/database`cpm_dev` is supported. An advisory lock protects this runner. It checks native prerequisites and both existing 007 tables before any DDL: complete signed column types/nullability/defaults, ascii_bin digest/UUID fields, InnoDB base tables, complete indexes without prefixes, original local composite foreign keys and no cascade. Unknown existing schemas fail without alterations. Apply creates only missing 007 tables, validates them afterwards, and merges only their two names into the extension manifest under its file lock; reapplication creates nothing. No seeds, old migrations, legacy 005 mapping/backfill, row updates or runtime controls are invoked. MySQL DDL is not transactional: if interrupted between the two additive tables, the same guarded runner resumes only the missing table.

Isolated helper regressions in `import_operations_helpers_test.py` use synthetic temporary mode0600 files and fake schema metadata, with no actual Dev credentials/DB/Docker. They cover exact credential subset/alias, duplicates/overrides/missing keys/unsafe files, no-run behavior, immutable Core-only execution arguments, idempotent/partial migration, foreign/cascade/prefix/unknown-schema rejection before DDL, and extension-manifest preservation. The source freeze manifest is `dev/local/core-import-operations-source-freeze.json`; actual migration, provisioning, HTTP/Rabbit and coordinated restart acceptance remain pending.
