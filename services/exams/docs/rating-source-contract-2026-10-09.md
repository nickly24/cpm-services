# Exams rating source contract — 2026-10-09

Exams exports subject grades and a scoped catalogue. Ratings/Calculate own the overall formula, participants, publication and stale-state handling. Exams does not start rating jobs automatically.

## Scope and snapshot

`GET /internal/exams/rating-snapshot` requires the existing `X-Service-Key`, `organization_id`, one to 100 unique `division_ids`, `date_from`, `date_to`, and optional RFC3339 `as_of`. The two date aliases `from_date` / `to_date` are accepted when they agree with their canonical fields. Core verifies that the active divisions belong to the organization. Duplicate or unsupported parameters are rejected.

The response envelope is `{status:true,data:{source_revisions,snapshot}}`. `source_revisions` maps each requested division ID to a monotonic integer from `exams_source_v1`. All catalogue documents and these counters are captured in one repeatable-read SQL transaction.

`snapshot` has `source`, `organization_id`, `division_ids`, `date_from`, `date_to`, `as_of`, `source_revision`, `revisions`, `next_exam_start_at`, `exams`, and `results`. The singular `source_revision` is a deterministic fingerprint of the semantic revision vector. The plural `source_revisions` contains the event counters and must not be confused with that fingerprint.

Catalogue entries include `exam_id`, `exam_name`, `division_id`, `direction_id`, `exam_type`, `exam_date`, `start_at`, `exam_revision`, and `division_revision`. The last two are **semantic rating revisions**, independent of public CAS revisions. Result rows retain explicit zero grades and the effective completed attempt/last appeal. A missing result is inferred by joining the catalogue to scoped participants; absence does not erase a published zero.

Classic membership uses the current placement's fixed UTC+03 calendar start date within the inclusive period and `start_at <= as_of`. Outside membership uses the placement date within the period. `as_of` does not reconstruct historical completion/appeal state: grades come from the captured persisted state, matching the source Flask rules. The next future eligible classic start is reported for time-driven invalidation even when no new event arrives.

## Independent versions

| Actual change | Semantic exam revision | Semantic division revision | Event counter |
| --- | --- | --- | --- |
| Effective completion/current appeal/outside result/history removal in A | unchanged | A advances | A advances |
| Placement date/start/config change in A | unchanged | A advances | A advances |
| Shared name or direction | advances | unchanged | every connected placement advances |
| New connection / disconnection / deletion / deep copy | own new identity or changed catalogue | independent placement | affected placements advance |
| Pending votes/readiness/commission/assignment/privilege/training | unchanged | unchanged | unchanged |
| Appeal on a superseded attempt | unchanged | unchanged | unchanged |
| Live question bank/part edit or question import | unchanged | unchanged | unchanged |
| Identical metadata or already-present connection | unchanged | unchanged | unchanged |

Started attempts retain immutable content and scoring rules. Editing the live bank cannot retroactively alter a completed grade; a later effective completion advances its own placement. Legacy documents initialize semantic revisions once from their stored content/operational versions and persist those independent values on the next write.

## Transactional event

The subject change, its event counter and outbox record commit together. The producer publishes persistent messages to durable topic exchange `cpm.events`, routing key `exams.source.changed`, using mandatory routing and publisher confirmation. Unroutable/nacked/disconnected publication remains pending. Delivery is at least once, so consumers deduplicate by UUID and handle the publication/DB crash window.

```json
{
  "message_id": "00000000-0000-4000-8000-000000000001",
  "message_type": "exams.source.changed",
  "schema_version": 1,
  "occurred_at": "2026-10-09T12:00:00Z",
  "producer": "exams-service",
  "correlation_id": "00000000-0000-4000-8000-000000000001",
  "payload": {
    "organization_id": 1001,
    "division_id": 1002,
    "exam_id": 42,
    "exam_revision": 7,
    "source_revision": 3,
    "reason": "vote"
  }
}
```

Event `payload.exam_revision` is the **technical aggregate CAS version**, for tracing. It is not comparable with snapshot `exam_revision` or `division_revision`. `payload.source_revision` is the placement's monotonic integer, identical in meaning to `data.source_revisions[division_id]`. Ratings compares its saved placement counter with this watermark; technical changes in another division cannot make the saved placement perpetually stale. Consumer topology, inbox, ACK-after-commit and publication fencing belong to Ratings.

## Live proof and remaining limits

The Docker HTTP baseline passed 1462 checks. The separate two-organization run passed 10510 checks with a 5000-row import, live permission revocation, scope switching and actual semantic revision assertions. The final 251-row regression passed 1033 checks, including eight concurrent identical DELETE pairs, current catalogue/result dates versus immutable attempt windows; all 33 exact fixture events were confirmed published and observed in the Ratings inbox before scoped cleanup. Private reports and exact synthetic IDs are retained under `dev/local/exams-audit-http-report-*.json` and matching fixture files.

Independent real MySQL/RabbitMQ integration passed rollback, unroutable retry, persistent confirmed publication, duplicate message identity and crash-window recovery using temporary resources. No common broker or database was stopped. These checks prove source transport and scope; the separate Ratings owner verifies calculation/publication across all rating inputs. They do not establish a 450-concurrent-session SLA. The aggregate remains limited to 40 MiB and serializes writes to one shared exam.
