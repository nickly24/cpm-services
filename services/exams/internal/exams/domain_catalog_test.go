package exams

import (
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestCatalogDirectionSortUsesScopedNamesAndExamID(t *testing.T) {
	// IDs intentionally disagree with alphabetical order. Equal direction
	// names, including an exam-name fallback, must use exam ID rather than date.
	names := map[int64]string{900: "Алгебра", 2: "Физика", 1: "Геометрия", 901: "аЛгЕбРа", 902: ""}
	items := []*Exam{
		{ID: 7, DirectionID: 2, Name: "А должно быть проигнорировано"},
		{ID: 60, DirectionID: 900},
		{ID: 3, DirectionID: 901},
		{ID: 5, DirectionID: 999, Name: "алгебра"},
		{ID: 4, DirectionID: 1},
		{ID: 8, DirectionID: 998, Name: "Химия"},
		{ID: 9, DirectionID: 902, Name: "З не используется: имя направления существует"},
		{ID: 2, DirectionID: 900},
	}
	for i, item := range items {
		start := time.Date(2026, 10, 1+i, 0, 0, 0, 0, time.UTC)
		item.Divisions = map[int64]*Division{1: {ID: 1, Config: Config{StartAt: &start}}}
	}
	sort.Slice(items, func(i, j int) bool { return catalogDirectionLess(items[i], items[j], names) })
	ids := []int64{}
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	if want := []int64{9, 2, 3, 5, 60, 4, 7, 8}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("direction-name ordering got %v want %v", ids, want)
	}
	for _, item := range items {
		if catalogDirectionLess(item, item, names) {
			t.Fatal("sort comparator is not strict")
		}
	}
	// Core-scoped omission falls back to this exam's label, never another
	// organization's label cached under the same direction ID.
	left, right := &Exam{ID: 1, DirectionID: 42, Name: "Биология"}, &Exam{ID: 2, DirectionID: 43, Name: "Алгебра"}
	if catalogDirectionLess(left, right, nil) || !catalogDirectionLess(right, left, nil) {
		t.Fatal("missing scoped names did not use exam labels")
	}
}
