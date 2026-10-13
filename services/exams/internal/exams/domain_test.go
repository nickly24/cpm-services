package exams

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type domainFixture struct {
	e                          *Exam
	p                          Params
	admin, x1, x2, x3, student Actor
	assignment, commission     int64
}

func fixtureDomain(t *testing.T, mode string) *domainFixture {
	t.Helper()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	f := &domainFixture{e: &Exam{ID: 1, OrganizationID: 10, Kind: "classic", Revision: 1, ContentRevision: 1, NextID: 100, Divisions: map[int64]*Division{1: {ID: 1, Revision: 1, Config: Config{Revision: 1}}, 2: {ID: 2, Revision: 1, Config: Config{Revision: 1}}}}, p: Params{DivisionID: 1, Now: now, Members: map[int64]Member{11: {AccountID: 11, FullName: "Student", Role: "student", Active: true}, 21: {AccountID: 21, FullName: "First", Role: "examinator", Active: true}, 22: {AccountID: 22, FullName: "Second", Role: "examinator", Active: true}, 23: {AccountID: 23, FullName: "Third", Role: "examinator", Active: true}}}, admin: Actor{AccountID: 31, Role: "admin", OrganizationAdmin: true}, x1: Actor{AccountID: 21, Role: "examinator"}, x2: Actor{AccountID: 22, Role: "examinator"}, x3: Actor{AccountID: 23, Role: "examinator"}, student: Actor{AccountID: 11, Role: "student"}}
	for i, w := range []int{3, 2} {
		r := f.cmd(t, f.admin, "part-create", map[string]any{"code": string(rune('A' + i)), "weight": w, "quota": 1})
		id := r["part"].(Part).ID
		for j := 0; j < 4; j++ {
			f.p.PartID = id
			f.cmd(t, f.admin, "question-create", map[string]any{"part_id": id, "question_text": fmt.Sprintf("Question %d-%d", i, j), "answer_text": fmt.Sprintf("Answer %d-%d", i, j)})
		}
	}
	f.p.PartID = 0
	thresholds := []Threshold{}
	for i := 0; i < 6; i++ {
		thresholds = append(thresholds, Threshold{i, i})
	}
	f.cmd(t, f.admin, "config", map[string]any{"expected_revision": f.e.Divisions[1].Config.Revision, "start_at": start, "end_at": end, "fractional_mode": mode, "tie_breaker_source_mode": "any_part", "tie_breaker_half_mode": "repeat", "thresholds": thresholds})
	r := f.cmd(t, f.admin, "commission-create", map[string]any{"name": "Commission", "examinator_ids": []int64{21, 22}})
	f.commission = r["commission"].(Commission).ID
	r = f.cmd(t, f.admin, "assignment-create", map[string]any{"student_account_id": 11, "commission_id": f.commission})
	f.assignment = r["assignment"].(Assignment).ID
	f.p.AssignmentID = f.assignment
	return f
}
func (f *domainFixture) cmd(t *testing.T, a Actor, op string, body any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	v, err := Command(f.e, a, op, f.p, raw)
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	return v.(map[string]any)
}
func (f *domainFixture) fail(t *testing.T, a Actor, op, body, code string) {
	t.Helper()
	before, _ := json.Marshal(f.e)
	next := f.e.NextID
	_, err := Command(f.e, a, op, f.p, json.RawMessage(body))
	if err == nil || err.Error() != code {
		t.Fatalf("%s want %s got %v", op, code, err)
	}
	after, _ := json.Marshal(f.e)
	if string(before) != string(after) || next != f.e.NextID {
		t.Fatalf("%s error mutated aggregate", op)
	}
}
func (f *domainFixture) attempt() *Attempt {
	d := f.e.Divisions[f.p.DivisionID]
	i := attemptIndex(d, f.p.AttemptID)
	if i < 0 {
		return nil
	}
	return &d.Attempts[i]
}
func (f *domainFixture) start(t *testing.T) {
	t.Helper()
	f.cmd(t, f.x1, "ensure", map[string]any{"expected_history_generation": 1})
	f.p.AttemptID = f.e.Divisions[f.p.DivisionID].Attempts[len(f.e.Divisions[f.p.DivisionID].Attempts)-1].ID
	f.cmd(t, f.x1, "ready", map[string]any{})
	f.cmd(t, f.x2, "ready", map[string]any{})
	f.cmd(t, f.x1, "start", map[string]any{"expected_state_version": f.attempt().StateVersion})
}
func (f *domainFixture) vote(t *testing.T, a Actor, value float64) map[string]any {
	t.Helper()
	q := currentQuestion(f.attempt())
	r := q.Rounds[len(q.Rounds)-1]
	return f.cmd(t, a, "vote", map[string]any{"presented_question_id": q.ID, "round_id": r.ID, "value": value})
}
func (f *domainFixture) consensus(t *testing.T, value float64) {
	t.Helper()
	f.vote(t, f.x1, value)
	f.vote(t, f.x2, value)
}
func (f *domainFixture) next(t *testing.T) {
	t.Helper()
	f.cmd(t, f.x1, "next-question", map[string]any{"expected_state_version": f.attempt().StateVersion, "presented_question_id": currentQuestion(f.attempt()).ID})
}
func queryDomain(t *testing.T, f *domainFixture, a Actor, op string) any {
	t.Helper()
	v, err := Query(f.e, a, op, f.p)
	if err != nil {
		t.Fatalf("query %s: %v", op, err)
	}
	return v
}

func TestDomainExactScoringExhaustive(t *testing.T) {
	for weight := 1; weight <= 1000; weight++ {
		for value := 0; value <= 2; value++ {
			raw := weight * value
			for _, up := range []bool{false, true} {
				expected := raw / 2
				if up && raw%2 == 1 {
					expected++
				}
				if got := roundHalf(raw, up); got != expected {
					t.Fatal(weight, value, up, got)
				}
			}
			parsed, err := voteHalf(halfNumber(value))
			if err != nil || parsed != value {
				t.Fatal(value, err)
			}
		}
	}
	// Exhaust all representable scores through the maximum 26-part exam.
	for raw := 0; raw <= 2*26*1000*100; raw++ {
		down, up := roundHalf(raw, false), roundHalf(raw, true)
		if down*2 > raw || up*2 < raw || up-down != raw%2 {
			t.Fatalf("rounding %d: %d/%d", raw, down, up)
		}
	}
	for _, n := range []string{"0.1", "-0.5", "1.5", "2", "NaN", "Infinity", "true", ""} {
		if _, err := voteHalf(json.Number(n)); err == nil {
			t.Fatal("accepted invalid", n)
		}
	}
	for _, n := range []string{"0", "0.0", "0e3", "0.5", "5e-1", "1.0"} {
		if _, err := voteHalf(json.Number(n)); err != nil {
			t.Fatal(n, err)
		}
	}
	for maximum := 5; maximum <= 100; maximum++ {
		ts := []Threshold{{0, 0}, {1, 1}, {2, 2}, {3, 3}, {4, 4}, {5, maximum}}
		if errs := validateThresholds(maximum, ts); len(errs) > 0 {
			t.Fatal(errs)
		}
		for score := 0; score <= maximum; score++ {
			expected := score
			if expected > 4 {
				expected = 4
			}
			if score == maximum {
				expected = 5
			}
			if gradeForScore(score, ts) != expected {
				t.Fatal(maximum, score)
			}
		}
	}
	for _, row := range []struct {
		vote       int
		mode, want string
	}{{0, "repeat", "down"}, {2, "repeat", "up"}, {1, "repeat", "repeat"}, {1, "round_down", "down"}, {1, "round_up", "up"}} {
		got, err := extraDirection(row.vote, row.mode)
		if err != nil || got != row.want {
			t.Fatal(row, got, err)
		}
	}
}
func TestDomainWeightedSelectionAndCycles(t *testing.T) {
	a := &Attempt{Config: Config{TieBreakerSourceMode: "any_part"}, Parts: []Part{{ID: 1, Quota: 2}, {ID: 2, Quota: 1}}, Questions: []Question{{ID: 11, PartID: 1}, {ID: 12, PartID: 1}, {ID: 21, PartID: 2}, {ID: 22, PartID: 2}}, Usage: map[int64]Usage{}, Progress: map[int64]int{}, Cycles: map[int64]int{1: 1, 2: 1}}
	for ticket, want := range []int64{1, 1, 2} {
		calls := 0
		choice, err := selectQuestion(a, "regular", 0, 0, func(n int) (int, error) {
			calls++
			if calls == 1 {
				return ticket, nil
			}
			return 0, nil
		})
		if err != nil || choice.partID != want {
			t.Fatal(ticket, choice, err)
		}
	}
	a.Usage[11] = Usage{Presented: true, Cycle: 1}
	a.Usage[12] = Usage{Presented: true, Excluded: true, Cycle: 1}
	a.Presented = []Presented{{Question: a.Questions[0]}}
	choice, err := selectQuestion(a, "regular", 1, 0, func(int) (int, error) { return 0, nil })
	if err == nil {
		t.Fatal("regular repeated", choice)
	}
	// Selected part cycle increments; last question remains forbidden. The
	// excluded question does not reappear even in later additional cycles.
	a.Presented = []Presented{{Question: a.Questions[2]}}
	choice, err = selectQuestion(a, "tie_breaker", 1, 0, func(int) (int, error) { return 0, nil })
	if err != nil || choice.question.ID != 11 || choice.cycle != 2 {
		t.Fatal(choice, err)
	}
	if a.Cycles[1] != 1 || a.Cycles[2] != 1 {
		t.Fatal("selection mutated cycles")
	}
	a.Config.TieBreakerSourceMode = "specific_part"
	a.Config.TieBreakerPartID = 2
	a.Usage[21] = Usage{Presented: true, Cycle: 1}
	a.Usage[22] = Usage{Presented: true, Cycle: 1}
	choice, err = selectQuestion(a, "tie_breaker", 0, 0, func(int) (int, error) { return 0, nil })
	if err != nil || choice.question.ID != 22 || choice.cycle != 2 {
		t.Fatal(choice, err)
	}
}
func TestDomainLifecycleDisputesSnapshotAndPrivacy(t *testing.T) {
	f := fixtureDomain(t, "extra_question")
	f.start(t)
	at := f.attempt()
	if at.Status != "in_progress" || at.StateVersion != 4 || at.ContentRevision != f.e.ContentRevision {
		t.Fatal(at)
	}
	if _, err := Query(f.e, f.student, "protocol", f.p); err == nil {
		t.Fatal("student saw unpublished protocol")
	}
	detail := queryDomain(t, f, f.x1, "detail").(map[string]any)
	if _, ok := detail["config"]; ok {
		t.Fatal("examiner bank config leak")
	}
	q := currentQuestion(f.attempt())
	oldText := q.Question.Text
	f.p.QuestionID = q.Question.ID
	f.cmd(t, f.admin, "question-update", map[string]any{"expected_revision": f.e.Questions[questionIndex(f.e, q.Question.ID)].Revision, "question_text": "CHANGED BANK"})
	if currentQuestion(f.attempt()).Question.Text != oldText {
		t.Fatal("mutable bank snapshot")
	}
	f.vote(t, f.x1, 1)
	state := queryDomain(t, f, f.x2, "attempt").(map[string]any)
	round := state["current_question"].(map[string]any)["round"].(map[string]any)
	if _, ok := round["votes"]; ok {
		t.Fatal("open foreign votes leak")
	}
	if _, ok := round["own_vote"]; ok {
		t.Fatal("wrong own vote")
	}
	f.vote(t, f.x2, 0)
	q = currentQuestion(f.attempt())
	if len(q.Rounds) != 2 || q.Rounds[0].Status != "disputed" || q.Rounds[1].Status != "open" {
		t.Fatal(q)
	}
	protocol := queryDomain(t, f, f.x2, "protocol").(map[string]any)
	row := protocol["items"].([]any)[0].(map[string]any)
	if len(row["rounds"].([]any)[0].(map[string]any)["votes"].([]any)) != 2 {
		t.Fatal("closed votes hidden")
	}
	f.fail(t, f.x1, "replace-question", fmt.Sprintf(`{"expected_state_version":%d,"presented_question_id":%d}`, f.attempt().StateVersion, q.ID), "replacement_after_vote")
	f.consensus(t, 0.5)
	f.next(t)
	f.consensus(t, 0.5)
	if f.attempt().Phase != "tie_breaker" || f.attempt().RawHalf != 5 {
		t.Fatal(f.attempt())
	}
	last := currentQuestion(f.attempt()).Question.ID
	f.consensus(t, 0.5)
	if currentQuestion(f.attempt()).Question.ID == last {
		t.Fatal("extra repeated last")
	}
	f.consensus(t, 1)
	at = f.attempt()
	if at.Status != "completed" || at.RawHalf != 5 || at.RoundedTotal != 3 || at.CalculatedGrade != 3 || at.Resolution != "tie_breaker_up" {
		t.Fatal(at)
	}
	if at.MaxScore != 5 {
		t.Fatal("extra counted in maximum")
	}
	state = queryDomain(t, f, f.student, "attempt").(map[string]any)
	if _, ok := state["config_snapshot"]; ok {
		t.Fatal("student snapshot leak")
	}
	protocol = queryDomain(t, f, f.student, "protocol").(map[string]any)
	for _, item := range protocol["items"].([]any) {
		raw, _ := json.Marshal(item)
		if strings.Contains(string(raw), "votes") || strings.Contains(string(raw), "rounds") {
			t.Fatal("student votes leak", string(raw))
		}
	}
	f.fail(t, f.x1, "vote", `{"presented_question_id":1,"round_id":1,"value":1}`, "attempt_not_active")
}
func TestDomainReadinessWindowAndReplacement(t *testing.T) {
	f := fixtureDomain(t, "round_down")
	f.p.StudentID = 11
	f.cmd(t, f.admin, "privilege-put", map[string]any{"expected_revision": 0, "replacement_limit": 4})
	f.fail(t, f.x1, "ensure", `{"expected_history_generation":1}`, "part_bank_too_small")
	f.cmd(t, f.admin, "privilege-put", map[string]any{"expected_revision": 1, "replacement_limit": 1})
	f.cmd(t, f.x1, "ensure", map[string]any{"expected_history_generation": 1})
	f.p.AttemptID = f.e.Divisions[1].Attempts[0].ID
	f.fail(t, f.admin, "ready", `{}`, "forbidden")
	f.cmd(t, f.x1, "ready", map[string]any{})
	v := f.attempt().StateVersion
	r := f.cmd(t, f.x1, "ready", map[string]any{})
	if r["changed"] != false || f.attempt().StateVersion != v {
		t.Fatal("ready repeat changed")
	}
	f.fail(t, f.x1, "start", fmt.Sprintf(`{"expected_state_version":%d}`, v), "commission_not_ready")
	f.cmd(t, f.x2, "ready", map[string]any{})
	f.p.Now = f.p.Now.Add(24 * time.Hour)
	f.fail(t, f.x1, "start", fmt.Sprintf(`{"expected_state_version":%d}`, f.attempt().StateVersion), "exam_outside_window")
	f.p.Now = f.p.Now.Add(-24 * time.Hour)
	f.cmd(t, f.x1, "start", map[string]any{"expected_state_version": f.attempt().StateVersion})
	q := currentQuestion(f.attempt())
	old, part := q.Question.ID, q.Question.PartID
	f.cmd(t, f.x1, "replace-question", map[string]any{"expected_state_version": f.attempt().StateVersion, "presented_question_id": q.ID})
	q = currentQuestion(f.attempt())
	if q.Question.ID == old || q.Question.PartID != part || !f.attempt().Usage[old].Excluded {
		t.Fatal("replacement")
	}
	f.fail(t, f.x1, "replace-question", fmt.Sprintf(`{"expected_state_version":%d,"presented_question_id":%d}`, f.attempt().StateVersion, q.ID), "replacement_limit_exhausted")
	// Ending the start window does not stop an in-progress attempt.
	f.p.Now = f.p.Now.Add(24 * time.Hour)
	f.consensus(t, 1)
	f.next(t)
	f.consensus(t, 1)
	if f.attempt().Status != "completed" {
		t.Fatal("timed out")
	}
}
func TestDomainRetakeAppealAndHistory(t *testing.T) {
	f := fixtureDomain(t, "round_up")
	f.start(t)
	f.consensus(t, 1)
	f.next(t)
	f.consensus(t, 1)
	firstID := f.p.AttemptID
	f.p.StudentID = 11
	f.fail(t, f.admin, "retake", fmt.Sprintf(`{"commission_id":%d,"expected_history_generation":1}`, f.commission), "retake_commission_unchanged")
	r := f.cmd(t, f.admin, "commission-create", map[string]any{"name": "Retake", "examinator_ids": []int64{21, 23}})
	secondCommission := r["commission"].(Commission).ID
	r = f.cmd(t, f.admin, "retake", map[string]any{"commission_id": secondCommission, "expected_history_generation": 1})
	f.p.AssignmentID = r["assignment"].(Assignment).ID
	f.cmd(t, f.x1, "ensure", map[string]any{"expected_history_generation": 1})
	f.p.AttemptID = f.e.Divisions[1].Attempts[1].ID
	if effectiveAttempt(f.e.Divisions[1], 11).ID != firstID {
		t.Fatal("pending retake replaced result")
	}
	f.cmd(t, f.x1, "ready", map[string]any{})
	f.cmd(t, f.x3, "ready", map[string]any{})
	f.cmd(t, f.x1, "start", map[string]any{"expected_state_version": f.attempt().StateVersion})
	f.vote(t, f.x1, 0)
	f.vote(t, f.x3, 0)
	f.next(t)
	f.vote(t, f.x1, 0)
	f.vote(t, f.x3, 0)
	secondID := f.p.AttemptID
	if effectiveAttempt(f.e.Divisions[1], 11).CalculatedGrade != 0 {
		t.Fatal("worse retake discarded")
	}
	f.p.AttemptID = firstID
	r = f.cmd(t, f.admin, "appeal", map[string]any{"grade": 4, "expected_result_version": f.attempt().ResultVersion})
	if r["rating_changed"] != false {
		t.Fatal("historical appeal invalidated rating")
	}
	f.p.AttemptID = secondID
	r = f.cmd(t, f.admin, "appeal", map[string]any{"grade": 2, "expected_result_version": f.attempt().ResultVersion})
	if r["rating_changed"] != true || effectiveGrade(f.attempt()) != 2 || f.attempt().RawHalf != 0 {
		t.Fatal("appeal recalculated raw score")
	}
	f.fail(t, f.admin, "retake", fmt.Sprintf(`{"commission_id":%d,"expected_history_generation":1}`, secondCommission), "retake_already_assigned")
	studentResult := queryDomain(t, f, f.student, "result").(map[string]any)
	history := studentResult["history"].([]any)
	if len(history) != 1 || history[0].(map[string]any)["grade"] != 4 || studentResult["current"].(map[string]any)["grade"] != 2 {
		t.Fatal("student history", studentResult)
	}
	studentRaw, _ := json.Marshal(studentResult)
	for _, private := range []string{"votes_half", "thresholds", "calculated_grade", "actor_account_id"} {
		if strings.Contains(string(studentRaw), private) {
			t.Fatal("student internal result leak", private)
		}
	}
	f.p.AttemptID = secondID
	preview := queryDomain(t, f, f.admin, "history-preview").(map[string]any)
	if preview["history_generation"] != int64(1) {
		t.Fatal(preview)
	}
	f.cmd(t, f.admin, "history-delete", map[string]any{"expected_history_generation": 1})
	d := f.e.Divisions[1]
	if len(d.Attempts) != 0 || len(d.Assignments) != 1 || d.Assignments[0].HistoryGeneration != 2 {
		t.Fatal(d)
	}
	f.p.AssignmentID = f.assignment
	f.fail(t, f.x1, "ensure", `{"expected_history_generation":1}`, "history_generation_changed")
	f.cmd(t, f.x1, "ensure", map[string]any{"expected_history_generation": 2})
}
func TestDomainDivisionIndependenceAndOutsideResults(t *testing.T) {
	f := fixtureDomain(t, "round_up")
	f.start(t)
	oneID := f.p.AttemptID
	d2 := f.e.Divisions[2]
	d2.Config = f.e.Divisions[1].Config
	f.p.DivisionID = 2
	r := f.cmd(t, f.admin, "commission-create", map[string]any{"name": "Division two", "examinator_ids": []int64{21, 22}})
	r = f.cmd(t, f.admin, "assignment-create", map[string]any{"student_account_id": 11, "commission_id": r["commission"].(Commission).ID})
	f.p.AssignmentID = r["assignment"].(Assignment).ID
	f.start(t)
	if f.p.AttemptID == oneID || len(f.e.Divisions[1].Attempts) != 1 || len(f.e.Divisions[2].Attempts) != 1 {
		t.Fatal("shared sessions")
	}
	f.p.AttemptID = oneID
	f.fail(t, f.x1, "ready", `{}`, "not_found")
	f.e.Kind = "outside_lms"
	f.p.ResultID = 0
	for _, bad := range []string{"0.001", "-1", "10000000000", "1.234"} {
		f.fail(t, f.admin, "outside-create", fmt.Sprintf(`{"student_account_id":11,"points":%s,"grade":4,"examinator":"External"}`, bad), "invalid_points")
	}
	r = f.cmd(t, f.admin, "outside-create", map[string]any{"student_account_id": 11, "points": json.Number("9999999999.99"), "grade": 1, "examinator": "External"})
	out := r["result"].(OutsideResult)
	if out.Grade != 1 || out.Points != "9999999999.99" {
		t.Fatal(out)
	}
	f.p.ResultID = out.ID
	f.fail(t, f.admin, "outside-create", `{"student_account_id":11,"points":1,"grade":5,"examinator":"External"}`, "outside_result_already_exists")
	f.p.DivisionID = 1
	f.cmd(t, f.admin, "outside-create", map[string]any{"student_account_id": 11, "points": 2, "grade": 5, "examinator": "External"})
	result := queryDomain(t, f, f.student, "result").(map[string]any)
	if result["grade"] != 5 {
		t.Fatal("cross division result")
	}
}
func TestDomainStrictDTOsFiltersAndAtomicFailures(t *testing.T) {
	f := fixtureDomain(t, "round_up")
	f.fail(t, f.admin, "part-create", `{"code":"C","code":"D"}`, "invalid_json")
	f.fail(t, f.admin, "part-create", `{"code":"C","organization_id":99}`, "invalid_request")
	f.fail(t, f.admin, "part-create", `{"code":"C","weight":true}`, "invalid_request")
	f.fail(t, f.admin, "config", fmt.Sprintf(`{"expected_revision":%d,"thresholds":[{"grade":0},{"grade":1,"min_score":1},{"grade":2,"min_score":2},{"grade":3,"min_score":3},{"grade":4,"min_score":4},{"grade":5,"min_score":5}]}`, f.e.Divisions[1].Config.Revision), "invalid_request")
	f.start(t)
	q := currentQuestion(f.attempt())
	f.fail(t, f.x1, "vote", fmt.Sprintf(`{"presented_question_id":%d,"round_id":%d,"value":"0.5"}`, q.ID, q.Rounds[0].ID), "invalid_request")
	f.fail(t, f.admin, "part-create", `null`, "invalid_json_object")
	f.fail(t, f.admin, "part-create", `{"code":"C"}{}`, "invalid_json")
	f.fail(t, f.admin, "question-create", `{"part_id":999,"question_text":"X","answer_text":"Y"}`, "not_found")
	f.p.Query = map[string]string{"q": "Question 1-", "limit": "1", "offset": "2"}
	page := queryDomain(t, f, f.admin, "questions").(map[string]any)
	if page["total"] != 4 || len(page["items"].([]any)) != 1 || page["items"].([]any)[0].(Question).Text != "Question 1-2" {
		t.Fatal("filter after pagination", page)
	}
	// Draft part is permitted, but a draft exam cannot start until configured.
	f.p.Query = nil
	r := f.cmd(t, f.admin, "part-create", map[string]any{"code": "C"})
	part := r["part"].(Part)
	if part.Weight != 0 || part.Quota != 0 {
		t.Fatal(part)
	}
	before := f.e.ContentRevision
	f.p.PartID = part.ID
	f.cmd(t, f.admin, "part-update", map[string]any{"expected_revision": part.Revision, "weight": 5, "quota": 1})
	if f.e.ContentRevision != before+1 {
		t.Fatal("content revision")
	}
}
func TestDomainReadinessUsesCurrentAccounts(t *testing.T) {
	f := fixtureDomain(t, "round_up")
	m := f.p.Members[22]
	m.Active = false
	f.p.Members[22] = m
	f.fail(t, f.x1, "ensure", `{"expected_history_generation":1}`, "examinator_not_found")
	m.Active = true
	m.Role = "student"
	f.p.Members[22] = m
	f.fail(t, f.admin, "commission-create", `{"name":"Wrong role","examinator_ids":[22]}`, "examinator_not_found")
	m.Role = "examinator"
	f.p.Members[22] = m
	f.fail(t, f.admin, "commission-create", `{"name":"Duplicate","examinator_ids":[21,21]}`, "duplicate_examinator")
}
func TestDomainThresholdRejectCases(t *testing.T) {
	valid := []Threshold{{0, 0}, {1, 1}, {2, 2}, {3, 3}, {4, 4}, {5, 5}}
	for _, row := range []struct {
		maximum int
		rows    []Threshold
		code    string
	}{{4, valid, "max_score_too_low"}, {5, nil, "grade_thresholds_required"}, {5, valid[:5], "invalid_threshold_set"}, {5, []Threshold{{0, 0}, {1, 1}, {2, 2}, {3, 3}, {4, 4}, {5, 6}}, "threshold_above_max"}, {5, []Threshold{{0, 0}, {1, 1}, {2, 2}, {3, 3}, {4, 4}, {5, 4}}, "threshold_not_increasing"}} {
		errs := validateThresholds(row.maximum, row.rows)
		found := false
		for _, code := range errs {
			if code == row.code {
				found = true
			}
		}
		if !found {
			t.Fatal(row, errs)
		}
	}
	if !reflect.DeepEqual(valid, []Threshold{{0, 0}, {1, 1}, {2, 2}, {3, 3}, {4, 4}, {5, 5}}) {
		t.Fatal("validation mutated input")
	}
}

func TestDomainReceiptReplayAuthorizationAndBankPrivacy(t *testing.T) {
	f := fixtureDomain(t, "round_up")
	f.start(t)
	if err := authorizeCommand(f.e, f.x1, "vote", f.p); err != nil {
		t.Fatal(err)
	}
	outsider := Actor{AccountID: 24, Role: "examinator"}
	if err := authorizeCommand(f.e, outsider, "vote", f.p); err != Missing {
		t.Fatal("outsider replay", err)
	}
	former := f.x1
	former.Role = "student"
	if err := authorizeCommand(f.e, former, "vote", f.p); err != Denied {
		t.Fatal("changed role replay", err)
	}
	if err := authorizeCommand(f.e, f.student, "copy", f.p); err != Denied {
		t.Fatal("student admin replay", err)
	}
	f.e.InBank = true
	f.p.DivisionID = 99
	if err := authorizeCommand(f.e, f.admin, "copy", f.p); err != nil {
		t.Fatal("bank copy authorization", err)
	}
	detail := queryDomain(t, f, f.admin, "detail").(map[string]any)
	for _, key := range []string{"config", "commissions", "assignments", "results", "readiness"} {
		if _, ok := detail[key]; ok {
			t.Fatal("bank cross division leak", key)
		}
	}
	if detail["connected"] != false {
		t.Fatal(detail)
	}
	queryDomain(t, f, f.admin, "questions")
	if _, err := Query(f.e, f.admin, "commissions", f.p); err != Missing {
		t.Fatal("foreign division commissions", err)
	}
}
func TestDomainCommissionSnapshotAndTimestamps(t *testing.T) {
	f := fixtureDomain(t, "round_up")
	f.start(t)
	at := f.attempt()
	if at.ReadyAt[21] != f.p.Now || at.ReadyAt[22] != f.p.Now || currentQuestion(at).PresentedAt != f.p.Now || currentQuestion(at).Rounds[0].OpenedAt != f.p.Now {
		t.Fatal("missing transition timestamps")
	}
	f.p.CommissionID = f.commission
	f.cmd(t, f.admin, "commission-delete", map[string]any{"expected_revision": f.e.Divisions[1].Commissions[0].Revision})
	as := f.e.Divisions[1].Assignments[0]
	if as.CommissionID != 0 || as.CommissionName != "Commission" || len(as.Members) != 2 || len(f.attempt().Members) != 2 {
		t.Fatal("deleted copied commission", as)
	}
	f.consensus(t, 1)
	q := currentQuestion(f.attempt())
	if q.ConsensusAt == nil || q.Rounds[0].CompletedAt == nil || !q.ConsensusAt.Equal(f.p.Now) || q.Rounds[0].VoteTimes[21] != f.p.Now || q.Rounds[0].VoteTimes[22] != f.p.Now {
		t.Fatal("missing consensus timestamp")
	}
	f.next(t)
	f.consensus(t, 1)
	if f.attempt().Status != "completed" {
		t.Fatal("deleted commission blocked attempt")
	}
	f.fail(t, f.admin, "commission-create", fmt.Sprintf(`{"name":%q,"examinator_ids":[21]}`, strings.Repeat("a", 121)), "invalid_commission_name")
	f.cmd(t, f.admin, "commission-create", map[string]any{"name": "Name", "examinator_ids": []int64{21}})
	f.fail(t, f.admin, "commission-create", `{"name":"name","examinator_ids":[22]}`, "commission_name_conflict")
}

func TestDomainBankPartOrderAndMovedQuestion(t *testing.T) {
	f := fixtureDomain(t, "round_up")
	a, b := f.e.Parts[0].ID, f.e.Parts[1].ID
	f.cmd(t, f.admin, "question-create", map[string]any{"part_id": a, "question_text": "Late A", "answer_text": "Late A answer"})
	rows := queryDomain(t, f, f.admin, "questions").(map[string]any)["items"].([]any)
	for i, row := range rows {
		part := row.(Question).PartID
		if i < 5 && part != a || i >= 5 && part != b {
			t.Fatal("questions not grouped in part order", i, part)
		}
	}
	q := f.e.Questions[0]
	f.p.QuestionID = q.ID
	f.cmd(t, f.admin, "question-update", map[string]any{"expected_revision": q.Revision, "part_id": b})
	f.p.Query = map[string]string{"part_id": fmt.Sprint(b)}
	rows = queryDomain(t, f, f.admin, "questions").(map[string]any)["items"].([]any)
	if len(rows) != 5 || rows[4].(Question).ID != q.ID {
		t.Fatal("moved question was not appended to target part", rows)
	}
}

func TestDomainConfigDateEditPersistsAndSnapshotsStayFrozen(t *testing.T) {
	f := fixtureDomain(t, "round_up")
	f.start(t)
	oldStart := *f.attempt().Config.StartAt
	oldEnd := *f.attempt().Config.EndAt
	nextStart, nextEnd := oldStart.Add(time.Hour), oldEnd.Add(time.Hour)
	revision := f.e.Divisions[1].Config.Revision
	r := f.cmd(t, f.admin, "config", map[string]any{"expected_revision": revision, "start_at": nextStart, "end_at": nextEnd})
	if r["changed"] != true || r["rating_changed"] != true || f.e.Divisions[1].Config.Revision != revision+1 || !f.e.Divisions[1].Config.StartAt.Equal(nextStart) {
		t.Fatal("date-only change not persisted", r)
	}
	if !f.attempt().Config.StartAt.Equal(oldStart) || !f.attempt().Config.EndAt.Equal(oldEnd) {
		t.Fatal("started window snapshot mutated")
	}
	r = f.cmd(t, f.admin, "config", map[string]any{"expected_revision": revision + 1, "start_at": nextStart, "end_at": nextEnd})
	if r["changed"] != false || r["rating_changed"] != false {
		t.Fatal("identical dates changed state", r)
	}
}
