package core

import "net/http"

type Permission struct {
	View bool `json:"view"`
	Edit bool `json:"edit"`
}
type Actor struct {
	ID                int64                 `json:"id"`
	AccountID         int64                 `json:"account_id"`
	Role              string                `json:"role"`
	OrganizationID    int64                 `json:"organization_id"`
	DivisionID        *int64                `json:"division_id"`
	GroupID           *int64                `json:"group_id"`
	OrganizationAdmin bool                  `json:"organization_admin"`
	Permissions       map[string]Permission `json:"permissions"`
}
type Identity struct {
	Status bool  `json:"status"`
	Active bool  `json:"active"`
	User   Actor `json:"user"`
}
type NameDTO struct {
	Name string `json:"name"`
}
type SchoolDTO struct {
	Name      string `json:"name"`
	ShortName string `json:"short_name"`
	Notes     string `json:"notes"`
	Active    bool   `json:"is_active"`
}
type UserDTO struct {
	ProfileID     *int64 `json:"profile_id,omitempty"`
	classSupplied bool
	Role          string `json:"role"`
	FullName      string `json:"full_name"`
	Class         int    `json:"class"`
	TGName        string `json:"tg_name"`
	SchoolID      *int64 `json:"school_id"`
	GroupID       *int64 `json:"group_id"`
	RoleID        *int64 `json:"role_id"`
	Login         string `json:"login"`
	Password      string `json:"password"`
}
type ProfileDTO struct {
	classSupplied bool
	FullName      string `json:"full_name"`
	Class         int    `json:"class"`
	TGName        string `json:"tg_name"`
	SchoolID      *int64 `json:"school_id"`
}
type CredentialsDTO struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}
type ActiveDTO struct {
	Active *bool `json:"is_active"`
}
type GrantDTO struct {
	RoleID *int64 `json:"role_id"`
}
type GroupMemberDTO struct {
	AccountID int64 `json:"account_id"`
}
type RoleDTO struct {
	Name        string                `json:"name"`
	Permissions map[string]Permission `json:"permissions"`
}
type LessonDTO struct {
	Date      string `json:"date"`
	Start     string `json:"start_time"`
	End       string `json:"end_time"`
	Name      string `json:"lesson_name"`
	Teacher   string `json:"teacher_name"`
	Location  string `json:"location"`
	Classroom string `json:"classroom"`
	Color     string `json:"color"`
	Changed   bool   `json:"is_changed"`
	InPerson  bool   `json:"is_in_person"`
	ForAll    bool   `json:"is_for_all"`
	Public    bool   `json:"is_public"`
	SchoolID  *int64 `json:"school_id"`
}
type ClassDayDTO struct {
	Date    string `json:"date"`
	Comment string `json:"comment"`
}
type AttendanceDTO struct {
	AccountID int64 `json:"account_id"`
	TypeID    int64 `json:"attendance_type_id"`
}
type ImageDTO struct {
	MIME string `json:"mime"`
	Data string `json:"data"`
}
type ZapDTO struct {
	AccountID int64      `json:"account_id"`
	Text      string     `json:"text"`
	Dates     []string   `json:"dates"`
	Images    []ImageDTO `json:"images"`
}
type ZapProcessDTO struct {
	Status string `json:"status"`
	Answer string `json:"answer"`
}
type ImportDTO struct {
	Users []UserDTO `json:"users"`
}
type APIError struct {
	Code    int
	Message string
}

func (e *APIError) Error() string { return e.Message }
func bad(message string) error    { return &APIError{400, message} }

var denied = &APIError{http.StatusForbidden, "access_denied"}
var missing = &APIError{http.StatusNotFound, "not_found"}
var conflict = &APIError{http.StatusConflict, "conflict"}
var unavailable = &APIError{http.StatusServiceUnavailable, "dependency_unavailable"}
