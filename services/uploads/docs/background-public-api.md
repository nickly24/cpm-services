# Uploads background imports: public API v1

Wire dependency: frozen [Auth protocol](../../auth/docs/import-operation-grants-v1.md) revision 2.
Human implementation consent was directly verified in main message
`01a12221-7d06-73f3-8f27-8c05f352977a`. The earlier analytics stop is historical,
superseded by resume and this consent. Source work continues; this document does
not announce deployed execution or general QA readiness.

Chronology checkpoint: historical stop `01a120f8-d08b-7af3-864a-a057a18ebc4d`
at Unix `1791554539`, resume `1791561203`, then direct consent above at
`1791573982`. Latest owner turns `01a1224c-7506-7880-b18c-cb1f27690f93`,
`01a12251-03ae-7ff2-ac5b-4d8cc96f2c4d` and
`01a12251-a9f6-7280-9d2c-37510a0af95c` contain peer/tool deliveries, no new
human message. Their mistaken pause summaries are retracted after direct main
and owner history verification. A thread preview is not the latest instruction.

## Confirmation

With `UPLOADS_OPERATIONS_ENABLED=1`, authenticated
`POST /v1/uploads/divisions/{division}/sessions/{session}/commit` requires the
original `Idempotency-Key` and strict JSON:

```json
{"expected_revision":3,"confirmed":true}
```

The UI first shows the original organization/division, type, target and preview
revision. A user confirmation reserves one scoped job, stable native UUID and
exact immutable bytes. Native readonly preparation/proof and Auth issuance follow
outside SQL locks, using the current bearer only in this HTTP request. Binding
the grant and publishing intent to Rabbit occur in one SQL transaction. No user
bearer is persisted. The response is `202 {status:true,data:<Job>}`; 202 alone is
not completion or evidence that rows were applied.

Job includes `authorization:{bound:boolean,mode:"apply"|"receipt",expires_at?:UTC}`.
`bound` is true only when the original grant has been durably bound in the same
transaction as the Rabbit publishing intent. No grant ID or credential is exposed.
The UI preserves actor-namespaced scope/key/revision/intent metadata across reload
and tabs on every `authorizing` or `authorization_unknown` 202 and while bound=false.
Neither a positive 202 nor a completed action receipt alone clears this metadata.
It can clear the pending confirmation when a matching scoped Job proves bound=true;
retain separate unknown-outcome/recovery metadata until a matching native result.

Same command/body returns the original job. While authorization is incomplete,
the same original command may resume readonly preparation/issuance with the
exact original binding, UUID and bytes. A different key cannot replace the intent.
Changed command body/revision is 409. Historical technical jobs with no execution
plan never gain authorization from a replay; they require a fresh preview/intent.
The retained original credentials unknown case must never be resent.

With the feature disabled, existing technical `{expected_revision:N}` behavior
remains and the catalog reports `commit:false/awaiting_authorization`. This is
backward compatibility for transport checks, not background-apply readiness.
Cards transform, Homework PDF and Attendance attachments are excluded.

## Job actions and capabilities

All paths below require current authentication in the original actor/org/div
scope. Actions require current upload.edit and the corresponding subject edit
permission. Missing capabilities mean no available action; zero attempts/progress
do not imply safe retry.

| Operation | Path suffix | Body | Receipt action |
|---|---|---|---|
| Retry active undispatched delivery | `/jobs/{job}/retry` | `{}` | `job.retry:{job}` |
| Authorize readonly receipt recovery | `/jobs/{job}/recover` | `{"confirmed":true}` | `job.recover:{job}` |
| Revoke authorization | `/jobs/{job}/revoke` | `{"confirmed":true}` | `job.revoke:{job}` |

Each is POST with its original HTTP Idempotency-Key. Action receipts bind the same
request hash and resource job ID; successful scheduling/revoke completes that
action's receipt. Matching HTTP replay does not enqueue another generation.
Recover reserves a distinct Auth issuance UUID derived from job+original recovery
HTTP key; it copies the original grant binding and native UUID/bytes. Its new mode
permits receipt reads only, never a second apply. Revoke does not undo writes.
Successful recover/retry responds 202 with the same Job; revoke responds 200.

Job adds `capabilities:{retry_delivery:boolean,recover_receipt:boolean,
revoke_authorization:boolean,retry_confirmation:boolean}`. All are computed from
current permissions, original scope, private execution marker, grant mode/expiry
and actual job status. `retry_confirmation` means repeating the original pending
confirmation command with its original key; it is not `/jobs/{job}/retry` and never
uses a newly generated key. The UI must retain the pending command until a matching
positive result/receipt is established.

## Status semantics

| Status | Meaning and permitted UI action |
|---|---|
| authorizing | Intent/bytes reserved; no Rabbit application yet. Poll. |
| authorization_unknown | Preparation/issuance result not bound. Repeat original confirmation only if capability permits; no generic apply retry. |
| queued / running | Authorized delivery / execution; poll, revoke if permitted. |
| outcome_unknown | Apply may have happened; readonly receipt recovery only. |
| access_required | Current authorization denied; no promise that changes are absent. Restore access, then readonly recovery. |
| authorization_expired / revoked | No new application; readonly recovery with fresh explicit confirmation if permitted. |
| completed | Owner-confirmed result persisted. Read protected report. |
| failed / cancelled / awaiting_authorization | Preserve historical semantics; no newly inferred background authorization. |

Before the first native call the worker durably saves its execution marker. A
restart/redelivery after that marker selects receipt, never apply, even when the
original request may not have reached the owner. Missing/expired/deleted receipts
remain UNKNOWN. Auth failure or revoke after possible execution is not no-change.
An already authorized atomic owner commit may finish concurrently with revoke.

Public jobs/receipts/reports never expose grant binding, issuer/native credentials,
private execution bytes or a browser bearer. Catalog commit becomes available only
for the eight registered types after the configured dev protocol is deployed and
verified. This source contract is not evidence of such readiness.
