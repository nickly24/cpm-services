package bff

import "encoding/json"

type studentImportIssue struct {
	Row    int    `json:"row"`
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

func studentValidationResponse(raw []byte) any {
	var v struct {
		Error  string               `json:"error"`
		Issues []studentImportIssue `json:"issues"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Error != "student_import_validation" || len(v.Issues) == 0 || len(v.Issues) > 512 {
		return nil
	}
	fields := map[string]bool{"full_name": true, "profile_id": true, "school": true, "class": true, "login": true}
	reasons := map[string]bool{"name_required_or_too_long": true, "invalid_login": true, "invalid_class": true, "school_not_found": true, "school_ambiguous": true, "duplicate_login": true, "login_exists": true, "invalid_profile_id": true, "duplicate_profile_id": true, "profile_id_exists": true, "duplicate_row": true}
	for _, x := range v.Issues {
		if x.Row < 2 || x.Row > 10000000 || !fields[x.Field] || !reasons[x.Reason] {
			return nil
		}
	}
	return map[string]any{"status": false, "error": v.Error, "issues": v.Issues}
}
