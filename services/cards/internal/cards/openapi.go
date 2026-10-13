package cards

import (
	"encoding/json"
	"reflect"
	"strings"
	"time"
)

func schema(t reflect.Type) map[string]any {
	if t == reflect.TypeOf(time.Time{}) {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	if t == reflect.TypeOf(json.RawMessage{}) {
		return map[string]any{}
	}
	if t.Kind() == reflect.Pointer {
		return map[string]any{"anyOf": []any{schema(t.Elem()), map[string]any{"type": "null"}}}
	}
	switch t.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": schema(t.Elem())}
	case reflect.Struct:
		p := map[string]any{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				p[name] = schema(f.Type)
			}
		}
		return map[string]any{"type": "object", "properties": p, "additionalProperties": false}
	}
	return map[string]any{}
}
func content(s map[string]any) map[string]any {
	return map[string]any{"application/json": map[string]any{"schema": s}}
}
func success(s map[string]any) map[string]any {
	return map[string]any{"type": "object", "required": []string{"status", "data"}, "properties": map[string]any{"status": map[string]any{"const": true}, "data": s}}
}
func (s *Server) openAPI() map[string]any {
	paths := map[string]any{}
	schemas := map[string]any{}
	for _, dto := range []any{Theme{}, Card{}, Stats{}, Settings{}, Batch{}, View{}, Node{}, DirectionView{}, Preview{}, PreviewSummary{}, Session{}, ThemeDTO{}, CardDTO{}, RevisionDTO{}, MarkDTO{}, SessionEdit{}, TransformDTO{}, Receipt{}, ImportProof{}, ImportTarget{}} {
		t := reflect.TypeOf(dto)
		schemas[t.Name()] = schema(t)
	}
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	for _, spec := range s.routes() {
		parts := strings.SplitN(spec.Pattern, " ", 2)
		method, path := parts[0], parts[1]
		ops, ok := paths[path].(map[string]any)
		if !ok {
			ops = map[string]any{}
			paths[path] = ops
		}
		params := []any{}
		for _, segment := range strings.Split(path, "/") {
			if strings.HasPrefix(segment, "{") {
				name := strings.Trim(segment, "{}")
				def := map[string]any{"type": "string"}
				switch name {
				case "theme", "card", "direction":
					def = map[string]any{"type": "integer", "minimum": 1}
				case "batch":
					def = map[string]any{"type": "integer", "minimum": 0}
				case "kind":
					def["enum"] = []string{"manual", "test"}
				case "session":
					def["pattern"] = "^[a-f0-9]{32}$"
				case "command":
					def["pattern"] = "^[A-Za-z0-9:_-]{8,128}$"
				}
				params = append(params, map[string]any{"name": name, "in": "path", "required": true, "schema": def})
			}
		}
		if strings.HasSuffix(path, "/sections") {
			for _, name := range []string{"page", "limit", "search", "kind", "progress"} {
				def := map[string]any{"type": "string"}
				switch name {
				case "page", "limit":
					def = map[string]any{"type": "integer", "minimum": 1}
					if name == "limit" {
						def["maximum"] = 100
						def["default"] = 6
					}
				case "kind":
					def["enum"] = []string{"all", "manual", "test"}
				case "progress":
					def["enum"] = []string{"all", "in_progress", "learned"}
				}
				params = append(params, map[string]any{"name": name, "in": "query", "schema": def})
			}
		}
		if strings.Contains(path, "/batches/") {
			params = append(params, map[string]any{"name": "study_mode", "in": "query", "schema": map[string]any{"type": "string", "enum": []string{"all", "unlearned", "learned", "stale"}}})
		}
		if method != "GET" && (strings.Contains(path, "/themes") || strings.HasSuffix(path, "/transforms") || strings.HasSuffix(path, "/imports/parse") || strings.Contains(path, "/sessions/")) {
			params = append(params, map[string]any{"name": "Idempotency-Key", "in": "header", "required": false, "description": "UUID or case-sensitive ASCII [A-Za-z0-9:_-] key, 8..128 characters, retained for one logical command. Parse identity uses source bytes SHA256 and target fields; multipart boundaries and filenames do not affect identity. The same account and organization replay the original committed result; changing the command or body conflicts. Current permissions are checked on every replay.", "schema": map[string]any{"type": "string", "minLength": 8, "maxLength": 128, "pattern": "^[A-Za-z0-9:_-]+$"}})
		}
		output := map[string]any{"type": "object"}
		switch {
		case strings.HasSuffix(path, "/sections/{kind}/{section}"):
			output = ref("View")
		case strings.HasSuffix(path, "/settings"):
			output = ref("Settings")
		case method == "GET" && strings.HasSuffix(path, "/cards"):
			output = map[string]any{"type": "array", "items": ref("Card")}
		case strings.Contains(path, "/themes") && method != "DELETE":
			if strings.Contains(path, "/cards") {
				output = ref("Card")
			} else {
				output = ref("Theme")
			}
		case strings.Contains(path, "/receipts/"):
			output = ref("Receipt")
		case strings.Contains(path, "/sessions/") && !strings.HasSuffix(path, "/commit") && !strings.HasSuffix(path, "/source") || strings.HasSuffix(path, "/parse") || strings.HasSuffix(path, "/transforms"):
			output = ref("Session")
		case strings.HasSuffix(path, "/tree"):
			output = map[string]any{"type": "object", "properties": map[string]any{"directions": map[string]any{"type": "array", "items": ref("DirectionView")}}}
		}
		responses := map[string]any{"200": map[string]any{"description": "Successful operation; writes and repeats return 200", "content": content(success(output))}}
		for code, message := range map[string]string{"400": "Invalid DTO/filter/file", "401": "Invalid current session", "403": "Permission or answer disclosure denied", "404": "Missing, expired or foreign object", "409": "Revision, duplicate name, fingerprint or idempotency conflict", "415": "JSON required", "500": "Internal persistence error", "503": "Dependency unavailable; never an empty successful catalog"} {
			responses[code] = map[string]any{"description": message, "content": content(map[string]any{"type": "object", "required": []string{"status", "error"}, "properties": map[string]any{"status": map[string]any{"const": false}, "error": map[string]any{"type": "string"}}})}
		}
		op := map[string]any{"summary": method + " " + strings.TrimPrefix(path, "/v1/cards/"), "parameters": params, "security": []any{map[string]any{"bearerAuth": []string{}}}, "responses": responses}
		if strings.Contains(path, "/imports/") {
			op["description"] = "Native Cards requires train.view for contract/targets and train.edit for parse. Uploads additionally enforces current upload permission and original job scope. Manual targets do not depend on Tests."
		}
		if strings.Contains(path, "/sessions/") || strings.Contains(path, "/receipts/") || strings.HasSuffix(path, "/transforms") {
			op["description"] = "Native train.edit; own account and organization and active direction. Transform additionally requires current tests.edit and its pinned verified division. Source returns metadata only; original bytes/download belong to Uploads with its additional current upload permission. Receipts return the immutable original response, not the latest session. A missing receipt does not prove absence of an in-flight command: retry the same request/key."
		}
		if spec.Input != nil {
			input := schema(reflect.TypeOf(spec.Input))
			required := []string{}
			switch spec.Input.(type) {
			case ThemeDTO:
				required = []string{"name", "direction_id"}
				if method == "PUT" {
					required = append(required, "expected_revision")
				}
			case CardDTO:
				required = []string{"question", "answer"}
				if method == "PUT" {
					required = append(required, "expected_revision")
				}
			case RevisionDTO:
				required = []string{"expected_revision"}
			case MarkDTO:
				required = []string{"card_ref"}
				if method == "POST" {
					required = append(required, "content_fingerprint")
				}
			case Settings:
				required = []string{"batch_size", "study_mode"}
			case SessionEdit:
				required = []string{"expected_revision", "cards"}
			case TransformDTO:
				required = []string{"theme_id", "card_ids"}
			}
			input["required"] = required
			op["requestBody"] = map[string]any{"required": true, "content": content(input)}
		}
		if strings.HasSuffix(path, "/parse") {
			op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"multipart/form-data": map[string]any{"schema": map[string]any{"type": "object", "required": []string{"file", "direction_id"}, "properties": map[string]any{"file": map[string]any{"type": "string", "format": "binary"}, "direction_id": map[string]any{"type": "integer", "minimum": 1}, "theme_id": map[string]any{"type": "integer", "minimum": 1}, "create_new_theme": map[string]any{"type": "boolean"}, "new_theme_name": map[string]any{"type": "string", "maxLength": 200}}}}}}
		}
		ops[strings.ToLower(method)] = op
	}
	paths["/v1/cards/imports/template"] = map[string]any{"get": map[string]any{
		"summary":   "Download protected blank XLSX template (Вопрос, Ответ)",
		"security":  []any{map[string]any{"bearerAuth": []string{}}},
		"responses": map[string]any{"200": map[string]any{"description": "XLSX template; native train.view required; Uploads additionally enforces upload.view", "content": map[string]any{"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": map[string]any{"schema": map[string]any{"type": "string", "format": "binary"}}}}},
	}}
	return map[string]any{"openapi": "3.1.0", "info": map[string]any{"title": "CPM Cards", "version": "1.2.0", "description": "Organization-owned manual and test-projected cards. Current auth session and core directions checked on every business request; test answers require tests authorization."}, "paths": paths, "components": map[string]any{"schemas": schemas, "securitySchemes": map[string]any{"bearerAuth": map[string]any{"type": "http", "scheme": "bearer"}}}}
}
