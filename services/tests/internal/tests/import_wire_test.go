package tests

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestImportWirePinsExactBytesAndRestoresNativeBody(t *testing.T) {
	for _, raw := range []string{`{"organization_id":11,"division_id":17,"direction_id":31,"payload":{"number":9007199254740993}}`, " {\"organization_id\":11,\"division_id\":17,\"direction_id\":31,\"payload\":{\"number\":9007199254740993}}\n"} {
		r := httptest.NewRequest("POST", "/internal/tests/imports/json/validate", strings.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		wire, err := readImportWire(r)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(raw))
		restored, _ := io.ReadAll(r.Body)
		if string(restored) != raw || string(wire.Body) != raw || wire.SHA256 != hex.EncodeToString(sum[:]) || wire.Type != "tests.json" || wire.Stage != "preview" {
			t.Fatal("wire was normalized or lost", wire)
		}
	}
}

func TestImportWireRejectsQueryAliasesUnknownStagesAndAmbiguousJSON(t *testing.T) {
	for _, tc := range []struct{ path, body string }{
		{"/internal/tests/imports/json/commit?", `{}`}, {"/internal/tests/imports/json/commit?bad=%", `{}`},
		{"/internal/tests/imports/json/commit?stage=receipt", `{}`}, {"/internal/tests/imports/json/commit/", `{}`},
		{"/internal/tests/imports/json/%63ommit", `{}`}, {"/v1/tests/divisions/17/imports/json/commit", `{}`},
		{"/internal/tests/imports/json/commit", `{"organization_id":11,"organization_id":12}`},
		{"/internal/tests/imports/json/commit", `{"Organization_ID":11}`},
		{"/internal/tests/imports/json/commit", `{"job_id":"untrusted"}`},
		{"/internal/tests/imports/json/commit", `{"payload":{"answers":[{"id":"a","id":"b"}]}}`},
		{"/internal/tests/imports/json/commit", `{} {}`}, {"/internal/tests/imports/json/commit", `[]`},
		{"/internal/tests/external-results/commit", `{"rows":[{"Account_ID":71}]}`},
		{"/internal/tests/external-results/commit", `{"rows":[{"percent":0,"Percent":1}]}`},
	} {
		r := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		if _, err := readImportWire(r); err == nil {
			t.Fatal("ambiguous/unregistered wire accepted", tc)
		}
	}
}

func TestImportCarrierAliasesAreRejectedBeforeServeMuxRedirects(t *testing.T) {
	s := &Server{Config: importTestConfig()}
	for _, path := range []string{"/internal/tests/imports/json/../json/commit", "/internal//tests/imports/json/commit", "/internal/tests/imports/json/%63ommit", "/internal/tests/imports/json/commit?", "/internal/tests/imports/json/prepare", "/openapi.json", "/internal/tests/ratings/snapshot"} {
		r := importTestRequest(path, "apply", []byte(`{}`))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		assertImportResponse(t, w, 409, "operation_request_mismatch")
		if w.Header().Get("Location") != "" {
			t.Fatal("grant route redirected before validation")
		}
	}
}
