package bff

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sync"
	"time"
)

var errSessionMissing = errors.New("session missing")
var errSessionLeaseLost = errors.New("session lease lost")
var sessionID = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Requests have a 25-second deadline. A crashed process cannot hold the cabinet
// indefinitely; lease expiry is longer than that deadline. Save/delete require
// ownership, so an expired writer cannot overwrite a newer session.
const sessionLeaseDuration = 45 * time.Second

type sessionStore interface {
	Create(context.Context, string, *session) error
	Load(context.Context, string) (*session, error)
	Acquire(context.Context, string, bool) (*sessionLease, error)
	Ready(context.Context) error
	Close() error
}

type sessionLease struct {
	value   *session
	save    func(context.Context, *session) error
	delete  func(context.Context) error
	release func()
}

func (l *sessionLease) Release() {
	if l != nil && l.release != nil {
		l.release()
		l.release = nil
	}
}

type storedSession struct {
	Identity    Identity  `json:"identity"`
	Revision    string    `json:"revision"`
	AccessUntil time.Time `json:"access_until"`
	Until       time.Time `json:"until"`
}

func encodeSession(v *session) ([]byte, error) {
	return json.Marshal(storedSession{v.identity, v.revision, v.accessUntil, v.until})
}

func decodeSession(raw []byte) (*session, error) {
	var v storedSession
	if json.Unmarshal(raw, &v) != nil || v.Revision == "" || v.Identity.Access == "" || v.Identity.Refresh == "" {
		return nil, errors.New("invalid stored session")
	}
	if !v.Until.After(time.Now()) {
		return nil, errSessionMissing
	}
	return &session{identity: v.Identity, revision: v.Revision, accessUntil: v.AccessUntil, until: v.Until}, nil
}

func cookieSessionID(r *http.Request) string {
	c, err := r.Cookie("cpm_bff")
	if err != nil || !sessionID.MatchString(c.Value) {
		return ""
	}
	return c.Value
}

func sessionFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		failure(w, 504, "request_timeout")
		return
	}
	failure(w, 503, "session_store_unavailable")
}

func (s *Server) acquireSession(w http.ResponseWriter, r *http.Request, id string, exclusive bool) (*sessionLease, error) {
	l, err := s.store.Acquire(r.Context(), id, exclusive)
	if err == nil {
		return l, nil
	}
	if errors.Is(err, errSessionMissing) {
		s.cookie(w, "")
		failure(w, 401, "session_expired")
	} else {
		sessionFailure(w, err)
	}
	return nil, err
}

// This store is for isolated unit tests and embedders. The executable always
// configures Redis; a Redis outage never silently falls back to process memory.
type memorySessionStore struct {
	mu      sync.Mutex
	entries map[string]*memorySession
}

type memorySession struct {
	mu      sync.Mutex
	raw     []byte
	readers int
	writing bool
	waiters int
	changed chan struct{}
}

func newMemorySessionStore() *memorySessionStore {
	return &memorySessionStore{entries: make(map[string]*memorySession)}
}

func (m *memorySessionStore) entry(id string) *memorySession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.entries[id]
}

func (e *memorySession) changedLocked() {
	close(e.changed)
	e.changed = make(chan struct{})
}

func (m *memorySessionStore) Create(ctx context.Context, id string, v *session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := encodeSession(v)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries[id] != nil {
		return errors.New("session already exists")
	}
	m.entries[id] = &memorySession{raw: raw, changed: make(chan struct{})}
	return nil
}

func (m *memorySessionStore) Load(ctx context.Context, id string) (*session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e := m.entry(id)
	if e == nil {
		return nil, errSessionMissing
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.raw == nil {
		return nil, errSessionMissing
	}
	return decodeSession(e.raw)
}

func (m *memorySessionStore) Acquire(ctx context.Context, id string, exclusive bool) (*sessionLease, error) {
	e := m.entry(id)
	if e == nil {
		return nil, errSessionMissing
	}
	e.mu.Lock()
	if exclusive {
		e.waiters++
	}
	for e.writing || exclusive && e.readers != 0 || !exclusive && e.waiters != 0 {
		changed := e.changed
		e.mu.Unlock()
		select {
		case <-ctx.Done():
			e.mu.Lock()
			if exclusive {
				e.waiters--
				e.changedLocked()
			}
			e.mu.Unlock()
			return nil, ctx.Err()
		case <-changed:
			e.mu.Lock()
		}
	}
	if exclusive {
		e.waiters--
	}
	if e.raw == nil {
		e.changedLocked()
		e.mu.Unlock()
		return nil, errSessionMissing
	}
	v, err := decodeSession(e.raw)
	if err != nil {
		e.changedLocked()
		e.mu.Unlock()
		return nil, err
	}
	if exclusive {
		e.writing = true
	} else {
		e.readers++
	}
	e.mu.Unlock()
	l := &sessionLease{value: v}
	l.save = func(c context.Context, next *session) error {
		if !exclusive {
			return errSessionLeaseLost
		}
		if err := c.Err(); err != nil {
			return err
		}
		raw, err := encodeSession(next)
		if err != nil {
			return err
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		e.raw = raw
		return nil
	}
	l.delete = func(c context.Context) error {
		if err := c.Err(); err != nil {
			return err
		}
		e.mu.Lock()
		e.raw = nil
		e.changedLocked()
		e.mu.Unlock()
		m.mu.Lock()
		delete(m.entries, id)
		m.mu.Unlock()
		return nil
	}
	l.release = func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if exclusive {
			e.writing = false
		} else {
			e.readers--
		}
		e.changedLocked()
	}
	return l, nil
}

func (m *memorySessionStore) Ready(context.Context) error { return nil }
func (m *memorySessionStore) Close() error                { return nil }
