package core

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var coreOperationPath = regexp.MustCompile(`^/v1/core/divisions/([1-9][0-9]*)/(imports|legacy-imports)/([1-9][0-9]*)(/commit|/receipts/([a-f0-9-]{36}))?$`)
var coreDirectionPath = regexp.MustCompile(`^/internal/core/import-directions/([1-9][0-9]*)$`)
var coordinatorKeyPattern = regexp.MustCompile(`^[a-zA-Z0-9:_-]{8,128}$`)

type operationBinding struct {
	ContractVersion       int                 `json:"contract_version"`
	UploadID              string              `json:"upload_id"`
	UploadSessionID       string              `json:"upload_session_id"`
	JobID                 string              `json:"job_id"`
	CoordinatorCommandKey string              `json:"coordinator_command_key"`
	Type                  string              `json:"type"`
	Owner                 string              `json:"owner"`
	NativeCommandKey      string              `json:"native_command_key"`
	OwnerSessionID        *string             `json:"owner_session_id"`
	UploadPreviewRevision string              `json:"upload_preview_revision"`
	OwnerPreviewRevision  *string             `json:"owner_preview_revision"`
	TargetRevision        *string             `json:"target_revision"`
	Target                json.RawMessage     `json:"target"`
	SourceSHA256          string              `json:"source_sha256"`
	OwnerProof            operationOwnerProof `json:"owner_proof"`
	OwnerRequestSHA256    string              `json:"owner_request_sha256"`
	ApplyWireBodySHA256   string              `json:"apply_wire_body_sha256"`
	PreviewWireBodySHA256 *string             `json:"preview_wire_body_sha256"`
	UploadExpiresAt       time.Time           `json:"upload_expires_at"`
	OwnerPreviewExpiresAt *time.Time          `json:"owner_preview_expires_at"`
	RequiredDivisionIDs   []string            `json:"required_division_ids"`
}
type operationOwnerProof struct {
	Scheme           string  `json:"scheme"`
	ValidationSHA256 string  `json:"validation_sha256"`
	InputSHA256      *string `json:"input_sha256"`
	MappingSHA256    *string `json:"mapping_sha256"`
}
type operationActor struct {
	ID                int64                 `json:"id"`
	AccountID         int64                 `json:"account_id"`
	Role              string                `json:"role"`
	OrganizationID    int64                 `json:"organization_id"`
	DivisionID        int64                 `json:"division_id"`
	DivisionIDs       []int64               `json:"division_ids"`
	OrganizationAdmin bool                  `json:"organization_admin"`
	Permissions       map[string]Permission `json:"permissions"`
}
type operationDivisionPermissions struct {
	DivisionID  int64                 `json:"division_id"`
	Permissions map[string]Permission `json:"permissions"`
}
type operationEnvelope struct {
	Status              bool                           `json:"status"`
	ContractVersion     int                            `json:"contract_version"`
	GrantID             string                         `json:"grant_id"`
	Mode                string                         `json:"mode"`
	ExpiresAt           time.Time                      `json:"expires_at"`
	Actor               operationActor                 `json:"actor"`
	DivisionPermissions []operationDivisionPermissions `json:"division_permissions"`
	Binding             operationBinding               `json:"binding"`
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
type operationContextKey struct{}
type operationRequestContext struct {
	Request  operationAuthorizeRequest
	GrantID  string
	Body     []byte
	Envelope *operationEnvelope
}

func requestOperation(r *http.Request) *operationRequestContext {
	out, _ := r.Context().Value(operationContextKey{}).(*operationRequestContext)
	return out
}
func validateCoreOperationKeys(c Config) error {
	keys := []string{c.OperationOwnerKey, c.OperationDeliveryKey, c.CardsOperationKey}
	if keys[0] == "" && keys[1] == "" && keys[2] == "" {
		return nil
	}
	seen := map[string]bool{c.InternalKey: true}
	for _, key := range keys {
		if len(key) < 32 || strings.TrimSpace(key) != key || seen[key] {
			return errors.New("invalid or non-distinct Core operation credentials")
		}
		seen[key] = true
	}
	return nil
}
func operationCarrier(r *http.Request) bool {
	for _, h := range []string{"X-CPM-Operation-Grant", "X-CPM-Operation-Stage", "X-CPM-Operation-Job", "X-CPM-Operation-Service-Key"} {
		if len(r.Header.Values(h)) > 0 {
			return true
		}
	}
	return false
}
func canonicalOperationDecimal(raw string) (int64, bool) {
	n, e := strconv.ParseInt(raw, 10, 64)
	return n, e == nil && n > 0 && strconv.FormatInt(n, 10) == raw
}
func validateOperationTransport(r *http.Request) error {
	if !operationCarrier(r) {
		return nil
	}
	if len(r.Header.Values("Authorization")) > 0 {
		return bad("mixed_operation_identity")
	}
	for _, h := range []string{"X-CPM-Operation-Grant", "X-CPM-Operation-Stage", "X-CPM-Operation-Job", "X-CPM-Operation-Service-Key", "Idempotency-Key"} {
		if values := r.Header.Values(h); len(values) != 1 || values[0] == "" {
			return &APIError{401, "invalid_operation_credential"}
		}
	}
	if !lowerHex(r.Header.Get("X-CPM-Operation-Grant"), 32) || !lowerHex(r.Header.Get("X-CPM-Operation-Job"), 32) {
		return bad("invalid_operation_reference")
	}
	if _, e := legacyCommandKey(r, true); e != nil {
		return e
	}
	for h := range r.Header {
		name := strings.ToLower(h)
		if strings.HasPrefix(name, "x-cpm-operation-") && name != "x-cpm-operation-grant" && name != "x-cpm-operation-stage" && name != "x-cpm-operation-job" && name != "x-cpm-operation-service-key" {
			return bad("unknown_operation_header")
		}
		if strings.HasPrefix(name, "x-cpm-") && !strings.HasPrefix(name, "x-cpm-operation-") || name == "x-service-key" || name == "origin" || name == "cookie" || name == "x-organization-id" || name == "x-division-id" || name == "x-user-id" || name == "x-account-id" || name == "x-role" || name == "x-context-revision" {
			return bad("operation_scope_header_forbidden")
		}
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" || r.URL.EscapedPath() != r.URL.Path || path.Clean(r.URL.Path) != r.URL.Path {
		return &APIError{409, "operation_request_mismatch"}
	}
	stage := r.Header.Get("X-CPM-Operation-Stage")
	if parts := coreOperationPath.FindStringSubmatch(r.URL.Path); parts != nil {
		if _, ok := canonicalOperationDecimal(parts[1]); !ok {
			return bad("invalid_id")
		}
		if _, ok := canonicalOperationDecimal(parts[3]); !ok {
			return bad("invalid_id")
		}
		valid := r.Method == "GET" && parts[4] == "" && stage == "preview" || r.Method == "POST" && parts[4] == "/commit" && stage == "apply" || r.Method == "GET" && parts[5] != "" && stage == "receipt" && parts[5] == r.Header.Get("Idempotency-Key")
		if !valid {
			return &APIError{409, "operation_request_mismatch"}
		}
	} else if parts := coreDirectionPath.FindStringSubmatch(r.URL.Path); parts != nil {
		if _, ok := canonicalOperationDecimal(parts[1]); !ok {
			return bad("invalid_id")
		}
		if r.Method != "GET" || stage != "core-direction" {
			return &APIError{409, "operation_request_mismatch"}
		}
	} else {
		return &APIError{404, "operation_not_found"}
	}
	return nil
}

// Token validation catches duplicates, including escaped keys, before the
// schema walk enforces exact spelling and explicit nullable fields.
func operationJSONTokens(dec *json.Decoder) error {
	t, e := dec.Token()
	if e != nil {
		return e
	}
	if delim, ok := t.(json.Delim); ok {
		if delim == '{' {
			seen := map[string]bool{}
			for dec.More() {
				token, e := dec.Token()
				key, ok := token.(string)
				if e != nil || !ok || seen[key] {
					return bad("invalid_operation_response")
				}
				seen[key] = true
				if e := operationJSONTokens(dec); e != nil {
					return e
				}
			}
		} else if delim == '[' {
			for dec.More() {
				if e := operationJSONTokens(dec); e != nil {
					return e
				}
			}
		} else {
			return bad("invalid_operation_response")
		}
		close, e := dec.Token()
		if e != nil || delim == '{' && close != json.Delim('}') || delim == '[' && close != json.Delim(']') {
			return bad("invalid_operation_response")
		}
	}
	return nil
}
func operationJSONSchema(raw json.RawMessage, t reflect.Type) error {
	if t == reflect.TypeFor[json.RawMessage]() {
		return nil
	}
	if t == reflect.TypeFor[time.Time]() {
		var stamp string
		if json.Unmarshal(raw, &stamp) != nil || !strings.HasSuffix(stamp, "Z") {
			return bad("invalid_operation_response")
		}
		return nil
	}
	if t.Kind() == reflect.Pointer {
		if bytes.Equal(raw, []byte("null")) {
			return nil
		}
		return operationJSONSchema(raw, t.Elem())
	}
	switch t.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return bad("invalid_operation_response")
		}
		if len(fields) != t.NumField() {
			return bad("invalid_operation_response")
		}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			key := strings.Split(field.Tag.Get("json"), ",")[0]
			value, ok := fields[key]
			if !ok {
				return bad("invalid_operation_response")
			}
			if e := operationJSONSchema(value, field.Type); e != nil {
				return e
			}
		}
	case reflect.Map:
		var values map[string]json.RawMessage
		if json.Unmarshal(raw, &values) != nil || values == nil {
			return bad("invalid_operation_response")
		}
		for _, v := range values {
			if e := operationJSONSchema(v, t.Elem()); e != nil {
				return e
			}
		}
	case reflect.Slice:
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil || values == nil {
			return bad("invalid_operation_response")
		}
		for _, v := range values {
			if e := operationJSONSchema(v, t.Elem()); e != nil {
				return e
			}
		}
	default:
		if bytes.Equal(raw, []byte("null")) {
			return bad("invalid_operation_response")
		}
	}
	return nil
}
func decodeOperationJSON(raw []byte, out any) error {
	if !utf8.Valid(raw) {
		return bad("invalid_operation_response")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if e := operationJSONTokens(dec); e != nil {
		return e
	}
	if _, e := dec.Token(); e != io.EOF {
		return bad("invalid_operation_response")
	}
	if e := operationJSONSchema(raw, reflect.TypeOf(out).Elem()); e != nil {
		return e
	}
	return json.Unmarshal(raw, out)
}
func (a *AuthClient) operationIdentity(r *http.Request) (Actor, error) {
	if e := validateOperationTransport(r); e != nil {
		return Actor{}, e
	}
	key := a.OperationDeliveryKey
	if r.Header.Get("X-CPM-Operation-Stage") == "core-direction" {
		key = a.CardsOperationKey
	}
	if len(key) < 32 || len(a.OperationOwnerKey) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CPM-Operation-Service-Key")), []byte(key)) != 1 {
		return Actor{}, &APIError{401, "invalid_operation_credential"}
	}
	current := requestOperation(r)
	if current == nil {
		body, e := io.ReadAll(io.LimitReader(r.Body, 4097))
		if e != nil || len(body) > 4096 {
			return Actor{}, bad("operation_body_too_large")
		}
		if r.Method == "GET" && len(body) != 0 {
			return Actor{}, bad("operation_get_body_forbidden")
		}
		current = &operationRequestContext{GrantID: r.Header.Get("X-CPM-Operation-Grant"), Body: body, Request: operationAuthorizeRequest{1, r.Header.Get("X-CPM-Operation-Job"), r.Header.Get("X-CPM-Operation-Stage"), r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.URL.ForceQuery, importSHA256(body), r.Header.Get("Idempotency-Key")}}
		r.Body = io.NopCloser(bytes.NewReader(body))
		*r = *r.WithContext(context.WithValue(r.Context(), operationContextKey{}, current))
	}
	body, _ := json.Marshal(current.Request)
	req, e := http.NewRequestWithContext(r.Context(), "POST", a.URL+"/internal/auth/operation-grants/"+current.GrantID+"/authorize", bytes.NewReader(body))
	if e != nil {
		return Actor{}, unavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Key", a.OperationOwnerKey)
	res, e := a.HTTP.Do(req)
	if e != nil {
		return Actor{}, &APIError{503, "authorization_unavailable"}
	}
	defer res.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if e != nil || len(raw) > 2<<20 {
		return Actor{}, &APIError{503, "authorization_unavailable"}
	}
	if res.StatusCode != 200 {
		var out struct {
			Status bool   `json:"status"`
			Error  string `json:"error"`
		}
		allowed := map[int]map[string]bool{401: {"invalid_operation_credential": true}, 403: {"operation_access_denied": true}, 404: {"operation_not_found": true}, 409: {"operation_request_mismatch": true, "operation_issuance_conflict": true}, 410: {"operation_expired": true, "operation_revoked": true}}
		if decodeOperationJSON(raw, &out) == nil && !out.Status && allowed[res.StatusCode][out.Error] {
			return Actor{}, &APIError{res.StatusCode, out.Error}
		}
		return Actor{}, &APIError{503, "authorization_unavailable"}
	}
	var out operationEnvelope
	if decodeOperationJSON(raw, &out) != nil || validateOperationEnvelope(r, current, &out) != nil {
		return Actor{}, &APIError{503, "authorization_unavailable"}
	}
	if current.Envelope != nil && (importJSONHash(current.Envelope.Binding) != importJSONHash(out.Binding) || current.Envelope.Actor.ID != out.Actor.ID || current.Envelope.Actor.AccountID != out.Actor.AccountID || current.Envelope.Actor.Role != out.Actor.Role || current.Envelope.Actor.OrganizationID != out.Actor.OrganizationID || current.Envelope.Actor.DivisionID != out.Actor.DivisionID) {
		return Actor{}, &APIError{409, "operation_request_mismatch"}
	}
	current.Envelope = &out
	return Actor{ID: out.Actor.ID, AccountID: out.Actor.AccountID, Role: out.Actor.Role, OrganizationID: out.Actor.OrganizationID, DivisionID: &out.Actor.DivisionID, OrganizationAdmin: out.Actor.Role == "admin" && out.Actor.OrganizationAdmin, Permissions: out.Actor.Permissions}, nil
}
func validateOperationEnvelope(r *http.Request, ctx *operationRequestContext, out *operationEnvelope) error {
	b, a := out.Binding, out.Actor
	if !out.Status || out.ContractVersion != 1 || b.ContractVersion != 1 || out.GrantID != ctx.GrantID || !out.ExpiresAt.After(time.Now()) || out.ExpiresAt.Location() != time.UTC || a.ID < 1 || a.AccountID < 1 || a.OrganizationID < 1 || a.DivisionID < 1 || a.Role != "admin" && a.Role != "staff_admin" || a.OrganizationAdmin && a.Role != "admin" {
		return denied
	}
	if out.Mode != "apply" && out.Mode != "receipt" || out.Mode == "receipt" && ctx.Request.Stage != "receipt" && ctx.Request.Stage != "core-direction" {
		return denied
	}
	if !lowerHex(b.UploadID, 32) || !lowerHex(b.UploadSessionID, 32) || b.JobID != ctx.Request.JobID || !coordinatorKeyPattern.MatchString(b.CoordinatorCommandKey) || b.NativeCommandKey != ctx.Request.NativeCommandKey || b.OwnerSessionID == nil || !lowerHex(b.SourceSHA256, 64) || !lowerHex(b.OwnerProof.ValidationSHA256, 64) || !lowerHex(b.OwnerRequestSHA256, 64) || !lowerHex(b.ApplyWireBodySHA256, 64) || b.PreviewWireBodySHA256 != nil || b.TargetRevision != nil {
		return denied
	}
	if _, ok := canonicalOperationDecimal(b.UploadPreviewRevision); !ok {
		return denied
	}
	if len(a.DivisionIDs) > 1000 || len(out.DivisionPermissions) > 1000 || len(b.RequiredDivisionIDs) != 1 || b.RequiredDivisionIDs[0] != strconv.FormatInt(a.DivisionID, 10) {
		return denied
	}
	seen := map[int64]bool{}
	found := false
	for _, d := range a.DivisionIDs {
		if d < 1 || seen[d] {
			return denied
		}
		seen[d] = true
		if d == a.DivisionID {
			found = true
		}
	}
	if !found {
		return denied
	}
	seen = map[int64]bool{}
	found = false
	for _, d := range out.DivisionPermissions {
		if d.DivisionID < 1 || seen[d.DivisionID] {
			return denied
		}
		seen[d.DivisionID] = true
		if d.DivisionID == a.DivisionID {
			found = importJSONHash(d.Permissions) == importJSONHash(a.Permissions)
		}
	}
	if !found {
		return denied
	}
	actor := Actor{Role: a.Role, OrganizationAdmin: a.OrganizationAdmin, Permissions: a.Permissions}
	if !actor.Can("upload", true) {
		return denied
	}
	if ctx.Request.Stage == "core-direction" {
		if b.OwnerProof.MappingSHA256 != nil || b.OwnerProof.InputSHA256 != nil || b.OwnerPreviewExpiresAt == nil {
			return denied
		}
		var target struct {
			Kind        string `json:"kind"`
			DirectionID string `json:"direction_id"`
			ThemeID     string `json:"theme_id"`
		}
		var direction string
		var discriminator struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(b.Target, &discriminator) != nil {
			return denied
		}
		if discriminator.Kind == "existing_theme" {
			if decodeOperationJSON(b.Target, &target) != nil {
				return denied
			}
			direction = target.DirectionID
			if _, ok := canonicalOperationDecimal(target.ThemeID); !ok {
				return denied
			}
		} else if discriminator.Kind == "create_theme" {
			var create struct {
				Kind            string `json:"kind"`
				DirectionID     string `json:"direction_id"`
				Name            string `json:"name"`
				CollisionPolicy string `json:"collision_policy"`
			}
			if decodeOperationJSON(b.Target, &create) != nil || create.Name == "" || strings.TrimSpace(create.Name) != create.Name || create.CollisionPolicy != "reuse_existing" {
				return denied
			}
			direction = create.DirectionID
		} else {
			return denied
		}
		if _, ok := canonicalOperationDecimal(direction); !ok {
			return denied
		}
		if b.Owner != "cards" || b.Type != "cards.xlsx" || !lowerHex(*b.OwnerSessionID, 32) || b.OwnerPreviewRevision == nil || b.OwnerProof.Scheme != "cards.xlsx.v1" || !actor.Can("train", true) || ctx.Request.Path != "/internal/core/import-directions/"+direction {
			return denied
		}
		if _, ok := canonicalOperationDecimal(*b.OwnerPreviewRevision); !ok {
			return denied
		}
	} else {
		var target struct {
			Kind       string `json:"kind"`
			DivisionID string `json:"division_id"`
		}
		if decodeOperationJSON(b.Target, &target) != nil || target.Kind != "division_users" || target.DivisionID != strconv.FormatInt(a.DivisionID, 10) || b.Owner != "core" || !actor.Can("users", true) || b.OwnerProof.InputSHA256 == nil || !lowerHex(*b.OwnerProof.InputSHA256, 64) || b.OwnerProof.MappingSHA256 != nil {
			return denied
		}
		session, sessionOK := canonicalOperationDecimal(*b.OwnerSessionID)
		if !sessionOK {
			return denied
		}
		kind := "imports"
		if b.Type == "users.credentials" {
			if b.OwnerPreviewRevision != nil || b.OwnerPreviewExpiresAt != nil || b.OwnerProof.Scheme != credentialSnapshotScheme {
				return denied
			}
			if b.OwnerRequestSHA256 != credentialCommitHash(session) {
				return denied
			}
		} else if b.Type == "users.legacy" {
			kind = "legacy-imports"
			if b.OwnerPreviewRevision == nil || b.OwnerPreviewExpiresAt == nil || b.OwnerProof.Scheme != legacySnapshotScheme {
				return denied
			}
			revision, revisionOK := canonicalOperationDecimal(*b.OwnerPreviewRevision)
			if !revisionOK {
				return denied
			}
			if b.OwnerRequestSHA256 != legacyCommandHash("commit", session, LegacyImportCommitDTO{revision}) {
				return denied
			}
		} else {
			return denied
		}
		want := "/v1/core/divisions/" + target.DivisionID + "/" + kind + "/" + *b.OwnerSessionID
		if ctx.Request.Stage == "apply" {
			want += "/commit"
			if ctx.Request.WireBodySHA256 != b.ApplyWireBodySHA256 {
				return denied
			}
		}
		if ctx.Request.Stage == "receipt" {
			want += "/receipts/" + b.NativeCommandKey
		}
		if ctx.Request.Path != want {
			return denied
		}
	}
	if out.Mode == "apply" && ctx.Request.Stage != "receipt" && ctx.Request.Stage != "core-direction" {
		if !b.UploadExpiresAt.After(time.Now()) || out.ExpiresAt.After(b.UploadExpiresAt) || b.OwnerPreviewExpiresAt != nil && (!b.OwnerPreviewExpiresAt.After(time.Now()) || out.ExpiresAt.After(*b.OwnerPreviewExpiresAt)) {
			return denied
		}
	}
	return nil
}
func verifyImportGrantProof(r *http.Request, kind string, session int64, source *importOperationSource, proof *ImportAuthorizationProof, requestHash string) error {
	ctx := requestOperation(r)
	if ctx == nil {
		return nil
	}
	if ctx.Envelope == nil {
		return denied
	}
	b := ctx.Envelope.Binding
	if source == nil || proof == nil || b.Type != kind || b.OwnerSessionID == nil || *b.OwnerSessionID != strconv.FormatInt(session, 10) || b.SourceSHA256 != source.SHA256 || b.OwnerRequestSHA256 != requestHash || importJSONHash(b.OwnerProof) != importJSONHash(proof) {
		return &APIError{409, "operation_request_mismatch"}
	}
	return nil
}
func verifyImportGrantReceipt(r *http.Request, out *importStoredReceipt) error {
	ctx := requestOperation(r)
	if ctx == nil {
		return nil
	}
	if ctx.Envelope == nil {
		return denied
	}
	b := ctx.Envelope.Binding
	if out == nil || out.Kind != b.Type || b.OwnerSessionID == nil || strconv.FormatInt(out.SessionID, 10) != *b.OwnerSessionID || out.CommandHash != b.OwnerRequestSHA256 || out.SourceSHA256 != b.SourceSHA256 || importJSONHash(out.Proof) != importJSONHash(b.OwnerProof) {
		return &APIError{409, "operation_request_mismatch"}
	}
	return nil
}
func verifyLegacyGrantSession(r *http.Request, out LegacyImportSession) error {
	ctx := requestOperation(r)
	if ctx == nil {
		return nil
	}
	if ctx.Envelope == nil {
		return denied
	}
	b := ctx.Envelope.Binding
	if b.OwnerPreviewRevision == nil || *b.OwnerPreviewRevision != strconv.FormatInt(out.Revision, 10) || b.OwnerPreviewExpiresAt == nil || !importMicrosecond(*b.OwnerPreviewExpiresAt).Equal(importMicrosecond(out.ExpiresAt)) {
		return &APIError{409, "operation_request_mismatch"}
	}
	return nil
}
