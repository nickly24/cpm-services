package core

import (
	"net/http"

	"github.com/xuri/excelize/v2"
)

const legacyImportTemplatePath = "/v1/core/divisions/{division}/legacy-imports/template"
const legacyImportTemplateMIME = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
const legacyImportTemplateFilename = "users-legacy-template.xlsx"

// The legacy Flask parser and Uploads users.legacy parser recognize these five
// fields. Group membership is derived from the proctor; credentials and IDs are
// deliberately absent. All sample values are fixed synthetic data.
func legacyImportTemplateWorkbook() ([]byte, error) {
	book := excelize.NewFile()
	defer book.Close()
	const sheet = "Ученики"
	if e := book.SetSheetName("Sheet1", sheet); e != nil {
		return nil, e
	}
	rows := [][]any{
		{"ФИО", "Класс", "Школа", "Проктор", "Telegram"},
		{"Примеров Ученик", 7, "Пример школы", "Примеров Проктор", "@example_student"},
	}
	for row, values := range rows {
		cell, e := excelize.CoordinatesToCellName(1, row+1)
		if e != nil {
			return nil, e
		}
		if e = book.SetSheetRow(sheet, cell, &values); e != nil {
			return nil, e
		}
	}
	buffer, e := book.WriteToBuffer()
	if e != nil {
		return nil, e
	}
	return buffer.Bytes(), nil
}

func (s *Server) legacyImportTemplate(w http.ResponseWriter, r *http.Request) {
	a, e := s.Auth.Identify(r)
	if e != nil {
		fail(w, e)
		return
	}
	d, e := id(r, "division")
	if e == nil {
		// Match native legacy creation gates on every download. No caller-supplied
		// actor/organization/context headers or template contents can grant access.
		e = legacyImportPermission(a, d, true)
	}
	if e != nil {
		fail(w, e)
		return
	}
	if r.URL.RawQuery != "" {
		fail(w, bad("unexpected_query"))
		return
	}
	var found int64
	e = s.DB.QueryRowContext(r.Context(), "SELECT d.id FROM divisions d JOIN organizations o ON o.id=d.organization_id AND o.is_active=1 WHERE d.id=? AND d.organization_id=? AND d.is_active=1", d, a.OrganizationID).Scan(&found)
	if e != nil {
		fail(w, e)
		return
	}
	file, e := legacyImportTemplateWorkbook()
	if e != nil {
		fail(w, e)
		return
	}
	w.Header().Set("Content-Type", legacyImportTemplateMIME)
	w.Header().Set("Content-Disposition", `attachment; filename="`+legacyImportTemplateFilename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(file)
}

func legacyImportTemplateOpenAPI() map[string]any {
	return map[string]any{"get": map[string]any{
		"summary":    "Защищённый XLSX старого импорта: 5 исходных колонок и синтетический пример; upload.edit и users.edit",
		"security":   []any{map[string]any{"bearerAuth": []string{}}},
		"parameters": []any{map[string]any{"name": "division", "in": "path", "required": true, "schema": map[string]any{"type": "integer", "minimum": 1}}},
		"responses": map[string]any{
			"200": map[string]any{"description": "Файл users-legacy-template.xlsx: ФИО, Класс, Школа, Проктор, Telegram; без реальных записей, логинов и паролей", "headers": map[string]any{"Content-Disposition": map[string]any{"schema": map[string]any{"type": "string"}}}, "content": map[string]any{legacyImportTemplateMIME: map[string]any{"schema": map[string]any{"type": "string", "format": "binary"}}}},
			"400": map[string]string{"description": "Неверный division или неожиданный query"},
			"401": map[string]string{"description": "Недействующий токен"},
			"403": map[string]string{"description": "Нет одновременно upload.edit и users.edit либо чужой выбранный division"},
			"404": map[string]string{"description": "Подразделение/организация отсутствует, неактивна или чужая"},
			"503": map[string]string{"description": "Auth/БД недоступны; доступ закрыт"},
		},
	}}
}
