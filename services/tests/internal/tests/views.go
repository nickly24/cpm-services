package tests

import (
	"go.mongodb.org/mongo-driver/v2/bson"
	"net/http"
	"time"
)

type Actions struct {
	EditMetadata bool `json:"can_edit_metadata"`
	EditSettings bool `json:"can_edit_settings"`
	Attach       bool `json:"can_attach"`
	Copy         bool `json:"can_copy"`
	Detach       bool `json:"can_detach"`
	Archive      bool `json:"can_archive"`
	ManageBank   bool `json:"can_manage_bank"`
}
type TestView struct {
	ID                bson.ObjectID `json:"id"`
	OrganizationID    int64         `json:"organization_id"`
	OwnerDivisionID   int64         `json:"owner_division_id"`
	OwnerDivisionName string        `json:"owner_division_name"`
	DirectionID       int64         `json:"direction_id"`
	DirectionName     string        `json:"direction_name"`
	DirectionActive   bool          `json:"direction_active"`
	Title             string        `json:"title"`
	Description       string        `json:"description"`
	Revision          int64         `json:"revision"`
	ContentVersionID  bson.ObjectID `json:"content_version_id"`
	QuestionCount     int           `json:"question_count"`
	Archived          bool          `json:"is_archived"`
	BankAvailable     bool          `json:"bank_available"`
	Origin            *Origin       `json:"origin"`
	IsOwner           bool          `json:"is_owner"`
	IsAttached        bool          `json:"is_attached"`
	Placements        []Placement   `json:"placements"`
	Actions           Actions       `json:"actions"`
	CreatedAt         time.Time     `json:"created_at"`
	UpdatedAt         time.Time     `json:"updated_at"`
	CreatedBy         int64         `json:"created_by"`
	UpdatedBy         int64         `json:"updated_by"`
}

func (s *Server) view(r *http.Request, a Actor, d Document, target int64) (TestView, error) {
	refs, e := s.Dependencies.References(r.Context(), a.OrganizationID, []int64{d.OwnerDivisionID}, &d.DirectionID)
	if e != nil {
		return TestView{}, e
	}
	return buildTestView(a, d, target, refs.Divisions[0], *refs.Direction), nil
}

// Keep presentation pure: dependencies and permissions are supplied fresh for
// each request, while the document itself can come from the revision cache.
func buildTestView(a Actor, d Document, target int64, ownerReference, direction Reference) TestView {
	placements := []Placement{}
	admin := a.Can(false)
	owner := d.Editable(a)
	for _, p := range d.Placements {
		if admin && (a.OrganizationAdmin || a.InDivision(d.OwnerDivisionID)) || p.DivisionID == target && a.InDivision(target) {
			placements = append(placements, p)
		}
	}
	attached := d.Placement(target) != nil
	act := Actions{}
	if admin {
		usable := !d.Archived && direction.Active
		act.EditMetadata = owner && !d.Archived
		act.Archive = owner
		act.ManageBank = owner && !d.Archived
		act.EditSettings = usable && attached && a.Can(true) && a.InDivision(target)
		act.Attach = usable && !attached && d.Readable(a) && a.Can(true) && a.InDivision(target) && target > 0
		act.Copy = usable && d.Readable(a) && a.Can(true) && a.InDivision(target) && target > 0
		act.Detach = attached && target != d.OwnerDivisionID && a.Can(true) && a.InDivision(target)
	}
	return TestView{ID: d.ID, OrganizationID: d.OrganizationID, OwnerDivisionID: d.OwnerDivisionID, OwnerDivisionName: ownerReference.Name, DirectionID: d.DirectionID, DirectionName: direction.Name, DirectionActive: direction.Active, Title: d.Title, Description: d.Description, Revision: d.Revision, ContentVersionID: d.ContentVersionID, QuestionCount: len(d.Questions), Archived: d.Archived, BankAvailable: d.BankAvailable, Origin: d.Origin, IsOwner: target == d.OwnerDivisionID, IsAttached: attached, Placements: placements, Actions: act, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt, CreatedBy: d.CreatedBy, UpdatedBy: d.UpdatedBy}
}

type AccessView struct {
	TestID                bson.ObjectID  `json:"test_id"`
	DivisionID            int64          `json:"division_id"`
	Revision              int64          `json:"revision"`
	OfficialAccessAllowed bool           `json:"official_access_allowed"`
	ReviewAccessAllowed   bool           `json:"review_access_allowed"`
	OfficialReason        string         `json:"official_reason"`
	ReviewReason          string         `json:"review_reason"`
	Scope                 string         `json:"scope"`
	AttemptStateChecked   bool           `json:"attempt_state_checked"`
	ContentChecked        bool           `json:"content_checked"`
	ServerTime            time.Time      `json:"server_time"`
	PracticeAccessAllowed bool           `json:"practice_access_allowed"`
	PracticeReason        string         `json:"practice_reason"`
	AttemptID             *bson.ObjectID `json:"attempt_id"`
	SubmissionID          *bson.ObjectID `json:"submission_id"`
	CalculationStatus     string         `json:"calculation_status"`
}

func availability(d Document, p Placement, directionActive bool, now time.Time) AccessView {
	out := AccessView{TestID: d.ID, DivisionID: p.DivisionID, Revision: d.Revision, Scope: "test_settings_only", ServerTime: now.UTC()}
	switch {
	case d.Archived:
		out.OfficialReason = "test_archived"
		out.ReviewReason = "test_archived"
	case !directionActive:
		out.OfficialReason = "direction_archived"
		out.ReviewReason = "direction_archived"
	case !p.Settings.Published:
		out.OfficialReason = "unpublished"
		out.ReviewReason = "unpublished"
	default:
		switch {
		case p.Settings.StartAt == nil || p.Settings.EndAt == nil || p.Settings.TimeLimitMinutes < 1:
			out.OfficialReason = "schedule_not_configured"
		case now.Before(*p.Settings.StartAt):
			out.OfficialReason = "test_upcoming"
		case now.After(*p.Settings.EndAt):
			out.OfficialReason = "test_ended"
		default:
			out.OfficialAccessAllowed = true
			out.OfficialReason = "allowed"
		}
		if p.Settings.AnswersVisible {
			out.ReviewAccessAllowed = true
			out.ReviewReason = "allowed"
		} else {
			out.ReviewReason = "review_hidden"
		}
	}
	return out
}
