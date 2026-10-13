package auth

import (
	"errors"
	"github.com/golang-jwt/jwt/v5"
	"time"
)

var ErrCredentials = errors.New("invalid_credentials")
var ErrForbidden = errors.New("context_forbidden")
var ErrToken = errors.New("invalid_token")

var Sections = []string{"dashboard", "organizations", "divisions", "groups", "directions", "users", "schools", "upload", "schedule", "assignments", "review-queue", "homework-archive", "monitoring", "tests", "test-results", "exams", "attendance", "scan", "zaps", "train", "ratings"}
var profileTables = map[string]string{"student": "students", "proctor": "proctors", "examinator": "examinators", "admin": "admins", "supervisor": "supervisors", "staff_admin": "admin_role_users"}

type Permission struct {
	View bool `json:"view"`
	Edit bool `json:"edit"`
}
type Division struct {
	ID          int64                 `json:"id"`
	Name        string                `json:"name"`
	GroupID     *int64                `json:"group_id"`
	RoleID      *int64                `json:"role_id"`
	RoleName    string                `json:"role_name,omitempty"`
	Permissions map[string]Permission `json:"permissions"`
}
type Organization struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Divisions []Division `json:"divisions"`
}
type Account struct {
	UserID               int64
	EntityID             int64
	Username             string
	Password             string
	Role                 string
	FullName             string
	AuthVersion          int64
	LegacySessionVersion int64
	Organizations        []Organization
}
type ContextDTO struct {
	OrganizationID int64  `json:"organization_id"`
	DivisionID     *int64 `json:"division_id"`
}
type LoginDTO struct {
	Login          string `json:"login"`
	Password       string `json:"password"`
	OrganizationID *int64 `json:"organization_id,omitempty"`
	DivisionID     *int64 `json:"division_id,omitempty"`
}
type RefreshDTO struct {
	RefreshToken string `json:"refresh_token"`
}
type ErrorDTO struct {
	Status bool   `json:"status"`
	Error  string `json:"error"`
}
type StatusDTO struct {
	Status bool `json:"status"`
}
type TokenResponseDTO struct {
	Status        bool           `json:"status"`
	Token         string         `json:"token"`
	AccessToken   string         `json:"access_token"`
	TokenType     string         `json:"token_type"`
	ExpiresIn     int64          `json:"expires_in"`
	RefreshToken  string         `json:"refresh_token,omitempty"`
	User          UserDTO        `json:"user"`
	Organizations []Organization `json:"organizations"`
}
type IdentityResponseDTO struct {
	Status         bool                  `json:"status"`
	Active         bool                  `json:"active"`
	User           UserDTO               `json:"user"`
	Organizations  []Organization        `json:"organizations"`
	Role           string                `json:"role"`
	EntityID       int64                 `json:"entity_id"`
	FullName       string                `json:"full_name"`
	GroupID        *int64                `json:"group_id"`
	OrganizationID int64                 `json:"organization_id"`
	DivisionID     *int64                `json:"division_id"`
	DivisionIDs    []int64               `json:"division_ids"`
	RoleID         *int64                `json:"role_id"`
	RoleName       string                `json:"role_name"`
	Permissions    map[string]Permission `json:"permissions"`
}
type UserDTO struct {
	ID                int64                 `json:"id"`
	AccountID         int64                 `json:"account_id"`
	Role              string                `json:"role"`
	FullName          string                `json:"full_name"`
	GroupID           *int64                `json:"group_id"`
	OrganizationID    int64                 `json:"organization_id"`
	DivisionID        *int64                `json:"division_id"`
	DivisionIDs       []int64               `json:"division_ids"`
	OrganizationAdmin bool                  `json:"organization_admin"`
	RoleID            *int64                `json:"role_id"`
	RoleName          string                `json:"role_name,omitempty"`
	Permissions       map[string]Permission `json:"permissions"`
}
type Claims struct {
	jwt.RegisteredClaims
	UserDTO
	SessionID      string `json:"sid"`
	ContextVersion int64  `json:"context_version"`
	AuthVersion    int64  `json:"auth_version"`
	SessionVersion int64  `json:"session_version,omitempty"`
}
type Session struct {
	ContextVersion       int64
	ID                   string
	UserID               int64
	AuthVersion          int64
	LegacySessionVersion int64
	Context              ContextDTO
	ExpiresAt            time.Time
}

func (a Account) User(ctx ContextDTO) (UserDTO, error) {
	u := UserDTO{ID: a.EntityID, AccountID: a.UserID, Role: a.Role, FullName: a.FullName,
		OrganizationID: ctx.OrganizationID, DivisionID: ctx.DivisionID,
		DivisionIDs: []int64{}, Permissions: map[string]Permission{}, OrganizationAdmin: a.Role == "admin"}
	for _, o := range a.Organizations {
		if o.ID != ctx.OrganizationID {
			continue
		}
		found := ctx.DivisionID == nil && a.Role == "admin"
		for _, d := range o.Divisions {
			u.DivisionIDs = append(u.DivisionIDs, d.ID)
			if ctx.DivisionID != nil && *ctx.DivisionID == d.ID {
				found = true
				u.GroupID = d.GroupID
				u.RoleID = d.RoleID
				u.RoleName = d.RoleName
				u.Permissions = d.Permissions
			}
		}
		if !found {
			return UserDTO{}, ErrForbidden
		}
		if a.Role == "admin" {
			for _, s := range Sections {
				u.Permissions[s] = Permission{true, true}
			}
		}
		return u, nil
	}
	return UserDTO{}, ErrForbidden
}

func (a Account) DefaultContext() (ContextDTO, error) {
	for _, o := range a.Organizations {
		if len(o.Divisions) > 0 {
			id := o.Divisions[0].ID
			return ContextDTO{o.ID, &id}, nil
		}
		if a.Role == "admin" {
			return ContextDTO{OrganizationID: o.ID}, nil
		}
	}
	return ContextDTO{}, ErrForbidden
}
