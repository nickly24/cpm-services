package exams

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	DB           *sql.DB
	Deps         IdentityProvider
	InternalKey  string
	OperationKey string
}
type mutation func(*sql.Tx, *Exam) (any, error)

func respond(w http.ResponseWriter, code int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "data": data})
}
func failure(w http.ResponseWriter, err error) {
	var e *APIError
	if !errors.As(err, &e) {
		e = &APIError{503, "storage_unavailable"}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": false, "error": e.Code})
}
func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, map[string]any{"service": "exams"}) })
	m.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		if s.DB.PingContext(r.Context()) != nil {
			failure(w, &APIError{503, "storage_unavailable"})
			return
		}
		var n int
		if s.DB.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM exams_source_v1").Scan(&n) != nil {
			failure(w, &APIError{503, "schema_not_ready"})
			return
		}
		if e := s.Deps.Ready(r); e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, map[string]any{"service": "exams"})
	})
	m.HandleFunc("GET /openapi.json", serveOpenAPI)
	m.HandleFunc("GET /v1/exams", s.catalog)
	m.HandleFunc("POST /v1/exams", s.newExam)
	m.HandleFunc("GET /internal/exams/rating-snapshot", s.snapshot)
	m.HandleFunc("GET /v1/exams/{exam}/imports/{kind}/template", s.importTemplateRoute)
	m.HandleFunc("/v1/exams/{exam}/imports/{kind}", s.importRoute)
	m.HandleFunc("/v1/exams/{exam}/imports/{kind}/{session}", s.importRoute)
	m.HandleFunc("POST /v1/exams/{exam}/imports/{kind}/{session}/commit", s.importRoute)
	m.HandleFunc("GET /v1/exams/{exam}/imports/{kind}/{session}/receipts/{receipt}", s.importReceiptRoute)
	m.HandleFunc("/v1/exams/{exam}/training", s.trainingRoute)
	m.HandleFunc("PUT /v1/exams/{exam}/training/settings", s.trainingRoute)
	m.HandleFunc("PUT /v1/exams/{exam}/training/progress/{question}", s.trainingRoute)
	m.HandleFunc("DELETE /v1/exams/{exam}/training/progress/{question}", s.trainingRoute)
	routes := []struct{ pattern, op string }{
		{"GET /v1/exams/{exam}", "detail"}, {"GET /v1/exams/{exam}/config", "config"}, {"PUT /v1/exams/{exam}/config", "config"},
		{"GET /v1/exams/{exam}/parts", "parts"}, {"POST /v1/exams/{exam}/parts", "part-create"}, {"PUT /v1/exams/{exam}/parts/{part}", "part-update"}, {"DELETE /v1/exams/{exam}/parts/{part}", "part-delete"},
		{"GET /v1/exams/{exam}/questions", "questions"}, {"POST /v1/exams/{exam}/questions", "question-create"}, {"PUT /v1/exams/{exam}/questions/{question}", "question-update"}, {"DELETE /v1/exams/{exam}/questions/{question}", "question-delete"},
		{"GET /v1/exams/{exam}/commissions", "commissions"}, {"POST /v1/exams/{exam}/commissions", "commission-create"}, {"PUT /v1/exams/{exam}/commissions/{commission}", "commission-update"}, {"DELETE /v1/exams/{exam}/commissions/{commission}", "commission-delete"},
		{"GET /v1/exams/{exam}/assignments", "assignments"}, {"POST /v1/exams/{exam}/assignments", "assignment-create"}, {"PUT /v1/exams/{exam}/assignments/{assignment}", "assignment-update"}, {"DELETE /v1/exams/{exam}/assignments/{assignment}", "assignment-delete"},
		{"GET /v1/exams/{exam}/privileges", "privileges"}, {"PUT /v1/exams/{exam}/privileges/{student}", "privilege-put"}, {"DELETE /v1/exams/{exam}/privileges/{student}", "privilege-delete"},
		{"GET /v1/exams/{exam}/readiness", "readiness"}, {"GET /v1/exams/{exam}/attempts", "attempts"}, {"POST /v1/exams/{exam}/assignments/{assignment}/attempts/ensure", "ensure"},
		{"GET /v1/exams/{exam}/attempts/{attempt}", "attempt"}, {"GET /v1/exams/{exam}/attempts/{attempt}/protocol", "protocol"}, {"GET /v1/exams/{exam}/attempts/{attempt}/questions/{presented}/rounds", "rounds"},
		{"POST /v1/exams/{exam}/attempts/{attempt}/ready", "ready"}, {"POST /v1/exams/{exam}/attempts/{attempt}/start", "start"}, {"POST /v1/exams/{exam}/attempts/{attempt}/vote", "vote"}, {"POST /v1/exams/{exam}/attempts/{attempt}/next-question", "next-question"}, {"POST /v1/exams/{exam}/attempts/{attempt}/replace-question", "replace-question"},
		{"POST /v1/exams/{exam}/attempts/{attempt}/appeals", "appeal"}, {"POST /v1/exams/{exam}/retakes/{student}", "retake"},
		{"GET /v1/exams/{exam}/results", "results"}, {"GET /v1/exams/{exam}/results/{student}", "result"}, {"POST /v1/exams/{exam}/outside-results", "outside-create"}, {"PUT /v1/exams/{exam}/outside-results/{result}", "outside-update"}, {"DELETE /v1/exams/{exam}/outside-results/{result}", "outside-delete"},
		{"GET /v1/exams/{exam}/history/{student}/delete-preview", "history-preview"}, {"DELETE /v1/exams/{exam}/history/{student}", "history-delete"},
		{"PUT /v1/exams/{exam}/metadata", "metadata"}, {"PUT /v1/exams/{exam}/bank", "bank"}, {"PUT /v1/exams/{exam}/direction", "direction"}, {"POST /v1/exams/{exam}/connections", "connect"}, {"DELETE /v1/exams/{exam}/connections/{division}", "disconnect"}, {"POST /v1/exams/{exam}/copy", "copy"}, {"GET /v1/exams/{exam}/delete-preview", "delete-preview"}, {"DELETE /v1/exams/{exam}", "delete"},
	}
	for _, route := range routes {
		op := route.op
		m.HandleFunc(route.pattern, func(w http.ResponseWriter, r *http.Request) { s.operation(w, r, op) })
	}
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { failure(w, Missing) })
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Correlation-ID", UUID())
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		r.Body = http.MaxBytesReader(w, r.Body, 18<<20)
		defer func() {
			if recover() != nil {
				failure(w, &APIError{500, "internal_error"})
			}
		}()
		var err error
		r, err = s.authorizeOperationRequest(r)
		if err != nil {
			failure(w, err)
			return
		}
		m.ServeHTTP(w, r)
	})
}
func (s *Server) identity(r *http.Request) (Actor, error) {
	if op := requestOperation(r); op != nil {
		return op.Actor, nil
	}
	a, e := s.Deps.Identify(r)
	if e != nil {
		return a, e
	}
	if a.Role != "student" && a.Role != "examinator" && !a.Can(false) {
		return a, Denied
	}
	return a, nil
}
func params(r *http.Request, a Actor) (Params, error) {
	p := Params{Now: time.Now().UTC(), Query: map[string]string{}}
	if a.DivisionID != nil {
		p.DivisionID = *a.DivisionID
	}
	for key, values := range r.URL.Query() {
		if len(values) != 1 {
			return p, Bad("duplicate_query_parameter")
		}
		if !oneOf(key, "limit", "offset", "q", "type", "direction_id", "scope", "sort", "expected_revision", "part_id", "assignment_id", "student_account_id", "status", "through_sequence_no", "through_round_no", "preview", "date_from", "date_to", "from_date", "to_date", "as_of") {
			return p, Bad("unknown_query_parameter")
		}
		p.Query[key] = values[0]
	}
	for key, target := range map[string]*int64{"part": &p.PartID, "question": &p.QuestionID, "commission": &p.CommissionID, "assignment": &p.AssignmentID, "attempt": &p.AttemptID, "student": &p.StudentID, "result": &p.ResultID, "presented": &p.PresentedID} {
		if raw := r.PathValue(key); raw != "" {
			n, e := strconv.ParseInt(raw, 10, 64)
			if e != nil || n < 1 {
				return p, Bad("invalid_id")
			}
			*target = n
		}
	}
	if n := p.Query["part_id"]; n != "" {
		v, e := strconv.ParseInt(n, 10, 64)
		if e != nil || v < 1 {
			return p, Bad("invalid_id")
		}
		p.PartID = v
	}
	for key, target := range map[string]*int64{"assignment_id": &p.AssignmentID, "student_account_id": &p.StudentID} {
		if v := p.Query[key]; v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 1 {
				return p, Bad("invalid_id")
			}
			if *target > 0 && *target != n {
				return p, Bad("conflicting_id")
			}
			*target = n
		}
	}
	return p, nil
}
func examID(r *http.Request) (int64, error) {
	id, e := strconv.ParseInt(r.PathValue("exam"), 10, 64)
	if e != nil || id < 1 {
		return 0, Bad("invalid_id")
	}
	return id, nil
}
func body(r *http.Request) (json.RawMessage, error) {
	if strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) != "application/json" {
		return nil, &APIError{415, "json_required"}
	}
	b, e := io.ReadAll(r.Body)
	if e != nil {
		return nil, &APIError{413, "payload_too_large"}
	}
	var v map[string]json.RawMessage
	if DecodeStrict(b, &v) != nil || v == nil {
		return nil, Bad("invalid_json")
	}
	return b, nil
}
func scope(e *Exam, a Actor, p Params, edit, content bool) error {
	if e.OrganizationID != a.OrganizationID {
		return Missing
	}
	if a.Can(edit) {
		if edit && content && !a.OrganizationAdmin && e.OwnerDivisionID != p.DivisionID {
			return Denied
		}
		if edit && content && !a.OrganizationAdmin {
			for id := range e.Divisions {
				if !contains(a.DivisionIDs, id) {
					return Denied
				}
			}
		}
		if a.OrganizationAdmin {
			return nil
		}
		if e.Divisions[p.DivisionID] != nil || !edit && e.InBank {
			return nil
		}
		return Missing
	}
	if edit {
		return Denied
	}
	if p.DivisionID < 1 || e.Divisions[p.DivisionID] == nil {
		return Missing
	}
	return nil
}
func contentOp(op string) bool {
	return strings.HasPrefix(op, "part-") || strings.HasPrefix(op, "question-") || op == "bank" || op == "direction" || op == "delete" || op == "metadata"
}
func operationScope(e *Exam, a Actor, p Params, op string, edit bool) error {
	if op == "copy" && e.OrganizationID == a.OrganizationID && e.InBank && a.Can(true) && p.DivisionID > 0 {
		return nil
	}
	return scope(e, a, p, edit, contentOp(op))
}
func (s *Server) operation(w http.ResponseWriter, r *http.Request, op string) {
	a, err := s.identity(r)
	if err != nil {
		failure(w, err)
		return
	}
	p, err := params(r, a)
	if err != nil {
		failure(w, err)
		return
	}
	id, err := examID(r)
	if err != nil {
		failure(w, err)
		return
	}
	mut := r.Method != "GET"
	var raw json.RawMessage
	if mut {
		raw, err = body(r)
		if err != nil {
			failure(w, err)
			return
		}
	}
	scopeOp := op
	if mut && op == "metadata" {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		if _, sharedName := fields["name"]; !sharedName {
			scopeOp = "division-metadata"
		}
	}
	e, err := load(r.Context(), s.DB, a.OrganizationID, id, false)
	if err != nil {
		if mut && op == "delete" && a.Can(true) { // Receipt replay after a successful physical deletion.
			if data, code, ok, replayErr := s.replay(r, a, raw); replayErr != nil {
				failure(w, replayErr)
				return
			} else if ok {
				respond(w, code, data)
				return
			}
		}
		failure(w, err)
		return
	}
	examinerCommand := op == "ensure" || op == "ready" || op == "start" || op == "vote" || op == "next-question" || op == "replace-question"
	if examinerCommand && a.Role != "examinator" {
		failure(w, Denied)
		return
	}
	if mut && !examinerCommand && !a.Can(true) {
		failure(w, Denied)
		return
	}
	if err = operationScope(e, a, p, scopeOp, mut && !examinerCommand); err != nil {
		failure(w, err)
		return
	}
	if p.DivisionID < 1 && op != "delete" && op != "delete-preview" {
		failure(w, Bad("division_context_required"))
		return
	}
	if p.DivisionID == 0 {
		p.DivisionID = e.OwnerDivisionID
	}
	if mut {
		if er := authorizeCommand(e, a, op, p); er != nil {
			failure(w, er)
			return
		}
	}
	ids := operationMemberIDs(e, op, p, raw)
	if op == "copy" || op == "connect" {
		var in struct {
			DivisionID       int64 `json:"division_id"`
			ExpectedRevision int64 `json:"expected_revision"`
		}
		if err = DecodeStrict(raw, &in); err == nil {
			if in.DivisionID < 1 {
				err = Bad("invalid_division")
			}
			if !a.OrganizationAdmin && in.DivisionID != p.DivisionID {
				err = Denied
			}
			if err == nil {
				_, err = s.Deps.References(r, a.OrganizationID, []int64{in.DivisionID}, e.DirectionID, nil)
			}
		}
		if err != nil {
			failure(w, err)
			return
		}
	}
	// Fail closed on stale/foreign references. Historical reads do not require active accounts.
	if needsReferences(op) {
		members, er := s.Deps.References(r, a.OrganizationID, []int64{p.DivisionID}, e.DirectionID, uniqueIDs(ids))
		if er != nil {
			failure(w, er)
			return
		}
		p.Members = scopeMembers(members, p.DivisionID)
	}
	if !mut {
		if op == "delete-preview" {
			if !a.Can(false) {
				failure(w, Denied)
				return
			}
			respond(w, 200, map[string]any{"exam_id": id, "revision": e.Revision, "confirmation": confirmation(e, 0), "counts": examDeletionCounts(e)})
			return
		}
		result, er := Query(e, a, op, p)
		if er == nil && op == "history-preview" {
			if m, ok := result.(map[string]any); ok {
				m["confirmation"] = confirmation(e, p.StudentID)
			}
		}
		if er != nil {
			failure(w, er)
			return
		}
		respond(w, 200, result)
		return
	}
	result, code, err := s.mutate(r, a, raw, id, func(tx *sql.Tx, locked *Exam) (any, error) {
		p.Now = time.Now().UTC()
		if er := operationScope(locked, a, p, scopeOp, !examinerCommand); er != nil {
			return nil, er
		}
		if op == "delete" || op == "history-delete" {
			want := confirmation(locked, 0)
			if op == "history-delete" {
				want = confirmation(locked, p.StudentID)
			}
			if subtle.ConstantTimeCompare([]byte(want), []byte(r.Header.Get("X-Exam-Confirmation"))) != 1 {
				return nil, Conflict("preview_changed")
			}
		}
		if op == "metadata" || op == "bank" || op == "direction" || op == "connect" || op == "disconnect" || op == "copy" || op == "delete" {
			return s.catalogMutation(r, tx, locked, a, p, op, raw)
		}
		data, er := Command(locked, a, op, p, raw)
		if er != nil {
			return nil, er
		}
		changed, ratingChanged := true, false
		if m, ok := data.(map[string]any); ok {
			if v, ok := m["changed"].(bool); ok {
				changed = v
			}
			ratingChanged, _ = m["rating_changed"].(bool)
		}
		if changed {
			locked.Revision++
			if ratingChanged {
				locked.Divisions[p.DivisionID].RatingRevision++
			}
			if er = save(r.Context(), tx, locked); er != nil {
				return nil, er
			}
			if ratingChanged {
				er = sourceChange(r.Context(), tx, locked, []int64{p.DivisionID}, op)
			}
		}
		return data, er
	}, func(locked *Exam) error {
		if er := operationScope(locked, a, p, scopeOp, !examinerCommand); er != nil {
			return er
		}
		return authorizeCommand(locked, a, op, p)
	})
	if err != nil {
		// A concurrent identical DELETE may have loaded the object before the
		// first request committed, then waited on its receipt. Once the object
		// is gone, return that same authorized receipt rather than a transient 404.
		if op == "delete" && errors.Is(err, Missing) && a.Can(true) {
			if data, savedCode, ok, replayErr := s.replay(r, a, raw); replayErr != nil {
				failure(w, replayErr)
				return
			} else if ok {
				respond(w, savedCode, data)
				return
			}
		}
		failure(w, err)
		return
	}
	respond(w, code, result)
}
func confirmation(e *Exam, student int64) string {
	b, _ := json.Marshal(struct{ ID, Revision, Student int64 }{e.ID, e.Revision, student})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func requestHash(r *http.Request, a Actor, raw []byte) string {
	var division int64
	if a.DivisionID != nil {
		division = *a.DivisionID
	}
	h := sha256.New()
	h.Write([]byte(r.Method + " " + r.URL.Path + "?" + r.URL.Query().Encode() + " " + strconv.FormatInt(division, 10) + " "))
	h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}
func (s *Server) replay(r *http.Request, a Actor, raw []byte) (any, int, bool, error) {
	if !validateKey(r.Header.Get("Idempotency-Key")) {
		return nil, 0, false, nil
	}
	var hash string
	var b []byte
	var code int
	var created time.Time
	if s.DB.QueryRowContext(r.Context(), "SELECT request_hash,response,status_code,created_at FROM exams_receipts_v1 WHERE organization_id=? AND account_id=? AND idempotency_key=?", a.OrganizationID, a.AccountID, r.Header.Get("Idempotency-Key")).Scan(&hash, &b, &code, &created) != nil {
		return nil, 0, false, nil
	}
	if hash != requestHash(r, a, raw) {
		return nil, 0, false, Conflict("idempotency_key_reused")
	}
	if code == 410 || !created.Add(ReceiptRetention).After(time.Now().UTC()) {
		return nil, 0, false, &APIError{410, "idempotency_expired"}
	}
	if len(b) == 0 {
		return nil, 0, false, nil
	}
	out, err := decodeStoredValue(b)
	if err != nil {
		return nil, 0, false, nil
	}
	return out, code, true, nil
}
func (s *Server) mutate(r *http.Request, a Actor, raw []byte, id int64, fn mutation, checks ...func(*Exam) error) (any, int, error) {
	key := r.Header.Get("Idempotency-Key")
	if !validateKey(key) {
		return nil, 0, Bad("idempotency_key_required")
	}
	tx, err := s.DB.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	division := int64(0)
	if a.DivisionID != nil {
		division = *a.DivisionID
	}
	hash := requestHash(r, a, raw)
	_, err = tx.ExecContext(r.Context(), "INSERT INTO exams_receipts_v1(organization_id,account_id,idempotency_key,division_id,request_hash) VALUES(?,?,?,?,?) ON DUPLICATE KEY UPDATE idempotency_key=idempotency_key", a.OrganizationID, a.AccountID, key, division, hash)
	if err != nil {
		return nil, 0, err
	}
	var oldHash string
	var response []byte
	var code int
	var created time.Time
	if err = tx.QueryRowContext(r.Context(), "SELECT request_hash,response,status_code,created_at FROM exams_receipts_v1 WHERE organization_id=? AND account_id=? AND idempotency_key=? FOR UPDATE", a.OrganizationID, a.AccountID, key).Scan(&oldHash, &response, &code, &created); err != nil {
		return nil, 0, err
	}
	if oldHash != hash {
		return nil, 0, Conflict("idempotency_key_reused")
	}
	var ex *Exam
	if id > 0 {
		ex, err = load(r.Context(), tx, a.OrganizationID, id, true)
		if err != nil {
			return nil, 0, err
		}
		for _, check := range checks {
			if err = check(ex); err != nil {
				return nil, 0, err
			}
		}
	}
	if code == 410 || !created.Add(ReceiptRetention).After(time.Now().UTC()) {
		return nil, 0, &APIError{410, "idempotency_expired"}
	}
	if len(response) > 0 {
		out, er := decodeStoredValue(response)
		if er != nil {
			return nil, 0, er
		}
		return out, code, nil
	}
	out, err := fn(tx, ex)
	if err != nil {
		return nil, 0, err
	}
	code = 200
	if id == 0 {
		code = 201
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, 0, err
	}
	_, err = tx.ExecContext(r.Context(), "UPDATE exams_receipts_v1 SET response=?,status_code=? WHERE organization_id=? AND account_id=? AND idempotency_key=?", b, code, a.OrganizationID, a.AccountID, key)
	if err != nil {
		return nil, 0, err
	}
	if err = tx.Commit(); err != nil {
		return nil, 0, err
	}
	return out, code, nil
}
func (s *Server) newExam(w http.ResponseWriter, r *http.Request) {
	a, err := s.identity(r)
	if err != nil {
		failure(w, err)
		return
	}
	if !a.Can(true) || a.DivisionID == nil {
		failure(w, Denied)
		return
	}
	raw, err := body(r)
	if err != nil {
		failure(w, err)
		return
	}
	var in struct {
		Name        string `json:"name"`
		Kind        string `json:"exam_type"`
		DirectionID int64  `json:"direction_id"`
		Date        string `json:"date"`
	}
	if err = DecodeStrict(raw, &in); err != nil {
		failure(w, err)
		return
	}
	if in.Name == "" {
		in.Name = "Экзамен"
	}
	in.Name, err = validText(in.Name, "exam_name", 250, 1000)
	if err != nil {
		failure(w, err)
		return
	}
	if in.DirectionID < 1 || (in.Kind != "classic" && in.Kind != "outside_lms") {
		failure(w, Bad("invalid_exam"))
		return
	}
	if in.Kind == "classic" && in.Date != "" {
		failure(w, Bad("unexpected_date"))
		return
	}
	if in.Kind == "outside_lms" {
		if !validExamDate(in.Date) {
			failure(w, Bad("invalid_date"))
			return
		}
	}
	if _, err = s.Deps.References(r, a.OrganizationID, []int64{*a.DivisionID}, in.DirectionID, nil); err != nil {
		failure(w, err)
		return
	}
	out, code, err := s.mutate(r, a, raw, 0, func(tx *sql.Tx, _ *Exam) (any, error) {
		e := &Exam{Name: in.Name, OrganizationID: a.OrganizationID, OwnerDivisionID: *a.DivisionID, DirectionID: in.DirectionID, Kind: in.Kind, Revision: 1, ContentRevision: 1, RatingRevision: 1, CreatedAt: time.Now().UTC(), Parts: []Part{}, Questions: []Question{}, Divisions: map[int64]*Division{*a.DivisionID: newDivision(*a.DivisionID)}}
		e.Divisions[*a.DivisionID].Date = in.Date
		if er := create(r.Context(), tx, e); er != nil {
			return nil, er
		}
		if er := sourceChange(r.Context(), tx, e, []int64{*a.DivisionID}, "created"); er != nil {
			return nil, er
		}
		return summary(e, *a.DivisionID), nil
	})
	if err != nil {
		failure(w, err)
		return
	}
	respond(w, code, out)
}
func newDivision(id int64) *Division {
	return &Division{ID: id, Revision: 1, RatingRevision: 1, Config: Config{Revision: 1, Thresholds: []Threshold{}}, Privileges: map[int64]Privilege{}, Commissions: []Commission{}, Assignments: []Assignment{}, Attempts: []Attempt{}, Outside: []OutsideResult{}}
}
func summary(e *Exam, division int64) map[string]any {
	out := map[string]any{"id": e.ID, "name": e.Name, "connected_division_ids": allDivisions(e), "organization_id": e.OrganizationID, "owner_division_id": e.OwnerDivisionID, "division_id": division, "direction_id": e.DirectionID, "exam_type": e.Kind, "revision": e.Revision, "content_revision": e.ContentRevision, "in_bank": e.InBank, "origin_exam_id": e.OriginID, "exam_date": examDateValue(e, division), "connected": e.Divisions[division] != nil}
	if d := e.Divisions[division]; d != nil {
		out["start_at"], out["end_at"], out["date"], out["training_enabled"] = d.Config.StartAt, d.Config.EndAt, d.Date, d.TrainingEnabled
	}
	return out
}
func (s *Server) catalog(w http.ResponseWriter, r *http.Request) {
	a, err := s.identity(r)
	if err != nil {
		failure(w, err)
		return
	}
	p, err := params(r, a)
	if err != nil {
		failure(w, err)
		return
	}
	dateFilter, err := ParseExamDateFilter(p.Query)
	if err != nil {
		failure(w, err)
		return
	}
	if v := p.Query["scope"]; v != "" && !oneOf(v, "bank", "division", "organization") {
		failure(w, Bad("invalid_scope"))
		return
	}
	if p.Query["scope"] == "organization" && !a.OrganizationAdmin {
		failure(w, Denied)
		return
	}
	if v := p.Query["type"]; v != "" && !oneOf(v, "classic", "outside_lms") {
		failure(w, Bad("invalid_type"))
		return
	}
	if v := p.Query["sort"]; v != "" && !oneOf(v, "date_asc", "date_desc", "created_desc", "direction_asc") {
		failure(w, Bad("invalid_sort"))
		return
	}
	if v := p.Query["direction_id"]; v != "" {
		if n, er := strconv.ParseInt(v, 10, 64); er != nil || n < 1 {
			failure(w, Bad("invalid_direction"))
			return
		}
	}
	limit, offset := 100, 0
	for key, target := range map[string]*int{"limit": &limit, "offset": &offset} {
		if v := p.Query[key]; v != "" {
			n, e := strconv.Atoi(v)
			if e != nil {
				failure(w, Bad("invalid_pagination"))
				return
			}
			*target = n
		}
	}
	if limit < 1 || limit > 200 || offset < 0 || offset > 100000 {
		failure(w, Bad("invalid_pagination"))
		return
	}
	es, err := listExams(r.Context(), s.DB, a.OrganizationID)
	if err != nil {
		failure(w, err)
		return
	}
	filtered := []*Exam{}
	for _, e := range es {
		if scope(e, a, p, false, false) != nil {
			continue
		}
		if p.Query["scope"] == "bank" && (!a.Can(false) || !e.InBank) {
			continue
		}
		if p.Query["scope"] == "division" && e.Divisions[p.DivisionID] == nil {
			continue
		}
		if q := p.Query["q"]; q != "" && !strings.Contains(strings.ToLower(e.Name), strings.ToLower(q)) {
			continue
		}
		if direction := p.Query["direction_id"]; direction != "" && direction != strconv.FormatInt(e.DirectionID, 10) {
			continue
		}
		if kind := p.Query["type"]; kind != "" && e.Kind != kind {
			continue
		}
		if !dateFilter.Match(e, p.DivisionID) {
			continue
		}
		if a.Role == "examinator" {
			found := false
			if d := e.Divisions[p.DivisionID]; d != nil {
				for _, as := range d.Assignments {
					for _, m := range as.Members {
						if m.AccountID == a.AccountID {
							found = true
						}
					}
				}
			}
			if !found {
				continue
			}
		}
		filtered = append(filtered, e)
	}
	directionNames := map[int64]string{}
	if p.Query["sort"] == "direction_asc" && len(filtered) > 0 {
		provider, ok := s.Deps.(interface {
			Fetch(*http.Request, int64, []int64, int64, []int64, []string) (ReferenceSet, error)
		})
		if !ok {
			failure(w, &APIError{503, "dependency_unavailable"})
			return
		}
		divisions := []int64{}
		if p.DivisionID > 0 {
			divisions = append(divisions, p.DivisionID)
		}
		references, err := provider.Fetch(r, a.OrganizationID, divisions, 0, nil, nil)
		if err != nil {
			failure(w, err)
			return
		}
		for _, direction := range references.Directions {
			directionNames[direction.ID] = direction.Name
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		a, b := filtered[i], filtered[j]
		switch p.Query["sort"] {
		case "created_desc":
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.After(b.CreatedAt)
			}
			return a.ID > b.ID
		case "direction_asc":
			return catalogDirectionLess(a, b, directionNames)
		default:
			return ExamCatalogDateLess(a, b, p.DivisionID, p.Query["sort"] == "date_asc")
		}
	})
	items := make([]any, 0, len(filtered))
	for _, e := range filtered {
		row := summary(e, p.DivisionID)
		if !a.Can(false) {
			for _, field := range []string{"connected_division_ids", "owner_division_id", "origin_exam_id", "in_bank"} {
				delete(row, field)
			}
		}
		items = append(items, row)
	}
	total := len(items)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	respond(w, 200, map[string]any{"items": items[offset:end], "total": total, "limit": limit, "offset": offset})
}
func catalogDirectionLess(a, b *Exam, names map[int64]string) bool {
	name := func(e *Exam) string {
		value, found := names[e.DirectionID]
		if !found {
			value = e.Name
		}
		return strings.ToLower(value)
	}
	left, right := name(a), name(b)
	if left != right {
		return left < right
	}
	return a.ID < b.ID
}
func (s *Server) catalogMutation(r *http.Request, tx *sql.Tx, e *Exam, a Actor, p Params, op string, raw []byte) (any, error) {
	var in struct {
		ExpectedRevision int64   `json:"expected_revision"`
		InBank           *bool   `json:"in_bank"`
		DivisionID       int64   `json:"division_id"`
		DirectionID      int64   `json:"direction_id"`
		Name             *string `json:"name"`
		Date             *string `json:"date"`
	}
	if err := DecodeStrict(raw, &in); err != nil {
		return nil, err
	}
	allowed := map[string]bool{"expected_revision": true}
	switch op {
	case "bank":
		allowed["in_bank"] = true
	case "direction":
		allowed["direction_id"] = true
	case "copy", "connect":
		allowed["division_id"] = true
	case "metadata":
		allowed["name"] = true
		allowed["date"] = true
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	for field := range fields {
		if !allowed[field] {
			return nil, Bad("unexpected_fields")
		}
	}
	if in.ExpectedRevision < 1 {
		return nil, Bad("expected_revision_required")
	}
	if in.ExpectedRevision != e.Revision {
		return nil, Conflict("exam_modified")
	}
	affected := allDivisions(e)
	ratingChanged := false
	changed := false
	sharedChanged := false
	switch op {
	case "metadata":
		if in.Name == nil && in.Date == nil {
			return nil, Bad("metadata_required")
		}
		if in.Name != nil {
			name, err := validText(*in.Name, "exam_name", 250, 1000)
			if err != nil {
				return nil, err
			}
			if e.Name != name {
				e.Name = name
				e.RatingRevision++
				changed, sharedChanged, ratingChanged = true, true, true
			}
		}
		if in.Date != nil {
			if e.Kind != "outside_lms" {
				return nil, Bad("outside_exam_required")
			}
			if !validExamDate(*in.Date) {
				return nil, Bad("invalid_date")
			}
			d, err := e.Division(p.DivisionID)
			if err != nil {
				return nil, err
			}
			if d.Date != *in.Date {
				d.Date = *in.Date
				d.Revision++
				d.RatingRevision++
				if !sharedChanged {
					affected = []int64{p.DivisionID}
				}
				changed, ratingChanged = true, true
			}
		}
	case "bank":
		if in.InBank == nil {
			return nil, Bad("in_bank_required")
		}
		if e.InBank != *in.InBank {
			e.InBank = *in.InBank
			changed = true
		}
	case "direction":
		if in.DirectionID < 1 {
			return nil, Bad("invalid_direction")
		}
		if _, err := s.Deps.References(r, a.OrganizationID, []int64{p.DivisionID}, in.DirectionID, nil); err != nil {
			return nil, err
		}
		if e.DirectionID != in.DirectionID {
			e.DirectionID = in.DirectionID
			e.ContentRevision++
			e.RatingRevision++
			changed, ratingChanged = true, true
		}
	case "connect":
		if !a.OrganizationAdmin {
			return nil, Denied
		}
		if in.DivisionID < 1 {
			return nil, Bad("invalid_division")
		}
		if e.Divisions[in.DivisionID] == nil {
			e.Divisions[in.DivisionID] = newDivision(in.DivisionID)
			if e.Kind == "outside_lms" {
				e.Divisions[in.DivisionID].Date = e.Divisions[e.OwnerDivisionID].Date
			}
			changed, ratingChanged = true, true
		}
		affected = []int64{in.DivisionID}
	case "disconnect":
		division, err := strconv.ParseInt(r.PathValue("division"), 10, 64)
		if err != nil || division < 1 {
			return nil, Bad("invalid_division")
		}
		if !a.OrganizationAdmin && division != p.DivisionID {
			return nil, Denied
		}
		if division == e.OwnerDivisionID {
			return nil, Conflict("owner_connection_required")
		}
		d, err := e.Division(division)
		if err != nil {
			return nil, err
		}
		if len(d.Attempts) > 0 || len(d.Assignments) > 0 || len(d.Outside) > 0 {
			return nil, Conflict("connection_has_history")
		}
		delete(e.Divisions, division)
		affected = []int64{division}
		changed, ratingChanged = true, true
	case "copy":
		if in.DivisionID < 1 {
			return nil, Bad("invalid_division")
		}
		cp := Exam{Name: e.Name, OrganizationID: e.OrganizationID, OwnerDivisionID: in.DivisionID, DirectionID: e.DirectionID, Kind: e.Kind, Revision: 1, ContentRevision: 1, RatingRevision: 1, OriginID: e.ID, CreatedAt: p.Now, NextID: e.NextID, Parts: append([]Part(nil), e.Parts...), Questions: append([]Question(nil), e.Questions...)}
		d := newDivision(in.DivisionID)
		source := e.Divisions[p.DivisionID]
		if source == nil {
			source = e.Divisions[e.OwnerDivisionID]
		}
		if source != nil {
			encoded, err := json.Marshal(source.Config)
			if err != nil {
				return nil, err
			}
			if err = json.Unmarshal(encoded, &d.Config); err != nil {
				return nil, err
			}
			d.Config.Revision = 1
			d.Date = source.Date
			d.TrainingEnabled = source.TrainingEnabled
		}
		cp.Divisions = map[int64]*Division{in.DivisionID: d}
		if err := create(r.Context(), tx, &cp); err != nil {
			return nil, err
		}
		if err := sourceChange(r.Context(), tx, &cp, []int64{in.DivisionID}, "copied"); err != nil {
			return nil, err
		}
		return summary(&cp, in.DivisionID), nil
	case "delete":
		if _, err := tx.ExecContext(r.Context(), "DELETE FROM exams_catalog_v1 WHERE organization_id=? AND id=?", e.OrganizationID, e.ID); err != nil {
			return nil, err
		}
		e.Revision++
		if err := sourceChange(r.Context(), tx, e, affected, "deleted"); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true, "exam_id": e.ID}, nil
	}
	if !changed {
		return summary(e, p.DivisionID), nil
	}
	e.Revision++
	if err := save(r.Context(), tx, e); err != nil {
		return nil, err
	}
	if ratingChanged {
		if err := sourceChange(r.Context(), tx, e, affected, op); err != nil {
			return nil, err
		}
	}
	return summary(e, p.DivisionID), nil
}
func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	if len(s.InternalKey) < 32 || subtle.ConstantTimeCompare([]byte(s.InternalKey), []byte(r.Header.Get("X-Service-Key"))) != 1 {
		failure(w, &APIError{401, "invalid_service_key"})
		return
	}
	query := map[string]string{}
	for key, values := range r.URL.Query() {
		if len(values) != 1 {
			failure(w, Bad("duplicate_query_parameter"))
			return
		}
		if !oneOf(key, "organization_id", "division_ids", "date_from", "date_to", "from_date", "to_date", "as_of") {
			failure(w, Bad("unknown_query_parameter"))
			return
		}
		query[key] = values[0]
	}
	period, err := ParseExamDateFilter(query)
	if err != nil {
		failure(w, err)
		return
	}
	org, err := strconv.ParseInt(r.URL.Query().Get("organization_id"), 10, 64)
	if err != nil || org < 1 {
		failure(w, Bad("invalid_organization"))
		return
	}
	divs := []int64{}
	for _, v := range strings.Split(r.URL.Query().Get("division_ids"), ",") {
		id, er := strconv.ParseInt(v, 10, 64)
		if er != nil || id < 1 || contains(divs, id) {
			failure(w, Bad("invalid_divisions"))
			return
		}
		divs = append(divs, id)
	}
	if len(divs) > 100 {
		failure(w, Bad("division_limit"))
		return
	}
	if _, err = s.Deps.References(r, org, divs, 0, nil); err != nil {
		failure(w, err)
		return
	}
	asOf := time.Now().UTC()
	if v := r.URL.Query().Get("as_of"); v != "" {
		asOf, err = time.Parse(time.RFC3339Nano, v)
		if err != nil {
			failure(w, Bad("invalid_as_of"))
			return
		}
	}
	tx, err := s.DB.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		failure(w, err)
		return
	}
	defer tx.Rollback()
	es, err := listExams(r.Context(), tx, org)
	if err != nil {
		failure(w, err)
		return
	}
	versions := map[int64]int64{}
	sort.Slice(divs, func(i, j int) bool { return divs[i] < divs[j] })
	for _, division := range divs {
		var rev int64
		er := tx.QueryRowContext(r.Context(), "SELECT revision FROM exams_source_v1 WHERE organization_id=? AND division_id=?", org, division).Scan(&rev)
		if er != nil && er != sql.ErrNoRows {
			failure(w, er)
			return
		}
		versions[division] = rev
	}
	out, err := RatingSnapshot(es, org, divs, period.From, period.To, asOf)
	if err != nil {
		failure(w, err)
		return
	}
	respond(w, 200, map[string]any{"source_revisions": versions, "snapshot": out})
}
