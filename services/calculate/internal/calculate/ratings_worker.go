package calculate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

const RatingMaxAttempts int64 = 8

type RatingsClient struct {
	URL        string
	ServiceKey string
	HTTP       *http.Client
}

func NewRatingsClient(endpoint, key string) *RatingsClient {
	return &RatingsClient{URL: endpoint, ServiceKey: key, HTTP: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

type ratingRemoteError struct {
	code int
	name string
}

func (e *ratingRemoteError) Error() string { return fmt.Sprintf("ratings_response_%d", e.code) }

var errRatingsUnavailable = errors.New("ratings_temporarily_unavailable")

func (c *RatingsClient) call(ctx context.Context, method, path string, input any) (json.RawMessage, error) {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return nil, ErrInvalidRatingInput
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, body)
	if err != nil {
		return nil, ErrInvalidRatingInput
	}
	req.Header.Set("X-Service-Key", c.ServiceKey)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.HTTP.Do(req)
	if err != nil {
		return nil, errRatingsUnavailable
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxMessageBytes+1))
	if err != nil {
		return nil, errRatingsUnavailable
	}
	// A gateway may return HTML for outages. HTTP failure semantics must not
	// become an invalid-snapshot failure because its body is not JSON.
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		if len(raw) <= MaxMessageBytes && validateJSON(raw) == nil {
			_ = json.Unmarshal(raw, &failure)
		}
		return nil, &ratingRemoteError{response.StatusCode, failure.Error}
	}
	if len(raw) > MaxMessageBytes || validateJSON(raw) != nil {
		return nil, ErrInvalidRatingInput
	}
	var envelope struct {
		Status bool            `json:"status"`
		Data   json.RawMessage `json:"data"`
		Error  string          `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, ErrInvalidRatingInput
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !envelope.Status {
		return nil, &ratingRemoteError{response.StatusCode, envelope.Error}
	}
	return envelope.Data, nil
}

func (c *RatingsClient) Ready(ctx context.Context) error {
	_, err := c.call(ctx, http.MethodGet, "/health/ready", nil)
	return err
}

// Hash the full fetched JSON object, excluding only its input_hash. UseNumber
// preserves integer IDs and numeric lexemes; key order is canonical/sorted.
func RatingInputHash(raw []byte) (string, error) {
	if validateJSON(raw) != nil {
		return "", ErrInvalidRatingInput
	}
	var value map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&value) != nil || value == nil {
		return "", ErrInvalidRatingInput
	}
	delete(value, "input_hash")
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", ErrInvalidRatingInput
	}
	hash := sha256.Sum256(canonical)
	return hex.EncodeToString(hash[:]), nil
}

type RatingWorker struct {
	Store   ProcessingRepository
	Ratings *RatingsClient
}

func ratingPath(request RatingRequest) string {
	return "/internal/ratings/jobs/" + request.JobID + "/accounts/" + strconv.FormatInt(request.AccountID, 10)
}

func ratingRequestMessage(m Message) (RatingRequest, error) {
	parsed, err := ParseRatingMessage(m.Body, RatingsRoutingKey, m.ReceivedAt)
	if err != nil || parsed.ID != m.ID || parsed.JobID != m.JobID || parsed.Revision != m.Revision || parsed.OrganizationID != m.OrganizationID || parsed.DivisionID != m.DivisionID || parsed.AccountID != m.AccountID || parsed.InputHash != m.InputHash {
		return RatingRequest{}, ErrInvalidMessage
	}
	return RatingRequest{JobID: m.JobID, Revision: m.Revision, OrganizationID: m.OrganizationID, DivisionID: m.DivisionID, AccountID: m.AccountID, InputHash: m.InputHash}, nil
}

func resultRatingRequest(result RatingResult) RatingRequest {
	return RatingRequest{JobID: result.JobID, Revision: result.Revision, OrganizationID: result.OrganizationID, DivisionID: result.DivisionID, AccountID: result.AccountID, InputHash: result.InputHash}
}

func validCachedRating(result RatingResult, request RatingRequest) bool {
	if resultRatingRequest(result) != request || result.Algorithm != RatingAlgorithm {
		return false
	}
	if result.Status == "failed" {
		return result.ErrorCode != ""
	}
	return result.Status == "ready" && result.Homework != nil && result.Exams != nil && result.Tests != nil && result.FinalRating != nil && finiteRating(*result.FinalRating) && finiteRating(result.Homework.Rating) && finiteRating(result.Exams.Rating) && finiteRating(result.Tests.Rating)
}

// Process always checkpoints a complete result before sending it to its owner.
// A lost response replays the same bytes; no source is reread on delivery retry.
func (w *RatingWorker) Process(ctx context.Context, m Message) (string, string, json.RawMessage, error) {
	request, err := ratingRequestMessage(m)
	if err != nil {
		return "", "", nil, err
	}
	var result RatingResult
	output := m.Output
	if len(output) != 0 {
		if json.Unmarshal(output, &result) != nil || !validCachedRating(result, request) {
			return "", "", nil, ErrInvalidRatingInput
		}
	} else {
		raw, err := w.Ratings.call(ctx, http.MethodGet, ratingPath(request)+"/input", nil)
		if err != nil {
			return "", "", nil, err
		}
		var input RatingInput
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		hash, hashErr := RatingInputHash(raw)
		if d.Decode(&input) != nil || ratingInputRequest(input) != request || hashErr != nil || hash != request.InputHash {
			return "", "", nil, ErrInvalidRatingInput
		}
		result, err = CalculateRating(input)
		if err != nil {
			return "", "", nil, err
		}
		output, err = json.Marshal(result)
		if err != nil {
			return "", "", nil, ErrInvalidRatingInput
		}
		if err := w.Store.Checkpoint(ctx, m, "", output); err != nil {
			return "", "", output, err
		}
	}
	if _, err := w.Ratings.call(ctx, http.MethodPost, ratingPath(request)+"/result", result); err != nil {
		return "", "", output, err
	}
	return result.Status, result.ErrorCode, output, nil
}

func classifyRatingError(err error) (string, string) {
	var remote *ratingRemoteError
	if errors.As(err, &remote) {
		if remote.code == http.StatusNotFound || remote.code == http.StatusGone {
			return "skipped", "rating_job_removed"
		}
		if remote.code == http.StatusConflict {
			if remote.name == "result_idempotency_conflict" {
				return "failed", "rating_result_conflict"
			}
			return "skipped", "rating_job_cancelled_or_superseded"
		}
		if remote.code == http.StatusTooManyRequests || remote.code >= 500 {
			return "retrying", "ratings_temporarily_unavailable"
		}
		return "failed", "ratings_rejected_calculation"
	}
	if errors.Is(err, ErrRatingPending) {
		return "retrying", "rating_source_pending"
	}
	if errors.Is(err, ErrInvalidRatingInput) || errors.Is(err, ErrInvalidMessage) {
		return "failed", "invalid_rating_input"
	}
	return "retrying", "rating_dependency_unavailable"
}

func (w *RatingWorker) failure(ctx context.Context, m Message, reason string) (json.RawMessage, error) {
	request, err := ratingRequestMessage(m)
	if err != nil {
		return nil, err
	}
	result := ratingMetadata(request)
	result.Status, result.ErrorCode = "failed", reason
	output, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if err = w.Store.Checkpoint(ctx, m, "", output); err != nil {
		return output, err
	}
	_, err = w.Ratings.call(ctx, http.MethodPost, ratingPath(request)+"/result", result)
	return output, err
}

func (w *RatingWorker) Run(ctx context.Context) {
	for ctx.Err() == nil {
		claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		m, err := w.Store.Claim(claimCtx, time.Now().UTC(), leaseID())
		cancel()
		if err != nil {
			if !noJob(err) {
				slog.Warn("calculate rating inbox unavailable; retry scheduled")
			}
			if !waitProcess(ctx, time.Second) {
				return
			}
			continue
		}
		processCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		status, reason, output, err := w.Process(processCtx, m)
		if err != nil && !errors.Is(err, ErrLeaseLost) {
			status, reason = classifyRatingError(err)
			if status == "retrying" && m.ProcessingAttempts >= RatingMaxAttempts {
				status, reason = "failed", "rating_retry_exhausted"
			}
			// Preserve already computed output when delivery fails. If no output
			// exists, persist a terminal failure and notify the ratings owner.
			if status == "failed" && len(output) == 0 && len(m.Output) == 0 {
				output, err = w.failure(processCtx, m, reason)
				if err != nil && !errors.Is(err, ErrLeaseLost) && m.ProcessingAttempts < RatingMaxAttempts {
					status, reason = classifyRatingError(err)
				}
			}
		}
		cancel()
		if errors.Is(err, ErrLeaseLost) {
			continue
		}
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if status == "retrying" {
			err = w.Store.Retry(writeCtx, m, reason, time.Now().UTC().Add(retryDelay(m.ProcessingAttempts)))
		} else {
			if status == "" {
				status, reason = "failed", "rating_processing_failed"
			}
			err = w.Store.Finish(writeCtx, m, status, reason, output)
		}
		cancel()
		if err != nil && !errors.Is(err, ErrLeaseLost) && ctx.Err() == nil {
			slog.Warn("calculate rating processing state write failed", "message_id", m.ID)
		}
	}
}
