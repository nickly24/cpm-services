package bff

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const metricsCacheTTL = 10 * time.Second

var metricID = regexp.MustCompile(`^(?:[0-9]+|[a-fA-F0-9]{24}|[a-fA-F0-9]{32}|[a-fA-F0-9-]{36}|[0-9]{4}-[0-9]{2}-[0-9]{2})$`)

var requestDurationBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30}

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
type serviceMetrics struct {
	mu                     sync.Mutex
	requests               map[requestMetricKey]*durationMetric
	collectorMu            sync.Mutex
	attemptedAt, successAt time.Time
	collectorOK            bool
}

func (s *Server) telemetry() *serviceMetrics {
	s.metricsOnce.Do(func() { s.metrics = &serviceMetrics{requests: map[requestMetricKey]*durationMetric{}} })
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
}

// Match the same route allowlists as ServeHTTP before removing IDs. Unknown
// paths collapse to one label instead of allowing arbitrary metric cardinality.
func metricRoute(method, path string) string {
	if method == "GET" && (path == "/health/live" || path == "/health/ready" || path == "/openapi.json") {
		return path
	}
	tail, ok := strings.CutPrefix(path, "/bff/")
	if !ok {
		return "unmatched"
	}
	allowed := false
	switch method + " " + tail {
	case "POST auth/login", "POST auth/logout", "POST auth/refresh", "POST auth/context", "GET auth/me", "GET exams/capabilities":
		allowed = true
	}
	if v, ok := strings.CutPrefix(tail, "tests/"); ok {
		route, _ := testsRoute(method, v)
		allowed = route != ""
	}
	if v, ok := strings.CutPrefix(tail, "core/"); ok {
		allowed = coreRoute(method, v) != ""
	}
	if v, ok := strings.CutPrefix(tail, "homework/"); ok {
		allowed = hwRoutes.MatchString(method + " " + v)
	}
	if v, ok := strings.CutPrefix(tail, "cards/"); ok {
		allowed = cardsRoute(method, v) != ""
	}
	if v, ok := strings.CutPrefix(tail, "attendance/"); ok {
		allowed = attendanceRoute(method, v) != ""
	}
	if v, ok := strings.CutPrefix(tail, "ratings/"); ok {
		allowed = ratingsRoute(method, v) != ""
	}
	if v, ok := strings.CutPrefix(tail, "uploads/"); ok {
		allowed = uploadsRoute(method, v) != ""
	}
	if v, ok := examTail(tail); ok && tail != "exams/capabilities" {
		allowed = examsRoute(method, v) != ""
		if allowed {
			tail = "exams/" + v
		}
	}
	if !allowed {
		return "unmatched"
	}
	parts := strings.Split(tail, "/")
	for i, part := range parts {
		if metricID.MatchString(part) || i > 0 && parts[i-1] == "receipts" {
			parts[i] = "{id}"
		}
	}
	return "/bff/" + strings.Join(parts, "/")
}

func (s *Server) metricsHTTP(w http.ResponseWriter, r *http.Request) {
	m := s.telemetry()
	m.collectorMu.Lock()
	now := time.Now()
	if m.attemptedAt.IsZero() || now.Sub(m.attemptedAt) >= metricsCacheTTL {
		m.attemptedAt = now
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		m.collectorOK = s.store != nil && s.store.Ready(ctx) == nil
		cancel()
		if m.collectorOK {
			m.successAt = time.Now()
		}
	}
	ok, successAt := m.collectorOK, m.successAt
	m.collectorMu.Unlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	m.writeHTTP(w)
	flag := 0
	if ok {
		flag = 1
	}
	backend := "memory"
	if s.config.RedisAddress != "" {
		backend = "redis"
	}
	timestamp := 0.
	if !successAt.IsZero() {
		timestamp = float64(successAt.UnixMilli()) / 1000
	}
	fmt.Fprintf(w, "# HELP cpm_bff_session_store_up Whether session store's latest readiness check succeeded.\n# TYPE cpm_bff_session_store_up gauge\ncpm_bff_session_store_up{backend=%s} %d\n# HELP cpm_metrics_collector_success Whether the last collector completed.\n# TYPE cpm_metrics_collector_success gauge\ncpm_metrics_collector_success %d\n# HELP cpm_metrics_collector_last_success_timestamp_seconds Last successful session store check.\n# TYPE cpm_metrics_collector_last_success_timestamp_seconds gauge\ncpm_metrics_collector_last_success_timestamp_seconds %g\n", strconv.Quote(backend), flag, flag, timestamp)
}
