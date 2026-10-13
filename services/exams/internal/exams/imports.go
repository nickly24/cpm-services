package exams

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math/big"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const maxImportFile = 10 * 1024 * 1024
const maxImportExpanded = 64 * 1024 * 1024
const maxImportPreview = 16 * 1024 * 1024

type ImportRow struct {
	RowID                      string          `json:"row_id"`
	SourceRow                  int             `json:"source_row"`
	Excluded                   bool            `json:"excluded"`
	Input                      json.RawMessage `json:"input"`
	Errors                     []string        `json:"errors"`
	PrivilegeBaselineVersion   *int64          `json:"privilege_baseline_version,omitempty"`
	PrivilegeBaselineAccountID int64           `json:"privilege_baseline_account_id,omitempty"`
}

func importKind(kind string) bool {
	return kind == "questions" || kind == "assignments" || kind == "outside"
}
func importHeader(value any, kind string) (string, error) {
	text, ok := value.(string)
	if !ok {
		return "", Bad("invalid_import_headers")
	}
	text = strings.ToLower(strings.TrimSpace(text))
	aliases := map[string]string{}
	switch kind {
	case "questions":
		aliases = map[string]string{"часть": "partCode", "part_code": "partCode", "вопрос": "questionText", "question_text": "questionText", "эталонный ответ": "answerText", "answer_text": "answerText"}
	case "outside":
		aliases = map[string]string{"student_id": "studentId", "student_login": "studentLogin", "points": "points", "grade": "grade", "examinator": "examinator"}
	case "assignments":
		aliases = map[string]string{"student_id": "studentId", "student_login": "studentLogin", "replacement_limit": "replacementLimit"}
		for n := 1; n <= 6; n++ {
			aliases[fmt.Sprintf("examinator_%d_id", n)] = fmt.Sprintf("examinator%dId", n)
			for _, h := range []string{fmt.Sprintf("examinator_%d", n), fmt.Sprintf("examinator_%d_login", n), fmt.Sprintf("экзаменатор %d", n)} {
				aliases[h] = fmt.Sprintf("examinator%dLogin", n)
			}
		}
	}
	name := aliases[text]
	if name == "" {
		return "", Bad("unknown_import_header")
	}
	return name, nil
}

type xlsxText struct {
	Text string `xml:"t"`
	Runs []struct {
		Text string `xml:"t"`
	} `xml:"r"`
}

func (s xlsxText) value() string {
	if len(s.Runs) == 0 {
		return s.Text
	}
	var b strings.Builder
	b.WriteString(s.Text)
	for _, r := range s.Runs {
		b.WriteString(r.Text)
	}
	return b.String()
}

type xlsxCell struct {
	Ref     string   `xml:"r,attr"`
	Type    string   `xml:"t,attr"`
	Style   int      `xml:"s,attr"`
	Value   string   `xml:"v"`
	Formula *string  `xml:"f"`
	Inline  xlsxText `xml:"is"`
}
type xlsxRow struct {
	Number int        `xml:"r,attr"`
	Cells  []xlsxCell `xml:"c"`
}
type xlsxSheet struct {
	Dimension struct {
		Ref string `xml:"ref,attr"`
	} `xml:"dimension"`
	Rows []xlsxRow `xml:"sheetData>row"`
}
type importSourceRow struct {
	number int
	values []any
}

var xlsxCellRef = regexp.MustCompile(`^([A-Za-z]+)([1-9][0-9]*)$`)

func xlsxPosition(ref string) (int, int, error) {
	m := xlsxCellRef.FindStringSubmatch(ref)
	if m == nil {
		return 0, 0, Bad("invalid_import_file")
	}
	col := 0
	for _, c := range strings.ToUpper(m[1]) {
		col = col*26 + int(c-'A'+1)
		if col > 32 {
			return 0, 0, Bad("import_columns_limit")
		}
	}
	row, err := strconv.Atoi(m[2])
	if err != nil || row > 100000 {
		return 0, 0, Bad("import_rows_limit")
	}
	return col, row, nil
}
func invalidExcel(kind, value string) any {
	return map[string]any{"invalidExcelCell": kind, "value": value}
}
func xlsxValue(c xlsxCell, shared []string, dates map[int]bool) (any, error) {
	if c.Formula != nil {
		return invalidExcel("f", *c.Formula), nil
	}
	switch c.Type {
	case "inlineStr":
		return c.Inline.value(), nil
	case "s":
		index, err := strconv.Atoi(c.Value)
		if err != nil || index < 0 || index >= len(shared) {
			return nil, Bad("invalid_import_file")
		}
		return shared[index], nil
	case "b", "e", "d":
		return invalidExcel(c.Type, c.Value), nil
	case "str":
		return c.Value, nil
	case "", "n":
		if c.Value == "" {
			return nil, nil
		}
		if dates[c.Style] {
			return invalidExcel("d", c.Value), nil
		}
		var number json.Number
		if err := json.Unmarshal([]byte(c.Value), &number); err != nil {
			return nil, Bad("invalid_import_file")
		}
		if _, err := number.Float64(); err != nil {
			return nil, Bad("invalid_import_file")
		}
		if rational, ok := new(big.Rat).SetString(string(number)); ok && rational.IsInt() && rational.Num().IsInt64() {
			return json.Number(rational.Num().String()), nil
		}
		return number, nil
	default:
		return nil, Bad("invalid_import_file")
	}
}
func xlsxDateFormat(code string) bool {
	var cleaned strings.Builder
	quoted, bracket, escape := false, false, false
	for _, r := range strings.ToLower(code) {
		if escape {
			escape = false
			continue
		}
		if r == '\\' {
			escape = true
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
		if bracket {
			continue
		}
		cleaned.WriteRune(r)
	}
	s := cleaned.String()
	return strings.ContainsAny(s, "ydhms")
}

// ParseXLSX is bounded in compressed and expanded size, row count and columns.
// It never follows external relationships or evaluates formulas.
func ParseXLSX(content []byte, filename, kind string) ([]ImportRow, error) {
	if !importKind(kind) {
		return nil, Bad("invalid_import_type")
	}
	if !strings.HasSuffix(strings.ToLower(filename), ".xlsx") {
		return nil, Bad("invalid_import_file")
	}
	if len(content) > maxImportFile {
		return nil, &APIError{413, "payload_too_large"}
	}
	archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return nil, Bad("invalid_import_file")
	}
	if len(archive.File) > 10000 {
		return nil, &APIError{413, "payload_too_large"}
	}
	files := map[string][]byte{}
	total := uint64(0)
	actualTotal := 0
	for _, member := range archive.File {
		if member.Flags&1 != 0 {
			return nil, Bad("invalid_import_file")
		}
		if _, exists := files[member.Name]; exists {
			return nil, Bad("invalid_import_file")
		}
		total += member.UncompressedSize64
		if total > maxImportExpanded {
			return nil, &APIError{413, "payload_too_large"}
		}
		stream, err := member.Open()
		if err != nil {
			return nil, Bad("invalid_import_file")
		}
		data, readErr := io.ReadAll(io.LimitReader(stream, maxImportExpanded+1))
		closeErr := stream.Close()
		if readErr != nil || closeErr != nil {
			return nil, Bad("invalid_import_file")
		}
		actualTotal += len(data)
		if actualTotal > maxImportExpanded {
			return nil, &APIError{413, "payload_too_large"}
		}
		if strings.HasSuffix(strings.ToLower(member.Name), ".xml") || strings.HasSuffix(strings.ToLower(member.Name), ".rels") {
			scan := bytes.ToUpper(bytes.ReplaceAll(data, []byte{0}, nil))
			if bytes.Contains(scan, []byte("<!DOCTYPE")) || bytes.Contains(scan, []byte("<!ENTITY")) {
				return nil, Bad("invalid_import_file")
			}
		}
		files[member.Name] = data
	}
	unmarshal := func(name string, out any) error {
		data, ok := files[name]
		if !ok {
			return Bad("invalid_import_file")
		}
		if err := xml.Unmarshal(data, out); err != nil {
			return Bad("invalid_import_file")
		}
		return nil
	}
	var workbook struct {
		Sheets []struct {
			ID string `xml:"id,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := unmarshal("xl/workbook.xml", &workbook); err != nil {
		return nil, err
	}
	var relationships struct {
		Items []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
			Mode   string `xml:"TargetMode,attr"`
			Type   string `xml:"Type,attr"`
		} `xml:"Relationship"`
	}
	if err := unmarshal("xl/_rels/workbook.xml.rels", &relationships); err != nil {
		return nil, err
	}
	targets := map[string]string{}
	for _, rel := range relationships.Items {
		if rel.Mode == "External" {
			continue
		}
		target := path.Clean(path.Join("xl", rel.Target))
		if strings.HasPrefix(rel.Target, "/") {
			target = strings.TrimPrefix(path.Clean(rel.Target), "/")
		}
		if strings.Contains(target, "..") || !strings.HasPrefix(target, "xl/worksheets/") {
			continue
		}
		targets[rel.ID] = target
	}
	shared := []string{}
	if data, ok := files["xl/sharedStrings.xml"]; ok {
		var table struct {
			Items []xlsxText `xml:"si"`
		}
		if err := xml.Unmarshal(data, &table); err != nil {
			return nil, Bad("invalid_import_file")
		}
		for _, item := range table.Items {
			shared = append(shared, item.value())
		}
	}
	dates := map[int]bool{}
	if data, ok := files["xl/styles.xml"]; ok {
		var styles struct {
			Formats []struct {
				ID   int    `xml:"numFmtId,attr"`
				Code string `xml:"formatCode,attr"`
			} `xml:"numFmts>numFmt"`
			XFs []struct {
				ID int `xml:"numFmtId,attr"`
			} `xml:"cellXfs>xf"`
		}
		if err := xml.Unmarshal(data, &styles); err != nil {
			return nil, Bad("invalid_import_file")
		}
		formats := map[int]bool{}
		for _, format := range styles.Formats {
			formats[format.ID] = xlsxDateFormat(format.Code)
		}
		for i, xf := range styles.XFs {
			dates[i] = formats[xf.ID] || (xf.ID >= 14 && xf.ID <= 22) || (xf.ID >= 27 && xf.ID <= 36) || (xf.ID >= 45 && xf.ID <= 47) || (xf.ID >= 50 && xf.ID <= 58)
		}
	}
	var dataRows []importSourceRow
	sheetCount := 0
	for _, sheetEntry := range workbook.Sheets {
		target := targets[sheetEntry.ID]
		if target == "" {
			return nil, Bad("invalid_import_file")
		}
		var sheet xlsxSheet
		if err := unmarshal(target, &sheet); err != nil {
			return nil, err
		}
		if sheet.Dimension.Ref != "" {
			pieces := strings.Split(sheet.Dimension.Ref, ":")
			if _, _, err := xlsxPosition(pieces[len(pieces)-1]); err != nil {
				return nil, err
			}
		}
		rows := []importSourceRow{}
		lastRow := 0
		for _, row := range sheet.Rows {
			sourceRow := row.Number
			if sourceRow == 0 {
				sourceRow = lastRow + 1
			}
			if sourceRow > 100000 || sourceRow <= lastRow {
				return nil, Bad("import_rows_limit")
			}
			lastRow = sourceRow
			values := []any{}
			seen := map[int]bool{}
			nextCol := 1
			for _, cell := range row.Cells {
				col := nextCol
				if cell.Ref != "" {
					var cellRow int
					var err error
					col, cellRow, err = xlsxPosition(cell.Ref)
					if err != nil {
						return nil, err
					}
					if cellRow != sourceRow {
						return nil, Bad("invalid_import_file")
					}
				}
				nextCol = col + 1
				if col > 32 {
					return nil, Bad("import_columns_limit")
				}
				if seen[col] {
					return nil, Bad("invalid_import_file")
				}
				seen[col] = true
				for len(values) < col {
					values = append(values, nil)
				}
				v, err := xlsxValue(cell, shared, dates)
				if err != nil {
					return nil, err
				}
				values[col-1] = v
			}
			nonempty := false
			for _, v := range values {
				if v != nil && v != "" {
					nonempty = true
					break
				}
			}
			if !nonempty {
				continue
			}
			rows = append(rows, importSourceRow{sourceRow, values})
			if len(rows) > 5001 {
				return nil, Bad("import_rows_limit")
			}
		}
		if len(rows) > 0 {
			sheetCount++
			dataRows = rows
		}
		if sheetCount > 1 {
			return nil, Bad("multiple_import_sheets")
		}
	}
	if len(dataRows) < 2 {
		return nil, &APIError{422, "import_no_rows"}
	}
	if dataRows[0].number != 1 {
		return nil, Bad("invalid_import_headers")
	}
	rawHeaders := dataRows[0].values
	for len(rawHeaders) > 0 && rawHeaders[len(rawHeaders)-1] == nil {
		rawHeaders = rawHeaders[:len(rawHeaders)-1]
	}
	headers := []string{}
	present := map[string]bool{}
	for _, value := range rawHeaders {
		name, err := importHeader(value, kind)
		if err != nil {
			return nil, err
		}
		if present[name] {
			return nil, Bad("duplicate_import_headers")
		}
		present[name] = true
		headers = append(headers, name)
	}
	required := []string{}
	if kind == "questions" {
		required = []string{"partCode", "questionText", "answerText"}
	} else {
		if !present["studentId"] && !present["studentLogin"] {
			return nil, Bad("missing_import_headers")
		}
		if kind == "outside" {
			required = []string{"points", "grade", "examinator"}
		}
	}
	for _, name := range required {
		if !present[name] {
			return nil, Bad("missing_import_headers")
		}
	}
	rows := make([]ImportRow, 0, len(dataRows)-1)
	for _, source := range dataRows[1:] {
		values := map[string]any{}
		for col, value := range source.values {
			if value == nil || value == "" {
				continue
			}
			if col >= len(headers) {
				return nil, Bad("invalid_import_headers")
			}
			values[headers[col]] = value
		}
		b, _ := json.Marshal(values)
		rows = append(rows, ImportRow{RowID: fmt.Sprintf("row-%d", source.number), SourceRow: source.number, Input: b, Errors: []string{}})
	}
	if err := importPreviewBound(rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func importPreviewBound(rows []ImportRow) error {
	raw, err := json.Marshal(rows)
	if err != nil {
		return Bad("invalid_preview")
	}
	if len(raw) > maxImportPreview {
		return &APIError{413, "payload_too_large"}
	}
	return nil
}

func cloneAuxExam(e *Exam) (*Exam, error) {
	raw, err := encodeExam(e)
	if err != nil {
		return nil, err
	}
	return decodeExam(raw, e.NextID)
}

func importValues(input json.RawMessage) (map[string]any, error) {
	var values map[string]any
	if err := auxDecode(input, &values); err != nil {
		return nil, err
	}
	if values == nil {
		return nil, Bad("invalid_payload")
	}
	return values, nil
}
func importInteger(value any, field string, minimum, maximum int64) (int64, error) {
	n, ok := value.(json.Number)
	if !ok {
		return 0, Bad("invalid_" + field)
	}
	id, err := strconv.ParseInt(string(n), 10, 64)
	if err != nil || id < minimum || id > maximum {
		return 0, Bad("invalid_" + field)
	}
	return id, nil
}
func importText(value any, field string) (string, error) {
	s, ok := value.(string)
	if !ok {
		return "", Bad(field + "_required")
	}
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n"))
	if s == "" {
		return "", Bad(field + "_required")
	}
	return s, nil
}
func ExtractImportReferences(rows []ImportRow) ([]int64, []string, error) {
	idsMap := map[int64]bool{}
	loginsMap := map[string]bool{}
	for _, row := range rows {
		if row.Excluded {
			continue
		}
		values, err := importValues(row.Input)
		if err != nil {
			continue
		}
		for key, value := range values {
			if key == "studentId" || (strings.HasPrefix(key, "examinator") && strings.HasSuffix(key, "Id")) {
				if id, err := importInteger(value, key, 1, 1<<53-1); err == nil {
					idsMap[id] = true
				}
			}
			if key == "studentLogin" || (strings.HasPrefix(key, "examinator") && strings.HasSuffix(key, "Login")) {
				if login, err := importText(value, key); err == nil {
					loginsMap[login] = true
				}
			}
		}
	}
	ids := make([]int64, 0, len(idsMap))
	for id := range idsMap {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	logins := make([]string, 0, len(loginsMap))
	for login := range loginsMap {
		logins = append(logins, login)
	}
	sort.Strings(logins)
	return ids, logins, nil
}
func importPerson(p Params, values map[string]any, idKey, loginKey, role string) (Member, error) {
	var byID, byLogin *Member
	if value, exists := values[idKey]; exists && value != nil && value != "" {
		id, err := importInteger(value, idKey, 1, 1<<53-1)
		if err != nil {
			return Member{}, err
		}
		member, ok := p.Members[id]
		if !ok {
			return Member{}, Bad(role + "_not_found")
		}
		byID = &member
	}
	if value, exists := values[loginKey]; exists && value != nil && value != "" {
		login, err := importText(value, loginKey)
		if err != nil {
			return Member{}, err
		}
		for _, m := range p.Members {
			if m.Login == login {
				if byLogin != nil && byLogin.AccountID != m.AccountID {
					return Member{}, Conflict("ambiguous_login")
				}
				member := m
				byLogin = &member
			}
		}
		if byLogin == nil {
			return Member{}, Bad(role + "_not_found")
		}
	}
	if byID == nil && byLogin == nil {
		return Member{}, Bad(role + "_required")
	}
	if byID != nil && byLogin != nil && byID.AccountID != byLogin.AccountID {
		return Member{}, Bad("person_identity_mismatch")
	}
	member := byID
	if member == nil {
		member = byLogin
	}
	if !member.Active || member.Role != role {
		return Member{}, Bad(role + "_not_found")
	}
	if len(member.DivisionIDs) > 0 {
		allowed := false
		for _, id := range member.DivisionIDs {
			if id == p.DivisionID {
				allowed = true
			}
		}
		if !allowed {
			return Member{}, Denied
		}
	}
	return *member, nil
}
func importAllowed(values map[string]any, kind string) error {
	allowed := map[string]bool{}
	switch kind {
	case "questions":
		for _, k := range []string{"partCode", "questionText", "answerText"} {
			allowed[k] = true
		}
	case "outside":
		for _, k := range []string{"studentId", "studentLogin", "points", "grade", "examinator"} {
			allowed[k] = true
		}
	case "assignments":
		for _, k := range []string{"studentId", "studentLogin", "replacementLimit"} {
			allowed[k] = true
		}
		for n := 1; n <= 6; n++ {
			allowed[fmt.Sprintf("examinator%dId", n)] = true
			allowed[fmt.Sprintf("examinator%dLogin", n)] = true
		}
	}
	for key := range values {
		if !allowed[key] {
			return Bad("unknown_import_field")
		}
	}
	return nil
}
func importKey(e *Exam, p Params, kind string, values map[string]any) (string, error) {
	if err := importAllowed(values, kind); err != nil {
		return "", err
	}
	if kind == "questions" {
		code, err := importText(values["partCode"], "partCode")
		if err != nil {
			return "", err
		}
		code = strings.ToUpper(code)
		if len(code) != 1 || code[0] < 'A' || code[0] > 'Z' {
			return "", Bad("invalid_part_code")
		}
		question, err := importText(values["questionText"], "questionText")
		if err != nil {
			return "", err
		}
		answer, err := importText(values["answerText"], "answerText")
		if err != nil {
			return "", err
		}
		values["partCode"] = code
		values["questionText"] = question
		values["answerText"] = answer
		return code + ":" + TrainingFingerprint(question, answer), nil
	}
	student, err := importPerson(p, values, "studentId", "studentLogin", "student")
	if err != nil {
		return "", err
	}
	return fmt.Sprint(student.AccountID), nil
}
func importCommand(e *Exam, a Actor, op string, p Params, values map[string]any) (any, error) {
	b, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	return command(e, a, op, p, b)
}
func applyImportRow(e *Exam, a Actor, kind string, p Params, row *ImportRow, values map[string]any, counts map[string]int) error {
	if kind == "questions" {
		code := values["partCode"].(string)
		partID := int64(0)
		for _, part := range e.Parts {
			if part.Code == code {
				partID = part.ID
				break
			}
		}
		if partID == 0 {
			if _, err := importCommand(e, a, "part-create", p, map[string]any{"code": code}); err != nil {
				return err
			}
			for _, part := range e.Parts {
				if part.Code == code {
					partID = part.ID
				}
			}
			counts["parts_created"]++
		}
		if _, err := importCommand(e, a, "question-create", p, map[string]any{"part_id": partID, "question_text": values["questionText"], "answer_text": values["answerText"]}); err != nil {
			return err
		}
		counts["questions_created"]++
		return nil
	}
	student, err := importPerson(p, values, "studentId", "studentLogin", "student")
	if err != nil {
		return err
	}
	if kind == "outside" {
		grade, err := importInteger(values["grade"], "grade", 0, 5)
		if err != nil {
			return err
		}
		points, ok := values["points"].(json.Number)
		if !ok {
			return Bad("invalid_points")
		}
		name, err := importText(values["examinator"], "examinator")
		if err != nil {
			return err
		}
		if _, err := importCommand(e, a, "outside-create", p, map[string]any{"student_account_id": student.AccountID, "grade": grade, "points": points, "examinator": name}); err != nil {
			return err
		}
		counts["results_created"]++
		return nil
	}
	members := []int64{}
	seen := map[int64]bool{}
	for n := 1; n <= 6; n++ {
		idKey := fmt.Sprintf("examinator%dId", n)
		loginKey := fmt.Sprintf("examinator%dLogin", n)
		id, hasID := values[idKey]
		login, hasLogin := values[loginKey]
		if (!hasID || id == nil || id == "") && (!hasLogin || login == nil || login == "") {
			continue
		}
		member, err := importPerson(p, values, idKey, loginKey, "examinator")
		if err != nil {
			return err
		}
		if seen[member.AccountID] {
			return Bad("duplicate_examinator")
		}
		seen[member.AccountID] = true
		members = append(members, member.AccountID)
	}
	if len(members) < 1 || len(members) > 6 {
		return Bad("invalid_commission_size")
	}
	sort.Slice(members, func(i, j int) bool { return members[i] < members[j] })
	d, _ := e.Division(p.DivisionID)
	commissionID := int64(0)
	for _, c := range d.Commissions {
		if len(c.Members) != len(members) {
			continue
		}
		matches := true
		for _, m := range c.Members {
			if !seen[m.AccountID] {
				matches = false
			}
		}
		if matches && (commissionID == 0 || c.ID < commissionID) {
			commissionID = c.ID
		}
	}
	if commissionID == 0 {
		if _, err := importCommand(e, a, "commission-create", p, map[string]any{"name": fmt.Sprintf("Комиссия %d", len(d.Commissions)+1), "examinator_ids": members}); err != nil {
			return err
		}
		d, _ = e.Division(p.DivisionID)
		commissionID = d.Commissions[len(d.Commissions)-1].ID
		counts["commissions_created"]++
	}
	if _, err := importCommand(e, a, "assignment-create", p, map[string]any{"student_account_id": student.AccountID, "commission_id": commissionID}); err != nil {
		return err
	}
	counts["assignments_created"]++
	if value, exists := values["replacementLimit"]; exists {
		limit, err := importInteger(value, "replacementLimit", 0, 100)
		if err != nil {
			return err
		}
		d, _ = e.Division(p.DivisionID)
		current := d.Privileges[student.AccountID]
		if row.PrivilegeBaselineVersion != nil && (*row.PrivilegeBaselineVersion != current.Revision || row.PrivilegeBaselineAccountID != student.AccountID) {
			return Conflict("privilege_modified")
		}
		baseline := current.Revision
		row.PrivilegeBaselineVersion = &baseline
		row.PrivilegeBaselineAccountID = student.AccountID
		privParams := p
		privParams.StudentID = student.AccountID
		if _, err := importCommand(e, a, "privilege-put", privParams, map[string]any{"replacement_limit": limit, "expected_revision": current.Revision}); err != nil {
			return err
		}
		if current.Revision == 0 || current.Limit != int(limit) {
			counts["privileges_changed"]++
		}
	}
	return nil
}
func validateImport(e *Exam, a Actor, kind string, p Params, rows []ImportRow, requireEdit bool) ([]ImportRow, *Exam, map[string]int, error) {
	if !importKind(kind) {
		return nil, nil, nil, Bad("invalid_import_type")
	}
	if len(rows) > 5000 {
		return nil, nil, nil, Bad("import_rows_limit")
	}
	if err := importPreviewBound(rows); err != nil {
		return nil, nil, nil, err
	}

	if !a.Can(requireEdit) {
		return nil, nil, nil, Denied
	}
	if _, err := auxScope(e, a, p, requireEdit); err != nil {
		return nil, nil, nil, err
	}
	if requireEdit && kind == "questions" && !a.OrganizationAdmin && e.OwnerDivisionID != p.DivisionID {
		return nil, nil, nil, Denied
	}
	if requireEdit && kind == "questions" && !a.OrganizationAdmin {
		for division := range e.Divisions {
			if !contains(a.DivisionIDs, division) {
				return nil, nil, nil, Denied
			}
		}
	}

	if (kind == "outside") != (e.Kind == "outside_lms") {
		return nil, nil, nil, Bad("wrong_exam_type")
	}
	// Read-only preview validation simulates writes on a detached candidate;
	// it needs no write authority over the original exam.
	commandActor := a
	if !requireEdit && !commandActor.Can(true) {
		commandActor.Permissions = make(map[string]Permission, len(a.Permissions))
		for key, value := range a.Permissions {
			commandActor.Permissions[key] = value
		}
		commandActor.Permissions["exams"] = Permission{View: true, Edit: true}
	}
	copy, err := cloneAuxExam(e)
	if err != nil {
		return nil, nil, nil, err
	}
	out := append([]ImportRow(nil), rows...)
	keys := map[string]int{}
	rowIDs := map[string]int{}
	counts := map[string]int{"rows": 0, "parts_created": 0, "questions_created": 0, "commissions_created": 0, "assignments_created": 0, "results_created": 0, "privileges_changed": 0}
	for i := range out {
		row := &out[i]
		row.Errors = []string{}
		if row.RowID == "" {
			row.Errors = append(row.Errors, "invalid_row_id")
		} else if previous, exists := rowIDs[row.RowID]; exists {
			row.Errors = append(row.Errors, "duplicate_row_id")
			out[previous].Errors = append(out[previous].Errors, "duplicate_row_id")
		} else {
			rowIDs[row.RowID] = i
		}
		if row.Excluded {
			continue
		}
		values, err := importValues(row.Input)
		if err != nil {
			row.Errors = append(row.Errors, err.Error())
			continue
		}
		key, err := importKey(copy, p, kind, values)
		if err != nil {
			row.Errors = append(row.Errors, err.Error())
			continue
		}
		if previous, exists := keys[key]; exists {
			row.Errors = append(row.Errors, "duplicate_import_row")
			out[previous].Errors = append(out[previous].Errors, "duplicate_import_row")
		} else {
			keys[key] = i
		}
		candidate := importCandidate(copy)
		rowCounts := map[string]int{}
		if err = applyImportRow(candidate, commandActor, kind, p, row, values, rowCounts); err != nil {
			row.Errors = append(row.Errors, err.Error())
			continue
		}
		copy = candidate
		counts["rows"]++
		for k, v := range rowCounts {
			counts[k] += v
		}
	}
	if err := importPreviewBound(out); err != nil {
		return nil, nil, nil, err
	}
	return out, copy, counts, nil
}
func ValidateImport(e *Exam, a Actor, kind string, p Params, rows []ImportRow) ([]ImportRow, error) {
	out, _, _, err := validateImport(e, a, kind, p, rows, false)
	return out, err
}

// CommitImport writes back the complete candidate only after every included row
// has passed; failed imports leave the live exam and ID allocator untouched.
func CommitImport(e *Exam, a Actor, kind string, p Params, rows []ImportRow) (any, error) {
	checked, copy, counts, err := validateImport(e, a, kind, p, rows, true)
	if err != nil {
		return nil, err
	}
	for _, row := range checked {
		if !row.Excluded && len(row.Errors) > 0 {
			return nil, Conflict("import_conflict")
		}
	}
	if counts["rows"] == 0 {
		return map[string]any{"committed": true, "changed": false, "rating_changed": false, "counts": counts}, nil
	}
	*e = *copy
	return map[string]any{"committed": true, "changed": true, "rating_changed": kind == "outside", "counts": counts}, nil
}

// Import commands only append entities and replace scalar revisions. Copying
// division structs and privilege maps provides cheap row rollback while the
// full original exam clone keeps validation and commit atomic.
func importCandidate(e *Exam) *Exam {
	candidate := *e
	candidate.Divisions = make(map[int64]*Division, len(e.Divisions))
	for id, d := range e.Divisions {
		division := *d
		division.Privileges = make(map[int64]Privilege, len(d.Privileges))
		for student, privilege := range d.Privileges {
			division.Privileges[student] = privilege
		}
		candidate.Divisions[id] = &division
	}
	return &candidate
}
