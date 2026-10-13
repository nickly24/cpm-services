package exams

import (
	"encoding/json"
	"net/http"
	"time"
)

type APIError struct {
	Status int
	Code   string
}

func (e *APIError) Error() string { return e.Code }
func Bad(code string) error       { return &APIError{400, code} }
func Conflict(code string) error  { return &APIError{409, code} }

var Missing = &APIError{404, "not_found"}
var Denied = &APIError{403, "forbidden"}

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
	DivisionIDs       []int64               `json:"division_ids"`
	OrganizationAdmin bool                  `json:"organization_admin"`
	Permissions       map[string]Permission `json:"permissions"`
	FullName          string                `json:"full_name"`
}

func (a Actor) Can(edit bool) bool {
	if a.Role == "admin" && a.OrganizationAdmin {
		return true
	}
	if a.Role != "staff_admin" {
		return false
	}
	p := a.Permissions["exams"]
	if edit {
		return p.Edit
	}
	return p.View || p.Edit
}

type Params struct {
	DivisionID, PartID, QuestionID, CommissionID, AssignmentID, AttemptID, StudentID, ResultID, PresentedID int64
	Query                                                                                                   map[string]string
	Now                                                                                                     time.Time
	Members                                                                                                 map[int64]Member
}
type Member struct {
	AccountID   int64   `json:"account_id"`
	FullName    string  `json:"full_name"`
	Role        string  `json:"role"`
	Active      bool    `json:"active"`
	Login       string  `json:"login"`
	DivisionIDs []int64 `json:"division_ids"`
}
type Config struct {
	StartAt              *time.Time  `json:"start_at"`
	EndAt                *time.Time  `json:"end_at"`
	FractionalMode       string      `json:"fractional_mode"`
	TieBreakerSourceMode string      `json:"tie_breaker_source_mode"`
	TieBreakerPartID     int64       `json:"tie_breaker_part_id"`
	TieBreakerHalfMode   string      `json:"tie_breaker_half_mode"`
	Thresholds           []Threshold `json:"thresholds"`
	Revision             int64       `json:"revision"`
}
type Threshold struct {
	Grade    int `json:"grade"`
	MinScore int `json:"min_score"`
}
type Part struct {
	ID       int64  `json:"id"`
	Code     string `json:"code"`
	Weight   int    `json:"weight"`
	Quota    int    `json:"quota"`
	Revision int64  `json:"revision"`
}
type Question struct {
	ID       int64  `json:"id"`
	PartID   int64  `json:"part_id"`
	Text     string `json:"question_text"`
	Answer   string `json:"answer_text"`
	Revision int64  `json:"revision"`
}
type Exam struct {
	Name            string              `json:"name"`
	ID              int64               `json:"id"`
	OrganizationID  int64               `json:"organization_id"`
	OwnerDivisionID int64               `json:"owner_division_id"`
	DirectionID     int64               `json:"direction_id"`
	Kind            string              `json:"exam_type"`
	Revision        int64               `json:"revision"`
	ContentRevision int64               `json:"content_revision"`
	RatingRevision  int64               `json:"rating_revision"`
	InBank          bool                `json:"in_bank"`
	OriginID        int64               `json:"origin_exam_id,omitempty"`
	NextID          int64               `json:"-"`
	Parts           []Part              `json:"parts"`
	Questions       []Question          `json:"questions"`
	Divisions       map[int64]*Division `json:"divisions"`
	CreatedAt       time.Time           `json:"created_at"`
}
type Division struct {
	ID              int64               `json:"division_id"`
	Revision        int64               `json:"revision"`
	RatingRevision  int64               `json:"rating_revision"`
	Date            string              `json:"date"`
	Config          Config              `json:"config"`
	TrainingEnabled bool                `json:"training_enabled"`
	Commissions     []Commission        `json:"commissions"`
	Assignments     []Assignment        `json:"assignments"`
	Attempts        []Attempt           `json:"attempts"`
	Privileges      map[int64]Privilege `json:"privileges"`
	Outside         []OutsideResult     `json:"outside_results"`
}
type Commission struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Members  []Member `json:"members"`
	Revision int64    `json:"revision"`
}
type Assignment struct {
	ID                int64    `json:"id"`
	Student           Member   `json:"student"`
	CommissionID      int64    `json:"commission_id"`
	CommissionName    string   `json:"commission_name"`
	Members           []Member `json:"members"`
	AttemptNo         int      `json:"attempt_no"`
	HistoryGeneration int64    `json:"history_generation"`
	Revision          int64    `json:"revision"`
}
type Privilege struct {
	Limit    int   `json:"replacement_limit"`
	Revision int64 `json:"revision"`
}
type OutsideResult struct {
	ID         int64       `json:"id"`
	Student    Member      `json:"student"`
	Points     json.Number `json:"points"`
	Grade      int         `json:"grade"`
	Examinator string      `json:"examinator"`
	Revision   int64       `json:"revision"`
}
type Attempt struct {
	DefinitionKey     string              `json:"definition_key,omitempty"`
	ID                int64               `json:"id"`
	AssignmentID      int64               `json:"assignment_id"`
	Student           Member              `json:"student"`
	Members           []Member            `json:"members"`
	Ready             map[int64]bool      `json:"ready"`
	ReadyAt           map[int64]time.Time `json:"ready_at"`
	AttemptNo         int                 `json:"attempt_no"`
	HistoryGeneration int64               `json:"history_generation"`
	Status            string              `json:"status"`
	Phase             string              `json:"phase"`
	StateVersion      int64               `json:"state_version"`
	ResultVersion     int64               `json:"result_version"`
	Config            Config              `json:"config_snapshot"`
	Parts             []Part              `json:"parts_snapshot"`
	Questions         []Question          `json:"questions_snapshot"`
	ContentRevision   int64               `json:"content_revision"`
	ReplacementLimit  int                 `json:"replacement_limit"`
	ReplacementUsed   int                 `json:"replacement_used"`
	Presented         []Presented         `json:"presented"`
	Usage             map[int64]Usage     `json:"usage"`
	Progress          map[int64]int       `json:"progress"`
	Cycles            map[int64]int       `json:"cycles"`
	StartedAt         *time.Time          `json:"started_at"`
	CompletedAt       *time.Time          `json:"completed_at"`
	RawHalf           int                 `json:"raw_half"`
	RoundedTotal      int                 `json:"rounded_total"`
	MaxScore          int                 `json:"max_score"`
	CalculatedGrade   int                 `json:"calculated_grade"`
	Resolution        string              `json:"resolution_type"`
	Appeals           []Appeal            `json:"appeals"`
}
type Usage struct {
	Presented bool `json:"ever_presented"`
	Excluded  bool `json:"excluded"`
	Cycle     int  `json:"cycle"`
}
type Presented struct {
	ID          int64      `json:"id"`
	Question    Question   `json:"question"`
	Sequence    int        `json:"sequence_no"`
	Purpose     string     `json:"purpose"`
	Status      string     `json:"status"`
	Cycle       int        `json:"cycle_no"`
	ReplacesID  int64      `json:"replaces_id"`
	Consensus   *int       `json:"consensus_half"`
	AwardedHalf int        `json:"awarded_half"`
	Rounds      []Round    `json:"rounds"`
	PresentedAt time.Time  `json:"presented_at"`
	ConsensusAt *time.Time `json:"consensus_at"`
	ReplacedAt  *time.Time `json:"replaced_at"`
	ReplacedBy  int64      `json:"replaced_by_account_id"`
}
type Round struct {
	ID          int64               `json:"id"`
	Number      int                 `json:"round_no"`
	Status      string              `json:"status"`
	Votes       map[int64]int       `json:"votes_half"`
	VoteTimes   map[int64]time.Time `json:"vote_times"`
	Consensus   *int                `json:"consensus_half"`
	OpenedAt    time.Time           `json:"opened_at"`
	CompletedAt *time.Time          `json:"completed_at"`
}
type Appeal struct {
	ID            int64     `json:"id"`
	PreviousGrade int       `json:"previous_grade"`
	Grade         int       `json:"new_grade"`
	ActorID       int64     `json:"actor_account_id"`
	At            time.Time `json:"created_at"`
}

func (e *Exam) Alloc() int64 { e.NextID++; return e.NextID }
func (e *Exam) Division(id int64) (*Division, error) {
	d := e.Divisions[id]
	if d == nil {
		return nil, Missing
	}
	return d, nil
}

type IdentityProvider interface {
	Identify(*http.Request) (Actor, error)
	References(*http.Request, int64, []int64, int64, []int64) (map[int64]Member, error)
	Ready(*http.Request) error
}
