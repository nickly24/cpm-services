package uploads

// Source parsing has no domain access: it extracts workbook cells into owner
// DTOs. Core/Tests validate names, memberships, catalogs, scores and permissions.
import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const ownerInputMaxRows = 5000

type legacySourceRow struct {
	Row         int    `json:"row"`
	FullName    string `json:"full_name"`
	ClassRaw    string `json:"class_raw"`
	SchoolName  string `json:"school_name"`
	ProctorName string `json:"proctor_name"`
	TGName      string `json:"tg_name"`
}

type externalSourceRow struct {
	RowID           string   `json:"row_id"`
	Row             int      `json:"row"`
	FullName        string   `json:"full_name"`
	AccountID       *int64   `json:"account_id,omitempty"`
	LegacyStudentID *int64   `json:"legacy_student_id,omitempty"`
	Percent         *float64 `json:"percent"`
	CorrectCount    *int64   `json:"correct_count,omitempty"`
	CompletedAt     string   `json:"completed_at,omitempty"`
	TimeSpent       string   `json:"time_spent,omitempty"`
	Login           string   `json:"login,omitempty"`
}

type sourceText struct {
	Text string `xml:"t"`
	Runs []struct {
		Text string `xml:"t"`
	} `xml:"r"`
}

func (t sourceText) value() string {
	var b strings.Builder
	b.WriteString(t.Text)
	for _, run := range t.Runs {
		b.WriteString(run.Text)
	}
	return b.String()
}

type sourceCell struct {
	Ref     string     `xml:"r,attr"`
	Type    string     `xml:"t,attr"`
	Style   int        `xml:"s,attr"`
	Value   string     `xml:"v"`
	Inline  sourceText `xml:"is"`
	Formula *string    `xml:"f"`
}
type sourceRow struct {
	Number int          `xml:"r,attr"`
	Cells  []sourceCell `xml:"c"`
}
type sourceValue struct{ text, kind string }
type sourceRecord struct {
	number int
	cells  map[int]sourceValue
}

func sourceXML(files map[string]*zip.File, name string, target any, optional bool) error {
	f := files[name]
	if f == nil {
		if optional {
			return nil
		}
		return api(400, "invalid_xlsx_structure")
	}
	r, err := f.Open()
	if err != nil {
		return api(400, "invalid_xlsx")
	}
	defer r.Close()
	b, err := io.ReadAll(io.LimitReader(r, (32<<20)+1))
	if err != nil || len(b) > 32<<20 {
		return api(413, "decompression_limit")
	}
	if xml.Unmarshal(b, target) != nil {
		return api(400, "invalid_xlsx_xml")
	}
	return nil
}

// Excel stores date values as numeric serials and a cell style. Only date
// metadata is converted; a percent number format never scales the input value.
func sourceDateFormat(code string) bool {
	var b strings.Builder
	quoted, bracket, escaped := false, false, false
	for _, r := range strings.ToLower(code) {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if r == '"' {
			quoted = !quoted
			continue
		}
		if quoted {
			continue
		}
		if r == '[' {
			bracket = true
			continue
		}
		if r == ']' {
			bracket = false
			continue
		}
		if !bracket {
			b.WriteRune(r)
		}
	}
	return strings.ContainsAny(b.String(), "ydhms")
}
func sourceDate(raw string, date1904 bool) (string, error) {
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsInf(n, 0) || math.IsNaN(n) || n < 0 || n > 2958465 {
		return "", api(422, "invalid_excel_date")
	}
	days := int64(math.Floor(n))
	base := time.Date(1899, 12, 31, 0, 0, 0, 0, time.UTC)
	if date1904 {
		base = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	} else if days >= 60 {
		days--
	}
	milliseconds := int64(math.Round((n - math.Floor(n)) * 86400000))
	return base.AddDate(0, 0, int(days)).Add(time.Duration(milliseconds) * time.Millisecond).Format("2006-01-02 15:04:05.999999"), nil
}

var sourceCellReference = regexp.MustCompile(`^([A-Za-z]+)([1-9][0-9]*)$`)

func sourcePosition(ref string) (int, int, error) {
	parts := sourceCellReference.FindStringSubmatch(ref)
	if parts == nil {
		return 0, 0, api(400, "invalid_xlsx_cell_reference")
	}
	column := 0
	for _, c := range strings.ToUpper(parts[1]) {
		column = column*26 + int(c-'A'+1)
		if column > 64 {
			return 0, 0, api(413, "column_limit_exceeded")
		}
	}
	row, err := strconv.Atoi(parts[2])
	if err != nil || row > 1048576 {
		return 0, 0, api(400, "invalid_xlsx_cell_reference")
	}
	return column - 1, row, nil
}

func sourceWorkbook(content []byte, externalIDs bool, table ...bool) ([]sourceRecord, error) {
	allowFormatted := len(table) > 0 && table[0]
	maxRows := ownerInputMaxRows
	if allowFormatted {
		maxRows = 0 // Question tables have no row-count ceiling.
	}
	if err := validFile(content, "xlsx", maxRows, allowFormatted); err != nil {
		return nil, err
	}
	z, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return nil, api(400, "invalid_xlsx")
	}
	files := map[string]*zip.File{}
	for _, entry := range z.File {
		files[entry.Name] = entry
	}
	var book struct {
		Properties struct {
			Date1904 string `xml:"date1904,attr"`
		} `xml:"workbookPr"`
		Views []struct {
			ActiveTab int `xml:"activeTab,attr"`
		} `xml:"bookViews>workbookView"`
		Sheets []struct {
			ID string `xml:"id,attr"`
		} `xml:"sheets>sheet"`
	}
	if err = sourceXML(files, "xl/workbook.xml", &book, false); err != nil {
		return nil, err
	}
	active := 0
	if len(book.Views) > 0 {
		active = book.Views[0].ActiveTab
	}
	if active < 0 || active >= len(book.Sheets) {
		return nil, api(400, "invalid_xlsx_active_sheet")
	}
	var rels struct {
		Items []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
			Type   string `xml:"Type,attr"`
			Mode   string `xml:"TargetMode,attr"`
		} `xml:"Relationship"`
	}
	if err = sourceXML(files, "xl/_rels/workbook.xml.rels", &rels, false); err != nil {
		return nil, err
	}
	sheetPath := ""
	for _, rel := range rels.Items {
		if rel.ID != book.Sheets[active].ID {
			continue
		}
		if sheetPath != "" || rel.Mode != "" && rel.Mode != "Internal" || !strings.HasSuffix(rel.Type, "/worksheet") || strings.ContainsAny(rel.Target, "\\?#\x00") {
			return nil, api(400, "invalid_xlsx_relationship")
		}
		if strings.HasPrefix(rel.Target, "/") {
			sheetPath = strings.TrimPrefix(path.Clean(rel.Target), "/")
		} else {
			sheetPath = path.Clean(path.Join("xl", rel.Target))
		}
		if !strings.HasPrefix(sheetPath, "xl/worksheets/") || !strings.HasSuffix(sheetPath, ".xml") {
			return nil, api(400, "invalid_xlsx_relationship")
		}
	}
	if sheetPath == "" {
		return nil, api(400, "invalid_xlsx_relationship")
	}
	var shared struct {
		Items []sourceText `xml:"si"`
	}
	if err = sourceXML(files, "xl/sharedStrings.xml", &shared, true); err != nil {
		return nil, err
	}
	var styles struct {
		Formats []struct {
			ID   int    `xml:"numFmtId,attr"`
			Code string `xml:"formatCode,attr"`
		} `xml:"numFmts>numFmt"`
		Cells []struct {
			ID int `xml:"numFmtId,attr"`
		} `xml:"cellXfs>xf"`
	}
	if err = sourceXML(files, "xl/styles.xml", &styles, true); err != nil {
		return nil, err
	}
	dateIDs := map[int]bool{}
	for n := 14; n <= 22; n++ {
		dateIDs[n] = true
	}
	for n := 45; n <= 47; n++ {
		dateIDs[n] = true
	}
	for _, format := range styles.Formats {
		dateIDs[format.ID] = sourceDateFormat(format.Code)
	}
	dateStyles := map[int]bool{}
	for index, style := range styles.Cells {
		dateStyles[index] = dateIDs[style.ID]
	}
	var sheet struct {
		Rows []sourceRow `xml:"sheetData>row"`
	}
	if err = sourceXML(files, sheetPath, &sheet, false); err != nil {
		return nil, err
	}
	records := make([]sourceRecord, 0, len(sheet.Rows))
	idColumns := map[int]string{}
	headerFound := false
	last := 0
	for _, row := range sheet.Rows {
		number := row.Number
		if number == 0 {
			number = last + 1
		}
		if number <= last || number > 1048576 {
			return nil, api(400, "invalid_xlsx_row_number")
		}
		last = number
		record := sourceRecord{number: number, cells: map[int]sourceValue{}}
		previousCol := -1
		for _, cell := range row.Cells {
			if allowFormatted && cell.Formula == nil && cell.Value == "" && cell.Inline.value() == "" {
				continue
			}
			column := previousCol + 1
			if cell.Ref != "" {
				var cellRow int
				column, cellRow, err = sourcePosition(cell.Ref)
				if err != nil {
					return nil, err
				}
				if cellRow != number {
					return nil, api(400, "invalid_xlsx_cell_reference")
				}
			}
			if column > 63 || column <= previousCol {
				return nil, api(400, "invalid_xlsx_cell_order")
			}
			previousCol = column
			if cell.Formula != nil {
				return nil, api(400, "formulas_forbidden")
			}
			value := sourceValue{kind: cell.Type, text: cell.Value}
			switch cell.Type {
			case "inlineStr":
				value.text, value.kind = cell.Inline.value(), "text"
			case "s":
				index, parseErr := strconv.Atoi(cell.Value)
				if parseErr != nil || index < 0 || index >= len(shared.Items) {
					return nil, api(400, "invalid_xlsx_shared_string")
				}
				value.text, value.kind = shared.Items[index].value(), "text"
			case "str":
				value.kind = "text"
			case "", "n":
				if cell.Value == "" {
					value.kind = "empty"
					break
				}
				if idColumns[column] != "" {
					// Preserve the XML integer lexeme without any float conversion.
					// ID parsing reports malformed values against this physical row.
					value.kind = "number"
					if dateStyles[cell.Style] {
						value.kind = "date"
					}
					break
				}
				n, parseErr := strconv.ParseFloat(cell.Value, 64)
				if parseErr != nil || math.IsNaN(n) || math.IsInf(n, 0) {
					return nil, api(400, "invalid_xlsx_number")
				}
				value.kind = "number"
				if dateStyles[cell.Style] {
					value.text, err = sourceDate(cell.Value, book.Properties.Date1904 == "1" || book.Properties.Date1904 == "true")
					if err != nil {
						return nil, err
					}
					value.kind = "date"
				}
			case "d":
				if idColumns[column] != "" {
					value.kind = "date"
					break
				}
				stamp, parseErr := time.Parse(time.RFC3339, cell.Value)
				if parseErr != nil {
					stamp, parseErr = time.Parse("2006-01-02T15:04:05", cell.Value)
				}
				if parseErr != nil {
					stamp, parseErr = time.Parse("2006-01-02", cell.Value)
				}
				if parseErr != nil {
					return nil, api(422, "invalid_excel_date")
				}
				value.text, value.kind = stamp.Format("2006-01-02 15:04:05.999999"), "date"
			case "b", "e": // Do not turn booleans/errors into a numeric score.
			default:
				if field := idColumns[column]; field != "" {
					return nil, api(422, fmt.Sprintf("invalid_source_cell_%d_%s", number, field))
				}
				return nil, api(400, "unsupported_xlsx_cell_type")
			}
			record.cells[column] = value
		}
		if externalIDs && !headerFound && number <= 10 {
			mapping := sourceHeaders(record, externalSourceAliases)
			_, hasName := mapping["full_name"]
			_, hasPercent := mapping["percent"]
			if hasName && hasPercent {
				mapping, err = sourceExternalHeaders(record)
				if err != nil {
					return nil, err
				}
				for _, field := range []string{"account_id", "legacy_student_id"} {
					if column, exists := mapping[field]; exists {
						idColumns[column] = field
					}
				}
				headerFound = true
			}
		}
		records = append(records, record)
	}
	return records, nil
}

var legacySourceAliases = map[string][]string{
	"full_name": {"фио", "фио ученика", "ученик", "студент", "full_name", "имя", "ф.и.о", "ф.и.о."},
	"class_raw": {"класс", "class"}, "school_name": {"школа", "school"},
	"proctor_name": {"проктор", "proctor", "куратор"}, "tg_name": {"telegram", "телеграм", "tg", "tg_name", "ник telegram"},
}
var externalSourceAliases = map[string][]string{
	"source_number": {"№", "номер", "id", "номер попытки"}, "platform_user": {"пользователь", "user", "username"}, "ip": {"ip", "ip адрес", "ip-адрес"},
	"completed_at": {"дата завершения", "завершено", "completed_at", "completed"}, "time_spent": {"потрачено времени", "время", "time_spent", "duration"},
	"login": {"логин", "login"}, "full_name": {"фио", "ф.и.о", "ф.и.о.", "ученик", "студент", "full_name"},
	"correct_count": {"количество правильных ответов", "правильных ответов", "верных ответов"},
	"percent":       {"процент правильных ответов (%)", "процент правильных ответов", "процент", "rate", "score"},
	// Additional aliases require the Tests owner's explicit contract agreement.
	// Legacy generic "id" remains source_number, never an account/profile ID.
	"account_id": {"account_id"}, "legacy_student_id": {"legacy_student_id"},
}

func sourceHeader(text string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(text)), "ё", "е")), " ")
}
func sourceHeaders(row sourceRecord, aliases map[string][]string) map[string]int {
	mapping := map[string]int{}
	for field, names := range aliases {
		for column := 0; column < 64; column++ {
			cell := row.cells[column]
			if cell.kind != "text" {
				continue
			}
			normalized := sourceHeader(cell.text)
			for _, name := range names {
				if normalized == name {
					mapping[field] = column
					break
				}
			}
			if _, found := mapping[field]; found {
				break
			}
		}
	}
	return mapping
}

// Only explicit owner ID columns are strict. Legacy non-ID aliases retain their
// existing first-match behavior, but a duplicate ID header cannot fall back to
// name/login matching or to a later header row.
func sourceExternalHeaders(row sourceRecord) (map[string]int, error) {
	mapping := sourceHeaders(row, externalSourceAliases)
	seen := map[string]bool{}
	for column := 0; column < 64; column++ {
		cell := row.cells[column]
		if cell.kind != "text" {
			continue
		}
		normalized := sourceHeader(cell.text)
		matched := ""
		for _, field := range []string{"account_id", "legacy_student_id"} {
			for _, alias := range externalSourceAliases[field] {
				if normalized != sourceHeader(alias) {
					continue
				}
				if matched != "" && matched != field {
					return nil, api(422, fmt.Sprintf("conflicting_source_header_%d_%s_%s", row.number, matched, field))
				}
				matched = field
				break
			}
		}
		if matched == "" {
			continue
		}
		if seen[matched] {
			return nil, api(422, fmt.Sprintf("duplicate_source_header_%d_%s", row.number, matched))
		}
		seen[matched], mapping[matched] = true, column
	}
	return mapping, nil
}

func sourcePositiveID(row sourceRecord, fields map[string]int, field string) (*int64, error) {
	column, exists := fields[field]
	if !exists {
		return nil, nil
	}
	value := row.cells[column]
	raw := strings.TrimSpace(value.text)
	invalid := func() (*int64, error) {
		return nil, api(422, fmt.Sprintf("invalid_source_cell_%d_%s", row.number, field))
	}
	switch value.kind {
	case "", "empty", "text", "number":
		if raw == "" {
			return nil, nil
		}
	default:
		return invalid()
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return invalid()
		}
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 {
		return invalid()
	}
	if value.kind == "number" && id > (1<<53)-1 {
		return nil, api(422, fmt.Sprintf("invalid_source_cell_%d_%s_use_text", row.number, field))
	}
	return &id, nil
}

func sourceBlank(row sourceRecord) bool {
	for _, cell := range row.cells {
		if strings.TrimSpace(cell.text) != "" {
			return false
		}
	}
	return true
}
func sourceString(row sourceRecord, fields map[string]int, field string, allowDate bool) (string, error) {
	index, exists := fields[field]
	if !exists {
		return "", nil
	}
	value := row.cells[index]
	if value.kind == "b" || value.kind == "e" || value.kind == "date" && !allowDate {
		return "", api(422, fmt.Sprintf("invalid_source_cell_%d_%s", row.number, field))
	}
	return strings.TrimSpace(value.text), nil
}
func sourceNumber(row sourceRecord, fields map[string]int, field string) (*float64, error) {
	raw, err := sourceString(row, fields, field, false)
	if err != nil || raw == "" {
		return nil, err
	}
	raw = strings.ReplaceAll(strings.ReplaceAll(raw, "%", ""), ",", ".")
	number, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return nil, nil
	} // Preserve blank/unparseable as null for owner preview.
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return nil, api(422, fmt.Sprintf("invalid_source_cell_%d_%s", row.number, field))
	}
	return &number, nil
}
func sourceRowUUID(content []byte, row int) string {
	return sourceRowUUIDHash(sha256.Sum256(content), row)
}
func sourceRowUUIDHash(sourceDigest [32]byte, row int) string {
	hash := sha256.New()
	hash.Write([]byte("cpm.uploads.source-row.v1\x00"))
	hash.Write(sourceDigest[:])
	hash.Write([]byte(fmt.Sprintf("\x00%d", row)))
	b := hash.Sum(nil)[:16]
	b[6], b[8] = b[6]&0x0f|0x80, b[8]&0x3f|0x80
	raw := hex.EncodeToString(b)
	return raw[:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:]
}

// parseOwnerInput returns only raw source fields. Scope, target IDs, current
// revisions and owner authorization are added by the coordinator, never cells.
func parseOwnerInput(kind string, content []byte, extension string, fields map[string]string) (json.RawMessage, error) {
	switch kind {
	case "tests.json":
		if extension == "xlsx" {
			return questionTableInput(content, fields)
		}
		if len(content) == 0 {
			return nil, api(400, "empty_file")
		}
		if len(content) > maxFile {
			return nil, api(413, "file_too_large")
		}
		if !utf8.Valid(content) {
			return nil, api(400, "file_utf8_required")
		}
		if extension == "json" {
			if !json.Valid(content) {
				return nil, api(400, "invalid_json")
			}
			return append(json.RawMessage(nil), content...), nil
		}
		if extension != "js" {
			return nil, api(415, "unsupported_file_format")
		}
		if _, err := onlineTestPadObject(content); err != nil {
			return nil, err
		}
		metadata, err := questionImportMetadata(fields)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"importFormat": "online_test_pad", "sourceText": string(content), "metadata": metadata})
	case "users.legacy", "tests.external-results":
		if extension != "xlsx" {
			return nil, api(415, "unsupported_file_format")
		}
		records, err := sourceWorkbook(content, kind == "tests.external-results")
		if err != nil {
			return nil, err
		}
		if len(records) == 0 {
			return nil, api(400, "empty_workbook")
		}
		headerIndex := -1
		var columns map[string]int
		if kind == "users.legacy" {
			if records[0].number != 1 {
				return nil, api(400, "missing_source_headers")
			}
			columns = sourceHeaders(records[0], legacySourceAliases)
			_, hasName := columns["full_name"]
			_, hasClass := columns["class_raw"]
			if hasName && hasClass {
				headerIndex = 0
			}
		} else {
			for index, row := range records {
				if row.number > 10 {
					break
				}
				mapping := sourceHeaders(row, externalSourceAliases)
				_, hasName := mapping["full_name"]
				_, hasPercent := mapping["percent"]
				if hasName && hasPercent {
					columns, err = sourceExternalHeaders(row)
					if err != nil {
						return nil, err
					}
					headerIndex = index
					break
				}
			}
		}
		if headerIndex < 0 {
			return nil, api(400, "missing_source_headers")
		}
		legacyRows := []legacySourceRow{}
		externalRows := []externalSourceRow{}
		count := 0
		sourceDigest := sha256.Sum256(content)
		maxRows := ownerInputMaxRows
		if kind == "users.legacy" {
			maxRows = 1000
		}
		for _, row := range records[headerIndex+1:] {
			if sourceBlank(row) {
				if kind == "tests.external-results" {
					for _, field := range []string{"account_id", "legacy_student_id"} {
						if _, err := sourcePositiveID(row, columns, field); err != nil {
							return nil, err
						}
					}
				}
				continue
			}
			count++
			if count > maxRows {
				return nil, api(413, "row_limit_exceeded")
			}
			name, err := sourceString(row, columns, "full_name", false)
			if err != nil {
				return nil, err
			}
			if kind == "users.legacy" {
				out := legacySourceRow{Row: row.number, FullName: name}
				for key, value := range map[string]*string{"class_raw": &out.ClassRaw, "school_name": &out.SchoolName, "proctor_name": &out.ProctorName, "tg_name": &out.TGName} {
					*value, err = sourceString(row, columns, key, false)
					if err != nil {
						return nil, err
					}
				}
				legacyRows = append(legacyRows, out)
			} else {
				out := externalSourceRow{RowID: sourceRowUUIDHash(sourceDigest, row.number), Row: row.number, FullName: name}
				out.AccountID, err = sourcePositiveID(row, columns, "account_id")
				if err != nil {
					return nil, err
				}
				out.LegacyStudentID, err = sourcePositiveID(row, columns, "legacy_student_id")
				if err != nil {
					return nil, err
				}
				out.Percent, err = sourceNumber(row, columns, "percent")
				if err != nil {
					return nil, err
				}
				correct, err := sourceNumber(row, columns, "correct_count")
				if err != nil {
					return nil, err
				}
				if correct != nil {
					if math.Trunc(*correct) != *correct || *correct < -9223372036854775808 || *correct >= 9223372036854775808 {
						return nil, api(422, fmt.Sprintf("invalid_source_cell_%d_correct_count", row.number))
					}
					n := int64(*correct)
					out.CorrectCount = &n
				}
				for key, value := range map[string]*string{"completed_at": &out.CompletedAt, "time_spent": &out.TimeSpent, "login": &out.Login} {
					*value, err = sourceString(row, columns, key, key == "completed_at")
					if err != nil {
						return nil, err
					}
				}
				externalRows = append(externalRows, out)
			}
		}
		if count == 0 {
			return nil, api(400, "source_has_no_data")
		}
		if kind == "users.legacy" {
			return json.Marshal(struct {
				Rows []legacySourceRow `json:"rows"`
			}{legacyRows})
		}
		return json.Marshal(struct {
			Rows []externalSourceRow `json:"rows"`
		}{externalRows})
	default:
		return nil, api(400, "source_adapter_unsupported")
	}
}

var onlineTestPadMarker = regexp.MustCompile(`\bvar\s+test\s*=`)

// Extract the legacy var test JSON object without evaluating JavaScript. Only
// the object is inspected. Original source is forwarded to the Tests owner.
func onlineTestPadObject(content []byte) (json.RawMessage, error) {
	source := bytes.TrimPrefix(content, []byte{0xef, 0xbb, 0xbf})
	marker := onlineTestPadMarker.FindIndex(source)
	if marker == nil {
		return nil, api(400, "online_test_pad_object_missing")
	}
	startOffset := bytes.IndexByte(source[marker[1]:], '{')
	if startOffset < 0 {
		return nil, api(400, "online_test_pad_object_missing")
	}
	start := marker[1] + startOffset
	depth, quoted, escaped := 0, false, false
	for index := start; index < len(source); index++ {
		c := source[index]
		if quoted {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		if c == '"' {
			quoted = true
			continue
		}
		if c == '{' {
			depth++
		}
		if c == '}' {
			depth--
			if depth == 0 {
				object := source[start : index+1]
				if !json.Valid(object) {
					return nil, api(400, "invalid_online_test_pad_json")
				}
				return append(json.RawMessage(nil), object...), nil
			}
		}
	}
	return nil, api(400, "invalid_online_test_pad_json")
}
