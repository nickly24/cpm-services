package exams

import "testing"

func TestSafeExamWindowVisibleToStudentAndAssignedExaminer(t *testing.T) {
	f := fixtureDomain(t, "round_down")
	for _, actor := range []Actor{f.student, f.x1} {
		detail := queryDomain(t, f, actor, "detail").(map[string]any)
		window, ok := detail["exam_window"].(map[string]any)
		if !ok || window["start_at"] != f.e.Divisions[1].Config.StartAt || window["end_at"] != f.e.Divisions[1].Config.EndAt {
			t.Fatal("safe exam window omitted or changed")
		}
		for _, field := range []string{"config", "parts", "questions", "readiness"} {
			if _, ok := detail[field]; ok {
				t.Fatalf("window projection leaked admin field %s", field)
			}
		}
	}
	if _, err := Query(f.e, f.x3, "detail", f.p); err != Missing {
		t.Fatalf("unassigned examiner can read the exam: %v", err)
	}
}
