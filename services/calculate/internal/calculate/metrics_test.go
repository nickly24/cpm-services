package calculate

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/drivertest"
)

type metricsRepository struct {
	*memoryRepository
	calls atomic.Int64
	fail  atomic.Bool
}

func (s *metricsRepository) metricsBacklog(context.Context) ([]inboxMetric, error) {
	s.calls.Add(1)
	if s.fail.Load() {
		return nil, errors.New("private-db-address")
	}
	return []inboxMetric{{Kind: InitialGradeKind, Status: "received", Count: 8, Oldest: time.Now().Add(-2 * time.Minute)}}, nil
}

func TestMetricsHTTPDurableBacklogCacheFailureAndPrivacy(t *testing.T) {
	store := &metricsRepository{memoryRepository: &memoryRepository{}}
	s := &Server{Store: store, Worker: &Worker{}, Consumer: &Consumer{}}
	s.Consumer.Connected.Store(true)
	handler := s.Handler()
	for i := 0; i < 25; i++ {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", fmt.Sprintf("/internal/calculate/messages/%036x?token=private-token", i), nil))
		if w.Code != 401 {
			t.Fatal("unauthorized not observed", w.Code)
		}
	}
	var workers sync.WaitGroup
	for range 20 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
			if w.Code != 200 {
				t.Error(w.Code)
			}
		}()
	}
	workers.Wait()
	if store.calls.Load() != 1 {
		t.Fatal("scrapes bypassed ten-second cache", store.calls.Load())
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	text := w.Body.String()
	for _, want := range []string{`route="GET /internal/calculate/messages/{message}",status="401"} 25`, `kind="initial_grade",status="received"} 8`, `cpm_calculate_consumer_connected 1`, `cpm_calculate_worker_completed_total{kind="initial_grade"} 0`, `le="+Inf"} 25`} {
		if !strings.Contains(text, want) {
			t.Fatal("missing metric", want, text)
		}
	}
	if strings.Contains(text, "private-token") || strings.Contains(text, "000000000000") || strings.Contains(text, "/metrics") {
		t.Fatal("private or scrape path leaked", text)
	}
	store.fail.Store(true)
	m := s.telemetry()
	m.backlogMu.Lock()
	m.attemptedAt = time.Now().Add(-time.Minute)
	m.backlogMu.Unlock()
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(w.Body.String(), "cpm_metrics_collector_success 0") || !strings.Contains(w.Body.String(), `status="received"} 8`) || strings.Contains(w.Body.String(), "private-db-address") {
		t.Fatal("failed collector corrupted backlog or leaked error", w.Body.String())
	}
}

func TestMetricsInboxIndexedQueriesAndOldestByKind(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	responses := []bson.D{}
	for _, kind := range []string{InitialGradeKind, RegradeKind, ContentKind, "unknown"} {
		for _, status := range []string{"received", "retrying", "processing", "failed"} {
			count := int64(0)
			if kind == InitialGradeKind && status == "received" {
				count = 3
			}
			responses = append(responses, bson.D{{Key: "ok", Value: 1}, {Key: "cursor", Value: bson.M{"id": int64(0), "ns": "metrics_unit." + CollectionName, "firstBatch": bson.A{bson.M{"n": count}}}}})
			if count > 0 {
				responses = append(responses, bson.D{{Key: "ok", Value: 1}, {Key: "cursor", Value: bson.M{"id": int64(0), "ns": "metrics_unit." + CollectionName, "firstBatch": bson.A{bson.M{"received_at": now.Add(-100 * time.Second)}}}}})
			}
		}
	}
	commands := []bson.Raw{}
	deployment := drivertest.NewMockDeployment(responses...)
	opts := options.Client().SetMonitor(&event.CommandMonitor{Started: func(_ context.Context, e *event.CommandStartedEvent) {
		commands = append(commands, append(bson.Raw(nil), e.Command...))
	}})
	opts.Deployment = deployment
	client, err := mongo.Connect(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(context.Background())
	rows, err := (&Store{Collection: client.Database("metrics_unit").Collection(CollectionName)}).metricsBacklog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 16 || rows[0].Count != 3 || !rows[0].Oldest.Equal(now.Add(-100*time.Second)) {
		t.Fatal("incorrect Mongo backlog", rows)
	}
	if len(commands) != 17 {
		t.Fatal("unbounded query count", len(commands))
	}
	for _, cmd := range commands {
		if cmd.Lookup("hint").StringValue() != "inbox_monitoring" {
			t.Fatal("historical scan fallback", cmd)
		}
		if cmd.Lookup("find").Type != 0 {
			if cmd.Lookup("limit").Int64() != 1 || cmd.Lookup("projection").Document().Lookup("body").Type != 0 {
				t.Fatal("oldest query read raw messages", cmd)
			}
		}
	}
}
