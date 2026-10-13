package filework

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type probeFunc func(context.Context, string) error

func (f probeFunc) Probe(ctx context.Context, instance string) error { return f(ctx, instance) }

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func monitorFixture() *HealthMonitor {
	worker := &BrokerWorker{}
	worker.Ready.Store(true)
	return NewHealthMonitor(testInstanceID, "http://homework.example", "secret-monitoring-service-key", worker, true, nil)
}

func TestHeartbeatExactSafePayloadAndAcceptance(t *testing.T) {
	m := monitorFixture()
	m.Worker.Processing.Store(true)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/internal/homework/file-work/heartbeat" || r.Header.Get("X-Service-Key") != m.ServiceKey || r.Header.Get("Content-Type") != "application/json" {
			t.Error("incorrect authenticated heartbeat route")
		}
		body, _ := io.ReadAll(r.Body)
		for _, unsafe := range []string{m.ServiceKey, "job_id", "student", "source_key", "output_key", "bucket", "S3", "homework.example"} {
			if bytes.Contains(body, []byte(unsafe)) {
				t.Errorf("heartbeat leaked %s", unsafe)
			}
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Error(err)
		}
		if len(fields) != 7 {
			t.Errorf("unexpected fields %s", body)
		}
		for _, name := range []string{"schema_version", "instance_id", "observed_at", "rabbitmq", "tools", "storage", "processing"} {
			if _, ok := fields[name]; !ok {
				t.Errorf("missing %s", name)
			}
		}
		var event WorkerHeartbeat
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&event); err != nil {
			t.Error(err)
		}
		if event.SchemaVersion != 1 || event.InstanceID != testInstanceID || !event.RabbitMQ || !event.Tools || !event.Processing {
			t.Error("incorrect heartbeat snapshot")
		}
		if event.ObservedAt.Location() != time.UTC || time.Since(event.ObservedAt) > time.Second {
			t.Error("invalid UTC observation")
		}
		if string(fields["storage"]) != `{"state":"unknown","checked_at":null}` {
			t.Errorf("initial storage claimed success %s", fields["storage"])
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":true}`)
	}))
	defer server.Close()
	m.HomeworkURL = server.URL
	if err := m.SendHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, acceptedAt := m.snapshot()
	if calls.Load() != 1 || !freshAt(time.Now(), acceptedAt, heartbeatFreshness) {
		t.Fatal("accepted heartbeat not recorded")
	}
}

func TestHeartbeatAlwaysIncludesFalseProcessing(t *testing.T) {
	m := monitorFixture()
	body, err := json.Marshal(m.heartbeat())
	if err != nil || !bytes.Contains(body, []byte(`"processing":false`)) {
		t.Fatal("required false processing field omitted")
	}
}

func TestHeartbeatRejectedResponsesDoNotRefresh(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", 403, `{"status":true}`}, {"stale", 409, `{"status":false}`}, {"wrong_success_code", 202, `{"status":true}`},
		{"false", 200, `{"status":false}`}, {"missing", 200, `{}`}, {"invalid", 200, `not-json`},
		{"trailing", 200, `{"status":true} {}`}, {"unknown_field", 200, `{"status":true,"job_id":"private"}`},
		{"too_large", 200, `{"status":true}` + strings.Repeat(" ", 4096)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := monitorFixture()
			old := time.Now().Add(-time.Minute).UTC()
			m.lastHeartbeat = &old
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); io.WriteString(w, tc.body) }))
			defer server.Close()
			m.HomeworkURL = server.URL
			if err := m.SendHeartbeat(context.Background()); err == nil {
				t.Fatal("unaccepted heartbeat counted as success")
			}
			_, accepted := m.snapshot()
			if !accepted.Equal(old) {
				t.Fatal("failure refreshed heartbeat")
			}
		})
	}
}

func TestHeartbeatRejectsRedirectAndBoundsTimeout(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true); io.WriteString(w, `{"status":true}`) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	m := monitorFixture()
	m.HomeworkURL = origin.URL
	m.Client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
	if err := m.SendHeartbeat(context.Background()); err == nil || leaked.Load() {
		t.Fatal("service key followed redirect")
	}
	m.Client = &http.Client{Timeout: time.Hour, Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > heartbeatTimeout {
			t.Error("heartbeat missing 3-second request bound")
		}
		return nil, errors.New("http://secret endpoint with credentials")
	})}
	if err := m.SendHeartbeat(context.Background()); err == nil || err.Error() != "heartbeat_failed" {
		t.Fatal("transport error disclosed metadata or succeeded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.Client = nil
	if err := m.SendHeartbeat(ctx); err == nil {
		t.Fatal("canceled heartbeat succeeded")
	}
}

func TestReadinessUnknownStaleAndComponents(t *testing.T) {
	now := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	for _, mode := range []string{"ok", "unknown", "storage_error", "storage_missing", "storage_stale", "heartbeat_missing", "heartbeat_stale", "storage_future", "heartbeat_future", "rabbit_down", "tools_missing", "at_boundary"} {
		t.Run(mode, func(t *testing.T) {
			m := monitorFixture()
			storageAt, acceptedAt := now, now
			m.storage = StorageObservation{State: "ok", CheckedAt: &storageAt}
			m.lastHeartbeat = &acceptedAt
			switch mode {
			case "unknown":
				m.storage = StorageObservation{State: "unknown"}
			case "storage_error":
				m.storage.State = "error"
			case "storage_missing":
				m.storage.CheckedAt = nil
			case "storage_stale":
				storageAt = now.Add(-storageFreshness - time.Nanosecond)
			case "heartbeat_missing":
				m.lastHeartbeat = nil
			case "heartbeat_stale":
				acceptedAt = now.Add(-heartbeatFreshness - time.Nanosecond)
			case "storage_future":
				storageAt = now.Add(time.Nanosecond)
			case "heartbeat_future":
				acceptedAt = now.Add(time.Nanosecond)
			case "rabbit_down":
				m.Worker.Ready.Store(false)
			case "tools_missing":
				m.Tools = false
			case "at_boundary":
				storageAt = now.Add(-storageFreshness)
				acceptedAt = now.Add(-heartbeatFreshness)
			}
			ready := m.Readiness(now)
			if ready.Status != (mode == "ok" || mode == "at_boundary") {
				t.Fatalf("unexpected readiness %#v", ready)
			}
			// Returned timestamps must not alias the monitor's mutable state.
			if ready.Components.Storage.CheckedAt != nil {
				*ready.Components.Storage.CheckedAt = time.Time{}
				if m.storage.CheckedAt.IsZero() {
					t.Fatal("snapshot leaked state pointer")
				}
			}
		})
	}
}

func TestReadinessHandlerAndProbeCompletion(t *testing.T) {
	m := monitorFixture()
	record := httptest.NewRecorder()
	m.ReadyzHandler(record, httptest.NewRequest("GET", "/readyz", nil))
	if record.Code != 503 || !strings.Contains(record.Body.String(), `"state":"unknown","checked_at":null`) {
		t.Fatal("startup incorrectly ready")
	}
	before := time.Now().UTC()
	m.Prober = probeFunc(func(context.Context, string) error { return nil })
	m.ProbeStorage(context.Background())
	storage, _ := m.snapshot()
	if storage.State != "ok" || storage.CheckedAt == nil || storage.CheckedAt.Before(before) || storage.CheckedAt.After(time.Now()) || storage.CheckedAt.Location() != time.UTC {
		t.Fatal("probe observation not taken at actual completion")
	}
	accepted := time.Now().UTC()
	m.lastHeartbeat = &accepted
	record = httptest.NewRecorder()
	m.ReadyzHandler(record, httptest.NewRequest("GET", "/readyz", nil))
	if record.Code != 200 {
		t.Fatal("healthy components not ready")
	}
	m.Prober = probeFunc(func(context.Context, string) error { return errors.New("secret storage failure") })
	m.ProbeStorage(context.Background())
	if m.Readiness(time.Now()).Status {
		t.Fatal("failed probe retained previous success")
	}
}

func TestMonitorProbeDoesNotBlockHeartbeatAndConcurrentSnapshots(t *testing.T) {
	m := monitorFixture()
	probeStarted := make(chan struct{})
	heartbeatReceived := make(chan struct{}, 1)
	m.Prober = probeFunc(func(ctx context.Context, instance string) error { close(probeStarted); <-ctx.Done(); return ctx.Err() })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		heartbeatReceived <- struct{}{}
		io.WriteString(w, `{"status":true}`)
	}))
	defer server.Close()
	m.HomeworkURL = server.URL
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	go func() { m.Run(ctx); close(finished) }()
	select {
	case <-probeStarted:
	case <-time.After(time.Second):
		t.Fatal("probe never started")
	}
	select {
	case <-heartbeatReceived:
	case <-time.After(time.Second):
		t.Fatal("storage probe blocked heartbeat")
	}
	var readers sync.WaitGroup
	for range 8 {
		readers.Go(func() {
			for range 100 {
				m.Readiness(time.Now())
				m.heartbeat()
			}
		})
	}
	readers.Wait()
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("monitor ignored shutdown")
	}
}

type processingClaim struct {
	worker *BrokerWorker
	active bool
}

func (c *processingClaim) Claim(context.Context, Command) (bool, string, error) {
	c.active = c.worker.Processing.Load()
	return false, "", nil
}
func (*processingClaim) Release(context.Context, Command, string) error { return nil }

func TestProcessingFlagAlwaysResets(t *testing.T) {
	w := &BrokerWorker{}
	claim := &processingClaim{worker: w}
	w.Engine.Claim = claim
	if _, _, err := w.process(context.Background(), validCommand()); err != nil {
		t.Fatal(err)
	}
	if !claim.active || w.Processing.Load() {
		t.Fatal("processing snapshot did not track/reset job claim")
	}
	if _, _, err := w.process(context.Background(), Command{}); err == nil || w.Processing.Load() {
		t.Fatal("invalid command left processing active")
	}
}
