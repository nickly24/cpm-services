package exams

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRatingSnapshotKeepsSharedExamNameAndRenameRevision(t *testing.T) {
	e, _, p := auxFixture()
	e.RatingRevision = examRatingRevision(e)
	e.Name = "Устный экзамен · анализ"
	start := p.Now.AddDate(0, 0, -1)
	e.Divisions[2].Config.StartAt = &start
	e.Divisions[3] = &Division{ID: 3, Revision: 1, Config: Config{StartAt: &start}}
	capture := func() RatingSnapshotDTO {
		t.Helper()
		value, err := RatingSnapshot([]*Exam{e}, e.OrganizationID, []int64{2, 3}, "2026-10-01", "2026-10-31", p.Now)
		if err != nil {
			t.Fatal(err)
		}
		return value.(RatingSnapshotDTO)
	}
	first := capture()
	if len(first.Exams) != 2 || first.Exams[0].Name != e.Name || first.Exams[1].Name != e.Name {
		t.Fatal("shared catalogue lost the owner exam name", first.Exams)
	}
	raw, _ := json.Marshal(first.Exams)
	if !strings.Contains(string(raw), `"exam_name":"`+e.Name+`"`) {
		t.Fatal("wire catalogue omitted the optional exam label")
	}
	e.Name = "Переименованный экзамен"
	e.Revision++
	e.ContentRevision++
	e.RatingRevision++ // Public PUT metadata advances the shared rating input.
	next := capture()
	if next.Exams[0].Name == first.Exams[0].Name || next.SourceRevision == first.SourceRevision {
		t.Fatal("rename did not fence the old source snapshot")
	}
	if first.Exams[0].Name != "Устный экзамен · анализ" {
		t.Fatal("rename mutated a captured catalogue")
	}
}
