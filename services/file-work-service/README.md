# file-work-service

Go 1.27.1 worker for homework PDF validation and compression. It consumes RabbitMQ messages, reads a staging object from S3, uploads a processed object to S3, and publishes the result. It has no MySQL driver and never writes homework business records. The homework service owns submissions, generations, deadlines, review, grades, and file references.

## Processing

1. Decode a strict, bounded version-1 command and validate organization/division S3 prefixes and policy ceilings.
2. Ask homework to claim the exact job generation. Terminal/stale jobs return `run:false`; an active lease returns HTTP 409.
3. Download with `If-Match` for the recorded S3 ETag. Check both advertised and streamed size.
4. Verify PDF signature; qpdf rejects encrypted, damaged, empty, over-page-limit, scripted, embedded-file, XFA, multimedia, and remote-action content before Ghostscript sees the PDF.
5. Ghostscript `pdfwrite` downsamples photographic images using bicubic resampling and JPEG recompression. The policy supplies DPI and JPEG quality; vector text remains text. Defaults supplied by homework are 120 DPI and JPEG quality 75. It preserves page orientation and all pages, with an explicit page-count check.
6. qpdf rewrites and linearizes the result after removing document information and XMP metadata references. If the image rewrite increases size, compare a metadata-clean structural rewrite of the verified input and select the smaller output. A tiny text-only PDF can still grow: metadata cleaning and PDF rewriting have overhead; universal size reduction is not guaranteed.
7. Check output size and PDF validity again, hash SHA-256, and upload to the deterministic processed key with integrity metadata.
8. Publish a mandatory persistent completion message and wait for a publisher confirm before acknowledging the input.

The bytes remain in S3. Homework receives the processed object's key, size, pages, SHA-256, identity, generation, and lease token through RabbitMQ, verifies the result, and commits its own file/business state. User-facing filenames never appear in worker logs.

## Contract

Exchange `cpm.events` is durable/topic. Commands use routing key `files.pdf.process.requested.v1`, queue `filework.pdf.v1`. Results use `homework.pdf.completed.v1`, queue `homework.pdf.results.v1`.

All messages use `{message_id,message_type,schema_version:1,occurred_at,producer,correlation_id,payload}`. Command producer is `homework`; result producer is `file-work-service`. `occurred_at` is an RFC3339 UTC timestamp. Unknown fields, malformed envelopes, and payloads above 32 KiB go to `filework.pdf.dlq.v1`.

Command payload:

```json
{
  "job_id": "00000000-0000-4000-8000-000000000001",
  "generation": 1,
  "organization_id": 11,
  "division_id": 12,
  "source_key": "homework/11/12/staging/example.pdf",
  "source_etag": "recorded-s3-etag",
  "output_key": "homework/11/12/processed/00000000-0000-4000-8000-000000000001/1.pdf",
  "limits": {
    "max_source_bytes": 10485760,
    "max_output_bytes": 10485760,
    "max_pages": 35,
    "dpi": 120,
    "jpeg_quality": 75
  }
}
```

Limits are an immutable snapshot of the division's database settings captured by homework when processing is requested. The worker does not read or edit settings. Independent resource ceilings are 100 MiB source/output, 200 pages, DPI 72..300, JPEG quality 35..95; persisted policies may be stricter.

Internal homework calls use `X-Service-Key`:

- `POST /internal/homework/file-jobs/{job_id}/claim`, body `{generation}`; response `{status:true,data:{run:true,claim_token:UUID}}` or `{status:true,data:{run:false}}`. Leases last 300 seconds, longer than the worker deadline (maximum 240 seconds).
- `POST /internal/homework/file-jobs/{job_id}/release`, body `{generation,claim_token}`; a stale token cannot release a newer owner. Called after a temporary failure before scheduling its retry.

Result payload has `job_id`, `generation`, `organization_id`, `division_id`, `output_key`, `size_bytes`, `page_count`, `sha256`, `error_code`, `claim_token`. Empty `error_code` is success. A rejection still carries the current claim token, and numeric/hash fields are empty. Result message IDs include job, generation, and claim token to keep deduplication distinct across owners.

Output S3 metadata: `job_id`, `generation`, `organization_id`, `division_id`, `page_count`, `sha256`. Output path is checked exactly; the command cannot redirect the worker to another division or arbitrary bucket/object.

## Delivery and recovery

Prefetch is 1; `x-single-active-consumer` restricts all replicas to one active PDF consumer. Commands are durable/persistent. Temporary work failures get three confirmed republishes through delayed quorum queues (5s, 30s, 120s). Delay queues use at-least-once dead lettering, so an unavailable command destination does not silently discard an expired retry.

An active claim or failed lease release is delayed in `filework.pdf.lease-wait.v1` for 310s. Active claims do not consume processing attempts. Known file-policy rejections publish a terminal result immediately. Exhausted work failures publish `processing_failed`, then a diagnostic command to DLQ. An exhausted claim outage goes to DLQ without a fabricated business completion; homework's queued/expired-job recovery remains responsible for resending it. Invalid commands go directly to DLQ.

No ACK is sent before a result/retry/DLQ publish has been both routed and confirmed. On publish failure, connection loss, or shutdown, the session closes and RabbitMQ redelivers its unacknowledged command. ETag checks, deterministic keys, generation checks, and homework claim tokens support at-least-once delivery. Homework must reject old tokens/generations and queue obsolete output objects for deletion; a late worker cannot reopen a cancelled submission.

## Configuration and runtime

Required: `RABBITMQ_URL`, `HOMEWORK_SERVICE_KEY` (at least 32 bytes), `S3_ENDPOINT_URL`, `S3_BUCKET`, `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY`. No production credentials/defaults are included.

Optional: `LISTEN_ADDR=:8007`, `HOMEWORK_URL=http://homework:8006`, `S3_REGION=us-east-1`, `PDF_TEMP_DIR=/tmp`, `PDF_PROCESS_TIMEOUT_SECONDS=120` (5..240), `QPDF_BINARY=qpdf`, `GHOSTSCRIPT_BINARY=gs`.

`GET /healthz` checks process liveness. `GET /readyz` returns component details and HTTP 200 only when RabbitMQ is ready, qpdf/Ghostscript were present at startup, the last S3 capability check succeeded within 60 seconds, and homework accepted a heartbeat within 30 seconds. Otherwise it returns HTTP 503. Storage starts as `unknown`; a failed check immediately replaces an earlier success. These checks do not assert that every future job will succeed.

Each process creates a new UUID and sends an authenticated heartbeat immediately, then every 10 seconds to `POST /internal/homework/file-work/heartbeat`. The exact version-1 DTO contains `schema_version`, `instance_id`, `observed_at` (UTC), `rabbitmq`, `tools`, `storage:{state,checked_at}`, and `processing` (always a bool). Before any storage check, storage is `{"state":"unknown","checked_at":null}`. No business identifiers, object paths, filenames, or credentials appear in the DTO. A heartbeat has a three-second HTTP deadline, never follows redirects, and counts as accepted only for HTTP 200 with `{"status":true}`.

An independent loop checks S3 immediately, then every 30 seconds using Put/Get/Delete of fresh synthetic bytes at exactly `file-work-health/{instance_id}/probe`. Reads are bounded and compared byte for byte. The returned S3 version, if present, is used for both Get and Delete. Cleanup also runs after failed/ambiguous Put or failed Get, using a separate context so an expired operation cannot prevent deletion. Put/Get together have a five-second deadline; cleanup has three seconds. Any operation, mismatch, or cleanup failure reports `error`; `checked_at` records actual completion. Probe traffic never creates a homework job or touches its objects. Worker credentials therefore also need Put/Get/Delete rights for this isolated health prefix.

The runtime runs as UID/GID 65532, uses a shell-free command runner, Ghostscript SAFER mode, a processing deadline, bounded parser output, Linux address-space/file/CPU limits, and immediate temporary-directory cleanup. Compose should set a read-only root filesystem, 1 GiB memory limit, `pids_limit:64`, a 512 MiB `/tmp` tmpfs, `cap_drop:ALL`, and `no-new-privileges:true`. Processed intermediates are deleted as soon as metadata rewriting finishes. There is no mount of host documents, source secrets, or Docker socket. Review/update PDF parser packages and their security advisories before production release; these controls do not replace parser maintenance. Ghostscript is AGPL/commercial software; its licensing must be reviewed for CPM's intended production deployment.

Verified dev image on 2026-10-07: Debian trixie, Ghostscript `10.05.1~dfsg-1+deb13u2`, qpdf `12.2.0-1`, util-linux `2.41.5-0+deb13u1`. Docker build records installed versions; security updates remain enabled.

## Verification

```sh
go test -race ./...
go vet ./...
docker build --target test -t cpm-file-work-service-test:dev .
docker run --rm --memory=1g --pids-limit=128 cpm-file-work-service-test:dev test -race -p=2 -v ./...
python3 scripts/run_broker_tests.py
```

Host tests cover strict contracts, scope/resource limits, authenticated claim/release, conditional S3 reads, streamed byte limits, integrity upload metadata, temporary cleanup, processing deadlines, terminal generations, confirmed ACK boundaries, bounded retries, lease waiting, and DLQ behavior. Container tests additionally execute actual qpdf/Ghostscript on synthetic photo-heavy/text PDFs and unsafe/corrupt/encrypted/over-limit inputs, check preserved pages and selectable text, and report measured compression. No production PDF, bucket, or database is needed.

`run_broker_tests.py` uses localhost credentials from ignored `dev/.env` without printing them, creates a unique disposable vhost, and deletes only that vhost afterward. It checks actual broker confirms/mandatory returns, persistent body delivery, repeated topology declarations, prefetch and single-active-consumer behavior, a delayed quorum retry, and standby takeover. It does not consume existing development queues.

Monitoring tests additionally cover the exact safe heartbeat payload, accepted/rejected responses, response-size limits, redirect protection, request deadlines, startup/unknown/stale/future readiness, independent heartbeat/probe loops, processing flag reset, exact S3 health paths and versions, wrong/short/long readback, and cleanup after failure/cancellation/timeout. Monitoring does not change consumer topology, retries, claims, or ACK behavior.

Primary implementation references: [Ghostscript pdfwrite](https://ghostscript.readthedocs.io/en/latest/VectorDevices.html), [qpdf JSON v2](https://qpdf.readthedocs.io/en/stable/json.html), [RabbitMQ confirms/mandatory returns](https://www.rabbitmq.com/docs/confirms), [RabbitMQ reliability](https://www.rabbitmq.com/docs/reliability), [Debian Ghostscript security status](https://security-tracker.debian.org/tracker/source-package/ghostscript).
