package ratings

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixtureJob() Job {
	return Job{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", OrganizationID: 11, DivisionID: 17, Revision: 3, DateFrom: "2026-10-01", DateTo: "2026-10-31", AsOf: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
}

func fixtureSnapshot(j Job) Snapshot {
	account := Account{AccountID: 71, ProfileID: 9, FullName: "Synthetic pupil", IsActive: true}
	h := strings.Repeat("a", 64)
	return Snapshot{
		Core:      CoreSnapshot{Source: "core", OrganizationID: j.OrganizationID, DivisionID: j.DivisionID, Accounts: []Account{account}, InputHash: h},
		Homework:  HomeworkSnapshot{Source: "homework", OrganizationID: j.OrganizationID, DivisionID: j.DivisionID, Assignments: []Assignment{}, InputHash: h},
		Tests:     TestsSnapshot{Source: "tests", OrganizationID: j.OrganizationID, DivisionID: j.DivisionID, Tests: []Test{}, ExternalTests: []Test{}, ExternalSourceVersion: h, InputHash: h, ExternalDomain: &ExternalDomain{SchemaVersion: 1, Status: "ready", OrganizationID: j.OrganizationID, DivisionID: j.DivisionID}},
		Exams:     ExamsSnapshot{Source: "exams", OrganizationID: j.OrganizationID, DivisionID: j.DivisionID, Exams: []Exam{}, InputHash: h},
		ExamsMode: "required",
	}
}

func assertAPIError(t *testing.T, err error, code int, name string) {
	t.Helper()
	var api *APIError
	if !errors.As(err, &api) || api.Code != code || name != "" && api.Name != name {
		t.Fatalf("got error %v; expected HTTP %d %s", err, code, name)
	}
}

func TestCanonicalInputHashMatchesCalculateWireSemantics(t *testing.T) {
	// This golden uses calculate's RatingInputHash contract: recursively sorted
	// object keys, UseNumber, then Go JSON escaping. Owner source hashes have a
	// separate canonical encoding and must not be substituted here.
	raw := []byte(`{"z":"<ОВ>&\u2028","input_hash":"old","a":{"z":9007199254740993,"a":[1.20,0]}}`)
	canonical := []byte(`{"a":{"a":[1.20,0],"z":9007199254740993},"z":"\u003cОВ\u003e\u0026\u2028"}`)
	sum := sha256.Sum256(canonical)
	want := hex.EncodeToString(sum[:])
	got, e := canonicalHash(raw, "input_hash")
	if e != nil || got != want {
		t.Fatalf("canonical hash lost numeric lexemes or calculate escaping: got %s want %s (%v)", got, want, e)
	}
	reordered := []byte(`{"a":{"a":[1.20,0],"z":9007199254740993},"input_hash":"new","z":"<ОВ>&\u2028"}`)
	got, e = canonicalHash(reordered, "input_hash")
	if e != nil || got != want {
		t.Fatal("key order or the omitted hash changed the input fingerprint", e)
	}
	for _, invalid := range []string{`null`, `[]`, `"text"`, `{} {}`, `{"a":1,"a":2}`} {
		if _, e = canonicalHash([]byte(invalid), "input_hash"); e == nil {
			t.Fatalf("noncanonical ambiguous input was accepted: %s", invalid)
		}
	}
}

func TestStrictJSONRejectsUnknownDuplicateNullAndTrailingData(t *testing.T) {
	for _, invalid := range []string{`null`, `[]`, `{"unexpected":1}`, `{} {}`, `{"date_from":"2026-10-01","date_from":"2026-10-02"}`} {
		var in struct {
			DateFrom string `json:"date_from"`
		}
		if e := strict([]byte(invalid), &in); e == nil {
			t.Fatalf("strict object DTO accepted %s", invalid)
		}
	}
	var valid struct {
		DateFrom string `json:"date_from"`
	}
	if strict([]byte(`{"date_from":"2026-10-01"}`), &valid) != nil || valid.DateFrom != "2026-10-01" {
		t.Fatal("valid DTO failed")
	}
}

func TestRatingsPermissionsDoNotPromoteMembershipOrUnrelatedRights(t *testing.T) {
	for _, tc := range []struct {
		name       string
		actor      Actor
		view, edit bool
	}{
		{"organization admin", Actor{Role: "admin", OrganizationAdmin: true}, true, true},
		{"admin without organization authority", Actor{Role: "admin", Permissions: map[string]Permission{"ratings": {View: true, Edit: true}}}, false, false},
		{"staff read", Actor{Role: "staff_admin", Permissions: map[string]Permission{"ratings": {View: true}}}, true, false},
		{"staff edit", Actor{Role: "staff_admin", Permissions: map[string]Permission{"ratings": {View: true, Edit: true}}}, true, true},
		{"edit without view", Actor{Role: "staff_admin", Permissions: map[string]Permission{"ratings": {Edit: true}}}, false, false},
		{"unrelated tests edit", Actor{Role: "staff_admin", Permissions: map[string]Permission{"tests": {View: true, Edit: true}}}, false, false},
		{"student with forged permissions", Actor{Role: "student", OrganizationAdmin: true, Permissions: map[string]Permission{"ratings": {View: true, Edit: true}}}, false, false},
		{"proctor", Actor{Role: "proctor"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.actor.Can(false) != tc.view || tc.actor.Can(true) != tc.edit {
				t.Fatal("role/permission checks widened ratings authority")
			}
		})
	}
}

func TestPersonalInputUsesAccountIDAndPreservesMissingAndZeroInputs(t *testing.T) {
	j := fixtureJob()
	s := fixtureSnapshot(j)
	zero, five := 0.0, 5.0
	attempt := 2
	datePass := "2026-10-06T12:00:00Z"
	s.Homework.Assignments = []Assignment{
		{ID: 4, Name: "<ОВ>&", TypeName: "ОВ", Deadline: "2026-10-06", Results: []HWResult{{AccountID: 9, Status: 1, Result: &five}, {AccountID: 71, Status: 1, Result: &zero, DatePass: &datePass}}},
		{ID: 5, Name: "Missing", TypeName: "ОВ", Deadline: "2026-10-07"},
	}
	s.Tests.Tests = []Test{
		{ID: "internal-zero", Title: "Zero", DirectionName: " МАТЕМАТИКА ", Results: []TestResult{{AccountID: 71, Status: "ready", Score: &zero}}},
		{ID: "internal-missing", Title: "Missing", DirectionName: "Math", Results: []TestResult{{AccountID: 9, Status: "ready", Score: &five}}},
	}
	s.Tests.ExternalTests = []Test{{ID: "external-zero", Title: "Outside", DirectionName: "Math", Results: []TestResult{{AccountID: 71, Status: "ready", Score: &zero}}}}
	s.Exams.Exams = []Exam{
		{ID: "shared-exam", Name: "Shared", Date: "2026-10-02", Type: "classic", Results: []ExamResult{{AccountID: 9, Score: &zero}, {AccountID: 71, Score: &five, AttemptNo: &attempt, HasAppeal: true}}},
		{ID: "missing-exam", Name: "Missing", Date: "2026-10-03", Type: "classic"},
	}
	in, e := personal(j, s, s.Core.Accounts[0])
	if e != nil || in.AccountID != 71 || in.OrganizationID != 11 || in.DivisionID != 17 || len(in.InputHash) != 64 {
		t.Fatal("account identity or scope was lost", in, e)
	}
	if len(in.Homework) != 2 || in.Homework[0].Result == nil || *in.Homework[0].Result != 0 || in.Homework[0].Status != 1 || in.Homework[0].DatePass == nil || in.Homework[1].Result != nil || in.Homework[1].Status != 0 {
		t.Fatal("homework ready zero or missing result was changed", in.Homework)
	}
	if len(in.Tests) != 3 || in.Tests[0].Score == nil || *in.Tests[0].Score != 0 || in.Tests[1].Status != "missing" || in.Tests[1].Score != nil || in.Tests[2].Source != "external" || in.Tests[0].Direction != " МАТЕМАТИКА " {
		t.Fatal("test input was preaggregated or missing replaced with another account result", in.Tests)
	}
	if len(in.Exams) != 2 || in.Exams[0].Score == nil || *in.Exams[0].Score != five || in.Exams[0].AttemptNo == nil || *in.Exams[0].AttemptNo != 2 || !in.Exams[0].HasAppeal || !in.Exams[1].MissingResult || in.Exams[1].Score != nil {
		t.Fatal("selected exam attempt/appeal or absence was changed", in.Exams)
	}
	raw, _ := json.Marshal(in)
	if strings.Contains(string(raw), "Synthetic pupil") || strings.Contains(string(raw), "profile_id") {
		t.Fatal("personal calculate input leaked identity metadata")
	}
	got, e := canonicalHash(raw, "input_hash")
	if e != nil || got != in.InputHash {
		t.Fatal("stored calculate input did not retain its hash", e)
	}
	otherDivision := j
	otherDivision.DivisionID++
	other, e := personal(otherDivision, s, s.Core.Accounts[0])
	if e != nil || other.InputHash == in.InputHash {
		t.Fatal("same account's inputs in two divisions collided", e)
	}
}

func TestPersonalPendingTestBlocksNewRatingButOtherAccountPendingDoesNot(t *testing.T) {
	j := fixtureJob()
	for _, status := range []string{"queued", "processing", "retrying", "failed", "ready"} {
		t.Run(status, func(t *testing.T) {
			s := fixtureSnapshot(j)
			s.Tests.Tests = []Test{{ID: "pending", Results: []TestResult{{AccountID: 71, Status: status}}}}
			_, e := personal(j, s, s.Core.Accounts[0])
			assertAPIError(t, e, 409, "tests_calculation_pending")
			s.Tests.Tests[0].Results[0].AccountID = 99
			in, e := personal(j, s, s.Core.Accounts[0])
			if e != nil || in.Tests[0].Status != "missing" || in.Tests[0].Score != nil {
				t.Fatal("another account's pending result changed this input", e)
			}
		})
	}
}

func TestCalendarTransitionsAndPeriodBounds(t *testing.T) {
	j := fixtureJob()
	s := fixtureSnapshot(j)
	transition := j.AsOf.Add(time.Hour)
	s.Homework.NextTransitionAt = &transition
	if transitioned(s, transition.Add(-time.Nanosecond)) || !transitioned(s, transition) || !transitioned(s, transition.Add(time.Hour)) {
		t.Fatal("homework midnight transition lost its inclusive boundary")
	}
	s.Homework.NextTransitionAt = nil
	s.Exams.NextTransitionAt = &transition
	if !transitioned(s, transition) {
		t.Fatal("exam start transition was ignored")
	}
	for _, period := range [][2]string{{"2026-10-08", "2026-10-07"}, {"2026-02-29", "2026-03-01"}, {"2026-01-01", "2027-01-03"}, {"", "2026-10-08"}} {
		if validPeriod(period[0], period[1]) {
			t.Fatal("invalid or oversized period accepted", period)
		}
	}
	if !validPeriod("2026-10-08", "2026-10-08") || !validPeriod("2026-01-01", "2027-01-01") {
		t.Fatal("valid inclusive rating period was rejected")
	}
	if reflect.DeepEqual(fingerprint(s), fingerprint(Snapshot{})) {
		t.Fatal("source fingerprint discarded its input sources")
	}
}
