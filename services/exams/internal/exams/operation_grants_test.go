package exams

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type grantIdentity struct {
	auditIdentity
	answer func(*http.Request, OperationRequest) (OperationAuthorization, error)
	calls  int
}

func (g *grantIdentity) AuthorizeOperation(r *http.Request, in OperationRequest) (OperationAuthorization, error) {
	g.calls++
	return g.answer(r, in)
}

func grantTestRequest(stage string) (*http.Request, OperationAuthorization, []byte) {
	session, key := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	path := "/v1/exams/83/imports/questions/" + session
	method := "GET"
	var raw []byte
	switch stage {
	case "apply":
		path += "/commit"
		method = "POST"
		raw = []byte("{\"expected_revision\":3}\n")
	case "preview":
		path += "?expected_revision=3&limit=200&offset=0"
	case "receipt":
		path += "/receipts/" + key
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.Header.Set(operationGrantHeader, strings.Repeat("a", 32))
	r.Header.Set(operationJobHeader, strings.Repeat("b", 32))
	r.Header.Set(operationStageHeader, stage)
	r.Header.Set(operationDeliveryHeader, strings.Repeat("d", 32))
	r.Header.Set("Idempotency-Key", key)
	if stage == "apply" {
		r.Header.Set("Idempotency-Key", key)
		r.Header.Set("Content-Type", "application/json")
	}
	a := Actor{AccountID: 9, OrganizationID: 1, Role: "staff_admin", DivisionID: ptr(2), DivisionIDs: []int64{2, 3}, Permissions: map[string]Permission{"exams": {Edit: true}, "upload": {Edit: true}}}
	commit := httptest.NewRequest("POST", "/v1/exams/83/imports/questions/"+session+"/commit", nil)
	in, _ := operationRequest(r, raw)
	op := OperationAuthorization{Request: in, Actor: a, Binding: OperationBinding{OrganizationID: 1, AccountID: 9, DivisionID: 2, ExamID: 83, Kind: "questions", SessionID: session, OwnerRevision: 3, TargetRevision: 7, SourceHash: strings.Repeat("c", 64), ValidationHash: strings.Repeat("e", 64), NativeCommandKey: key, NativeRequestHash: requestHash(commit, a, []byte("{\"expected_revision\":3}\n")), ConnectedDivisionIDs: []int64{2, 3}}, DivisionPermissions: map[int64]Permission{2: {Edit: true}, 3: {Edit: true}}}
	return r, op, raw
}

func TestOperationTransportPreservesBytesAndLiveActor(t *testing.T) {
	for _, stage := range []string{"apply", "preview", "receipt"} {
		t.Run(stage, func(t *testing.T) {
			r, op, raw := grantTestRequest(stage)
			provider := &grantIdentity{answer: func(_ *http.Request, in OperationRequest) (OperationAuthorization, error) {
				if in != op.Request {
					t.Fatal("actual request not passed to Auth")
				}
				return op, nil
			}}
			s := &Server{Deps: provider, OperationKey: strings.Repeat("d", 32)}
			bound, err := s.authorizeOperationRequest(r)
			if err != nil {
				t.Fatal(err)
			}
			if provider.calls != 1 || requestOperation(bound) == nil {
				t.Fatal("missing fresh Auth call")
			}
			got, _ := io.ReadAll(bound.Body)
			if !bytes.Equal(raw, got) {
				t.Fatal("wire bytes changed")
			}
			hash := sha256.Sum256(got)
			if hex.EncodeToString(hash[:]) != op.Request.BodySHA256 {
				t.Fatal("wire-body hash mismatch")
			}
			actor, err := s.identity(bound)
			if err != nil || actor.Role != "staff_admin" || actor.OrganizationAdmin || actor.AccountID != 9 || !actor.Can(true) {
				t.Fatal("synthetic or missing actor", err)
			}
		})
	}
}

func TestOperationTransportRejectsUnregisteredInputsBeforeAuth(t *testing.T) {
	tests := []struct {
		name, stage string
		change      func(*http.Request)
	}{
		{"mixed_bearer", "apply", func(r *http.Request) { r.Header.Set("Authorization", "Bearer synthetic") }},
		{"shared_key", "apply", func(r *http.Request) { r.Header.Set("X-Service-Key", strings.Repeat("s", 32)) }},
		{"foreign_delivery", "apply", func(r *http.Request) { r.Header.Set(operationDeliveryHeader, strings.Repeat("x", 32)) }},
		{"duplicate_grant", "apply", func(r *http.Request) { r.Header.Add(operationGrantHeader, strings.Repeat("a", 32)) }},
		{"unknown_carrier", "apply", func(r *http.Request) { r.Header.Set("X-CPM-Operation-Admin", "true") }},
		{"query_apply", "apply", func(r *http.Request) { r.URL.RawQuery = "limit=1" }},
		{"bare_query_apply", "apply", func(r *http.Request) { r.URL.ForceQuery = true }},
		{"foreign_route", "apply", func(r *http.Request) { r.URL.Path = "/v1/exams/83/metadata" }},
		{"leading_zero_target", "apply", func(r *http.Request) { r.URL.Path = strings.Replace(r.URL.Path, "/83/", "/083/", 1) }},
		{"escaped_target", "apply", func(r *http.Request) { r.URL.RawPath = strings.Replace(r.URL.Path, "/83/", "/%38%33/", 1) }},
		{"changed_method", "apply", func(r *http.Request) { r.Method = "PUT" }},
		{"missing_key", "apply", func(r *http.Request) { r.Header.Del("Idempotency-Key") }},
		{"duplicate_key", "apply", func(r *http.Request) { r.Header.Add("Idempotency-Key", r.Header.Get("Idempotency-Key")) }},
		{"extra_fields", "apply", func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(`{"expected_revision":3,"changes":[]}`))
		}},
		{"duplicate_json", "apply", func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(`{"expected_revision":3,"expected_revision":3}`))
		}},
		{"wrongcase_json", "apply", func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader(`{"Expected_Revision":3}`)) }},
		{"null_revision", "apply", func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader(`{"expected_revision":null}`)) }},
		{"trailing_json", "apply", func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader(`{"expected_revision":3}{}`)) }},
		{"oversize_body", "apply", func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader(strings.Repeat(" ", 4097))) }},
		{"unbounded_page", "preview", func(r *http.Request) { r.URL.RawQuery = "limit=201&expected_revision=3" }},
		{"unbounded_offset", "preview", func(r *http.Request) { r.URL.RawQuery = "offset=5001&expected_revision=3" }},
		{"missing_revision", "preview", func(r *http.Request) { r.URL.RawQuery = "limit=200" }},
		{"scope_query", "preview", func(r *http.Request) { r.URL.RawQuery = "organization_id=9&expected_revision=3" }},
		{"malformed_query", "preview", func(r *http.Request) { r.URL.RawQuery = "%&expected_revision=3" }},
		{"duplicate_query", "preview", func(r *http.Request) { r.URL.RawQuery = "expected_revision=3&expected_revision=3" }},
		{"preview_body", "preview", func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("{}")) }},
		{"receipt_bare_query", "receipt", func(r *http.Request) { r.URL.ForceQuery = true }},
		{"receipt_malformed_query", "receipt", func(r *http.Request) { r.URL.RawQuery = "%" }},
		{"receipt_body", "receipt", func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("{}")) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r, op, _ := grantTestRequest(test.stage)
			test.change(r)
			provider := &grantIdentity{answer: func(_ *http.Request, _ OperationRequest) (OperationAuthorization, error) { return op, nil }}
			s := &Server{Deps: provider, OperationKey: strings.Repeat("d", 32)}
			if _, err := s.authorizeOperationRequest(r); err == nil || provider.calls != 0 {
				t.Fatal("unsafe request reached Auth", err, provider.calls)
			}
		})
	}
}

func TestOperationLiveDenialsAndHistoricalBinding(t *testing.T) {
	for _, err := range []error{Denied, Missing, Conflict("operation_request_mismatch"), &APIError{410, "operation_expired"}, &APIError{410, "operation_revoked"}, &APIError{503, "authorization_unavailable"}} {
		r, _, _ := grantTestRequest("apply")
		provider := &grantIdentity{answer: func(_ *http.Request, _ OperationRequest) (OperationAuthorization, error) {
			return OperationAuthorization{}, err
		}}
		_, got := (&Server{Deps: provider, OperationKey: strings.Repeat("d", 32)}).authorizeOperationRequest(r)
		if got != err || provider.calls != 1 {
			t.Fatal("Auth denial bypassed", got)
		}
	}
	for _, mutate := range []func(*OperationAuthorization){
		func(o *OperationAuthorization) { o.Actor.OrganizationID++ },
		func(o *OperationAuthorization) { o.Actor.DivisionID = ptr(3) },
		func(o *OperationAuthorization) { o.Actor.Permissions["upload"] = Permission{View: true} },
		func(o *OperationAuthorization) { o.Actor.OrganizationAdmin = true },
		func(o *OperationAuthorization) { o.Binding.NativeRequestHash = strings.Repeat("0", 64) },
		func(o *OperationAuthorization) { o.Request.JobID = strings.Repeat("c", 32) },
		func(o *OperationAuthorization) { o.Binding.SourceHash = "" },
	} {
		r, op, _ := grantTestRequest("apply")
		mutate(&op)
		provider := &grantIdentity{answer: func(_ *http.Request, _ OperationRequest) (OperationAuthorization, error) { return op, nil }}
		if _, err := (&Server{Deps: provider, OperationKey: strings.Repeat("d", 32)}).authorizeOperationRequest(r); err == nil {
			t.Fatal("invalid positive response accepted")
		}
	}
}

func TestOperationSharedQuestionsRequireEachCurrentEditScope(t *testing.T) {
	r, op, _ := grantTestRequest("apply")
	r = r.WithContext(context.WithValue(r.Context(), operationContextKey{}, &op))
	e, _, _ := auxFixture()
	e.ID = 83
	e.Revision = 7
	e.Divisions[3] = &Division{ID: 3}
	if err := operationImportScope(r, e, true); err != nil {
		t.Fatal(err)
	}
	op.DivisionPermissions[3] = Permission{View: true}
	auxError(t, operationImportScope(r, e, false), "forbidden")
	op.DivisionPermissions[3] = Permission{Edit: true}
	e.Revision = 8
	auxError(t, operationImportScope(r, e, true), "exam_modified")
	if err := operationImportScope(r, e, false); err != nil {
		t.Fatal("historical receipt depended on new revision", err)
	}
	e.Revision = 7
	e.Divisions[4] = &Division{ID: 4}
	op.DivisionPermissions[4] = Permission{Edit: true}
	auxError(t, operationImportScope(r, e, true), "exam_connections_modified")
	if err := operationImportScope(r, e, false); err != nil {
		t.Fatal(err)
	}
	delete(op.DivisionPermissions, 4)
	auxError(t, operationImportScope(r, e, false), "forbidden")
}

func TestOperationWireBodyNewlineIsPartOfNativeReceipt(t *testing.T) {
	r, op, _ := grantTestRequest("apply")
	r.Body = io.NopCloser(strings.NewReader(`{"expected_revision":3}`))
	provider := &grantIdentity{answer: func(_ *http.Request, in OperationRequest) (OperationAuthorization, error) {
		op.Request = in
		return op, nil
	}}
	_, err := (&Server{Deps: provider, OperationKey: strings.Repeat("d", 32)}).authorizeOperationRequest(r)
	auxError(t, err, "operation_request_mismatch")
}
