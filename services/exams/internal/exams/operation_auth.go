package exams

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
)

type operationAuthorizeWire struct {
	ContractVersion  int    `json:"contract_version"`
	JobID            string `json:"job_id"`
	Stage            string `json:"stage"`
	Method           string `json:"method"`
	Path             string `json:"path"`
	RawQuery         string `json:"raw_query"`
	ForceQuery       bool   `json:"force_query"`
	BodySHA256       string `json:"wire_body_sha256"`
	NativeCommandKey string `json:"native_command_key"`
}

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
type operationProofWire struct {
	Scheme           string  `json:"scheme"`
	ValidationSHA256 string  `json:"validation_sha256"`
	InputSHA256      *string `json:"input_sha256"`
	MappingSHA256    *string `json:"mapping_sha256"`
}
type operationTargetWire struct {
	Kind   string `json:"kind"`
	ExamID string `json:"exam_id"`
}
type operationBindingWire struct {
	ContractVersion       int                 `json:"contract_version"`
	UploadID              string              `json:"upload_id"`
	UploadSessionID       string              `json:"upload_session_id"`
	JobID                 string              `json:"job_id"`
	CoordinatorCommandKey string              `json:"coordinator_command_key"`
	Type                  string              `json:"type"`
	Owner                 string              `json:"owner"`
	NativeCommandKey      string              `json:"native_command_key"`
	OwnerSessionID        string              `json:"owner_session_id"`
	UploadPreviewRevision string              `json:"upload_preview_revision"`
	OwnerPreviewRevision  *string             `json:"owner_preview_revision"`
	TargetRevision        *string             `json:"target_revision"`
	Target                operationTargetWire `json:"target"`
	SourceSHA256          string              `json:"source_sha256"`
	OwnerProof            operationProofWire  `json:"owner_proof"`
	OwnerRequestSHA256    string              `json:"owner_request_sha256"`
	ApplyWireBodySHA256   string              `json:"apply_wire_body_sha256"`
	PreviewWireBodySHA256 *string             `json:"preview_wire_body_sha256"`
	UploadExpiresAt       string              `json:"upload_expires_at"`
	OwnerPreviewExpiresAt *string             `json:"owner_preview_expires_at"`
	RequiredDivisionIDs   []string            `json:"required_division_ids"`
}
type operationResponseWire struct {
	Status              bool               `json:"status"`
	ContractVersion     int                `json:"contract_version"`
	GrantID             string             `json:"grant_id"`
	Mode                string             `json:"mode"`
	ExpiresAt           string             `json:"expires_at"`
	Actor               operationActorWire `json:"actor"`
	DivisionPermissions []struct {
		DivisionID  int64                 `json:"division_id"`
		Permissions map[string]Permission `json:"permissions"`
	} `json:"division_permissions"`
	Binding operationBindingWire `json:"binding"`
}

// Go's JSON decoder accepts case-insensitive field names. The grant boundary
// requires exact names, including nested objects and permission entries.
func operationJSONFields(raw json.RawMessage, t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return true
	}
	switch t.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return false
		}
		allowed := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				allowed[name] = f.Type
			}
		}
		if len(fields) != len(allowed) {
			return false
		}
		for key, value := range fields {
			ft, ok := allowed[key]
			if !ok || !operationJSONFields(value, ft) {
				return false
			}
		}
	case reflect.Map:
		var values map[string]json.RawMessage
		if json.Unmarshal(raw, &values) != nil || values == nil {
			return false
		}
		for key, value := range values {
			if key != strings.ToLower(key) || !operationJSONFields(value, t.Elem()) {
				return false
			}
		}
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return true
		}
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return false
		}
		for _, value := range values {
			if !operationJSONFields(value, t.Elem()) {
				return false
			}
		}
	}
	return true
}

func decodeOperationJSON(raw []byte, out any) error {
	if !utf8.Valid(raw) {
		return Bad("invalid_json")
	}
	if err := DecodeStrict(raw, out); err != nil {
		return err
	}
	if !operationJSONFields(raw, reflect.TypeOf(out)) {
		return Bad("invalid_request")
	}
	return nil
}

func (d *Dependencies) AuthorizeOperation(r *http.Request, in OperationRequest) (OperationAuthorization, error) {
	var out OperationAuthorization
	if len(d.Config.OperationsAuthKey) < 32 {
		return out, &APIError{503, "authorization_unavailable"}
	}
	wire := operationAuthorizeWire{ContractVersion: 1, JobID: in.JobID, Stage: in.Stage, Method: in.Method, Path: r.URL.EscapedPath(), RawQuery: in.RawQuery, ForceQuery: r.URL.ForceQuery, BodySHA256: in.BodySHA256, NativeCommandKey: r.Header.Get("Idempotency-Key")}
	raw, err := json.Marshal(wire)
	if err != nil {
		return out, &APIError{503, "authorization_unavailable"}
	}
	req, err := http.NewRequestWithContext(r.Context(), "POST", d.Config.AuthURL+"/internal/auth/operation-grants/"+in.GrantID+"/authorize", bytes.NewReader(raw))
	if err != nil {
		return out, &APIError{503, "authorization_unavailable"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Key", d.Config.OperationsAuthKey)
	response, err := d.HTTP.Do(req)
	if err != nil {
		return out, &APIError{503, "authorization_unavailable"}
	}
	defer response.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		return out, &APIError{503, "authorization_unavailable"}
	}
	if response.StatusCode != 200 {
		var denied struct {
			Status bool   `json:"status"`
			Error  string `json:"error"`
		}
		if decodeOperationJSON(raw, &denied) != nil || denied.Status {
			return out, &APIError{503, "authorization_unavailable"}
		}
		codes := map[string]int{"invalid_operation_credential": 401, "operation_access_denied": 403, "operation_not_found": 404, "operation_request_mismatch": 409, "operation_expired": 410, "operation_revoked": 410, "authorization_unavailable": 503}
		if code, ok := codes[denied.Error]; ok && code == response.StatusCode {
			return out, &APIError{code, denied.Error}
		}
		return out, &APIError{503, "authorization_unavailable"}
	}
	var positive operationResponseWire
	if decodeOperationJSON(raw, &positive) != nil {
		return out, &APIError{503, "invalid_operation_authorization"}
	}
	return normalizeOperationResponse(in, positive)
}

func normalizeOperationResponse(in OperationRequest, p operationResponseWire) (OperationAuthorization, error) {
	bad := &APIError{503, "invalid_operation_authorization"}
	var out OperationAuthorization
	b := p.Binding
	if !p.Status || p.ContractVersion != 1 || p.GrantID != in.GrantID || !oneOf(p.Mode, "apply", "receipt") || p.Mode == "receipt" && in.Stage != "receipt" || b.ContractVersion != 1 || b.JobID != in.JobID || b.Owner != "exams" || b.Type != "exams."+in.Kind || b.Target.Kind != "exam" || b.NativeCommandKey != in.NativeCommandKey || b.OwnerSessionID != in.SessionID || b.OwnerProof.Scheme != "exams.import.v1" || b.OwnerProof.InputSHA256 != nil || b.OwnerProof.MappingSHA256 != nil || b.PreviewWireBodySHA256 != nil || b.OwnerPreviewRevision == nil || b.TargetRevision == nil || b.OwnerPreviewExpiresAt == nil || !canonicalHexID(b.UploadID) || !canonicalHexID(b.UploadSessionID) || !operationCoordinatorKey(b.CoordinatorCommandKey) || !canonicalSHA256(b.ApplyWireBodySHA256) {
		return out, bad
	}
	expiry, err := time.Parse(time.RFC3339Nano, p.ExpiresAt)
	if err != nil || expiry.Location() != time.UTC || !expiry.After(time.Now().UTC()) {
		return out, bad
	}
	uploadExpiry, err := time.Parse(time.RFC3339Nano, b.UploadExpiresAt)
	if err != nil || uploadExpiry.Location() != time.UTC {
		return out, bad
	}
	previewExpiry, err := time.Parse(time.RFC3339Nano, *b.OwnerPreviewExpiresAt)
	if err != nil || previewExpiry.Location() != time.UTC {
		return out, bad
	}
	if p.Mode == "apply" && (expiry.After(uploadExpiry) || expiry.After(previewExpiry)) {
		return out, bad
	}
	exam, ok := canonicalPositive(b.Target.ExamID)
	if !ok || exam != in.ExamID {
		return out, bad
	}
	ownerRevision, ok := canonicalPositive(*b.OwnerPreviewRevision)
	if !ok {
		return out, bad
	}
	targetRevision, ok := canonicalPositive(*b.TargetRevision)
	if !ok {
		return out, bad
	}
	if _, ok = canonicalPositive(b.UploadPreviewRevision); !ok {
		return out, bad
	}
	if in.Stage == "apply" && b.ApplyWireBodySHA256 != in.BodySHA256 {
		return out, Conflict("operation_request_mismatch")
	}
	a := p.Actor
	if a.ID < 1 {
		return out, bad
	}
	out.Request = in
	out.Actor = Actor{ID: a.ID, AccountID: a.AccountID, Role: a.Role, OrganizationID: a.OrganizationID, DivisionID: a.DivisionID, DivisionIDs: a.DivisionIDs, OrganizationAdmin: a.OrganizationAdmin, Permissions: a.Permissions}
	if a.DivisionID == nil {
		return out, bad
	}
	out.Binding = OperationBinding{OrganizationID: a.OrganizationID, AccountID: a.AccountID, DivisionID: *a.DivisionID, ExamID: exam, OwnerRevision: ownerRevision, TargetRevision: targetRevision, Kind: in.Kind, SessionID: b.OwnerSessionID, SourceHash: b.SourceSHA256, ValidationHash: b.OwnerProof.ValidationSHA256, NativeCommandKey: b.NativeCommandKey, NativeRequestHash: b.OwnerRequestSHA256}
	for _, raw := range b.RequiredDivisionIDs {
		id, ok := canonicalPositive(raw)
		if !ok {
			return out, bad
		}
		out.Binding.ConnectedDivisionIDs = append(out.Binding.ConnectedDivisionIDs, id)
	}
	out.DivisionPermissions = map[int64]Permission{}
	if len(p.DivisionPermissions) > 1000 {
		return out, bad
	}
	for _, scope := range p.DivisionPermissions {
		if scope.DivisionID < 1 {
			return out, bad
		}
		if _, exists := out.DivisionPermissions[scope.DivisionID]; exists {
			return out, bad
		}
		out.DivisionPermissions[scope.DivisionID] = scope.Permissions["exams"]
	}
	if err = validateOperationAuthorization(in, out); err != nil {
		return out, err
	}
	return out, nil
}

func operationCoordinatorKey(value string) bool {
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == ':' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
