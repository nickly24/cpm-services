package attendance

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Server struct {
	Store Store
	Peers Peers
	QRKey []byte
}
type Route struct {
	Pattern string
	Kind    string
	Input   any
}

func routes() []Route {
	p := "/v1/attendance/divisions/{division}"
	return []Route{
		{"GET /v1/attendance/command-receipts/{command_id}", "command-receipt", nil},
		{"GET /v1/attendance/calendars", "calendars", nil}, {"GET /v1/attendance/schedule", "overlay", nil}, {"GET /v1/attendance/me/schedule", "personal", nil},
		{"POST /v1/attendance/schedule/bulk", "batch", Batch{}}, {"GET /v1/attendance/resources/rooms", "rooms", nil}, {"POST /v1/attendance/resources/rooms", "room", Room{}},
		{"GET /v1/attendance/audit", "audit", nil}, {"GET /v1/attendance/me/attendance", "own-report", nil},
		{"GET " + p + "/calendar", "calendar", nil}, {"PUT " + p + "/calendar", "calendar", Calendar{}},
		{"GET " + p + "/schedule", "schedule", nil}, {"POST " + p + "/schedule", "lesson", Lesson{}}, {"PUT " + p + "/schedule/{lesson}", "lesson", Lesson{}}, {"DELETE " + p + "/schedule/{lesson}", "lesson", struct{}{}},
		{"GET " + p + "/attendance-types", "types", nil}, {"POST " + p + "/attendance-types", "type", Type{}}, {"PUT " + p + "/attendance-types/{type}", "type", Type{}}, {"DELETE " + p + "/attendance-types/{type}", "type", struct{}{}},
		{"GET " + p + "/class-days", "days", nil}, {"POST " + p + "/class-days", "day", Day{}}, {"GET " + p + "/class-days/{day}", "day", nil}, {"PUT " + p + "/class-days/{day}", "day", Day{}}, {"DELETE " + p + "/class-days/{day}", "day", struct{}{}},
		{"GET " + p + "/class-days/{day}/attendance", "entries", nil}, {"POST " + p + "/class-days/{day}/attendance", "mark", Mark{}}, {"DELETE " + p + "/class-days/{day}/attendance/{account}", "unmark", struct{}{}}, {"GET " + p + "/attendance-report", "report", nil},
		{"GET " + p + "/students", "students", nil},
		{"GET " + p + "/schedule-references", "schedule-references", nil},
		{"POST " + p + "/class-days/{day}/qr", "qr", nil}, {"POST " + p + "/class-days/{day}/scan", "scan", struct {
			QR string `json:"qr"`
		}{}},
		{"GET " + p + "/zaps", "zaps", nil}, {"POST " + p + "/zaps", "zap-create", ZapInput{}}, {"GET " + p + "/zaps/{zap}", "zap", nil}, {"GET " + p + "/zaps/{zap}/images/{image}", "image", nil},
		{"POST " + p + "/zaps/{zap}/process", "zap-process", ZapDecision{}}, {"POST " + p + "/zaps/{zap}/dates/{date}/retry", "zap-retry", struct{}{}}, {"DELETE " + p + "/zaps/{zap}/dates/{date}", "zap-unlink", struct{}{}},
	}
}
func reply(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
func fail(w http.ResponseWriter, e error) {
	var a *APIError
	if !errors.As(e, &a) {
		a = unavailable
	}
	reply(w, a.Status, map[string]any{"status": false, "error": a.Code})
}
func decodeJSON(b []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return bad("invalid_request")
	}
	return nil
}
func uniqueJSON(d *json.Decoder) error {
	t, e := d.Token()
	if e != nil {
		return e
	}
	v, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch v {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			t, e := d.Token()
			if e != nil {
				return e
			}
			k, ok := t.(string)
			if !ok || seen[k] {
				return bad("duplicate_json_key")
			}
			seen[k] = true
			if e = uniqueJSON(d); e != nil {
				return e
			}
		}
	case '[':
		for d.More() {
			if e := uniqueJSON(d); e != nil {
				return e
			}
		}
	default:
		return bad("invalid_json")
	}
	_, e = d.Token()
	return e
}
func body(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) != "application/json" {
		return nil, &APIError{415, "json_required"}
	}
	r.Body = http.MaxBytesReader(w, r.Body, 3<<20)
	b, e := io.ReadAll(r.Body)
	if e != nil {
		return nil, bad("body_too_large")
	}
	if !utf8.Valid(b) {
		return nil, bad("invalid_json")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	if e = uniqueJSON(d); e != nil {
		return nil, bad("invalid_json")
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, bad("invalid_json")
	}
	return b, nil
}
func pid(r *http.Request, k string) (int64, error) {
	v := r.PathValue(k)
	if v == "" {
		return 0, nil
	}
	n, e := strconv.ParseInt(v, 10, 64)
	if e != nil || n < 1 {
		return 0, bad("invalid_id")
	}
	return n, nil
}
func qid(r *http.Request, k string) (*int64, error) {
	v := r.URL.Query().Get(k)
	if v == "" {
		return nil, nil
	}
	n, e := strconv.ParseInt(v, 10, 64)
	if e != nil || n < 1 {
		return nil, bad("invalid_filter")
	}
	return &n, nil
}
func query(r *http.Request, kind string) error {
	allowed := map[string]bool{}
	switch kind {
	case "schedule", "overlay", "personal", "days", "report", "own-report":
		allowed["date_from"] = true
		allowed["date_to"] = true
	}
	if kind == "overlay" {
		allowed["division_ids"] = true
	}
	if kind == "report" || kind == "entries" {
		allowed["account_id"] = true
		allowed["group_id"] = true
	}
	if kind == "audit" {
		allowed["after_revision"] = true
	}
	for k, v := range r.URL.Query() {
		if !allowed[k] || len(v) != 1 || v[0] == "" {
			return bad("invalid_query")
		}
	}
	return nil
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, map[string]bool{"status": true}) })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, c := context.WithTimeout(r.Context(), 3*time.Second)
		defer c()
		var n int
		if s.Store.DB.PingContext(ctx) != nil || s.Store.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM attendance_state_v1").Scan(&n) != nil {
			fail(w, unavailable)
			return
		}
		reply(w, 200, map[string]any{"status": true, "history_writes": "require_permanent_core_guard", "scope": "local_dev"})
	})
	mux.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, OpenAPI()) })
	for _, route := range routes() {
		rt := route
		mux.HandleFunc(rt.Pattern, func(w http.ResponseWriter, r *http.Request) { s.serve(w, r, rt) })
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		ctx, c := context.WithTimeout(r.Context(), 20*time.Second)
		defer c()
		defer func() {
			if recover() != nil {
				fail(w, unavailable)
			}
		}()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}
func (s *Server) serve(w http.ResponseWriter, r *http.Request, rt Route) {
	a, e := s.Peers.Identify(r.Context(), r.Header.Get("Authorization"))
	if e != nil {
		fail(w, e)
		return
	}
	if rt.Kind == "command-receipt" {
		s.readCommandReceipt(w, r, a)
		return
	}
	if e = query(r, rt.Kind); e != nil {
		fail(w, e)
		return
	}
	d, e := pid(r, "division")
	if e != nil {
		fail(w, e)
		return
	}
	ids := map[string]int64{}
	for _, k := range []string{"lesson", "type", "day", "account", "zap", "image"} {
		ids[k], e = pid(r, k)
		if e != nil {
			fail(w, e)
			return
		}
	}
	write := r.Method != "GET" && rt.Kind != "qr"
	if write {
		allowed := true
		switch rt.Kind {
		case "calendar", "lesson":
			allowed = a.can(d, "schedule", true)
		case "type", "day":
			allowed = a.can(d, "attendance", true)
		case "mark", "unmark":
			allowed = a.journal(d, true)
		case "scan":
			allowed = a.journal(d, true) || a.can(d, "scan", true)
		case "zap-create":
			allowed = a.zapAccess(d, true)
		case "zap-process", "zap-retry", "zap-unlink":
			allowed = a.can(d, "zaps", true)
		case "room":
			allowed = a.admin()
		}
		if !allowed {
			fail(w, denied)
			return
		}
	}
	if d > 0 {
		if _, e = s.Peers.Division(r.Context(), a.User.OrganizationID, d); e != nil {
			fail(w, e)
			return
		}
	}
	if rt.Kind == "qr" {
		b, err := body(w, r)
		if err == nil {
			err = decodeJSON(b, &struct{}{})
		}
		if err != nil {
			fail(w, err)
			return
		}
		st, rev, err := s.Store.Read(r.Context(), a.User.OrganizationID)
		if err != nil {
			fail(w, err)
			return
		}
		day, ok := st.Days[ids["day"]]
		_, member := a.division(d)
		if a.User.Role != "student" || !member {
			fail(w, denied)
			return
		}
		if !ok || day.DivisionID != d {
			fail(w, missing)
			return
		}
		c := QRClaims{a.User.OrganizationID, d, day.ID, a.User.AccountID, time.Now().Add(3 * time.Minute).Unix()}
		reply(w, 200, map[string]any{"status": true, "data": Result{rev, map[string]any{"qr": s.signQR(c), "expires_at": c.ExpiresAt}}})
		return
	}
	if !write {
		st, rev, err := s.Store.Read(r.Context(), a.User.OrganizationID)
		if err != nil {
			fail(w, err)
			return
		}
		if rt.Kind == "image" {
			z, ok := st.Zaps[ids["zap"]]
			if !ok || z.DivisionID != d || !zapVisible(a, z) || ids["image"] > int64(len(z.Images)) {
				fail(w, missing)
				return
			}
			i := z.Images[ids["image"]-1]
			b, err := base64.StdEncoding.DecodeString(i.Data)
			if err != nil {
				fail(w, unavailable)
				return
			}
			w.Header().Set("Content-Type", i.MIME)
			w.Header().Set("Content-Disposition", "inline")
			w.WriteHeader(200)
			_, _ = w.Write(b)
			return
		}
		v, err := s.read(r, st, a, d, ids, rt.Kind)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, map[string]any{"status": true, "data": Result{rev, v}})
		return
	}
	raw, e := body(w, r)
	if e != nil {
		fail(w, e)
		return
	}
	var envelope struct {
		Expected *int64          `json:"expected_revision"`
		Data     json.RawMessage `json:"data"`
	}
	if e = decodeJSON(raw, &envelope); e != nil || envelope.Expected == nil || *envelope.Expected < 0 || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		fail(w, bad("invalid_envelope"))
		return
	}
	k := r.Header.Get("Idempotency-Key")
	if !textOK(k, 100, true) || len(k) < 8 || strings.TrimSpace(k) != k {
		fail(w, bad("idempotency_key_required"))
		return
	}
	// Validate batch permissions before receipt replay as well as in the transaction.
	if rt.Kind == "batch" {
		var b Batch
		if e = decodeJSON(envelope.Data, &b); e != nil {
			fail(w, e)
			return
		}
		for _, l := range append(b.Creates, b.Updates...) {
			if !a.can(l.DivisionID, "schedule", true) {
				fail(w, denied)
				return
			}
		}
		for _, l := range b.Deletes {
			if !a.can(l.DivisionID, "schedule", true) {
				fail(w, denied)
				return
			}
		}
	}
	result, e := s.Store.Mutate(r.Context(), a, *envelope.Expected, k, r.Method+" "+r.URL.Path, raw, s.replayAuthorization(r, a, d, ids, rt.Kind), func(st *State) (any, error) { return s.write(r, st, a, d, ids, rt.Kind, envelope.Data, k) }, func() (commandScope, error) {
		return scopeAfterWrite(r.Method+" "+r.URL.Path, rt.Kind, d, envelope.Data)
	})
	if e != nil {
		fail(w, e)
		return
	}
	reply(w, 200, map[string]any{"status": true, "data": result})
}

func (s *Server) replayAuthorization(r *http.Request, a Identity, d int64, ids map[string]int64, kind string) func(*State, Result) error {
	if a.User.Role != "proctor" || kind != "mark" && kind != "scan" && kind != "unmark" {
		return nil
	}
	return func(original *State, result Result) error {
		u := ids["account"]
		var resultEntry *Entry
		if kind != "unmark" {
			// The original signed scan has already been verified. A receipt retry
			// stays valid after QR expiry, but still requires today's target grant.
			b, e := json.Marshal(result.Value)
			var entry Entry
			if e != nil || json.Unmarshal(b, &entry) != nil || entry.DivisionID != d || entry.DayID != ids["day"] || entry.AccountID < 1 {
				return unavailable
			}
			u = entry.AccountID
			resultEntry = &entry
		}
		entry, ok := original.Entries[key(ids["day"], u)]
		if !ok || entry.DivisionID != d || entry.DayID != ids["day"] || entry.AccountID != u {
			return unavailable
		}
		if resultEntry != nil && (resultEntry.ID != entry.ID || !same(resultEntry.GroupID, entry.GroupID)) {
			return unavailable
		}
		if _, e := s.target(r.Context(), a, d, u, kind == "scan"); e != nil {
			return e
		}
		division, ok := a.division(d)
		if !ok || !same(division.GroupID, entry.GroupID) {
			return denied
		}
		return nil
	}
}

func (s *Server) read(r *http.Request, st *State, a Identity, d int64, ids map[string]int64, kind string) (any, error) {
	from, to := r.URL.Query().Get("date_from"), r.URL.Query().Get("date_to")
	switch kind {
	case "calendars":
		v, e := s.calendarRead(r.Context(), st, a, r.Header.Get("Authorization"), "2000-01-01", "2000-01-01", nil, false)
		if e != nil {
			return nil, e
		}
		return v.(map[string]any)["calendars"], nil
	case "overlay", "personal", "schedule":
		selected := []int64{}
		if kind == "schedule" {
			selected = append(selected, d)
		} else if kind == "overlay" {
			for _, raw := range strings.Split(r.URL.Query().Get("division_ids"), ",") {
				n, e := strconv.ParseInt(raw, 10, 64)
				if e != nil || n < 1 {
					return nil, bad("invalid_calendars")
				}
				for _, id := range selected {
					if id == n {
						return nil, bad("duplicate_calendar")
					}
				}
				selected = append(selected, n)
			}
			if len(selected) > 20 {
				return nil, bad("too_many_calendars")
			}
		}
		return s.calendarRead(r.Context(), st, a, r.Header.Get("Authorization"), from, to, selected, kind == "personal")
	case "calendar":
		c, ok := st.Calendars[d]
		if !ok || !readableCalendar(a, c) {
			return nil, missing
		}
		return c, nil
	case "rooms":
		out := []Room{}
		for _, v := range st.Rooms {
			out = append(out, v)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out, nil
	case "types":
		if !a.journal(d, false) && !a.can(d, "scan", false) {
			return nil, denied
		}
		out := []Type{}
		for _, v := range st.Types {
			if v.DivisionID == d {
				out = append(out, v)
			}
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Sort != out[j].Sort {
				return out[i].Sort < out[j].Sort
			}
			return out[i].ID < out[j].ID
		})
		return out, nil
	case "days", "day":
		if !a.journal(d, false) && !a.can(d, "scan", false) {
			return nil, denied
		}
		if kind == "day" {
			v, ok := st.Days[ids["day"]]
			if !ok || v.DivisionID != d {
				return nil, missing
			}
			return v, nil
		}
		if e := period(from, to); e != nil {
			return nil, e
		}
		out := []Day{}
		for _, v := range st.Days {
			if v.DivisionID == d && v.Date >= from && v.Date <= to {
				out = append(out, v)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
		return out, nil
	case "entries", "report":
		account, e := qid(r, "account_id")
		if e != nil {
			return nil, e
		}
		group, e := qid(r, "group_id")
		if e != nil {
			return nil, e
		}
		return s.report(r.Context(), st, a, d, ids["day"], from, to, account, group)
	case "own-report":
		if a.User.Role != "student" {
			return nil, denied
		}
		if e := period(from, to); e != nil {
			return nil, e
		}
		out := []any{}
		for _, o := range a.Organizations {
			if o.ID == a.User.OrganizationID {
				for _, v := range o.Divisions {
					x, e := s.report(r.Context(), st, a, v.ID, 0, from, to, nil, nil)
					if e != nil {
						return nil, e
					}
					out = append(out, x)
				}
			}
		}
		return out, nil
	case "schedule-references":
		if !a.can(d, "schedule", true) {
			return nil, denied
		}
		refs, e := s.Peers.Refs(r.Context(), a.User.OrganizationID, d, []int64{}, nil)
		if e != nil {
			return nil, e
		}
		schools, e := s.Peers.Schools(r.Context(), r.Header.Get("Authorization"))
		if e != nil {
			return nil, e
		}
		teachers := []Member{}
		for _, m := range refs.Members {
			if m.Role == "staff_admin" || m.Role == "proctor" || m.Role == "examinator" || m.Role == "supervisor" {
				teachers = append(teachers, m)
			}
		}
		rooms := []Room{}
		for _, room := range st.Rooms {
			rooms = append(rooms, room)
		}
		return map[string]any{"groups": refs.Groups, "teachers": teachers, "schools": schools, "rooms": rooms}, nil

	case "students":
		if !a.journal(d, false) && !a.can(d, "scan", false) && !a.can(d, "zaps", false) {
			return nil, denied
		}
		var group *int64
		if a.User.Role == "proctor" {
			v, _ := a.division(d)
			group = v.GroupID
			if group == nil {
				return []Member{}, nil
			}
		}
		refs, e := s.Peers.Refs(r.Context(), a.User.OrganizationID, d, []int64{}, group)
		if e != nil {
			return nil, e
		}
		if a.User.Role == "proctor" {
			assigned := false
			for _, g := range refs.Groups {
				if g.ProctorID != nil && *g.ProctorID == a.User.AccountID {
					assigned = true
				}
			}
			if !assigned {
				return nil, denied
			}
		}
		out := []Member{}
		for _, m := range refs.Members {
			if m.Role == "student" && (a.User.Role != "student" || m.AccountID == a.User.AccountID) {
				out = append(out, m)
			}
		}
		return out, nil

	case "zaps", "zap":
		if !a.zapAccess(d, false) {
			return nil, denied
		}
		if kind == "zap" {
			z, ok := st.Zaps[ids["zap"]]
			if !ok || z.DivisionID != d || !zapVisible(a, z) {
				return nil, missing
			}
			count := len(z.Images)
			z.Images = nil
			return map[string]any{"zap": z, "image_count": count}, nil
		}
		out := []Zap{}
		for _, z := range st.Zaps {
			if z.DivisionID == d && zapVisible(a, z) {
				z.Images = nil
				out = append(out, z)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out, nil
	case "audit":
		if !a.admin() {
			return nil, denied
		}
		after := int64(0)
		if raw := r.URL.Query().Get("after_revision"); raw != "" {
			var e error
			after, e = strconv.ParseInt(raw, 10, 64)
			if e != nil || after < 0 {
				return nil, bad("invalid_revision")
			}
		}
		rows, e := s.Store.DB.QueryContext(r.Context(), "SELECT revision,actor_account_id,action,created_at FROM attendance_audit_v1 WHERE organization_id=? AND revision>? ORDER BY revision LIMIT 100", a.User.OrganizationID, after)
		if e != nil {
			return nil, unavailable
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var rev, actor int64
			var action string
			var at time.Time
			if rows.Scan(&rev, &actor, &action, &at) != nil {
				return nil, unavailable
			}
			out = append(out, map[string]any{"revision": rev, "actor_account_id": actor, "action": action, "created_at": at})
		}
		return out, rows.Err()
	}
	return nil, missing
}
