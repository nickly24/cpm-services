// Package bff is the browser boundary. Shared sessions support multiple instances.
// Auth owns identity and revocation; domain services own authorization.
package bff

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Auth, Core, Homework, Tests, Exams, Ratings, Attendance, Cards, Uploads, Origin string
	AllowedOrigins                                                                  []string
	Secure                                                                          bool
	RedisAddress, RedisPassword                                                     string
	GatewayKey, HomeworkGatewayKey, AttendanceGatewayKey                            string
}
type Identity struct {
	User          json.RawMessage `json:"user"`
	Organizations json.RawMessage `json:"organizations"`
	Access        string          `json:"access_token,omitempty"`
	Refresh       string          `json:"refresh_token,omitempty"`
	Expires       int             `json:"expires_in,omitempty"`
}
type session struct {
	identity           Identity
	revision           string
	accessUntil, until time.Time
}
type Server struct {
	config      Config
	client      *http.Client
	store       sessionStore
	metricsOnce sync.Once
	metrics     *serviceMetrics
}

func New(c Config) *Server {
	store := sessionStore(newMemorySessionStore())
	if c.RedisAddress != "" {
		store = newRedisSessionStore(c.RedisAddress, c.RedisPassword)
	}
	return &Server{config: c, client: &http.Client{Timeout: 22 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, store: store}
}

func (s *Server) Close() error { return s.store.Close() }
func random() string {
	var b [32]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func reply(w http.ResponseWriter, code int, body any) {
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
func failure(w http.ResponseWriter, code int, msg string) {
	reply(w, code, map[string]any{"status": false, "error": msg})
}
func (s *Server) call(ctx context.Context, base, method, path, token string, body []byte, rid string, idempotency ...string) (int, []byte) {
	code, data, _ := s.callResponse(ctx, base, method, path, token, body, rid, idempotency...)
	return code, data
}
func (s *Server) callResponse(ctx context.Context, base, method, path, token string, body []byte, rid string, idempotency ...string) (int, []byte, string) {
	req, e := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
	if e != nil {
		return 503, nil, ""
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", rid)
	if len(idempotency) > 0 && idempotency[0] != "" {
		req.Header.Set("Idempotency-Key", idempotency[0])
	}
	if len(idempotency) > 1 && idempotency[1] != "" {
		req.Header.Set("Content-Type", idempotency[1])
	}
	if len(idempotency) > 2 && idempotency[2] != "" {
		req.Header.Set("X-Exam-Confirmation", idempotency[2])
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if err := s.attachGatewayContext(req, body); err != nil {
		return 502, nil, ""
	}
	res, e := s.client.Do(req)
	if e != nil {
		return 503, nil, ""
	}
	defer res.Body.Close()
	responseLimit := int64(8 << 20)
	if base != "" && (base == s.config.Exams || base == s.config.Ratings || base == s.config.Core || base == s.config.Cards) {
		responseLimit = 64 << 20
	}
	if base != "" && base == s.config.Uploads {
		responseLimit = 16 << 20
	}
	data, e := io.ReadAll(io.LimitReader(res.Body, responseLimit+1))
	if e != nil || int64(len(data)) > responseLimit {
		return 502, nil, ""
	}
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		return 502, nil, ""
	}
	if !json.Valid(data) {
		return 502, nil, ""
	}
	return res.StatusCode, data, safeRetryAfter(res.StatusCode, res.Header.Get("Retry-After"))
}
func upstream(w http.ResponseWriter, code int, data []byte, retryAfter ...string) {
	if len(retryAfter) > 0 {
		if value := safeRetryAfter(code, retryAfter[0]); value != "" {
			w.Header().Set("Retry-After", value)
		}
	}
	if code >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if !regexp.MustCompile(`^[a-z_]{1,100}$`).MatchString(e.Error) {
			e.Error = "upstream_unavailable"
		}
		failure(w, code, e.Error)
		return
	}
	w.WriteHeader(code)
	_, _ = w.Write(data)
}
func (s *Server) cookie(w http.ResponseWriter, id string) {
	age := 14 * 24 * 60 * 60
	if id == "" {
		age = -1
	}
	http.SetCookie(w, &http.Cookie{Name: "cpm_bff", Value: id, Path: "/bff", HttpOnly: true, Secure: s.config.Secure, SameSite: http.SameSiteStrictMode, MaxAge: age})
}
func identity(w http.ResponseWriter, v *session) {
	reply(w, 200, map[string]any{"status": true, "user": v.identity.User, "organizations": v.identity.Organizations, "context_revision": v.revision})
}
func (s *Server) token(v *session, raw []byte, preserve bool) bool {
	var d Identity
	if json.Unmarshal(raw, &d) != nil || d.Access == "" || len(d.User) == 0 || d.Expires <= 0 {
		return false
	}
	if preserve && d.Refresh == "" {
		d.Refresh = v.identity.Refresh
	}
	if d.Refresh == "" {
		return false
	}
	v.identity = d
	v.accessUntil = time.Now().Add(time.Duration(d.Expires) * time.Second)
	return true
}
func (s *Server) refresh(r *http.Request, v *session, rid string) (int, []byte, string) {
	b, _ := json.Marshal(map[string]string{"refresh_token": v.identity.Refresh})
	code, raw, retryAfter := s.callResponse(r.Context(), s.config.Auth, "POST", "/v1/auth/refresh", "", b, rid)
	if code == 200 && !s.token(v, raw, false) {
		return 502, nil, ""
	}
	return code, raw, retryAfter
}

var hwRoutes = regexp.MustCompile(`^(GET (types|assignments|assignments/[1-9][0-9]*(/students|/workspace)?|jobs/active|jobs/[a-fA-F0-9-]{36}|review-queue|archive|monitoring|submissions/[1-9][0-9]*/file-url|report|settings|audit)|POST (types|assignments|assignments/[1-9][0-9]*/(uploads|submit|manual|manual/bulk)|uploads/[a-fA-F0-9-]{36}/complete|jobs/[a-fA-F0-9-]{36}/(cancel|retry)|submissions/[1-9][0-9]*/(claim|takeover|release|request-revision|grade|edit-grade|resubmit))|PUT (types/[1-9][0-9]*|assignments/[1-9][0-9]*(/manual/[1-9][0-9]*)?|settings)|DELETE (types/[1-9][0-9]*|assignments/[1-9][0-9]*(/draft)?))$`)

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Path != "/metrics" {
		started := time.Now()
		measured := &measuredResponse{ResponseWriter: w}
		w = measured
		defer func() {
			status := measured.status
			if status == 0 {
				status = http.StatusOK
			}
			s.telemetry().observeRequest(r.Method, metricRoute(r.Method, r.URL.Path), status, time.Since(started).Seconds())
		}()
	}
	rid := random()[:24]
	w.Header().Set("X-Request-ID", rid)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == "GET" && r.URL.Path == "/metrics" {
		s.metricsHTTP(w, r)
		return
	}
	if r.Method == "GET" && (r.URL.Path == "/health/live" || r.URL.Path == "/health/ready") {
		if r.URL.Path == "/health/ready" {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if s.store.Ready(ctx) != nil {
				failure(w, 503, "session_store_unavailable")
				return
			}
		}
		reply(w, 200, map[string]any{"status": true, "service": "bff"})
		return
	}
	if r.Method == "GET" && r.URL.Path == "/openapi.json" {
		w.Write(openAPI)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/bff/") {
		failure(w, 404, "not_found")
		return
	}
	if s.cors(w, r) {
		return
	}
	if r.Header.Get("X-CPM-Request") != "1" {
		failure(w, 403, "request_header_required")
		return
	}
	origin := r.Header.Get("Origin")
	if (origin != "" && !s.allowedOrigin(origin)) || (r.Method != "GET" && !s.allowedOrigin(origin)) {
		failure(w, 403, "origin_forbidden")
		return
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		failure(w, 403, "origin_forbidden")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/bff/")
	tail, examRequest := examTail(path)
	publicExam := examRequest && examsRoute(r.Method, tail) != ""
	importUpload := publicExam && r.Method == "POST" && examsUpload.MatchString(tail)
	uploadTail, uploadRequest := strings.CutPrefix(path, "uploads/")
	publicUpload := uploadRequest && uploadsRoute(r.Method, uploadTail) != ""
	mediaType, mediaParams, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	multipart := (importUpload || publicUpload && r.Method == "POST" && uploadTail == "sessions") && mediaErr == nil && mediaType == "multipart/form-data" && mediaParams["boundary"] != ""
	bodyLimit := int64(1 << 20)
	if publicExam {
		bodyLimit = 18 << 20
	}
	if publicUpload {
		bodyLimit = (8 << 20) + (64 << 10)
	}
	if cardsRoute(r.Method, strings.TrimPrefix(path, "cards/")) != "" && strings.HasPrefix(path, "cards/") {
		bodyLimit = 10 << 20
	}
	if strings.HasPrefix(path, "attendance/") && attendanceRoute(r.Method, strings.TrimPrefix(path, "attendance/")) != "" {
		bodyLimit = 3 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, bodyLimit)
	body, e := io.ReadAll(r.Body)
	if e != nil {
		failure(w, 413, "body_too_large")
		return
	}
	if len(body) > 0 && !multipart && (!strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") || !json.Valid(body)) {
		failure(w, 400, "invalid_json")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	if path == "auth/login" && r.Method == "POST" {
		if id := cookieSessionID(r); id != "" {
			_, err := s.store.Load(ctx, id)
			if err == nil {
				failure(w, 409, "session_exists")
				return
			}
			if !errors.Is(err, errSessionMissing) {
				sessionFailure(w, err)
				return
			}
		}
		code, data := s.call(ctx, s.config.Auth, "POST", "/v1/auth/login", "", body, rid)
		if code != 200 {
			upstream(w, code, data)
			return
		}
		v := &session{revision: random(), until: time.Now().Add(14 * 24 * time.Hour)}
		if !s.token(v, data, false) {
			failure(w, 502, "invalid_upstream_response")
			return
		}
		id := random()
		if err := s.store.Create(ctx, id, v); err != nil {
			sessionFailure(w, err)
			return
		}
		s.cookie(w, id)
		identity(w, v)
		return
	}
	id := cookieSessionID(r)
	if id == "" {
		s.cookie(w, "")
		failure(w, 401, "session_expired")
		return
	}
	exclusive := r.Method == "POST" && (path == "auth/refresh" || path == "auth/context" || path == "auth/logout")
	lease, err := s.acquireSession(w, r, id, exclusive)
	if err != nil {
		return
	}
	defer func() { lease.Release() }()
	v := lease.value
	upgraded := false
	if path != "auth/me" && r.Header.Get("X-CPM-Context") != v.revision {
		failure(w, 409, "context_changed")
		return
	}
	// Only token rotation/context/logout serialize a session. Reader leases let
	// independent operations and tabs proceed together while a context switch
	// waits for all operations in the old cabinet to finish.
	if !exclusive && time.Until(v.accessUntil) < 15*time.Second {
		lease.Release()
		lease, err = s.acquireSession(w, r, id, true)
		if err != nil {
			return
		}
		v = lease.value
		upgraded = true
		if path != "auth/me" && r.Header.Get("X-CPM-Context") != v.revision {
			failure(w, 409, "context_changed")
			return
		}
	}
	if path == "auth/refresh" && r.Method == "POST" || time.Until(v.accessUntil) < 15*time.Second {
		code, data, retryAfter := s.refresh(r, v, rid)
		if code != 200 {
			if code == 401 {
				if err := lease.delete(ctx); err != nil {
					sessionFailure(w, err)
					return
				}
				s.cookie(w, "")
			}
			upstream(w, code, data, retryAfter)
			return
		}
		if err := lease.save(ctx, v); err != nil {
			sessionFailure(w, err)
			return
		}
		if path == "auth/refresh" {
			identity(w, v)
			return
		}
	}
	if upgraded {
		// Reacquire as a reader after an automatic refresh. The cabinet may have
		// changed between leases, so its revision is checked again below.
		lease.Release()
		lease, err = s.acquireSession(w, r, id, false)
		if err != nil {
			return
		}
		v = lease.value
		if path != "auth/me" && r.Header.Get("X-CPM-Context") != v.revision {
			failure(w, 409, "context_changed")
			return
		}
	}
	var code int
	var data []byte
	var retryAfter string
	// Context/logout authenticate inside Auth themselves. Other browser operations
	// still obtain live permissions once; Tests/Homework reuse the signed context.
	if !(r.Method == "POST" && (path == "auth/context" || path == "auth/logout")) {
		// No permission cache: each browser operation sees current auth state.
		code, data, retryAfter = s.callResponse(ctx, s.config.Auth, "GET", "/v1/auth/me", v.identity.Access, nil, rid)
		if code != 200 {
			if code == 401 {
				if err := lease.delete(ctx); err != nil {
					sessionFailure(w, err)
					return
				}
				s.cookie(w, "")
			}
			upstream(w, code, data, retryAfter)
			return
		}
		var current Identity
		if json.Unmarshal(data, &current) != nil {
			failure(w, 502, "invalid_upstream_response")
			return
		}
		v.identity.User = current.User
		v.identity.Organizations = current.Organizations
		ctx = withGatewayIdentity(ctx, current.User)
		ctx = context.WithValue(ctx, attendanceIdentityKey{}, append(json.RawMessage(nil), data...))
		r = r.WithContext(ctx)
		if path == "auth/me" && r.Method == "GET" {
			identity(w, v)
			return
		}
	}
	if path == "auth/context" && r.Method == "POST" {
		code, data = s.call(ctx, s.config.Auth, "POST", "/v1/auth/context", v.identity.Access, body, rid)
		if code != 200 {
			if code == 401 {
				if err := lease.delete(ctx); err != nil {
					sessionFailure(w, err)
					return
				}
				s.cookie(w, "")
			}
			upstream(w, code, data)
			return
		}
		if !s.token(v, data, true) {
			if err := lease.delete(ctx); err != nil {
				sessionFailure(w, err)
				return
			}
			s.cookie(w, "")
			failure(w, 502, "invalid_upstream_response")
			return
		}
		v.revision = random()
		if err := lease.save(ctx, v); err != nil {
			sessionFailure(w, err)
			return
		}
		identity(w, v)
		return
	}
	if path == "auth/logout" && r.Method == "POST" {
		code, data = s.call(ctx, s.config.Auth, "POST", "/v1/auth/logout", v.identity.Access, body, rid)
		if code == 200 || code == 401 {
			if err := lease.delete(ctx); err != nil {
				sessionFailure(w, err)
				return
			}
			s.cookie(w, "")
			reply(w, 200, map[string]bool{"status": true})
			return
		}
		upstream(w, code, data)
		return
	}
	var user struct {
		DivisionID *int64 `json:"division_id"`
	}
	_ = json.Unmarshal(v.identity.User, &user)
	if path == "exams/capabilities" && r.Method == "GET" {
		if len(r.URL.Query()) != 0 {
			failure(w, 400, "invalid_scope_parameter")
			return
		}
		reply(w, 200, map[string]any{"status": true, "data": examsCapabilities(v.identity.User)})
		return
	}

	var cardsUser struct {
		Role string `json:"role"`
	}
	_ = json.Unmarshal(v.identity.User, &cardsUser)
	if r.Method == "GET" && cardsUser.Role == "student" && strings.HasPrefix(path, "cards/directions/") && strings.HasSuffix(path, "/sections") && cardsRoute("GET", strings.TrimPrefix(path, "cards/")) != "" {
		s.cardsCatalogPage(w, r, path, v.identity.Access, rid, user.DivisionID)
		return
	}
	base, up, entity := "", "", ""
	if tail, ok := strings.CutPrefix(path, "homework/"); ok && hwRoutes.MatchString(r.Method+" "+tail) {
		base = s.config.Homework
		up = "/v1/homework/divisions/%d/" + tail
	}
	if tail, ok := strings.CutPrefix(path, "core/"); ok && coreRoute(r.Method, tail) != "" {
		if !coreQuery(r.Method, tail, r.URL.Query()) {
			failure(w, 400, "invalid_scope_parameter")
			return
		}
		base, up = s.config.Core, coreRoute(r.Method, tail)
	}
	if tail, ok := strings.CutPrefix(path, "cards/"); ok && cardsRoute(r.Method, tail) != "" {
		if !cardsQuery(r.Method, tail, r.URL.Query()) {
			failure(w, 400, "invalid_scope_parameter")
			return
		}
		base, up = s.config.Cards, cardsRoute(r.Method, tail)
	}
	if tail, ok := strings.CutPrefix(path, "tests/"); ok {
		if route, scoped := testsRoute(r.Method, tail); route != "" {
			base, up, entity = s.config.Tests, route, scoped
		}
	}
	if tail, ok := strings.CutPrefix(path, "ratings/"); ok && ratingsRoute(r.Method, tail) != "" {
		if !ratingsQuery(r.Method, tail, r.URL.Query()) {
			failure(w, 400, "invalid_scope_parameter")
			return
		}
		base, up = s.config.Ratings, ratingsRoute(r.Method, tail)
	}
	if publicUpload {
		if !uploadsQuery(r.Method, uploadTail, r.URL.Query()) {
			failure(w, 400, "invalid_scope_parameter")
			return
		}
		base, up = s.config.Uploads, uploadsRoute(r.Method, uploadTail)
	}
	if tail, ok := strings.CutPrefix(path, "attendance/"); ok && attendanceRoute(r.Method, tail) != "" {
		if attendanceReceipt.MatchString(tail) && (len(body) != 0 || r.URL.RawQuery != "" || r.URL.ForceQuery) {
			failure(w, 400, "invalid_receipt_request")
			return
		}
		if !attendanceQuery(r.Method, tail, r.URL.Query()) {
			failure(w, 400, "invalid_scope_parameter")
			return
		}
		base, up = s.config.Attendance, attendanceRoute(r.Method, tail)
		if r.Method == "GET" && attendanceImage.MatchString(tail) && base != "" {
			s.attendanceImageResponse(w, r, up, v.identity.Access, rid)
			return
		}
	}
	if publicExam {
		if r.Method == "GET" && examsTemplate.MatchString(tail) && len(r.URL.Query()) != 0 {
			failure(w, 400, "invalid_scope_parameter")
			return
		}
		if user.DivisionID == nil {
			failure(w, 409, "division_required")
			return
		}
		base, up = s.config.Exams, examsRoute(r.Method, tail)
	}
	if base == "" {
		failure(w, 501, "section_not_connected")
		return
	}
	if strings.Contains(up, "%d") {
		if user.DivisionID == nil {
			failure(w, 409, "division_required")
			return
		}
		up = fmt.Sprintf(up, *user.DivisionID)
	}
	if publicUpload && r.Method == "GET" && uploadsDownload.MatchString(uploadTail) {
		s.uploadsFileResponse(w, r, up, v.identity.Access, rid)
		return
	}
	// Scope query overrides are forbidden. Explicit target divisions on the
	// Core membership and Attendance paths are reauthorized by their owners.
	for _, k := range []string{"organization_id", "division_id", "url"} {
		if r.URL.Query().Has(k) {
			failure(w, 400, "invalid_scope_parameter")
			return
		}
	}
	query := r.URL.Query()
	if strings.HasPrefix(path, "tests/") {
		if query.Has("target_division_id") && (user.DivisionID == nil || query.Get("target_division_id") != fmt.Sprint(*user.DivisionID)) {
			failure(w, 400, "invalid_scope_parameter")
			return
		}
		if r.Method == "GET" && (path == "tests/bank" || testIDPath.MatchString(strings.TrimPrefix(path, "tests/"))) {
			if user.DivisionID == nil {
				failure(w, 409, "division_required")
				return
			}
			query.Set("target_division_id", fmt.Sprint(*user.DivisionID))
		}
	}
	// Attempts and submissions retain their original division. Even a full admin
	// must select that cabinet before reading or changing an individual record.
	if entity != "" && !(base == s.config.Tests && s.config.GatewayKey != "") {
		code, data, retryAfter = s.callResponse(ctx, base, "GET", entity, v.identity.Access, nil, rid)
		if code != 200 {
			upstream(w, code, data, retryAfter)
			return
		}
		if !inSelectedDivision(data, user.DivisionID) {
			failure(w, 404, "not_found")
			return
		}
		if r.Method == "GET" && up == entity {
			upstream(w, code, testsBrowserLinks(data))
			return
		}
	}
	if len(query) > 0 {
		up += "?" + query.Encode()
	}
	if r.Method == "GET" && (publicExam && examsTemplate.MatchString(strings.TrimPrefix(path, "exams/")) || publicUpload && uploadsTemplate.MatchString(uploadTail)) {
		s.templateResponse(w, r, base, up, v.identity.Access, rid)
		return
	}
	contentType, confirmation := "", ""
	if publicUpload && multipart {
		contentType = r.Header.Get("Content-Type")
	}
	if publicExam {
		if multipart {
			contentType = r.Header.Get("Content-Type")
		}
		confirmation = r.Header.Get("X-Exam-Confirmation")
	}
	code, data, retryAfter = s.callResponse(ctx, base, r.Method, up, v.identity.Access, body, rid, r.Header.Get("Idempotency-Key"), contentType, confirmation)
	if strings.HasPrefix(path, "tests/") && code < 300 {
		data = testsBrowserLinks(data)
	}
	if strings.HasPrefix(path, "ratings/") && code < 300 {
		data = ratingsBrowserLinks(data)
	}
	upstream(w, code, data, retryAfter)
}
