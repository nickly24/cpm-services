package calculate

import (
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

func gradeQuestions() []Question {
	return []Question{
		{ID: 1, Type: "single", Text: "Choose", Points: 5, Answers: []Option{{ID: "a", IsCorrect: true}, {ID: "b"}, {ID: "c"}}},
		{ID: 2, Type: "multiple", Text: "Choose all", Points: 7, Answers: []Option{{ID: "a", IsCorrect: true}, {ID: "b"}, {ID: "c", IsCorrect: true}}},
		{ID: 3, Type: "text", Text: "Enter", Points: 3, CorrectAnswers: []string{"Москва", "İ", "ΟΣ"}},
	}
}

func TestOfficialGradesWholePinnedSnapshotInAttemptOrder(t *testing.T) {
	input := CalculationInput{Operation: "initial_grade", Questions: gradeQuestions(), QuestionOrder: []int64{3, 1, 2}, Answers: []PracticeAnswer{{QuestionID: 1, Value: json.RawMessage(`"a"`)}, {QuestionID: 2, Value: json.RawMessage(`["c","a"]`)}, {QuestionID: 3, Value: json.RawMessage(`"\u001c МОСКВА \u001f"`)}}}
	grade, err := GradeOfficial(input)
	if err != nil || grade.Score != 100 || grade.EarnedPoints != 15 || grade.MaxPoints != 15 || grade.Questions[0].QuestionID != 3 {
		t.Fatalf("wrong official grade: %+v %v", grade, err)
	}
	input.Answers = input.Answers[:1]
	grade, err = GradeOfficial(input)
	if err != nil || grade.Score != 33 || grade.EarnedPoints != 5 || grade.MaxPoints != 15 || grade.Questions[0].IsCorrect || grade.Questions[2].IsCorrect {
		t.Fatalf("missing answers changed denominator: %+v %v", grade, err)
	}
	input.Answers = nil
	grade, err = GradeOfficial(input)
	if err != nil || grade.Score != 0 || grade.MaxPoints != 15 {
		t.Fatal("empty finalized answer list must retain all possible points")
	}
}

func TestRegradeRetainsHistoricalRemovedOptionsButAwardsZero(t *testing.T) {
	q := gradeQuestions()[0]
	q.Answers = q.Answers[:2]
	input := CalculationInput{Operation: "regrade", Questions: []Question{q}, QuestionOrder: []int64{1, 2, 3}, ExcludedQuestionIDs: []int64{2, 3}, Answers: []PracticeAnswer{{QuestionID: 1, Value: json.RawMessage(`"c"`)}, {QuestionID: 2, Value: json.RawMessage(`["a","c"]`)}, {QuestionID: 3, Value: json.RawMessage(`"Москва"`)}}}
	grade, err := GradeOfficial(input)
	if err != nil || grade.Score != 0 || grade.MaxPoints != 5 || len(grade.Questions) != 1 {
		t.Fatalf("removed option incorrectly rejected historical snapshot: %+v %v", grade, err)
	}
	input.Operation = "initial_grade"
	if _, err = GradeOfficial(input); !errors.Is(err, ErrInvalidGradeInput) {
		t.Fatal("initial grade admitted unknown submitted options/questions")
	}
	input.Operation = "regrade"
	input.Questions = nil
	input.ExcludedQuestionIDs = []int64{1, 2, 3}
	grade, err = GradeOfficial(input)
	if err != nil || grade.Score != 0 || grade.MaxPoints != 0 || grade.EarnedPoints != 0 || len(grade.Questions) != 0 {
		t.Fatalf("all excluded questions must become zero, not a failed job: %+v %v", grade, err)
	}
	q = gradeQuestions()[1]
	q.Answers = q.Answers[:2]
	input = CalculationInput{Operation: "regrade", Questions: []Question{q}, QuestionOrder: []int64{2}, Answers: []PracticeAnswer{{QuestionID: 2, Value: json.RawMessage(`["c","a"]`)}}}
	grade, err = GradeOfficial(input)
	if err != nil || grade.Score != 0 || grade.MaxPoints != 7 {
		t.Fatalf("removed multiple option: %+v %v", grade, err)
	}
	input.Answers[0].Value = json.RawMessage(`["c","c"]`)
	if _, err = GradeOfficial(input); !errors.Is(err, ErrInvalidGradeInput) {
		t.Fatal("removed-option tolerance must not admit malformed duplicate input")
	}
}

func TestOfficialRejectsCorruptImmutableJobs(t *testing.T) {
	base := CalculationInput{Operation: "initial_grade", Questions: gradeQuestions(), QuestionOrder: []int64{1, 2, 3}}
	tests := map[string]CalculationInput{}
	duplicateQ := base
	duplicateQ.Questions = []Question{base.Questions[0], base.Questions[0]}
	tests["duplicate question"] = duplicateQ
	duplicateOrder := base
	duplicateOrder.QuestionOrder = []int64{1, 1, 2, 3}
	tests["duplicate order"] = duplicateOrder
	missingOrder := base
	missingOrder.QuestionOrder = []int64{1, 2}
	tests["new question outside attempt order"] = missingOrder
	excluded := base
	excluded.ExcludedQuestionIDs = []int64{1}
	tests["excluded question resurrected"] = excluded
	duplicateAnswer := base
	duplicateAnswer.Answers = []PracticeAnswer{{QuestionID: 1, Value: json.RawMessage(`"a"`)}, {QuestionID: 1, Value: json.RawMessage(`"a"`)}}
	tests["duplicate answer"] = duplicateAnswer
	wrongAnswer := base
	wrongAnswer.Answers = []PracticeAnswer{{QuestionID: 3, Value: json.RawMessage(`["Москва"]`)}}
	tests["wrong type"] = wrongAnswer
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := GradeOfficial(input); !errors.Is(err, ErrInvalidGradeInput) {
				t.Fatal("corrupt job accepted")
			}
		})
	}
}

func cloneQuestions(qs []Question) []Question {
	var copy []Question
	raw, _ := json.Marshal(qs)
	_ = json.Unmarshal(raw, &copy)
	return copy
}

func TestRecalculationPolicyMatchesSupportedTriggersAndNonTriggers(t *testing.T) {
	base := gradeQuestions()
	cases := []struct {
		name     string
		edit     func([]Question) []Question
		reasons  []string
		excluded []int64
	}{
		{"question removed", func(q []Question) []Question { return q[1:] }, []string{"question_removed"}, nil},
		{"type change excludes and ignores other changes", func(q []Question) []Question {
			q[0].Type = "text"
			q[0].Points = 100
			q[0].Answers = nil
			q[0].CorrectAnswers = []string{"a"}
			return q
		}, []string{"question_type_changed"}, []int64{1}},
		{"weight", func(q []Question) []Question { q[0].Points++; return q }, []string{"points_changed"}, nil},
		{"correct flags", func(q []Question) []Question {
			q[0].Answers[0].IsCorrect = false
			q[0].Answers[1].IsCorrect = true
			return q
		}, []string{"correct_flag_changed"}, nil},
		{"remove wrong option", func(q []Question) []Question { q[0].Answers = q[0].Answers[:2]; return q }, []string{"option_removed"}, nil},
		{"remove alias", func(q []Question) []Question { q[2].CorrectAnswers = q[2].CorrectAnswers[:2]; return q }, []string{"text_correct_removed"}, nil},
		{"case only alias edit still counts replacement", func(q []Question) []Question { q[2].CorrectAnswers[0] = "МОСКВА"; return q }, []string{"text_correct_removed", "text_correct_added"}, nil},
		{"replace alias", func(q []Question) []Question { q[2].CorrectAnswers = []string{"Питер"}; return q }, []string{"text_correct_removed", "text_correct_added"}, nil},
		{"new question", func(q []Question) []Question { next := q[0]; next.ID = 4; return append(q, next) }, nil, nil},
		{"new correct option", func(q []Question) []Question {
			q[1].Answers = append(q[1].Answers, Option{ID: "d", IsCorrect: true})
			return q
		}, nil, nil},
		{"new alias", func(q []Question) []Question { q[2].CorrectAnswers = append(q[2].CorrectAnswers, "Moscow"); return q }, []string{"text_correct_added"}, nil},
		{"alias reorder", func(q []Question) []Question {
			q[2].CorrectAnswers[0], q[2].CorrectAnswers[1] = q[2].CorrectAnswers[1], q[2].CorrectAnswers[0]
			return q
		}, nil, nil},
		{"question and option text", func(q []Question) []Question { q[0].Text = "Different"; q[0].Answers[0].Text = "Different"; return q }, nil, nil},
		{"reorder", func(q []Question) []Question { return []Question{q[2], q[0], q[1]} }, nil, nil},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			decision := DecideRecalc(base, test.edit(cloneQuestions(base)))
			if decision.NeedsRecalc != (len(test.reasons) > 0) || !reflect.DeepEqual(decision.Reasons, append([]string{}, test.reasons...)) || !reflect.DeepEqual(decision.ExcludedQuestionIDs, append([]int64{}, test.excluded...)) {
				t.Fatalf("policy mismatch: %+v", decision)
			}
		})
	}
	changed := cloneQuestions(base)
	changed = changed[:2]
	changed[0].Points++
	changed[1].Points++
	decision := DecideRecalc(base, changed)
	if !reflect.DeepEqual(decision.Reasons, []string{"question_removed", "points_changed"}) {
		t.Fatalf("reason ordering/dedup changed: %+v", decision)
	}
}

type processingMemory struct {
	checkpoint      json.RawMessage
	cursor          string
	checkpointCalls int
}

func (m *processingMemory) Claim(context.Context, time.Time, string) (Message, error) {
	return Message{}, errors.New("unused")
}
func (m *processingMemory) Checkpoint(_ context.Context, _ Message, cursor string, output json.RawMessage) error {
	m.checkpoint = append(json.RawMessage(nil), output...)
	m.cursor = cursor
	m.checkpointCalls++
	return nil
}
func (m *processingMemory) Finish(context.Context, Message, string, string, json.RawMessage) error {
	return nil
}
func (m *processingMemory) Retry(context.Context, Message, string, time.Time) error { return nil }

func workerMessage(t *testing.T) Message {
	t.Helper()
	body := []byte(`{"message_id":"bd095746-6c38-4b77-bc40-c6d0e62d1b2d","message_type":"tests.grade.requested","schema_version":1,"occurred_at":"2026-10-07T12:00:00Z","producer":"tests-service","correlation_id":"workflow","payload":{"submission_id":"507f1f77bcf86cd799439011","test_id":"507f1f77bcf86cd799439012","organization_id":1,"division_id":2,"account_id":3,"calculation_revision":1,"operation":"initial_grade","grading_test_version_id":"507f1f77bcf86cd799439013"}}`)
	m, err := ParseMessage(body, "tests.grade.requested.v1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	m.LeaseToken = "lease"
	return m
}

func workerInput() CalculationInput {
	return CalculationInput{SubmissionID: "507f1f77bcf86cd799439011", TestID: "507f1f77bcf86cd799439012", OrganizationID: 1, DivisionID: 2, AccountID: 3, CalculationRevision: 1, GradingVersionID: "507f1f77bcf86cd799439013", InputHash: strings.Repeat("a", 64), Operation: "initial_grade", Questions: gradeQuestions(), QuestionOrder: []int64{3, 1, 2}, Answers: []PracticeAnswer{{QuestionID: 1, Value: json.RawMessage(`"a"`)}}}
}

func TestWorkerDurablyCachesComputedResultBeforeCallbackAndReplaysAfterLostResponse(t *testing.T) {
	store := &processingMemory{}
	getCalls, postCalls := 0, 0
	key := strings.Repeat("k", 48)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Key") != key {
			t.Error("service key not sent")
		}
		if strings.HasSuffix(r.URL.Path, "/calculation-status") {
			reply(w, 200, map[string]bool{"ok": true})
			return
		}
		if r.Method == "GET" {
			getCalls++
			if r.URL.Query().Get("calculation_revision") != "1" {
				t.Error("missing revision selector")
			}
			reply(w, 200, workerInput())
			return
		}
		postCalls++
		if len(store.checkpoint) == 0 {
			t.Error("result was not persisted before external callback")
		}
		var result CalculationResult
		if json.NewDecoder(r.Body).Decode(&result) != nil || result.Score != 33 || result.InputHash != strings.Repeat("a", 64) {
			t.Error("wrong callback")
		}
		if postCalls == 1 {
			fail(w, 503, "temporary")
			return
		}
		reply(w, 200, map[string]bool{"ready": true})
	}))
	defer server.Close()
	worker := &Worker{Store: store, Tests: NewTestsClient(server.URL, key)}
	m := workerMessage(t)
	_, _, output, err := worker.Process(context.Background(), m)
	if err == nil || len(output) == 0 || store.checkpointCalls != 1 {
		t.Fatal("temporary callback error lost computed result")
	}
	m.Output = append(json.RawMessage(nil), store.checkpoint...)
	status, _, output, err := worker.Process(context.Background(), m)
	if err != nil || status != "ready" || getCalls != 1 || postCalls != 2 || store.checkpointCalls != 1 || !reflect.DeepEqual(output, store.checkpoint) {
		t.Fatalf("retry didn't replay immutable callback: status=%s calls=%d/%d error=%v", status, getCalls, postCalls, err)
	}
}

func TestWorkerRejectsForgedScopeBeforeWritingResult(t *testing.T) {
	postCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			postCalls++
		}
		input := workerInput()
		input.DivisionID = 99
		reply(w, 200, input)
	}))
	defer server.Close()
	worker := &Worker{Store: &processingMemory{}, Tests: NewTestsClient(server.URL, strings.Repeat("k", 48))}
	_, _, _, err := worker.Process(context.Background(), workerMessage(t))
	if !errors.Is(err, ErrInvalidGradeInput) || postCalls != 0 {
		t.Fatal("forged envelope scope reached result callback")
	}
}

func TestWorkerErrorsDistinguishStaleAndTransientFromInvalidJobs(t *testing.T) {
	for _, test := range []struct {
		err    error
		status string
	}{
		{&remoteError{404, "missing"}, "skipped"},
		{&remoteError{409, "stale_calculation_revision"}, "skipped"},
		{&remoteError{409, "result_idempotency_conflict"}, "failed"},
		{&remoteError{409, "recalculation_busy"}, "retrying"},
		{&remoteError{409, "calculation_busy"}, "retrying"},
		{&remoteError{409, "attempt_changed"}, "retrying"},
		{&remoteError{400, "invalid"}, "failed"},
		{&remoteError{503, "down"}, "retrying"},
		{ErrInvalidGradeInput, "failed"},
		{errors.New("socket closed"), "retrying"},
		{errMorePages, "retrying"},
	} {
		status, _ := classifyProcessError(test.err)
		if status != test.status {
			t.Fatalf("%v classified %s", test.err, status)
		}
	}
	if retryCeiling(1) != time.Second || retryCeiling(10) != 32*time.Second {
		t.Fatal("unbounded retry delay")
	}
	for _, attempt := range []int64{0, 1, 3, 6, 10} {
		ceiling := retryCeiling(attempt)
		for range 32 {
			if delay := retryDelay(attempt); delay < ceiling/2 || delay > ceiling {
				t.Fatal("retry jitter exceeded its bounded interval")
			}
		}
	}
}

func TestWorkerLeavesPreviouslyReadyBacklogResultUntouched(t *testing.T) {
	postCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			postCalls++
		}
		input := workerInput()
		input.CalculationStatus = "ready"
		reply(w, 200, input)
	}))
	defer server.Close()
	worker := &Worker{Store: &processingMemory{}, Tests: NewTestsClient(server.URL, strings.Repeat("k", 48))}
	status, reason, _, err := worker.Process(context.Background(), workerMessage(t))
	if err != nil || status != "ready" || reason != "already_ready" || postCalls != 0 {
		t.Fatalf("existing result was rewritten: %s %s %v", status, reason, err)
	}
}

func TestContentWorkerCheckpointsPaginationAndPreservesPolicy(t *testing.T) {
	before := gradeQuestions()
	after := cloneQuestions(before)
	after[0].Points++
	body, _ := json.Marshal(Envelope{MessageID: "bd095746-6c38-4b77-bc40-c6d0e62d1b2d", MessageType: "tests.content.changed", SchemaVersion: 1, OccurredAt: time.Now().UTC(), Producer: "tests-service", CorrelationID: "edit", Payload: mustJSON(map[string]any{"test_id": "507f1f77bcf86cd799439012", "organization_id": 1, "before_version_id": "507f1f77bcf86cd799439013", "after_version_id": "507f1f77bcf86cd799439014", "change_batch": map[string]any{"before": map[string]any{"questions": before}, "after": map[string]any{"questions": after}}})})
	m, err := ParseMessage(body, "tests.content.changed.v1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	store := &processingMemory{}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/internal/tests/recalculations/prepare" {
			t.Error("wrong prepare endpoint")
		}
		var input PrepareRecalculation
		if json.NewDecoder(r.Body).Decode(&input) != nil || !input.NeedsRecalc || !reflect.DeepEqual(input.Reasons, []string{"points_changed"}) {
			t.Error("policy decision not supplied")
		}
		if calls == 1 {
			reply(w, 200, PreparedRecalculation{Status: "queued", ProcessedCount: 50, NextCursor: "507f1f77bcf86cd799439015"})
			return
		}
		if input.Cursor != store.cursor {
			t.Error("cursor was not durably resumed")
		}
		reply(w, 200, PreparedRecalculation{Status: "queued", ProcessedCount: 1})
	}))
	defer server.Close()
	worker := &Worker{Store: store, Tests: NewTestsClient(server.URL, strings.Repeat("k", 48))}
	_, _, _, err = worker.Process(context.Background(), m)
	if !errors.Is(err, errMorePages) || store.cursor != "507f1f77bcf86cd799439015" {
		t.Fatal("page progress not checkpointed")
	}
	m.ProcessCursor = store.cursor
	status, _, _, err := worker.Process(context.Background(), m)
	if err != nil || status != "ready" || calls != 2 {
		t.Fatalf("batch wasn't resumed: %s %v", status, err)
	}
}

func mustJSON(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }
