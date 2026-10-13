package tests

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"reflect"
	"strings"
)

const importXLSXMIME = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

func (s *Server) importTemplateSpecs() []routeSpec {
	return []routeSpec{
		{"GET /v1/tests/divisions/{division}/imports/json/template", "Синтетический JSON/OnlineTestPad шаблон; upload.view и tests.view", s.jsonTemplate, nil, nil},
		{"GET /v1/tests/divisions/{division}/imports/json/schema", "Форматы JSON/OnlineTestPad и предметные validate/commit DTO", s.jsonTemplateSchema, nil, ImportSchemaView{}},
		{"GET /v1/tests/divisions/{division}/external-tests/{external}/imports/results/template", "Синтетический XLSX для выбранной внешней работы; upload.view и test-results.view", s.externalTemplate, nil, nil},
		{"GET /v1/tests/divisions/{division}/external-tests/{external}/imports/results/schema", "Колонки XLSX и предметные validate/commit DTO внешних результатов", s.externalTemplateSchema, nil, ImportSchemaView{}},
		{"GET /v1/tests/divisions/{division}/external-tests/{external}/imports/results/sample", "Синтетический пример предметного запроса внешних результатов", s.externalTemplateSample, nil, nil},
	}
}

func importDownloadOpenAPI(path string) map[string]any {
	switch {
	case strings.HasSuffix(path, "/imports/json/template"):
		return map[string]any{"application/json": map[string]any{"schema": legacyImportFileSchema()}, "text/javascript": map[string]any{"schema": map[string]any{"type": "string"}}}
	case strings.HasSuffix(path, "/imports/results/template"):
		return map[string]any{importXLSXMIME: map[string]any{"schema": map[string]any{"type": "string", "format": "binary"}}}
	case strings.HasSuffix(path, "/imports/results/sample"):
		return map[string]any{"application/json": map[string]any{"schema": externalImportTransportSchema(false)}}
	}
	return nil
}

func importCommandProperties(props map[string]any) {
	props["command_id"] = map[string]any{"type": "string", "format": "uuid"}
	props["preview_hash"] = map[string]any{"type": "string", "pattern": "^[a-fA-F0-9]{64}$"}
}

func jsonImportTransportSchema(commit bool) map[string]any {
	def := schema(reflect.TypeOf(JSONImportPreviewDTO{}))
	props := def["properties"].(map[string]any)
	for _, key := range []string{"organization_id", "division_id", "direction_id"} {
		props[key] = map[string]any{"type": "integer", "minimum": 1}
	}
	props["payload"] = map[string]any{"type": "object", "description": "Нативный JSON-файл либо {importFormat:online_test_pad,sourceText,metadata}. Внешние org/div/direction не берутся из файла."}
	required := []string{"organization_id", "division_id", "direction_id", "payload"}
	if commit {
		importCommandProperties(props)
		required = append(required, "command_id", "preview_hash")
	}
	def["required"] = required
	return def
}

func externalImportTransportSchema(commit bool) map[string]any {
	def := schema(reflect.TypeOf(ExternalImportValidateDTO{}))
	props := def["properties"].(map[string]any)
	for _, key := range []string{"organization_id", "division_id"} {
		props[key] = map[string]any{"type": "integer", "minimum": 1}
	}
	props["test_id"] = map[string]any{"type": "string", "pattern": "^(external_)?[a-fA-F0-9]{24}$"}
	minimumRevision := 0
	if commit {
		minimumRevision = 1
	}
	props["expected_test_revision"] = map[string]any{"type": "integer", "minimum": minimumRevision}
	row := schema(reflect.TypeOf(ExternalImportRowDTO{}))
	fields := row["properties"].(map[string]any)
	fields["row_id"] = map[string]any{"type": "string", "minLength": 1, "description": "Уникальный ID исходной строки в наборе"}
	fields["row"] = map[string]any{"type": "integer", "minimum": 1}
	fields["full_name"] = map[string]any{"type": "string", "description": "Минимум фамилия и имя; обязательно также при явном ID"}
	fields["percent"] = map[string]any{"type": "number", "minimum": 0, "maximum": 100, "description": "null запрещён; ноль и дробные проценты допустимы"}
	for _, key := range []string{"account_id", "legacy_student_id"} {
		fields[key] = map[string]any{"anyOf": []any{map[string]any{"type": "integer", "minimum": 1, "maximum": int64(9223372036854775807)}, map[string]any{"type": "null"}}, "description": "Optional exact int64. account_id — Auth; legacy_student_id — профиль Core; оба ID должны относиться к одному scoped ученику."}
	}
	row["required"] = []string{"row_id", "row", "full_name", "percent"}
	props["rows"] = map[string]any{"type": "array", "minItems": 1, "maxItems": externalImportMaxRows, "items": row}
	required := []string{"organization_id", "division_id", "test_id", "rows"}
	if commit {
		importCommandProperties(props)
		for _, key := range []string{"upload_id", "upload_session_id"} {
			props[key] = map[string]any{"type": "string", "minLength": 1}
		}
		props["upload_preview_revision"] = map[string]any{"type": "integer", "minimum": 1}
		required = append(required, "expected_test_revision", "command_id", "preview_hash", "upload_id", "upload_session_id", "upload_preview_revision")
	}
	def["required"] = required
	return def
}

type importDownload struct {
	Body           []byte
	MIME, Filename string
}

type ImportSchemaView struct {
	Format         string         `json:"format"`
	Capability     string         `json:"capability"`
	OrganizationID int64          `json:"organization_id"`
	DivisionID     int64          `json:"division_id"`
	DirectionID    int64          `json:"direction_id,omitempty"`
	TestID         string         `json:"test_id,omitempty"`
	Filename       string         `json:"filename"`
	MIME           string         `json:"mime"`
	FileSchema     map[string]any `json:"file_schema"`
	ValidateSchema map[string]any `json:"validate_schema"`
	CommitSchema   map[string]any `json:"commit_schema"`
	Notes          []string       `json:"notes"`
}

func templateQuery(r *http.Request, allowed ...string) error {
	for key, values := range r.URL.Query() {
		if len(values) != 1 {
			return bad("invalid_template_query")
		}
		found := false
		for _, name := range allowed {
			if name == key {
				found = true
				break
			}
		}
		if !found {
			return bad("invalid_template_query")
		}
	}
	return nil
}

func (s *Server) jsonTemplateAccess(r *http.Request, a Actor, division int64) (Reference, string, error) {
	if a.Role == "student" || !a.Can(false) || !actorUpload(a, false) {
		return Reference{}, "", denied
	}
	if e := templateQuery(r, "direction_id", "format"); e != nil {
		return Reference{}, "", e
	}
	direction, e := numericID(r.URL.Query().Get("direction_id"))
	if e != nil {
		return Reference{}, "", bad("template_direction_required")
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "cpm_json"
	}
	if format != "cpm_json" && format != "online_test_pad" {
		return Reference{}, "", bad("invalid_template_format")
	}
	refs, e := s.Dependencies.References(r.Context(), a.OrganizationID, []int64{division}, &direction)
	if e != nil {
		return Reference{}, "", e
	}
	if !refs.Divisions[0].Active {
		return Reference{}, "", missing
	}
	if !refs.Direction.Active {
		return Reference{}, "", conflict("direction_archived")
	}
	return *refs.Direction, format, nil
}

func nativeImportSample(direction string) map[string]any {
	return map[string]any{
		"title": "Пример импорта — замените вопросы", "direction": direction,
		"startDate": "2099-01-01T10:00", "endDate": "2099-01-01T11:00", "timeLimitMinutes": 30, "published": false, "visible": false,
		"questions": []any{
			map[string]any{"questionId": 1, "type": "single", "text": "Пример: выберите вариант A", "points": 1, "answers": []any{
				map[string]any{"id": "a", "text": "A", "isCorrect": true}, map[string]any{"id": "b", "text": "B", "isCorrect": false}}, "correctAnswers": []string{}},
			map[string]any{"questionId": 2, "type": "multiple", "text": "Пример: выберите A и B", "points": 2, "answers": []any{
				map[string]any{"id": "a", "text": "A", "isCorrect": true}, map[string]any{"id": "b", "text": "B", "isCorrect": true}, map[string]any{"id": "c", "text": "C", "isCorrect": false}}, "correctAnswers": []string{}},
			map[string]any{"questionId": 3, "type": "text", "text": "Пример: введите слово «пример»", "points": 1, "answers": []any{}, "correctAnswers": []string{"пример", "example"}},
		},
	}
}

func otpImportSample() map[string]any {
	return map[string]any{"Name": "Пример OnlineTestPad — замените вопросы", "Questions": []any{
		map[string]any{"Number": 1, "AnswerType": 10, "Text": "<p>Пример: выберите A</p>", "maxScore": 1, "AnswerTemplates": []any{
			map[string]any{"Number": 1, "Text": "A", "IsRightAnswer": true}, map[string]any{"Number": 2, "Text": "B", "IsRightAnswer": false}}},
		map[string]any{"Number": 2, "AnswerType": 210, "Text": "Пример: выберите A и B", "maxScore": 2, "AnswerTemplates": []any{
			map[string]any{"Number": 1, "Text": "A", "IsRightAnswer": true}, map[string]any{"Number": 2, "Text": "B", "IsRightAnswer": true}, map[string]any{"Number": 3, "Text": "C", "IsRightAnswer": false}}},
		map[string]any{"Number": 3, "AnswerType": 420, "Text": "Пример: введите слово «пример»", "maxScore": 1, "AnswerTemplates": []any{
			map[string]any{"Number": 1, "ValueText": "пример <или> example", "IsRightAnswer": true}}},
	}}
}

func jsonTemplateBytes(direction, format string) (importDownload, error) {
	payload := nativeImportSample(direction)
	mime, filename := "application/json; charset=utf-8", "primer_importa_testa_cpm.json"
	if format == "online_test_pad" {
		payload = otpImportSample()
		mime, filename = "text/javascript; charset=utf-8", "primer_importa_testa_online_test_pad.js"
	}
	raw, e := json.MarshalIndent(payload, "", "  ")
	if e != nil {
		return importDownload{}, e
	}
	if format == "online_test_pad" {
		raw = append(append([]byte("var test = "), raw...), ';')
	}
	raw = append(raw, '\n')
	return importDownload{raw, mime, filename}, nil
}

func (s *Server) jsonTemplate(r *http.Request, a Actor, division int64) (any, int, error) {
	direction, format, e := s.jsonTemplateAccess(r, a, division)
	if e != nil {
		return nil, 0, e
	}
	file, e := jsonTemplateBytes(direction.Name, format)
	return file, 200, e
}

func legacyImportFileSchema() map[string]any {
	option := map[string]any{"type": "object", "required": []string{"id", "text", "isCorrect"}, "properties": map[string]any{
		"id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "pattern": "^[A-Za-z0-9_-]+$"}, "text": map[string]any{"type": "string", "minLength": 1, "maxLength": 2000}, "isCorrect": map[string]any{"type": "boolean"}}}
	question := map[string]any{"type": "object", "required": []string{"questionId", "type", "text", "points"}, "properties": map[string]any{
		"questionId": map[string]any{"type": "integer", "minimum": 1, "maximum": int64(9007199254740991)}, "type": map[string]any{"enum": []string{"single", "multiple", "text"}},
		"text": map[string]any{"type": "string", "minLength": 1, "maxLength": 4000}, "points": map[string]any{"type": "integer", "minimum": 1, "maximum": 100000},
		"answers": map[string]any{"type": "array", "maxItems": 50, "items": option}, "correctAnswers": map[string]any{"type": "array", "maxItems": 50, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 1000}},
	}}
	return map[string]any{"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object", "required": []string{"title", "questions", "startDate", "endDate"}, "properties": map[string]any{
		"title": map[string]any{"type": "string", "minLength": 1, "maxLength": 200}, "direction": map[string]any{"type": "string", "description": "Пустое имя заполняется выбранным направлением; непустое должно совпадать с его текущим именем"},
		"startDate": map[string]any{"type": "string", "description": "RFC3339 либо старый локальный формат YYYY-MM-DDTHH:mm, московское время"},
		"endDate":   map[string]any{"type": "string"}, "timeLimitMinutes": map[string]any{"type": "integer", "minimum": 0, "maximum": 1440},
		"published": map[string]any{"type": "boolean", "default": true}, "visible": map[string]any{"type": "boolean", "default": false},
		"questions": map[string]any{"type": "array", "minItems": 1, "maxItems": 200, "items": question}},
		"description": "single: ровно один верный вариант; multiple: хотя бы один; text: непустые correctAnswers и пустые answers. Публикация требует обе даты и положительное время. Окончательную проверку выполняет Tests preview."}
}

func (s *Server) jsonTemplateSchema(r *http.Request, a Actor, division int64) (any, int, error) {
	direction, format, e := s.jsonTemplateAccess(r, a, division)
	if e != nil {
		return nil, 0, e
	}
	file, e := jsonTemplateBytes(direction.Name, format)
	if e != nil {
		return nil, 0, e
	}
	fileSchema := legacyImportFileSchema()
	if format == "online_test_pad" {
		fileSchema = map[string]any{"type": "string", "description": "Файл .js содержит var test = { ... }; объект разбирается как JSON без выполнения JavaScript. Name, Questions[].AnswerType (10/210/410/420), Text, maxScore/RightScore, AnswerTemplates[] с Text/ValueText/ValueInt/ValueFloat, IsRightAnswer/Score.", "converted_schema": fileSchema}
	}
	return ImportSchemaView{Format: format, Capability: "supported", OrganizationID: a.OrganizationID, DivisionID: division, DirectionID: direction.ID,
		Filename: file.Filename, MIME: file.MIME, FileSchema: fileSchema, ValidateSchema: jsonImportTransportSchema(false), CommitSchema: jsonImportTransportSchema(true),
		Notes: []string{"Все вопросы и ответы примера вымышлены; реальные тесты и результаты не читаются.", "Нативный пример скрыт; демонстрационные даты 2099 года замените перед импортом. Даты обязательны по прежнему контракту.", "Для .js Uploads передаёт payload {importFormat:online_test_pad,sourceText,metadata}; направление и даты задаются в metadata; файл не исполняется."}}, 200, nil
}

func (s *Server) externalTemplateAccess(r *http.Request, a Actor) (ExternalTestDocument, error) {
	if a.Role == "student" || !actorUpload(a, false) || !actorResults(a, false) {
		return ExternalTestDocument{}, denied
	}
	if e := templateQuery(r); e != nil {
		return ExternalTestDocument{}, e
	}
	d, e := s.externalLoad(r, a)
	if e != nil {
		return d, e
	}
	if d.Deleted {
		return d, missing
	}
	return d, nil
}

func externalImportSample(d ExternalTestDocument) ExternalImportValidateDTO {
	zero, decimal := 0.0, 87.5
	return ExternalImportValidateDTO{OrganizationID: d.OrganizationID, DivisionID: d.DivisionID, TestID: d.ID.Hex(), ExpectedTestRevision: d.Revision,
		Rows: []ExternalImportRowDTO{{RowID: "sheet-1-row-2", Row: 2, FullName: "Примеров Ученик", Percent: &zero}, {RowID: "sheet-1-row-3", Row: 3, FullName: "Образцова Ученица", Percent: &decimal}}}
}

func (s *Server) externalTemplateSample(r *http.Request, a Actor, _ int64) (any, int, error) {
	d, e := s.externalTemplateAccess(r, a)
	if e != nil {
		return nil, 0, e
	}
	raw, e := json.MarshalIndent(externalImportSample(d), "", "  ")
	return importDownload{append(raw, '\n'), "application/json; charset=utf-8", "external-test-results-sample.json"}, 200, e
}

func externalImportSheet() string {
	headers := []string{"ФИО", "Процент правильных ответов (%)", "Количество правильных ответов", "Дата завершения", "Потрачено времени", "Логин", "account_id", "legacy_student_id"}
	var sheet strings.Builder
	sheet.WriteString(`<?xml version="1.0" encoding="UTF-8"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1">`)
	cell := func(ref, value string) {
		var escaped bytes.Buffer
		_ = xml.EscapeText(&escaped, []byte(value))
		fmt.Fprintf(&sheet, `<c r="%s" t="inlineStr"><is><t>%s</t></is></c>`, ref, escaped.String())
	}
	for i, name := range headers {
		cell(fmt.Sprintf("%c1", 'A'+i), name)
	}
	sheet.WriteString(`</row><row r="2">`)
	cell("A2", "Примеров Ученик")
	sheet.WriteString(`<c r="B2"><v>0</v></c></row><row r="3">`)
	cell("A3", "Образцова Ученица")
	sheet.WriteString(`<c r="B3"><v>87.5</v></c></row></sheetData></worksheet>`)
	return sheet.String()
}

func externalImportWorkbook() ([]byte, error) {
	files := []struct{ name, content string }{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{"xl/workbook.xml", `<?xml version="1.0" encoding="UTF-8"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><bookViews><workbookView activeTab="0"/></bookViews><sheets><sheet name="Результаты" sheetId="1" r:id="rId1"/></sheets></workbook>`},
		{"xl/_rels/workbook.xml.rels", `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`},
		{"xl/worksheets/sheet1.xml", externalImportSheet()},
	}
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	for _, file := range files {
		entry, e := w.Create(file.name)
		if e != nil {
			return nil, e
		}
		if _, e = entry.Write([]byte(file.content)); e != nil {
			return nil, e
		}
	}
	if e := w.Close(); e != nil {
		return nil, e
	}
	return out.Bytes(), nil
}

func (s *Server) externalTemplate(r *http.Request, a Actor, _ int64) (any, int, error) {
	if _, e := s.externalTemplateAccess(r, a); e != nil {
		return nil, 0, e
	}
	raw, e := externalImportWorkbook()
	return importDownload{raw, importXLSXMIME, "external-test-results-template.xlsx"}, 200, e
}

func (s *Server) externalTemplateSchema(r *http.Request, a Actor, _ int64) (any, int, error) {
	d, e := s.externalTemplateAccess(r, a)
	if e != nil {
		return nil, 0, e
	}
	return ImportSchemaView{Format: "external_results_xlsx", Capability: "supported", OrganizationID: d.OrganizationID, DivisionID: d.DivisionID, TestID: d.ID.Hex(),
		Filename: "external-test-results-template.xlsx", MIME: importXLSXMIME,
		FileSchema:     map[string]any{"required_columns": []string{"ФИО", "Процент правильных ответов (%)"}, "optional_columns": []string{"Количество правильных ответов", "Дата завершения", "Потрачено времени", "Логин", "account_id", "legacy_student_id"}, "max_rows": externalImportMaxRows},
		ValidateSchema: externalImportTransportSchema(false), CommitSchema: externalImportTransportSchema(true),
		Notes: []string{"Две строки вымышлены: замените их перед импортом. Оценки и ФИО учеников из БД не выгружаются.", "Процент — число от 0 до 100, включая 0 и дроби; null запрещён. 0.9 означает 0.9%, а не 90%.", "account_id — Auth аккаунт; legacy_student_id — профиль Core. Не подставляйте один вместо другого; для XLSX сохраняйте большие ID как текст.", "Организация, подразделение, target и его revision берутся из выбранной области, а не из колонок XLSX.", "Сопоставление, дубли и существующие результаты окончательно проверяет защищённый Tests validate."}}, 200, nil
}
