package uploads

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func sourceTestArchive(t *testing.T, sheet string, extras map[string]string) []byte {
	t.Helper()
	entries := map[string]string{
		"[Content_Types].xml":        `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/></Types>`,
		"xl/workbook.xml":            `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Fixture" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>` + sheet + `</sheetData></worksheet>`,
	}
	for key, value := range extras {
		entries[key] = value
	}
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for name, value := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = entry.Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
func sourceTestCell(ref, value string) string {
	value = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(value)
	return fmt.Sprintf(`<c r="%s" t="inlineStr"><is><t>%s</t></is></c>`, ref, value)
}
func sourceTestError(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil || err.Error() != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestOwnerInputLegacyAliasesAndPhysicalRows(t *testing.T) {
	sheet := `<row r="1">` + sourceTestCell("A1", " Ф.и.о. ") + sourceTestCell("B1", "CLASS") + sourceTestCell("C1", "Куратор") + `</row><row r="2"/><row r="8">` + sourceTestCell("A8", " Тестов УченикА ") + `<c r="B8"><v>7.5</v></c>` + sourceTestCell("C8", "Синтетиков ПрокторА") + `</row>`
	raw, err := parseOwnerInput("users.legacy", sourceTestArchive(t, sheet, nil), "xlsx", map[string]string{"organization_id": "999", "division_id": "999", "account_id": "999"})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Rows []legacySourceRow `json:"rows"`
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) != 1 || out.Rows[0].Row != 8 || out.Rows[0].ClassRaw != "7.5" || out.Rows[0].FullName != "Тестов УченикА" || out.Rows[0].ProctorName != "Синтетиков ПрокторА" {
		t.Fatalf("raw contract mismatch: %s", raw)
	}
	if bytes.Contains(raw, []byte("organization_id")) || bytes.Contains(raw, []byte("division_id")) || bytes.Contains(raw, []byte("account_id")) {
		t.Fatal("scope must not come from source fields")
	}
}

func TestOwnerInputExternalHeaderSearchAndZero(t *testing.T) {
	sheet := `<row r="1">` + sourceTestCell("A1", "Synthetic export") + `</row><row r="10">` + sourceTestCell("A10", "ФИО") + sourceTestCell("B10", "Процент") + sourceTestCell("C10", "Верных ответов") + `</row><row r="12">` + sourceTestCell("A12", "Тестов УченикА") + `<c r="B12"><v>0</v></c><c r="C12"><v>0</v></c></row>`
	content := sourceTestArchive(t, sheet, nil)
	raw, err := parseOwnerInput("tests.external-results", content, "xlsx", nil)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Rows []externalSourceRow `json:"rows"`
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) != 1 || out.Rows[0].Row != 12 || out.Rows[0].Percent == nil || *out.Rows[0].Percent != 0 || out.Rows[0].CorrectCount == nil || *out.Rows[0].CorrectCount != 0 {
		t.Fatalf("zero/source-row lost: %s", raw)
	}
	id := out.Rows[0].RowID
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-8[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) || id != sourceRowUUID(content, 12) || id == sourceRowUUID(content, 13) {
		t.Fatalf("unstable row reference: %s", id)
	}
	late := strings.NewReplacer(`r="10"`, `r="11"`, `r="A10"`, `r="A11"`, `r="B10"`, `r="B11"`, `r="C10"`, `r="C11"`).Replace(sheet)
	_, err = parseOwnerInput("tests.external-results", sourceTestArchive(t, late, nil), "xlsx", nil)
	sourceTestError(t, err, "missing_source_headers")
}

func TestOwnerInputDoesNotValidateSubjectRules(t *testing.T) {
	sheet := `<row r="1">` + sourceTestCell("A1", "ФИО") + sourceTestCell("B1", "Процент") + `</row><row r="2">` + sourceTestCell("A2", "Однослово") + sourceTestCell("B2", "101,5%") + `</row><row r="3">` + sourceTestCell("A3", "Однослово") + sourceTestCell("B3", "нет числа") + `</row>`
	raw, err := parseOwnerInput("tests.external-results", sourceTestArchive(t, sheet, nil), "xlsx", nil)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Rows []externalSourceRow `json:"rows"`
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) != 2 || out.Rows[0].Percent == nil || *out.Rows[0].Percent != 101.5 || out.Rows[1].Percent != nil || out.Rows[0].FullName != out.Rows[1].FullName {
		t.Fatalf("domain input altered: %s", raw)
	}
}

func TestOwnerInputTypedCells(t *testing.T) {
	header := `<row r="1">` + sourceTestCell("A1", "ФИО") + sourceTestCell("B1", "Процент") + sourceTestCell("C1", "Верных ответов") + `</row>`
	for _, test := range []struct{ name, score, count, code string }{
		{"boolean_score", `<c r="B2" t="b"><v>1</v></c>`, "", "invalid_source_cell_2_percent"},
		{"error_score", `<c r="B2" t="e"><v>#VALUE!</v></c>`, "", "invalid_source_cell_2_percent"},
		{"fractional_count", `<c r="B2"><v>50</v></c>`, `<c r="C2"><v>2.5</v></c>`, "invalid_source_cell_2_correct_count"},
		{"nonfinite_score", sourceTestCell("B2", "NaN"), "", "invalid_source_cell_2_percent"},
	} {
		t.Run(test.name, func(t *testing.T) {
			sheet := header + `<row r="2">` + sourceTestCell("A2", "Тестов УченикА") + test.score + test.count + `</row>`
			_, err := parseOwnerInput("tests.external-results", sourceTestArchive(t, sheet, nil), "xlsx", nil)
			sourceTestError(t, err, test.code)
		})
	}
}

func TestOwnerInputExternalExplicitIDsPreservePrecision(t *testing.T) {
	header := `<row r="10">` + sourceTestCell("A10", "ФИО") + sourceTestCell("B10", "Процент") + sourceTestCell("C10", "Логин") + sourceTestCell("D10", "ACCOUNT_ID") + sourceTestCell("E10", "legacy_student_id") + sourceTestCell("F10", "id") + `</row>`
	sheet := `<row r="1">` + sourceTestCell("A1", "Synthetic export") + `</row>` + header +
		`<row r="12">` + sourceTestCell("A12", " Тестов УченикА ") + `<c r="B12"><v>0.875</v></c>` + sourceTestCell("C12", "Uploads-Fixture-A") + `<c r="D12"><v>9007199254740991</v></c>` + sourceTestCell("E12", "9223372036854775807") + `<c r="F12"><v>999</v></c></row>` +
		`<row r="13">` + sourceTestCell("A13", "Тестов УченикБ") + `<c r="B13"><v>0</v></c>` + sourceTestCell("D13", "9007199254740993") + `<c r="E13" t="n"><v>17</v></c></row>` +
		`<row r="14">` + sourceTestCell("A14", "Тестов УченикВ") + sourceTestCell("B14", "87,5%") + sourceTestCell("D14", "  ") + `<c r="E14"><v/></c><c r="F14"><v>123</v></c></row>` +
		`<row r="15">` + sourceTestCell("A15", "Тестов УченикГ") + `<c r="B15"><v>50</v></c></row>`
	content := sourceTestArchive(t, sheet, nil)
	raw, err := parseOwnerInput("tests.external-results", content, "xlsx", map[string]string{"account_id": "999", "legacy_student_id": "999"})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Rows []externalSourceRow `json:"rows"`
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) != 4 || out.Rows[0].AccountID == nil || *out.Rows[0].AccountID != 9007199254740991 || out.Rows[0].LegacyStudentID == nil || *out.Rows[0].LegacyStudentID != 9223372036854775807 ||
		out.Rows[1].AccountID == nil || *out.Rows[1].AccountID != 9007199254740993 || out.Rows[1].LegacyStudentID == nil || *out.Rows[1].LegacyStudentID != 17 {
		t.Fatalf("explicit ID precision lost: %s", raw)
	}
	if out.Rows[0].FullName != "Тестов УченикА" || out.Rows[0].Login != "Uploads-Fixture-A" || out.Rows[0].Row != 12 || out.Rows[0].Percent == nil || *out.Rows[0].Percent != 0.875 ||
		out.Rows[1].Row != 13 || out.Rows[1].Percent == nil || *out.Rows[1].Percent != 0 || out.Rows[0].RowID != sourceRowUUID(content, 12) {
		t.Fatalf("existing source contract changed: %s", raw)
	}
	var encoded struct {
		Rows []map[string]json.RawMessage `json:"rows"`
	}
	if json.Unmarshal(raw, &encoded) != nil {
		t.Fatal("invalid rows JSON")
	}
	for _, index := range []int{2, 3} {
		if out.Rows[index].AccountID != nil || out.Rows[index].LegacyStudentID != nil || encoded.Rows[index]["account_id"] != nil || encoded.Rows[index]["legacy_student_id"] != nil {
			t.Fatalf("blank/absent IDs must be omitted, generic id must stay ignored: %s", raw)
		}
	}
}

func TestOwnerInputExternalIDsRejectMalformedCells(t *testing.T) {
	header := `<row r="10">` + sourceTestCell("A10", "ФИО") + sourceTestCell("B10", "Процент") + sourceTestCell("C10", "account_id") + sourceTestCell("D10", "legacy_student_id") + `</row>`
	styles := map[string]string{"xl/styles.xml": `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><cellXfs><xf numFmtId="0"/><xf numFmtId="14"/></cellXfs></styleSheet>`}
	for _, field := range []string{"account_id", "legacy_student_id"} {
		column := "C12"
		if field == "legacy_student_id" {
			column = "D12"
		}
		for _, test := range []struct {
			name, cell, suffix string
		}{
			{"text_zero", sourceTestCell(column, "0"), ""},
			{"text_negative", sourceTestCell(column, "-1"), ""},
			{"text_plus", sourceTestCell(column, "+1"), ""},
			{"text_fraction", sourceTestCell(column, "1.0"), ""},
			{"text_exponent", sourceTestCell(column, "1e3"), ""},
			{"text_overflow", sourceTestCell(column, "9223372036854775808"), ""},
			{"text_inner_space", sourceTestCell(column, "1 2"), ""},
			{"text_non_ascii_digits", sourceTestCell(column, "١٢"), ""},
			{"number_zero", fmt.Sprintf(`<c r="%s"><v>0</v></c>`, column), ""},
			{"number_negative", fmt.Sprintf(`<c r="%s"><v>-1</v></c>`, column), ""},
			{"number_fraction", fmt.Sprintf(`<c r="%s"><v>1.5</v></c>`, column), ""},
			{"number_exponent", fmt.Sprintf(`<c r="%s"><v>1e3</v></c>`, column), ""},
			{"number_nan", fmt.Sprintf(`<c r="%s"><v>NaN</v></c>`, column), ""},
			{"number_infinity", fmt.Sprintf(`<c r="%s"><v>Inf</v></c>`, column), ""},
			{"number_not_numeric", fmt.Sprintf(`<c r="%s"><v>bad-value</v></c>`, column), ""},
			{"number_unsafe_rounded_boundary", fmt.Sprintf(`<c r="%s"><v>9007199254740992</v></c>`, column), "_use_text"},
			{"number_unsafe_odd", fmt.Sprintf(`<c r="%s"><v>9007199254740993</v></c>`, column), "_use_text"},
			{"number_max_int64", fmt.Sprintf(`<c r="%s"><v>9223372036854775807</v></c>`, column), "_use_text"},
			{"boolean", fmt.Sprintf(`<c r="%s" t="b"><v>1</v></c>`, column), ""},
			{"blank_boolean", fmt.Sprintf(`<c r="%s" t="b"><v/></c>`, column), ""},
			{"error", fmt.Sprintf(`<c r="%s" t="e"><v>#VALUE!</v></c>`, column), ""},
			{"date", fmt.Sprintf(`<c r="%s" t="d"><v>2026-10-09</v></c>`, column), ""},
			{"invalid_date", fmt.Sprintf(`<c r="%s" t="d"><v>bad-date</v></c>`, column), ""},
			{"numeric_date", fmt.Sprintf(`<c r="%s" s="1"><v>123</v></c>`, column), ""},
			{"invalid_numeric_date", fmt.Sprintf(`<c r="%s" s="1"><v>bad-date</v></c>`, column), ""},
		} {
			t.Run(field+"/"+test.name, func(t *testing.T) {
				sheet := header + `<row r="12">` + sourceTestCell("A12", "Тестов УченикА") + `<c r="B12"><v>0</v></c>` + test.cell + `</row>`
				_, err := parseOwnerInput("tests.external-results", sourceTestArchive(t, sheet, styles), "xlsx", nil)
				sourceTestError(t, err, "invalid_source_cell_12_"+field+test.suffix)
				if apiErr, ok := err.(*APIError); !ok || apiErr.Code != 422 {
					t.Fatalf("malformed ID must return422, got %v", err)
				}
			})
		}
	}
	_, err := parseOwnerInput("tests.external-results", sourceTestArchive(t, header+`<row r="12"><c r="C12" t="b"><v/></c></row>`, nil), "xlsx", nil)
	sourceTestError(t, err, "invalid_source_cell_12_account_id")
}

func TestOwnerInputExternalIDHeadersCannotFallBack(t *testing.T) {
	for _, field := range []string{"account_id", "legacy_student_id"} {
		t.Run(field, func(t *testing.T) {
			sheet := `<row r="4">` + sourceTestCell("A4", "ФИО") + sourceTestCell("B4", "Процент") + sourceTestCell("C4", field) + sourceTestCell("D4", strings.ToUpper(field)) + `</row>` +
				`<row r="5">` + sourceTestCell("A5", "ФИО") + sourceTestCell("B5", "Процент") + sourceTestCell("C5", field) + `</row>` +
				`<row r="12">` + sourceTestCell("A12", "Тестов УченикА") + `<c r="B12"><v>50</v></c><c r="C12"><v>17</v></c><c r="D12"><v>18</v></c></row>`
			_, err := parseOwnerInput("tests.external-results", sourceTestArchive(t, sheet, nil), "xlsx", nil)
			sourceTestError(t, err, "duplicate_source_header_4_"+field)
			if apiErr, ok := err.(*APIError); !ok || apiErr.Code != 422 {
				t.Fatalf("duplicate ID headers must return422, got %v", err)
			}
		})
	}
}

func TestOwnerInputActiveSheetSharedStringsAndDate(t *testing.T) {
	extras := map[string]string{
		"xl/workbook.xml":            `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><bookViews><workbookView activeTab="1"/></bookViews><sheets><sheet name="Ignored" sheetId="1" r:id="rId1"/><sheet name="Data" sheetId="2" r:id="rId2"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="/xl/worksheets/sheet2.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>ФИО</t></si><si><t>Процент</t></si><si><t>Дата завершения</t></si><si><r><t>Тестов </t></r><r><t>УченикА</t></r></si></sst>`,
		"xl/styles.xml":              `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><cellXfs><xf numFmtId="0"/><xf numFmtId="14"/><xf numFmtId="10"/></cellXfs></styleSheet>`,
		"xl/worksheets/sheet2.xml":   `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="C1" t="s"><v>2</v></c></row><row r="2"><c r="A2" t="s"><v>3</v></c><c r="B2" s="2"><v>0.875</v></c><c r="C2" s="1"><v>25569.5</v></c></row></sheetData></worksheet>`,
	}
	raw, err := parseOwnerInput("tests.external-results", sourceTestArchive(t, `<row r="1">`+sourceTestCell("A1", "Wrong sheet")+`</row>`, extras), "xlsx", nil)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Rows []externalSourceRow `json:"rows"`
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Rows[0].FullName != "Тестов УченикА" || out.Rows[0].CompletedAt != "1970-01-01 12:00:00" || *out.Rows[0].Percent != 0.875 {
		t.Fatalf("active/shared/date contract: %s", raw)
	}
}

func TestOwnerInputRejectsUnsafeWorkbook(t *testing.T) {
	header := `<row r="1">` + sourceTestCell("A1", "ФИО") + sourceTestCell("B1", "Класс") + `</row>`
	for _, test := range []struct {
		name, sheet string
		extra       map[string]string
		code        string
	}{
		{"formula", header + `<row r="2">` + sourceTestCell("A2", "Тестов УченикА") + `<c r="B2"><f>1+1</f><v>2</v></c></row>`, nil, "formulas_forbidden"},
		{"macro", header, map[string]string{"xl/vbaProject.bin": "synthetic-no-executable-macro"}, "active_content_forbidden"},
		{"external_relationship", header, map[string]string{"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="https://example.invalid/no-fetch" TargetMode="External"/></Relationships>`}, "external_relationship_forbidden"},
		{"entity", header, map[string]string{"xl/sharedStrings.xml": `<!DOCTYPE sst [<!ENTITY x "fixture">]><sst/>`}, "xml_entities_forbidden"},
		{"bad_reference", header + `<row r="2">` + sourceTestCell("A3", "Тестов УченикА") + `</row>`, nil, "invalid_xlsx_cell_reference"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseOwnerInput("users.legacy", sourceTestArchive(t, test.sheet, test.extra), "xlsx", nil)
			sourceTestError(t, err, test.code)
		})
	}
}

func TestOwnerInputJSONAndOnlineTestPadNeverExecute(t *testing.T) {
	content := []byte("  {\"title\":\"fixture\",\"questions\":[]}\n")
	raw, err := parseOwnerInput("tests.json", content, "json", nil)
	if err != nil || !bytes.Equal(raw, content) {
		t.Fatalf("native JSON bytes altered: %s %v", raw, err)
	}
	js := []byte("\xef\xbb\xbfvar test = {\"Name\":\"brace } and quote \\\"\",\"Questions\":[]};\nthrow new Error('must never execute');")
	raw, err = parseOwnerInput("tests.json", js, "js", map[string]string{"title": "fixture", "timeLimitMinutes": "20", "visible": "false", "organization_id": "999"})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Format   string         `json:"importFormat"`
		Source   string         `json:"sourceText"`
		Metadata map[string]any `json:"metadata"`
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Format != "online_test_pad" || out.Source != string(js) || out.Metadata["timeLimitMinutes"] != float64(20) || out.Metadata["visible"] != false || out.Metadata["organization_id"] != nil {
		t.Fatalf("source/settings lost: %s", raw)
	}
	for _, bad := range []string{`var test={"Questions":function(){return []}};`, `var test={"Name":"unterminated}`, `let test={};`, `var test = `} {
		if _, err := parseOwnerInput("tests.json", []byte(bad), "js", nil); err == nil {
			t.Fatalf("invalid source accepted: %s", bad)
		}
	}
}

func TestOwnerInputSyntheticSpecimens(t *testing.T) {
	for _, test := range []struct {
		path, kind string
		rows       int
	}{
		{"users/legacy-success.xlsx", "users.legacy", 2},
		{"external-results/success.xlsx", "tests.external-results", 2},
		{"external-results/header-row-10.xlsx", "tests.external-results", 1},
	} {
		t.Run(test.path, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("..", "..", "fixtures", filepath.FromSlash(test.path)))
			if err != nil {
				t.Fatal(err)
			}
			raw, err := parseOwnerInput(test.kind, b, "xlsx", nil)
			if err != nil {
				t.Fatal(err)
			}
			var out struct {
				Rows []json.RawMessage `json:"rows"`
			}
			if json.Unmarshal(raw, &out) != nil || len(out.Rows) != test.rows {
				t.Fatalf("wrong row count: %s", raw)
			}
		})
	}
}

func TestOwnerInputRawRowLimitsAndExpansion(t *testing.T) {
	var rows strings.Builder
	rows.WriteString(`<row r="1">` + sourceTestCell("A1", "ФИО") + sourceTestCell("B1", "Процент") + `</row>`)
	for n := 2; n <= 1002; n++ {
		rows.WriteString(fmt.Sprintf(`<row r="%d">`, n) + sourceTestCell(fmt.Sprintf("A%d", n), fmt.Sprintf("Фикстуров Ученик%d", n)) + fmt.Sprintf(`<c r="B%d"><v>75</v></c></row>`, n))
	}
	raw, err := parseOwnerInput("tests.external-results", sourceTestArchive(t, rows.String(), nil), "xlsx", nil)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Rows []externalSourceRow `json:"rows"`
	}
	if json.Unmarshal(raw, &out) != nil || len(out.Rows) != 1001 {
		t.Fatal("external syntactic cap must not be the old1000 cap")
	}
	users := strings.ReplaceAll(rows.String(), ">Процент<", ">Класс<")
	_, err = parseOwnerInput("users.legacy", sourceTestArchive(t, users, nil), "xlsx", nil)
	sourceTestError(t, err, "row_limit_exceeded")
	for _, test := range []struct{ path, kind, error string }{
		{"external-results/5001-rows.xlsx", "tests.external-results", "row_limit_exceeded"},
		{"security/expanded-limit.xlsx", "users.legacy", "decompression_limit"},
	} {
		t.Run(test.path, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("..", "..", "fixtures", filepath.FromSlash(test.path)))
			if err != nil {
				t.Fatal(err)
			}
			_, err = parseOwnerInput(test.kind, b, "xlsx", nil)
			sourceTestError(t, err, test.error)
		})
	}
}
