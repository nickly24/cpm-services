package attendance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

type combinedReceiptPeers struct {
	*receiptPeers
	http     *HTTPPeers
	oldCalls atomic.Int64
}

func (p *combinedReceiptPeers) CellTargets(ctx context.Context, a Identity, d int64, ids []int64, op string) (map[int64]Member, error) {
	return p.http.CellTargets(ctx, a, d, ids, op)
}
func (p *combinedReceiptPeers) Division(context.Context, int64, int64) (string, error) {
	p.oldCalls.Add(1)
	return "", unavailable
}
func (p *combinedReceiptPeers) Guard(context.Context, int64, int64, int64, string) error {
	p.oldCalls.Add(1)
	return unavailable
}
func (p *combinedReceiptPeers) Refs(context.Context, int64, int64, []int64, *int64) (References, error) {
	p.oldCalls.Add(1)
	return References{}, unavailable
}
func TestCellBatchStrictBoundsAndExplicitErase(t *testing.T) {
	for _, raw := range []string{`{}`, `{"cells":[]}`, `{"cells":[{"class_day_id":1,"account_id":7}]}`, `{"cells":[{"class_day_id":1,"account_id":7,"attendance_type_id":null}]}`, `{"cells":[{"class_day_id":0,"account_id":7,"attendance_type_id":1}]}`, `{"cells":[{"class_day_id":1,"account_id":7,"attendance_type_id":-1}]}`, `{"cells":[{"class_day_id":1,"account_id":7,"attendance_type_id":1},{"class_day_id":1,"account_id":7,"attendance_type_id":0}]}`, `{"cells":[{"class_day_id":1,"account_id":7,"attendance_type_id":1,"organization_id":99}]}`} {
		if _, err := cellEdits("cells", nil, json.RawMessage(raw)); err == nil {
			t.Fatal("invalid batch accepted", raw)
		}
	}
	cells := []CellEdit{}
	for n := int64(1); n <= 101; n++ {
		cells = append(cells, CellEdit{1, n, ptr(1)})
	}
	raw, _ := json.Marshal(CellBatch{cells})
	if _, err := cellEdits("cells", nil, raw); err == nil {
		t.Fatal("oversized batch accepted")
	}
}
func TestMySQLCellBatchOneCoreAtomicHistoryReplayAndErase(t *testing.T) {
	f := newReceiptFixture(t)
	typ := receiptValue[Type](t, f.write("admin", "POST", "/v1/attendance/divisions/2/attendance-types", Type{Code: "present", Name: "Present", Color: "#00aa00", Mark: "P", Active: true, Presence: true}).Result)
	st, _, err := f.s.Store.Read(context.Background(), f.org)
	if err != nil {
		t.Fatal(err)
	}
	cells := []CellEdit{}
	for n := int64(0); n < 50; n++ {
		day := 100 + n
		st.Days[day] = Day{ID: day, DivisionID: 2, Date: fmt.Sprintf("2026-%02d-%02d", 1+n/28, 1+n%28), LessonIDs: []int64{}}
		for _, account := range []int64{6, 7} {
			cells = append(cells, CellEdit{day, account, &typ.ID})
		}
	}
	st.Lessons[90000] = Lesson{ID: 90000, DivisionID: 1, Name: strings.Repeat("Unrelated", 1000)}
	st.NextID = 100000
	raw, _ := json.Marshal(st)
	if _, err = f.db.Exec("UPDATE attendance_state_v1 SET document=? WHERE organization_id=?", raw, f.org); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	var outage atomic.Bool
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/internal/core/attendance/cell-targets" || r.Header.Get("X-Service-Key") != strings.Repeat("k", 32) {
			t.Error("old RPC/untrusted call", r.URL.Path)
		}
		if outage.Load() {
			reply(w, 503, map[string]any{"status": false, "error": "dependency_unavailable"})
			return
		}
		var in struct {
			OrganizationID int64   `json:"organization_id"`
			DivisionID     int64   `json:"division_id"`
			AccountIDs     []int64 `json:"account_ids"`
			OperationKey   string  `json:"operation_key"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.OrganizationID != f.org || in.DivisionID != 2 || !reflect.DeepEqual(in.AccountIDs, []int64{6, 7}) || len(in.OperationKey) < 8 {
			t.Error("scope/accounts not deduplicated", in)
		}
		members := []any{}
		for _, account := range in.AccountIDs {
			members = append(members, map[string]any{"account_id": account, "role": "student", "full_name": fmt.Sprint("Student ", account), "group_id": 11, "permanent": true, "reservation_id": strings.Repeat("a", 64)})
		}
		reply(w, 200, map[string]any{"status": true, "data": map[string]any{"organization_id": in.OrganizationID, "division_id": in.DivisionID, "members": members}})
	}))
	defer core.Close()
	peer := &combinedReceiptPeers{receiptPeers: f.p, http: NewPeers("", core.URL, strings.Repeat("k", 32))}
	f.s.Peers = peer
	f.h = f.s.Handler()
	path := "/v1/attendance/divisions/2/attendance-cells/batch"
	saved := f.write("admin", "POST", path, CellBatch{cells})
	batch := receiptValue[CellBatchResult](t, saved.Result)
	if len(batch.Cells) != 100 || calls.Load() != 1 || peer.oldCalls.Load() != 0 {
		t.Fatal("batch multiplied RPCs", len(batch.Cells), calls.Load(), peer.oldCalls.Load())
	}
	check := func() {
		current, rev, e := f.s.Store.Read(context.Background(), f.org)
		if e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(current.Lessons, st.Lessons) || len(current.Days) != 50 || len(current.Entries) != 100 {
			t.Fatal("unrelated state changed")
		}
		var audit []byte
		if e = f.db.QueryRow("SELECT snapshot FROM attendance_audit_v1 WHERE organization_id=? AND revision=?", f.org, rev).Scan(&audit); e != nil {
			t.Fatal(e)
		}
		var snapshot State
		json.Unmarshal(audit, &snapshot)
		if !reflect.DeepEqual(current, &snapshot) {
			t.Fatal("audit truncated")
		}
	}
	check()
	ids := map[int64]bool{}
	for _, entry := range batch.Cells {
		if ids[entry.ID] || entry.Type.Version != typ.Version || entry.Deleted {
			t.Fatal("canonical entry allocation/type", entry)
		}
		ids[entry.ID] = true
	}
	// Local validation fails before Core and rolls back the entire command.
	bad := append([]CellEdit{}, cells...)
	bad[99].TypeID = ptr(999)
	body, _ := json.Marshal(map[string]any{"expected_revision": f.revision, "data": CellBatch{bad}})
	f.call("admin", "POST", path, body, f.uuid(), 400, "invalid_attendance_type")
	if calls.Load() != 1 {
		t.Fatal("invalid batch reached Core")
	}
	outage.Store(true)
	body, _ = json.Marshal(map[string]any{"expected_revision": f.revision, "data": CellBatch{cells}})
	f.call("admin", "POST", path, body, f.uuid(), 503, "dependency_unavailable")
	check()
	outage.Store(false)
	for i := range cells {
		cells[i].TypeID = ptr(0)
	}
	erased := f.write("admin", "POST", path, CellBatch{cells})
	deleted := receiptValue[CellBatchResult](t, erased.Result)
	for i, e := range deleted.Cells {
		if !e.Deleted || e.ID != batch.Cells[i].ID {
			t.Fatal("erase lost tombstone", e)
		}
	}
	check()
	before := calls.Load()
	f.call("admin", "POST", path, saved.Body, saved.ID, 200, "")
	if calls.Load() != before+1 {
		t.Fatal("receipt skipped live Core checks")
	}
	current, _, _ := f.s.Store.Read(context.Background(), f.org)
	for _, e := range current.Entries {
		if !e.Deleted {
			t.Fatal("replay resurrected entry")
		}
	}
	// Protected batch receipt validates every canonical entry against its original audit.
	peer.oldCalls.Store(0)
	f.s.Peers = f.p
	f.h = f.s.Handler()
	f.call("admin", "GET", "/v1/attendance/command-receipts/"+saved.ID, nil, "", 200, "")
	var audits, receipts int
	f.db.QueryRow("SELECT COUNT(*) FROM attendance_audit_v1 WHERE organization_id=?", f.org).Scan(&audits)
	f.db.QueryRow("SELECT COUNT(*) FROM attendance_receipts_v1 WHERE organization_id=?", f.org).Scan(&receipts)
	if audits != 3 || receipts != 3 {
		t.Fatal("not one audit/receipt per batch", audits, receipts)
	}
}

func TestMySQLSingleCellsReuseCombinedCoreAndFenceHistoricalProctorScope(t *testing.T) {
	f := newReceiptFixture(t)
	typ := receiptValue[Type](t, f.write("admin", "POST", "/v1/attendance/divisions/2/attendance-types", Type{Code: "present", Name: "Present", Color: "#00aa00", Active: true, Presence: true}).Result)
	day := receiptValue[Day](t, f.write("admin", "POST", "/v1/attendance/divisions/2/class-days", Day{Date: "2026-10-20"}).Result)
	var calls atomic.Int64
	group := int64(11)
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var in struct {
			OrganizationID int64   `json:"organization_id"`
			DivisionID     int64   `json:"division_id"`
			IDs            []int64 `json:"account_ids"`
			ProctorID      *int64  `json:"proctor_account_id"`
			GroupID        *int64  `json:"group_id"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if r.URL.Path != "/internal/core/attendance/cell-targets" || in.OrganizationID != f.org || in.DivisionID != 2 || !reflect.DeepEqual(in.IDs, []int64{7}) || in.ProctorID == nil || *in.ProctorID != 8 || in.GroupID == nil || *in.GroupID != group {
			t.Error("lost proctor proof", in)
		}
		reply(w, 200, map[string]any{"status": true, "data": map[string]any{"organization_id": f.org, "division_id": 2, "members": []any{map[string]any{"account_id": 7, "role": "student", "full_name": "Student", "group_id": group, "permanent": true, "reservation_id": strings.Repeat("a", 64)}}}})
	}))
	defer core.Close()
	peer := &combinedReceiptPeers{receiptPeers: f.p, http: NewPeers("", core.URL, strings.Repeat("k", 32))}
	f.s.Peers = peer
	f.h = f.s.Handler()
	path := "/v1/attendance/divisions/2/class-days/" + itoa(day.ID) + "/attendance"
	saved := f.write("proctor", "POST", path, Mark{7, typ.ID})
	f.write("proctor", "DELETE", path+"/7", struct{}{})
	if calls.Load() != 2 || peer.oldCalls.Load() != 0 {
		t.Fatal("single edit multiplied Core calls", calls.Load(), peer.oldCalls.Load())
	}
	f.call("proctor", "POST", path, saved.Body, saved.ID, 200, "")
	current, _, _ := f.s.Store.Read(context.Background(), f.org)
	if !current.Entries[key(day.ID, 7)].Deleted {
		t.Fatal("replay resurrected mark")
	}
	group = 12
	f.p.ProctorGroup = 12
	f.call("proctor", "POST", path, saved.Body, saved.ID, 403, "forbidden")
	current, _, _ = f.s.Store.Read(context.Background(), f.org)
	if !current.Entries[key(day.ID, 7)].Deleted {
		t.Fatal("historical group fence lost")
	}
}
