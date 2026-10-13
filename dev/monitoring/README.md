# Test delivery monitoring

Start alongside the existing Dev network:

```sh
docker compose --project-directory dev --env-file dev/.env -f dev/compose.yaml -f dev/compose.monitoring.yaml --profile monitoring up -d --wait prometheus
```

Open <http://127.0.0.1:9090/query> for graphs and <http://127.0.0.1:9090/alerts> for local alerts. No external notification or data export is configured.

Useful queries:

```promql
cpm:http_duration_p95_seconds5m{job="bff",route=~".*commits|.*finalize"}
cpm:initial_result_latency_p95_seconds5m
cpm:calculate_pending_jobs
cpm:calculate_oldest_active_job_seconds
cpm_tests_outbox_pending_documents
cpm_tests_calculations_pending
rate(cpm_calculate_worker_completed_total[5m])
cpm_calculate_workers_active / cpm_calculate_worker_capacity
cpm_bff_session_store_up
```

The HTTP histograms measure real route-level request durations. Result latency measures from durable calculation-job creation to successful result selection; duplicate result callbacks do not add observations. Worker processing time is a cumulative sum, not a percentile. `content` and `regrade` share one background slot, so their capacities must not be summed as two independent workers.

The RabbitMQ queue can be empty while the Mongo inbox is still processing work. Backlog gauges query the durable inbox by processing kind/status, with an indexed oldest receipt. Tests' oldest submission age covers first calculations only: the original acceptance timestamp of an old regrade is not a correct recalculation age. All retained failures are exposed, including older entries with unknown processing kind.

All Calculate replicas read the same shared inbox. Backlog recording rules take the maximum count across healthy, fresh replicas before summing statuses; they do not multiply one shared queue by the number of instances. Per-worker counters remain per-process and can be summed to measure throughput.

Collectors use a ten-second cache and a three-second Mongo deadline (one second for the BFF session store). Every Mongo query specifies a working-set index. A missing index produces `cpm_metrics_collector_success=0` and does not trigger a historical full scan. Last successful values remain available on collector failure, with a separate success timestamp; alert on collection failure/staleness before trusting backlog values. HTTP metrics stay available if Mongo/Redis collection fails.

No account, organization, test, attempt, message, session, URL query, credential, or error-text labels are exposed. Unknown routes collapse to one bounded label. Business HTTP metrics do not include scrapes of `/metrics` itself.

Validate configuration and local alert behavior with the same image:

```sh
docker run --rm --entrypoint /bin/promtool -v "$PWD/dev/monitoring:/etc/prometheus:ro" prom/prometheus:v3.13.4 check rules /etc/prometheus/rules.yaml
docker run --rm --entrypoint /bin/promtool -v "$PWD/dev/monitoring:/etc/prometheus:ro" prom/prometheus:v3.13.4 test rules /etc/prometheus/rules_test.yaml
```

Alert thresholds are initial Dev thresholds, to be calibrated against measured exam-load requirements. Historical intentional fault fixtures may trigger the terminal-failure alert; investigate those entries before deciding whether to requeue or archive them.
