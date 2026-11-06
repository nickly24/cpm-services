package exams

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Select lightweight indexes first. Rich DTOs (protocols and results in particular)
// are materialized only for the page which is actually returned.
func pageWindow(total int, p Params) (int, int, int, error) {
	limit, offset := 50, 0
	var err error
	if v := p.Query["limit"]; v != "" {
		limit, err = strconv.Atoi(v)
		if err != nil || limit < 1 || limit > 200 {
			return 0, 0, 0, Bad("invalid_limit")
		}
	}
	if v := p.Query["offset"]; v != "" {
		offset, err = strconv.Atoi(v)
		if err != nil || offset < 0 {
			return 0, 0, 0, Bad("invalid_offset")
		}
	}
	start := min(offset, total)
	return start, min(start+limit, total), limit, nil
}
func selectedPage[T any](rows []T, p Params, convert func(T) any) (any, error) {
	start, end, limit, err := pageWindow(len(rows), p)
	if err != nil {
		return nil, err
	}
	items := make([]any, 0, end-start)
	for _, row := range rows[start:end] {
		items = append(items, convert(row))
	}
	return map[string]any{"items": items, "total": len(rows), "limit": limit, "offset": start}, nil
}
func queryMatches(p Params, values ...any) bool {
	q := strings.ToLower(strings.TrimSpace(p.Query["q"]))
	if q == "" {
		return true
	}
	for _, v := range values {
		if strings.Contains(strings.ToLower(fmt.Sprint(v)), q) {
			return true
		}
	}
	return false
}
func attemptByAssignment(d *Division) map[int64]*Attempt {
	m := make(map[int64]*Attempt, len(d.Attempts))
	for i := range d.Attempts {
		m[d.Attempts[i].AssignmentID] = &d.Attempts[i]
	}
	return m
}
func assignmentStatus(as *Assignment, attempts map[int64]*Attempt) string {
	if at := attempts[as.ID]; at != nil {
		return at.Status
	}
	return "not_started"
}
func filteredAssignments(d *Division, a Actor, p Params) []*Assignment {
	ats := attemptByAssignment(d)
	rows := []*Assignment{}
	for i := range d.Assignments {
		as := &d.Assignments[i]
		if !a.Can(false) && !examiner(a, as.Members) {
			continue
		}
		if p.StudentID > 0 && as.Student.AccountID != p.StudentID {
			continue
		}
		if s := p.Query["status"]; s != "" && assignmentStatus(as, ats) != s {
			continue
		}
		if n := p.Query["attempt_no"]; n != "" && strconv.Itoa(as.AttemptNo) != n {
			continue
		}
		if !queryMatches(p, as.Student.FullName, as.Student.Login, as.Student.AccountID, as.CommissionName) {
			continue
		}
		rows = append(rows, as)
	}
	return rows
}
func assignmentPage(e *Exam, d *Division, a Actor, p Params) (any, error) {
	rows := filteredAssignments(d, a, p)
	ats := attemptByAssignment(d)
	validation := map[int][]readinessError{}
	if p.Query["include_readiness"] == "true" {
		validation[0] = configurationErrors(e, d, 0)
	}
	return selectedPage(rows, p, func(as *Assignment) any {
		// A one-element view avoids an O(assignments*attempts) scan in the legacy DTO.
		view := *d
		view.Attempts = nil
		if at := ats[as.ID]; at != nil {
			view.Attempts = []Attempt{*at}
		}
		row := assignmentDTO(&view, as)
		if p.Query["include_readiness"] == "true" {
			reserve := d.Privileges[as.Student.AccountID].Limit
			errors, ok := validation[reserve]
			if !ok {
				errors = configurationErrors(e, d, reserve)
				validation[reserve] = errors
			}
			row["readiness"] = readinessReport(&view, as, p, true, validation[0], errors)
		}
		return row
	})
}
func pagedQuery(e *Exam, d *Division, a Actor, op string, p Params) (any, bool, error) {
	done := func(v any, err error) (any, bool, error) { return v, true, err }
	switch op {
	case "protocol", "rounds":
		// Preserve legacy role-shaped JSON search, while ordinary history
		// pagination materializes only the requested questions/rounds.
		if strings.TrimSpace(p.Query["q"]) != "" {
			return nil, false, nil
		}
		index := attemptIndex(d, p.AttemptID)
		if index < 0 {
			return nil, true, Missing
		}
		at := &d.Attempts[index]
		if !canReadAttempt(at, a, a.Can(false)) {
			return nil, true, Missing
		}
		if op == "rounds" && a.Role == "student" {
			return nil, true, Denied
		}
		anchorKey := "through_sequence_no"
		upper := len(at.Presented)
		var question *Presented
		if op == "rounds" {
			anchorKey = "through_round_no"
			for i := range at.Presented {
				if at.Presented[i].ID == p.PresentedID {
					question = &at.Presented[i]
					break
				}
			}
			if question == nil || len(question.Rounds) == 0 {
				return nil, true, Missing
			}
			upper = len(question.Rounds)
		}
		anchor := upper
		if raw := p.Query[anchorKey]; raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 {
				return nil, true, Bad("invalid_" + anchorKey)
			}
			anchor = min(anchor, n)
		}
		var value any
		var err error
		if op == "rounds" {
			rows := []*Round{}
			for i := range question.Rounds {
				if question.Rounds[i].Number <= anchor {
					rows = append(rows, &question.Rounds[i])
				}
			}
			value, err = selectedPage(rows, p, func(r *Round) any { return roundDTO(r, a, len(at.Members), a.Can(false), at.Members) })
		} else {
			rows := []*Presented{}
			for i := range at.Presented {
				if at.Presented[i].Sequence <= anchor {
					rows = append(rows, &at.Presented[i])
				}
			}
			value, err = selectedPage(rows, p, func(q *Presented) any {
				row := questionDTO(at, q, a, a.Can(false))
				if a.Role == "student" {
					for _, key := range []string{"round", "rounds", "can_replace", "can_go_next"} {
						delete(row, key)
					}
				}
				return row
			})
		}
		if err != nil {
			return nil, true, err
		}
		value.(map[string]any)[anchorKey] = anchor
		return value, true, nil
	case "overview":
		if !a.Can(false) {
			return nil, true, Denied
		}
		detail, err := Query(e, a, "detail", p)
		if err != nil {
			return nil, true, err
		}
		counts := map[string]int{"parts": len(e.Parts), "questions": len(e.Questions), "commissions": len(d.Commissions), "assignments": len(d.Assignments), "results": 0, "pending": 0, "in_progress": 0, "completed": 0}
		students := map[int64]bool{}
		sum := 0
		if e.Kind == "outside_lms" {
			counts["results"] = len(d.Outside)
			for _, r := range d.Outside {
				sum += r.Grade
			}
		} else {
			for _, at := range d.Attempts {
				switch at.Status {
				case "pending_ready":
					counts["pending"]++
				case "in_progress":
					counts["in_progress"]++
				case "completed":
					counts["completed"]++
					students[at.Student.AccountID] = true
				}
			}
			counts["results"] = len(students)
		}
		var avg any
		if e.Kind == "outside_lms" && counts["results"] > 0 {
			avg = float64(sum) / float64(counts["results"])
		}
		return map[string]any{"exam": detail, "counts": counts, "average_grade": avg}, true, nil
	case "parts":
		sizes := map[int64]int{}
		for _, q := range e.Questions {
			sizes[q.PartID]++
		}
		rows := []Part{}
		for _, part := range e.Parts {
			if (p.PartID == 0 || part.ID == p.PartID) && queryMatches(p, part.Code, part.ID) {
				rows = append(rows, part)
			}
		}
		v, err := selectedPage(rows, p, func(part Part) any {
			return map[string]any{"id": part.ID, "code": part.Code, "weight": part.Weight, "quota": part.Quota, "revision": part.Revision, "bank_size": sizes[part.ID]}
		})
		return done(v, err)
	case "questions":
		if raw := p.Query["part_id"]; raw != "" {
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || id < 1 {
				return nil, true, Bad("invalid_part_id")
			}
			p.PartID = id
		}
		order := map[int64]int{}
		parts := map[int64]string{}
		for i, part := range e.Parts {
			order[part.ID] = i
			parts[part.ID] = part.Code
		}
		rows := []*Question{}
		for i := range e.Questions {
			q := &e.Questions[i]
			if (p.PartID == 0 || q.PartID == p.PartID) && queryMatches(p, q.Text, q.Answer, parts[q.PartID], q.ID) {
				rows = append(rows, q)
			}
		}
		sort.SliceStable(rows, func(i, j int) bool { return order[rows[i].PartID] < order[rows[j].PartID] })
		v, err := selectedPage(rows, p, func(q *Question) any {
			return map[string]any{"id": q.ID, "part_id": q.PartID, "part_code": parts[q.PartID], "question_text": q.Text, "answer_text": q.Answer, "revision": q.Revision}
		})
		return done(v, err)
	case "commissions":
		counts := map[int64]int{}
		for _, as := range d.Assignments {
			counts[as.CommissionID]++
		}
		rows := []Commission{}
		for _, c := range d.Commissions {
			if queryMatches(p, c.Name, c.ID, c.Members) {
				rows = append(rows, c)
			}
		}
		v, err := selectedPage(rows, p, func(c Commission) any {
			return map[string]any{"id": c.ID, "name": c.Name, "members": c.Members, "revision": c.Revision, "assigned_students_count": counts[c.ID]}
		})
		return done(v, err)
	case "assignments":
		v, err := assignmentPage(e, d, a, p)
		return done(v, err)
	case "privileges":
		people := map[int64]Member{}
		for _, as := range d.Assignments {
			people[as.Student.AccountID] = as.Student
		}
		ids := []int64{}
		for id := range d.Privileges {
			if (p.StudentID == 0 || p.StudentID == id) && queryMatches(p, id, people[id].FullName, people[id].Login) {
				ids = append(ids, id)
			}
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		v, err := selectedPage(ids, p, func(id int64) any {
			v := d.Privileges[id]
			person := people[id]
			person.AccountID = id
			return map[string]any{"student_account_id": id, "student": person, "replacement_limit": v.Limit, "revision": v.Revision}
		})
		return done(v, err)
	case "attempts":
		rows := []*Attempt{}
		for i := range d.Attempts {
			at := &d.Attempts[i]
			if !canReadAttempt(at, a, a.Can(false)) || p.StudentID > 0 && at.Student.AccountID != p.StudentID || p.Query["status"] != "" && p.Query["status"] != at.Status || p.Query["attempt_no"] != "" && p.Query["attempt_no"] != strconv.Itoa(at.AttemptNo) || !queryMatches(p, at.Student.FullName, at.Student.AccountID, at.Student.Login) {
				continue
			}
			rows = append(rows, at)
		}
		v, err := selectedPage(rows, p, func(at *Attempt) any { row := attemptDTO(e, d, at, a, p); delete(row, "current_question"); return row })
		return done(v, err)
	case "results":
		filter, err := ParseExamDateFilter(p.Query)
		if err != nil {
			return nil, true, err
		}
		if !filter.Match(e, p.DivisionID) {
			v, err := selectedPage([]int{}, p, func(int) any { return nil })
			return done(v, err)
		}
		if e.Kind == "outside_lms" {
			rows := []*OutsideResult{}
			for i := range d.Outside {
				r := &d.Outside[i]
				if a.Role == "student" && r.Student.AccountID != a.AccountID || p.StudentID > 0 && r.Student.AccountID != p.StudentID || p.Query["grade"] != "" && p.Query["grade"] != strconv.Itoa(r.Grade) || !queryMatches(p, r.Student.FullName, r.Student.Login, r.Student.AccountID) {
					continue
				}
				rows = append(rows, r)
			}
			sort.SliceStable(rows, func(i, j int) bool {
				l, r := rows[i], rows[j]
				switch p.Query["sort"] {
				case "grade_desc":
					return l.Grade > r.Grade
				case "points_desc":
					lv, _ := l.Points.Float64()
					rv, _ := r.Points.Float64()
					return lv > rv
				case "updated_desc":
					return l.Revision > r.Revision
				case "created_desc":
					return l.ID > r.ID
				case "name_asc":
					return strings.ToLower(l.Student.FullName) < strings.ToLower(r.Student.FullName)
				}
				return l.ID < r.ID
			})
			v, err := selectedPage(rows, p, func(r *OutsideResult) any { return outsideDTO(e, d, *r, a) })
			return done(v, err)
		}
		latest := map[int64]*Attempt{}
		first := map[int64]int64{}
		retakes := map[int64]*Assignment{}
		assigned := map[int64]*Assignment{}
		for i := range d.Attempts {
			at := &d.Attempts[i]
			if at.AttemptNo == 1 {
				first[at.Student.AccountID] = at.ID
			}
			if at.Status == "completed" {
				old := latest[at.Student.AccountID]
				if old == nil || at.AttemptNo > old.AttemptNo || at.AttemptNo == old.AttemptNo && at.ID > old.ID {
					latest[at.Student.AccountID] = at
				}
			}
		}
		for i := range d.Assignments {
			as := &d.Assignments[i]
			if as.AttemptNo == 2 {
				retakes[as.Student.AccountID] = as
			}
			if a.Role == "student" && as.Student.AccountID != a.AccountID || !a.Can(false) && a.Role == "examinator" && !examiner(a, as.Members) || p.StudentID > 0 && as.Student.AccountID != p.StudentID {
				continue
			}
			assigned[as.Student.AccountID] = as
		}
		ids := []int64{}
		for id, as := range assigned {
			at := latest[id]
			if at == nil && (p.Query["completed_only"] == "true" || !a.Can(false)) {
				continue
			}
			if at != nil && !canReadAttempt(at, a, a.Can(false)) {
				continue
			}
			if !queryMatches(p, as.Student.FullName, as.Student.Login, id) {
				continue
			}
			if v := p.Query["grade"]; v != "" && (at == nil || v != strconv.Itoa(effectiveGrade(at))) {
				continue
			}
			if v := p.Query["has_appeal"]; v != "" && (at == nil || strconv.FormatBool(len(at.Appeals) > 0) != v) {
				continue
			}
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		v, err := selectedPage(ids, p, func(id int64) any {
			at := latest[id]
			if at == nil {
				return map[string]any{"student": assigned[id].Student, "result": nil, "status": "not_completed", "exam_date": examDateValue(e, d.ID)}
			}
			row := resultDTO(e, d, at, a)
			if a.Can(false) {
				row["first_attempt_id"] = first[id]
				row["retake_assignment_id"] = nil
				if as := retakes[id]; as != nil {
					row["retake_assignment_id"] = as.ID
				}
			}
			return row
		})
		return done(v, err)
	}
	return nil, false, nil
}
