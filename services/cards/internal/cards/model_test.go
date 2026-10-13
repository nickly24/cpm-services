package cards

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLegacyFingerprint(t *testing.T) {
	if got := fingerprint(" Вопрос <b> & </b> ", " Ответ\n\nстрока "); got != "f96387df39397fdd0256eb168927c7443c53c05660148047494c5e2fd9ae27af" {
		t.Fatal(got)
	}
	if fingerprint("Q", "A\nB") == fingerprint("Q", "A B") {
		t.Fatal("line breaks lost")
	}
	if fingerprint(" Q ", " A ") != fingerprint("Q", "A") {
		t.Fatal("outer whitespace significant")
	}
}
func TestProjectionFormatting(t *testing.T) {
	tests := []struct {
		q    Question
		want string
	}{{Question{Type: "single", Answers: []Option{{Text: " A ", Correct: true}, {Text: "B", Correct: false}}}, "A"}, {Question{Type: "multiple", Answers: []Option{{Text: " A ", Correct: true}, {Text: "B", Correct: true}}}, "A; B"}, {Question{Type: "text", Correct: []string{" One\nTwo ", " Alias "}}, "One\nTwo / Alias"}}
	for _, tt := range tests {
		if got := answer(tt.q); got != tt.want {
			t.Fatalf("got %q want %q", got, tt.want)
		}
	}
}
func TestBatchesBeforeFilteringAndStats(t *testing.T) {
	cs := make([]Card, 25)
	for i := range cs {
		cs[i].Status = "unlearned"
	}
	cs[0].Status = "learned"
	cs[11].Status = "answer_changed"
	bs := batches(cs, 10)
	if len(bs) != 3 || bs[2].Size != 5 || bs[1].From != 11 {
		t.Fatal(bs)
	}
	if len(filtered(cs[0:10], "stale")) != 0 || len(filtered(cs[10:20], "stale")) != 1 || len(filtered(cs[10:20], "unlearned")) != 10 {
		t.Fatal("filter changed batch boundaries")
	}
	st := stats(cs)
	if st.Total != 25 || st.Learned != 1 || st.Changed != 1 || st.Unlearned != 23 || st.Percent != 4 {
		t.Fatal(st)
	}
	if statsForCounts(1, 8) != 12 || statsForCounts(3, 8) != 38 {
		t.Fatal("Python ties-to-even parity")
	}
}
func TestPreviewDuplicateAndMultiline(t *testing.T) {
	rows := rebuildRows([]ImportRow{{Row: 2, Question: "Q", Answer: "One\r\n\r\nTwo\r\n"}, {Row: 3, Question: "Q", Answer: "One Two"}, {Row: 4, Question: "Bad"}, {Row: 5, Action: "skip"}}, []Card{{Question: "Q", Answer: "One Two"}})
	if rows[0].Answer != "One\n\nTwo\n" || rows[0].Action != "warning" || len(rows[1].Warnings) != 2 || rows[2].Action != "error" || rows[3].Action != "skip" {
		t.Fatal(rows)
	}
}
func TestPermissionsAndStrictDTO(t *testing.T) {
	for _, role := range []string{"student", "proctor", "supervisor", "staff_admin"} {
		if (Actor{Role: role, OrganizationAdmin: true}).Can(true) {
			t.Fatal("role escalation")
		}
	}
	a := Actor{Role: "staff_admin", Permissions: map[string]Permission{"train": {View: true}}}
	if !a.Can(false) || a.Can(true) {
		t.Fatal("view escalation")
	}
	for _, body := range []string{`{"question":"Q","answer":"A","organization_id":3}`, `{"question":"Q","answer":"A"} {}`, `{"question":1}`} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if decode(r, &CardDTO{}) == nil {
			t.Fatal("accepted", body)
		}
	}
}
func TestDependenciesFailClosedAndForwardCredentials(t *testing.T) {
	calls := 0
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer opaque" || r.Header.Get("X-Service-Key") != "service-key" {
			t.Error("credentials not forwarded")
		}
		switch r.URL.Path {
		case "/v1/auth/introspect":
			json.NewEncoder(w).Encode(map[string]any{"status": true, "active": true, "user": Actor{AccountID: 4, OrganizationID: 5, Role: "student"}})
		case "/internal/core/cards-context":
			json.NewEncoder(w).Encode(map[string]any{"status": true, "data": Scope{OrganizationID: 6}})
		default:
			w.WriteHeader(503)
		}
	}))
	defer source.Close()
	d := NewDependencies(Config{AuthURL: source.URL, CoreURL: source.URL, TestsURL: source.URL, Key: "service-key"})
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer opaque")
	if _, _, e := d.Identify(r); e == nil {
		t.Fatal("cross-org context accepted")
	}
	if _, e := d.catalog(r); e != unavailable {
		t.Fatal("outage became successful empty catalog", e)
	}
	if calls != 3 {
		t.Fatal(calls)
	}
}
func TestNoRedirectCredentialLeak(t *testing.T) {
	received := false
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received = true; io.WriteString(w, `{"status":true}`) }))
	defer dest.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, dest.URL, 302) }))
	defer src.Close()
	d := NewDependencies(Config{TestsURL: src.URL, Key: "secret"})
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer token")
	if _, e := d.catalog(r); e == nil || received {
		t.Fatal("redirect followed")
	}
}
