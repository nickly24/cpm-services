package homework

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const workerFreshFor = 30 * time.Second
const storageFreshFor = 60 * time.Second

// Heartbeats describe shared infrastructure only. They never carry job,
// student, object key, bucket or credentials across organization boundaries.
type workerHeartbeatDTO struct {
	SchemaVersion int       `json:"schema_version"`
	InstanceID    string    `json:"instance_id"`
	ObservedAt    time.Time `json:"observed_at"`
	RabbitMQ      *bool     `json:"rabbitmq"`
	Tools         *bool     `json:"tools"`
	Processing    *bool     `json:"processing"`
	Storage       struct {
		State     string     `json:"state"`
		CheckedAt *time.Time `json:"checked_at"`
	} `json:"storage"`
}

type workerObservation struct {
	workerHeartbeatDTO
	ReceivedAt time.Time
}

type workerHealth struct {
	mu        sync.Mutex
	instances map[string]workerObservation
}

func validObservationTime(at, now time.Time) bool {
	_, offset := at.Zone()
	return !at.IsZero() && offset == 0 && !at.After(now.Add(5*time.Second))
}

func (h workerHeartbeatDTO) valid(now time.Time) bool {
	if h.SchemaVersion != 1 || !validUUID(h.InstanceID) || h.RabbitMQ == nil || h.Tools == nil || h.Processing == nil || !validObservationTime(h.ObservedAt, now) || now.Sub(h.ObservedAt) > workerFreshFor {
		return false
	}
	switch h.Storage.State {
	case "unknown":
		return h.Storage.CheckedAt == nil
	case "ok", "error":
		return h.Storage.CheckedAt != nil && validObservationTime(*h.Storage.CheckedAt, now) && !h.Storage.CheckedAt.After(h.ObservedAt)
	default:
		return false
	}
}

func (h *workerHealth) record(in workerHeartbeatDTO, now time.Time) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.instances == nil {
		h.instances = make(map[string]workerObservation)
	}
	for id, observation := range h.instances {
		if now.Sub(observation.ReceivedAt) > 10*time.Minute {
			delete(h.instances, id)
		}
	}
	if old, exists := h.instances[in.InstanceID]; exists {
		// A delayed or replayed successful heartbeat must not overwrite a later
		// failure or extend its freshness window.
		if !in.ObservedAt.After(old.ObservedAt) {
			return conflict("stale_heartbeat")
		}
	} else if len(h.instances) >= 16 {
		return conflict("worker_instance_limit")
	}
	h.instances[in.InstanceID] = workerObservation{in, now}
	return nil
}

func (s *Server) workerHeartbeat(w http.ResponseWriter, r *http.Request) {
	if !s.internal(r) {
		fail(w, &APIError{401, "unauthorized_service"})
		return
	}
	if strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) != "application/json" {
		fail(w, &APIError{415, "json_required"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	var in workerHeartbeatDTO
	now := time.Now().UTC()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF || !in.valid(now) {
		fail(w, bad("invalid_heartbeat"))
		return
	}
	if err := s.Workers.record(in, now); err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, map[string]any{"status": true})
}

func (h *workerHealth) snapshot(now time.Time) (map[string]any, map[string]any, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	worker := map[string]any{"state": "unknown", "received_at": nil, "observed_at": nil, "rabbitmq": nil, "tools": nil, "processing": nil, "fresh_instances": 0}
	storage := map[string]any{"state": "unknown", "checked_at": nil}
	var latest workerObservation
	fresh := []workerObservation{}
	for _, in := range h.instances {
		if in.ReceivedAt.After(latest.ReceivedAt) {
			latest = in
		}
		if now.Sub(in.ReceivedAt) <= workerFreshFor && now.Sub(in.ObservedAt) <= workerFreshFor {
			fresh = append(fresh, in)
		}
	}
	if !latest.ReceivedAt.IsZero() {
		worker["received_at"], worker["observed_at"] = latest.ReceivedAt, latest.ObservedAt
		storage["checked_at"] = latest.Storage.CheckedAt
		worker["state"], storage["state"] = "stale", "stale"
	}
	if len(fresh) == 0 {
		return worker, storage, false
	}
	broker, tools, processing := true, true, false
	storageState := "ok"
	var checked *time.Time
	// A fresh worker reporting an error cannot be hidden by another healthy
	// instance. An absent probe or an expired probe also prevents readiness.
	rank := map[string]int{"ok": 0, "unknown": 1, "stale": 2, "error": 3}
	for _, in := range fresh {
		broker = broker && *in.RabbitMQ
		tools = tools && *in.Tools
		processing = processing || *in.Processing
		state := in.Storage.State
		if in.Storage.CheckedAt != nil {
			if now.Sub(*in.Storage.CheckedAt) > storageFreshFor {
				state = "stale"
			}
		}
		if rank[state] > rank[storageState] {
			storageState = state
			checked = in.Storage.CheckedAt
		} else if state == storageState && in.Storage.CheckedAt != nil && (checked == nil || in.Storage.CheckedAt.Before(*checked)) {
			checked = in.Storage.CheckedAt
		}
	}
	workerState := "ok"
	if !broker || !tools {
		workerState = "error"
	}
	worker["state"], worker["rabbitmq"], worker["tools"], worker["processing"], worker["fresh_instances"] = workerState, broker, tools, processing, len(fresh)
	storage["state"], storage["checked_at"] = storageState, checked
	return worker, storage, workerState == "ok" && storageState == "ok"
}
