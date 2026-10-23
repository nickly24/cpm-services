package core

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"github.com/xuri/excelize/v2"
	"io"
	"net/http"
	"strconv"
	"strings"
)

var studentHeaders = []string{"ФИО", "ID для посещаемости (необязательно)", "Школа (необязательно)", "Класс (необязательно)"}

func parseStudentWorkbook(content []byte) ([]StudentImportRow, error) {
	z, e := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if e != nil {
		return nil, bad("invalid_xlsx")
	}
	if len(z.File) > 2048 {
		return nil, &APIError{413, "student_import_memory_limit"}
	}
	var size uint64
	for _, f := range z.File {
		size += f.UncompressedSize64
		if size > 32<<20 || f.UncompressedSize64 > 8<<20 {
			return nil, &APIError{413, "student_import_memory_limit"}
		}
		name := strings.ToLower(f.Name)
		if strings.Contains(name, "vbaproject") || strings.Contains(name, "externallinks/") || strings.Contains(name, "embeddings/") {
			return nil, bad("unsafe_workbook")
		}
		if !strings.HasSuffix(name, ".xml") && !strings.HasSuffix(name, ".rels") {
			continue
		}
		file, e := f.Open()
		if e != nil {
			return nil, bad("invalid_xlsx")
		}
		dec := xml.NewDecoder(io.LimitReader(file, (8<<20)+1))
		for {
			t, e := dec.Token()
			if e == io.EOF {
				break
			}
			if e != nil {
				file.Close()
				return nil, bad("invalid_xlsx")
			}
			if node, ok := t.(xml.StartElement); ok {
				if node.Name.Local == "f" || node.Name.Local == "oleObject" {
					file.Close()
					return nil, bad("student_import_formula")
				}
				if node.Name.Local == "Relationship" {
					external, hyperlink := false, false
					for _, a := range node.Attr {
						if a.Name.Local == "TargetMode" && strings.EqualFold(a.Value, "External") {
							external = true
						}
						if a.Name.Local == "Type" && strings.HasSuffix(a.Value, "/hyperlink") {
							hyperlink = true
						}
					}
					if external && !hyperlink {
						file.Close()
						return nil, bad("unsafe_workbook")
					}
				}
			}
		}
		file.Close()
	}
	book, e := excelize.OpenReader(bytes.NewReader(content), excelize.Options{UnzipSizeLimit: 32 << 20, UnzipXMLSizeLimit: 8 << 20})
	if e != nil {
		return nil, bad("invalid_xlsx")
	}
	defer book.Close()
	sheets := book.GetSheetList()
	if len(sheets) == 0 {
		return nil, bad("empty_workbook")
	}
	it, e := book.Rows(sheets[0])
	if e != nil {
		return nil, bad("invalid_xlsx")
	}
	defer it.Close()
	headers := map[string]int{}
	out := []StudentImportRow{}
	rownum := 0
	memory := 0
	aliases := map[string]string{"фио": "full_name", "full_name": "full_name", "id для посещаемости (необязательно)": "profile_id", "id для посещаемости": "profile_id", "внешний id": "profile_id", "profile_id": "profile_id", "id": "profile_id", "школа (необязательно)": "school", "школа": "school", "school": "school", "класс (необязательно)": "class", "класс": "class", "class": "class"}
	for it.Next() {
		rownum++
		cells, e := it.Columns()
		if e != nil {
			return nil, bad("invalid_xlsx")
		}
		if rownum == 1 {
			for i, v := range cells {
				k := aliases[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(v, "\ufeff")))]
				if k == "" {
					return nil, bad("student_import_columns")
				}
				if _, ok := headers[k]; ok {
					return nil, bad("student_import_columns")
				}
				headers[k] = i
			}
			if _, ok := headers["full_name"]; !ok {
				return nil, bad("student_import_name_column")
			}
			continue
		}
		nonempty := false
		for _, c := range cells {
			if strings.TrimSpace(c) != "" {
				nonempty = true
			}
			memory += len(c) + 64
		}
		if memory > 6<<20 {
			return nil, &APIError{413, "student_import_memory_limit"}
		}
		if !nonempty {
			continue
		}
		for i, v := range cells {
			if i >= len(headers) && strings.TrimSpace(v) != "" {
				return nil, bad("student_import_columns")
			}
		}
		get := func(k string) string {
			i, ok := headers[k]
			if ok && i < len(cells) {
				return strings.TrimSpace(cells[i])
			}
			return ""
		}
		out = append(out, StudentImportRow{Row: rownum, FullName: get("full_name"), ProfileID: get("profile_id"), School: get("school"), Class: get("class")})
	}
	if it.Error() != nil {
		return nil, bad("invalid_xlsx")
	}
	if len(out) == 0 {
		return nil, bad("student_import_empty")
	}
	return out, nil
}
func workbookBase64(book *excelize.File) (any, int, error) {
	defer book.Close()
	// Excelize retains the initial A1 dimension unless explicitly updated.
	// Streaming spreadsheet readers use this bound and would miss other cells.
	for _, sheet := range book.GetSheetList() {
		rows, e := book.GetRows(sheet)
		if e != nil {
			return nil, 0, e
		}
		columns := 0
		for _, row := range rows {
			if len(row) > columns {
				columns = len(row)
			}
		}
		if columns > 0 {
			last, e := excelize.CoordinatesToCellName(columns, len(rows))
			if e != nil {
				return nil, 0, e
			}
			if e = book.SetSheetDimension(sheet, "A1:"+last); e != nil {
				return nil, 0, e
			}
		}
	}
	buf, e := book.WriteToBuffer()
	if e != nil {
		return nil, 0, e
	}
	return ok(map[string]any{"filename": "students.xlsx", "content_base64": base64.StdEncoding.EncodeToString(buf.Bytes())})
}
func (s *Server) studentTemplate(r *http.Request, a Actor, div int64) (any, int, error) {
	if e := studentAccess(a); e != nil {
		return nil, 0, e
	}
	book := excelize.NewFile()
	sheet := "Ученики"
	_ = book.SetSheetName("Sheet1", sheet)
	for i, v := range studentHeaders {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = book.SetCellStr(sheet, cell, v)
	}
	for i, row := range [][]string{{"Иванов Иван Иванович", "", "", ""}, {"Петрова Анна Сергеевна", "", "", "10"}} {
		for j, v := range row {
			cell, _ := excelize.CoordinatesToCellName(j+1, i+2)
			_ = book.SetCellStr(sheet, cell, v)
		}
	}
	styleStudentBook(book, sheet, 4)
	help, _ := book.NewSheet("Как заполнить")
	_ = help
	for i, text := range []string{"Подразделение выберите один раз в кабинете перед загрузкой.", "ФИО обязательно (до 50 символов). ID, школа и класс можно оставить пустыми.", "ID для посещаемости — постоянный ID профиля/QR, НЕ ID аккаунта. Без ID система выдаст новый.", "Школа — точное название или краткое название существующей школы организации. Новые школы не создаются.", "Класс: целое число от 1 до 99 либо пусто. Логин и пароль будут предложены в редакторе.", "Удалите примеры и внесите учеников. Формулы не допускаются. Файл до 6 МиБ, распакованный до 32 МиБ.", "После подтверждения создание идёт порциями по 25. Можно скачать только реально созданные реквизиты.", "Защищённая выдача доступна инициатору до указанного срока (24 часа с загрузки); сохраните файл."} {
		_ = book.SetCellStr("Как заполнить", "A"+strconv.Itoa(i+1), text)
	}
	_ = book.SetColWidth("Как заполнить", "A", "A", 110)
	return workbookBase64(book)
}
func styleStudentBook(b *excelize.File, sheet string, cols int) {
	last, _ := excelize.ColumnNumberToName(cols)
	_ = b.SetColWidth(sheet, "A", "A", 42)
	_ = b.SetColWidth(sheet, "B", last, 28)
	style, _ := b.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Color: "FFFFFF"}, Fill: excelize.Fill{Type: "pattern", Color: []string{"357B5A"}, Pattern: 1}, Alignment: &excelize.Alignment{WrapText: true, Vertical: "center"}})
	_ = b.SetCellStyle(sheet, "A1", last+"1", style)
	_ = b.SetRowHeight(sheet, 1, 38)
	bodyStyle, _ := b.NewStyle(&excelize.Style{Alignment: &excelize.Alignment{WrapText: true, Vertical: "top"}})
	if rows, e := b.GetRows(sheet); e == nil && len(rows) > 1 {
		_ = b.SetCellStyle(sheet, "A2", last+strconv.Itoa(len(rows)), bodyStyle)
		for row := 2; row <= len(rows); row++ {
			_ = b.SetRowHeight(sheet, row, 32)
		}
	}
	_ = b.SetPanes(sheet, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
}
func (s *Server) studentExport(r *http.Request, a Actor, div int64) (any, int, error) {
	s.studentExpire(r)
	d, e := s.studentLoad(r, s.DB, a, div, false)
	if e != nil {
		return nil, 0, e
	}
	if d.State != "frozen" {
		return nil, 0, &APIError{409, "student_import_not_confirmed"}
	}
	book := excelize.NewFile()
	sheet := "Результат"
	_ = book.SetSheetName("Sheet1", sheet)
	headers := []string{"ФИО", "Логин", "Пароль", "ID для посещаемости / QR", "ID аккаунта", "Подразделение", "Статус"}
	for i, v := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = book.SetCellStr(sheet, cell, v)
	}
	division, e := one(r.Context(), s.DB, "SELECT name FROM divisions WHERE id=? AND organization_id=?", div, a.OrganizationID)
	if e != nil {
		return nil, 0, e
	}
	for start := 0; start < len(d.Payload.Rows); start += studentImportBatchSize {
		index := start / studentImportBatchSize
		var status string
		var report, payload []byte
		e = s.DB.QueryRowContext(r.Context(), "SELECT i.status,i.report,i.payload FROM core_student_import_batches b JOIN core_imports i ON i.id=b.native_session_id WHERE b.draft_id=? AND b.batch_index=?", d.ID, index).Scan(&status, &report, &payload)
		if e != nil && e != sql.ErrNoRows {
			return nil, 0, e
		}
		var native struct {
			Created []int64 `json:"created"`
		}
		var prepared ImportDTO
		if status == "completed" {
			if json.Unmarshal(report, &native) != nil || json.Unmarshal(payload, &prepared) != nil {
				return nil, 0, unavailable
			}
		}
		end := start + studentImportBatchSize
		if end > len(d.Payload.Rows) {
			end = len(d.Payload.Rows)
		}
		for i := start; i < end; i++ {
			u := d.Payload.Rows[i]
			state, login, password, profile, account := "Ещё не создан", "", "", "", ""
			pos := i - start
			if status == "completed" {
				if pos >= len(native.Created) || pos >= len(prepared.Users) {
					return nil, 0, unavailable
				}
				uid := native.Created[pos]
				var ref int64
				var actualLogin, actualHash string
				e = s.DB.QueryRowContext(r.Context(), "SELECT a.ref_id,a.username,a.password FROM auth_users a JOIN core_user_owners o ON o.user_id=a.id JOIN auth_division_access x ON x.user_id=a.id WHERE a.id=? AND a.role='student' AND o.organization_id=? AND x.organization_id=? AND x.division_id=?", uid, a.OrganizationID, a.OrganizationID, div).Scan(&ref, &actualLogin, &actualHash)
				if e != nil {
					return nil, 0, e
				}
				profile, account = strconv.FormatInt(ref, 10), strconv.FormatInt(uid, 10)
				state = "Создан"
				if actualLogin == u.Login && actualHash == prepared.Users[pos].Password {
					login, password = u.Login, u.Password
				} else {
					state = "Реквизиты изменены после импорта"
				}
			}
			for j, v := range []string{u.FullName, login, password, profile, account, division["name"].(string), state} {
				cell, _ := excelize.CoordinatesToCellName(j+1, i+2)
				_ = book.SetCellStr(sheet, cell, v)
			}
		}
	}
	styleStudentBook(book, sheet, 7)
	return workbookBase64(book)
}
