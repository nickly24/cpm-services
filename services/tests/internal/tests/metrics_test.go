package tests

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/drivertest"
)

func metricCountResponse(count int64) bson.D {
	return bson.D{{Key: "ok", Value: 1}, {Key: "cursor", Value: bson.M{"id": int64(0), "ns": "metrics_unit." + AttemptsCollection, "firstBatch": bson.A{bson.M{"n": count}}}}}
}
func metricFindResponse(collection string, docs ...any) bson.D {
	return bson.D{{Key: "ok", Value: 1}, {Key: "cursor", Value: bson.M{"id": int64(0), "ns": "metrics_unit." + collection, "firstBatch": docs}}}
}

func TestMetricsMongoWorkingSetHintsAndInitialAge(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	responses := []bson.D{metricCountResponse(2), metricCountResponse(3)}
	for _, item := range []struct {
		count int64
		age   time.Duration
	}{{2, 90 * time.Second}, {1, 120 * time.Second}, {1, 60 * time.Second}, {1, 0}} {
		responses = append(responses, metricCountResponse(item.count))
		if item.age > 0 {
			responses = append(responses, metricFindResponse(AttemptsCollection, bson.M{"accepted_at": now.Add(-item.age)}))
		}
	}
	deployment := drivertest.NewMockDeployment(responses...)
	commands := []bson.Raw{}
	clientOptions := options.Client().SetMonitor(&event.CommandMonitor{Started: func(_ context.Context, e *event.CommandStartedEvent) {
		commands = append(commands, append(bson.Raw(nil), e.Command...))
	}})
	clientOptions.Deployment = deployment
	client, err := mongo.Connect(clientOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(context.Background())
	store := &Store{Collection: client.Database("metrics_unit").Collection(CollectionName)}
	got, err := store.metricsBacklog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Outbox != [2]int64{2, 3} || got.Calculations != [4]int64{2, 1, 1, 1} || !got.OldestInitial.Equal(now.Add(-120*time.Second)) {
		t.Fatalf("incorrect durable backlog: %+v", got)
	}
	if len(commands) != 9 {
		t.Fatalf("expected bounded nine queries, got %d", len(commands))
	}
	for _, command := range commands {
		hint := command.Lookup("hint")
		if hint.Type != bson.TypeString {
			t.Fatal("collector query missing mandatory hint", command)
		}
		if name := hint.StringValue(); name != outboxReadyIndex && name != "attempts_monitoring_v1" {
			t.Fatal("unexpected hint", name)
		}
		if command.Lookup("find").Type != 0 {
			filter := command.Lookup("filter").Document()
			if filter.Lookup("calculation_revision").Int32() != 1 {
				t.Fatal("old regrade included in acceptance age", command)
			}
			if command.Lookup("limit").Int64() != 1 {
				t.Fatal("oldest job query unbounded", command)
			}
			projection := command.Lookup("projection").Document()
			if projection.Lookup("accepted_at").Type == 0 || projection.Lookup("answers").Type != 0 {
				t.Fatal("oldest query reads full attempt", command)
			}
		}
	}
}

func TestMetricsHTTPPrivacyCachingAndCollectorFailure(t *testing.T) {
	s := &Server{}
	handler := s.Handler()
	for i := 0; i < 20; i++ {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", fmt.Sprintf("/unknown/%024x?password=private-secret", i), nil))
		if w.Code != 404 {
			t.Fatal(w.Code)
		}
	}
	m := s.telemetry()
	m.backlogMu.Lock()
	m.backlog = testsBacklog{Outbox: [2]int64{7, 2}, Calculations: [4]int64{9, 3, 1, 0}}
	m.successAt = time.Now()
	m.attemptedAt = time.Now()
	m.collectorOK = true
	m.backlogMu.Unlock()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatal("metrics unavailable", w.Code)
	}
	text := w.Body.String()
	for _, required := range []string{`route="unmatched",status="404"} 20`, `cpm_metrics_collector_success 1`, `cpm_tests_outbox_pending_documents{collection="catalog"} 7`, `le="+Inf"} 20`} {
		if !strings.Contains(text, required) {
			t.Fatal("missing metric", required, text)
		}
	}
	if strings.Contains(text, "private-secret") || strings.Contains(text, "/unknown/") || strings.Contains(text, "/metrics") {
		t.Fatal("raw or self-scrape label leaked", text)
	}
	// Expire only the sample cache. A missing Mongo store must not erase the
	// actual last-good backlog or make HTTP instrumentation disappear.
	m.backlogMu.Lock()
	m.attemptedAt = time.Now().Add(-time.Minute)
	m.backlogMu.Unlock()
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(w.Body.String(), "cpm_metrics_collector_success 0") || !strings.Contains(w.Body.String(), `collection="catalog"} 7`) {
		t.Fatal("collector failure fabricated healthy backlog", w.Body.String())
	}
}

func TestMetricsConcurrentHTTPAndResults(t *testing.T) {
	s := &Server{}
	m := s.telemetry()
	var workers sync.WaitGroup
	for range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 100 {
				m.observeRequest("POST", "POST /attempts/{attempt}/finalize", 202, .2)
				s.observeResult("initial_grade", time.Unix(0, 0), time.Unix(2, 0))
				m.writeHTTP(httptest.NewRecorder())
			}
		}()
	}
	workers.Wait()
	if len(m.requests) != 1 || m.requests[requestMetricKey{"POST", "POST /attempts/{attempt}/finalize", 202}].Count != 1600 || m.results["initial_grade"].Count != 1600 {
		t.Fatal("lost concurrent observations")
	}
	var h durationMetric
	h.observe(.02, []float64{.01, .05, 1})
	if h.Count != 1 || h.Buckets[0] != 0 || h.Buckets[1] != 1 || h.Buckets[2] != 1 {
		t.Fatal("histogram buckets are not cumulative", h)
	}
}
