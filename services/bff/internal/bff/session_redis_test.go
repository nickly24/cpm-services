package bff

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func testSession() *session {
	return &session{
		identity: Identity{User: json.RawMessage(`{"division_id":1001}`), Organizations: json.RawMessage(`[]`), Access: "access", Refresh: "refresh", Expires: 900},
		revision: random(), accessUntil: time.Now().Add(15 * time.Minute), until: time.Now().Add(14 * 24 * time.Hour),
	}
}

func TestRedisSharedSessionTTLAndSnapshots(t *testing.T) {
	r := miniredis.RunT(t)
	a, b := newRedisSessionStore(r.Addr(), ""), newRedisSessionStore(r.Addr(), "")
	t.Cleanup(func() { a.Close(); b.Close() })
	id, v := random(), testSession()
	if err := a.Create(context.Background(), id, v); err != nil {
		t.Fatal(err)
	}
	got, err := b.Load(context.Background(), id)
	if err != nil || got.revision != v.revision || got.identity.Refresh != v.identity.Refresh {
		t.Fatalf("session not shared: %v", err)
	}
	got.identity.User[2] = 'x'
	if got, err = a.Load(context.Background(), id); err != nil || !json.Valid(got.identity.User) {
		t.Fatal("a reader mutated stored identity")
	}
	ttl := r.TTL(sessionKeys(id)[0])
	if ttl <= 13*24*time.Hour || ttl > 14*24*time.Hour {
		t.Fatal("session TTL is not bounded to fourteen days")
	}
	r.FastForward(14*24*time.Hour + time.Second)
	if _, err := b.Load(context.Background(), id); !errors.Is(err, errSessionMissing) {
		t.Fatal("expired session accepted", err)
	}
}

func TestRedisReadersParallelWriterFencedAndCancellation(t *testing.T) {
	r := miniredis.RunT(t)
	a, b := newRedisSessionStore(r.Addr(), ""), newRedisSessionStore(r.Addr(), "")
	t.Cleanup(func() { a.Close(); b.Close() })
	ctx := context.Background()
	id, v := random(), testSession()
	if err := a.Create(ctx, id, v); err != nil {
		t.Fatal(err)
	}
	first, err := a.Acquire(ctx, id, false)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	second, err := b.Acquire(ctx, id, false)
	if err != nil {
		t.Fatal("readers serialized", err)
	}
	defer second.Release()
	type acquired struct {
		lease *sessionLease
		err   error
	}
	waiting := make(chan acquired, 1)
	writerCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	go func() { l, e := b.Acquire(writerCtx, id, true); waiting <- acquired{l, e} }()
	deadline := time.Now().Add(time.Second)
	for !r.Exists(sessionKeys(id)[2]) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !r.Exists(sessionKeys(id)[2]) {
		t.Fatal("writer did not reserve session")
	}
	lateCtx, lateCancel := context.WithTimeout(ctx, 35*time.Millisecond)
	defer lateCancel()
	if l, err := a.Acquire(lateCtx, id, false); err == nil || !errors.Is(err, context.DeadlineExceeded) {
		if l != nil {
			l.Release()
		}
		t.Fatal("queued writer allowed a new reader", err)
	}
	select {
	case <-waiting:
		t.Fatal("writer entered while readers were running")
	default:
	}
	first.Release()
	second.Release()
	got := <-waiting
	if got.err != nil {
		t.Fatal(got.err)
	}
	got.lease.value.revision = random()
	if err := got.lease.save(ctx, got.lease.value); err != nil {
		t.Fatal(err)
	}
	got.lease.Release()
	loaded, err := a.Load(ctx, id)
	if err != nil || loaded.revision != got.lease.value.revision {
		t.Fatal("writer update not shared", err)
	}
	stale, err := a.Acquire(ctx, id, true)
	if err != nil {
		t.Fatal(err)
	}
	r.FastForward(sessionLeaseDuration + time.Second)
	newer, err := b.Acquire(ctx, id, true)
	if err != nil {
		t.Fatal(err)
	}
	defer newer.Release()
	if err := stale.save(ctx, stale.value); !errors.Is(err, errSessionLeaseLost) {
		t.Fatal("expired writer overwrote session", err)
	}
	if err := stale.delete(ctx); !errors.Is(err, errSessionLeaseLost) {
		t.Fatal("expired writer deleted session", err)
	}
	stale.Release()
	if err := newer.save(ctx, newer.value); err != nil {
		t.Fatal("stale cleanup released newer writer", err)
	}
}

type redisAuthFixture struct {
	mu                  sync.Mutex
	access, refresh     string
	division, rotations int
	contextStarted      chan struct{}
	domainStarted       chan struct{}
	domainRelease       chan struct{}
}

func (f *redisAuthFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if strings.HasPrefix(r.URL.Path, "/v1/homework/") {
		if f.domainStarted != nil {
			f.domainStarted <- struct{}{}
			<-f.domainRelease
		}
		w.Write([]byte(`{"status":true,"data":{"items":[]}}`))
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	ident := func(tokens bool) {
		v := map[string]any{"status": true, "user": map[string]any{"account_id": 9, "organization_id": 1001, "division_id": f.division, "role": "student"}, "organizations": []any{}}
		if tokens {
			v["access_token"], v["refresh_token"], v["expires_in"] = f.access, f.refresh, 900
		}
		json.NewEncoder(w).Encode(v)
	}
	switch r.URL.Path {
	case "/v1/auth/login":
		ident(true)
	case "/v1/auth/refresh":
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["refresh_token"] != f.refresh {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"invalid_token"}`))
			return
		}
		f.rotations++
		f.access, f.refresh = random(), random()
		ident(true)
	case "/v1/auth/me":
		if r.Header.Get("Authorization") != "Bearer "+f.access {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"invalid_token"}`))
			return
		}
		ident(false)
	case "/v1/auth/context":
		if f.contextStarted != nil {
			f.contextStarted <- struct{}{}
		}
		f.division, f.access = 1002, random()
		ident(true)
	case "/v1/auth/logout":
		w.Write([]byte(`{"status":true}`))
	default:
		w.WriteHeader(404)
		w.Write([]byte(`{"error":"not_found"}`))
	}
}

func redisBrowserRequest(s *Server, method, path, revision string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
	r.Header.Set("Origin", s.config.Origin)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CPM-Request", "1")
	r.Header.Set("X-CPM-Context", revision)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func redisRevision(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "access_token") || strings.Contains(w.Body.String(), "refresh_token") {
		t.Fatal("credential leak")
	}
	var v struct {
		Revision string `json:"context_revision"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || v.Revision == "" {
		t.Fatal("missing context revision")
	}
	return v.Revision
}

func TestRedisBFFRestartAndCrossInstanceRefreshLogout(t *testing.T) {
	r := miniredis.RunT(t)
	f := &redisAuthFixture{access: random(), refresh: random(), division: 1001}
	auth := httptest.NewServer(f)
	defer auth.Close()
	config := Config{Auth: auth.URL, Homework: auth.URL, Origin: "http://127.0.0.1:3001", RedisAddress: r.Addr()}
	a, b := New(config), New(config)
	defer b.Close()
	w := redisBrowserRequest(a, "POST", "/bff/auth/login", "", nil)
	revision, cookie := redisRevision(t, w), w.Result().Cookies()[0]
	a.Close()
	restarted := New(config)
	defer restarted.Close()
	if redisRevision(t, redisBrowserRequest(restarted, "GET", "/bff/auth/me", "", cookie)) != revision {
		t.Fatal("restart changed session")
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := b
			if i%2 == 0 {
				s = restarted
			}
			w := redisBrowserRequest(s, "POST", "/bff/auth/refresh", revision, cookie)
			if w.Code != 200 {
				t.Errorf("refresh failed: %d", w.Code)
			}
		}(i)
	}
	wg.Wait()
	f.mu.Lock()
	rotations := f.rotations
	f.mu.Unlock()
	if rotations != 12 {
		t.Fatal("lost or replayed token rotation", rotations)
	}
	if w := redisBrowserRequest(b, "POST", "/bff/auth/logout", revision, cookie); w.Code != 200 {
		t.Fatal("logout failed", w.Code)
	}
	if w := redisBrowserRequest(restarted, "GET", "/bff/auth/me", "", cookie); w.Code != 401 {
		t.Fatal("logout was not shared", w.Code)
	}
}

func TestRedisAutomaticRefreshCoalescesAcrossReplicas(t *testing.T) {
	r := miniredis.RunT(t)
	f := &redisAuthFixture{access: random(), refresh: random(), division: 1001}
	auth := httptest.NewServer(f)
	defer auth.Close()
	config := Config{Auth: auth.URL, Homework: auth.URL, Origin: "http://127.0.0.1:3001", RedisAddress: r.Addr()}
	a, b := New(config), New(config)
	defer a.Close()
	defer b.Close()
	w := redisBrowserRequest(a, "POST", "/bff/auth/login", "", nil)
	redisRevision(t, w)
	cookie := w.Result().Cookies()[0]
	l, err := a.store.Acquire(context.Background(), cookie.Value, true)
	if err != nil {
		t.Fatal(err)
	}
	l.value.accessUntil = time.Now().Add(time.Second)
	if err := l.save(context.Background(), l.value); err != nil {
		t.Fatal(err)
	}
	l.Release()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := a
			if i%2 == 0 {
				s = b
			}
			if w := redisBrowserRequest(s, "GET", "/bff/auth/me", "", cookie); w.Code != 200 {
				t.Errorf("automatic refresh failed: %d", w.Code)
			}
		}(i)
	}
	wg.Wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rotations != 1 {
		t.Fatal("automatic refresh amplified across replicas", f.rotations)
	}
}

func TestRedisConcurrentContextChangesAcceptOnlyCurrentRevision(t *testing.T) {
	r := miniredis.RunT(t)
	f := &redisAuthFixture{access: random(), refresh: random(), division: 1001}
	auth := httptest.NewServer(f)
	defer auth.Close()
	config := Config{Auth: auth.URL, Homework: auth.URL, Origin: "http://127.0.0.1:3001", RedisAddress: r.Addr()}
	a, b := New(config), New(config)
	defer a.Close()
	defer b.Close()
	w := redisBrowserRequest(a, "POST", "/bff/auth/login", "", nil)
	revision, cookie := redisRevision(t, w), w.Result().Cookies()[0]
	done := make(chan *httptest.ResponseRecorder, 2)
	go func() { done <- redisBrowserRequest(a, "POST", "/bff/auth/context", revision, cookie) }()
	go func() { done <- redisBrowserRequest(b, "POST", "/bff/auth/context", revision, cookie) }()
	accepted, rejected := 0, 0
	newRevision := ""
	for i := 0; i < 2; i++ {
		w := <-done
		switch w.Code {
		case 200:
			accepted++
			newRevision = redisRevision(t, w)
		case 409:
			rejected++
		default:
			t.Fatal("unexpected context race result", w.Code)
		}
	}
	if accepted != 1 || rejected != 1 || newRevision == revision {
		t.Fatal("context race lost its fence")
	}
	if got := redisRevision(t, redisBrowserRequest(a, "GET", "/bff/auth/me", "", cookie)); got != newRevision {
		t.Fatal("replica did not observe new context")
	}
}

func TestRedisBFFParallelOperationsAndContextFence(t *testing.T) {
	r := miniredis.RunT(t)
	f := &redisAuthFixture{access: random(), refresh: random(), division: 1001, contextStarted: make(chan struct{}, 1), domainStarted: make(chan struct{}, 2), domainRelease: make(chan struct{})}
	auth := httptest.NewServer(f)
	defer auth.Close()
	config := Config{Auth: auth.URL, Homework: auth.URL, Origin: "http://127.0.0.1:3001", RedisAddress: r.Addr()}
	a, b := New(config), New(config)
	defer a.Close()
	defer b.Close()
	w := redisBrowserRequest(a, "POST", "/bff/auth/login", "", nil)
	revision, cookie := redisRevision(t, w), w.Result().Cookies()[0]
	done := make(chan *httptest.ResponseRecorder, 2)
	go func() { done <- redisBrowserRequest(a, "GET", "/bff/homework/assignments", revision, cookie) }()
	go func() { done <- redisBrowserRequest(b, "GET", "/bff/homework/assignments", revision, cookie) }()
	for i := 0; i < 2; i++ {
		select {
		case <-f.domainStarted:
		case <-time.After(2 * time.Second):
			close(f.domainRelease)
			t.Fatal("ordinary requests serialized")
		}
	}
	contextDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { contextDone <- redisBrowserRequest(b, "POST", "/bff/auth/context", revision, cookie) }()
	select {
	case <-f.contextStarted:
		close(f.domainRelease)
		t.Fatal("context changed during old-cabinet operations")
	case <-time.After(50 * time.Millisecond):
	}
	close(f.domainRelease)
	for i := 0; i < 2; i++ {
		if w := <-done; w.Code != 200 {
			t.Fatal("old operation failed", w.Code)
		}
	}
	newRevision := redisRevision(t, <-contextDone)
	if newRevision == revision {
		t.Fatal("context fence did not change")
	}
	if w := redisBrowserRequest(a, "GET", "/bff/homework/assignments", revision, cookie); w.Code != 409 {
		t.Fatal("other replica accepted stale cabinet", w.Code)
	}
}

func TestRedisOutagePreservesCookieAndFailsReadiness(t *testing.T) {
	r := miniredis.RunT(t)
	f := &redisAuthFixture{access: random(), refresh: random(), division: 1001}
	auth := httptest.NewServer(f)
	defer auth.Close()
	s := New(Config{Auth: auth.URL, Origin: "http://127.0.0.1:3001", RedisAddress: r.Addr()})
	defer s.Close()
	w := redisBrowserRequest(s, "POST", "/bff/auth/login", "", nil)
	revision, cookie := redisRevision(t, w), w.Result().Cookies()[0]
	r.Close()
	w = redisBrowserRequest(s, "GET", "/bff/auth/me", revision, cookie)
	if w.Code != 503 || len(w.Result().Cookies()) != 0 {
		t.Fatal("Redis outage cleared valid browser cookie or became unauthorized", w.Code)
	}
	for path, expected := range map[string]int{"/health/live": 200, "/health/ready": 503} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != expected {
			t.Fatal(path, w.Code)
		}
	}
}

// Optional repeatable integration check against a real Redis. It touches only
// its random session keys; it never flushes the DB or reads existing sessions.
func TestRedisRealServer(t *testing.T) {
	address := os.Getenv("BFF_TEST_REDIS_ADDR")
	if address == "" {
		t.Skip("set BFF_TEST_REDIS_ADDR for a real Redis integration check")
	}
	a, b := newRedisSessionStore(address, os.Getenv("BFF_TEST_REDIS_PASSWORD")), newRedisSessionStore(address, os.Getenv("BFF_TEST_REDIS_PASSWORD"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id, v := random(), testSession()
	t.Cleanup(func() {
		c, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		b.client.Del(c, sessionKeys(id)...)
		a.Close()
		b.Close()
	})
	if err := a.Ready(ctx); err != nil {
		t.Fatal("Redis unavailable")
	}
	if err := a.Create(ctx, id, v); err != nil {
		t.Fatal("Redis session creation failed")
	}
	first, err := a.Acquire(ctx, id, false)
	if err != nil {
		t.Fatal("first Redis reader failed")
	}
	second, err := b.Acquire(ctx, id, false)
	if err != nil {
		first.Release()
		t.Fatal("second Redis reader failed")
	}
	first.Release()
	second.Release()
	writer, err := b.Acquire(ctx, id, true)
	if err != nil {
		t.Fatal("Redis writer failed")
	}
	writer.value.revision = random()
	if err := writer.save(ctx, writer.value); err != nil {
		writer.Release()
		t.Fatal("Redis session update failed")
	}
	writer.Release()
	loaded, err := a.Load(ctx, id)
	if err != nil || loaded.revision != writer.value.revision {
		t.Fatal("Redis update not shared")
	}
	writer, err = a.Acquire(ctx, id, true)
	if err != nil {
		t.Fatal("Redis writer reacquisition failed")
	}
	if err := writer.delete(ctx); err != nil {
		writer.Release()
		t.Fatal("Redis logout failed")
	}
	writer.Release()
	if _, err := b.Load(ctx, id); !errors.Is(err, errSessionMissing) {
		t.Fatal("Redis logout not shared")
	}
}
