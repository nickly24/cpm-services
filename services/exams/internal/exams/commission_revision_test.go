package exams

import (
	"fmt"
	"reflect"
	"testing"
)

func TestDeletedCommissionInvalidatesAssignmentRevision(t *testing.T) {
	f := fixtureDomain(t, "round_down")
	before := f.e.Divisions[f.p.DivisionID].Assignments[0]
	replacement := f.cmd(t, f.admin, "commission-create", map[string]any{"name": "Replacement", "examinator_ids": []int64{21, 23}})["commission"].(Commission)
	f.p.CommissionID = f.commission
	f.cmd(t, f.admin, "commission-delete", map[string]any{"expected_revision": f.e.Divisions[f.p.DivisionID].Commissions[0].Revision})
	after := f.e.Divisions[f.p.DivisionID].Assignments[0]
	if after.CommissionID != 0 || after.Revision != before.Revision+1 {
		t.Fatalf("removed reference must invalidate assignment CAS: before=%+v after=%+v", before, after)
	}
	if after.CommissionName != before.CommissionName || !reflect.DeepEqual(after.Members, before.Members) || after.HistoryGeneration != before.HistoryGeneration {
		t.Fatal("commission deletion changed the preserved assignment snapshot")
	}
	f.fail(t, f.admin, "assignment-update", fmt.Sprintf(`{"expected_revision":%d,"commission_id":%d}`, before.Revision, replacement.ID), "assignment_modified")
	f.cmd(t, f.admin, "assignment-update", map[string]any{"expected_revision": after.Revision, "commission_id": replacement.ID})
}
