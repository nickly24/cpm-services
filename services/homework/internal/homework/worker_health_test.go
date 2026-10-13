package homework

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func healthyHeartbeat(at time.Time) workerHeartbeatDTO {
	yes, no := true, false
	h := workerHeartbeatDTO{SchemaVersion: 1, InstanceID: uuid(), ObservedAt: at, RabbitMQ: &yes, Tools: &yes, Processing: &no}
	h.Storage.State, h.Storage.CheckedAt = "ok", &at
	return h
}

func TestWorkerHeartbeatAuthenticationAndStrictContract(t *testing.T) {
	s := &Server{Config: Config{Key: strings.Repeat("w", 32), CoreKey: strings.Repeat("c", 32)}}
	h := healthyHeartbeat(time.Now().UTC())
	b, _ := json.Marshal(h)
	for _, c := range []struct {
		name, body, key, content string
		want                     int
	}{
		{"no key", string(b), "", "application/json", 401},
		{"user bearer cannot authenticate", string(b), "wrong", "application/json", 401},
		{"core key cannot authenticate worker", string(b), s.Config.CoreKey, "application/json", 401},
		{"wrong media", string(b), s.Config.Key, "text/plain", 415},
		{"unknown business field", strings.TrimSuffix(string(b), "}") + `,"job_id":"private"}`, s.Config.Key, "application/json", 400},
		{"oversize", strings.Repeat(" ", 4097) + string(b), s.Config.Key, "application/json", 400},
		{"trailing JSON", string(b) + `{}`, s.Config.Key, "application/json", 400},
		{"null", `null`, s.Config.Key, "application/json", 400},
		{"absent booleans", `{}`, s.Config.Key, "application/json", 400},
		{"accepted", string(b), s.Config.Key, "application/json", 200},
		{"replay", string(b), s.Config.Key, "application/json", 409},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/internal/homework/file-work/heartbeat", strings.NewReader(c.body))
			r.Header.Set("X-Service-Key", c.key)
			r.Header.Set("Authorization", "Bearer ordinary-user")
			r.Header.Set("Content-Type", c.content)
			w := httptest.NewRecorder()
			s.Routes().ServeHTTP(w, r)
			if w.Code != c.want {
				t.Fatalf("got %d want %d", w.Code, c.want)
			}
		})
	}
}

func TestWorkerObservationTimeAndComponentValidation(t *testing.T) {
	at := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name   string
		mutate func(*workerHeartbeatDTO)
		valid  bool
	}{
		{"healthy", func(h *workerHeartbeatDTO) {}, true},
		{"wrong version", func(h *workerHeartbeatDTO) { h.SchemaVersion = 2 }, false},
		{"bad instance", func(h *workerHeartbeatDTO) { h.InstanceID = "private/job" }, false},
		{"missing Rabbit", func(h *workerHeartbeatDTO) { h.RabbitMQ = nil }, false},
		{"missing tools", func(h *workerHeartbeatDTO) { h.Tools = nil }, false},
		{"missing processing", func(h *workerHeartbeatDTO) { h.Processing = nil }, false},
		{"zero clock", func(h *workerHeartbeatDTO) { h.ObservedAt = time.Time{} }, false},
		{"future clock", func(h *workerHeartbeatDTO) { h.ObservedAt = at.Add(6 * time.Second) }, false},
		{"old clock", func(h *workerHeartbeatDTO) { h.ObservedAt = at.Add(-31 * time.Second) }, false},
		{"non UTC", func(h *workerHeartbeatDTO) { h.ObservedAt = at.In(moscow) }, false},
		{"probe missing", func(h *workerHeartbeatDTO) { h.Storage.CheckedAt = nil }, false},
		{"probe invented", func(h *workerHeartbeatDTO) { h.Storage.State = "ready" }, false},
		{"probe future", func(h *workerHeartbeatDTO) { t := at.Add(6 * time.Second); h.Storage.CheckedAt = &t }, false},
		{"double clock grace cannot admit future probe", func(h *workerHeartbeatDTO) {
			h.ObservedAt = at.Add(5 * time.Second)
			t := at.Add(10 * time.Second)
			h.Storage.CheckedAt = &t
		}, false},
		{"probe later than its observation", func(h *workerHeartbeatDTO) {
			t := at.Add(time.Second)
			h.Storage.CheckedAt = &t
		}, false},
		{"unknown no probe", func(h *workerHeartbeatDTO) { h.Storage.State = "unknown"; h.Storage.CheckedAt = nil }, true},
		{"unknown with probe", func(h *workerHeartbeatDTO) { h.Storage.State = "unknown" }, false},
		{"failed probe", func(h *workerHeartbeatDTO) { h.Storage.State = "error" }, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := healthyHeartbeat(at)
			c.mutate(&h)
			if h.valid(at) != c.valid {
				t.Fatal("invalid component/time contract")
			}
		})
	}
}

func TestMonitoringDoesNotReplaceUnknownOrStaleWithSuccess(t *testing.T) {
	at := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name, worker, storage string
		prepare               func(*workerHealth)
		now                   time.Time
		ready                 bool
	}{
		{"never observed", "unknown", "unknown", func(h *workerHealth) {}, at, false},
		{"healthy", "ok", "ok", func(h *workerHealth) { h.record(healthyHeartbeat(at), at) }, at, true},
		{"worker stale", "stale", "stale", func(h *workerHealth) { h.record(healthyHeartbeat(at), at) }, at.Add(31 * time.Second), false},
		{"fresh worker stale storage", "ok", "stale", func(h *workerHealth) {
			v := healthyHeartbeat(at)
			old := at.Add(-61 * time.Second)
			v.Storage.CheckedAt = &old
			h.record(v, at)
		}, at, false},
		{"fresh worker unknown storage", "ok", "unknown", func(h *workerHealth) {
			v := healthyHeartbeat(at)
			v.Storage.State = "unknown"
			v.Storage.CheckedAt = nil
			h.record(v, at)
		}, at, false},
		{"failed storage", "ok", "error", func(h *workerHealth) { v := healthyHeartbeat(at); v.Storage.State = "error"; h.record(v, at) }, at, false},
		{"worker disconnected", "error", "ok", func(h *workerHealth) { v := healthyHeartbeat(at); no := false; v.RabbitMQ = &no; h.record(v, at) }, at, false},
		{"worker tools missing", "error", "ok", func(h *workerHealth) { v := healthyHeartbeat(at); no := false; v.Tools = &no; h.record(v, at) }, at, false},
		{"one worker cannot hide another failure", "ok", "error", func(h *workerHealth) {
			v := healthyHeartbeat(at)
			v.Storage.State = "error"
			h.record(v, at)
			h.record(healthyHeartbeat(at), at)
		}, at, false},
		{"healthy instance cannot invent probe for unknown instance", "ok", "unknown", func(h *workerHealth) {
			h.record(healthyHeartbeat(at), at)
			v := healthyHeartbeat(at)
			v.Storage.State, v.Storage.CheckedAt = "unknown", nil
			h.record(v, at)
		}, at, false},
		{"error timestamp does not come from older healthy probe", "ok", "error", func(h *workerHealth) {
			v := healthyHeartbeat(at)
			old := at.Add(-20 * time.Second)
			v.Storage.CheckedAt = &old
			h.record(v, at)
			v = healthyHeartbeat(at)
			v.Storage.State = "error"
			h.record(v, at)
		}, at, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var h workerHealth
			c.prepare(&h)
			worker, storage, ready := h.snapshot(c.now)
			if worker["state"] != c.worker || storage["state"] != c.storage || ready != c.ready {
				t.Fatalf("worker=%v storage=%v ready=%v", worker, storage, ready)
			}
			if c.storage == "unknown" && storage["checked_at"] != nil {
				if p, ok := storage["checked_at"].(*time.Time); !ok || p != nil {
					t.Fatal("unknown probe has fabricated timestamp")
				}
			}
			if c.name == "error timestamp does not come from older healthy probe" && !storage["checked_at"].(*time.Time).Equal(at) {
				t.Fatal("error probe timestamp replaced by healthy timestamp")
			}
			b, _ := json.Marshal([]any{worker, storage})
			for _, private := range []string{"instance_id", "job_id", "source_key", "output_key", "account_id", "bucket", "secret"} {
				if strings.Contains(string(b), private) {
					t.Fatal("infrastructure DTO exposed private information")
				}
			}
		})
	}
}

func TestLateHeartbeatCannotHideLaterFailure(t *testing.T) {
	at := time.Now().UTC()
	var h workerHealth
	v := healthyHeartbeat(at)
	if h.record(v, at) != nil {
		t.Fatal("first record")
	}
	failure := v
	failure.ObservedAt = at.Add(time.Second)
	failure.Storage.State = "error"
	h.record(failure, failure.ObservedAt)
	if h.record(v, at.Add(2*time.Second)) == nil {
		t.Fatal("old success replaced newer failure")
	}
	_, storage, ready := h.snapshot(at.Add(2 * time.Second))
	if ready || storage["state"] != "error" {
		t.Fatal("failure hidden")
	}
}

func TestWorkerHealthBoundedAndConcurrent(t *testing.T) {
	at := time.Now().UTC()
	var h workerHealth
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if h.record(healthyHeartbeat(at), at) != nil {
				t.Error("valid instance rejected")
			}
			h.snapshot(at)
		}()
	}
	wg.Wait()
	if h.record(healthyHeartbeat(at), at) == nil {
		t.Fatal("unbounded registry")
	}
	if h.record(healthyHeartbeat(at.Add(11*time.Minute)), at.Add(11*time.Minute)) != nil {
		t.Fatal("expired instances not removed")
	}
}

func TestPublicJobLimitsUseImmutableSnapshotAndHideStorage(t *testing.T) {
	settings := DefaultSettings()
	settings.MaxPages = 17
	b, _ := json.Marshal(settings)
	j := map[string]any{"settings_json": string(b), "source_key": "private-staging", "output_key": "private-output", "lease_token": "private-token"}
	output := jobDTO(j)
	limits := output["limits"].(map[string]any)
	if len(limits) != 5 || limits["max_pages"] != 17 || limits["max_source_bytes"] != settings.MaxSourceBytes || limits["max_output_bytes"] != settings.MaxOutputBytes {
		t.Fatal("limits lost")
	}
	for _, key := range []string{"source_key", "output_key", "lease_token", "settings_json"} {
		if _, exists := output[key]; exists {
			t.Fatal("private field leaked")
		}
	}
	j["settings_json"] = "{}"
	if jobDTO(j)["limits"] != nil {
		t.Fatal("missing snapshot invented current policy")
	}
}

func TestCoreMemberClassNullable(t *testing.T) {
	for _, c := range []struct {
		body string
		want *int
	}{{`{"class":10}`, func() *int { n := 10; return &n }()}, {`{"class":null}`, nil}, {`{}`, nil}} {
		var m Member
		if json.Unmarshal([]byte(c.body), &m) != nil || (m.Class == nil) != (c.want == nil) || m.Class != nil && *m.Class != *c.want {
			t.Fatal("class null/value changed")
		}
	}
}
