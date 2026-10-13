package tests

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const metricsCacheTTL = 10 * time.Second

var requestDurationBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30}
var resultDurationBuckets = []float64{1, 5, 15, 30, 60, 120, 300, 600, 1800, 3600}

type durationMetric struct {
	Count   uint64
	Sum     float64
	Buckets []uint64
}

func (h *durationMetric) observe(seconds float64, bounds []float64) {
	if h.Buckets == nil {
		h.Buckets = make([]uint64, len(bounds))
	}
	h.Count++
	h.Sum += seconds
	for i, bound := range bounds {
		if seconds <= bound {
			h.Buckets[i]++
		}
	}
}

type requestMetricKey struct {
	Method, Route string
	Status        int
}
type testsBacklog struct {
	Outbox        [2]int64
	Calculations  [4]int64
	OldestInitial time.Time
}
type serviceMetrics struct {
	mu          sync.Mutex
	requests    map[requestMetricKey]*durationMetric
	results     map[string]*durationMetric
	backlogMu   sync.Mutex
	backlog     testsBacklog
	attemptedAt time.Time
	successAt   time.Time
	collectorOK bool
}

func (s *Server) telemetry() *serviceMetrics {
	s.metricsOnce.Do(func() {
		s.metrics = &serviceMetrics{requests: map[requestMetricKey]*durationMetric{}, results: map[string]*durationMetric{}}
	})
	return s.metrics
}

// The router pattern is bounded by registered routes. URL paths, query strings,
// account/test IDs, authentication headers and error text never become labels.
func (m *serviceMetrics) observeRequest(method, pattern string, status int, seconds float64) {
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
	default:
		method = "OTHER"
	}
	if pattern == "" {
		pattern = "unmatched"
	}
	if status < 100 || status > 599 {
		status = 500
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := requestMetricKey{method, pattern, status}
	h := m.requests[key]
	if h == nil {
		h = &durationMetric{}
		m.requests[key] = h
	}
	h.observe(seconds, requestDurationBuckets)
}

func (s *Server) observeResult(operation string, created, completed time.Time) {
	switch operation {
	case "initial_grade", "regrade":
	default:
		operation = "unknown"
	}
	if created.IsZero() || completed.Before(created) {
		return
	}
	m := s.telemetry()
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.results[operation]
	if h == nil {
		h = &durationMetric{}
		m.results[operation] = h
	}
	h.observe(completed.Sub(created).Seconds(), resultDurationBuckets)
}

type measuredResponse struct {
	http.ResponseWriter
	status int
}

func (w *measuredResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *measuredResponse) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.ResponseWriter.Write(body)
}
func (w *measuredResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func writeHistogram(w io.Writer, name, labels string, h durationMetric, bounds []float64) {
	for i, bound := range bounds {
		fmt.Fprintf(w, "%s_bucket{%sle=%s} %d\n", name, labels, strconv.Quote(strconv.FormatFloat(bound, 'g', -1, 64)), h.Buckets[i])
	}
	fmt.Fprintf(w, "%s_bucket{%sle=\"+Inf\"} %d\n%s_sum{%s} %g\n%s_count{%s} %d\n", name, labels, h.Count, name, strings.TrimSuffix(labels, ","), h.Sum, name, strings.TrimSuffix(labels, ","), h.Count)
}

func (m *serviceMetrics) writeHTTP(w io.Writer) {
	// Snapshot under the lock; a slow scrape client cannot hold up exam requests.
	m.mu.Lock()
	requests := make(map[requestMetricKey]durationMetric, len(m.requests))
	for key, value := range m.requests {
		row := *value
		row.Buckets = append([]uint64(nil), value.Buckets...)
		requests[key] = row
	}
	results := make(map[string]durationMetric, len(m.results))
	for key, value := range m.results {
		row := *value
		row.Buckets = append([]uint64(nil), value.Buckets...)
		results[key] = row
	}
	m.mu.Unlock()
	keys := make([]requestMetricKey, 0, len(requests))
	for key := range requests {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.Route != b.Route {
			return a.Route < b.Route
		}
		if a.Method != b.Method {
			return a.Method < b.Method
		}
		return a.Status < b.Status
	})
	labels := func(key requestMetricKey) string {
		return fmt.Sprintf("method=%s,route=%s,status=%s", strconv.Quote(key.Method), strconv.Quote(key.Route), strconv.Quote(strconv.Itoa(key.Status)))
	}
	fmt.Fprintln(w, "# HELP cpm_http_requests_total Completed HTTP requests by registered route, method and status.\n# TYPE cpm_http_requests_total counter")
	for _, key := range keys {
		fmt.Fprintf(w, "cpm_http_requests_total{%s} %d\n", labels(key), requests[key].Count)
	}
	fmt.Fprintln(w, "# HELP cpm_http_request_duration_seconds HTTP request duration.\n# TYPE cpm_http_request_duration_seconds histogram")
	for _, key := range keys {
		writeHistogram(w, "cpm_http_request_duration_seconds", labels(key)+",", requests[key], requestDurationBuckets)
	}
	fmt.Fprintln(w, "# HELP cpm_tests_calculation_result_latency_seconds Time from durable job creation until a new result is selected; duplicate callbacks are excluded.\n# TYPE cpm_tests_calculation_result_latency_seconds histogram")
	for _, kind := range []string{"initial_grade", "regrade", "unknown"} {
		if h, ok := results[kind]; ok {
			writeHistogram(w, "cpm_tests_calculation_result_latency_seconds", "kind="+strconv.Quote(kind)+",", h, resultDurationBuckets)
		}
	}
}

// Every query is forced onto a bounded working-set index. A missing index is an
// observable collector error, never a fallback full scan of accepted history.
func (s *Store) metricsBacklog(ctx context.Context) (testsBacklog, error) {
	if s == nil || s.Collection == nil {
		return testsBacklog{}, errors.New("metrics_store_unavailable")
	}
	return collectTestsBacklog(ctx, s.Collection, s.Attempts())
}

func collectTestsBacklog(ctx context.Context, catalog, attempts *mongo.Collection) (testsBacklog, error) {
	var result testsBacklog
	for i, collection := range []*mongo.Collection{catalog, attempts} {
		count, err := collection.CountDocuments(ctx, bson.M{"outbox_pending": true}, options.Count().SetHint(outboxReadyIndex))
		if err != nil {
			return result, err
		}
		result.Outbox[i] = count
	}
	for i, status := range []string{"queued", "processing", "retrying", "failed"} {
		filter := bson.M{"active": true, "status": "accepted", "calculation_status": status}
		count, err := attempts.CountDocuments(ctx, filter, options.Count().SetHint("attempts_monitoring_v1"))
		if err != nil {
			return result, err
		}
		result.Calculations[i] = count
		if status == "failed" || count == 0 {
			continue
		}
		// AcceptedAt is an honest start time only for the first calculation;
		// recalculation backlog age is measured by Calculate's new inbox receipt.
		filter["calculation_revision"] = 1
		var oldest struct {
			AcceptedAt time.Time `bson:"accepted_at"`
		}
		err = attempts.FindOne(ctx, filter, options.FindOne().SetHint("attempts_monitoring_v1").SetSort(bson.D{{Key: "accepted_at", Value: 1}, {Key: "_id", Value: 1}}).SetProjection(bson.M{"accepted_at": 1, "_id": 0})).Decode(&oldest)
		if errors.Is(err, mongo.ErrNoDocuments) {
			continue
		}
		if err != nil {
			return result, err
		}
		if !oldest.AcceptedAt.IsZero() && (result.OldestInitial.IsZero() || oldest.AcceptedAt.Before(result.OldestInitial)) {
			result.OldestInitial = oldest.AcceptedAt
		}
	}
	return result, nil
}

func (s *Server) metricsHTTP(w http.ResponseWriter, r *http.Request) {
	m := s.telemetry()
	m.backlogMu.Lock()
	now := time.Now()
	if m.attemptedAt.IsZero() || now.Sub(m.attemptedAt) >= metricsCacheTTL {
		m.attemptedAt = now
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		backlog, err := s.Store.metricsBacklog(ctx)
		cancel()
		m.collectorOK = err == nil
		if err == nil {
			m.backlog, m.successAt = backlog, time.Now()
		}
	}
	backlog, successAt, ok := m.backlog, m.successAt, m.collectorOK
	m.backlogMu.Unlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	m.writeHTTP(w)
	flag := 0
	if ok {
		flag = 1
	}
	fmt.Fprintf(w, "# HELP cpm_metrics_collector_success Whether the last indexed Mongo collector query succeeded.\n# TYPE cpm_metrics_collector_success gauge\ncpm_metrics_collector_success %d\n# HELP cpm_metrics_collector_last_success_timestamp_seconds Time of the last complete successful Mongo sample; zero means no successful sample.\n# TYPE cpm_metrics_collector_last_success_timestamp_seconds gauge\ncpm_metrics_collector_last_success_timestamp_seconds %g\n", flag, unixSeconds(successAt))
	// Failed collection does not fabricate zero backlog. Stale last-good values
	// remain visible alongside failure and freshness metrics.
	if successAt.IsZero() {
		return
	}
	fmt.Fprintln(w, "# HELP cpm_tests_outbox_pending_documents Documents with at least one event awaiting durable publisher confirmation.\n# TYPE cpm_tests_outbox_pending_documents gauge")
	for i, collection := range []string{"catalog", "attempts"} {
		fmt.Fprintf(w, "cpm_tests_outbox_pending_documents{collection=%s} %d\n", strconv.Quote(collection), backlog.Outbox[i])
	}
	fmt.Fprintln(w, "# HELP cpm_tests_calculations_pending Active accepted submissions by calculation status.\n# TYPE cpm_tests_calculations_pending gauge")
	for i, status := range []string{"queued", "processing", "retrying", "failed"} {
		fmt.Fprintf(w, "cpm_tests_calculations_pending{status=%s} %d\n", strconv.Quote(status), backlog.Calculations[i])
	}
	fmt.Fprintf(w, "# HELP cpm_tests_oldest_initial_submission_age_seconds Age since acceptance of the oldest unfinished first calculation, excluding recalculations and terminal failures.\n# TYPE cpm_tests_oldest_initial_submission_age_seconds gauge\ncpm_tests_oldest_initial_submission_age_seconds %g\n", metricAge(time.Now(), backlog.OldestInitial))
}

func unixSeconds(t time.Time) float64 {
	if t.IsZero() {
		return 0
	}
	return float64(t.UnixMilli()) / 1000
}
func metricAge(now, t time.Time) float64 {
	if t.IsZero() || now.Before(t) {
		return 0
	}
	return now.Sub(t).Seconds()
}
