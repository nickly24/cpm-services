package uploads

import (
	"encoding/json"
	"sort"
	"strconv"
	"time"
)

func stringPointer(value string) *string { return &value }

// A quote is an owner-authenticated readonly response to the exact Tests commit
// bytes. Uploads copies native digests; it does not reproduce native hashing.
type testsOperationQuote struct {
	OwnerRequestSHA256 string `json:"owner_request_sha256"`
	NativeCommandKey   string `json:"native_command_key"`
	OwnerProof         struct {
		SchemaVersion int `json:"schema_version"`
		operationProof
	} `json:"owner_proof"`
}

func validateOperationProof(p operationProof, scheme string) bool {
	if p.Scheme != scheme || !lowerHexReference(p.ValidationSHA256, 64) {
		return false
	}
	if p.InputSHA256 != nil && !lowerHexReference(*p.InputSHA256, 64) || p.MappingSHA256 != nil && !lowerHexReference(*p.MappingSHA256, 64) {
		return false
	}
	return true
}

func bindOwnerOperation(ss Session, p privateExecutionPlan, owner json.RawMessage, quote *testsOperationQuote) (operationBinding, error) {
	b := operationBinding{ContractVersion: 1, UploadID: ss.FileID, UploadSessionID: ss.ID, JobID: p.JobID,
		CoordinatorCommandKey: p.CoordinatorKey, Type: ss.Type, Owner: ss.Owner, NativeCommandKey: p.Command.NativeKey,
		UploadPreviewRevision: strconv.FormatInt(ss.Revision, 10), SourceSHA256: ss.SourceHash,
		ApplyWireBodySHA256: p.Command.BodySHA256, UploadExpiresAt: ss.Expires.UTC().Truncate(time.Microsecond),
		RequiredDivisionIDs: []string{strconv.FormatInt(ss.DivisionID, 10)}}
	if _, err := operationJSONValue(owner); err != nil {
		return b, api(409, "owner_authorization_proof_required")
	}
	if ss.Scope != p.Scope || ss.ID != p.SessionID || !lowerHexReference(ss.FileID, 32) || !keyPattern.MatchString(p.CoordinatorKey) ||
		p.Command.NativeKey != ownerCommand("commit", p.JobID, "") || digest(p.Command.Body) != p.Command.BodySHA256 {
		return b, api(409, "invalid_execution_snapshot")
	}
	if !stagedType(ss.Type) {
		b.OwnerSessionID = stringPointer(ss.OwnerSessionID)
		if ss.Type != "users.credentials" {
			b.OwnerPreviewRevision = stringPointer(strconv.FormatInt(ss.OwnerRevision, 10))
		}
	}
	switch ss.Type {
	case "users.credentials", "users.legacy":
		var v struct {
			ID             int64           `json:"id"`
			OrganizationID int64           `json:"organization_id"`
			DivisionID     int64           `json:"division_id"`
			ActorID        int64           `json:"actor_id"`
			Revision       int64           `json:"revision"`
			Status         string          `json:"status"`
			SourceSHA256   string          `json:"source_sha256"`
			Proof          *operationProof `json:"authorization_proof"`
			RequestSHA256  string          `json:"owner_request_sha256"`
			ExpiresAt      time.Time       `json:"expires_at"`
		}
		if json.Unmarshal(owner, &v) != nil || strconv.FormatInt(v.ID, 10) != ss.OwnerSessionID || v.OrganizationID != ss.OrganizationID || v.DivisionID != ss.DivisionID ||
			v.ActorID != ss.ActorID || v.Status != "preview" || v.SourceSHA256 != ss.SourceHash || v.Proof == nil {
			return b, api(409, "owner_authorization_proof_required")
		}
		scheme := "core.credentials.snapshot.v1"
		if ss.Type == "users.legacy" {
			scheme = "core.legacy.snapshot.v1"
			if v.Revision != ss.OwnerRevision || v.ExpiresAt.IsZero() {
				return b, api(409, "owner_preview_modified")
			}
			expiry := v.ExpiresAt.UTC().Truncate(time.Microsecond)
			b.OwnerPreviewExpiresAt = &expiry
		}
		if !validateOperationProof(*v.Proof, scheme) || v.Proof.InputSHA256 == nil || v.Proof.MappingSHA256 != nil {
			return b, api(409, "owner_authorization_proof_required")
		}
		b.OwnerProof = *v.Proof
		b.OwnerRequestSHA256 = v.RequestSHA256
		b.Target, _ = json.Marshal(map[string]string{"kind": "division_users", "division_id": strconv.FormatInt(ss.DivisionID, 10)})
	case "cards.xlsx":
		var v struct {
			ID             string    `json:"session_id"`
			OrganizationID int64     `json:"organization_id"`
			Revision       int64     `json:"revision"`
			State          string    `json:"state"`
			ExpiresAt      time.Time `json:"expires_at"`
			Proof          *struct {
				Scheme        string          `json:"scheme"`
				SHA256        string          `json:"sha256"`
				SourceSHA256  string          `json:"source_sha256"`
				Revision      int64           `json:"owner_preview_revision"`
				RequestSHA256 string          `json:"owner_request_sha256"`
				Target        json.RawMessage `json:"target"`
			} `json:"authorization_proof"`
		}
		if json.Unmarshal(owner, &v) != nil || v.ID != ss.OwnerSessionID || v.OrganizationID != ss.OrganizationID || v.Revision != ss.OwnerRevision || v.State != "preview" || v.ExpiresAt.IsZero() ||
			v.Proof == nil || v.Proof.SourceSHA256 != ss.SourceHash || v.Proof.Revision != ss.OwnerRevision {
			return b, api(409, "owner_authorization_proof_required")
		}
		b.OwnerProof = operationProof{Scheme: v.Proof.Scheme, ValidationSHA256: v.Proof.SHA256}
		b.OwnerRequestSHA256 = v.Proof.RequestSHA256
		if !validateOperationProof(b.OwnerProof, "cards.xlsx.v1") {
			return b, api(409, "owner_authorization_proof_required")
		}
		var target struct {
			Kind        string `json:"kind"`
			DirectionID string `json:"direction_id"`
			ThemeID     string `json:"theme_id"`
			Name        string `json:"name"`
			Policy      string `json:"collision_policy"`
		}
		if json.Unmarshal(v.Proof.Target, &target) != nil || !canonicalPositiveID(target.DirectionID) {
			return b, api(409, "owner_authorization_proof_required")
		}
		switch target.Kind {
		case "existing_theme":
			if !canonicalPositiveID(target.ThemeID) || target.Name != "" || target.Policy != "" {
				return b, api(409, "owner_authorization_proof_required")
			}
			b.Target, _ = json.Marshal(map[string]string{"kind": target.Kind, "direction_id": target.DirectionID, "theme_id": target.ThemeID})
		case "create_theme":
			if target.ThemeID != "" || target.Name == "" || target.Policy != "reuse_existing" {
				return b, api(409, "owner_authorization_proof_required")
			}
			b.Target, _ = json.Marshal(map[string]string{"kind": target.Kind, "direction_id": target.DirectionID, "name": target.Name, "collision_policy": target.Policy})
		default:
			return b, api(409, "owner_authorization_proof_required")
		}
		expiry := v.ExpiresAt.UTC().Truncate(time.Microsecond)
		b.OwnerPreviewExpiresAt = &expiry
	case "tests.json", "tests.external-results":
		var v struct {
			PreviewHash string `json:"preview_hash"`
			Proof       *struct {
				SchemaVersion int `json:"schema_version"`
				operationProof
			} `json:"owner_proof"`
		}
		if json.Unmarshal(owner, &v) != nil || quote == nil || quote.NativeCommandKey != p.Command.NativeKey || v.Proof == nil ||
			v.Proof.SchemaVersion != 1 || quote.OwnerProof.SchemaVersion != 1 || v.PreviewHash != quote.OwnerProof.ValidationSHA256 {
			return b, api(409, "owner_authorization_proof_required")
		}
		old, _ := operationCanonical(v.Proof.operationProof)
		fresh, _ := operationCanonical(quote.OwnerProof.operationProof)
		if string(old) != string(fresh) {
			return b, api(409, "owner_preview_modified")
		}
		b.OwnerProof = quote.OwnerProof.operationProof
		b.OwnerRequestSHA256 = quote.OwnerRequestSHA256
		scheme := ss.Type + ".v1"
		if !validateOperationProof(b.OwnerProof, scheme) || b.OwnerProof.InputSHA256 == nil || !p.PreviewBodySHA256.Valid || digest(p.PreviewBody) != p.PreviewBodySHA256.String {
			return b, api(409, "owner_authorization_proof_required")
		}
		b.PreviewWireBodySHA256 = stringPointer(p.PreviewBodySHA256.String)
		if ss.Type == "tests.json" {
			var input struct {
				DirectionID int64 `json:"direction_id"`
			}
			if json.Unmarshal(p.PreviewBody, &input) != nil || input.DirectionID < 1 || b.OwnerProof.MappingSHA256 != nil {
				return b, api(409, "owner_authorization_proof_required")
			}
			b.Target, _ = json.Marshal(map[string]string{"kind": "create_tests", "direction_id": strconv.FormatInt(input.DirectionID, 10)})
		} else {
			var input struct {
				TestID   string `json:"test_id"`
				Revision int64  `json:"expected_test_revision"`
			}
			if json.Unmarshal(p.PreviewBody, &input) != nil || input.TestID != ss.TargetID || input.Revision < 1 || b.OwnerProof.MappingSHA256 == nil {
				return b, api(409, "owner_authorization_proof_required")
			}
			b.TargetRevision = stringPointer(strconv.FormatInt(input.Revision, 10))
			b.Target, _ = json.Marshal(map[string]string{"kind": "external_test", "test_id": ss.TargetID})
		}
	case "exams.questions", "exams.assignments", "exams.outside":
		var v struct {
			ID                   string    `json:"id"`
			Revision             int64     `json:"revision"`
			State                string    `json:"state"`
			OrganizationID       int64     `json:"organization_id"`
			DivisionID           int64     `json:"division_id"`
			AccountID            int64     `json:"account_id"`
			ExamID               int64     `json:"exam_id"`
			ExamRevision         int64     `json:"exam_revision"`
			SourceHash           string    `json:"source_hash"`
			ValidationHash       string    `json:"validation_hash"`
			ExpiresAt            time.Time `json:"expires_at"`
			ConnectedDivisionIDs []int64   `json:"connected_division_ids"`
		}
		if json.Unmarshal(owner, &v) != nil || v.ID != ss.OwnerSessionID || v.Revision != ss.OwnerRevision || v.State != "editable" || v.OrganizationID != ss.OrganizationID || v.DivisionID != ss.DivisionID ||
			v.AccountID != ss.ActorID || strconv.FormatInt(v.ExamID, 10) != ss.TargetID || v.ExamRevision < 1 || v.SourceHash != ss.SourceHash || v.ExpiresAt.IsZero() {
			return b, api(409, "owner_authorization_proof_required")
		}
		b.OwnerProof = operationProof{Scheme: "exams.import.v1", ValidationSHA256: v.ValidationHash}
		b.OwnerRequestSHA256 = p.Command.OwnerRequestSHA256
		if !validateOperationProof(b.OwnerProof, "exams.import.v1") {
			return b, api(409, "owner_authorization_proof_required")
		}
		b.TargetRevision = stringPointer(strconv.FormatInt(v.ExamRevision, 10))
		b.Target, _ = json.Marshal(map[string]string{"kind": "exam", "exam_id": ss.TargetID})
		expiry := v.ExpiresAt.UTC().Truncate(time.Microsecond)
		b.OwnerPreviewExpiresAt = &expiry
		if ss.Type == "exams.questions" {
			if len(v.ConnectedDivisionIDs) == 0 || len(v.ConnectedDivisionIDs) > 1000 {
				return b, api(409, "owner_authorization_proof_required")
			}
			sort.Slice(v.ConnectedDivisionIDs, func(i, j int) bool { return v.ConnectedDivisionIDs[i] < v.ConnectedDivisionIDs[j] })
			b.RequiredDivisionIDs = nil
			for i, id := range v.ConnectedDivisionIDs {
				if id < 1 || i > 0 && v.ConnectedDivisionIDs[i-1] == id {
					return b, api(409, "owner_authorization_proof_required")
				}
				b.RequiredDivisionIDs = append(b.RequiredDivisionIDs, strconv.FormatInt(id, 10))
			}
		}
	default:
		return b, api(409, "background_type_unavailable")
	}
	if !lowerHexReference(b.OwnerRequestSHA256, 64) {
		return b, api(409, "owner_authorization_proof_required")
	}
	return b, nil
}
