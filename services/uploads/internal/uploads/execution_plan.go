package uploads

import (
	"encoding/json"
	"strconv"
	"strings"
)

// ownerCommandPlan is private coordinator data. Body is retained as bytes in the
// protected execution ledger, never returned in a Job or published to Rabbit.
// Building this plan does not authorize, enqueue or execute an operation.
type ownerCommandPlan struct {
	Owner, Type, NativeKey, Method, Path string
	Body                                 []byte
	BodySHA256, OwnerRequestSHA256       string
}

// immutableOwnerCommand captures the native command once. Retries and receipt
// recovery must reuse this command's bytes and key rather than rebuilding it
// from a newer preview. Auth grants and owner proofs are checked separately.
func immutableOwnerCommand(ss Session, jobID string, staged *stagedPreview) (ownerCommandPlan, error) {
	p := ownerCommandPlan{Owner: ss.Owner, Type: ss.Type, Method: "POST"}
	if !lowerHexReference(jobID, 32) || !lowerHexReference(ss.ID, 32) ||
		ss.OrganizationID < 1 || ss.DivisionID < 1 || ss.ActorID < 1 ||
		ss.Revision < 1 || !lowerHexReference(ss.SourceHash, 64) {
		return p, api(409, "invalid_execution_snapshot")
	}
	t, e := findType(ss.Type)
	if e != nil || t.Owner != ss.Owner || !t.Commit || ss.Type == "cards.transform" {
		return p, api(409, "background_type_unavailable")
	}
	p.NativeKey = ownerCommand("commit", jobID, "")
	division := strconv.FormatInt(ss.DivisionID, 10)
	switch ss.Type {
	case "users.credentials", "users.legacy":
		if !canonicalPositiveID(ss.OwnerSessionID) {
			return p, api(409, "invalid_execution_snapshot")
		}
		kind := "imports"
		if ss.Type == "users.legacy" {
			kind = "legacy-imports"
		}
		p.Path = "/v1/core/divisions/" + division + "/" + kind + "/" + ss.OwnerSessionID + "/commit"
	case "cards.xlsx":
		if !lowerHexReference(ss.OwnerSessionID, 32) {
			return p, api(409, "invalid_execution_snapshot")
		}
		p.Path = "/v1/cards/sessions/" + ss.OwnerSessionID + "/commit"
	case "exams.questions", "exams.assignments", "exams.outside":
		if !canonicalPositiveID(ss.TargetID) || !canonicalUUID(ss.OwnerSessionID) {
			return p, api(409, "invalid_execution_snapshot")
		}
		p.Path = "/v1/exams/" + ss.TargetID + "/imports/" + strings.TrimPrefix(ss.Type, "exams.") + "/" + ss.OwnerSessionID + "/commit"
	case "tests.json", "tests.external-results":
		if staged == nil || staged.Revision != ss.Revision || staged.SourceHash != ss.SourceHash ||
			!lowerHexReference(staged.OwnerHash, 64) || digest(staged.Input) != staged.InputHash ||
			digest(staged.Preview) != staged.Hash {
			return p, api(409, "invalid_execution_snapshot")
		}
		// Use raw values, including int64 IDs and JSON number lexemes. Do not
		// decode through float64 while adding the existing native DTO fields.
		var fields map[string]json.RawMessage
		if stagedDecode(staged.Input, &fields) != nil || fields == nil {
			return p, api(409, "invalid_execution_snapshot")
		}
		allowed := map[string]bool{"organization_id": true, "division_id": true, "direction_id": true, "payload": true}
		if ss.Type == "tests.external-results" {
			allowed = map[string]bool{"organization_id": true, "division_id": true, "test_id": true, "expected_test_revision": true, "rows": true}
		}
		for field := range fields {
			if !allowed[field] {
				return p, api(409, "invalid_execution_snapshot")
			}
		}
		var scope struct {
			OrganizationID int64 `json:"organization_id"`
			DivisionID     int64 `json:"division_id"`
		}
		if json.Unmarshal(staged.Input, &scope) != nil || scope.OrganizationID != ss.OrganizationID || scope.DivisionID != ss.DivisionID {
			return p, api(409, "invalid_execution_snapshot")
		}
		if ss.Type == "tests.json" {
			var input struct {
				DirectionID int64           `json:"direction_id"`
				Payload     json.RawMessage `json:"payload"`
			}
			if json.Unmarshal(staged.Input, &input) != nil || input.DirectionID < 1 || len(input.Payload) == 0 || string(input.Payload) == "null" || ss.TargetID != "" {
				return p, api(409, "invalid_execution_snapshot")
			}
		}
		fields["command_id"], _ = json.Marshal(p.NativeKey)
		fields["preview_hash"], _ = json.Marshal(staged.OwnerHash)
		p.Path = "/internal/tests/imports/json/commit"
		if ss.Type == "tests.external-results" {
			var target struct {
				TestID   string `json:"test_id"`
				Revision int64  `json:"expected_test_revision"`
			}
			if json.Unmarshal(staged.Input, &target) != nil || !lowerHexReference(ss.TargetID, 24) || target.TestID != ss.TargetID || target.Revision < 1 || target.Revision != staged.TargetRevision {
				return p, api(409, "invalid_execution_snapshot")
			}
			p.Path = "/internal/tests/external-results/commit"
			fields["upload_id"], _ = json.Marshal(ss.FileID)
			fields["upload_session_id"], _ = json.Marshal(ss.ID)
			fields["upload_preview_revision"], _ = json.Marshal(ss.Revision)
			if !lowerHexReference(ss.FileID, 32) {
				return p, api(409, "invalid_execution_snapshot")
			}
		}
		p.Body, e = json.Marshal(fields)
		if e != nil || len(p.Body) > stagedInputLimit {
			return p, api(413, "owner_input_size_limit")
		}
	}
	if ss.Type == "users.credentials" {
		p.Body = []byte("{}") // Exactly two bytes; no LF or credential contents.
	} else if !stagedType(ss.Type) {
		if ss.OwnerRevision < 1 {
			return p, api(409, "invalid_execution_snapshot")
		}
		p.Body, e = json.Marshal(RevisionDTO{ss.OwnerRevision})
		if e != nil {
			return p, e
		}
	}
	p.BodySHA256 = digest(p.Body)
	if strings.HasPrefix(ss.Type, "exams.") {
		// Native Exams request hash v1 includes origin division and exact path.
		p.OwnerRequestSHA256 = digest(append([]byte("POST "+p.Path+"? "+division+" "), p.Body...))
	}
	return p, nil
}

func lowerHexReference(value string, size int) bool {
	return len(value) == size && strings.ToLower(value) == value && hexSize(value, size/2)
}

func canonicalPositiveID(value string) bool {
	n, e := strconv.ParseInt(value, 10, 64)
	return e == nil && n > 0 && strconv.FormatInt(n, 10) == value
}

func canonicalUUID(value string) bool {
	return len(value) == 36 && value[8] == '-' && value[13] == '-' && value[18] == '-' && value[23] == '-' && lowerHexReference(strings.ReplaceAll(value, "-", ""), 32)
}
