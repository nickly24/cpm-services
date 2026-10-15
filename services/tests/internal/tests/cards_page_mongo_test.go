package tests

import (
	"context"
	"encoding/json"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCardsPageIsolatedMongoProjectionAndBoundedDigests(t *testing.T) {
	raw := os.Getenv("CARDS_PAGE_TEST_MONGO_URI")
	if raw == "" {
		t.Skip("dedicated cards QA Mongo not configured")
	}
	u, e := url.Parse(raw)
	if e != nil || u.Host != "127.0.0.1:27319" || u.Path != "/cards_qa" {
		t.Fatal("requires dedicated loopback cards QA Mongo")
	}
	commands := []bson.Raw{}
	c, e := mongo.Connect(options.Client().ApplyURI(raw).SetMonitor(&event.CommandMonitor{Started: func(_ context.Context, e *event.CommandStartedEvent) {
		if e.CommandName == "find" {
			commands = append(commands, append(bson.Raw{}, e.Command...))
		}
	}}))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Disconnect(context.Background())
	collection := c.Database("cards_qa").Collection("cards_page_" + bson.NewObjectID().Hex())
	defer collection.Drop(context.Background())
	docs := []any{}
	ids := []bson.ObjectID{}
	past := time.Now().Add(-time.Hour)
	for i := 0; i < 285; i++ {
		d := cacheDocument()
		d.Placements[0].Settings = Settings{Published: true, AnswersVisible: true, EndAt: &past}
		d.Questions[0].Text = "PRIVATE_PAGE_SENTINEL" + strings.Repeat("x", 10000)
		docs = append(docs, d)
		if len(ids) < 12 {
			ids = append(ids, d.ID)
		}
	}
	if _, e = collection.InsertMany(context.Background(), docs); e != nil {
		t.Fatal(e)
	}
	division := int64(11)
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]any{"status": true, "active": true, "user": Actor{AccountID: 23, OrganizationID: 7, Role: "student", DivisionID: &division, DivisionIDs: []int64{11}}})
	}))
	defer auth.Close()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, Envelope{true, References{Organization: Reference{ID: 7}, Divisions: []Reference{{ID: 11, Active: true}}, Directions: []Reference{{ID: 13, Active: true}}}})
	}))
	defer core.Close()
	key := strings.Repeat("k", 32)
	s := &Server{Store: &Store{Collection: collection}, Dependencies: NewDependencies(Config{AuthURL: auth.URL, CoreURL: core.URL, ServiceKey: key}), Config: Config{ServiceKey: key}}
	mux := http.NewServeMux()
	s.cardsRoutes(mux)
	run := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer qa")
		r.Header.Set("X-Service-Key", key)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	metadata := run("GET", "/internal/tests/cards/catalog?metadata=true&direction_id=13", "")
	if metadata.Code != 200 || strings.Contains(metadata.Body.String(), "PRIVATE_PAGE_SENTINEL") || metadata.Body.Len() > 100000 {
		t.Fatal("metadata payload unbounded or sensitive", metadata.Code, metadata.Body.Len())
	}
	projection := commands[0].Lookup("projection").Document()
	if projection.Lookup("questions").Type != 0 || projection.Lookup("placements").Type == 0 {
		t.Fatal("metadata queried question bodies", projection)
	}
	body, _ := json.Marshal(map[string]any{"test_ids": ids})
	summary := run("POST", "/internal/tests/cards/digests", string(body))
	if summary.Code != 200 || strings.Contains(summary.Body.String(), "PRIVATE_PAGE_SENTINEL") {
		t.Fatal("digest content leak", summary.Code, summary.Body.String())
	}
	values, e := commands[1].Lookup("filter").Document().Lookup("_id").Document().Lookup("$in").Array().Values()
	if e != nil || len(values) != 12 {
		t.Fatal("page selected wrong number of tests", e, len(values))
	}
	ids[0] = bson.NewObjectID()
	body, _ = json.Marshal(map[string]any{"test_ids": ids})
	missing := run("POST", "/internal/tests/cards/digests", string(body))
	if missing.Code != 404 {
		t.Fatal("unavailable test became empty digest", missing.Code)
	}
}
