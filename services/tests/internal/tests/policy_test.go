package tests

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAccessPolicyBoundaries(t *testing.T) {
	start := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	p := Placement{DivisionID: 1001, Settings: Settings{Published: true, StartAt: &start, EndAt: &end, TimeLimitMinutes: 60}}
	for _, tc := range []struct {
		name                                             string
		now                                              time.Time
		archived, direction, published, review, official bool
		reason                                           string
	}{
		{"before", start.Add(-time.Nanosecond), false, true, true, false, false, "test_upcoming"},
		{"start", start, false, true, true, false, true, "allowed"},
		{"end", end, false, true, true, false, true, "allowed"},
		{"after", end.Add(time.Nanosecond), false, true, true, true, false, "test_ended"},
		{"archived", start, true, true, true, true, false, "test_archived"},
		{"inactive direction", start, false, false, true, true, false, "direction_archived"},
		{"unpublished", start, false, true, false, true, false, "unpublished"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local := p
			local.Settings.Published, local.Settings.AnswersVisible = tc.published, tc.review
			out := availability(Document{Archived: tc.archived}, local, tc.direction, tc.now)
			if out.OfficialAccessAllowed != tc.official || out.OfficialReason != tc.reason {
				t.Fatalf("unexpected official access: %+v", out)
			}
			review := tc.review && tc.published && tc.direction && !tc.archived
			if out.ReviewAccessAllowed != review || out.Scope != "test_settings_only" || out.AttemptStateChecked || out.ContentChecked {
				t.Fatalf("unexpected review or scope: %+v", out)
			}
		})
	}
	if err := validateSettings(Settings{Published: true}); err == nil {
		t.Fatal("publication without dates accepted")
	}
	badRange := p.Settings
	badRange.EndAt = &start
	if err := validateSettings(badRange); err == nil {
		t.Fatal("empty range accepted")
	}
}

func TestOwnershipAndBankPermissions(t *testing.T) {
	owner, other := int64(1001), int64(1002)
	a := Actor{Role: "staff_admin", OrganizationID: 1001, DivisionID: &owner, Permissions: map[string]Permission{"tests": {true, true}}}
	d := Document{OrganizationID: 1001, OwnerDivisionID: owner, Placements: []Placement{{DivisionID: owner}}}
	if !d.Editable(a) || !d.Readable(a) {
		t.Fatal("owner rejected")
	}
	a.DivisionID = &other
	if d.Readable(a) || d.Editable(a) {
		t.Fatal("private test visible in another division")
	}
	d.BankAvailable = true
	if !d.Readable(a) || d.Editable(a) {
		t.Fatal("bank visibility grants ownership")
	}
	d.Placements = append(d.Placements, Placement{DivisionID: other})
	d.BankAvailable = false
	if !d.Readable(a) || d.Editable(a) {
		t.Fatal("attached division must retain read without ownership")
	}
	a.OrganizationID = 2001
	if d.Readable(a) || d.Editable(a) {
		t.Fatal("cross organization access")
	}
	a.OrganizationID = 1001
	a.Role = "student"
	a.OrganizationAdmin = true
	if a.Can(true) || a.Can(false) || d.Editable(a) || d.Readable(a) {
		t.Fatal("role escalation")
	}
}

func TestAuthFailsClosedAndIgnoresPrivilegeHeaders(t *testing.T) {
	for _, tc := range []struct {
		name, body   string
		status, want int
	}{
		{"unavailable", `{}`, 503, 503},
		{"revoked", `{}`, 401, 401},
		{"malformed", `{`, 200, 503},
		{"inactive", `{"status":true,"active":false,"user":{"account_id":1,"organization_id":1001}}`, 200, 503},
		{"student admin flag", `{"status":true,"active":true,"user":{"account_id":1,"organization_id":1001,"role":"student","organization_admin":true}}`, 200, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/auth/introspect" || r.Header.Get("Authorization") != "Bearer sample" {
					t.Error("introspection contract")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			deps := NewDependencies(Config{AuthURL: server.URL})
			r := httptest.NewRequest("GET", "/v1/tests/bank", nil)
			r.Header.Set("Authorization", "Bearer sample")
			r.Header.Set("X-Role", "admin")
			a, err := deps.Identify(r)
			if tc.want == 0 {
				if err != nil || a.OrganizationAdmin || a.Can(true) {
					t.Fatalf("privilege accepted: %+v %v", a, err)
				}
			} else {
				var e *APIError
				if !errors.As(err, &e) || e.Code != tc.want {
					t.Fatalf("got %v", err)
				}
			}
		})
	}
	deps := NewDependencies(Config{AuthURL: "http://127.0.0.1:1"})
	r := httptest.NewRequest("GET", "/v1/tests/bank", nil)
	r.Header.Set("Authorization", "Bearer sample")
	if _, err := deps.Identify(r); !errors.Is(err, unavailable) {
		t.Fatalf("network failure: %v", err)
	}
}

func TestCoreReferenceMismatchFailsClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":true,"data":{"organization":{"id":2001},"divisions":[]}}`)
	}))
	defer server.Close()
	deps := NewDependencies(Config{CoreURL: server.URL})
	if _, err := deps.References(context.Background(), 1001, nil, nil); !errors.Is(err, unavailable) {
		t.Fatalf("mismatched org: %v", err)
	}
}

func TestStrictMutationJSON(t *testing.T) {
	for _, body := range []string{`{"expected_revision":1,"organization_id":2001}`, `{"expected_revision":1} {}`, `{"expected_revision":"1"}`} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		var in RevisionDTO
		if decode(r, &in) == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}
