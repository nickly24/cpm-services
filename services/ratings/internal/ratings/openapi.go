package ratings

// openAPI describes the implemented HTTP boundary. Source-owner adapters are
// documented in docs/exams-snapshot-contract.md; their routes are not served
// by ratings and deliberately do not appear in these paths.
func openAPI() any {
	type object = map[string]any
	ref := func(name string) object { return object{"$ref": "#/components/schemas/" + name} }
	str := object{"type": "string"}
	integer := object{"type": "integer", "format": "int64"}
	positive := object{"type": "integer", "format": "int64", "minimum": 1}
	number := object{"type": "number", "format": "double"}
	date := object{"type": "string", "format": "date"}
	timestamp := object{"type": "string", "format": "date-time"}
	uuid := object{"type": "string", "format": "uuid"}
	digest := object{"type": "string", "pattern": "^[a-fA-F0-9]{64}$"}
	nullable := func(value object) object { return object{"anyOf": []any{value, object{"type": "null"}}} }
	array := func(items any) object { return object{"type": "array", "items": items} }
	obj := func(properties object, required ...string) object {
		return object{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	schemas := object{}
	schemas["Error"] = obj(object{"status": object{"const": false}, "error": str}, "status", "error")
	schemas["Period"] = obj(object{"date_from": date, "date_to": date}, "date_from", "date_to")
	schemas["EmptyRequest"] = obj(object{})
	schemas["Job"] = obj(object{
		"id": uuid, "organization_id": positive, "division_id": positive, "revision": positive,
		"status":    object{"type": "string", "enum": []string{"preparing", "waiting_sources", "queued", "running", "validating", "completed", "failed", "cancelled", "superseded"}},
		"date_from": date, "date_to": date, "as_of": timestamp, "created_by": positive,
		"total_count": object{"type": "integer", "minimum": 0}, "processed_count": object{"type": "integer", "minimum": 0},
		"error_code": str, "attempts": object{"type": "integer", "minimum": 0}, "created_at": timestamp, "completed_at": nullable(timestamp),
	}, "id", "organization_id", "division_id", "revision", "status", "date_from", "date_to", "as_of", "created_by", "total_count", "processed_count", "error_code", "attempts", "created_at", "completed_at")
	schemas["Freshness"] = object{"type": "object", "properties": object{
		"is_stale": object{"type": "boolean"}, "reason": str, "version_id": uuid,
		"revision": positive, "as_of": timestamp, "exams_mode": object{"type": "string", "enum": []string{"required", "dev_empty"}},
		"external_tests_mode": object{"type": "string", "enum": []string{"required", "dev_internal_only"}},
	}, "required": []string{"is_stale", "reason"}}
	schemas["Profile"] = obj(object{
		"account_id": positive, "profile_id": integer, "full_name": str, "class": object{},
		"group_id": nullable(integer), "group_name": nullable(str), "school_id": nullable(integer),
		"school_name": nullable(str), "school_short_name": nullable(str), "is_active": object{"type": "boolean"},
	}, "account_id", "profile_id", "full_name", "class", "group_id", "group_name", "school_id", "school_name", "school_short_name", "is_active")
	schemas["HomeworkDetail"] = obj(object{
		"homework_id": positive, "name": str, "deadline": date, "score": number,
		"status": object{"type": "string", "enum": []string{"Сдано", "Не сдано"}}, "date_pass": nullable(str),
	}, "homework_id", "name", "deadline", "score", "status", "date_pass")
	schemas["TestDetail"] = obj(object{
		"test_id": str, "title": str, "direction": str, "score": number,
		"source":             object{"type": "string", "enum": []string{"internal", "external"}},
		"calculation_status": object{"type": "string", "enum": []string{"ready", "missing"}, "description": "Optional for historical results; distinguishes a completed zero from a missing result."},
	}, "test_id", "title", "direction", "score", "source")
	schemas["ExamDetail"] = obj(object{
		"exam_id": str, "exam_name": str, "exam_date": date, "score": number,
		"status": object{"type": "string", "enum": []string{"Сдан", "Не сдавал"}}, "exam_type": str,
		"direction_id": nullable(integer), "attempt_no": nullable(integer), "has_appeal": object{"type": "boolean"}, "missing_result": object{"type": "boolean"},
	}, "exam_id", "exam_name", "exam_date", "score", "status", "exam_type", "direction_id", "attempt_no", "has_appeal", "missing_result")
	schemas["HomeworkRating"] = obj(object{"rating": number, "details": array(ref("HomeworkDetail"))}, "rating", "details")
	schemas["TestsRating"] = obj(object{"rating": number, "directions": object{"type": "object", "additionalProperties": number}, "details": array(ref("TestDetail"))}, "rating", "directions", "details")
	schemas["ExamsRating"] = obj(object{"rating": number, "details": array(ref("ExamDetail"))}, "rating", "details")
	resultProperties := object{
		"job_id": uuid, "revision": positive, "organization_id": positive, "division_id": positive, "account_id": positive,
		"input_hash": digest, "algorithm_version": object{"const": Algorithm}, "status": object{"type": "string", "enum": []string{"ready", "failed"}},
		"error_code": object{"type": "string", "pattern": "^[a-z0-9_]{1,100}$"},
		"homework":   ref("HomeworkRating"), "tests": ref("TestsRating"), "exams": ref("ExamsRating"), "final_rating": number,
	}
	schemas["CalculationResult"] = obj(resultProperties, "job_id", "revision", "organization_id", "division_id", "account_id", "input_hash", "algorithm_version", "status")
	schemas["CalculationResult"].(object)["allOf"] = []any{
		object{"if": object{"properties": object{"status": object{"const": "ready"}}}, "then": object{"required": []string{"homework", "tests", "exams", "final_rating"}}},
		object{"if": object{"properties": object{"status": object{"const": "failed"}}}, "then": object{"required": []string{"error_code"}}},
	}
	schemas["PublishedRating"] = obj(object{"profile": ref("Profile"), "rating": ref("CalculationResult"), "place": positive}, "profile", "rating", "place")
	summaryProperties := object{}
	for key, value := range resultProperties {
		summaryProperties[key] = value
	}
	delete(summaryProperties, "error_code")
	for _, key := range []string{"homework", "tests", "exams"} {
		summaryProperties[key] = obj(object{"rating": number}, "rating")
	}
	schemas["TableRating"] = obj(summaryProperties, "job_id", "revision", "organization_id", "division_id", "account_id", "input_hash", "algorithm_version", "status", "homework", "tests", "exams", "final_rating")
	schemas["TableRow"] = obj(object{"profile": ref("Profile"), "rating": ref("TableRating"), "place": positive}, "profile", "rating", "place")
	schemas["Publication"] = obj(object{"version_id": uuid, "revision": positive, "as_of": timestamp, "date_from": date, "date_to": date}, "version_id", "revision", "as_of", "date_from", "date_to")
	schemas["PersonalHomework"] = obj(object{
		"assignment_id": positive, "name": str, "type_name": str, "deadline": date, "status": integer, "result": nullable(number), "date_pass": nullable(str),
	}, "assignment_id", "name", "type_name", "deadline", "status", "result", "date_pass")
	schemas["PersonalTest"] = obj(object{
		"test_id": str, "title": str, "direction": str, "source": object{"type": "string", "enum": []string{"internal", "external"}},
		"calculation_status": object{"type": "string", "enum": []string{"ready", "missing", "queued", "processing", "retrying", "failed"}}, "score": nullable(number),
	}, "test_id", "title", "direction", "source", "calculation_status", "score")
	schemas["PersonalExam"] = obj(object{
		"exam_id": str, "exam_name": str, "exam_date": date, "exam_type": str, "direction_id": nullable(integer),
		"attempt_no": nullable(integer), "has_appeal": object{"type": "boolean"}, "missing_result": object{"type": "boolean"}, "score": nullable(number),
	}, "exam_id", "exam_name", "exam_date", "exam_type", "direction_id", "attempt_no", "has_appeal", "missing_result", "score")
	schemas["CalculationInput"] = obj(object{
		"job_id": uuid, "revision": positive, "organization_id": positive, "division_id": positive, "account_id": positive,
		"input_hash": digest, "algorithm_version": object{"const": Algorithm}, "date_from": date, "date_to": date, "as_of": timestamp,
		"homework": array(ref("PersonalHomework")), "tests": array(ref("PersonalTest")), "exams": array(ref("PersonalExam")),
	}, "job_id", "revision", "organization_id", "division_id", "account_id", "input_hash", "algorithm_version", "date_from", "date_to", "as_of", "homework", "tests", "exams")
	response := func(schema any, description string) object {
		return object{"description": description, "content": object{"application/json": object{"schema": schema}}}
	}
	envelope := func(data any) object {
		return obj(object{"status": object{"const": true}, "data": data}, "status", "data")
	}
	parameter := func(name, in string, schema any, required bool) object {
		return object{"name": name, "in": in, "required": required, "schema": schema}
	}
	divisionParam := parameter("division", "path", positive, true)
	jobParam := parameter("job", "path", uuid, true)
	accountParam := parameter("account", "path", positive, true)
	idempotency := parameter("Idempotency-Key", "header", object{"type": "string", "pattern": "^[A-Za-z0-9_-]{16,128}$"}, true)
	page := []any{parameter("limit", "query", object{"type": "integer", "minimum": 1, "maximum": 200, "default": 100}, false), parameter("offset", "query", object{"type": "integer", "minimum": 0, "maximum": 100000, "default": 0}, false)}
	version := parameter("version", "query", uuid, false)
	operation := func(id, description string, internal bool, parameters []any, request, data any, codes ...string) object {
		security := "bearerAuth"
		if internal {
			security = "serviceKey"
		}
		op := object{"operationId": id, "description": description, "security": []any{object{security: []string{}}}}
		if len(parameters) > 0 {
			op["parameters"] = parameters
		}
		if request != nil {
			op["requestBody"] = object{"required": true, "content": object{"application/json": object{"schema": request}}}
		}
		responses := object{}
		for _, code := range codes {
			responses[code] = response(envelope(data), "Successful response")
		}
		for _, code := range []string{"400", "401", "403", "404", "409", "415", "503"} {
			responses[code] = response(ref("Error"), "Rejected request or unavailable dependency; error is a stable diagnostic code")
		}
		op["responses"] = responses
		return op
	}
	paths := object{}
	publicBase := "/v1/ratings/divisions/{division}"
	jobBase := publicBase + "/jobs"
	paths[jobBase] = object{
		"post": operation("createRatingJob", "Create a saved asynchronous job for this division. Requires ratings edit permission. Same key and period return the original job; another active job gives 409. Endpoints are inclusive: at most 366 calendar dates (365 days between endpoints).", false, []any{divisionParam, idempotency}, ref("Period"), obj(object{"job": ref("Job"), "status_url": str}, "job", "status_url"), "200", "202"),
		"get":  operation("listRatingJobs", "Read this division's jobs; requires ratings view permission or supervisor role.", false, append([]any{divisionParam}, page...), nil, obj(object{"jobs": array(ref("Job"))}, "jobs"), "200"),
	}
	paths[jobBase+"/{job}"] = object{"get": operation("getRatingJob", "Read job stage, counts and diagnostic code in the authorized organization and division.", false, []any{divisionParam, jobParam}, nil, ref("Job"), "200")}
	paths[jobBase+"/{job}/cancel"] = object{"post": operation("cancelRatingJob", "Cancel a job with ratings edit permission. A published job cannot be cancelled. Send an empty JSON object.", false, []any{divisionParam, jobParam}, ref("EmptyRequest"), ref("Job"), "200")}
	paths[jobBase+"/{job}/retry"] = object{"post": operation("retryRatingJob", "Create a new job/revision for the original period of a failed, cancelled or superseded job. Requires ratings edit permission and a fresh idempotency key. Published and active jobs cannot be retried.", false, []any{divisionParam, jobParam, idempotency}, ref("EmptyRequest"), ref("Job"), "200", "202")}
	paths[publicBase+"/ratings"] = object{"get": operation("listPublishedRatings", "Read the published version. Students see their own row; proctors see their group. Search and pagination follow access filtering; place is the ordinal position in the entire division version before filtering.", false, append([]any{divisionParam, version, parameter("q", "query", str, false)}, page...), nil, obj(object{"ratings": array(ref("PublishedRating")), "total": object{"type": "integer", "minimum": 0}, "freshness": ref("Freshness")}, "ratings", "total", "freshness"), "200")}
	paths[publicBase+"/table"] = object{"get": operation("listRatingTable", "Admin/staff ratings view or supervisor: SQL page of persisted summary scores, without details or live source checks. Search includes name, group, class and account ID. Global rank precedes search. Pin later pages to publication.version_id.", false, append([]any{divisionParam, version, parameter("q", "query", str, false)}, page...), nil, obj(object{"ratings": array(ref("TableRow")), "total": object{"type": "integer", "minimum": 0}, "publication": nullable(ref("Publication"))}, "ratings", "total", "publication"), "200")}
	paths[publicBase+"/freshness"] = object{"get": operation("getRatingFreshness", "Separate live source check for the chosen publication. Requires ratings view permission or supervisor. Never changes stored scores or publication.", false, []any{divisionParam, version}, nil, obj(object{"freshness": ref("Freshness")}, "freshness"), "200")}
	paths[publicBase+"/me"] = object{"get": operation("getMyRating", "Student's own published result in the selected division. Before first publication rating is null.", false, []any{divisionParam, version}, nil, obj(object{"rating": nullable(ref("PublishedRating")), "freshness": ref("Freshness")}, "rating", "freshness"), "200")}
	paths[publicBase+"/accounts/{account}"] = object{"get": operation("getAccountRating", "Read an authorized student's published details. A student cannot read another account; a proctor is limited to their group.", false, []any{divisionParam, accountParam, version}, nil, obj(object{"rating": ref("PublishedRating"), "freshness": ref("Freshness")}, "rating", "freshness"), "200")}
	paths[publicBase+"/versions"] = object{"get": operation("listRatingVersions", "List up to 100 completed versions of this division, newest revision first. Requires ratings view permission or supervisor role.", false, []any{divisionParam}, nil, obj(object{"versions": array(ref("Job"))}, "versions"), "200")}
	paths[publicBase+"/report"] = object{"get": operation("getRatingReport", "Return the complete authorized JSON report with details and period (null before first publication). Requires ratings view permission or supervisor role. XLSX rendering belongs to the client.", false, []any{divisionParam, version}, nil, obj(object{"period": nullable(ref("Period")), "students": array(ref("PublishedRating")), "freshness": ref("Freshness")}, "period", "students", "freshness"), "200")}
	internalBase := "/internal/ratings/jobs/{job}/accounts/{account}"
	paths[internalBase+"/input"] = object{"get": operation("getRatingCalculationInput", "Calculate reads the immutable personal snapshot. Requires X-Service-Key. Cancelled, failed and superseded jobs reject input; completed jobs remain readable for idempotent result replay.", true, []any{jobParam, accountParam}, nil, ref("CalculationInput"), "200")}
	paths[internalBase+"/result"] = object{"post": operation("acceptRatingCalculationResult", "Accept a matching scoped result. Before saving a ready result, ratings obtains core's permanent participation lock. An identical result is idempotent; different repeated output gives result_idempotency_conflict. The result remains staged until complete atomic publication.", true, []any{jobParam, accountParam}, ref("CalculationResult"), obj(object{"accepted": object{"const": true}}, "accepted"), "200")}
	for path, description := range map[string]string{"/health/live": "Process liveness", "/health/ready": "Database readiness and configured exams mode"} {
		paths[path] = object{"get": object{"description": description, "security": []any{}, "responses": object{"200": response(envelope(object{"type": "object"}), "Available"), "503": response(ref("Error"), "Unavailable")}}}
	}
	paths["/openapi.json"] = object{"get": object{"description": "This OpenAPI document", "security": []any{}, "responses": object{"200": response(object{"type": "object"}, "OpenAPI 3.1 document")}}}
	return object{
		"openapi": "3.1.0", "info": object{"title": "CPM Ratings", "version": "1.0.0", "description": "Asynchronous ratings scoped by organization and division; durable personal snapshots, calculate callbacks, staging and version publication. JSON request body limit is 4 MiB; personal immutable calculation input limit is 2 MiB. Exams/external-tests required modes do not silently substitute absent sources with zero."},
		"paths": paths, "components": object{"schemas": schemas, "securitySchemes": object{"bearerAuth": object{"type": "http", "scheme": "bearer"}, "serviceKey": object{"type": "apiKey", "in": "header", "name": "X-Service-Key"}}},
	}
}
