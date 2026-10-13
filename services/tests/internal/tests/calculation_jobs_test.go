package tests

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestJobIdentityPinsRawAnswersAndRevision(t *testing.T) {
	submission, attempt, version := bson.NewObjectID(), bson.NewObjectID(), bson.NewObjectID()
	a := Attempt{ID: attempt, SubmissionID: &submission}
	job := CalculationJob{SubmissionID: submission, AttemptID: attempt, OrganizationID: 1, DivisionID: 2, AccountID: 3, TestID: bson.NewObjectID(), Revision: 1, Operation: "initial_grade", AttemptVersionID: version, GradingVersionID: version, Questions: []Question{{QuestionID: 1, Type: "text", Points: 3, CorrectAnswers: []string{"yes"}}}, Answers: []SubmittedAnswer{{QuestionID: 1, Value: "yes"}}, QuestionOrder: []int64{1}, ExcludedQuestionIDs: []int64{}}
	first := jobInputHash(job)
	id := jobIdentity(a, version, 1, first)
	job.EventID = "redelivered"
	job.CreatedAt = time.Now()
	job.Output = &GradeResult{Score: 100}
	if jobInputHash(job) != first || jobIdentity(a, version, 1, first) != id {
		t.Fatal("delivery/output metadata changed immutable input identity")
	}
	job.Answers = []SubmittedAnswer{{QuestionID: 1, Value: "no"}}
	if jobInputHash(job) == first || jobIdentity(a, version, 1, jobInputHash(job)) == id {
		t.Fatal("different concurrent final snapshots share one job")
	}
	job.Answers = []SubmittedAnswer{{QuestionID: 1, Value: "yes"}}
	job.Revision = 2
	if jobInputHash(job) == first {
		t.Fatal("operator retry did not invalidate previous input")
	}
	job.Revision = 1
	job.DivisionID = 9
	if jobInputHash(job) == first {
		t.Fatal("division was omitted from grading identity")
	}
}

func TestResultValidationGuardsImmutableGradeAndEmptyScope(t *testing.T) {
	version := bson.NewObjectID()
	job := CalculationJob{GradingVersionID: version, InputHash: "hash", Questions: []Question{{QuestionID: 1, Points: 3}, {QuestionID: 2, Points: 7}}, ExcludedQuestionIDs: []int64{9}}
	valid := ResultDTO{InputHash: "hash", GradingVersionID: version, ExcludedQuestionIDs: []int64{9}, GradeResult: GradeResult{Score: 30, EarnedPoints: 3, MaxPoints: 10, Questions: []QuestionResult{{QuestionID: 1, Points: 3, IsCorrect: true}, {QuestionID: 2}}}}
	if e := validateCalculationResult(job, valid); e != nil {
		t.Fatal(e)
	}
	cases := map[string]func(*ResultDTO){
		"wrong version":      func(v *ResultDTO) { v.GradingVersionID = bson.NewObjectID() },
		"wrong input":        func(v *ResultDTO) { v.InputHash = "other" },
		"resurrected scope":  func(v *ResultDTO) { v.ExcludedQuestionIDs = []int64{} },
		"partial score":      func(v *ResultDTO) { v.Questions[0].Points = 1; v.EarnedPoints = 1; v.Score = 10 },
		"wrong percentage":   func(v *ResultDTO) { v.Score = 31 },
		"duplicate question": func(v *ResultDTO) { v.Questions[1] = v.Questions[0] },
		"missing question":   func(v *ResultDTO) { v.Questions = v.Questions[:1] },
		"new question":       func(v *ResultDTO) { v.Questions[1].QuestionID = 9 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			v := valid
			v.Questions = append([]QuestionResult{}, valid.Questions...)
			change(&v)
			if validateCalculationResult(job, v) == nil {
				t.Fatal("accepted inconsistent callback")
			}
		})
	}
	job.Questions = []Question{}
	valid.GradeResult = GradeResult{Questions: []QuestionResult{}}
	if e := validateCalculationResult(job, valid); e != nil {
		t.Fatalf("all historical questions removed must save a zero grade: %v", e)
	}
}

func TestApplyingRegradeKeepsSessionAndPriorAudit(t *testing.T) {
	id, submission, version := bson.NewObjectID(), bson.NewObjectID(), bson.NewObjectID()
	a := Attempt{ID: id, SubmissionID: &submission, VersionID: version, DivisionID: 7, Answers: []SubmittedAnswer{{QuestionID: 1, Value: "raw"}}, QuestionOrder: []int64{1}, CalculationHistory: []CalculationAudit{{Score: 100}}, Result: &GradeResult{Score: 100}, ResultHash: "old", CalculationStatus: "ready", CalculationError: "old_error"}
	j := CalculationJob{ID: bson.NewObjectID(), GradingVersionID: bson.NewObjectID(), ExcludedQuestionIDs: []int64{1}, GradingVersionIndex: 2}
	applyJob(&a, j)
	if a.ID != id || *a.SubmissionID != submission || a.VersionID != version || a.DivisionID != 7 || a.Answers[0].Value != "raw" || len(a.QuestionOrder) != 1 || len(a.CalculationHistory) != 1 {
		t.Fatal("regrade changed raw session or removed prior score audit")
	}
	if a.Result != nil || a.ResultHash != "" || a.CalculationStatus != "queued" || a.CalculationError != "" {
		t.Fatal("regrade exposed a stale score as the current result")
	}
}
