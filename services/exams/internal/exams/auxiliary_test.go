package exams

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func auxFixture() (*Exam, Actor, Params) {
	now := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	e := &Exam{ID: 1, OrganizationID: 1, OwnerDivisionID: 2, DirectionID: 4, Kind: "classic", Revision: 1, ContentRevision: 1, NextID: 20, Parts: []Part{{ID: 4, Code: "A", Weight: 1, Quota: 1, Revision: 1}}, Questions: []Question{{ID: 5, PartID: 4, Text: "q", Answer: "a", Revision: 1}}, Divisions: map[int64]*Division{2: {ID: 2, Revision: 1, Config: Config{Revision: 1}, TrainingEnabled: true}}}
	a := Actor{AccountID: 10, OrganizationID: 1, Role: "admin", OrganizationAdmin: true}
	p := Params{DivisionID: 2, Now: now, Members: map[int64]Member{11: {AccountID: 11, Role: "student", Active: true, Login: "student", DivisionIDs: []int64{2}}, 12: {AccountID: 12, Role: "student", Active: true, Login: "12345", DivisionIDs: []int64{2}}, 13: {AccountID: 13, Role: "examinator", Active: true, Login: "examiner", DivisionIDs: []int64{2}}, 14: {AccountID: 14, Role: "examinator", Active: true, Login: "examiner2", DivisionIDs: []int64{2}}}}
	return e, a, p
}
func auxJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func auxError(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil || err.Error() != code {
		t.Fatalf("got %v, want %s", err, code)
	}
}
func auxRow(t *testing.T, id string, value any) ImportRow {
	return ImportRow{RowID: id, Input: auxJSON(t, value)}
}
func auxXML(text string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(text))
	return b.String()
}
func auxInline(col string, row int, value string) string {
	return fmt.Sprintf(`<c r="%s%d" t="inlineStr"><is><t>%s</t></is></c>`, col, row, auxXML(value))
}
func auxSheet(headers []string, rows [][]string) string {
	var b strings.Builder
	b.WriteString(`<worksheet><sheetData><row r="1">`)
	for i, h := range headers {
		b.WriteString(auxInline(string(rune('A'+i)), 1, h))
	}
	b.WriteString(`</row>`)
	for n, row := range rows {
		fmt.Fprintf(&b, `<row r="%d">`, n+2)
		for i, v := range row {
			b.WriteString(auxInline(string(rune('A'+i)), n+2, v))
		}
		b.WriteString(`</row>`)
	}
	b.WriteString(`</sheetData></worksheet>`)
	return b.String()
}
func auxXLSX(t testing.TB, sheets ...string) []byte {
	t.Helper()
	entries := map[string]string{}
	var workbook, rels strings.Builder
	workbook.WriteString(`<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`)
	rels.WriteString(`<Relationships>`)
	for n, sheet := range sheets {
		id := n + 1
		fmt.Fprintf(&workbook, `<sheet name="Sheet%d" sheetId="%d" r:id="rId%d"/>`, id, id, id)
		fmt.Fprintf(&rels, `<Relationship Id="rId%d" Target="worksheets/sheet%d.xml" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet"/>`, id, id)
		entries[fmt.Sprintf("xl/worksheets/sheet%d.xml", id)] = sheet
	}
	workbook.WriteString(`</sheets></workbook>`)
	rels.WriteString(`</Relationships>`)
	entries["xl/workbook.xml"] = workbook.String()
	entries["xl/_rels/workbook.xml.rels"] = rels.String()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, value := range entries {
		writer, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = writer.Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestAuxParseXLSXQuestions(t *testing.T) {
	content := auxXLSX(t, auxSheet([]string{"часть", "вопрос", "эталонный ответ"}, [][]string{{"A", "Что?", "Ответ"}}))
	rows, err := ParseXLSX(content, "BANK.XLSX", "questions")
	if err != nil || len(rows) != 1 {
		t.Fatalf("parse %v %+v", err, rows)
	}
	if rows[0].RowID != "row-2" || rows[0].SourceRow != 2 {
		t.Fatal(rows)
	}
	var input map[string]string
	if err = json.Unmarshal(rows[0].Input, &input); err != nil || input["questionText"] != "Что?" {
		t.Fatal(err, input)
	}
}
func TestAuxParseXLSXGuardrails(t *testing.T) {
	valid := auxSheet([]string{"part_code", "question_text", "answer_text"}, [][]string{{"A", "q", "a"}})
	cases := []struct {
		name   string
		sheets []string
		code   string
	}{
		{"multiple", []string{valid, valid}, "multiple_import_sheets"},
		{"DTD", []string{`<!DOCTYPE worksheet [<!ENTITY x "danger">]>` + valid}, "invalid_import_file"},
		{"too many columns", []string{strings.Replace(valid, `<worksheet>`, `<worksheet><dimension ref="A1:AG2"/>`, 1)}, "import_columns_limit"},
		{"too many physical rows", []string{strings.Replace(valid, `<worksheet>`, `<worksheet><dimension ref="A1:A100001"/>`, 1)}, "import_rows_limit"},
		{"unknown header", []string{auxSheet([]string{"part_code", "question_text", "answer_text", "other"}, [][]string{{"A", "q", "a", "x"}})}, "unknown_import_header"},
		{"duplicate header aliases", []string{auxSheet([]string{"part_code", "часть", "question_text", "answer_text"}, [][]string{{"A", "A", "q", "a"}})}, "duplicate_import_headers"},
		{"missing header", []string{auxSheet([]string{"part_code", "question_text"}, [][]string{{"A", "q"}})}, "missing_import_headers"},
		{"empty", []string{`<worksheet><sheetData/></worksheet>`}, "import_no_rows"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseXLSX(auxXLSX(t, test.sheets...), "a.xlsx", "questions")
			auxError(t, err, test.code)
		})
	}
	_, err := ParseXLSX([]byte("not zip"), "a.xlsx", "questions")
	auxError(t, err, "invalid_import_file")
	_, err = ParseXLSX(nil, "a.xls", "questions")
	auxError(t, err, "invalid_import_file")
	_, err = ParseXLSX(nil, "a.xlsx", "wrong")
	auxError(t, err, "invalid_import_type")
	_, err = ParseXLSX(make([]byte, maxImportFile+1), "a.xlsx", "questions")
	auxError(t, err, "payload_too_large")
}
func TestAuxFormulaIsRowError(t *testing.T) {
	sheet := auxSheet([]string{"part_code", "question_text", "answer_text"}, [][]string{{"A", "q", "a"}})
	sheet = strings.Replace(sheet, auxInline("B", 2, "q"), `<c r="B2"><f>1+1</f><v>2</v></c>`, 1)
	rows, err := ParseXLSX(auxXLSX(t, sheet), "a.xlsx", "questions")
	if err != nil {
		t.Fatal(err)
	}
	e, a, p := auxFixture()
	checked, err := ValidateImport(e, a, "questions", p, rows)
	if err != nil || len(checked[0].Errors) == 0 {
		t.Fatalf("formula accepted %v %+v", err, checked)
	}
}
func TestAuxImportQuestionsAtomicityAndDuplicates(t *testing.T) {
	e, a, p := auxFixture()
	rows := []ImportRow{auxRow(t, "first", map[string]any{"partCode": "B", "questionText": "new", "answerText": "answer"}), auxRow(t, "second", map[string]any{"partCode": "B", "questionText": "bad", "answerText": ""})}
	before := auxJSON(t, e)
	next := e.NextID
	checked, err := ValidateImport(e, a, "questions", p, rows)
	if err != nil || len(checked[1].Errors) == 0 {
		t.Fatal(err, checked)
	}
	if !bytes.Equal(before, auxJSON(t, e)) || e.NextID != next {
		t.Fatal("preview mutated exam")
	}
	_, err = CommitImport(e, a, "questions", p, rows)
	auxError(t, err, "import_conflict")
	if !bytes.Equal(before, auxJSON(t, e)) || e.NextID != next {
		t.Fatal("failed commit mutated exam")
	}
	rows[1].Excluded = true
	if _, err = CommitImport(e, a, "questions", p, rows); err != nil {
		t.Fatal(err)
	}
	if len(e.Parts) != 2 || len(e.Questions) != 2 {
		t.Fatal(e)
	}
	duplicate := []ImportRow{auxRow(t, "1", map[string]any{"partCode": "A", "questionText": "dupe", "answerText": "a"}), auxRow(t, "2", map[string]any{"partCode": "A", "questionText": "dupe", "answerText": "a"})}
	checked, err = ValidateImport(e, a, "questions", p, duplicate)
	if err != nil || len(checked[0].Errors) == 0 || len(checked[1].Errors) == 0 {
		t.Fatal(err, checked)
	}
}
func TestAuxAssignmentsImportsAndPrivilegeBaseline(t *testing.T) {
	e, a, p := auxFixture()
	rows := []ImportRow{auxRow(t, "1", map[string]any{"studentLogin": "student", "examinator1Login": "examiner", "replacementLimit": 2}), auxRow(t, "2", map[string]any{"studentLogin": "12345", "examinator1Id": 13})}
	ids, logins, err := ExtractImportReferences(rows)
	if err != nil || !reflect.DeepEqual(ids, []int64{13}) || !reflect.DeepEqual(logins, []string{"12345", "examiner", "student"}) {
		t.Fatal(err, ids, logins)
	}
	checked, err := ValidateImport(e, a, "assignments", p, rows)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range checked {
		if len(row.Errors) > 0 {
			t.Fatal(checked)
		}
	}
	if checked[0].PrivilegeBaselineVersion == nil {
		t.Fatal("baseline missing")
	}
	e.Divisions[2].Privileges = map[int64]Privilege{11: {Limit: 3, Revision: 1}}
	_, err = CommitImport(e, a, "assignments", p, checked)
	auxError(t, err, "import_conflict")
	if len(e.Divisions[2].Assignments) != 0 {
		t.Fatal("partial assignment")
	}
	checked[0].PrivilegeBaselineVersion = nil
	checked, err = ValidateImport(e, a, "assignments", p, checked)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = CommitImport(e, a, "assignments", p, checked); err != nil {
		t.Fatal(err)
	}
	if len(e.Divisions[2].Assignments) != 2 || len(e.Divisions[2].Commissions) != 1 || e.Divisions[2].Privileges[11].Limit != 2 {
		t.Fatal(e.Divisions[2])
	}
}
func TestAuxImportScopeAndOutside(t *testing.T) {
	e, a, p := auxFixture()
	e.Kind = "outside_lms"
	rows := []ImportRow{auxRow(t, "1", map[string]any{"studentId": 11, "points": 123.25, "grade": 0, "examinator": "External"})}
	if _, err := CommitImport(e, a, "outside", p, rows); err != nil {
		t.Fatal(err)
	}
	if e.Divisions[2].Outside[0].Grade != 0 || e.Divisions[2].Outside[0].Points != "123.25" {
		t.Fatal(e.Divisions[2].Outside)
	}
	a.OrganizationID = 99
	_, err := ValidateImport(e, a, "outside", p, rows)
	auxError(t, err, "forbidden")
	a.OrganizationID = 1
	a.Role = "staff_admin"
	a.OrganizationAdmin = false
	a.DivisionIDs = []int64{3}
	a.Permissions = map[string]Permission{"exams": {Edit: true}}
	_, err = ValidateImport(e, a, "outside", p, rows)
	auxError(t, err, "forbidden")
}
func TestAuxTrainingFingerprintAndLiveChanges(t *testing.T) {
	if fp := TrainingFingerprint(" q ", " a "); fp != "4c394540473456c9e95034603ef746c19e4ce6a2de9fcbc0d65d93a2072c968d" {
		t.Fatal(fp)
	}
	if fp := TrainingFingerprint("Вопрос <&>\n", "Ответ \u2028"); fp != "fbb010d999931ad41cd6b51526a1234e4c25adcf0585162f246861cc21d9c529" {
		t.Fatal(fp)
	}
	e, _, p := auxFixture()
	student := Actor{AccountID: 11, OrganizationID: 1, Role: "student", DivisionIDs: []int64{2}}
	progress := map[string]string{"exam:1:5": TrainingFingerprint("q", "a")}
	view, err := TrainingView(e, student, p, progress, DefaultTrainingSettings())
	if err != nil {
		t.Fatal(err)
	}
	cards := view.(map[string]any)["cards"].([]TrainingCard)
	if cards[0].Status != "learned" {
		t.Fatal(cards)
	}
	e.Questions[0].Answer = "new"
	view, err = TrainingView(e, student, p, progress, DefaultTrainingSettings())
	if err != nil {
		t.Fatal(err)
	}
	if view.(map[string]any)["cards"].([]TrainingCard)[0].Status != "answer_changed" {
		t.Fatal(view)
	}
	_, err = ResolveTrainingCard(e, p, 5, progress["exam:1:5"])
	auxError(t, err, "content_changed")
	e.Divisions[2].TrainingEnabled = false
	_, err = TrainingView(e, student, p, progress, DefaultTrainingSettings())
	auxError(t, err, "training_disabled")
}
func TestAuxTrainingPreviewAndSettings(t *testing.T) {
	e, a, p := auxFixture()
	e.Divisions[2].TrainingEnabled = false
	p.Query = map[string]string{"preview": "true"}
	if _, err := TrainingView(e, a, p, nil, DefaultTrainingSettings()); err != nil {
		t.Fatal(err)
	}
	a.Role = "examinator"
	a.OrganizationAdmin = false
	a.DivisionIDs = []int64{2}
	_, err := TrainingView(e, a, p, nil, DefaultTrainingSettings())
	auxError(t, err, "forbidden")
	settings, err := ValidateTrainingSettings(e, p, DefaultTrainingSettings(), json.RawMessage(`{"batch_size":20,"study_mode":"stale","last_batch_index":99}`))
	if err != nil || settings.LastBatchIndex != 0 || settings.StudyMode != "stale" {
		t.Fatal(err, settings)
	}
	for _, raw := range []string{`{"batch_size":15}`, `{"study_mode":"unknown"}`, `{"last_batch_index":-2}`, `{"last_part_id":99}`, `{"batch_size":10,"batch_size":20}`, `{"extra":1}`} {
		if _, err := ValidateTrainingSettings(e, p, DefaultTrainingSettings(), json.RawMessage(raw)); err == nil {
			t.Fatal("accepted", raw)
		}
	}
}
func TestAuxRatingSnapshotDivisionAndRetake(t *testing.T) {
	e, _, p := auxFixture()
	start := time.Date(2026, 10, 7, 21, 0, 0, 0, time.UTC)
	future := p.Now.Add(time.Hour)
	e.Divisions[2].Config.StartAt = &start
	e.Divisions[2].Attempts = []Attempt{{ID: 30, Student: p.Members[11], AttemptNo: 1, Status: "completed", CalculatedGrade: 5, ResultVersion: 1}, {ID: 31, Student: p.Members[11], AttemptNo: 2, Status: "in_progress", CalculatedGrade: 0}}
	e.Divisions[3] = &Division{ID: 3, Revision: 1, Config: Config{StartAt: &start}}
	e2, _, _ := auxFixture()
	e2.ID = 2
	e2.Divisions[2].Config.StartAt = &future
	value, err := RatingSnapshot([]*Exam{e, e2}, 1, []int64{2, 3}, "2026-10-08", "2026-10-08", p.Now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := value.(RatingSnapshotDTO)
	if len(snapshot.Exams) != 2 || len(snapshot.Results) != 1 || snapshot.Results[0].Grade != 5 || snapshot.Results[0].DivisionID != 2 || snapshot.NextExamStartAt == nil {
		t.Fatal(snapshot)
	}
	e.Divisions[2].Attempts[1].Status = "completed"
	e.Divisions[2].Attempts[1].CalculatedGrade = 1
	e.Divisions[2].Attempts[1].ResultVersion = 2
	e.Divisions[2].Attempts[1].Appeals = []Appeal{{Grade: 2}}
	e.Divisions[2].Revision++
	value, err = RatingSnapshot([]*Exam{e, e2}, 1, []int64{2, 3}, "2026-10-08", "2026-10-08", p.Now)
	if err != nil {
		t.Fatal(err)
	}
	next := value.(RatingSnapshotDTO)
	if next.Results[0].Grade != 2 || next.Results[0].AttemptNo != 2 || !next.Results[0].HasAppeal || next.SourceRevision == snapshot.SourceRevision {
		t.Fatal(next)
	}
}
func FuzzAuxParseXLSX(f *testing.F) {
	f.Add(auxXLSX(f, auxSheet([]string{"part_code", "question_text", "answer_text"}, [][]string{{"A", "q", "a"}})))
	f.Add([]byte("not zip"))
	f.Fuzz(func(t *testing.T, content []byte) { _, _ = ParseXLSX(content, "fuzz.xlsx", "questions") })
}

func TestAuxTrainingToggleStrictAndEmpty(t *testing.T) {
	e, a, p := auxFixture()
	for _, raw := range []string{`{"training_enabled":true,"training_enabled":false,"expected_revision":1}`, `{"training_enabled":true}`, `{"training_enabled":true,"expected_revision":1,"unknown":1}`} {
		if _, err := AuxCommand(e, a, "training-toggle", p, json.RawMessage(raw)); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	e.Questions = nil
	_, err := AuxCommand(e, a, "training-toggle", p, json.RawMessage(`{"training_enabled":true,"expected_revision":1}`))
	auxError(t, err, "exam_empty")
	if _, err = AuxCommand(e, a, "training-toggle", p, json.RawMessage(`{"training_enabled":false,"expected_revision":1}`)); err != nil {
		t.Fatal(err)
	}
}
func TestAuxImportReadOnlyPreview(t *testing.T) {
	e, _, p := auxFixture()
	a := Actor{AccountID: 10, OrganizationID: 1, Role: "staff_admin", DivisionIDs: []int64{2}, Permissions: map[string]Permission{"exams": {View: true}}}
	rows := []ImportRow{auxRow(t, "1", map[string]any{"partCode": "B", "questionText": "new", "answerText": "answer"})}
	checked, err := ValidateImport(e, a, "questions", p, rows)
	if err != nil || len(checked[0].Errors) > 0 {
		t.Fatal(err, checked)
	}
	if len(e.Parts) != 1 || len(e.Questions) != 1 || a.Permissions["exams"].Edit {
		t.Fatal("preview altered exam or actor")
	}
	_, err = CommitImport(e, a, "questions", p, rows)
	auxError(t, err, "forbidden")
}

func BenchmarkAuxValidateQuestions5000(b *testing.B) {
	rows := make([]ImportRow, 5000)
	for i := range rows {
		input, _ := json.Marshal(map[string]any{"partCode": "A", "questionText": fmt.Sprintf("question-%d", i), "answerText": "synthetic answer"})
		rows[i] = ImportRow{RowID: fmt.Sprint(i), Input: input}
	}
	for n := 0; n < b.N; n++ {
		e, a, p := auxFixture()
		e.Questions = nil
		checked, err := ValidateImport(e, a, "questions", p, rows)
		if err != nil || len(checked) != 5000 || len(checked[4999].Errors) > 0 {
			b.Fatal(err, checked[4999])
		}
	}
}
func TestAuxXLSXSharedStringsAndIntegralNumbers(t *testing.T) {
	// Numeric cells from Excel are normalized to integers before strict row DTOs.
	sheet := auxSheet([]string{"student_id", "points", "grade", "examinator"}, [][]string{{"11", "3.50", "0", "External"}})
	sheet = strings.Replace(sheet, auxInline("A", 2, "11"), `<c r="A2"><v>11.0</v></c>`, 1)
	sheet = strings.Replace(sheet, auxInline("B", 2, "3.50"), `<c r="B2"><v>3.50</v></c>`, 1)
	sheet = strings.Replace(sheet, auxInline("C", 2, "0"), `<c r="C2"><v>0.0</v></c>`, 1)
	rows, err := ParseXLSX(auxXLSX(t, sheet), "a.xlsx", "outside")
	if err != nil {
		t.Fatal(err)
	}
	e, a, p := auxFixture()
	e.Kind = "outside_lms"
	if _, err = CommitImport(e, a, "outside", p, rows); err != nil {
		t.Fatal(err)
	}
	if e.Divisions[2].Outside[0].Points != "3.50" {
		t.Fatal(e.Divisions[2].Outside)
	}
}

func auxAddZIP(t testing.TB, content []byte, name, value string) []byte {
	t.Helper()
	source, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	out := zip.NewWriter(&b)
	for _, file := range source.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		writer, err := out.Create(file.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = writer.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	writer, err := out.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Write([]byte(value)); err != nil {
		t.Fatal(err)
	}
	if err = out.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestAuxSharedStringRichTextAndDates(t *testing.T) {
	sheet := auxSheet([]string{"student_id", "points", "grade", "examinator"}, [][]string{{"11", "3.50", "0", "External"}})
	for _, cell := range []struct{ col, value, xml string }{{"A", "11", `<c r="A2"><v>11</v></c>`}, {"B", "3.50", `<c r="B2"><v>3.50</v></c>`}, {"C", "0", `<c r="C2"><v>0</v></c>`}, {"D", "External", `<c r="D2" t="s"><v>0</v></c>`}} {
		sheet = strings.Replace(sheet, auxInline(cell.col, 2, cell.value), cell.xml, 1)
	}
	file := auxAddZIP(t, auxXLSX(t, sheet), "xl/sharedStrings.xml", `<sst><si><r><t>Ex</t></r><r><t>ternal</t></r></si></sst>`)
	rows, err := ParseXLSX(file, "a.xlsx", "outside")
	if err != nil {
		t.Fatal(err)
	}
	e, a, p := auxFixture()
	e.Kind = "outside_lms"
	if _, err = CommitImport(e, a, "outside", p, rows); err != nil {
		t.Fatal(err)
	}
	if e.Divisions[2].Outside[0].Examinator != "External" {
		t.Fatal(e.Divisions[2].Outside)
	}
	sheet = strings.Replace(sheet, `<c r="B2"><v>3.50</v></c>`, `<c r="B2" s="1"><v>46000</v></c>`, 1)
	file = auxAddZIP(t, auxXLSX(t, sheet), "xl/sharedStrings.xml", `<sst><si><t>External</t></si></sst>`)
	file = auxAddZIP(t, file, "xl/styles.xml", `<styleSheet><cellXfs><xf numFmtId="0"/><xf numFmtId="14"/></cellXfs></styleSheet>`)
	rows, err = ParseXLSX(file, "a.xlsx", "outside")
	if err != nil {
		t.Fatal(err)
	}
	e, a, p = auxFixture()
	e.Kind = "outside_lms"
	checked, err := ValidateImport(e, a, "outside", p, rows)
	if err != nil || len(checked[0].Errors) == 0 {
		t.Fatal("Excel date accepted as numeric score", err, checked)
	}
}
func FuzzAuxWorksheetXML(f *testing.F) {
	f.Add([]byte(auxSheet([]string{"part_code", "question_text", "answer_text"}, [][]string{{"A", "q", "a"}})))
	f.Add([]byte(`<worksheet><sheetData/></worksheet>`))
	f.Fuzz(func(t *testing.T, source []byte) {
		if len(source) > 1<<20 {
			return
		}
		content := auxXLSX(t, string(source))
		_, _ = ParseXLSX(content, "fuzz.xlsx", "questions")
	})
}

func TestAuxImportPreviewSizeAndContentOwner(t *testing.T) {
	e, a, p := auxFixture()
	oversized := []ImportRow{auxRow(t, "1", map[string]any{"partCode": "A", "questionText": strings.Repeat("x", maxImportPreview), "answerText": "answer"})}
	_, err := ValidateImport(e, a, "questions", p, oversized)
	auxError(t, err, "payload_too_large")
	e.Divisions[3] = &Division{ID: 3, Revision: 1}
	p.DivisionID = 3
	a.Role = "staff_admin"
	a.OrganizationAdmin = false
	a.DivisionIDs = []int64{2, 3}
	a.Permissions = map[string]Permission{"exams": {Edit: true}}
	_, err = CommitImport(e, a, "questions", p, []ImportRow{auxRow(t, "1", map[string]any{"partCode": "B", "questionText": "new", "answerText": "answer"})})
	auxError(t, err, "forbidden")
}
