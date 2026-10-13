package exams

import "testing"

func TestPrivilegeDeleteValidatesThePathStudentReference(t *testing.T) {
	f := fixtureDomain(t, "round_down")
	f.p.StudentID = 11
	if !needsReferences("privilege-delete") {
		t.Fatal("deleting an active student's privilege must load the student reference")
	}
	ids := operationMemberIDs(f.e, "privilege-delete", f.p, nil)
	if len(ids) != 1 || ids[0] != 11 {
		t.Fatalf("privilege delete references must contain the path student, got %v", ids)
	}
	if needsReferences("history-delete") {
		t.Fatal("historical cleanup must remain independent of active membership")
	}
}
