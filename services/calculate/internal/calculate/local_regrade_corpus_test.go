package calculate

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

// Private diagnostic inputs are supplied explicitly; user answers never become
// repository fixtures. This replay uses only pure grading and loopback HTTP.
func TestLocalRegradeCorpus(t *testing.T) {
	path := os.Getenv("CPM_CALCULATE_REPLAY_FIXTURE")
	if path == "" {
		t.Skip("optional private local replay fixture not supplied")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Input    CalculationInput `json:"input"`
		Expected GradeResult      `json:"expected"`
		Control  bool             `json:"control"`
	}
	if json.Unmarshal(raw, &rows) != nil || len(rows) == 0 {
		t.Fatal("invalid local fixture")
	}
	controls := 0
	for i, row := range rows {
		t.Run(fmt.Sprintf("snapshot_%03d", i+1), func(t *testing.T) {
			before, _ := json.Marshal(row.Input)
			got, err := GradeOfficial(row.Input)
			if err != nil || !reflect.DeepEqual(got, row.Expected) {
				t.Fatalf("local grade differs from independent legacy reference, error=%v", err)
			}
			// Native HTTP boundary with synthetic scope; original question and
			// answer bytes remain unchanged. No production client is created.
			input := row.Input
			input.SubmissionID = fmt.Sprintf("%024x", i+1)
			input.TestID = "507f1f77bcf86cd799439012"
			input.OrganizationID, input.DivisionID, input.AccountID = 1, 2, 3
			input.CalculationRevision = 3
			input.GradingVersionID = "507f1f77bcf86cd799439013"
			sum := sha256.Sum256(before)
			input.InputHash = fmt.Sprintf("%x", sum)
			m := workerMessage(t)
			var env Envelope
			_ = json.Unmarshal(m.Body, &env)
			env.Payload = mustJSON(map[string]any{"submission_id": input.SubmissionID, "test_id": input.TestID, "organization_id": 1, "division_id": 2, "account_id": 3, "calculation_revision": 3, "operation": "regrade", "grading_test_version_id": input.GradingVersionID})
			m.Body, _ = json.Marshal(env)
			m.SubmissionID = input.SubmissionID
			gets, callbacks, unique := 0, 0, 0
			var persisted []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Service-Key") != strings.Repeat("k", 48) {
					t.Error("missing synthetic service credential")
				}
				if r.Method == http.MethodGet {
					gets++
					reply(w, 200, input)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/calculation-status") {
					reply(w, 200, map[string]bool{"ok": true})
					return
				}
				callbacks++
				var result CalculationResult
				if json.NewDecoder(r.Body).Decode(&result) != nil || !reflect.DeepEqual(result.GradeResult, row.Expected) || result.InputHash != input.InputHash || result.CalculationRevision != 3 {
					t.Error("HTTP result differs from legacy grade/reference")
				}
				b, _ := json.Marshal(result)
				if persisted == nil {
					persisted = b
					unique++
				} else if string(persisted) != string(b) {
					t.Error("retry changed result bytes")
				}
				reply(w, 200, map[string]bool{"ok": true})
			}))
			defer server.Close()
			store := &processingMemory{}
			worker := &Worker{Store: store, Tests: NewTestsClient(server.URL, strings.Repeat("k", 48))}
			status, _, output, err := worker.Process(context.Background(), m)
			if err != nil || status != "ready" {
				t.Fatal("local HTTP regrade failed", err)
			}
			m.Output = output
			status, _, _, err = worker.Process(context.Background(), m)
			if err != nil || status != "ready" || gets != 1 || callbacks != 2 || unique != 1 || store.checkpointCalls != 1 {
				t.Fatal("local callback replay lost idempotency")
			}
			after, _ := json.Marshal(row.Input)
			if string(before) != string(after) {
				t.Fatal("immutable snapshot mutated")
			}
		})
		if row.Control {
			controls++
		}
	}
	t.Logf("local snapshots=%d affected=%d controls=%d; legacy scores and HTTP callback replay match", len(rows), len(rows)-controls, controls)
}
