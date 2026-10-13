package exams

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// Query uses role-specific DTOs instead of serializing the persistence model.
// Bank snapshots and other examiners' open votes never leak.
func Query(e *Exam, a Actor, op string, p Params) (any, error) {
	admin := a.Can(false)
	bankOnly := false
	d, err := e.Division(p.DivisionID)
	if err != nil {
		if admin && (e.InBank || a.OrganizationAdmin) && oneOf(op, "detail", "parts", "questions") {
			d = &Division{ID: p.DivisionID}
			bankOnly = true
		} else {
			return nil, err
		}
	}
	if !admin && a.Role != "examinator" && a.Role != "student" {
		return nil, Denied
	}
	if a.Role == "examinator" && !examinerHasExam(d, a) {
		return nil, Missing
	}
	if !admin && oneOf(op, "config", "parts", "questions", "commissions", "privileges", "history-preview") {
		return nil, Denied
	}
	if a.Role == "student" && !oneOf(op, "detail", "results", "result", "protocol", "attempt") {
		return nil, Denied
	}
	switch op {
	case "detail":
		result := map[string]any{"id": e.ID, "name": e.Name, "organization_id": e.OrganizationID, "division_id": d.ID, "direction_id": e.DirectionID, "exam_type": e.Kind, "revision": e.Revision, "content_revision": e.ContentRevision, "date": d.Date}
		result["exam_date"] = examDateValue(e, p.DivisionID)
		if !bankOnly {
			result["exam_window"] = map[string]any{"start_at": d.Config.StartAt, "end_at": d.Config.EndAt}
		}
		if admin {
			ids := []int64{}
			for id := range e.Divisions {
				ids = append(ids, id)
			}
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			result["connected_division_ids"] = ids
			result["owner_division_id"] = e.OwnerDivisionID
			result["in_bank"] = e.InBank
			result["origin_exam_id"] = e.OriginID
			result["division_revision"] = d.Revision
			result["connected"] = !bankOnly
			if bankOnly {
				result["parts"] = e.Parts
				return result, nil
			}
			result["config"] = d.Config
			result["parts"] = e.Parts
			result["training_enabled"] = d.TrainingEnabled
			result["readiness"] = readiness(e, d, nil, p, true)
		}
		return result, nil
	case "config":
		return map[string]any{"config": d.Config, "max_score": maxScore(e.Parts), "local_validation": configurationErrors(e, d, 0)}, nil
	case "parts":
		items := []any{}
		for _, part := range e.Parts {
			size := 0
			for _, q := range e.Questions {
				if q.PartID == part.ID {
					size++
				}
			}
			items = append(items, map[string]any{"id": part.ID, "code": part.Code, "weight": part.Weight, "quota": part.Quota, "revision": part.Revision, "bank_size": size})
		}
		return page(items, p)
	case "questions":
		items := []any{}
		filter := p.PartID
		if p.Query["part_id"] != "" {
			id, err := strconv.ParseInt(p.Query["part_id"], 10, 64)
			if err != nil || id < 1 {
				return nil, Bad("invalid_part_id")
			}
			filter = id
		}
		questions := append([]Question(nil), e.Questions...)
		sort.SliceStable(questions, func(i, j int) bool { return partIndex(e, questions[i].PartID) < partIndex(e, questions[j].PartID) })
		for _, q := range questions {
			if filter == 0 || q.PartID == filter {
				items = append(items, q)
			}
		}
		return page(items, p)
	case "commissions":
		items := []any{}
		for _, c := range d.Commissions {
			items = append(items, c)
		}
		return page(items, p)
	case "assignments":
		items := []any{}
		for _, as := range d.Assignments {
			if !admin && !examiner(a, as.Members) {
				continue
			}
			items = append(items, assignmentDTO(d, &as))
		}
		return page(items, p)
	case "privileges":
		items := []any{}
		ids := []int64{}
		for id := range d.Privileges {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			v := d.Privileges[id]
			items = append(items, map[string]any{"student_account_id": id, "replacement_limit": v.Limit, "revision": v.Revision})
		}
		return page(items, p)
	case "readiness":
		var as *Assignment
		if p.AssignmentID != 0 {
			i := assignmentIndex(d, p.AssignmentID)
			if i < 0 {
				return nil, Missing
			}
			as = &d.Assignments[i]
			if !admin && !examiner(a, as.Members) {
				return nil, Missing
			}
		} else if !admin {
			return nil, Denied
		}
		return readiness(e, d, as, p, true), nil
	case "attempt":
		i := attemptIndex(d, p.AttemptID)
		if i < 0 {
			return nil, Missing
		}
		at := &d.Attempts[i]
		if !canReadAttempt(at, a, admin) {
			return nil, Missing
		}
		if a.Role == "student" {
			return resultDTO(e, d, at, a), nil
		}
		return attemptDTO(e, d, at, a, p), nil
	case "attempts":
		items := []any{}
		for i := range d.Attempts {
			at := &d.Attempts[i]
			if !canReadAttempt(at, a, admin) || p.StudentID > 0 && at.Student.AccountID != p.StudentID || p.Query["status"] != "" && at.Status != p.Query["status"] {
				continue
			}
			row := attemptDTO(e, d, at, a, p)
			delete(row, "current_question")
			items = append(items, row)
		}
		return page(items, p)
	case "results":
		items := []any{}
		dateFilter, err := ParseExamDateFilter(p.Query)
		if err != nil {
			return nil, err
		}
		if !dateFilter.Match(e, p.DivisionID) {
			return page(items, p)
		}
		if e.Kind == "outside_lms" {
			for _, r := range d.Outside {
				if a.Role == "student" && r.Student.AccountID != actorID(a) || p.StudentID > 0 && r.Student.AccountID != p.StudentID {
					continue
				}
				items = append(items, outsideDTO(e, d, r, a))
			}
		} else {
			ids := map[int64]bool{}
			for _, as := range d.Assignments {
				if a.Role == "student" && as.Student.AccountID != actorID(a) || !admin && a.Role == "examinator" && !examiner(a, as.Members) || p.StudentID > 0 && as.Student.AccountID != p.StudentID {
					continue
				}
				ids[as.Student.AccountID] = true
			}
			ordered := []int64{}
			for id := range ids {
				ordered = append(ordered, id)
			}
			sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
			for _, id := range ordered {
				at := effectiveAttempt(d, id)
				if at == nil {
					if admin {
						as := firstAssignment(d, id)
						if as != nil {
							items = append(items, map[string]any{"student": as.Student, "result": nil, "status": "not_completed", "exam_date": examDateValue(e, d.ID)})
						}
					}
					continue
				}
				if !canReadAttempt(at, a, admin) {
					continue
				}
				items = append(items, resultDTO(e, d, at, a))
			}
		}
		return page(items, p)
	case "result":
		student := p.StudentID
		if a.Role == "student" {
			if student != 0 && student != actorID(a) {
				return nil, Missing
			}
			student = actorID(a)
		}
		if student < 1 {
			return nil, Bad("student_account_id_required")
		}
		if e.Kind == "outside_lms" {
			for _, r := range d.Outside {
				if r.Student.AccountID == student {
					result := outsideDTO(e, d, r, a)
					result["current"] = outsideDTO(e, d, r, a)
					result["history"] = []any{}
					return result, nil
				}
			}
			return nil, Missing
		}
		at := effectiveAttempt(d, student)
		if at == nil || !canReadAttempt(at, a, admin) {
			return nil, Missing
		}
		result := resultDTO(e, d, at, a)
		current := resultDTO(e, d, at, a)
		history := []any{}
		for i := range d.Attempts {
			previous := &d.Attempts[i]
			if previous.Student.AccountID == student && previous.Status == "completed" && previous.ID != at.ID && canReadAttempt(previous, a, admin) {
				history = append(history, resultDTO(e, d, previous, a))
			}
		}
		result["current"] = current
		result["history"] = history
		return result, nil
	case "protocol", "rounds":
		i := attemptIndex(d, p.AttemptID)
		if i < 0 {
			return nil, Missing
		}
		at := &d.Attempts[i]
		if !canReadAttempt(at, a, admin) {
			return nil, Missing
		}
		if op == "rounds" && a.Role == "student" {
			return nil, Denied
		}
		items := []any{}
		anchorKey := "through_sequence_no"
		upper := len(at.Presented)
		if op == "rounds" {
			anchorKey = "through_round_no"
			upper = 0
			for _, q := range at.Presented {
				if q.ID == p.PresentedID {
					upper = len(q.Rounds)
				}
			}
			if upper == 0 {
				return nil, Missing
			}
		}
		anchor := upper
		if raw := p.Query[anchorKey]; raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 {
				return nil, Bad("invalid_" + anchorKey)
			}
			if n < anchor {
				anchor = n
			}
		}
		for _, q := range at.Presented {
			if op == "rounds" {
				if q.ID != p.PresentedID {
					continue
				}
				for _, r := range q.Rounds {
					if r.Number > anchor {
						continue
					}
					items = append(items, roundDTO(&r, a, len(at.Members), admin, at.Members))
				}
			} else {
				if q.Sequence > anchor {
					continue
				}
				row := questionDTO(at, &q, a, admin)
				if a.Role == "student" {
					delete(row, "round")
					delete(row, "rounds")
					delete(row, "can_replace")
					delete(row, "can_go_next")
				}
				items = append(items, row)
			}
		}
		result, err := page(items, p)
		if err != nil {
			return nil, err
		}
		result.(map[string]any)[anchorKey] = anchor
		return result, nil
	case "history-preview":
		f := firstAssignment(d, p.StudentID)
		if f == nil {
			return nil, Missing
		}
		assignments := []any{}
		attempts := []any{}
		questions, rounds, votes, appeals := 0, 0, 0, 0
		for _, as := range d.Assignments {
			if as.Student.AccountID == p.StudentID {
				assignments = append(assignments, map[string]any{"id": as.ID, "revision": as.Revision, "attempt_no": as.AttemptNo})
			}
		}
		for _, at := range d.Attempts {
			if at.Student.AccountID != p.StudentID {
				continue
			}
			attempts = append(attempts, map[string]any{"id": at.ID, "state_version": at.StateVersion, "result_version": at.ResultVersion, "status": at.Status})
			questions += len(at.Presented)
			appeals += len(at.Appeals)
			for _, q := range at.Presented {
				rounds += len(q.Rounds)
				for _, r := range q.Rounds {
					votes += len(r.Votes)
				}
			}
		}
		return map[string]any{"exam_id": e.ID, "division_id": d.ID, "student_account_id": p.StudentID, "history_generation": f.HistoryGeneration, "assignments": assignments, "attempts": attempts, "counts": map[string]int{"attempts": len(attempts), "presented_questions": questions, "rounds": rounds, "votes": votes, "appeals": appeals}}, nil
	default:
		return nil, Missing
	}
}
func examinerHasExam(d *Division, a Actor) bool {
	for _, as := range d.Assignments {
		if examiner(a, as.Members) {
			return true
		}
	}
	for _, at := range d.Attempts {
		if examiner(a, at.Members) {
			return true
		}
	}
	return false
}
func canReadAttempt(at *Attempt, a Actor, admin bool) bool {
	if admin {
		return true
	}
	if a.Role == "student" {
		return at.Student.AccountID == actorID(a) && at.Status == "completed"
	}
	return examiner(a, at.Members)
}
func page(items []any, p Params) (any, error) {
	limit := 50
	offset := 0
	var err error
	if p.Query["limit"] != "" {
		limit, err = strconv.Atoi(p.Query["limit"])
		if err != nil || limit < 1 || limit > 200 {
			return nil, Bad("invalid_limit")
		}
	}
	if p.Query["offset"] != "" {
		offset, err = strconv.Atoi(p.Query["offset"])
		if err != nil || offset < 0 {
			return nil, Bad("invalid_offset")
		}
	}
	q := strings.ToLower(strings.TrimSpace(p.Query["q"]))
	filtered := []any{}
	for _, item := range items {
		raw, _ := json.Marshal(item)
		if q == "" || strings.Contains(strings.ToLower(string(raw)), q) {
			filtered = append(filtered, item)
		}
	}
	total := len(filtered)
	start := offset
	if start > total {
		start = total
	}
	end := total
	if limit < total-start {
		end = start + limit
	}
	return map[string]any{"items": filtered[start:end], "total": total, "limit": limit, "offset": offset}, nil
}
func assignmentDTO(d *Division, as *Assignment) map[string]any {
	status := "not_started"
	var id any
	var state any
	ready := false
	if at := assignmentAttempt(d, as.ID); at != nil {
		status = at.Status
		id = at.ID
		state = at.StateVersion
		ready = allReady(at)
	}
	return map[string]any{"id": as.ID, "student": as.Student, "commission_id": as.CommissionID, "commission_name": as.CommissionName, "members": as.Members, "attempt_no": as.AttemptNo, "history_generation": as.HistoryGeneration, "revision": as.Revision, "status": status, "attempt_id": id, "state_version": state, "all_members_ready": ready, "replacement_limit": d.Privileges[as.Student.AccountID].Limit, "privilege_revision": d.Privileges[as.Student.AccountID].Revision}
}
func resultDTO(e *Exam, d *Division, at *Attempt, a Actor) map[string]any {
	current := effectiveAttempt(d, at.Student.AccountID)
	row := map[string]any{"id": at.ID, "exam_id": e.ID, "division_id": d.ID, "student": map[string]any{"account_id": at.Student.AccountID, "full_name": at.Student.FullName}, "attempt_no": at.AttemptNo, "status": at.Status, "raw_total": halfNumber(at.RawHalf), "rounded_total": at.RoundedTotal, "max_score": at.MaxScore, "calculated_grade": at.CalculatedGrade, "effective_grade": effectiveGrade(at), "has_appeal": len(at.Appeals) > 0, "result_version": at.ResultVersion, "resolution_type": at.Resolution, "started_at": at.StartedAt, "completed_at": at.CompletedAt, "published_at": at.CompletedAt, "is_current_attempt": current != nil && current.ID == at.ID}
	row["exam_date"] = examDateValue(e, d.ID)
	row["has_active_retake"] = false
	row["retake_status"] = nil
	for _, assignment := range d.Assignments {
		if assignment.Student.AccountID == at.Student.AccountID && assignment.AttemptNo == 2 {
			status := "not_started"
			if retake := assignmentAttempt(d, assignment.ID); retake != nil {
				status = retake.Status
			}
			row["has_active_retake"] = status != "completed"
			row["retake_status"] = status
			break
		}
	}
	commission := []any{}
	for _, m := range at.Members {
		commission = append(commission, map[string]any{"account_id": m.AccountID, "full_name": m.FullName})
	}
	row["commission"] = commission
	row["grade"] = effectiveGrade(at)
	row["question_count"] = len(at.Presented)
	row["exam_window_snapshot"] = map[string]any{"start_at": at.Config.StartAt, "end_at": at.Config.EndAt}
	if a.Role == "student" {
		delete(row, "calculated_grade")
		delete(row, "resolution_type")
		delete(row, "result_version")
	}
	if a.Can(false) {
		row["history_generation"] = at.HistoryGeneration
		row["appeals"] = at.Appeals
	}
	return row
}
func outsideDTO(e *Exam, d *Division, r OutsideResult, a Actor) map[string]any {
	row := map[string]any{"id": r.ID, "exam_id": e.ID, "division_id": d.ID, "student": map[string]any{"account_id": r.Student.AccountID, "full_name": r.Student.FullName}, "points": r.Points, "grade": r.Grade, "examinator": r.Examinator}
	row["exam_date"] = examDateValue(e, d.ID)
	if a.Can(false) {
		row["revision"] = r.Revision
	}
	return row
}
func roundDTO(r *Round, a Actor, required int, admin bool, ms ...[]Member) map[string]any {
	own, voted := r.Votes[actorID(a)]
	row := map[string]any{"id": r.ID, "round_no": r.Number, "status": r.Status, "votes_count": len(r.Votes), "required_votes": required, "has_voted": voted}
	row["opened_at"] = r.OpenedAt
	row["completed_at"] = r.CompletedAt
	if voted {
		row["own_vote"] = halfNumber(own)
	}
	if r.Consensus != nil {
		row["consensus"] = halfNumber(*r.Consensus)
	}
	if admin || r.Status != "open" {
		votes := []any{}
		ids := []int64{}
		for id := range r.Votes {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			name := ""
			if len(ms) > 0 {
				for _, member := range ms[0] {
					if member.AccountID == id {
						name = member.FullName
					}
				}
			}
			var timestamp any
			if ts, ok := r.VoteTimes[id]; ok {
				timestamp = ts
			}
			votes = append(votes, map[string]any{"examinator_account_id": id, "full_name": name, "voted_at": timestamp, "value": halfNumber(r.Votes[id])})
		}
		row["votes"] = votes
	}
	return row
}
func questionDTO(at *Attempt, q *Presented, a Actor, admin bool) map[string]any {
	code := ""
	weight := 0
	for _, part := range at.Parts {
		if part.ID == q.Question.PartID {
			code = part.Code
			weight = part.Weight
		}
	}
	row := map[string]any{"id": q.ID, "question_id": q.Question.ID, "part_id": q.Question.PartID, "part_code": code, "question_weight": weight, "question_text": q.Question.Text, "answer_text": q.Question.Answer, "sequence_no": q.Sequence, "purpose": q.Purpose, "status": q.Status, "cycle_no": q.Cycle, "replaces_id": q.ReplacesID, "weighted_score": halfNumber(q.AwardedHalf)}
	row["presented_at"] = q.PresentedAt
	row["consensus_at"] = q.ConsensusAt
	row["replaced_at"] = q.ReplacedAt
	if q.Consensus != nil {
		row["consensus"] = halfNumber(*q.Consensus)
	}
	if a.Role != "student" {
		row["replaced_by_account_id"] = q.ReplacedBy
		rounds := []any{}
		votes := 0
		for _, r := range q.Rounds {
			rounds = append(rounds, roundDTO(&r, a, len(at.Members), admin, at.Members))
			votes += len(r.Votes)
		}
		row["rounds"] = rounds
		if len(q.Rounds) > 0 {
			row["round"] = roundDTO(&q.Rounds[len(q.Rounds)-1], a, len(at.Members), admin, at.Members)
		}
		current := currentQuestion(at)
		own := examiner(a, at.Members)
		row["can_replace"] = own && current != nil && current.ID == q.ID && q.Status == "open" && votes == 0 && at.ReplacementUsed < at.ReplacementLimit
		remaining := 0
		for _, part := range at.Parts {
			remaining += part.Quota - at.Progress[part.ID]
		}
		row["can_go_next"] = own && current != nil && current.ID == q.ID && q.Status == "consensus" && q.Purpose == "regular" && remaining > 0
	}
	return row
}
func attemptDTO(e *Exam, d *Division, at *Attempt, a Actor, p Params) map[string]any {
	members := []any{}
	for _, m := range at.Members {
		var readyAt any
		if timestamp, ok := at.ReadyAt[m.AccountID]; ok {
			readyAt = timestamp
		}
		members = append(members, map[string]any{"account_id": m.AccountID, "full_name": m.FullName, "ready": at.Ready[m.AccountID], "ready_at": readyAt})
	}
	progress := []any{}
	consensus, required := 0, 0
	for _, part := range at.Parts {
		consensus += at.Progress[part.ID]
		required += part.Quota
		progress = append(progress, map[string]any{"part_id": part.ID, "code": part.Code, "consensus": at.Progress[part.ID], "required": part.Quota})
	}
	limit := at.ReplacementLimit
	if at.Status == "pending_ready" {
		limit = d.Privileges[at.Student.AccountID].Limit
	}
	var current any
	canVote, canReplace, canNext := false, false, false
	if q := currentQuestion(at); q != nil {
		row := questionDTO(at, q, a, a.Can(false))
		current = row
		canReplace, _ = row["can_replace"].(bool)
		canNext, _ = row["can_go_next"].(bool)
		if len(q.Rounds) > 0 {
			_, voted := q.Rounds[len(q.Rounds)-1].Votes[actorID(a)]
			canVote = examiner(a, at.Members) && q.Status == "open" && !voted
		}
	}
	var result any
	if at.Status == "completed" {
		result = resultDTO(e, d, at, a)
	}
	canReady, canStart := false, false
	if at.Status == "pending_ready" {
		i := assignmentIndex(d, at.AssignmentID)
		if i >= 0 {
			report := readiness(e, d, &d.Assignments[i], p, true)
			canReady = examiner(a, at.Members) && !at.Ready[actorID(a)] && report["can_prepare"] == true
			canStart = examiner(a, at.Members) && report["can_start_now"] == true
		}
	}
	return map[string]any{"id": at.ID, "exam_id": e.ID, "exam_type": e.Kind, "division_id": d.ID, "direction_id": e.DirectionID, "assignment_id": at.AssignmentID, "student": map[string]any{"account_id": at.Student.AccountID, "full_name": at.Student.FullName}, "attempt_no": at.AttemptNo, "history_generation": at.HistoryGeneration, "status": at.Status, "phase": at.Phase, "state_version": at.StateVersion, "result_version": at.ResultVersion, "members": members, "all_members_ready": allReady(at), "started_at": at.StartedAt, "completed_at": at.CompletedAt, "server_now": p.Now, "current_question": current, "progress": map[string]any{"regular_consensus": consensus, "regular_required": required, "replacement_used": at.ReplacementUsed, "replacement_limit": limit, "parts": progress}, "result": result, "permissions": map[string]bool{"can_ready": canReady, "can_start": canStart, "can_vote": canVote, "can_replace": canReplace, "can_go_next": canNext}}
}
