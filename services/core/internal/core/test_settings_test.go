package core

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOrganizationTestSettingsCannotBeDelegated(t *testing.T) {
	s := &Server{}
	for _, role := range []string{"student", "proctor", "supervisor", "examinator", "staff_admin"} {
		r := httptest.NewRequest("PUT", "/", strings.NewReader(`{"expected_revision":0,"recalculate_on_test_change":false}`))
		r.Header.Set("Content-Type", "application/json")
		_, _, err := s.organizationTestSettingsUpdate(r, Actor{Role: role, OrganizationID: 1}, 0)
		if err != denied {
			t.Fatalf("%s was allowed to change the organization setting", role)
		}
	}
}

func TestOrganizationTestSettingsRequiresExplicitValueAndRevision(t *testing.T) {
	s := &Server{}
	for _, body := range []string{`{}`, `{"expected_revision":0}`, `{"recalculate_on_test_change":false}`, `{"expected_revision":-1,"recalculate_on_test_change":true}`, `{"expected_revision":0,"recalculate_on_test_change":null}`, `{"expected_revision":0,"recalculate_on_test_change":false,"organization_id":2}`} {
		r := httptest.NewRequest("PUT", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		_, _, err := s.organizationTestSettingsUpdate(r, Actor{OrganizationAdmin: true, OrganizationID: 1}, 0)
		if err == nil {
			t.Fatalf("invalid setting accepted: %s", body)
		}
	}
}

func TestInternalOrganizationTestSettingsRequiresServiceKey(t *testing.T) {
	s := &Server{InternalKey: strings.Repeat("k", 32)}
	for _, key := range []string{"", "wrong", strings.Repeat("x", 32)} {
		r := httptest.NewRequest("GET", "/internal/core/organizations/1/test-settings", nil)
		r.Header.Set("X-Service-Key", key)
		r.Header.Set("Authorization", "Bearer ordinary-user-token")
		w := httptest.NewRecorder()
		s.internalOrganizationTestSettings(w, r)
		if w.Code != 401 {
			t.Fatalf("internal settings allowed a user token: %d", w.Code)
		}
	}
}
