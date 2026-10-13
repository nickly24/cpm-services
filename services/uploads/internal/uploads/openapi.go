package uploads

import "strings"

func OpenAPI() map[string]any {
	p := "/v1/uploads/divisions/{division}"
	paths := map[string]any{}
	definitions := []struct{ method, path, summary, input string }{
		{"get", p + "/catalog", "Supported owner adapters and explicit execution dependencies", ""},
		{"post", p + "/sessions", "Create or recover one scoped owner preview with the same command", "Create"},
		{"get", p + "/sessions", "Actor-private session history in the selected division", ""},
		{"get", p + "/sessions/{session}", "Read the complete live owner preview", ""},
		{"put", p + "/sessions/{session}", "Edit the same owner preview with coordinator revision", "Edit"},
		{"post", p + "/sessions/{session}/reconcile", "Reconcile an uncertain edit by reading owner state", "Empty"},
		{"post", p + "/sessions/{session}/commit", "Confirm immutable preview, bind scoped Auth grant and enqueue Rabbit; 202 does not prove apply", "Confirmation"},
		{"get", p + "/jobs", "Actor-private job history", ""},
		{"get", p + "/jobs/{job}", "Read durable job status and explicit dependencies", ""},
		{"get", p + "/jobs/{job}/report", "Read the confirmed owner report when available", ""},
		{"post", p + "/jobs/{job}/retry", "Requeue the same job through Rabbit with a new command", "Empty"},
		{"post", p + "/jobs/{job}/recover", "Explicit readonly native receipt recovery with current rights; operations mode required", "ConfirmAction"},
		{"post", p + "/jobs/{job}/revoke", "Revoke bound authorization; a possible prior apply remains unknown", "ConfirmAction"},
		{"post", p + "/files/register", "Register a protected existing Homework or Attendance reference", "Register"},
		{"get", p + "/files/{file}", "Read registry metadata after current owner access check", ""},
		{"get", p + "/files/{file}/download", "Download an authorized input or Attendance image", ""},
		{"get", p + "/receipts/{command}", "Recover the command resource in its original scope", ""},
		{"get", p + "/events", "Actor-private masked event journal", ""},
		{"get", p + "/templates/{type}", "Download the protected CSV/XLSX/JSON/OnlineTestPad template", ""},
	}
	for _, d := range definitions {
		ops, ok := paths[d.path].(map[string]any)
		if !ok {
			ops = map[string]any{}
			paths[d.path] = ops
		}
		params := []any{}
		for _, part := range strings.Split(d.path, "/") {
			if strings.HasPrefix(part, "{") {
				name := strings.Trim(part, "{}")
				schema := map[string]any{"type": "string"}
				if name == "division" {
					schema = map[string]any{"type": "integer", "minimum": 1}
				}
				params = append(params, map[string]any{"name": name, "in": "path", "required": true, "schema": schema})
			}
		}
		if d.method == "post" || d.method == "put" {
			params = append(params, map[string]any{"name": "Idempotency-Key", "in": "header", "required": !strings.HasSuffix(d.path, "/reconcile"), "schema": map[string]any{"type": "string", "pattern": "^[a-zA-Z0-9:_-]{8,128}$"}})
		}
		if strings.HasSuffix(d.path, "/sessions") && d.method == "get" || strings.HasSuffix(d.path, "/jobs") || strings.HasSuffix(d.path, "/events") {
			params = append(params, map[string]any{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "default": 50}})
		}
		if strings.HasSuffix(d.path, "/templates/{type}") {
			params = append(params,
				map[string]any{"name": "target_id", "in": "query", "description": "Required positive exam ID for exams.* or 24-hex external test ID for tests.external-results; rejected for other types", "schema": map[string]any{"type": "string"}},
				map[string]any{"name": "direction_id", "in": "query", "description": "Required current direction ID for tests.json only", "schema": map[string]any{"type": "integer", "minimum": 1}},
				map[string]any{"name": "format", "in": "query", "description": "tests.json only: native JSON or OnlineTestPad JavaScript containing a literal JSON object", "schema": map[string]any{"type": "string", "enum": []string{"cpm_json", "online_test_pad"}, "default": "cpm_json"}},
			)
		}
		op := map[string]any{"summary": d.summary, "parameters": params, "responses": map[string]any{"200": map[string]any{"description": "Protected scoped response, {status:true,data:...}"}, "400": map[string]any{"description": "Invalid DTO/file"}, "401": map[string]any{"description": "Invalid or revoked signed session"}, "403": map[string]any{"description": "Live scope/upload/subject permission denied"}, "404": map[string]any{"description": "Scoped reference not found"}, "409": map[string]any{"description": "Revision, command hash or outcome conflict"}, "410": map[string]any{"description": "Expired input or owner preview"}, "413": map[string]any{"description": "Body/file/expanded/row limit exceeded"}, "501": map[string]any{"description": "Catalog dependency; no mock import"}, "503": map[string]any{"description": "Owner unavailable or command outcome unknown"}}}
		if d.input != "" {
			content := map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/" + d.input}}}
			if d.input == "Create" {
				content["multipart/form-data"] = map[string]any{"schema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"type", "file"}, "properties": map[string]any{"file": map[string]any{"type": "string", "format": "binary"}, "type": map[string]any{"type": "string"}, "target_id": map[string]any{"type": "string"}, "direction_id": map[string]any{"type": "string"}, "theme_id": map[string]any{"type": "string"}, "create_new_theme": map[string]any{"type": "string"}, "new_theme_name": map[string]any{"type": "string"}}}}
				multipart := content["multipart/form-data"].(map[string]any)["schema"].(map[string]any)
				fields := multipart["properties"].(map[string]any)
				for _, name := range []string{"title", "direction", "startDate", "endDate", "timeLimitMinutes", "published", "visible"} {
					fields[name] = map[string]any{"type": "string", "description": "Optional OnlineTestPad metadata interpreted by Tests"}
				}
			}
			op["requestBody"] = map[string]any{"required": true, "content": content}
		}
		responses := op["responses"].(map[string]any)
		if d.method == "post" && strings.HasSuffix(d.path, "/sessions") {
			responses["201"] = map[string]any{"description": "Created scoped preview"}
		}
		if d.method == "post" && (strings.HasSuffix(d.path, "/commit") || strings.HasSuffix(d.path, "/recover")) {
			responses["202"] = map[string]any{"description": "Durable job, possibly authorization_unknown; preserve confirmation until authorization.bound is true", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/JobEnvelope"}}}}
		}
		if d.method == "get" && strings.HasSuffix(d.path, "/jobs/{job}") || d.method == "post" && strings.HasSuffix(d.path, "/revoke") {
			responses["200"] = map[string]any{"description": "Actor-private current job metadata", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/JobEnvelope"}}}}
		}
		if strings.Contains(d.path, "/templates/") || strings.HasSuffix(d.path, "/download") {
			responses["200"] = map[string]any{"description": "Protected binary response", "content": map[string]any{"application/octet-stream": map[string]any{"schema": map[string]any{"type": "string", "format": "binary"}}}}
		}
		ops[d.method] = op
	}
	obj := func(props map[string]any, required ...string) any {
		return map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": required}
	}
	str := map[string]any{"type": "string"}
	rev := map[string]any{"type": "integer", "minimum": 1}
	payload := map[string]any{"type": "object", "description": "Native owner DTO; actor/org/division and expected_revision must never be supplied here. For tests.json: {direction_id,payload}; for tests.external-results: {rows} with an immutable target_id on create. Tests normalized input is at most 1 MiB."}
	schemas := map[string]any{"Create": obj(map[string]any{"type": str, "target_id": str, "owner_payload": payload}, "type", "owner_payload"), "Edit": obj(map[string]any{"expected_revision": rev, "owner_payload": payload}, "expected_revision", "owner_payload"), "Revision": obj(map[string]any{"expected_revision": rev}, "expected_revision"), "Register": obj(map[string]any{"type": str, "owner_reference": str, "target_id": str}, "type", "owner_reference"), "Empty": obj(map[string]any{}), "OwnerCommand": payload}
	confirmed := map[string]any{"type": "boolean", "const": true}
	schemas["ConfirmImport"] = obj(map[string]any{"expected_revision": rev, "confirmed": confirmed}, "expected_revision", "confirmed")
	schemas["ConfirmAction"] = obj(map[string]any{"confirmed": confirmed}, "confirmed")
	schemas["Confirmation"] = map[string]any{"description": "UPLOADS_OPERATIONS_ENABLED=1 requires ConfirmImport. Disabled historical transport mode accepts Revision and cannot apply domain changes.", "oneOf": []any{map[string]any{"$ref": "#/components/schemas/ConfirmImport"}, map[string]any{"$ref": "#/components/schemas/Revision"}}}
	caps := map[string]any{}
	for _, name := range []string{"retry_delivery", "recover_receipt", "revoke_authorization", "retry_confirmation"} {
		caps[name] = map[string]any{"type": "boolean"}
	}
	schemas["JobCapabilities"] = obj(caps, "retry_delivery", "recover_receipt", "revoke_authorization", "retry_confirmation")
	schemas["JobAuthorization"] = obj(map[string]any{"bound": map[string]any{"type": "boolean"}, "mode": map[string]any{"type": "string", "enum": []string{"apply", "receipt"}}, "expires_at": map[string]any{"type": "string", "format": "date-time"}}, "bound", "mode")
	job := map[string]any{"id": str, "session_id": str, "type": str, "owner": str, "status": str, "error": str, "created_at": map[string]any{"type": "string", "format": "date-time"}, "capabilities": map[string]any{"$ref": "#/components/schemas/JobCapabilities"}, "authorization": map[string]any{"$ref": "#/components/schemas/JobAuthorization"}}
	for _, name := range []string{"organization_id", "division_id", "actor_account_id", "attempt", "processed", "total"} {
		job[name] = map[string]any{"type": "integer"}
	}
	schemas["Job"] = obj(job, "id", "organization_id", "division_id", "actor_account_id", "session_id", "type", "owner", "status", "attempt", "processed", "total", "created_at")
	schemas["JobEnvelope"] = obj(map[string]any{"status": confirmed, "data": map[string]any{"$ref": "#/components/schemas/Job"}}, "status", "data")
	return map[string]any{"openapi": "3.1.0", "info": map[string]any{"title": "CPM uploads (dev)", "version": "0.3.0", "description": "Uploads owns scoped coordinator records, source files and journals. Rabbit delivers execution commands; SQL stores state, outbox and deduplication. Confirmed operations use frozen Auth grant protocol revision 2 when explicitly enabled; default disabled mode reports awaiting_authorization. No user bearer is retained. Native owners enforce current permissions and atomically apply original bytes. Receipt recovery never applies again. Homework/Attendance lifecycles remain authoritative. Source contract does not announce runtime deployment. See docs/background-public-api.md."}, "security": []any{map[string]any{"bearerAuth": []string{}}}, "paths": paths, "components": map[string]any{"securitySchemes": map[string]any{"bearerAuth": map[string]any{"type": "http", "scheme": "bearer", "bearerFormat": "RS256 JWT via auth introspect"}}, "schemas": schemas}}
}
