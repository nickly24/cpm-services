package exams

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpenAPIReferencesAndStrictCommands(t *testing.T) {
	doc := examsOpenAPI()
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	var walk func(any)
	walk = func(value any) {
		switch object := value.(type) {
		case map[string]any:
			if ref, ok := object["$ref"].(string); ok {
				prefix := "#/components/schemas/"
				if !strings.HasPrefix(ref, prefix) || schemas[strings.TrimPrefix(ref, prefix)] == nil {
					t.Errorf("unresolved schema reference %q", ref)
				}
			}
			for _, member := range object {
				walk(member)
			}
		case []any:
			for _, member := range object {
				walk(member)
			}
		}
	}
	walk(doc)
	for _, name := range []string{"ExamCreate", "ConfigCommand", "VoteCommand", "QuestionTransition", "OutsideCreate", "ImportCommit", "TrainingProgress"} {
		schema := schemas[name].(map[string]any)
		if schema["additionalProperties"] != false {
			t.Errorf("%s allows untrusted DTO fields", name)
		}
		for _, field := range []string{"organization_id", "account_id", "role", "permissions"} {
			if schema["properties"].(map[string]any)[field] != nil {
				t.Errorf("%s accepts identity override %s", name, field)
			}
		}
	}
	for _, name := range []string{"Attempt", "PresentedQuestion"} {
		for _, field := range []string{"questions_snapshot", "config_snapshot", "votes_half"} {
			if schemas[name].(map[string]any)["properties"].(map[string]any)[field] != nil {
				t.Errorf("%s exposes persistence field %s", name, field)
			}
		}
	}
}

func TestOpenAPIImportPagingAndBinaryTemplateContract(t *testing.T) {
	doc := examsOpenAPI()
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	preview := schemas["ImportPreview"].(map[string]any)["properties"].(map[string]any)
	for _, field := range []string{"source_hash", "validation_hash", "total", "limit", "offset", "has_more", "next_offset", "summary", "error_report", "expires_at", "result"} {
		if preview[field] == nil {
			t.Errorf("paged import preview missing %s", field)
		}
	}
	if preview["rows"].(map[string]any)["maxItems"] != 200 {
		t.Fatal("import preview must not document an unbounded first page")
	}
	row := schemas["ImportRow"].(map[string]any)["properties"].(map[string]any)
	for _, field := range []string{"privilege_baseline_version", "privilege_account_id"} {
		if row[field] != nil {
			t.Errorf("private import baseline exposed in public schema: %s", field)
		}
	}
	paths := doc["paths"].(map[string]any)
	get := paths["/v1/exams/{exam}/imports/{kind}/{session}"].(map[string]any)["get"].(map[string]any)
	parameters := map[string]bool{}
	for _, raw := range get["parameters"].([]any) {
		parameters[raw.(map[string]any)["name"].(string)] = true
	}
	for _, name := range []string{"limit", "offset", "expected_revision"} {
		if !parameters[name] {
			t.Errorf("import preview query missing %s", name)
		}
	}
	template := paths["/v1/exams/{exam}/imports/{kind}/template"].(map[string]any)["get"].(map[string]any)
	response := template["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)
	if response["application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"] == nil || response["application/json"] != nil {
		t.Fatal("template success must be documented as XLSX bytes without a JSON envelope")
	}
}

func TestOpenAPIDatesAndStableHistoryPages(t *testing.T) {
	paths := examsOpenAPI()["paths"].(map[string]any)
	for _, path := range []string{"/v1/exams", "/v1/exams/{exam}/results", "/v1/exams/{exam}/results/{student}"} {
		params := paths[path].(map[string]any)["get"].(map[string]any)["parameters"].([]any)
		found := map[string]bool{}
		for _, raw := range params {
			found[raw.(map[string]any)["name"].(string)] = true
		}
		for _, key := range []string{"date_from", "date_to", "from_date", "to_date"} {
			if !found[key] {
				t.Errorf("%s missing date filter %s", path, key)
			}
		}
		if found["organization_id"] || found["division_id"] {
			t.Errorf("%s documents a public scope override", path)
		}
	}
	for path, anchor := range map[string]string{"/v1/exams/{exam}/attempts/{attempt}/protocol": "through_sequence_no", "/v1/exams/{exam}/attempts/{attempt}/questions/{presented}/rounds": "through_round_no"} {
		found := false
		for _, raw := range paths[path].(map[string]any)["get"].(map[string]any)["parameters"].([]any) {
			if raw.(map[string]any)["name"] == anchor {
				found = true
			}
		}
		if !found {
			t.Errorf("%s missing stable history anchor", path)
		}
	}
}

func TestOpenAPICatalogAdministrativeFieldsAreOptional(t *testing.T) {
	schemas := examsOpenAPI()["components"].(map[string]any)["schemas"].(map[string]any)
	for _, name := range []string{"ExamSummary", "ExamDetail"} {
		schema := schemas[name].(map[string]any)
		properties := schema["properties"].(map[string]any)
		required := map[string]bool{}
		for _, field := range schema["required"].([]string) {
			required[field] = true
		}
		for _, field := range []string{"connected_division_ids", "owner_division_id", "origin_exam_id", "in_bank"} {
			if required[field] {
				t.Errorf("%s requires admin-only %s for student/examiner DTO", name, field)
			}
			if !strings.Contains(properties[field].(map[string]any)["description"].(string), "Administrative") {
				t.Errorf("%s.%s privacy restriction missing", name, field)
			}
		}
	}
}

// Check real transport objects against their declared field sets. This catches
// additive runtime DTO changes that a reference-only specification test misses.
func TestOpenAPIRuntimeDTOFieldsAreDocumented(t *testing.T) {
	schemas := examsOpenAPI()["components"].(map[string]any)["schemas"].(map[string]any)
	check := func(name string, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]any
		if err = json.Unmarshal(raw, &object); err != nil {
			t.Fatal(err)
		}
		schema := schemas[name].(map[string]any)
		properties := schema["properties"].(map[string]any)
		for field := range object {
			if properties[field] == nil {
				t.Errorf("%s runtime DTO field %s missing from specification", name, field)
			}
		}
		for _, field := range schema["required"].([]string) {
			if _, ok := object[field]; !ok {
				t.Errorf("%s specification requires omitted DTO field %s", name, field)
			}
		}
	}
	f := fixtureDomain(t, "round_down")
	check("ExamSummary", summary(f.e, f.p.DivisionID))
	detail, err := Query(f.e, f.admin, "detail", f.p)
	if err != nil {
		t.Fatal(err)
	}
	check("ExamDetail", detail)
	d := f.e.Divisions[f.p.DivisionID]
	check("Assignment", assignmentDTO(d, &d.Assignments[0]))
	at := &Attempt{ID: 999, Student: f.p.Members[11], AttemptNo: 1, Status: "completed", Config: d.Config, HistoryGeneration: 1, ResultVersion: 1}
	d.Attempts = append(d.Attempts, *at)
	check("Result", resultDTO(f.e, d, at, f.admin))
	check("Result", resultDTO(f.e, d, at, f.student))
	check("OutsideResult", outsideDTO(f.e, d, OutsideResult{ID: 123, Student: f.p.Members[11], Points: json.Number("2.5"), Grade: 0, Examinator: "Fixture"}, f.admin))
	preview, err := importPreview("00000000-0000-4000-8000-000000000001", 1, "editable", "questions", f.e, f.admin, f.p, []ImportRow{}, f.p.Now.Add(time.Hour), "fixture.xlsx", strings.Repeat("a", 64), nil, ImportPage{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	check("ImportPreview", preview)
}

type openAPIUnauthorizedDependencies struct{}

func (openAPIUnauthorizedDependencies) Identify(*http.Request) (Actor, error) {
	return Actor{}, &APIError{401, "invalid_token"}
}
func (openAPIUnauthorizedDependencies) References(*http.Request, int64, []int64, int64, []int64) (map[int64]Member, error) {
	return nil, Denied
}
func (openAPIUnauthorizedDependencies) Ready(*http.Request) error { return Denied }

// This fails when the spec declares a business route that is not registered, or
// when a route starts processing DTO/SQL before enforcing bearer authentication.
func TestOpenAPIBusinessRoutesRequireAuthentication(t *testing.T) {
	handler := (&Server{Deps: openAPIUnauthorizedDependencies{}}).Handler()
	for path, value := range examsOpenAPI()["paths"].(map[string]any) {
		if !strings.HasPrefix(path, "/v1/exams") {
			continue
		}
		concrete := path
		for _, key := range []string{"exam", "part", "question", "commission", "assignment", "attempt", "student", "result", "presented", "division"} {
			concrete = strings.ReplaceAll(concrete, "{"+key+"}", "1")
		}
		concrete = strings.ReplaceAll(concrete, "{kind}", "questions")
		concrete = strings.ReplaceAll(concrete, "{session}", "00000000-0000-4000-8000-000000000001")
		for method, operation := range value.(map[string]any) {
			op := operation.(map[string]any)
			if method != "get" {
				found := false
				for _, parameter := range op["parameters"].([]any) {
					p := parameter.(map[string]any)
					if p["name"] == "Idempotency-Key" && p["in"] == "header" && p["required"] == true {
						found = true
					}
				}
				if !found {
					t.Errorf("%s %s omits required idempotency header", method, path)
				}
			}
			r := httptest.NewRequest(strings.ToUpper(method), concrete, strings.NewReader(`{}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Role", "admin")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 401 {
				t.Errorf("%s %s requires authentication: got %d", method, path, w.Code)
			}
		}
	}
	request := httptest.NewRequest("GET", "/openapi.json", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var doc map[string]any
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &doc) != nil || doc["openapi"] != "3.1.0" || doc["data"] != nil {
		t.Fatal("OpenAPI endpoint must return its direct valid document")
	}
}
