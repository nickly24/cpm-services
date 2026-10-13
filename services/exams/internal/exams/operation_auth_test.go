package exams

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func operationWireFixture(in OperationRequest, op OperationAuthorization) operationResponseWire {
	owner, target := "3", "7"
	expires := time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339Nano)
	b := op.Binding
	a := op.Actor
	p := operationResponseWire{Status: true, ContractVersion: 1, GrantID: in.GrantID, Mode: "apply", ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), Actor: operationActorWire{ID: 9, AccountID: a.AccountID, Role: a.Role, OrganizationID: a.OrganizationID, DivisionID: a.DivisionID, DivisionIDs: a.DivisionIDs, OrganizationAdmin: a.OrganizationAdmin, Permissions: a.Permissions}, Binding: operationBindingWire{ContractVersion: 1, UploadID: strings.Repeat("c", 32), UploadSessionID: strings.Repeat("e", 32), JobID: in.JobID, CoordinatorCommandKey: "33333333-3333-4333-8333-333333333333", Type: "exams." + in.Kind, Owner: "exams", NativeCommandKey: b.NativeCommandKey, OwnerSessionID: b.SessionID, UploadPreviewRevision: "5", OwnerPreviewRevision: &owner, TargetRevision: &target, Target: operationTargetWire{Kind: "exam", ExamID: "83"}, SourceSHA256: b.SourceHash, OwnerProof: operationProofWire{Scheme: "exams.import.v1", ValidationSHA256: b.ValidationHash}, OwnerRequestSHA256: b.NativeRequestHash, ApplyWireBodySHA256: in.BodySHA256, UploadExpiresAt: expires, OwnerPreviewExpiresAt: &expires, RequiredDivisionIDs: []string{"2", "3"}}}
	for _, id := range []int64{2, 3} {
		p.DivisionPermissions = append(p.DivisionPermissions, struct {
			DivisionID  int64                 `json:"division_id"`
			Permissions map[string]Permission `json:"permissions"`
		}{id, map[string]Permission{"exams": {View: true, Edit: true}}})
	}
	return p
}

func TestOperationAuthUsesDistinctOwnerCredentialAndExactRequest(t *testing.T) {
	for _, stage := range []string{"apply", "preview", "receipt"} {
		t.Run(stage, func(t *testing.T) {
			r, op, _ := grantTestRequest(stage)
			calls := 0
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				calls++
				if req.Method != "POST" || req.URL.Path != "/internal/auth/operation-grants/"+op.Request.GrantID+"/authorize" || req.Header.Get("X-Service-Key") != strings.Repeat("o", 32) || req.Header.Get("Authorization") != "" || req.Header.Get(operationDeliveryHeader) != "" {
					t.Error("wrong credential or request to Auth")
				}
				raw, _ := io.ReadAll(req.Body)
				var wire operationAuthorizeWire
				if decodeOperationJSON(raw, &wire) != nil || wire.ContractVersion != 1 || wire.JobID != op.Request.JobID || wire.Stage != stage || wire.Method != r.Method || wire.Path != r.URL.EscapedPath() || wire.RawQuery != r.URL.RawQuery || wire.ForceQuery || wire.BodySHA256 != op.Request.BodySHA256 || wire.NativeCommandKey != op.Binding.NativeCommandKey {
					t.Error("incomplete actual request binding")
				}
				_ = json.NewEncoder(w).Encode(operationWireFixture(op.Request, op))
			}))
			defer remote.Close()
			d := NewDependencies(ServiceConfig{AuthURL: remote.URL, Key: strings.Repeat("s", 32), OperationsAuthKey: strings.Repeat("o", 32)})
			bound, err := (&Server{Deps: d, OperationKey: strings.Repeat("d", 32)}).authorizeOperationRequest(r)
			if err != nil || calls != 1 || requestOperation(bound) == nil {
				t.Fatal(err, calls)
			}
		})
	}
}

func TestOperationAuthRejectsWrongCaseNullsAndTamperedBinding(t *testing.T) {
	r, op, _ := grantTestRequest("apply")
	tests := []struct {
		name   string
		change func([]byte) []byte
	}{
		{"wrongcase", func(b []byte) []byte { return []byte(strings.Replace(string(b), `"grant_id"`, `"Grant_ID"`, 1)) }},
		{"nestedwrongcase", func(b []byte) []byte { return []byte(strings.Replace(string(b), `"exam_id"`, `"EXAM_ID"`, 1)) }},
		{"duplicateescaped", func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"contract_version":1`, `"contract_version":1,"contract_\u0076ersion":1`, 1))
		}},
		{"missednull", func(b []byte) []byte { return []byte(strings.Replace(string(b), `,"input_sha256":null`, "", 1)) }},
		{"nullrevision", func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"owner_preview_revision":"3"`, `"owner_preview_revision":null`, 1))
		}},
		{"numericrevision", func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"target_revision":"7"`, `"target_revision":7`, 1))
		}},
		{"leadingzero", func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"exam_id":"83"`, `"exam_id":"083"`, 1))
		}},
		{"foreignowner", func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"owner":"exams"`, `"owner":"cards"`, 1))
		}},
		{"foreignjob", func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"job_id":"`+op.Request.JobID+`"`, `"job_id":"`+strings.Repeat("f", 32)+`"`, 1))
		}},
		{"schememismatch", func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"scheme":"exams.import.v1"`, `"scheme":"cards.xlsx.v1"`, 1))
		}},
		{"receiptmodeapply", func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"mode":"apply"`, `"mode":"receipt"`, 1))
		}},
		{"foreignwirehash", func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"apply_wire_body_sha256":"`+op.Request.BodySHA256+`"`, `"apply_wire_body_sha256":"`+strings.Repeat("f", 64)+`"`, 1))
		}},
		{"privaterows", func(b []byte) []byte { return append([]byte(`{"private_rows":[],`), b[1:]...) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			positive, _ := json.Marshal(operationWireFixture(op.Request, op))
			body := test.change(positive)
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
			defer remote.Close()
			d := NewDependencies(ServiceConfig{AuthURL: remote.URL, OperationsAuthKey: strings.Repeat("o", 32)})
			if _, err := d.AuthorizeOperation(r, op.Request); err == nil {
				t.Fatal("unsafe Auth response accepted")
			}
		})
	}
	positive := operationWireFixture(op.Request, op)
	positive.ExpiresAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	if _, err := normalizeOperationResponse(op.Request, positive); err == nil {
		t.Fatal("expired positive response accepted")
	}
	positive = operationWireFixture(op.Request, op)
	positive.ExpiresAt = time.Now().UTC().Add(3 * time.Hour).Format(time.RFC3339Nano)
	if _, err := normalizeOperationResponse(op.Request, positive); err == nil {
		t.Fatal("grant extends preview life")
	}
	// Recovery is allowed to read a historical receipt after preview expiry;
	// it can never authorize a business apply.
	r, op, _ = grantTestRequest("receipt")
	positive = operationWireFixture(op.Request, op)
	positive.Mode = "receipt"
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	positive.Binding.OwnerPreviewExpiresAt = &past
	positive.Binding.UploadExpiresAt = past
	if _, err := normalizeOperationResponse(op.Request, positive); err != nil {
		t.Fatal("receipt recovery requires live preview", err)
	}
}

func TestOperationAuthErrorsAndRedirectAreClosed(t *testing.T) {
	r, op, _ := grantTestRequest("apply")
	for _, code := range []int{401, 403, 404, 409, 410, 503} {
		name := map[int]string{401: "invalid_operation_credential", 403: "operation_access_denied", 404: "operation_not_found", 409: "operation_request_mismatch", 410: "operation_expired", 503: "authorization_unavailable"}[code]
		remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": false, "error": name})
		}))
		d := NewDependencies(ServiceConfig{AuthURL: remote.URL, OperationsAuthKey: strings.Repeat("o", 32)})
		_, err := d.AuthorizeOperation(r, op.Request)
		auxError(t, err, name)
		remote.Close()
	}
	leaked := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { leaked++; w.WriteHeader(200) }))
	defer destination.Close()
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { http.Redirect(w, req, destination.URL, 307) }))
	defer remote.Close()
	d := NewDependencies(ServiceConfig{AuthURL: remote.URL, OperationsAuthKey: strings.Repeat("o", 32)})
	_, err := d.AuthorizeOperation(r, op.Request)
	auxError(t, err, "authorization_unavailable")
	if leaked != 0 {
		t.Fatal("owner credential followed redirect")
	}
}

func TestStoredReceiptKeepsInt64ProofOnReplay(t *testing.T) {
	value, err := decodeStoredValue([]byte(`{"exam_id":9007199254740993,"account_id":9007199254740993,"revision":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	p, err := receiptProof(value)
	if err != nil || p.ExamID != 9007199254740993 || p.AccountID != 9007199254740993 || p.Revision != 9007199254740993 {
		t.Fatal("historical proof lost precision", err)
	}
}
