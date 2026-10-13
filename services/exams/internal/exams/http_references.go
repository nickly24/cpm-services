package exams

import "encoding/json"

func needsReferences(op string) bool {
	return oneOf(op, "readiness", "commission-create", "commission-update", "assignment-create", "assignment-update", "ensure", "ready", "start", "retake", "privilege-put", "privilege-delete", "outside-create")
}

// Only validate references involved in the action. Removed historical accounts
// must not block editing the bank, deleting history or recording a live vote.
func operationMemberIDs(e *Exam, op string, p Params, raw json.RawMessage) []int64 {
	ids := requestMemberIDs(raw)
	d := e.Divisions[p.DivisionID]
	if d == nil {
		return ids
	}
	var in struct {
		CommissionID int64 `json:"commission_id"`
	}
	_ = json.Unmarshal(raw, &in)
	if oneOf(op, "assignment-create", "assignment-update", "retake") {
		for _, c := range d.Commissions {
			if c.ID == in.CommissionID {
				for _, m := range c.Members {
					ids = append(ids, m.AccountID)
				}
			}
		}
	}
	if oneOf(op, "readiness", "ensure", "ready", "start") {
		if op == "readiness" {
			ids = append(ids, memberIDs(e, p.DivisionID)...)
		} else {
			var as *Assignment
			if op == "ensure" {
				i := assignmentIndex(d, p.AssignmentID)
				if i >= 0 {
					as = &d.Assignments[i]
				}
			} else {
				i := attemptIndex(d, p.AttemptID)
				if i >= 0 {
					ai := assignmentIndex(d, d.Attempts[i].AssignmentID)
					if ai >= 0 {
						as = &d.Assignments[ai]
					}
				}
			}
			if as != nil {
				ids = append(ids, as.Student.AccountID)
				for _, m := range as.Members {
					ids = append(ids, m.AccountID)
				}
			}
		}
	}
	if op == "privilege-put" || op == "privilege-delete" {
		ids = append(ids, p.StudentID)
	}
	return uniqueIDs(ids)
}
