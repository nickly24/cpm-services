package uploads

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLivePreviewRedactionPreservesExactOwnerIDs(t *testing.T) {
	raw := []byte(`{"data":{"id":"owner-session-123","revision":17,"preview":{"rows":[{"row":2,"account_id":9007199254740993,"legacy_student_id":9223372036854775807,"percent":1.50,"extra":1e2,"password":"synthetic-private-password","nested":{"access_token":"synthetic-private-token"}}]}}}`)
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(raw) }))
	defer owner.Close()
	s := &Server{HTTP: newHTTP(), Config: Config{CardsURL: owner.URL}}
	r := httptest.NewRequest("GET", "http://uploads.invalid/session", nil)
	r.Header.Set("Authorization", "Bearer synthetic")
	view, status, e := s.sessionView(r, ownerAdapterSession("cards.xlsx"))
	if e != nil || status != 200 {
		t.Fatal("live owner preview failed", e)
	}
	encoded, e := json.Marshal(view)
	if e != nil {
		t.Fatal(e)
	}
	for _, exact := range []string{`"account_id":9007199254740993`, `"legacy_student_id":9223372036854775807`, `"percent":1.50`, `"extra":1e2`} {
		if !bytes.Contains(encoded, []byte(exact)) {
			t.Fatal("client preview rounded or changed an owner number", exact)
		}
	}
	if bytes.Contains(encoded, []byte("synthetic-private")) {
		t.Fatal("exact-number masking leaked credentials")
	}
}

func TestOwnerReportPreservesLargeSourceRowsAndResultIDs(t *testing.T) {
	preview := json.RawMessage(`{"preview":{"rows":[{"row":9007199254740993,"row_id":"synthetic-row","warnings":[{"reference":9223372036854775807}]}]}}`)
	result := json.RawMessage(`{"account_id":9007199254740993,"legacy_student_id":9223372036854775807,"password":"synthetic-private-password"}`)
	report := makeReport(ownerAdapterSession("tests.external-results"), preview, result)
	for _, exact := range []string{`"source_row":9007199254740993`, `"reference":9223372036854775807`, `"account_id":9007199254740993`, `"legacy_student_id":9223372036854775807`} {
		if !bytes.Contains(report, []byte(exact)) {
			t.Fatal("report rounded an owner number", exact)
		}
	}
	if bytes.Contains(report, []byte("synthetic-private")) {
		t.Fatal("exact-number report leaked credentials")
	}
}

func TestMaskedInvalidTrailingJSONFailsClosed(t *testing.T) {
	for _, raw := range []string{`{"account_id":9007199254740993} {}`, `{"password":"synthetic-private-password"} trailing`, `{"incomplete":`} {
		if got := masked(json.RawMessage(raw)); string(got) != "null" {
			t.Fatal("invalid owner JSON produced a partial preview")
		}
	}
}
