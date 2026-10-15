package calculate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type failureDeliveryStore struct {
	*poolRepository
	retried chan Message
}

func (s *failureDeliveryStore) Retry(ctx context.Context, m Message, reason string, next time.Time) error {
	if err := s.poolRepository.Retry(ctx, m, reason, next); err != nil {
		return err
	}
	s.retried <- m
	return nil
}

func TestWorkerRetriesInboxWhenFailureStatusDeliveryIsUnavailable(t *testing.T) {
	m, input := poolGradeMessage(t, 43, "regrade")
	input.Answers = []PracticeAnswer{{QuestionID: 1, Value: json.RawMessage(`false`)}}
	store := &failureDeliveryStore{poolRepository: &poolRepository{items: map[string]Message{m.ID: m}, finished: make(chan Message, 1)}, retried: make(chan Message, 1)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			reply(w, 200, input)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/calculation-status") {
			t.Error("invalid input produced a grade")
		}
		fail(w, 503, "temporary")
	}))
	defer server.Close()
	w := &Worker{Store: store, Tests: NewTestsClient(server.URL, strings.Repeat("k", 48))}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.RunPool(ctx, 1) }()
	_ = awaitMessage(t, store.retried)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	stored := store.items[m.ID]
	store.mu.Unlock()
	if stored.Status != "retrying" || stored.LastError != "tests_temporarily_unavailable" || stored.NextAttemptAt == nil || len(stored.Output) != 0 {
		t.Fatal("lost status update was terminal or fabricated a grade")
	}
}

func TestWorkerPersistsFailureAndNotifiesTestsBeforeGradeExists(t *testing.T) {
	m, input := poolGradeMessage(t, 42, "regrade")
	// Invalid answer, not the supported historical null sentinel.
	input.Answers = []PracticeAnswer{{QuestionID: 1, Value: json.RawMessage(`false`)}}
	store := &poolRepository{items: map[string]Message{m.ID: m}, finished: make(chan Message, 1)}
	var mu sync.Mutex
	statuses := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			reply(w, 200, input)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/calculation-status") {
			t.Error("invalid input produced a grade callback")
			fail(w, 400, "unexpected")
			return
		}
		var status struct {
			Revision int64  `json:"calculation_revision"`
			Hash     string `json:"input_hash"`
			Status   string `json:"status"`
			Reason   string `json:"reason"`
		}
		if json.NewDecoder(r.Body).Decode(&status) != nil || status.Revision != input.CalculationRevision || status.Hash != input.InputHash {
			t.Error("failure lost verified reference")
		}
		if status.Status == "failed" && status.Reason != "invalid_calculation_input" {
			t.Error("wrong safe failure code")
		}
		mu.Lock()
		statuses = append(statuses, status.Status)
		mu.Unlock()
		reply(w, 200, map[string]bool{"ok": true})
	}))
	defer server.Close()
	w := &Worker{Store: store, Tests: NewTestsClient(server.URL, strings.Repeat("k", 48))}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.RunPool(ctx, 1) }()
	finished := awaitMessage(t, store.finished)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if finished.Status != "failed" {
		t.Fatal("invalid job did not terminate")
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(statuses, ",") != "processing,failed" {
		t.Fatalf("student left in processing: %v", statuses)
	}
	stored := store.items[m.ID]
	if len(stored.Output) != 0 || stored.LastError != "invalid_calculation_input" || stored.Status != "failed" {
		t.Fatal("failure persisted a fake grade or lost its reason")
	}
}

func TestGradeFailureNotificationRetriesLostDeliveryWithoutGradeOutput(t *testing.T) {
	input := workerInput()
	input.Answers = []PracticeAnswer{{QuestionID: 1, Value: json.RawMessage(`false`)}}
	m := workerMessage(t)
	failCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			reply(w, 200, input)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/calculation-status") {
			t.Error("unexpected grade callback")
			return
		}
		var status struct {
			Status string `json:"status"`
		}
		_ = json.NewDecoder(r.Body).Decode(&status)
		if status.Status == "failed" {
			failCalls++
			if failCalls == 1 {
				fail(w, 503, "temporary")
				return
			}
		}
		reply(w, 200, map[string]bool{"ok": true})
	}))
	defer server.Close()
	w := &Worker{Store: &processingMemory{}, Tests: NewTestsClient(server.URL, strings.Repeat("k", 48))}
	_, _, output, err := w.Process(context.Background(), m)
	if !errors.Is(err, ErrInvalidGradeInput) || len(output) != 0 {
		t.Fatal("invalid input did not retain a failure reference")
	}
	notifyErr := w.notifyGradeFailure(context.Background(), m, output, err, "failed", "invalid_calculation_input")
	status, _ := classifyProcessError(notifyErr)
	if status != "retrying" {
		t.Fatal("lost failure delivery must be retried")
	}
	_, _, output, err = w.Process(context.Background(), m)
	if e := w.notifyGradeFailure(context.Background(), m, output, err, "failed", "invalid_calculation_input"); e != nil || failCalls != 2 {
		t.Fatal("failure delivery did not recover")
	}
}

func TestUnverifiedScopeCannotNotifyFailureForAnotherSubmission(t *testing.T) {
	posts := 0
	input := workerInput()
	input.DivisionID++
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
		}
		reply(w, 200, input)
	}))
	defer server.Close()
	w := &Worker{Store: &processingMemory{}, Tests: NewTestsClient(server.URL, strings.Repeat("k", 48))}
	m := workerMessage(t)
	_, _, output, err := w.Process(context.Background(), m)
	if !errors.Is(err, ErrInvalidGradeInput) {
		t.Fatal("foreign scope was accepted")
	}
	if e := w.notifyGradeFailure(context.Background(), m, output, err, "failed", "invalid_calculation_input"); e != nil || posts != 0 {
		t.Fatal("unverified input gained a status-write reference")
	}
}
