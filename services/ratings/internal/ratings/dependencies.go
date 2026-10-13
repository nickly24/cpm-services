package ratings

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Dependencies struct {
	Config Config
	HTTP   *http.Client
}

func NewDependencies(c Config) *Dependencies {
	return &Dependencies{c, &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (d *Dependencies) Call(ctx context.Context, base, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, e := json.Marshal(in)
		if e != nil {
			return unavailable
		}
		body = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, base+path, body)
	if e != nil {
		return unavailable
	}
	req.Header.Set("X-Service-Key", d.Config.ServiceKey)
	req.Header.Set("Content-Type", "application/json")
	res, e := d.HTTP.Do(req)
	if e != nil {
		return unavailable
	}
	defer res.Body.Close()
	b, e := io.ReadAll(io.LimitReader(res.Body, MaxSnapshotBytes+1))
	if e != nil || len(b) > MaxSnapshotBytes {
		return unavailable
	}
	var env struct {
		Status bool            `json:"status"`
		Data   json.RawMessage `json:"data"`
		Error  string          `json:"error"`
	}
	if json.Unmarshal(b, &env) != nil {
		return unavailable
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 || !env.Status {
		if res.StatusCode == 404 {
			return missing
		}
		if res.StatusCode == 409 {
			return conflict("rating_source_changed")
		}
		return unavailable
	}
	if out != nil && json.Unmarshal(env.Data, out) != nil {
		return unavailable
	}
	return nil
}
func (d *Dependencies) Identify(r *http.Request) (Actor, error) {
	p := strings.Fields(r.Header.Get("Authorization"))
	if len(p) != 2 || !strings.EqualFold(p[0], "Bearer") {
		return Actor{}, &APIError{401, "invalid_token"}
	}
	req, _ := http.NewRequestWithContext(r.Context(), "POST", d.Config.AuthURL+"/v1/auth/introspect", nil)
	req.Header.Set("Authorization", r.Header.Get("Authorization"))
	res, e := d.HTTP.Do(req)
	if e != nil {
		return Actor{}, unavailable
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return Actor{}, &APIError{401, "invalid_token"}
	}
	if res.StatusCode != 200 {
		return Actor{}, unavailable
	}
	var v struct {
		Status bool  `json:"status"`
		Active bool  `json:"active"`
		User   Actor `json:"user"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&v) != nil || !v.Status || !v.Active || v.User.AccountID < 1 || v.User.OrganizationID < 1 {
		return Actor{}, unavailable
	}
	v.User.OrganizationAdmin = v.User.Role == "admin" && v.User.OrganizationAdmin
	return v.User, nil
}
func (d *Dependencies) Collect(ctx context.Context, j Job) (Snapshot, error) {
	s := Snapshot{ExamsMode: d.Config.ExamsMode, ExternalTestsMode: d.Config.ExternalTestsMode}
	req := SnapshotRequest{j.OrganizationID, j.DivisionID, j.DateFrom, j.DateTo, j.AsOf.Format(time.RFC3339Nano)}
	tasks := []struct {
		base, path string
		out        any
	}{{d.Config.CoreURL, "/internal/core/ratings/snapshot", &s.Core}, {d.Config.HomeworkURL, "/internal/homework/ratings/snapshot", &s.Homework}, {d.Config.TestsURL, "/internal/tests/ratings/snapshot", &s.Tests}}
	var wg sync.WaitGroup
	errs := make([]error, len(tasks))
	for i, x := range tasks {
		wg.Add(1)
		go func(i int, base, path string, out any) {
			defer wg.Done()
			errs[i] = d.Call(ctx, base, "POST", path, req, out)
		}(i, x.base, x.path, x.out)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			return s, e
		}
	}
	if d.Config.ExamsMode == "dev_empty" {
		s.Exams = ExamsSnapshot{Source: "exams", OrganizationID: j.OrganizationID, DivisionID: j.DivisionID, Exams: []Exam{}, InputHash: hash("explicit-dev-empty-exams-v1")}
	} else {
		if d.Config.ExamsURL == "" {
			return s, conflict("exams_not_connected")
		}
		var e error
		s.Exams, e = d.collectExams(ctx, j)
		if e != nil {
			return s, e
		}
	}
	if d.Config.ExternalTestsMode == "required" && s.Tests.ExternalTests == nil {
		return s, conflict("external_tests_not_connected")
	}
	if e := validateExternalTestsSource(s.Tests, j, d.Config.ExternalTestsMode == "required"); e != nil {
		return s, e
	}
	if e := validateHomeworkSource(s.Homework, j); e != nil {
		return s, e
	}
	for _, scope := range [][3]int64{{s.Core.OrganizationID, s.Core.DivisionID, int64(len(s.Core.InputHash))}, {s.Homework.OrganizationID, s.Homework.DivisionID, int64(len(s.Homework.InputHash))}, {s.Tests.OrganizationID, s.Tests.DivisionID, int64(len(s.Tests.InputHash))}, {s.Exams.OrganizationID, s.Exams.DivisionID, int64(len(s.Exams.InputHash))}} {
		if scope[0] != j.OrganizationID || scope[1] != j.DivisionID || scope[2] != 64 {
			return s, unavailable
		}
	}
	if s.Core.Source != "core" || s.Homework.Source != "homework" || s.Tests.Source != "tests" || s.Exams.Source != "exams" {
		return s, unavailable
	}
	for _, h := range []string{s.Core.InputHash, s.Homework.InputHash, s.Tests.InputHash, s.Exams.InputHash} {
		if _, e := hex.DecodeString(h); e != nil {
			return s, unavailable
		}
	}
	if s.Core.Accounts == nil || s.Homework.Assignments == nil || s.Tests.Tests == nil || s.Exams.Exams == nil {
		return s, unavailable
	}
	if len(s.Core.Accounts) > 2000 || len(s.Exams.Exams) > 2000 {
		return s, bad("snapshot_too_large")
	}
	seen := map[int64]bool{}
	for _, a := range s.Core.Accounts {
		if a.AccountID < 1 || seen[a.AccountID] {
			return s, unavailable
		}
		seen[a.AccountID] = true
	}
	return s, nil
}
func fingerprint(s Snapshot) string {
	return hash([]string{s.Core.InputHash, s.Homework.InputHash, s.Tests.InputHash, s.Tests.ExternalSourceVersion, s.Exams.InputHash, s.ExamsMode, s.ExternalTestsMode})
}

// An explicitly empty connected source is valid; an absent version or malformed
// external record cannot become an implicit zero component in required mode.
func validateExternalTestsSource(s TestsSnapshot, j Job, required bool) error {
	if required && (s.ExternalDomain == nil || s.ExternalDomain.Status != "ready") {
		return conflict("external_tests_not_connected")
	}
	if s.ExternalDomain != nil && (s.ExternalDomain.SchemaVersion != 1 || s.ExternalDomain.Status != "ready" || s.ExternalDomain.OrganizationID != j.OrganizationID || s.ExternalDomain.DivisionID != j.DivisionID) {
		return unavailable
	}
	if s.ExternalTests == nil {
		return nil
	}
	if required || s.ExternalSourceVersion != "" {
		if len(s.ExternalSourceVersion) != 64 {
			return unavailable
		}
		if _, e := hex.DecodeString(s.ExternalSourceVersion); e != nil {
			return unavailable
		}
	}
	if len(s.Tests)+len(s.ExternalTests) > 1000 {
		return bad("snapshot_too_large")
	}
	seen := map[string]bool{}
	resultCount := 0
	for _, t := range s.ExternalTests {
		date, e := time.Parse(time.RFC3339Nano, t.StartAt)
		if t.ID == "" || seen[t.ID] || t.DirectionID == nil || *t.DirectionID < 1 || t.SourceRevision < 1 || !t.Published || t.IsArchived || t.Results == nil || e != nil {
			return unavailable
		}
		calendar := date.In(Moscow).Format(time.DateOnly)
		if calendar < j.DateFrom || calendar > j.DateTo {
			return unavailable
		}
		seen[t.ID] = true
		accounts := map[int64]bool{}
		for _, r := range t.Results {
			resultCount++
			if resultCount > 100000 {
				return bad("snapshot_too_large")
			}
			if r.AccountID < 1 || accounts[r.AccountID] || r.SubmissionID == "" || r.Revision < 1 || r.Status != "ready" && r.Status != "missing" || r.Status == "ready" && r.Score == nil || r.Status == "missing" && r.Score != nil || r.Score != nil && (math.IsNaN(*r.Score) || math.IsInf(*r.Score, 0) || *r.Score < 0 || *r.Score > 100) {
				return unavailable
			}
			accounts[r.AccountID] = true
		}
	}
	return nil
}
func transitioned(s Snapshot, now time.Time) bool {
	for _, t := range []*time.Time{s.Homework.NextTransitionAt, s.Exams.NextTransitionAt} {
		if t != nil && !now.Before(*t) {
			return true
		}
	}
	return false
}

// The owner selects assignments; reject an inconsistent scope-period calendar
// contract instead of silently omitting invalid rows in personal arithmetic.
func validateHomeworkSource(s HomeworkSnapshot, j Job) error {
	if s.Assignments == nil {
		return unavailable
	}
	seen := map[int64]bool{}
	var next time.Time
	for _, a := range s.Assignments {
		deadline, err := time.Parse(time.DateOnly, a.Deadline)
		if a.ID < 1 || seen[a.ID] || err != nil || a.Deadline < j.DateFrom || a.Deadline > j.DateTo || a.Results == nil {
			return unavailable
		}
		seen[a.ID] = true
		accounts := map[int64]bool{}
		for _, r := range a.Results {
			if r.AccountID < 1 || accounts[r.AccountID] || r.Result != nil && (math.IsNaN(*r.Result) || math.IsInf(*r.Result, 0)) {
				return unavailable
			}
			accounts[r.AccountID] = true
			if r.DatePass != nil {
				if _, err := time.Parse(time.DateOnly, *r.DatePass); err != nil {
					return unavailable
				}
			}
		}
		if a.TypeName == "ОВ" {
			at := time.Date(deadline.Year(), deadline.Month(), deadline.Day()+1, 0, 0, 0, 0, Moscow).UTC()
			if at.After(j.AsOf) && (next.IsZero() || at.Before(next)) {
				next = at
			}
		}
	}
	if next.IsZero() {
		if s.NextTransitionAt != nil {
			return unavailable
		}
	} else if s.NextTransitionAt == nil || !s.NextTransitionAt.Equal(next) {
		return unavailable
	}
	return nil
}
