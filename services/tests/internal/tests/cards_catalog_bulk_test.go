package tests

import (
	"encoding/json"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCardsCatalogBatches285TestsAndKeepsFreshScope(t *testing.T) {
	division := int64(11)
	actor := Actor{AccountID: 23, OrganizationID: 7, Role: "student", DivisionID: &division, DivisionIDs: []int64{11, 12}}
	calls := 0
	inactive, outage := false, false
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if outage {
			w.WriteHeader(503)
			return
		}
		var in struct {
			OrganizationID int64   `json:"organization_id"`
			DivisionIDs    []int64 `json:"division_ids"`
			DirectionIDs   []int64 `json:"direction_ids"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.OrganizationID != 7 || len(in.DivisionIDs) != 2 || len(in.DirectionIDs) != 19 {
			t.Error("lost batch scope", in)
		}
		refs := References{Organization: Reference{ID: 7, Active: true}}
		for _, id := range in.DivisionIDs {
			refs.Divisions = append(refs.Divisions, Reference{ID: id, Active: !inactive})
		}
		for _, id := range in.DirectionIDs {
			refs.Directions = append(refs.Directions, Reference{ID: id, Active: true})
		}
		reply(w, 200, Envelope{true, refs})
	}))
	defer core.Close()
	past := time.Now().Add(-time.Hour)
	docs := make([]Document, 285)
	for i := range docs {
		d := cacheDocument()
		d.DirectionID = int64(13 + i%19)
		d.Title = fmt.Sprint(i)
		// An unselected but authorized membership contributes cards as before.
		d.Placements = []Placement{{DivisionID: 12, Settings: Settings{Published: true, AnswersVisible: true, EndAt: &past}}}
		docs[i] = d
	}
	// Hidden answers and unpublished placements never become projections.
	docs[0].Placements[0].Settings.AnswersVisible = false
	docs[1].Placements[0].Settings.Published = false
	server := &Server{Dependencies: NewDependencies(Config{CoreURL: core.URL})}
	r := httptest.NewRequest("GET", "/", nil)
	out, err := server.cardsCatalog(r, actor, docs, true)
	if err != nil || len(out) != 283 || calls != 1 || len(out[0].Questions) != 1 {
		t.Fatal("batch failed", len(out), calls, err)
	}
	inactive = true
	out, err = server.cardsCatalog(r, actor, docs, true)
	if err != nil || len(out) != 0 || calls != 2 {
		t.Fatal("stale membership access", err)
	}
	outage = true
	if _, err = server.cardsCatalog(r, actor, docs, true); err != unavailable {
		t.Fatal("dependency failure became empty success", err)
	}
	docs[0].OrganizationID = 8
	before := calls
	if _, err = server.cardsCatalog(r, actor, docs, true); err != unavailable || calls != before {
		t.Fatal("foreign tenant entered catalog", err)
	}
	out, err = server.cardsCatalog(r, actor, nil, true)
	if err != nil || len(out) != 0 || calls != before {
		t.Fatal("empty catalog should not query dependencies", err)
	}
}

func TestCardsCatalogOpenWindowRequiresOwnOfficialAcceptedAttempt(t *testing.T) {
	division := int64(11)
	actor := Actor{AccountID: 23, OrganizationID: 7, Role: "student", DivisionID: &division, DivisionIDs: []int64{11}}
	docs := []Document{cacheDocument(), cacheDocument(), cacheDocument()}
	end := time.Now().Add(time.Hour)
	for i := range docs {
		docs[i].Placements[0].Settings = Settings{Published: true, AnswersVisible: true, EndAt: &end}
	}
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, Envelope{true, References{Organization: Reference{ID: 7}, Divisions: []Reference{{ID: 11, Active: true}}, Directions: []Reference{{ID: 13, Active: true}}}})
	}))
	defer core.Close()
	accepted := Attempt{ID: bson.NewObjectID(), AccountID: 23, OrganizationID: 7, DivisionID: 11, TestID: docs[0].ID, Active: true, Status: "accepted"}
	started := accepted
	started.ID = bson.NewObjectID()
	started.TestID = docs[1].ID
	started.Status = "started"
	store, commands := cacheMongoStore(t, ratingMongoBatch(AttemptsCollection, accepted, started))
	server := &Server{Store: store, Dependencies: NewDependencies(Config{CoreURL: core.URL})}
	out, err := server.cardsCatalog(httptest.NewRequest("GET", "/", nil), actor, docs, false)
	if err != nil || len(out) != 1 || out[0].TestID != docs[0].ID || len(out[0].Questions) != 0 || len(*commands) != 1 {
		t.Fatal("attempt access changed", out, err)
	}
	filter := (*commands)[0].raw.Lookup("filter").Document()
	if filter.Lookup("account_id").Int64() != 23 || filter.Lookup("organization_id").Int64() != 7 || filter.Lookup("division_id").Int64() != 11 || filter.Lookup("practice").Boolean() {
		t.Fatal("wrong progress owner", filter)
	}
}
