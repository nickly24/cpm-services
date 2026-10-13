package tests

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"net/http"
	"sort"
	"time"
)

const AttemptsCollection = "tests_attempts_v1"

type SubmittedAnswer struct {
	QuestionID int64 `json:"question_id" bson:"question_id"`
	Value      any   `json:"value" bson:"value"`
}
type Commit struct {
	ID         string    `json:"commit_id" bson:"commit_id"`
	Sequence   int64     `json:"sequence" bson:"sequence"`
	QuestionID int64     `json:"question_id" bson:"question_id"`
	Value      any       `json:"value" bson:"value"`
	AnsweredAt time.Time `json:"answered_at" bson:"answered_at"`
}
type Attempt struct {
	ID                  bson.ObjectID      `json:"id" bson:"_id"`
	OrganizationID      int64              `json:"organization_id" bson:"organization_id"`
	DivisionID          int64              `json:"division_id" bson:"division_id"`
	AccountID           int64              `json:"account_id" bson:"account_id"`
	TestID              bson.ObjectID      `json:"test_id" bson:"test_id"`
	VersionID           bson.ObjectID      `json:"test_version_id" bson:"test_version_id"`
	SettingsSnapshot    Settings           `json:"settings_snapshot" bson:"settings_snapshot"`
	Practice            bool               `json:"practice" bson:"practice"`
	Active              bool               `json:"-" bson:"active"`
	Revision            int64              `json:"revision" bson:"revision"`
	Status              string             `json:"status" bson:"status"`
	StartedAt           time.Time          `json:"started_at" bson:"started_at"`
	AnswerDeadline      time.Time          `json:"answer_deadline" bson:"answer_deadline"`
	UploadDeadline      time.Time          `json:"upload_deadline" bson:"upload_deadline"`
	QuestionOrder       []int64            `json:"question_order" bson:"question_order"`
	Answers             []SubmittedAnswer  `json:"answers" bson:"answers"`
	Commits             []Commit           `json:"commits" bson:"commits"`
	SubmissionID        *bson.ObjectID     `json:"submission_id" bson:"submission_id"`
	AcceptedAt          *time.Time         `json:"accepted_at" bson:"accepted_at"`
	CompletedAt         *time.Time         `json:"completed_at" bson:"completed_at"`
	Reason              string             `json:"reason" bson:"reason"`
	SnapshotHash        string             `json:"-" bson:"snapshot_hash"`
	CalculationRevision int64              `json:"calculation_revision" bson:"calculation_revision"`
	CalculationStatus   string             `json:"calculation_status" bson:"calculation_status"`
	Result              *GradeResult       `json:"result" bson:"result"`
	ResultHash          string             `json:"-" bson:"result_hash"`
	ResetBy             int64              `json:"-" bson:"reset_by"`
	PracticeResults     []QuestionResult   `json:"-" bson:"practice_results"`
	CalculationJobID    *bson.ObjectID     `json:"calculation_job_id" bson:"calculation_job_id"`
	CalculationJobIDs   []bson.ObjectID    `json:"-" bson:"calculation_job_ids"`
	GradingVersionID    bson.ObjectID      `json:"grading_test_version_id" bson:"grading_test_version_id"`
	GradingVersionIndex int64              `json:"-" bson:"grading_version_index"`
	ExcludedQuestionIDs []int64            `json:"excluded_question_ids" bson:"excluded_question_ids"`
	CalculationHistory  []CalculationAudit `json:"-" bson:"calculation_history"`
	CalculatedAt        *time.Time         `json:"calculated_at" bson:"calculated_at"`
	CalculationError    string             `json:"calculation_error,omitempty" bson:"calculation_error"`
}
type QuestionResult struct {
	QuestionID int64 `json:"question_id" bson:"question_id"`
	Points     int   `json:"points" bson:"points"`
	IsCorrect  bool  `json:"is_correct" bson:"is_correct"`
}
type GradeResult struct {
	Score        int              `json:"score" bson:"score"`
	EarnedPoints int              `json:"earned_points" bson:"earned_points"`
	MaxPoints    int              `json:"max_points" bson:"max_points"`
	Questions    []QuestionResult `json:"question_results" bson:"question_results"`
}
type StartDTO struct {
	Practice bool `json:"practice"`
}
type AnswerDTO = SubmittedAnswer
type CommitDTO struct {
	Commits []Commit `json:"commits"`
}
type FinalizeDTO struct {
	VersionID    bson.ObjectID     `json:"test_version_id"`
	CompletedAt  time.Time         `json:"completed_at"`
	Reason       string            `json:"reason"`
	Answers      []SubmittedAnswer `json:"answers"`
	SnapshotHash string            `json:"snapshot_hash,omitempty"`
	ClientHash   string            `json:"-"`
}
type AttemptView struct {
	Attempt
	Questions  []StudentQuestion `json:"questions"`
	ServerTime time.Time         `json:"server_time"`
}
type SubmissionView struct {
	AttemptID           bson.ObjectID  `json:"attempt_id"`
	SubmissionID        *bson.ObjectID `json:"submission_id"`
	OrganizationID      int64          `json:"organization_id"`
	DivisionID          int64          `json:"division_id"`
	AccountID           int64          `json:"account_id"`
	TestID              bson.ObjectID  `json:"test_id"`
	VersionID           bson.ObjectID  `json:"test_version_id"`
	SubmissionStatus    string         `json:"submission_status"`
	CalculationStatus   string         `json:"calculation_status"`
	CalculationRevision int64          `json:"calculation_revision"`
	AcceptedAt          *time.Time     `json:"accepted_at"`
	Result              *GradeResult   `json:"result"`
	StatusURL           string         `json:"status_url"`
	Revision            int64          `json:"revision"`
	GradingVersionID    bson.ObjectID  `json:"grading_test_version_id"`
	CalculatedAt        *time.Time     `json:"calculated_at"`
	CalculationError    string         `json:"calculation_error,omitempty"`
}

func submissionView(a Attempt) SubmissionView {
	if a.Result != nil {
		result := *a.Result
		result.Questions = nil
		a.Result = &result
	}
	url := ""
	if a.SubmissionID != nil {
		url = "/v1/tests/submissions/" + a.SubmissionID.Hex()
	}
	return SubmissionView{a.ID, a.SubmissionID, a.OrganizationID, a.DivisionID, a.AccountID, a.TestID, a.VersionID, a.Status, a.CalculationStatus, a.CalculationRevision, a.AcceptedAt, a.Result, url, a.Revision, a.GradingVersionID, a.CalculatedAt, a.CalculationError}
}
func (s *Store) Attempts() *mongo.Collection {
	return s.Collection.Database().Collection(AttemptsCollection)
}
func (s *Store) EnsureAttemptIndexes(ctx context.Context) error {
	_, e := s.Attempts().Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "division_id", Value: 1}, {Key: "account_id", Value: 1}, {Key: "test_id", Value: 1}, {Key: "practice", Value: 1}}, Options: options.Index().SetName("active_attempt_scope").SetUnique(true).SetPartialFilterExpression(bson.M{"active": true})},
		{Keys: bson.D{{Key: "submission_id", Value: 1}}, Options: options.Index().SetName("submission_identity").SetUnique(true).SetPartialFilterExpression(bson.M{"submission_id": bson.M{"$type": "objectId"}})},
		{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "division_id", Value: 1}, {Key: "accepted_at", Value: -1}, {Key: "_id", Value: -1}}, Options: options.Index().SetName("division_submissions")},
		{Keys: bson.D{{Key: "calculation_status", Value: 1}, {Key: "accepted_at", Value: 1}, {Key: "_id", Value: 1}}, Options: options.Index().SetName("attempts_monitoring_v1").SetPartialFilterExpression(bson.M{"active": true, "status": "accepted"})},
	})
	if e != nil {
		return e
	}
	return s.EnsureOutboxIndexes(ctx)
}
func (s *Store) attempt(ctx context.Context, org int64, id bson.ObjectID) (Attempt, error) {
	var a Attempt
	e := s.Attempts().FindOne(ctx, bson.M{"_id": id, "organization_id": org}).Decode(&a)
	if errors.Is(e, mongo.ErrNoDocuments) {
		e = missing
	}
	return a, e
}
func (s *Store) saveAttempt(ctx context.Context, a Attempt, event *Event) (Attempt, error) {
	revision := a.Revision
	a.Revision++
	fields, e := documentFields(a)
	if e != nil {
		return a, e
	}
	update := bson.M{"$set": fields}
	if event != nil {
		pending, err := storeEvent(*event)
		if err != nil {
			return a, err
		}
		enqueueOutbox(update, pending)
	}
	result, e := s.Attempts().UpdateOne(ctx, bson.M{"_id": a.ID, "organization_id": a.OrganizationID, "revision": revision, "active": true}, update)
	if e == nil && result.MatchedCount != 1 {
		e = conflict("attempt_changed")
	}
	return a, e
}
func actorResults(a Actor, edit bool) bool {
	if a.Role == "admin" && a.OrganizationAdmin {
		return true
	}
	p, canonical := a.Permissions["test-results"]
	if !canonical {
		p = a.Permissions["test_results"]
	}
	return a.Role == "staff_admin" && p.View && (!edit || p.Edit)
}
func (s *Server) loadAttempt(r *http.Request, actor Actor) (Attempt, error) {
	id, e := bson.ObjectIDFromHex(r.PathValue("attempt"))
	if e != nil {
		return Attempt{}, bad("invalid_id")
	}
	a, e := s.Store.attempt(r.Context(), actor.OrganizationID, id)
	if e != nil {
		return a, e
	}
	if e = gatewaySelectedDivision(r, actor, a.DivisionID); e != nil {
		return a, e
	}
	if !actor.InDivision(a.DivisionID) || (a.AccountID != actor.AccountID && !actorResults(actor, false)) {
		return a, denied
	}
	if !a.Active && a.Status != "practice_completed" {
		return a, conflict("attempt_reset")
	}
	if e = s.scope(r, actor, a.DivisionID); e != nil {
		return a, e
	}
	return a, nil
}
func (s *Server) attemptView(r *http.Request, a Attempt) (AttemptView, error) {
	qs, e := s.Store.LoadSafeQuestions(r.Context(), a.OrganizationID, a.VersionID)
	if e != nil {
		return AttemptView{}, e
	}
	ordered := make([]StudentQuestion, 0, len(qs))
	for _, id := range a.QuestionOrder {
		for _, q := range qs {
			if q.QuestionID == id {
				ordered = append(ordered, q)
				break
			}
		}
	}
	if a.Answers == nil {
		a.Answers = []SubmittedAnswer{}
	}
	if a.Commits == nil {
		a.Commits = []Commit{}
	}
	if a.Result != nil {
		result := *a.Result
		result.Questions = nil
		a.Result = &result
	}
	view := AttemptView{a, ordered, time.Now().UTC()}
	if a.Status == "in_progress" && !a.Practice && time.Now().After(a.AnswerDeadline) {
		view.Status = "expired_pending_upload"
	}
	return view, nil
}
func attemptFilter(actor Actor, d int64, test bson.ObjectID, practice bool) bson.M {
	return bson.M{"organization_id": actor.OrganizationID, "division_id": d, "account_id": actor.AccountID, "test_id": test, "practice": practice, "active": true}
}
func (s *Server) startAttempt(r *http.Request, actor Actor, division int64) (any, int, error) {
	if actor.Role != "student" {
		return nil, 0, denied
	}
	var input StartDTO
	if e := decode(r, &input); e != nil {
		return nil, 0, e
	}
	test, e := s.load(r, actor)
	if e != nil {
		return nil, 0, e
	}
	placement := test.Placement(division)
	if placement == nil {
		return nil, 0, missing
	}
	refs, e := s.Dependencies.References(r.Context(), actor.OrganizationID, []int64{division}, &test.DirectionID)
	if e != nil {
		return nil, 0, e
	}
	if test.Archived || !refs.Direction.Active || !placement.Settings.Published {
		return nil, 0, conflict("test_unavailable")
	}
	if input.Practice {
		allowed, err := s.practiceEligible(r, actor.AccountID, actor.OrganizationID, division, test, *placement, refs.Direction.Active)
		if err != nil {
			return nil, 0, err
		}
		if !allowed {
			return nil, 0, conflict("practice_unavailable")
		}
	}
	var existing Attempt
	e = s.Store.Attempts().FindOne(r.Context(), attemptFilter(actor, division, test.ID, input.Practice)).Decode(&existing)
	if e == nil {
		if !input.Practice && existing.Status == "accepted" {
			return nil, 0, conflict("already_submitted")
		}
		v, e := s.attemptView(r, existing)
		return v, 200, e
	}
	if !errors.Is(e, mongo.ErrNoDocuments) {
		return nil, 0, e
	}
	access := availability(test, *placement, refs.Direction.Active, time.Now())
	if !input.Practice && !access.OfficialAccessAllowed {
		return nil, 0, conflict(access.OfficialReason)
	}
	if len(test.Questions) == 0 || test.ContentVersionID.IsZero() {
		return nil, 0, conflict("test_has_no_questions")
	}
	version, e := s.Store.LoadContent(r.Context(), actor.OrganizationID, test.ContentVersionID)
	if e != nil {
		return nil, 0, e
	}
	order := make([]int64, len(version.Questions))
	for i, q := range version.Questions {
		order[i] = q.QuestionID
	}
	shuffleQuestions(order)
	now := time.Now().UTC().Truncate(time.Millisecond)
	deadline := now.Add(time.Duration(placement.Settings.TimeLimitMinutes) * time.Minute)
	if input.Practice {
		deadline = now.Add(365 * 24 * time.Hour)
	}
	a := Attempt{ID: bson.NewObjectID(), OrganizationID: actor.OrganizationID, DivisionID: division, AccountID: actor.AccountID, TestID: test.ID, VersionID: version.ID, Practice: input.Practice, Active: true, Revision: 1, Status: "in_progress", StartedAt: now, AnswerDeadline: deadline, UploadDeadline: deadline.Add(24 * time.Hour), QuestionOrder: order, Answers: []SubmittedAnswer{}, Commits: []Commit{}, CalculationStatus: "not_submitted"}
	a.SettingsSnapshot = placement.Settings
	_, e = s.Store.Attempts().InsertOne(r.Context(), a)
	if mongo.IsDuplicateKeyError(e) {
		e = s.Store.Attempts().FindOne(r.Context(), attemptFilter(actor, division, test.ID, input.Practice)).Decode(&a)
	}
	if e != nil {
		return nil, 0, e
	}
	view, e := s.attemptView(r, a)
	return view, 201, e
}
func (s *Server) getAttempt(r *http.Request, actor Actor, _ int64) (any, int, error) {
	a, e := s.loadAttempt(r, actor)
	if e != nil {
		return nil, 0, e
	}
	v, e := s.attemptView(r, a)
	return v, 200, e
}
func normalizeAnswer(q Question, value any) (any, error) {
	switch q.Type {
	case "single":
		v, ok := value.(string)
		if !ok {
			return nil, bad("invalid_single_answer")
		}
		for _, o := range q.Answers {
			if o.ID == v {
				return v, nil
			}
		}
		return nil, bad("unknown_answer_option")
	case "text":
		v, ok := value.(string)
		if !ok || len(v) > 16000 {
			return nil, bad("invalid_text_answer")
		}
		return v, nil
	case "multiple":
		var values []string
		switch v := value.(type) {
		case []string:
			values = append([]string{}, v...)
		case bson.A:
			for _, i := range v {
				s, ok := i.(string)
				if !ok {
					return nil, bad("invalid_multiple_answer")
				}
				values = append(values, s)
			}
		case []any:
			for _, i := range v {
				s, ok := i.(string)
				if !ok {
					return nil, bad("invalid_multiple_answer")
				}
				values = append(values, s)
			}
		default:
			return nil, bad("invalid_multiple_answer")
		}
		seen := map[string]bool{}
		for _, v := range values {
			if seen[v] {
				return nil, bad("duplicate_answer_option")
			}
			seen[v] = true
			found := false
			for _, o := range q.Answers {
				if o.ID == v {
					found = true
					break
				}
			}
			if !found {
				return nil, bad("unknown_answer_option")
			}
		}
		sort.Strings(values)
		if values == nil {
			values = []string{}
		}
		return values, nil
	}
	return nil, bad("invalid_question_type")
}
func normalizedAnswers(questions []Question, answers []SubmittedAnswer) ([]SubmittedAnswer, error) {
	out := make([]SubmittedAnswer, 0, len(answers))
	seen := map[int64]bool{}
	for _, a := range answers {
		if seen[a.QuestionID] {
			return nil, bad("duplicate_question")
		}
		seen[a.QuestionID] = true
		found := false
		for _, q := range questions {
			if q.QuestionID == a.QuestionID {
				value, e := normalizeAnswer(q, a.Value)
				if e != nil {
					return nil, e
				}
				out = append(out, SubmittedAnswer{a.QuestionID, value})
				found = true
				break
			}
		}
		if !found {
			return nil, bad("unknown_question")
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].QuestionID < out[j].QuestionID })
	return out, nil
}
func equalValue(a, b any) bool {
	first, _ := json.Marshal(a)
	second, _ := json.Marshal(b)
	return string(first) == string(second)
}
func putAnswer(a *Attempt, answer SubmittedAnswer) error {
	for _, old := range a.Answers {
		if old.QuestionID == answer.QuestionID {
			if equalValue(old.Value, answer.Value) {
				return nil
			}
			return conflict("answer_locked")
		}
	}
	a.Answers = append(a.Answers, answer)
	return nil
}
func (s *Server) patchAnswer(r *http.Request, actor Actor, _ int64) (any, int, error) {
	a, e := s.loadAttempt(r, actor)
	if e != nil {
		return nil, 0, e
	}
	if a.AccountID != actor.AccountID || a.Practice {
		return nil, 0, denied
	}
	var input AnswerDTO
	if e = decode(r, &input); e != nil {
		return nil, 0, e
	}
	if a.Status != "in_progress" {
		return nil, 0, conflict("attempt_closed")
	}
	if time.Now().After(a.AnswerDeadline) {
		return nil, 0, conflict("answer_deadline_passed")
	}
	version, e := s.Store.LoadContent(r.Context(), a.OrganizationID, a.VersionID)
	if e != nil {
		return nil, 0, e
	}
	answers, e := normalizedAnswers(version.Questions, []SubmittedAnswer{input})
	if e != nil {
		return nil, 0, e
	}
	if e = putAnswer(&a, answers[0]); e != nil {
		return nil, 0, e
	}
	a, e = s.Store.saveAttempt(r.Context(), a, nil)
	if e != nil {
		return nil, 0, e
	}
	v, e := s.attemptView(r, a)
	return v, 200, e
}
func (s *Server) commitAnswers(r *http.Request, actor Actor, _ int64) (any, int, error) {
	var input CommitDTO
	if e := decode(r, &input); e != nil {
		return nil, 0, e
	}
	if len(input.Commits) < 1 || len(input.Commits) > 25 {
		return nil, 0, bad("invalid_commit_batch")
	}
	for retries := 0; retries < 5; retries++ {
		a, e := s.loadAttempt(r, actor)
		if e != nil {
			return nil, 0, e
		}
		if a.AccountID != actor.AccountID || a.Practice {
			return nil, 0, denied
		}
		if a.Status != "in_progress" {
			return nil, 0, conflict("attempt_closed")
		}
		if time.Now().After(a.UploadDeadline) {
			return nil, 0, conflict("upload_deadline_passed")
		}
		version, e := s.Store.LoadContent(r.Context(), a.OrganizationID, a.VersionID)
		if e != nil {
			return nil, 0, e
		}
		ack := []string{}
		changed := false
		for _, c := range input.Commits {
			if len(c.ID) < 16 || len(c.ID) > 128 || c.Sequence < 1 || c.AnsweredAt.Before(a.StartedAt.Add(-time.Millisecond)) || c.AnsweredAt.After(a.AnswerDeadline.Add(time.Millisecond)) || c.AnsweredAt.After(time.Now().Add(time.Second)) {
				return nil, 0, bad("invalid_commit")
			}
			ans, e := normalizedAnswers(version.Questions, []SubmittedAnswer{{c.QuestionID, c.Value}})
			if e != nil {
				return nil, 0, e
			}
			c.Value = ans[0].Value
			c.AnsweredAt = c.AnsweredAt.UTC().Truncate(time.Millisecond)
			duplicate := false
			for _, old := range a.Commits {
				if old.ID == c.ID {
					if !equalValue(old, c) {
						return nil, 0, conflict("commit_idempotency_conflict")
					}
					duplicate = true
					break
				}
				if old.Sequence == c.Sequence {
					return nil, 0, conflict("commit_sequence_conflict")
				}
			}
			ack = append(ack, c.ID)
			if duplicate {
				continue
			}
			if e = putAnswer(&a, ans[0]); e != nil {
				return nil, 0, e
			}
			a.Commits = append(a.Commits, c)
			changed = true
		}
		if !changed {
			return commitAcknowledgement(a, ack), 200, nil
		}
		a, e = s.Store.saveAttempt(r.Context(), a, nil)
		if e != nil {
			var api *APIError
			if errors.As(e, &api) && api.Message == "attempt_changed" {
				continue
			}
			return nil, 0, e
		}
		return commitAcknowledgement(a, ack), 200, nil
	}
	return nil, 0, conflict("attempt_changed")
}
func hashJSON(value any) string {
	body := canonicalJSON(value)
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

// Final delivery may add offline answers, but cannot change or omit a server-locked answer.
// Normalize both sides so equivalent multiple-choice sets remain retriable.
func validateLockedAnswers(questions []Question, locked, submitted []SubmittedAnswer) error {
	normalized, e := normalizedAnswers(questions, locked)
	if e != nil {
		return e
	}
	values := make(map[int64]any, len(submitted))
	for _, answer := range submitted {
		values[answer.QuestionID] = answer.Value
	}
	for _, answer := range normalized {
		value, exists := values[answer.QuestionID]
		if !exists || !equalValue(answer.Value, value) {
			return conflict("answer_locked")
		}
	}
	return nil
}
func (s *Server) finalize(r *http.Request, actor Actor, _ int64) (any, int, error) {
	var input FinalizeDTO
	if e := decode(r, &input); e != nil {
		return nil, 0, e
	}
	a, e := s.loadAttempt(r, actor)
	if e != nil {
		return nil, 0, e
	}
	if a.AccountID != actor.AccountID || a.Practice {
		return nil, 0, denied
	}
	return s.accept(r, a, input)
}
func (s *Server) accept(r *http.Request, a Attempt, input FinalizeDTO) (any, int, error) {
	for retry := 0; retry < 5; retry++ {
		data, status, e := s.acceptSnapshot(r, a, input)
		if !attemptCASConflict(e) {
			return data, status, e
		}
		a, e = s.Store.attempt(r.Context(), a.OrganizationID, a.ID)
		if e != nil {
			return nil, 0, e
		}
		if !a.Active {
			return nil, 0, conflict("attempt_closed")
		}
	}
	return nil, 0, conflict("attempt_changed")
}
func (s *Server) acceptSnapshot(r *http.Request, a Attempt, input FinalizeDTO) (any, int, error) {
	version, e := s.Store.LoadContent(r.Context(), a.OrganizationID, a.VersionID)
	if e != nil {
		return nil, 0, e
	}
	if input.VersionID != a.VersionID {
		return nil, 0, conflict("test_version_mismatch")
	}
	if input.Reason != "manual" && input.Reason != "timeout" {
		return nil, 0, bad("invalid_completion_reason")
	}
	if input.CompletedAt.Before(a.StartedAt.Add(-time.Millisecond)) || input.CompletedAt.After(a.AnswerDeadline.Add(time.Millisecond)) || input.CompletedAt.After(time.Now().Add(time.Second)) {
		return nil, 0, bad("invalid_completion_time")
	}
	input.Answers, e = normalizedAnswers(version.Questions, input.Answers)
	if e != nil {
		return nil, 0, e
	}
	input.CompletedAt = input.CompletedAt.UTC().Truncate(time.Millisecond)
	provided := input.SnapshotHash
	input.SnapshotHash = ""
	hash := hashJSON(input)
	if provided != "" && provided != input.ClientHash {
		return nil, 0, bad("snapshot_hash_mismatch")
	}
	if a.Status == "accepted" {
		if a.SnapshotHash != hash {
			return nil, 0, conflict("submission_idempotency_conflict")
		}
		return submissionView(a), 202, nil
	}
	if a.Status != "in_progress" {
		return nil, 0, conflict("attempt_closed")
	}
	if time.Now().After(a.UploadDeadline) {
		return nil, 0, conflict("upload_deadline_passed")
	}
	if e := validateLockedAnswers(version.Questions, a.Answers, input.Answers); e != nil {
		return nil, 0, e
	}
	now := time.Now().UTC()
	submissionIdentity := sha256.Sum256([]byte("tests-submission-v1:" + a.ID.Hex()))
	var id bson.ObjectID
	copy(id[:], submissionIdentity[:12])
	a.SubmissionID = &id
	a.AcceptedAt = &now
	a.CompletedAt = &input.CompletedAt
	a.Reason = input.Reason
	a.SnapshotHash = hash
	a.Answers = input.Answers
	a.Status = "accepted"
	a.CalculationRevision = 1
	a.CalculationStatus = "queued"
	a.Result = nil
	job, e := s.makeJob(r.Context(), a, a.VersionID, "initial_grade", "")
	if e != nil {
		return nil, 0, e
	}
	applyJob(&a, job)
	event := jobEvent(job)
	a, e = s.Store.saveAttempt(r.Context(), a, &event)
	if e != nil {
		var api *APIError
		if errors.As(e, &api) && api.Message == "attempt_changed" {
			fresh, err := s.Store.attempt(r.Context(), a.OrganizationID, a.ID)
			if err == nil && fresh.Status == "accepted" && fresh.SnapshotHash == hash {
				return submissionView(fresh), 202, nil
			}
			if err == nil && fresh.Active && fresh.Status == "in_progress" {
				return nil, 0, conflict("attempt_changed")
			}
			return nil, 0, conflict("submission_idempotency_conflict")
		}
		return nil, 0, e
	}
	return submissionView(a), 202, nil
}
func (s *Server) submitMirror(r *http.Request, actor Actor, _ int64) (any, int, error) {
	var empty struct{}
	if e := decode(r, &empty); e != nil {
		return nil, 0, e
	}
	a, e := s.loadAttempt(r, actor)
	if e != nil {
		return nil, 0, e
	}
	if a.AccountID != actor.AccountID {
		return nil, 0, denied
	}
	if a.Status == "accepted" {
		return submissionView(a), 202, nil
	}
	if a.Practice {
		return s.finishPractice(r, a)
	}
	if len(a.Answers) == 0 {
		return nil, 0, conflict("no_answers")
	}
	now := time.Now().UTC()
	reason := "manual"
	if now.After(a.AnswerDeadline) {
		now = a.AnswerDeadline
		reason = "timeout"
	}
	return s.accept(r, a, FinalizeDTO{VersionID: a.VersionID, CompletedAt: now, Reason: reason, Answers: a.Answers})
}
func (s *Server) forceSubmit(r *http.Request, actor Actor, _ int64) (any, int, error) {
	var empty struct{}
	if e := decode(r, &empty); e != nil {
		return nil, 0, e
	}
	a, e := s.loadAttempt(r, actor)
	if e != nil {
		return nil, 0, e
	}
	if !actorResults(actor, true) || a.Practice {
		return nil, 0, denied
	}
	if a.Status == "accepted" {
		return submissionView(a), 202, nil
	}
	now := time.Now().UTC()
	reason := "manual"
	if now.After(a.AnswerDeadline) {
		now = a.AnswerDeadline
		reason = "timeout"
	}
	return s.accept(r, a, FinalizeDTO{VersionID: a.VersionID, CompletedAt: now, Reason: reason, Answers: a.Answers})
}
func (s *Server) resetAttempt(r *http.Request, actor Actor, _ int64) (any, int, error) {
	var input RevisionDTO
	if e := decode(r, &input); e != nil {
		return nil, 0, e
	}
	a, e := s.loadAttempt(r, actor)
	if e != nil {
		return nil, 0, e
	}
	if !actorResults(actor, true) {
		return nil, 0, denied
	}
	if input.ExpectedRevision != a.Revision {
		return nil, 0, conflict("stale_revision")
	}
	a.Active = false
	a.Status = "reset"
	a.ResetBy = actor.AccountID
	a.CalculationRevision++
	a, e = s.Store.saveAttempt(r.Context(), a, nil)
	return map[string]any{"attempt_id": a.ID, "division_id": a.DivisionID, "revision": a.Revision, "status": "reset"}, 200, e
}
func parseOptionalObjectID(raw string) (bson.ObjectID, error) {
	if raw == "" {
		return bson.NilObjectID, nil
	}
	id, e := bson.ObjectIDFromHex(raw)
	if e != nil {
		return id, bad("invalid_id")
	}
	return id, nil
}
func (s *Server) attemptList(r *http.Request, actor Actor, division int64) (any, int, error) {
	limit, offset, e := pagination(r)
	if e != nil {
		return nil, 0, e
	}
	filter := bson.M{"organization_id": actor.OrganizationID, "division_id": division, "active": true}
	if !actorResults(actor, false) {
		if actor.Role != "student" {
			return nil, 0, denied
		}
		filter["account_id"] = actor.AccountID
	} else if raw := r.URL.Query().Get("account_id"); raw != "" {
		id, e := numericID(raw)
		if e != nil {
			return nil, 0, e
		}
		filter["account_id"] = id
	}
	id, e := parseOptionalObjectID(r.URL.Query().Get("test_id"))
	if e != nil {
		return nil, 0, e
	}
	if !id.IsZero() {
		filter["test_id"] = id
	}
	practice := r.URL.Query().Get("practice")
	if practice != "" && practice != "true" && practice != "false" {
		return nil, 0, bad("invalid_practice_filter")
	}
	filter["practice"] = practice == "true"
	return s.listAttempts(r, filter, limit, offset, false)
}
func (s *Server) listAttempts(r *http.Request, filter bson.M, limit, offset int64, submitted bool) (any, int, error) {
	total, e := s.Store.Attempts().CountDocuments(r.Context(), filter)
	if e != nil {
		return nil, 0, e
	}
	cursor, e := s.Store.Attempts().Find(r.Context(), filter, options.Find().SetSort(bson.D{{Key: "started_at", Value: -1}, {Key: "_id", Value: -1}}).SetSkip(offset).SetLimit(limit))
	if e != nil {
		return nil, 0, e
	}
	defer cursor.Close(r.Context())
	rows := []Attempt{}
	if e = cursor.All(r.Context(), &rows); e != nil {
		return nil, 0, e
	}
	items := []any{}
	for _, a := range rows {
		if submitted {
			items = append(items, submissionView(a))
		} else {
			v, e := s.attemptView(r, a)
			if e != nil {
				return nil, 0, e
			}
			items = append(items, v)
		}
	}
	return map[string]any{"items": items, "total": total, "limit": limit, "offset": offset}, 200, nil
}

// The list and its statistics must apply identical scope and selection filters.
// Student-provided account IDs never replace the authenticated account.
func submissionFilter(r *http.Request, actor Actor, division int64) (bson.M, error) {
	filter := bson.M{"organization_id": actor.OrganizationID, "division_id": division, "active": true, "status": "accepted", "practice": false}
	if !actorResults(actor, false) {
		if actor.Role != "student" {
			return nil, denied
		}
		filter["account_id"] = actor.AccountID
	} else if raw := r.URL.Query().Get("account_id"); raw != "" {
		id, e := numericID(raw)
		if e != nil {
			return nil, e
		}
		filter["account_id"] = id
	}
	id, e := parseOptionalObjectID(r.URL.Query().Get("test_id"))
	if e != nil {
		return nil, e
	}
	if !id.IsZero() {
		filter["test_id"] = id
	}
	if state := r.URL.Query().Get("calculation_status"); state != "" {
		if state != "queued" && state != "processing" && state != "retrying" && state != "ready" && state != "failed" {
			return nil, bad("invalid_calculation_status")
		}
		filter["calculation_status"] = state
	}
	return filter, nil
}
func (s *Server) submissionList(r *http.Request, actor Actor, division int64) (any, int, error) {
	limit, offset, e := pagination(r)
	if e != nil {
		return nil, 0, e
	}
	filter, e := submissionFilter(r, actor, division)
	if e != nil {
		return nil, 0, e
	}
	return s.listAttempts(r, filter, limit, offset, true)
}
func (s *Server) loadSubmission(r *http.Request, actor Actor) (Attempt, error) {
	id, e := bson.ObjectIDFromHex(r.PathValue("submission"))
	if e != nil {
		return Attempt{}, bad("invalid_id")
	}
	var a Attempt
	e = s.Store.Attempts().FindOne(r.Context(), bson.M{"organization_id": actor.OrganizationID, "submission_id": id, "active": true, "status": "accepted"}).Decode(&a)
	if errors.Is(e, mongo.ErrNoDocuments) {
		e = missing
	}
	if e != nil {
		return a, e
	}
	if e = gatewaySelectedDivision(r, actor, a.DivisionID); e != nil {
		return a, e
	}
	if !actor.InDivision(a.DivisionID) || (actor.AccountID != a.AccountID && !actorResults(actor, false)) {
		return a, denied
	}
	if e = s.scope(r, actor, a.DivisionID); e != nil {
		return a, e
	}
	return a, nil
}
func (s *Server) getSubmission(r *http.Request, actor Actor, _ int64) (any, int, error) {
	a, e := s.loadSubmission(r, actor)
	return submissionView(a), 200, e
}
func (s *Server) reviewSubmission(r *http.Request, actor Actor, _ int64) (any, int, error) {
	a, e := s.loadSubmission(r, actor)
	if e != nil {
		return nil, 0, e
	}
	if a.CalculationStatus != "ready" || a.Result == nil {
		return nil, 0, conflict("calculation_pending")
	}
	test, e := s.Store.Get(r.Context(), actor.OrganizationID, a.TestID)
	if e != nil {
		return nil, 0, e
	}
	p := test.Placement(a.DivisionID)
	if p == nil {
		return nil, 0, missing
	}
	if !actorResults(actor, false) {
		refs, e := s.Dependencies.References(r.Context(), actor.OrganizationID, []int64{a.DivisionID}, &test.DirectionID)
		if e != nil {
			return nil, 0, e
		}
		if !availability(test, *p, refs.Direction.Active, time.Now()).ReviewAccessAllowed {
			return nil, 0, denied
		}
	}
	version, e := s.Store.LoadContent(r.Context(), a.OrganizationID, a.VersionID)
	if e != nil {
		return nil, 0, e
	}
	questions, order := version.Questions, a.QuestionOrder
	if a.CalculationJobID != nil {
		job, err := s.Store.currentJob(r.Context(), a)
		if err != nil {
			return nil, 0, err
		}
		questions, order = job.Questions, job.QuestionOrder
	}
	return map[string]any{"submission": submissionView(a), "questions": questions, "answers": a.Answers, "question_order": order, "question_results": a.Result.Questions, "excluded_question_ids": a.ExcludedQuestionIDs}, 200, nil
}
func (s *Server) submissionStats(r *http.Request, actor Actor, division int64) (any, int, error) {
	filter, e := submissionFilter(r, actor, division)
	if e != nil {
		return nil, 0, e
	}
	cursor, e := s.Store.Attempts().Find(r.Context(), filter)
	if e != nil {
		return nil, 0, e
	}
	defer cursor.Close(r.Context())
	rows := []Attempt{}
	if e = cursor.All(r.Context(), &rows); e != nil {
		return nil, 0, e
	}
	ready, queued, failed, sum := 0, 0, 0, 0
	for _, a := range rows {
		switch a.CalculationStatus {
		case "ready":
			ready++
			sum += a.Result.Score
		case "failed":
			failed++
		default:
			queued++
		}
	}
	var average *float64
	if ready > 0 {
		v := float64(sum) / float64(ready)
		average = &v
	}
	return map[string]any{"division_id": division, "accepted": len(rows), "ready": ready, "queued": queued, "failed": failed, "average_score": average}, 200, nil
}
