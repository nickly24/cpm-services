package homework

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type APIError struct {
	Code    int
	Message string
}

func (e *APIError) Error() string { return e.Message }

var missing = &APIError{404, "not_found"}
var denied = &APIError{403, "forbidden"}
var unavailable = &APIError{503, "dependency_unavailable"}

func bad(s string) error      { return &APIError{400, s} }
func conflict(s string) error { return &APIError{409, s} }

type Permission struct {
	View bool `json:"view"`
	Edit bool `json:"edit"`
}
type Actor struct {
	AccountID         int64                 `json:"account_id"`
	ProfileID         int64                 `json:"id"`
	Role              string                `json:"role"`
	OrganizationID    int64                 `json:"organization_id"`
	DivisionID        *int64                `json:"division_id"`
	GroupID           *int64                `json:"group_id"`
	OrganizationAdmin bool                  `json:"organization_admin"`
	Permissions       map[string]Permission `json:"permissions"`
}

func (a Actor) Can(section string, edit bool) bool {
	if a.OrganizationAdmin {
		return true
	}
	p := a.Permissions[section]
	return a.Role == "staff_admin" && p.View && (!edit || p.Edit)
}

type Member struct {
	AccountID int64   `json:"account_id"`
	ProfileID int64   `json:"profile_id"`
	Role      string  `json:"role"`
	FullName  string  `json:"full_name"`
	GroupID   *int64  `json:"group_id"`
	GroupName *string `json:"group_name"`
	Class     *int    `json:"class"`
}
type Group struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	ProctorAccountID *int64 `json:"proctor_account_id"`
}
type References struct {
	OrganizationID int64    `json:"organization_id"`
	DivisionID     int64    `json:"division_id"`
	Members        []Member `json:"members"`
	Groups         []Group  `json:"groups"`
}

func (ref References) Member(id int64) (Member, error) {
	for _, m := range ref.Members {
		if m.AccountID == id {
			return m, nil
		}
	}
	return Member{}, missing
}
func (ref References) Group(id int64) (Group, error) {
	for _, g := range ref.Groups {
		if g.ID == id {
			return g, nil
		}
	}
	return Group{}, missing
}
func (a Actor) Access(m Member) bool {
	return a.OrganizationAdmin || a.Role == "staff_admin" || a.Role == "student" && a.AccountID == m.AccountID || a.Role == "proctor" && a.GroupID != nil && m.GroupID != nil && *a.GroupID == *m.GroupID && m.Role == "student"
}

type Settings struct {
	MaxSourceBytes     int64 `json:"max_source_bytes"`
	MaxOutputBytes     int64 `json:"max_output_bytes"`
	MaxPages           int   `json:"max_pages"`
	DPI                int   `json:"dpi"`
	JPEGQuality        int   `json:"jpeg_quality"`
	PresignTTLSeconds  int   `json:"presign_ttl_seconds"`
	UploadStaleSeconds int   `json:"upload_stale_seconds"`
}

func DefaultSettings() Settings { return Settings{10 << 20, 10 << 20, 35, 120, 75, 300, 1800} }
func (s Settings) Validate() error {
	if s.MaxSourceBytes < 1 || s.MaxSourceBytes > 100<<20 || s.MaxOutputBytes < 1 || s.MaxOutputBytes > 100<<20 || s.MaxPages < 1 || s.MaxPages > 200 || s.DPI < 72 || s.DPI > 300 || s.JPEGQuality < 35 || s.JPEGQuality > 95 || s.PresignTTLSeconds < 60 || s.PresignTTLSeconds > 900 || s.UploadStaleSeconds < 2*s.PresignTTLSeconds || s.UploadStaleSeconds > 86400 {
		return bad("invalid_settings")
	}
	return nil
}

type TypeDTO struct {
	Name string `json:"name"`
}
type AssignmentDTO struct {
	Name      string `json:"name"`
	TypeID    int64  `json:"type_id"`
	Deadline  string `json:"deadline"`
	Published *bool  `json:"published"`
}
type UploadDTO struct {
	ClientUploadID string `json:"client_upload_id"`
}
type TransitionDTO struct {
	Message string          `json:"message"`
	Result  json.RawMessage `json:"result"`
}
type ManualDTO struct {
	AccountID int64  `json:"account_id"`
	GroupID   int64  `json:"group_id"`
	DatePass  string `json:"date_pass"`
	Result    *int   `json:"result"`
	Status    *int   `json:"status"`
}
type Event struct {
	MessageID     string    `json:"message_id"`
	MessageType   string    `json:"message_type"`
	SchemaVersion int       `json:"schema_version"`
	OccurredAt    time.Time `json:"occurred_at"`
	Producer      string    `json:"producer"`
	CorrelationID string    `json:"correlation_id"`
	Payload       any       `json:"payload"`
}
type ProcessCommand struct {
	JobID          string   `json:"job_id"`
	Generation     int      `json:"generation"`
	OrganizationID int64    `json:"organization_id"`
	DivisionID     int64    `json:"division_id"`
	SourceKey      string   `json:"source_key"`
	SourceETag     string   `json:"source_etag"`
	OutputKey      string   `json:"output_key"`
	Limits         Settings `json:"limits"`
}
type ProcessResult struct {
	JobID          string `json:"job_id"`
	Generation     int    `json:"generation"`
	OrganizationID int64  `json:"organization_id"`
	DivisionID     int64  `json:"division_id"`
	OutputKey      string `json:"output_key"`
	SizeBytes      int64  `json:"size_bytes"`
	PageCount      int    `json:"page_count"`
	SHA256         string `json:"sha256"`
	ErrorCode      string `json:"error_code"`
	ClaimToken     string `json:"claim_token"`
}

func uuid() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}
func score(v *int) (int, error) {
	if v == nil || *v < 0 || *v > 100 {
		return 0, bad("invalid_result")
	}
	return *v, nil
}

var moscow = time.FixedZone("Europe/Moscow", 3*3600)

func parseDay(raw string) (time.Time, error) {
	day, e := time.Parse("2006-01-02", raw)
	if e != nil || day.Year() < 1000 {
		return time.Time{}, bad("invalid_date")
	}
	return day, nil
}

func suggested(deadline time.Time, stamp time.Time) int {
	day := stamp.In(moscow)
	d := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, moscow)
	end := time.Date(deadline.Year(), deadline.Month(), deadline.Day(), 0, 0, 0, 0, moscow)
	late := int(d.Sub(end).Hours() / 24)
	if late < 0 {
		late = 0
	}
	r := 100 - 5*late
	if r < 0 {
		return 0
	}
	return r
}
func fileName(student, assignment string, stamp time.Time) string {
	clean := func(s string) string {
		return strings.TrimSpace(strings.Map(func(r rune) rune {
			if strings.ContainsRune("\\/:*?\"<>|\r\n", r) {
				return -1
			}
			return r
		}, s))
	}
	return fmt.Sprintf("%s — %s — %s.pdf", clean(student), clean(assignment), stamp.In(moscow).Format("02.01.2006"))
}
func validName(s string, max int) bool {
	return strings.TrimSpace(s) != "" && len([]rune(s)) <= max && !strings.ContainsAny(s, "\x00\r\n")
}
func asError(err error) *APIError {
	var e *APIError
	if errors.As(err, &e) {
		return e
	}
	return &APIError{500, "internal_error"}
}
