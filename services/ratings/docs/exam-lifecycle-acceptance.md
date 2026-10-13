# Classic exam lifecycle and ratings acceptance

`scripts/exam_lifecycle_test.py` is an HTTP-only acceptance scenario using the
latest successful `ratings-tenant-report.json` and its matching private tenant
manifest. Run it after the tenant test and period/source test have completed and
the Exams and Tests owners confirm their current runtimes are ready:

```sh
dev/.venv/bin/python services/ratings/scripts/exam_lifecycle_test.py --allow-source-mutations
```

The script uses the fixed loopback `cpm_dev` service origins and standard-library
HTTP helpers from `period_sources_test.py`. It refuses a mismatched manifest,
unrelated division names, unselected organization contexts, or a stale/partial
starting publication. Tokens and passwords never enter its report. Exams public
routes use the division selected in the authenticated token; they do not accept
a `division_id` query override. Every Exams mutation gets a UUID idempotency key.

It creates three own examiner accounts through Core in the retained A1 scope
and adds only those new accounts to A2. It uses the existing owned direction,
creates one shared classic question bank, and configures independent conduct
settings, commissions and assignments in A1/A2. Existing tenant student
memberships and source fixtures are retained without modification.

The real owner workflow prepares the assigned attempt, marks both commission
members ready, starts it and records two unanimous votes. A one-question part
with weight 5 produces first grade 5 in A1; a half vote and A2 `round_down`
settings produce grade 2 in A2. A pending A1 retake keeps grade 5 and leaves
rating revisions/freshness unchanged. Its completed grade 0 replaces the first
grade; a current-attempt appeal changes it to 3. An appeal to the historical
first attempt leaves the selected current result and rating revisions stable.

The script creates eight asynchronous rating jobs. Manual expectations retain
the starting publication's H/T and prior exam grades, adding exactly the known
new effective classic grade to the exam average. Missing peer results remain
in the denominator, and a completed zero remains distinguishable from a missing
result. A1 completion/current appeal invalidates only A1; shared exam renaming
invalidates A1 and A2. Published rows and pointers remain unchanged until an
explicit successful recalculation. Every historical version stays immutable,
including B1/B2 control publications in the other organization.

HTTP checks observe exact scoped source counter deltas, semantic snapshot
hashes, selected attempt/result versions, public freshness and two durable
Calculate callback identities per job. The source-event journal is available
for a separate root-run read-only inbox audit; HTTP freshness alone is not
reported as proof of Rabbit source-event delivery.

New accounts, exam, attempts and rating history are retained. Their own manifest
is `dev/local/ratings-exam-lifecycle-<tenant-run>-<lifecycle-run>-fixtures.json`;
the latest report is `dev/local/ratings-exam-lifecycle-report.json`. Both files
are written with mode `0600`. The script has no database client, queue
administration, source import, service restart or destructive cleanup.

## Observed acceptance run: 2026-10-09

The authorized live run completed successfully at 19:49 Moscow time using tenant
`10d3deb9e73c`, lifecycle `10fd996afaad`, and own shared classic exam `98`.
It passed **808 HTTP checks**, completed **8 rating jobs / 16 personal
calculations**, and retained three completed attempts. Execution took about
27 seconds, from `2026-10-09T16:49:03.510005Z` to
`2026-10-09T16:49:30.833252Z`. No routine script correction or owner-data
remediation was needed during this live run.

The private manifest is
`dev/local/ratings-exam-lifecycle-10d3deb9e73c-10fd996afaad-fixtures.json`;
the nonsecret report is `dev/local/ratings-exam-lifecycle-report.json`.
Both were checked to have mode `0600`. The report's `event_expectations` contains
the scoped counters, semantic hashes and selected result IDs/attempt versions
before and after each lifecycle transition.

| Transition | A1 counter | A2 counter | Invalidated scope |
| --- | --- | --- | --- |
| Pending first A1 attempt | 5 → 5 | 5 → 5 | None |
| Completed first A1 attempt, grade 5 | 5 → 6 | 5 → 5 | A1 |
| Pending first A2 attempt | 6 → 6 | 5 → 5 | None |
| Completed first A2 attempt, grade 2 | 6 → 6 | 5 → 6 | A2 |
| Unfinished A1 retake | 6 → 6 | 6 → 6 | None |
| Completed worse A1 retake, grade 0 | 6 → 7 | 6 → 6 | A1 |
| Current A1 attempt appeal, grade 3 | 7 → 8 | 6 → 6 | A1 |
| Historical A1 attempt appeal | 8 → 8 | 6 → 6 | None |
| Shared exam name change | 8 → 9 | 6 → 7 | A1 and A2 |

B1/B2 counters stayed at 2 throughout. Assertions confirmed fresh unaffected
publications, stale affected pointers without partial replacement, and immutable
historical rows. A1's manual final ratings followed `56 → 71 → 56 → 65`; A2's
followed `29 → 35`. Renaming preserved both final values after explicit
recalculation.

The final full-period publication pointers observed by the successful script are:

| Scope | Organization / division | Version |
| --- | --- | --- |
| A1 | 1001 / 10188 | `5b6e726d-76d0-44a2-adcc-9d12fd6a949e` |
| A2 | 1001 / 10189 | `0993c156-0d78-4e0f-af95-2787d0d212a7` |
| B1 | 2001 / 10190 | `980eafb6-b445-4acb-8b89-f30b803df386` |
| B2 | 2001 / 10191 | `1840457d-2de6-49ca-a640-bd61103b1d66` |

This run verifies owner HTTP state, ratings behavior and durable Calculate
callback diagnostics. Root then completed a separate **52-check read-only SQL
audit** of this exact exam and A1/A2 scopes: all **10 own Exams outbox events**
had `published_at`, a matching canonical payload hash in the Ratings inbox, and
the exact organization/division/source revision. Scoped watermarks were 9 and 7.
Each journal delta matched delivered event counts, including zero changes for
pending/historical transitions, and no automatic rating jobs were created.
This is recorded separately in
`dev/local/ratings-exam-events-audit-report.json` (`0600`, no credentials). It
does not rely on HTTP freshness as proof of Rabbit delivery. The diagnostic
audit made no database writes or queue/runtime changes.
