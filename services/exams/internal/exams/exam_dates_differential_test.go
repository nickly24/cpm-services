package exams

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Execute the original date helpers and capture SQL in an isolated in-memory
// SQLite database. Only AST-selected functions/constants are loaded, so this
// neither imports Flask nor reads environment/credentials or opens a network.
const examDatePythonReference = `
import ast, datetime as dt, json, pathlib, re, sqlite3, sys, types
sqlite3.register_adapter(dt.datetime, lambda value: value.isoformat(" "))
root = pathlib.Path(sys.argv[1]).resolve()
package = types.ModuleType("_exam_date_reference")
package.__path__ = [str(root)]
sys.modules[package.__name__] = package
def extract(file, names, namespace):
    tree = ast.parse((root/file).read_text())
    nodes = [n for n in tree.body if
        isinstance(n,(ast.FunctionDef,ast.ClassDef)) and n.name in names or
        isinstance(n,ast.Assign) and any(isinstance(t,ast.Name) and t.id in names for t in n.targets)]
    exec(compile(ast.Module(body=nodes,type_ignores=[]),str(root/file),"exec"),namespace)
namespace = dict(re=re,date=dt.date,datetime=dt.datetime,time=dt.time,
                 timezone=dt.timezone,timedelta=dt.timedelta,
                 __name__="_exam_date_reference.rating",__package__="_exam_date_reference")
extract("common.py",{"ExamError","fail","parse_date","UTC","MOSCOW"},namespace)
results = types.ModuleType("_exam_date_reference.results")
sys.modules[results.__name__] = results
extract("results.py",{"RESULT_SELECT","CURRENT_CLAUSE"},results.__dict__)
extract("rating.py",{"period_bounds","capture_exam_input","calculate_from_snapshot"},namespace)
def timestamp(value):
    if value is None: return None
    return dt.datetime.fromisoformat(value.replace("Z","+00:00")).astimezone(dt.timezone.utc).replace(tzinfo=None)
class MemoryDB:
    def __init__(self, exams):
        self.db = sqlite3.connect(":memory:")
        self.db.row_factory = sqlite3.Row
        self.db.executescript('''
          CREATE TABLE rating_source_state(id INTEGER,source_revision INTEGER);
          INSERT INTO rating_source_state VALUES(1,1);
          CREATE TABLE students(id INTEGER);
          INSERT INTO students VALUES(11),(12);
          CREATE TABLE directions(id INTEGER,name TEXT);
          CREATE TABLE exams(id INTEGER,exam_type TEXT,direction_id INTEGER,name TEXT,date TEXT);
          CREATE TABLE classic_exam_settings(exam_id INTEGER,start_at TEXT);
          CREATE TABLE exam_sessions(exam_id INTEGER,student_id INTEGER,points REAL);
          CREATE TABLE classic_exam_attempts(id INTEGER,exam_id INTEGER,student_id INTEGER,status TEXT,attempt_no INTEGER,calculated_grade INTEGER,completed_at TEXT);
          CREATE TABLE classic_exam_appeals(id INTEGER,attempt_id INTEGER,new_grade INTEGER,created_at TEXT);
        ''')
        for exam in exams:
            self.db.execute("INSERT INTO exams VALUES(?,?,?,?,?)",(exam["id"],exam["exam_type"],0,"exam",exam["date"]))
            if exam["start_at"] is not None:
                self.db.execute("INSERT INTO classic_exam_settings VALUES(?,?)",(exam["id"],timestamp(exam["start_at"])))
            for row in exam["outside"]:
                self.db.execute("INSERT INTO exam_sessions VALUES(?,?,?)",(exam["id"],row["account_id"],row["grade"]))
            for row in exam["attempts"]:
                self.db.execute("INSERT INTO classic_exam_attempts VALUES(?,?,?,?,?,?,?)",(row["id"],exam["id"],row["student"]["account_id"],row["status"],row["attempt_no"],row["calculated_grade"],timestamp(row["completed_at"])))
                for appeal in row["appeals"] or []:
                    self.db.execute("INSERT INTO classic_exam_appeals VALUES(?,?,?,?)",(appeal["id"],row["id"],appeal["new_grade"],timestamp(appeal["created_at"])))
    def all(self,sql,args=()):
        rows=[dict(row) for row in self.db.execute(sql.replace("%s","?"),args)]
        for row in rows:
            if row.get("start_at") is not None: row["start_at"]=dt.datetime.fromisoformat(row["start_at"])
            if row.get("date"): row["date"]=dt.date.fromisoformat(row["date"])
        return rows
    def scalar(self,sql,args=(),default=None):
        value=self.db.execute(sql.replace("%s","?"),args).fetchone()[0]
        if value is None: return default
        if "MIN(s.start_at)" in sql: return dt.datetime.fromisoformat(value)
        return value
data=json.load(sys.stdin)
valid=[]
for value in data["dates"]:
    try: namespace["parse_date"](value);valid.append(True)
    except namespace["ExamError"]:valid.append(False)
out=[]
for case in data["cases"]:
    db=MemoryDB(case["exams"])
    snapshot=namespace["capture_exam_input"](db,case["date_from"],case["date_to"],timestamp(case["as_of"]))
    details=namespace["calculate_from_snapshot"](snapshot,11)["details"]
    exams=[dict(exam_id=row["exam_id"],exam_date=row["exam_date"]) for row in details]
    values=[dict(exam_id=key[0],account_id=key[1],grade=value["grade"],attempt_no=value["attempt_no"] or 0,has_appeal=value["has_appeal"])
            for key,value in sorted(snapshot["values"].items())]
    future=snapshot["nextExamStartAt"]
    out.append(dict(exams=exams,results=values,next_exam_start_at=future.replace(tzinfo=dt.timezone.utc).isoformat() if future else None))
    db.db.close()
json.dump(dict(valid=valid,cases=out),sys.stdout)
`

func TestExamDateFlaskParseAndSnapshotDifferential(t *testing.T) {
	python := filepath.Join("..", "..", "..", "..", "dev", ".venv", "bin", "python")
	if _, err := os.Stat(python); err != nil {
		t.Skip("local Python dev runtime unavailable")
	}
	inputs := []any{}
	expected := []RatingSnapshotDTO{}
	for _, day := range []string{"1980-01-01", "2024-02-29", "2026-10-08", "2026-12-31"} {
		base, _ := time.Parse("2006-01-02", day)
		base = base.Add(-3 * time.Hour)
		for _, endpoints := range [][2]int{{0, 0}, {0, 1}, {1, 1}} {
			for _, cutoff := range []time.Duration{-time.Hour, 0, 12 * time.Hour, 24 * time.Hour, 48 * time.Hour} {
				asOf := base.Add(cutoff)
				from, to := base.AddDate(0, 0, endpoints[0]).In(examMoscow).Format("2006-01-02"), base.AddDate(0, 0, endpoints[1]).In(examMoscow).Format("2006-01-02")
				exams := []*Exam{}
				flat := []any{}
				for i, offset := range []time.Duration{-time.Microsecond, 0, time.Microsecond, 12 * time.Hour, 24*time.Hour - time.Microsecond, 24 * time.Hour, 48 * time.Hour} {
					start := base.Add(offset)
					completed := asOf.Add(7 * 24 * time.Hour)
					e := &Exam{ID: int64(i + 1), OrganizationID: 1, Kind: "classic", ContentRevision: 1, Divisions: map[int64]*Division{1: {ID: 1, Revision: 1, Config: Config{StartAt: &start}}}}
					idBase := e.ID * 100
					status := "in_progress"
					if i%2 == 0 {
						status = "completed"
					}
					e.Divisions[1].Attempts = []Attempt{
						{ID: idBase + 1, Student: Member{AccountID: 11}, AttemptNo: 1, Status: "completed", CalculatedGrade: 5, CompletedAt: &completed},
						{ID: idBase + 2, Student: Member{AccountID: 11}, AttemptNo: 2, Status: status, CalculatedGrade: 1, CompletedAt: &completed, Appeals: []Appeal{{ID: idBase + 11, Grade: 4, At: completed}, {ID: idBase + 12, Grade: 2, At: completed.Add(time.Hour)}}},
						{ID: idBase + 3, Student: Member{AccountID: 12}, AttemptNo: 1, Status: "completed", CalculatedGrade: 0, CompletedAt: &completed},
					}
					exams = append(exams, e)
					flat = append(flat, map[string]any{"id": e.ID, "exam_type": e.Kind, "start_at": &start, "date": "", "attempts": e.Divisions[1].Attempts, "outside": []any{}})
				}
				for i, offset := range []int{-1, 0, 1} {
					date := base.AddDate(0, 0, offset).In(examMoscow).Format("2006-01-02")
					e := &Exam{ID: int64(i + 8), OrganizationID: 1, Kind: "outside_lms", Divisions: map[int64]*Division{1: {ID: 1, Revision: 1, Date: date, Outside: []OutsideResult{{ID: 2000 + int64(i), Student: Member{AccountID: 11}, Grade: 2, Revision: 1}}}}}
					exams = append(exams, e)
					flat = append(flat, map[string]any{"id": e.ID, "exam_type": e.Kind, "start_at": nil, "date": date, "attempts": []any{}, "outside": []any{map[string]any{"account_id": 11, "grade": 2}}})
				}
				draft := &Exam{ID: 11, OrganizationID: 1, Kind: "classic", Divisions: map[int64]*Division{1: {ID: 1}}}
				exams = append(exams, draft)
				flat = append(flat, map[string]any{"id": draft.ID, "exam_type": draft.Kind, "start_at": nil, "date": "", "attempts": []any{}, "outside": []any{}})
				inputs = append(inputs, map[string]any{"date_from": from, "date_to": to, "as_of": asOf, "exams": flat})
				expected = append(expected, snapshotDates(t, exams, 1, []int64{1}, from, to, asOf))
			}
		}
	}
	dates := []string{"0000-01-01", "0001-01-01", "9999-12-31", "2024-02-29", "2026-02-29", "2026-1-09", "2026-10-9", "2026-10-09", "2026-10-09 ", "2026-10-09T00:00:00Z", "٢٠٢٦-١٠-٠٩"}
	raw, err := json.Marshal(map[string]any{"dates": dates, "cases": inputs})
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join("..", "..", "..", "..", "cpm-back-main", "cpm_back", "services", "exams")
	cmd := exec.Command(python, "-c", examDatePythonReference, source)
	cmd.Stdin = bytes.NewReader(raw)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	result, err := cmd.Output()
	if err != nil {
		t.Fatalf("date/SQL reference failed: %v\n%s", err, stderr.Bytes())
	}
	type dateRow struct {
		ExamID int64  `json:"exam_id"`
		Date   string `json:"exam_date"`
	}
	type resultRow struct {
		ExamID    int64   `json:"exam_id"`
		AccountID int64   `json:"account_id"`
		Grade     float64 `json:"grade"`
		AttemptNo int     `json:"attempt_no"`
		HasAppeal bool    `json:"has_appeal"`
	}
	var reference struct {
		Valid []bool `json:"valid"`
		Cases []struct {
			Exams   []dateRow   `json:"exams"`
			Results []resultRow `json:"results"`
			Next    *time.Time  `json:"next_exam_start_at"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(result, &reference); err != nil {
		t.Fatal(err)
	}
	if len(reference.Valid) != len(dates) || len(reference.Cases) != len(expected) || len(expected) != 60 {
		t.Fatal("incomplete date reference vectors")
	}
	for i, valid := range reference.Valid {
		if validExamDate(dates[i]) != valid {
			t.Fatal("strict date differs", dates[i], valid)
		}
	}
	for i, row := range reference.Cases {
		wantExams, wantResults := []dateRow{}, []resultRow{}
		for _, exam := range expected[i].Exams {
			wantExams = append(wantExams, dateRow{exam.ExamID, exam.Date})
		}
		for _, result := range expected[i].Results {
			wantResults = append(wantResults, resultRow{result.ExamID, result.AccountID, float64(result.Grade), result.AttemptNo, result.HasAppeal})
		}
		if !reflect.DeepEqual(row.Exams, wantExams) || !reflect.DeepEqual(row.Results, wantResults) || row.Next == nil && expected[i].NextExamStartAt != nil || row.Next != nil && (expected[i].NextExamStartAt == nil || !row.Next.Equal(*expected[i].NextExamStartAt)) {
			t.Fatalf("snapshot vector %d differs: reference %+v owner %+v", i, row, expected[i])
		}
	}
}
