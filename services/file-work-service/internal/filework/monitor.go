package filework

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	heartbeatInterval  = 10 * time.Second
	heartbeatTimeout   = 3 * time.Second
	probeInterval      = 30 * time.Second
	storageFreshness   = 60 * time.Second
	heartbeatFreshness = 30 * time.Second
)

type StorageObservation struct {
	State     string     `json:"state"`
	CheckedAt *time.Time `json:"checked_at"`
}

// WorkerHeartbeat deliberately contains no business, object, or credential data.
type WorkerHeartbeat struct {
	SchemaVersion int                `json:"schema_version"`
	InstanceID    string             `json:"instance_id"`
	ObservedAt    time.Time          `json:"observed_at"`
	RabbitMQ      bool               `json:"rabbitmq"`
	Tools         bool               `json:"tools"`
	Storage       StorageObservation `json:"storage"`
	Processing    bool               `json:"processing"`
}

type StorageProber interface {
	Probe(context.Context, string) error
}

type StorageReadiness struct {
	StorageObservation
	Fresh bool `json:"fresh"`
}

type HeartbeatReadiness struct {
	AcceptedAt *time.Time `json:"accepted_at"`
	Fresh      bool       `json:"fresh"`
}

type WorkerReadiness struct {
	Status     bool `json:"status"`
	Components struct {
		RabbitMQ  bool               `json:"rabbitmq"`
		Tools     bool               `json:"tools"`
		Storage   StorageReadiness   `json:"storage"`
		Heartbeat HeartbeatReadiness `json:"heartbeat"`
	} `json:"components"`
	Processing bool `json:"processing"`
}

type HealthMonitor struct {
	InstanceID  string
	HomeworkURL string
	ServiceKey  string
	Worker      *BrokerWorker
	Tools       bool
	Prober      StorageProber
	Client      *http.Client

	mu            sync.RWMutex
	storage       StorageObservation
	lastHeartbeat *time.Time
}

func NewHealthMonitor(instanceID, homeworkURL, serviceKey string, worker *BrokerWorker, tools bool, prober StorageProber) *HealthMonitor {
	return &HealthMonitor{InstanceID: instanceID, HomeworkURL: homeworkURL, ServiceKey: serviceKey, Worker: worker, Tools: tools, Prober: prober, storage: StorageObservation{State: "unknown"}}
}

// The loops are independent: a slow storage probe never blocks heartbeats or jobs.
func (m *HealthMonitor) Run(ctx context.Context) {
	var loops sync.WaitGroup
	loops.Add(2)
	go func() {
		defer loops.Done()
		m.loop(ctx, probeInterval, func() { m.ProbeStorage(ctx) })
	}()
	go func() {
		defer loops.Done()
		m.loop(ctx, heartbeatInterval, func() { _ = m.SendHeartbeat(ctx) })
	}()
	loops.Wait()
}

func (m *HealthMonitor) loop(ctx context.Context, interval time.Duration, action func()) {
	if ctx.Err() != nil {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	action()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			action()
		}
	}
}

func copyTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (m *HealthMonitor) snapshot() (StorageObservation, *time.Time) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return StorageObservation{State: m.storage.State, CheckedAt: copyTime(m.storage.CheckedAt)}, copyTime(m.lastHeartbeat)
}

func (m *HealthMonitor) ProbeStorage(ctx context.Context) {
	state := "error"
	if m.Prober != nil && m.Prober.Probe(ctx, m.InstanceID) == nil {
		state = "ok"
	}
	checkedAt := time.Now().UTC()
	m.mu.Lock()
	m.storage = StorageObservation{State: state, CheckedAt: &checkedAt}
	m.mu.Unlock()
}

func (m *HealthMonitor) heartbeat() WorkerHeartbeat {
	storage, _ := m.snapshot()
	return WorkerHeartbeat{SchemaVersion: 1, InstanceID: m.InstanceID, ObservedAt: time.Now().UTC(), RabbitMQ: m.Worker != nil && m.Worker.Ready.Load(), Tools: m.Tools, Storage: storage, Processing: m.Worker != nil && m.Worker.Processing.Load()}
}

func (m *HealthMonitor) SendHeartbeat(ctx context.Context) error {
	body, err := json.Marshal(m.heartbeat())
	if err != nil {
		return errors.New("heartbeat_failed")
	}
	requestCtx, cancel := context.WithTimeout(ctx, heartbeatTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, strings.TrimRight(m.HomeworkURL, "/")+"/internal/homework/file-work/heartbeat", bytes.NewReader(body))
	if err != nil {
		return errors.New("heartbeat_failed")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Service-Key", m.ServiceKey)
	client := http.Client{Timeout: heartbeatTimeout}
	if m.Client != nil {
		client = *m.Client
	}
	client.Timeout = heartbeatTimeout
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return errors.New("heartbeat_failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("heartbeat_rejected")
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(responseBody) > 4096 {
		return errors.New("heartbeat_rejected")
	}
	var accepted struct {
		Status bool `json:"status"`
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&accepted); err != nil || !accepted.Status {
		return errors.New("heartbeat_rejected")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("heartbeat_rejected")
	}
	acceptedAt := time.Now().UTC()
	m.mu.Lock()
	m.lastHeartbeat = &acceptedAt
	m.mu.Unlock()
	return nil
}

func freshAt(now time.Time, checkedAt *time.Time, maximumAge time.Duration) bool {
	return checkedAt != nil && !checkedAt.IsZero() && !checkedAt.After(now) && now.Sub(*checkedAt) <= maximumAge
}

func (m *HealthMonitor) Readiness(now time.Time) WorkerReadiness {
	storage, heartbeat := m.snapshot()
	var result WorkerReadiness
	result.Components.RabbitMQ = m.Worker != nil && m.Worker.Ready.Load()
	result.Components.Tools = m.Tools
	result.Components.Storage = StorageReadiness{StorageObservation: storage, Fresh: freshAt(now, storage.CheckedAt, storageFreshness)}
	result.Components.Heartbeat = HeartbeatReadiness{AcceptedAt: heartbeat, Fresh: freshAt(now, heartbeat, heartbeatFreshness)}
	result.Processing = m.Worker != nil && m.Worker.Processing.Load()
	result.Status = result.Components.RabbitMQ && result.Components.Tools && storage.State == "ok" && result.Components.Storage.Fresh && result.Components.Heartbeat.Fresh
	return result
}

func (m *HealthMonitor) ReadyzHandler(w http.ResponseWriter, _ *http.Request) {
	readiness := m.Readiness(time.Now().UTC())
	w.Header().Set("Content-Type", "application/json")
	if !readiness.Status {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(readiness)
}
