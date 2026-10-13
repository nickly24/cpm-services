package exams

import "testing"

func TestOrganizationAdminCanInspectDisconnectedPrivateContent(t *testing.T) {
	f := fixtureDomain(t, "round_down")
	f.e.OwnerDivisionID = 1
	f.e.InBank = false
	f.p.DivisionID = 99
	for _, op := range []string{"detail", "parts", "questions"} {
		if _, err := Query(f.e, f.admin, op, f.p); err != nil {
			t.Fatalf("organization catalogue content %s: %v", op, err)
		}
	}
	detail := queryDomain(t, f, f.admin, "detail").(map[string]any)
	if detail["connected"] != false {
		t.Fatal("disconnected catalogue detail invented a local connection")
	}
	for _, key := range []string{"config", "readiness", "training_enabled"} {
		if _, ok := detail[key]; ok {
			t.Fatalf("disconnected content contains operational field %s", key)
		}
	}
	for _, op := range []string{"config", "commissions", "assignments", "attempts", "results"} {
		if _, err := Query(f.e, f.admin, op, f.p); err != Missing {
			t.Fatalf("disconnected operational %s must stay unavailable: %v", op, err)
		}
	}
	staff := Actor{AccountID: 32, Role: "staff_admin", Permissions: map[string]Permission{"exams": {View: true, Edit: true}}}
	for _, op := range []string{"detail", "parts", "questions"} {
		if _, err := Query(f.e, staff, op, f.p); err != Missing {
			t.Fatalf("private content leaked to disconnected staff for %s: %v", op, err)
		}
	}
}
