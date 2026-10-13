package tests

import (
	"errors"
	"net/http/httptest"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestSubmissionSelectionPreservesScopeAndSummaryFilters(t *testing.T) {
	testID := bson.NewObjectID()
	division := int64(17)
	viewer := Actor{Role: "staff_admin", OrganizationID: 11, DivisionID: &division, Permissions: map[string]Permission{"test-results": {View: true}}}
	r := httptest.NewRequest("GET", "/?account_id=23&calculation_status=ready&test_id="+testID.Hex(), nil)
	filter, err := submissionFilter(r, viewer, division)
	if err != nil {
		t.Fatal(err)
	}
	wantFields := bson.M{"organization_id": int64(11), "division_id": division, "account_id": int64(23), "test_id": testID, "calculation_status": "ready", "active": true, "status": "accepted", "practice": false}
	for field, want := range wantFields {
		if filter[field] != want {
			t.Fatalf("selection field %s: got %#v want %#v", field, filter[field], want)
		}
	}
	student := Actor{Role: "student", OrganizationID: 11, AccountID: 31, DivisionID: &division}
	filter, err = submissionFilter(r, student, division)
	if err != nil || filter["account_id"] != student.AccountID {
		t.Fatal("request account filter expanded the student's scope")
	}
	_, err = submissionFilter(r, Actor{Role: "staff_admin", OrganizationID: 11, Permissions: map[string]Permission{"tests": {View: true, Edit: true}}}, division)
	if !errors.Is(err, denied) {
		t.Fatal("catalog permission expanded access to results")
	}
}

func TestSubmissionSelectionRejectsInvalidFilters(t *testing.T) {
	admin := Actor{Role: "admin", OrganizationID: 11, OrganizationAdmin: true}
	for _, query := range []string{"account_id=zero", "account_id=0", "test_id=wrong", "calculation_status=done"} {
		r := httptest.NewRequest("GET", "/?"+query, nil)
		_, err := submissionFilter(r, admin, 17)
		var api *APIError
		if !errors.As(err, &api) || api.Code != 400 {
			t.Fatalf("invalid filter accepted: %s (%v)", query, err)
		}
	}
}

func TestAcceptedSubmissionHasNoScoreUntilCalculation(t *testing.T) {
	for _, status := range []string{"queued", "processing", "retrying", "failed"} {
		view := submissionView(Attempt{Status: "accepted", CalculationStatus: status})
		if view.Result != nil || view.SubmissionStatus != "accepted" || view.CalculationStatus != status {
			t.Fatalf("pending status fabricated a result: %+v", view)
		}
	}
	view := submissionView(Attempt{Status: "accepted", CalculationStatus: "ready", Result: &GradeResult{Score: 0}})
	if view.Result == nil || view.Result.Score != 0 {
		t.Fatal("ready zero score was confused with a pending result")
	}
}

func TestAttemptListRejectsUnknownPracticeFilter(t *testing.T) {
	r := httptest.NewRequest("GET", "/?practice=unknown", nil)
	_, _, err := (&Server{}).attemptList(r, Actor{Role: "student", OrganizationID: 11, AccountID: 31}, 17)
	var api *APIError
	if !errors.As(err, &api) || api.Code != 400 || api.Message != "invalid_practice_filter" {
		t.Fatalf("unknown practice filter silently became official attempts: %v", err)
	}
}
