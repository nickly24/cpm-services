package uploads

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func stagedTestSession(kind string) Session {
	s := Session{ID: strings.Repeat("a", 32), Scope: Scope{OrganizationID: 1001, DivisionID: 1002, ActorID: 37}, Type: kind, Owner: "tests", Revision: 3, OwnerRevision: 3, SourceHash: strings.Repeat("b", 64)}
	if kind == "tests.external-results" {
		s.TargetID = "external_0123456789abcdef01234567"
	}
	return s
}

func stagedTestPreview(ss Session, valid bool) json.RawMessage {
	v := map[string]any{"organization_id": ss.OrganizationID, "division_id": ss.DivisionID, "preview_hash": strings.Repeat("f", 64)}
	if ss.Type == "tests.json" {
		v["valid"], v["direction_id"] = valid, 7
	} else {
		v["can_commit"], v["revision"], v["test_id"] = valid, 9, "0123456789abcdef01234567"
	}
	raw, _ := json.Marshal(v)
	return raw
}

func TestStagedInputsPinScopeTargetAndPreserveSourceNumbers(t *testing.T) {
	ss := stagedTestSession("tests.json")
	raw, e := stagedEnvelope(ss, json.RawMessage(`{"direction_id":7,"payload":{"counter":9007199254740993,"questions":[]}}`), 0)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(raw), `"organization_id":1001`) || !strings.Contains(string(raw), `"division_id":1002`) || !strings.Contains(string(raw), `9007199254740993`) {
		t.Fatalf("server scope or source number changed: %s", raw)
	}
	for _, bad := range []string{
		`{"organization_id":2001,"direction_id":7,"payload":{}}`,
		`{"division_id":2002,"direction_id":7,"payload":{}}`,
		`{"actor_account_id":38,"direction_id":7,"payload":{}}`,
		`{"direction_id":7,"payload":null}`,
		`{"direction_id":7,"payload":{}} {}`,
	} {
		if _, e = stagedEnvelope(ss, json.RawMessage(bad), 0); e == nil {
			t.Fatalf("accepted untrusted envelope: %s", bad)
		}
	}
	external := stagedTestSession("tests.external-results")
	raw, e = stagedEnvelope(external, json.RawMessage(`{"rows":[{"row_id":"sheet-1-row-2","row":2,"percent":0}]} `), 9)
	if e != nil || !strings.Contains(string(raw), `"expected_test_revision":9`) || !strings.Contains(string(raw), `"test_id":"0123456789abcdef01234567"`) || !strings.Contains(string(raw), `"percent":0`) {
		t.Fatalf("external scope/revision/zero corrupted: %s %v", raw, e)
	}
	for _, bad := range []string{
		`{"rows":[]}`,
		`{"rows":[{}],"test_id":"0123456789abcdef01234568"}`,
		`{"rows":[{}],"expected_test_revision":8}`,
		`{"rows":[{}],"organization_id":2001}`,
		`{"rows":[{}],"authorization":"Bearer unsupported"}`,
	} {
		if _, e = stagedEnvelope(external, json.RawMessage(bad), 9); e == nil {
			t.Fatalf("accepted external target/scope/revision spoof: %s", bad)
		}
	}
}

func TestStagedTransportBoundsAndEditPins(t *testing.T) {
	ss := stagedTestSession("tests.external-results")
	rows := make([]json.RawMessage, 5001)
	for i := range rows {
		rows[i] = json.RawMessage(`{}`)
	}
	raw, _ := json.Marshal(stagedExternalInput{Rows: rows})
	if _, e := stagedEnvelope(ss, raw, 9); e == nil {
		t.Fatal("accepted more than 5000 raw external rows")
	}
	ss = stagedTestSession("tests.json")
	raw, _ = json.Marshal(stagedJSONInput{DirectionID: 7, Payload: json.RawMessage(`"` + strings.Repeat("x", stagedInputLimit) + `"`)})
	if _, e := stagedEnvelope(ss, raw, 0); e == nil {
		t.Fatal("accepted more than 1 MiB owner input")
	}
	stored := stagedPreview{Input: json.RawMessage(`{"organization_id":1001,"division_id":1002,"direction_id":7,"payload":{"old":true}}`)}
	updated, e := stagedEditInput(ss, stored, json.RawMessage(`{"payload":{"new":true},"expected_revision":3}`))
	if e != nil || !strings.Contains(string(updated), `"direction_id":7`) || !strings.Contains(string(updated), `"new":true`) {
		t.Fatalf("valid source edit not forwarded: %s %v", updated, e)
	}
	for _, payload := range []string{`{"payload":{},"direction_id":8}`, `{"payload":{},"expected_revision":2}`, `{"payload":{},"rows":[{}]}`, `{"payload":{},"division_id":2002}`} {
		if _, e := stagedEditInput(ss, stored, json.RawMessage(payload)); e == nil {
			t.Fatalf("edit changed pinned scope/direction/revision: %s", payload)
		}
	}
	ss = stagedTestSession("tests.external-results")
	stored.TargetRevision = 9
	if _, e := stagedEditInput(ss, stored, json.RawMessage(`{"rows":[{"row_id":"r2","row":2,"account_id":9007199254740993,"percent":87.5}]}`)); e != nil {
		t.Fatal(e)
	}
	if _, e := stagedEditInput(ss, stored, json.RawMessage(`{"rows":[{}],"test_id":"0123456789abcdef01234568"}`)); e == nil {
		t.Fatal("external edit accepted changed target")
	}
}

// These are adapter unit tests. Their local HTTP server is deliberately not
// counted as real owner acceptance by scripts/http_test.py.
func TestStagedOwnerValidationRequiresLiveAuthorizationAndImmutablePreview(t *testing.T) {
	ss := stagedTestSession("tests.external-results")
	ownerPreview := stagedTestPreview(ss, false)
	var requests int
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != "POST" || r.URL.Path != "/internal/tests/external-results/validate" || r.Header.Get("Authorization") != "Bearer ephemeral-user" || r.Header.Get("X-Service-Key") != "transport-key" {
			t.Errorf("wrong validation transport: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(403)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "ephemeral-user") || strings.Contains(string(raw), "transport-key") {
			t.Error("transport credentials inserted into owner input")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(ownerPreview)
	}))
	defer owner.Close()
	s := &Server{Config: Config{TestsURL: owner.URL, InternalKey: "transport-key"}, HTTP: owner.Client()}
	input, e := stagedEnvelope(ss, json.RawMessage(`{"rows":[{"row_id":"r2","row":2,"percent":87.5}]} `), 9)
	if e != nil {
		t.Fatal(e)
	}
	preview, ownerHash, e := s.validateStagedInput(context.Background(), ss, "Bearer ephemeral-user", input, 9)
	if e != nil {
		t.Fatal(e)
	}
	p := stagedPreview{Revision: 3, TargetRevision: 9, Input: input, Preview: preview, Hash: digest(preview), OwnerHash: ownerHash}
	if e = s.freshStagedPreview(context.Background(), ss, "Bearer ephemeral-user", p); e != nil {
		t.Fatal(e)
	}
	// Changed candidates/diagnostics matter even if the owner hash is unchanged.
	var changed map[string]any
	json.Unmarshal(ownerPreview, &changed)
	changed["rows"] = []any{map[string]any{"row_id": "r2", "candidates": []any{1, 2}}}
	ownerPreview, _ = json.Marshal(changed)
	if e = s.freshStagedPreview(context.Background(), ss, "Bearer ephemeral-user", p); e == nil || e.Error() != "owner_preview_changed" {
		t.Fatalf("changed preview was silently replaced: %v", e)
	}
	if digest(p.Preview) != p.Hash || requests != 3 {
		t.Fatal("reading mutated the saved snapshot or made an owner apply request")
	}
}

func TestStagedPreviewRejectsWrongOwnerScopeAndMissingConfirmationFlag(t *testing.T) {
	for _, kind := range []string{"tests.json", "tests.external-results"} {
		ss := stagedTestSession(kind)
		valid := stagedTestPreview(ss, false)
		if _, _, e := stagedValidatePreview(ss, valid, 9); e != nil {
			t.Fatalf("valid nonconfirmable diagnostics must remain previewable: %v", e)
		}
		for _, field := range []string{"organization_id", "division_id", "preview_hash", "valid", "can_commit", "test_id", "revision"} {
			var bad map[string]any
			json.Unmarshal(valid, &bad)
			if _, exists := bad[field]; !exists {
				continue
			}
			delete(bad, field)
			raw, _ := json.Marshal(bad)
			if _, _, e := stagedValidatePreview(ss, raw, 9); e == nil {
				t.Fatalf("accepted missing owner field %s for %s", field, kind)
			}
		}
	}
}

type stagedBytesDriver struct{ values []driver.Value }
type stagedBytesConn struct{ values []driver.Value }
type stagedBytesRows struct {
	values []driver.Value
	done   bool
}

func (d stagedBytesDriver) Open(string) (driver.Conn, error) {
	return &stagedBytesConn{values: d.values}, nil
}
func (c *stagedBytesConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepared statement")
}
func (c *stagedBytesConn) Close() error { return nil }
func (c *stagedBytesConn) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected write transaction")
}
func (c *stagedBytesConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &stagedBytesRows{values: c.values}, nil
}
func (r *stagedBytesRows) Columns() []string {
	return []string{"revision", "target_revision", "source_hash", "input_hash", "preview_hash", "owner_preview_hash", "input", "preview"}
}
func (r *stagedBytesRows) Close() error { return nil }
func (r *stagedBytesRows) Next(out []driver.Value) error {
	if r.done {
		return io.EOF
	}
	copy(out, r.values)
	r.done = true
	return nil
}

var stagedBytesDriverSequence atomic.Uint64

func TestStagedByteStoragePreservesExponentPrecisionAndDetectsHistoricalJSONNormalization(t *testing.T) {
	ss := stagedTestSession("tests.json")
	input, e := stagedEnvelope(ss, json.RawMessage(`{"direction_id":7,"payload":{"scientific":1e2,"fraction":1.50,"identifier":9007199254740993}}`), 0)
	if e != nil {
		t.Fatal(e)
	}
	preview, ownerHash, e := stagedValidatePreview(ss, stagedTestPreview(ss, true), 0)
	if e != nil {
		t.Fatal(e)
	}
	for _, normalized := range []bool{false, true} {
		stored := append([]byte(nil), input...)
		if normalized {
			// Actual cpm_dev read-only CAST(... AS JSON) normalized these exact
			// lexemes. Their original validation/hash cannot be silently replaced.
			stored = []byte(strings.NewReplacer(`1e2`, `100.0`, `1.50`, `1.5`).Replace(string(stored)))
		}
		name := fmt.Sprintf("staged-bytes-%d", stagedBytesDriverSequence.Add(1))
		sql.Register(name, stagedBytesDriver{values: []driver.Value{int64(3), int64(0), ss.SourceHash, digest(input), digest(preview), ownerHash, stored, []byte(preview)}})
		db, e := sql.Open(name, "")
		if e != nil {
			t.Fatal(e)
		}
		p, e := loadStagedPreview(context.Background(), db, ss, false)
		db.Close()
		if normalized {
			if e == nil || e.Error() != "staged_preview_integrity_failed" {
				t.Fatalf("normalized historical JSON was silently trusted: %v", e)
			}
		} else if e != nil || !strings.Contains(string(p.Input), `1e2`) || !strings.Contains(string(p.Input), `1.50`) || !strings.Contains(string(p.Input), `9007199254740993`) {
			t.Fatalf("exact binary storage lost source numeric semantics: %s %v", p.Input, e)
		}
	}
}
