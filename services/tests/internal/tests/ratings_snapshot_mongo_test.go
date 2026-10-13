package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/drivertest"
)

func ratingMongoBatch(collection string, documents ...any) bson.D {
	if documents == nil {
		documents = []any{}
	}
	return bson.D{{Key: "ok", Value: 1}, {Key: "cursor", Value: bson.M{
		"id": int64(0), "ns": "ratings_unit." + collection, "firstBatch": documents,
	}}}
}

func readyExternalRatingDomain() bson.D {
	return ratingMongoBatch(externalRatingsDomainCollection, bson.M{"_id": "external-tests", "schema_version": 1, "status": "ready"})
}

func TestRatingSnapshotHTTPReadsProjectedMongoAndCoreTwice(t *testing.T) {
	in, _, _, fixture := ratingFixture()
	key := strings.Repeat("k", 32)
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			DirectionID *int64 `json:"direction_id"`
		}
		if r.Header.Get("X-Service-Key") != key || json.NewDecoder(r.Body).Decode(&input) != nil {
			w.WriteHeader(401)
			return
		}
		refs := References{Organization: Reference{ID: in.OrganizationID}, Divisions: []Reference{{ID: in.DivisionID}}}
		if input.DirectionID != nil {
			refs.Direction = &Reference{ID: *input.DirectionID, Name: fixture.directionName}
		}
		reply(w, 200, Envelope{true, refs})
	}))
	defer core.Close()
	deployment := drivertest.NewMockDeployment(
		ratingMongoBatch(CollectionName, fixture.documents[0]), ratingMongoBatch(AttemptsCollection, fixture.attempts[0]),
		readyExternalRatingDomain(),
		ratingMongoBatch(externalRatingsCollection),
		ratingMongoBatch(CollectionName, fixture.documents[0]), ratingMongoBatch(AttemptsCollection, fixture.attempts[0]),
		readyExternalRatingDomain(),
		ratingMongoBatch(externalRatingsCollection),
	)
	commands := []bson.Raw{}
	clientOptions := options.Client().SetMonitor(&event.CommandMonitor{Started: func(_ context.Context, e *event.CommandStartedEvent) {
		if e.CommandName == "find" {
			commands = append(commands, append(bson.Raw{}, e.Command...))
		}
	}})
	clientOptions.Deployment = deployment
	client, e := mongo.Connect(clientOptions)
	if e != nil {
		t.Fatal(e)
	}
	defer client.Disconnect(context.Background())
	s := &Server{Config: Config{ServiceKey: key}, Store: &Store{Collection: client.Database("ratings_unit").Collection(CollectionName)}, Dependencies: NewDependencies(Config{CoreURL: core.URL, ServiceKey: key})}
	raw, _ := json.Marshal(in)
	r := httptest.NewRequest("POST", "/internal/tests/ratings/snapshot", strings.NewReader(string(raw)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Service-Key", key)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("full snapshot handler failed", w.Code, w.Body.String())
	}
	var response struct {
		Status bool           `json:"status"`
		Data   RatingSnapshot `json:"data"`
	}
	if json.Unmarshal(w.Body.Bytes(), &response) != nil || !response.Status || len(response.Data.Tests) != 1 || len(response.Data.Tests[0].Results) != 1 || response.Data.Tests[0].Results[0].Score == nil || *response.Data.Tests[0].Results[0].Score != 0 {
		t.Fatal("BSON zero grade was lost in the HTTP DTO", w.Body.String())
	}
	if response.Data.ExternalDomain != makeExternalDomainStatus(in.OrganizationID, in.DivisionID) {
		t.Fatal("successful Mongo snapshot omitted explicit owner readiness", response.Data.ExternalDomain)
	}
	if len(commands) != 8 {
		t.Fatal("did not compare two complete Mongo reads", len(commands))
	}
	for _, command := range commands {
		filter := command.Lookup("filter").Document()
		if command.Lookup("find").StringValue() == externalRatingsDomainCollection {
			if filter.Lookup("_id").StringValue() != "external-tests" {
				t.Fatal("readiness marker is not the owner domain", command)
			}
			continue
		}
		if filter.Lookup("organization_id").Int64() != in.OrganizationID {
			t.Fatal("Mongo request omitted the organization filter")
		}
		projection := command.Lookup("projection").Document()
		for _, forbidden := range []string{"questions", "answers", "commits", "calculation_history"} {
			if projection.Lookup(forbidden).Type != 0 {
				t.Fatalf("Mongo projection includes %s", forbidden)
			}
		}
	}
}

func TestRatingSnapshotMongoRejectsMissingReadyScore(t *testing.T) {
	in, _, _, fixture := ratingFixture()
	attempt := fixture.attempts[0]
	raw, _ := bson.Marshal(attempt)
	var malformed bson.M
	_ = bson.Unmarshal(raw, &malformed)
	malformed["result"] = bson.M{}
	deployment := drivertest.NewMockDeployment(ratingMongoBatch(AttemptsCollection, malformed))
	clientOptions := options.Client()
	clientOptions.Deployment = deployment
	client, e := mongo.Connect(clientOptions)
	if e != nil {
		t.Fatal(e)
	}
	defer client.Disconnect(context.Background())
	source := mongoRatingSource{store: &Store{Collection: client.Database("ratings_unit").Collection(CollectionName)}}
	_, e = source.submissions(context.Background(), in, []bson.ObjectID{attempt.TestID})
	assertRatingAPIError(t, e, 409, "rating_source_incomplete")
}

func TestExternalRatingCatalogMongoPreservesScopeRevisionAndNullableScore(t *testing.T) {
	in, from, until, _ := ratingFixture()
	zero := 0.0
	testID, zeroID, unknownID := bson.NewObjectID(), bson.NewObjectID(), bson.NewObjectID()
	document := externalRatingDocument{ID: testID, OrganizationID: in.OrganizationID, DivisionID: in.DivisionID,
		Name: "Внешний", DirectionID: 31, Date: in.DateFrom, Revision: 5,
		Results: []externalRatingResult{{ID: zeroID, AccountID: 23, Revision: 2, Score: &zero}, {ID: unknownID, AccountID: 24, Revision: 1}}}
	deployment := drivertest.NewMockDeployment(readyExternalRatingDomain(), ratingMongoBatch(externalRatingsCollection, document))
	var command bson.Raw
	clientOptions := options.Client().SetMonitor(&event.CommandMonitor{Started: func(_ context.Context, e *event.CommandStartedEvent) {
		if e.CommandName == "find" {
			command = append(bson.Raw{}, e.Command...)
		}
	}})
	clientOptions.Deployment = deployment
	client, e := mongo.Connect(clientOptions)
	if e != nil {
		t.Fatal(e)
	}
	defer client.Disconnect(context.Background())
	source := mongoRatingSource{store: &Store{Collection: client.Database("ratings_unit").Collection(CollectionName)}}
	out, e := source.externalCatalog(context.Background(), in, from, until)
	if e != nil || len(out) != 1 || out[0].TestID != testID.Hex() || out[0].Revision != 5 || len(out[0].Results) != 2 {
		t.Fatal(out, e)
	}
	if out[0].Results[0].ResultID != zeroID.Hex() || out[0].Results[0].Score == nil || *out[0].Results[0].Score != 0 || out[0].Results[1].Score != nil {
		t.Fatal("zero/null lost on BSON conversion", out[0].Results)
	}
	filter := command.Lookup("filter").Document()
	if filter.Lookup("organization_id").Int64() != in.OrganizationID || filter.Lookup("division_id").Int64() != in.DivisionID || filter.Lookup("date").Document().Lookup("$gte").StringValue() != in.DateFrom || filter.Lookup("date").Document().Lookup("$lte").StringValue() != in.DateTo {
		t.Fatal("external source filter lost scope/date", command)
	}
	projection := command.Lookup("projection").Document()
	for _, forbidden := range []string{"results.full_name", "results.student_id", "history", "outbox"} {
		if projection.Lookup(forbidden).Type != 0 {
			t.Fatal("rating projection leaked personal/transport data", forbidden)
		}
	}
}

func TestExternalRatingCatalogMongoRejectsUnscopedOrDeletedDocument(t *testing.T) {
	for _, mutate := range []func(*externalRatingDocument){
		func(d *externalRatingDocument) { d.OrganizationID++ },
		func(d *externalRatingDocument) { d.DivisionID++ },
		func(d *externalRatingDocument) { d.Deleted = true },
		func(d *externalRatingDocument) { d.ID = bson.NilObjectID },
		func(d *externalRatingDocument) { d.Results[0].ID = bson.NilObjectID },
	} {
		in, from, until, _ := ratingFixture()
		d := externalRatingDocument{ID: bson.NewObjectID(), OrganizationID: in.OrganizationID, DivisionID: in.DivisionID,
			Date: in.DateFrom, Results: []externalRatingResult{{ID: bson.NewObjectID()}}}
		mutate(&d)
		deployment := drivertest.NewMockDeployment(readyExternalRatingDomain(), ratingMongoBatch(externalRatingsCollection, d))
		clientOptions := options.Client()
		clientOptions.Deployment = deployment
		client, e := mongo.Connect(clientOptions)
		if e != nil {
			t.Fatal(e)
		}
		source := mongoRatingSource{store: &Store{Collection: client.Database("ratings_unit").Collection(CollectionName)}}
		_, e = source.externalCatalog(context.Background(), in, from, until)
		client.Disconnect(context.Background())
		assertRatingAPIError(t, e, 409, "rating_source_incomplete")
	}
}

func TestExternalRatingCatalogRequiresInitializedOwnerDomain(t *testing.T) {
	for _, tc := range []struct {
		name   string
		marker any
	}{
		{"absent", nil},
		{"initializing", bson.M{"_id": "external-tests", "schema_version": 1, "status": "initializing"}},
		{"wrong schema", bson.M{"_id": "external-tests", "schema_version": 2, "status": "ready"}},
		{"missing schema", bson.M{"_id": "external-tests", "status": "ready"}},
		{"malformed schema", bson.M{"_id": "external-tests", "schema_version": "1", "status": "ready"}},
		{"malformed status", bson.M{"_id": "external-tests", "schema_version": 1, "status": true}},
		{"wrong owner", bson.M{"_id": "another-domain", "schema_version": 1, "status": "ready"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := ratingMongoBatch(externalRatingsDomainCollection)
			if tc.marker != nil {
				response = ratingMongoBatch(externalRatingsDomainCollection, tc.marker)
			}
			deployment := drivertest.NewMockDeployment(response)
			reads := []string{}
			clientOptions := options.Client().SetMonitor(&event.CommandMonitor{Started: func(_ context.Context, e *event.CommandStartedEvent) {
				if e.CommandName == "find" {
					reads = append(reads, e.Command.Lookup("find").StringValue())
				}
			}})
			clientOptions.Deployment = deployment
			client, e := mongo.Connect(clientOptions)
			if e != nil {
				t.Fatal(e)
			}
			defer client.Disconnect(context.Background())
			in, from, until, _ := ratingFixture()
			source := mongoRatingSource{store: &Store{Collection: client.Database("ratings_unit").Collection(CollectionName)}}
			out, e := source.externalCatalog(context.Background(), in, from, until)
			assertRatingAPIError(t, e, 503, "external_domain_unavailable")
			if out != nil || len(reads) != 1 || reads[0] != externalRatingsDomainCollection {
				t.Fatal("uninitialized domain was read or declared empty", out, reads)
			}
		})
	}
}

func TestRatingSnapshotHTTPDoesNotPublishVersionForMissingExternalDomain(t *testing.T) {
	in, _, _, fixture := ratingFixture()
	key := strings.Repeat("k", 32)
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			DirectionID *int64 `json:"direction_id"`
		}
		if r.Header.Get("X-Service-Key") != key || json.NewDecoder(r.Body).Decode(&input) != nil {
			w.WriteHeader(401)
			return
		}
		refs := References{Organization: Reference{ID: in.OrganizationID}, Divisions: []Reference{{ID: in.DivisionID}}}
		if input.DirectionID != nil {
			refs.Direction = &Reference{ID: *input.DirectionID, Name: fixture.directionName}
		}
		reply(w, 200, Envelope{true, refs})
	}))
	defer core.Close()
	deployment := drivertest.NewMockDeployment(ratingMongoBatch(CollectionName, fixture.documents[0]),
		ratingMongoBatch(AttemptsCollection, fixture.attempts[0]), ratingMongoBatch(externalRatingsDomainCollection))
	clientOptions := options.Client()
	clientOptions.Deployment = deployment
	client, e := mongo.Connect(clientOptions)
	if e != nil {
		t.Fatal(e)
	}
	defer client.Disconnect(context.Background())
	s := &Server{Config: Config{ServiceKey: key}, Store: &Store{Collection: client.Database("ratings_unit").Collection(CollectionName)}, Dependencies: NewDependencies(Config{CoreURL: core.URL, ServiceKey: key})}
	raw, _ := json.Marshal(in)
	r := httptest.NewRequest("POST", "/internal/tests/ratings/snapshot", strings.NewReader(string(raw)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Service-Key", key)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "external_domain_unavailable") || strings.Contains(w.Body.String(), "external_source_version") || strings.Contains(w.Body.String(), `"external_domain":`) || strings.Contains(w.Body.String(), "input_hash") {
		t.Fatal("missing external owner appeared connected", w.Code, w.Body.String())
	}
}
