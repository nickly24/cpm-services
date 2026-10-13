package exams

import (
	"encoding/json"
	"net/http"
	"strings"
)

// The specification describes public transport DTOs, never the stored document.
func serveOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(examsOpenAPI())
}

func openAPIObject(properties map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "additionalProperties": false, "properties": properties}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}
func openAPIRef(name string) map[string]any {
	return map[string]any{"$ref": "#/components/schemas/" + name}
}
func openAPIArray(item any) map[string]any  { return map[string]any{"type": "array", "items": item} }
func openAPIInteger(min int) map[string]any { return map[string]any{"type": "integer", "minimum": min} }
func openAPIString() map[string]any         { return map[string]any{"type": "string"} }
func openAPIBool() map[string]any           { return map[string]any{"type": "boolean"} }
func openAPIEnum(values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}

func examsOpenAPI() map[string]any {
	id := func() any { return openAPIInteger(1) }
	rev := func() any { return openAPIInteger(0) }
	str := func() any { return openAPIString() }
	boolean := func() any { return openAPIBool() }
	date := map[string]any{"type": "string", "format": "date"}
	at := map[string]any{"type": []string{"string", "null"}, "format": "date-time"}
	grade := map[string]any{"type": "integer", "minimum": 0, "maximum": 5}
	memberIDs := map[string]any{"type": "array", "items": id(), "minItems": 1, "maxItems": 6, "uniqueItems": true}
	schemas := map[string]any{
		"Error":                   openAPIObject(map[string]any{"status": map[string]any{"const": false}, "error": str()}, "status", "error"),
		"ExamCreate":              openAPIObject(map[string]any{"exam_type": openAPIEnum("classic", "outside_lms"), "direction_id": id(), "date": date}, "exam_type", "direction_id"),
		"ExamSummary":             openAPIObject(map[string]any{"id": id(), "organization_id": id(), "owner_division_id": id(), "division_id": id(), "direction_id": id(), "exam_type": openAPIEnum("classic", "outside_lms"), "revision": rev(), "content_revision": rev(), "in_bank": boolean(), "origin_exam_id": rev()}, "id", "organization_id", "owner_division_id", "division_id", "exam_type", "revision"),
		"RevisionCommand":         openAPIObject(map[string]any{"expected_revision": rev()}, "expected_revision"),
		"BankCommand":             openAPIObject(map[string]any{"expected_revision": rev(), "in_bank": boolean()}, "expected_revision", "in_bank"),
		"DirectionCommand":        openAPIObject(map[string]any{"expected_revision": rev(), "direction_id": id()}, "expected_revision", "direction_id"),
		"ConnectionCommand":       openAPIObject(map[string]any{"expected_revision": rev(), "division_id": id()}, "expected_revision", "division_id"),
		"Threshold":               openAPIObject(map[string]any{"grade": grade, "min_score": rev()}, "grade", "min_score"),
		"ConfigCommand":           openAPIObject(map[string]any{"expected_revision": rev(), "start_at": at, "end_at": at, "fractional_mode": openAPIEnum("", "round_up", "round_down", "extra_question"), "tie_breaker_source_mode": openAPIEnum("", "any_part", "specific_part"), "tie_breaker_part_id": rev(), "tie_breaker_half_mode": openAPIEnum("", "repeat", "round_up", "round_down"), "thresholds": map[string]any{"type": "array", "items": openAPIRef("Threshold"), "minItems": 6, "maxItems": 6}}, "expected_revision"),
		"Config":                  openAPIObject(map[string]any{"revision": rev(), "start_at": at, "end_at": at, "fractional_mode": str(), "tie_breaker_source_mode": str(), "tie_breaker_part_id": rev(), "tie_breaker_half_mode": str(), "thresholds": openAPIArray(openAPIRef("Threshold"))}, "revision", "thresholds"),
		"PartCreate":              openAPIObject(map[string]any{"code": map[string]any{"type": "string", "pattern": "^[A-Za-z]$"}, "weight": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000}, "quota": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}, "code"),
		"PartUpdate":              openAPIObject(map[string]any{"expected_revision": rev(), "code": map[string]any{"type": "string", "pattern": "^[A-Za-z]$"}, "weight": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000}, "quota": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}, "expected_revision"),
		"Part":                    openAPIObject(map[string]any{"id": id(), "code": str(), "weight": rev(), "quota": rev(), "revision": rev(), "bank_size": rev()}, "id", "code", "weight", "quota", "revision"),
		"QuestionCreate":          openAPIObject(map[string]any{"part_id": id(), "question_text": str(), "answer_text": str()}, "part_id", "question_text", "answer_text"),
		"QuestionUpdate":          openAPIObject(map[string]any{"expected_revision": rev(), "part_id": id(), "question_text": str(), "answer_text": str()}, "expected_revision"),
		"Question":                openAPIObject(map[string]any{"id": id(), "part_id": id(), "question_text": str(), "answer_text": str(), "revision": rev()}, "id", "part_id", "question_text", "answer_text", "revision"),
		"Member":                  openAPIObject(map[string]any{"account_id": id(), "full_name": str(), "role": str(), "active": boolean(), "login": str(), "division_ids": openAPIArray(id())}, "account_id", "full_name"),
		"CommissionCreate":        openAPIObject(map[string]any{"name": str(), "examinator_ids": memberIDs}, "name", "examinator_ids"),
		"CommissionUpdate":        openAPIObject(map[string]any{"expected_revision": rev(), "name": str(), "examinator_ids": memberIDs}, "expected_revision"),
		"Commission":              openAPIObject(map[string]any{"id": id(), "name": str(), "members": openAPIArray(openAPIRef("Member")), "revision": rev()}, "id", "name", "members", "revision"),
		"AssignmentCreate":        openAPIObject(map[string]any{"student_account_id": id(), "commission_id": id()}, "student_account_id", "commission_id"),
		"AssignmentUpdate":        openAPIObject(map[string]any{"expected_revision": rev(), "commission_id": id()}, "expected_revision", "commission_id"),
		"Assignment":              openAPIObject(map[string]any{"id": id(), "student": openAPIRef("Member"), "commission_id": id(), "members": openAPIArray(openAPIRef("Member")), "attempt_no": id(), "history_generation": id(), "revision": rev(), "status": str(), "attempt_id": map[string]any{"type": []string{"integer", "null"}}, "state_version": map[string]any{"type": []string{"integer", "null"}}, "all_members_ready": boolean(), "replacement_limit": rev()}, "id", "student", "commission_id", "attempt_no", "history_generation", "revision"),
		"PrivilegeCommand":        openAPIObject(map[string]any{"expected_revision": rev(), "replacement_limit": map[string]any{"type": "integer", "minimum": 0, "maximum": 100}}, "expected_revision", "replacement_limit"),
		"HistoryCommand":          openAPIObject(map[string]any{"expected_history_generation": id()}, "expected_history_generation"),
		"ReadyCommand":            openAPIObject(map[string]any{}),
		"StartCommand":            openAPIObject(map[string]any{"expected_state_version": id()}, "expected_state_version"),
		"VoteCommand":             openAPIObject(map[string]any{"presented_question_id": id(), "round_id": id(), "value": map[string]any{"type": "number", "enum": []float64{0, 0.5, 1}}}, "presented_question_id", "round_id", "value"),
		"QuestionTransition":      openAPIObject(map[string]any{"expected_state_version": id(), "presented_question_id": id()}, "expected_state_version", "presented_question_id"),
		"AppealCommand":           openAPIObject(map[string]any{"expected_result_version": id(), "grade": grade}, "expected_result_version", "grade"),
		"RetakeCommand":           openAPIObject(map[string]any{"expected_history_generation": id(), "commission_id": id()}, "expected_history_generation", "commission_id"),
		"OutsideCreate":           openAPIObject(map[string]any{"student_account_id": id(), "points": map[string]any{"type": "number", "minimum": 0, "maximum": 999999999.99, "multipleOf": 0.01}, "grade": grade, "examinator": str()}, "student_account_id", "points", "grade", "examinator"),
		"OutsideUpdate":           openAPIObject(map[string]any{"expected_revision": rev(), "points": map[string]any{"type": "number", "minimum": 0, "maximum": 999999999.99, "multipleOf": 0.01}, "grade": grade, "examinator": str()}, "expected_revision"),
		"OutsideResult":           openAPIObject(map[string]any{"id": id(), "exam_id": id(), "division_id": id(), "student": openAPIRef("Member"), "points": map[string]any{"type": "number"}, "grade": grade, "examinator": str(), "revision": rev()}, "id", "exam_id", "division_id", "student", "points", "grade", "examinator"),
		"Result":                  openAPIObject(map[string]any{"id": id(), "exam_id": id(), "division_id": id(), "student": openAPIRef("Member"), "attempt_no": id(), "status": openAPIEnum("completed"), "raw_total": map[string]any{"type": "number"}, "rounded_total": rev(), "max_score": rev(), "calculated_grade": grade, "effective_grade": grade, "has_appeal": boolean(), "result_version": id(), "resolution_type": str(), "started_at": at, "completed_at": at, "published_at": at, "is_current_attempt": boolean(), "history_generation": id(), "appeals": openAPIArray(openAPIRef("Appeal"))}, "id", "exam_id", "division_id", "student", "attempt_no", "calculated_grade", "effective_grade", "result_version"),
		"Appeal":                  openAPIObject(map[string]any{"id": id(), "previous_grade": grade, "new_grade": grade, "actor_account_id": id(), "created_at": at}, "id", "previous_grade", "new_grade", "actor_account_id", "created_at"),
		"Vote":                    openAPIObject(map[string]any{"examinator_account_id": id(), "value": map[string]any{"type": "number", "enum": []float64{0, 0.5, 1}}}, "examinator_account_id", "value"),
		"Round":                   openAPIObject(map[string]any{"id": id(), "round_no": id(), "status": openAPIEnum("open", "consensus", "disputed", "voided"), "votes_count": rev(), "required_votes": id(), "has_voted": boolean(), "own_vote": map[string]any{"type": "number"}, "consensus": map[string]any{"type": "number"}, "votes": openAPIArray(openAPIRef("Vote"))}, "id", "round_no", "status", "votes_count", "required_votes", "has_voted"),
		"PresentedQuestion":       openAPIObject(map[string]any{"id": id(), "question_id": id(), "part_id": id(), "part_code": str(), "question_weight": rev(), "question_text": str(), "answer_text": str(), "sequence_no": id(), "purpose": openAPIEnum("regular", "tie_breaker"), "status": openAPIEnum("open", "consensus", "replaced"), "cycle_no": id(), "replaces_id": rev(), "weighted_score": map[string]any{"type": "number"}, "consensus": map[string]any{"type": "number"}, "round": openAPIRef("Round"), "rounds": openAPIArray(openAPIRef("Round")), "can_replace": boolean(), "can_go_next": boolean()}, "id", "question_id", "part_id", "question_text", "answer_text", "purpose", "status"),
		"ReadinessError":          openAPIObject(map[string]any{"code": str(), "entity_id": id(), "required": rev(), "available": rev()}, "code"),
		"Readiness":               openAPIObject(map[string]any{"is_configured": boolean(), "assignment_ready": boolean(), "can_prepare": boolean(), "can_start_now": boolean(), "errors": openAPIArray(openAPIRef("ReadinessError"))}, "is_configured", "assignment_ready", "can_prepare", "can_start_now", "errors"),
		"TrainingToggle":          openAPIObject(map[string]any{"expected_revision": rev(), "training_enabled": boolean()}, "expected_revision", "training_enabled"),
		"TrainingProgress":        openAPIObject(map[string]any{"content_fingerprint": map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"}}, "content_fingerprint"),
		"TrainingSettingsCommand": openAPIObject(map[string]any{"batch_size": map[string]any{"type": "integer", "enum": []int{10, 20, 30}}, "last_batch_index": openAPIInteger(-1), "study_mode": openAPIEnum("all", "unlearned", "learned", "stale"), "last_part_id": map[string]any{"type": []string{"integer", "null"}, "minimum": 0}}),
		"TrainingCard":            openAPIObject(map[string]any{"card_ref": str(), "question_id": id(), "exam_id": id(), "part_id": id(), "part_code": str(), "question": str(), "answer": str(), "content_fingerprint": str(), "status": openAPIEnum("learned", "unlearned", "answer_changed")}, "card_ref", "question_id", "content_fingerprint", "status"),
		"TrainingStats":           openAPIObject(map[string]any{"total": rev(), "learned": rev(), "answer_changed": rev(), "unlearned": rev(), "progress_percent": map[string]any{"type": "integer", "minimum": 0, "maximum": 100}}, "total", "learned", "answer_changed", "unlearned", "progress_percent"),
		"QuestionImportInput":     openAPIObject(map[string]any{"partCode": str(), "questionText": str(), "answerText": str()}),
		"OutsideImportInput":      openAPIObject(map[string]any{"studentId": id(), "studentLogin": str(), "points": map[string]any{"type": []string{"number", "string"}}, "grade": map[string]any{"type": []string{"integer", "string"}}, "examinator": str()}),
		"ImportCommit":            openAPIObject(map[string]any{"expected_revision": id()}, "expected_revision"),
		"DeletePreview":           openAPIObject(map[string]any{"exam_id": id(), "revision": rev(), "confirmation": str(), "counts": openAPIObject(map[string]any{"parts": rev(), "questions": rev(), "divisions": rev(), "commissions": rev(), "assignments": rev(), "privileges": rev(), "attempts": rev(), "presented_questions": rev(), "rounds": rev(), "votes": rev(), "appeals": rev(), "outside_results": rev(), "classic_results": rev()})}, "exam_id", "revision", "confirmation", "counts"),
	}
	assignmentInput := map[string]any{"studentId": id(), "studentLogin": str(), "replacementLimit": map[string]any{"type": []string{"integer", "string"}}}
	for _, suffix := range []string{"1", "2", "3", "4", "5", "6"} {
		assignmentInput["examinator"+suffix+"Id"] = id()
		assignmentInput["examinator"+suffix+"Login"] = str()
	}
	schemas["AssignmentImportInput"] = openAPIObject(assignmentInput)
	schemas["InvalidExcelCell"] = openAPIObject(map[string]any{"invalidExcelCell": str(), "value": str()}, "invalidExcelCell", "value")
	for _, name := range []string{"QuestionImportInput", "AssignmentImportInput", "OutsideImportInput"} {
		fields := schemas[name].(map[string]any)["properties"].(map[string]any)
		for field, expected := range fields {
			// Previews retain invalid source values for correction. Domain
			// validation reports errors per row rather than erasing the cell.
			fields[field] = map[string]any{"anyOf": []any{expected, map[string]any{"type": []string{"string", "number", "null"}}, openAPIRef("InvalidExcelCell")}}
		}
	}
	schemas["ImportInput"] = map[string]any{"anyOf": []any{openAPIRef("QuestionImportInput"), openAPIRef("AssignmentImportInput"), openAPIRef("OutsideImportInput")}}
	schemas["ImportChange"] = openAPIObject(map[string]any{"row_id": str(), "input": openAPIRef("ImportInput"), "excluded": boolean()}, "row_id")
	schemas["ImportUpdate"] = openAPIObject(map[string]any{"expected_revision": id(), "changes": map[string]any{"type": "array", "items": openAPIRef("ImportChange"), "maxItems": 5000}}, "expected_revision")
	schemas["ImportRow"] = openAPIObject(map[string]any{"row_id": str(), "source_row": id(), "excluded": boolean(), "input": openAPIRef("ImportInput"), "errors": openAPIArray(str())}, "row_id", "source_row", "excluded", "input", "errors")
	schemas["ImportSummary"] = openAPIObject(map[string]any{"total_rows": rev(), "included_rows": rev(), "excluded_rows": rev(), "error_rows": rev(), "valid_rows": rev()}, "total_rows", "included_rows", "excluded_rows", "error_rows", "valid_rows")
	schemas["ImportErrorRow"] = openAPIObject(map[string]any{"row_id": str(), "source_row": id(), "errors": openAPIArray(str())}, "row_id", "source_row", "errors")
	hash := map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"}
	schemas["ImportPreview"] = openAPIObject(map[string]any{"id": map[string]any{"type": "string", "format": "uuid"}, "revision": id(), "state": openAPIEnum("editable", "committed"), "kind": openAPIEnum("questions", "assignments", "outside"), "exam_id": id(), "organization_id": id(), "division_id": id(), "account_id": id(), "filename": str(), "source_hash": hash, "validation_hash": hash, "rows": map[string]any{"type": "array", "items": openAPIRef("ImportRow"), "maxItems": 200}, "total": rev(), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}, "offset": rev(), "has_more": boolean(), "next_offset": map[string]any{"type": []string{"integer", "null"}, "minimum": 0}, "summary": openAPIRef("ImportSummary"), "error_report": openAPIArray(openAPIRef("ImportErrorRow")), "expires_at": at, "result": map[string]any{"type": []string{"object", "null"}}}, "id", "revision", "state", "kind", "exam_id", "organization_id", "division_id", "account_id", "filename", "source_hash", "validation_hash", "rows", "total", "limit", "offset", "has_more", "next_offset", "summary", "error_report", "expires_at", "result")
	schemas["Progress"] = openAPIObject(map[string]any{"regular_consensus": rev(), "regular_required": rev(), "replacement_used": rev(), "replacement_limit": rev(), "parts": openAPIArray(openAPIObject(map[string]any{"part_id": id(), "code": str(), "consensus": rev(), "required": rev()}, "part_id", "code", "consensus", "required"))}, "regular_consensus", "regular_required", "replacement_used", "replacement_limit", "parts")
	schemas["Permissions"] = openAPIObject(map[string]any{"can_ready": boolean(), "can_start": boolean(), "can_vote": boolean(), "can_replace": boolean(), "can_go_next": boolean()}, "can_ready", "can_start", "can_vote", "can_replace", "can_go_next")
	schemas["Attempt"] = openAPIObject(map[string]any{"id": id(), "exam_id": id(), "exam_type": str(), "division_id": id(), "direction_id": id(), "assignment_id": id(), "student": openAPIRef("Member"), "attempt_no": id(), "history_generation": id(), "status": openAPIEnum("pending_ready", "in_progress", "completed"), "phase": openAPIEnum("preparation", "regular_questions", "tie_breaker", "completed"), "state_version": id(), "result_version": id(), "members": openAPIArray(openAPIObject(map[string]any{"account_id": id(), "full_name": str(), "ready": boolean()}, "account_id", "full_name", "ready")), "all_members_ready": boolean(), "started_at": at, "completed_at": at, "server_now": at, "current_question": map[string]any{"anyOf": []any{openAPIRef("PresentedQuestion"), map[string]any{"type": "null"}}}, "progress": openAPIRef("Progress"), "permissions": openAPIRef("Permissions"), "result": map[string]any{"anyOf": []any{openAPIRef("Result"), map[string]any{"type": "null"}}}}, "id", "exam_id", "division_id", "student", "attempt_no", "history_generation", "status", "phase", "state_version", "result_version", "progress", "permissions")
	schemas["Receipt"] = openAPIObject(map[string]any{"outcome": str(), "attempt_id": id(), "applied_state_version": id(), "presented_question_id": id(), "replaced_question_id": id(), "resolved_question_id": id(), "round_id": id(), "next_round_id": id(), "consensus": map[string]any{"type": "number"}, "effective_grade": grade}, "outcome", "attempt_id", "applied_state_version")
	schemas["LifecycleResponse"] = openAPIObject(map[string]any{"changed": boolean(), "rating_changed": boolean(), "outcome": str(), "created": boolean(), "receipt": openAPIRef("Receipt"), "attempt": openAPIRef("Attempt")}, "changed", "rating_changed", "outcome", "receipt", "attempt")
	schemas["TrainingView"] = openAPIObject(map[string]any{"success": boolean(), "account_id": id(), "organization_id": id(), "division_id": id(), "section_kind": str(), "section_ref_id": str(), "exam": openAPIObject(map[string]any{"id": id(), "exam_type": str(), "direction_id": id(), "training_enabled": boolean(), "start_at": at, "end_at": at, "version": rev()}, "id", "exam_type", "training_enabled", "version"), "parts": openAPIArray(openAPIObject(map[string]any{"part_id": id(), "part_code": str(), "ref_id": str(), "stats": openAPIRef("TrainingStats")}, "part_id", "part_code", "ref_id", "stats")), "part_id": rev(), "cards": openAPIArray(openAPIRef("TrainingCard")), "stats": openAPIRef("TrainingStats"), "exam_stats": openAPIRef("TrainingStats"), "batches": openAPIArray(openAPIObject(map[string]any{"index": rev(), "from": id(), "to": id(), "size": rev(), "stats": openAPIRef("TrainingStats")}, "index", "from", "to", "size", "stats")), "settings": openAPIRef("TrainingSettingsCommand"), "preview": boolean()}, "success", "account_id", "organization_id", "division_id", "cards", "stats", "settings", "preview")
	schemas["RatingCatalog"] = openAPIObject(map[string]any{"exam_id": id(), "division_id": id(), "direction_id": id(), "exam_type": openAPIEnum("classic", "outside_lms"), "exam_date": date, "start_at": at, "exam_revision": rev(), "division_revision": rev()}, "exam_id", "division_id", "direction_id", "exam_type", "exam_date", "exam_revision", "division_revision")
	schemas["RatingResult"] = openAPIObject(map[string]any{"exam_id": id(), "division_id": id(), "account_id": id(), "grade": grade, "missing_result": boolean(), "result_id": id(), "attempt_no": id(), "has_appeal": boolean(), "result_version": id(), "history_generation": id()}, "exam_id", "division_id", "account_id", "grade", "missing_result", "result_id", "has_appeal", "result_version")
	schemas["RatingSnapshot"] = openAPIObject(map[string]any{"source": map[string]any{"const": "exams"}, "organization_id": id(), "division_ids": openAPIArray(id()), "date_from": date, "date_to": date, "as_of": at, "source_revision": str(), "revisions": openAPIArray(openAPIObject(map[string]any{"exam_id": id(), "division_id": id(), "exam_revision": rev(), "division_revision": rev()}, "exam_id", "division_id", "exam_revision", "division_revision")), "next_exam_start_at": at, "exams": openAPIArray(openAPIRef("RatingCatalog")), "results": openAPIArray(openAPIRef("RatingResult"))}, "source", "organization_id", "division_ids", "date_from", "date_to", "as_of", "source_revision", "revisions", "exams", "results")
	schemas["ExamDetail"] = openAPIObject(map[string]any{"id": id(), "organization_id": id(), "division_id": id(), "division_revision": rev(), "direction_id": id(), "exam_type": str(), "revision": rev(), "content_revision": rev(), "date": str(), "config": openAPIRef("Config"), "parts": openAPIArray(openAPIRef("Part")), "training_enabled": boolean(), "readiness": openAPIRef("Readiness")}, "id", "organization_id", "division_id", "direction_id", "exam_type", "revision", "content_revision")
	properties := func(name string) map[string]any { return schemas[name].(map[string]any)["properties"].(map[string]any) }
	properties("RatingCatalog")["exam_name"] = str()
	properties("ExamCreate")["name"] = map[string]any{"type": "string", "maxLength": 250}
	schemas["CommissionCreate"].(map[string]any)["required"] = []string{"examinator_ids"}
	properties("ExamSummary")["name"] = str()
	properties("ExamSummary")["connected_division_ids"] = openAPIArray(id())
	properties("ExamSummary")["connected"] = boolean()
	schemas["ExamSummary"].(map[string]any)["required"] = []string{"id", "organization_id", "division_id", "exam_type", "revision"}
	for _, name := range []string{"ExamSummary", "ExamDetail"} {
		for _, field := range []string{"connected_division_ids", "owner_division_id", "origin_exam_id", "in_bank"} {
			if schema, ok := properties(name)[field].(map[string]any); ok {
				schema["description"] = "Administrative exam view only; omitted for students and examiners"
			}
		}
	}
	properties("ExamDetail")["name"] = str()
	properties("ExamDetail")["owner_division_id"] = id()
	properties("ExamDetail")["in_bank"] = boolean()
	properties("ExamDetail")["origin_exam_id"] = rev()
	properties("ExamDetail")["connected_division_ids"] = openAPIArray(id())
	properties("ExamDetail")["connected"] = boolean()
	properties("ExamDetail")["exam_window"] = openAPIObject(map[string]any{"start_at": at, "end_at": at}, "start_at", "end_at")
	for _, field := range []string{"owner_division_id", "in_bank", "origin_exam_id", "connected_division_ids"} {
		properties("ExamDetail")[field].(map[string]any)["description"] = "Administrative exam view only; omitted for students and examiners"
	}
	properties("Assignment")["commission_name"] = str()
	properties("Assignment")["privilege_revision"] = rev()
	for _, name := range []string{"ExamSummary", "ExamDetail", "Result", "OutsideResult"} {
		properties(name)["exam_date"] = map[string]any{"type": []string{"string", "null"}, "format": "date"}
	}
	properties("ExamSummary")["start_at"] = at
	properties("ExamSummary")["end_at"] = at
	properties("ExamSummary")["date"] = map[string]any{"anyOf": []any{date, map[string]any{"const": ""}, map[string]any{"type": "null"}}, "description": "Outside-LMS operational date; classic or draft rows may carry an empty value"}
	properties("ExamSummary")["training_enabled"] = boolean()
	properties("Result")["grade"] = grade
	properties("Result")["has_active_retake"] = boolean()
	properties("Result")["retake_status"] = map[string]any{"type": []string{"string", "null"}, "enum": []any{"not_started", "pending_ready", "in_progress", "completed", nil}}
	properties("Result")["question_count"] = rev()
	properties("Result")["commission"] = openAPIArray(openAPIRef("Member"))
	properties("Result")["exam_window_snapshot"] = openAPIObject(map[string]any{"start_at": at, "end_at": at}, "start_at", "end_at")
	properties("Result")["current"] = openAPIRef("Result")
	properties("Result")["history"] = openAPIArray(openAPIRef("Result"))
	properties("OutsideResult")["current"] = openAPIRef("OutsideResult")
	properties("OutsideResult")["history"] = openAPIArray(openAPIRef("OutsideResult"))
	properties("Vote")["full_name"] = str()
	properties("Vote")["voted_at"] = at
	// These fields are deliberately absent from the student result DTO.
	schemas["Result"].(map[string]any)["required"] = []string{"id", "exam_id", "division_id", "student", "attempt_no", "effective_grade"}
	mutationFields := map[string]any{}
	for key, value := range properties("OutsideResult") {
		if key != "exam_id" && key != "division_id" && key != "current" && key != "history" {
			mutationFields[key] = value
		}
	}
	schemas["OutsideMutationResult"] = openAPIObject(mutationFields, "id", "student", "points", "grade", "examinator", "revision")
	properties("ReadinessError")["blocking"] = boolean()
	properties("Readiness")["configuration_ready"] = boolean()
	properties("Readiness")["warnings"] = openAPIArray(str())
	properties("Readiness")["checked_at"] = at
	properties("Round")["opened_at"] = at
	properties("Round")["completed_at"] = at
	for _, field := range []string{"presented_at", "consensus_at", "replaced_at"} {
		properties("PresentedQuestion")[field] = at
	}
	properties("PresentedQuestion")["replaced_by_account_id"] = rev()
	attemptMembers := properties("Attempt")["members"].(map[string]any)["items"].(map[string]any)
	attemptMembers["properties"].(map[string]any)["ready_at"] = at
	schemas["MetadataCommand"] = openAPIObject(map[string]any{"expected_revision": rev(), "name": map[string]any{"type": "string", "maxLength": 250}, "date": date}, "expected_revision")
	paths := map[string]any{}
	add := func(method, path, input string, output any, description string) {
		params := []any{}
		for _, segment := range strings.Split(path, "/") {
			if strings.HasPrefix(segment, "{") {
				name := strings.Trim(segment, "{}")
				schema := id()
				if name == "kind" {
					schema = openAPIEnum("questions", "assignments", "outside")
				}
				if name == "session" || name == "receipt" {
					schema = map[string]any{"type": "string", "format": "uuid"}
				}
				params = append(params, map[string]any{"name": name, "in": "path", "required": true, "schema": schema})
			}
		}
		security := []any{map[string]any{"bearerAuth": []string{}}}
		if strings.HasPrefix(path, "/health/") || path == "/openapi.json" {
			security = []any{}
		}
		if strings.HasPrefix(path, "/internal/") {
			security = []any{map[string]any{"serviceKey": []string{}}}
		}
		grantRoute := path == "/v1/exams/{exam}/imports/{kind}/{session}" && method == "get" || strings.HasSuffix(path, "/imports/{kind}/{session}/commit") || strings.Contains(path, "/imports/{kind}/{session}/receipts/")
		if grantRoute {
			security = append(security, map[string]any{"operationGrant": []string{}, "operationDelivery": []string{}, "operationJob": []string{}, "operationStage": []string{}, "operationNativeKey": []string{}})
			description += " Background delivery uses the frozen Auth operation-grants v1 contract: all five headers required, no bearer, current upload.edit and exams.edit, original scope/job/native key. Questions require edit rights in all current connected divisions. First apply pins exam revision/source/full validation under locks; receipts never apply. Disabled unless both distinct operations credentials are configured."
		}
		if method != "get" {
			params = append(params, map[string]any{"name": "Idempotency-Key", "in": "header", "required": true, "description": "UUID identifying this command. Retry the same command with the same key; reuse with different body, path or division returns 409.", "schema": map[string]any{"type": "string", "format": "uuid"}})
		}
		if method == "delete" && (path == "/v1/exams/{exam}" || path == "/v1/exams/{exam}/history/{student}") {
			params = append(params, map[string]any{"name": "X-Exam-Confirmation", "in": "header", "required": true, "description": "Confirmation from the corresponding delete-preview. A changed revision invalidates it.", "schema": openAPIString()})
		}
		response := openAPIObject(map[string]any{"status": map[string]any{"const": true}, "data": output}, "status", "data")
		responses := map[string]any{"200": map[string]any{"description": "Successful operation", "content": map[string]any{"application/json": map[string]any{"schema": response}}}}
		if method == "post" && path == "/v1/exams" {
			responses["201"] = responses["200"]
			delete(responses, "200")
		}
		for _, code := range []string{"400", "401", "403", "404", "409", "410", "413", "415", "422", "503"} {
			responses[code] = map[string]any{"description": "Rejected request or unavailable dependency", "content": map[string]any{"application/json": map[string]any{"schema": openAPIRef("Error")}}}
		}
		op := map[string]any{"operationId": method + strings.NewReplacer("/", "_", "{", "", "}", "", "-", "_").Replace(path), "description": description, "security": security, "parameters": params, "responses": responses}
		if input != "" {
			op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": openAPIRef(input)}}}
		}
		entry, ok := paths[path].(map[string]any)
		if !ok {
			entry = map[string]any{}
			paths[path] = entry
		}
		entry[method] = op
	}
	page := func(item any) any {
		return openAPIObject(map[string]any{"items": openAPIArray(item), "total": rev(), "limit": id(), "offset": rev()}, "items", "total", "limit", "offset")
	}
	command := func(field, schema string) any {
		return openAPIObject(map[string]any{"changed": boolean(), "rating_changed": boolean(), field: openAPIRef(schema)}, "changed", "rating_changed", field)
	}
	add("get", "/health/live", "", openAPIObject(map[string]any{"service": str()}, "service"), "Liveness")
	add("get", "/health/ready", "", openAPIObject(map[string]any{"service": str()}, "service"), "SQL schema, auth and core dependency readiness")
	add("get", "/openapi.json", "", map[string]any{"type": "object"}, "This OpenAPI document is returned directly, without the normal response envelope")
	paths["/openapi.json"].(map[string]any)["get"].(map[string]any)["responses"].(map[string]any)["200"] = map[string]any{"description": "OpenAPI document", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "required": []string{"openapi", "info", "paths", "components"}}}}}
	add("get", "/v1/exams", "", page(openAPIRef("ExamSummary")), "Catalog in the authenticated organization and selected division")
	add("post", "/v1/exams", "ExamCreate", openAPIRef("ExamSummary"), "Create an exam in the selected division. Outside exams require date; classic exams reject date.")
	add("get", "/v1/exams/{exam}", "", openAPIRef("ExamDetail"), "Role-specific exam detail; administrators receive conduct configuration")
	add("get", "/v1/exams/{exam}/config", "", openAPIObject(map[string]any{"config": openAPIRef("Config"), "max_score": rev(), "local_validation": openAPIArray(openAPIRef("ReadinessError"))}, "config", "max_score", "local_validation"), "Administrator configuration and validation")
	add("put", "/v1/exams/{exam}/config", "ConfigCommand", command("config", "Config"), "Update selected division rules using the configuration revision")
	for _, collection := range []struct{ name, parameter, item, create, update string }{
		{"parts", "part", "Part", "PartCreate", "PartUpdate"}, {"questions", "question", "Question", "QuestionCreate", "QuestionUpdate"}, {"commissions", "commission", "Commission", "CommissionCreate", "CommissionUpdate"}, {"assignments", "assignment", "Assignment", "AssignmentCreate", "AssignmentUpdate"},
	} {
		path := "/v1/exams/{exam}/" + collection.name
		add("get", path, "", page(openAPIRef(collection.item)), "Paged authorized collection")
		add("post", path, collection.create, command(collection.parameter, collection.item), "Create an item. New items reject expected_revision.")
		add("put", path+"/{"+collection.parameter+"}", collection.update, command(collection.parameter, collection.item), "Update using the item's revision")
		add("delete", path+"/{"+collection.parameter+"}", "RevisionCommand", openAPIObject(map[string]any{"changed": boolean(), "rating_changed": boolean(), "deleted": id()}, "changed", "rating_changed", "deleted"), "Delete the item using its revision and subject restrictions")
	}
	add("get", "/v1/exams/{exam}/privileges", "", page(openAPIObject(map[string]any{"student_account_id": id(), "replacement_limit": rev(), "revision": rev()}, "student_account_id", "replacement_limit", "revision")), "Student replacement limits in the selected division")
	add("put", "/v1/exams/{exam}/privileges/{student}", "PrivilegeCommand", command("privilege", "Privilege"), "Use revision 0 to create a replacement privilege")
	add("delete", "/v1/exams/{exam}/privileges/{student}", "RevisionCommand", command("privilege", "Privilege"), "Delete the replacement privilege")
	schemas["Privilege"] = openAPIObject(map[string]any{"replacement_limit": rev(), "revision": rev()}, "replacement_limit", "revision")
	add("get", "/v1/exams/{exam}/readiness", "", openAPIRef("Readiness"), "Administrative readiness of configuration and live participants")
	add("get", "/v1/exams/{exam}/attempts", "", page(openAPIRef("Attempt")), "Attempts visible to the administrator or assigned examiner")
	add("post", "/v1/exams/{exam}/assignments/{assignment}/attempts/ensure", "HistoryCommand", openAPIRef("LifecycleResponse"), "Assigned examiner creates or returns a pending attempt in this history generation")
	add("get", "/v1/exams/{exam}/attempts/{attempt}", "", map[string]any{"oneOf": []any{openAPIRef("Attempt"), openAPIRef("Result")}}, "Role-specific attempt; students receive only their completed result")
	add("get", "/v1/exams/{exam}/attempts/{attempt}/protocol", "", page(openAPIRef("PresentedQuestion")), "Presented-question history; student DTO excludes all voting rounds")
	add("get", "/v1/exams/{exam}/attempts/{attempt}/questions/{presented}/rounds", "", page(openAPIRef("Round")), "Voting rounds. Open foreign votes are omitted for examiners; students have no access.")
	for _, operation := range []struct{ name, input string }{{"ready", "ReadyCommand"}, {"start", "StartCommand"}, {"vote", "VoteCommand"}, {"next-question", "QuestionTransition"}, {"replace-question", "QuestionTransition"}} {
		add("post", "/v1/exams/{exam}/attempts/{attempt}/"+operation.name, operation.input, openAPIRef("LifecycleResponse"), "Command available only to assigned examiners; versions protect state transitions")
	}
	add("post", "/v1/exams/{exam}/attempts/{attempt}/appeals", "AppealCommand", command("appeal", "Appeal"), "Administrator overrides effective grade without changing calculated sums or votes")
	add("post", "/v1/exams/{exam}/retakes/{student}", "RetakeCommand", command("assignment", "Assignment"), "One retake after completion, with a commission differing by at least one member")
	resultSchema := map[string]any{"oneOf": []any{openAPIRef("Result"), openAPIRef("OutsideResult"), openAPIObject(map[string]any{"student": openAPIRef("Member"), "result": map[string]any{"type": "null"}, "status": openAPIEnum("not_completed")}, "student", "result", "status")}}
	add("get", "/v1/exams/{exam}/results", "", page(resultSchema), "Effective completed results; students see only their own")
	add("get", "/v1/exams/{exam}/results/{student}", "", resultSchema, "Effective subject result, including a worse completed retake")
	add("post", "/v1/exams/{exam}/outside-results", "OutsideCreate", command("result", "OutsideMutationResult"), "Outside-LMS points and grade are entered independently")
	add("put", "/v1/exams/{exam}/outside-results/{result}", "OutsideUpdate", command("result", "OutsideMutationResult"), "Update outside result; student identity is immutable")
	add("delete", "/v1/exams/{exam}/outside-results/{result}", "RevisionCommand", openAPIObject(map[string]any{"changed": boolean(), "rating_changed": boolean(), "deleted": id()}, "changed", "rating_changed", "deleted"), "Delete outside result")
	schemas["HistoryPreview"] = openAPIObject(map[string]any{"exam_id": id(), "division_id": id(), "student_account_id": id(), "history_generation": id(), "assignments": openAPIArray(openAPIObject(map[string]any{"id": id(), "revision": rev(), "attempt_no": id()}, "id", "revision", "attempt_no")), "attempts": openAPIArray(openAPIObject(map[string]any{"id": id(), "state_version": id(), "result_version": id(), "status": str()}, "id", "state_version", "result_version", "status")), "counts": openAPIObject(map[string]any{"attempts": rev(), "presented_questions": rev(), "rounds": rev(), "votes": rev(), "appeals": rev()}, "attempts", "presented_questions", "rounds", "votes", "appeals"), "confirmation": str()}, "exam_id", "division_id", "student_account_id", "history_generation", "confirmation")
	add("get", "/v1/exams/{exam}/history/{student}/delete-preview", "", openAPIRef("HistoryPreview"), "Review affected history before physical cleanup")
	add("delete", "/v1/exams/{exam}/history/{student}", "HistoryCommand", openAPIObject(map[string]any{"changed": boolean(), "rating_changed": boolean(), "history_generation": id()}, "changed", "rating_changed", "history_generation"), "Clear attempts and retake; preserve first assignment and increment history generation")
	for _, op := range []struct{ method, suffix, input string }{{"put", "/bank", "BankCommand"}, {"put", "/direction", "DirectionCommand"}, {"post", "/connections", "ConnectionCommand"}, {"delete", "/connections/{division}", "RevisionCommand"}, {"post", "/copy", "ConnectionCommand"}} {
		add(op.method, "/v1/exams/{exam}"+op.suffix, op.input, openAPIRef("ExamSummary"), "Organization bank, shared-division connection or independent deep copy; object revision required")
	}
	add("put", "/v1/exams/{exam}/metadata", "MetadataCommand", openAPIRef("ExamSummary"), "Edit exam name or the selected outside-LMS division date using the exam revision")
	add("get", "/v1/exams/{exam}/delete-preview", "", openAPIRef("DeletePreview"), "Review current exam revision before deletion")
	add("delete", "/v1/exams/{exam}", "RevisionCommand", openAPIObject(map[string]any{"deleted": boolean(), "exam_id": id()}, "deleted", "exam_id"), "Physical exam deletion after current preview confirmation")
	add("get", "/v1/exams/{exam}/imports/{kind}/{session}", "", openAPIRef("ImportPreview"), "Author-owned bounded preview. Pin expected_revision across pages to prevent mixing concurrent edits. Summary counts cover the whole source; error_report covers only this page. Editable retention 72h, committed recovery 7d; expiry returns 410.")
	add("put", "/v1/exams/{exam}/imports/{kind}/{session}", "ImportUpdate", openAPIRef("ImportPreview"), "Edit or exclude preview rows using preview revision")
	schemas["ImportResult"] = openAPIObject(map[string]any{"committed": boolean(), "changed": boolean(), "rating_changed": boolean(), "counts": map[string]any{"type": "object", "additionalProperties": rev()}, "id": str(), "session_id": str(), "revision": id(), "state": openAPIEnum("committed"), "total": rev(), "source_hash": hash, "expires_at": at}, "committed", "changed", "rating_changed", "counts", "id", "session_id", "revision", "state", "total", "source_hash", "expires_at")
	add("post", "/v1/exams/{exam}/imports/{kind}/{session}/commit", "ImportCommit", openAPIRef("ImportResult"), "Revalidate and commit atomically; a committed session returns its saved result")
	properties("ImportPreview")["exam_revision"] = id()
	properties("ImportPreview")["connected_division_ids"] = openAPIArray(id())
	for key, value := range map[string]any{"validation_hash": hash, "exam_id": id(), "exam_revision": id(), "kind": openAPIEnum("questions", "assignments", "outside"), "organization_id": id(), "division_id": id(), "account_id": id(), "native_command_key": map[string]any{"type": "string", "format": "uuid"}, "operation_job_id": str()} {
		properties("ImportResult")[key] = value
	}
	add("get", "/v1/exams/{exam}/imports/{kind}/{session}/receipts/{receipt}", "", openAPIRef("ImportResult"), "Read only the creator's original matching commit receipt with current import edit permissions. No query or body. Does not require a live editable preview and never applies an import. Missing or expired receipt cannot prove that no commit happened.")
	add("post", "/v1/exams/{exam}/imports/{kind}", "", openAPIRef("ImportPreview"), "Single-sheet .xlsx, at most 10 MiB and 5000 rows; questions/assignments/outside schemas depend on kind")
	paths["/v1/exams/{exam}/imports/{kind}"].(map[string]any)["post"].(map[string]any)["requestBody"] = map[string]any{"required": true, "content": map[string]any{"multipart/form-data": map[string]any{"schema": openAPIObject(map[string]any{"file": map[string]any{"type": "string", "format": "binary"}}, "file")}}}
	add("get", "/v1/exams/{exam}/imports/{kind}/template", "", nil, "Native XLSX template without real account data. Requires live Exams view and Upload view in the exam's organization and selected division.")
	paths["/v1/exams/{exam}/imports/{kind}/template"].(map[string]any)["get"].(map[string]any)["responses"].(map[string]any)["200"] = map[string]any{"description": "Single-sheet XLSX headers for this import kind", "headers": map[string]any{"Content-Disposition": map[string]any{"schema": str()}}, "content": map[string]any{"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": map[string]any{"schema": map[string]any{"type": "string", "format": "binary"}}}}
	add("get", "/v1/exams/{exam}/training", "", openAPIRef("TrainingView"), "Current-bank training with fingerprint-based personal progress; selected division must enable it")
	add("put", "/v1/exams/{exam}/training", "TrainingToggle", openAPIObject(map[string]any{"success": boolean(), "training_enabled": boolean(), "version": rev()}, "success", "training_enabled", "version"), "Administrator toggles training using division_revision")
	add("put", "/v1/exams/{exam}/training/settings", "TrainingSettingsCommand", openAPIRef("TrainingSettingsCommand"), "Persist personal study settings")
	add("put", "/v1/exams/{exam}/training/progress/{question}", "TrainingProgress", openAPIObject(map[string]any{"learned": boolean()}, "learned"), "Mark current question learned only with its current content fingerprint")
	add("delete", "/v1/exams/{exam}/training/progress/{question}", "ReadyCommand", openAPIObject(map[string]any{"learned": boolean()}, "learned"), "Remove own learned mark")
	add("get", "/internal/exams/rating-snapshot", "", openAPIObject(map[string]any{"source_revisions": map[string]any{"type": "object", "additionalProperties": rev()}, "snapshot": openAPIRef("RatingSnapshot")}, "source_revisions", "snapshot"), "Consistent immutable source snapshot for ratings; subject results only, no rating arithmetic")
	snapshot := paths["/internal/exams/rating-snapshot"].(map[string]any)["get"].(map[string]any)
	snapshot["parameters"] = []any{map[string]any{"name": "organization_id", "in": "query", "required": true, "schema": id()}, map[string]any{"name": "division_ids", "in": "query", "required": true, "description": "Comma-separated unique active division IDs, at most 100", "schema": str()}, map[string]any{"name": "date_from", "in": "query", "required": true, "schema": date}, map[string]any{"name": "date_to", "in": "query", "required": true, "schema": date}, map[string]any{"name": "as_of", "in": "query", "schema": at}}
	for path, entry := range paths {
		if !strings.HasPrefix(path, "/v1/exams") {
			continue
		}
		for method, raw := range entry.(map[string]any) {
			if strings.Contains(path, "/imports/") && !strings.HasSuffix(path, "/commit") && !strings.HasSuffix(path, "/template") && !strings.Contains(path, "/receipts/") {
				op := raw.(map[string]any)
				params := op["parameters"].([]any)
				if method != "get" {
					params = append(params, map[string]any{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "default": 100}}, map[string]any{"name": "offset", "in": "query", "schema": rev()})
				}
				if method == "get" {
					params = append(params, map[string]any{"name": "expected_revision", "in": "query", "description": "Reject 409 preview_modified if preview changed between pages", "schema": id()})
				}
				op["parameters"] = params
			}
			if method != "get" {
				continue
			}
			op := raw.(map[string]any)
			params := op["parameters"].([]any)
			if strings.HasSuffix(path, "/questions") || strings.HasSuffix(path, "/training") {
				params = append(params, map[string]any{"name": "part_id", "in": "query", "schema": id()})
			}
			if path == "/v1/exams" || strings.HasSuffix(path, "/parts") || strings.HasSuffix(path, "/questions") || strings.HasSuffix(path, "/commissions") || strings.HasSuffix(path, "/assignments") || strings.HasSuffix(path, "/privileges") || strings.HasSuffix(path, "/attempts") || strings.HasSuffix(path, "/results") || strings.HasSuffix(path, "/protocol") || strings.HasSuffix(path, "/rounds") || strings.HasSuffix(path, "/{session}") {
				params = append(params, map[string]any{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}}, map[string]any{"name": "offset", "in": "query", "schema": rev()}, map[string]any{"name": "q", "in": "query", "schema": str()})
			}
			if path == "/v1/exams" {
				params = append(params, map[string]any{"name": "type", "in": "query", "schema": openAPIEnum("classic", "outside_lms")})
				params = append(params, map[string]any{"name": "direction_id", "in": "query", "schema": id()})
				params = append(params, map[string]any{"name": "scope", "in": "query", "schema": openAPIEnum("division", "bank", "organization")}, map[string]any{"name": "sort", "in": "query", "schema": openAPIEnum("date_asc", "date_desc", "created_desc", "direction_asc")})
			}
			if path == "/v1/exams" || strings.HasSuffix(path, "/results") || strings.HasSuffix(path, "/results/{student}") {
				for _, name := range []string{"date_from", "date_to", "from_date", "to_date"} {
					params = append(params, map[string]any{"name": name, "in": "query", "description": "Inclusive current selected-division exam date; classic start uses fixed UTC+03, outside uses date. Equal canonical and legacy aliases are accepted; conflicts are 400. Filtering occurs before pagination.", "schema": date})
				}
			}
			if strings.HasSuffix(path, "/attempts") {
				params = append(params, map[string]any{"name": "status", "in": "query", "schema": openAPIEnum("pending_ready", "in_progress", "completed")})
			}
			if strings.HasSuffix(path, "/readiness") {
				params = append(params, map[string]any{"name": "assignment_id", "in": "query", "description": "Required for an examiner; only their own commission assignment is visible.", "schema": id()})
			}
			if strings.HasSuffix(path, "/protocol") || strings.HasSuffix(path, "/rounds") {
				name := "through_sequence_no"
				if strings.HasSuffix(path, "/rounds") {
					name = "through_round_no"
				}
				params = append(params, map[string]any{"name": name, "in": "query", "description": "Stable upper bound returned by the first page; send unchanged for the remaining pages", "schema": rev()})
			}
			op["parameters"] = params
		}
	}
	securitySchemes := map[string]any{"bearerAuth": map[string]any{"type": "http", "scheme": "bearer", "bearerFormat": "JWT"}, "serviceKey": map[string]any{"type": "apiKey", "in": "header", "name": "X-Service-Key"}}
	for key, header := range map[string]string{"operationGrant": operationGrantHeader, "operationDelivery": operationDeliveryHeader, "operationJob": operationJobHeader, "operationStage": operationStageHeader, "operationNativeKey": "Idempotency-Key"} {
		securitySchemes[key] = map[string]any{"type": "apiKey", "in": "header", "name": header}
	}
	return map[string]any{"openapi": "3.1.0", "info": map[string]any{"title": "CPM exams service", "version": "1.0.0", "description": "Organization exam bank; shared content and independent division sessions. Auth determines organization, selected division and permissions."}, "paths": paths, "components": map[string]any{"securitySchemes": securitySchemes, "schemas": schemas}}
}
