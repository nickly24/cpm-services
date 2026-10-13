package exams

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const operationGrantHeader = "X-CPM-Operation-Grant"
const operationStageHeader = "X-CPM-Operation-Stage"
const operationJobHeader = "X-CPM-Operation-Job"
const operationDeliveryHeader = "X-CPM-Operation-Service-Key"

// OperationAuthorizer is implemented by the Auth dependency. It returns a live
// actor and immutable binding, never a locally fabricated permission context.
type OperationAuthorizer interface {
	AuthorizeOperation(*http.Request, OperationRequest) (OperationAuthorization, error)
}

type OperationRequest struct {
	GrantID, JobID, Stage, Method, Path, RawQuery, BodySHA256, NativeCommandKey string
	ExamID, OwnerRevision                                                       int64
	Kind, SessionID                                                             string
}

type OperationBinding struct {
	OrganizationID, AccountID, DivisionID, ExamID, OwnerRevision, TargetRevision     int64
	Kind, SessionID, SourceHash, ValidationHash, NativeCommandKey, NativeRequestHash string
	ConnectedDivisionIDs                                                             []int64
}

type OperationAuthorization struct {
	Request             OperationRequest
	Actor               Actor
	Binding             OperationBinding
	DivisionPermissions map[int64]Permission
}

type operationContextKey struct{}

func requestOperation(r *http.Request) *OperationAuthorization {
	value, _ := r.Context().Value(operationContextKey{}).(*OperationAuthorization)
	return value
}

func canonicalUUID(value string) bool { return validateKey(value) && value == strings.ToLower(value) }
func canonicalHexID(value string) bool {
	if len(value) != 32 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
func canonicalPositive(value string) (int64, bool) {
	n, err := strconv.ParseInt(value, 10, 64)
	return n, err == nil && n > 0 && strconv.FormatInt(n, 10) == value
}
func operationCarrierPresent(r *http.Request) bool {
	for name := range r.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-cpm-operation-") {
			return true
		}
	}
	return false
}

func operationRequest(r *http.Request, raw []byte) (OperationRequest, error) {
	in := OperationRequest{GrantID: r.Header.Get(operationGrantHeader), JobID: r.Header.Get(operationJobHeader), Stage: r.Header.Get(operationStageHeader), Method: r.Method, Path: r.URL.Path, RawQuery: r.URL.RawQuery, NativeCommandKey: r.Header.Get("Idempotency-Key")}
	hash := sha256.Sum256(raw)
	in.BodySHA256 = hex.EncodeToString(hash[:])
	if !canonicalHexID(in.GrantID) || !canonicalHexID(in.JobID) || !oneOf(in.Stage, "apply", "preview", "receipt") || r.URL.RawPath != "" {
		return in, Conflict("operation_request_mismatch")
	}
	parts := strings.Split(strings.TrimPrefix(in.Path, "/"), "/")
	if len(parts) < 6 || parts[0] != "v1" || parts[1] != "exams" || parts[3] != "imports" {
		return in, Conflict("operation_request_mismatch")
	}
	var ok bool
	if in.ExamID, ok = canonicalPositive(parts[2]); !ok {
		return in, Conflict("operation_request_mismatch")
	}
	in.Kind, in.SessionID = parts[4], parts[5]
	if !importKind(in.Kind) || !canonicalUUID(in.SessionID) {
		return in, Conflict("operation_request_mismatch")
	}
	switch in.Stage {
	case "apply":
		if in.Method != "POST" || len(parts) != 7 || parts[6] != "commit" || r.URL.ForceQuery || in.RawQuery != "" || !canonicalUUID(in.NativeCommandKey) {
			return in, Conflict("operation_request_mismatch")
		}
		var dto struct {
			ExpectedRevision *int64 `json:"expected_revision"`
		}
		if decodeOperationJSON(raw, &dto) != nil || dto.ExpectedRevision == nil || *dto.ExpectedRevision < 1 {
			return in, Bad("invalid_preview")
		}
		in.OwnerRevision = *dto.ExpectedRevision
	case "preview":
		if in.Method != "GET" || len(parts) != 6 || len(raw) != 0 || !canonicalUUID(in.NativeCommandKey) || r.URL.ForceQuery {
			return in, Conflict("operation_request_mismatch")
		}
		q, err := url.ParseQuery(in.RawQuery)
		if err != nil {
			return in, Conflict("operation_request_mismatch")
		}
		for k, values := range q {
			if !oneOf(k, "limit", "offset", "expected_revision") || len(values) != 1 {
				return in, Conflict("operation_request_mismatch")
			}
		}
		if len(q) != 3 || in.RawQuery != q.Encode() {
			return in, Conflict("operation_request_mismatch")
		}
		if in.OwnerRevision, ok = canonicalPositive(q.Get("expected_revision")); !ok {
			return in, Conflict("operation_request_mismatch")
		}
		if v, exists := q["limit"]; exists {
			n, valid := canonicalPositive(v[0])
			if !valid || n > 200 {
				return in, Conflict("operation_request_mismatch")
			}
		}
		if v, exists := q["offset"]; exists {
			n, err := strconv.ParseInt(v[0], 10, 64)
			if err != nil || n < 0 || n > 5000 || strconv.FormatInt(n, 10) != v[0] {
				return in, Conflict("operation_request_mismatch")
			}
		}
	case "receipt":
		if in.Method != "GET" || len(parts) != 8 || parts[6] != "receipts" || !canonicalUUID(parts[7]) || len(raw) != 0 || r.URL.ForceQuery || in.RawQuery != "" || in.NativeCommandKey != parts[7] {
			return in, Conflict("operation_request_mismatch")
		}
		in.NativeCommandKey = parts[7]
	}
	return in, nil
}

func validateOperationAuthorization(in OperationRequest, out OperationAuthorization) error {
	if out.Request != in {
		return &APIError{503, "invalid_operation_authorization"}
	}
	b, a := out.Binding, out.Actor
	if a.AccountID < 1 || a.OrganizationID < 1 || a.DivisionID == nil || *a.DivisionID < 1 || a.AccountID != b.AccountID || a.OrganizationID != b.OrganizationID || *a.DivisionID != b.DivisionID || b.ExamID != in.ExamID || b.Kind != in.Kind || b.SessionID != in.SessionID || b.OwnerRevision < 1 || b.TargetRevision < 1 || !canonicalSHA256(b.SourceHash) || !canonicalSHA256(b.ValidationHash) || !canonicalSHA256(b.NativeRequestHash) || !canonicalUUID(b.NativeCommandKey) {
		return &APIError{503, "invalid_operation_authorization"}
	}
	if in.Stage != "receipt" && in.OwnerRevision != b.OwnerRevision || in.NativeCommandKey != b.NativeCommandKey {
		return Conflict("operation_request_mismatch")
	}
	if a.OrganizationAdmin && a.Role != "admin" || !importPermission(a, true) || !a.OrganizationAdmin && !contains(a.DivisionIDs, b.DivisionID) {
		return Denied
	}
	if len(b.ConnectedDivisionIDs) == 0 || len(b.ConnectedDivisionIDs) > 1000 {
		return &APIError{503, "invalid_operation_authorization"}
	}
	for i, id := range b.ConnectedDivisionIDs {
		if id < 1 || i > 0 && b.ConnectedDivisionIDs[i-1] >= id {
			return &APIError{503, "invalid_operation_authorization"}
		}
	}
	return nil
}

func (s *Server) authorizeOperationRequest(r *http.Request) (*http.Request, error) {
	if !operationCarrierPresent(r) {
		return r, nil
	}
	for name, values := range r.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-cpm-operation-") && (!oneOf(strings.ToLower(name), strings.ToLower(operationGrantHeader), strings.ToLower(operationJobHeader), strings.ToLower(operationStageHeader), strings.ToLower(operationDeliveryHeader)) || len(values) != 1) {
			return r, Conflict("operation_request_mismatch")
		}
	}
	if r.Header.Get("Authorization") != "" || r.Header.Get("X-Service-Key") != "" || len(r.Header.Values("Idempotency-Key")) > 1 {
		return r, Conflict("operation_request_mismatch")
	}
	for _, name := range []string{"X-Organization-ID", "X-Division-ID", "X-Account-ID", "X-User-ID", "X-Role", "X-Selected-Division-ID"} {
		if len(r.Header.Values(name)) > 0 {
			return r, Conflict("operation_request_mismatch")
		}
	}
	if len(s.OperationKey) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get(operationDeliveryHeader)), []byte(s.OperationKey)) != 1 {
		return r, &APIError{401, "invalid_operation_service_key"}
	}
	provider, ok := s.Deps.(OperationAuthorizer)
	if !ok {
		return r, &APIError{503, "authorization_unavailable"}
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil || len(raw) > 4096 {
		return r, &APIError{413, "payload_too_large"}
	}
	in, err := operationRequest(r, raw)
	if err != nil {
		return r, err
	}
	out, err := provider.AuthorizeOperation(r, in)
	if err != nil {
		return r, err
	}
	if err = validateOperationAuthorization(in, out); err != nil {
		return r, err
	}
	if in.Stage == "apply" && requestHash(r, out.Actor, raw) != out.Binding.NativeRequestHash {
		return r, Conflict("operation_request_mismatch")
	}
	r = r.WithContext(context.WithValue(r.Context(), operationContextKey{}, &out))
	r.Body = io.NopCloser(bytes.NewReader(raw))
	return r, nil
}

func operationImportScope(r *http.Request, e *Exam, firstApply bool) error {
	op := requestOperation(r)
	if op == nil {
		return nil
	}
	b := op.Binding
	if e.OrganizationID != b.OrganizationID || e.ID != b.ExamID {
		return Missing
	}
	if b.Kind == "questions" {
		divs := allDivisions(e)
		sort.Slice(divs, func(i, j int) bool { return divs[i] < divs[j] })
		for _, id := range divs {
			if !op.Actor.OrganizationAdmin && !op.DivisionPermissions[id].Edit {
				return Denied
			}
		}
		if firstApply {
			if len(divs) != len(b.ConnectedDivisionIDs) {
				return Conflict("exam_connections_modified")
			}
			for i, id := range divs {
				if id != b.ConnectedDivisionIDs[i] {
					return Conflict("exam_connections_modified")
				}
			}
		}
	}
	if firstApply && e.Revision != b.TargetRevision {
		return Conflict("exam_modified")
	}
	return nil
}
