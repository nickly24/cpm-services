package ratings

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// These DTOs mirror exams' GET /internal/exams/rating-snapshot. The owner
// selects effective attempts/appeals inside its own consistent read; ratings
// translates subject results and preserves the exam/division identities.
type examOwnerCatalogue struct {
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
type examOwnerResult struct {
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

// A present zero grade is valid, but a missing/null grade is not zero. Keep
// that distinction while decoding the owner's non-null integer grade DTO.
func (r *examOwnerResult) UnmarshalJSON(raw []byte) error {
	type alias examOwnerResult
	var value alias
	if err := strict(raw, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	grade, exists := fields["grade"]
	if !exists || bytes.Equal(bytes.TrimSpace(grade), []byte("null")) {
		return bad("invalid_exam_grade")
	}
	for _, name := range []string{"missing_result", "has_appeal"} {
		boolean := string(bytes.TrimSpace(fields[name]))
		if boolean != "true" && boolean != "false" {
			return bad("invalid_exam_result")
		}
	}
	*r = examOwnerResult(value)
	return nil
}

type examOwnerRevision struct {
	ExamID           int64 `json:"exam_id"`
	DivisionID       int64 `json:"division_id"`
	ExamRevision     int64 `json:"exam_revision"`
	DivisionRevision int64 `json:"division_revision"`
}
type examOwnerSnapshot struct {
	Source          string               `json:"source"`
	OrganizationID  int64                `json:"organization_id"`
	DivisionIDs     []int64              `json:"division_ids"`
	DateFrom        string               `json:"date_from"`
	DateTo          string               `json:"date_to"`
	AsOf            time.Time            `json:"as_of"`
	SourceRevision  string               `json:"source_revision"`
	Revisions       []examOwnerRevision  `json:"revisions"`
	NextExamStartAt *time.Time           `json:"next_exam_start_at"`
	Exams           []examOwnerCatalogue `json:"exams"`
	Results         []examOwnerResult    `json:"results"`
}
type examOwnerResponse struct {
	SourceRevisions map[string]int64  `json:"source_revisions"`
	Snapshot        examOwnerSnapshot `json:"snapshot"`
}

func (d *Dependencies) collectExams(ctx context.Context, j Job) (ExamsSnapshot, error) {
	q := url.Values{}
	q.Set("organization_id", strconv.FormatInt(j.OrganizationID, 10))
	q.Set("division_ids", strconv.FormatInt(j.DivisionID, 10))
	q.Set("date_from", j.DateFrom)
	q.Set("date_to", j.DateTo)
	q.Set("as_of", j.AsOf.UTC().Format(time.RFC3339Nano))
	var owner examOwnerResponse
	if err := d.Call(ctx, d.Config.ExamsURL, "GET", "/internal/exams/rating-snapshot?"+q.Encode(), nil, &owner); err != nil {
		return ExamsSnapshot{}, err
	}
	return translateExamSnapshot(j, owner)
}

func translateExamSnapshot(j Job, owner examOwnerResponse) (ExamsSnapshot, error) {
	output := ExamsSnapshot{Source: "exams", OrganizationID: j.OrganizationID, DivisionID: j.DivisionID, Exams: []Exam{}}
	snapshot := owner.Snapshot
	divisionKey := strconv.FormatInt(j.DivisionID, 10)
	divisionRevision, ok := owner.SourceRevisions[divisionKey]
	_, hashErr := hex.DecodeString(snapshot.SourceRevision)
	if !ok || divisionRevision < 0 || len(owner.SourceRevisions) != 1 || snapshot.Source != "exams" || snapshot.OrganizationID != j.OrganizationID || len(snapshot.DivisionIDs) != 1 || snapshot.DivisionIDs[0] != j.DivisionID || snapshot.DateFrom != j.DateFrom || snapshot.DateTo != j.DateTo || !snapshot.AsOf.Equal(j.AsOf) || len(snapshot.SourceRevision) != 64 || hashErr != nil || snapshot.Exams == nil || snapshot.Results == nil || snapshot.Revisions == nil {
		return output, unavailable
	}
	output.SourceRevision = divisionRevision
	if snapshot.NextExamStartAt != nil && !snapshot.NextExamStartAt.After(j.AsOf) {
		return output, unavailable
	}
	if snapshot.NextExamStartAt != nil {
		copyAt := *snapshot.NextExamStartAt
		output.NextTransitionAt = &copyAt
	}
	snapshot.Exams = append(make([]examOwnerCatalogue, 0, len(snapshot.Exams)), snapshot.Exams...)
	snapshot.Results = append(make([]examOwnerResult, 0, len(snapshot.Results)), snapshot.Results...)
	snapshot.Revisions = append(make([]examOwnerRevision, 0, len(snapshot.Revisions)), snapshot.Revisions...)
	// Canonicalize owner arrays so equivalent subject data cannot acquire a
	// different fingerprint merely through a different serialization order.
	sort.Slice(snapshot.Exams, func(i, k int) bool { return snapshot.Exams[i].ExamID < snapshot.Exams[k].ExamID })
	sort.Slice(snapshot.Results, func(i, k int) bool {
		a, b := snapshot.Results[i], snapshot.Results[k]
		if a.ExamID != b.ExamID {
			return a.ExamID < b.ExamID
		}
		return a.AccountID < b.AccountID
	})
	sort.Slice(snapshot.Revisions, func(i, k int) bool { return snapshot.Revisions[i].ExamID < snapshot.Revisions[k].ExamID })
	indexes := map[int64]int{}
	for _, entry := range snapshot.Exams {
		if entry.ExamID < 1 || entry.DivisionID != j.DivisionID || entry.DirectionID < 1 || entry.Revision < 1 || entry.DivisionRevision < 1 {
			return output, unavailable
		}
		if _, exists := indexes[entry.ExamID]; exists {
			return output, unavailable
		}
		if _, err := time.Parse(time.DateOnly, entry.Date); err != nil || entry.Date < j.DateFrom || entry.Date > j.DateTo {
			return output, unavailable
		}
		switch entry.Kind {
		case "classic":
			if entry.StartAt == nil || entry.StartAt.After(j.AsOf) || entry.StartAt.In(Moscow).Format(time.DateOnly) != entry.Date {
				return output, unavailable
			}
		case "outside_lms":
		default:
			return output, unavailable
		}
		id := strconv.FormatInt(entry.ExamID, 10)
		direction := entry.DirectionID
		indexes[entry.ExamID] = len(output.Exams)
		// Older owner snapshots omit the optional label; preserve their fallback.
		name := entry.Name
		if strings.TrimSpace(name) == "" {
			name = "Экзамен " + id
		}
		output.Exams = append(output.Exams, Exam{ID: id, Name: name, Date: entry.Date, Type: entry.Kind, DirectionID: &direction, Results: []ExamResult{}})
	}
	seenResults := map[[2]int64]bool{}
	for _, row := range snapshot.Results {
		index, exists := indexes[row.ExamID]
		key := [2]int64{row.ExamID, row.AccountID}
		if !exists || row.DivisionID != j.DivisionID || row.AccountID < 1 || seenResults[key] || row.Grade < 0 || row.Grade > 5 || row.AttemptNo < 0 || row.ResultVersion < 0 || row.HistoryGeneration < 0 {
			return output, unavailable
		}
		if !row.Missing && (row.ResultID < 1 || row.ResultVersion < 1 || output.Exams[index].Type == "classic" && (row.AttemptNo < 1 || row.HistoryGeneration < 1)) {
			return output, unavailable
		}
		seenResults[key] = true
		result := ExamResult{AccountID: row.AccountID, HasAppeal: row.HasAppeal, MissingResult: row.Missing}
		if !row.Missing {
			score := float64(row.Grade)
			result.Score = &score
		}
		if row.AttemptNo > 0 {
			attempt := row.AttemptNo
			result.AttemptNo = &attempt
		}
		output.Exams[index].Results = append(output.Exams[index].Results, result)
	}
	seenRevisions := map[int64]examOwnerRevision{}
	for _, row := range snapshot.Revisions {
		_, duplicate := seenRevisions[row.ExamID]
		if row.ExamID < 1 || row.DivisionID != j.DivisionID || row.ExamRevision < 1 || row.DivisionRevision < 1 || duplicate {
			return output, unavailable
		}
		seenRevisions[row.ExamID] = row
	}
	for _, entry := range snapshot.Exams {
		revision, present := seenRevisions[entry.ExamID]
		if !present || entry.Revision != revision.ExamRevision || entry.DivisionRevision != revision.DivisionRevision {
			return output, unavailable
		}
	}
	// as_of is a request identity, not a change to subject data. Removing it
	// keeps freshness stable until an input/revision/calendar transition changes.
	// Do retain all result versions, catalogue revisions and source counters.
	snapshot.AsOf = time.Time{}
	owner.Snapshot = snapshot
	output.InputHash = hash(owner)
	return output, nil
}
