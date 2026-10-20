package attendance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The fixtures exercise the legacy zap workflow using the configured types of
// one division. They never call a live peer or assume legacy hard-coded IDs.
func legacyZapFixture(t *testing.T, org int64) (Server, *State, Identity, Type, Type, []Day) {
	t.Helper()
	s := Server{Peers: &testPeers{Org: org}}
	st := NewState()
	a := fixtureIdentity(org, "admin")
	presentValue, err := typeSave(st, 2, 0, Type{
		Code: "legacy_present", Name: "Present before zap", Color: "#00aa00",
		Active: true, Presence: true, ScanDefault: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	excusedValue, err := typeSave(st, 2, 0, Type{
		Code: "legacy_excused", Name: "Configured excuse", Color: "#aaaa00",
		Active: true, Absence: true, Excused: true, ZapDefault: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	days := []Day{}
	for _, date := range []string{"2026-10-08", "2026-10-09"} {
		value, err := daySave(st, 2, 0, Day{Date: date})
		if err != nil {
			t.Fatal(err)
		}
		days = append(days, value.(Day))
	}
	return s, st, a, presentValue.(Type), excusedValue.(Type), days
}

func legacyZapCreate(t *testing.T, s Server, st *State, a Identity, dates ...string) Zap {
	t.Helper()
	value, err := s.zapCreate(context.Background(), st, a, 2, ZapInput{
		AccountID: 7, Text: "Synthetic legacy request", Dates: dates,
	}, "legacy-zap-create")
	if err != nil {
		t.Fatal(err)
	}
	return value.(Zap)
}

func legacyZapProcess(t *testing.T, s Server, st *State, a Identity, id int64, status, answer string) Zap {
	t.Helper()
	value, err := s.zapProcess(context.Background(), st, a, 2, id, ZapDecision{
		Status: status, Answer: answer,
	}, "legacy-zap-process")
	if err != nil {
		t.Fatal(err)
	}
	return value.(Zap)
}

func TestZapLegacyDateNormalizationAndOptionalAnswer(t *testing.T) {
	s, st, a, _, _, days := legacyZapFixture(t, 1)
	z := legacyZapCreate(t, s, st, a,
		" 2026-10-08 ", "2026-10-08", "\t2026-10-10\n", "2026-10-10")
	if len(z.Dates) != 2 || z.Dates[0].Date != days[0].Date || z.Dates[1].Date != "2026-10-10" {
		t.Fatalf("dates were not normalized and deduplicated: %+v", z.Dates)
	}
	z = legacyZapProcess(t, s, st, a, z.ID, "approved", "")
	if z.Answer != "" || z.Dates[0].Status != "linked" || z.Dates[1].Status != "no_class_day" {
		t.Fatalf("empty approval answer changed the result: %+v", z)
	}
	z = legacyZapProcess(t, s, st, a, z.ID, "rejected", "")
	if z.Answer != "" || z.Status != "rejected" || z.Dates[0].Status != "cancelled" || z.Dates[1].Status != "cancelled" {
		t.Fatalf("empty rejection answer changed the result: %+v", z)
	}
	before, _ := json.Marshal(st)
	_, err := s.zapCreate(context.Background(), st, a, 2, ZapInput{
		AccountID: 7, Text: "Invalid date probe", Dates: []string{" 2026-02-30 "},
	}, "legacy-invalid-date")
	after, _ := json.Marshal(st)
	if errorCode(err) != "invalid_zap_date" || !bytes.Equal(before, after) {
		t.Fatal("invalid normalized date was accepted or mutated the journal", err)
	}
}

func TestZapLegacyAcceptsCoreRequestLimits(t *testing.T) {
	s, st, a, _, _, _ := legacyZapFixture(t, 1)
	start, _ := time.Parse("2006-01-02", "2026-10-08")
	dates := make([]string, 60)
	for i := range dates {
		dates[i] = start.AddDate(0, 0, i).Format("2006-01-02")
	}
	value, err := s.zapCreate(context.Background(), st, a, 2, ZapInput{
		AccountID: 7, Text: strings.Repeat("Ж", 5000), Dates: dates,
	}, "legacy-core-limits")
	if err != nil {
		t.Fatal("existing Core request limits were narrowed", err)
	}
	z := value.(Zap)
	answer := strings.Repeat("О", 5000)
	z = legacyZapProcess(t, s, st, a, z.ID, "approved", answer)
	if len(z.Dates) != 60 || z.Answer != answer {
		t.Fatal("maximum accepted request was truncated")
	}
	before, _ := json.Marshal(st)
	_, err = s.zapProcess(context.Background(), st, a, 2, z.ID, ZapDecision{
		Status: "rejected", Answer: answer + "О",
	}, "legacy-answer-over-limit")
	after, _ := json.Marshal(st)
	if errorCode(err) != "invalid_zap_decision" || !bytes.Equal(before, after) {
		t.Fatal("oversized answer changed an already approved request", err)
	}
}

func TestZapLegacyManualReplacementRepeatAndDecisionReversal(t *testing.T) {
	s, st, a, present, excused, days := legacyZapFixture(t, 1)
	value, err := s.setMark(context.Background(), st, a, 2, days[0].ID, Mark{7, present.ID}, "legacy-manual-mark", false)
	if err != nil {
		t.Fatal(err)
	}
	manual := value.(Entry)
	z := legacyZapCreate(t, s, st, a, days[0].Date)
	legacyZapProcess(t, s, st, a, z.ID, "approved", "")
	linked := st.Entries[key(days[0].ID, 7)]
	if linked.ID != manual.ID || linked.Deleted || linked.ZapID == nil || *linked.ZapID != z.ID || !reflect.DeepEqual(linked.Type, excused) || !st.Days[days[0].ID].HasHistory {
		t.Fatalf("approval did not replace the manual mark with its configured type and existing ID: %+v", linked)
	}

	// Re-approving a linked date must preserve the historical type and timestamp,
	// even after the default has been renamed and archived.
	excused.Name = "Renamed and archived excuse"
	excused.Active = false
	excused.ZapDefault = false
	if _, err = typeSave(st, 2, excused.ID, excused); err != nil {
		t.Fatal(err)
	}
	legacyZapProcess(t, s, st, a, z.ID, "approved", "Updated answer")
	if got := st.Entries[key(days[0].ID, 7)]; !reflect.DeepEqual(got, linked) {
		t.Fatalf("repeated approval rewrote the linked snapshot: before=%+v after=%+v", linked, got)
	}
	if st.Zaps[z.ID].Answer != "Updated answer" {
		t.Fatal("repeated decision did not update the answer")
	}

	rejected := legacyZapProcess(t, s, st, a, z.ID, "rejected", "")
	tombstone := st.Entries[key(days[0].ID, 7)]
	if !tombstone.Deleted || tombstone.ID != manual.ID || tombstone.ZapID == nil || *tombstone.ZapID != z.ID || !st.Days[days[0].ID].HasHistory || rejected.Dates[0].Status != "cancelled" || rejected.Dates[0].DayID != nil {
		t.Fatalf("rejection lost history or left a live linked date: entry=%+v date=%+v", tombstone, rejected.Dates[0])
	}
	if _, err = daySave(st, 2, days[0].ID, Day{Date: "2026-10-11"}); errorCode(err) != "day_has_history" {
		t.Fatal("rejected zap allowed its historical day to move", err)
	}

	excused.Active = true
	excused.ZapDefault = true
	excused.Name = "Current excuse after reversal"
	value, err = typeSave(st, 2, excused.ID, excused)
	if err != nil {
		t.Fatal(err)
	}
	currentType := value.(Type)
	approved := legacyZapProcess(t, s, st, a, z.ID, "approved", "")
	newLinked := st.Entries[key(days[0].ID, 7)]
	if newLinked.Deleted || newLinked.ID != manual.ID || !reflect.DeepEqual(newLinked.Type, currentType) || approved.Dates[0].Status != "linked" || !st.Days[days[0].ID].HasHistory {
		t.Fatalf("re-approval failed to relink using the current default while keeping history: %+v", newLinked)
	}
}

func TestZapLegacyRejectionPreservesManualMarksAfterUnlink(t *testing.T) {
	s, st, a, present, _, days := legacyZapFixture(t, 1)
	z := legacyZapCreate(t, s, st, a, days[0].Date, days[1].Date)
	legacyZapProcess(t, s, st, a, z.ID, "approved", "")
	if _, err := s.zapDateAction(context.Background(), st, a, 2, z.ID, days[0].Date, true, "legacy-unlink"); err != nil {
		t.Fatal(err)
	}
	for _, mark := range []struct{ day, account int64 }{{days[0].ID, 7}, {days[1].ID, 6}} {
		if _, err := s.setMark(context.Background(), st, a, 2, mark.day, Mark{mark.account, present.ID}, "legacy-unrelated-manual", false); err != nil {
			t.Fatal(err)
		}
	}
	manualSameStudent := st.Entries[key(days[0].ID, 7)]
	manualOtherStudent := st.Entries[key(days[1].ID, 6)]
	legacyZapProcess(t, s, st, a, z.ID, "rejected", "")
	if got := st.Entries[key(days[0].ID, 7)]; !reflect.DeepEqual(got, manualSameStudent) {
		t.Fatalf("rejection erased the manual mark entered after unlink: %+v", got)
	}
	if got := st.Entries[key(days[1].ID, 6)]; !reflect.DeepEqual(got, manualOtherStudent) {
		t.Fatalf("rejection erased another student's manual mark: %+v", got)
	}
	if !st.Entries[key(days[1].ID, 7)].Deleted || !st.Days[days[0].ID].HasHistory || !st.Days[days[1].ID].HasHistory {
		t.Fatal("rejection did not tombstone only the remaining owned mark and preserve both days' history")
	}
}

func TestZapLegacyApprovalReplacesQRButScanCannotOverrideZap(t *testing.T) {
	s, st, a, present, excused, days := legacyZapFixture(t, 1)
	s.QRKey = []byte(strings.Repeat("q", 32))
	qr := s.signQR(QRClaims{1, 2, days[0].ID, 7, time.Now().Add(time.Minute).Unix()})
	input, _ := json.Marshal(map[string]string{"qr": qr})
	request := httptest.NewRequest(http.MethodPost, "/v1/attendance/divisions/2/class-days/scan", nil)
	scanner := fixtureIdentity(1, "scanner")
	ids := map[string]int64{"day": days[0].ID}
	value, err := s.write(request, st, scanner, 2, ids, "scan", input, "legacy-qr-mark")
	if err != nil {
		t.Fatal(err)
	}
	scanned := value.(Entry)
	if scanned.Type.ID != present.ID || scanned.ZapID != nil {
		t.Fatal("signed scan did not use its configured presence type")
	}
	z := legacyZapCreate(t, s, st, a, days[0].Date)
	legacyZapProcess(t, s, st, a, z.ID, "approved", "")
	linked := st.Entries[key(days[0].ID, 7)]
	if linked.ID != scanned.ID || linked.ZapID == nil || *linked.ZapID != z.ID || !reflect.DeepEqual(linked.Type, excused) {
		t.Fatalf("approval failed to replace QR attendance with the configured excuse: %+v", linked)
	}
	if _, err = s.write(request, st, scanner, 2, ids, "scan", input, "legacy-qr-after-zap"); errorCode(err) != "attendance_already_recorded" {
		t.Fatal("scanning overwrote a linked zap mark", err)
	}
	if got := st.Entries[key(days[0].ID, 7)]; !reflect.DeepEqual(got, linked) {
		t.Fatal("rejected scan changed the linked zap snapshot")
	}
}

func TestZapLegacyApprovalNeverReplacesAnotherZap(t *testing.T) {
	s, st, a, _, _, days := legacyZapFixture(t, 1)
	first := legacyZapCreate(t, s, st, a, days[0].Date)
	legacyZapProcess(t, s, st, a, first.ID, "approved", "")
	second := legacyZapCreate(t, s, st, a, days[0].Date)
	before, _ := json.Marshal(st)
	_, err := s.zapProcess(context.Background(), st, a, 2, second.ID, ZapDecision{Status: "approved"}, "legacy-other-zap")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 409 {
		t.Fatal("approval did not reject another zap's linked mark", err)
	}
	after, _ := json.Marshal(st)
	if !bytes.Equal(before, after) {
		t.Fatal("single-date conflicting approval changed the other zap or its journal entry")
	}
}

func TestMySQLZapLegacyMixedDateApprovalRollsBack(t *testing.T) {
	db := testDatabase(t)
	org := time.Now().UnixNano() / 1000
	t.Cleanup(func() {
		for _, table := range []string{"attendance_audit_v1", "attendance_receipts_v1", "attendance_state_v1"} {
			if _, err := db.Exec("DELETE FROM "+table+" WHERE organization_id=?", org); err != nil {
				t.Error(err)
			}
		}
	})
	s, seed, a, present, _, days := legacyZapFixture(t, org)
	s.Store = Store{DB: db}
	if _, err := s.setMark(context.Background(), seed, a, 2, days[0].ID, Mark{7, present.ID}, "legacy-seed-manual", false); err != nil {
		t.Fatal(err)
	}
	first := legacyZapCreate(t, s, seed, a, days[1].Date)
	legacyZapProcess(t, s, seed, a, first.ID, "approved", "")
	mixed := legacyZapCreate(t, s, seed, a, days[0].Date, days[1].Date)
	result, err := s.Store.Mutate(context.Background(), a, 0, "legacy-zap-atomic-seed", "synthetic legacy fixture", []byte(`{}`), nil, func(st *State) (any, error) {
		*st = *clone(seed)
		return mixed, nil
	})
	if err != nil || result.Revision != 1 {
		t.Fatal("fixture seed", result, err)
	}
	before, revision, err := s.Store.Read(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}
	beforeJSON, _ := json.Marshal(before)
	_, err = s.Store.Mutate(context.Background(), a, revision, "legacy-zap-atomic-conflict", "POST synthetic zap approval", []byte(`{"status":"approved"}`), nil, func(st *State) (any, error) {
		return s.zapProcess(context.Background(), st, a, 2, mixed.ID, ZapDecision{Status: "approved"}, "legacy-zap-atomic-conflict")
	})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 409 {
		t.Fatal("mixed-date approval did not reject the second date's other zap", err)
	}
	after, afterRevision, err := s.Store.Read(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}
	afterJSON, _ := json.Marshal(after)
	if afterRevision != revision || !bytes.Equal(beforeJSON, afterJSON) {
		t.Fatal("failed approval committed its first manual replacement, decision, or revision")
	}
	var audits, receipts int
	if err = db.QueryRow("SELECT COUNT(*) FROM attendance_audit_v1 WHERE organization_id=?", org).Scan(&audits); err != nil || audits != 1 {
		t.Fatal("failed approval wrote an audit snapshot", audits, err)
	}
	if err = db.QueryRow("SELECT COUNT(*) FROM attendance_receipts_v1 WHERE organization_id=?", org).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatal("failed approval wrote an idempotency receipt", receipts, err)
	}
}
