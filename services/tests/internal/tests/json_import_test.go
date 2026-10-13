package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/drivertest"
)

func jsonImportFixture() JSONImportPreviewDTO {
	return JSONImportPreviewDTO{OrganizationID: 11, DivisionID: 17, DirectionID: 31, Payload: json.RawMessage(`{"title":" Тест ","direction":"Математика","startDate":"2026-10-08T10:00","endDate":"2026-10-08T12:00","timeLimitMinutes":30,"questions":[{"questionId":7,"type":"single","text":" Один? ","points":2,"answers":[{"id":"a","text":" Да ","isCorrect":true},{"id":"b","text":"Нет","isCorrect":false}]},{"questionId":8,"type":"multiple","text":"Несколько?","points":3,"answers":[{"id":"a","text":"1","isCorrect":true},{"id":"b","text":"2","isCorrect":true}]},{"questionId":9,"type":"text","text":"Текст?","points":4,"correctAnswers":[" ответ ",42,"","второй"]}]}`)}
}
func jsonImportChange(in JSONImportPreviewDTO, change func(map[string]any)) JSONImportPreviewDTO {
	payload, _ := jsonImportObject(in.Payload)
	change(payload)
	in.Payload, _ = json.Marshal(payload)
	return in
}
func jsonImportHasPath(out JSONImportPreview, path string) bool {
	for _, e := range out.Errors {
		if e.Path == path {
			return true
		}
	}
	return false
}
func TestJSONImportNativePreservesDefaultsTypesQuestionsAndMoscowDates(t *testing.T) {
	out := buildJSONImportPreview(jsonImportFixture(), "Математика")
	if !out.Valid || out.Summary != (JSONImportSummary{QuestionsTotal: 3, TotalPoints: 9, SingleCount: 1, MultipleCount: 1, TextCount: 1}) || len(out.PreviewHash) != 64 {
		t.Fatal(out)
	}
	if out.Preview.Title != "Тест" || !out.Preview.Settings.Published || out.Preview.Settings.AnswersVisible || out.Preview.Settings.StartAt.Format("2006-01-02T15:04:05Z07:00") != "2026-10-08T07:00:00Z" || out.Preview.DirectionID != 31 {
		t.Fatal("native settings/direction changed", out.Preview)
	}
	if out.Preview.Questions[0].QuestionID != 7 || out.Preview.Questions[0].Answers[0].Text != "Да" || !reflect.DeepEqual(out.Preview.Questions[2].CorrectAnswers, []string{"ответ", "второй"}) {
		t.Fatal(out.Preview.Questions)
	}
	changed := jsonImportChange(jsonImportFixture(), func(p map[string]any) {
		p["published"] = nil
		p["visible"] = true
		p["startDate"] = "2026-1-2T3:4"
		p["endDate"] = "2026-1-2T5:6"
	})
	if p := buildJSONImportPreview(changed, "Математика"); !p.Valid || !p.Preview.Settings.Published || !p.Preview.Settings.AnswersVisible {
		t.Fatal("legacy None/default/nonpadded date changed", p)
	}
}
func TestJSONImportNativeReportsAllErrorsAndEnforcesOwnerLimits(t *testing.T) {
	in := jsonImportChange(jsonImportFixture(), func(p map[string]any) {
		p["title"] = ""
		p["direction"] = "Чужое"
		p["published"] = "yes"
		p["timeLimitMinutes"] = true
		p["startDate"] = "2026-02-30T10:00"
		qs := p["questions"].([]any)
		q := qs[0].(map[string]any)
		q["questionId"] = json.Number("8")
		q["points"] = json.Number("2.5")
		q["text"] = ""
		q["answers"].([]any)[0].(map[string]any)["isCorrect"] = "true"
	})
	out := buildJSONImportPreview(in, "Математика")
	for _, path := range []string{"title", "direction", "published", "timeLimitMinutes", "startDate", "questions[0].points", "questions[0].text", "questions[0].answers[0].isCorrect", "questions[1].questionId"} {
		if !jsonImportHasPath(out, path) {
			t.Fatal("error lost", path, out.Errors)
		}
	}
	if out.Valid || out.Summary.ErrorsCount != len(out.Errors) {
		t.Fatal(out)
	}
	tooLarge := jsonImportChange(jsonImportFixture(), func(p map[string]any) {
		p["questions"].([]any)[0].(map[string]any)["points"] = json.Number("9223372036854775807")
	})
	if p := buildJSONImportPreview(tooLarge, "Математика"); p.Valid || p.Summary.TotalPoints < 0 {
		t.Fatal("large points overflowed the preview", p)
	}
	if p := buildJSONImportPreview(JSONImportPreviewDTO{Payload: json.RawMessage(`[]`)}, "Математика"); p.Valid || !jsonImportHasPath(p, "$") {
		t.Fatal("non-object imported", p)
	}
}
func otpImportFixture(source map[string]any) JSONImportPreviewDTO {
	raw, _ := json.Marshal(source)
	payload := map[string]any{"importFormat": "online_test_pad", "sourceText": "\ufeff var test = " + string(raw) + ";\nwindow.trailer = true;", "metadata": map[string]any{"direction": "Математика", "startDate": "2026-10-08T10:00", "endDate": "2026-10-08T12:00"}}
	out := JSONImportPreviewDTO{OrganizationID: 11, DivisionID: 17, DirectionID: 31}
	out.Payload, _ = json.Marshal(payload)
	return out
}
func TestJSONImportOTPConvertsHTMLOrderingScorePrecedenceAndBankersWeights(t *testing.T) {
	in := otpImportFixture(map[string]any{"Name": "Название } \" brace", "IsLimitedQuestions": true, "LimitedQuestionsCount": 1, "IsRandomQuestions": true, "IsRandomAnswersInAllQuestions": true, "Questions": []any{
		map[string]any{"Number": 2, "AnswerType": 210, "Text": "<p>Два&nbsp; &amp; три</p><br>Четыре", "maxScore": 3.5, "AnswerTemplates": []any{map[string]any{"Number": 2, "Text": "<b>B</b>", "Score": 0, "IsRightAnswer": true}, map[string]any{"Number": 1, "TextWOHtml": "<i>A</i>", "Score": 1, "IsRightAnswer": false}}},
		map[string]any{"Number": 1, "AnswerType": 10, "Text": "<div>Первый</div>", "maxScore": 2.5, "AnswerTemplates": []any{map[string]any{"Number": 2, "Text": "Нет", "IsRightAnswer": false}, map[string]any{"Number": 1, "Text": "Да", "IsRightAnswer": true}}},
	}})
	out := buildJSONImportPreview(in, "Математика")
	if !out.Valid || out.Source != "online_test_pad" || out.Preview.Settings.Published || out.Preview.Settings.AnswersVisible || out.Preview.Settings.TimeLimitMinutes != 30 || len(out.Warnings) != 3 {
		t.Fatal(out)
	}
	qs := out.Preview.Questions
	if qs[0].QuestionID != 1 || qs[0].Text != "Первый" || qs[0].Points != 2 || qs[1].Points != 4 || qs[1].Text != "Два & три\nЧетыре" || qs[1].Answers[0].Text != "A" || !qs[1].Answers[0].IsCorrect || qs[1].Answers[1].IsCorrect {
		t.Fatal("OTP legacy conversion changed", qs)
	}
	if jsonImportOptionID(25) != "z" || jsonImportOptionID(26) != "aa" || jsonImportOptionID(51) != "az" {
		t.Fatal("Excel style option IDs changed")
	}
	if !jsonImportOTPPositiveScore(true) || jsonImportOTPPositiveScore(false) {
		t.Fatal("Python boolean score conversion changed")
	}
}
func TestJSONImportOTPTextVariantsDecimalAndZeroFallbackAreLegacyExact(t *testing.T) {
	for _, tc := range []struct {
		answer   map[string]any
		kind     int
		expected []string
		valid    bool
	}{
		{map[string]any{"ValueInt": 0, "ValueFloat": "2.50"}, 410, []string{"2.5"}, true},
		{map[string]any{"ValueInt": 0, "ValueFloat": 0, "ValueText": "0"}, 410, []string{"0"}, true},
		{map[string]any{"ValueInt": 0, "ValueFloat": 0, "Text": "<b>0</b>"}, 410, []string{"0"}, true},
		{map[string]any{"ValueInt": 0, "ValueFloat": 0}, 410, []string{}, false},
		{map[string]any{"ValueInt": json.Number("-0"), "ValueText": "integer fallback"}, 410, []string{"integer fallback"}, true},
		{map[string]any{"ValueFloat": json.Number("-0.0"), "ValueText": "float fallback"}, 410, []string{"float fallback"}, true},
		{map[string]any{"ValueInt": json.Number("-0"), "ValueFloat": json.Number("-0.0"), "Text": "<b>0</b>"}, 410, []string{"0"}, true},
		{map[string]any{"ValueInt": json.Number("-0"), "ValueFloat": json.Number("-0.0")}, 410, []string{}, false},
		{map[string]any{"ValueInt": "-0", "ValueText": "unused fallback"}, 410, []string{"-0"}, true},
		{map[string]any{"ValueFloat": "-0.0", "ValueText": "string float fallback"}, 410, []string{"string float fallback"}, true},
		{map[string]any{"ValueText": " один <ИЛИ> второй <или> один "}, 420, []string{"один", "второй"}, true},
	} {
		tc.answer["IsRightAnswer"] = true
		in := otpImportFixture(map[string]any{"Name": "Текст", "Questions": []any{map[string]any{"AnswerType": tc.kind, "Text": "Ввод", "maxScore": 0, "RightScore": 7, "AnswerTemplates": []any{tc.answer}}}})
		out := buildJSONImportPreview(in, "Математика")
		if out.Valid != tc.valid || !reflect.DeepEqual(out.Preview.Questions[0].CorrectAnswers, tc.expected) || out.Preview.Questions[0].Points != 7 {
			t.Fatal(tc, out)
		}
	}
}
func TestJSONImportOTPMalformedSourceAndUnsupportedTypesNeverCreateQuestions(t *testing.T) {
	for _, source := range []string{"", "let test = {};", "var test = {", `var test = {"Name": invalid};`} {
		in := jsonImportFixture()
		in.Payload, _ = json.Marshal(map[string]any{"importFormat": "online_test_pad", "sourceText": source})
		out := buildJSONImportPreview(in, "Математика")
		if out.Valid || !jsonImportHasPath(out, "sourceText") {
			t.Fatal(source, out)
		}
	}
	in := otpImportFixture(map[string]any{"Name": "Неизвестный", "Questions": []any{map[string]any{"AnswerType": 999, "Text": "Тип", "AnswerTemplates": []any{}}}})
	if p := buildJSONImportPreview(in, "Математика"); p.Valid || !jsonImportHasPath(p, "Questions[0].AnswerType") {
		t.Fatal(p)
	}
}
func TestJSONImportHashAndCommandIdentityAreStableAcrossPublicInternalAndFormatting(t *testing.T) {
	in := JSONImportCommitDTO{JSONImportPreviewDTO: jsonImportFixture(), CommandID: "11111111-1111-4111-8111-111111111111"}
	in.PreviewHash = buildJSONImportPreview(in.JSONImportPreviewDTO, "Математика").PreviewHash
	actor := Actor{AccountID: 23, OrganizationID: 11}
	r := httptest.NewRequest("POST", "/v1/tests/divisions/17/imports/json/commit", nil)
	id, hash, e := jsonImportOperation(r, actor, in)
	if e != nil {
		t.Fatal(e)
	}
	r2 := httptest.NewRequest("POST", "/internal/tests/imports/json/commit", nil)
	in2 := in
	var value any
	_ = json.Unmarshal(in.Payload, &value)
	in2.Payload, _ = json.MarshalIndent(value, "", "  ")
	id2, hash2, e := jsonImportOperation(r2, actor, in2)
	if e != nil || id != id2 || hash != hash2 {
		t.Fatal("transport route or formatting changed owner command", e)
	}
	in2.DivisionID++
	id3, hash3, _ := jsonImportOperation(r2, actor, in2)
	if id3 != id || hash3 == hash {
		t.Fatal("changed division did not conflict with stable command")
	}
	in2 = in
	in2.Payload = json.RawMessage(`{"title":false}`)
	_, changed, _ := jsonImportOperation(r2, actor, in2)
	if changed == hash {
		t.Fatal("changed invalid payload reused original receipt")
	}
	if p := buildJSONImportPreview(in.JSONImportPreviewDTO, "Другое имя"); p.PreviewHash == in.PreviewHash {
		t.Fatal("direction name change did not require another preview")
	}
}

func jsonImportCoreServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			OrganizationID int64   `json:"organization_id"`
			DivisionIDs    []int64 `json:"division_ids"`
			DirectionID    *int64  `json:"direction_id"`
		}
		if r.URL.Path != "/internal/core/test-references" || json.NewDecoder(r.Body).Decode(&in) != nil || in.OrganizationID != 11 || !reflect.DeepEqual(in.DivisionIDs, []int64{17}) {
			t.Error("owner scope was not preserved")
			w.WriteHeader(404)
			return
		}
		refs := References{Organization: Reference{ID: 11, Active: true}, Divisions: []Reference{{ID: 17, Active: true}}}
		if in.DirectionID != nil {
			refs.Direction = &Reference{ID: *in.DirectionID, Name: "Математика", Active: true}
		}
		reply(w, 200, Envelope{true, refs})
	}))
}
func jsonImportActor() Actor {
	return Actor{AccountID: 23, OrganizationID: 11, Role: "admin", OrganizationAdmin: true}
}
func jsonImportRequest(in JSONImportCommitDTO) *http.Request {
	raw, _ := json.Marshal(in)
	r := httptest.NewRequest("POST", "/v1/tests/divisions/17/imports/json/commit", bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	return r
}
func jsonImportMongoServer(t *testing.T, core string, responses []bson.D, commands *[]bson.Raw) *Server {
	t.Helper()
	deployment := drivertest.NewMockDeployment(responses...)
	opts := options.Client().SetMonitor(&event.CommandMonitor{Started: func(_ context.Context, e *event.CommandStartedEvent) {
		if commands != nil {
			*commands = append(*commands, append(bson.Raw{}, e.Command...))
		}
	}})
	opts.Deployment = deployment
	client, e := mongo.Connect(opts)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { client.Disconnect(context.Background()) })
	return &Server{Store: &Store{Collection: client.Database("ratings_unit").Collection(CollectionName)}, Dependencies: NewDependencies(Config{CoreURL: core, ServiceKey: strings.Repeat("k", 32)})}
}
func TestJSONImportCommitPublishesOneRealTestWithFinalSettingsAndInitialVersion(t *testing.T) {
	core := jsonImportCoreServer(t)
	defer core.Close()
	in := JSONImportCommitDTO{JSONImportPreviewDTO: jsonImportFixture(), CommandID: "11111111-1111-4111-8111-111111111111"}
	in.PreviewHash = buildJSONImportPreview(in.JSONImportPreviewDTO, "Математика").PreviewHash
	commands := []bson.Raw{}
	s := jsonImportMongoServer(t, core.URL, []bson.D{ratingMongoBatch(CollectionName), {{Key: "ok", Value: 1}, {Key: "n", Value: 1}}, {{Key: "ok", Value: 1}, {Key: "n", Value: 1}}}, &commands)
	body, code, e := s.jsonImportCommit(jsonImportRequest(in), jsonImportActor(), 17)
	if e != nil || code != 201 {
		t.Fatal(body, code, e)
	}
	result := body.(JSONImportCommitResult)
	if result.Test.QuestionCount != 3 || len(result.Test.Placements) != 1 || !result.Test.Placements[0].Settings.Published || result.Test.ContentVersionID.IsZero() {
		t.Fatal("created a draft or partly initialized test", result)
	}
	writes := []string{}
	for _, cmd := range commands {
		if cmd.Lookup("insert").Type == 0 {
			continue
		}
		collection := cmd.Lookup("insert").StringValue()
		writes = append(writes, collection)
		if collection == CollectionName {
			d := cmd.Lookup("documents").Array().Index(0).Document()
			questions, _ := d.Lookup("questions").Array().Values()
			if len(questions) == 0 {
				t.Fatal("catalog content absent")
			}
			p := d.Lookup("placements").Array().Index(0).Document()
			if p.Lookup("division_id").Int64() != 17 || !p.Lookup("settings").Document().Lookup("published").Boolean() || p.Lookup("settings").Document().Lookup("start_at").Type == 0 {
				t.Fatal("settings were not committed with catalogue", d)
			}
		}
	}
	if !reflect.DeepEqual(writes, []string{ContentCollectionName, CollectionName}) {
		t.Fatal("unexpected owner writes or workflow/draft creation", writes)
	}
}
func TestJSONImportCommitReplaysDurableReceiptAndRejectsChangedInvalidWithoutWrites(t *testing.T) {
	core := jsonImportCoreServer(t)
	defer core.Close()
	in := JSONImportCommitDTO{JSONImportPreviewDTO: jsonImportFixture(), CommandID: "11111111-1111-4111-8111-111111111111"}
	in.PreviewHash = buildJSONImportPreview(in.JSONImportPreviewDTO, "Математика").PreviewHash
	id, hash, _ := jsonImportOperation(jsonImportRequest(in), jsonImportActor(), in)
	old := Document{ID: id, OrganizationID: 11, OwnerDivisionID: 17, DirectionID: 31, Title: "Существующий", Revision: 4, OperationHash: hash, Placements: []Placement{{DivisionID: 17}}}
	for _, changed := range []bool{false, true} {
		commands := []bson.Raw{}
		s := jsonImportMongoServer(t, core.URL, []bson.D{ratingMongoBatch(CollectionName, old), ratingMongoBatch(CollectionName, old)}, &commands)
		request := in
		if changed {
			request.Payload = json.RawMessage(`{"title":false}`)
		} else {
			request.CommandID = " " + strings.ToUpper(in.CommandID) + " "
		}
		body, code, e := s.jsonImportCommit(jsonImportRequest(request), jsonImportActor(), 17)
		if changed {
			assertRatingAPIError(t, e, 409, "idempotency_conflict")
		} else if e != nil || code != 200 {
			t.Fatal(code, e)
		} else if body.(JSONImportCommitResult).CommandID != in.CommandID {
			t.Fatal("receipt command UUID was not normalized", body)
		}
		for _, cmd := range commands {
			if cmd.Lookup("insert").Type != 0 || cmd.Lookup("update").Type != 0 {
				t.Fatal("replay created an owner write", cmd)
			}
		}
	}
}
func TestJSONImportAccessRequiresBothLiveMethodsAndExactOrgDivision(t *testing.T) {
	s := &Server{}
	base := jsonImportActor()
	d := int64(17)
	staff := Actor{AccountID: 23, OrganizationID: 11, Role: "staff_admin", DivisionID: &d, Permissions: map[string]Permission{"tests": {View: true, Edit: true}, "upload": {View: true, Edit: true}}}
	for _, tc := range []struct {
		actor Actor
		input JSONImportPreviewDTO
		path  int64
	}{
		{Actor{AccountID: 23, OrganizationID: 11, Role: "student"}, jsonImportFixture(), 17},
		{func() Actor {
			a := staff
			a.Permissions = map[string]Permission{"tests": {View: true, Edit: true}}
			return a
		}(), jsonImportFixture(), 17},
		{func() Actor {
			a := staff
			a.Permissions = map[string]Permission{"upload": {View: true, Edit: true}}
			return a
		}(), jsonImportFixture(), 17},
		{base, func() JSONImportPreviewDTO { v := jsonImportFixture(); v.OrganizationID++; return v }(), 17},
		{staff, func() JSONImportPreviewDTO { v := jsonImportFixture(); v.DivisionID++; return v }(), 18},
		{base, jsonImportFixture(), 18},
	} {
		if _, e := s.jsonImportAccess(httptest.NewRequest("POST", "/", nil), tc.actor, tc.input, tc.path); e != denied {
			t.Fatal("authorization widened scope", tc, e)
		}
	}
	s.Dependencies = NewDependencies(Config{})
	if _, _, e := s.jsonImportValidateInternal(httptest.NewRequest("POST", "/internal/tests/imports/json/validate", strings.NewReader("{}"))); e == nil {
		t.Fatal("service key without live user was sufficient")
	} else {
		assertRatingAPIError(t, e, 401, "invalid_token")
	}
}
