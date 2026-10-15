package calculate

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	random "math/rand/v2"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type TestsClient struct {
	URL        string
	ServiceKey string
	HTTP       *http.Client
}

func NewTestsClient(endpoint, key string) *TestsClient {
	return &TestsClient{URL: endpoint, ServiceKey: key, HTTP: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

type remoteError struct {
	code int
	name string
}

// A reference is carried only after the immutable input has passed the event,
// tenant, revision and hash checks. It is never persisted as a grade output.
type gradeInputFailure struct {
	cause     error
	revision  int64
	inputHash string
}

func (e *gradeInputFailure) Error() string { return e.cause.Error() }
func (e *gradeInputFailure) Unwrap() error { return e.cause }

func (e *remoteError) Error() string { return fmt.Sprintf("tests_response_%d", e.code) }

// call deliberately omits remote error text, addresses and credentials from
// persisted errors and logs. No redirect can forward the internal key.
func (c *TestsClient) call(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, body)
	if err != nil {
		return errors.New("tests_request_invalid")
	}
	req.Header.Set("X-Service-Key", c.ServiceKey)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.HTTP.Do(req)
	if err != nil {
		return errors.New("tests_transport_unavailable")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxMessageBytes+1))
	if err != nil || len(raw) > MaxMessageBytes {
		return errors.New("tests_response_unavailable")
	}
	var envelope struct {
		Status bool            `json:"status"`
		Data   json.RawMessage `json:"data"`
		Error  string          `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return errors.New("tests_response_invalid")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !envelope.Status {
		return &remoteError{response.StatusCode, envelope.Error}
	}
	if output != nil && json.Unmarshal(envelope.Data, output) != nil {
		return errors.New("tests_response_invalid")
	}
	return nil
}

func (c *TestsClient) Ready(ctx context.Context) error {
	return c.call(ctx, http.MethodGet, "/health/ready", nil, nil)
}

type PrepareRecalculation struct {
	MessageID           string   `json:"message_id"`
	TestID              string   `json:"test_id"`
	OrganizationID      int64    `json:"organization_id"`
	BeforeVersionID     string   `json:"before_version_id"`
	AfterVersionID      string   `json:"after_version_id"`
	NeedsRecalc         bool     `json:"needs_recalc"`
	Reasons             []string `json:"reasons"`
	ExcludedQuestionIDs []int64  `json:"excluded_question_ids"`
	Cursor              string   `json:"cursor,omitempty"`
}

type PreparedRecalculation struct {
	Status         string `json:"status"`
	Reason         string `json:"reason,omitempty"`
	ProcessedCount int64  `json:"processed_count"`
	NextCursor     string `json:"next_cursor,omitempty"`
}

type Worker struct {
	Store ProcessingRepository
	Tests *TestsClient
	stats [3]workerCounters
}

type workerCounters struct {
	capacity atomic.Int64
	active   atomic.Int64
	attempts atomic.Uint64
	complete atomic.Uint64
	retried  atomic.Uint64
	failed   atomic.Uint64
	nanos    atomic.Int64
}

type WorkerKindStats struct {
	Kind              string
	Capacity          int
	Active            int64
	Attempts          uint64
	Completed         uint64
	Retried           uint64
	Failed            uint64
	ProcessingSeconds float64
}

// Stats is safe while workers are running. content and regrade share one
// background slot; their capacities must not be added as independent slots.
func (w *Worker) Stats() []WorkerKindStats {
	values := make([]WorkerKindStats, 0, len(w.stats))
	for i, kind := range []string{InitialGradeKind, RegradeKind, ContentKind} {
		c := &w.stats[i]
		values = append(values, WorkerKindStats{Kind: kind, Capacity: int(c.capacity.Load()), Active: c.active.Load(), Attempts: c.attempts.Load(), Completed: c.complete.Load(), Retried: c.retried.Load(), Failed: c.failed.Load(), ProcessingSeconds: float64(c.nanos.Load()) / float64(time.Second)})
	}
	return values
}

var errMorePages = errors.New("recalculation batch has more pages")

func leaseID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(bytes[:])
}

func (w *Worker) Run(ctx context.Context) {
	w.run(ctx, func(ctx context.Context, now time.Time, token string) (Message, error) {
		return w.Store.Claim(ctx, now, token)
	})
}

func (w *Worker) run(ctx context.Context, claim func(context.Context, time.Time, string) (Message, error)) {
	for ctx.Err() == nil {
		claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		m, err := claim(claimCtx, time.Now().UTC(), leaseID())
		cancel()
		if err != nil {
			if !noJob(err) {
				slog.Warn("calculate inbox unavailable; retry scheduled")
			}
			if !waitProcess(ctx, time.Second) {
				return
			}
			continue
		}
		started := time.Now()
		index := kindIndex(processingKind(m))
		if index >= 0 {
			w.stats[index].active.Add(1)
			w.stats[index].attempts.Add(1)
		}
		processCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		status, reason, output, err := w.Process(processCtx, m)
		cancel()
		if err != nil && m.Type == "tests.grade.requested" {
			failedStatus, failedReason := classifyProcessError(err)
			if failedStatus == "retrying" || failedStatus == "failed" {
				if notifyErr := w.notifyGradeFailure(ctx, m, output, err, failedStatus, failedReason); notifyErr != nil {
					// A lost status callback must not leave a terminal inbox job
					// behind a forever-processing submission. Retry delivery via
					// the same immutable job, without inventing a grade result.
					err = notifyErr
				}
			}
		}
		// Cancellation stops remote work immediately, then a bounded durable
		// update releases the lease into retrying before all pool workers exit.
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		if err == nil {
			err = w.Store.Finish(writeCtx, m, status, reason, output)
		} else {
			status, reason = classifyProcessError(err)
			if status == "retrying" {
				delay := retryDelay(m.ProcessingAttempts)
				if errors.Is(err, errMorePages) {
					delay = 0
				}
				err = w.Store.Retry(writeCtx, m, reason, time.Now().UTC().Add(delay))
			} else {
				err = w.Store.Finish(writeCtx, m, status, reason, output)
			}
		}
		cancel()
		if index >= 0 {
			c := &w.stats[index]
			c.active.Add(-1)
			c.nanos.Add(time.Since(started).Nanoseconds())
			if err == nil {
				switch status {
				case "ready", "skipped":
					c.complete.Add(1)
				case "retrying":
					c.retried.Add(1)
				case "failed":
					c.failed.Add(1)
				}
			}
		}
		if err != nil && !errors.Is(err, ErrLeaseLost) && ctx.Err() == nil {
			slog.Warn("calculate processing state write failed", "message_id", m.ID)
		}
	}
}

func waitProcess(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func retryDelay(attempt int64) time.Duration {
	ceiling := retryCeiling(attempt)
	return ceiling/2 + time.Duration(random.Int64N(int64(ceiling/2)+1))
}

func retryCeiling(attempt int64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	return time.Second * time.Duration(1<<uint(attempt-1))
}

func classifyProcessError(err error) (string, string) {
	if errors.Is(err, errMorePages) {
		return "retrying", "recalculation_batch_continues"
	}
	var remote *remoteError
	if errors.As(err, &remote) {
		if remote.code == http.StatusNotFound {
			return "skipped", "submission_or_source_removed"
		}
		if remote.code == http.StatusConflict {
			switch remote.name {
			case "recalculation_busy", "calculation_busy", "attempt_changed":
				return "retrying", "calculation_concurrently_changed"
			case "result_idempotency_conflict":
				return "failed", "calculation_conflict"
			default:
				return "skipped", "stale_or_reset_calculation"
			}
		}
		if remote.code == http.StatusTooManyRequests || remote.code >= 500 {
			return "retrying", "tests_temporarily_unavailable"
		}
		return "failed", "tests_rejected_calculation"
	}
	if errors.Is(err, ErrInvalidGradeInput) || errors.Is(err, ErrInvalidMessage) {
		return "failed", "invalid_calculation_input"
	}
	return "retrying", "calculation_dependency_unavailable"
}

// Process also handles messages ACKed by the earlier receipt-only service.
// The raw envelope remains untouched; mutable processing metadata has a lease.
func (w *Worker) Process(ctx context.Context, m Message) (string, string, json.RawMessage, error) {
	var envelope Envelope
	if json.Unmarshal(m.Body, &envelope) != nil {
		return "", "", nil, ErrInvalidMessage
	}
	switch m.Type {
	case "tests.grade.requested":
		return w.grade(ctx, m, envelope.Payload)
	case "tests.content.changed":
		return w.content(ctx, m, envelope.Payload)
	default:
		return "", "", nil, ErrInvalidMessage
	}
}

func validObjectID(id string) bool { _, err := bson.ObjectIDFromHex(id); return err == nil }

func (w *Worker) grade(ctx context.Context, m Message, raw json.RawMessage) (string, string, json.RawMessage, error) {
	var event struct {
		SubmissionID        string `json:"submission_id"`
		TestID              string `json:"test_id"`
		CalculationRevision int64  `json:"calculation_revision"`
		Operation           string `json:"operation"`
		GradingVersionID    string `json:"grading_test_version_id"`
	}
	if json.Unmarshal(raw, &event) != nil || !validObjectID(event.SubmissionID) || !validObjectID(event.TestID) {
		return "", "", nil, ErrInvalidMessage
	}
	if event.CalculationRevision == 0 {
		event.CalculationRevision = 1
	}
	if event.Operation == "" {
		event.Operation = "initial_grade"
	}
	if event.CalculationRevision < 1 || event.Operation != "initial_grade" && event.Operation != "regrade" {
		return "", "", nil, ErrInvalidMessage
	}
	result := CalculationResult{}
	if len(m.Output) > 0 {
		if json.Unmarshal(m.Output, &result) != nil || result.CalculationRevision != event.CalculationRevision || result.InputHash == "" {
			return "", "", nil, ErrInvalidGradeInput
		}
		w.notifyStatus(ctx, event.SubmissionID, result, "processing", "")
	} else {
		var input CalculationInput
		path := "/internal/tests/submissions/" + event.SubmissionID + "/input?calculation_revision=" + strconv.FormatInt(event.CalculationRevision, 10)
		if err := w.Tests.call(ctx, http.MethodGet, path, nil, &input); err != nil {
			return "", "", nil, err
		}
		if input.SubmissionID != event.SubmissionID || input.TestID != event.TestID || input.CalculationRevision != event.CalculationRevision || input.Operation != event.Operation || !validObjectID(input.GradingVersionID) || len(input.InputHash) != 64 || m.OrganizationID > 0 && input.OrganizationID != m.OrganizationID || m.DivisionID > 0 && input.DivisionID != m.DivisionID || m.AccountID > 0 && input.AccountID != m.AccountID || event.GradingVersionID != "" && event.GradingVersionID != input.GradingVersionID {
			return "", "", nil, ErrInvalidGradeInput
		}
		if _, err := hex.DecodeString(input.InputHash); err != nil {
			return "", "", nil, ErrInvalidGradeInput
		}
		if input.CalculationStatus == "ready" {
			output, _ := json.Marshal(map[string]any{"calculation_revision": input.CalculationRevision, "input_hash": input.InputHash, "already_ready": true})
			return "ready", "already_ready", output, nil
		}
		w.notifyStatus(ctx, event.SubmissionID, CalculationResult{CalculationRevision: input.CalculationRevision, InputHash: input.InputHash}, "processing", "")
		grade, err := GradeOfficial(input)
		if err != nil {
			return "", "", nil, &gradeInputFailure{cause: err, revision: input.CalculationRevision, inputHash: input.InputHash}
		}
		result = CalculationResult{CalculationRevision: input.CalculationRevision, GradingVersionID: input.GradingVersionID, InputHash: input.InputHash, ExcludedQuestionIDs: input.ExcludedQuestionIDs, GradeResult: grade}
		output, _ := json.Marshal(result)
		if err := w.Store.Checkpoint(ctx, m, m.ProcessCursor, output); err != nil {
			return "", "", output, err
		}
	}
	output, _ := json.Marshal(result)
	if err := w.Tests.call(ctx, http.MethodPost, "/internal/tests/submissions/"+event.SubmissionID+"/result", result, nil); err != nil {
		return "", "", output, err
	}
	return "ready", "", output, nil
}

func (w *Worker) notifyGradeFailure(ctx context.Context, m Message, output json.RawMessage, processErr error, status, reason string) error {
	var verified *gradeInputFailure
	if errors.As(processErr, &verified) {
		return w.notifyStatus(ctx, m.SubmissionID, CalculationResult{CalculationRevision: verified.revision, InputHash: verified.inputHash}, status, reason)
	}
	// Existing durable grade outputs remain the reference for callback retries.
	if len(output) == 0 {
		output = m.Output
	}
	var result CalculationResult
	if json.Unmarshal(output, &result) == nil && result.InputHash != "" {
		return w.notifyStatus(ctx, m.SubmissionID, result, status, reason)
	}
	return nil
}

func (w *Worker) notifyStatus(ctx context.Context, submissionID string, result CalculationResult, status, reason string) error {
	notifyCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	input := struct {
		CalculationRevision int64  `json:"calculation_revision"`
		InputHash           string `json:"input_hash"`
		Status              string `json:"status"`
		Reason              string `json:"reason"`
	}{result.CalculationRevision, result.InputHash, status, reason}
	return w.Tests.call(notifyCtx, http.MethodPost, "/internal/tests/submissions/"+submissionID+"/calculation-status", input, nil)
}

func (w *Worker) content(ctx context.Context, m Message, raw json.RawMessage) (string, string, json.RawMessage, error) {
	var event struct {
		TestID          string `json:"test_id"`
		OrganizationID  int64  `json:"organization_id"`
		BeforeVersionID string `json:"before_version_id"`
		AfterVersionID  string `json:"after_version_id"`
		Batch           struct {
			Before struct {
				Questions []Question `json:"questions"`
			} `json:"before"`
			After struct {
				Questions []Question `json:"questions"`
			} `json:"after"`
		} `json:"change_batch"`
	}
	if json.Unmarshal(raw, &event) != nil || !validObjectID(event.TestID) || !validObjectID(event.BeforeVersionID) || !validObjectID(event.AfterVersionID) || event.OrganizationID < 1 || event.OrganizationID != m.OrganizationID {
		return "", "", nil, ErrInvalidMessage
	}
	decision := DecideRecalc(event.Batch.Before.Questions, event.Batch.After.Questions)
	request := PrepareRecalculation{MessageID: m.ID, TestID: event.TestID, OrganizationID: event.OrganizationID, BeforeVersionID: event.BeforeVersionID, AfterVersionID: event.AfterVersionID, NeedsRecalc: decision.NeedsRecalc, Reasons: decision.Reasons, ExcludedQuestionIDs: decision.ExcludedQuestionIDs, Cursor: m.ProcessCursor}
	var response PreparedRecalculation
	if err := w.Tests.call(ctx, http.MethodPost, "/internal/tests/recalculations/prepare", request, &response); err != nil {
		return "", "", nil, err
	}
	if response.Status != "queued" && response.Status != "skipped" {
		return "", "", nil, ErrInvalidGradeInput
	}
	output, _ := json.Marshal(response)
	if response.Status == "skipped" {
		return "skipped", response.Reason, output, nil
	}
	if response.NextCursor != "" {
		if response.NextCursor == m.ProcessCursor || !validObjectID(response.NextCursor) {
			return "", "", nil, ErrInvalidGradeInput
		}
		if err := w.Store.Checkpoint(ctx, m, response.NextCursor, output); err != nil {
			return "", "", nil, err
		}
		return "", "", output, errMorePages
	}
	return "ready", "", output, nil
}
