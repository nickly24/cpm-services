package uploads

import (
	"encoding/json"
	"time"
)

type Permission struct {
	View bool `json:"view"`
	Edit bool `json:"edit"`
}
type Actor struct {
	AccountID         int64                 `json:"account_id"`
	OrganizationID    int64                 `json:"organization_id"`
	DivisionID        *int64                `json:"division_id"`
	Role              string                `json:"role"`
	OrganizationAdmin bool                  `json:"organization_admin"`
	Permissions       map[string]Permission `json:"permissions"`
}

func (a Actor) Can(section string, edit bool) bool {
	if a.OrganizationAdmin && a.Role == "admin" {
		return true
	}
	p := a.Permissions[section]
	return a.Role == "staff_admin" && p.View && (!edit || p.Edit)
}

type Scope struct {
	OrganizationID int64 `json:"organization_id"`
	DivisionID     int64 `json:"division_id"`
	ActorID        int64 `json:"actor_account_id"`
}
type Type struct {
	ID          string   `json:"type"`
	Owner       string   `json:"owner"`
	Permission  string   `json:"permission"`
	Formats     []string `json:"formats"`
	Mode        string   `json:"mode"`
	TargetScope string   `json:"target_scope"`
	MaxBytes    int64    `json:"max_bytes,omitempty"`
	MaxRows     int      `json:"max_rows,omitempty"`
	Create      bool     `json:"create"`
	Edit        bool     `json:"edit"`
	Commit      bool     `json:"commit"`
	Dependency  string   `json:"dependency,omitempty"`
	Template    bool     `json:"template"`
}

func Catalog() []Type {
	return []Type{
		{"users.credentials", "core", "users", []string{"csv", "xlsx"}, "owner_session", "division", 8 << 20, 100, true, false, true, "", true},
		{"users.legacy", "core", "users", []string{"xlsx"}, "owner_session", "division + organization catalogs", 8 << 20, 100, true, true, true, "Native Core max_rows=100; legacy workbook mapping stays in Core", true},
		{"cards.xlsx", "cards", "train", []string{"xlsx"}, "owner_session", "organization", 8 << 20, 1000, true, true, true, "", true},
		{"cards.transform", "cards", "train", []string{"json"}, "owner_session", "organization cards -> division test draft", 1 << 20, 200, true, false, true, "", false},
		{"tests.json", "tests", "tests", []string{"json", "js"}, "validated_input", "division", 8 << 20, 200, true, true, true, "Tests validates at most 200 questions; normalized owner DTO bounded to 1 MiB", true},
		{"tests.external-results", "tests", "test-results", []string{"xlsx"}, "validated_input", "division", 8 << 20, 5000, true, true, true, "Tests validates all rows atomically; owner DTO bounded to 1 MiB", true},
		{"exams.questions", "exams", "exams", []string{"xlsx"}, "owner_session", "division", 8 << 20, 5000, true, true, true, "", true},
		{"exams.assignments", "exams", "exams", []string{"xlsx"}, "owner_session", "division", 8 << 20, 5000, true, true, true, "", true},
		{"exams.outside", "exams", "exams", []string{"xlsx"}, "owner_session", "division", 8 << 20, 5000, true, true, true, "", true},
		{"homework.pdf", "homework", "assignments", []string{"pdf"}, "owner_direct_s3", "division + own submission", 0, 0, false, false, false, "Upload/complete/retry/cancel/submit/review belong to Homework; follow its existing UI", false},
		{"attendance.zap-images", "attendance", "zaps", []string{"png", "jpeg"}, "owner_attachment", "division + own zap", 0, 0, false, false, false, "Register existing owner attachment; create remains attendance zap command", false},
	}
}
func findType(id string) (Type, error) {
	for _, t := range Catalog() {
		if t.ID == id {
			return t, nil
		}
	}
	return Type{}, api(400, "unsupported_type")
}

type Session struct {
	ID string `json:"id"`
	Scope
	Type           string          `json:"type"`
	Owner          string          `json:"owner"`
	OwnerSessionID string          `json:"owner_session_id,omitempty"`
	TargetID       string          `json:"target_id,omitempty"`
	Revision       int64           `json:"revision"`
	OwnerRevision  int64           `json:"owner_revision"`
	State          string          `json:"state"`
	FileID         string          `json:"file_id,omitempty"`
	SourceHash     string          `json:"source_hash"`
	Preview        json.RawMessage `json:"preview,omitempty"`
	Expires        time.Time       `json:"expires_at"`
	Created        time.Time       `json:"created_at"`
}
type Job struct {
	ID string `json:"id"`
	Scope
	SessionID     string                    `json:"session_id"`
	Type          string                    `json:"type"`
	Owner         string                    `json:"owner"`
	Status        string                    `json:"status"`
	Attempt       int                       `json:"attempt"`
	Processed     int                       `json:"processed"`
	Total         int                       `json:"total"`
	Error         string                    `json:"error,omitempty"`
	Report        json.RawMessage           `json:"report,omitempty"`
	Created       time.Time                 `json:"created_at"`
	Capabilities  *JobCapabilities          `json:"capabilities,omitempty"`
	Authorization *JobAuthorizationMetadata `json:"authorization,omitempty"`
}
type JobAuthorizationMetadata struct {
	Bound     bool       `json:"bound"`
	Mode      string     `json:"mode"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}
type JobCapabilities struct {
	RetryDelivery       bool `json:"retry_delivery"`
	RecoverReceipt      bool `json:"recover_receipt"`
	RevokeAuthorization bool `json:"revoke_authorization"`
	RetryConfirmation   bool `json:"retry_confirmation"`
}
type File struct {
	ID string `json:"id"`
	Scope
	Type      string    `json:"type"`
	Owner     string    `json:"owner"`
	OwnerID   string    `json:"owner_reference,omitempty"`
	TargetID  string    `json:"target_id,omitempty"`
	Hash      string    `json:"sha256,omitempty"`
	Size      int64     `json:"size_bytes"`
	Extension string    `json:"extension"`
	Expires   time.Time `json:"expires_at"`
}
type RevisionDTO struct {
	ExpectedRevision int64 `json:"expected_revision"`
}
type EditDTO struct {
	ExpectedRevision int64           `json:"expected_revision"`
	OwnerPayload     json.RawMessage `json:"owner_payload"`
}
type CreateDTO struct {
	Type         string          `json:"type"`
	TargetID     string          `json:"target_id,omitempty"`
	OwnerPayload json.RawMessage `json:"owner_payload"`
}
type RegisterDTO struct {
	Type           string `json:"type"`
	OwnerReference string `json:"owner_reference"`
	TargetID       string `json:"target_id,omitempty"`
}
