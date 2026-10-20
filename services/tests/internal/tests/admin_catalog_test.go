package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func adminCatalogActor() Actor {
	division := int64(11)
	return Actor{OrganizationID: 7, AccountID: 23, DivisionID: &division, Role: "staff_admin", Permissions: map[string]Permission{"tests": {View: true, Edit: true}}}
}

func TestAdminCatalogFiltersValidateScopeAndCombinedConditions(t *testing.T) {
	a := adminCatalogActor()
	q, e := adminCatalogFilters(httptest.NewRequest("GET", "/?limit=6&offset=12&q=a%2Bb&direction_id=13&relation=attached&owner_division_id=12&is_archived=all&status=hidden&include_external=true&date_from=2026-10-01&date_to=2026-10-19", nil), a, 11, true)
	if e != nil {
		t.Fatal(e)
	}
	if q.Limit != 6 || q.Offset != 12 || q.Status != "hidden" || q.IncludeExternal || q.Online["is_archived"] != nil || q.Online["organization_id"] != int64(7) || q.Online["placements.division_id"] != int64(11) || q.Online["owner_division_id"] != int64(12) || q.Online["title"].(bson.Regex).Pattern != `a\+b` {
		t.Fatal("scope/filter lost", q)
	}
	if len(q.Dates) != 2 || len(q.Online["$and"].(bson.A)) != 1 || len(q.Online["$or"].(bson.A)) != 2 {
		t.Fatal("visibility/relation/date predicates overwritten", q)
	}
	end := q.Dates[1].(bson.M)["$or"].(bson.A)[1].(bson.M)["selected.settings.end_at"].(bson.M)["$lt"].(time.Time)
	if end.UTC().Format(time.RFC3339) != "2026-10-19T21:00:00Z" {
		t.Fatal("incorrect Moscow inclusive boundary", end)
	}
	for _, raw := range []string{"status=oops", "limit=101", "limit=0", "offset=-1", "include_external=1", "date_from=2026-02-30", "date_from=2026-10-20&date_to=2026-10-19", "is_archived=oops", "relation=nope"} {
		if _, e := adminCatalogFilters(httptest.NewRequest("GET", "/?"+raw, nil), a, 11, false); e == nil {
			t.Fatal("accepted invalid query", raw)
		}
	}
	for _, raw := range []string{"include_external=true", "include_external=true&is_archived=all", "include_external=true&relation=owned&owner_division_id=11"} {
		q, e := adminCatalogFilters(httptest.NewRequest("GET", "/?"+raw, nil), a, 11, false)
		if e != nil || !q.IncludeExternal {
			t.Fatal("external incorrectly omitted", raw, e)
		}
	}
	for _, raw := range []string{"include_external=false", "include_external=true&is_archived=true", "include_external=true&relation=copy", "include_external=true&relation=attached", "include_external=true&owner_division_id=12"} {
		q, e := adminCatalogFilters(httptest.NewRequest("GET", "/?"+raw, nil), a, 11, false)
		if e != nil || q.IncludeExternal {
			t.Fatal("external scope widened", raw, e)
		}
	}
	for _, role := range []string{"student", "staff_admin"} {
		a.Role = role
		a.Permissions = nil
		if _, e := adminCatalogFilters(httptest.NewRequest("GET", "/", nil), a, 11, false); e != denied {
			t.Fatal("unauthorised admin view", role, e)
		}
	}
}

func adminCatalogCore(t *testing.T, org int64, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var in struct {
			OrganizationID int64   `json:"organization_id"`
			DivisionIDs    []int64 `json:"division_ids"`
			DirectionIDs   []int64 `json:"direction_ids"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.OrganizationID != org {
			t.Error("bad reference request")
		}
		refs := References{Organization: Reference{ID: org, Active: true}, Divisions: []Reference{}, Directions: []Reference{}}
		for _, id := range in.DivisionIDs {
			refs.Divisions = append(refs.Divisions, Reference{ID: id, Name: fmt.Sprint("Подразделение ", id), Active: true})
		}
		for _, id := range in.DirectionIDs {
			refs.Directions = append(refs.Directions, Reference{ID: id, Name: fmt.Sprint("Направление ", id), Active: true})
		}
		reply(w, 200, Envelope{true, refs})
	}))
}

func TestAdminCatalogOnlyHydratesOnePageAndClampsOnce(t *testing.T) {
	row := adminCatalogRow{Document: cacheDocument(), QuestionCount: 200, Status: "active"}
	row.Document.Questions = nil
	store, commands := cacheMongoStore(t,
		ratingMongoBatch(CollectionName, bson.M{"items": bson.A{}, "totals": bson.A{bson.M{"total": int64(7)}}, "counts": bson.A{bson.M{"_id": "active", "count": int64(7)}}}),
		ratingMongoBatch(CollectionName, bson.M{"items": []adminCatalogRow{row}, "totals": bson.A{bson.M{"total": int64(7)}}, "counts": bson.A{bson.M{"_id": "active", "count": int64(7)}}}))
	var calls atomic.Int64
	core := adminCatalogCore(t, 7, &calls)
	defer core.Close()
	s := &Server{Store: store, Dependencies: NewDependencies(Config{CoreURL: core.URL})}
	value, code, e := s.list(httptest.NewRequest("GET", "/?view=admin&limit=6&offset=600&include_external=true", nil), adminCatalogActor(), 11, false)
	if e != nil || code != 200 {
		t.Fatal(e, code)
	}
	out := value.(AdminCatalogView)
	if out.Total != 7 || out.Offset != 6 || len(out.Items) != 1 || out.Items[0].Test.QuestionCount != 200 || calls.Load() != 1 || len(*commands) != 2 {
		t.Fatal("unbounded page/ref hydration or wrong clamp", out, calls.Load(), len(*commands))
	}
	for _, command := range *commands {
		if command.name != "aggregate" {
			t.Fatal("read full document", command.name)
		}
		raw := command.raw.String()
		if strings.Contains(raw, `"$lookup"`) || !strings.Contains(raw, `"$limit":`) || !strings.Contains(raw, `"$unionWith":`) {
			t.Fatal("missing database pagination", raw)
		}
	}
	raw, _ := json.Marshal(out)
	for _, key := range []string{`"questions"`, `"answers"`, `"results"`, `"history_ids"`, `"commands"`} {
		if strings.Contains(string(raw), key) {
			t.Fatal("private payload in catalogue", key)
		}
	}
}

func TestAdminCatalogLargeTotalStillReadsOnlyRequestedPage(t *testing.T) {
	row := adminCatalogRow{Document: cacheDocument(), QuestionCount: 200, Status: "active"}
	row.Document.Questions = nil
	store, commands := cacheMongoStore(t, ratingMongoBatch(CollectionName, bson.M{"items": []adminCatalogRow{row}, "totals": bson.A{bson.M{"total": int64(50000)}}, "counts": bson.A{bson.M{"_id": "active", "count": int64(50000)}}}))
	var calls atomic.Int64
	core := adminCatalogCore(t, 7, &calls)
	defer core.Close()
	server := &Server{Store: store, Dependencies: NewDependencies(Config{CoreURL: core.URL})}
	value, code, err := server.list(httptest.NewRequest("GET", "/?view=admin&limit=6&offset=12", nil), adminCatalogActor(), 11, false)
	if err != nil || code != 200 {
		t.Fatal(err, code)
	}
	out := value.(AdminCatalogView)
	if out.Total != 50000 || out.Offset != 12 || len(out.Items) != 1 || len(*commands) != 1 || calls.Load() != 1 {
		t.Fatal("large total amplified page reads", out, len(*commands), calls.Load())
	}
}

// This test refuses any shared or production database. The named disposable
// loopback Mongo is created by the local QA command and removed afterwards.
func TestAdminCatalogMongoIntegration(t *testing.T) {
	uri := os.Getenv("CPM_ADMIN_CATALOG_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("set dedicated loopback QA Mongo URI")
	}
	if uri != "mongodb://127.0.0.1:27320" {
		t.Fatal("requires dedicated admin catalogue QA Mongo")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var aggregates atomic.Int64
	client, e := mongo.Connect(options.Client().ApplyURI(uri).SetMonitor(&event.CommandMonitor{Started: func(_ context.Context, e *event.CommandStartedEvent) {
		if e.CommandName == "aggregate" {
			aggregates.Add(1)
		}
	}}))
	if e != nil {
		t.Fatal(e)
	}
	defer client.Disconnect(context.Background())
	db := client.Database("admin_catalog_qa_" + bson.NewObjectID().Hex())
	defer db.Drop(context.Background())
	store := &Store{Collection: db.Collection(CollectionName)}
	a := adminCatalogActor()
	now := time.Now().UTC().Truncate(time.Millisecond)
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	docs := []any{}
	counts := map[string]int64{"all": 144, "active": 0, "hidden": 0, "ended": 0, "upcoming": 0, "external": 7}
	for i := 0; i < 137; i++ {
		d := cacheDocument()
		d.Title = fmt.Sprintf("Каталог %03d", i)
		d.CreatedAt = now.Add(-time.Duration(i) * time.Second)
		d.DirectionID = 13 + int64(i%2)
		if i%10 == 0 {
			d.Title += " право"
		}
		if i == 136 {
			d.Title += " a+b"
		}
		d.Questions[0].Text = "PRIVATE_QUESTION_SENTINEL" + strings.Repeat("x", 16000)
		settings := Settings{Published: true, StartAt: &start, EndAt: &end, TimeLimitMinutes: 30}
		status := []string{"active", "hidden", "upcoming", "ended"}[i%4]
		switch status {
		case "hidden":
			settings.Published = false
		case "upcoming":
			future := now.Add(time.Hour)
			settings.StartAt = &future
		case "ended":
			past := now.Add(-time.Minute)
			settings.EndAt = &past
		}
		counts[status]++
		d.Placements = []Placement{{DivisionID: 11, Settings: settings}, {DivisionID: 12, Settings: Settings{Published: false}}}
		docs = append(docs, d)
	}
	bankOnly := cacheDocument()
	bankOnly.Title = "Банк без подключения"
	bankOnly.OwnerDivisionID = 12
	bankOnly.BankAvailable = true
	bankOnly.Placements = []Placement{{DivisionID: 12}}
	docs = append(docs, bankOnly)
	archived := cacheDocument()
	archived.Title = "Архивный"
	archived.Archived = true
	docs = append(docs, archived)
	foreign := cacheDocument()
	foreign.OrganizationID = 8
	foreign.Title = "PRIVATE_FOREIGN_ORG"
	docs = append(docs, foreign)
	closed := bankOnly
	closed.ID = bson.NewObjectID()
	closed.BankAvailable = false
	closed.Title = "PRIVATE_CLOSED_BANK"
	docs = append(docs, closed)
	if _, e = store.Collection.InsertMany(ctx, docs); e != nil {
		t.Fatal(e)
	}
	externals := []any{}
	for i := 0; i < 7; i++ {
		externals = append(externals, ExternalTestDocument{ID: bson.NewObjectID(), OrganizationID: 7, DivisionID: 11, DirectionID: 13, Name: fmt.Sprintf("Внешний %d", i), Date: "2026-10-19", Revision: 1, Results: []ExternalResult{{AccountID: 999, FullName: "PRIVATE_RESULT_SENTINEL"}}})
	}
	externals = append(externals, ExternalTestDocument{ID: bson.NewObjectID(), OrganizationID: 7, DivisionID: 12, DirectionID: 13, Name: "PRIVATE_OTHER_DIVISION", Date: "2026-10-19"}, ExternalTestDocument{ID: bson.NewObjectID(), OrganizationID: 8, DivisionID: 11, DirectionID: 13, Name: "PRIVATE_OTHER_ORG", Date: "2026-10-19"})
	if _, e = store.External().InsertMany(ctx, externals); e != nil {
		t.Fatal(e)
	}
	var calls atomic.Int64
	core := adminCatalogCore(t, 7, &calls)
	defer core.Close()
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor := a
		if r.Header.Get("Authorization") == "Bearer student" {
			actor.Role = "student"
		}
		reply(w, 200, map[string]any{"status": true, "active": true, "user": actor})
	}))
	defer auth.Close()
	s := &Server{Store: store, Dependencies: NewDependencies(Config{CoreURL: core.URL, AuthURL: auth.URL})}
	handler := s.Handler()
	run := func(path string) AdminCatalogView {
		t.Helper()
		before := aggregates.Load()
		refsBefore := calls.Load()
		request := httptest.NewRequest("GET", path, nil)
		request.Header.Set("Authorization", "Bearer qa")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, request)
		if w.Code != 200 {
			t.Fatal(path, w.Code, w.Body.String())
		}
		var envelope struct {
			Data AdminCatalogView `json:"data"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &envelope); e != nil {
			t.Fatal(e)
		}
		out := envelope.Data
		if len(out.Items) > int(out.Limit) || aggregates.Load()-before > 2 || calls.Load()-refsBefore > 3 || w.Body.Len() > 35000 {
			t.Fatal("unbounded reads/payload", path, w.Body.Len(), aggregates.Load()-before, calls.Load()-refsBefore)
		}
		for _, secret := range []string{"PRIVATE_", "\"questions\"", "\"results\"", "\"commands\""} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("private data leaked", secret)
			}
		}
		return out
	}
	base := "/v1/tests/divisions/11/tests?view=admin&is_archived=false&include_external=true&limit=6"
	first := run(base)
	if first.Total != 144 || len(first.Items) != 6 || first.Items[0].Test.Title != "Каталог 000 право" {
		t.Fatal("first page", first)
	}
	for key, n := range counts {
		if first.StatusCounts[key] != n {
			t.Fatal("wrong status count", key, first.StatusCounts, n)
		}
	}
	boundary := run(base + "&offset=132")
	if len(boundary.Items) != 6 || boundary.Items[4].Test == nil || boundary.Items[5].External == nil {
		t.Fatal("internal/external boundary broken", boundary)
	}
	tail := run(base + "&offset=138")
	if len(tail.Items) != 6 || tail.Items[0].External == nil || tail.Items[0].External.Actions.CanDelete {
		t.Fatal("external page or result permissions", tail)
	}
	clamped := run(base + "&offset=600")
	if clamped.Offset != 138 || len(clamped.Items) != 6 {
		t.Fatal("outdated last page not clamped", clamped)
	}
	seen := map[bson.ObjectID]bool{}
	for offset := int64(0); offset < 144; offset += 6 {
		out := run(base + fmt.Sprint("&offset=", offset))
		for _, item := range out.Items {
			var id bson.ObjectID
			if item.Test != nil {
				id = item.Test.ID
			} else {
				id = item.External.ID
			}
			if seen[id] {
				t.Fatal("duplicate across pages", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != 144 {
		t.Fatal("page traversal lost tests", len(seen))
	}
	for _, tc := range []struct {
		query string
		total int64
	}{{"&status=hidden", counts["hidden"]}, {"&status=external", 7}, {"&q=право", 14}, {"&q=a%2Bb", 1}, {"&q=право&status=active&direction_id=13", 7}, {"&relation=attached&owner_division_id=12", 0}, {"&relation=owned&owner_division_id=11&status=external", 7}, {"&date_from=2026-10-19&date_to=2026-10-19&status=external", 7}, {"&date_from=2031-01-01", 0}, {"&q=несуществующий", 0}} {
		out := run(base + tc.query)
		if out.Total != tc.total {
			t.Fatal("combined filter wrong", tc.query, out.Total, tc.total)
		}
	}
	bank := run("/v1/tests/bank?view=admin&is_archived=all&include_external=true&limit=6")
	if bank.Total != 139 || bank.StatusCounts["external"] != 0 || bank.StatusCounts["bank"] != 1 || bank.StatusCounts["archived"] != 1 {
		t.Fatal("bank visibility/counts", bank)
	}
	archivedOnly := run("/v1/tests/bank?view=admin&is_archived=true&limit=6")
	if archivedOnly.Total != 1 {
		t.Fatal("archive filter", archivedOnly)
	}
	request := httptest.NewRequest("GET", base, nil)
	request.Header.Set("Authorization", "Bearer student")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, request)
	if w.Code != 403 {
		t.Fatal("student admin view", w.Code)
	}
	request = httptest.NewRequest("GET", "/v1/tests/divisions/12/tests?view=admin", nil)
	request.Header.Set("Authorization", "Bearer qa")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, request)
	if w.Code != 403 {
		t.Fatal("foreign division admin view", w.Code)
	}
	t.Logf("144 visible tests; 24 disjoint pages; mixed boundary, server counts/search/status/date/relation/archive, permissions and bounded projections PASS")
}
