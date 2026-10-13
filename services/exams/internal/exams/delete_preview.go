package exams

// The deletion command removes the entire shared aggregate, so its preview must
// count every connected division rather than only the currently selected one.
func examDeletionCounts(e *Exam) map[string]int {
	counts := map[string]int{"parts": len(e.Parts), "questions": len(e.Questions), "divisions": len(e.Divisions), "commissions": 0, "assignments": 0, "privileges": 0, "attempts": 0, "presented_questions": 0, "rounds": 0, "votes": 0, "appeals": 0, "outside_results": 0, "classic_results": 0}
	for _, d := range e.Divisions {
		counts["commissions"] += len(d.Commissions)
		counts["assignments"] += len(d.Assignments)
		counts["privileges"] += len(d.Privileges)
		counts["outside_results"] += len(d.Outside)
		counts["attempts"] += len(d.Attempts)
		completed := map[int64]bool{}
		for _, at := range d.Attempts {
			if at.Status == "completed" {
				completed[at.Student.AccountID] = true
			}
			counts["presented_questions"] += len(at.Presented)
			counts["appeals"] += len(at.Appeals)
			for _, q := range at.Presented {
				counts["rounds"] += len(q.Rounds)
				for _, round := range q.Rounds {
					counts["votes"] += len(round.Votes)
				}
			}
		}
		counts["classic_results"] += len(completed)
	}
	return counts
}
