package calculate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
)

type poolRepository struct {
	mu       sync.Mutex
	items    map[string]Message
	finished chan Message
}

func (s *poolRepository) Claim(context.Context, time.Time, string) (Message, error) {
	return Message{}, errors.New("pool must select a lane")
}

func (s *poolRepository) ClaimKind(_ context.Context, now time.Time, token, kind string) (Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, m := range s.items {
		if processingKind(m) != kind || m.Status != "received" && (m.Status != "retrying" || m.NextAttemptAt == nil || m.NextAttemptAt.After(now)) {
			continue
		}
		until := now.Add(processLease)
		m.Status, m.LeaseToken, m.LeaseUntil = "processing", token, &until
		m.ProcessingAttempts++
		s.items[id] = m
		return m, nil
	}
	return Message{}, mongo.ErrNoDocuments
}

func (s *poolRepository) update(m Message, mutate func(*Message)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, exists := s.items[m.ID]
	if !exists || stored.LeaseToken != m.LeaseToken || stored.Status != "processing" {
		return ErrLeaseLost
	}
	mutate(&stored)
	s.items[m.ID] = stored
	return nil
}

func (s *poolRepository) Checkpoint(_ context.Context, m Message, cursor string, output json.RawMessage) error {
	return s.update(m, func(value *Message) {
		value.ProcessCursor = cursor
		value.Output = append(json.RawMessage(nil), output...)
	})
}

func (s *poolRepository) Finish(ctx context.Context, m Message, status, reason string, output json.RawMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := s.update(m, func(value *Message) {
		value.Status, value.LastError = status, reason
		value.Output = append(json.RawMessage(nil), output...)
	})
	if err == nil {
		m.Status = status
		s.finished <- m
	}
	return err
}

func (s *poolRepository) Retry(ctx context.Context, m Message, reason string, next time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.update(m, func(value *Message) {
		value.Status, value.LastError, value.NextAttemptAt = "retrying", reason, &next
	})
}

func poolGradeMessage(t *testing.T, number int, operation string) (Message, CalculationInput) {
	t.Helper()
	input := workerInput()
	input.SubmissionID = fmt.Sprintf("%024x", number)
	input.Operation = operation
	body, _ := json.Marshal(Envelope{
		MessageID: fmt.Sprintf("bd095746-6c38-4b77-bc40-%012x", number), MessageType: "tests.grade.requested", SchemaVersion: 1,
		OccurredAt: time.Now().UTC(), Producer: "tests-service", CorrelationID: "pool-test",
		Payload: mustJSON(map[string]any{"submission_id": input.SubmissionID, "test_id": input.TestID, "organization_id": input.OrganizationID, "division_id": input.DivisionID, "account_id": input.AccountID, "calculation_revision": input.CalculationRevision, "operation": operation, "grading_test_version_id": input.GradingVersionID}),
	})
	m, err := ParseMessage(body, "tests.grade.requested.v1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return m, input
}

func awaitMessage(t *testing.T, messages <-chan Message) Message {
	t.Helper()
	select {
	case m := <-messages:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not finish its bounded task")
		return Message{}
	}
}

func TestGradingPoolRunsConcurrentlyWithoutExceedingItsBound(t *testing.T) {
	const total, slots = 24, 4
	store := &poolRepository{items: map[string]Message{}, finished: make(chan Message, total)}
	inputs := map[string]CalculationInput{}
	for i := 1; i <= total; i++ {
		m, input := poolGradeMessage(t, i, "initial_grade")
		store.items[m.ID], inputs[input.SubmissionID] = m, input
	}
	var mu sync.Mutex
	active, maximum := 0, 0
	callbacks := map[string]int{}
	arrived := make(chan struct{}, total)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		id := parts[4]
		switch parts[5] {
		case "input":
			mu.Lock()
			active++
			if active > maximum {
				maximum = active
			}
			mu.Unlock()
			arrived <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
			}
			mu.Lock()
			active--
			mu.Unlock()
			reply(w, 200, inputs[id])
		case "result":
			mu.Lock()
			callbacks[id]++
			mu.Unlock()
			reply(w, 200, map[string]bool{"ok": true})
		default:
			reply(w, 200, map[string]bool{"ok": true})
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker := &Worker{Store: store, Tests: NewTestsClient(server.URL, strings.Repeat("k", 48))}
	done := make(chan error, 1)
	go func() { done <- worker.RunPool(ctx, slots) }()
	for range slots {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("initial jobs were not processed in parallel")
		}
	}
	select {
	case <-arrived:
		close(release)
		t.Fatal("pool exceeded its configured bound")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	seen := map[string]bool{}
	for range total {
		m := awaitMessage(t, store.finished)
		if m.Status != "ready" || seen[m.ID] {
			t.Fatalf("job completed with wrong status or twice: %+v", m)
		}
		seen[m.ID] = true
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if maximum != slots || len(callbacks) != total {
		t.Fatalf("concurrency/results mismatch: max=%d callbacks=%d", maximum, len(callbacks))
	}
	for id, count := range callbacks {
		if count != 1 {
			t.Fatalf("submission %s received %d callbacks", id, count)
		}
	}
	stats := worker.Stats()[0]
	if stats.Active != 0 || stats.Capacity != slots || stats.Completed != total || stats.Attempts != total || stats.ProcessingSeconds <= 0 {
		t.Fatalf("worker counters do not describe completed work: %+v", stats)
	}
}

func TestBlockedContentPreparationDoesNotBlockNewGradingAndBackgroundLaneIsBounded(t *testing.T) {
	store := &poolRepository{items: map[string]Message{}, finished: make(chan Message, 3)}
	initial, initialInput := poolGradeMessage(t, 1, "initial_grade")
	regrade, regradeInput := poolGradeMessage(t, 2, "regrade")
	before := gradeQuestions()
	after := cloneQuestions(before)
	after[0].Points++
	body, _ := json.Marshal(Envelope{MessageID: "bd095746-6c38-4b77-bc40-000000000003", MessageType: "tests.content.changed", SchemaVersion: 1, OccurredAt: time.Now().UTC(), Producer: "tests-service", CorrelationID: "pool-edit", Payload: mustJSON(map[string]any{"test_id": initial.TestID, "organization_id": initial.OrganizationID, "before_version_id": "507f1f77bcf86cd799439013", "after_version_id": "507f1f77bcf86cd799439014", "change_batch": map[string]any{"before": map[string]any{"questions": before}, "after": map[string]any{"questions": after}}})})
	content, err := ParseMessage(body, "tests.content.changed.v1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	store.items[initial.ID], store.items[regrade.ID], store.items[content.ID] = initial, regrade, content
	contentStarted, releaseContent := make(chan struct{}), make(chan struct{})
	regradeStarted := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/tests/recalculations/prepare" {
			close(contentStarted)
			select {
			case <-releaseContent:
			case <-r.Context().Done():
			}
			reply(w, 200, PreparedRecalculation{Status: "queued"})
			return
		}
		if r.Method == "GET" {
			if strings.Contains(r.URL.Path, regrade.SubmissionID) {
				regradeStarted <- struct{}{}
				reply(w, 200, regradeInput)
			} else {
				reply(w, 200, initialInput)
			}
			return
		}
		reply(w, 200, map[string]bool{"ok": true})
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker := &Worker{Store: store, Tests: NewTestsClient(server.URL, strings.Repeat("k", 48))}
	done := make(chan error, 1)
	go func() { done <- worker.RunPool(ctx, 2) }()
	select {
	case <-contentStarted:
	case <-time.After(5 * time.Second):
		close(releaseContent)
		t.Fatal("content preparation never started")
	}
	if m := awaitMessage(t, store.finished); m.ID != initial.ID {
		close(releaseContent)
		t.Fatal("new grading was blocked behind content preparation")
	}
	select {
	case <-regradeStarted:
		close(releaseContent)
		t.Fatal("background pool exceeded its single slot")
	default:
	}
	close(releaseContent)
	for range 2 {
		if m := awaitMessage(t, store.finished); m.Status != "ready" {
			t.Fatalf("background work not completed: %+v", m)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPoolShutdownWaitsForWorkersAndReleasesCancelledJobForRetry(t *testing.T) {
	m, _ := poolGradeMessage(t, 1, "initial_grade")
	store := &poolRepository{items: map[string]Message{m.ID: m}, finished: make(chan Message, 1)}
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker := &Worker{Store: store, Tests: NewTestsClient(server.URL, strings.Repeat("k", 48))}
	done := make(chan error, 1)
	go func() { done <- worker.RunPool(ctx, 4) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("job was not started")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pool did not finish its workers on shutdown")
	}
	store.mu.Lock()
	stored := store.items[m.ID]
	store.mu.Unlock()
	if stored.Status != "retrying" || stored.NextAttemptAt == nil || worker.Stats()[0].Active != 0 {
		t.Fatalf("shutdown abandoned an active lease: %+v", stored)
	}
}

func TestPoolConfigurationRejectsUnboundedConcurrency(t *testing.T) {
	for key, value := range map[string]string{"MONGO_HOST": "localhost", "MONGO_PORT": "27018", "MONGO_DATABASE": "cpm_dev", "MONGO_USER": "dev", "MONGO_PASSWORD": "synthetic", "RABBITMQ_URL": "amqp://dev:synthetic@localhost:5672/", "AUTH_INTERNAL_KEY": strings.Repeat("k", 48), "CALCULATE_TESTS_URL": "http://tests:8004"} {
		t.Setenv(key, value)
	}
	t.Setenv("CALCULATE_GRADING_WORKERS", "")
	t.Setenv("CALCULATE_RECEIPT_WORKERS", "")
	t.Setenv("CALCULATE_MONGO_POOL_SIZE", "")
	c, err := LoadConfig()
	if err != nil || c.GradingWorkers != 4 || c.ReceiptWorkers != 4 || c.MongoPoolSize != 10 {
		t.Fatalf("unexpected defaults: %+v %v", c, err)
	}
	for _, count := range []string{"0", "33", "-1", "all"} {
		t.Setenv("CALCULATE_GRADING_WORKERS", count)
		if _, err := LoadConfig(); err == nil {
			t.Fatalf("accepted invalid worker count %q", count)
		}
	}
	t.Setenv("CALCULATE_GRADING_WORKERS", "8")
	for _, count := range []string{"0", "9", "-1", "all"} {
		t.Setenv("CALCULATE_RECEIPT_WORKERS", count)
		if _, err := LoadConfig(); err == nil {
			t.Fatalf("accepted invalid receipt worker count %q", count)
		}
	}
	t.Setenv("CALCULATE_RECEIPT_WORKERS", "8")
	t.Setenv("CALCULATE_MONGO_POOL_SIZE", "24")
	c, err = LoadConfig()
	if err != nil || c.GradingWorkers != 8 || c.ReceiptWorkers != 8 || c.MongoPoolSize != 24 {
		t.Fatalf("explicit bounds not applied: %+v %v", c, err)
	}
	for _, count := range []string{"0", "1", "129", "-1", "all"} {
		t.Setenv("CALCULATE_MONGO_POOL_SIZE", count)
		if _, err := LoadConfig(); err == nil {
			t.Fatalf("accepted invalid Mongo pool %q", count)
		}
	}
}
