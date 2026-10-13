# Exams background import contract

9 October 2026. Human authorization: main user message
`01a12221-7d06-73f3-8f27-8c05f352977a`. Native Exams is the only writer of
Exams code. Auth, Uploads and shared runtime changes remain with their owners.
Exams ACKs the shared wire candidate v1 revision 2 in
`services/auth/docs/import-operation-grants-v1.md`. Local enforcement compiles;
shared runtime enablement awaits final owner freeze and coordinated checks.
This does not announce general QA release.

## Immutable command

Types: `exams.questions`, `exams.assignments`, `exams.outside`.
Target: canonical positive decimal exam ID. Native session and command key:
canonical lowercase UUID. Owner preview revision and target exam revision:
positive int64. Preview publishes `exam_revision` as the target revision.

Native apply is exactly `POST /v1/exams/{exam}/imports/{kind}/{session}/commit`,
without query, with the existing strict JSON `{"expected_revision":N}`.
No extra grant or audit fields are added to this DTO. Uploads retains the actual
wire bytes and UUID once, including any newline, before issuance/delivery.

`source_sha256` is native `source_hash`. Empty legacy source hashes cannot
authorize a new apply. `validation_sha256` is native `validation_hash`: canonical
JSON of **all** public rows, including errors and excluded rows, preserving JSON
number precision. Private privilege baseline fields are excluded. First apply
compares revision, source and freshly validated full hash under exam and session
locks. It also compares the pinned target exam revision; a content/config change
cannot silently alter a confirmed import.

Native request hash v1 is SHA256 of these concatenated bytes:

```
POST {exactCommitPath}? {originDivisionDecimal} {actualCommitBodyBytes}
```

There is one ASCII space between the shown components; there is no implied LF.
This hash is distinct from the wire body digest. Auth must pin it as an owner
proof so readonly recovery can match the existing native receipt.

## Carrier and live context

Agreed header names:
`X-CPM-Operation-Grant` (32 lowercase hex),
`X-CPM-Operation-Stage` (`apply`, `preview`, `receipt`),
`X-CPM-Operation-Job` (32 lowercase hex),
`X-CPM-Operation-Service-Key` (distinct Uploads-to-Exams delivery credential).
Bearer and grant identities cannot be mixed. Browser calls do not forward grants
or credentials. Exams itself authorizes every stage through Auth using its own
`AUTH_OPERATIONS_EXAMS_KEY`, never Uploads' issuer credential.
Incoming delivery credential is `EXAMS_OPERATIONS_DELIVERY_KEY` (plural).
The Docker wrapper optionally loads the sole Auth writer's private
`dev/local/import-operation-credentials.env` (0600, regular file), rejects shell
overrides and never provisions or prints credentials. Missing overlay disables
the lane. Deployment still requires Auth READY and a coordinated exact window.
Every stage, including both reads, carries the original `Idempotency-Key` UUID.
The coordinator key is the actual original Uploads key, 8..128 ASCII characters
from letters, digits, colon, underscore and hyphen; it is not a synthetic UUID.

Positive Auth context must supply the actual account/org/origin division, role,
explicit organization-admin flag, current scoped permissions and division
participations. No synthetic administrator is used. All stages require current
`upload.edit` and `exams.edit`. Questions additionally requires edit authority for
all currently connected divisions and the existing owner-division guard.
Participation IDs alone are not proof of edit authority. First apply compares
the complete pinned connection set with the locked exam. Historical replay uses
current edit authority over current connections, without demanding the old
target revision still be current.

Core exam-references retains its existing service-key-only bounded DTO; a grant
does not travel to Core or become a generic dependency credential.

## Read, replay and retention

Preview: exact session GET, limit 1..200, offset 0..5000, required pinned
`expected_revision`; no body. All three query parameters are required in canonical
URL-encoded order (`expected_revision`, `limit`, `offset`). Every page carries the same
source/full validation hash, revision, state, total and target exam revision.
Worker compares those fields across all pages. Each read is freshly authorized.

Receipt: proposed readonly
`GET /v1/exams/{exam}/imports/{kind}/{session}/receipts/{nativeCommandKey}`.
No query (including bare `?`) or body; native key header must match the path.
Receipt stage binds the
same original job, actor, scope, target/session/key and native request hash.
It only reads an existing matching receipt: no session creation or mutation.
It can recover an existing receipt after editable preview expiry, but cannot
revive a preview or tombstone. Missing receipt is an unknown outcome rather than
proof that no application happened. Deleted exam or lost current permissions
require access recovery; neither permits a second apply.

Apply replay checks live authorization and scope, then matching historical
receipt/input, before revalidating newly applied rows as possible duplicates.
The session lifecycle check for ordinary apply remains. New background commits
record safe historical proof fields in their result for recovery binding.

Editable preview is 72 hours; grant expiry cannot exceed preview expiry.
Committed session and full receipt last 7 days. Logical expiry is 410; permanent
command tombstones continue to prevent a fresh command under the old key.
Physical session purge remains 404. Recovery is bounded by full receipt
retention and never uses a fresh command key.

## Verification

Owner tests cover full-row proof, strict carrier/route/query/body binding,
live permissions across shared divisions, historical replay, CAS and source
changes, exact bytes, unknown receipt, expiry/revoke/Auth failure, and durable
receipts with isolated local MySQL tables. Joint tests with Uploads prove the
confirmed UI-to-Rabbit-to-owner path, duplicates and lost replies. Runtime
changes require a separate exact window and affected owners' ACKs.
