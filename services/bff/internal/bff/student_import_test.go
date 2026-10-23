package bff

import (
	"encoding/json"
	"testing"
)

func TestStudentValidationCannotExposeNativeValues(t *testing.T) {
	for _, raw := range []string{`{"error":"student_import_validation","issues":[{"row":2,"field":"login","reason":"login_exists"}]}`, `{"error":"student_import_validation","issues":[{"row":2,"field":"password","reason":"login_exists"}]}`, `{"error":"student_import_validation","issues":[{"row":2,"field":"login","reason":"SELECT secret"}]}`} {
		v := studentValidationResponse([]byte(raw))
		var x struct {
			Issues []studentImportIssue `json:"issues"`
		}
		json.Unmarshal([]byte(raw), &x)
		valid := x.Issues[0].Field == "login" && x.Issues[0].Reason == "login_exists"
		if (v != nil) != valid {
			t.Fatal("unsafe validation boundary")
		}
	}
}
func TestStudentExplicitUploadSurfaceIsNarrow(t *testing.T) {
	for _, v := range []struct {
		m, p string
		ok   bool
	}{{"POST", "divisions/12/student-imports/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/batches/0", true}, {"GET", "divisions/12/sessions/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/job", true}, {"GET", "divisions/12/jobs", false}, {"POST", "divisions/12/files/register", false}, {"GET", "divisions/12/sessions", false}} {
		if (uploadsRoute(v.m, v.p) != "") != v.ok {
			t.Fatal(v.p)
		}
	}
}
