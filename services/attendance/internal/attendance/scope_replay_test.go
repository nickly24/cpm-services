package attendance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type scopeReplayPeers struct {
	*testPeers
	Archived     map[int64]bool
	TargetGroup  int64
	ProctorGroup int64
	Assigned     bool
}

func (p *scopeReplayPeers) Division(ctx context.Context, o, d int64) (string, error) {
	if p.Archived[d] {
		return "", fmt.Errorf("archived division: %w", missing)
	}
	return p.testPeers.Division(ctx, o, d)
}

func (p *scopeReplayPeers) Identify(ctx context.Context, token string) (Identity, error) {
	a, e := p.testPeers.Identify(ctx, token)
	if e == nil && a.User.Role == "proctor" {
		if p.ProctorGroup == 0 {
			a.Organizations[0].Divisions[1].GroupID = nil
		} else {
			a.Organizations[0].Divisions[1].GroupID = ptr(p.ProctorGroup)
		}
	}
	return a, e
}

func (p *scopeReplayPeers) Refs(ctx context.Context, o, d int64, ids []int64, group *int64) (References, error) {
	r, e := p.testPeers.Refs(ctx, o, d, ids, group)
	if e != nil {
		return r, e
	}
	for i := range r.Members {
		if r.Members[i].AccountID == 7 {
			r.Members[i].GroupID = ptr(p.TargetGroup)
		}
	}
	for i := range r.Groups {
		r.Groups[i].ProctorID = nil
		if p.Assigned && r.Groups[i].ID == p.ProctorGroup {
			r.Groups[i].ProctorID = ptr(8)
		}
	}
	return r, nil
}

func TestArchivedCalendarDoesNotBreakLiveSchedules(t *testing.T) {
	p := &scopeReplayPeers{testPeers: &testPeers{Org: 1}, Archived: map[int64]bool{3: true}}
	s := Server{Peers: p}
	st := NewState()
	for d := int64(1); d <= 3; d++ {
		st.Calendars[d] = Calendar{d, "organization", "Europe/Moscow"}
		l := sampleLesson(d, "2026-10-08", "09:00", "10:00")
		l.ID = st.allocate()
		st.Lessons[l.ID] = l
	}
	a := fixtureIdentity(1, "student")
	personal, e := s.calendarRead(context.Background(), st, a, "", "2026-10-08", "2026-10-08", nil, true)
	if e != nil {
		t.Fatal("archived published nonmember calendar broke personal schedule", e)
	}
	view := personal.(map[string]any)
	if len(view["calendars"].([]map[string]any)) != 2 || len(view["events"].([]Event)) != 2 {
		t.Fatal("archived calendar remained in personal/calendar index", view)
	}
	for _, event := range view["events"].([]Event) {
		if event.DivisionID == 3 {
			t.Fatal("archived calendar leaked its event")
		}
	}
	selected, e := s.calendarRead(context.Background(), st, a, "", "2026-10-08", "2026-10-08", []int64{2}, false)
	if e != nil || len(selected.(map[string]any)["events"].([]Event)) != 1 {
		t.Fatal("archived unrelated calendar broke selected live schedule", e)
	}
	if _, e = s.calendarRead(context.Background(), st, a, "", "2026-10-08", "2026-10-08", []int64{3}, false); e != missing {
		t.Fatal("explicit archived calendar must remain inaccessible", e)
	}
	p.CoreErr = unavailable
	if _, e = s.calendarRead(context.Background(), st, a, "", "2026-10-08", "2026-10-08", nil, true); e != unavailable {
		t.Fatal("core outage silently hid live calendars", e)
	}
}

func TestProctorReplayRequiresCurrentAndHistoricalGroup(t *testing.T) {
	p := &scopeReplayPeers{testPeers: &testPeers{Org: 1}, TargetGroup: 11, ProctorGroup: 11, Assigned: true}
	s := Server{Peers: p}
	entry := Entry{ID: 4, DivisionID: 2, DayID: 3, AccountID: 7, GroupID: ptr(11), FullName: "Synthetic student"}
	original := NewState()
	original.Entries[key(3, 7)] = entry
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	ids := map[string]int64{"day": 3, "account": 7}
	for _, kind := range []string{"mark", "scan", "unmark"} {
		t.Run(kind, func(t *testing.T) {
			p.TargetGroup, p.ProctorGroup, p.Assigned = 11, 11, true
			a, _ := p.Identify(context.Background(), "Bearer proctor")
			result := Result{Revision: 9, Value: entry}
			if kind == "unmark" {
				result.Value = map[string]bool{"deleted": true}
			}
			if e := s.replayAuthorization(r, a, 2, ids, kind)(original, result); e != nil {
				t.Fatal("unchanged current grant prevented exact replay", e)
			}
			p.Assigned = false
			if e := s.replayAuthorization(r, a, 2, ids, kind)(original, result); e != denied {
				t.Fatal("revoked core proctor assignment allowed replay", e)
			}
			p.ProctorGroup = 0
			a, _ = p.Identify(context.Background(), "Bearer proctor")
			if e := s.replayAuthorization(r, a, 2, ids, kind)(original, result); e != denied {
				t.Fatal("unassigned proctor allowed replay", e)
			}
			p.TargetGroup, p.ProctorGroup, p.Assigned = 12, 12, true
			a, _ = p.Identify(context.Background(), "Bearer proctor")
			if e := s.replayAuthorization(r, a, 2, ids, kind)(original, result); e != denied {
				t.Fatal("new current group inherited historical receipt", e)
			}
			p.CoreErr = unavailable
			if e := s.replayAuthorization(r, a, 2, ids, kind)(original, result); e != unavailable {
				t.Fatal("unavailable live assignment check allowed replay", e)
			}
			p.CoreErr = nil
		})
	}
}

func TestMySQLProctorReplayAfterUnassignAndGroupMove(t *testing.T) {
	db := testDatabase(t)
	o := time.Now().UnixNano()
	t.Cleanup(func() {
		for _, table := range []string{"attendance_audit_v1", "attendance_receipts_v1", "attendance_state_v1"} {
			_, _ = db.Exec("DELETE FROM "+table+" WHERE organization_id=?", o)
		}
	})
	p := &scopeReplayPeers{testPeers: &testPeers{Org: o}, TargetGroup: 11, ProctorGroup: 11, Assigned: true}
	s := Server{Store: Store{db}, Peers: p, QRKey: []byte(strings.Repeat("q", 32))}
	h := s.Handler()
	call := func(role, method, path string, body []byte, key string, want int) Result {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+role)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s got %d, want %d: %s", role, path, w.Code, want, w.Body.String())
		}
		var out struct {
			Data Result `json:"data"`
		}
		if json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatal("invalid response")
		}
		return out.Data
	}
	encode := func(revision int64, value any) []byte {
		b, _ := json.Marshal(map[string]any{"expected_revision": revision, "data": value})
		return b
	}
	parse := func(result Result, value any) {
		b, _ := json.Marshal(result.Value)
		if json.Unmarshal(b, value) != nil {
			t.Fatal("invalid value")
		}
	}
	var typ Type
	parse(call("admin", http.MethodPost, "/v1/attendance/divisions/2/attendance-types", encode(0, Type{Code: "scope_present", Name: "Scope presence", Color: "#00aa00", Active: true, Presence: true, ScanDefault: true}), "scope-type", 200), &typ)
	var day Day
	parse(call("admin", http.MethodPost, "/v1/attendance/divisions/2/class-days", encode(1, Day{Date: "2026-10-08"}), "scope-day", 200), &day)
	path := "/v1/attendance/divisions/2/class-days/" + itoa(day.ID)
	markBody := encode(2, Mark{7, typ.ID})
	marked := call("proctor", http.MethodPost, path+"/attendance", markBody, "scope-mark", 200)
	if replay := call("proctor", http.MethodPost, path+"/attendance", markBody, "scope-mark", 200); replay.Revision != marked.Revision {
		t.Fatal("unchanged grant replay changed revision")
	}
	p.Assigned = false
	call("proctor", http.MethodPost, path+"/attendance", markBody, "scope-mark", 403)
	p.ProctorGroup = 0
	call("proctor", http.MethodPost, path+"/attendance", markBody, "scope-mark", 403)
	p.ProctorGroup, p.Assigned = 11, true
	unmarkBody := encode(3, struct{}{})
	unmarked := call("proctor", http.MethodDelete, path+"/attendance/7", unmarkBody, "scope-unmark", 200)
	if replay := call("proctor", http.MethodDelete, path+"/attendance/7", unmarkBody, "scope-unmark", 200); replay.Revision != unmarked.Revision {
		t.Fatal("unchanged deletion replay changed revision")
	}
	// A later valid correction moves the live entry to group 12. Replaying the
	// earlier deletion must still check its immutable group-11 audit snapshot.
	p.TargetGroup, p.ProctorGroup = 12, 12
	call("proctor", http.MethodPost, path+"/attendance", encode(4, Mark{7, typ.ID}), "scope-moved-mark", 200)
	call("proctor", http.MethodPost, path+"/attendance", markBody, "scope-mark", 403)
	call("proctor", http.MethodDelete, path+"/attendance/7", unmarkBody, "scope-unmark", 403)
	var qr QRResult
	parse(call("student", http.MethodPost, path+"/qr", []byte("{}"), "", 200), &qr)
	call("proctor", http.MethodDelete, path+"/attendance/7", encode(5, struct{}{}), "scope-before-scan", 200)
	scanBody := encode(6, map[string]string{"qr": qr.QR})
	scanned := call("proctor", http.MethodPost, path+"/scan", scanBody, "scope-scan", 200)
	// Receipt replay does not reverify a once-valid QR after key rotation/expiry.
	s.QRKey = []byte(strings.Repeat("r", 32))
	if replay := call("proctor", http.MethodPost, path+"/scan", scanBody, "scope-scan", 200); replay.Revision != scanned.Revision {
		t.Fatal("exact scan replay reexecuted or required a new QR")
	}
	p.Assigned = false
	call("proctor", http.MethodPost, path+"/scan", scanBody, "scope-scan", 403)
	if _, revision, e := s.Store.Read(context.Background(), o); e != nil || revision != 7 {
		t.Fatal("denied receipt replays mutated state", revision, e)
	}
}
