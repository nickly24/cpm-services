package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestStudentCatalogReadModelUsesAuthoritativeAccessAndNoAnswerPayload(t *testing.T) {
	now := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	base := studentCatalogRow{ID: bson.NewObjectID(), OrganizationID: 7, DivisionID: 11, AccountID: 23, DirectionID: 13, QuestionCount: 2, Settings: Settings{Published: true, StartAt: &start, EndAt: &end, TimeLimitMinutes: 30, AnswersVisible: true}}
	direction := Reference{ID: 13, Name: "Алгебра", Active: true}
	for _, tc := range []struct {
		name, status, calculation string
		answer, upload            time.Duration
		accepted, inactive        bool
		expected                  string
	}{
		{name: "available", expected: "available"},
		{name: "running", status: "started", answer: time.Minute, upload: time.Hour, expected: "running"},
		{name: "pinned deadline at boundary", status: "started", upload: time.Hour, expected: "running"},
		{name: "pending offline upload", status: "started", answer: -time.Minute, upload: time.Hour, expected: "pending_upload"},
		{name: "upload expired despite open schedule", status: "started", answer: -time.Hour, upload: -time.Minute, expected: "upload_window_closed"},
		{name: "queued", status: "accepted", calculation: "queued", accepted: true, expected: "checking"},
		{name: "zero score", status: "accepted", calculation: "ready", accepted: true, expected: "completed"},
		{name: "failed", status: "accepted", calculation: "failed", accepted: true, expected: "calculation_failed"},
		{name: "archived direction prevents resume", status: "started", answer: time.Minute, upload: time.Hour, inactive: true, expected: "missed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row, ref := base, direction
			ref.Active = !tc.inactive
			if tc.status != "" {
				row.Attempt = &StudentAttemptSummary{ID: bson.NewObjectID(), Status: tc.status, AnswerDeadline: now.Add(tc.answer), UploadDeadline: now.Add(tc.upload), AnsweredCount: 1, TotalQuestions: 2, CalculationStatus: tc.calculation}
			}
			if tc.accepted {
				id, zero := bson.NewObjectID(), 0
				row.Attempt.SubmissionID, row.Attempt.Score, row.Attempt.AcceptedAt = &id, &zero, &now
			}
			item := buildStudentCatalogItem(row, ref, now)
			if item.Status != tc.expected {
				t.Fatalf("status %s, expected %s", item.Status, tc.expected)
			}
			if tc.accepted && (item.ActiveAttempt != nil || item.Session == nil) {
				t.Fatal("accepted attempt was exposed as active")
			}
			if tc.calculation == "ready" && (item.Session.Score == nil || *item.Session.Score != 0) {
				t.Fatal("zero score lost")
			}
			if tc.accepted && tc.calculation != "ready" && item.Session.Score != nil {
				t.Fatal("pending result exposed a score")
			}
			raw, _ := json.Marshal(item)
			for _, forbidden := range []string{"\"questions\"", "\"answers\"", "\"commits\"", "\"question_order\"", "\"question_results\"", "\"is_correct\"", "\"correct_answers\""} {
				if strings.Contains(string(raw), forbidden) {
					t.Fatal("list contains attempt content", forbidden)
				}
			}
		})
	}
}

func TestStudentCatalogFiltersValidateAndUseSelectedPlacement(t *testing.T) {
	division := int64(11)
	a := Actor{OrganizationID: 7, AccountID: 23, DivisionID: &division, Role: "student"}
	q, err := studentCatalogFilters(httptest.NewRequest("GET", "/?q=a%2Bb&direction_id=13&status=completed&date_from=2026-10-01&date_to=2026-10-13&overview=true&include_external=true&limit=5&offset=10", nil), a, division)
	if err != nil {
		t.Fatal(err)
	}
	if q.Limit != 5 || q.Offset != 10 || q.Status != "completed" || !q.Overview || !q.IncludeExternal || q.externalIncluded() {
		t.Fatal("invalid combined list filters", q)
	}
	if q.Online["organization_id"] != a.OrganizationID || q.External["division_id"] != division || q.Online["title"].(bson.Regex).Pattern != `a\+b` {
		t.Fatal("scope or escaped search lost")
	}
	conditions := q.Online["$and"].(bson.A)
	start := conditions[0].(bson.M)["$or"].(bson.A)[1].(bson.M)["placement.settings.start_at"].(bson.M)["$gte"].(time.Time)
	end := conditions[1].(bson.M)["$or"].(bson.A)[1].(bson.M)["placement.settings.end_at"].(bson.M)["$lt"].(time.Time)
	if start.UTC().Format(time.RFC3339) != "2026-09-30T21:00:00Z" || end.UTC().Format(time.RFC3339) != "2026-10-13T21:00:00Z" {
		t.Fatal("Moscow inclusive calendar boundaries are wrong", start, end)
	}
	if q.onlineMatch()["$and"] != nil {
		t.Fatal("date predicates ran before placement selection")
	}
	for _, raw := range []string{"status=nope", "overview=1", "include_external=yes", "date_from=2026-10-14&date_to=2026-10-13", "date_to=2026-02-30", "limit=101", "offset=-1"} {
		if _, err := studentCatalogFilters(httptest.NewRequest("GET", "/?"+raw, nil), a, division); err == nil {
			t.Fatal("invalid filters accepted", raw)
		}
	}
	a.Role = "staff_admin"
	if _, err := studentCatalogFilters(httptest.NewRequest("GET", "/", nil), a, division); err != denied {
		t.Fatal("student read model exposed to staff", err)
	}
}

func TestStudentCatalogHTTPBoundedPageOneAttemptProjectionAndOneDirectionBatch(t *testing.T) {
	division := int64(11)
	a := Actor{OrganizationID: 7, AccountID: 23, DivisionID: &division, Role: "student"}
	now := time.Now().UTC()
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	row := studentCatalogRow{ID: bson.NewObjectID(), OrganizationID: 7, DivisionID: division, AccountID: 23, DirectionID: 13, Title: "Тест", Settings: Settings{Published: true, StartAt: &start, EndAt: &end, TimeLimitMinutes: 30}, QuestionCount: 2, Status: "available"}
	store, commands := cacheMongoStore(t, ratingMongoBatch(CollectionName, bson.M{"_id": int64(13)}), ratingMongoBatch(CollectionName, bson.M{"items": []studentCatalogRow{row}, "totals": bson.A{bson.M{"total": int64(50000)}}}))
	batchCalls := 0
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			OrganizationID int64   `json:"organization_id"`
			DivisionIDs    []int64 `json:"division_ids"`
			DirectionIDs   []int64 `json:"direction_ids"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.OrganizationID != 7 || len(in.DirectionIDs) != 1 || in.DirectionIDs[0] != 13 || len(in.DivisionIDs) != 0 {
			t.Error("reference batch has wrong scope")
		}
		batchCalls++
		reply(w, 200, Envelope{true, References{Organization: Reference{ID: 7, Active: true}, Divisions: []Reference{}, Directions: []Reference{{ID: 13, Name: "Алгебра", Active: true}}}})
	}))
	defer core.Close()
	s := &Server{Store: store, Dependencies: NewDependencies(Config{CoreURL: core.URL})}
	data, code, err := s.studentCatalog(httptest.NewRequest("GET", "/?limit=5&offset=10&status=available", nil), a, division)
	if err != nil || code != 200 {
		t.Fatal("catalog failed", code, err)
	}
	out := data.(StudentCatalogView)
	if out.Total != 50000 || len(out.Items) != 1 || out.Limit != 5 || out.Offset != 10 || batchCalls != 1 || len(*commands) != 2 {
		t.Fatal("list traversed history or lost pagination", out, batchCalls, len(*commands))
	}
	main := (*commands)[1].raw.Lookup("pipeline").Array()
	stages, _ := main.Values()
	lookup, statusIndex, filterIndex, facetIndex := false, -1, -1, -1
	for i, stage := range stages {
		doc := stage.Document()
		if value := doc.Lookup("$lookup"); value.Type != 0 {
			lookup = true
			if value.Document().Lookup("from").StringValue() != AttemptsCollection {
				t.Fatal("wrong attempt source")
			}
			inner, _ := value.Document().Lookup("pipeline").Array().Values()
			filter := inner[0].Document().Lookup("$match").Document()
			if filter.Lookup("organization_id").Int64() != 7 || filter.Lookup("account_id").Int64() != 23 || filter.Lookup("division_id").Int64() != division || filter.Lookup("practice").Boolean() || !filter.Lookup("active").Boolean() {
				t.Fatal("attempt lookup lost identity or active official scope")
			}
			projection := inner[1].Document().Lookup("$project").Document()
			if projection.Lookup("answered_count").Document().Lookup("$size").Type == 0 || projection.Lookup("total_questions").Document().Lookup("$size").Type == 0 {
				t.Fatal("summary copied answers instead of counting")
			}
			for _, key := range []string{"answers", "commits", "question_order", "result", "questions"} {
				if projection.Lookup(key).Type != 0 {
					t.Fatal("attempt projection copied payload", key)
				}
			}
		}
		if value := doc.Lookup("$set"); value.Type != 0 && value.Document().Lookup("status").Type != 0 {
			statusIndex = i
		}
		if value := doc.Lookup("$match"); value.Type != 0 && value.Document().Lookup("status").Type != 0 {
			filterIndex = i
		}
		if value := doc.Lookup("$facet"); value.Type != 0 {
			facetIndex = i
		}
	}
	if !lookup || statusIndex < 0 || filterIndex <= statusIndex || facetIndex <= filterIndex {
		t.Fatal("pagination or total applied before status filtering", statusIndex, filterIndex, facetIndex)
	}
}

func TestStudentCatalogUnfilteredPageReadsAttemptsOnlyAfterLimit(t *testing.T) {
	division := int64(11)
	a := Actor{OrganizationID: 7, AccountID: 23, DivisionID: &division, Role: "student"}
	q, err := studentCatalogFilters(httptest.NewRequest("GET", "/?limit=5&offset=10&include_external=true", nil), a, division)
	if err != nil {
		t.Fatal(err)
	}
	pipeline := studentCatalogPipeline(q, a, division, nil, time.Now())
	for _, stage := range pipeline {
		if stage[0].Key == "$lookup" {
			t.Fatal("unfiltered list reads every historical attempt before paging")
		}
	}
	facet := pipeline[len(pipeline)-1][0].Value.(bson.M)
	page := facet["items"].(mongo.Pipeline)
	limitIndex, lookupIndex := -1, -1
	for index, stage := range page {
		if stage[0].Key == "$limit" {
			limitIndex = index
		}
		if stage[0].Key == "$lookup" {
			lookupIndex = index
		}
	}
	if limitIndex < 0 || lookupIndex <= limitIndex {
		t.Fatal("attempt summary lookup was not bounded to the selected page")
	}
	if totals := facet["totals"].(mongo.Pipeline); len(totals) != 1 || totals[0][0].Key != "$count" {
		t.Fatal("unfiltered total depends on historical attempts")
	}
}

func TestStudentCatalogUnfilteredPageHasOneMongoQueryAndOnlyPageDirectionReferences(t *testing.T) {
	division := int64(11)
	a := Actor{OrganizationID: 7, AccountID: 23, DivisionID: &division, Role: "student"}
	now := time.Now().UTC()
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	row := studentCatalogRow{ID: bson.NewObjectID(), OrganizationID: 7, DivisionID: division, AccountID: 23, DirectionID: 13, Settings: Settings{Published: true, StartAt: &start, EndAt: &end, TimeLimitMinutes: 30}, QuestionCount: 1, Status: "available"}
	store, commands := cacheMongoStore(t, ratingMongoBatch(CollectionName, bson.M{"items": []studentCatalogRow{row}, "totals": bson.A{bson.M{"total": int64(50000)}}}))
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			DirectionIDs []int64 `json:"direction_ids"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || len(in.DirectionIDs) != 1 || in.DirectionIDs[0] != 13 {
			t.Error("references include unselected pages")
		}
		// A recently archived direction must remain unavailable even in the fast
		// path where status was not used as a Mongo filter.
		reply(w, 200, Envelope{true, References{Organization: Reference{ID: 7, Active: true}, Divisions: []Reference{}, Directions: []Reference{{ID: 13, Name: "Алгебра", Active: false}}}})
	}))
	defer core.Close()
	s := &Server{Store: store, Dependencies: NewDependencies(Config{CoreURL: core.URL})}
	result, code, err := s.studentCatalog(httptest.NewRequest("GET", "/?limit=5&offset=10", nil), a, division)
	if err != nil || code != 200 {
		t.Fatal("fast list failed", code, err)
	}
	out := result.(StudentCatalogView)
	if len(*commands) != 1 || out.Total != 50000 || len(out.Items) != 1 || out.Items[0].Status != "missed" || out.Items[0].Access.OfficialAccessAllowed {
		t.Fatal("fast list scanned history or trusted stale availability")
	}
}

// Opt in against the fixed loopback Dev Mongo. Fixtures have an isolated org
// and cleanup uses only generated IDs; no existing accounts/tests are changed.
func TestStudentCatalogMongoIntegration(t *testing.T) {
	uri := os.Getenv("CPM_CATALOG_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("set CPM_CATALOG_TEST_MONGO_URI for real Mongo aggregation checks")
	}
	u, err := url.Parse(uri)
	if err != nil || u.Hostname() != "127.0.0.1" || u.Port() != "27018" || u.Path != "/cpm_dev" {
		t.Fatal("integration requires the dedicated loopback Dev Mongo")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal("Mongo connect failed")
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
	if err := client.Ping(ctx, nil); err != nil {
		t.Fatal("Mongo unavailable")
	}
	store := &Store{Collection: client.Database("cpm_dev").Collection(CollectionName)}
	org, division, account := int64(9000000000+time.Now().UnixNano()%1000000000), int64(9191), int64(9292)
	a := Actor{OrganizationID: org, AccountID: account, DivisionID: &division, Role: "student"}
	now := time.Now().UTC().Truncate(time.Millisecond)
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	docIDs, attemptIDs, externalIDs := []bson.ObjectID{}, []bson.ObjectID{}, []bson.ObjectID{}
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 15*time.Second)
		defer done()
		for _, entry := range []struct {
			collection *mongo.Collection
			ids        []bson.ObjectID
		}{{store.Collection, docIDs}, {store.Attempts(), attemptIDs}, {store.External(), externalIDs}} {
			if len(entry.ids) > 0 {
				if _, err := entry.collection.DeleteMany(cleanupCtx, bson.M{"_id": bson.M{"$in": entry.ids}, "organization_id": org}); err != nil {
					t.Error("fixture cleanup failed")
				}
			}
		}
	})
	byStatus := map[string]bson.ObjectID{}
	for index, status := range []string{"available", "running", "pending_upload", "upload_window_closed", "checking", "completed", "calculation_failed", "upcoming", "missed", "inactive_direction"} {
		doc := Document{ID: bson.NewObjectID(), OrganizationID: org, DirectionID: 13, Title: status, Revision: 1, CreatedAt: now.Add(-time.Duration(index) * time.Second), Questions: []Question{{QuestionID: 1, Text: strings.Repeat("large question ", 1000)}}, Placements: []Placement{{DivisionID: division, Settings: Settings{Published: true, StartAt: &start, EndAt: &end, TimeLimitMinutes: 30, AnswersVisible: true}}, {DivisionID: division + 1, Settings: Settings{Published: true, StartAt: &start, EndAt: &end, TimeLimitMinutes: 30}}}}
		if status == "upcoming" {
			future := now.Add(time.Hour)
			doc.Placements[0].Settings.StartAt = &future
		}
		if status == "missed" {
			past := now.Add(-time.Minute)
			doc.Placements[0].Settings.EndAt = &past
		}
		if status == "inactive_direction" {
			doc.DirectionID = 14
		}
		docIDs = append(docIDs, doc.ID)
		if _, err := store.Collection.InsertOne(ctx, doc); err != nil {
			t.Fatal("catalog fixture insert failed")
		}
		byStatus[status] = doc.ID
		if status == "available" || status == "upcoming" || status == "missed" {
			continue
		}
		attempt := Attempt{ID: bson.NewObjectID(), OrganizationID: org, DivisionID: division, AccountID: account, TestID: doc.ID, Active: true, Status: "started", AnswerDeadline: now.Add(time.Minute), UploadDeadline: now.Add(time.Hour), QuestionOrder: []int64{1}, Answers: []SubmittedAnswer{{QuestionID: 1, Value: strings.Repeat("answer ", 1000)}}}
		if status == "pending_upload" || status == "upload_window_closed" {
			attempt.AnswerDeadline = now.Add(-time.Minute)
		}
		if status == "upload_window_closed" {
			attempt.UploadDeadline = now.Add(-time.Second)
		}
		if status == "checking" || status == "completed" || status == "calculation_failed" {
			id := bson.NewObjectID()
			attempt.Status, attempt.SubmissionID, attempt.AcceptedAt = "accepted", &id, &now
			attempt.CalculationStatus = map[string]string{"checking": "queued", "completed": "ready", "calculation_failed": "failed"}[status]
			attempt.Result = &GradeResult{Score: 0}
		}
		attemptIDs = append(attemptIDs, attempt.ID)
		if _, err := store.Attempts().InsertOne(ctx, attempt); err != nil {
			t.Fatal("attempt fixture insert failed")
		}
	}
	// A different student's active attempt must never supply the summary/score.
	other := Attempt{ID: bson.NewObjectID(), OrganizationID: org, DivisionID: division, AccountID: account + 1, TestID: byStatus["available"], Active: true, Status: "accepted", Result: &GradeResult{Score: 100}}
	attemptIDs = append(attemptIDs, other.ID)
	if _, err := store.Attempts().InsertOne(ctx, other); err != nil {
		t.Fatal("foreign account fixture insert failed")
	}
	zero := 0.0
	for i := 0; i < 2; i++ {
		external := ExternalTestDocument{ID: bson.NewObjectID(), OrganizationID: org, DivisionID: division, DirectionID: 13, Name: "external", Date: now.Format(time.DateOnly), Results: []ExternalResult{{ID: bson.NewObjectID(), AccountID: account + 1, Score: &zero}}}
		if i == 0 {
			external.Results = append(external.Results, ExternalResult{ID: bson.NewObjectID(), AccountID: account, Score: &zero})
		}
		externalIDs = append(externalIDs, external.ID)
		if _, err := store.External().InsertOne(ctx, external); err != nil {
			t.Fatal("external fixture insert failed")
		}
	}
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			DirectionIDs []int64 `json:"direction_ids"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			t.Error("invalid reference body")
		}
		refs := References{Organization: Reference{ID: org, Active: true}, Divisions: []Reference{}, Directions: []Reference{}}
		for _, id := range in.DirectionIDs {
			refs.Directions = append(refs.Directions, Reference{ID: id, Name: "Алгебра", Active: id != 14})
		}
		reply(w, 200, Envelope{true, refs})
	}))
	defer core.Close()
	s := &Server{Store: store, Dependencies: NewDependencies(Config{CoreURL: core.URL})}
	load := func(query string) StudentCatalogView {
		t.Helper()
		result, code, err := s.studentCatalog(httptest.NewRequest("GET", "/?"+query, nil), a, division)
		if err != nil || code != 200 {
			t.Fatal("real Mongo catalog failed", code, err)
		}
		out := result.(StudentCatalogView)
		raw, _ := json.Marshal(out)
		if len(raw) > 20000 || strings.Contains(string(raw), "large question") || strings.Contains(string(raw), "answer answer") {
			t.Fatal("catalog downloaded attempt content")
		}
		return out
	}
	first, second := load("include_external=true&limit=5"), load("include_external=true&limit=5&offset=5")
	if first.Total != 12 || len(first.Items) != 5 || second.Total != 12 || len(second.Items) != 5 {
		t.Fatal("combined total/page size wrong", first.Total, second.Total)
	}
	seen := map[bson.ObjectID]bool{}
	for _, item := range append(first.Items, second.Items...) {
		if seen[item.ID] {
			t.Fatal("unstable pagination duplicated a row")
		}
		seen[item.ID] = true
		if item.ID == byStatus["inactive_direction"] && item.Status != "missed" {
			t.Fatal("fast page hid live direction archive")
		}
	}
	for _, status := range []string{"available", "running", "pending_upload", "upload_window_closed", "checking", "completed", "calculation_failed", "upcoming"} {
		page := load("include_external=true&status=" + status)
		if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != byStatus[status] || page.Items[0].Status != status {
			t.Fatal("status filter ran after pagination or mismatched access", status, page.Total, page.Items)
		}
		if status == "completed" && (page.Items[0].Session.Score == nil || *page.Items[0].Session.Score != 0) {
			t.Fatal("ready zero score missing")
		}
	}
	if page := load("include_external=true&status=missed"); page.Total != 2 {
		t.Fatal("inactive direction still allowed a running attempt")
	}
	if page := load("include_external=true&overview=true"); page.Total != 8 {
		t.Fatal("overview eligibility or external result scope wrong", page.Total)
	}
	if page := load("include_external=true&status=external&overview=true"); page.Total != 1 || !page.Items[0].HasResult || page.Items[0].Rate == nil || *page.Items[0].Rate != 0 {
		t.Fatal("external result belonging to another student leaked")
	}
	if page := load("include_external=false&status=external"); page.Total != 0 || len(page.Items) != 0 {
		t.Fatal("disabled external domain still visible")
	}
	if page := load("q=completed&direction_id=13"); page.Total != 1 {
		t.Fatal("search/direction filtering wrong")
	}
	if page := load("direction_id=14&status=running"); page.Total != 0 {
		t.Fatal("inactive direction was ignored by server filtering")
	}
	// Start-date filtering must only inspect this cabinet's placement.
	if page := load("q=upcoming&date_from=" + now.Add(30*time.Minute).In(time.FixedZone("MSK", 10800)).Format(time.DateOnly)); page.Total != 1 {
		t.Fatal("date filtering matched the wrong placement")
	}
}
