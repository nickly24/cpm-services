package attendance

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Isolated peer fixtures complement the real core/MySQL HTTP guard checks.
type testPeers struct {
	Org      int64
	GuardErr error
	AuthErr  error
	CoreErr  error
}

func fixtureIdentity(o int64, role string) Identity {
	edit := Permission{true, true}
	view := Permission{true, false}
	g := int64(11)
	div := int64(1)
	a := Identity{Status: true, Active: true, User: User{AccountID: 9, Role: role, OrganizationID: o, DivisionID: &div, OrganizationAdmin: role == "admin"}, Organizations: []Organization{{o, "Synthetic", []Division{{1, "First", &g, map[string]Permission{"schedule": view, "attendance": view, "zaps": view, "scan": view}}, {2, "Second", &g, map[string]Permission{"schedule": edit, "attendance": edit, "zaps": edit, "scan": edit}}}}}}
	if role == "student" {
		a.User.AccountID = 7
	}
	if role == "proctor" {
		a.User.AccountID = 8
	}
	if role == "journal_editor" {
		a.User.Role = "staff_admin"
		a.Organizations[0].Divisions[1].Permissions = map[string]Permission{"attendance": edit}
	}
	if role == "scanner" {
		a.User.Role = "staff_admin"
		a.Organizations[0].Divisions[1].Permissions = map[string]Permission{"scan": edit}
	}
	return a
}
func (p *testPeers) Identify(ctx context.Context, token string) (Identity, error) {
	if p.AuthErr != nil {
		return Identity{}, p.AuthErr
	}
	role := strings.TrimPrefix(token, "Bearer ")
	switch role {
	case "admin", "student", "proctor", "staff_admin", "scanner", "journal_editor", "supervisor":
		return fixtureIdentity(p.Org, role), nil
	}
	return Identity{}, &APIError{401, "invalid_token"}
}
func (p *testPeers) Division(ctx context.Context, o, d int64) (string, error) {
	if p.CoreErr != nil {
		return "", p.CoreErr
	}
	if o != p.Org || d < 1 || d > 3 {
		return "", missing
	}
	return "Division " + itoa(d), nil
}
func (p *testPeers) Refs(ctx context.Context, o, d int64, ids []int64, g *int64) (References, error) {
	if _, e := p.Division(ctx, o, d); e != nil {
		return References{}, e
	}
	if g != nil && *g != 11 && *g != 12 {
		return References{}, missing
	}
	r := References{OrganizationID: o, DivisionID: d, Groups: []Group{{"First group", 11, ptr(8)}, {"Other group", 12, nil}}}
	for _, id := range ids {
		switch id {
		case 7:
			r.Members = append(r.Members, Member{AccountID: 7, ProfileID: 700, Role: "student", Name: "Synthetic student", GroupID: ptr(11)})
		case 6:
			r.Members = append(r.Members, Member{AccountID: 6, ProfileID: 600, Role: "student", Name: "Other student", GroupID: ptr(12)})
		case 8:
			r.Members = append(r.Members, Member{AccountID: 8, ProfileID: 800, Role: "proctor", Name: "Synthetic proctor", GroupID: ptr(11)})
		case 9:
			r.Members = append(r.Members, Member{AccountID: 9, ProfileID: 900, Role: "staff_admin", Name: "Synthetic staff", GroupID: nil})
		default:
			return References{}, missing
		}
	}
	return r, nil
}
func (p *testPeers) School(ctx context.Context, token string, id int64) error {
	if id != 31 {
		return missing
	}
	return p.CoreErr
}
func (p *testPeers) OwnSchool(ctx context.Context, token string) (*int64, error) {
	return ptr(31), p.CoreErr
}
func (p *testPeers) Guard(ctx context.Context, o, d, u int64, k string) error { return p.GuardErr }
func ptr(n int64) *int64                                                      { return &n }
func errorCode(e error) string {
	var a *APIError
	if errors.As(e, &a) {
		return a.Code
	}
	if e == nil {
		return ""
	}
	return e.Error()
}
func sampleLesson(d int64, date, start, end string) Lesson {
	return Lesson{DivisionID: d, Date: date, Start: start, End: end, Name: "Synthetic lesson", Teacher: "Display teacher", Location: "Site", Classroom: "101", Color: "#00aa00", Public: true, ForAll: true, InPerson: true, GroupIDs: []int64{}}
}
func TestTimeAndPeriods(t *testing.T) {
	for _, in := range []struct{ date, clock, zone, want string }{{"2026-10-08", "09:00", "Europe/Moscow", ""}, {"2026-03-29", "02:30", "Europe/Berlin", "nonexistent_local_time"}, {"2026-10-25", "02:30", "Europe/Berlin", "ambiguous_local_time"}, {"2026-10-08", "9:00", "Europe/Moscow", "invalid_time"}, {"2026-02-30", "09:00", "Europe/Moscow", "invalid_date"}} {
		_, e := localInstant(in.date, in.clock, in.zone)
		if errorCode(e) != in.want {
			t.Fatalf("%+v: %v", in, e)
		}
	}
	if period("2024-01-01", "2024-12-31") != nil {
		t.Fatal("366 inclusive leap days")
	}
	if period("2024-01-01", "2025-01-01") == nil {
		t.Fatal("367 days")
	}
}
func TestBatchVisibilityAndOverlaps(t *testing.T) {
	p := &testPeers{Org: 1}
	s := Server{Peers: p}
	st := NewState()
	for d := int64(1); d <= 3; d++ {
		st.Calendars[d] = Calendar{d, "organization", "Europe/Moscow"}
	}
	a := fixtureIdentity(1, "admin")
	l1 := sampleLesson(1, "2026-10-08", "09:00", "10:00")
	l2 := sampleLesson(2, "2026-10-08", "09:30", "10:30")
	l3 := sampleLesson(2, "2026-10-08", "10:30", "11:30")
	l4 := sampleLesson(3, "2026-10-08", "09:10", "10:10")
	_, e := s.batch(context.Background(), st, a, "", Batch{Creates: []Lesson{l1, l2, l3, l4}})
	if e != nil {
		t.Fatal(e)
	}
	student := fixtureIdentity(1, "student")
	v, e := s.calendarRead(context.Background(), st, student, "", "2026-10-08", "2026-10-08", nil, true)
	if e != nil {
		t.Fatal(e)
	}
	data := v.(map[string]any)
	if len(data["events"].([]Event)) != 3 || len(data["overlap_clusters"].([]Overlap)) != 1 {
		t.Fatalf("personal %#v", data)
	}
	student.User.DivisionID = ptr(2)
	v2, _ := s.calendarRead(context.Background(), st, student, "", "2026-10-08", "2026-10-08", nil, true)
	b1, _ := json.Marshal(v)
	b2, _ := json.Marshal(v2)
	if !bytes.Equal(b1, b2) {
		t.Fatal("active division changed personal schedule")
	}
	v, e = s.calendarRead(context.Background(), st, student, "", "2026-10-08", "2026-10-08", []int64{3}, false)
	if e != nil || len(v.(map[string]any)["events"].([]Event)) != 1 {
		t.Fatal("published foreign calendar", e)
	}
	if _, e = s.report(context.Background(), st, student, 3, 0, "2026-10-08", "2026-10-08", nil, nil); errorCode(e) != "forbidden" {
		t.Fatal("calendar leaked journal")
	}
	c := st.Calendars[3]
	c.Visibility = "closed"
	st.Calendars[3] = c
	if _, e = s.calendarRead(context.Background(), st, student, "", "2026-10-08", "2026-10-08", []int64{3}, false); errorCode(e) != "not_found" {
		t.Fatal("closed calendar", e)
	}
	before, _ := json.Marshal(st)
	invalid := sampleLesson(2, "2026-10-08", "12:00", "11:00")
	_, e = s.batch(context.Background(), st, a, "", Batch{Deletes: []LessonRef{{1, 1}}, Creates: []Lesson{invalid}})
	after, _ := json.Marshal(st)
	if e == nil || !bytes.Equal(before, after) {
		t.Fatal("partial failed batch")
	}
	_, e = s.batch(context.Background(), st, fixtureIdentity(1, "staff_admin"), "", Batch{Creates: []Lesson{l1}})
	if e != denied {
		t.Fatal("staff inherited another division edit")
	}
	if _, e = s.batch(context.Background(), st, a, "", Batch{Deletes: []LessonRef{{1, 2}}}); e != missing {
		t.Fatal("forged scoped ID")
	}
	school := sampleLesson(2, "2026-10-08", "12:00", "13:00")
	school.Public = false
	school.SchoolID = ptr(32)
	if _, e = s.batch(context.Background(), st, a, "", Batch{Creates: []Lesson{school}}); e != missing {
		t.Fatal("foreign school accepted")
	}
	school.SchoolID = ptr(31)
	if _, e = s.batch(context.Background(), st, a, "", Batch{Creates: []Lesson{school}}); e != nil {
		t.Fatal(e)
	}
	school = sampleLesson(2, "2026-10-08", "13:00", "14:00")
	school.ForAll = false
	school.GroupIDs = []int64{12}
	_, e = s.batch(context.Background(), st, a, "", Batch{Creates: []Lesson{school}})
	if e != nil {
		t.Fatal(e)
	}
	v, _ = s.calendarRead(context.Background(), st, student, "", "2026-10-08", "2026-10-08", nil, true)
	if len(v.(map[string]any)["events"].([]Event)) != 4 {
		t.Fatal("school/group audience")
	}
}
func TestTypeHistoryProctorAndZaps(t *testing.T) {
	p := &testPeers{Org: 1}
	s := Server{Peers: p}
	st := NewState()
	a := fixtureIdentity(1, "admin")
	typ := Type{Code: "custom_present", Name: "Present", Active: true, Presence: true, ScanDefault: true, Color: "#00ff00"}
	v, e := typeSave(st, 2, 0, typ)
	if e != nil {
		t.Fatal(e)
	}
	tid := v.(Type).ID
	v, e = daySave(st, 2, 0, Day{Date: "2026-10-08"})
	if e != nil {
		t.Fatal(e)
	}
	day := v.(Day).ID
	_, e = s.setMark(context.Background(), st, fixtureIdentity(1, "proctor"), 2, day, Mark{6, tid}, "a", false)
	if e != denied {
		t.Fatal("proctor crossed group", e)
	}
	_, e = s.setMark(context.Background(), st, fixtureIdentity(1, "proctor"), 2, day, Mark{7, tid}, "a", false)
	if e != nil {
		t.Fatal(e)
	}
	typ.Name = "Renamed"
	typ.Presence = false
	typ.Absence = true
	typ.ScanDefault = false
	_, e = typeSave(st, 2, tid, typ)
	if e != nil {
		t.Fatal(e)
	}
	if st.Entries[key(day, 7)].Type.Name != "Present" || !st.Entries[key(day, 7)].Type.Presence {
		t.Fatal("history recounted")
	}
	if _, e = daySave(st, 2, day, Day{Date: "2026-10-09"}); errorCode(e) != "day_has_history" {
		t.Fatal("moved historical day", e)
	}
	if _, e = s.deleteMark(context.Background(), st, a, 2, day, 7); e != nil {
		t.Fatal(e)
	}
	if !st.Entries[key(day, 7)].Deleted || !st.Days[day].HasHistory {
		t.Fatal("deleted history")
	}
	excused := Type{Code: "excused_config", Name: "Reason", Active: true, Absence: true, Excused: true, ZapDefault: true, Color: "#aaaa00"}
	if _, e = typeSave(st, 2, 0, excused); e != nil {
		t.Fatal(e)
	}
	zv, e := s.zapCreate(context.Background(), st, fixtureIdentity(1, "student"), 2, ZapInput{Text: "Synthetic reason", Dates: []string{"2026-10-08", "2026-10-10"}}, "zap")
	if e != nil {
		t.Fatal(e)
	}
	z := zv.(Zap)
	_, e = s.zapProcess(context.Background(), st, a, 2, z.ID, ZapDecision{"approved", "Accepted"}, "process")
	if e != nil {
		t.Fatal(e)
	}
	z = st.Zaps[z.ID]
	if z.Dates[0].Status != "linked" || z.Dates[1].Status != "no_class_day" {
		t.Fatal("zap dates", z)
	}
	_, e = s.setMark(context.Background(), st, a, 2, day, Mark{7, tid}, "manual", false)
	if errorCode(e) != "attendance_linked_to_zap" {
		t.Fatal("manual zap override")
	}
	_, e = s.zapDateAction(context.Background(), st, a, 2, z.ID, "2026-10-08", true, "unlink")
	if e != nil || !st.Entries[key(day, 7)].Deleted {
		t.Fatal("unlink", e)
	}
	v, e = daySave(st, 2, 0, Day{Date: "2026-10-10"})
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.zapDateAction(context.Background(), st, a, 2, z.ID, "2026-10-10", false, "retry")
	if e != nil || st.Zaps[z.ID].Dates[1].Status != "linked" {
		t.Fatal("retry", e)
	}
	p.GuardErr = guardMissing
	if _, e = s.zapCreate(context.Background(), st, a, 2, ZapInput{AccountID: 7, Text: "test", Dates: []string{"2026-10-11"}}, "missing"); e != guardMissing {
		t.Fatal("unguarded history")
	}
}
func TestQRAndStrictJSON(t *testing.T) {
	s := Server{QRKey: []byte(strings.Repeat("x", 32))}
	c := QRClaims{1, 2, 3, 7, time.Now().Add(time.Minute).Unix()}
	q := s.signQR(c)
	if _, e := s.verifyQR(q); e != nil {
		t.Fatal(e)
	}
	if _, e := s.verifyQR(q + "x"); e == nil {
		t.Fatal("forged qr")
	}
	c.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	if _, e := s.verifyQR(s.signQR(c)); e == nil {
		t.Fatal("expired qr")
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"data":{"x":1,"x":2}}`))
	r.Header.Set("Content-Type", "application/json")
	if _, e := body(httptest.NewRecorder(), r); e == nil {
		t.Fatal("duplicate accepted")
	}
	if decodeJSON([]byte(`{"account_id":7,"extra":8}`), &Mark{}) == nil {
		t.Fatal("unknown field")
	}
}
func TestHTTPPeersFailures(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/introspect":
			reply(w, 200, fixtureIdentity(1, "admin"))
		default:
			http.NotFound(w, r)
		}
	}))
	p := NewPeers(up.URL, up.URL, strings.Repeat("k", 32))
	a, e := p.Identify(context.Background(), "Bearer fixture")
	if e != nil || !a.admin() {
		t.Fatal(e)
	}
	if p.Guard(context.Background(), 1, 2, 7, "key") != guardMissing {
		t.Fatal("missing guard allowed")
	}
	up.Close()
	if _, e = p.Identify(context.Background(), "Bearer fixture"); e != unavailable {
		t.Fatal("auth outage allowed")
	}
}
func TestOpenAPIMatchesRoutes(t *testing.T) {
	spec := OpenAPI()
	paths := spec["paths"].(map[string]any)
	for _, rt := range routes() {
		p := strings.SplitN(rt.Pattern, " ", 2)
		item := paths[p[1]].(map[string]any)
		if item[strings.ToLower(p[0])] == nil {
			t.Fatal(rt.Pattern)
		}
	}
}

func testDatabase(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("ATTENDANCE_TEST_DSN")
	if dsn == "" {
		t.Skip("real isolated MySQL integration requires ATTENDANCE_TEST_DSN")
	}
	db, e := sql.Open("mysql", dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	var name string
	if e = db.QueryRow("SELECT DATABASE()").Scan(&name); e != nil || name != "cpm_dev" {
		t.Fatal("only cpm_dev", e)
	}
	b, e := os.ReadFile(filepath.Join("..", "..", "migrations", "001_attendance.sql"))
	if e != nil {
		t.Fatal(e)
	}
	for _, q := range strings.Split(string(b), ";") {
		if strings.TrimSpace(q) != "" {
			if _, e = db.Exec(q); e != nil {
				t.Fatal(e)
			}
		}
	}
	return db
}
func TestMySQLHTTPAtomicityAndIsolation(t *testing.T) {
	db := testDatabase(t)
	o := time.Now().UnixNano() / 1000
	defer func() {
		for _, table := range []string{"attendance_audit_v1", "attendance_receipts_v1", "attendance_state_v1"} {
			_, _ = db.Exec("DELETE FROM "+table+" WHERE organization_id IN (?,?)", o, o+1)
		}
	}()
	peers := &testPeers{Org: o}
	s := Server{Store: Store{db}, Peers: peers, QRKey: []byte(strings.Repeat("q", 32))}
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	call := func(role, method, path string, input any, key string) (int, Result, string) {
		t.Helper()
		b, _ := json.Marshal(input)
		req, _ := http.NewRequest(method, server.URL+path, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+role)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		res, e := server.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		var out struct {
			Status bool   `json:"status"`
			Data   Result `json:"data"`
			Error  string `json:"error"`
		}
		if json.NewDecoder(res.Body).Decode(&out) != nil {
			t.Fatal("invalid response")
		}
		return res.StatusCode, out.Data, out.Error
	}
	mut := func(rev int64, data any) any { return map[string]any{"expected_revision": rev, "data": data} }
	for i := int64(1); i <= 3; i++ {
		status, _, err := call("admin", "PUT", "/v1/attendance/divisions/"+itoa(i)+"/calendar", mut(i-1, Calendar{Visibility: "organization", Timezone: "Europe/Moscow"}), "calendar-"+itoa(i))
		if status != 200 {
			t.Fatal(status, err)
		}
	}
	lesson := sampleLesson(1, "2026-10-08", "09:00", "10:00")
	status, result, err := call("admin", "POST", "/v1/attendance/schedule/bulk", mut(3, Batch{Creates: []Lesson{lesson, sampleLesson(2, "2026-10-08", "09:30", "10:30")}}), "initial-batch")
	if status != 200 || result.Revision != 4 {
		t.Fatal(status, err)
	}
	status, result, err = call("admin", "POST", "/v1/attendance/schedule/bulk", mut(3, Batch{Creates: []Lesson{lesson, sampleLesson(2, "2026-10-08", "09:30", "10:30")}}), "initial-batch")
	if status != 200 || result.Revision != 4 {
		t.Fatal("receipt retry", status, err)
	}
	status, _, _ = call("admin", "POST", "/v1/attendance/schedule/bulk", mut(3, Batch{Creates: []Lesson{lesson}}), "initial-batch")
	if status != 409 {
		t.Fatal("changed receipt")
	}
	status, _, err = call("student", "GET", "/v1/attendance/me/schedule?date_from=2026-10-08&date_to=2026-10-08", nil, "")
	if status != 200 {
		t.Fatal(status, err)
	}
	status, _, _ = call("student", "GET", "/v1/attendance/divisions/3/attendance-report?date_from=2026-10-08&date_to=2026-10-08", nil, "")
	if status != 403 {
		t.Fatal("published journal")
	}
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status, _, _ := call("admin", "POST", "/v1/attendance/divisions/2/class-days", mut(4, Day{Date: "2026-10-0" + itoa(int64(i+8))}), "concurrent-"+itoa(int64(i)))
			statuses <- status
		}(i)
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("CAS winners", counts)
	}
	_, rev, e := s.Store.Read(context.Background(), o)
	if e != nil || rev != 5 {
		t.Fatal("revision", rev, e)
	}
	var audits int
	if e = db.QueryRow("SELECT COUNT(*) FROM attendance_audit_v1 WHERE organization_id=?", o).Scan(&audits); e != nil || audits != 5 {
		t.Fatal("audit", audits, e)
	}
	other, otherRev, e := s.Store.Read(context.Background(), o+1)
	if e != nil || otherRev != 0 || len(other.Lessons) != 0 {
		t.Fatal("org isolation")
	}
}
func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func (p *testPeers) Schools(ctx context.Context, token string) ([]SchoolReference, error) {
	return []SchoolReference{{31, "Synthetic school", true}}, p.CoreErr
}

func TestMySQLHTTPJournalScanAndGuard(t *testing.T) {
	db := testDatabase(t)
	o := time.Now().UnixNano() / 1000
	defer func() {
		for _, table := range []string{"attendance_audit_v1", "attendance_receipts_v1", "attendance_state_v1"} {
			_, _ = db.Exec("DELETE FROM "+table+" WHERE organization_id=?", o)
		}
	}()
	p := &testPeers{Org: o}
	s := Server{Store: Store{db}, Peers: p, QRKey: []byte(strings.Repeat("q", 32))}
	h := s.Handler()
	rev := int64(0)
	serial := int64(0)
	call := func(role, method, path string, data any, want int) json.RawMessage {
		t.Helper()
		serial++
		input := data
		if method != "GET" && !strings.HasSuffix(path, "/qr") {
			input = map[string]any{"expected_revision": rev, "data": data}
		}
		b, _ := json.Marshal(input)
		r := httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer "+role)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "journal-test-"+itoa(serial))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		var env struct {
			Data struct {
				Revision int64           `json:"revision"`
				Value    json.RawMessage `json:"value"`
			} `json:"data"`
		}
		if json.Unmarshal(w.Body.Bytes(), &env) != nil {
			t.Fatal("response")
		}
		if want == 200 {
			rev = env.Data.Revision
		}
		return env.Data.Value
	}
	parse := func(b json.RawMessage, out any) {
		t.Helper()
		if json.Unmarshal(b, out) != nil {
			t.Fatal("decode", string(b))
		}
	}
	present := Type{Code: "synthetic_present", Name: "Custom presence", Color: "#00aa00", Active: true, Presence: true, ScanDefault: true}
	var typ Type
	parse(call("admin", "POST", "/v1/attendance/divisions/2/attendance-types", present, 200), &typ)
	var day Day
	parse(call("admin", "POST", "/v1/attendance/divisions/2/class-days", Day{Date: "2026-10-08"}, 200), &day)
	path := "/v1/attendance/divisions/2/class-days/" + itoa(day.ID)
	call("admin", "POST", path+"/attendance", Mark{7, 9999}, 400)
	call("proctor", "POST", path+"/attendance", Mark{6, typ.ID}, 403)
	var qr struct {
		QR string `json:"qr"`
	}
	parse(call("student", "POST", path+"/qr", struct{}{}, 200), &qr)
	call("scanner", "POST", path+"/attendance", Mark{7, typ.ID}, 403)
	var mark Entry
	parse(call("scanner", "POST", path+"/scan", map[string]any{"qr": qr.QR}, 200), &mark)
	firstID := mark.ID
	parse(call("journal_editor", "POST", path+"/scan", map[string]any{"qr": qr.QR}, 200), &mark)
	if mark.ID != firstID {
		t.Fatal("attendance editor scan duplicated mark")
	}
	parse(call("scanner", "POST", path+"/scan", map[string]any{"qr": qr.QR}, 200), &mark)
	if mark.ID != firstID {
		t.Fatal("scan duplicated mark")
	}
	call("scanner", "POST", path+"/scan", map[string]any{"qr": "7"}, 400)
	call("scanner", "GET", "/v1/attendance/divisions/2/attendance-report?date_from=2026-10-08&date_to=2026-10-08", nil, 403)
	call("student", "GET", path+"/attendance?account_id=6", nil, 403)
	call("admin", "DELETE", path, struct{}{}, 409)
	call("admin", "PUT", path, Day{Date: "2026-10-09"}, 409)
	present.Name = "Changed label"
	present.Active = false
	call("admin", "PUT", "/v1/attendance/divisions/2/attendance-types/"+itoa(typ.ID), present, 200)
	var report struct {
		Entries []Entry `json:"entries"`
	}
	parse(call("student", "GET", path+"/attendance", nil, 200), &report)
	if len(report.Entries) != 1 || report.Entries[0].Type.Name != "Custom presence" {
		t.Fatal("snapshot changed")
	}
	call("admin", "DELETE", path+"/attendance/7", struct{}{}, 200)
	call("admin", "DELETE", path, struct{}{}, 409)
	before := rev
	p.GuardErr = guardMissing
	call("student", "POST", "/v1/attendance/divisions/2/zaps", ZapInput{Text: "Synthetic guard negative", Dates: []string{"2026-10-08"}}, 503)
	_, after, e := s.Store.Read(context.Background(), o)
	if e != nil || after != before {
		t.Fatal("missing guard committed")
	}
	p.GuardErr = nil
	var zap Zap
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lYkAAAAASUVORK5CYII="
	parse(call("student", "POST", "/v1/attendance/divisions/2/zaps", ZapInput{Text: "Synthetic request", Dates: []string{"2026-10-08", "2026-10-10"}, Images: []Image{{MIME: "image/png", Data: png}}}, 200), &zap)
	if len(zap.Images) != 0 {
		t.Fatal("unprotected image data in create response")
	}
	excused := Type{Code: "custom_zap", Name: "Custom excuse", Color: "#ffff00", Active: true, Excused: true, Absence: true, ZapDefault: true}
	call("admin", "POST", "/v1/attendance/divisions/2/attendance-types", excused, 200)
	zpath := "/v1/attendance/divisions/2/zaps/" + itoa(zap.ID)
	var detail ZapDetail
	parse(call("student", "GET", zpath, nil, 200), &detail)
	if detail.ImageCount != 1 || len(detail.Zap.Images) != 0 {
		t.Fatal("protected image metadata")
	}
	for _, role := range []string{"student", "admin", "staff_admin", "scanner", "proctor"} {
		r := httptest.NewRequest("GET", zpath+"/images/1", nil)
		r.Header.Set("Authorization", "Bearer "+role)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		allowed := role == "student" || role == "admin" || role == "staff_admin"
		if allowed {
			want, _ := base64.StdEncoding.DecodeString(png)
			if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || !bytes.Equal(w.Body.Bytes(), want) {
				t.Fatalf("image %s %d", role, w.Code)
			}
		} else if w.Code != 404 {
			t.Fatalf("image leak to %s: %d", role, w.Code)
		}
	}
	call("staff_admin", "GET", "/v1/attendance/divisions/1/zaps/"+itoa(zap.ID), nil, 404)
	parse(call("admin", "POST", zpath+"/process", ZapDecision{"approved", "Synthetic accepted"}, 200), &zap)
	if zap.Dates[0].Status != "linked" || zap.Dates[1].Status != "no_class_day" {
		t.Fatal("approval", zap)
	}
	call("admin", "DELETE", path+"/attendance/7", struct{}{}, 409)
	call("admin", "DELETE", zpath+"/dates/2026-10-08", struct{}{}, 200)
	call("admin", "POST", zpath+"/dates/2026-10-08/retry", struct{}{}, 200)
	p.AuthErr = unavailable
	call("admin", "GET", "/v1/attendance/calendars", nil, 503)
	p.AuthErr = nil
	p.CoreErr = unavailable
	call("student", "GET", path+"/attendance", nil, 503)
}

func TestGuardMissingMembershipVersusMissingEndpoint(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply(w, 404, map[string]any{"status": false, "error": "not_found"})
	}))
	defer up.Close()
	p := NewPeers(up.URL, up.URL, strings.Repeat("k", 32))
	if e := p.Guard(context.Background(), 1, 2, 7, "fixed-key"); e != missing {
		t.Fatal("missing membership misreported as unimplemented guard", e)
	}
}
