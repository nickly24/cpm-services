package exams

import (
	"encoding/json"
	"net/http"
)

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
		if op == "readiness" && p.AssignmentID == 0 {
			// Exam configuration readiness does not depend on every historical participant.
		} else {
			var as *Assignment
			if op == "ensure" || op == "readiness" {
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

// Batch visible references; a missing historical account is a row readiness
// error, while unavailable Core/invalid scope still fails closed. Bisect failed
// batches instead of turning one removed account into 500 sequential requests.
func (s *Server) readMembers(r *http.Request, a Actor, p Params, direction int64, ids []int64) (map[int64]Member, error) {
	out := map[int64]Member{}
	var fetch func([]int64) error
	fetch = func(ids []int64) error {
		members, err := s.Deps.References(r, a.OrganizationID, []int64{p.DivisionID}, direction, ids)
		if err == nil {
			for id, m := range scopeMembers(members, p.DivisionID) {
				out[id] = m
			}
			return nil
		}
		api, ok := err.(*APIError)
		if !ok || api.Status != 404 {
			return err
		}
		if len(ids) <= 1 {
			return nil
		}
		mid := len(ids) / 2
		if err = fetch(ids[:mid]); err != nil {
			return err
		}
		return fetch(ids[mid:])
	}
	// Validate the division/direction independently so a scope 404 is not hidden.
	if _, err := s.Deps.References(r, a.OrganizationID, []int64{p.DivisionID}, direction, nil); err != nil {
		return nil, err
	}
	ids = uniqueIDs(ids)
	for i := 0; i < len(ids); i += 500 {
		if err := fetch(ids[i:min(i+500, len(ids))]); err != nil {
			return nil, err
		}
	}
	return out, nil
}
