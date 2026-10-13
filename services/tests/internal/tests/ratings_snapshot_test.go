package tests

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type fakeRatingSource struct {
	documents       []Document
	attempts        []Attempt
	external        []RatingExternalTest
	changeExternal  bool
	externalErr     error
	directionName   string
	catalogReads    int
	directionReads  int
	changeResult    bool
	changeDirection bool
	referenceErr    error
}

func (f *fakeRatingSource) externalCatalog(_ context.Context, _ RatingSnapshotRequest, _, _ time.Time) ([]RatingExternalTest, error) {
	if f.externalErr != nil {
		return nil, f.externalErr
	}
	out := append([]RatingExternalTest{}, f.external...)
	if f.changeExternal && f.catalogReads > 1 && len(out) > 0 {
		out[0].Revision++
	}
	return out, nil
}

func (f *fakeRatingSource) catalog(_ context.Context, in RatingSnapshotRequest, _, _ time.Time) ([]Document, error) {
	f.catalogReads++
	return append([]Document{}, f.documents...), nil
}
func (f *fakeRatingSource) submissions(_ context.Context, _ RatingSnapshotRequest, _ []bson.ObjectID) ([]Attempt, error) {
	attempts := append([]Attempt{}, f.attempts...)
	if f.changeResult && f.catalogReads > 1 && len(attempts) > 0 {
		attempts[0].CalculationRevision++
	}
	return attempts, nil
}
func (f *fakeRatingSource) references(_ context.Context, org int64, divisions []int64, direction *int64) (References, error) {
	if f.referenceErr != nil {
		return References{}, f.referenceErr
	}
	refs := References{Organization: Reference{ID: org}, Divisions: []Reference{{ID: divisions[0]}}}
	if direction != nil {
		f.directionReads++
		name := f.directionName
		if f.changeDirection && f.catalogReads > 1 {
			name += " changed"
		}
		refs.Direction = &Reference{ID: *direction, Name: name}
	}
	return refs, nil
}

func ratingFixture() (RatingSnapshotRequest, time.Time, time.Time, *fakeRatingSource) {
	in := RatingSnapshotRequest{11, 17, "2026-10-08", "2026-10-08", "2026-10-07T12:00:00Z"}
	from, until, _ := ratingDateBounds(in)
	id, _ := bson.ObjectIDFromHex("000000000000000000000002")
	start := from.Add(time.Hour)
	d := Document{ID: id, OrganizationID: 11, DirectionID: 31, Title: "Тест", Placements: []Placement{{DivisionID: 17, Settings: Settings{Published: true, StartAt: &start}}}}
	submission := bson.NewObjectID()
	a := Attempt{OrganizationID: 11, DivisionID: 17, AccountID: 23, TestID: id, Active: true, Status: "accepted", SubmissionID: &submission, CalculationRevision: 1, CalculationStatus: "ready", Result: &GradeResult{Score: 0}}
	return in, from, until, &fakeRatingSource{documents: []Document{d}, attempts: []Attempt{a}, directionName: " Математика "}
}

func assertRatingAPIError(t *testing.T, err error, code int, message string) {
	t.Helper()
	var api *APIError
	if !errors.As(err, &api) || api.Code != code || api.Message != message {
		t.Fatalf("got error %v, want %d %s", err, code, message)
	}
}

func TestRatingDateBoundsAreMoscowCalendarAndRequireUTCAsOf(t *testing.T) {
	in, from, until, _ := ratingFixture()
	if from.Format(time.RFC3339) != "2026-10-07T21:00:00Z" || until.Format(time.RFC3339) != "2026-10-08T21:00:00Z" {
		t.Fatal("Moscow calendar range was converted incorrectly", from, until)
	}
	in.AsOf = "2026-10-07T12:00:00+00:00"
	if _, _, e := ratingDateBounds(in); e != nil {
		t.Fatal("valid RFC3339 UTC offset rejected", e)
	}
	// Historical Moscow offsets are read from IANA rules, not a fixed UTC+3.
	in.DateFrom, in.DateTo = "2013-10-08", "2013-10-08"
	from, _, e := ratingDateBounds(in)
	if e != nil || from.Format(time.RFC3339) != "2013-10-07T20:00:00Z" {
		t.Fatal("historical Moscow calendar changed", from, e)
	}
	for _, mutate := range []func(*RatingSnapshotRequest){
		func(in *RatingSnapshotRequest) { in.OrganizationID = 0 },
		func(in *RatingSnapshotRequest) { in.DivisionID = -1 },
		func(in *RatingSnapshotRequest) { in.DateFrom = "2026-10-09" },
		func(in *RatingSnapshotRequest) { in.DateTo = "2026-02-30" },
		func(in *RatingSnapshotRequest) { in.AsOf = "" },
		func(in *RatingSnapshotRequest) { in.AsOf = "2026-10-08T10:00:00" },
		func(in *RatingSnapshotRequest) { in.AsOf = "2026-10-08T10:00:00+03:00" },
	} {
		invalid, _, _, _ := ratingFixture()
		mutate(&invalid)
		_, _, err := ratingDateBounds(invalid)
		assertRatingAPIError(t, err, 400, "invalid_rating_snapshot_request")
	}
}

func TestRatingMongoFiltersKeepPlacementAndSubmissionScope(t *testing.T) {
	in, from, until, source := ratingFixture()
	filter := ratingCatalogFilter(in, from, until)
	placement := filter["placements"].(bson.M)["$elemMatch"].(bson.M)
	if filter["organization_id"] != in.OrganizationID || placement["division_id"] != in.DivisionID || placement["settings.published"] != true {
		t.Fatal("catalog selection expanded its organization/division", filter)
	}
	if !reflect.DeepEqual(placement["settings.start_at"], bson.M{"$gte": from, "$lt": until}) {
		t.Fatal("inclusive date interval is incorrect", filter)
	}
	ids := []bson.ObjectID{source.documents[0].ID}
	filter = ratingSubmissionFilter(in, ids)
	for field, want := range (bson.M{"organization_id": in.OrganizationID, "division_id": in.DivisionID, "active": true, "practice": false, "status": "accepted"}) {
		if filter[field] != want {
			t.Fatalf("submission field %s is not scoped", field)
		}
	}
	if !reflect.DeepEqual(filter["test_id"], bson.M{"$in": ids}) {
		t.Fatal("submissions escaped the selected tests")
	}
	if _, exists := filter["accepted_at"]; exists {
		t.Fatal("as_of introduced a new legacy cutoff")
	}
}

func TestRatingSnapshotSelectsCurrentDivisionAndKeepsPendingDistinctFromZero(t *testing.T) {
	in, from, until, source := ratingFixture()
	base := source.attempts[0]
	for i, status := range []string{"queued", "processing", "retrying", "failed"} {
		a := base
		a.AccountID = int64(30 - i)
		a.CalculationStatus = status
		// An old grade is intentionally ignored while a regrade is pending.
		a.Result = &GradeResult{Score: 99}
		source.attempts = append(source.attempts, a)
	}
	for _, change := range []func(*Attempt){
		func(a *Attempt) { a.OrganizationID++ },
		func(a *Attempt) { a.DivisionID++ },
		func(a *Attempt) { a.Practice = true },
		func(a *Attempt) { a.Status = "reset" },
		func(a *Attempt) { a.Active = false },
		func(a *Attempt) { a.TestID = bson.NewObjectID() },
	} {
		a := base
		change(&a)
		source.attempts = append(source.attempts, a)
	}
	out, e := stableRatingSnapshot(context.Background(), in, from, until, source)
	if e != nil || len(out.Tests) != 1 || len(out.Tests[0].Results) != 5 {
		t.Fatal("selection of official results is incorrect", out, e)
	}
	if out.Tests[0].DirectionName != source.directionName || !out.Tests[0].StartAt.Equal(from.Add(time.Hour)) {
		t.Fatal("test metadata was changed", out)
	}
	results := out.Tests[0].Results
	if results[0].Score == nil || *results[0].Score != 0 {
		t.Fatal("a ready zero grade was treated as pending", results[0])
	}
	for i := 1; i < len(results); i++ {
		if results[i].Score != nil || results[i].AccountID <= results[i-1].AccountID {
			t.Fatal("pending grade leaked or result order is unstable", results)
		}
	}
	if source.catalogReads != 2 || len(out.InputHash) != 64 {
		t.Fatal("snapshot did not perform both reads", source.catalogReads, out.InputHash)
	}
}

func TestRatingSnapshotUsesOnlyPublishedLinkedUnarchivedTestsInDateRange(t *testing.T) {
	in, from, until, source := ratingFixture()
	base := source.documents[0]
	for _, change := range []func(*Document){
		func(d *Document) { d.OrganizationID++ },
		func(d *Document) { d.Archived = true },
		func(d *Document) { d.Placements = nil },
		func(d *Document) { d.Placements[0].DivisionID++ },
		func(d *Document) { d.Placements[0].Settings.Published = false },
		func(d *Document) { start := from.Add(-time.Millisecond); d.Placements[0].Settings.StartAt = &start },
		func(d *Document) { d.Placements[0].Settings.StartAt = &until },
	} {
		d := base
		d.ID = bson.NewObjectID()
		d.Placements = append([]Placement{}, base.Placements...)
		change(&d)
		source.documents = append(source.documents, d)
	}
	// An earlier ID at the last millisecond of the Moscow day sorts first.
	d := base
	d.ID, _ = bson.ObjectIDFromHex("000000000000000000000001")
	d.Placements = append([]Placement{}, base.Placements...)
	start := until.Add(-time.Millisecond)
	d.Placements[0].Settings.StartAt = &start
	source.documents = append(source.documents, d)
	out, e := stableRatingSnapshot(context.Background(), in, from, until, source)
	if e != nil || len(out.Tests) != 2 || out.Tests[0].TestID != d.ID.Hex() {
		t.Fatal("catalog inclusion or deterministic sorting failed", out, e)
	}
	if len(out.Tests[0].Results) != 0 || out.Tests[0].Results == nil {
		t.Fatal("a test with no submission disappeared or contains null results")
	}
	// Both tests start after as_of, which does not exclude legacy rating inputs.
}

func TestRatingSnapshotHashIsStableAndDetectsConcurrentSourceChanges(t *testing.T) {
	in, from, until, source := ratingFixture()
	first, e := stableRatingSnapshot(context.Background(), in, from, until, source)
	if e != nil {
		t.Fatal(e)
	}
	in.AsOf = "2026-10-08T20:00:00Z"
	second, e := stableRatingSnapshot(context.Background(), in, from, until, source)
	if e != nil || first.InputHash != second.InputHash {
		t.Fatal("request time polluted the semantic fingerprint", e)
	}
	for _, direction := range []bool{false, true} {
		in, from, until, changed := ratingFixture()
		changed.changeResult, changed.changeDirection = !direction, direction
		_, e = stableRatingSnapshot(context.Background(), in, from, until, changed)
		assertRatingAPIError(t, e, 409, "rating_source_changed")
	}
}

func TestRatingSnapshotRejectsIncompleteOrAmbiguousResults(t *testing.T) {
	for _, mutate := range []func(*fakeRatingSource){
		func(f *fakeRatingSource) { f.attempts[0].SubmissionID = nil },
		func(f *fakeRatingSource) { f.attempts[0].CalculationRevision = 0 },
		func(f *fakeRatingSource) { f.attempts[0].CalculationStatus = "unknown" },
		func(f *fakeRatingSource) { f.attempts[0].Result = nil },
		func(f *fakeRatingSource) { f.attempts[0].Result = &GradeResult{Score: 101} },
		func(f *fakeRatingSource) { f.attempts = append(f.attempts, f.attempts[0]) },
		func(f *fakeRatingSource) { f.documents = append(f.documents, f.documents[0]) },
	} {
		in, from, until, source := ratingFixture()
		mutate(source)
		_, e := stableRatingSnapshot(context.Background(), in, from, until, source)
		assertRatingAPIError(t, e, 409, "rating_source_incomplete")
	}
}

func TestRatingSnapshotBoundsAndReferenceFailure(t *testing.T) {
	in, from, until, source := ratingFixture()
	source.documents = make([]Document, ratingsMaxTests+1)
	_, e := stableRatingSnapshot(context.Background(), in, from, until, source)
	assertRatingAPIError(t, e, 413, "rating_snapshot_too_large")
	in, from, until, source = ratingFixture()
	base := source.attempts[0]
	source.attempts = nil
	for account := int64(1); account <= ratingsMaxAccounts+1; account++ {
		a := base
		a.AccountID = account
		source.attempts = append(source.attempts, a)
	}
	_, e = stableRatingSnapshot(context.Background(), in, from, until, source)
	assertRatingAPIError(t, e, 413, "rating_snapshot_too_large")
	in, from, until, source = ratingFixture()
	source.documents = nil
	source.referenceErr = missing
	_, e = stableRatingSnapshot(context.Background(), in, from, until, source)
	if !errors.Is(e, missing) || source.catalogReads != 0 {
		t.Fatal("empty catalog bypassed core scope validation", e)
	}
}

func TestRatingSnapshotRouteRequiresServiceKeyAndStrictBody(t *testing.T) {
	s := &Server{Config: Config{ServiceKey: strings.Repeat("k", 32)}}
	mux := http.NewServeMux()
	s.internalRoutes(mux)
	for _, key := range []string{"", "wrong"} {
		r := httptest.NewRequest("POST", "/internal/tests/ratings/snapshot", strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer user-token")
		r.Header.Set("X-Service-Key", key)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal("user credentials bypassed service authentication", w.Code)
		}
	}
	for _, body := range []string{`{"unexpected":true}`, `{}`, `{} {}`, `{"organization_id":11,"division_id":17,"date_from":"2026-10-08","date_to":"2026-10-08","as_of":"2026-10-08T12:00:00+03:00"}`} {
		r := httptest.NewRequest("POST", "/internal/tests/ratings/snapshot", strings.NewReader(body))
		r.Header.Set("X-Service-Key", s.Config.ServiceKey)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal("invalid internal DTO reached the datastore", body, w.Code)
		}
	}
}

func TestRatingSnapshotVerifiesCoreScopeBeforeReadingMongo(t *testing.T) {
	key := strings.Repeat("k", 32)
	for _, code := range []int{404, 503} {
		calls := 0
		core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.URL.Path != "/internal/core/test-references" || r.Method != "POST" || r.Header.Get("X-Service-Key") != key || r.Header.Get("Authorization") != "" {
				t.Error("snapshot did not use the core service reference contract")
			}
			var in struct {
				OrganizationID int64   `json:"organization_id"`
				DivisionIDs    []int64 `json:"division_ids"`
				DirectionID    *int64  `json:"direction_id"`
			}
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.OrganizationID != 11 || !reflect.DeepEqual(in.DivisionIDs, []int64{17}) || in.DirectionID != nil {
				t.Error("snapshot sent an unscoped reference request")
			}
			w.WriteHeader(code)
		}))
		s := &Server{Config: Config{ServiceKey: key}, Dependencies: NewDependencies(Config{CoreURL: core.URL, ServiceKey: key})}
		mux := http.NewServeMux()
		s.internalRoutes(mux)
		body := `{"organization_id":11,"division_id":17,"date_from":"2026-10-08","date_to":"2026-10-08","as_of":"2026-10-08T12:00:00Z"}`
		r := httptest.NewRequest("POST", "/internal/tests/ratings/snapshot", strings.NewReader(body))
		r.Header.Set("X-Service-Key", key)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		core.Close()
		if w.Code != code || calls != 1 {
			t.Fatal("invalid or unavailable core scope reached Mongo", w.Code, calls)
		}
	}
}

func TestRatingSnapshotOpenAPIHasServiceAuthentication(t *testing.T) {
	api := openAPI()
	paths := api["paths"].(map[string]any)
	op := paths["/internal/tests/ratings/snapshot"].(map[string]any)["post"].(map[string]any)
	if !reflect.DeepEqual(op["security"], []any{map[string]any{"serviceKey": []string{}}}) {
		t.Fatal("internal OpenAPI advertised user-token access")
	}
	input := op["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if input["additionalProperties"] != false || len(input["required"].([]string)) != 5 {
		t.Fatal("OpenAPI lost strict required snapshot DTO")
	}
	output := op["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["properties"].(map[string]any)["data"].(map[string]any)
	domain := output["properties"].(map[string]any)["external_domain"].(map[string]any)
	domainProperties := domain["properties"].(map[string]any)
	if domain["type"] != "object" || len(domain["required"].([]string)) != 4 || domainProperties["schema_version"].(map[string]any)["const"] != 1 || domainProperties["status"].(map[string]any)["const"] != "ready" {
		t.Fatal("OpenAPI lost explicit nonnullable initialized domain status", domain)
	}
}

func externalRatingFixture() (RatingSnapshotRequest, time.Time, time.Time, *fakeRatingSource) {
	in, from, until, source := ratingFixture()
	zero, fractional := 0.0, 87.25
	source.external = []RatingExternalTest{{OrganizationID: in.OrganizationID, DivisionID: in.DivisionID,
		TestID: "external-one", Title: "Внешний", DirectionID: 31, Date: in.DateFrom, Revision: 3,
		Results: []RatingExternalResult{{AccountID: 25, ResultID: "result-known", Revision: 2, Score: &fractional},
			{AccountID: 23, ResultID: "result-zero", Revision: 1, Score: &zero},
			{AccountID: 24, ResultID: "result-unknown", Revision: 1}}}}
	return in, from, until, source
}

func TestExternalRatingSnapshotPreservesKnownZeroUnknownAndAbsence(t *testing.T) {
	in, from, until, source := externalRatingFixture()
	out, e := stableRatingSnapshot(context.Background(), in, from, until, source)
	if e != nil || len(out.ExternalTests) != 1 || len(out.ExternalSourceVersion) != 64 {
		t.Fatal(out, e)
	}
	test := out.ExternalTests[0]
	if test.SourceRevision != 3 || test.DirectionName != source.directionName || test.StartAt.Format(time.RFC3339) != "2026-10-07T21:00:00Z" || !test.Published || test.Archived {
		t.Fatal("external catalogue metadata lost", test)
	}
	if len(test.Results) != 3 || test.Results[0].AccountID != 23 || test.Results[0].Score == nil || *test.Results[0].Score != 0 || test.Results[0].CalculationStatus != "ready" {
		t.Fatal("ready zero lost", test.Results)
	}
	if test.Results[1].Score != nil || test.Results[1].CalculationStatus != "missing" || test.Results[1].SubmissionID != "result-unknown" {
		t.Fatal("unknown became absent or known zero", test.Results)
	}
	if test.Results[2].Score == nil || *test.Results[2].Score != 87.25 || test.Results[2].CalculationRevision != 2 {
		t.Fatal("fractional score/revision changed", test.Results)
	}
	for _, r := range test.Results {
		if r.AccountID == 26 {
			t.Fatal("invented absent result", r)
		}
	}
	in, from, until, empty := ratingFixture()
	zeroSource, e := stableRatingSnapshot(context.Background(), in, from, until, empty)
	if e != nil || zeroSource.ExternalTests == nil || len(zeroSource.ExternalTests) != 0 || len(zeroSource.ExternalSourceVersion) != 64 {
		t.Fatal("empty connected external source not explicit", zeroSource, e)
	}
	if out.ExternalDomain != makeExternalDomainStatus(in.OrganizationID, in.DivisionID) || zeroSource.ExternalDomain != out.ExternalDomain {
		t.Fatal("successful reads omitted their scoped domain status", out.ExternalDomain, zeroSource.ExternalDomain)
	}
}

func TestRatingSnapshotHashIncludesExplicitDomainScopeAndSchema(t *testing.T) {
	in, from, until, source := externalRatingFixture()
	out, e := stableRatingSnapshot(context.Background(), in, from, until, source)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(out)
	if e != nil {
		t.Fatal(e)
	}
	var fields map[string]any
	if e = json.Unmarshal(raw, &fields); e != nil {
		t.Fatal(e)
	}
	delete(fields, "input_hash")
	baseline := sha256.Sum256(canonicalJSON(fields))
	if hex.EncodeToString(baseline[:]) != out.InputHash {
		t.Fatal("wire domain metadata excluded from owner input hash", fields)
	}
	domain := fields["external_domain"].(map[string]any)
	for _, field := range []string{"schema_version", "organization_id", "division_id"} {
		old := domain[field]
		domain[field] = old.(float64) + 1
		changed := sha256.Sum256(canonicalJSON(fields))
		if hex.EncodeToString(changed[:]) == out.InputHash {
			t.Fatal("domain identity did not affect owner hash", field)
		}
		domain[field] = old
	}
}

func TestExternalRatingSnapshotVersionsInvalidateEvenUnchangedScores(t *testing.T) {
	in, from, until, source := externalRatingFixture()
	base, e := stableRatingSnapshot(context.Background(), in, from, until, source)
	if e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*fakeRatingSource){
		func(f *fakeRatingSource) { f.external[0].Revision++ },
		func(f *fakeRatingSource) { f.external[0].Results[0].Revision++ },
		func(f *fakeRatingSource) { f.external[0].Results = f.external[0].Results[:2] },
		func(f *fakeRatingSource) { f.external = nil },
	} {
		in, from, until, source = externalRatingFixture()
		mutate(source)
		updated, e := stableRatingSnapshot(context.Background(), in, from, until, source)
		if e != nil || updated.InputHash == base.InputHash || updated.ExternalSourceVersion == base.ExternalSourceVersion {
			t.Fatal("external edit/deletion did not invalidate source", updated, e)
		}
	}
	in, from, until, source = externalRatingFixture()
	source.changeExternal = true
	_, e = stableRatingSnapshot(context.Background(), in, from, until, source)
	assertRatingAPIError(t, e, 409, "rating_source_changed")
}

func TestExternalRatingSnapshotRejectsForeignMalformedAndAmbiguousRows(t *testing.T) {
	for _, mutate := range []func(*fakeRatingSource){
		func(f *fakeRatingSource) { f.external[0].OrganizationID++ },
		func(f *fakeRatingSource) { f.external[0].DivisionID++ },
		func(f *fakeRatingSource) { f.external[0].DirectionID = 0 },
		func(f *fakeRatingSource) { f.external[0].Revision = 0 },
		func(f *fakeRatingSource) { f.external[0].Date = "2026-10-09" },
		func(f *fakeRatingSource) { f.external[0].Date = "2026-02-30" },
		func(f *fakeRatingSource) { f.external[0].Results = nil },
		func(f *fakeRatingSource) { f.external = append(f.external, f.external[0]) },
		func(f *fakeRatingSource) {
			f.external[0].Results = append(f.external[0].Results, f.external[0].Results[0])
		},
		func(f *fakeRatingSource) { f.external[0].Results[0].Revision = 0 },
		func(f *fakeRatingSource) { f.external[0].Results[0].ResultID = "" },
		func(f *fakeRatingSource) { score := 100.01; f.external[0].Results[0].Score = &score },
	} {
		in, from, until, source := externalRatingFixture()
		mutate(source)
		_, e := stableRatingSnapshot(context.Background(), in, from, until, source)
		assertRatingAPIError(t, e, 409, "rating_source_incomplete")
	}
}

func TestExternalRatingSourceFailureNeverBecomesEmptySource(t *testing.T) {
	in, from, until, source := ratingFixture()
	source.externalErr = unavailable
	_, e := stableRatingSnapshot(context.Background(), in, from, until, source)
	if !errors.Is(e, unavailable) {
		t.Fatal("external read failure silently became empty", e)
	}
}
