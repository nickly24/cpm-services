package attendance

import (
	"reflect"
	"strings"
)

func commandReceiptSchema() map[string]any {
	sc := schema(reflect.TypeOf(CommandReceipt{}))
	sc["required"] = []string{"contract_version", "command_id", "state", "organization_id", "scope", "division_ids", "method", "path"}
	p := sc["properties"].(map[string]any)
	p["contract_version"] = map[string]any{"type": "integer", "const": 1}
	p["command_id"] = map[string]any{"type": "string", "format": "uuid", "pattern": commandUUID.String()}
	p["state"] = map[string]any{"type": "string", "const": "committed"}
	p["organization_id"] = map[string]any{"type": "integer", "minimum": 1}
	p["scope"] = map[string]any{"type": "string", "enum": []string{"division", "divisions", "organization"}}
	p["division_ids"] = map[string]any{"type": "array", "maxItems": 100, "uniqueItems": true, "items": map[string]any{"type": "integer", "minimum": 1}, "description": "Sorted original target division IDs; empty for organization scope"}
	p["method"] = map[string]any{"type": "string", "enum": []string{"POST", "PUT", "DELETE"}}
	p["path"] = map[string]any{"type": "string", "pattern": "^/", "description": "Original command-relative path without /v1/attendance; contains no query"}
	return sc
}

func schema(t reflect.Type) map[string]any {
	if t.Kind() == reflect.Pointer {
		return map[string]any{"anyOf": []any{schema(t.Elem()), map[string]any{"type": "null"}}}
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Int, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": schema(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": schema(t.Elem())}
	case reflect.Struct:
		p := map[string]any{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			k := strings.Split(f.Tag.Get("json"), ",")[0]
			if f.Anonymous {
				for key, v := range schema(f.Type)["properties"].(map[string]any) {
					p[key] = v
				}
				continue
			}
			if k != "" && k != "-" {
				p[k] = schema(f.Type)
			}
		}
		return map[string]any{"type": "object", "additionalProperties": false, "properties": p}
	}
	return map[string]any{}
}
func OpenAPI() map[string]any {
	paths := map[string]any{}
	for _, rt := range routes() {
		parts := strings.SplitN(rt.Pattern, " ", 2)
		method, path := strings.ToLower(parts[0]), parts[1]
		item, ok := paths[path].(map[string]any)
		if !ok {
			item = map[string]any{}
			paths[path] = item
		}
		parameters := []any{}
		for _, chunk := range strings.Split(path, "/") {
			if strings.HasPrefix(chunk, "{") {
				name := strings.Trim(chunk, "{}")
				sc := map[string]any{"type": "integer", "minimum": 1}
				if name == "date" {
					sc = map[string]any{"type": "string", "format": "date"}
				}
				if name == "command_id" {
					sc = map[string]any{"type": "string", "format": "uuid", "pattern": commandUUID.String()}
				}
				parameters = append(parameters, map[string]any{"name": name, "in": "path", "required": true, "schema": sc})
			}
		}
		switch rt.Kind {
		case "schedule", "overlay", "personal", "days", "report", "own-report", "journal-workspace":
			for _, n := range []string{"date_from", "date_to"} {
				parameters = append(parameters, map[string]any{"name": n, "in": "query", "required": true, "schema": map[string]any{"type": "string", "format": "date"}})
			}
		}
		if rt.Kind == "overlay" {
			parameters = append(parameters, map[string]any{"name": "division_ids", "in": "query", "required": true, "schema": map[string]any{"type": "string", "description": "Comma-separated unique IDs; max 20"}})
		}
		if rt.Kind == "report" || rt.Kind == "entries" {
			for _, n := range []string{"account_id", "group_id"} {
				parameters = append(parameters, map[string]any{"name": n, "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1}})
			}
		}
		if rt.Kind == "audit" {
			parameters = append(parameters, map[string]any{"name": "after_revision", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 0}})
		}
		responses := map[string]any{}
		for _, code := range []string{"200", "400", "401", "403", "404", "409", "415", "503"} {
			responses[code] = map[string]any{"description": map[string]string{"200": "{status:true,data:{revision,value}}; image returns protected binary", "400": "Invalid JSON, fields, scope, period or capacity", "401": "Invalid/revoked auth session", "403": "No object permission", "404": "Missing/inaccessible object", "409": "Revision, receipt, historical protection or state conflict", "415": "application/json required", "503": "Auth/core/database unavailable; permanent history guard missing"}[code]}
		}
		if rt.Kind == "image" {
			imageSchema := map[string]any{"schema": map[string]any{"type": "string", "format": "binary"}}
			responses["200"] = map[string]any{"description": "Protected attachment", "content": map[string]any{"image/png": imageSchema, "image/jpeg": imageSchema}}
		} else {
			dataSchema := map[string]any{"type": "object", "required": []string{"revision", "value"}, "properties": map[string]any{"revision": map[string]any{"type": "integer", "minimum": 0}, "value": schema(reflect.TypeOf(outputShape(rt.Kind, method)))}}
			resultSchema := map[string]any{"type": "object", "required": []string{"status", "data"}, "properties": map[string]any{"status": map[string]any{"const": true}, "data": dataSchema}}
			responses["200"] = map[string]any{"description": "Scoped result and current organization revision", "content": map[string]any{"application/json": map[string]any{"schema": resultSchema}}}
			if rt.Kind == "command-receipt" {
				dataSchema["properties"].(map[string]any)["revision"] = map[string]any{"type": "integer", "minimum": 1, "description": "Original committed revision, not current organization revision"}
				dataSchema["properties"].(map[string]any)["value"] = commandReceiptSchema()
				responses["200"].(map[string]any)["description"] = "Positive commit evidence after current authorization; metadata only"
				responses["400"] = map[string]any{"description": "invalid_command_id or invalid_receipt_request; query and request body forbidden"}
				responses["404"] = map[string]any{"description": "not_found; missing own or foreign actor/organization receipt. Never proves non-commit"}
				responses["409"] = map[string]any{"description": "receipt_scope_unverifiable; legacy bulk without immutable scope metadata"}
				responses["503"] = map[string]any{"description": "dependency_unavailable; unavailable dependency or missing/corrupt receipt/audit evidence"}
				responses["405"] = map[string]any{"description": "method_not_allowed; GET only, including no HEAD"}
				delete(responses, "415")
			}
		}
		op := map[string]any{"operationId": strings.NewReplacer("/", "_", " ", "_", "{", "", "}", "").Replace(rt.Pattern), "summary": rt.Kind, "parameters": parameters, "security": []any{map[string]any{"bearerAuth": []string{}}}, "responses": responses}
		if method != "get" {
			input := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}}
			if rt.Input != nil {
				input = schema(reflect.TypeOf(rt.Input))
			}
			if rt.Kind == "cells" {
				input["required"] = []string{"cells"}
				cell := schema(reflect.TypeOf(CellEdit{}))
				cell["required"] = []string{"class_day_id", "account_id", "attendance_type_id"}
				properties := cell["properties"].(map[string]any)
				for _, id := range []string{"class_day_id", "account_id"} {
					properties[id] = map[string]any{"type": "integer", "minimum": 1}
				}
				properties["attendance_type_id"] = map[string]any{"type": "integer", "minimum": 0, "description": "0 erases an existing mark; otherwise active type in this division"}
				input["properties"].(map[string]any)["cells"] = map[string]any{"type": "array", "minItems": 1, "maxItems": maxCellBatch, "items": cell, "description": "Unique (class_day_id, account_id) pairs; atomic command, one revision/receipt/audit. Zap-linked marks are protected."}
			}
			if rt.Kind != "qr" {
				parameters = append(parameters, map[string]any{"name": "Idempotency-Key", "in": "header", "required": true, "schema": map[string]any{"type": "string", "minLength": 8, "maxLength": 100}})
				op["parameters"] = parameters
				input = map[string]any{"type": "object", "additionalProperties": false, "required": []string{"expected_revision", "data"}, "properties": map[string]any{"expected_revision": map[string]any{"type": "integer", "minimum": 0}, "data": input}}
			}
			op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": input}}}
		}
		item[method] = op
	}
	components := map[string]any{}
	components["CommandReceipt"] = commandReceiptSchema()
	for name, t := range map[string]reflect.Type{"Lesson": reflect.TypeOf(Lesson{}), "Event": reflect.TypeOf(Event{}), "Calendar": reflect.TypeOf(Calendar{}), "AttendanceType": reflect.TypeOf(Type{}), "Day": reflect.TypeOf(Day{}), "Entry": reflect.TypeOf(Entry{}), "Zap": reflect.TypeOf(Zap{}), "OverlapCluster": reflect.TypeOf(Overlap{})} {
		components[name] = schema(t)
	}
	return map[string]any{"openapi": "3.1.0", "info": map[string]any{"title": "CPM Attendance & Schedule", "version": "0.1.0", "description": "Standalone local-dev owner. Organization is authenticated; calendar selection never changes auth context. All overlaps allowed; periods inclusive <=366 days. Mutations use organization CAS revision and Idempotency-Key. Journal/zap creation fails closed without permanent core guard. See README/contracts for exact input/output and rollout blockers."}, "paths": paths, "components": map[string]any{"schemas": components, "securitySchemes": map[string]any{"bearerAuth": map[string]any{"type": "http", "scheme": "bearer", "bearerFormat": "signed RS256 JWT, verified online by auth"}}}}
}
