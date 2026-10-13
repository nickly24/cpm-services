package uploads

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func operationFixturePlan(t *testing.T, kind string) (Session, privateExecutionPlan, json.RawMessage, *testsOperationQuote) {
	t.Helper()
	owner := "core"
	if strings.HasPrefix(kind, "cards.") {
		owner = "cards"
	}
	if strings.HasPrefix(kind, "tests.") {
		owner = "tests"
	}
	if strings.HasPrefix(kind, "exams.") {
		owner = "exams"
	}
	ss := executionSession(kind, owner)
	ss.Expires = time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	if owner == "cards" {
		ss.OwnerSessionID = strings.Repeat("f", 32)
	}
	if owner == "exams" {
		ss.OwnerSessionID = "12345678-1234-4123-a123-123456789abc"
		ss.TargetID = "42"
	}
	job := strings.Repeat("e", 32)
	var staged *stagedPreview
	if kind == "tests.json" {
		staged = executionStaged(ss, `{"organization_id":1001,"division_id":10235,"direction_id":9,"payload":{}}`)
	}
	if kind == "tests.external-results" {
		ss.TargetID = strings.Repeat("f", 24)
		staged = executionStaged(ss, `{"organization_id":1001,"division_id":10235,"test_id":"`+ss.TargetID+`","expected_test_revision":7,"rows":[{"row_id":"row-2","row":2,"account_id":9007199254740993,"percent":0}]}`)
	}
	cmd, err := immutableOwnerCommand(ss, job, staged)
	if err != nil {
		t.Fatal(err)
	}
	p := privateExecutionPlan{JobID: job, SessionID: ss.ID, Scope: ss.Scope, CoordinatorKey: "confirm:original:001", IssuanceKey: ownerCommand("grant", job, "apply"), Command: cmd, GrantMode: "apply", GrantID: sql.NullString{String: strings.Repeat("7", 32), Valid: true}, OriginalGrantID: sql.NullString{String: strings.Repeat("7", 32), Valid: true}, GrantExpiresAt: sql.NullTime{Time: time.Now().Add(time.Minute), Valid: true}, UploadExpiresAt: ss.Expires}
	proof := operationProof{ValidationSHA256: strings.Repeat("d", 64), InputSHA256: stringPointer(strings.Repeat("6", 64))}
	var quote *testsOperationQuote
	v := map[string]any{}
	switch owner {
	case "core":
		proof.Scheme = "core.credentials.snapshot.v1"
		if kind == "users.legacy" {
			proof.Scheme = "core.legacy.snapshot.v1"
		}
		v = map[string]any{"id": 17, "organization_id": 1001, "division_id": 10235, "actor_id": 3, "revision": 2, "status": "preview", "source_sha256": ss.SourceHash, "authorization_proof": proof, "owner_request_sha256": strings.Repeat("8", 64), "expires_at": ss.Expires}
	case "cards":
		v = map[string]any{"session_id": ss.OwnerSessionID, "organization_id": 1001, "revision": 2, "state": "preview", "expires_at": ss.Expires, "authorization_proof": map[string]any{"scheme": "cards.xlsx.v1", "sha256": proof.ValidationSHA256, "source_sha256": ss.SourceHash, "owner_preview_revision": 2, "owner_request_sha256": strings.Repeat("8", 64), "target": map[string]string{"kind": "existing_theme", "direction_id": "9", "theme_id": "17"}}}
	case "tests":
		proof.Scheme = kind + ".v1"
		if kind == "tests.external-results" {
			proof.MappingSHA256 = stringPointer(strings.Repeat("5", 64))
		}
		quote = &testsOperationQuote{OwnerRequestSHA256: strings.Repeat("8", 64), NativeCommandKey: cmd.NativeKey}
		quote.OwnerProof.SchemaVersion, quote.OwnerProof.operationProof = 1, proof
		v = map[string]any{"preview_hash": proof.ValidationSHA256, "owner_proof": quote.OwnerProof}
		if kind == "tests.external-results" {
			v["rows"] = []map[string]any{{"row_id": "row-2", "account_id": int64(9007199254740993)}}
		}
		p.PreviewBody = staged.Input
		p.PreviewBodySHA256 = sql.NullString{String: digest(p.PreviewBody), Valid: true}
	case "exams":
		v = map[string]any{"id": ss.OwnerSessionID, "organization_id": 1001, "division_id": 10235, "account_id": 3, "exam_id": 42, "exam_revision": 7, "revision": 2, "state": "editable", "source_hash": ss.SourceHash, "validation_hash": proof.ValidationSHA256, "expires_at": ss.Expires, "connected_division_ids": []int64{10236, 10235}}
	}
	raw, _ := json.Marshal(v)
	p.ConfirmedPreview = masked(raw)
	b, err := bindOwnerOperation(ss, p, raw, quote)
	if err != nil {
		t.Fatal(kind, err)
	}
	p.BindingBody, _ = operationCanonical(b)
	p.Command.OwnerRequestSHA256 = b.OwnerRequestSHA256
	return ss, p, raw, quote
}

var operationKinds = []string{"users.credentials", "users.legacy", "cards.xlsx", "tests.json", "tests.external-results", "exams.questions", "exams.assignments", "exams.outside"}

func TestOperationBindingAllNativeOwnersAndExplicitNulls(t *testing.T) {
	for _, kind := range operationKinds {
		t.Run(kind, func(t *testing.T) {
			ss, p, raw, quote := operationFixturePlan(t, kind)
			var b operationBinding
			if decodeOperationJSON(p.BindingBody, &b) != nil {
				t.Fatal("invalid serialized frozen wire")
			}
			if b.SourceSHA256 != ss.SourceHash || b.ApplyWireBodySHA256 != digest(p.Command.Body) || b.NativeCommandKey != p.Command.NativeKey {
				t.Fatal("lost original source/bytes/key")
			}
			if kind == "exams.questions" && strings.Join(b.RequiredDivisionIDs, ",") != "10235,10236" {
				t.Fatal("lost full connected union")
			}
			if kind == "users.credentials" && (!bytes.Contains(p.BindingBody, []byte(`"owner_preview_revision":null`)) || !bytes.Contains(p.BindingBody, []byte(`"owner_preview_expires_at":null`))) {
				t.Fatal("nullable fields omitted")
			}
			foreign := p
			foreign.Scope.ActorID++
			if _, err := bindOwnerOperation(ss, foreign, raw, quote); err == nil {
				t.Fatal("foreign actor accepted")
			}
			mutated := p
			mutated.Command.Body = append(append([]byte(nil), p.Command.Body...), ' ')
			if _, err := bindOwnerOperation(ss, mutated, raw, quote); err == nil {
				t.Fatal("mutated exact bytes accepted")
			}
			var v map[string]any
			json.Unmarshal(raw, &v)
			if strings.HasPrefix(kind, "exams.") {
				v["source_hash"] = strings.Repeat("9", 64)
			} else if strings.HasPrefix(kind, "users.") {
				v["source_sha256"] = strings.Repeat("9", 64)
			} else if kind == "cards.xlsx" {
				v["authorization_proof"].(map[string]any)["source_sha256"] = strings.Repeat("9", 64)
			} else {
				v["preview_hash"] = strings.Repeat("9", 64)
			}
			bad, _ := json.Marshal(v)
			if _, err := bindOwnerOperation(ss, p, bad, quote); err == nil {
				t.Fatal("changed native source/proof accepted")
			}
		})
	}
}

func TestOperationStrictJSONRejectsAmbiguousConfirmationAndAuthFields(t *testing.T) {
	for _, raw := range []string{`{"confirmed":true,"confirmed":false,"expected_revision":3}`, `{"confirmed":true,"confir\u006ded":false,"expected_revision":3}`, `{"Confirmed":true,"expected_revision":3}`, `{"confirmed":true,"expected_revision":null}`, `{"confirmed":true}`, `{"confirmed":true,"expected_revision":3,"extra":0}`, `{"confirmed":true,"expected_revision":3} {}`, `null`, "\xff"} {
		var v ConfirmImportDTO
		if decodeOperationJSON([]byte(raw), &v) == nil {
			t.Fatalf("accepted ambiguous confirmation %q", raw)
		}
	}
	if err := decodeOperationJSON([]byte(`{"confirmed":true,"expected_revision":9007199254740993}`), new(ConfirmImportDTO)); err != nil {
		t.Fatal("large integer changed", err)
	}
	_, p, _, _ := operationFixturePlan(t, "users.credentials")
	var b operationBinding
	json.Unmarshal(p.BindingBody, &b)
	d := p.Scope.DivisionID
	env := operationEnvelope{Status: true, ContractVersion: 1, GrantID: p.GrantID.String, Mode: "apply", ExpiresAt: p.GrantExpiresAt.Time, Binding: b, Actor: operationActor{Actor: credentialActor(p.Scope, true), ID: 999, DivisionIDs: []int64{d}}}
	env.DivisionPermissions = make([]struct {
		DivisionID  int64                 `json:"division_id"`
		Permissions map[string]Permission `json:"permissions"`
	}, 0)
	raw, _ := json.Marshal(env)
	if decodeOperationJSON(raw, new(operationEnvelope)) != nil {
		t.Fatal("valid distinct profile/account with explicit nullable fields rejected")
	}
	for _, bad := range [][]byte{bytes.Replace(raw, []byte(`"account_id":3`), []byte(`"Account_id":3`), 1), bytes.Replace(raw, []byte(`"account_id":3`), []byte(`"account_id":3,"account_id":4`), 1), bytes.Replace(raw, []byte(`"owner_preview_revision":null,`), nil, 1), bytes.Replace(raw, []byte(`"expires_at":"`), []byte(`"Expires_at":"`), 1)} {
		if decodeOperationJSON(bad, new(operationEnvelope)) == nil {
			t.Fatal("ambiguous/missing Auth field accepted")
		}
	}
}

func TestOperationOwnerCarrierAndRouteCannotBeSubstituted(t *testing.T) {
	_, p, _, _ := operationFixturePlan(t, "users.credentials")
	var calls int
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Service-Key") != "" || r.Header.Get("X-CPM-Operation-Service-Key") != strings.Repeat("k", 32) || r.Header.Get("Idempotency-Key") != p.Command.NativeKey || r.Header.Get("X-CPM-Operation-Grant") != p.GrantID.String || r.Header.Get("X-CPM-Operation-Job") != p.JobID {
			t.Error("invalid trusted carrier")
		}
		if r.Method == "GET" && len(body) != 0 {
			t.Error("GET body")
		}
		w.Write([]byte(`{"status":true,"data":{"created":[9007199254740993],"count":1}}`))
	}))
	defer owner.Close()
	s := &Server{HTTP: newHTTP(), Config: Config{CoreURL: owner.URL, OperationDeliveryKeys: map[string]string{"core": strings.Repeat("k", 32)}}}
	if _, err := s.callOperationOwner(context.Background(), p, "apply", "POST", p.Command.Path, p.Command.Body); err != nil {
		t.Fatal(err)
	}
	m, path, body, err := operationReceiptCommand(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.callOperationOwner(context.Background(), p, "receipt", m, path, body); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{p.Command.Path + "?", p.Command.Path + "?division_id=3", strings.Replace(p.Command.Path, "/17/", "/18/", 1), "/internal/auth/operation-grants"} {
		if _, err = s.callOperationOwner(context.Background(), p, "apply", "POST", bad, p.Command.Body); err == nil {
			t.Fatal("untrusted path accepted")
		}
	}
	if _, err = s.callOperationOwner(context.Background(), p, "apply", "POST", p.Command.Path, []byte("{ }")); err == nil {
		t.Fatal("changed body accepted")
	}
	if _, err = s.callOperationOwner(context.Background(), p, "receipt", "POST", p.Command.Path, p.Command.Body); err == nil {
		t.Fatal("receipt called apply")
	}
	if calls != 2 {
		t.Fatal("invalid route reached owner", calls)
	}
}

func TestOperationPrepareDoesNotSendLegacyServiceCredential(t *testing.T) {
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Key") != "" || r.Header.Get("Authorization") != "Bearer live" || r.Header.Get("X-CPM-Import-Prepare-Key") != strings.Repeat("k", 32) {
			t.Error("mixed prepare authority")
		}
		w.Write([]byte(`{"status":true,"data":{}}`))
	}))
	defer owner.Close()
	s := &Server{HTTP: newHTTP(), Config: Config{TestsURL: owner.URL, InternalKey: strings.Repeat("i", 32)}}
	_, err := s.callHeaders(context.Background(), "POST", owner.URL, "/internal/tests/imports/json/prepare", "Bearer live", ownerCommand("commit", strings.Repeat("e", 32), ""), "application/json", []byte("{}"), map[string]string{"X-CPM-Import-Prepare-Key": strings.Repeat("k", 32)})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOperationCapabilitiesRespectUnknownAndStoredMarker(t *testing.T) {
	_, p, _, _ := operationFixturePlan(t, "users.credentials")
	j := Job{ID: p.JobID, Scope: p.Scope, Status: "retry"}
	if !operationCapabilities(j, &p, true, time.Now()).RetryDelivery {
		t.Fatal("active unsent retry unavailable")
	}
	p.ApplyStartedAt = sql.NullTime{Time: time.Now(), Valid: true}
	if operationCapabilities(j, &p, true, time.Now()).RetryDelivery {
		t.Fatal("possible apply allowed second delivery")
	}
	j.Status = "outcome_unknown"
	caps := operationCapabilities(j, &p, true, time.Now())
	if !caps.RecoverReceipt || caps.RetryDelivery || caps.RetryConfirmation {
		t.Fatal("unknown inferred as no apply")
	}
	for _, status := range []string{"completed", "queued", "running"} {
		j.Status = status
		if operationCapabilities(j, &p, true, time.Now()).RecoverReceipt {
			t.Fatal("recovery offered while active/completed")
		}
	}
	j.Status = "authorizing"
	p.GrantID = sql.NullString{}
	p.OriginalGrantID = sql.NullString{}
	p.ApplyStartedAt = sql.NullTime{}
	if !operationCapabilities(j, &p, true, time.Now()).RetryConfirmation {
		t.Fatal("lost issuance response cannot resume original confirmation")
	}
	p.UploadExpiresAt = time.Now().Add(-time.Second)
	if operationCapabilities(j, &p, true, time.Now()).RetryConfirmation {
		t.Fatal("expired confirmation retriable")
	}
	if operationCapabilities(j, &p, false, time.Now()) != (JobCapabilities{}) || operationCapabilities(j, nil, true, time.Now()) != (JobCapabilities{}) {
		t.Fatal("readonly/historical action exposed")
	}
}

func TestOperationReceiptsRequireNativeCompletionAndOriginalIdentity(t *testing.T) {
	for _, kind := range operationKinds {
		t.Run(kind, func(t *testing.T) {
			ss, p, _, _ := operationFixturePlan(t, kind)
			var b operationBinding
			json.Unmarshal(p.BindingBody, &b)
			v := map[string]any{}
			switch kind {
			case "users.credentials":
				v = map[string]any{"created": []int64{9007199254740993}, "count": 1}
			case "users.legacy":
				v = map[string]any{"id": 17, "organization_id": 1001, "division_id": 10235, "actor_id": 3, "status": "completed", "report": map[string]any{"rows": []any{}}}
			case "cards.xlsx":
				v = map[string]any{"state": "completed", "operation": "session:commit:" + ss.OwnerSessionID, "session_id": ss.OwnerSessionID, "request_hash": b.OwnerRequestSHA256, "organization_id": 1001, "direction_id": 9, "result": map[string]any{"theme_id": 17, "card_ids": []int64{9007199254740993}, "created": 1, "skipped": 0}}
			case "tests.json":
				v = map[string]any{"command_id": p.Command.NativeKey, "test": map[string]any{"id": strings.Repeat("4", 24), "organization_id": 1001, "owner_division_id": 10235, "direction_id": 9}}
			case "tests.external-results":
				v = map[string]any{"command_id": p.Command.NativeKey, "status": "committed", "source_revision": 8, "results": []map[string]any{{"row_id": "row-2", "result_id": strings.Repeat("4", 24), "account_id": int64(9007199254740993), "revision": 1, "percent": 0}}}
			default:
				v = map[string]any{"session_id": ss.OwnerSessionID, "revision": 2, "exam_id": 42, "exam_revision": 7, "kind": strings.TrimPrefix(kind, "exams."), "organization_id": 1001, "division_id": 10235, "account_id": 3, "source_hash": ss.SourceHash, "validation_hash": b.OwnerProof.ValidationSHA256, "native_command_key": p.Command.NativeKey, "operation_job_id": p.JobID, "state": "committed"}
			}
			raw, _ := json.Marshal(v)
			result, err := confirmedOperationResult(p, raw)
			if err != nil || len(result) == 0 {
				t.Fatal("valid historical receipt rejected", err)
			}
			// A positive HTTP status alone is never native completion proof.
			if _, err = confirmedOperationResult(p, []byte(`{}`)); err == nil {
				t.Fatal("empty positive receipt completed job")
			}
			if _, err = confirmedOperationResult(p, append(raw, []byte(` {}`)...)); err == nil {
				t.Fatal("trailing object accepted")
			}
			switch kind {
			case "users.credentials":
				v["count"] = 2
			case "users.legacy":
				v["actor_id"] = 4
			case "cards.xlsx":
				v["request_hash"] = strings.Repeat("0", 64)
			case "tests.json":
				v["test"].(map[string]any)["owner_division_id"] = 10236
			case "tests.external-results":
				v["status"] = "completed"
			default:
				v["operation_job_id"] = strings.Repeat("0", 32)
			}
			raw, _ = json.Marshal(v)
			if _, err = confirmedOperationResult(p, raw); err == nil {
				t.Fatal("mismatching receipt completed job")
			}
		})
	}
}

func TestOperationJSONReceiptAllowsCurrentDirectionButPinsKnownResult(t *testing.T) {
	_, p, _, _ := operationFixturePlan(t, "tests.json")
	id := strings.Repeat("4", 24)
	raw := []byte(`{"command_id":"` + p.Command.NativeKey + `","test":{"id":"` + id + `","organization_id":1001,"owner_division_id":10235,"direction_id":32}}`)
	if _, err := confirmedOperationResult(p, raw); err != nil {
		t.Fatal("current direction rejected as original creation proof", err)
	}
	p.OwnerResult = raw
	changed := bytes.Replace(raw, []byte(id), []byte(strings.Repeat("5", 24)), 1)
	if _, err := confirmedOperationResult(p, changed); err == nil {
		t.Fatal("known result ID changed")
	}
	changed = bytes.Replace(raw, []byte(`"direction_id":32`), []byte(`"direction_id":0`), 1)
	if _, err := confirmedOperationResult(p, changed); err == nil {
		t.Fatal("invalid current direction accepted")
	}
}

func TestOperationReceiptZeroRequiresExplicitCanonicalFieldAndPinnedStudents(t *testing.T) {
	_, p, _, _ := operationFixturePlan(t, "tests.external-results")
	raw := []byte(`{"command_id":"` + p.Command.NativeKey + `","status":"committed","source_revision":8,"results":[{"row_id":"row-2","result_id":"` + strings.Repeat("4", 24) + `","account_id":9007199254740993,"revision":1,"percent":0,"legitimate_owner_extra":true}]}`)
	if _, err := confirmedOperationResult(p, raw); err != nil {
		t.Fatal("explicit zero/unrelated owner extra rejected", err)
	}
	for _, bad := range [][]byte{
		bytes.Replace(raw, []byte(`,"percent":0`), nil, 1),
		bytes.Replace(raw, []byte(`"percent":0`), []byte(`"percent":null`), 1),
		bytes.Replace(raw, []byte(`"percent":0`), []byte(`"Percent":0`), 1),
		bytes.Replace(raw, []byte(`"percent":0`), []byte(`"percent":0,"Percent":1`), 1),
		bytes.Replace(raw, []byte(`9007199254740993`), []byte(`9007199254740994`), 1),
	} {
		if _, err := confirmedOperationResult(p, bad); err == nil {
			t.Fatal("ambiguous zero or swapped student completed job")
		}
	}
	var body map[string]any
	json.Unmarshal(p.PreviewBody, &body)
	body["rows"] = []map[string]any{{"row_id": "row-2", "account_id": int64(9007199254740993)}, {"row_id": "row-3", "account_id": int64(9007199254740994)}}
	p.PreviewBody, _ = json.Marshal(body)
	p.ConfirmedPreview = []byte(`{"rows":[{"row_id":"row-2","account_id":9007199254740993},{"row_id":"row-3","account_id":9007199254740994}]}`)
	var result map[string]any
	json.Unmarshal(raw, &result)
	result["results"] = []map[string]any{{"row_id": "row-2", "result_id": strings.Repeat("4", 24), "account_id": int64(9007199254740993), "revision": 1, "percent": 0}, {"row_id": "row-3", "result_id": strings.Repeat("4", 24), "account_id": int64(9007199254740994), "revision": 1, "percent": 0}}
	bad, _ := json.Marshal(result)
	if _, err := confirmedOperationResult(p, bad); err == nil {
		t.Fatal("duplicate native result IDs completed job")
	}
	ss, cards, _, _ := operationFixturePlan(t, "cards.xlsx")
	var binding operationBinding
	json.Unmarshal(cards.BindingBody, &binding)
	cardReceipt := []byte(`{"state":"completed","operation":"session:commit:` + ss.OwnerSessionID + `","session_id":"` + ss.OwnerSessionID + `","request_hash":"` + binding.OwnerRequestSHA256 + `","organization_id":1001,"direction_id":9,"result":{"theme_id":17,"card_ids":[1],"created":1,"skipped":0}}`)
	if _, err := confirmedOperationResult(cards, cardReceipt); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{bytes.Replace(cardReceipt, []byte(`,"skipped":0`), nil, 1), bytes.Replace(cardReceipt, []byte(`"skipped":0`), []byte(`"skipped":null`), 1), bytes.Replace(cardReceipt, []byte(`"skipped":0`), []byte(`"Skipped":0`), 1)} {
		if _, err := confirmedOperationResult(cards, bad); err == nil {
			t.Fatal("missing/null/aliased skipped accepted")
		}
	}
}
