package ratings

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const Algorithm = "legacy-rating-v1"
const MaxSnapshotBytes = 32 << 20

var Moscow = func() *time.Location {
	l, e := time.LoadLocation("Europe/Moscow")
	if e != nil {
		panic(e)
	}
	return l
}()

type APIError struct {
	Code int
	Name string
}

func (e *APIError) Error() string { return e.Name }
func bad(s string) error          { return &APIError{400, s} }
func conflict(s string) error     { return &APIError{409, s} }

var missing = &APIError{404, "not_found"}
var denied = &APIError{403, "forbidden"}
var unavailable = &APIError{503, "dependency_unavailable"}

func uuid() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func validUUID(s string) bool {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return false
	}
	_, e := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	return e == nil
}
func hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func canonicalHash(raw []byte, omit string) (string, error) {
	if validateJSON(raw) != nil {
		return "", bad("invalid_json")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v map[string]any
	if e := d.Decode(&v); e != nil {
		return "", e
	}
	if d.Decode(new(any)) != io.EOF {
		return "", bad("invalid_json")
	}
	if v == nil {
		return "", bad("invalid_json")
	}
	delete(v, omit)
	return hash(v), nil
}
func strict(raw []byte, v any) error {
	if validateJSON(raw) != nil || len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return bad("invalid_json")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return bad("invalid_json")
	}
	if d.Decode(new(any)) != io.EOF {
		return bad("invalid_json")
	}
	return nil
}
func validPeriod(from, to string) bool {
	a, e := time.Parse("2006-01-02", from)
	if e != nil {
		return false
	}
	b, e := time.Parse("2006-01-02", to)
	return e == nil && a.Year() >= 1000 && !b.Before(a) && b.Sub(a) <= 365*24*time.Hour
}

type Permission struct {
	View bool `json:"view"`
	Edit bool `json:"edit"`
}
type Actor struct {
	AccountID         int64                 `json:"account_id"`
	Role              string                `json:"role"`
	OrganizationID    int64                 `json:"organization_id"`
	DivisionID        *int64                `json:"division_id"`
	GroupID           *int64                `json:"group_id"`
	OrganizationAdmin bool                  `json:"organization_admin"`
	Permissions       map[string]Permission `json:"permissions"`
}

func (a Actor) Can(edit bool) bool {
	if a.Role == "admin" && a.OrganizationAdmin {
		return true
	}
	p := a.Permissions["ratings"]
	return a.Role == "staff_admin" && p.View && (!edit || p.Edit)
}

type SnapshotRequest struct {
	OrganizationID int64  `json:"organization_id"`
	DivisionID     int64  `json:"division_id"`
	DateFrom       string `json:"date_from"`
	DateTo         string `json:"date_to"`
	AsOf           string `json:"as_of"`
}
type Account struct {
	AccountID       int64           `json:"account_id"`
	ProfileID       int64           `json:"profile_id"`
	FullName        string          `json:"full_name"`
	Class           json.RawMessage `json:"class"`
	GroupID         *int64          `json:"group_id"`
	GroupName       *string         `json:"group_name"`
	SchoolID        *int64          `json:"school_id"`
	SchoolName      *string         `json:"school_name"`
	SchoolShortName *string         `json:"school_short_name"`
	IsActive        bool            `json:"is_active"`
}
type CoreSnapshot struct {
	Source         string    `json:"source"`
	OrganizationID int64     `json:"organization_id"`
	DivisionID     int64     `json:"division_id"`
	Accounts       []Account `json:"accounts"`
	Directions     []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"directions"`
	InputHash string `json:"input_hash"`
}
type HWResult struct {
	AccountID int64    `json:"account_id"`
	Status    int      `json:"status"`
	Result    *float64 `json:"result"`
	DatePass  *string  `json:"date_pass"`
}
type Assignment struct {
	ID       int64      `json:"assignment_id"`
	Name     string     `json:"name"`
	TypeName string     `json:"type_name"`
	Deadline string     `json:"deadline"`
	Results  []HWResult `json:"results"`
}
type HomeworkSnapshot struct {
	Source           string       `json:"source"`
	OrganizationID   int64        `json:"organization_id"`
	DivisionID       int64        `json:"division_id"`
	Assignments      []Assignment `json:"assignments"`
	InputHash        string       `json:"input_hash"`
	NextTransitionAt *time.Time   `json:"next_transition_at"`
}
type TestResult struct {
	AccountID    int64    `json:"account_id"`
	SubmissionID string   `json:"submission_id"`
	Revision     int64    `json:"calculation_revision"`
	Status       string   `json:"calculation_status"`
	Score        *float64 `json:"score"`
}
type Test struct {
	ID             string       `json:"test_id"`
	Title          string       `json:"title"`
	DirectionID    *int64       `json:"direction_id"`
	DirectionName  string       `json:"direction_name"`
	StartAt        string       `json:"start_at"`
	Published      bool         `json:"published"`
	IsArchived     bool         `json:"is_archived"`
	SourceRevision int64        `json:"source_revision,omitempty"`
	Results        []TestResult `json:"results"`
}
type TestsSnapshot struct {
	Source                string          `json:"source"`
	OrganizationID        int64           `json:"organization_id"`
	DivisionID            int64           `json:"division_id"`
	Tests                 []Test          `json:"tests"`
	InputHash             string          `json:"input_hash"`
	ExternalTests         []Test          `json:"external_tests"`
	ExternalSourceVersion string          `json:"external_source_version,omitempty"`
	ExternalDomain        *ExternalDomain `json:"external_domain,omitempty"`
}
type ExternalDomain struct {
	SchemaVersion  int    `json:"schema_version"`
	Status         string `json:"status"`
	OrganizationID int64  `json:"organization_id"`
	DivisionID     int64  `json:"division_id"`
}
type ExamResult struct {
	AccountID     int64    `json:"account_id"`
	Score         *float64 `json:"score"`
	AttemptNo     *int     `json:"attempt_no"`
	HasAppeal     bool     `json:"has_appeal"`
	MissingResult bool     `json:"missing_result"`
}
type Exam struct {
	ID          string       `json:"exam_id"`
	Name        string       `json:"exam_name"`
	Date        string       `json:"exam_date"`
	Type        string       `json:"exam_type"`
	DirectionID *int64       `json:"direction_id"`
	Results     []ExamResult `json:"results"`
}
type ExamsSnapshot struct {
	Source           string     `json:"source"`
	OrganizationID   int64      `json:"organization_id"`
	DivisionID       int64      `json:"division_id"`
	Exams            []Exam     `json:"exams"`
	InputHash        string     `json:"input_hash"`
	NextTransitionAt *time.Time `json:"next_transition_at"`
	SourceRevision   int64      `json:"source_revision,omitempty"`
}
type Snapshot struct {
	Core              CoreSnapshot     `json:"core"`
	Homework          HomeworkSnapshot `json:"homework"`
	Tests             TestsSnapshot    `json:"tests"`
	Exams             ExamsSnapshot    `json:"exams"`
	ExternalTestsMode string           `json:"external_tests_mode"`
	ExamsMode         string           `json:"exams_mode"`
}
type PersonalHW struct {
	ID       int64    `json:"assignment_id"`
	Name     string   `json:"name"`
	TypeName string   `json:"type_name"`
	Deadline string   `json:"deadline"`
	Status   int      `json:"status"`
	Result   *float64 `json:"result"`
	DatePass *string  `json:"date_pass"`
}
type PersonalTest struct {
	ID        string   `json:"test_id"`
	Title     string   `json:"title"`
	Direction string   `json:"direction"`
	Source    string   `json:"source"`
	Status    string   `json:"calculation_status"`
	Score     *float64 `json:"score"`
}
type PersonalExam struct {
	ID            string   `json:"exam_id"`
	Name          string   `json:"exam_name"`
	Date          string   `json:"exam_date"`
	Type          string   `json:"exam_type"`
	DirectionID   *int64   `json:"direction_id"`
	AttemptNo     *int     `json:"attempt_no"`
	HasAppeal     bool     `json:"has_appeal"`
	MissingResult bool     `json:"missing_result"`
	Score         *float64 `json:"score"`
}
type Input struct {
	JobID          string         `json:"job_id"`
	Revision       int64          `json:"revision"`
	OrganizationID int64          `json:"organization_id"`
	DivisionID     int64          `json:"division_id"`
	AccountID      int64          `json:"account_id"`
	InputHash      string         `json:"input_hash"`
	Algorithm      string         `json:"algorithm_version"`
	DateFrom       string         `json:"date_from"`
	DateTo         string         `json:"date_to"`
	AsOf           string         `json:"as_of"`
	Homework       []PersonalHW   `json:"homework"`
	Tests          []PersonalTest `json:"tests"`
	Exams          []PersonalExam `json:"exams"`
}
type Job struct {
	ID             string          `json:"id"`
	OrganizationID int64           `json:"organization_id"`
	DivisionID     int64           `json:"division_id"`
	Revision       int64           `json:"revision"`
	Status         string          `json:"status"`
	DateFrom       string          `json:"date_from"`
	DateTo         string          `json:"date_to"`
	AsOf           time.Time       `json:"as_of"`
	CreatedBy      int64           `json:"created_by"`
	Total          int             `json:"total_count"`
	Processed      int             `json:"processed_count"`
	Error          string          `json:"error_code"`
	Attempts       int             `json:"attempts"`
	CreatedAt      time.Time       `json:"created_at"`
	CompletedAt    *time.Time      `json:"completed_at"`
	Snapshot       json.RawMessage `json:"-"`
	SourceHash     *string         `json:"-"`
	LeaseToken     *string         `json:"-"`
}
type Result struct {
	JobID          string          `json:"job_id"`
	Revision       int64           `json:"revision"`
	OrganizationID int64           `json:"organization_id"`
	DivisionID     int64           `json:"division_id"`
	AccountID      int64           `json:"account_id"`
	InputHash      string          `json:"input_hash"`
	Algorithm      string          `json:"algorithm_version"`
	Status         string          `json:"status"`
	ErrorCode      string          `json:"error_code,omitempty"`
	Homework       json.RawMessage `json:"homework,omitempty"`
	Tests          json.RawMessage `json:"tests,omitempty"`
	Exams          json.RawMessage `json:"exams,omitempty"`
	Final          *float64        `json:"final_rating,omitempty"`
}

func personal(j Job, s Snapshot, a Account) (Input, error) {
	in := Input{JobID: j.ID, Revision: j.Revision, OrganizationID: j.OrganizationID, DivisionID: j.DivisionID, AccountID: a.AccountID, Algorithm: Algorithm, DateFrom: j.DateFrom, DateTo: j.DateTo, AsOf: j.AsOf.Format(time.RFC3339Nano), Homework: []PersonalHW{}, Tests: []PersonalTest{}, Exams: []PersonalExam{}}
	for _, h := range s.Homework.Assignments {
		p := PersonalHW{ID: h.ID, Name: h.Name, TypeName: h.TypeName, Deadline: h.Deadline}
		for _, r := range h.Results {
			if r.AccountID == a.AccountID {
				p.Status = r.Status
				p.Result = r.Result
				p.DatePass = r.DatePass
				break
			}
		}
		in.Homework = append(in.Homework, p)
	}
	for n, tt := range [][]Test{s.Tests.Tests, s.Tests.ExternalTests} {
		for _, t := range tt {
			p := PersonalTest{ID: t.ID, Title: t.Title, Direction: t.DirectionName, Source: "internal", Status: "missing"}
			if n == 1 {
				p.Source = "external"
			}
			for _, r := range t.Results {
				if r.AccountID == a.AccountID {
					p.Status = r.Status
					p.Score = r.Score
					break
				}
			}
			if p.Status != "missing" && p.Status != "ready" {
				return in, conflict("tests_calculation_pending")
			}
			if p.Status == "ready" && p.Score == nil {
				return in, conflict("tests_calculation_pending")
			}
			in.Tests = append(in.Tests, p)
		}
	}
	for _, e := range s.Exams.Exams {
		p := PersonalExam{ID: e.ID, Name: e.Name, Date: e.Date, Type: e.Type, DirectionID: e.DirectionID, MissingResult: true}
		for _, r := range e.Results {
			if r.AccountID == a.AccountID {
				p.Score = r.Score
				p.AttemptNo = r.AttemptNo
				p.HasAppeal = r.HasAppeal
				p.MissingResult = r.MissingResult
				break
			}
		}
		in.Exams = append(in.Exams, p)
	}
	raw, _ := json.Marshal(in)
	in.InputHash, _ = canonicalHash(raw, "input_hash")
	return in, nil
}
func errorName(err error) string {
	var a *APIError
	if errors.As(err, &a) {
		return a.Name
	}
	return "ratings_operation_failed"
}
