package exams

import (
	"bytes"
	"encoding/json"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func unprocessable(code string) error { return &APIError{422, code} }
func actorID(a Actor) int64           { return a.AccountID }
func memberOf(ms []Member, id int64) bool {
	for _, m := range ms {
		if m.AccountID == id {
			return true
		}
	}
	return false
}
func examiner(a Actor, ms []Member) bool { return a.Role == "examinator" && memberOf(ms, actorID(a)) }

// Used by the HTTP layer before reading receipts and again under the exam
// lock, so a replay never bypasses current role or commission membership.
func authorizeCommand(e *Exam, a Actor, op string, p Params) error {
	if !oneOf(op, "ensure", "ready", "start", "vote", "next-question", "replace-question") {
		if !a.Can(true) {
			return Denied
		}
		return nil
	}
	d, err := e.Division(p.DivisionID)
	if err != nil {
		return err
	}
	if oneOf(op, "ensure", "ready", "start", "vote", "next-question", "replace-question") {
		if a.Role != "examinator" {
			return Denied
		}
		if op == "ensure" {
			i := assignmentIndex(d, p.AssignmentID)
			if i < 0 || !examiner(a, d.Assignments[i].Members) {
				return Missing
			}
		} else {
			i := attemptIndex(d, p.AttemptID)
			if i < 0 || !examiner(a, d.Attempts[i].Members) {
				return Missing
			}
		}
		return nil
	}
	return nil
}
func strict(input json.RawMessage, target any) error {
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	if len(bytes.TrimSpace(input)) == 0 || bytes.TrimSpace(input)[0] != '{' {
		return Bad("invalid_json_object")
	}
	return DecodeStrict(input, target)
}

type numericInput struct{ Number json.Number }

func (n *numericInput) UnmarshalJSON(raw []byte) error {
	v := bytes.TrimSpace(raw)
	if len(v) == 0 || v[0] == '"' || v[0] == 'n' || v[0] == 't' || v[0] == 'f' {
		return Bad("numeric_value_required")
	}
	return json.Unmarshal(v, &n.Number)
}
func (t *Threshold) UnmarshalJSON(raw []byte) error {
	var in struct {
		Grade *int `json:"grade"`
		Score *int `json:"min_score"`
	}
	if err := DecodeStrict(raw, &in); err != nil {
		return err
	}
	if in.Grade == nil || in.Score == nil {
		return Bad("threshold_fields_required")
	}
	*t = Threshold{Grade: *in.Grade, MinScore: *in.Score}
	return nil
}
func checkRevision(expected *int64, current int64, code string) error {
	if expected == nil {
		return Bad("expected_revision_required")
	}
	if *expected < 0 {
		return Bad("invalid_revision")
	}
	if *expected != current {
		return Conflict(code)
	}
	return nil
}
func checkHistory(expected *int64, current int64) error {
	if expected == nil || *expected < 1 {
		return Bad("expected_history_generation_required")
	}
	if *expected != current {
		return Conflict("history_generation_changed")
	}
	return nil
}
func validMember(p Params, id int64, role string) (Member, error) {
	m, ok := p.Members[id]
	if id < 1 || !ok || !m.Active || m.Role != role {
		return Member{}, Bad(role + "_not_found")
	}
	return m, nil
}
func validText(v, field string, maxChars, maxBytes int) (string, error) {
	v = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(v, "\r\n", "\n"), "\r", "\n"))
	if v == "" || maxChars > 0 && utf8.RuneCountInString(v) > maxChars || maxBytes > 0 && len(v) > maxBytes {
		return "", Bad("invalid_" + field)
	}
	return v, nil
}
func partIndex(e *Exam, id int64) int {
	for i := range e.Parts {
		if e.Parts[i].ID == id {
			return i
		}
	}
	return -1
}
func questionIndex(e *Exam, id int64) int {
	for i := range e.Questions {
		if e.Questions[i].ID == id {
			return i
		}
	}
	return -1
}
func commissionIndex(d *Division, id int64) int {
	for i := range d.Commissions {
		if d.Commissions[i].ID == id {
			return i
		}
	}
	return -1
}
func assignmentIndex(d *Division, id int64) int {
	for i := range d.Assignments {
		if d.Assignments[i].ID == id {
			return i
		}
	}
	return -1
}
func attemptIndex(d *Division, id int64) int {
	for i := range d.Attempts {
		if d.Attempts[i].ID == id {
			return i
		}
	}
	return -1
}
func outsideIndex(d *Division, id int64) int {
	for i := range d.Outside {
		if d.Outside[i].ID == id {
			return i
		}
	}
	return -1
}
func sameMembers(a, b []Member) bool {
	if len(a) != len(b) {
		return false
	}
	for _, m := range a {
		if !memberOf(b, m.AccountID) {
			return false
		}
	}
	return true
}
func bumpContent(e *Exam) {
	e.ContentRevision++
	for _, d := range e.Divisions {
		d.Config.Revision++
		d.Revision++
	}
}
func commandResult(changed, rating bool, key string, item any) map[string]any {
	r := map[string]any{"changed": changed, "rating_changed": rating}
	if key != "" {
		r[key] = item
	}
	return r
}
func firstAssignment(d *Division, student int64) *Assignment {
	for i := range d.Assignments {
		a := &d.Assignments[i]
		if a.Student.AccountID == student && a.AttemptNo == 1 {
			return a
		}
	}
	return nil
}
func assignmentAttempt(d *Division, id int64) *Attempt {
	for i := range d.Attempts {
		if d.Attempts[i].AssignmentID == id {
			return &d.Attempts[i]
		}
	}
	return nil
}
func effectiveAttempt(d *Division, student int64) *Attempt {
	var latest *Attempt
	for i := range d.Attempts {
		a := &d.Attempts[i]
		if a.Student.AccountID == student && a.Status == "completed" && (latest == nil || a.AttemptNo > latest.AttemptNo) {
			latest = a
		}
	}
	return latest
}
func effectiveGrade(a *Attempt) int {
	if len(a.Appeals) > 0 {
		return a.Appeals[len(a.Appeals)-1].Grade
	}
	return a.CalculatedGrade
}

// Command applies an atomic in-memory transaction. SQL persistence, scope
// checks, and durable idempotency receipts are supplied by the service layer.
func Command(e *Exam, a Actor, op string, p Params, input json.RawMessage) (any, error) {
	raw, err := encodeExam(e)
	if err != nil {
		return nil, err
	}
	copy, err := decodeExam(raw, e.NextID)
	if err != nil {
		return nil, err
	}
	result, err := command(copy, a, op, p, input)
	if err != nil {
		return nil, err
	}
	*e = *copy
	return result, nil
}
func command(e *Exam, a Actor, op string, p Params, input json.RawMessage) (any, error) {
	d, err := e.Division(p.DivisionID)
	if err != nil {
		return nil, err
	}
	if p.Now.IsZero() {
		return nil, Bad("server_time_required")
	}
	lifecycle := op == "ensure" || op == "ready" || op == "start" || op == "vote" || op == "next-question" || op == "replace-question"
	if !lifecycle && !a.Can(true) {
		return nil, Denied
	}
	if strings.HasPrefix(op, "outside-") {
		if e.Kind != "outside_lms" {
			return nil, Bad("outside_exam_required")
		}
	} else if e.Kind != "classic" {
		return nil, Bad("classic_exam_required")
	}
	if lifecycle {
		return lifecycleCommand(e, d, a, op, p, input)
	}
	switch op {
	case "config":
		var in struct {
			Expected   *int64          `json:"expected_revision"`
			Start      json.RawMessage `json:"start_at"`
			End        json.RawMessage `json:"end_at"`
			Mode       *string         `json:"fractional_mode"`
			Source     *string         `json:"tie_breaker_source_mode"`
			PartID     *int64          `json:"tie_breaker_part_id"`
			Half       *string         `json:"tie_breaker_half_mode"`
			Thresholds *[]Threshold    `json:"thresholds"`
		}
		if err = strict(input, &in); err != nil {
			return nil, err
		}
		if err = checkRevision(in.Expected, d.Config.Revision, "config_modified"); err != nil {
			return nil, err
		}
		c := d.Config
		// time.Time implements UnmarshalJSON and updates a non-nil pointer in
		// place. Detach both dates before merging so change detection compares
		// against the original values and started snapshots stay immutable.
		if c.StartAt != nil {
			v := *c.StartAt
			c.StartAt = &v
		}
		if c.EndAt != nil {
			v := *c.EndAt
			c.EndAt = &v
		}
		if in.Start != nil {
			if err = json.Unmarshal(in.Start, &c.StartAt); err != nil {
				return nil, Bad("invalid_start_at")
			}
		}
		if in.End != nil {
			if err = json.Unmarshal(in.End, &c.EndAt); err != nil {
				return nil, Bad("invalid_end_at")
			}
		}
		if in.Mode != nil {
			c.FractionalMode = *in.Mode
			if !oneOf(c.FractionalMode, "", "round_up", "round_down", "extra_question") {
				return nil, Bad("invalid_fractional_mode")
			}
		}
		if in.Source != nil {
			c.TieBreakerSourceMode = *in.Source
			if !oneOf(*in.Source, "", "any_part", "specific_part") {
				return nil, Bad("invalid_tie_breaker_source_mode")
			}
		}
		if in.Half != nil {
			c.TieBreakerHalfMode = *in.Half
			if !oneOf(*in.Half, "", "repeat", "round_up", "round_down") {
				return nil, Bad("invalid_tie_breaker_half_mode")
			}
		}
		if in.PartID != nil {
			c.TieBreakerPartID = *in.PartID
			if *in.PartID != 0 && partIndex(e, *in.PartID) < 0 {
				return nil, Missing
			}
		}
		if c.StartAt != nil && c.EndAt != nil && !c.EndAt.After(*c.StartAt) {
			return nil, Bad("invalid_time_window")
		}
		if c.FractionalMode != "extra_question" {
			c.TieBreakerSourceMode = ""
			c.TieBreakerPartID = 0
			c.TieBreakerHalfMode = ""
		}
		if c.TieBreakerSourceMode == "any_part" {
			c.TieBreakerPartID = 0
		}
		if in.Thresholds != nil {
			if errors := validateThresholds(maxScore(e.Parts), *in.Thresholds); len(errors) > 0 {
				return nil, Bad(errors[0])
			}
			c.Thresholds = append([]Threshold(nil), (*in.Thresholds)...)
			sort.Slice(c.Thresholds, func(i, j int) bool { return c.Thresholds[i].Grade < c.Thresholds[j].Grade })
		}
		changed := !reflect.DeepEqual(c, d.Config)
		rating := !reflect.DeepEqual(c.StartAt, d.Config.StartAt) || !reflect.DeepEqual(c.EndAt, d.Config.EndAt)
		if changed {
			c.Revision++
			d.Config = c
			d.Revision++
		}
		return commandResult(changed, rating, "config", d.Config), nil
	case "part-create", "part-update":
		var in struct {
			Expected *int64  `json:"expected_revision"`
			Code     *string `json:"code"`
			Weight   *int    `json:"weight"`
			Quota    *int    `json:"quota"`
		}
		if err = strict(input, &in); err != nil {
			return nil, err
		}
		i := partIndex(e, p.PartID)
		v := Part{Revision: 1}
		if op == "part-update" {
			if i < 0 {
				return nil, Missing
			}
			v = e.Parts[i]
			if err = checkRevision(in.Expected, v.Revision, "part_modified"); err != nil {
				return nil, err
			}
		} else {
			if in.Expected != nil {
				return nil, Bad("unexpected_revision")
			}
			if in.Code == nil {
				return nil, Bad("part_code_required")
			}
		}
		if in.Code != nil {
			v.Code = strings.ToUpper(strings.TrimSpace(*in.Code))
			if len(v.Code) != 1 || v.Code[0] < 'A' || v.Code[0] > 'Z' {
				return nil, Bad("invalid_part_code")
			}
		}
		if in.Weight != nil {
			if *in.Weight < 1 || *in.Weight > 1000 {
				return nil, Bad("invalid_weight")
			}
			v.Weight = *in.Weight
		}
		if in.Quota != nil {
			if *in.Quota < 1 || *in.Quota > 100 {
				return nil, Bad("invalid_question_count")
			}
			v.Quota = *in.Quota
		}
		for _, other := range e.Parts {
			if other.ID != v.ID && other.Code == v.Code {
				return nil, Conflict("part_code_conflict")
			}
		}
		changed := i < 0 || !reflect.DeepEqual(v, e.Parts[i])
		if changed {
			if op == "part-create" {
				v.ID = e.Alloc()
				e.Parts = append(e.Parts, v)
			} else {
				v.Revision++
				e.Parts[i] = v
			}
			bumpContent(e)
		}
		return commandResult(changed, false, "part", v), nil
	case "part-delete":
		var in struct {
			Expected *int64 `json:"expected_revision"`
		}
		if err = strict(input, &in); err != nil {
			return nil, err
		}
		i := partIndex(e, p.PartID)
		if i < 0 {
			return nil, Missing
		}
		if err = checkRevision(in.Expected, e.Parts[i].Revision, "part_modified"); err != nil {
			return nil, err
		}
		e.Parts = append(e.Parts[:i], e.Parts[i+1:]...)
		qs := []Question{}
		for _, q := range e.Questions {
			if q.PartID != p.PartID {
				qs = append(qs, q)
			}
		}
		e.Questions = qs
		for _, scope := range e.Divisions {
			if scope.Config.TieBreakerPartID == p.PartID {
				scope.Config.TieBreakerPartID = 0
			}
		}
		bumpContent(e)
		return commandResult(true, false, "deleted", p.PartID), nil
	case "question-create", "question-update":
		var in struct {
			Expected *int64  `json:"expected_revision"`
			PartID   *int64  `json:"part_id"`
			Text     *string `json:"question_text"`
			Answer   *string `json:"answer_text"`
		}
		if err = strict(input, &in); err != nil {
			return nil, err
		}
		i := questionIndex(e, p.QuestionID)
		v := Question{Revision: 1}
		if op == "question-update" {
			if i < 0 {
				return nil, Missing
			}
			v = e.Questions[i]
			if err = checkRevision(in.Expected, v.Revision, "question_modified"); err != nil {
				return nil, err
			}
		} else {
			if in.Expected != nil {
				return nil, Bad("unexpected_revision")
			}
			if in.PartID == nil || in.Text == nil || in.Answer == nil {
				return nil, Bad("question_fields_required")
			}
		}
		if in.PartID != nil {
			v.PartID = *in.PartID
		}
		if partIndex(e, v.PartID) < 0 {
			return nil, Missing
		}
		if in.Text != nil {
			v.Text, err = validText(*in.Text, "question_text", 0, 61440)
			if err != nil {
				return nil, err
			}
		}
		if in.Answer != nil {
			v.Answer, err = validText(*in.Answer, "answer_text", 0, 61440)
			if err != nil {
				return nil, err
			}
		}
		total := len(v.Text) + len(v.Answer)
		for _, q := range e.Questions {
			if q.ID == v.ID {
				continue
			}
			total += len(q.Text) + len(q.Answer)
			if q.PartID == v.PartID && q.Text == v.Text && q.Answer == v.Answer {
				return nil, Conflict("question_duplicate")
			}
		}
		if total > 16*1024*1024 || op == "question-create" && len(e.Questions) >= 5000 {
			return nil, unprocessable("bank_capacity_exceeded")
		}
		changed := i < 0 || !reflect.DeepEqual(v, e.Questions[i])
		if changed {
			if op == "question-create" {
				v.ID = e.Alloc()
				e.Questions = append(e.Questions, v)
			} else {
				v.Revision++
				if e.Questions[i].PartID != v.PartID {
					e.Questions = append(e.Questions[:i], e.Questions[i+1:]...)
					e.Questions = append(e.Questions, v)
				} else {
					e.Questions[i] = v
				}
			}
			bumpContent(e)
		}
		return commandResult(changed, false, "question", v), nil
	case "question-delete":
		var in struct {
			Expected *int64 `json:"expected_revision"`
		}
		if err = strict(input, &in); err != nil {
			return nil, err
		}
		i := questionIndex(e, p.QuestionID)
		if i < 0 {
			return nil, Missing
		}
		if err = checkRevision(in.Expected, e.Questions[i].Revision, "question_modified"); err != nil {
			return nil, err
		}
		e.Questions = append(e.Questions[:i], e.Questions[i+1:]...)
		bumpContent(e)
		return commandResult(true, false, "deleted", p.QuestionID), nil
	case "commission-create", "commission-update":
		var in struct {
			Expected *int64   `json:"expected_revision"`
			Name     *string  `json:"name"`
			IDs      *[]int64 `json:"examinator_ids"`
		}
		if err = strict(input, &in); err != nil {
			return nil, err
		}
		i := commissionIndex(d, p.CommissionID)
		v := Commission{Revision: 1}
		if op == "commission-update" {
			if i < 0 {
				return nil, Missing
			}
			v = d.Commissions[i]
			if err = checkRevision(in.Expected, v.Revision, "commission_modified"); err != nil {
				return nil, err
			}
		} else {
			if in.Expected != nil {
				return nil, Bad("unexpected_revision")
			}
			if in.IDs == nil {
				return nil, Bad("commission_fields_required")
			}
		}
		if in.Name == nil && op == "commission-create" {
			v.Name = "Комиссия " + strconv.FormatInt(e.NextID+1, 10)
			for {
				duplicate := false
				for _, other := range d.Commissions {
					if other.Name == v.Name {
						duplicate = true
						break
					}
				}
				if !duplicate {
					break
				}
				v.Name += " (копия)"
			}
		}
		if in.Name != nil {
			v.Name, err = validText(*in.Name, "commission_name", 120, 0)
			if err != nil {
				return nil, err
			}
		}
		if in.IDs != nil {
			v.Members, err = commissionMembers(*in.IDs, p)
			if err != nil {
				return nil, err
			}
		}
		for _, other := range d.Commissions {
			if other.ID != v.ID && strings.EqualFold(other.Name, v.Name) {
				return nil, Conflict("commission_name_conflict")
			}
		}
		changed := i < 0 || !reflect.DeepEqual(v, d.Commissions[i])
		if changed {
			if op == "commission-create" {
				v.ID = e.Alloc()
				d.Commissions = append(d.Commissions, v)
			} else {
				v.Revision++
				d.Commissions[i] = v
			}
			d.Revision++
		}
		return commandResult(changed, false, "commission", v), nil
	case "commission-delete":
		var in struct {
			Expected *int64 `json:"expected_revision"`
		}
		if err = strict(input, &in); err != nil {
			return nil, err
		}
		i := commissionIndex(d, p.CommissionID)
		if i < 0 {
			return nil, Missing
		}
		if err = checkRevision(in.Expected, d.Commissions[i].Revision, "commission_modified"); err != nil {
			return nil, err
		}
		for i := range d.Assignments {
			if d.Assignments[i].CommissionID == p.CommissionID {
				d.Assignments[i].CommissionID = 0
				d.Assignments[i].Revision++
			}
		}
		d.Commissions = append(d.Commissions[:i], d.Commissions[i+1:]...)
		d.Revision++
		return commandResult(true, false, "deleted", p.CommissionID), nil
	case "assignment-create", "assignment-update":
		var in struct {
			Expected     *int64 `json:"expected_revision"`
			Student      *int64 `json:"student_account_id"`
			CommissionID *int64 `json:"commission_id"`
		}
		if err = strict(input, &in); err != nil {
			return nil, err
		}
		if in.CommissionID == nil {
			return nil, Bad("commission_id_required")
		}
		ci := commissionIndex(d, *in.CommissionID)
		if ci < 0 {
			return nil, Missing
		}
		c := d.Commissions[ci]
		if err = validateExistingMembers(c.Members, p); err != nil {
			return nil, err
		}
		i := assignmentIndex(d, p.AssignmentID)
		v := Assignment{Revision: 1, HistoryGeneration: 1, AttemptNo: 1}
		if op == "assignment-update" {
			if i < 0 {
				return nil, Missing
			}
			v = d.Assignments[i]
			if in.Student != nil {
				return nil, Bad("student_immutable")
			}
			if err = checkRevision(in.Expected, v.Revision, "assignment_modified"); err != nil {
				return nil, err
			}
			if assignmentAttempt(d, v.ID) != nil {
				return nil, Conflict("assignment_has_attempt")
			}
			if v.AttemptNo == 2 {
				f := firstAssignment(d, v.Student.AccountID)
				if f != nil && sameMembers(f.Members, c.Members) {
					return nil, unprocessable("retake_commission_unchanged")
				}
			}
		} else {
			if in.Expected != nil {
				return nil, Bad("unexpected_revision")
			}
			if in.Student == nil {
				return nil, Bad("student_account_id_required")
			}
			v.Student, err = validMember(p, *in.Student, "student")
			if err != nil {
				return nil, err
			}
			if firstAssignment(d, *in.Student) != nil {
				return nil, Conflict("assignment_already_exists")
			}
		}
		v.CommissionID = c.ID
		v.CommissionName = c.Name
		v.Members = append([]Member(nil), c.Members...)
		changed := i < 0 || !reflect.DeepEqual(v, d.Assignments[i])
		if changed {
			if op == "assignment-create" {
				v.ID = e.Alloc()
				d.Assignments = append(d.Assignments, v)
			} else {
				v.Revision++
				d.Assignments[i] = v
			}
			d.Revision++
		}
		return commandResult(changed, false, "assignment", v), nil
	case "assignment-delete":
		var in struct {
			Expected *int64 `json:"expected_revision"`
		}
		if err = strict(input, &in); err != nil {
			return nil, err
		}
		i := assignmentIndex(d, p.AssignmentID)
		if i < 0 {
			return nil, Missing
		}
		if err = checkRevision(in.Expected, d.Assignments[i].Revision, "assignment_modified"); err != nil {
			return nil, err
		}
		if assignmentAttempt(d, p.AssignmentID) != nil {
			return nil, Conflict("assignment_has_attempt")
		}
		d.Assignments = append(d.Assignments[:i], d.Assignments[i+1:]...)
		d.Revision++
		return commandResult(true, false, "deleted", p.AssignmentID), nil
	case "privilege-put", "privilege-delete":
		var in struct {
			Expected *int64 `json:"expected_revision"`
			Limit    *int   `json:"replacement_limit"`
		}
		if err = strict(input, &in); err != nil {
			return nil, err
		}
		if _, err = validMember(p, p.StudentID, "student"); err != nil {
			return nil, err
		}
		if d.Privileges == nil {
			d.Privileges = map[int64]Privilege{}
		}
		v := d.Privileges[p.StudentID]
		if err = checkRevision(in.Expected, v.Revision, "privilege_modified"); err != nil {
			return nil, err
		}
		changed := false
		if op == "privilege-delete" {
			if in.Limit != nil {
				return nil, Bad("unexpected_replacement_limit")
			}
			_, changed = d.Privileges[p.StudentID]
			delete(d.Privileges, p.StudentID)
			v = Privilege{}
		} else {
			if in.Limit == nil || *in.Limit < 0 || *in.Limit > 100 {
				return nil, Bad("invalid_replacement_limit")
			}
			changed = v.Revision == 0 || v.Limit != *in.Limit
			if changed {
				v.Limit = *in.Limit
				v.Revision++
				d.Privileges[p.StudentID] = v
			}
		}
		if changed {
			d.Revision++
		}
		return commandResult(changed, false, "privilege", v), nil
	case "appeal":
		var in struct {
			Grade    *int   `json:"grade"`
			Expected *int64 `json:"expected_result_version"`
		}
		if err = strict(input, &in); err != nil {
			return nil, err
		}
		i := attemptIndex(d, p.AttemptID)
		if i < 0 {
			return nil, Missing
		}
		at := &d.Attempts[i]
		if at.Status != "completed" {
			return nil, unprocessable("attempt_not_completed")
		}
		if in.Expected == nil {
			return nil, Bad("expected_result_version_required")
		}
		if err = checkRevision(in.Expected, at.ResultVersion, "result_modified"); err != nil {
			return nil, err
		}
		if in.Grade == nil || *in.Grade < 0 || *in.Grade > 5 {
			return nil, Bad("invalid_grade")
		}
		ap := Appeal{ID: e.Alloc(), PreviousGrade: effectiveGrade(at), Grade: *in.Grade, ActorID: actorID(a), At: p.Now}
		at.Appeals = append(at.Appeals, ap)
		at.ResultVersion++
		at.StateVersion++
		d.Revision++
		current := effectiveAttempt(d, at.Student.AccountID)
		return commandResult(true, current != nil && current.ID == at.ID, "appeal", ap), nil
	case "retake":
		var in struct {
			CommissionID *int64 `json:"commission_id"`
			Expected     *int64 `json:"expected_history_generation"`
		}
		if err = strict(input, &in); err != nil {
			return nil, err
		}
		f := firstAssignment(d, p.StudentID)
		if f == nil {
			return nil, unprocessable("first_attempt_not_completed")
		}
		if err = checkHistory(in.Expected, f.HistoryGeneration); err != nil {
			return nil, err
		}
		at := assignmentAttempt(d, f.ID)
		if at == nil || at.Status != "completed" {
			return nil, unprocessable("first_attempt_not_completed")
		}
		for _, as := range d.Assignments {
			if as.Student.AccountID == p.StudentID && as.AttemptNo == 2 {
				return nil, Conflict("retake_already_assigned")
			}
		}
		if in.CommissionID == nil {
			return nil, Bad("commission_id_required")
		}
		ci := commissionIndex(d, *in.CommissionID)
		if ci < 0 {
			return nil, Missing
		}
		c := d.Commissions[ci]
		if err = validateExistingMembers(c.Members, p); err != nil {
			return nil, err
		}
		if sameMembers(f.Members, c.Members) {
			return nil, unprocessable("retake_commission_unchanged")
		}
		v := Assignment{ID: e.Alloc(), Student: f.Student, CommissionID: c.ID, CommissionName: c.Name, Members: append([]Member(nil), c.Members...), AttemptNo: 2, HistoryGeneration: f.HistoryGeneration, Revision: 1}
		d.Assignments = append(d.Assignments, v)
		d.Revision++
		return commandResult(true, false, "assignment", v), nil
	case "history-delete":
		var in struct {
			Expected *int64 `json:"expected_history_generation"`
		}
		if err = strict(input, &in); err != nil {
			return nil, err
		}
		f := firstAssignment(d, p.StudentID)
		if f == nil {
			return nil, Missing
		}
		if err = checkHistory(in.Expected, f.HistoryGeneration); err != nil {
			return nil, err
		}
		rating := effectiveAttempt(d, p.StudentID) != nil
		f.HistoryGeneration++
		f.Revision++
		generation := f.HistoryGeneration
		assignments := []Assignment{}
		for _, as := range d.Assignments {
			if as.Student.AccountID != p.StudentID || as.AttemptNo == 1 {
				assignments = append(assignments, as)
			}
		}
		attempts := []Attempt{}
		for _, at := range d.Attempts {
			if at.Student.AccountID != p.StudentID {
				attempts = append(attempts, at)
			}
		}
		d.Assignments = assignments
		d.Attempts = attempts
		d.Revision++
		return commandResult(true, rating, "history_generation", generation), nil
	case "outside-create", "outside-update", "outside-delete":
		return outsideCommand(e, d, op, p, input)
	default:
		return nil, Missing
	}
}

func oneOf(v string, values ...string) bool {
	for _, x := range values {
		if v == x {
			return true
		}
	}
	return false
}
func commissionMembers(ids []int64, p Params) ([]Member, error) {
	if len(ids) < 1 || len(ids) > 6 {
		return nil, Bad("invalid_commission_size")
	}
	seen := map[int64]bool{}
	ms := []Member{}
	for _, id := range ids {
		if seen[id] {
			return nil, Bad("duplicate_examinator")
		}
		seen[id] = true
		m, err := validMember(p, id, "examinator")
		if err != nil {
			return nil, err
		}
		ms = append(ms, m)
	}
	return ms, nil
}
func validateExistingMembers(ms []Member, p Params) error {
	if len(ms) < 1 || len(ms) > 6 {
		return Bad("invalid_commission_size")
	}
	for _, m := range ms {
		if p.Members != nil {
			if _, err := validMember(p, m.AccountID, "examinator"); err != nil {
				return err
			}
		} else if !m.Active || m.Role != "examinator" {
			return Bad("examinator_not_found")
		}
	}
	return nil
}
func outsideCommand(e *Exam, d *Division, op string, p Params, input json.RawMessage) (any, error) {
	var in struct {
		Expected   *int64        `json:"expected_revision"`
		Student    *int64        `json:"student_account_id"`
		Points     *numericInput `json:"points"`
		Grade      *int          `json:"grade"`
		Examinator *string       `json:"examinator"`
	}
	if err := strict(input, &in); err != nil {
		return nil, err
	}
	i := outsideIndex(d, p.ResultID)
	v := OutsideResult{Revision: 1}
	if op != "outside-create" {
		if i < 0 {
			return nil, Missing
		}
		v = d.Outside[i]
		if err := checkRevision(in.Expected, v.Revision, "result_modified"); err != nil {
			return nil, err
		}
		if in.Student != nil {
			return nil, Bad("student_immutable")
		}
	} else {
		if in.Expected != nil {
			return nil, Bad("unexpected_revision")
		}
		if in.Student == nil || in.Points == nil || in.Grade == nil || in.Examinator == nil {
			return nil, Bad("outside_fields_required")
		}
		m, err := validMember(p, *in.Student, "student")
		if err != nil {
			return nil, err
		}
		v.Student = m
		for _, other := range d.Outside {
			if other.Student.AccountID == m.AccountID {
				return nil, Conflict("outside_result_already_exists")
			}
		}
	}
	if op == "outside-delete" {
		if in.Points != nil || in.Grade != nil || in.Examinator != nil {
			return nil, Bad("unexpected_fields")
		}
		d.Outside = append(d.Outside[:i], d.Outside[i+1:]...)
		d.Revision++
		return commandResult(true, true, "deleted", p.ResultID), nil
	}
	if in.Points != nil {
		n, err := outsidePoints(in.Points.Number)
		if err != nil {
			return nil, err
		}
		v.Points = n
	}
	if in.Grade != nil {
		if *in.Grade < 0 || *in.Grade > 5 {
			return nil, Bad("invalid_grade")
		}
		v.Grade = *in.Grade
	}
	if in.Examinator != nil {
		name, err := validText(*in.Examinator, "examinator", 255, 0)
		if err != nil {
			return nil, err
		}
		v.Examinator = name
	}
	changed := i < 0 || !reflect.DeepEqual(v, d.Outside[i])
	if changed {
		if op == "outside-create" {
			v.ID = e.Alloc()
			d.Outside = append(d.Outside, v)
		} else {
			v.Revision++
			d.Outside[i] = v
		}
		d.Revision++
	}
	return commandResult(changed, changed, "result", v), nil
}
func outsidePoints(n json.Number) (json.Number, error) {
	r, ok := new(big.Rat).SetString(string(n))
	if !ok || r.Sign() < 0 || r.Cmp(big.NewRat(999999999999, 100)) > 0 {
		return "", Bad("invalid_points")
	}
	hundred := new(big.Rat).Mul(r, big.NewRat(100, 1))
	if !hundred.IsInt() {
		return "", Bad("invalid_points")
	}
	return json.Number(r.FloatString(2)), nil
}

type readinessError struct {
	Code      string `json:"code"`
	EntityID  int64  `json:"entity_id,omitempty"`
	Required  int    `json:"required,omitempty"`
	Available int    `json:"available,omitempty"`
	Blocking  bool   `json:"blocking"`
}

func configurationErrors(e *Exam, d *Division, reserve int) []readinessError {
	errors := []readinessError{}
	add := func(code string, id int64, required, available int) {
		errors = append(errors, readinessError{code, id, required, available, true})
	}
	c := d.Config
	if c.StartAt == nil {
		add("start_at_required", 0, 0, 0)
	}
	if c.EndAt == nil {
		add("end_at_required", 0, 0, 0)
	}
	if c.FractionalMode == "" {
		add("fractional_mode_required", 0, 0, 0)
	}
	if !oneOf(c.FractionalMode, "", "round_up", "round_down", "extra_question") {
		add("invalid_fractional_mode", 0, 0, 0)
	}
	if c.StartAt != nil && c.EndAt != nil && !c.EndAt.After(*c.StartAt) {
		add("invalid_time_window", 0, 0, 0)
	}
	if len(e.Parts) == 0 {
		add("parts_required", 0, 0, 0)
	}
	for _, code := range validateThresholds(maxScore(e.Parts), c.Thresholds) {
		add(code, 0, 0, 0)
	}
	for _, part := range e.Parts {
		if part.Weight < 1 || part.Weight > 1000 || part.Quota < 1 || part.Quota > 100 {
			add("part_settings_required", part.ID, 0, 0)
		}
		available := 0
		for _, q := range e.Questions {
			if q.PartID == part.ID {
				available++
			}
		}
		if available < part.Quota+reserve {
			add("part_bank_too_small", part.ID, part.Quota+reserve, available)
		}
	}
	if c.FractionalMode == "extra_question" {
		if !oneOf(c.TieBreakerSourceMode, "any_part", "specific_part") {
			add("tie_breaker_source_mode_required", 0, 0, 0)
		}
		if !oneOf(c.TieBreakerHalfMode, "repeat", "round_up", "round_down") {
			add("tie_breaker_half_mode_required", 0, 0, 0)
		}
		if c.TieBreakerSourceMode == "specific_part" && partIndex(e, c.TieBreakerPartID) < 0 {
			add("tie_breaker_part_required", 0, 0, 0)
		}
		size := 0
		for _, q := range e.Questions {
			if c.TieBreakerSourceMode == "any_part" || q.PartID == c.TieBreakerPartID {
				size++
			}
		}
		if c.TieBreakerSourceMode != "" && size < reserve+2 {
			add("tie_breaker_bank_too_small", 0, reserve+2, size)
		}
	}
	return errors
}
func readiness(e *Exam, d *Division, as *Assignment, p Params, window bool) map[string]any {
	base := configurationErrors(e, d, 0)
	errors := append([]readinessError(nil), base...)
	if errors == nil {
		errors = []readinessError{}
	}
	if as != nil {
		errors = configurationErrors(e, d, d.Privileges[as.Student.AccountID].Limit)
		if errors == nil {
			errors = []readinessError{}
		}
		if err := validateExistingMembers(as.Members, p); err != nil {
			errors = append(errors, readinessError{Code: err.Error(), Blocking: true})
		}
		student := as.Student
		if p.Members != nil {
			student = p.Members[as.Student.AccountID]
		}
		if !student.Active || student.Role != "student" {
			errors = append(errors, readinessError{Code: "student_not_found", EntityID: as.Student.AccountID, Blocking: true})
		}
	}
	assignmentReady := len(errors) == 0
	canStart := assignmentReady
	if window && (d.Config.StartAt == nil || d.Config.EndAt == nil || p.Now.Before(*d.Config.StartAt) || p.Now.After(*d.Config.EndAt)) {
		errors = append(errors, readinessError{Code: "exam_outside_window", Blocking: true})
		canStart = false
	}
	if as != nil {
		at := assignmentAttempt(d, as.ID)
		canStart = canStart && at != nil && at.Status == "pending_ready" && allReady(at)
	}
	return map[string]any{"is_configured": len(base) == 0, "configuration_ready": len(base) == 0, "assignment_ready": assignmentReady, "can_prepare": assignmentReady, "can_start_now": canStart, "errors": errors, "warnings": []string{}, "checked_at": p.Now}
}
func assertPrepare(e *Exam, d *Division, as *Assignment, p Params, window bool) error {
	report := readiness(e, d, as, p, window)
	errors := report["errors"].([]readinessError)
	if len(errors) > 0 {
		return unprocessable(errors[0].Code)
	}
	return nil
}
func allReady(at *Attempt) bool {
	if len(at.Members) == 0 {
		return false
	}
	for _, m := range at.Members {
		if !at.Ready[m.AccountID] {
			return false
		}
	}
	return true
}
func currentQuestion(at *Attempt) *Presented {
	if at.Status != "in_progress" || len(at.Presented) == 0 {
		return nil
	}
	return &at.Presented[len(at.Presented)-1]
}
func present(e *Exam, at *Attempt, purpose string, choice questionChoice, replaces int64, now time.Time) int64 {
	if at.Usage == nil {
		at.Usage = map[int64]Usage{}
	}
	if at.Cycles == nil {
		at.Cycles = map[int64]int{}
	}
	id := e.Alloc()
	round := Round{ID: e.Alloc(), Number: 1, Status: "open", Votes: map[int64]int{}, OpenedAt: now}
	at.Presented = append(at.Presented, Presented{ID: id, Question: choice.question, Sequence: len(at.Presented) + 1, Purpose: purpose, Status: "open", Cycle: choice.cycle, ReplacesID: replaces, Rounds: []Round{round}, PresentedAt: now})
	at.Usage[choice.question.ID] = Usage{Presented: true, Cycle: choice.cycle}
	at.Cycles[choice.partID] = choice.cycle
	return id
}
func chooseAndPresent(e *Exam, at *Attempt, purpose string, now time.Time) (int64, error) {
	choice, err := selectQuestion(at, purpose, 0, 0, securePick)
	if err != nil {
		return 0, err
	}
	return present(e, at, purpose, choice, 0, now), nil
}
func finishOrExtra(e *Exam, at *Attempt, consensus int, p Params) (map[string]any, error) {
	raw := 0
	for _, q := range at.Presented {
		if q.Purpose == "regular" && q.Status == "consensus" {
			raw += q.AwardedHalf
		}
	}
	at.RawHalf = raw
	at.MaxScore = maxScore(at.Parts)
	direction, resolution := "down", "integer"
	if at.Phase == "tie_breaker" {
		var err error
		direction, err = extraDirection(consensus, at.Config.TieBreakerHalfMode)
		if err != nil {
			return nil, err
		}
		if direction == "repeat" {
			id, err := chooseAndPresent(e, at, "tie_breaker", p.Now)
			return map[string]any{"outcome": "next_extra", "presented_question_id": id}, err
		}
		resolution = "tie_breaker_" + direction
	} else if raw%2 != 0 {
		if at.Config.FractionalMode == "extra_question" {
			at.Phase = "tie_breaker"
			id, err := chooseAndPresent(e, at, "tie_breaker", p.Now)
			return map[string]any{"outcome": "next_extra", "presented_question_id": id}, err
		}
		if at.Config.FractionalMode == "round_up" {
			direction = "up"
		}
		resolution = "configured_" + direction
	}
	at.RoundedTotal = roundHalf(raw, direction == "up")
	at.CalculatedGrade = gradeForScore(at.RoundedTotal, at.Config.Thresholds)
	at.Resolution = resolution
	at.Status = "completed"
	at.Phase = "completed"
	timestamp := p.Now
	at.CompletedAt = &timestamp
	return map[string]any{"outcome": "completed", "attempt_id": at.ID}, nil
}
func lifecycleCommand(e *Exam, d *Division, a Actor, op string, p Params, input json.RawMessage) (any, error) {
	if a.Role != "examinator" {
		return nil, Denied
	}
	if op == "ensure" {
		var in struct {
			Expected *int64 `json:"expected_history_generation"`
		}
		if err := strict(input, &in); err != nil {
			return nil, err
		}
		i := assignmentIndex(d, p.AssignmentID)
		if i < 0 || !examiner(a, d.Assignments[i].Members) {
			return nil, Missing
		}
		as := &d.Assignments[i]
		f := firstAssignment(d, as.Student.AccountID)
		if f == nil {
			return nil, Missing
		}
		if err := checkHistory(in.Expected, f.HistoryGeneration); err != nil {
			return nil, err
		}
		if at := assignmentAttempt(d, as.ID); at != nil {
			return lifecycleResponse(e, d, at, a, p, false, "existing", nil), nil
		}
		if err := assertPrepare(e, d, as, p, false); err != nil {
			return nil, err
		}
		if as.AttemptNo == 2 {
			at := assignmentAttempt(d, f.ID)
			if at == nil || at.Status != "completed" {
				return nil, unprocessable("first_attempt_not_completed")
			}
		}
		at := Attempt{ID: e.Alloc(), AssignmentID: as.ID, Student: as.Student, Members: append([]Member(nil), as.Members...), Ready: map[int64]bool{}, ReadyAt: map[int64]time.Time{}, AttemptNo: as.AttemptNo, HistoryGeneration: as.HistoryGeneration, Status: "pending_ready", Phase: "preparation", StateVersion: 1, ResultVersion: 1, Progress: map[int64]int{}, Cycles: map[int64]int{}, Usage: map[int64]Usage{}}
		d.Attempts = append(d.Attempts, at)
		d.Revision++
		return lifecycleResponse(e, d, &d.Attempts[len(d.Attempts)-1], a, p, true, "created", nil), nil
	}
	i := attemptIndex(d, p.AttemptID)
	if i < 0 || !examiner(a, d.Attempts[i].Members) {
		return nil, Missing
	}
	at := &d.Attempts[i]
	changed := true
	outcome := ""
	extra := map[string]any{}
	rating := false
	switch op {
	case "ready":
		var in struct{}
		if err := strict(input, &in); err != nil {
			return nil, err
		}
		if at.Status != "pending_ready" {
			return nil, Conflict("attempt_not_pending")
		}
		if at.Ready[actorID(a)] {
			changed = false
			outcome = "already_ready"
		} else {
			as := assignmentIndex(d, at.AssignmentID)
			if as < 0 {
				return nil, Missing
			}
			if err := assertPrepare(e, d, &d.Assignments[as], p, false); err != nil {
				return nil, err
			}
			at.Ready[actorID(a)] = true
			if at.ReadyAt == nil {
				at.ReadyAt = map[int64]time.Time{}
			}
			at.ReadyAt[actorID(a)] = p.Now
			outcome = "ready"
		}
	case "start":
		var in struct {
			Expected *int64 `json:"expected_state_version"`
		}
		if err := strict(input, &in); err != nil {
			return nil, err
		}
		if in.Expected == nil || *in.Expected < 1 {
			return nil, Bad("expected_state_version_required")
		}
		if at.Status != "pending_ready" {
			changed = false
			outcome = "already_started"
			break
		}
		if err := checkRevision(in.Expected, at.StateVersion, "stale_state"); err != nil {
			return nil, err
		}
		if !allReady(at) {
			return nil, unprocessable("commission_not_ready")
		}
		as := assignmentIndex(d, at.AssignmentID)
		if as < 0 {
			return nil, Missing
		}
		if err := assertPrepare(e, d, &d.Assignments[as], p, true); err != nil {
			return nil, err
		}
		at.Config = d.Config
		at.Config.Thresholds = append([]Threshold(nil), d.Config.Thresholds...)
		at.Parts = append([]Part(nil), e.Parts...)
		at.Questions = append([]Question(nil), e.Questions...)
		at.ContentRevision = e.ContentRevision
		at.ReplacementLimit = d.Privileges[at.Student.AccountID].Limit
		now := p.Now
		at.StartedAt = &now
		at.Status = "in_progress"
		at.Phase = "regular_questions"
		at.MaxScore = maxScore(at.Parts)
		for _, part := range at.Parts {
			at.Progress[part.ID] = 0
			at.Cycles[part.ID] = 1
		}
		id, err := chooseAndPresent(e, at, "regular", p.Now)
		if err != nil {
			return nil, err
		}
		outcome = "started"
		extra["presented_question_id"] = id
	case "vote":
		var in struct {
			PresentedID *int64        `json:"presented_question_id"`
			RoundID     *int64        `json:"round_id"`
			Value       *numericInput `json:"value"`
		}
		if err := strict(input, &in); err != nil {
			return nil, err
		}
		if in.PresentedID == nil || in.RoundID == nil || in.Value == nil {
			return nil, Bad("vote_fields_required")
		}
		value, err := voteHalf(in.Value.Number)
		if err != nil {
			return nil, err
		}
		if at.Status != "in_progress" {
			return nil, unprocessable("attempt_not_active")
		}
		q := currentQuestion(at)
		if q == nil || q.ID != *in.PresentedID {
			return nil, Conflict("question_not_current")
		}
		if q.Status != "open" {
			return nil, Conflict("question_already_resolved")
		}
		if len(q.Rounds) == 0 {
			return nil, Conflict("round_not_current")
		}
		round := &q.Rounds[len(q.Rounds)-1]
		if round.ID != *in.RoundID || round.Status != "open" {
			return nil, Conflict("round_not_current")
		}
		if _, ok := round.Votes[actorID(a)]; ok {
			return nil, Conflict("vote_already_cast")
		}
		round.Votes[actorID(a)] = value
		if round.VoteTimes == nil {
			round.VoteTimes = map[int64]time.Time{}
		}
		round.VoteTimes[actorID(a)] = p.Now
		outcome = "vote_recorded"
		extra["presented_question_id"] = q.ID
		extra["round_id"] = round.ID
		if len(round.Votes) == len(at.Members) {
			unanimous := true
			for _, v := range round.Votes {
				if v != value {
					unanimous = false
				}
			}
			if !unanimous {
				round.Status = "disputed"
				timestamp := p.Now
				round.CompletedAt = &timestamp
				next := Round{ID: e.Alloc(), Number: round.Number + 1, Status: "open", Votes: map[int64]int{}, OpenedAt: p.Now}
				q.Rounds = append(q.Rounds, next)
				outcome = "disputed"
				extra["next_round_id"] = next.ID
			} else {
				round.Status = "consensus"
				timestamp := p.Now
				round.CompletedAt = &timestamp
				q.ConsensusAt = &timestamp
				round.Consensus = &value
				q.Status = "consensus"
				q.Consensus = &value
				outcome = "consensus"
				extra["resolved_question_id"] = q.ID
				extra["consensus"] = halfNumber(value)
				if q.Purpose == "regular" {
					weight := 0
					for _, part := range at.Parts {
						if part.ID == q.Question.PartID {
							weight = part.Weight
						}
					}
					q.AwardedHalf = value * weight
					at.Progress[q.Question.PartID]++
					remaining := 0
					for _, part := range at.Parts {
						remaining += part.Quota - at.Progress[part.ID]
					}
					if remaining == 0 {
						result, err := finishOrExtra(e, at, value, p)
						if err != nil {
							return nil, err
						}
						for k, v := range result {
							extra[k] = v
						}
						outcome = result["outcome"].(string)
					}
				} else {
					result, err := finishOrExtra(e, at, value, p)
					if err != nil {
						return nil, err
					}
					for k, v := range result {
						extra[k] = v
					}
					outcome = result["outcome"].(string)
				}
				rating = at.Status == "completed"
			}
		}
	case "next-question", "replace-question":
		var in struct {
			Expected    *int64 `json:"expected_state_version"`
			PresentedID *int64 `json:"presented_question_id"`
		}
		if err := strict(input, &in); err != nil {
			return nil, err
		}
		if in.Expected == nil {
			return nil, Bad("expected_state_version_required")
		}
		if err := checkRevision(in.Expected, at.StateVersion, "stale_state"); err != nil {
			return nil, err
		}
		if at.Status != "in_progress" {
			return nil, unprocessable("attempt_not_active")
		}
		q := currentQuestion(at)
		if in.PresentedID == nil || q == nil || q.ID != *in.PresentedID {
			return nil, Conflict("question_not_current")
		}
		if op == "next-question" {
			if q.Status != "consensus" || q.Purpose != "regular" {
				return nil, Conflict("question_not_resolved")
			}
			remaining := 0
			for _, part := range at.Parts {
				remaining += part.Quota - at.Progress[part.ID]
			}
			if remaining == 0 {
				return nil, Conflict("no_regular_questions_remaining")
			}
			id, err := chooseAndPresent(e, at, "regular", p.Now)
			if err != nil {
				return nil, err
			}
			outcome = "next_question"
			extra["presented_question_id"] = id
		} else {
			if q.Status != "open" {
				return nil, Conflict("question_already_resolved")
			}
			for _, round := range q.Rounds {
				if len(round.Votes) > 0 {
					return nil, Conflict("replacement_after_vote")
				}
			}
			if at.ReplacementUsed >= at.ReplacementLimit {
				return nil, unprocessable("replacement_limit_exhausted")
			}
			choice, err := selectQuestion(at, q.Purpose, q.Question.PartID, q.Question.ID, securePick)
			if err != nil {
				return nil, err
			}
			q.Status = "replaced"
			timestamp := p.Now
			q.ReplacedAt = &timestamp
			q.ReplacedBy = actorID(a)
			for i := range q.Rounds {
				if q.Rounds[i].Status == "open" {
					q.Rounds[i].Status = "voided"
					q.Rounds[i].CompletedAt = &timestamp
				}
			}
			u := at.Usage[q.Question.ID]
			u.Excluded = true
			at.Usage[q.Question.ID] = u
			at.ReplacementUsed++
			previous, purpose := q.ID, q.Purpose
			id := present(e, at, purpose, choice, previous, p.Now)
			outcome = "replaced"
			extra["replaced_question_id"] = previous
			extra["presented_question_id"] = id
		}
	default:
		return nil, Missing
	}
	if changed {
		at.StateVersion++
		d.Revision++
	}
	response := lifecycleResponse(e, d, at, a, p, changed, outcome, extra)
	response["rating_changed"] = rating
	return response, nil
}
func lifecycleResponse(e *Exam, d *Division, at *Attempt, a Actor, p Params, changed bool, outcome string, extra map[string]any) map[string]any {
	receipt := map[string]any{"outcome": outcome, "attempt_id": at.ID, "applied_state_version": at.StateVersion}
	for k, v := range extra {
		receipt[k] = v
	}
	return map[string]any{"changed": changed, "rating_changed": false, "outcome": outcome, "receipt": receipt, "attempt": attemptDTO(e, d, at, a, p), "created": outcome == "created"}
}
