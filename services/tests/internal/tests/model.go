package tests

import (
	"go.mongodb.org/mongo-driver/v2/bson"
	"net/http"
	"time"
)

type Permission struct {
	View bool `json:"view"`
	Edit bool `json:"edit"`
}
type Actor struct {
	DivisionIDs       []int64               `json:"division_ids"`
	AccountID         int64                 `json:"account_id"`
	Role              string                `json:"role"`
	OrganizationID    int64                 `json:"organization_id"`
	DivisionID        *int64                `json:"division_id"`
	OrganizationAdmin bool                  `json:"organization_admin"`
	Permissions       map[string]Permission `json:"permissions"`
}

func (a Actor) Can(edit bool) bool {
	if a.Role == "admin" && a.OrganizationAdmin {
		return true
	}
	p := a.Permissions["tests"]
	return a.Role == "staff_admin" && p.View && (!edit || p.Edit)
}
func (a Actor) InDivision(d int64) bool {
	return a.Role == "admin" && a.OrganizationAdmin || a.DivisionID != nil && *a.DivisionID == d
}

type Settings struct {
	Published        bool       `json:"published" bson:"published"`
	StartAt          *time.Time `json:"start_at" bson:"start_at"`
	EndAt            *time.Time `json:"end_at" bson:"end_at"`
	TimeLimitMinutes int        `json:"time_limit_minutes" bson:"time_limit_minutes"`
	PassingScore     *float64   `json:"passing_score" bson:"passing_score"`
	AnswersVisible   bool       `json:"answers_visible" bson:"answers_visible"`
}
type Placement struct {
	DivisionID int64     `json:"division_id" bson:"division_id"`
	Settings   Settings  `json:"settings" bson:"settings"`
	AddedBy    int64     `json:"added_by" bson:"added_by"`
	AddedAt    time.Time `json:"added_at" bson:"added_at"`
	UpdatedBy  int64     `json:"updated_by" bson:"updated_by"`
	UpdatedAt  time.Time `json:"updated_at" bson:"updated_at"`
}
type Origin struct {
	TestID         bson.ObjectID `json:"test_id" bson:"test_id"`
	SourceRevision int64         `json:"source_revision" bson:"source_revision"`
	DivisionID     int64         `json:"division_id" bson:"division_id"`
	CopiedAt       time.Time     `json:"copied_at" bson:"copied_at"`
}
type Document struct {
	ID                bson.ObjectID       `json:"id" bson:"_id"`
	OrganizationID    int64               `json:"organization_id" bson:"organization_id"`
	OwnerDivisionID   int64               `json:"owner_division_id" bson:"owner_division_id"`
	DirectionID       int64               `json:"direction_id" bson:"direction_id"`
	Title             string              `json:"title" bson:"title"`
	Description       string              `json:"description" bson:"description"`
	BankAvailable     bool                `json:"bank_available" bson:"bank_available"`
	Archived          bool                `json:"is_archived" bson:"is_archived"`
	Revision          int64               `json:"revision" bson:"revision"`
	Origin            *Origin             `json:"origin" bson:"origin"`
	Placements        []Placement         `json:"placements" bson:"placements"`
	CreatedBy         int64               `json:"created_by" bson:"created_by"`
	CreatedAt         time.Time           `json:"created_at" bson:"created_at"`
	UpdatedBy         int64               `json:"updated_by" bson:"updated_by"`
	UpdatedAt         time.Time           `json:"updated_at" bson:"updated_at"`
	OperationHash     string              `json:"-" bson:"operation_hash"`
	Questions         []Question          `json:"-" bson:"questions"`
	ContentVersionID  bson.ObjectID       `json:"content_version_id" bson:"content_version_id"`
	MaxQuestionID     int64               `json:"-" bson:"max_question_id"`
	UsedOptionIDs     map[string][]string `json:"-" bson:"used_option_ids"`
	HistoryVersionIDs []bson.ObjectID     `json:"-" bson:"history_version_ids"`
}

func (d Document) Placement(id int64) *Placement {
	for i := range d.Placements {
		if d.Placements[i].DivisionID == id {
			return &d.Placements[i]
		}
	}
	return nil
}
func (d Document) Readable(a Actor) bool {
	if d.OrganizationID != a.OrganizationID || !a.Can(false) {
		return false
	}
	return a.OrganizationAdmin || d.BankAvailable && !d.Archived || a.DivisionID != nil && d.Placement(*a.DivisionID) != nil
}
func (d Document) Editable(a Actor) bool {
	return d.OrganizationID == a.OrganizationID && a.Can(true) && a.InDivision(d.OwnerDivisionID)
}

type CreateDTO struct {
	Title       string     `json:"title"`
	Description string     `json:"description"`
	DirectionID int64      `json:"direction_id"`
	Questions   []Question `json:"questions,omitempty"`
}
type MetadataDTO struct {
	ExpectedRevision int64 `json:"expected_revision"`
	CreateDTO
}
type RevisionDTO struct {
	ExpectedRevision int64 `json:"expected_revision"`
}
type BankDTO struct {
	ExpectedRevision int64 `json:"expected_revision"`
	Available        *bool `json:"available"`
}
type ArchiveDTO struct {
	ExpectedRevision int64 `json:"expected_revision"`
	Archived         *bool `json:"is_archived"`
}
type CopyDTO struct {
	ExpectedRevision int64  `json:"expected_revision"`
	Title            string `json:"title"`
}
type SettingsDTO struct {
	ExpectedRevision int64 `json:"expected_revision"`
	Settings
}
type APIError struct {
	Code    int
	Message string
}

func (e *APIError) Error() string { return e.Message }
func bad(s string) error          { return &APIError{400, s} }

var denied = &APIError{http.StatusForbidden, "access_denied"}
var missing = &APIError{http.StatusNotFound, "not_found"}
var unavailable = &APIError{http.StatusServiceUnavailable, "dependency_unavailable"}

func conflict(s string) error { return &APIError{409, s} }
