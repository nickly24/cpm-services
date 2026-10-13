package uploads

import (
	"encoding/json"
	"strconv"
	"strings"
)

func verifyOperationRoute(p privateExecutionPlan) error {
	var b operationBinding
	if json.Unmarshal(p.BindingBody, &b) != nil || b.ContractVersion != 1 || b.JobID != p.JobID || b.Owner != p.Command.Owner || b.Type != p.Command.Type || b.NativeCommandKey != p.Command.NativeKey ||
		b.ApplyWireBodySHA256 != digest(p.Command.Body) || !canonicalUUID(b.NativeCommandKey) {
		return api(503, "execution_plan_integrity_failed")
	}
	t, err := findType(b.Type)
	if err != nil || t.Owner != b.Owner || b.Type == "cards.transform" {
		return api(503, "execution_plan_integrity_failed")
	}
	var target struct {
		Kind       string `json:"kind"`
		DivisionID string `json:"division_id"`
		ExamID     string `json:"exam_id"`
	}
	if json.Unmarshal(b.Target, &target) != nil {
		return api(503, "execution_plan_integrity_failed")
	}
	path := ""
	switch b.Type {
	case "users.credentials", "users.legacy":
		if b.OwnerSessionID == nil || !canonicalPositiveID(*b.OwnerSessionID) || target.Kind != "division_users" || target.DivisionID != strconv.FormatInt(p.Scope.DivisionID, 10) {
			return api(503, "execution_plan_integrity_failed")
		}
		kind := "imports"
		if b.Type == "users.legacy" {
			kind = "legacy-imports"
		}
		path = "/v1/core/divisions/" + target.DivisionID + "/" + kind + "/" + *b.OwnerSessionID + "/commit"
	case "cards.xlsx":
		if b.OwnerSessionID == nil || !lowerHexReference(*b.OwnerSessionID, 32) || target.Kind != "existing_theme" && target.Kind != "create_theme" {
			return api(503, "execution_plan_integrity_failed")
		}
		path = "/v1/cards/sessions/" + *b.OwnerSessionID + "/commit"
	case "tests.json":
		if b.OwnerSessionID != nil || target.Kind != "create_tests" {
			return api(503, "execution_plan_integrity_failed")
		}
		path = "/internal/tests/imports/json/commit"
	case "tests.external-results":
		if b.OwnerSessionID != nil || target.Kind != "external_test" {
			return api(503, "execution_plan_integrity_failed")
		}
		path = "/internal/tests/external-results/commit"
	case "exams.questions", "exams.assignments", "exams.outside":
		if b.OwnerSessionID == nil || !canonicalUUID(*b.OwnerSessionID) || target.Kind != "exam" || !canonicalPositiveID(target.ExamID) {
			return api(503, "execution_plan_integrity_failed")
		}
		path = "/v1/exams/" + target.ExamID + "/imports/" + strings.TrimPrefix(b.Type, "exams.") + "/" + *b.OwnerSessionID + "/commit"
	default:
		return api(503, "execution_plan_integrity_failed")
	}
	if p.Command.Method != "POST" || p.Command.Path != path {
		return api(503, "execution_plan_integrity_failed")
	}
	return nil
}
