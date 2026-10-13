package core

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const reportMaxBytes = 32 << 20
const reportMaxCells = 100000

type ReportClient struct {
	Tests, Exams, Attendance, Key string
	HTTP                          *http.Client
}

func NewReportClient(c Config) *ReportClient {
	return &ReportClient{c.ReportTestsURL, c.ReportExamsURL, c.ReportAttendanceURL, c.InternalKey, &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (p *ReportClient) call(ctx context.Context, source, method, path, bearer string, in, out any) error {
	base := map[string]string{"tests": p.Tests, "exams": p.Exams, "attendance": p.Attendance}[source]
	if base == "" {
		return &APIError{503, source + "_reports_not_connected"}
	}
	var body io.Reader
	if in != nil {
		raw, e := json.Marshal(in)
		if e != nil {
			return unavailable
		}
		body = bytes.NewReader(raw)
	}
	req, e := http.NewRequestWithContext(ctx, method, base+path, body)
	if e != nil {
		return unavailable
	}
	req.Header.Set("Content-Type", "application/json")
	if source == "attendance" {
		req.Header.Set("Authorization", bearer)
	} else {
		req.Header.Set("X-Service-Key", p.Key)
	}
	if p.HTTP == nil {
		return &APIError{503, source + "_report_unavailable"}
	}
	res, e := p.HTTP.Do(req)
	if e != nil {
		return &APIError{503, source + "_report_unavailable"}
	}
	defer res.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(res.Body, reportMaxBytes+1))
	if e != nil || len(raw) > reportMaxBytes {
		return &APIError{503, "report_source_too_large"}
	}
	var envelope struct {
		Status bool            `json:"status"`
		Data   json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil || res.StatusCode < 200 || res.StatusCode >= 300 || !envelope.Status {
		return &APIError{503, source + "_report_unavailable"}
	}
	if json.Unmarshal(envelope.Data, out) != nil {
		return &APIError{503, "invalid_report_source"}
	}
	return nil
}

type ReportItem struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	DirectionID    int64  `json:"direction_id,omitempty"`
	DirectionName  string `json:"direction_name,omitempty"`
	Date           string `json:"date"`
	Kind           string `json:"kind"`
	TargetID       string `json:"target_id,omitempty"`
	SourceRevision int64  `json:"source_revision,omitempty"`
}
type ReportCell struct {
	AccountID           int64    `json:"account_id"`
	ItemID              string   `json:"item_id"`
	Status              string   `json:"status"`
	Score               *float64 `json:"score"`
	SubmissionID        string   `json:"submission_id,omitempty"`
	CalculationRevision int64    `json:"calculation_revision,omitempty"`
	AttemptNo           int      `json:"attempt_no,omitempty"`
	HasAppeal           bool     `json:"has_appeal,omitempty"`
	ResultID            int64    `json:"result_id,omitempty"`
	ResultVersion       int64    `json:"result_version,omitempty"`
	Mark                string   `json:"mark,omitempty"`
	TypeName            string   `json:"type_name,omitempty"`
	TypeCode            string   `json:"type_code,omitempty"`
	Recorded            bool     `json:"recorded,omitempty"`
	ExternalResultID    string   `json:"external_result_id,omitempty"`
}
type CoreReport struct {
	Source                string           `json:"source"`
	OrganizationID        int64            `json:"organization_id"`
	DivisionID            int64            `json:"division_id"`
	DateFrom              string           `json:"date_from"`
	DateTo                string           `json:"date_to"`
	AsOf                  string           `json:"as_of"`
	Students              []map[string]any `json:"students"`
	Items                 []ReportItem     `json:"items"`
	Cells                 []ReportCell     `json:"cells"`
	Limitations           []string         `json:"limitations"`
	UnmarkedIsAbsent      *bool            `json:"unmarked_is_absent,omitempty"`
	SourceRevision        *int64           `json:"source_revision,omitempty"`
	SourceInputHash       string           `json:"source_input_hash,omitempty"`
	ExternalSourceVersion string           `json:"external_source_version,omitempty"`
}

type reportTestResult struct {
	AccountID    int64    `json:"account_id"`
	SubmissionID string   `json:"submission_id"`
	Revision     int64    `json:"calculation_revision"`
	Status       string   `json:"calculation_status"`
	Score        *float64 `json:"score"`
}
type reportTest struct {
	ID             string             `json:"test_id"`
	Name           string             `json:"title"`
	DirectionID    int64              `json:"direction_id"`
	DirectionName  string             `json:"direction_name"`
	StartAt        time.Time          `json:"start_at"`
	Published      bool               `json:"published"`
	Archived       bool               `json:"is_archived"`
	SourceRevision int64              `json:"source_revision,omitempty"`
	Results        []reportTestResult `json:"results"`
}
type reportExternalDomain struct {
	SchemaVersion  int    `json:"schema_version"`
	Status         string `json:"status"`
	OrganizationID int64  `json:"organization_id"`
	DivisionID     int64  `json:"division_id"`
}
type testsReportSnapshot struct {
	Source                string                `json:"source"`
	OrganizationID        int64                 `json:"organization_id"`
	DivisionID            int64                 `json:"division_id"`
	Tests                 []reportTest          `json:"tests"`
	ExternalTests         []reportTest          `json:"external_tests"`
	ExternalDomain        *reportExternalDomain `json:"external_domain"`
	ExternalSourceVersion string                `json:"external_source_version"`
	InputHash             string                `json:"input_hash"`
}
type examsReportSnapshot struct {
	Source         string  `json:"source"`
	OrganizationID int64   `json:"organization_id"`
	DivisionIDs    []int64 `json:"division_ids"`
	DateFrom       string  `json:"date_from"`
	DateTo         string  `json:"date_to"`
	Exams          []struct {
		ID          int64  `json:"exam_id"`
		DivisionID  int64  `json:"division_id"`
		Name        string `json:"exam_name"`
		DirectionID int64  `json:"direction_id"`
		Kind        string `json:"exam_type"`
		Date        string `json:"exam_date"`
	} `json:"exams"`
	Results []struct {
		ExamID        int64    `json:"exam_id"`
		DivisionID    int64    `json:"division_id"`
		AccountID     int64    `json:"account_id"`
		Grade         *float64 `json:"grade"`
		Missing       bool     `json:"missing_result"`
		ResultID      int64    `json:"result_id"`
		AttemptNo     int      `json:"attempt_no"`
		HasAppeal     bool     `json:"has_appeal"`
		ResultVersion int64    `json:"result_version"`
	} `json:"results"`
}
type attendanceReportSnapshot struct {
	Revision int64 `json:"revision"`
	Value    struct {
		OrganizationID int64 `json:"organization_id"`
		DivisionID     int64 `json:"division_id"`
		Days           []struct {
			ID         int64  `json:"id"`
			DivisionID int64  `json:"division_id"`
			Date       string `json:"date"`
			Comment    string `json:"comment"`
		} `json:"days"`
		Entries []struct {
			ID         int64 `json:"id"`
			DivisionID int64 `json:"division_id"`
			DayID      int64 `json:"class_day_id"`
			AccountID  int64 `json:"account_id"`
			Deleted    bool  `json:"is_deleted"`
			Type       struct {
				Code string `json:"code"`
				Name string `json:"name"`
				Mark string `json:"mark"`
			} `json:"type"`
		} `json:"entries"`
		UnmarkedIsAbsent bool `json:"unmarked_is_absent"`
	} `json:"value"`
}

func reportQuery(r *http.Request) (string, string, error) {
	for key, values := range r.URL.Query() {
		if len(values) != 1 || (key != "date_from" && key != "date_to") {
			return "", "", bad("invalid_report_query")
		}
	}
	from, to := r.URL.Query().Get("date_from"), r.URL.Query().Get("date_to")
	return from, to, period(from, to)
}
func reportAllowed(a Actor, source string) bool {
	if a.Role == "supervisor" || a.OrganizationAdmin {
		return true
	}
	if source == "tests" {
		section := "test-results"
		if _, canonical := a.Permissions[section]; !canonical {
			section = "test_results"
		}
		return a.Can(section, false)
	}
	return a.Can(map[string]string{"exams": "exams", "attendance": "attendance"}[source], false)
}
func (s *Server) reportRoster(r *http.Request, a Actor, d int64) ([]map[string]any, error) {
	rows, e := list(r.Context(), s.DB, `SELECT a.id account_id,a.ref_id profile_id,p.full_name,NULLIF(p.`+"`class`"+`,0) class,gs.group_id,g.name group_name,sc.id school_id,sc.name school_name,sc.short_name school_short_name FROM auth_division_access m JOIN auth_users a ON a.id=m.user_id AND a.role='student' JOIN students p ON p.id=a.ref_id LEFT JOIN core_group_scope gs ON gs.group_id=m.group_id AND gs.organization_id=m.organization_id AND gs.division_id=m.division_id AND gs.is_active=1 LEFT JOIN `+"`groups`"+` g ON g.id=gs.group_id LEFT JOIN core_school_scope ss ON ss.school_id=p.school_id AND ss.organization_id=m.organization_id LEFT JOIN schools sc ON sc.id=ss.school_id WHERE m.organization_id=? AND m.division_id=? ORDER BY a.id LIMIT 2001`, a.OrganizationID, d)
	if e != nil {
		return nil, e
	}
	if len(rows) > 2000 {
		return nil, &APIError{422, "report_too_large"}
	}
	return rows, nil
}
func (s *Server) newReport(r *http.Request, a Actor, d int64, source string) (CoreReport, error) {
	out := CoreReport{Source: source, OrganizationID: a.OrganizationID, DivisionID: d, Students: []map[string]any{}, Items: []ReportItem{}, Cells: []ReportCell{}, Limitations: []string{}}
	if !reportAllowed(a, source) {
		return out, denied
	}
	var e error
	out.DateFrom, out.DateTo, e = reportQuery(r)
	if e != nil {
		return out, e
	}
	if s.Reports == nil {
		return out, &APIError{503, source + "_reports_not_connected"}
	}
	out.AsOf = time.Now().UTC().Format(time.RFC3339Nano)
	out.Students, e = s.reportRoster(r, a, d)
	return out, e
}
func finishReport(out CoreReport, cells map[string]ReportCell) (CoreReport, error) {
	if len(out.Items)*len(out.Students) > reportMaxCells {
		return out, &APIError{422, "report_too_large"}
	}
	for _, student := range out.Students {
		for _, item := range out.Items {
			key := fmt.Sprintf("%d/%s", num(student["account_id"]), item.ID)
			cell, ok := cells[key]
			if !ok {
				cell = ReportCell{AccountID: num(student["account_id"]), ItemID: item.ID, Status: "missing"}
			}
			out.Cells = append(out.Cells, cell)
		}
	}
	return out, nil
}
func reportBad() error { return &APIError{503, "invalid_report_source"} }
func (s *Server) testsReport(r *http.Request, a Actor, d int64) (any, int, error) {
	out, e := s.newReport(r, a, d, "tests")
	if e != nil {
		return nil, 0, e
	}
	var source testsReportSnapshot
	e = s.Reports.call(r.Context(), "tests", "POST", "/internal/tests/ratings/snapshot", "", RatingSnapshotRequest{a.OrganizationID, d, out.DateFrom, out.DateTo, out.AsOf}, &source)
	if e != nil {
		return nil, 0, e
	}
	out, e = translateTestsReport(out, source)
	return out, 200, e
}
func translateTestsReport(out CoreReport, source testsReportSnapshot) (CoreReport, error) {
	domain := source.ExternalDomain
	if source.Source != "tests" || source.OrganizationID != out.OrganizationID || source.DivisionID != out.DivisionID || source.Tests == nil || source.ExternalTests == nil || domain == nil || domain.SchemaVersion != 1 || domain.Status != "ready" || domain.OrganizationID != out.OrganizationID || domain.DivisionID != out.DivisionID || !reportHash(source.ExternalSourceVersion) || !reportHash(source.InputHash) {
		return out, reportBad()
	}
	count := len(source.Tests) + len(source.ExternalTests)
	if count > 1000 || count*len(out.Students) > reportMaxCells {
		return out, &APIError{422, "report_too_large"}
	}
	results := 0
	for _, group := range [][]reportTest{source.Tests, source.ExternalTests} {
		for _, test := range group {
			results += len(test.Results)
			if results > reportMaxCells {
				return out, &APIError{422, "report_too_large"}
			}
		}
	}
	cells := map[string]ReportCell{}
	seen := map[string]bool{}
	moscow, e := time.LoadLocation("Europe/Moscow")
	if e != nil {
		return out, unavailable
	}
	for _, group := range []struct {
		tests    []reportTest
		external bool
	}{{source.Tests, false}, {source.ExternalTests, true}} {
		for _, test := range group.tests {
			date := test.StartAt.In(moscow).Format(time.DateOnly)
			itemID, kind := test.ID, "test"
			if group.external {
				itemID, kind = "external:"+test.ID, "external_test"
			}
			if test.ID == "" || seen[itemID] || !test.Published || test.Archived || date < out.DateFrom || date > out.DateTo || test.Results == nil || group.external && (!reportObjectID(test.ID) || test.SourceRevision < 1 || test.DirectionID < 1 || strings.TrimSpace(test.Name) == "") {
				return out, reportBad()
			}
			seen[itemID] = true
			out.Items = append(out.Items, ReportItem{ID: itemID, Name: test.Name, DirectionID: test.DirectionID, DirectionName: test.DirectionName, Date: date, Kind: kind, TargetID: test.ID, SourceRevision: test.SourceRevision})
			externalResults := map[string]bool{}
			for _, v := range test.Results {
				if v.AccountID < 1 || v.Revision < 1 || v.SubmissionID == "" || group.external && !reportObjectID(v.SubmissionID) {
					return out, reportBad()
				}
				key := fmt.Sprintf("%d/%s", v.AccountID, itemID)
				if _, ok := cells[key]; ok {
					return out, reportBad()
				}
				cell := ReportCell{AccountID: v.AccountID, ItemID: itemID, Status: v.Status, SubmissionID: v.SubmissionID, CalculationRevision: v.Revision}
				if group.external {
					if externalResults[v.SubmissionID] {
						return out, reportBad()
					}
					externalResults[v.SubmissionID] = true
					cell.Recorded = true
					cell.ExternalResultID = v.SubmissionID
					cell.ResultVersion = v.Revision
					if v.Status != "ready" && v.Status != "missing" || v.Status == "missing" && v.Score != nil {
						return out, reportBad()
					}
				}
				switch v.Status {
				case "missing":
					if !group.external {
						return out, reportBad()
					}
				case "queued", "processing", "retrying", "failed":
				case "ready":
					if v.Score == nil || math.IsNaN(*v.Score) || math.IsInf(*v.Score, 0) || *v.Score < 0 || *v.Score > 100 {
						return out, reportBad()
					}
					cell.Score = v.Score
				default:
					return out, reportBad()
				}
				cells[key] = cell
			}
		}
	}
	// Keep internal and external work distinguishable while sharing canonical
	// period/direction filters and a stable date/ID order in the complete matrix.
	sort.Slice(out.Items, func(i, j int) bool {
		if out.Items[i].Date != out.Items[j].Date {
			return out.Items[i].Date < out.Items[j].Date
		}
		return out.Items[i].ID < out.Items[j].ID
	})
	out.SourceInputHash = source.InputHash
	out.ExternalSourceVersion = source.ExternalSourceVersion
	out.Limitations = []string{"published_official_internal_tests", "in_progress_attempts_not_submissions", "scoped_external_tests", "current_division_members"}
	return finishReport(out, cells)
}
func reportHash(value string) bool {
	_, e := hex.DecodeString(value)
	return len(value) == 64 && e == nil
}
func reportObjectID(value string) bool {
	_, e := hex.DecodeString(value)
	return len(value) == 24 && value != strings.Repeat("0", 24) && value == strings.ToLower(value) && e == nil
}
func (s *Server) examsReport(r *http.Request, a Actor, d int64) (any, int, error) {
	out, e := s.newReport(r, a, d, "exams")
	if e != nil {
		return nil, 0, e
	}
	q := url.Values{"organization_id": {strconv.FormatInt(a.OrganizationID, 10)}, "division_ids": {strconv.FormatInt(d, 10)}, "date_from": {out.DateFrom}, "date_to": {out.DateTo}, "as_of": {out.AsOf}}
	var source struct {
		Snapshot examsReportSnapshot `json:"snapshot"`
	}
	e = s.Reports.call(r.Context(), "exams", "GET", "/internal/exams/rating-snapshot?"+q.Encode(), "", nil, &source)
	if e != nil {
		return nil, 0, e
	}
	v := source.Snapshot
	if v.Source != "exams" || v.OrganizationID != a.OrganizationID || len(v.DivisionIDs) != 1 || v.DivisionIDs[0] != d || v.DateFrom != out.DateFrom || v.DateTo != out.DateTo || v.Exams == nil || v.Results == nil {
		return nil, 0, reportBad()
	}
	out, e = translateExamsReport(out, v)
	return out, 200, e
}
func translateExamsReport(out CoreReport, source examsReportSnapshot) (CoreReport, error) {
	cells := map[string]ReportCell{}
	seen := map[int64]bool{}
	for _, exam := range source.Exams {
		if exam.ID < 1 || seen[exam.ID] || exam.DivisionID != out.DivisionID || exam.Date < out.DateFrom || exam.Date > out.DateTo || (exam.Kind != "classic" && exam.Kind != "outside_lms") {
			return out, reportBad()
		}
		seen[exam.ID] = true
		name := exam.Name
		if strings.TrimSpace(name) == "" {
			name = "Экзамен " + strconv.FormatInt(exam.ID, 10)
		}
		out.Items = append(out.Items, ReportItem{ID: strconv.FormatInt(exam.ID, 10), Name: name, DirectionID: exam.DirectionID, Date: exam.Date, Kind: exam.Kind})
	}
	for _, v := range source.Results {
		if !seen[v.ExamID] || v.DivisionID != out.DivisionID || v.AccountID < 1 {
			return out, reportBad()
		}
		id := strconv.FormatInt(v.ExamID, 10)
		key := fmt.Sprintf("%d/%s", v.AccountID, id)
		if _, ok := cells[key]; ok {
			return out, reportBad()
		}
		cell := ReportCell{AccountID: v.AccountID, ItemID: id, Status: "missing"}
		if !v.Missing {
			if v.Grade == nil || math.IsNaN(*v.Grade) || math.IsInf(*v.Grade, 0) || *v.Grade < 0 || *v.Grade > 5 || math.Trunc(*v.Grade) != *v.Grade || v.ResultID < 1 || v.ResultVersion < 1 {
				return out, reportBad()
			}
			cell.Status = "completed"
			cell.Score = v.Grade
			cell.ResultID = v.ResultID
			cell.ResultVersion = v.ResultVersion
			cell.AttemptNo = v.AttemptNo
			cell.HasAppeal = v.HasAppeal
		}
		cells[key] = cell
	}
	out.Limitations = []string{"effective_completed_results", "current_division_members"}
	return finishReport(out, cells)
}
func (s *Server) currentAttendanceReport(r *http.Request, a Actor, d int64) (any, int, error) {
	out, e := s.newReport(r, a, d, "attendance")
	if e != nil {
		return nil, 0, e
	}
	q := url.Values{"date_from": {out.DateFrom}, "date_to": {out.DateTo}}
	var source attendanceReportSnapshot
	e = s.Reports.call(r.Context(), "attendance", "GET", fmt.Sprintf("/v1/attendance/divisions/%d/attendance-report?%s", d, q.Encode()), r.Header.Get("Authorization"), nil, &source)
	if e != nil {
		return nil, 0, e
	}
	if source.Value.OrganizationID != a.OrganizationID || source.Value.DivisionID != d || source.Value.UnmarkedIsAbsent || source.Value.Days == nil || source.Value.Entries == nil {
		return nil, 0, reportBad()
	}
	out, e = translateAttendanceReport(out, source)
	return out, 200, e
}
func translateAttendanceReport(out CoreReport, source attendanceReportSnapshot) (CoreReport, error) {
	cells := map[string]ReportCell{}
	days := map[int64]bool{}
	sort.Slice(source.Value.Days, func(i, j int) bool { return source.Value.Days[i].Date < source.Value.Days[j].Date })
	for _, v := range source.Value.Days {
		if v.ID < 1 || days[v.ID] || v.DivisionID != out.DivisionID || v.Date < out.DateFrom || v.Date > out.DateTo {
			return out, reportBad()
		}
		days[v.ID] = true
		name := v.Date
		if strings.TrimSpace(v.Comment) != "" {
			name += " · " + v.Comment
		}
		out.Items = append(out.Items, ReportItem{ID: strconv.FormatInt(v.ID, 10), Name: name, Date: v.Date, Kind: "attendance"})
	}
	for _, v := range source.Value.Entries {
		if v.DivisionID != out.DivisionID || !days[v.DayID] || v.AccountID < 1 || v.Deleted || v.Type.Code == "" {
			return out, reportBad()
		}
		id := strconv.FormatInt(v.DayID, 10)
		key := fmt.Sprintf("%d/%s", v.AccountID, id)
		if _, ok := cells[key]; ok {
			return out, reportBad()
		}
		cells[key] = ReportCell{AccountID: v.AccountID, ItemID: id, Status: "marked", Mark: v.Type.Mark, TypeName: v.Type.Name, TypeCode: v.Type.Code}
	}
	absent := false
	out.UnmarkedIsAbsent = &absent
	out.SourceRevision = &source.Revision
	out.Limitations = []string{"unmarked_is_not_absence", "current_attendance_owner", "current_division_members"}
	return finishReport(out, cells)
}
