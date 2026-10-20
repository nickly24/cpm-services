package uploads

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const templateXLSXMIME = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

func templateTestActor() Actor {
	a := Actor{Role: "staff_admin", Permissions: map[string]Permission{}}
	for _, section := range []string{"upload", "users", "train", "tests", "test-results", "exams", "assignments", "zaps"} {
		a.Permissions[section] = Permission{View: true, Edit: true}
	}
	return a
}

func templateTestRequest(kind, query string) *http.Request {
	r := httptest.NewRequest("GET", "http://uploads.invalid/template"+query, nil)
	r.SetPathValue("type", kind)
	r.SetPathValue("division", "7")
	r.Header.Set("Authorization", "Bearer synthetic-template-token")
	r.Header.Set("X-Organization-ID", "999")
	r.Header.Set("X-Division-ID", "999")
	r.Header.Set("X-Account-ID", "999")
	r.Header.Set("Cookie", "synthetic-browser-cookie=private")
	return r
}

func templateTestServer(ownerURL string) *Server {
	return &Server{HTTP: newHTTP(), Config: Config{CoreURL: ownerURL, CardsURL: ownerURL, ExamsURL: ownerURL, TestsURL: ownerURL}}
}

func TestTemplateCatalogAdvertisesApplicability(t *testing.T) {
	applicable := map[string]bool{
		"users.credentials": true, "users.legacy": true, "cards.xlsx": true,
		"tests.json": true, "tests.external-results": true,
		"exams.questions": true, "exams.assignments": true, "exams.outside": true,
		"cards.transform": false, "homework.pdf": false, "attendance.zap-images": false,
	}
	for _, kind := range Catalog() {
		want, known := applicable[kind.ID]
		if !known || kind.Template != want {
			t.Fatalf("unexpected template capability for %s", kind.ID)
		}
		b, e := json.Marshal(kind)
		var public map[string]any
		if e != nil || json.Unmarshal(b, &public) != nil || public["template"] != want {
			t.Fatalf("catalog did not expose template applicability for %s", kind.ID)
		}
	}
}

func TestTemplateRequiresUploadAndSubjectPermissionsBeforeOwnerRequest(t *testing.T) {
	var calls atomic.Int64
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(500)
	}))
	defer owner.Close()
	s := templateTestServer(owner.URL)
	for _, kind := range Catalog() {
		if !kind.Template {
			continue
		}
		for _, missing := range []string{"upload", kind.Permission, "staff_role"} {
			t.Run(kind.ID+"/"+missing, func(t *testing.T) {
				a := templateTestActor()
				if missing == "staff_role" {
					a.Role = "student"
				} else {
					delete(a.Permissions, missing)
				}
				w := httptest.NewRecorder()
				e := s.template(w, templateTestRequest(kind.ID, "?target_id=83"), a, Scope{42, 7, 9})
				assertOwnerAPIError(t, e, 403)
				if calls.Load() != 0 || w.Body.Len() != 0 {
					t.Fatal("denied template access contacted the owner or returned file bytes")
				}
			})
		}
	}
}

func TestNonFileTemplatesRejectWithoutOwnerRequest(t *testing.T) {
	var calls atomic.Int64
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer owner.Close()
	s := templateTestServer(owner.URL)
	for _, kind := range []string{"cards.transform", "homework.pdf", "attendance.zap-images"} {
		t.Run(kind, func(t *testing.T) {
			w := httptest.NewRecorder()
			e := s.template(w, templateTestRequest(kind, ""), templateTestActor(), Scope{42, 7, 9})
			assertOwnerAPIError(t, e, 400)
			var ae *APIError
			if !errors.As(e, &ae) || ae.Message != "template_not_applicable" || calls.Load() != 0 || w.Body.Len() != 0 {
				t.Fatal("a type without file input attempted to download a template")
			}
		})
	}
}

func TestTemplateRejectsInvalidNativeTargetBeforeOwnerRequest(t *testing.T) {
	var calls atomic.Int64
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer owner.Close()
	s := templateTestServer(owner.URL)
	for _, tc := range []struct{ kind, query string }{
		{"missing.type", ""}, {"cards.xlsx", "?target_id=83"}, {"users.credentials", "?target_id=83"},
		{"exams.questions", ""}, {"exams.assignments", "?target_id=0"}, {"exams.outside", "?target_id=83%2F..%2F84"},
	} {
		t.Run(tc.kind+tc.query, func(t *testing.T) {
			w := httptest.NewRecorder()
			e := s.template(w, templateTestRequest(tc.kind, tc.query), templateTestActor(), Scope{42, 7, 9})
			assertOwnerAPIError(t, e, 400)
			if calls.Load() != 0 || w.Body.Len() != 0 {
				t.Fatal("invalid template target reached the owner")
			}
		})
	}
}

func TestLegacyTemplateRequiresBothEditPermissionsBeforeOwnerRequest(t *testing.T) {
	var calls atomic.Int64
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer owner.Close()
	for _, section := range []string{"upload", "users"} {
		t.Run(section, func(t *testing.T) {
			a := templateTestActor()
			a.Permissions[section] = Permission{View: true, Edit: false}
			w := httptest.NewRecorder()
			e := templateTestServer(owner.URL).template(w, templateTestRequest("users.legacy", ""), a, Scope{42, 7, 9})
			assertOwnerAPIError(t, e, 403)
			if calls.Load() != 0 || w.Body.Len() != 0 {
				t.Fatal("legacy template bypassed the native edit permission gate")
			}
		})
	}
}

func TestTemplateQueriesRejectOverridesDuplicatesAndMissingOwnerContext(t *testing.T) {
	var calls atomic.Int64
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer owner.Close()
	for _, tc := range []struct{ kind, query string }{
		{"cards.xlsx", "organization_id=999"}, {"users.legacy", "division_id=999"}, {"users.credentials", "limit=10"},
		{"exams.questions", "target_id=83&organization_id=999"}, {"exams.questions", "target_id=83&target_id=84"},
		{"exams.questions", "target_id=%GG"}, {"exams.questions", "target_id=83;target_id=84"},
		{"tests.json", ""}, {"tests.json", "direction_id=0"}, {"tests.json", "direction_id=83%2F84"},
		{"tests.json", "direction_id=83&direction_id=84"}, {"tests.json", "direction_id=83&format=cpm_json&format=online_test_pad"},
		{"tests.json", "direction_id=83&format=executable"}, {"tests.json", "direction_id=83&target_id=999"},
		{"tests.json", "direction_id=83&account_id=999"}, {"tests.json", "direction_id=83&format=%GG"},
		{"tests.external-results", ""}, {"tests.external-results", "target_id=83"},
		{"tests.external-results", "target_id=abcdefabcdefabcdefabcdef%2F.."},
		{"tests.external-results", "target_id=abcdefabcdefabcdefabcdef&target_id=abcdefabcdefabcdefabcdef"},
		{"tests.external-results", "target_id=abcdefabcdefabcdefabcdef&direction_id=83"},
	} {
		t.Run(tc.kind+"/"+tc.query, func(t *testing.T) {
			r := templateTestRequest(tc.kind, "")
			r.URL.RawQuery = tc.query
			w := httptest.NewRecorder()
			e := templateTestServer(owner.URL).template(w, r, templateTestActor(), Scope{42, 7, 9})
			assertOwnerAPIError(t, e, 400)
			if calls.Load() != 0 || w.Body.Len() != 0 {
				t.Fatal("invalid query reached an owner or returned template bytes")
			}
		})
	}
}

func TestNativeXLSXTemplatesPreserveExactBytesAndControlledHeaders(t *testing.T) {
	content := sourceTestArchive(t, `<row r="1"><c r="A1" t="inlineStr"><is><t>Synthetic header</t></is></c></row>`, nil)
	for _, tc := range []struct{ kind, query, path string }{
		{"users.legacy", "", "/v1/core/divisions/7/legacy-imports/template"},
		{"cards.xlsx", "", "/v1/cards/imports/template"},
		{"exams.questions", "?target_id=00083", "/v1/exams/83/imports/questions/template"},
		{"exams.assignments", "?target_id=83", "/v1/exams/83/imports/assignments/template"},
		{"exams.outside", "?target_id=83", "/v1/exams/83/imports/outside/template"},
		{"tests.external-results", "?target_id=external_ABCDEFABCDEFABCDEFABCDEF", "/v1/tests/divisions/7/external-tests/abcdefabcdefabcdefabcdef/imports/results/template"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			var calls atomic.Int64
			owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "GET" || r.URL.Path != tc.path || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer synthetic-template-token" {
					t.Error("template request lost the protected owner route or actor authorization")
				}
				for _, name := range []string{"X-Organization-ID", "X-Division-ID", "X-Account-ID", "Cookie"} {
					if r.Header.Get(name) != "" {
						t.Error("template forwarded a caller-supplied actor or context header")
					}
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Disposition", `attachment; filename="untrusted-owner-name.exe"`)
				w.Header().Set("Set-Cookie", "synthetic-owner-cookie=private")
				w.Write(content)
			}))
			defer owner.Close()
			w := httptest.NewRecorder()
			r := templateTestRequest(tc.kind, tc.query)
			r.SetPathValue("division", "999") // Verified scope, rather than caller context, owns routing.
			e := templateTestServer(owner.URL).template(w, r, templateTestActor(), Scope{42, 7, 9})
			if e != nil || calls.Load() != 1 || !bytes.Equal(w.Body.Bytes(), content) {
				t.Fatal("template bytes changed or owner route failed", e)
			}
			if w.Header().Get("Content-Type") != templateXLSXMIME || w.Header().Get("Content-Disposition") != `attachment; filename="`+strings.ReplaceAll(tc.kind, ".", "-")+`-template.xlsx"` || w.Header().Get("Set-Cookie") != "" {
				t.Fatal("template exposed unsafe owner headers or an incorrect MIME")
			}
		})
	}
}

func TestTestsJSONAndJSTemplatesPreserveBytesAndCanonicalOwnerContext(t *testing.T) {
	jsonBody := []byte("{\n  \"title\": \"Синтетический тест\", \"questions\": []\n}\n")
	jsBody := []byte("var test = {\"Name\":\"Синтетический тест\",\"Questions\":[]};\n")
	nestedJSBody := append([]byte{0xef, 0xbb, 0xbf}, []byte(" \n\t var \t test =\n"+`{"Name":"Quote \" and brace }", "Questions":[{"AnswerTemplates":[{"Text":"escaped \\ and ; { }"}]}]}`+" ; \n")...)
	for _, tc := range []struct {
		name, query, format, mime, ext string
		body                           []byte
	}{
		{"default_json", "?direction_id=00083", "cpm_json", "application/json", "json", jsonBody},
		{"explicit_json", "?format=cpm_json&direction_id=83", "cpm_json", "application/json", "json", jsonBody},
		{"online_test_pad", "?format=online_test_pad&direction_id=83", "online_test_pad", "text/javascript", "js", jsBody},
		{"online_test_pad_without_semicolon", "?direction_id=83&format=online_test_pad", "online_test_pad", "text/javascript", "js", []byte(`var test = {"Name":"synthetic","Questions":[]}`)},
		{"online_test_pad_bom_whitespace_nested_escapes", "?direction_id=83&format=online_test_pad", "online_test_pad", "text/javascript", "js", nestedJSBody},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				wantQuery := "direction_id=83&format=" + tc.format
				if r.Method != "GET" || r.URL.Path != "/v1/tests/divisions/7/imports/json/template" || r.URL.RawQuery != wantQuery || r.Header.Get("Authorization") != "Bearer synthetic-template-token" {
					t.Error("Tests template did not canonicalize its verified owner context")
				}
				if r.Header.Get("X-Division-ID") != "" || r.Header.Get("X-Organization-ID") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Service-Key") != "" {
					t.Error("public Tests template forwarded unverified caller or internal context")
				}
				w.Header().Set("Content-Type", tc.mime+"; charset=utf-8")
				w.Write(tc.body)
			}))
			defer owner.Close()
			a := templateTestActor()
			a.Permissions["upload"] = Permission{View: true}
			a.Permissions["tests"] = Permission{View: true}
			r := templateTestRequest("tests.json", tc.query)
			r.SetPathValue("division", "999")
			w := httptest.NewRecorder()
			e := templateTestServer(owner.URL).template(w, r, a, Scope{42, 7, 9})
			if e != nil || calls.Load() != 1 || !bytes.Equal(w.Body.Bytes(), tc.body) {
				t.Fatal("Tests template changed its source bytes or denied native view access", e)
			}
			if w.Header().Get("Content-Type") != tc.mime || w.Header().Get("Content-Disposition") != `attachment; filename="tests-json-template.`+tc.ext+`"` {
				t.Fatal("Tests template returned a different MIME or extension")
			}
		})
	}
}

func TestCredentialsCSVTemplateContainsOnlyCanonicalHeader(t *testing.T) {
	var calls atomic.Int64
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer owner.Close()
	w := httptest.NewRecorder()
	e := templateTestServer(owner.URL).template(w, templateTestRequest("users.credentials", ""), templateTestActor(), Scope{42, 7, 9})
	want := "full_name,login,password,class,tg_name,school_id,group_id,role\n"
	if e != nil || calls.Load() != 0 || w.Body.String() != want || w.Header().Get("Content-Type") != "text/csv; charset=utf-8" || w.Header().Get("Content-Disposition") != `attachment; filename="users-credentials-template.csv"` {
		t.Fatal("credentials template contains generated data or lost the canonical header", e)
	}
}

func TestOwnerTemplateRejectsUnsafeOrOversizedXLSXWithoutPartialOutput(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content []byte
	}{
		{"empty", nil}, {"not_xlsx", []byte("synthetic-owner-private-data")},
		{"too_large", bytes.Repeat([]byte{'x'}, maxFile+1)},
		{"traversal", sourceTestArchive(t, "", map[string]string{"../unsafe.xml": `<unsafe/>`})},
		{"macros", sourceTestArchive(t, "", map[string]string{"xl/vbaProject.bin": "synthetic"})},
		{"external_link", sourceTestArchive(t, "", map[string]string{"xl/externalLinks/externalLink1.xml": `<externalLink/>`})},
		{"external_relationship", sourceTestArchive(t, "", map[string]string{"xl/_rels/sheet.xml.rels": `<Relationships><Relationship TargetMode="External" Target="https://example.invalid/private"/></Relationships>`})},
		{"formula", sourceTestArchive(t, `<row><c><f>1+1</f></c></row>`, nil)},
		{"xml_entity", sourceTestArchive(t, "", map[string]string{"xl/custom.xml": `<!DOCTYPE root [<!ENTITY x "synthetic">]><root>&x;</root>`})},
		{"rows_above_owner_limit", sourceTestArchive(t, strings.Repeat(`<row/>`, 1012), nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", templateXLSXMIME)
				w.Write(tc.content)
			}))
			defer owner.Close()
			w := httptest.NewRecorder()
			e := templateTestServer(owner.URL).template(w, templateTestRequest("cards.xlsx", ""), templateTestActor(), Scope{42, 7, 9})
			assertOwnerAPIError(t, e, 503)
			if w.Body.Len() != 0 || w.Header().Get("Content-Disposition") != "" || strings.Contains(e.Error(), "synthetic-owner-private") {
				t.Fatal("invalid owner template leaked partial bytes or private source details")
			}
		})
	}
}

func TestJSONTemplateRejectsMalformedOwnerPayloadWithoutPartialOutput(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content []byte
	}{
		{"empty", nil},
		{"html", []byte(`<html>synthetic-private-owner-error</html>`)},
		{"truncated", []byte(`{"title":"synthetic","questions":[`)},
		{"script_in_json_format", []byte(`var test = {"title":"synthetic","questions":[]};`)},
		{"invalid_utf8", []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Write(tc.content)
			}))
			defer owner.Close()
			w := httptest.NewRecorder()
			e := templateTestServer(owner.URL).template(w, templateTestRequest("tests.json", "?direction_id=83&format=cpm_json"), templateTestActor(), Scope{42, 7, 9})
			assertOwnerAPIError(t, e, 503)
			if w.Body.Len() != 0 || w.Header().Get("Content-Disposition") != "" || strings.Contains(e.Error(), "synthetic-private") {
				t.Fatal("malformed JSON template exposed owner bytes or details")
			}
		})
	}
}

func TestTemplateRejectsMIMEConfusionWithoutFileOutput(t *testing.T) {
	xlsx := sourceTestArchive(t, "", nil)
	for _, tc := range []struct {
		name, kind, query, mime string
		body                    []byte
	}{
		{"xlsx_as_html", "cards.xlsx", "", "text/html", xlsx},
		{"json_as_text", "tests.json", "?direction_id=83", "text/plain", []byte(`{"title":"synthetic","questions":[]}`)},
		{"json_as_js", "tests.json", "?direction_id=83", "text/javascript", []byte(`{"title":"synthetic","questions":[]}`)},
		{"js_as_json", "tests.json", "?direction_id=83&format=online_test_pad", "application/json", []byte(`var test = {"Name":"synthetic","Questions":[]};`)},
		{"invalid_mime", "tests.json", "?direction_id=83", "application/json; charset=", []byte(`{"title":"synthetic","questions":[]}`)},
		{"json_non_utf8_charset", "tests.json", "?direction_id=83", "application/json; charset=windows-1251", []byte(`{"title":"synthetic","questions":[]}`)},
		{"js_non_utf8_charset", "tests.json", "?direction_id=83&format=online_test_pad", "text/javascript; charset=iso-8859-1", []byte(`var test = {"Name":"synthetic","Questions":[]};`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.mime)
				w.Write(tc.body)
			}))
			defer owner.Close()
			w := httptest.NewRecorder()
			e := templateTestServer(owner.URL).template(w, templateTestRequest(tc.kind, tc.query), templateTestActor(), Scope{42, 7, 9})
			assertOwnerAPIError(t, e, 503)
			if w.Body.Len() != 0 || w.Header().Get("Content-Disposition") != "" {
				t.Fatal("template returned file bytes under an unexpected owner MIME")
			}
		})
	}
}

func TestOnlineTestPadTemplateRejectsExecutableValuesAndMalformedSource(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"no_literal", []byte(`globalThis.syntheticSideEffect = "never execute";`)},
		{"function_value", []byte(`var test = {"Name":"synthetic","Questions": buildQuestions()};`)},
		{"function_before_object", []byte(`var test = buildQuestions() || {"Name":"synthetic","Questions":[]};`)},
		{"code_before_assignment", []byte(`globalThis.syntheticSideEffect(); var test = {"Name":"synthetic","Questions":[]};`)},
		{"code_after_assignment", []byte(`var test = {"Name":"synthetic","Questions":[]}; globalThis.syntheticSideEffect();`)},
		{"double_semicolon", []byte(`var test = {"Name":"synthetic","Questions":[]};;`)},
		{"truncated_object", []byte(`var test = {"Name":"synthetic","Questions":[`)},
		{"unquoted_properties", []byte(`var test = {Name:"synthetic",Questions:[]};`)},
		{"invalid_utf8", append([]byte(`var test = {"Name":"`), 0xff, '"', '}', ';')},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
				w.Write(tc.body)
			}))
			defer owner.Close()
			w := httptest.NewRecorder()
			e := templateTestServer(owner.URL).template(w, templateTestRequest("tests.json", "?direction_id=83&format=online_test_pad"), templateTestActor(), Scope{42, 7, 9})
			assertOwnerAPIError(t, e, 503)
			if w.Body.Len() != 0 || w.Header().Get("Content-Disposition") != "" {
				t.Fatal("nonliteral OnlineTestPad source was returned as a template")
			}
		})
	}
}

func TestTemplateOwnerFailuresAndRedirectsDoNotExposePrivateBytes(t *testing.T) {
	var redirected atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer target.Close()
	for _, status := range []int{401, 403, 404, 500, 302} {
		owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", target.URL)
			w.WriteHeader(status)
			w.Write([]byte("synthetic-private-template-error"))
		}))
		w := httptest.NewRecorder()
		e := templateTestServer(owner.URL).template(w, templateTestRequest("cards.xlsx", ""), templateTestActor(), Scope{42, 7, 9})
		owner.Close()
		if e == nil || w.Body.Len() != 0 || strings.Contains(e.Error(), "synthetic-private") || w.Header().Get("Location") != "" {
			t.Fatal("owner template failure exposed source details or redirected the caller")
		}
	}
	if redirected.Load() != 0 {
		t.Fatal("template download forwarded actor authorization across an owner redirect")
	}
}

func TestTestsQuestionTableTemplateForwardsXLSXWithReadOnlyRights(t *testing.T) {
	body := sourceTestArchive(t, `<row r="1"><c r="A1" t="inlineStr"><is><t>Synthetic header</t></is></c></row>`, nil) // structural XLSX fixture used by the existing file policy tests
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/v1/tests/divisions/7/imports/json/template?direction_id=83&format=question_table" {
			t.Error("wrong Excel template target", r.URL.RequestURI())
		}
		w.Header().Set("Content-Type", templateXLSXMIME)
		w.Write(body)
	}))
	defer owner.Close()
	actor := templateTestActor()
	actor.Permissions["upload"] = Permission{View: true}
	actor.Permissions["tests"] = Permission{View: true}
	w := httptest.NewRecorder()
	err := templateTestServer(owner.URL).template(w, templateTestRequest("tests.json", "?direction_id=83&format=question_table"), actor, Scope{42, 7, 9})
	if err != nil || !bytes.Equal(w.Body.Bytes(), body) || w.Header().Get("Content-Type") != templateXLSXMIME || !strings.Contains(w.Header().Get("Content-Disposition"), ".xlsx") {
		t.Fatal("Excel template failed", err)
	}
}
