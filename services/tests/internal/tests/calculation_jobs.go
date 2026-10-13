package tests

import (
	"context"
	"crypto/sha256"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"net/http"
	"sort"
	"strconv"
	"time"
)

const CalculationJobsCollection = "tests_calculation_jobs_v1"

type CalculationAudit struct {
	JobID        bson.ObjectID `json:"job_id" bson:"job_id"`
	Revision     int64         `json:"calculation_revision" bson:"calculation_revision"`
	Operation    string        `json:"operation" bson:"operation"`
	VersionID    bson.ObjectID `json:"grading_test_version_id" bson:"grading_test_version_id"`
	InputHash    string        `json:"input_hash" bson:"input_hash"`
	ResultHash   string        `json:"result_hash" bson:"result_hash"`
	Score        int           `json:"score" bson:"score"`
	EarnedPoints int           `json:"earned_points" bson:"earned_points"`
	MaxPoints    int           `json:"max_points" bson:"max_points"`
	CalculatedAt time.Time     `json:"calculated_at" bson:"calculated_at"`
}
type CalculationJob struct {
	ID                  bson.ObjectID     `json:"job_id" bson:"_id"`
	SubmissionID        bson.ObjectID     `json:"submission_id" bson:"submission_id"`
	AttemptID           bson.ObjectID     `json:"attempt_id" bson:"attempt_id"`
	OrganizationID      int64             `json:"organization_id" bson:"organization_id"`
	DivisionID          int64             `json:"division_id" bson:"division_id"`
	AccountID           int64             `json:"account_id" bson:"account_id"`
	TestID              bson.ObjectID     `json:"test_id" bson:"test_id"`
	Revision            int64             `json:"calculation_revision" bson:"calculation_revision"`
	Operation           string            `json:"operation" bson:"operation"`
	AttemptVersionID    bson.ObjectID     `json:"attempt_test_version_id" bson:"attempt_test_version_id"`
	GradingVersionID    bson.ObjectID     `json:"grading_test_version_id" bson:"grading_test_version_id"`
	GradingVersionIndex int64             `json:"-" bson:"grading_version_index"`
	Questions           []Question        `json:"questions" bson:"questions"`
	Answers             []SubmittedAnswer `json:"answers" bson:"answers"`
	QuestionOrder       []int64           `json:"question_order" bson:"question_order"`
	ExcludedQuestionIDs []int64           `json:"excluded_question_ids" bson:"excluded_question_ids"`
	InputHash           string            `json:"input_hash" bson:"input_hash"`
	EventID             string            `json:"-" bson:"event_id"`
	SourceEventID       string            `json:"source_event_id" bson:"source_event_id"`
	CreatedAt           time.Time         `json:"created_at" bson:"created_at"`
	Output              *GradeResult      `json:"result,omitempty" bson:"result,omitempty"`
	OutputHash          string            `json:"-" bson:"result_hash,omitempty"`
}

func (s *Store) Jobs() *mongo.Collection {
	return s.Collection.Database().Collection(CalculationJobsCollection)
}
func (s *Store) EnsureJobIndexes(ctx context.Context) error {
	_, e := s.Jobs().Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "submission_id", Value: 1}, {Key: "calculation_revision", Value: 1}}, Options: options.Index().SetName("submission_calculations")},
		{Keys: bson.D{{Key: "test_id", Value: 1}, {Key: "organization_id", Value: 1}, {Key: "created_at", Value: -1}}, Options: options.Index().SetName("test_calculation_history")},
	})
	return e
}
func versionIndex(doc Document, id bson.ObjectID) (int64, error) {
	for i, v := range doc.HistoryVersionIDs {
		if v == id {
			return int64(i), nil
		}
	}
	return 0, conflict("uncommitted_test_version")
}
func jobIdentity(a Attempt, target bson.ObjectID, revision int64, inputHash string) bson.ObjectID {
	body := []byte(a.ID.Hex() + ":" + target.Hex() + ":" + strconv.FormatInt(revision, 10) + ":" + inputHash)
	sum := sha256.Sum256(body)
	var id bson.ObjectID
	copy(id[:], sum[:12])
	return id
}
func jobInputHash(j CalculationJob) string {
	return hashJSON(map[string]any{"submission_id": j.SubmissionID, "attempt_id": j.AttemptID, "organization_id": j.OrganizationID, "division_id": j.DivisionID, "account_id": j.AccountID, "test_id": j.TestID, "calculation_revision": j.Revision, "operation": j.Operation, "attempt_test_version_id": j.AttemptVersionID, "grading_test_version_id": j.GradingVersionID, "questions": j.Questions, "answers": j.Answers, "question_order": j.QuestionOrder, "excluded_question_ids": j.ExcludedQuestionIDs})
}
func (s *Store) insertJob(ctx context.Context, j CalculationJob) (CalculationJob, error) {
	_, e := s.Jobs().InsertOne(ctx, j)
	if !mongo.IsDuplicateKeyError(e) {
		return j, e
	}
	var old CalculationJob
	e = s.Jobs().FindOne(ctx, bson.M{"_id": j.ID, "organization_id": j.OrganizationID}).Decode(&old)
	if e != nil {
		return old, e
	}
	if old.InputHash != j.InputHash {
		return old, conflict("calculation_input_conflict")
	}
	return old, nil
}
func (s *Store) currentJob(ctx context.Context, a Attempt) (CalculationJob, error) {
	if a.CalculationJobID == nil {
		return CalculationJob{}, conflict("calculation_not_prepared")
	}
	var j CalculationJob
	e := s.Jobs().FindOne(ctx, bson.M{"_id": *a.CalculationJobID, "submission_id": *a.SubmissionID, "organization_id": a.OrganizationID, "calculation_revision": a.CalculationRevision}).Decode(&j)
	if errors.Is(e, mongo.ErrNoDocuments) {
		e = missing
	}
	return j, e
}
func (s *Server) makeJob(ctx context.Context, a Attempt, target bson.ObjectID, operation, sourceEvent string) (CalculationJob, error) {
	doc, e := s.Store.Get(ctx, a.OrganizationID, a.TestID)
	if e != nil {
		return CalculationJob{}, e
	}
	originalIndex, e := versionIndex(doc, a.VersionID)
	if e != nil {
		return CalculationJob{}, e
	}
	targetIndex, e := versionIndex(doc, target)
	if e != nil {
		return CalculationJob{}, e
	}
	if targetIndex < originalIndex {
		return CalculationJob{}, conflict("stale_recalculation")
	}
	original, e := s.Store.LoadContent(ctx, a.OrganizationID, a.VersionID)
	if e != nil {
		return CalculationJob{}, e
	}
	grading, e := s.Store.LoadContent(ctx, a.OrganizationID, target)
	if e != nil {
		return CalculationJob{}, e
	}
	if original.TestID != a.TestID || grading.TestID != a.TestID {
		return CalculationJob{}, conflict("test_version_mismatch")
	}
	excluded := map[int64]bool{}
	for _, id := range a.ExcludedQuestionIDs {
		excluded[id] = true
	}
	if operation == "regrade" {
		// An answer cannot regain meaning after a type change, even if another edit
		// changes the type back. Derive compatibility from immutable committed history.
		previous := original
		for i := originalIndex + 1; i <= targetIndex; i++ {
			next, e := s.Store.LoadContent(ctx, a.OrganizationID, doc.HistoryVersionIDs[i])
			if e != nil {
				return CalculationJob{}, e
			}
			types := map[int64]string{}
			for _, q := range next.Questions {
				types[q.QuestionID] = q.Type
			}
			for _, q := range previous.Questions {
				if t, ok := types[q.QuestionID]; !ok || t != q.Type {
					excluded[q.QuestionID] = true
				}
			}
			previous = next
		}
	}
	sourceIDs := map[int64]bool{}
	for _, q := range original.Questions {
		sourceIDs[q.QuestionID] = true
	}
	gradingMap := map[int64]Question{}
	for _, q := range grading.Questions {
		gradingMap[q.QuestionID] = q
	}
	questions := []Question{}
	order := []int64{}
	for _, id := range a.QuestionOrder {
		q, ok := gradingMap[id]
		if !ok {
			excluded[id] = true
			continue
		}
		if !sourceIDs[id] || excluded[id] {
			continue
		}
		questions = append(questions, q)
		order = append(order, id)
	}
	excludes := []int64{}
	for id := range excluded {
		if sourceIDs[id] {
			excludes = append(excludes, id)
		}
	}
	sort.Slice(excludes, func(i, k int) bool { return excludes[i] < excludes[k] })
	answers := append([]SubmittedAnswer{}, a.Answers...)
	j := CalculationJob{SubmissionID: *a.SubmissionID, AttemptID: a.ID, OrganizationID: a.OrganizationID, DivisionID: a.DivisionID, AccountID: a.AccountID, TestID: a.TestID, Revision: a.CalculationRevision, Operation: operation, AttemptVersionID: a.VersionID, GradingVersionID: target, GradingVersionIndex: targetIndex, Questions: questions, Answers: answers, QuestionOrder: order, ExcludedQuestionIDs: excludes, EventID: uuid(), SourceEventID: sourceEvent, CreatedAt: time.Now().UTC().Truncate(time.Millisecond)}
	j.InputHash = jobInputHash(j)
	j.ID = jobIdentity(a, target, a.CalculationRevision, j.InputHash)
	return s.Store.insertJob(ctx, j)
}
func applyJob(a *Attempt, j CalculationJob) {
	a.CalculationJobID = &j.ID
	a.CalculationJobIDs = append(a.CalculationJobIDs, j.ID)
	a.GradingVersionID = j.GradingVersionID
	a.GradingVersionIndex = j.GradingVersionIndex
	a.ExcludedQuestionIDs = j.ExcludedQuestionIDs
	a.CalculationStatus = "queued"
	a.CalculationError = ""
	a.Result = nil
	a.ResultHash = ""
	a.CalculatedAt = nil
}
func jobEvent(j CalculationJob) Event {
	event := NewEvent("tests.grade.requested", map[string]any{"job_id": j.ID, "submission_id": j.SubmissionID, "attempt_id": j.AttemptID, "test_id": j.TestID, "organization_id": j.OrganizationID, "division_id": j.DivisionID, "account_id": j.AccountID, "calculation_revision": j.Revision, "operation": j.Operation, "attempt_test_version_id": j.AttemptVersionID, "grading_test_version_id": j.GradingVersionID, "input_hash": j.InputHash})
	event.MessageID = j.EventID
	event.CorrelationID = j.EventID
	return event
}
func (s *Server) ensureInitialJob(ctx context.Context, a Attempt) (Attempt, error) {
	if a.CalculationJobID != nil {
		return a, nil
	}
	if a.CalculationRevision != 1 {
		return a, conflict("stale_calculation_revision")
	}
	j, e := s.makeJob(ctx, a, a.VersionID, "initial_grade", "")
	if e != nil {
		return a, e
	}
	oldResult, oldHash, oldStatus, oldCalculatedAt := a.Result, a.ResultHash, a.CalculationStatus, a.CalculatedAt
	applyJob(&a, j)
	if oldStatus == "ready" && oldResult != nil {
		if oldHash == "" {
			oldHash = hashJSON(oldResult)
		}
		result, e := s.Store.Jobs().UpdateOne(ctx, bson.M{"_id": j.ID, "result_hash": bson.M{"$exists": false}}, bson.M{"$set": bson.M{"result": oldResult, "result_hash": oldHash}})
		if e != nil {
			return a, e
		}
		if result.MatchedCount == 0 {
			var stored CalculationJob
			if e = s.Store.Jobs().FindOne(ctx, bson.M{"_id": j.ID}).Decode(&stored); e != nil {
				return a, e
			}
			if stored.OutputHash != oldHash {
				return a, conflict("result_idempotency_conflict")
			}
		}
		capturedAt := time.Now().UTC().Truncate(time.Millisecond)
		if oldCalculatedAt != nil {
			capturedAt = *oldCalculatedAt
		}
		a.Result = oldResult
		a.ResultHash = oldHash
		a.CalculationStatus = oldStatus
		a.CalculatedAt = oldCalculatedAt
		a.CalculationHistory = append(a.CalculationHistory, CalculationAudit{JobID: j.ID, Revision: j.Revision, Operation: j.Operation, VersionID: j.GradingVersionID, InputHash: j.InputHash, ResultHash: oldHash, Score: oldResult.Score, EarnedPoints: oldResult.EarnedPoints, MaxPoints: oldResult.MaxPoints, CalculatedAt: capturedAt})
	}
	updated, e := s.Store.saveAttempt(ctx, a, nil)
	if e != nil {
		var api *APIError
		if errors.As(e, &api) && api.Message == "attempt_changed" {
			fresh, err := s.Store.attempt(ctx, a.OrganizationID, a.ID)
			if err == nil && fresh.Active && fresh.CalculationJobID != nil {
				return fresh, nil
			}
		}
	}
	return updated, e
}

type PrepareRecalculationsDTO struct {
	MessageID           string        `json:"message_id"`
	TestID              bson.ObjectID `json:"test_id"`
	OrganizationID      int64         `json:"organization_id"`
	BeforeVersionID     bson.ObjectID `json:"before_version_id"`
	AfterVersionID      bson.ObjectID `json:"after_version_id"`
	NeedsRecalc         bool          `json:"needs_recalc"`
	Reasons             []string      `json:"reasons"`
	ExcludedQuestionIDs []int64       `json:"excluded_question_ids"`
	Cursor              string        `json:"cursor"`
}

func (s *Server) prepareRecalculations(r *http.Request) (any, int, error) {
	var input PrepareRecalculationsDTO
	if e := decode(r, &input); e != nil {
		return nil, 0, e
	}
	if input.OrganizationID < 1 || input.TestID.IsZero() || input.AfterVersionID.IsZero() || input.MessageID == "" {
		return nil, 0, bad("invalid_recalculation")
	}
	doc, e := s.Store.Get(r.Context(), input.OrganizationID, input.TestID)
	if e != nil {
		return nil, 0, e
	}
	index, e := versionIndex(doc, input.AfterVersionID)
	if e != nil {
		return nil, 0, e
	}
	v, e := s.Store.LoadContent(r.Context(), input.OrganizationID, input.AfterVersionID)
	if e != nil {
		return nil, 0, e
	}
	if v.TestID != doc.ID || v.Batch == nil || v.Batch.BeforeVersionID != input.BeforeVersionID || v.Batch.AfterVersionID != input.AfterVersionID || (v.Batch.EventID != "" && v.Batch.EventID != input.MessageID) {
		return nil, 0, bad("invalid_recalculation_event")
	}
	if v.Batch.RecalculateOnChange != nil && !*v.Batch.RecalculateOnChange {
		return map[string]any{"status": "skipped", "reason": "organization_recalculation_disabled", "processed_count": 0, "next_cursor": ""}, 200, nil
	}
	if !input.NeedsRecalc {
		return map[string]any{"status": "skipped", "reason": "no_scoring_changes", "processed_count": 0, "next_cursor": ""}, 200, nil
	}
	filter := bson.M{"organization_id": input.OrganizationID, "test_id": input.TestID, "active": true, "practice": false, "status": "accepted", "accepted_at": bson.M{"$lte": v.Batch.ChangedAt}}
	if input.Cursor != "" {
		id, e := bson.ObjectIDFromHex(input.Cursor)
		if e != nil {
			return nil, 0, bad("invalid_cursor")
		}
		filter["_id"] = bson.M{"$gt": id}
	}
	cursor, e := s.Store.Attempts().Find(r.Context(), filter, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(50))
	if e != nil {
		return nil, 0, e
	}
	defer cursor.Close(r.Context())
	attempts := []Attempt{}
	if e = cursor.All(r.Context(), &attempts); e != nil {
		return nil, 0, e
	}
	items := []map[string]any{}
	nextCursor := ""
	for _, a := range attempts {
		nextCursor = a.ID.Hex()
		for retry := 0; retry < 5; retry++ {
			if a.CalculationJobID == nil {
				a, e = s.ensureInitialJob(r.Context(), a)
				if e != nil {
					return nil, 0, e
				}
			}
			if a.GradingVersionID.IsZero() {
				originalIndex, err := versionIndex(doc, a.VersionID)
				if err != nil {
					return nil, 0, err
				}
				a.GradingVersionIndex = originalIndex
			}
			if a.GradingVersionIndex >= index || !a.Active || a.Status != "accepted" {
				break
			}
			a.CalculationRevision++
			j, e := s.makeJob(r.Context(), a, input.AfterVersionID, "regrade", input.MessageID)
			if e != nil {
				return nil, 0, e
			}
			applyJob(&a, j)
			event := jobEvent(j)
			updated, e := s.Store.saveAttempt(r.Context(), a, &event)
			if e == nil {
				items = append(items, map[string]any{"submission_id": j.SubmissionID, "calculation_revision": j.Revision, "job_id": j.ID})
				a = updated
				break
			}
			var api *APIError
			if !errors.As(e, &api) || api.Message != "attempt_changed" {
				return nil, 0, e
			}
			a, e = s.Store.attempt(r.Context(), a.OrganizationID, a.ID)
			if e != nil {
				return nil, 0, e
			}
			if retry == 4 {
				return nil, 0, conflict("recalculation_busy")
			}
		}
	}
	if len(attempts) < 50 {
		nextCursor = ""
	}
	return map[string]any{"status": "queued", "reason": "", "processed_count": len(items), "next_cursor": nextCursor, "items": items}, 200, nil
}
