package exams

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// Auxiliary operations use the same object and division scope as exam commands.
func auxScope(e *Exam, a Actor, p Params, edit bool) (*Division, error) {
	if e == nil || e.OrganizationID != a.OrganizationID || a.AccountID <= 0 {
		return nil, Denied
	}
	d, err := e.Division(p.DivisionID)
	if err != nil {
		return nil, err
	}
	if a.OrganizationAdmin && a.Role == "admin" {
		return d, nil
	}
	allowed := false
	for _, id := range a.DivisionIDs {
		if id == p.DivisionID {
			allowed = true
		}
	}
	if !allowed {
		return nil, Denied
	}
	if edit && !a.Can(true) {
		return nil, Denied
	}
	return d, nil
}

func AuxCommand(e *Exam, a Actor, op string, p Params, input json.RawMessage) (any, error) {
	if op != "training-toggle" {
		return nil, Missing
	}
	d, err := auxScope(e, a, p, true)
	if err != nil {
		return nil, err
	}
	if e.Kind != "classic" {
		return nil, Bad("wrong_exam_type")
	}
	var in struct {
		Enabled         *bool  `json:"training_enabled"`
		ExpectedVersion *int64 `json:"expected_revision"`
	}
	if err := auxDecode(input, &in); err != nil {
		return nil, err
	}
	if in.Enabled == nil {
		return nil, Bad("training_enabled_required")
	}
	if in.ExpectedVersion == nil {
		return nil, Bad("expected_revision_required")
	}
	if *in.ExpectedVersion != d.Revision {
		return nil, Conflict("version_conflict")
	}
	if *in.Enabled {
		validParts := map[int64]bool{}
		for _, part := range e.Parts {
			validParts[part.ID] = true
		}
		hasCard := false
		for _, question := range e.Questions {
			if validParts[question.PartID] && strings.TrimSpace(question.Text) != "" && strings.TrimSpace(question.Answer) != "" {
				hasCard = true
				break
			}
		}
		if e.DirectionID < 1 || !hasCard {
			return nil, &APIError{422, "exam_empty"}
		}
	}

	if d.TrainingEnabled != *in.Enabled {
		d.TrainingEnabled = *in.Enabled
		d.Revision++
		e.Revision++
	}
	return map[string]any{"success": true, "training_enabled": d.TrainingEnabled, "version": d.Revision}, nil
}

func AuxQuery(e *Exam, a Actor, op string, p Params) (any, error) {
	if _, err := auxScope(e, a, p, false); err != nil {
		return nil, err
	}
	switch op {
	case "training-view":
		return TrainingView(e, a, p, nil, DefaultTrainingSettings())
	case "training-settings":
		if _, err := trainingScope(e, a, p); err != nil {
			return nil, err
		}
		return map[string]any{"settings": DefaultTrainingSettings()}, nil
	case "rating-snapshot":
		if !a.Can(false) {
			return nil, Denied
		}
		dateFilter, err := ParseExamDateFilter(p.Query)
		if err != nil {
			return nil, err
		}
		asOf := p.Now
		if asOf.IsZero() {
			asOf = time.Now().UTC()
		}
		return RatingSnapshot([]*Exam{e}, a.OrganizationID, []int64{p.DivisionID}, dateFilter.From, dateFilter.To, asOf)
	}
	return nil, Missing
}

type RatingCatalogueEntry struct {
	ExamID           int64      `json:"exam_id"`
	Name             string     `json:"exam_name,omitempty"`
	DivisionID       int64      `json:"division_id"`
	DirectionID      int64      `json:"direction_id"`
	Kind             string     `json:"exam_type"`
	Date             string     `json:"exam_date"`
	StartAt          *time.Time `json:"start_at,omitempty"`
	Revision         int64      `json:"exam_revision"`
	DivisionRevision int64      `json:"division_revision"`
}
type RatingResult struct {
	ExamID            int64 `json:"exam_id"`
	DivisionID        int64 `json:"division_id"`
	AccountID         int64 `json:"account_id"`
	Grade             int   `json:"grade"`
	Missing           bool  `json:"missing_result"`
	ResultID          int64 `json:"result_id"`
	AttemptNo         int   `json:"attempt_no,omitempty"`
	HasAppeal         bool  `json:"has_appeal"`
	ResultVersion     int64 `json:"result_version"`
	HistoryGeneration int64 `json:"history_generation,omitempty"`
}
type RatingRevision struct {
	ExamID           int64 `json:"exam_id"`
	DivisionID       int64 `json:"division_id"`
	ExamRevision     int64 `json:"exam_revision"`
	DivisionRevision int64 `json:"division_revision"`
}
type RatingSnapshotDTO struct {
	Source          string                 `json:"source"`
	OrganizationID  int64                  `json:"organization_id"`
	DivisionIDs     []int64                `json:"division_ids"`
	DateFrom        string                 `json:"date_from"`
	DateTo          string                 `json:"date_to"`
	AsOf            time.Time              `json:"as_of"`
	SourceRevision  string                 `json:"source_revision"`
	Revisions       []RatingRevision       `json:"revisions"`
	NextExamStartAt *time.Time             `json:"next_exam_start_at"`
	Exams           []RatingCatalogueEntry `json:"exams"`
	Results         []RatingResult         `json:"results"`
}

// Legacy aggregates are initialized with these fallback values when loaded.
// New rating revisions only advance when effective rating inputs change.
func examRatingRevision(e *Exam) int64 {
	if e.RatingRevision != 0 {
		return e.RatingRevision
	}
	if e.ContentRevision > 0 {
		return e.ContentRevision
	}
	return 1
}
func divisionRatingRevision(d *Division) int64 {
	if d.RatingRevision != 0 {
		return d.RatingRevision
	}
	if d.Revision > 0 {
		return d.Revision
	}
	return 1
}

// RatingSnapshot exports subject grades and catalogue membership, never a rating
// average. The caller captures its input exams in one database read transaction.
// A missing result is inferred by joining the catalogue to core's participants;
// present zero grades remain explicit result rows. asOf limits catalogue start
// times; results capture the current persisted completed attempt and appeal,
// without a completion/appeal timestamp cutoff, matching the original SQL.
func RatingSnapshot(exams []*Exam, organizationID int64, divisionIDs []int64, dateFrom, dateTo string, asOf time.Time) (any, error) {
	if organizationID <= 0 || len(divisionIDs) == 0 || asOf.IsZero() {
		return nil, Bad("invalid_rating_scope")
	}
	if !validExamDate(dateFrom) {
		return nil, Bad("invalid_date_from")
	}
	if !validExamDate(dateTo) || dateFrom > dateTo {
		return nil, Bad("invalid_date_to")
	}
	dateFilter := ExamDateFilter{From: dateFrom, To: dateTo}
	ids := append([]int64(nil), divisionIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	allowed := map[int64]bool{}
	unique := ids[:0]
	for _, id := range ids {
		if id <= 0 {
			return nil, Bad("invalid_division_id")
		}
		if !allowed[id] {
			allowed[id] = true
			unique = append(unique, id)
		}
	}
	ids = unique
	out := RatingSnapshotDTO{Source: "exams", OrganizationID: organizationID, DivisionIDs: ids, DateFrom: dateFrom, DateTo: dateTo, AsOf: asOf.UTC(), Exams: []RatingCatalogueEntry{}, Results: []RatingResult{}, Revisions: []RatingRevision{}}
	ordered := append([]*Exam(nil), exams...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i] == nil {
			return ordered[j] != nil
		}
		if ordered[j] == nil {
			return false
		}
		return ordered[i].ID < ordered[j].ID
	})
	for _, e := range ordered {
		if e == nil || e.OrganizationID != organizationID {
			continue
		}
		for _, id := range ids {
			d := e.Divisions[id]
			if d == nil {
				continue
			}
			// Operational revisions also track pending votes, historical appeals and
			// other divisions. Only effective rating inputs invalidate this snapshot.
			examRevision, divisionRevision := examRatingRevision(e), divisionRatingRevision(d)
			out.Revisions = append(out.Revisions, RatingRevision{e.ID, id, examRevision, divisionRevision})
			date, ok := ExamDate(e, id)
			if !ok || !dateFilter.Contains(date) {
				continue
			}
			var start *time.Time
			switch e.Kind {
			case "classic":
				start = d.Config.StartAt
				if start.After(asOf) {
					if out.NextExamStartAt == nil || start.Before(*out.NextExamStartAt) {
						copyAt := start.UTC()
						out.NextExamStartAt = &copyAt
					}
					continue
				}
			case "outside_lms":
			default:
				continue
			}
			out.Exams = append(out.Exams, RatingCatalogueEntry{ExamID: e.ID, Name: e.Name, DivisionID: id, DirectionID: e.DirectionID, Kind: e.Kind, Date: date, StartAt: start, Revision: examRevision, DivisionRevision: divisionRevision})
			if e.Kind == "outside_lms" {
				for _, r := range d.Outside {
					out.Results = append(out.Results, RatingResult{ExamID: e.ID, DivisionID: id, AccountID: r.Student.AccountID, Grade: r.Grade, ResultID: r.ID, ResultVersion: r.Revision})
				}
			} else {
				current := map[int64]*Attempt{}
				for i := range d.Attempts {
					attempt := &d.Attempts[i]
					if attempt.Status != "completed" {
						continue
					}
					old := current[attempt.Student.AccountID]
					if old == nil || attempt.AttemptNo > old.AttemptNo || (attempt.AttemptNo == old.AttemptNo && attempt.ID > old.ID) {
						current[attempt.Student.AccountID] = attempt
					}
				}
				for accountID, attempt := range current {
					grade := attempt.CalculatedGrade
					hasAppeal := len(attempt.Appeals) > 0
					if hasAppeal {
						grade = attempt.Appeals[len(attempt.Appeals)-1].Grade
					}
					out.Results = append(out.Results, RatingResult{ExamID: e.ID, DivisionID: id, AccountID: accountID, Grade: grade, ResultID: attempt.ID, AttemptNo: attempt.AttemptNo, HasAppeal: hasAppeal, ResultVersion: attempt.ResultVersion, HistoryGeneration: attempt.HistoryGeneration})
				}
			}
		}
	}
	sort.Slice(out.Results, func(i, j int) bool {
		a, b := out.Results[i], out.Results[j]
		if a.ExamID != b.ExamID {
			return a.ExamID < b.ExamID
		}
		if a.DivisionID != b.DivisionID {
			return a.DivisionID < b.DivisionID
		}
		return a.AccountID < b.AccountID
	})
	b, _ := json.Marshal(out.Revisions)
	h := sha256.Sum256(b)
	out.SourceRevision = hex.EncodeToString(h[:])
	return out, nil
}
