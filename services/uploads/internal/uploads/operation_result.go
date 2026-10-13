package uploads

import (
	"encoding/json"
	"strconv"
	"strings"
)

// A positive owner reply is checked against the original command and receipt
// shape before a public job can claim completion. This is not domain validation.
func confirmedOperationResult(p privateExecutionPlan, raw json.RawMessage) (json.RawMessage, error) {
	if _, err := operationJSONValue(raw); err != nil {
		return nil, api(503, "owner_outcome_unknown")
	}
	var b operationBinding
	if json.Unmarshal(p.BindingBody, &b) != nil {
		return nil, api(503, "execution_plan_integrity_failed")
	}
	invalid := func() (json.RawMessage, error) { return nil, api(503, "owner_outcome_unknown") }
	switch p.Command.Type {
	case "users.credentials":
		var result struct {
			Created []int64 `json:"created"`
			Count   int     `json:"count"`
		}
		if decodeNativeOperationJSON(raw, &result) != nil || result.Count < 1 || result.Count > 100 || result.Count != len(result.Created) || !uniquePositiveIDs(result.Created) {
			return invalid()
		}
	case "users.legacy":
		var result struct {
			ID             int64           `json:"id"`
			OrganizationID int64           `json:"organization_id"`
			DivisionID     int64           `json:"division_id"`
			ActorID        int64           `json:"actor_id"`
			Status         string          `json:"status"`
			Report         json.RawMessage `json:"report"`
		}
		if decodeNativeOperationJSON(raw, &result) != nil || b.OwnerSessionID == nil || strconv.FormatInt(result.ID, 10) != *b.OwnerSessionID || result.OrganizationID != p.Scope.OrganizationID || result.DivisionID != p.Scope.DivisionID || result.ActorID != p.Scope.ActorID || result.Status != "completed" || len(result.Report) == 0 || string(result.Report) == "null" {
			return invalid()
		}
	case "cards.xlsx":
		var receipt struct {
			State          string          `json:"state"`
			Operation      string          `json:"operation"`
			RequestHash    string          `json:"request_hash"`
			OrganizationID int64           `json:"organization_id"`
			DirectionID    int64           `json:"direction_id"`
			SessionID      string          `json:"session_id"`
			Result         json.RawMessage `json:"result"`
		}
		var result struct {
			ThemeID int64   `json:"theme_id"`
			CardIDs []int64 `json:"card_ids"`
			Created int     `json:"created"`
			Skipped int     `json:"skipped"`
		}
		var target struct {
			Kind        string `json:"kind"`
			DirectionID string `json:"direction_id"`
			ThemeID     string `json:"theme_id"`
		}
		if decodeNativeOperationJSON(raw, &receipt) != nil || decodeNativeOperationJSON(receipt.Result, &result) != nil || json.Unmarshal(b.Target, &target) != nil || b.OwnerSessionID == nil ||
			receipt.State != "completed" || receipt.Operation != "session:commit:"+*b.OwnerSessionID || receipt.SessionID != *b.OwnerSessionID || receipt.RequestHash != b.OwnerRequestSHA256 || receipt.OrganizationID != p.Scope.OrganizationID || strconv.FormatInt(receipt.DirectionID, 10) != target.DirectionID ||
			result.ThemeID < 1 || result.Created < 1 || result.Created > 1000 || result.Created != len(result.CardIDs) || result.Skipped < 0 || result.Created+result.Skipped > 1000 || !uniquePositiveIDs(result.CardIDs) || target.Kind == "existing_theme" && strconv.FormatInt(result.ThemeID, 10) != target.ThemeID {
			return invalid()
		}
		return receipt.Result, nil
	case "tests.json":
		var result struct {
			CommandID string `json:"command_id"`
			Test      struct {
				ID             string `json:"id"`
				OrganizationID int64  `json:"organization_id"`
				DivisionID     int64  `json:"owner_division_id"`
				DirectionID    int64  `json:"direction_id"`
			} `json:"test"`
		}
		// Tests enforces the immutable creation target before returning its
		// current TestView. Its direction may legitimately change afterwards.
		if decodeNativeOperationJSON(raw, &result) != nil || result.CommandID != p.Command.NativeKey || !lowerHexReference(result.Test.ID, 24) || result.Test.OrganizationID != p.Scope.OrganizationID || result.Test.DivisionID != p.Scope.DivisionID || result.Test.DirectionID < 1 {
			return invalid()
		}
		if len(p.OwnerResult) > 0 {
			var previous struct {
				Test struct {
					ID string `json:"id"`
				} `json:"test"`
			}
			if json.Unmarshal(p.OwnerResult, &previous) != nil || previous.Test.ID != result.Test.ID {
				return invalid()
			}
		}
	case "tests.external-results":
		var result struct {
			CommandID      string `json:"command_id"`
			Status         string `json:"status"`
			SourceRevision int64  `json:"source_revision"`
			Results        []struct {
				RowID     string  `json:"row_id"`
				ResultID  string  `json:"result_id"`
				AccountID int64   `json:"account_id"`
				Revision  int64   `json:"revision"`
				Percent   float64 `json:"percent"`
			} `json:"results"`
		}
		var input struct {
			Rows []struct {
				RowID     string `json:"row_id"`
				AccountID *int64 `json:"account_id"`
			} `json:"rows"`
		}
		var confirmed struct {
			Rows []struct {
				RowID     string `json:"row_id"`
				AccountID *int64 `json:"account_id"`
			} `json:"rows"`
		}
		targetRevision := int64(0)
		if b.TargetRevision != nil {
			targetRevision, _ = strconv.ParseInt(*b.TargetRevision, 10, 64)
		}
		if decodeNativeOperationJSON(raw, &result) != nil || json.Unmarshal(p.PreviewBody, &input) != nil || decodeNativeOperationJSON(p.ConfirmedPreview, &confirmed) != nil || result.CommandID != p.Command.NativeKey || result.Status != "committed" || targetRevision < 1 || result.SourceRevision <= targetRevision || result.SourceRevision != targetRevision+1 || len(input.Rows) == 0 || len(result.Results) != len(input.Rows) || len(confirmed.Rows) != len(input.Rows) {
			return invalid()
		}
		seen := map[string]bool{}
		resultIDs := map[string]bool{}
		for i, row := range result.Results {
			if row.RowID != input.Rows[i].RowID || confirmed.Rows[i].RowID != row.RowID || seen[row.RowID] || !lowerHexReference(row.ResultID, 24) || resultIDs[row.ResultID] || row.AccountID < 1 || confirmed.Rows[i].AccountID == nil || row.AccountID != *confirmed.Rows[i].AccountID || input.Rows[i].AccountID != nil && row.AccountID != *input.Rows[i].AccountID || row.Revision < 1 || row.Percent < 0 || row.Percent > 100 {
				return invalid()
			}
			seen[row.RowID] = true
			resultIDs[row.ResultID] = true
		}
	case "exams.questions", "exams.assignments", "exams.outside":
		var result struct {
			SessionID        string `json:"session_id"`
			Revision         int64  `json:"revision"`
			ExamID           int64  `json:"exam_id"`
			ExamRevision     int64  `json:"exam_revision"`
			Kind             string `json:"kind"`
			OrganizationID   int64  `json:"organization_id"`
			DivisionID       int64  `json:"division_id"`
			AccountID        int64  `json:"account_id"`
			SourceHash       string `json:"source_hash"`
			ValidationHash   string `json:"validation_hash"`
			NativeCommandKey string `json:"native_command_key"`
			JobID            string `json:"operation_job_id"`
			State            string `json:"state"`
		}
		var target struct {
			ExamID string `json:"exam_id"`
		}
		if decodeNativeOperationJSON(raw, &result) != nil || json.Unmarshal(b.Target, &target) != nil || b.OwnerSessionID == nil || b.OwnerPreviewRevision == nil || b.TargetRevision == nil ||
			result.SessionID != *b.OwnerSessionID || strconv.FormatInt(result.Revision, 10) != *b.OwnerPreviewRevision || strconv.FormatInt(result.ExamID, 10) != target.ExamID || strconv.FormatInt(result.ExamRevision, 10) != *b.TargetRevision || result.Kind != strings.TrimPrefix(p.Command.Type, "exams.") ||
			result.OrganizationID != p.Scope.OrganizationID || result.DivisionID != p.Scope.DivisionID || result.AccountID != p.Scope.ActorID || result.SourceHash != b.SourceSHA256 || result.ValidationHash != b.OwnerProof.ValidationSHA256 || result.NativeCommandKey != p.Command.NativeKey || result.JobID != p.JobID || result.State != "committed" {
			return invalid()
		}
	default:
		return invalid()
	}
	return raw, nil
}

func uniquePositiveIDs(ids []int64) bool {
	seen := map[int64]bool{}
	for _, id := range ids {
		if id < 1 || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}
