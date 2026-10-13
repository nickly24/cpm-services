package uploads

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// The v1 Auth binding contains digests and typed references, never native rows,
// passwords or a browser bearer. Native exact bytes live only in our private plan.
type operationProof struct {
	Scheme           string  `json:"scheme"`
	ValidationSHA256 string  `json:"validation_sha256"`
	InputSHA256      *string `json:"input_sha256"`
	MappingSHA256    *string `json:"mapping_sha256"`
}

type operationBinding struct {
	ContractVersion       int             `json:"contract_version"`
	UploadID              string          `json:"upload_id"`
	UploadSessionID       string          `json:"upload_session_id"`
	JobID                 string          `json:"job_id"`
	CoordinatorCommandKey string          `json:"coordinator_command_key"`
	Type                  string          `json:"type"`
	Owner                 string          `json:"owner"`
	NativeCommandKey      string          `json:"native_command_key"`
	OwnerSessionID        *string         `json:"owner_session_id"`
	UploadPreviewRevision string          `json:"upload_preview_revision"`
	OwnerPreviewRevision  *string         `json:"owner_preview_revision"`
	TargetRevision        *string         `json:"target_revision"`
	Target                json.RawMessage `json:"target"`
	SourceSHA256          string          `json:"source_sha256"`
	OwnerProof            operationProof  `json:"owner_proof"`
	OwnerRequestSHA256    string          `json:"owner_request_sha256"`
	ApplyWireBodySHA256   string          `json:"apply_wire_body_sha256"`
	PreviewWireBodySHA256 *string         `json:"preview_wire_body_sha256"`
	UploadExpiresAt       time.Time       `json:"upload_expires_at"`
	OwnerPreviewExpiresAt *time.Time      `json:"owner_preview_expires_at"`
	RequiredDivisionIDs   []string        `json:"required_division_ids"`
}

type operationActor struct {
	Actor
	ID          int64   `json:"id"`
	DivisionIDs []int64 `json:"division_ids"`
}

type operationEnvelope struct {
	Status              bool           `json:"status"`
	ContractVersion     int            `json:"contract_version"`
	GrantID             string         `json:"grant_id"`
	Mode                string         `json:"mode"`
	ExpiresAt           time.Time      `json:"expires_at"`
	Actor               operationActor `json:"actor"`
	DivisionPermissions []struct {
		DivisionID  int64                 `json:"division_id"`
		Permissions map[string]Permission `json:"permissions"`
	} `json:"division_permissions"`
	Binding operationBinding `json:"binding"`
}

func operationCanonical(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return stagedCanonical(raw)
}

func sameOperationBinding(a, b operationBinding) bool {
	x, xe := operationCanonical(a)
	y, ye := operationCanonical(b)
	return xe == nil && ye == nil && bytes.Equal(x, y)
}

func verifyIssuedOperation(env operationEnvelope, binding operationBinding, sc Scope, mode string, now time.Time) error {
	if !env.Status || env.ContractVersion != 1 || !lowerHexReference(env.GrantID, 32) || env.Mode != mode ||
		env.Actor.ID < 1 || env.Actor.AccountID != sc.ActorID || env.Actor.OrganizationID != sc.OrganizationID ||
		env.Actor.DivisionID == nil || *env.Actor.DivisionID != sc.DivisionID || !sameOperationBinding(env.Binding, binding) ||
		!env.ExpiresAt.After(now) || env.ExpiresAt.After(now.Add(24*time.Hour)) {
		return api(503, "operation_invalid_auth_response")
	}
	if mode == "apply" && (env.ExpiresAt.After(binding.UploadExpiresAt) || binding.OwnerPreviewExpiresAt != nil && env.ExpiresAt.After(*binding.OwnerPreviewExpiresAt)) {
		return api(503, "operation_invalid_auth_response")
	}
	t, err := findType(binding.Type)
	if err != nil || permission(env.Actor.Actor, t, true) != nil {
		return api(403, "operation_access_denied")
	}
	return nil
}

// Only the confirming HTTP request may issue a grant. This client does not
// retain the bearer or expose an authorize method using an issuer credential.
func (s *Server) requestOperationGrant(ctx context.Context, bearer, issuanceKey, path string, body []byte) (operationEnvelope, error) {
	var out operationEnvelope
	if !s.Config.OperationsEnabled || len(s.Config.OperationsIssuerKey) < 32 || !canonicalUUID(issuanceKey) ||
		!strings.HasPrefix(bearer, "Bearer ") || len(body) > 32<<10 {
		return out, api(503, "background_authorization_pending")
	}
	if path != "/internal/auth/operation-grants" {
		parts := strings.Split(path, "/")
		if len(parts) != 6 || parts[1] != "internal" || parts[2] != "auth" || parts[3] != "operation-grants" || !lowerHexReference(parts[4], 32) || parts[5] != "recover" {
			return out, api(400, "invalid_operation_issue_path")
		}
		if !bytes.Equal(body, []byte("{}")) {
			return out, api(400, "invalid_operation_recovery_body")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Config.AuthURL+path, bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", bearer)
	req.Header.Set("X-Service-Key", s.Config.OperationsIssuerKey)
	req.Header.Set("Idempotency-Key", issuanceKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := s.HTTP.Do(req)
	if err != nil {
		return out, api(503, "operation_issuance_unknown")
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		return out, api(503, "operation_issuance_unknown")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		switch res.StatusCode {
		case 401, 403:
			return out, api(403, "operation_access_denied")
		case 404:
			return out, api(404, "operation_not_found")
		case 409:
			return out, api(409, "operation_issuance_conflict")
		case 410:
			return out, api(410, "operation_expired")
		default:
			return out, api(503, "operation_issuance_unknown")
		}
	}
	if decodeOperationJSON(raw, &out) != nil {
		return out, api(503, "operation_invalid_auth_response")
	}
	return out, nil
}
