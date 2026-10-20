package attendance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestProfileScanUsesExternalIDAndNeverOverwrites(t *testing.T) {
	s, st, _, present, _, days := legacyZapFixture(t, 1)
	scanner := fixtureIdentity(1, "scanner")
	scan := func(qr string, day int64) (any, error) {
		b, _ := json.Marshal(map[string]string{"qr": qr})
		return s.write(httptest.NewRequest("POST", "/", nil), st, scanner, 2, map[string]int64{"day": day}, "scan", b, "profile-scan")
	}
	value, err := scan("700", days[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	entry := value.(Entry)
	if entry.AccountID != 7 || entry.AccountID == 700 || entry.Type.ID != present.ID {
		t.Fatal("profile/account mismatch", entry)
	}
	before, _ := json.Marshal(st)
	if _, err = scan("700", days[0].ID); errorCode(err) != "attendance_already_recorded" {
		t.Fatal("duplicate", err)
	}
	after, _ := json.Marshal(st)
	if string(before) != string(after) {
		t.Fatal("duplicate mutated state")
	}
	if _, err = scan("7", days[1].ID); errorCode(err) != "student_not_in_division" {
		t.Fatal("account ID was treated as profile ID", err)
	}
	for _, badQR := range []string{"0", "-1", "+700", "0700", "700.0", "https://example.test/700", "999999999999999999999999"} {
		if _, err = scan(badQR, days[1].ID); errorCode(err) != "invalid_student_qr" {
			t.Fatal(badQR, err)
		}
	}
	foreign := days[1]
	foreign.DivisionID = 3
	st.Days[foreign.ID] = foreign
	if _, err = scan("700", foreign.ID); errorCode(err) != "not_found" {
		t.Fatal("foreign day", err)
	}
}
func TestProfileScanHonorsGroupAndCoreFailure(t *testing.T) {
	s, st, _, _, _, days := legacyZapFixture(t, 1)
	b := json.RawMessage(`{"qr":"600"}`)
	before, _ := json.Marshal(st)
	_, err := s.write(httptest.NewRequest("POST", "/", nil), st, fixtureIdentity(1, "proctor"), 2, map[string]int64{"day": days[0].ID}, "scan", b, "scope")
	if errorCode(err) != "forbidden" {
		t.Fatal("foreign group", err)
	}
	s.Peers.(*testPeers).CoreErr = unavailable
	_, err = s.write(httptest.NewRequest("POST", "/", nil), st, fixtureIdentity(1, "scanner"), 2, map[string]int64{"day": days[0].ID}, "scan", json.RawMessage(`{"qr":"700"}`), "scope")
	if errorCode(err) != "dependency_unavailable" {
		t.Fatal("Core failure", err)
	}
	after, _ := json.Marshal(st)
	if string(before) != string(after) {
		t.Fatal("failed scan mutated state")
	}
}
func TestProfileLookupChecksCoreScopeAndRole(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       string
	}{
		{"valid", `{"status":true,"data":{"organization_id":1,"division_id":2,"members":[{"account_id":7,"profile_id":700,"role":"student"}]}}`, 200, ""},
		{"foreign division", `{"status":true,"data":{"organization_id":1,"division_id":3,"members":[{"account_id":7,"profile_id":700,"role":"student"}]}}`, 200, "dependency_unavailable"},
		{"wrong identity", `{"status":true,"data":{"organization_id":1,"division_id":2,"members":[{"account_id":7,"profile_id":7,"role":"student"}]}}`, 200, "dependency_unavailable"},
		{"not student", `{"status":true,"data":{"organization_id":1,"division_id":2,"members":[{"account_id":7,"profile_id":700,"role":"proctor"}]}}`, 200, "dependency_unavailable"},
		{"missing membership", `{"status":false,"error":"not_found"}`, 404, "student_not_in_division"},
		{"old core", `404 page not found`, 404, "dependency_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				if !reflect.DeepEqual(body["profile_ids"], []any{float64(700)}) || body["division_id"] != float64(2) || body["organization_id"] != float64(1) || body["account_ids"] != nil || r.Header.Get("X-Service-Key") == "" {
					t.Error("wrong scope", body)
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer core.Close()
			p := NewPeers(core.URL, core.URL, strings.Repeat("k", 32))
			m, err := p.StudentProfile(context.Background(), 1, 2, 700)
			if errorCode(err) != tc.want {
				t.Fatal(err)
			}
			if err == nil && m.AccountID != 7 {
				t.Fatal(m)
			}
		})
	}
}

func TestMySQLProfileScanDuplicateReplayAndConcurrency(t *testing.T) {
	f := newReceiptFixture(t)
	typCmd := f.write("admin", "POST", "/v1/attendance/divisions/2/attendance-types", Type{Code: "profile_presence", Name: "Presence", Color: "#00aa00", Active: true, Presence: true, ScanDefault: true})
	typ := receiptValue[Type](t, typCmd.Result)
	day := receiptValue[Day](t, f.write("admin", "POST", "/v1/attendance/divisions/2/class-days", Day{Date: "2026-10-08"}).Result)
	path := "/v1/attendance/divisions/2/class-days/" + itoa(day.ID) + "/scan"
	saved := f.write("scanner", "POST", path, map[string]string{"qr": "700"})
	entry := receiptValue[Entry](t, saved.Result)
	if entry.AccountID != 7 || entry.Type.ID != typ.ID {
		t.Fatal(entry)
	}
	guards := f.p.Guards.Load()
	before, revision, err := f.s.Store.Read(context.Background(), f.org)
	if err != nil {
		t.Fatal(err)
	}
	beforeJSON, _ := json.Marshal(before)
	duplicate, _ := json.Marshal(map[string]any{"expected_revision": f.revision, "data": map[string]string{"qr": "700"}})
	f.call("scanner", "POST", path, duplicate, f.uuid(), 409, "attendance_already_recorded")
	f.call("scanner", "POST", path, saved.Body, saved.ID, 200, "")
	accountQR, _ := json.Marshal(map[string]any{"expected_revision": f.revision, "data": map[string]string{"qr": "7"}})
	f.call("scanner", "POST", path, accountQR, f.uuid(), 404, "student_not_in_division")
	after, afterRevision, _ := f.s.Store.Read(context.Background(), f.org)
	afterJSON, _ := json.Marshal(after)
	if string(beforeJSON) != string(afterJSON) || revision != afterRevision || guards != f.p.Guards.Load() {
		t.Fatal("duplicate/replay changed state or repeated history guard")
	}
	body, _ := json.Marshal(map[string]any{"expected_revision": f.revision, "data": map[string]string{"qr": "600"}})
	ids := []string{f.uuid(), f.uuid()}
	statuses := make(chan int, 2)
	for _, id := range ids {
		go func(id string) {
			r := httptest.NewRequest("POST", path, strings.NewReader(string(body)))
			r.Header.Set("Authorization", "Bearer scanner")
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Idempotency-Key", id)
			w := httptest.NewRecorder()
			f.h.ServeHTTP(w, r)
			statuses <- w.Code
		}(id)
	}
	a, b := <-statuses, <-statuses
	if !(a == 200 && b == 409 || a == 409 && b == 200) {
		t.Fatal(a, b)
	}
	final, finalRevision, _ := f.s.Store.Read(context.Background(), f.org)
	if len(final.Entries) != 2 || finalRevision != revision+1 || f.p.Guards.Load() != guards+1 {
		t.Fatal("concurrent duplicate inserted twice", finalRevision)
	}
	var audits, receipts int
	f.db.QueryRow("SELECT count(*) FROM attendance_audit_v1 WHERE organization_id=?", f.org).Scan(&audits)
	f.db.QueryRow("SELECT count(*) FROM attendance_receipts_v1 WHERE organization_id=?", f.org).Scan(&receipts)
	if int64(audits) != finalRevision || int64(receipts) != finalRevision {
		t.Fatal("rejected scan created audit/receipt", audits, receipts)
	}
}
