package attendance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMySQLProjectedCellPreservesWholeHistoryAndReplay(t *testing.T) {
	f := newReceiptFixture(t)
	typ := receiptValue[Type](t, f.write("admin", "POST", "/v1/attendance/divisions/2/attendance-types", Type{Code: "present", Name: "Present", Mark: "P", Color: "#00aa00", Active: true, Presence: true}).Result)
	day := receiptValue[Day](t, f.write("admin", "POST", "/v1/attendance/divisions/2/class-days", Day{Date: "2026-10-20"}).Result)
	// A different division's large history must survive every cell command.
	st, _, err := f.s.Store.Read(context.Background(), f.org)
	if err != nil {
		t.Fatal(err)
	}
	st.Lessons[90000] = Lesson{ID: 90000, DivisionID: 1, Name: strings.Repeat("Unrelated", 1000)}
	st.Entries["900:600"] = Entry{ID: 90001, DivisionID: 1, DayID: 900, AccountID: 600, Type: Type{ID: 90002, Name: "Historic", Version: 5, Color: "#abcdef"}}
	raw, _ := json.Marshal(st)
	if _, err = f.db.Exec("UPDATE attendance_state_v1 SET document=? WHERE organization_id=?", raw, f.org); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/v1/attendance/divisions/2/class-days/%d/attendance", day.ID)
	created := f.write("admin", "POST", path, Mark{7, typ.ID})
	entry := receiptValue[Entry](t, created.Result)
	if entry.ID != st.NextID || entry.Type.Version != typ.Version {
		t.Fatal("allocation/snapshot", entry)
	}
	check := func() {
		current, revision, e := f.s.Store.Read(context.Background(), f.org)
		if e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(current.Lessons, st.Lessons) || current.Entries["900:600"].Type.Version != 5 || len(current.Types) != len(st.Types) {
			t.Fatal("unrelated state overwritten")
		}
		var snapshot []byte
		if e = f.db.QueryRow("SELECT snapshot FROM attendance_audit_v1 WHERE organization_id=? AND revision=?", f.org, revision).Scan(&snapshot); e != nil {
			t.Fatal(e)
		}
		var audit State
		if json.Unmarshal(snapshot, &audit) != nil || !reflect.DeepEqual(current, &audit) {
			t.Fatal("full immutable audit diverges")
		}
	}
	check()
	f.call("admin", "POST", path, created.Body, created.ID, 200, "")
	if f.p.Guards.Load() != 1 {
		t.Fatal("replay called guard again")
	}
	// An exact old receipt survives later overwrite/deletion without mutation.
	f.write("admin", "POST", path, Mark{7, typ.ID})
	check()
	f.write("admin", "DELETE", path+"/7", struct{}{})
	check()
	f.call("admin", "POST", path, created.Body, created.ID, 200, "")
	state, _, _ := f.s.Store.Read(context.Background(), f.org)
	if !state.Entries[key(day.ID, 7)].Deleted {
		t.Fatal("replay resurrected mark")
	}
	beforeRaw, _ := json.Marshal(state)
	badBody, _ := json.Marshal(map[string]any{"expected_revision": f.revision, "data": Mark{7, typ.ID}})
	f.call("admin", "POST", "/v1/attendance/divisions/1/class-days/"+itoa(day.ID)+"/attendance", badBody, f.uuid(), 404, "not_found")
	state, _, _ = f.s.Store.Read(context.Background(), f.org)
	afterRaw, _ := json.Marshal(state)
	if string(beforeRaw) != string(afterRaw) {
		t.Fatal("foreign scope changed state")
	}
}

type workspacePeers struct {
	*testPeers
	refs int
}

func (p *workspacePeers) Refs(ctx context.Context, o, d int64, ids []int64, g *int64) (References, error) {
	p.refs++
	if len(ids) == 0 {
		ids = []int64{7, 6, 8}
	}
	return p.testPeers.Refs(ctx, o, d, ids, g)
}
func TestWorkspaceReusesOneStateAndKeepsRoleBoundaries(t *testing.T) {
	s, st, _, _, _, _ := legacyZapFixture(t, 1)
	p := &workspacePeers{testPeers: &testPeers{Org: 1}}
	s.Peers = p
	request := httptest.NewRequest("GET", "/v1/attendance/divisions/2/journal-workspace?date_from=2026-10-01&date_to=2026-10-31", nil)
	for _, role := range []string{"admin", "student", "proctor", "scanner"} {
		p.refs = 0
		value, err := s.read(request, st, fixtureIdentity(1, role), 2, map[string]int64{}, "journal-workspace")
		if role == "scanner" {
			if errorCode(err) != "forbidden" {
				t.Fatal(role, err)
			}
			continue
		}
		if err != nil {
			t.Fatal(role, err)
		}
		v := value.(map[string]any)
		students := v["students"].([]Member)
		if role == "student" && (len(students) != 1 || students[0].AccountID != 7) {
			t.Fatal("student roster leaked")
		}
		journal := v["journal"].(map[string]any)
		for _, e := range journal["entries"].([]Entry) {
			if e.DivisionID != 2 || role == "student" && e.AccountID != 7 || role == "proctor" && !same(e.GroupID, ptr(11)) {
				t.Fatal("journal leaked", role)
			}
		}
		if role == "admin" && p.refs != 1 {
			t.Fatal("roster fetched more than once", p.refs)
		}
	}
}
func TestMySQLCellProjectionPayloadAndLocalTiming(t *testing.T) {
	f := newReceiptFixture(t)
	s, st, _, present, _, days := legacyZapFixture(t, f.org)
	s.Store = f.s.Store
	s.Peers = f.p
	// The same synthetic aggregate is used for full and projected measurements.
	for n := int64(100); n < 6100; n++ {
		st.Entries[fmt.Sprintf("900:%d", n)] = Entry{ID: n, DivisionID: 1, DayID: 900, AccountID: n, FullName: "Synthetic", Type: present}
	}
	st.NextID = 10000
	raw, _ := json.Marshal(st)
	if _, e := f.db.Exec("INSERT INTO attendance_state_v1(organization_id,document) VALUES(?,?)", f.org, raw); e != nil {
		t.Fatal(e)
	}
	p := &cellProjection{day: days[0].ID, account: 7, typ: present.ID}
	tx, e := f.db.BeginTx(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	projected, _, e := p.read(context.Background(), tx, f.org)
	tx.Rollback()
	if e != nil {
		t.Fatal(e)
	}
	if len(projected)*100 > len(raw) {
		t.Fatal("projection unexpectedly large", len(projected), len(raw))
	}
	var timings [2]time.Duration
	for i := 0; i < 2; i++ {
		body, _ := json.Marshal(map[string]any{"expected_revision": i, "data": Mark{7, present.ID}})
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		start := time.Now()
		fn := func(state *State) (any, error) {
			return s.setMark(req.Context(), state, fixtureIdentity(f.org, "admin"), 2, days[0].ID, Mark{7, present.ID}, "payload-command", false)
		}
		if i == 0 {
			_, e = s.Store.Mutate(context.Background(), fixtureIdentity(f.org, "admin"), int64(i), "baseline-full", "POST /cell", body, nil, fn)
		} else {
			_, e = s.Store.mutate(context.Background(), fixtureIdentity(f.org, "admin"), int64(i), "candidate-cell", "POST /cell", body, nil, fn, p)
		}
		timings[i] = time.Since(start)
		if e != nil {
			t.Fatal(e)
		}
	}
	t.Logf("Synthetic 6000 entries: full input=%d B, projected input=%d B; one local full write=%s, projected write=%s (not production timings)", len(raw), len(projected), timings[0], timings[1])
}

func TestMySQLProjectedCellConcurrentCAS(t *testing.T) {
	f := newReceiptFixture(t)
	typ := receiptValue[Type](t, f.write("admin", "POST", "/v1/attendance/divisions/2/attendance-types", Type{Code: "present", Name: "Present", Color: "#00aa00", Active: true, Presence: true}).Result)
	day := receiptValue[Day](t, f.write("admin", "POST", "/v1/attendance/divisions/2/class-days", Day{Date: "2026-10-20"}).Result)
	body, _ := json.Marshal(map[string]any{"expected_revision": f.revision, "data": Mark{7, typ.ID}})
	path := fmt.Sprintf("/v1/attendance/divisions/2/class-days/%d/attendance", day.ID)
	results := make(chan int, 2)
	for _, id := range []string{f.uuid(), f.uuid()} {
		go func(id string) {
			r := httptest.NewRequest("POST", path, strings.NewReader(string(body)))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer admin")
			r.Header.Set("Idempotency-Key", id)
			w := httptest.NewRecorder()
			f.h.ServeHTTP(w, r)
			results <- w.Code
		}(id)
	}
	codes := map[int]int{}
	codes[<-results]++
	codes[<-results]++
	if codes[200] != 1 || codes[409] != 1 {
		t.Fatal(codes)
	}
	st, rev, e := f.s.Store.Read(context.Background(), f.org)
	if e != nil || rev != f.revision+1 || len(st.Entries) != 1 || f.p.Guards.Load() != 1 {
		t.Fatal("non-atomic cell", e, rev, len(st.Entries), f.p.Guards.Load())
	}
	var audits, receipts int
	f.db.QueryRow("SELECT COUNT(*) FROM attendance_audit_v1 WHERE organization_id=? AND revision=?", f.org, rev).Scan(&audits)
	f.db.QueryRow("SELECT COUNT(*) FROM attendance_receipts_v1 WHERE organization_id=?", f.org).Scan(&receipts)
	if audits != 1 || receipts != 3 {
		t.Fatal("duplicate history/receipt", audits, receipts)
	}
}
