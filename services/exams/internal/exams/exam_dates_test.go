package exams

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
	"time"
)

func dateTime(t testing.TB, value string) *time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return &parsed
}

func TestExamDateFilterStrictAliases(t *testing.T) {
	cases := []struct {
		name  string
		query map[string]string
		want  ExamDateFilter
		code  string
	}{
		{"empty", nil, ExamDateFilter{}, ""},
		{"leap", map[string]string{"date_from": "2024-02-29", "date_to": "2024-02-29"}, ExamDateFilter{"2024-02-29", "2024-02-29"}, ""},
		{"from only", map[string]string{"date_from": "2026-10-09"}, ExamDateFilter{"2026-10-09", ""}, ""},
		{"to alias", map[string]string{"to_date": "2026-10-09"}, ExamDateFilter{"", "2026-10-09"}, ""},
		{"aliases", map[string]string{"from_date": "2026-10-08", "to_date": "2026-10-09"}, ExamDateFilter{"2026-10-08", "2026-10-09"}, ""},
		{"equal duplicate", map[string]string{"date_from": "2026-10-08", "from_date": "2026-10-08"}, ExamDateFilter{"2026-10-08", ""}, ""},
		{"conflicting from", map[string]string{"date_from": "2026-10-08", "from_date": "2026-10-09"}, ExamDateFilter{}, "conflicting_date_from"},
		{"conflicting to", map[string]string{"date_to": "2026-10-08", "to_date": "2026-10-09"}, ExamDateFilter{}, "conflicting_date_to"},
		{"reversed", map[string]string{"date_from": "2026-10-09", "date_to": "2026-10-08"}, ExamDateFilter{}, "invalid_date_to"},
	}
	for _, value := range []string{"2026-2-08", "2026-02-8", "2026-02-29", "2024-02-30", "2026-13-01", "2026-00-01", "2026-01-00", "0000-01-01", "2026-10-09T00:00:00Z", " 2026-10-09", "2026-10-09 ", "２０２６-１０-０９"} {
		cases = append(cases, struct {
			name  string
			query map[string]string
			want  ExamDateFilter
			code  string
		}{value, map[string]string{"date_from": value}, ExamDateFilter{}, "invalid_date_from"})
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseExamDateFilter(test.query)
			if test.code != "" {
				auxError(t, err, test.code)
			} else if err != nil || got != test.want {
				t.Fatalf("got %+v %v want %+v", got, err, test.want)
			}
		})
	}
}

func TestExamDateMoscowBoundariesAndCatalogueOrder(t *testing.T) {
	filter := ExamDateFilter{"2026-10-09", "2026-10-09"}
	e := &Exam{Kind: "classic", Divisions: map[int64]*Division{1: {ID: 1}}}
	cases := []struct {
		start, date string
		matches     bool
	}{
		{"2026-10-08T20:59:59.999999999Z", "2026-10-08", false},
		{"2026-10-08T21:00:00Z", "2026-10-09", true},
		{"2026-10-09T02:00:00+05:00", "2026-10-09", true},
		{"2026-10-08T14:00:00-07:00", "2026-10-09", true},
		{"2026-10-09T20:59:59.999999999Z", "2026-10-09", true},
		{"2026-10-09T21:00:00Z", "2026-10-10", false},
		// Legacy Moscow is fixed +03, even when historical tzdata differs.
		{"1980-01-01T21:00:00Z", "1980-01-02", false},
	}
	for _, test := range cases {
		e.Divisions[1].Config.StartAt = dateTime(t, test.start)
		date, ok := ExamDate(e, 1)
		if !ok || date != test.date || filter.Match(e, 1) != test.matches {
			t.Fatal(test, date, ok)
		}
	}
	e.Divisions[1].Config.StartAt = nil
	if filter.Match(e, 1) || filter.Match(e, 2) || filter.Match(nil, 1) || !((ExamDateFilter{}).Match(e, 2)) {
		t.Fatal("draft or bank-only date invented")
	}
	e.Kind, e.Divisions[1].Date = "outside_lms", "2026-10-09"
	if !filter.Match(e, 1) {
		t.Fatal("outside inclusive date")
	}
	e.Divisions[1].Date = "0000-01-01"
	if _, ok := ExamDate(e, 1); ok {
		t.Fatal("invalid outside date")
	}
	makeExam := func(id int64, start string) *Exam {
		value := &Exam{ID: id, Kind: "classic", Divisions: map[int64]*Division{1: {ID: 1}}}
		if start != "" {
			value.Divisions[1].Config.StartAt = dateTime(t, start)
		}
		return value
	}
	items := []*Exam{makeExam(2, "2026-10-09T20:00:00Z"), makeExam(1, "2026-10-08T21:00:00Z"), makeExam(4, "2026-10-09T21:00:00Z"), makeExam(3, "")}
	for _, ascending := range []bool{true, false} {
		sort.Slice(items, func(i, j int) bool { return ExamCatalogDateLess(items[i], items[j], 1, ascending) })
		ids := []int64{}
		for _, item := range items {
			ids = append(ids, item.ID)
		}
		want := []int64{3, 1, 2, 4}
		if !ascending {
			want = []int64{4, 2, 1, 3}
		}
		if !reflect.DeepEqual(ids, want) {
			t.Fatal(ascending, ids, want)
		}
	}
}

func TestExamResultsDateAndImmutableStudentWindow(t *testing.T) {
	e, a, p := auxFixture()
	live := dateTime(t, "2026-10-08T21:00:00Z")
	old := dateTime(t, "2026-10-07T21:00:00Z")
	end := dateTime(t, "2026-10-08T10:00:00Z")
	d := e.Divisions[p.DivisionID]
	d.Config.StartAt = live
	student := p.Members[11]
	d.Assignments = []Assignment{{ID: 25, Student: student, AttemptNo: 1}}
	d.Attempts = []Attempt{{ID: 30, Student: student, Status: "completed", AttemptNo: 1, Config: Config{StartAt: old, EndAt: end}, CalculatedGrade: 4, ResultVersion: 1, Members: []Member{p.Members[13]}}}
	studentActor := Actor{AccountID: 11, OrganizationID: e.OrganizationID, Role: "student", DivisionIDs: []int64{2}}
	p.Query = map[string]string{"from_date": "2026-10-09", "to_date": "2026-10-09", "limit": "1", "offset": "0"}
	value, err := Query(e, studentActor, "results", p)
	if err != nil {
		t.Fatal(err)
	}
	page := value.(map[string]any)
	items := page["items"].([]any)
	if page["total"] != 1 || len(items) != 1 {
		t.Fatal(page)
	}
	row := items[0].(map[string]any)
	if row["exam_date"] != "2026-10-09" || row["exam_window_snapshot"].(map[string]any)["start_at"] != old {
		t.Fatal(row)
	}
	for _, key := range []string{"calculated_grade", "resolution_type", "result_version", "appeals", "config", "votes", "questions", "questions_snapshot"} {
		if _, ok := row[key]; ok {
			t.Fatal("student internal field", key)
		}
	}
	p.Query["date_from"], p.Query["date_to"] = "2026-10-08", "2026-10-08"
	delete(p.Query, "from_date")
	delete(p.Query, "to_date")
	value, err = Query(e, a, "results", p)
	if err != nil || value.(map[string]any)["total"] != 0 {
		t.Fatal("filter used historical window", value, err)
	}
	p.Query = map[string]string{"date_from": "2026-10-09", "date_to": "2026-10-09", "offset": "1"}
	value, err = Query(e, a, "results", p)
	if err != nil || value.(map[string]any)["total"] != 1 || len(value.(map[string]any)["items"].([]any)) != 0 {
		t.Fatal("pagination before filter", value, err)
	}
	// The same account has no result in the other attached division.
	e.Divisions[3] = &Division{ID: 3, Config: Config{StartAt: live}}
	p.DivisionID = 3
	value, err = Query(e, studentActor, "results", p)
	if err != nil || value.(map[string]any)["total"] != 0 {
		t.Fatal("result crossed division", value, err)
	}
	p.Query = map[string]string{"date_from": "2026-10-10", "date_to": "2026-10-09"}
	_, err = Query(e, a, "results", p)
	auxError(t, err, "invalid_date_to")
	p.DivisionID = 2
	p.Query = nil
	p.StudentID = 12
	_, err = Query(e, studentActor, "result", p)
	auxError(t, err, "not_found")
	value, err = Query(e, studentActor, "results", p)
	if err != nil || value.(map[string]any)["total"] != 0 {
		t.Fatal("foreign student selected", value, err)
	}
	// Outer authorization is the production entry point for organizations and
	// selected divisions; the domain itself does not replace that transport guard.
	foreign := a
	foreign.OrganizationID++
	auxError(t, scope(e, foreign, p, false, false), "not_found")
	_, err = AuxQuery(e, foreign, "rating-snapshot", p)
	auxError(t, err, "forbidden")
	p.DivisionID = 999
	_, err = Query(e, a, "results", p)
	auxError(t, err, "not_found")
}

func TestExamOutsideResultsDateFilter(t *testing.T) {
	e, a, p := auxFixture()
	e.Kind = "outside_lms"
	d := e.Divisions[p.DivisionID]
	d.Date = "2026-10-09"
	d.Outside = []OutsideResult{{ID: 31, Student: p.Members[11], Points: json.Number("4.5"), Grade: 3}, {ID: 32, Student: p.Members[12], Points: json.Number("0"), Grade: 0}}
	p.Query = map[string]string{"to_date": "2026-10-09", "limit": "1", "offset": "1"}
	value, err := Query(e, a, "results", p)
	if err != nil {
		t.Fatal(err)
	}
	page := value.(map[string]any)
	row := page["items"].([]any)[0].(map[string]any)
	if page["total"] != 2 || row["exam_date"] != d.Date || row["grade"] != 0 {
		t.Fatal(page)
	}
	student := Actor{AccountID: 11, Role: "student"}
	p.Query = nil
	value, err = Query(e, student, "results", p)
	if err != nil || value.(map[string]any)["total"] != 1 {
		t.Fatal(value, err)
	}
	p.Query = map[string]string{"date_from": "2026-10-10"}
	value, err = Query(e, student, "results", p)
	if err != nil || value.(map[string]any)["total"] != 0 {
		t.Fatal(value, err)
	}
}

func TestExamResultRetakeStatusAndPrivilegeRevision(t *testing.T) {
	e, a, p := auxFixture()
	d := e.Divisions[2]
	student := p.Members[11]
	d.Privileges = map[int64]Privilege{11: {Limit: 2, Revision: 7}}
	first := Attempt{ID: 31, Student: student, Status: "completed", AttemptNo: 1}
	d.Attempts = []Attempt{first}
	d.Assignments = []Assignment{{ID: 25, Student: student, AttemptNo: 1}}
	if assignmentDTO(d, &d.Assignments[0])["privilege_revision"] != int64(7) {
		t.Fatal("privilege edit version unavailable")
	}
	row := resultDTO(e, d, &d.Attempts[0], a)
	if row["has_active_retake"] != false || row["retake_status"] != nil {
		t.Fatal("unassigned retake advertised", row)
	}
	d.Assignments = append(d.Assignments, Assignment{ID: 26, Student: student, AttemptNo: 2})
	for _, status := range []string{"not_started", "pending_ready", "in_progress", "completed"} {
		if status != "not_started" {
			d.Attempts = []Attempt{first, {ID: 32, AssignmentID: 26, Student: student, AttemptNo: 2, Status: status}}
		}
		row = resultDTO(e, d, &d.Attempts[0], Actor{AccountID: 11, Role: "student"})
		if row["retake_status"] != status || row["has_active_retake"] != (status != "completed") {
			t.Fatal(status, row)
		}
		if _, ok := row["members"]; ok {
			t.Fatal("retake metadata leaked members")
		}
	}
}
