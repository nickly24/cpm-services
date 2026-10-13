package filework

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type Claimer interface {
	Claim(context.Context, Command) (bool, string, error)
	Release(context.Context, Command, string) error
}
type HomeworkClient struct {
	URL    string
	Key    string
	Client *http.Client
}

func (h HomeworkClient) Claim(ctx context.Context, c Command) (bool, string, error) {
	body, _ := json.Marshal(map[string]int64{"generation": c.Generation})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL+"/internal/homework/file-jobs/"+c.JobID+"/claim", bytes.NewReader(body))
	if err != nil {
		return false, "", errors.New("homework_unavailable")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Key", h.Key)
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(req)
	if err != nil {
		return false, "", errors.New("homework_unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict {
		return false, "", errors.New("job_busy")
	}
	if response.StatusCode != http.StatusOK {
		return false, "", errors.New("homework_unavailable")
	}
	var result struct {
		Status bool `json:"status"`
		Data   struct {
			Run        *bool  `json:"run"`
			ClaimToken string `json:"claim_token"`
		} `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&result) != nil || !result.Status || result.Data.Run == nil {
		return false, "", errors.New("invalid_claim_response")
	}
	if *result.Data.Run && !uuidPattern.MatchString(result.Data.ClaimToken) {
		return false, "", errors.New("invalid_claim_response")
	}
	return *result.Data.Run, result.Data.ClaimToken, nil
}

func (h HomeworkClient) Release(ctx context.Context, c Command, token string) error {
	body, _ := json.Marshal(map[string]any{"generation": c.Generation, "claim_token": token})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL+"/internal/homework/file-jobs/"+c.JobID+"/release", bytes.NewReader(body))
	if err != nil {
		return errors.New("release_failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Key", h.Key)
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(req)
	if err != nil {
		return errors.New("release_failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("release_failed")
	}
	var result struct {
		Status bool `json:"status"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&result) != nil || !result.Status {
		return errors.New("release_failed")
	}
	return nil
}

type Engine struct {
	Claim      Claimer
	Storage    Storage
	Compressor Compressor
	TempDir    string
	Timeout    time.Duration
}

// Process makes no business writes. false means homework authoritatively
// declared this generation terminal; it may be safely acknowledged directly.
func (e Engine) Process(ctx context.Context, c Command) (Result, bool, error) {
	if err := c.Validate(); err != nil {
		return Result{}, false, err
	}
	ctx, cancel := context.WithTimeout(ctx, e.Timeout)
	defer cancel()
	run, token, err := e.Claim.Claim(ctx, c)
	if err != nil {
		return Result{}, false, err
	}
	if !run {
		return Result{}, false, nil
	}
	r := c.Result("")
	r.ClaimToken = token
	dir, err := os.MkdirTemp(e.TempDir, "cpm-pdf-")
	if err != nil {
		return r, true, err
	}
	defer os.RemoveAll(dir)
	source := filepath.Join(dir, "source.pdf")
	if err = e.Storage.Download(ctx, c, source); err != nil {
		return r, true, err
	}
	a, err := e.Compressor.Compress(ctx, source, dir, c.Limits)
	if err != nil {
		return r, true, err
	}
	if a.SizeBytes < 1 || a.SizeBytes > c.Limits.MaxOutputBytes || a.PageCount < 1 || a.PageCount > c.Limits.MaxPages || len(a.SHA256) != 64 {
		return r, true, errors.New("invalid_artifact")
	}
	if err = e.Storage.Upload(ctx, c, a); err != nil {
		return r, true, err
	}
	r.SizeBytes = a.SizeBytes
	r.PageCount = a.PageCount
	r.SHA256 = a.SHA256
	return r, true, nil
}

func ResultEnvelope(event Envelope[Command], r Result) Envelope[Result] {
	return Envelope[Result]{MessageID: resultMessageID(r), MessageType: ResultRoute, SchemaVersion: 1, OccurredAt: time.Now().UTC(), Producer: "file-work-service", CorrelationID: event.CorrelationID, Payload: r}
}
func resultMessageID(r Result) string {
	return fmt.Sprintf("filework:%s:%d:%s:result", r.JobID, r.Generation, r.ClaimToken)
}

func permanentCode(err error) (string, bool) {
	var rejected *Rejected
	if errors.As(err, &rejected) {
		return rejected.Code, true
	}
	return "", false
}
