package exams

import "testing"

func TestExamDeletionCountsIncludeAllConnectedDivisions(t *testing.T) {
	e := &Exam{Parts: make([]Part, 2), Questions: make([]Question, 7), Divisions: map[int64]*Division{
		1: {Commissions: make([]Commission, 1), Assignments: make([]Assignment, 2), Privileges: map[int64]Privilege{11: {}}, Attempts: []Attempt{{Student: Member{AccountID: 11}, Status: "completed", Presented: []Presented{{Rounds: []Round{{Votes: map[int64]int{21: 2, 22: 2}}}}}}, {Student: Member{AccountID: 11}, Status: "completed", Appeals: make([]Appeal, 1)}}},
		2: {Commissions: make([]Commission, 3), Assignments: make([]Assignment, 4), Outside: make([]OutsideResult, 1), Attempts: []Attempt{{Student: Member{AccountID: 11}, Status: "completed"}, {Student: Member{AccountID: 12}, Status: "in_progress"}}},
	}}
	counts := examDeletionCounts(e)
	want := map[string]int{"parts": 2, "questions": 7, "divisions": 2, "commissions": 4, "assignments": 6, "privileges": 1, "attempts": 4, "presented_questions": 1, "rounds": 1, "votes": 2, "appeals": 1, "outside_results": 1, "classic_results": 2}
	for key, value := range want {
		if counts[key] != value {
			t.Fatalf("%s got %d want %d", key, counts[key], value)
		}
	}
}
