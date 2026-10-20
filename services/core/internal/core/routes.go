package core

import (
	"net/http"
	"reflect"
	"strings"
)

type routeSpec struct {
	Pattern, Summary string
	Handle           handler
	DTO              any
}

func (s *Server) specs() []routeSpec {
	return []routeSpec{
		{"GET /v1/core/directions", "Общие направления организации", s.directions, nil},
		{"POST /v1/core/directions", "Создание направления организации: полный администратор", s.directionSave, NameDTO{}},
		{"PUT /v1/core/directions/{direction}", "Переименование направления организации: полный администратор", s.directionSave, NameDTO{}},
		{"DELETE /v1/core/directions/{direction}", "Архивирование направления организации: полный администратор", s.directionArchive, nil},
		{"GET /v1/core/me", "Собственный профиль", s.myProfile, nil},
		{"GET /v1/core/organization", "Текущая организация", s.organization, nil},
		{"PUT /v1/core/organization", "Переименование своей организации полным администратором", s.organizationUpdate, NameDTO{}},
		{"GET /v1/core/organization/test-settings", "Настройка пересчёта тестов своей организации", s.organizationTestSettings, nil},
		{"PUT /v1/core/organization/test-settings", "Настройка пересчёта тестов: полный администратор, expected_revision", s.organizationTestSettingsUpdate, OrganizationTestSettingsDTO{}},
		{"GET /v1/core/organization/users", "Все учётные записи своей организации: только полный администратор", s.organizationUsers, nil},
		{"GET /v1/core/organization/users/{user}/memberships", "Участия аккаунта только в своей организации: полный администратор", s.organizationUserMemberships, nil},
		{"GET /v1/core/divisions", "Доступные подразделения", s.divisions, nil},
		{"POST /v1/core/divisions", "Создание подразделения полным администратором", s.divisionCreate, NameDTO{}},
		{"PUT /v1/core/divisions/{division}", "Переименование подразделения полным администратором", s.divisionUpdate, NameDTO{}},
		{"DELETE /v1/core/divisions/{division}", "Архивирование пустого подразделения", s.divisionArchive, nil},
		{"GET /v1/core/roles", "Роли и разрешения своей организации: только полный администратор", s.roles, nil},
		{"POST /v1/core/roles", "Создание роли своей организации", s.roleSave, RoleDTO{}},
		{"PUT /v1/core/roles/{role}", "Изменение роли своей организации", s.roleSave, RoleDTO{}},
		{"DELETE /v1/core/roles/{role}", "Удаление неиспользуемой роли", s.roleDelete, nil},
		{"GET /v1/core/schools", "Справочник школ своей организации", s.schools, nil},
		{"GET /v1/core/schools/{school}", "Школа своей организации", s.schoolGet, nil},
		{"POST /v1/core/schools", "Создание школы: только полный администратор", s.schoolSave, SchoolDTO{}},
		{"PUT /v1/core/schools/{school}", "Редактирование/деактивация школы: только полный администратор", s.schoolSave, SchoolDTO{}},
		{"GET /v1/core/attendance-types", "Типы посещения", s.attendanceTypes, nil},
		{"GET /v1/core/audit", "Аудит своей организации: только полный администратор", s.auditList, nil},
		{"GET /v1/core/divisions/{division}/users", "Пользователи: users.view, своя запись или ученики своей группы для проктора", s.users, nil},
		{"GET /v1/core/divisions/{division}/students", "Ученики доступной области: своя группа проктора либо users.view", s.students, nil},
		{"GET /v1/core/divisions/{division}/dashboard", "Сводка выбранного подразделения с учётом прав; скрытые счётчики null", s.dashboard, nil},
		{"GET /v1/core/divisions/{division}/reports/tests", "Официальные результаты опубликованных тестов периода; supervisor либо test_results.view", s.testsReport, nil},
		{"GET /v1/core/divisions/{division}/reports/exams", "Действующие опубликованные результаты экзаменов периода; supervisor либо exams.view", s.examsReport, nil},
		{"GET /v1/core/divisions/{division}/reports/attendance", "Read-only отчёт текущего владельца attendance; supervisor либо attendance.view", s.currentAttendanceReport, nil},
		{"GET /v1/core/divisions/{division}/users/{user}", "Профиль с проверкой доступа к пользователю", s.userGet, nil},
		{"POST /v1/core/divisions/{division}/users", "Создание пользователя: users.edit; сотрудников создаёт только полный администратор", s.userCreate, UserDTO{}},
		{"PUT /v1/core/divisions/{division}/users/{user}", "Обновление профиля с проверкой владельца и области влияния", s.userUpdate, ProfileDTO{}},
		{"PUT /v1/core/divisions/{division}/users/{user}/credentials", "Логин и пароль: полный администратор организации владельца", s.userCredentials, CredentialsDTO{}},
		{"PUT /v1/core/divisions/{division}/users/{user}/active", "Активация/блокировка: полный администратор; история сохраняется", s.userActive, ActiveDTO{}},
		{"PUT /v1/core/divisions/{division}/memberships/{user}", "Добавление доступа: users.edit; назначение роли только полным администратором", s.grant, GrantDTO{}},
		{"DELETE /v1/core/divisions/{division}/memberships/{user}", "Отзыв участия: запрет при рейтинге/истории/последнем участии", s.revokeGrant, nil},
		{"GET /v1/core/divisions/{division}/groups", "Группы: groups.view/users.view либо собственная группа", s.groups, nil},
		{"POST /v1/core/divisions/{division}/groups", "Создание группы: groups.edit", s.groupSave, NameDTO{}},
		{"PUT /v1/core/divisions/{division}/groups/{group}", "Переименование группы: groups.edit", s.groupSave, NameDTO{}},
		{"DELETE /v1/core/divisions/{division}/groups/{group}", "Архивирование группы: groups.edit, участие и история сохраняются", s.groupArchive, nil},
		{"GET /v1/core/divisions/{division}/groups/{group}/members", "Состав группы: groups.view/users.view или назначенный проктор", s.groupMembers, nil},
		{"POST /v1/core/divisions/{division}/groups/{group}/members", "Назначение ученика/проктора в группу: groups.edit", s.groupAssign, GroupMemberDTO{}},
		{"DELETE /v1/core/divisions/{division}/groups/{group}/members/{user}", "Удаление из группы: groups.edit, участие сохраняется", s.groupUnassign, nil},
		{"GET /v1/core/divisions/{division}/directions", "Общие направления организации: совместимый URL подразделения", s.directions, nil},
		{"POST /v1/core/divisions/{division}/directions", "Создание направления организации: полный администратор", s.directionSave, NameDTO{}},
		{"PUT /v1/core/divisions/{division}/directions/{direction}", "Переименование направления организации: полный администратор", s.directionSave, NameDTO{}},
		{"DELETE /v1/core/divisions/{division}/directions/{direction}", "Архивирование направления организации: полный администратор", s.directionArchive, nil},
		{"GET /v1/core/divisions/{division}/schedule", "Расписание: schedule.view или обычный участник; студенту только публичное/своей школы", s.lessons, nil},
		{"POST /v1/core/divisions/{division}/schedule", "Создание занятия: schedule.edit, проверка пересечений", s.lessonSave, LessonDTO{}},
		{"POST /v1/core/divisions/{division}/schedule/bulk", "Атомарное добавление до 100 занятий: schedule.edit", s.lessonsBulk, struct {
			Lessons []LessonDTO `json:"lessons"`
		}{}},
		{"PUT /v1/core/divisions/{division}/schedule/{lesson}", "Редактирование занятия: schedule.edit", s.lessonSave, LessonDTO{}},
		{"DELETE /v1/core/divisions/{division}/schedule/{lesson}", "Удаление занятия: schedule.edit", s.lessonDelete, nil},
		{"GET /v1/core/divisions/{division}/class-days", "Дни занятий: attendance.view или студент/проктор/супервизор", s.days, nil},
		{"POST /v1/core/divisions/{division}/class-days", "Создание дня занятий: attendance.edit", s.daySave, ClassDayDTO{}},
		{"GET /v1/core/divisions/{division}/class-days/{day}", "День занятий своего подразделения", s.dayGet, nil},
		{"PUT /v1/core/divisions/{division}/class-days/{day}", "Редактирование дня: attendance.edit", s.daySave, ClassDayDTO{}},
		{"DELETE /v1/core/divisions/{division}/class-days/{day}", "Удаление дня без записей посещения: attendance.edit", s.dayDelete, nil},
		{"GET /v1/core/divisions/{division}/class-days/{day}/attendance", "Посещения: свои, группы проктора или разрешённого подразделения", s.dayAttendance, nil},
		{"POST /v1/core/divisions/{division}/class-days/{day}/attendance", "Отметка посещения: attendance.edit либо проктор своей группы", s.attendanceSet, AttendanceDTO{}},
		{"DELETE /v1/core/divisions/{division}/class-days/{day}/attendance/{user}", "Удаление ручной отметки: attendance.edit либо проктор своей группы", s.attendanceDelete, nil},
		{"GET /v1/core/divisions/{division}/attendance-report", "Журнал за период с ограничением записей по пользователю/группе", s.attendanceReport, nil},
		{"GET /v1/core/divisions/{division}/zaps", "Отгулы: zaps.view либо собственные для студента", s.zaps, nil},
		{"POST /v1/core/divisions/{division}/zaps", "Заявка на отгул: zaps.edit либо студент за себя", s.zapCreate, ZapDTO{}},
		{"GET /v1/core/divisions/{division}/zaps/{zap}", "Заявка и даты с проверкой владельца", s.zapGet, nil},
		{"GET /v1/core/divisions/{division}/zaps/{zap}/images/{image}", "Вложение заявки с проверкой владельца", s.zapImage, nil},
		{"POST /v1/core/divisions/{division}/zaps/{zap}/process", "Решение по отгулу: zaps.edit; посещение типа 2", s.zapProcess, ZapProcessDTO{}},
		{"POST /v1/core/divisions/{division}/zaps/{zap}/dates/{date}/retry", "Повтор привязки даты одобренного отгула: zaps.edit", s.zapDateAction, nil},
		{"DELETE /v1/core/divisions/{division}/zaps/{zap}/dates/{date}", "Отвязка даты отгула: zaps.edit", s.zapDateAction, nil},
		{"GET /v1/core/divisions/{division}/imports", "История импортов: upload.view и users.view", s.imports, nil},
		{"POST /v1/core/divisions/{division}/imports", "Предпросмотр JSON/CSV/XLSX (до 100): upload.edit и users.edit", s.importPreview, ImportDTO{}},
		{"GET /v1/core/divisions/{division}/imports/receipts/{receipt}", "Восстановление своей сессии импорта по ключу команды без секретов", s.importReceiptGet, nil},
		{"GET /v1/core/divisions/{division}/imports/{import}", "Предпросмотр/отчёт без паролей и хешей", s.importGet, nil},
		{"POST /v1/core/divisions/{division}/imports/{import}/commit", "Атомарный идемпотентный импорт: upload.edit и users.edit", s.importCommit, nil},
		{"GET /v1/core/divisions/{division}/imports/{import}/receipts/{receipt}", "Только чтение своей квитанции применения; upload.edit и users.edit", s.importApplicationReceiptGet, nil},
		{"POST /v1/core/divisions/{division}/legacy-imports", "Предпросмотр старого импорта учеников: до 100 строк, upload.edit и users.edit", s.legacyImportPreview, LegacyImportDTO{}},
		{"GET /v1/core/divisions/{division}/legacy-imports/{import}", "Предпросмотр и отчёт своего старого импорта без секретов", s.legacyImportGet, nil},
		{"PUT /v1/core/divisions/{division}/legacy-imports/{import}", "Редактирование старого импорта: expected_revision и Idempotency-Key", s.legacyImportEdit, LegacyImportEditDTO{}},
		{"POST /v1/core/divisions/{division}/legacy-imports/{import}/commit", "Применение старого импорта: expected_revision и Idempotency-Key", s.legacyImportCommit, LegacyImportCommitDTO{}},
		{"GET /v1/core/divisions/{division}/legacy-imports/{import}/receipts/{receipt}", "Только чтение своей квитанции применения старого импорта; upload.edit и users.edit", s.importApplicationReceiptGet, nil},
	}
}
func (s *Server) routes(mux *http.ServeMux) {
	for _, r := range s.specs() {
		s.route(mux, r.Pattern, r.Handle)
	}
	mux.HandleFunc("GET "+legacyImportTemplatePath, s.legacyImportTemplate)
}
func schema(t reflect.Type) map[string]any {
	if t.Kind() == reflect.Pointer {
		return map[string]any{"anyOf": []any{schema(t.Elem()), map[string]any{"type": "null"}}}
	}
	switch t.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": schema(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": schema(t.Elem())}
	case reflect.Struct:
		props := map[string]any{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				props[name] = schema(f.Type)
			}
		}
		return map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	}
	return map[string]any{}
}
func openAPI() map[string]any {
	paths := map[string]any{}
	dummy := &Server{}
	for _, r := range dummy.specs() {
		parts := strings.SplitN(r.Pattern, " ", 2)
		path := parts[1]
		ops, found := paths[path].(map[string]any)
		if !found {
			ops = map[string]any{}
			paths[path] = ops
		}
		params := []any{}
		for _, chunk := range strings.Split(path, "/") {
			if strings.HasPrefix(chunk, "{") {
				params = append(params, map[string]any{"name": strings.Trim(chunk, "{}"), "in": "path", "required": true, "schema": map[string]any{"type": "integer", "minimum": 1}})
			}
		}
		if parts[0] == "GET" {
			keys := []string{"limit", "offset", "q", "role", "unassigned", "status", "date_from", "date_to", "account_id"}
			if strings.Contains(path, "/reports/") {
				keys = []string{"date_from", "date_to"}
			}
			if strings.HasSuffix(path, "/dashboard") {
				keys = nil
			}
			if strings.HasSuffix(path, "/students") {
				keys = []string{"limit", "offset", "q", "role", "unassigned"}
			}
			for _, k := range keys {
				params = append(params, map[string]any{"name": k, "in": "query", "schema": map[string]any{"type": "string"}})
			}
		}
		op := map[string]any{"summary": r.Summary, "security": []any{map[string]any{"bearerAuth": []string{}}}, "parameters": params, "responses": map[string]any{"200": map[string]string{"description": "Результат {status:true,data:...}"}, "201": map[string]string{"description": "Создано"}, "400": map[string]string{"description": "Неверные данные"}, "401": map[string]string{"description": "Недействующий токен"}, "403": map[string]string{"description": "Нет права/контекста"}, "404": map[string]string{"description": "Объект вне области доступа или отсутствует"}, "409": map[string]string{"description": "Конфликт или запрет удаления истории"}, "503": map[string]string{"description": "Auth/БД недоступны; доступ закрыт"}}}
		var output any
		if strings.Contains(path, "/reports/") {
			output = CoreReport{}
		}
		if strings.HasSuffix(path, "/dashboard") {
			output = Dashboard{}
		}
		if parts[0] == "GET" && path == "/v1/core/divisions/{division}/imports/{import}" {
			output = CredentialImportSessionView{}
		}
		if output != nil {
			outputSchema := schema(reflect.TypeOf(output))
			if _, ok := output.(CredentialImportSessionView); ok {
				outputSchema["required"] = []string{"id", "organization_id", "division_id", "actor_id", "status", "users", "report", "created_at", "authorization_proof", "source_sha256", "source_provenance", "owner_request_sha256", "owner_preview_expires_at"}
				props := outputSchema["properties"].(map[string]any)
				for _, name := range []string{"id", "organization_id", "division_id", "actor_id"} {
					props[name] = map[string]any{"type": "integer", "format": "int64", "minimum": 1}
				}
				props["created_at"] = map[string]any{"type": "string", "format": "date-time"}
				props["owner_preview_expires_at"] = map[string]any{"type": "null"}
				user := props["users"].(map[string]any)["items"].(map[string]any)
				user["properties"].(map[string]any)["password"] = map[string]any{"const": ""}
			}
			op["responses"].(map[string]any)["200"] = map[string]any{"description": "Результат выбранного подразделения; отсутствующее значение null", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "properties": map[string]any{"status": map[string]any{"const": true}, "data": outputSchema}}}}}
		}
		if r.DTO != nil {
			inputSchema := schema(reflect.TypeOf(r.DTO))
			if _, ok := r.DTO.(OrganizationTestSettingsDTO); ok {
				inputSchema = map[string]any{"type": "object", "additionalProperties": false, "required": []string{"expected_revision", "recalculate_on_test_change"}, "properties": map[string]any{"expected_revision": map[string]any{"type": "integer", "minimum": 0}, "recalculate_on_test_change": map[string]any{"type": "boolean"}}}
			}
			content := map[string]any{"application/json": map[string]any{"schema": inputSchema}}
			if r.Handle != nil && strings.HasSuffix(path, "/imports") {
				content["multipart/form-data"] = map[string]any{"schema": map[string]any{"type": "object", "required": []string{"file"}, "properties": map[string]any{"file": map[string]any{"type": "string", "format": "binary"}}}}
			}
			op["requestBody"] = map[string]any{"required": true, "content": content}
		}
		ops[strings.ToLower(parts[0])] = op
	}
	paths[legacyImportTemplatePath] = legacyImportTemplateOpenAPI()
	paths["/internal/core/import-directions/{direction}"] = map[string]any{"get": map[string]any{
		"summary":    "Only one pinned active Cards import direction; operation stage core-direction, distinct Cards delivery key, original UUID; no query/body",
		"security":   []any{map[string]any{"operationGrant": []string{}, "operationDeliveryKey": []string{}}},
		"parameters": []any{map[string]any{"name": "direction", "in": "path", "required": true, "schema": map[string]any{"type": "string", "pattern": "^[1-9][0-9]*$"}}, map[string]any{"name": "X-CPM-Operation-Stage", "in": "header", "required": true, "schema": map[string]any{"const": "core-direction"}}, map[string]any{"name": "X-CPM-Operation-Job", "in": "header", "required": true, "schema": map[string]any{"type": "string", "pattern": "^[a-f0-9]{32}$"}}, map[string]any{"name": "Idempotency-Key", "in": "header", "required": true, "schema": map[string]any{"type": "string", "format": "uuid"}}},
		"responses":  map[string]any{"200": map[string]string{"description": "{status:true,data:{account_id,organization_id,division_id,job_id,owner_session_id,direction_id,active:true}}; no catalog/roster"}, "401": map[string]string{"description": "Missing/wrong operation delivery credential"}, "403": map[string]string{"description": "Live upload.edit/train.edit missing"}, "404": map[string]string{"description": "Pinned direction/scope absent or inactive"}, "409": map[string]string{"description": "Operation request mismatch"}, "410": map[string]string{"description": "Grant expired/revoked; unknown outcome"}, "503": map[string]string{"description": "Authorization unavailable; access denied"}},
	}}
	input := schema(reflect.TypeOf(RatingSnapshotRequest{}))
	input["required"] = []string{"organization_id", "division_id", "date_from", "date_to", "as_of"}
	paths["/internal/core/ratings/snapshot"] = map[string]any{"post": map[string]any{
		"summary":     "Снимок участников и направлений для ratings; только служебный ключ, активная область",
		"security":    []any{map[string]any{"serviceKey": []string{}}},
		"requestBody": map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": input}}},
		"responses": map[string]any{
			"200": map[string]any{"description": "Согласованный снимок, до 2000 учеников. Заблокированные аккаунты и архивные направления сохраняются.", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "properties": map[string]any{"status": map[string]any{"const": true}, "data": schema(reflect.TypeOf(RatingCoreSnapshot{}))}}}}},
			"400": map[string]string{"description": "Неверный DTO, период больше 366 календарных дней или превышен размер снимка"},
			"401": map[string]string{"description": "Неверный служебный ключ"},
			"404": map[string]string{"description": "Неактивная или отсутствующая организация/подразделение"},
		},
	}}
	historyInput := schema(reflect.TypeOf(AttendanceHistoryRequest{}))
	historyInput["required"] = []string{"organization_id", "division_id", "account_id", "operation_key"}
	paths["/internal/core/attendance/history-reservations"] = map[string]any{"post": map[string]any{
		"summary":     "Permanent attendance history reservation; service key only; serializes with membership revoke",
		"security":    []any{map[string]any{"serviceKey": []string{}}},
		"requestBody": map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": historyInput}}},
		"responses": map[string]any{
			"200": map[string]any{"description": "Stable durable reservation; never released on mark deletion or zap unlink", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "properties": map[string]any{"status": map[string]any{"const": true}, "data": schema(reflect.TypeOf(AttendanceHistoryReservation{}))}}}}},
			"400": map[string]string{"description": "Invalid/unknown/duplicate fields or inactive scope DTO"},
			"401": map[string]string{"description": "Missing or invalid service key"},
			"404": map[string]string{"description": "Active student membership/account/division/organization missing"},
			"415": map[string]string{"description": "application/json required"},
			"503": map[string]string{"description": "Reservation not confirmed; database/runtime unavailable"},
		},
	}}
	cellsInput := schema(reflect.TypeOf(AttendanceCellsRequest{}))
	cellsInput["required"] = []string{"organization_id", "division_id", "account_ids", "operation_key"}
	cellsInput["properties"].(map[string]any)["account_ids"] = map[string]any{"type": "array", "minItems": 1, "maxItems": 100, "uniqueItems": true, "items": map[string]any{"type": "integer", "minimum": 1}}
	paths["/internal/core/attendance/cell-targets"] = map[string]any{"post": map[string]any{
		"summary":     "Atomic active division/student/group validation and permanent history reservations for up to 100 unique accounts; proctor and group must be supplied together",
		"security":    []any{map[string]any{"serviceKey": []string{}}},
		"requestBody": map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": cellsInput}}},
		"responses": map[string]any{
			"200": map[string]any{"description": "Committed permanent guards and canonical student names/groups", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "properties": map[string]any{"status": map[string]any{"const": true}, "data": map[string]any{"type": "object", "properties": map[string]any{"organization_id": map[string]any{"type": "integer"}, "division_id": map[string]any{"type": "integer"}, "members": schema(reflect.TypeOf([]AttendanceCellMember{}))}}}}}}},
			"400": map[string]string{"description": "Invalid, duplicate, unknown, oversized or incomplete DTO"},
			"401": map[string]string{"description": "Invalid service key"}, "403": map[string]string{"description": "Live proctor assignment or student group mismatch"}, "404": map[string]string{"description": "Active organization/division/student membership missing"}, "415": map[string]string{"description": "application/json required"}, "500": map[string]string{"description": "Database/runtime failure; guards not confirmed"},
		},
	}}
	return map[string]any{"openapi": "3.1.0", "info": map[string]string{"title": "CPM Core", "version": "0.1.0", "description": "Контекст организации из токена. Неадминистратор работает только в выбранном подразделении. Методы PUT полностью заменяют указанные поля DTO."}, "paths": paths, "components": map[string]any{"securitySchemes": map[string]any{"bearerAuth": map[string]string{"type": "http", "scheme": "bearer", "bearerFormat": "JWT"}, "serviceKey": map[string]string{"type": "apiKey", "in": "header", "name": "X-Service-Key"}, "operationGrant": map[string]string{"type": "apiKey", "in": "header", "name": "X-CPM-Operation-Grant"}, "operationDeliveryKey": map[string]string{"type": "apiKey", "in": "header", "name": "X-CPM-Operation-Service-Key"}}}}
}
func (s *Server) auditList(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.OrganizationAdmin {
		return nil, 0, denied
	}
	limit, offset, e := page(r)
	if e != nil {
		return nil, 0, e
	}
	rows, e := list(r.Context(), s.DB, "SELECT id,actor_id,division_id,action,entity_id,created_at FROM core_audit WHERE organization_id=? ORDER BY id DESC LIMIT ? OFFSET ?", a.OrganizationID, limit, offset)
	return rows, 200, e
}
func (s *Server) organizationUsers(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.OrganizationAdmin {
		return nil, 0, denied
	}
	limit, offset, e := page(r)
	if e != nil {
		return nil, 0, e
	}
	query := "SELECT a.id account_id,a.ref_id profile_id,a.role,a.username login,p.full_name,s.is_active,o.organization_id owner_organization_id FROM auth_users a JOIN " + profiles + " p ON p.id=a.ref_id AND p.role=a.role JOIN auth_account_state s ON s.user_id=a.id LEFT JOIN core_user_owners o ON o.user_id=a.id WHERE (o.organization_id=? OR EXISTS (SELECT 1 FROM auth_organization_access x WHERE x.user_id=a.id AND x.organization_id=?))"
	args := []any{a.OrganizationID, a.OrganizationID}
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		if !validText(q, 100, false) {
			return nil, 0, bad("invalid_query")
		}
		query += " AND (p.full_name LIKE ? OR a.username LIKE ?)"
		args = append(args, "%"+q+"%", "%"+q+"%")
	}
	if role := r.URL.Query().Get("role"); role != "" {
		if _, ok := profileTables[role]; !ok {
			return nil, 0, bad("invalid_role")
		}
		query += " AND a.role=?"
		args = append(args, role)
	}
	args = append(args, limit, offset)
	rows, e := list(r.Context(), s.DB, query+" ORDER BY a.id LIMIT ? OFFSET ?", args...)
	return rows, 200, e
}
