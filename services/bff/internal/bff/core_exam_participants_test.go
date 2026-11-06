package bff

import (
	"net/url"
	"testing"
)

func TestCoreExamParticipantsRemainInSelectedDivision(t *testing.T) {
	if coreRoute("GET", "exam-participants") != "/v1/core/divisions/%d/exam-participants" {
		t.Fatal("participant scope route missing")
	}
	if coreRoute("GET", "divisions/99/exam-participants") != "" || coreRoute("POST", "exam-participants") != "" {
		t.Fatal("scope override or mutation exposed")
	}
	for _, raw := range []string{"role=examinator&q=0012&limit=50&offset=0", "limit=200&after_id=42"} {
		query, _ := url.ParseQuery(raw)
		if !coreQuery("GET", "exam-participants", query) {
			t.Fatal("valid page blocked", raw)
		}
	}
	for _, raw := range []string{"organization_id=99", "division_id=99", "password=true", "q=a&q=b"} {
		query, _ := url.ParseQuery(raw)
		if coreQuery("GET", "exam-participants", query) {
			t.Fatal("unexpected override accepted", raw)
		}
	}
}
