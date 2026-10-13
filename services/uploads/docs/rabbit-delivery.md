# Administrative import delivery

Uploads administrative execution is delivered through RabbitMQ. Homework PDF
delivery remains in Homework/File-work, and Attendance remains the owner of zap
attachments. Uploads does not declare or consume their queues.

Confirmed operation execution now exists in source under the disabled-by-default
`UPLOADS_OPERATIONS_ENABLED` flag and frozen Auth revision 2. See
[public protocol](background-public-api.md). Historical transport proofs below
remain unchanged; they do not prove deployment of the new grant/receipt worker.
Before first owner apply a durable marker is saved. All later attempts for that
job use readonly native receipt routes with the original UUID and exact bytes.

Own durable exchange: `cpm.uploads.v1` (direct). Own quorum queues:

| Queue | Routing key | Purpose |
| --- | --- | --- |
| `uploads.import.execute.v1` | `uploads.import.execute.v1` | Single active consumer; manual ACK, prefetch 1 |
| `uploads.import.retry.v1` | `uploads.import.retry.v1` | Fixed 5s TTL; at-least-once dead-letter transfer back to execution |
| `uploads.import.dead.v1` | `uploads.import.dead.v1` | Invalid transport diagnostics or exhausted delivery references |

The execution/retry queues use quorum at-least-once dead lettering with
`reject-publish` overflow. No global broker configuration, other topology, or
broker restart is required. This is a local dev topology, not a measured capacity
claim or a production deployment.

## Persistence and ACK

`uploads_jobs_v1` is the job journal: pinned organization/division/actor, owner,
status, attempts, progress, error and report. It is not polled for execution.
`uploads_outbox_v1` is a transactional delivery proof; its publisher polls only
unsent messages and confirms persistent mandatory Rabbit publishes.
`uploads_inbox_v1` records delivery hashes, leases and completed acknowledgments.

Initial enqueue and an explicit authorized retry call `EnqueueImport` in the
same SQL transaction as the job transition. A generation distinguishes a new
manual retry from an older in-flight envelope. Automatic retries keep the same
generation and have a new transport ID. The owner commit key must remain stable
for the job, including after restart and response loss.

The envelope carries only pinned IDs, source SHA-256, preview/owner revisions and
the stable owner session reference. It carries no bearer, credentials, input
rows, names or file bytes. A consumer checks the complete envelope against its
recorded outbox and current scoped job/session before execution. Client-supplied
scope in Rabbit is not authorization.

`ExecuteImport(ctx, jobID, Scope) error` returns nil only after a persisted
completed, failed, awaiting-authorization or retry outcome. The broker checks
that outcome before marking the inbox completed and ACKing. If an executor
cannot save its outcome, re-delivery is persisted before ACK; if persistence
fails, the channel closes without ACK. The executor must respect its 120s context
deadline. Its idempotent owner operation is still required: the system does not
claim distributed exactly-once execution.

Publisher confirmation loss republishes the same message ID. Consumption after
ACK loss reads the completed inbox and does not invoke the executor again.
Unrecognized payloads are not copied to the DLQ; a durable diagnostic includes
only a body hash, reason, producer and timestamp.

Recovery checks expired job leases and delivery proofs missing for 15 minutes;
it republishes through Rabbit and never calls the executor directly. Retention
must preserve outbox/inbox records for the full command replay window. The
current implementation does not delete either ledger.

## Historical disabled execution mode

The transport acceptance below predates the later direct human consent and
frozen Auth revision 2 source implementation. In that unchanged disabled runtime,
the original browser bearer is not retained in memory or SQL; the real server
executor persists `awaiting_authorization` with
`background_authorization_pending`, and performs no domain commit. This is a
visible incomplete import capability; Rabbit delivery alone does not prove a
working end-to-end domain import. Preview/edit/read continue through the owner's
existing authenticated HTTP contract.

The eventual executor must check current Auth rights and the pinned scope,
recover the owner's existing receipt before an uncertain repeated application,
and use the owner's atomic validation/commit. Service keys alone are not actor
authorization.

## Transport verification

On 2026-10-09, unit tests passed under `go test -race ./...`; `go vet ./...`
also passed. The opt-in
`TestRealBrokerDeliveryRecoveryAndScope` passed against local MySQL/Rabbit with
`-race` (6.51s). It used only random `uploads.test.*` queues/exchange and synthetic
Uploads rows, and removed them afterwards. It verified persisted waiting status
with zero owner HTTP requests, ACK-loss dedupe, a lost response after a persisted
outcome, recovery of expired job/inbox leases, a new broker session consuming a
job queued while stopped, forged org/division/actor/source references, and
sanitized invalid-message diagnostics.

That transport test uses a small executor callback to save synthetic transport
outcomes. It is not a domain-owner integration test and does not prove the blocked
Auth delegation or real administrative apply. The current real server callback
was separately exercised in the same suite to assert its honest waiting status
and absence of owner calls.

Live opt-in: `UPLOADS_DELIVERY_INTEGRATION=true go test -race ./internal/uploads
-run '^TestRealBrokerDeliveryRecoveryAndScope$' -count=1 -v`, with the generated
private local environment loaded and the ordinary Uploads daemon stopped. The
normal test suite skips that test; never run it concurrently with another
Uploads outbox publisher on the shared dev ledger.
