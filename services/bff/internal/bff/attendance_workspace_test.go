package bff

import (
	"net/url"
	"testing"
)

func TestAttendanceWorkspaceRouteAndQueryFence(t *testing.T) {
	path := "divisions/2/journal-workspace"
	if attendanceRoute("GET", path) != "/v1/attendance/"+path {
		t.Fatal("workspace route missing")
	}
	if attendanceRoute("POST", path) != "" || attendanceRoute("GET", "divisions/0/journal-workspace") != "" {
		t.Fatal("route fence")
	}
	for _, query := range []string{"date_from=2026-10-01&date_to=2026-10-31", ""} {
		q, _ := url.ParseQuery(query)
		if !attendanceQuery("GET", path, q) {
			t.Fatal("valid query", query)
		}
	}
	for _, query := range []string{"account_id=7", "group_id=11", "organization_id=1", "date_from=x&date_from=y", "date_to="} {
		q, _ := url.ParseQuery(query)
		if attendanceQuery("GET", path, q) {
			t.Fatal("query crossed workspace fence", query)
		}
	}
}
