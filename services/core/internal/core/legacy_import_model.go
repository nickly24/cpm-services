package core

import "time"

// LegacyImportRow is the exact parsed column contract of the former user-import
// workbook. Groups are derived from ProctorName, never supplied as an ID/name.
type LegacyImportRow struct {
	Row         int    `json:"row"`
	FullName    string `json:"full_name"`
	ClassRaw    string `json:"class_raw"`
	SchoolName  string `json:"school_name"`
	ProctorName string `json:"proctor_name"`
	TGName      string `json:"tg_name"`
}
type LegacyImportDTO struct {
	Rows []LegacyImportRow `json:"rows"`
}
type LegacyImportEditDTO struct {
	ExpectedRevision int64             `json:"expected_revision"`
	Rows             []LegacyImportRow `json:"rows"`
}
type LegacyImportCommitDTO struct {
	ExpectedRevision int64 `json:"expected_revision"`
}
type LegacyImportStudent struct {
	LegacyImportRow
	PersonKey         string   `json:"person_key"`
	Class             *int     `json:"class"`
	SchoolKey         string   `json:"school_key"`
	ProctorKey        string   `json:"proctor_key"`
	GroupKey          string   `json:"group_key"`
	Action            string   `json:"action"`
	SkipReason        string   `json:"skip_reason,omitempty"`
	ExistingStudentID *int64   `json:"existing_student_id"`
	ExistingAccountID *int64   `json:"existing_account_id"`
	Errors            []string `json:"errors"`
	WithoutGroup      bool     `json:"without_group"`
}
type LegacyImportSchool struct {
	Key        string `json:"key"`
	Name       string `json:"name"`
	Action     string `json:"action"`
	ExistingID *int64 `json:"existing_id"`
}
type LegacyImportGroup struct {
	Key          string `json:"key"`
	Name         string `json:"name"`
	ProctorKey   string `json:"proctor_key"`
	Action       string `json:"action"`
	ExistingID   *int64 `json:"existing_id"`
	StudentCount int    `json:"student_count"`
}
type LegacyImportProctor struct {
	Key               string `json:"key"`
	FullName          string `json:"full_name"`
	GroupKey          string `json:"group_key"`
	Action            string `json:"action"`
	ExistingID        *int64 `json:"existing_id"`
	ExistingAccountID *int64 `json:"existing_account_id"`
	ExistingGroupID   *int64 `json:"existing_group_id"`
	StudentCount      int    `json:"student_count"`
}
type LegacyImportSummary struct {
	TotalRows            int `json:"total_rows"`
	RowErrors            int `json:"row_errors"`
	SchoolsTotal         int `json:"schools_total"`
	SchoolsCreate        int `json:"schools_create"`
	SchoolsExisting      int `json:"schools_existing"`
	GroupsTotal          int `json:"groups_total"`
	GroupsCreate         int `json:"groups_create"`
	GroupsExisting       int `json:"groups_existing"`
	ProctorsTotal        int `json:"proctors_total"`
	ProctorsCreate       int `json:"proctors_create"`
	ProctorsExisting     int `json:"proctors_existing"`
	StudentsCreate       int `json:"students_create"`
	StudentsSkip         int `json:"students_skip"`
	StudentsWithoutGroup int `json:"students_without_group"`
}
type LegacyImportPreview struct {
	Students  []LegacyImportStudent          `json:"students"`
	Schools   map[string]LegacyImportSchool  `json:"schools"`
	Groups    map[string]LegacyImportGroup   `json:"groups"`
	Proctors  map[string]LegacyImportProctor `json:"proctors"`
	Summary   LegacyImportSummary            `json:"summary"`
	CanCommit bool                           `json:"can_commit"`
}
type LegacyImportCreated struct {
	AccountID int64  `json:"account_id"`
	ProfileID int64  `json:"profile_id"`
	Role      string `json:"role"`
	FullName  string `json:"full_name"`
	Row       int    `json:"row,omitempty"`
}
type LegacyImportReport struct {
	Created           []LegacyImportCreated `json:"created"`
	CreatedStudents   int                   `json:"created_students"`
	CreatedProctors   int                   `json:"created_proctors"`
	CreatedSchools    int                   `json:"created_schools"`
	CreatedGroups     int                   `json:"created_groups"`
	SkippedStudents   int                   `json:"skipped_students"`
	CredentialsPolicy string                `json:"credentials_policy"`
}
type LegacyImportSession struct {
	ID                 int64                     `json:"id"`
	OrganizationID     int64                     `json:"organization_id"`
	DivisionID         int64                     `json:"division_id"`
	ActorID            int64                     `json:"actor_id"`
	Revision           int64                     `json:"revision"`
	MaxRows            int                       `json:"max_rows"`
	Status             string                    `json:"status"`
	Preview            LegacyImportPreview       `json:"preview"`
	Report             *LegacyImportReport       `json:"report"`
	CreatedAt          time.Time                 `json:"created_at"`
	ExpiresAt          time.Time                 `json:"expires_at"`
	AuthorizationProof *ImportAuthorizationProof `json:"authorization_proof"`
	SourceSHA256       *string                   `json:"source_sha256"`
	SourceProvenance   *string                   `json:"source_provenance"`
	OwnerRequestSHA256 *string                   `json:"owner_request_sha256"`
}

// Indexes contain only organization-owned profile projections; no credentials.
type legacyPersonMatch struct {
	AccountID, ProfileID, GroupID int64
	FullName                      string
	Active, InDivision            bool
}
type legacyNamedMatch struct {
	ID     int64
	Name   string
	Active bool
}
type legacyImportIndex struct {
	Students map[string][]legacyPersonMatch
	Proctors map[string][]legacyPersonMatch
	Schools  map[string][]legacyNamedMatch
	Groups   map[string][]legacyNamedMatch
	// A group cannot be assigned to a different existing proctor.
	GroupOwners map[int64]int64
}
