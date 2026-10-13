package tests

import (
	"encoding/json"
	"reflect"
	"strings"
	"time"
)

type routeSpec struct {
	Pattern, Summary string
	Handle           handler
	Input, Output    any
}

func (s *Server) specs() []routeSpec {
	routes := []routeSpec{
		{"GET /v1/tests/bank", "Банк своей организации: tests.view; чужие подразделения только при открытии теста в банк", s.bank, nil, ListView{}},
		{"GET /v1/tests/{test}", "Метаданные теста и разрешённые действия", s.detail, nil, TestView{}},
		{"PUT /v1/tests/{test}", "Общие реквизиты: редактор подразделения-владельца либо полный администратор", s.metadata, MetadataDTO{}, TestView{}},
		{"PUT /v1/tests/{test}/bank", "Открытие/закрытие теста в банке организации", s.setBank, BankDTO{}, TestView{}},
		{"PUT /v1/tests/{test}/archive", "Архивирование/восстановление общего теста", s.archive, ArchiveDTO{}, TestView{}},
		{"GET /v1/tests/divisions/{division}/tests", "Тесты подразделения; ученику только опубликованные", s.divisionList, nil, ListView{}},
		{"POST /v1/tests/divisions/{division}/tests", "Создание метаданных теста; обязательный Idempotency-Key", s.create, CreateDTO{}, TestView{}},
		{"POST /v1/tests/divisions/{division}/tests/{test}/attach", "Подключить тот же тест с новыми скрытыми настройками", s.attach, RevisionDTO{}, TestView{}},
		{"POST /v1/tests/divisions/{division}/tests/{test}/copy", "Независимая физическая копия теста с вопросами; обязательный Idempotency-Key", s.copyTest, CopyDTO{}, TestView{}},
		{"PUT /v1/tests/divisions/{division}/tests/{test}/settings", "Заменить настройки только выбранного подразделения", s.settings, SettingsDTO{}, TestView{}},
		{"DELETE /v1/tests/divisions/{division}/tests/{test}", "Снять подключение; подразделение-владелец снять нельзя", s.detach, RevisionDTO{}, nil},
		{"GET /v1/tests/divisions/{division}/tests/{test}/access", "Доступ к прохождению, тренировке и разбору с учётом подразделения, содержимого и сдачи", s.access, nil, AccessView{}},
	}
	routes = append(routes, s.contentSpecs()...)
	routes = append(routes, s.draftSpecs()...)
	routes = append(routes, s.attemptSpecs()...)
	routes = append(routes, s.catalogAccessSpec())
	routes = append(routes, s.studentCatalogSpec())
	routes = append(routes, s.importTemplateSpecs()...)
	routes = append(routes, routeSpec{"POST /v1/tests/divisions/{division}/imports/json/preview", "Предметный preview прежнего JSON/OnlineTestPad импорта; tests.edit и upload.edit", s.jsonImportPreview, JSONImportPreviewDTO{}, JSONImportPreview{}}, routeSpec{"POST /v1/tests/divisions/{division}/imports/json/commit", "Атомарно создать тест с содержимым и настройками из проверенного JSON; command_id идемпотентен", s.jsonImportCommit, JSONImportCommitDTO{}, JSONImportCommitResult{}})
	return append(routes, s.externalSpecs()...)
}
func schema(t reflect.Type) map[string]any {
	if t == reflect.TypeOf(json.RawMessage{}) {
		return map[string]any{}
	}
	if t == reflect.TypeOf(time.Time{}) {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	if t.Kind() == reflect.Pointer {
		return map[string]any{"anyOf": []any{schema(t.Elem()), map[string]any{"type": "null"}}}
	}
	switch t.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Array:
		return map[string]any{"type": "string", "pattern": "^[a-f0-9]{24}$"}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": schema(t.Elem())}
	case reflect.Struct:
		props := map[string]any{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.Anonymous {
				embedded := schema(f.Type)["properties"].(map[string]any)
				for k, v := range embedded {
					props[k] = v
				}
				continue
			}
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
		method, path := parts[0], parts[1]
		ops, _ := paths[path].(map[string]any)
		if ops == nil {
			ops = map[string]any{}
			paths[path] = ops
		}
		parameters := []any{}
		for _, p := range strings.Split(path, "/") {
			if strings.HasPrefix(p, "{") {
				name := strings.Trim(p, "{}")
				def := map[string]any{"type": "integer", "minimum": 1}
				if name != "division" && name != "account" {
					def = map[string]any{"type": "string", "pattern": "^[a-fA-F0-9]{24}$"}
				}
				parameters = append(parameters, map[string]any{"name": name, "in": "path", "required": true, "schema": def})
			}
		}
		if method == "GET" {
			if strings.HasSuffix(path, "/student-catalog") {
				for _, name := range []string{"limit", "offset", "q", "direction_id", "status", "date_from", "date_to", "overview", "include_external"} {
					parameters = append(parameters, map[string]any{"name": name, "in": "query", "schema": map[string]any{"type": "string"}})
				}
			}
			if (strings.Contains(path, "/external-tests") || strings.HasSuffix(path, "/external-results")) && !strings.Contains(path, "/imports/results/") {
				for _, name := range []string{"limit", "offset", "q", "direction_id", "date_from", "date_to", "account_id"} {
					parameters = append(parameters, map[string]any{"name": name, "in": "query", "schema": map[string]any{"type": "string"}})
				}
			}
			if strings.HasSuffix(path, "/attempts") || strings.HasSuffix(path, "/submissions") || strings.HasSuffix(path, "/history") || strings.HasSuffix(path, "/calculations") || strings.HasSuffix(path, "/drafts") {
				for _, name := range []string{"limit", "offset"} {
					parameters = append(parameters, map[string]any{"name": name, "in": "query", "schema": map[string]any{"type": "integer", "minimum": 0}})
				}
			}
			if strings.HasSuffix(path, "/attempts") || strings.HasSuffix(path, "/submissions") || strings.HasSuffix(path, "/submissions/stats") {
				for _, name := range []string{"test_id", "account_id"} {
					parameters = append(parameters, map[string]any{"name": name, "in": "query", "schema": map[string]any{"type": "string"}})
				}
			}
			for suffix, name := range map[string]string{"/attempts": "practice", "/submissions": "calculation_status", "/drafts": "state"} {
				if strings.HasSuffix(path, suffix) {
					parameters = append(parameters, map[string]any{"name": name, "in": "query", "schema": map[string]any{"type": "string"}})
				}
			}
			if strings.HasSuffix(path, "/submissions/stats") {
				parameters = append(parameters, map[string]any{"name": "calculation_status", "in": "query", "schema": map[string]any{"type": "string", "enum": []string{"queued", "processing", "retrying", "ready", "failed"}}})
			}
			if strings.HasSuffix(path, "/bank") || strings.HasSuffix(path, "/tests") || strings.HasSuffix(path, "/catalog-with-access") {
				for _, name := range []string{"q", "direction_id", "owner_division_id", "relation", "is_archived", "limit", "offset"} {
					parameters = append(parameters, map[string]any{"name": name, "in": "query", "schema": map[string]any{"type": "string"}})
				}
			}
			if path == "/v1/tests/bank" || path == "/v1/tests/{test}" {
				parameters = append(parameters, map[string]any{"name": "target_division_id", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1}})
			}
			if strings.HasSuffix(path, "/imports/json/template") || strings.HasSuffix(path, "/imports/json/schema") {
				parameters = append(parameters,
					map[string]any{"name": "direction_id", "in": "query", "required": true, "schema": map[string]any{"type": "integer", "minimum": 1}},
					map[string]any{"name": "format", "in": "query", "schema": map[string]any{"type": "string", "enum": []string{"cpm_json", "online_test_pad"}, "default": "cpm_json"}})
			}
		}
		if method == "POST" && (strings.HasSuffix(path, "/copy") || strings.HasSuffix(path, "/tests") || strings.HasSuffix(path, "/drafts") || strings.HasSuffix(path, "/draft")) {
			parameters = append(parameters, map[string]any{"name": "Idempotency-Key", "in": "header", "required": true, "schema": map[string]any{"type": "string", "minLength": 16, "maxLength": 128, "pattern": "^[A-Za-z0-9_-]+$"}})
		}
		if method == "POST" && strings.HasSuffix(path, "/external-tests") || (method == "PUT" || method == "DELETE") && strings.Contains(path, "/external-tests/") && strings.Contains(path, "/results/") {
			parameters = append(parameters, map[string]any{"name": "Idempotency-Key", "in": "header", "required": true, "schema": map[string]any{"type": "string", "minLength": 16, "maxLength": 128}})
		}
		responses := map[string]any{}
		output := map[string]any{"type": "object"}
		if r.Output != nil {
			output = schema(reflect.TypeOf(r.Output))
		}
		for _, code := range []string{"200", "201", "202"} {
			responses[code] = map[string]any{"description": "Результат операции", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "required": []string{"status", "data"}, "properties": map[string]any{"status": map[string]any{"const": true}, "data": output}}}}}
		}
		if content := importDownloadOpenAPI(path); content != nil {
			delete(responses, "201")
			delete(responses, "202")
			responses["200"] = map[string]any{"description": "Синтетический шаблон файлом без JSON envelope", "content": content,
				"headers": map[string]any{"Content-Disposition": map[string]any{"schema": map[string]any{"type": "string"}}, "Content-Length": map[string]any{"schema": map[string]any{"type": "integer"}}}}
		}
		for code, description := range map[string]string{"400": "Неверный ввод", "401": "Недействующий токен", "403": "Недостаточно прав", "404": "Объект отсутствует/чужой", "409": "Конфликт ревизии/идемпотентности/состояния", "415": "Требуется JSON", "500": "Ошибка сохранения", "503": "Зависимость недоступна; доступ закрыт"} {
			responses[code] = map[string]any{"description": description, "content": map[string]any{"application/json": map[string]any{"schema": schema(reflect.TypeOf(ErrorDTO{}))}}}
		}
		op := map[string]any{"summary": r.Summary, "parameters": parameters, "security": []any{map[string]any{"bearerAuth": []string{}}}, "responses": responses}
		if r.Input != nil {
			op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schema(reflect.TypeOf(r.Input))}}}
		}
		ops[strings.ToLower(method)] = op
	}
	paths["/internal/tests/ratings/snapshot"] = ratingSnapshotOpenAPI()
	paths["/internal/tests/external-domain/status"] = map[string]any{"post": map[string]any{
		"summary":     "Готовность внешнего домена в проверенной области организации и подразделения",
		"security":    []any{map[string]any{"serviceKey": []string{}}},
		"requestBody": map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schema(reflect.TypeOf(ExternalDomainRequest{}))}}},
		"responses":   map[string]any{"200": map[string]any{"description": "Домен инициализирован владельцем", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "required": []string{"status", "data"}, "properties": map[string]any{"status": map[string]any{"const": true}, "data": schema(reflect.TypeOf(ExternalDomainStatus{}))}}}}}},
	}}
	for path, defs := range map[string][]any{
		"/internal/tests/external-results/validate": {ExternalImportValidateDTO{}, ExternalImportPreview{}},
		"/internal/tests/external-results/commit":   {ExternalImportCommitDTO{}, ExternalImportCommitResult{}},
		"/internal/tests/external-results/receipt":  {ExternalImportCommitDTO{}, ExternalImportCommitResult{}},
		"/internal/tests/imports/json/validate":     {JSONImportPreviewDTO{}, JSONImportPreview{}},
		"/internal/tests/imports/json/commit":       {JSONImportCommitDTO{}, JSONImportCommitResult{}},
		"/internal/tests/imports/json/receipt":      {JSONImportCommitDTO{}, JSONImportCommitResult{}},
		"/internal/tests/imports/json/prepare":      {JSONImportCommitDTO{}, ImportPreparedCommand{}},
		"/internal/tests/external-results/prepare":  {ExternalImportCommitDTO{}, ImportPreparedCommand{}},
	} {
		paths[path] = map[string]any{"post": map[string]any{"summary": "Предметная операция для Uploads: service key и действующая пользовательская авторизация; org/div права перепроверяются", "security": []any{map[string]any{"serviceKey": []string{}, "bearerAuth": []string{}}}, "requestBody": map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schema(reflect.TypeOf(defs[0]))}}}, "responses": map[string]any{"200": map[string]any{"description": "Предметный результат", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "properties": map[string]any{"status": map[string]any{"const": true}, "data": schema(reflect.TypeOf(defs[1]))}}}}}}}}
		responses := paths[path].(map[string]any)["post"].(map[string]any)["responses"].(map[string]any)
		op := paths[path].(map[string]any)["post"].(map[string]any)
		if strings.HasSuffix(path, "/prepare") {
			op["summary"] = "Readonly native quote before Auth issuance; current bearer, distinct prepare key and original native UUID; no owner writes"
			op["security"] = []any{map[string]any{"bearerAuth": []string{}, "importPrepareKey": []string{}, "nativeCommandKey": []string{}}}
		} else {
			op["summary"] = "Native Uploads operation: legacy service+bearer or complete Rev2 grant carrier with direct live Auth authorization; receipt only reads history"
			op["security"] = []any{map[string]any{"serviceKey": []string{}, "bearerAuth": []string{}}, map[string]any{"operationDeliveryKey": []string{}, "operationGrant": []string{}, "operationJob": []string{}, "operationStage": []string{}, "nativeCommandKey": []string{}}}
		}
		if strings.HasSuffix(path, "/commit") {
			responses["201"] = responses["200"]
		}
		for code, description := range map[string]string{"400": "Неверный ввод", "401": "Требуется служебная и пользовательская авторизация", "403": "Недостаточно прав в выбранной области", "404": "Объект отсутствует/чужой или receipt не найден", "409": "Конфликт ревизии/идемпотентности/preview/grant", "410": "Grant истёк или отозван", "413": "Превышены ограничения пакета", "415": "Требуется JSON", "503": "Зависимость недоступна"} {
			responses[code] = map[string]any{"description": description, "content": map[string]any{"application/json": map[string]any{"schema": schema(reflect.TypeOf(ErrorDTO{}))}}}
		}
	}
	schemes := map[string]any{"bearerAuth": map[string]any{"type": "http", "scheme": "bearer", "bearerFormat": "JWT"}, "serviceKey": map[string]any{"type": "apiKey", "in": "header", "name": "X-Service-Key"}}
	for name, header := range map[string]string{"operationDeliveryKey": "X-CPM-Operation-Service-Key", "operationGrant": "X-CPM-Operation-Grant", "operationJob": "X-CPM-Operation-Job", "operationStage": "X-CPM-Operation-Stage", "nativeCommandKey": "Idempotency-Key", "importPrepareKey": "X-CPM-Import-Prepare-Key"} {
		schemes[name] = map[string]any{"type": "apiKey", "in": "header", "name": header}
	}
	return map[string]any{"openapi": "3.1.0", "info": map[string]any{"title": "CPM Tests", "version": "0.5.0", "description": "Тесты, вопросы, версии, история, черновики, сдачи, внешние результаты и импорты в выбранном подразделении. Официальная сдача принимается асинхронно; calculate считает баллы и пересчитывает результаты по настройке организации."}, "paths": paths, "components": map[string]any{"securitySchemes": schemes}}
}
