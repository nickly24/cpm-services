package tests

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"io"
	"math/big"
	"net/http"
	"time"
)

func shuffleQuestions(order []int64) {
	for i := len(order) - 1; i > 0; i-- {
		n, e := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if e != nil {
			panic(e)
		}
		j := int(n.Int64())
		order[i], order[j] = order[j], order[i]
	}
}
func (s *Server) internalRoutes(mux *http.ServeMux) {
	secure := func(fn func(*http.Request) (any, int, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if hasImportCarrier(r.Header) {
				fail(w, conflict("operation_request_mismatch"))
				return
			}
			key := r.Header.Get("X-Service-Key")
			if len(s.Config.ServiceKey) < 32 || subtle.ConstantTimeCompare([]byte(key), []byte(s.Config.ServiceKey)) != 1 {
				fail(w, &APIError{401, "service_authentication_required"})
				return
			}
			data, code, e := fn(r)
			if e != nil {
				fail(w, e)
				return
			}
			reply(w, code, Envelope{true, data})
		}
	}
	mux.HandleFunc("GET /internal/tests/submissions/{submission}/input", secure(s.calculationInput))
	mux.HandleFunc("POST /internal/tests/submissions/{submission}/result", secure(s.writeResult))
	mux.HandleFunc("POST /internal/tests/submissions/{submission}/calculation-status", secure(s.writeCalculationStatus))
	mux.HandleFunc("POST /internal/tests/recalculations/prepare", secure(s.prepareRecalculations))
	mux.HandleFunc("POST /internal/tests/ratings/snapshot", secure(s.ratingSnapshot))
	mux.HandleFunc("POST /internal/tests/external-domain/status", secure(s.externalStatus))
}
func (s *Server) internalSubmission(r *http.Request) (Attempt, error) {
	id, e := bson.ObjectIDFromHex(r.PathValue("submission"))
	if e != nil {
		return Attempt{}, bad("invalid_id")
	}
	var a Attempt
	e = s.Store.Attempts().FindOne(r.Context(), bson.M{"submission_id": id}).Decode(&a)
	if errors.Is(e, mongo.ErrNoDocuments) {
		e = missing
	}
	return a, e
}
func (s *Server) practiceAnswer(r *http.Request, actor Actor, _ int64) (any, int, error) {
	var input AnswerDTO
	if e := decode(r, &input); e != nil {
		return nil, 0, e
	}
	a, e := s.loadAttempt(r, actor)
	if e != nil {
		return nil, 0, e
	}
	if a.AccountID != actor.AccountID || !a.Practice {
		return nil, 0, denied
	}
	if a.Status != "in_progress" {
		return nil, 0, conflict("attempt_closed")
	}
	if e = s.checkPracticeAccess(r, a); e != nil {
		return nil, 0, e
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
	var question Question
	for _, q := range version.Questions {
		if q.QuestionID == input.QuestionID {
			question = q
			break
		}
	}
	body, _ := json.Marshal(map[string]any{"question": question, "value": answers[0].Value})
	req, e := http.NewRequestWithContext(r.Context(), "POST", s.Config.CalculateURL+"/internal/calculate/practice/check", bytes.NewReader(body))
	if e != nil {
		return nil, 0, unavailable
	}
	req.Header.Set("X-Service-Key", s.Config.ServiceKey)
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, e := client.Do(req)
	if e != nil {
		return nil, 0, unavailable
	}
	defer response.Body.Close()
	var payload struct {
		Status bool           `json:"status"`
		Data   map[string]any `json:"data"`
	}
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, testBodyLimit)).Decode(&payload) != nil || !payload.Status {
		return nil, 0, unavailable
	}
	points, validPoints := payload.Data["points"].(float64)
	correct, validCorrect := payload.Data["is_correct"].(bool)
	if !validPoints || !validCorrect || (correct && int(points) != question.Points) || (!correct && points != 0) {
		return nil, 0, unavailable
	}
	found := false
	for _, old := range a.PracticeResults {
		if old.QuestionID == question.QuestionID {
			found = true
			break
		}
	}
	if !found {
		a.PracticeResults = append(a.PracticeResults, QuestionResult{question.QuestionID, int(points), correct})
	}
	a, e = s.Store.saveAttempt(r.Context(), a, nil)
	if e != nil {
		return nil, 0, e
	}
	return map[string]any{"feedback": payload.Data, "answered_count": len(a.Answers), "total_questions": len(a.QuestionOrder), "revision": a.Revision}, 200, nil
}

type BatchAnswersDTO struct {
	Answers []SubmittedAnswer `json:"answers"`
}

func (s *Server) batchAnswers(r *http.Request, actor Actor, _ int64) (any, int, error) {
	var input BatchAnswersDTO
	if e := decode(r, &input); e != nil {
		return nil, 0, e
	}
	if len(input.Answers) < 1 {
		return nil, 0, bad("invalid_answer_batch")
	}
	a, e := s.loadAttempt(r, actor)
	if e != nil {
		return nil, 0, e
	}
	if a.AccountID != actor.AccountID || a.Practice {
		return nil, 0, denied
	}
	if a.Status != "in_progress" || time.Now().After(a.AnswerDeadline) {
		return nil, 0, conflict("attempt_closed")
	}
	v, e := s.Store.LoadContent(r.Context(), a.OrganizationID, a.VersionID)
	if e != nil {
		return nil, 0, e
	}
	answers, e := normalizedAnswers(v.Questions, input.Answers)
	if e != nil {
		return nil, 0, e
	}
	for _, answer := range answers {
		if e = putAnswer(&a, answer); e != nil {
			return nil, 0, e
		}
	}
	a, e = s.Store.saveAttempt(r.Context(), a, nil)
	if e != nil {
		return nil, 0, e
	}
	view, e := s.attemptView(r, a)
	return view, 200, e
}

func (s *Server) finishPractice(r *http.Request, a Attempt) (any, int, error) {
	if a.Status == "practice_completed" {
		view, e := s.attemptView(r, a)
		return view, 200, e
	}
	if a.Status != "in_progress" || len(a.Answers) == 0 {
		return nil, 0, conflict("no_answers")
	}
	if e := s.checkPracticeAccess(r, a); e != nil {
		return nil, 0, e
	}
	version, e := s.Store.LoadContent(r.Context(), a.OrganizationID, a.VersionID)
	if e != nil {
		return nil, 0, e
	}
	body, e := json.Marshal(map[string]any{"questions": version.Questions, "answers": a.Answers})
	if e != nil {
		return nil, 0, e
	}
	req, e := http.NewRequestWithContext(r.Context(), "POST", s.Config.CalculateURL+"/internal/calculate/practice/finish", bytes.NewReader(body))
	if e != nil {
		return nil, 0, unavailable
	}
	req.Header.Set("X-Service-Key", s.Config.ServiceKey)
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, e := client.Do(req)
	if e != nil {
		return nil, 0, unavailable
	}
	defer response.Body.Close()
	var payload struct {
		Status bool        `json:"status"`
		Data   GradeResult `json:"data"`
	}
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, testBodyLimit)).Decode(&payload) != nil || !payload.Status {
		return nil, 0, unavailable
	}
	grade := payload.Data
	maximum := 0
	for _, q := range version.Questions {
		maximum += q.Points
	}
	if grade.MaxPoints != maximum || grade.EarnedPoints < 0 || grade.EarnedPoints > maximum || grade.Score != legacyPercentage(grade.EarnedPoints, maximum) || len(grade.Questions) != len(version.Questions) {
		return nil, 0, unavailable
	}
	a.Result = &grade
	a.CalculationStatus = "ready"
	a.Status = "practice_completed"
	a.Active = false
	now := time.Now().UTC().Truncate(time.Millisecond)
	a.CompletedAt = &now
	a, e = s.Store.saveAttempt(r.Context(), a, nil)
	if e != nil {
		return nil, 0, e
	}
	view, e := s.attemptView(r, a)
	return view, 200, e
}
