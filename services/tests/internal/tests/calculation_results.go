package tests

import (
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"time"
	"unicode"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func (s *Server) calculationInput(r *http.Request) (any, int, error) {
	a, e := s.internalSubmission(r)
	if e != nil {
		return nil, 0, e
	}
	if !a.Active || a.Status != "accepted" {
		return nil, 0, conflict("submission_reset")
	}
	var requestedRevision int64
	if raw := r.URL.Query().Get("calculation_revision"); raw != "" {
		revision, e := strconv.ParseInt(raw, 10, 64)
		if e != nil || revision < 1 {
			return nil, 0, bad("invalid_calculation_revision")
		}
		if revision != a.CalculationRevision {
			return nil, 0, conflict("stale_calculation_revision")
		}
		requestedRevision = revision
	}
	a, e = s.ensureInitialJob(r.Context(), a)
	if e != nil {
		return nil, 0, e
	}
	if requestedRevision != 0 && requestedRevision != a.CalculationRevision {
		return nil, 0, conflict("stale_calculation_revision")
	}
	j, e := s.Store.currentJob(r.Context(), a)
	if e != nil {
		return nil, 0, e
	}
	return map[string]any{"job_id": j.ID, "submission_id": j.SubmissionID, "attempt_id": j.AttemptID, "organization_id": j.OrganizationID, "division_id": j.DivisionID, "account_id": j.AccountID, "test_id": j.TestID, "calculation_revision": j.Revision, "operation": j.Operation, "grading_test_version_id": j.GradingVersionID, "attempt_test_version_id": j.AttemptVersionID, "input_hash": j.InputHash, "calculation_status": a.CalculationStatus, "questions": j.Questions, "answers": j.Answers, "question_order": j.QuestionOrder, "excluded_question_ids": j.ExcludedQuestionIDs, "submission": submissionView(a), "settings_snapshot": a.SettingsSnapshot, "completed_at": a.CompletedAt, "snapshot_hash": a.SnapshotHash}, 200, nil
}

type ResultDTO struct {
	CalculationRevision int64         `json:"calculation_revision"`
	InputHash           string        `json:"input_hash"`
	GradingVersionID    bson.ObjectID `json:"grading_test_version_id"`
	ExcludedQuestionIDs []int64       `json:"excluded_question_ids"`
	GradeResult
}

func validateCalculationResult(j CalculationJob, input ResultDTO) error {
	if input.InputHash != j.InputHash || input.GradingVersionID != j.GradingVersionID || !reflect.DeepEqual(input.ExcludedQuestionIDs, j.ExcludedQuestionIDs) {
		return conflict("stale_calculation_input")
	}
	if len(input.Questions) != len(j.Questions) {
		return bad("incomplete_question_results")
	}
	questions := map[int64]Question{}
	max, earned := 0, 0
	for _, q := range j.Questions {
		questions[q.QuestionID] = q
		max += q.Points
	}
	seen := map[int64]bool{}
	for _, item := range input.Questions {
		if seen[item.QuestionID] {
			return bad("duplicate_question_result")
		}
		seen[item.QuestionID] = true
		q, ok := questions[item.QuestionID]
		if !ok {
			return bad("unknown_question_result")
		}
		if item.IsCorrect && item.Points != q.Points || !item.IsCorrect && item.Points != 0 {
			return bad("invalid_question_points")
		}
		earned += item.Points
	}
	if input.MaxPoints != max || input.EarnedPoints != earned || input.Score != legacyPercentage(earned, max) {
		return bad("inconsistent_score")
	}
	return nil
}

func attemptCASConflict(e error) bool {
	var api *APIError
	return errors.As(e, &api) && api.Message == "attempt_changed"
}

func (s *Server) writeResult(r *http.Request) (any, int, error) {
	var input ResultDTO
	if e := decode(r, &input); e != nil {
		return nil, 0, e
	}
	for retry := 0; retry < 5; retry++ {
		a, e := s.internalSubmission(r)
		if e != nil {
			return nil, 0, e
		}
		if !a.Active || a.Status != "accepted" {
			return nil, 0, conflict("submission_reset")
		}
		if input.CalculationRevision != a.CalculationRevision {
			return nil, 0, conflict("stale_calculation_revision")
		}
		a, e = s.ensureInitialJob(r.Context(), a)
		if e != nil {
			if attemptCASConflict(e) {
				continue
			}
			return nil, 0, e
		}
		if input.CalculationRevision != a.CalculationRevision {
			return nil, 0, conflict("stale_calculation_revision")
		}
		j, e := s.Store.currentJob(r.Context(), a)
		if e != nil {
			return nil, 0, e
		}
		if e = validateCalculationResult(j, input); e != nil {
			return nil, 0, e
		}
		hash := hashJSON(input)
		if a.CalculationStatus == "ready" {
			if a.ResultHash == hash {
				return submissionView(a), 200, nil
			}
			return nil, 0, conflict("result_idempotency_conflict")
		}
		// Reserve one immutable output before atomically selecting it on the attempt.
		// An interrupted or superseded callback leaves an unselected job, never a grade.
		result, e := s.Store.Jobs().UpdateOne(r.Context(), bson.M{"_id": j.ID, "input_hash": j.InputHash, "result_hash": bson.M{"$exists": false}}, bson.M{"$set": bson.M{"result": input.GradeResult, "result_hash": hash}})
		if e != nil {
			return nil, 0, e
		}
		if result.MatchedCount == 0 {
			stored, e := s.Store.currentJob(r.Context(), a)
			if e != nil {
				return nil, 0, e
			}
			if stored.OutputHash != hash {
				return nil, 0, conflict("result_idempotency_conflict")
			}
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		a.Result = &input.GradeResult
		a.ResultHash = hash
		a.CalculationStatus = "ready"
		a.CalculationError = ""
		a.CalculatedAt = &now
		a.CalculationHistory = append(a.CalculationHistory, CalculationAudit{JobID: j.ID, Revision: j.Revision, Operation: j.Operation, VersionID: j.GradingVersionID, InputHash: j.InputHash, ResultHash: hash, Score: input.Score, EarnedPoints: input.EarnedPoints, MaxPoints: input.MaxPoints, CalculatedAt: now})
		a, e = s.Store.saveAttempt(r.Context(), a, nil)
		if e == nil {
			s.observeResult(j.Operation, j.CreatedAt, now)
			return submissionView(a), 200, nil
		}
		if !attemptCASConflict(e) {
			return nil, 0, e
		}
	}
	return nil, 0, conflict("calculation_busy")
}

type CalculationStatusDTO struct {
	CalculationRevision int64  `json:"calculation_revision"`
	InputHash           string `json:"input_hash"`
	Status              string `json:"status"`
	Reason              string `json:"reason"`
}

func (s *Server) writeCalculationStatus(r *http.Request) (any, int, error) {
	var input CalculationStatusDTO
	if e := decode(r, &input); e != nil {
		return nil, 0, e
	}
	if input.Status != "processing" && input.Status != "retrying" && input.Status != "failed" {
		return nil, 0, bad("invalid_calculation_status")
	}
	if len(input.Reason) > 100 {
		return nil, 0, bad("invalid_calculation_reason")
	}
	for _, r := range input.Reason {
		if !unicode.IsLower(r) && !unicode.IsDigit(r) && r != '_' {
			return nil, 0, bad("invalid_calculation_reason")
		}
	}
	for retry := 0; retry < 5; retry++ {
		a, e := s.internalSubmission(r)
		if e != nil {
			return nil, 0, e
		}
		if !a.Active || a.Status != "accepted" {
			return nil, 0, conflict("submission_reset")
		}
		if input.CalculationRevision != a.CalculationRevision {
			return nil, 0, conflict("stale_calculation_revision")
		}
		a, e = s.ensureInitialJob(r.Context(), a)
		if e != nil {
			return nil, 0, e
		}
		if input.CalculationRevision != a.CalculationRevision {
			return nil, 0, conflict("stale_calculation_revision")
		}
		j, e := s.Store.currentJob(r.Context(), a)
		if e != nil {
			return nil, 0, e
		}
		if j.InputHash != input.InputHash {
			return nil, 0, conflict("stale_calculation_input")
		}
		if a.CalculationStatus == "ready" || a.CalculationStatus == input.Status && a.CalculationError == input.Reason {
			return submissionView(a), 200, nil
		}
		a.CalculationStatus = input.Status
		a.CalculationError = input.Reason
		a, e = s.Store.saveAttempt(r.Context(), a, nil)
		if e == nil {
			return submissionView(a), 200, nil
		}
		if !attemptCASConflict(e) {
			return nil, 0, e
		}
	}
	return nil, 0, conflict("calculation_busy")
}

func (s *Server) retryCalculation(r *http.Request, actor Actor, _ int64) (any, int, error) {
	var input RevisionDTO
	if e := decode(r, &input); e != nil {
		return nil, 0, e
	}
	a, e := s.loadSubmission(r, actor)
	if e != nil {
		return nil, 0, e
	}
	if !actorResults(actor, true) {
		return nil, 0, denied
	}
	if input.ExpectedRevision != a.Revision {
		return nil, 0, conflict("stale_revision")
	}
	if a.CalculationStatus == "ready" {
		return nil, 0, conflict("calculation_already_ready")
	}
	a, e = s.ensureInitialJob(r.Context(), a)
	if e != nil {
		return nil, 0, e
	}
	old, e := s.Store.currentJob(r.Context(), a)
	if e != nil {
		return nil, 0, e
	}
	a.CalculationRevision++
	j, e := s.makeJob(r.Context(), a, old.GradingVersionID, old.Operation, "administrator_retry")
	if e != nil {
		return nil, 0, e
	}
	applyJob(&a, j)
	event := jobEvent(j)
	a, e = s.Store.saveAttempt(r.Context(), a, &event)
	return submissionView(a), 202, e
}

func (s *Server) calculationHistory(r *http.Request, actor Actor, _ int64) (any, int, error) {
	a, e := s.loadSubmission(r, actor)
	if e != nil {
		return nil, 0, e
	}
	if !actorResults(actor, false) {
		return nil, 0, denied
	}
	limit, offset, e := pagination(r)
	if e != nil {
		return nil, 0, e
	}
	items := []any{}
	for i := len(a.CalculationHistory) - 1 - int(offset); i >= 0 && int64(len(items)) < limit; i-- {
		audit := a.CalculationHistory[i]
		var j CalculationJob
		e = s.Store.Jobs().FindOne(r.Context(), bson.M{"_id": audit.JobID, "submission_id": *a.SubmissionID, "organization_id": a.OrganizationID, "result_hash": audit.ResultHash}).Decode(&j)
		if e != nil {
			return nil, 0, e
		}
		items = append(items, map[string]any{"calculation": audit, "result": j.Output})
	}
	return map[string]any{"items": items, "total": len(a.CalculationHistory), "limit": limit, "offset": offset}, 200, nil
}
