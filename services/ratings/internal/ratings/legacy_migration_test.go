package ratings

import (
	"encoding/json"
	"os"
	"testing"
)

func TestRestoredDuplicateExamDetailsRetainDistinctArchiveEntries(t *testing.T) {
	r := detailedReadyResult()
	changeResultComponent(t, &r, "exams", func(c map[string]json.RawMessage) {
		var details []map[string]any
		_ = json.Unmarshal(c["details"], &details)
		first := details[0]
		second := map[string]any{}
		for k, v := range first {
			second[k] = v
		}
		first["legacy_entry_id"] = "legacy:000000000000000000000001:1"
		second["legacy_entry_id"] = "legacy:000000000000000000000001:2"
		second["score"] = 75
		c["details"], _ = json.Marshal([]any{first, second})
	})
	if e := validateResult(r); e != nil {
		t.Fatal("both historical grades must remain readable", e)
	}
	if !historicalExamEntries(r.Exams) {
		t.Fatal("live submission must recognize read-only legacy provenance")
	}
	changeResultDetail(t, &r, "exams", func(d map[string]json.RawMessage) { d["legacy_entry_id"] = json.RawMessage(`"unverified"`) })
	assertAPIError(t, validateResult(r), 400, "incomplete_rating_result")
}

func TestRehearsalPublishedRatingCompatibility(t *testing.T) {
	path := os.Getenv("CPM_RATING_MIGRATION_VALIDATION")
	if path == "" {
		t.Skip("private restored migration outputs not supplied")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal("cannot read private migration fixture")
	}
	var rows []Result
	if json.Unmarshal(raw, &rows) != nil {
		t.Fatal("cannot decode retained outputs")
	}
	for i, r := range rows {
		if e := validateResult(r); e != nil {
			for kind, raw := range map[string]json.RawMessage{"homework": r.Homework, "tests": r.Tests, "exams": r.Exams} {
				var component struct {
					Details []json.RawMessage `json:"details"`
				}
				_ = json.Unmarshal(raw, &component)
				for index, detail := range component.Details {
					if _, ok := validResultDetail(kind, detail); !ok {
						t.Fatalf("restored output index %d: invalid %s detail index %d", i, kind, index)
					}
				}
			}
			t.Fatalf("restored output index %d: %v", i, e)
		}
	}
}
