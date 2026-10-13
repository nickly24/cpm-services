package cards

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type operationActorWire struct {
	ID                int64                 `json:"id"`
	AccountID         int64                 `json:"account_id"`
	Role              string                `json:"role"`
	OrganizationID    int64                 `json:"organization_id"`
	DivisionID        *int64                `json:"division_id"`
	DivisionIDs       []int64               `json:"division_ids"`
	OrganizationAdmin bool                  `json:"organization_admin"`
	Permissions       map[string]Permission `json:"permissions"`
}

type operationDivisionWire struct {
	DivisionID  int64                 `json:"division_id"`
	Permissions map[string]Permission `json:"permissions"`
}

type operationProofWire struct {
	Scheme           string  `json:"scheme"`
	ValidationSHA256 string  `json:"validation_sha256"`
	InputSHA256      *string `json:"input_sha256"`
	MappingSHA256    *string `json:"mapping_sha256"`
}

type operationBindingWire struct {
	ContractVersion       int                `json:"contract_version"`
	UploadID              string             `json:"upload_id"`
	UploadSessionID       string             `json:"upload_session_id"`
	JobID                 string             `json:"job_id"`
	CoordinatorCommandKey string             `json:"coordinator_command_key"`
	Type                  string             `json:"type"`
	Owner                 string             `json:"owner"`
	NativeCommandKey      string             `json:"native_command_key"`
	OwnerSessionID        *string            `json:"owner_session_id"`
	UploadPreviewRevision string             `json:"upload_preview_revision"`
	OwnerPreviewRevision  *string            `json:"owner_preview_revision"`
	TargetRevision        *string            `json:"target_revision"`
	Target                ImportTarget       `json:"target"`
	SourceSHA256          string             `json:"source_sha256"`
	OwnerProof            operationProofWire `json:"owner_proof"`
	OwnerRequestSHA256    string             `json:"owner_request_sha256"`
	ApplyWireBodySHA256   string             `json:"apply_wire_body_sha256"`
	PreviewWireBodySHA256 *string            `json:"preview_wire_body_sha256"`
	UploadExpiresAt       time.Time          `json:"upload_expires_at"`
	OwnerPreviewExpiresAt *time.Time         `json:"owner_preview_expires_at"`
	RequiredDivisionIDs   []string           `json:"required_division_ids"`
}

type operationResponse struct {
	Status              bool                    `json:"status"`
	ContractVersion     int                     `json:"contract_version"`
	GrantID             string                  `json:"grant_id"`
	Mode                string                  `json:"mode"`
	ExpiresAt           time.Time               `json:"expires_at"`
	Actor               operationActorWire      `json:"actor"`
	DivisionPermissions []operationDivisionWire `json:"division_permissions"`
	Binding             operationBindingWire    `json:"binding"`
}

type operationAuthorizeRequest struct {
	ContractVersion  int    `json:"contract_version"`
	JobID            string `json:"job_id"`
	Stage            string `json:"stage"`
	Method           string `json:"method"`
	Path             string `json:"path"`
	RawQuery         string `json:"raw_query"`
	ForceQuery       bool   `json:"force_query"`
	WireBodySHA256   string `json:"wire_body_sha256"`
	NativeCommandKey string `json:"native_command_key"`
}

func operationDecimal(s string) (int64, bool) {
	n, e := strconv.ParseInt(s, 10, 64)
	return n, e == nil && n > 0 && strconv.FormatInt(n, 10) == s
}

// This client never forwards the incoming delivery credential or a user bearer.
func (d *Dependencies) operationCall(req *http.Request, out any) error {
	res, e := d.HTTP.Do(req)
	if e != nil {
		return &APIError{503, "authorization_unavailable"}
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		switch res.StatusCode {
		case 401:
			return &APIError{401, "invalid_operation_credential"}
		case 403:
			return &APIError{403, "operation_access_denied"}
		case 404:
			return &APIError{404, "operation_not_found"}
		case 409:
			return &APIError{409, "operation_request_mismatch"}
		case 410:
			var failure struct {
				Status bool   `json:"status"`
				Error  string `json:"error"`
			}
			raw, _ := io.ReadAll(io.LimitReader(res.Body, 4097))
			message := "operation_expired"
			if len(raw) <= 4096 && strictOperationJSON(raw, &failure) == nil && failure.Error == "operation_revoked" {
				message = "operation_revoked"
			}
			return &APIError{410, message}
		default:
			return &APIError{503, "authorization_unavailable"}
		}
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if e != nil || len(raw) > 4<<20 || strictOperationJSON(raw, out) != nil {
		return &APIError{503, "authorization_unavailable"}
	}
	return nil
}

func (d *Dependencies) authorizeOperation(r *http.Request, op operationRequest) (Actor, Scope, operationBinding, error) {
	input := operationAuthorizeRequest{1, op.JobID, op.Stage, r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.URL.ForceQuery, op.BodyHash, op.NativeKey}
	raw, _ := json.Marshal(input)
	req, e := http.NewRequestWithContext(r.Context(), "POST", d.Config.AuthURL+"/internal/auth/operation-grants/"+op.GrantID+"/authorize", bytes.NewReader(raw))
	if e != nil {
		return Actor{}, Scope{}, operationBinding{}, unavailable
	}
	req.Header.Set("X-Service-Key", d.Config.OperationAuthKey)
	req.Header.Set("Content-Type", "application/json")
	var out operationResponse
	if e = d.operationCall(req, &out); e != nil {
		return Actor{}, Scope{}, operationBinding{}, e
	}
	a, b, e := out.ownerBinding(op)
	if e != nil {
		return Actor{}, Scope{}, operationBinding{}, e
	}
	scope, e := d.operationDirection(r, a, op, b)
	return a, scope, b, e
}

func (out operationResponse) ownerBinding(op operationRequest) (Actor, operationBinding, error) {
	w := out.Binding
	badResponse := func() (Actor, operationBinding, error) {
		return Actor{}, operationBinding{}, &APIError{503, "authorization_unavailable"}
	}
	if !out.Status || out.ContractVersion != 1 || out.GrantID != op.GrantID || w.ContractVersion != 1 || w.Owner != "cards" || w.Type != "cards.xlsx" ||
		!lowerHex(w.UploadID, 32) || !lowerHex(w.UploadSessionID, 32) ||
		w.OwnerSessionID == nil || !lowerHex(*w.OwnerSessionID, 32) || w.OwnerPreviewRevision == nil || w.TargetRevision != nil ||
		w.OwnerProof.Scheme != "cards.xlsx.v1" || w.OwnerProof.InputSHA256 != nil || w.OwnerProof.MappingSHA256 != nil ||
		!lowerHex(w.OwnerProof.ValidationSHA256, 64) || !lowerHex(w.SourceSHA256, 64) || !lowerHex(w.OwnerRequestSHA256, 64) ||
		!lowerHex(w.ApplyWireBodySHA256, 64) || w.PreviewWireBodySHA256 != nil || w.OwnerPreviewExpiresAt == nil ||
		(out.Mode != "apply" && out.Mode != "receipt") || out.Actor.ID < 1 || out.Actor.DivisionID == nil {
		return badResponse()
	}
	if _, _, e := commandIdentity(Actor{}, w.CoordinatorCommandKey, "", nil); e != nil {
		return badResponse()
	}
	if !out.ExpiresAt.After(time.Now()) {
		return Actor{}, operationBinding{}, &APIError{410, "operation_expired"}
	}
	if out.Mode == "apply" && (out.ExpiresAt.After(w.UploadExpiresAt) || out.ExpiresAt.After(*w.OwnerPreviewExpiresAt)) {
		return badResponse()
	}
	revision, ok := operationDecimal(*w.OwnerPreviewRevision)
	if !ok {
		return badResponse()
	}
	if _, ok = operationDecimal(w.UploadPreviewRevision); !ok {
		return badResponse()
	}
	if _, ok = operationDecimal(w.Target.DirectionID); !ok {
		return badResponse()
	}
	switch w.Target.Kind {
	case "existing_theme":
		if _, ok = operationDecimal(w.Target.ThemeID); !ok || w.Target.Name != "" || w.Target.CollisionPolicy != "" {
			return badResponse()
		}
	case "create_theme":
		if w.Target.ThemeID != "" || !validText(w.Target.Name, 200) || strings.TrimSpace(w.Target.Name) != w.Target.Name || w.Target.CollisionPolicy != "reuse_existing" {
			return badResponse()
		}
	default:
		return badResponse()
	}
	a := Actor{AccountID: out.Actor.AccountID, OrganizationID: out.Actor.OrganizationID, DivisionID: out.Actor.DivisionID, Role: out.Actor.Role, OrganizationAdmin: out.Actor.OrganizationAdmin, Permissions: out.Actor.Permissions}
	if len(w.RequiredDivisionIDs) != 1 || w.RequiredDivisionIDs[0] != strconv.FormatInt(*a.DivisionID, 10) {
		return badResponse()
	}
	member := false
	seen := map[int64]bool{}
	for _, id := range out.Actor.DivisionIDs {
		if id < 1 || seen[id] {
			return badResponse()
		}
		seen[id] = true
		member = member || id == *a.DivisionID
	}
	if !member {
		return badResponse()
	}
	selected := false
	seen = map[int64]bool{}
	for _, context := range out.DivisionPermissions {
		if context.DivisionID < 1 || seen[context.DivisionID] {
			return badResponse()
		}
		seen[context.DivisionID] = true
		if context.DivisionID == *a.DivisionID {
			selected = true
			if a.Role != "admin" && (!context.Permissions["train"].Edit || !context.Permissions["upload"].Edit) {
				return Actor{}, operationBinding{}, denied
			}
		}
	}
	if !selected {
		return badResponse()
	}
	request, _ := json.Marshal(RevisionDTO{ExpectedRevision: revision})
	digest := sha256.Sum256(request)
	if w.OwnerRequestSHA256 != hex.EncodeToString(digest[:]) {
		return Actor{}, operationBinding{}, &APIError{409, "operation_request_mismatch"}
	}
	if op.Stage == "apply" && w.ApplyWireBodySHA256 != op.BodyHash {
		return Actor{}, operationBinding{}, &APIError{409, "operation_request_mismatch"}
	}
	b := operationBinding{GrantID: out.GrantID, JobID: w.JobID, NativeKey: w.NativeCommandKey, SessionID: *w.OwnerSessionID, AccountID: a.AccountID, OrganizationID: a.OrganizationID, DivisionID: *a.DivisionID, Revision: revision, ReceiptOnly: out.Mode == "receipt",
		Proof: ImportProof{Scheme: w.OwnerProof.Scheme, SHA256: w.OwnerProof.ValidationSHA256, SourceSHA256: w.SourceSHA256, OwnerPreviewRevision: revision, OwnerRequestSHA256: w.OwnerRequestSHA256, Target: w.Target}}
	if e := b.validateActor(a, op); e != nil {
		return Actor{}, operationBinding{}, e
	}
	return a, b, nil
}

func (d *Dependencies) operationDirection(r *http.Request, a Actor, op operationRequest, b operationBinding) (Scope, error) {
	req, e := http.NewRequestWithContext(r.Context(), "GET", d.Config.CoreURL+"/internal/core/import-directions/"+b.Proof.Target.DirectionID, nil)
	if e != nil {
		return Scope{}, unavailable
	}
	req.Header.Set(operationGrantHeader, op.GrantID)
	req.Header.Set(operationStageHeader, "core-direction")
	req.Header.Set(operationJobHeader, op.JobID)
	req.Header.Set(operationKeyHeader, d.Config.OperationCoreKey)
	req.Header.Set("Idempotency-Key", op.NativeKey)
	var out struct {
		Status bool `json:"status"`
		Data   struct {
			AccountID      int64  `json:"account_id"`
			OrganizationID int64  `json:"organization_id"`
			DivisionID     int64  `json:"division_id"`
			JobID          string `json:"job_id"`
			SessionID      string `json:"owner_session_id"`
			DirectionID    string `json:"direction_id"`
			Active         bool   `json:"active"`
		} `json:"data"`
	}
	if e = d.operationCall(req, &out); e != nil {
		return Scope{}, e
	}
	data := out.Data
	if !out.Status || !data.Active || data.AccountID != a.AccountID || data.OrganizationID != a.OrganizationID || a.DivisionID == nil || data.DivisionID != *a.DivisionID || data.JobID != op.JobID || data.SessionID != b.SessionID || data.DirectionID != b.Proof.Target.DirectionID {
		return Scope{}, &APIError{503, "authorization_unavailable"}
	}
	direction, _ := operationDecimal(data.DirectionID)
	return Scope{OrganizationID: a.OrganizationID, Directions: []Direction{{ID: direction}}}, nil
}
