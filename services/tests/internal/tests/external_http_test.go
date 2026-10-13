package tests

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/drivertest"
)

func TestExternalDeleteConfirmationBindsScopeActorAndRevision(t *testing.T) {
	s := &Server{Config: Config{ServiceKey: strings.Repeat("secret", 8)}}
	a := Actor{OrganizationID: 7, AccountID: 19}
	d := ExternalTestDocument{ID: bson.NewObjectID(), OrganizationID: 7, DivisionID: 31, Revision: 4}
	expires := time.Now().Add(5 * time.Minute).Unix()
	token := s.externalDeleteToken(a, d, expires)
	if e := s.externalCheckToken(a, d, token); e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*Actor, *ExternalTestDocument){func(a *Actor, d *ExternalTestDocument) { a.OrganizationID++ }, func(a *Actor, d *ExternalTestDocument) { a.AccountID++ }, func(a *Actor, d *ExternalTestDocument) { d.DivisionID++ }, func(a *Actor, d *ExternalTestDocument) { d.ID = bson.NewObjectID() }, func(a *Actor, d *ExternalTestDocument) { d.Revision++ }} {
		other, doc := a, d
		change(&other, &doc)
		if s.externalCheckToken(other, doc, token) == nil {
			t.Fatal("confirmation transferred across scope/revision")
		}
	}
	if s.externalCheckToken(a, d, s.externalDeleteToken(a, d, time.Now().Add(-time.Minute).Unix())) == nil {
		t.Fatal("expired confirmation accepted")
	}
}
func TestExternalScoreWirePreservesNullZeroAndDecimals(t *testing.T) {
	for _, raw := range []string{"null", "0", "0.9", "99.75", "100"} {
		r := httptest.NewRequest("PUT", "/", strings.NewReader(`{"expected_revision":3,"expected_result_revision":1,"score":`+raw+`}`))
		r.Header.Set("Content-Type", "application/json")
		var in ExternalResultDTO
		if e := decodeExternalResult(r, &in); e != nil || !validExternalScore(in.Score) {
			t.Fatal(raw, e)
		}
		if raw == "null" && in.Score != nil {
			t.Fatal("unknown became a zero")
		}
		if raw == "0" && (in.Score == nil || *in.Score != 0) {
			t.Fatal("confirmed zero became missing")
		}
	}
	for _, raw := range []string{`{}`, `{"score":true}`, `{"score":0,"organization_id":999}`, `{"score":"80"}`} {
		r := httptest.NewRequest("PUT", "/", strings.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		if decodeExternalResult(r, &ExternalResultDTO{}) == nil {
			t.Fatal("invalid score command accepted", raw)
		}
	}
	for _, value := range []float64{-0.1, 100.01, math.NaN(), math.Inf(1)} {
		if validExternalScore(&value) {
			t.Fatal("invalid score accepted", value)
		}
	}
}
func TestExternalStudentProjectionAndCanonicalRights(t *testing.T) {
	zero, score := 0.0, 80.25
	a := Actor{Role: "student", AccountID: 21, OrganizationID: 7}
	d := ExternalTestDocument{Results: []ExternalResult{{AccountID: 21, Score: &zero}, {AccountID: 22, Score: &score}}}
	v := externalViewWithDirection(a, d, "Направление")
	if v.Result == nil || v.Result.AccountID != 21 || v.Result.Score == nil || *v.Result.Score != 0 || v.Actions.CanEdit {
		t.Fatal("own zero projection changed", v)
	}
	r := httptest.NewRequest("GET", "/?account_id=22", nil)
	if _, e := externalAccountFilter(r, a); e != denied {
		t.Fatal("student selected somebody else", e)
	}
	a = Actor{Role: "staff_admin", Permissions: map[string]Permission{"test-results": {}, "test_results": {View: true, Edit: true}}}
	if actorResults(a, true) {
		t.Fatal("legacy alias bypassed canonical revocation")
	}
}
func TestExternalImportServiceKeyDoesNotReplaceUserAuthentication(t *testing.T) {
	key := strings.Repeat("k", 32)
	s := &Server{Config: Config{ServiceKey: key}, Dependencies: NewDependencies(Config{})}
	for _, endpoint := range []string{"validate", "commit", "receipt"} {
		r := httptest.NewRequest("POST", "/internal/tests/external-results/"+endpoint, strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Service-Key", key)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 401 || !strings.Contains(w.Body.String(), "invalid_token") {
			t.Fatal("key-only import authenticated", w.Code, w.Body.String())
		}
	}
}
func TestExternalCoreProjectionRequiresCompleteScopedDistinctIdentities(t *testing.T) {
	key := strings.Repeat("k", 32)
	complete := true
	org := int64(7)
	division := int64(31)
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if r.Header.Get("X-Service-Key") != key || json.NewDecoder(r.Body).Decode(&body) != nil || string(body["include_students"]) != "true" {
			t.Fatal("missing narrow projection request")
		}
		reply(w, 200, Envelope{true, map[string]any{"organization": Reference{ID: org}, "divisions": []Reference{{ID: division, Active: true}}, "accounts_complete": complete, "account_projection_version": 1, "accounts": []any{map[string]any{"account_id": 21, "profile_id": 91, "full_name": "Иванов Иван", "is_active": false}}}})
	}))
	defer core.Close()
	s := &Server{Config: Config{ServiceKey: key}, Dependencies: NewDependencies(Config{CoreURL: core.URL, ServiceKey: key})}
	people, e := s.externalPeople(context.Background(), 7, 31)
	if e != nil || len(people) != 1 || people[0].AccountID != 21 || people[0].StudentID != 91 || people[0].Active {
		t.Fatal(people, e)
	}
	complete = false
	if _, e = s.externalPeople(context.Background(), 7, 31); e == nil {
		t.Fatal("partial reference list accepted")
	}
	complete = true
	org = 8
	if _, e = s.externalPeople(context.Background(), 7, 31); e == nil {
		t.Fatal("foreign organization projection accepted")
	}
}
func TestExternalSaveSelectsWholeAggregateAndHistoryByScopedCAS(t *testing.T) {
	for _, matched := range []int{0, 1} {
		t.Run(string(rune('0'+matched)), func(t *testing.T) {
			deployment := drivertest.NewMockDeployment(bson.D{{Key: "ok", Value: 1}, {Key: "n", Value: 1}}, bson.D{{Key: "ok", Value: 1}, {Key: "n", Value: matched}, {Key: "nModified", Value: matched}})
			var update bson.Raw
			opt := options.Client().SetMonitor(&event.CommandMonitor{Started: func(_ context.Context, e *event.CommandStartedEvent) {
				if e.CommandName == "update" {
					update = append(bson.Raw{}, e.Command...)
				}
			}})
			opt.Deployment = deployment
			client, e := mongo.Connect(opt)
			if e != nil {
				t.Fatal(e)
			}
			defer client.Disconnect(context.Background())
			s := &Server{Store: &Store{Collection: client.Database("ratings_unit").Collection(CollectionName)}}
			a := Actor{Role: "admin", OrganizationAdmin: true, OrganizationID: 7, AccountID: 19}
			before := ExternalTestDocument{ID: bson.NewObjectID(), OrganizationID: 7, DivisionID: 31, Revision: 4, Results: []ExternalResult{}}
			after := before
			zero := 0.0
			after.Results = []ExternalResult{{ID: bson.NewObjectID(), Revision: 1, AccountID: 21, StudentID: 91, Score: &zero}, {ID: bson.NewObjectID(), Revision: 1, AccountID: 22, StudentID: 92}}
			after.Commands = []ExternalCommandReceipt{{CommandID: "synthetic", InputHash: "input", ActorID: 19, SourceRevision: 5}}
			saved, e := s.externalSave(context.Background(), before, after, a, "results_imported")
			if matched == 0 {
				assertRatingAPIError(t, e, 409, "stale_revision")
			} else if e != nil || saved.Revision != 5 || len(saved.HistoryIDs) != 1 {
				t.Fatal(saved, e)
			}
			values, _ := update.Lookup("updates").Array().Values()
			command := values[0].Document()
			filter := command.Lookup("q").Document()
			if filter.Lookup("organization_id").Int64() != 7 || filter.Lookup("division_id").Int64() != 31 || filter.Lookup("revision").Int64() != 4 {
				t.Fatal("CAS omitted scope or revision", command)
			}
			set := command.Lookup("u").Document().Lookup("$set").Document()
			rows, _ := set.Lookup("results").Array().Values()
			receipts, _ := set.Lookup("commands").Array().Values()
			history, _ := set.Lookup("history_ids").Array().Values()
			if len(rows) != 2 || len(receipts) != 1 || len(history) != 1 {
				t.Fatal("rows/receipt/history not one atomic selection", set)
			}
		})
	}
}

func externalRetryCore(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, Envelope{true, map[string]any{
			"organization": Reference{ID: 7, Active: true}, "divisions": []Reference{{ID: 31, Active: true}},
			"direction":         Reference{ID: 51, Name: "Математика", Active: true},
			"accounts_complete": true, "account_projection_version": 1,
			"accounts": []any{map[string]any{"account_id": 21, "profile_id": 91, "full_name": "Иванов Иван", "is_active": true}},
		}})
	}))
}

func externalRetryCursor(t *testing.T, d ExternalTestDocument) bson.D {
	t.Helper()
	raw, e := bson.Marshal(d)
	if e != nil {
		t.Fatal(e)
	}
	var doc bson.D
	if e = bson.Unmarshal(raw, &doc); e != nil {
		t.Fatal(e)
	}
	return bson.D{{Key: "ok", Value: 1}, {Key: "cursor", Value: bson.D{{Key: "id", Value: int64(0)}, {Key: "ns", Value: "external_retry.tests_external_catalog_v1"}, {Key: "firstBatch", Value: bson.A{doc}}}}}
}

func externalRetryStore(t *testing.T, before, winner ExternalTestDocument, core string, writes *int) *Server {
	t.Helper()
	deployment := drivertest.NewMockDeployment(externalRetryCursor(t, before),
		bson.D{{Key: "ok", Value: 1}, {Key: "n", Value: 1}},
		bson.D{{Key: "ok", Value: 1}, {Key: "n", Value: 0}, {Key: "nModified", Value: 0}},
		externalRetryCursor(t, winner))
	opt := options.Client().SetMonitor(&event.CommandMonitor{Started: func(_ context.Context, e *event.CommandStartedEvent) {
		if e.CommandName == "update" {
			*writes++
		}
	}})
	opt.Deployment = deployment
	client, e := mongo.Connect(opt)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { client.Disconnect(context.Background()) })
	config := Config{CoreURL: core, ServiceKey: strings.Repeat("k", 32)}
	return &Server{Config: config, Store: &Store{Collection: client.Database("external_retry").Collection(CollectionName)}, Dependencies: NewDependencies(config)}
}

func TestExternalManualConcurrentReplayUsesOnlyMatchingCommittedReceipt(t *testing.T) {
	core := externalRetryCore(t)
	defer core.Close()
	for _, method := range []string{"PUT", "DELETE"} {
		for _, state := range []string{"identical", "conflicting", "other_actor", "unrelated", "foreign_division"} {
			t.Run(method+"/"+state, func(t *testing.T) {
				zero, score := 0.0, 0.9
				actor := Actor{Role: "admin", OrganizationAdmin: true, OrganizationID: 7, AccountID: 19}
				before := ExternalTestDocument{ID: bson.NewObjectID(), OrganizationID: 7, DivisionID: 31, DirectionID: 51, Name: "Тест", Date: "2041-05-06", Revision: 4,
					Results: []ExternalResult{{ID: bson.NewObjectID(), Revision: 1, AccountID: 21, StudentID: 91, FullName: "Иванов Иван", Score: &zero}}}
				input := ExternalResultDTO{ExpectedRevision: 4, ExpectedResultRevision: 1}
				if method == "PUT" {
					input.Score = &score
				}
				key := strings.Repeat("a", 16)
				command := "manual:" + key
				hash := hashJSON([]any{actor.OrganizationID, before.DivisionID, before.ID, actor.AccountID, method, int64(21), input})
				winner := before
				winner.Revision++
				winner.Commands = []ExternalCommandReceipt{{CommandID: command, InputHash: hash, ActorID: actor.AccountID, SourceRevision: 5}}
				switch state {
				case "conflicting":
					winner.Commands[0].InputHash = "different"
				case "other_actor":
					winner.Commands[0].ActorID++
				case "unrelated":
					winner.Commands = nil
				case "foreign_division":
					winner.DivisionID++
				}
				writes := 0
				server := externalRetryStore(t, before, winner, core.URL, &writes)
				body, _ := json.Marshal(input)
				request := httptest.NewRequest(method, "/", strings.NewReader(string(body)))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Idempotency-Key", key)
				request.SetPathValue("external", before.ID.Hex())
				request.SetPathValue("division", "31")
				request.SetPathValue("account", "21")
				_, code, e := server.externalResultWrite(request, actor, 31)
				if state == "identical" {
					if e != nil || code != 200 {
						t.Fatal("winning receipt was not acknowledged", code, e)
					}
				} else if e == nil {
					t.Fatal("unrelated or conflicting write was accepted")
				}
				if writes != 1 {
					t.Fatal("retry wrote another mutation", writes)
				}
			})
		}
	}
}

func TestExternalConcurrentDeleteAcceptsOnlyExactConfirmedTombstone(t *testing.T) {
	core := externalRetryCore(t)
	defer core.Close()
	for _, state := range []string{"identical", "different_revision", "not_deleted", "foreign_division"} {
		t.Run(state, func(t *testing.T) {
			actor := Actor{Role: "admin", OrganizationAdmin: true, OrganizationID: 7, AccountID: 19}
			before := ExternalTestDocument{ID: bson.NewObjectID(), OrganizationID: 7, DivisionID: 31, DirectionID: 51, Name: "Тест", Date: "2041-05-06", Revision: 4, Results: []ExternalResult{}}
			winner := before
			winner.Deleted, winner.Revision = true, 5
			switch state {
			case "different_revision":
				winner.Revision++
			case "not_deleted":
				winner.Deleted = false
			case "foreign_division":
				winner.DivisionID++
			}
			writes := 0
			server := externalRetryStore(t, before, winner, core.URL, &writes)
			token := server.externalDeleteToken(actor, before, time.Now().Add(5*time.Minute).Unix())
			body, _ := json.Marshal(ExternalDeleteDTO{ExpectedRevision: 4, ConfirmationToken: token})
			request := httptest.NewRequest("DELETE", "/", strings.NewReader(string(body)))
			request.Header.Set("Content-Type", "application/json")
			request.SetPathValue("external", before.ID.Hex())
			request.SetPathValue("division", "31")
			_, code, e := server.externalDelete(request, actor, 31)
			if state == "identical" {
				if e != nil || code != 200 {
					t.Fatal("confirmed tombstone was not acknowledged", code, e)
				}
			} else if e == nil {
				t.Fatal("unrelated tombstone was accepted")
			}
			if writes != 1 {
				t.Fatal("retry wrote another mutation", writes)
			}
		})
	}
}
