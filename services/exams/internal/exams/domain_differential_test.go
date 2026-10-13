package exams

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Differential checks load only the original pure modules. The synthetic
// package avoids importing the Flask application and never connects to SQL,
// Mongo, queues, or any production environment.
const domainPythonReference = `
import ast, importlib.util, json, pathlib, sys, types
from decimal import Decimal
root = pathlib.Path(sys.argv[1]).resolve()
package = types.ModuleType("_exam_reference")
package.__path__ = [str(root)]
sys.modules[package.__name__] = package
def load(name):
    spec = importlib.util.spec_from_file_location("_exam_reference."+name, root/(name+".py"))
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module
# Extract the actual shared pure helpers, avoiding the transport module's
# Flask/MySQL imports. No implementation is re-created in the harness.
common = types.ModuleType("_exam_reference.common")
sys.modules[common.__name__] = common
tree = ast.parse((root/"common.py").read_text())
nodes = [node for node in tree.body
         if isinstance(node,(ast.FunctionDef,ast.ClassDef)) and node.name in ("ExamError","fail","integer")
         or isinstance(node,ast.Assign) and any(isinstance(target,ast.Name) and target.id=="MAX_ID" for target in node.targets)]
exec(compile(ast.Module(body=nodes,type_ignores=[]),str(root/"common.py"),"exec"),common.__dict__)
scoring, selection = load("scoring"), load("selection")
data = json.load(sys.stdin)
if data["mode"] == "scoring":
    rows = [[w, v, int(scoring.calculate_question_score(Decimal(v)/2,w)*2)]
            for w in range(1,1001) for v in range(3)]
    rounding = [[n,scoring.round_total(Decimal(n)/2,"up"),scoring.round_total(Decimal(n)/2,"down")]
                for n in list(range(10001))+[5199999,5200000]]
    extras = [[v,m,scoring.resolve_extra_vote(Decimal(v)/2,m)]
              for v in range(3) for m in ("repeat","round_up","round_down")]
    json.dump({"questions":rows,"rounding":rounding,"extras":extras},sys.stdout)
else:
    class Deterministic:
        def randrange(self,n): return 0
        def choice(self,items): return items[0]
    out=[]
    for c in data["cases"]:
        usage={int(k):v for k,v in c["usage"].items()}
        try:
            q,part,cycle=selection.select_question(c["questions"],usage,c["progress"],c["purpose"],c["last"],c["source"],c["source_part"],c["exclude"],Deterministic())
            out.append([q["id"],part,cycle,""])
        except Exception as e:
            if not hasattr(e,"code"): raise
            out.append([0,0,0,e.code])
    json.dump(out,sys.stdout)
`

func referenceDomain(t *testing.T, input any, out any) {
	t.Helper()
	python := filepath.Join("..", "..", "..", "..", "dev", ".venv", "bin", "python")
	if _, err := os.Stat(python); err != nil {
		t.Skip("local Python dev runtime unavailable")
	}
	source := filepath.Join("..", "..", "..", "..", "cpm-back-main", "cpm_back", "services", "exams")
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python, "-c", domainPythonReference, source)
	cmd.Stdin = bytes.NewReader(raw)
	result, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pure Flask reference failed: %v\n%s", err, result)
	}
	if err = json.Unmarshal(result, out); err != nil {
		t.Fatal(err)
	}
}
func TestDomainFlaskScoringDifferential(t *testing.T) {
	var out struct {
		Questions [][]int `json:"questions"`
		Rounding  [][]int `json:"rounding"`
		Extras    [][]any `json:"extras"`
	}
	referenceDomain(t, map[string]any{"mode": "scoring"}, &out)
	if len(out.Questions) != 3000 || len(out.Rounding) != 10003 || len(out.Extras) != 9 {
		t.Fatal("incomplete source vectors")
	}
	for _, row := range out.Questions {
		if row[0]*row[1] != row[2] {
			t.Fatal("score differs", row)
		}
	}
	for _, row := range out.Rounding {
		if roundHalf(row[0], true) != row[1] || roundHalf(row[0], false) != row[2] {
			t.Fatal("rounding differs", row)
		}
	}
	for _, row := range out.Extras {
		got, err := extraDirection(int(row[0].(float64)), row[1].(string))
		if err != nil || got != row[2] {
			t.Fatal("extra result differs", row, got, err)
		}
	}
}
func TestDomainFlaskSelectionDifferential(t *testing.T) {
	cases := []any{}
	expected := [][]any{}
	for p1 := 0; p1 <= 2; p1++ {
		for p2 := 0; p2 <= 1; p2++ {
			for mask := 0; mask < 16; mask++ {
				for _, purpose := range []string{"regular", "tie_breaker"} {
					for _, source := range []string{"any_part", "specific_part"} {
						for _, cycle := range []int{1, 2} {
							at := &Attempt{Parts: []Part{{ID: 1, Quota: 2}, {ID: 2, Quota: 1}}, Questions: []Question{{ID: 11, PartID: 1}, {ID: 12, PartID: 1}, {ID: 21, PartID: 2}, {ID: 22, PartID: 2}}, Config: Config{TieBreakerSourceMode: source, TieBreakerPartID: 2}, Progress: map[int64]int{1: p1, 2: p2}, Cycles: map[int64]int{1: cycle, 2: cycle}, Usage: map[int64]Usage{}, Presented: []Presented{{Question: Question{ID: 21}}}}
							usage := map[int64]any{}
							questions := []any{}
							for i, q := range at.Questions {
								presented := mask&(1<<i) != 0
								excluded := i == 1 && mask&1 != 0
								u := Usage{Presented: presented, Excluded: excluded}
								if presented {
									u.Cycle = 1
								}
								at.Usage[q.ID] = u
								usage[q.ID] = map[string]any{"ever_presented": u.Presented, "permanently_excluded": u.Excluded, "last_cycle_no": u.Cycle}
								questions = append(questions, map[string]any{"id": q.ID, "source_part_id": q.PartID})
							}
							progress := []any{map[string]any{"source_part_id": 1, "required_count": 2, "consensus_count": p1, "cycle_no": cycle}, map[string]any{"source_part_id": 2, "required_count": 1, "consensus_count": p2, "cycle_no": cycle}}
							cases = append(cases, map[string]any{"questions": questions, "usage": usage, "progress": progress, "purpose": purpose, "last": 21, "source": source, "source_part": 2, "exclude": 0})
							choice, err := selectQuestion(at, purpose, 0, 0, func(int) (int, error) { return 0, nil })
							if err != nil {
								expected = append(expected, []any{int64(0), int64(0), 0, err.Error()})
							} else {
								expected = append(expected, []any{choice.question.ID, choice.partID, choice.cycle, ""})
							}
						}
					}
				}
			}
		}
	}
	var out [][]any
	referenceDomain(t, map[string]any{"mode": "selection", "cases": cases}, &out)
	if len(out) != len(expected) {
		t.Fatal("missing selection vectors")
	}
	for i, got := range out {
		want := expected[i]
		if int64(got[0].(float64)) != want[0] || int64(got[1].(float64)) != want[1] || int(got[2].(float64)) != want[2] || got[3] != want[3] {
			t.Fatalf("selection vector %d differs: got %v want %v", i, got, want)
		}
	}
}
