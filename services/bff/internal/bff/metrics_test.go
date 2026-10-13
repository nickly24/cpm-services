package bff

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type metricSessionStore struct {
	sessionStore
	calls atomic.Int64
	fail  atomic.Bool
}

func (s *metricSessionStore) Ready(context.Context) error {
	s.calls.Add(1)
	if s.fail.Load() {
		return errors.New("private-session-password")
	}
	return nil
}

func TestMetricsRouteLabelsBoundedAndPrivate(t *testing.T) {
	for _, item := range []struct{ method, path, want string }{
		{"POST", "/bff/tests/attempts/507f1f77bcf86cd799439011/finalize", "/bff/tests/attempts/{id}/finalize"},
		{"GET", "/bff/uploads/receipts/command:private_secret-123", "/bff/uploads/receipts/{id}"},
		{"GET", "/bff/uploads/sessions/507f1f77bcf86cd79943901100000000", "/bff/uploads/sessions/{id}"},
		{"POST", "/bff/attendance/divisions/123/zaps/456/dates/2026-10-13/retry", "/bff/attendance/divisions/{id}/zaps/{id}/dates/{id}/retry"},
		{"GET", "/bff/exams/capabilities", "/bff/exams/capabilities"},
		{"GET", "/bff/arbitrary/personal-user", "unmatched"},
	} {
		if got := metricRoute(item.method, item.path); got != item.want {
			t.Errorf("route %s: %s != %s", item.path, got, item.want)
		}
	}
}

func TestMetricsSessionStoreCacheFailureAndConcurrency(t *testing.T) {
	s := New(Config{})
	defer s.Close()
	store := &metricSessionStore{sessionStore: s.store}
	s.store = store
	for _, id := range []string{"507f1f77bcf86cd799439011", "507f1f77bcf86cd799439012"} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("POST", "/bff/tests/attempts/"+id+"/finalize?secret=private", nil))
		if w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	var workers sync.WaitGroup
	for range 30 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			w := httptest.NewRecorder()
			s.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
			if w.Code != 200 {
				t.Error(w.Code)
			}
		}()
	}
	workers.Wait()
	if store.calls.Load() != 1 {
		t.Fatal("readiness checked on every scrape", store.calls.Load())
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	text := w.Body.String()
	for _, want := range []string{`route="/bff/tests/attempts/{id}/finalize",status="403"} 2`, `cpm_bff_session_store_up{backend="memory"} 1`, `le="+Inf"} 2`} {
		if !strings.Contains(text, want) {
			t.Fatal("missing metric", want, text)
		}
	}
	if strings.Contains(text, "507f1f") || strings.Contains(text, "private") || strings.Contains(text, "/metrics") {
		t.Fatal("private labels leaked", text)
	}
	store.fail.Store(true)
	m := s.telemetry()
	m.collectorMu.Lock()
	m.attemptedAt = time.Now().Add(-time.Minute)
	m.collectorMu.Unlock()
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(w.Body.String(), `cpm_bff_session_store_up{backend="memory"} 0`) {
		t.Fatal("lost failure signal", w.Body.String())
	}
}
