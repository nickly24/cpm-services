package tests

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var operationReference = regexp.MustCompile(`^[a-f0-9]{32}$`)
var operationSHA = regexp.MustCompile(`^[a-f0-9]{64}$`)
var operationUUID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
var operationExternalID = regexp.MustCompile(`^[a-f0-9]{24}$`)
var operationCoordinatorKey = regexp.MustCompile(`^[a-zA-Z0-9:_-]{8,128}$`)

type operationProof struct {
	Scheme           string  `json:"scheme"`
	ValidationSHA256 string  `json:"validation_sha256"`
	InputSHA256      *string `json:"input_sha256"`
	MappingSHA256    *string `json:"mapping_sha256"`
}
type operationTarget struct {
	Kind        string  `json:"kind"`
	DirectionID *string `json:"direction_id,omitempty"`
	TestID      *string `json:"test_id,omitempty"`
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
	Target                operationTarget `json:"target"`
	SourceSHA256          string          `json:"source_sha256"`
	OwnerProof            operationProof  `json:"owner_proof"`
	OwnerRequestSHA256    string          `json:"owner_request_sha256"`
	ApplyWireBodySHA256   string          `json:"apply_wire_body_sha256"`
	PreviewWireBodySHA256 *string         `json:"preview_wire_body_sha256"`
	UploadExpiresAt       string          `json:"upload_expires_at"`
	OwnerPreviewExpiresAt *string         `json:"owner_preview_expires_at"`
	RequiredDivisionIDs   []string        `json:"required_division_ids"`
}
type operationActor struct {
	ID int64 `json:"id"`
	Actor
}
type operationDivisionPermissions struct {
	DivisionID  int64                 `json:"division_id"`
	Permissions map[string]Permission `json:"permissions"`
}
type operationAuthorization struct {
	Status              bool                           `json:"status"`
	ContractVersion     int                            `json:"contract_version"`
	GrantID             string                         `json:"grant_id"`
	Mode                string                         `json:"mode"`
	ExpiresAt           string                         `json:"expires_at"`
	Actor               operationActor                 `json:"actor"`
	DivisionPermissions []operationDivisionPermissions `json:"division_permissions"`
	Binding             operationBinding               `json:"binding"`
}
type operationContext struct {
	Authorization operationAuthorization
	Wire          importWire
}
type operationContextKey struct{}

func importOperationContext(r *http.Request) *operationContext {
	value, _ := r.Context().Value(operationContextKey{}).(*operationContext)
	return value
}

func hasImportCarrier(h http.Header) bool {
	for name := range h {
		if strings.HasPrefix(strings.ToLower(name), "x-cpm-operation-") {
			return true
		}
	}
	return false
}

func singleOperationHeader(r *http.Request, name string) (string, error) {
	values := r.Header.Values(name)
	if len(values) != 1 || values[0] == "" || strings.TrimSpace(values[0]) != values[0] {
		return "", conflict("operation_request_mismatch")
	}
	return values[0], nil
}

func (s *Server) operationDelivery(r *http.Request) error {
	if len(s.Config.OperationDeliveryKey) < 32 || len(s.Config.OperationAuthKey) < 32 {
		return &APIError{503, "authorization_unavailable"}
	}
	if len(r.Header.Values("Authorization")) > 0 || len(r.Header.Values("X-Service-Key")) > 0 {
		return conflict("operation_request_mismatch")
	}
	if len(r.Header.Values("X-CPM-Import-Prepare-Key")) > 0 {
		return conflict("operation_request_mismatch")
	}
	allowed := map[string]bool{"x-cpm-operation-grant": true, "x-cpm-operation-stage": true, "x-cpm-operation-job": true, "x-cpm-operation-service-key": true}
	for name := range r.Header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-cpm-operation-") && !allowed[lower] {
			return conflict("operation_request_mismatch")
		}
	}
	if e := rejectImportSelectors(r); e != nil {
		return e
	}
	for _, name := range []string{"X-CPM-Operation-Grant", "X-CPM-Operation-Stage", "X-CPM-Operation-Job", "Idempotency-Key"} {
		if _, e := singleOperationHeader(r, name); e != nil {
			return e
		}
	}
	key, e := singleOperationHeader(r, "X-CPM-Operation-Service-Key")
	if e != nil {
		return e
	}
	if subtle.ConstantTimeCompare([]byte(key), []byte(s.Config.OperationDeliveryKey)) != 1 {
		return &APIError{401, "invalid_operation_credential"}
	}
	return nil
}

func rejectImportSelectors(r *http.Request) error {
	for name := range r.Header {
		for _, prefix := range []string{"x-actor", "x-account", "x-organization", "x-division", "x-target", "x-scope", "x-cpm-actor", "x-cpm-account", "x-cpm-org", "x-cpm-div", "x-cpm-user", "x-cpm-permission"} {
			if strings.HasPrefix(strings.ToLower(name), prefix) {
				return conflict("operation_request_mismatch")
			}
		}
	}
	return nil
}

func operationDecimal(value string) (int64, bool) {
	if value == "" || value[0] < '1' || value[0] > '9' {
		return 0, false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, e := strconv.ParseInt(value, 10, 64)
	return n, e == nil && n > 0
}

// Native owner authorizes directly with its own credential on every call.
// No actor, scope or permission is inferred from the caller's grant headers.
func (s *Server) identifyImport(r *http.Request) (Actor, error) {
	if !hasImportCarrier(r.Header) {
		return s.Dependencies.Identify(r)
	}
	if e := s.operationDelivery(r); e != nil {
		return Actor{}, e
	}
	wire, e := readImportWire(r)
	if e != nil {
		return Actor{}, e
	}
	if wire.Stage != "preview" && wire.Stage != "apply" && wire.Stage != "receipt" {
		return Actor{}, conflict("operation_request_mismatch")
	}
	grant, job, stage, key := r.Header.Get("X-CPM-Operation-Grant"), r.Header.Get("X-CPM-Operation-Job"), r.Header.Get("X-CPM-Operation-Stage"), r.Header.Get("Idempotency-Key")
	if !operationReference.MatchString(grant) || !operationReference.MatchString(job) || !operationUUID.MatchString(key) || !externalImportValidUUID(key) || stage != wire.Stage {
		return Actor{}, conflict("operation_request_mismatch")
	}
	if wire.Stage != "preview" {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(wire.Body, &fields)
		var command string
		if json.Unmarshal(fields["command_id"], &command) != nil || command != key {
			return Actor{}, conflict("operation_request_mismatch")
		}
	}
	in := struct {
		ContractVersion  int    `json:"contract_version"`
		JobID            string `json:"job_id"`
		Stage            string `json:"stage"`
		Method           string `json:"method"`
		Path             string `json:"path"`
		RawQuery         string `json:"raw_query"`
		ForceQuery       bool   `json:"force_query"`
		WireBodySHA256   string `json:"wire_body_sha256"`
		NativeCommandKey string `json:"native_command_key"`
	}{1, job, stage, r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.URL.ForceQuery, wire.SHA256, key}
	body, _ := json.Marshal(in)
	req, e := http.NewRequestWithContext(r.Context(), "POST", s.Dependencies.AuthURL+"/internal/auth/operation-grants/"+grant+"/authorize", bytes.NewReader(body))
	if e != nil {
		return Actor{}, &APIError{503, "authorization_unavailable"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Key", s.Config.OperationAuthKey)
	res, e := s.Dependencies.HTTP.Do(req)
	if e != nil {
		return Actor{}, &APIError{503, "authorization_unavailable"}
	}
	defer res.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if e != nil || len(raw) > 1<<20 || uniqueImportJSON(raw) != nil {
		return Actor{}, &APIError{503, "authorization_unavailable"}
	}
	if res.StatusCode != 200 {
		var failure ErrorDTO
		if exactImportJSONKeys(raw, reflect.TypeOf(failure), true) != nil || json.Unmarshal(raw, &failure) != nil || failure.Status {
			return Actor{}, &APIError{503, "authorization_unavailable"}
		}
		allowed := map[int][]string{401: {"invalid_operation_credential"}, 403: {"operation_access_denied"}, 404: {"operation_not_found"}, 409: {"operation_request_mismatch", "operation_issuance_conflict"}, 410: {"operation_expired", "operation_revoked"}}
		for _, name := range allowed[res.StatusCode] {
			if failure.Error == name {
				return Actor{}, &APIError{res.StatusCode, name}
			}
		}
		return Actor{}, &APIError{503, "authorization_unavailable"}
	}
	var authorization operationAuthorization
	if exactImportJSONKeys(raw, reflect.TypeOf(authorization), true) != nil {
		return Actor{}, &APIError{503, "authorization_unavailable"}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&authorization) != nil || d.Decode(new(any)) != io.EOF {
		return Actor{}, &APIError{503, "authorization_unavailable"}
	}
	if e := validateOperationAuthorization(authorization, wire, grant, job, key); e != nil {
		return Actor{}, e
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	var binding map[string]json.RawMessage
	_ = json.Unmarshal(fields["binding"], &binding)
	var target map[string]json.RawMessage
	_ = json.Unmarshal(binding["target"], &target)
	if len(target) != 2 {
		return Actor{}, conflict("operation_request_mismatch")
	}
	if wire.Type == "tests.json" {
		if _, ok := target["direction_id"]; !ok {
			return Actor{}, conflict("operation_request_mismatch")
		}
	} else {
		if _, ok := target["test_id"]; !ok {
			return Actor{}, conflict("operation_request_mismatch")
		}
	}
	ctx := context.WithValue(r.Context(), operationContextKey{}, &operationContext{authorization, wire})
	*r = *r.WithContext(ctx)
	return authorization.Actor.Actor, nil
}

func validateOperationAuthorization(a operationAuthorization, wire importWire, grant, job, key string) error {
	mismatch := func() error { return conflict("operation_request_mismatch") }
	b := a.Binding
	actor := a.Actor.Actor
	if !a.Status || a.ContractVersion != 1 || b.ContractVersion != 1 || a.GrantID != grant || b.JobID != job || b.NativeCommandKey != key || b.Type != wire.Type || b.Owner != "tests" || a.Actor.ID < 1 || actor.AccountID < 1 || actor.OrganizationID < 1 || actor.DivisionID == nil || *actor.DivisionID < 1 || actor.Role != "staff_admin" && actor.Role != "admin" || actor.OrganizationAdmin && actor.Role != "admin" {
		return mismatch()
	}
	// Profile identity and account identity are distinct. Tests receipts bind
	// the real account; the complete current origin context comes from Auth.
	if len(actor.DivisionIDs) > 1000 || len(a.DivisionPermissions) > 1000 {
		return mismatch()
	}
	member, contextFound := false, false
	seen := map[int64]bool{}
	for _, id := range actor.DivisionIDs {
		if id < 1 || seen[id] {
			return mismatch()
		}
		seen[id] = true
		member = member || id == *actor.DivisionID
	}
	seen = map[int64]bool{}
	for _, current := range a.DivisionPermissions {
		if current.DivisionID < 1 || seen[current.DivisionID] || current.Permissions == nil {
			return mismatch()
		}
		seen[current.DivisionID] = true
		if current.DivisionID == *actor.DivisionID {
			contextFound = reflect.DeepEqual(current.Permissions, actor.Permissions)
		}
	}
	if !member || !contextFound {
		return mismatch()
	}
	if a.Mode != "apply" && a.Mode != "receipt" || a.Mode == "receipt" && wire.Stage != "receipt" {
		return mismatch()
	}
	deadline, e := time.Parse(time.RFC3339Nano, a.ExpiresAt)
	if e != nil || !strings.HasSuffix(a.ExpiresAt, "Z") {
		return mismatch()
	}
	if !time.Now().Before(deadline) {
		return &APIError{410, "operation_expired"}
	}
	if !operationReference.MatchString(b.UploadID) || !operationReference.MatchString(b.UploadSessionID) || !operationCoordinatorKey.MatchString(b.CoordinatorCommandKey) || b.OwnerSessionID != nil || b.OwnerPreviewRevision != nil || b.OwnerPreviewExpiresAt != nil || !operationSHA.MatchString(b.SourceSHA256) || !operationSHA.MatchString(b.OwnerRequestSHA256) || !operationSHA.MatchString(b.ApplyWireBodySHA256) || b.PreviewWireBodySHA256 == nil || !operationSHA.MatchString(*b.PreviewWireBodySHA256) {
		return mismatch()
	}
	if _, valid := operationDecimal(b.UploadPreviewRevision); !valid {
		return mismatch()
	}
	if _, e := time.Parse(time.RFC3339Nano, b.UploadExpiresAt); e != nil || !strings.HasSuffix(b.UploadExpiresAt, "Z") {
		return mismatch()
	}
	if len(b.RequiredDivisionIDs) != 1 || b.RequiredDivisionIDs[0] != strconv.FormatInt(*actor.DivisionID, 10) {
		return mismatch()
	}
	want := b.ApplyWireBodySHA256
	if wire.Stage == "preview" {
		want = *b.PreviewWireBodySHA256
	}
	if want != wire.SHA256 {
		return mismatch()
	}
	if b.OwnerProof.InputSHA256 == nil || !operationSHA.MatchString(*b.OwnerProof.InputSHA256) || !operationSHA.MatchString(b.OwnerProof.ValidationSHA256) {
		return mismatch()
	}
	if wire.Type == "tests.json" {
		if b.Target.Kind != "create_tests" || b.Target.DirectionID == nil || b.Target.TestID != nil || b.TargetRevision != nil || b.OwnerProof.Scheme != "tests.json.v1" || b.OwnerProof.MappingSHA256 != nil {
			return mismatch()
		}
		if _, valid := operationDecimal(*b.Target.DirectionID); !valid {
			return mismatch()
		}
		if !actor.Can(true) || !actorUpload(actor, true) {
			return denied
		}
	} else {
		if b.Target.Kind != "external_test" || b.Target.TestID == nil || b.Target.DirectionID != nil || !operationExternalID.MatchString(*b.Target.TestID) || b.TargetRevision == nil || b.OwnerProof.Scheme != "tests.external-results.v1" || b.OwnerProof.MappingSHA256 == nil || !operationSHA.MatchString(*b.OwnerProof.MappingSHA256) {
			return mismatch()
		}
		if _, valid := operationDecimal(*b.TargetRevision); !valid {
			return mismatch()
		}
		if !actorResults(actor, true) || !actorUpload(actor, true) {
			return denied
		}
	}
	return nil
}
