package exams

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestVisiblePageBuildsOnlyRequestedDTOs(t *testing.T) {
	rows := make([]int, 100000)
	for i := range rows {
		rows[i] = i
	}
	calls := 0
	out, err := selectedPage(rows, Params{Query: map[string]string{"limit": "20", "offset": "99800"}}, func(v int) any { calls++; return map[string]int{"id": v} })
	if err != nil || calls != 20 || out.(map[string]any)["total"] != 100000 {
		t.Fatal("unbounded DTO work", calls, err)
	}
	if _, err = selectedPage(rows, Params{Query: map[string]string{"limit": "201"}}, func(v int) any { return v }); err == nil {
		t.Fatal("unbounded limit accepted")
	}
}
func TestPagedQueryFiltersEnrichmentAndNoMutation(t *testing.T) {
	f := fixtureDomain(t, "round_up")
	d := f.e.Divisions[1]
	for n := 0; n < 500; n++ {
		d.Assignments = append(d.Assignments, Assignment{ID: int64(1000 + n), Student: Member{AccountID: int64(10000 + n), FullName: fmt.Sprintf("Student %03d", n)}, Members: []Member{{AccountID: 21}}, AttemptNo: 1, CommissionID: f.commission})
	}
	before := auxJSON(t, f.e)
	p := f.p
	p.Query = map[string]string{"limit": "20", "offset": "20", "q": "Student"}
	out, err := Query(f.e, f.x1, "assignments", p)
	if err != nil {
		t.Fatal(err)
	}
	page := out.(map[string]any)
	if page["total"] != 501 || len(page["items"].([]any)) != 20 {
		t.Fatal(page)
	}
	p.Query = map[string]string{"limit": "1"}
	out, err = Query(f.e, f.admin, "commissions", p)
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["items"].([]any)[0].(map[string]any)["assigned_students_count"] != 501 {
		t.Fatal(out)
	}
	p.Query = map[string]string{"limit": "1", "q": "Question 1-2"}
	out, err = Query(f.e, f.admin, "questions", p)
	if err != nil {
		t.Fatal(err)
	}
	q := out.(map[string]any)["items"].([]any)[0].(map[string]any)
	if q["part_code"] != "B" {
		t.Fatal(q)
	}
	originalPrivileges := d.Privileges
	d.Privileges = map[int64]Privilege{11: {Limit: 2, Revision: 3}}
	p.StudentID = 11
	p.Query = map[string]string{"limit": "1"}
	out, err = Query(f.e, f.admin, "privileges", p)
	if err != nil {
		t.Fatal(err)
	}
	privilege := out.(map[string]any)["items"].([]any)[0].(map[string]any)
	if privilege["student"].(Member).FullName != "Student" {
		t.Fatal(privilege)
	}
	d.Privileges = originalPrivileges
	if string(before) != string(auxJSON(t, f.e)) {
		t.Fatal("query mutated aggregate")
	}
}
func TestTrainingSummaryPagesAndBatchDoNotReturnWholeBank(t *testing.T) {
	e, a, p := auxFixture()
	e.Name = "Study"
	for n := 0; n < 1000; n++ {
		e.Questions = append(e.Questions, Question{ID: int64(100 + n), PartID: 4, Text: fmt.Sprintf("Question %04d", n), Answer: "PRIVATE_ANSWER"})
	}
	p.Query = map[string]string{"preview": "true", "summary": "true", "batch_size": "20"}
	before := auxJSON(t, e)
	out, err := TrainingView(e, a, p, nil, DefaultTrainingSettings())
	if err != nil {
		t.Fatal(err)
	}
	summary := out.(map[string]any)
	if len(summary["cards"].([]TrainingCard)) != 0 || summary["cards_total"] != 1001 || summary["settings"].(TrainingSettings).BatchSize != 20 {
		t.Fatal(summary)
	}
	if strings.Contains(string(auxJSON(t, summary)), "PRIVATE_ANSWER") {
		t.Fatal("summary leaked card text")
	}
	p.Query = map[string]string{"preview": "true", "limit": "20", "offset": "980"}
	out, err = TrainingView(e, a, p, nil, DefaultTrainingSettings())
	if err != nil {
		t.Fatal(err)
	}
	page := out.(map[string]any)
	if len(page["cards"].([]TrainingCard)) != 20 || page["cards_total"] != 1001 || !reflect.DeepEqual(summary["stats"], page["stats"]) {
		t.Fatal("paged training stats changed")
	}
	if page["cards_pagination"].(map[string]int)["offset"] != 980 {
		t.Fatal("missing page offset")
	}
	p.Query = map[string]string{"preview": "true", "batch_index": "2", "batch_size": "20"}
	out, err = TrainingView(e, a, p, nil, DefaultTrainingSettings())
	if err != nil {
		t.Fatal(err)
	}
	cards := out.(map[string]any)["cards"].([]TrainingCard)
	if len(cards) != 20 || cards[0].QuestionID != 139 {
		t.Fatal("batch selection", len(cards))
	}
	p.Query = map[string]string{"preview": "true", "limit": "20", "offset": "2000"}
	out, err = TrainingView(e, a, p, nil, DefaultTrainingSettings())
	if err != nil || len(out.(map[string]any)["cards"].([]TrainingCard)) != 0 {
		t.Fatal("out of range", err)
	}
	if string(before) != string(auxJSON(t, e)) {
		t.Fatal("training preview mutated exam")
	}
}
func TestImportErrorPagesPreserveGlobalHashAndCounts(t *testing.T) {
	e, a, p := auxFixture()
	rows := make([]ImportRow, 5000)
	for i := range rows {
		rows[i] = auxRow(t, fmt.Sprint(i), map[string]any{"partCode": "A", "questionText": fmt.Sprint(i), "answerText": "a"})
	}
	rows[250].Errors = []string{"invalid"}
	rows[4999].Errors = []string{"invalid"}
	rows[4999].Excluded = true
	all, err := importPreview("id", 1, "editable", "questions", e, a, p, rows, p.Now, "file", "hash", nil, ImportPage{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	bad, err := importPreview("id", 1, "editable", "questions", e, a, p, rows, p.Now, "file", "hash", nil, ImportPage{Limit: 20, OnlyErrors: true})
	if err != nil {
		t.Fatal(err)
	}
	if bad.Total != 1 || len(bad.Rows) != 1 || bad.Rows[0].RowID != "250" || bad.Summary.Total != 5000 || bad.Summary.Errors != 1 || bad.ValidationHash != all.ValidationHash {
		t.Fatal("filtered summary or hash differs", bad.Summary)
	}
}
func TestReadinessReferencesScopedAndMissingBatchBisected(t *testing.T) {
	f := fixtureDomain(t, "round_up")
	for n := 0; n < 2000; n++ {
		f.e.Divisions[1].Assignments = append(f.e.Divisions[1].Assignments, Assignment{ID: int64(2000 + n), Student: Member{AccountID: int64(3000 + n)}, Members: []Member{{AccountID: 21}}})
	}
	ids := operationMemberIDs(f.e, "readiness", f.p, nil)
	if len(ids) != 3 || !contains(ids, 11) || !contains(ids, 21) || !contains(ids, 22) {
		t.Fatal("readiness scans all accounts", len(ids))
	}
	identity := &performanceIdentity{members: map[int64]Member{}}
	ids = nil
	for n := int64(1); n <= 500; n++ {
		identity.members[n] = Member{AccountID: n, DivisionIDs: []int64{1}}
		ids = append(ids, n)
	}
	delete(identity.members, 250)
	s := &Server{Deps: identity}
	members, err := s.readMembers(httptest.NewRequest("GET", "/", nil), Actor{OrganizationID: 10}, Params{DivisionID: 1}, 7, ids)
	if err != nil || len(members) != 499 || len(identity.calls) > 25 {
		t.Fatal("one missing account caused sequential per-account fetches", len(identity.calls), err)
	}
	for _, call := range identity.calls {
		if len(call) > 500 {
			t.Fatal("dependency limit exceeded")
		}
	}
	raw, _ := json.Marshal(members)
	if strings.Contains(string(raw), `"account_id":250,`) {
		t.Fatal("removed account returned")
	}
}
