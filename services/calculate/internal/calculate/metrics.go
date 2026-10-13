package calculate

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
type inboxMetric struct {
	Kind, Status string
	Count        int64
	Oldest       time.Time
}
type serviceMetrics struct {
	mu                     sync.Mutex
	requests               map[requestMetricKey]*durationMetric
	backlogMu              sync.Mutex
	backlog                []inboxMetric
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

// This optional capability keeps mock repositories simple and forces the live
// implementation to use its dedicated index rather than reading raw envelopes.
func (s *Store) metricsBacklog(ctx context.Context) ([]inboxMetric, error) {
	if s == nil || s.Collection == nil {
		return nil, errors.New("metrics_store_unavailable")
	}
	result := []inboxMetric{}
	for _, kind := range []string{InitialGradeKind, RegradeKind, ContentKind, "unknown"} {
		for _, status := range []string{"received", "retrying", "processing", "failed"} {
			filter := bson.M{"processing_kind": kind, "status": status}
			if kind == "unknown" {
				filter["processing_kind"] = bson.M{"$in": bson.A{nil, ""}}
			}
			count, err := s.Collection.CountDocuments(ctx, filter, options.Count().SetHint("inbox_monitoring"))
			if err != nil {
				return nil, err
			}
			row := inboxMetric{Kind: kind, Status: status, Count: count}
			if count > 0 && status != "failed" {
				var oldest struct {
					ReceivedAt time.Time `bson:"received_at"`
				}
				// Both unknown equality branches are queried separately to retain index
				// ordering; finished legacy history is never inspected or reclassified.
				kinds := []any{kind}
				if kind == "unknown" {
					kinds = []any{nil, ""}
				}
				for _, value := range kinds {
					filter["processing_kind"] = value
					err = s.Collection.FindOne(ctx, filter, options.FindOne().SetHint("inbox_monitoring").SetSort(bson.D{{Key: "received_at", Value: 1}, {Key: "_id", Value: 1}}).SetProjection(bson.M{"received_at": 1, "_id": 0})).Decode(&oldest)
					if errors.Is(err, mongo.ErrNoDocuments) {
						continue
					}
					if err != nil {
						return nil, err
					}
					if !oldest.ReceivedAt.IsZero() && (row.Oldest.IsZero() || oldest.ReceivedAt.Before(row.Oldest)) {
						row.Oldest = oldest.ReceivedAt
					}
				}
			}
			result = append(result, row)
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
		store, available := s.Store.(interface {
			metricsBacklog(context.Context) ([]inboxMetric, error)
		})
		var backlog []inboxMetric
		err := errors.New("metrics_store_unavailable")
		if available {
			backlog, err = store.metricsBacklog(ctx)
		}
		cancel()
		m.collectorOK = err == nil
		if err == nil {
			m.backlog, m.successAt = backlog, time.Now()
		}
	}
	backlog, successAt, ok := append([]inboxMetric(nil), m.backlog...), m.successAt, m.collectorOK
	m.backlogMu.Unlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	m.writeHTTP(w)
	flag := 0
	if ok {
		flag = 1
	}
	fmt.Fprintf(w, "# HELP cpm_metrics_collector_success Whether the last indexed Mongo collector query succeeded.\n# TYPE cpm_metrics_collector_success gauge\ncpm_metrics_collector_success %d\n# HELP cpm_metrics_collector_last_success_timestamp_seconds Last complete successful Mongo sample; zero means never.\n# TYPE cpm_metrics_collector_last_success_timestamp_seconds gauge\ncpm_metrics_collector_last_success_timestamp_seconds %g\n", flag, unixSeconds(successAt))
	if !successAt.IsZero() {
		fmt.Fprintln(w, "# HELP cpm_calculate_inbox_messages Durable grading inbox messages by kind and status, after RabbitMQ ACK.\n# TYPE cpm_calculate_inbox_messages gauge\n# HELP cpm_calculate_oldest_active_job_age_seconds Age since inbox receipt of oldest unfinished job by kind and status; zero means empty.\n# TYPE cpm_calculate_oldest_active_job_age_seconds gauge")
		for _, row := range backlog {
			labels := "kind=" + strconv.Quote(row.Kind) + ",status=" + strconv.Quote(row.Status)
			fmt.Fprintf(w, "cpm_calculate_inbox_messages{%s} %d\n", labels, row.Count)
			if row.Status != "failed" {
				fmt.Fprintf(w, "cpm_calculate_oldest_active_job_age_seconds{%s} %g\n", labels, metricAge(time.Now(), row.Oldest))
			}
		}
	}
	connected := 0
	if s.Consumer != nil && s.Consumer.Connected.Load() {
		connected = 1
	}
	fmt.Fprintf(w, "# HELP cpm_calculate_consumer_connected RabbitMQ grading consumer connection.\n# TYPE cpm_calculate_consumer_connected gauge\ncpm_calculate_consumer_connected %d\n", connected)
	if s.Worker == nil {
		return
	}
	fmt.Fprintln(w, "# HELP cpm_calculate_worker_capacity Configured slots by kind; content and regrade share the same background slot.\n# TYPE cpm_calculate_worker_capacity gauge\n# HELP cpm_calculate_workers_active Currently processing jobs.\n# TYPE cpm_calculate_workers_active gauge\n# HELP cpm_calculate_worker_attempts_total Processing attempts started.\n# TYPE cpm_calculate_worker_attempts_total counter\n# HELP cpm_calculate_worker_completed_total Jobs finished successfully.\n# TYPE cpm_calculate_worker_completed_total counter\n# HELP cpm_calculate_worker_retried_total Jobs scheduled for another attempt.\n# TYPE cpm_calculate_worker_retried_total counter\n# HELP cpm_calculate_worker_failed_total Jobs finished with permanent failure.\n# TYPE cpm_calculate_worker_failed_total counter\n# HELP cpm_calculate_worker_processing_seconds_total Time spent processing attempts, including retries.\n# TYPE cpm_calculate_worker_processing_seconds_total counter")
	for _, row := range s.Worker.Stats() {
		labels := "kind=" + strconv.Quote(row.Kind)
		fmt.Fprintf(w, "cpm_calculate_worker_capacity{%s} %d\ncpm_calculate_workers_active{%s} %d\ncpm_calculate_worker_attempts_total{%s} %d\ncpm_calculate_worker_completed_total{%s} %d\ncpm_calculate_worker_retried_total{%s} %d\ncpm_calculate_worker_failed_total{%s} %d\ncpm_calculate_worker_processing_seconds_total{%s} %g\n", labels, row.Capacity, labels, row.Active, labels, row.Attempts, labels, row.Completed, labels, row.Retried, labels, row.Failed, labels, row.ProcessingSeconds)
	}
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
