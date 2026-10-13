package attendance

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type receiptPeers struct {
	*scopeReplayPeers
	IdentityChange func(Identity) Identity
	Guards         atomic.Int64
}

func (p *receiptPeers) Identify(ctx context.Context, token string) (Identity, error) {
	a, e := p.scopeReplayPeers.Identify(ctx, token)
	if e == nil && p.IdentityChange != nil {
		a = p.IdentityChange(a)
	}
	return a, e
}

func (p *receiptPeers) Guard(ctx context.Context, o, d, u int64, k string) error {
	p.Guards.Add(1)
	return p.testPeers.Guard(ctx, o, d, u, k)
}

type receiptFixture struct {
	t        *testing.T
	db       *sql.DB
	org      int64
	p        *receiptPeers
	s        *Server
	h        http.Handler
	revision int64
	sequence int
}

type receiptCommand struct {
	ID, Role, Method, Path string
	Body, Wire             []byte
	Result                 Result
}

func newReceiptFixture(t *testing.T) *receiptFixture {
	t.Helper()
	db := testDatabase(t)
	o := time.Now().UnixNano()
	t.Cleanup(func() {
		for _, table := range []string{"attendance_audit_v1", "attendance_receipts_v1", "attendance_state_v1"} {
			if _, e := db.Exec("DELETE FROM "+table+" WHERE organization_id=?", o); e != nil {
				t.Error("fixture cleanup", e)
			}
		}
	})
	p := &receiptPeers{scopeReplayPeers: &scopeReplayPeers{testPeers: &testPeers{Org: o}, Archived: map[int64]bool{}, TargetGroup: 11, ProctorGroup: 11, Assigned: true}}
	s := &Server{Store: Store{db}, Peers: p, QRKey: []byte(strings.Repeat("q", 32))}
	return &receiptFixture{t: t, db: db, org: o, p: p, s: s, h: s.Handler()}
}

func (f *receiptFixture) uuid() string {
	f.sequence++
	return fmt.Sprintf("%08x-0000-4000-8000-%012x", f.sequence, f.sequence)
}

func (f *receiptFixture) call(role, method, path string, b []byte, id string, want int, code string) ([]byte, Result) {
	f.t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r.Header.Set("Authorization", "Bearer "+role)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", id)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != want {
		f.t.Fatalf("%s %s: got %d want %d: %s", role, path, w.Code, want, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		f.t.Fatal("receipt response must not be cached/sniffed")
	}
	var out struct {
		Status bool   `json:"status"`
		Data   Result `json:"data"`
		Error  string `json:"error"`
	}
	decoder := json.NewDecoder(bytes.NewReader(w.Body.Bytes()))
	decoder.UseNumber()
	if decoder.Decode(&out) != nil || out.Error != code || out.Status != (want == 200) {
		f.t.Fatalf("invalid response: %s", w.Body.String())
	}
	return append([]byte(nil), w.Body.Bytes()...), out.Data
}

func (f *receiptFixture) write(role, method, path string, data any) receiptCommand {
	f.t.Helper()
	b, e := json.Marshal(map[string]any{"expected_revision": f.revision, "data": data})
	if e != nil {
		f.t.Fatal(e)
	}
	id := f.uuid()
	wire, result := f.call(role, method, path, b, id, 200, "")
	if result.Revision != f.revision+1 {
		f.t.Fatal("new command did not increment revision once")
	}
	f.revision = result.Revision
	return receiptCommand{id, role, method, path, b, wire, result}
}

func receiptValue[T any](t *testing.T, result Result) T {
	t.Helper()
	b, e := json.Marshal(result.Value)
	var v T
	if e != nil || json.Unmarshal(b, &v) != nil {
		t.Fatal("invalid result value")
	}
	return v
}

func sameReceiptWire(t *testing.T, first, replay []byte) bool {
	t.Helper()
	decode := func(b []byte) any {
		d := json.NewDecoder(bytes.NewReader(b))
		d.UseNumber()
		var v any
		if d.Decode(&v) != nil {
			t.Fatal("invalid public wire")
		}
		return v
	}
	return reflect.DeepEqual(decode(first), decode(replay))
}

// Fingerprint every owned persistence row, not just revision/counts: GET cannot
// silently backfill legacy receipts or repair corrupted evidence either.
func (f *receiptFixture) fingerprint() string {
	f.t.Helper()
	var all bytes.Buffer
	for _, q := range []string{
		"SELECT revision,document FROM attendance_state_v1 WHERE organization_id=? ORDER BY revision",
		"SELECT revision,actor_account_id,action,snapshot FROM attendance_audit_v1 WHERE organization_id=? ORDER BY revision",
		"SELECT actor_account_id,request_key,request_hash,response FROM attendance_receipts_v1 WHERE organization_id=? ORDER BY actor_account_id,request_key",
	} {
		rows, e := f.db.Query(q, f.org)
		if e != nil {
			f.t.Fatal(e)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			values := make([][]byte, len(cols))
			args := make([]any, len(cols))
			for i := range values {
				args[i] = &values[i]
			}
			if rows.Scan(args...) != nil {
				f.t.Fatal("fixture fingerprint scan")
			}
			b, _ := json.Marshal(values)
			all.Write(b)
		}
		if e = rows.Err(); e != nil {
			f.t.Fatal(e)
		}
		rows.Close()
	}
	fmt.Fprint(&all, f.p.Guards.Load())
	return hash(all.Bytes())
}

func (f *receiptFixture) get(role, id string, want int, code string) (Result, []byte) {
	f.t.Helper()
	before := f.fingerprint()
	wire, result := f.call(role, "GET", "/v1/attendance/command-receipts/"+id, nil, "", want, code)
	if f.fingerprint() != before {
		f.t.Fatal("receipt GET changed state/audit/receipt/history guard")
	}
	return result, wire
}

func (f *receiptFixture) assertReceipt(c receiptCommand, scope string, ids []int64) {
	f.t.Helper()
	result, wire := f.get(c.Role, c.ID, 200, "")
	v := receiptValue[CommandReceipt](f.t, result)
	want := CommandReceipt{1, c.ID, "committed", f.org, scope, ids, c.Method, strings.TrimPrefix(c.Path, "/v1/attendance")}
	if result.Revision != c.Result.Revision || !reflect.DeepEqual(v, want) {
		f.t.Fatalf("receipt metadata differs: %+v want %+v; revision %d want %d", v, want, result.Revision, c.Result.Revision)
	}
	var envelope map[string]json.RawMessage
	var data map[string]json.RawMessage
	var value map[string]json.RawMessage
	_ = json.Unmarshal(wire, &envelope)
	_ = json.Unmarshal(envelope["data"], &data)
	_ = json.Unmarshal(data["value"], &value)
	if len(envelope) != 2 || len(data) != 2 || len(value) != 8 {
		f.t.Fatal("receipt leaked non-metadata fields", string(wire))
	}
}

func (f *receiptFixture) receiptJSON(c receiptCommand) map[string]json.RawMessage {
	f.t.Helper()
	var b []byte
	if e := f.db.QueryRow("SELECT response FROM attendance_receipts_v1 WHERE organization_id=? AND actor_account_id=? AND request_key=?", f.org, fixtureIdentity(f.org, c.Role).User.AccountID, c.ID).Scan(&b); e != nil {
		f.t.Fatal(e)
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(b, &obj) != nil {
		f.t.Fatal("invalid stored receipt")
	}
	return obj
}

func (f *receiptFixture) replaceReceipt(c receiptCommand, obj map[string]json.RawMessage) {
	f.t.Helper()
	b, _ := json.Marshal(obj)
	if _, e := f.db.Exec("UPDATE attendance_receipts_v1 SET response=? WHERE organization_id=? AND actor_account_id=? AND request_key=?", b, f.org, fixtureIdentity(f.org, c.Role).User.AccountID, c.ID); e != nil {
		f.t.Fatal(e)
	}
}

func TestCommandReceiptScopeAndOpenAPI(t *testing.T) {
	batch := Batch{Creates: []Lesson{{DivisionID: 3}, {DivisionID: 1}}, Updates: []Lesson{{ID: 5, DivisionID: 2}, {ID: 6, DivisionID: 1}}, Deletes: []LessonRef{{ID: 8, DivisionID: 3}}}
	b, _ := json.Marshal(batch)
	meta, e := scopeAfterWrite("POST /v1/attendance/schedule/bulk", "batch", 0, b)
	if e != nil || !reflect.DeepEqual(meta.DivisionIDs, []int64{1, 2, 3}) {
		t.Fatal("original bulk inputs did not preserve all targets", meta, e)
	}
	for _, action := range []string{"GET /v1/attendance/calendars", "POST /v1/attendance/divisions/2/class-days/1/qr", "POST /v1/attendance/divisions/2/class-days/1/scan?qr=secret", "POST /v1/attendance/schedule/bulk/extra", "POST /v1/attendance/divisions/2/zaps/1/dates/2026-02-30/retry"} {
		if _, e := originalCommand(action); e != unavailable {
			t.Fatal("non-command accepted", action, e)
		}
	}
	legacy, e := originalCommand("DELETE /v1/attendance/divisions/+2/class-days/01")
	if e != nil || legacy.Division != 2 || legacy.Path != "/divisions/+2/class-days/01" {
		t.Fatal("existing native numeric path was rewritten", legacy, e)
	}
	spec := OpenAPI()
	get := spec["paths"].(map[string]any)["/v1/attendance/command-receipts/{command_id}"].(map[string]any)["get"].(map[string]any)
	if get["requestBody"] != nil || len(get["parameters"].([]any)) != 1 {
		t.Fatal("receipt OpenAPI allows a body/query")
	}
	param := get["parameters"].([]any)[0].(map[string]any)["schema"].(map[string]any)
	if param["type"] != "string" || param["pattern"] != commandUUID.String() {
		t.Fatal("receipt OpenAPI UUID contract")
	}
	public := spec["components"].(map[string]any)["schemas"].(map[string]any)["CommandReceipt"].(map[string]any)
	if len(public["properties"].(map[string]any)) != 8 || len(public["required"].([]string)) != 8 {
		t.Fatal("receipt public metadata contract")
	}
}

func TestCommandReceiptRejectsParametersBeforeLookup(t *testing.T) {
	s := &Server{Peers: &testPeers{Org: 1}}
	h := s.Handler() // Deliberately no DB: invalid requests must never reach Store.
	id := "00000001-0000-4000-8000-000000000001"
	for _, tc := range []struct {
		method, suffix, body, code string
		status                     int
	}{
		{"GET", id + "?division_id=2", "", "invalid_receipt_request", 400},
		{"GET", id + "?", "", "invalid_receipt_request", 400},
		{"GET", id + "?x=%zz", "", "invalid_receipt_request", 400},
		{"GET", id + "?x=1;y=2", "", "invalid_receipt_request", 400},
		{"GET", id, "{}", "invalid_receipt_request", 400},
		{"GET", id, " ", "invalid_receipt_request", 400},
		{"GET", "00000001-0000-4000-8000-00000000000A", "", "invalid_command_id", 400},
		{"GET", "not-a-uuid", "", "invalid_command_id", 400},
		{"HEAD", id, "", "method_not_allowed", 405},
	} {
		t.Run(tc.method+tc.suffix+tc.body, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "/v1/attendance/command-receipts/"+tc.suffix, strings.NewReader(tc.body))
			r.Header.Set("Authorization", "Bearer admin")
			if tc.body != "" {
				r.ContentLength = -1 // Unknown/chunked length still has a forbidden body.
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) {
				t.Fatal(w.Code, w.Body.String())
			}
			if tc.method == "HEAD" && w.Header().Get("Allow") != "GET" {
				t.Fatal("HEAD did not advertise GET only")
			}
		})
	}
	for _, token := range []string{"", "Bearer invalid"} {
		r := httptest.NewRequest("GET", "/v1/attendance/command-receipts/"+id, nil)
		r.Header.Set("Authorization", token)
		r.Header.Set("X-Service-Key", strings.Repeat("k", 32))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal("service key bypassed current Auth", w.Code)
		}
	}
}

func TestMySQLCommandReceiptPublicReplayLegacyAndDeletedObjects(t *testing.T) {
	f := newReceiptFixture(t)
	dayWrite := f.write("staff_admin", "POST", "/v1/attendance/divisions/2/class-days", Day{Date: "2026-10-08"})
	day := receiptValue[Day](t, dayWrite.Result)
	f.assertReceipt(dayWrite, "division", []int64{2}) // Cabinet 1, explicit allowed division 2.
	stored := f.receiptJSON(dayWrite)
	if stored["scope_metadata"] == nil || bytes.Contains(dayWrite.Wire, []byte("scope_metadata")) {
		t.Fatal("scope metadata storage/public separation failed")
	}
	replay, _ := f.call(dayWrite.Role, dayWrite.Method, dayWrite.Path, dayWrite.Body, dayWrite.ID, 200, "")
	if !sameReceiptWire(t, replay, dayWrite.Wire) {
		t.Fatal("public first response and replay changed")
	}
	deleted := f.write("staff_admin", "DELETE", dayWrite.Path+"/"+itoa(day.ID), struct{}{})
	f.assertReceipt(deleted, "division", []int64{2})
	f.assertReceipt(dayWrite, "division", []int64{2}) // No current object requirement.
	typeWrite := f.write("staff_admin", "POST", "/v1/attendance/divisions/2/attendance-types", Type{Code: "receipt_present", Name: "Present", Active: true, Presence: true, Color: "#00ff00"})
	typ := receiptValue[Type](t, typeWrite.Result)
	f.write("staff_admin", "DELETE", typeWrite.Path+"/"+itoa(typ.ID), struct{}{})
	f.assertReceipt(typeWrite, "division", []int64{2})
	delete(stored, "scope_metadata")
	f.replaceReceipt(dayWrite, stored)
	f.assertReceipt(dayWrite, "division", []int64{2}) // Legacy receipt; GET cannot backfill it.
	replay, _ = f.call(dayWrite.Role, dayWrite.Method, dayWrite.Path, dayWrite.Body, dayWrite.ID, 200, "")
	if !sameReceiptWire(t, replay, dayWrite.Wire) {
		t.Fatal("legacy exact replay changed public wire")
	}
	room := f.write("admin", "POST", "/v1/attendance/resources/rooms", Room{Code: "receipt_room", Name: "Secret room title"})
	f.assertReceipt(room, "organization", []int64{})
	legacyRoom := f.receiptJSON(room)
	delete(legacyRoom, "scope_metadata")
	f.replaceReceipt(room, legacyRoom)
	f.assertReceipt(room, "organization", []int64{})
	f.get("staff_admin", room.ID, 403, "forbidden") // Same actor 9, current admin was removed.
	f.get("student", room.ID, 404, "not_found")
	f.p.IdentityChange = func(a Identity) Identity {
		a.User.OrganizationID = f.org + 1
		return a
	}
	f.get("admin", room.ID, 404, "not_found")
	f.p.IdentityChange = nil
	f.get("admin", f.uuid(), 404, "not_found")
	f.p.IdentityChange = func(a Identity) Identity {
		if a.User.Role == "staff_admin" {
			a.Organizations[0].Divisions[1].Permissions["attendance"] = Permission{View: true}
		}
		return a
	}
	f.get("staff_admin", dayWrite.ID, 403, "forbidden")
	f.p.IdentityChange = nil
	f.assertReceipt(dayWrite, "division", []int64{2})
	f.p.Archived[2] = true
	f.get("staff_admin", dayWrite.ID, 404, "not_found")
	f.p.Archived[2] = false
	f.p.CoreErr = unavailable
	f.get("staff_admin", dayWrite.ID, 503, "dependency_unavailable")
	f.p.CoreErr = nil
	f.p.AuthErr = &APIError{401, "invalid_token"}
	f.get("staff_admin", dayWrite.ID, 401, "invalid_token")
	f.p.AuthErr = nil
	f.assertReceipt(dayWrite, "division", []int64{2})
}

func TestMySQLCommandReceiptBulkIncludesNoopAndDeletedDivision(t *testing.T) {
	f := newReceiptFixture(t)
	for d := int64(1); d <= 3; d++ {
		f.write("admin", "PUT", "/v1/attendance/divisions/"+itoa(d)+"/calendar", Calendar{Visibility: "organization", Timezone: "Europe/Moscow"})
	}
	seed := f.write("admin", "POST", "/v1/attendance/schedule/bulk", Batch{Creates: []Lesson{sampleLesson(2, "2026-10-08", "09:00", "10:00"), sampleLesson(3, "2026-10-08", "10:00", "11:00")}})
	ids := receiptValue[BatchResult](t, seed.Result).IDs
	st, _, e := f.s.Store.Read(context.Background(), f.org)
	if e != nil {
		t.Fatal(e)
	}
	noop := st.Lessons[ids[0]]
	b := Batch{Creates: []Lesson{sampleLesson(1, "2026-10-08", "12:00", "13:00")}, Updates: []Lesson{noop}, Deletes: []LessonRef{{ID: ids[1], DivisionID: 3}}}
	bulk := f.write("admin", "POST", "/v1/attendance/schedule/bulk", b)
	f.assertReceipt(bulk, "divisions", []int64{1, 2, 3})
	st, _, _ = f.s.Store.Read(context.Background(), f.org)
	if !reflect.DeepEqual(st.Lessons[noop.ID], noop) || len(st.Lessons) != 2 {
		t.Fatal("fixture did not exercise noop and deletion")
	}
	for _, revoked := range []int64{2, 3} {
		f.p.IdentityChange = func(a Identity) Identity {
			a.User.Role, a.User.OrganizationAdmin = "staff_admin", false
			a.Organizations[0].Divisions = nil
			for d := int64(1); d <= 3; d++ {
				p := Permission{View: true, Edit: d != revoked}
				a.Organizations[0].Divisions = append(a.Organizations[0].Divisions, Division{ID: d, Permissions: map[string]Permission{"schedule": p}})
			}
			return a
		}
		f.get("admin", bulk.ID, 403, "forbidden")
	}
	f.p.IdentityChange = nil
	f.assertReceipt(bulk, "divisions", []int64{1, 2, 3})
	f.p.Archived[3] = true
	f.get("admin", bulk.ID, 404, "not_found")
	f.p.Archived[3] = false
	legacy := f.receiptJSON(bulk)
	delete(legacy, "scope_metadata")
	f.replaceReceipt(bulk, legacy)
	f.get("admin", bulk.ID, 409, "receipt_scope_unverifiable")
	replay, _ := f.call(bulk.Role, bulk.Method, bulk.Path, bulk.Body, bulk.ID, 200, "")
	if !sameReceiptWire(t, replay, bulk.Wire) {
		t.Fatal("legacy bulk public retry changed")
	}
}

func TestMySQLCommandReceiptProctorCurrentAndImmutableAuthorization(t *testing.T) {
	f := newReceiptFixture(t)
	typeWrite := f.write("admin", "POST", "/v1/attendance/divisions/2/attendance-types", Type{Code: "receipt_scan", Name: "Present", Active: true, Presence: true, ScanDefault: true, Color: "#00ff00"})
	typ := receiptValue[Type](t, typeWrite.Result)
	dayWrite := f.write("admin", "POST", "/v1/attendance/divisions/2/class-days", Day{Date: "2026-10-08"})
	day := receiptValue[Day](t, dayWrite.Result)
	path := dayWrite.Path + "/" + itoa(day.ID)
	marked := f.write("proctor", "POST", path+"/attendance", Mark{7, typ.ID})
	unmarked := f.write("proctor", "DELETE", path+"/attendance/7", struct{}{})
	claims := QRClaims{f.org, 2, day.ID, 7, time.Now().Add(2 * time.Second).Unix()}
	qr := f.s.signQR(claims)
	scanned := f.write("proctor", "POST", path+"/scan", map[string]string{"qr": qr})
	// Expiry is real, not a changed signed body: only the receipt is read after
	// the previously valid scan token expires.
	if wait := time.Until(time.Unix(claims.ExpiresAt, 0)); wait > 0 {
		timer := time.NewTimer(wait)
		<-timer.C
	}
	if _, e := f.s.verifyQR(qr); errorCode(e) != "expired_or_invalid_qr" {
		t.Fatal("test scan QR did not expire", e)
	}
	f.assertReceipt(scanned, "division", []int64{2})
	f.s.QRKey = []byte(strings.Repeat("r", 32))
	if _, e := f.s.verifyQR(qr); e == nil {
		t.Fatal("test QR still verifies after rotation")
	}
	commands := []receiptCommand{marked, unmarked, scanned}
	for _, c := range commands {
		f.assertReceipt(c, "division", []int64{2})
	}
	f.p.Assigned = false
	for _, c := range commands {
		f.get("proctor", c.ID, 403, "forbidden")
	}
	f.p.Assigned = true
	f.p.ProctorGroup, f.p.TargetGroup = 12, 12
	f.write("proctor", "POST", path+"/attendance", Mark{7, typ.ID}) // Overwrites current entry group.
	for _, c := range commands {
		f.get("proctor", c.ID, 403, "forbidden")
	}
	f.p.ProctorGroup, f.p.TargetGroup = 11, 11
	for _, c := range commands {
		f.assertReceipt(c, "division", []int64{2})
	}
	f.p.TargetGroup = 12
	for _, c := range commands {
		f.get("proctor", c.ID, 403, "forbidden")
	}
	f.p.TargetGroup = 11
	f.p.ProctorGroup = 0
	for _, c := range commands {
		f.get("proctor", c.ID, 403, "forbidden")
	}
	f.p.ProctorGroup = 11
	f.p.CoreErr = unavailable
	for _, c := range commands {
		f.get("proctor", c.ID, 503, "dependency_unavailable")
	}
	f.p.CoreErr = nil
	legacy := f.receiptJSON(unmarked)
	delete(legacy, "scope_metadata")
	f.replaceReceipt(unmarked, legacy)
	f.assertReceipt(unmarked, "division", []int64{2})
	corrupt := f.receiptJSON(marked)
	entry := receiptValue[Entry](t, marked.Result)
	entry.GroupID = ptr(12)
	corrupt["value"], _ = json.Marshal(entry)
	f.replaceReceipt(marked, corrupt)
	f.get("proctor", marked.ID, 503, "dependency_unavailable")
	f.p.IdentityChange = func(a Identity) Identity { a.User.Role, a.User.OrganizationAdmin = "admin", true; return a }
	f.get("proctor", marked.ID, 503, "dependency_unavailable") // Full admin still needs intact historical evidence.
}

func TestMySQLCommandReceiptFailedWriteAndScopeRollBack(t *testing.T) {
	f := newReceiptFixture(t)
	a := fixtureIdentity(f.org, "admin")
	before := f.fingerprint()
	built := false
	_, e := f.s.Store.Mutate(context.Background(), a, 0, f.uuid(), "POST /v1/attendance/divisions/2/class-days", []byte("{}"), nil, func(st *State) (any, error) {
		st.allocate()
		return nil, bad("invalid_day")
	}, func() (commandScope, error) { built = true; return commandScope{}, nil })
	if errorCode(e) != "invalid_day" || built || f.fingerprint() != before {
		t.Fatal("failed typed write built scope or persisted state/audit/receipt", e)
	}
	_, e = f.s.Store.Mutate(context.Background(), a, 0, f.uuid(), "POST /v1/attendance/divisions/2/class-days", []byte("{}"), nil, func(st *State) (any, error) {
		return daySave(st, 2, 0, Day{Date: "2026-10-08"})
	}, func() (commandScope, error) { return commandScope{}, unavailable })
	if e != unavailable || f.fingerprint() != before {
		t.Fatal("failed scope builder persisted state/audit/receipt", e)
	}
}

func TestMySQLCommandReceiptStudentZapOwnership(t *testing.T) {
	f := newReceiptFixture(t)
	student := f.write("student", "POST", "/v1/attendance/divisions/2/zaps", ZapInput{Text: "Private student text", Dates: []string{"2026-10-08"}})
	f.assertReceipt(student, "division", []int64{2})
	staff := f.write("staff_admin", "POST", student.Path, ZapInput{AccountID: 7, Text: "Private staff text", Dates: []string{"2026-10-09"}})
	f.p.IdentityChange = func(a Identity) Identity {
		if a.User.AccountID == 9 {
			a.User.Role = "student"
		}
		return a
	}
	f.get("staff_admin", staff.ID, 403, "forbidden") // Same receipt actor, now student 9 cannot recover student's zap 7.
	f.p.IdentityChange = nil
	f.assertReceipt(staff, "division", []int64{2})
	corrupt := f.receiptJSON(student)
	zap := receiptValue[Zap](t, student.Result)
	zap.AccountID = 6
	corrupt["value"], _ = json.Marshal(zap)
	f.replaceReceipt(student, corrupt)
	f.get("student", student.ID, 503, "dependency_unavailable")
}

func TestMySQLCommandReceiptCorruptEvidenceFailsClosed(t *testing.T) {
	f := newReceiptFixture(t)
	c := f.write("admin", "POST", "/v1/attendance/divisions/2/class-days", Day{Date: "2026-10-08"})
	clean := f.receiptJSON(c)
	copyJSON := func() map[string]json.RawMessage {
		m := map[string]json.RawMessage{}
		for k, v := range clean {
			m[k] = append([]byte(nil), v...)
		}
		return m
	}
	for _, raw := range []string{"null", "{}", `{"version":1}`, `{"version":2,"method":"POST","path":"/divisions/2/class-days","kind":"day","scope":"division","division_ids":[2]}`, `{"version":1,"method":"POST","path":"/divisions/2/class-days","kind":"day","scope":"division","division_ids":[2,2]}`, `{"version":1,"method":"POST","path":"/divisions/2/class-days","kind":"day","scope":"division","division_ids":null}`, `{"version":1,"method":"POST","path":"/divisions/2/class-days","kind":"day","scope":"division","division_ids":[2],"unexpected":true}`} {
		m := copyJSON()
		m["scope_metadata"] = json.RawMessage(raw)
		f.replaceReceipt(c, m)
		f.get("admin", c.ID, 503, "dependency_unavailable")
	}
	var scope map[string]json.RawMessage
	_ = json.Unmarshal(clean["scope_metadata"], &scope)
	for field := range scope {
		m := copyJSON()
		partial := map[string]json.RawMessage{}
		for k, v := range scope {
			if k != field {
				partial[k] = v
			}
		}
		m["scope_metadata"], _ = json.Marshal(partial)
		f.replaceReceipt(c, m)
		f.get("admin", c.ID, 503, "dependency_unavailable")
	}
	for field, invalid := range map[string]string{"revision": "0", "value": "null", "unexpected": "true"} {
		m := copyJSON()
		m[field] = json.RawMessage(invalid)
		f.replaceReceipt(c, m)
		f.get("admin", c.ID, 503, "dependency_unavailable")
	}
	f.replaceReceipt(c, clean)
	var action string
	var snapshot []byte
	if e := f.db.QueryRow("SELECT action,snapshot FROM attendance_audit_v1 WHERE organization_id=? AND revision=?", f.org, c.Result.Revision).Scan(&action, &snapshot); e != nil {
		t.Fatal(e)
	}
	for _, invalid := range []string{"GET /v1/attendance/divisions/2/class-days", "POST /v1/attendance/divisions/1/class-days", "POST /v1/attendance/divisions/2/class-days/1/qr", "unrecognized action"} {
		if _, e := f.db.Exec("UPDATE attendance_audit_v1 SET action=? WHERE organization_id=? AND revision=?", invalid, f.org, c.Result.Revision); e != nil {
			t.Fatal(e)
		}
		f.get("admin", c.ID, 503, "dependency_unavailable")
	}
	if _, e := f.db.Exec("UPDATE attendance_audit_v1 SET action=?,actor_account_id=? WHERE organization_id=? AND revision=?", action, 8, f.org, c.Result.Revision); e != nil {
		t.Fatal(e)
	}
	f.get("admin", c.ID, 503, "dependency_unavailable")
	for _, invalid := range []string{"null", "{}", `{"next_id":1}`} {
		if _, e := f.db.Exec("UPDATE attendance_audit_v1 SET actor_account_id=?,snapshot=? WHERE organization_id=? AND revision=?", 9, invalid, f.org, c.Result.Revision); e != nil {
			t.Fatal(e)
		}
		f.get("admin", c.ID, 503, "dependency_unavailable")
	}
	if _, e := f.db.Exec("UPDATE attendance_audit_v1 SET snapshot=? WHERE organization_id=? AND revision=?", snapshot, f.org, c.Result.Revision); e != nil {
		t.Fatal(e)
	}
	f.assertReceipt(c, "division", []int64{2})
	if _, e := f.db.Exec("DELETE FROM attendance_audit_v1 WHERE organization_id=? AND revision=?", f.org, c.Result.Revision); e != nil {
		t.Fatal(e)
	}
	f.get("admin", c.ID, 503, "dependency_unavailable")
}

type lateReceiptPeers struct {
	*receiptPeers
	entered, release chan struct{}
	once             sync.Once
}

func (p *lateReceiptPeers) Division(ctx context.Context, o, d int64) (string, error) {
	p.once.Do(func() { close(p.entered); <-p.release })
	return p.receiptPeers.Division(ctx, o, d)
}

func TestMySQLCommandReceiptMissingDoesNotPreventLateCommit(t *testing.T) {
	f := newReceiptFixture(t)
	p := &lateReceiptPeers{receiptPeers: f.p, entered: make(chan struct{}), release: make(chan struct{})}
	f.s.Peers = p
	f.h = f.s.Handler()
	id := f.uuid()
	b, _ := json.Marshal(map[string]any{"expected_revision": 0, "data": Day{Date: "2026-10-08"}})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("POST", "/v1/attendance/divisions/2/class-days", bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer admin")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", id)
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, r)
		done <- w
	}()
	select {
	case <-p.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("original request did not reach pre-Mutate gate")
	}
	// The original has passed Auth but has not entered Mutate. Scoped absence
	// cannot be advertised as cancellation/non-commit and writes no state row.
	f.get("admin", id, 404, "not_found")
	close(p.release)
	var response *httptest.ResponseRecorder
	select {
	case response = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("original command did not finish")
	}
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var envelope struct {
		Data Result `json:"data"`
	}
	if json.Unmarshal(response.Body.Bytes(), &envelope) != nil {
		t.Fatal("original response")
	}
	c := receiptCommand{ID: id, Role: "admin", Method: "POST", Path: "/v1/attendance/divisions/2/class-days", Body: b, Wire: response.Body.Bytes(), Result: envelope.Data}
	f.assertReceipt(c, "division", []int64{2})
	_, revision, e := f.s.Store.Read(context.Background(), f.org)
	if e != nil || revision != 1 {
		t.Fatal("late original commit duplicated/lost", revision, e)
	}
}
