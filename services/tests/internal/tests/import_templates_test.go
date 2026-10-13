package tests

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/drivertest"
)

func TestImportTemplatesRoundTripThroughActualConverters(t *testing.T) {
	for _, format := range []string{"cpm_json", "online_test_pad"} {
		file, err := jsonTemplateBytes("Математика", format)
		if err != nil {
			t.Fatal(err)
		}
		payload := json.RawMessage(file.Body)
		if format == "online_test_pad" {
			payload, _ = json.Marshal(map[string]any{"importFormat": format, "sourceText": string(file.Body), "metadata": map[string]any{"direction": "Математика", "startDate": "2099-01-01T10:00", "endDate": "2099-01-01T11:00", "published": false}})
		}
		preview := buildJSONImportPreview(JSONImportPreviewDTO{OrganizationID: 11, DivisionID: 17, DirectionID: 31, Payload: payload}, "Математика")
		if !preview.Valid || preview.Summary.QuestionsTotal != 3 || preview.Summary.SingleCount != 1 || preview.Summary.MultipleCount != 1 || preview.Summary.TextCount != 1 || preview.Preview.Settings.Published || preview.Preview.Settings.StartAt == nil {
			t.Fatalf("%s cannot be imported safely: %+v", format, preview)
		}
	}
	workbook, err := externalImportWorkbook()
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(workbook), int64(len(workbook)))
	if err != nil {
		t.Fatal(err)
	}
	var sheet string
	for _, file := range z.File {
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		decoder := xml.NewDecoder(bytes.NewReader(body))
		for {
			_, err = decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("invalid XLSX XML %s: %v", file.Name, err)
			}
		}
		if file.Name == "xl/worksheets/sheet1.xml" {
			sheet = string(body)
		}
	}
	if !strings.Contains(sheet, "account_id") || !strings.Contains(sheet, "legacy_student_id") || !strings.Contains(sheet, `<c r="B2"><v>0</v>`) || !strings.Contains(sheet, `<c r="B3"><v>87.5</v>`) {
		t.Fatal("headers or numeric percentages lost", sheet)
	}
	secret := "Actual Student Must Not Be Exported"
	sample := externalImportSample(ExternalTestDocument{ID: bson.NewObjectID(), OrganizationID: 11, DivisionID: 17, Revision: 4, Results: []ExternalResult{{FullName: secret, AccountID: 999}}})
	raw, _ := json.Marshal(sample)
	if strings.Contains(string(raw), secret) || len(sample.Rows) != 2 || *sample.Rows[0].Percent != 0 || *sample.Rows[1].Percent != 87.5 || sample.Rows[0].AccountID != nil {
		t.Fatal("sample exported actual student data")
	}
}

func templateTestDependencies(t *testing.T) *Dependencies {
	t.Helper()
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "revoked" {
			w.WriteHeader(401)
			return
		}
		division := int64(17)
		actor := Actor{AccountID: 21, OrganizationID: 11, Role: "staff_admin", DivisionID: &division, Permissions: map[string]Permission{"tests": {View: true}, "upload": {View: true}, "test-results": {View: true}}}
		if token == "student" {
			actor.Role = "student"
		}
		if token == "no-upload" {
			delete(actor.Permissions, "upload")
		}
		if token == "admin" {
			actor.Role = "admin"
			actor.OrganizationAdmin = true
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "active": true, "user": actor})
	}))
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			OrganizationID int64   `json:"organization_id"`
			DivisionIDs    []int64 `json:"division_ids"`
			DirectionID    *int64  `json:"direction_id"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.OrganizationID != 11 || len(in.DivisionIDs) != 1 || in.DivisionIDs[0] != 17 || in.DirectionID != nil && *in.DirectionID != 31 {
			w.WriteHeader(404)
			return
		}
		out := References{Organization: Reference{ID: 11, Active: true}, Divisions: []Reference{{ID: 17, Active: true}}}
		if in.DirectionID != nil {
			out.Direction = &Reference{ID: 31, Name: "Математика", Active: true}
		}
		reply(w, 200, Envelope{true, out})
	}))
	t.Cleanup(auth.Close)
	t.Cleanup(core.Close)
	return NewDependencies(Config{AuthURL: auth.URL, CoreURL: core.URL})
}

func templateHTTP(t *testing.T, s *Server, path, token string, status int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("private download cache/sniff protection missing")
	}
	return w
}

func TestImportTemplateHTTPProtectsScopeAndReturnsRealFile(t *testing.T) {
	s := &Server{Dependencies: templateTestDependencies(t)}
	base := "/v1/tests/divisions/17/imports/json/"
	for _, format := range []string{"cpm_json", "online_test_pad"} {
		w := templateHTTP(t, s, base+"template?direction_id=31&format="+format, "reader", 200)
		expected, _ := jsonTemplateBytes("Математика", format)
		if !bytes.Equal(w.Body.Bytes(), expected.Body) || w.Header().Get("Content-Type") != expected.MIME || w.Header().Get("Content-Length") != strconv.Itoa(len(expected.Body)) || w.Header().Get("Content-Disposition") != `attachment; filename="`+expected.Filename+`"` {
			t.Fatal("binary file wrapped or wrong MIME/name")
		}
		w = templateHTTP(t, s, base+"schema?direction_id=31&format="+format, "reader", 200)
		var envelope struct {
			Data ImportSchemaView `json:"data"`
		}
		if json.Unmarshal(w.Body.Bytes(), &envelope) != nil || envelope.Data.Capability != "supported" || envelope.Data.DirectionID != 31 {
			t.Fatal("schema not scoped")
		}
	}
	for _, tc := range []struct {
		path, token string
		code        int
	}{
		{base + "template?direction_id=31", "student", 403}, {base + "template?direction_id=31", "no-upload", 403},
		{base + "template?direction_id=31", "revoked", 401}, {base + "template?direction_id=31", "", 401},
		{base + "template?direction_id=999", "reader", 404}, {base + "template?direction_id=31&direction_id=31", "reader", 400},
		{base + "template?direction_id=31&target_id=1", "reader", 400}, {base + "template", "reader", 400},
		{base + "template?direction_id=31&format=html", "reader", 400}, {"/v1/tests/divisions/999/imports/json/template?direction_id=31", "admin", 404},
	} {
		w := templateHTTP(t, s, tc.path, tc.token, tc.code)
		if w.Header().Get("Content-Disposition") != "" {
			t.Fatal("error downloaded as file")
		}
	}
}

func TestExternalTemplateHTTPUsesScopedTargetAndNeverWrites(t *testing.T) {
	deps := templateTestDependencies(t)
	for _, suffix := range []string{"template", "schema", "sample"} {
		for _, state := range []string{"active", "deleted", "wrong-division"} {
			doc := ExternalTestDocument{ID: bson.NewObjectID(), OrganizationID: 11, DivisionID: 17, Revision: 4}
			code := 200
			if state == "deleted" {
				doc.Deleted = true
				code = 404
			}
			if state == "wrong-division" {
				doc.DivisionID = 18
				code = 404
			}
			deployment := drivertest.NewMockDeployment(externalRetryCursor(t, doc))
			opt := options.Client().SetMonitor(&event.CommandMonitor{Started: func(_ context.Context, e *event.CommandStartedEvent) {
				if e.CommandName != "find" && e.CommandName != "endSessions" {
					t.Errorf("template mutated Mongo via %s", e.CommandName)
				}
				if e.CommandName == "find" && e.Command.Lookup("filter").Document().Lookup("organization_id").Int64() != 11 {
					t.Error("unscoped Mongo read")
				}
			}})
			opt.Deployment = deployment
			client, err := mongo.Connect(opt)
			if err != nil {
				t.Fatal(err)
			}
			s := &Server{Dependencies: deps, Store: &Store{Collection: client.Database("external_retry").Collection(CollectionName)}}
			path := "/v1/tests/divisions/17/external-tests/" + doc.ID.Hex() + "/imports/results/" + suffix
			w := templateHTTP(t, s, path, "reader", code)
			if code == 200 && suffix == "template" {
				if _, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len())); err != nil || w.Header().Get("Content-Type") != importXLSXMIME {
					t.Fatal("invalid XLSX download", err)
				}
			}
			if code == 200 && suffix == "sample" {
				var dto ExternalImportValidateDTO
				if json.Unmarshal(w.Body.Bytes(), &dto) != nil || dto.TestID != doc.ID.Hex() || dto.ExpectedTestRevision != 4 {
					t.Fatal("sample not actual target DTO")
				}
			}
			client.Disconnect(context.Background())
		}
	}
	def := externalImportTransportSchema(false)["properties"].(map[string]any)["rows"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["percent"].(map[string]any)
	if def["type"] != "number" || def["minimum"] != 0 || def["maximum"] != 100 || def["anyOf"] != nil {
		t.Fatal("schema accepts missing/null percentages")
	}
}
