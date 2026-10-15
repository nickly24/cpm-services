package cards

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type catalogConnector struct{ t *testing.T }

func (c catalogConnector) Connect(context.Context) (driver.Conn, error) { return catalogConn{c.t}, nil }
func (c catalogConnector) Driver() driver.Driver                        { return catalogDriver{} }

type catalogDriver struct{}

func (catalogDriver) Open(string) (driver.Conn, error) { return nil, fmt.Errorf("connector required") }

type catalogConn struct{ t *testing.T }

func (catalogConn) Prepare(string) (driver.Stmt, error) { return nil, fmt.Errorf("no writes/prepares") }
func (catalogConn) Close() error                        { return nil }
func (catalogConn) Begin() (driver.Tx, error)           { return nil, fmt.Errorf("no writes/transactions") }

type catalogRows struct {
	rows    [][]driver.Value
	columns []string
}

func (r *catalogRows) Columns() []string { return r.columns }
func (r *catalogRows) Close() error      { return nil }
func (r *catalogRows) Next(out []driver.Value) error {
	if len(r.rows) == 0 {
		return io.EOF
	}
	copy(out, r.rows[0])
	r.rows = r.rows[1:]
	return nil
}
func (c catalogConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	var rows [][]driver.Value
	var columns []string
	switch {
	case strings.Contains(q, "SELECT id,direction_id,name FROM cards_themes_v1"):
		columns = []string{"id", "direction_id", "name"}
		if args[0].Value == int64(7) {
			rows = [][]driver.Value{{int64(15), int64(13), "Ручная тема"}}
		}
	case strings.Contains(q, "SELECT id,direction_id,name,revision"):
		columns = []string{"id", "direction_id", "name", "revision"}
		rows = [][]driver.Value{{int64(15), int64(13), "Ручная тема", int64(2)}}
	case strings.Contains(q, "SELECT id,question,answer"):
		columns = []string{"id", "question", "answer", "sort_order", "revision"}
		rows = [][]driver.Value{{int64(10), "Вопрос", "Ответ", int64(0), int64(1)}}
	case strings.Contains(q, "SELECT section_ref,card_ref,fingerprint"):
		if args[0].Value != int64(23) || args[1].Value != int64(7) {
			c.t.Fatal("batch progress owner changed", args)
		}
		columns = []string{"section_ref", "card_ref", "fingerprint"}
		rows = [][]driver.Value{{strings.Repeat("a", 24), "test:" + strings.Repeat("a", 24) + ":1", fingerprint("Тестовый вопрос", "Ответ теста")}}
	case strings.Contains(q, "SELECT card_ref,fingerprint"):
		columns = []string{"card_ref", "fingerprint"}
		if args[0].Value != int64(23) || args[1].Value != int64(7) {
			c.t.Fatal("progress changed account/org", args)
		}
		if args[2].Value == "manual" {
			rows = [][]driver.Value{{"card:10", fingerprint("Вопрос", "Ответ")}}
		} else {
			rows = [][]driver.Value{{"test:" + strings.Repeat("a", 24) + ":1", fingerprint("Тестовый вопрос", "Ответ теста")}}
		}
	case strings.Contains(q, "SELECT batch_size,study_mode"):
		columns = []string{"batch_size", "study_mode", "last_batch_index"}
		rows = [][]driver.Value{{int64(20), "all", nil}}
	default:
		c.t.Fatal("unexpected SQL", q)
	}
	return &catalogRows{rows, columns}, nil
}
func TestCatalogUsesAuthorizedProjectionAndPreservesProgress(t *testing.T) {
	id := strings.Repeat("a", 24)
	projection := Projection{TestID: id, Version: strings.Repeat("b", 24), DirectionID: 13, Title: "Тестовая тема", Visible: true, CardsCount: 1, Questions: []Question{{ID: 1, Type: "text", Text: "Тестовый вопрос", Correct: []string{"Ответ теста"}}}}
	catalogs, individual := 0, 0
	foreign := false
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer student" {
			t.Error("lost bearer")
		}
		if r.URL.Path == "/internal/tests/cards/catalog" {
			catalogs++
			if r.URL.Query().Get("include_questions") != "true" {
				t.Error("missing bulk projection")
			}
			if foreign {
				reply(w, 200, map[string]any{"status": true, "data": []Projection{}})
			} else {
				reply(w, 200, map[string]any{"status": true, "data": []Projection{projection}})
			}
		} else {
			individual++
			reply(w, 200, map[string]any{"status": true, "data": projection})
		}
	}))
	defer source.Close()
	db := sql.OpenDB(catalogConnector{t})
	defer db.Close()
	server := &Server{DB: db, Deps: NewDependencies(Config{TestsURL: source.URL})}
	actor := Actor{AccountID: 23, OrganizationID: 7, Role: "student"}
	scope := Scope{OrganizationID: 7, Directions: []Direction{{13, "Право"}}}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer student")
	value, err := server.tree(r, actor, scope)
	if err != nil {
		t.Fatal(err)
	}
	directions := value.(map[string]any)["directions"].([]DirectionView)
	if len(directions) != 1 || len(directions[0].Sections) != 2 || directions[0].Stats.Learned != 2 || catalogs != 1 || individual != 0 {
		t.Fatal("catalog lost progress or repeated projections", directions, catalogs, individual)
	}
	view, err := server.loadView(r, actor, scope, "test", id)
	if err != nil || view.Stats.Learned != 1 || view.Settings.BatchSize != 20 || individual != 1 {
		t.Fatal("direct study differs", view, err)
	}
	// Selected division is not part of manual/test progress or catalog ownership.
	d := int64(99)
	actor.DivisionID = &d
	value, err = server.tree(r, actor, scope)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(value)
	if !strings.Contains(string(raw), `"learned":2`) {
		t.Fatal("division switch discarded progress", string(raw))
	}
	foreign = true
	actor.OrganizationID = 8
	otherScope := Scope{OrganizationID: 8, Directions: []Direction{{113, "Другая организация"}}}
	value, err = server.tree(r, actor, otherScope)
	if err != nil || len(value.(map[string]any)["directions"].([]DirectionView)) != 0 {
		t.Fatal("previous organization's catalog leaked", value, err)
	}
	foreign = false
	actor.OrganizationID = 7
	value, err = server.tree(r, actor, scope)
	if err != nil || value.(map[string]any)["directions"].([]DirectionView)[0].Stats.Learned != 2 {
		t.Fatal("organization switch erased progress", value, err)
	}
	projection.Questions[0].Text = "Обновлённый тестовый вопрос"
	value, err = server.tree(r, actor, scope)
	if err != nil {
		t.Fatal(err)
	}
	changed := value.(map[string]any)["directions"].([]DirectionView)[0].Stats
	if changed.Learned != 1 || changed.Changed != 1 {
		t.Fatal("content changes lost stale progress", changed)
	}
}
func TestBulkProjectionFailsClosedOnOldOrTruncatedContract(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]any{"status": true, "data": []Projection{{TestID: strings.Repeat("a", 24), Version: "v1", Visible: true, CardsCount: 1}}})
	}))
	defer source.Close()
	deps := NewDependencies(Config{TestsURL: source.URL})
	if _, err := deps.catalog(httptest.NewRequest("GET", "/", nil)); err != unavailable {
		t.Fatal("missing content became empty cards", err)
	}
}
