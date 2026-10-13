package core

import (
	"archive/zip"
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestLegacyTemplateWorkbookRoundtripMatchesOldFiveFieldContract(t *testing.T) {
	file, e := legacyImportTemplateWorkbook()
	if e != nil {
		t.Fatal(e)
	}
	archive, e := zip.NewReader(bytes.NewReader(file), int64(len(file)))
	if e != nil {
		t.Fatal("template is not an XLSX ZIP", e)
	}
	for _, part := range archive.File {
		if strings.Contains(part.Name, "vba") || strings.Contains(part.Name, "externalLink") {
			t.Fatal("unexpected active or external workbook content")
		}
		reader, e := part.Open()
		if e != nil {
			t.Fatal(e)
		}
		content, e := io.ReadAll(reader)
		reader.Close()
		if e != nil || strings.Contains(string(content), "pbkdf2:") || strings.Contains(string(content), "password") || strings.Contains(string(content), "login") {
			t.Fatal("secret or credential fields in template")
		}
	}
	book, e := excelize.OpenReader(bytes.NewReader(file))
	if e != nil {
		t.Fatal(e)
	}
	defer book.Close()
	if !reflect.DeepEqual(book.GetSheetList(), []string{"Ученики"}) || book.GetActiveSheetIndex() != 0 {
		t.Fatal("legacy parser must read the one active sheet")
	}
	rows, e := book.GetRows("Ученики")
	if e != nil || len(rows) != 2 || !reflect.DeepEqual(rows[0], []string{"ФИО", "Класс", "Школа", "Проктор", "Telegram"}) || len(rows[1]) != 5 {
		t.Fatal("old XLSX columns changed", rows, e)
	}
	if !reflect.DeepEqual(rows[1], []string{"Примеров Ученик", "7", "Пример школы", "Примеров Проктор", "@example_student"}) {
		t.Fatal("unexpected non-synthetic sample", rows[1])
	}
	// Mirror the documented Flask/Uploads aliases into the native parsed DTO.
	// The numeric class and proctor-derived group must survive the workbook.
	parsed, e := legacyRows([]LegacyImportRow{{Row: 2, FullName: rows[1][0], ClassRaw: rows[1][1], SchoolName: rows[1][2], ProctorName: rows[1][3], TGName: rows[1][4]}})
	if e != nil {
		t.Fatal("template cannot enter native preview", e)
	}
	preview := legacyBuildPreview(parsed, legacyEmptyIndex(), legacyAdmin())
	if !preview.CanCommit || preview.Summary.StudentsCreate != 1 || preview.Summary.ProctorsCreate != 1 || preview.Summary.GroupsCreate != 1 || preview.Summary.SchoolsCreate != 1 || preview.Students[0].Class == nil || *preview.Students[0].Class != 7 || preview.Groups["примеров|проктор"].Name != "Примеров Проктор" {
		t.Fatal("template sample loses the native entity mapping", preview)
	}
}

func legacyTemplateRequest(t *testing.T, actor Actor, path string, authenticated bool, authStatus int, steps []scopeGuardStep) *httptest.ResponseRecorder {
	t.Helper()
	db, _ := scopeGuardDB(t, steps)
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/introspect" || r.Header.Get("X-Organization-Id") != "" || r.Header.Get("X-Role") != "" {
			t.Error("template identity bypasses native introspection")
		}
		if authStatus != 200 {
			w.WriteHeader(authStatus)
			return
		}
		_ = json.NewEncoder(w).Encode(Identity{Status: true, Active: true, User: actor})
	}))
	t.Cleanup(auth.Close)
	s := &Server{DB: db, Auth: NewAuthClient(Config{AuthURL: auth.URL})}
	r := httptest.NewRequest("GET", path, nil)
	if authenticated {
		r.Header.Set("Authorization", "Bearer synthetic-test")
	}
	r.Header.Set("X-Organization-Id", "999")
	r.Header.Set("X-Role", "admin")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func legacyTemplateDivision(found bool) scopeGuardStep {
	step := scopeGuardStep{contains: []string{"JOIN organizations o ON o.id=d.organization_id AND o.is_active=1", "WHERE d.id=? AND d.organization_id=? AND d.is_active=1"}, args: []any{int64(2), int64(1)}, columns: []string{"id"}}
	if found {
		step.rows = [][]driver.Value{{int64(2)}}
	}
	return step
}

func TestLegacyTemplateHTTPProtectedBinaryAndCurrentScopedPermissions(t *testing.T) {
	editor := Actor{AccountID: 7, OrganizationID: 1, Role: "staff_admin", DivisionID: legacyInt(2), Permissions: map[string]Permission{"upload": {true, true}, "users": {true, true}}}
	for _, test := range []struct {
		name  string
		actor Actor
	}{
		{"full_admin", legacyAdmin()},
		{"canonical_scoped_editor", editor},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := legacyTemplateRequest(t, test.actor, "/v1/core/divisions/2/legacy-imports/template", true, 200, []scopeGuardStep{legacyTemplateDivision(true)})
			if w.Code != 200 || w.Header().Get("Content-Type") != legacyImportTemplateMIME || w.Header().Get("Content-Disposition") != `attachment; filename="users-legacy-template.xlsx"` || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("protected binary headers invalid", w.Code, w.Header())
			}
			if _, e := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len())); e != nil {
				t.Fatal("JSON masquerades as binary", e)
			}
		})
	}
	for _, field := range []string{"upload", "users"} {
		for _, permission := range []Permission{{false, false}, {true, false}, {false, true}} {
			t.Run(field+"_withdrawn_"+strings.ReplaceAll(strings.TrimSpace(stringifyPermission(permission)), " ", "_"), func(t *testing.T) {
				actor := editor
				actor.Permissions = map[string]Permission{"upload": {true, true}, "users": {true, true}}
				actor.Permissions[field] = permission
				w := legacyTemplateRequest(t, actor, "/v1/core/divisions/2/legacy-imports/template", true, 200, nil)
				if w.Code != 403 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
					t.Fatal("live withdrawn permission still downloaded a workbook", w.Code)
				}
			})
		}
	}
}

func stringifyPermission(permission Permission) string {
	value, _ := json.Marshal(permission)
	return string(value)
}

func TestLegacyTemplateRejectsOrdinaryRolesAndWrongSelectedDivision(t *testing.T) {
	for _, role := range []string{"student", "proctor", "examinator", "supervisor"} {
		t.Run(role, func(t *testing.T) {
			actor := Actor{AccountID: 7, OrganizationID: 1, Role: role, OrganizationAdmin: true, DivisionID: legacyInt(2), Permissions: map[string]Permission{"upload": {true, true}, "users": {true, true}}}
			if w := legacyTemplateRequest(t, actor, "/v1/core/divisions/2/legacy-imports/template", true, 200, nil); w.Code != 403 {
				t.Fatal("forged ordinary-role admin or section permissions accepted", w.Code)
			}
		})
	}
	for _, division := range []*int64{nil, legacyInt(9)} {
		actor := Actor{AccountID: 7, OrganizationID: 1, Role: "staff_admin", DivisionID: division, Permissions: map[string]Permission{"upload": {true, true}, "users": {true, true}}}
		if w := legacyTemplateRequest(t, actor, "/v1/core/divisions/2/legacy-imports/template", true, 200, nil); w.Code != 403 {
			t.Fatal("scoped actor bypassed selected division", w.Code)
		}
	}
}

func TestLegacyTemplateRejectsForeignArchivedScopeAndQueryOverrides(t *testing.T) {
	if w := legacyTemplateRequest(t, legacyAdmin(), "/v1/core/divisions/2/legacy-imports/template", true, 200, []scopeGuardStep{legacyTemplateDivision(false)}); w.Code != 404 {
		t.Fatal("missing/foreign/inactive scope produced a workbook", w.Code)
	}
	for _, path := range []string{"/v1/core/divisions/0/legacy-imports/template", "/v1/core/divisions/not-an-id/legacy-imports/template", "/v1/core/divisions/2/legacy-imports/template?organization_id=999", "/v1/core/divisions/2/legacy-imports/template?division_id=9", "/v1/core/divisions/2/legacy-imports/template?limit=100"} {
		if w := legacyTemplateRequest(t, legacyAdmin(), path, true, 200, nil); w.Code != 400 {
			t.Fatal("template accepted invalid scope or query", path, w.Code)
		}
	}
}

func TestLegacyTemplateAuthFailureNeverProducesWorkbook(t *testing.T) {
	for _, code := range []int{401, 403, 500, 503} {
		w := legacyTemplateRequest(t, legacyAdmin(), "/v1/core/divisions/2/legacy-imports/template", true, code, nil)
		if w.Code != 401 && w.Code != 503 {
			t.Fatal("auth dependency failed open", w.Code)
		}
	}
	if w := legacyTemplateRequest(t, legacyAdmin(), "/v1/core/divisions/2/legacy-imports/template", false, 200, nil); w.Code != 401 {
		t.Fatal("missing token downloaded a workbook", w.Code)
	}
}

func TestLegacyTemplateOpenAPIDeclaresProtectedXLSXAndOnlyDivision(t *testing.T) {
	op := openAPI()["paths"].(map[string]any)[legacyImportTemplatePath].(map[string]any)["get"].(map[string]any)
	parameters := op["parameters"].([]any)
	if len(parameters) != 1 || parameters[0].(map[string]any)["name"] != "division" || parameters[0].(map[string]any)["required"] != true || !reflect.DeepEqual(op["security"], []any{map[string]any{"bearerAuth": []string{}}}) {
		t.Fatal("template contract permits ambiguous scope or lacks auth")
	}
	response := op["responses"].(map[string]any)["200"].(map[string]any)
	content := response["content"].(map[string]any)
	if len(content) != 1 || content[legacyImportTemplateMIME].(map[string]any)["schema"].(map[string]any)["format"] != "binary" {
		t.Fatal("template advertised as JSON")
	}
}
