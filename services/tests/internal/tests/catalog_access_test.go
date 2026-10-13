package tests

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestCatalogAccessAttemptAndTimeBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	doc := cacheDocument()
	p := Placement{DivisionID: 11, Settings: Settings{Published: true, AnswersVisible: true, StartAt: &start, EndAt: &end, TimeLimitMinutes: 60}}
	submissionID := bson.NewObjectID()
	for _, tc := range []struct {
		name, role, status, calculation, officialReason, reviewReason string
		answerOffset, uploadOffset                                    time.Duration
		archived, inactive, unpublished, hidden, empty, ended         bool
		submitted, official, review, practice                         bool
	}{
		{name: "new student", role: "student", official: true, officialReason: "allowed", reviewReason: "no_submission"},
		{name: "resume at answer deadline", role: "student", status: "started", uploadOffset: time.Hour, official: true, officialReason: "resume_attempt", reviewReason: "no_submission"},
		{name: "offline upload window", role: "student", status: "started", answerOffset: -time.Nanosecond, uploadOffset: time.Hour, officialReason: "pending_upload", reviewReason: "no_submission"},
		{name: "upload boundary", role: "student", status: "started", answerOffset: -time.Hour, officialReason: "pending_upload", reviewReason: "no_submission"},
		{name: "expired upload", role: "student", status: "started", answerOffset: -time.Hour, uploadOffset: -time.Nanosecond, ended: true, officialReason: "upload_deadline_passed", reviewReason: "no_submission", practice: true},
		{name: "expired upload while schedule open", role: "student", status: "started", answerOffset: -time.Hour, uploadOffset: -time.Nanosecond, officialReason: "upload_deadline_passed", reviewReason: "no_submission"},
		{name: "queued calculation", role: "student", status: "accepted", calculation: "queued", submitted: true, officialReason: "already_submitted", reviewReason: "calculation_pending", practice: true},
		{name: "ready calculation", role: "student", status: "accepted", calculation: "ready", submitted: true, officialReason: "already_submitted", reviewReason: "allowed", review: true, practice: true},
		{name: "hidden review", role: "student", status: "accepted", calculation: "ready", submitted: true, hidden: true, officialReason: "already_submitted", reviewReason: "review_hidden"},
		{name: "archive prevents resume", role: "student", status: "started", uploadOffset: time.Hour, answerOffset: time.Hour, archived: true, officialReason: "test_archived", reviewReason: "test_archived"},
		{name: "direction prevents resume", role: "student", status: "started", uploadOffset: time.Hour, answerOffset: time.Hour, inactive: true, officialReason: "direction_archived", reviewReason: "direction_archived"},
		{name: "unpublished prevents resume", role: "student", status: "started", uploadOffset: time.Hour, unpublished: true, officialReason: "unpublished", reviewReason: "unpublished"},
		{name: "no questions", role: "student", empty: true, officialReason: "test_has_no_questions", reviewReason: "no_submission"},
		{name: "practice after schedule", role: "student", ended: true, officialReason: "test_ended", reviewReason: "no_submission", practice: true},
		{name: "admin settings", role: "staff_admin", official: true, review: true, officialReason: "allowed", reviewReason: "allowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, placement := doc, p
			d.Archived = tc.archived
			if tc.empty {
				d.Questions = nil
			}
			placement.Settings.Published, placement.Settings.AnswersVisible = !tc.unpublished, !tc.hidden
			if tc.ended {
				past := now.Add(-time.Nanosecond)
				placement.Settings.EndAt = &past
			}
			var state *Attempt
			if tc.status != "" {
				state = &Attempt{ID: bson.NewObjectID(), Status: tc.status, CalculationStatus: tc.calculation, AnswerDeadline: now.Add(tc.answerOffset), UploadDeadline: now.Add(tc.uploadOffset)}
				if tc.submitted {
					state.SubmissionID = &submissionID
				}
			}
			out := accessState(d, placement, !tc.inactive, now, tc.role, state)
			if out.OfficialAccessAllowed != tc.official || out.ReviewAccessAllowed != tc.review || out.PracticeAccessAllowed != tc.practice || out.OfficialReason != tc.officialReason || out.ReviewReason != tc.reviewReason {
				t.Fatalf("unexpected access: %+v", out)
			}
			if !out.ContentChecked || !out.AttemptStateChecked || out.Scope != "test_content_and_attempt" || !out.ServerTime.Equal(now) {
				t.Fatal("access did not declare full checked state", out)
			}
		})
	}
	// An already started attempt keeps its own deadlines after a schedule edit.
	futureStart, futureEnd := now.Add(time.Hour), now.Add(2*time.Hour)
	p.Settings.StartAt, p.Settings.EndAt = &futureStart, &futureEnd
	out := accessState(doc, p, true, now, "student", &Attempt{Status: "started", AnswerDeadline: now.Add(time.Minute), UploadDeadline: now.Add(time.Hour)})
	if !out.OfficialAccessAllowed || out.OfficialReason != "resume_attempt" {
		t.Fatal("schedule edit discarded pinned attempt deadlines", out)
	}
}

type catalogLiveFixture struct {
	mu                                           sync.Mutex
	actor                                        Actor
	revoked, inactiveDivision, inactiveDirection bool
	ownerName, directionName                     string
	coreCalls, directionCalls, authCalls         int
}

func TestCatalogAccessHTTPBatchesLiveAttemptsReferencesAndRevocation(t *testing.T) {
	d1, d2 := cacheDocument(), cacheDocument()
	d2.DirectionID = 14
	now := time.Now().UTC().Truncate(time.Millisecond)
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	settings := Settings{Published: true, AnswersVisible: true, StartAt: &start, EndAt: &end, TimeLimitMinutes: 60}
	d1.Placements[0].Settings, d2.Placements[0].Settings = settings, settings
	division := int64(11)
	fixture := &catalogLiveFixture{actor: Actor{AccountID: 23, OrganizationID: 7, DivisionID: &division, Role: "student"}, ownerName: "Подразделение", directionName: "Направление"}
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		fixture.authCalls++
		if fixture.revoked {
			w.WriteHeader(401)
			return
		}
		if r.Header.Get("Authorization") != "Bearer live" {
			t.Error("unexpected auth token")
		}
		reply(w, 200, struct {
			Status bool  `json:"status"`
			Active bool  `json:"active"`
			User   Actor `json:"user"`
		}{true, true, fixture.actor})
	}))
	defer auth.Close()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			OrganizationID int64   `json:"organization_id"`
			DivisionIDs    []int64 `json:"division_ids"`
			DirectionID    *int64  `json:"direction_id"`
			DirectionIDs   []int64 `json:"direction_ids"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.OrganizationID != 7 {
			t.Error("core request scope mismatch")
			w.WriteHeader(503)
			return
		}
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		fixture.coreCalls++
		refs := References{Organization: Reference{ID: 7, Active: true}, Divisions: []Reference{}}
		for _, id := range input.DivisionIDs {
			refs.Divisions = append(refs.Divisions, Reference{ID: id, Name: fixture.ownerName, Active: !fixture.inactiveDivision})
		}
		if input.DirectionID != nil {
			fixture.directionCalls++
			refs.Direction = &Reference{ID: *input.DirectionID, Name: fixture.directionName, Active: !fixture.inactiveDirection}
		}
		if len(input.DirectionIDs) > 0 {
			fixture.directionCalls++
			if len(input.DirectionIDs) != 2 || input.DirectionIDs[0] != 13 || input.DirectionIDs[1] != 14 {
				t.Error("catalog directions were not batched")
			}
			for _, id := range input.DirectionIDs {
				refs.Directions = append(refs.Directions, Reference{ID: id, Name: fixture.directionName, Active: !fixture.inactiveDirection})
			}
		}
		reply(w, 200, Envelope{true, refs})
	}))
	defer core.Close()
	accepted := Attempt{ID: bson.NewObjectID(), TestID: d1.ID, OrganizationID: 7, DivisionID: 11, AccountID: 23, Active: true, Status: "accepted", CalculationStatus: "queued"}
	submitted := bson.NewObjectID()
	accepted.SubmissionID = &submitted
	uploading := Attempt{ID: bson.NewObjectID(), TestID: d2.ID, OrganizationID: 7, DivisionID: 11, AccountID: 23, Active: true, Status: "started", AnswerDeadline: now.Add(-time.Hour), UploadDeadline: now.Add(time.Hour)}
	ready := accepted
	ready.CalculationStatus = "ready"
	count := ratingMongoBatch(CollectionName, bson.M{"n": int64(2)})
	store, commands := cacheMongoStore(t,
		count, ratingMongoBatch(CollectionName, d1, d2), ratingMongoBatch(CollectionName, d1, d2), ratingMongoBatch(AttemptsCollection),
		count, ratingMongoBatch(CollectionName, d1, d2), ratingMongoBatch(AttemptsCollection, accepted, uploading),
		count, ratingMongoBatch(CollectionName, d1, d2), ratingMongoBatch(AttemptsCollection, ready),
		count, ratingMongoBatch(CollectionName, d1, d2), ratingMongoBatch(AttemptsCollection),
	)
	s := &Server{Store: store, Dependencies: NewDependencies(Config{AuthURL: auth.URL, CoreURL: core.URL})}
	mux := http.NewServeMux()
	spec := s.catalogAccessSpec()
	s.route(mux, spec.Pattern, spec.Handle)
	request := func(path string, expected int) CatalogWithAccessView {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer live")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != expected {
			t.Fatalf("HTTP %d expected %d: %s", w.Code, expected, w.Body.String())
		}
		if expected != 200 {
			return CatalogWithAccessView{}
		}
		var out struct {
			Status bool                  `json:"status"`
			Data   CatalogWithAccessView `json:"data"`
		}
		if json.Unmarshal(w.Body.Bytes(), &out) != nil || !out.Status || out.Data.Total != 2 || len(out.Data.Items) != 2 || out.Data.Limit != 20 || out.Data.Offset != 0 {
			t.Fatal("invalid paginated response", w.Body.String())
		}
		for _, forbidden := range []string{"\"questions\"", "\"answers\"", "\"is_correct\"", "\"correct_answers\"", "\"commits\"", "\"result\"", "\"test\""} {
			if bytes.Contains(w.Body.Bytes(), []byte(forbidden)) {
				t.Fatal("catalog leaked sensitive content or nested test", forbidden, w.Body.String())
			}
		}
		if !out.Data.Items[0].Access.ServerTime.Equal(out.Data.Items[1].Access.ServerTime) {
			t.Fatal("catalog access uses different clock snapshots")
		}
		return out.Data
	}
	path := "/v1/tests/divisions/11/catalog-with-access"
	first := request(path, 200)
	if !first.Items[0].Access.OfficialAccessAllowed || first.Items[0].Access.ReviewAccessAllowed {
		t.Fatal("new student availability is wrong", first.Items)
	}
	fixture.mu.Lock()
	fixture.ownerName, fixture.directionName = "Переименованное подразделение", "Переименованное направление"
	fixture.mu.Unlock()
	second := request(path, 200)
	if second.Items[0].OwnerDivisionName != "Переименованное подразделение" || second.Items[0].DirectionName != "Переименованное направление" || second.Items[0].Access.ReviewReason != "calculation_pending" || second.Items[1].Access.OfficialReason != "pending_upload" {
		t.Fatal("warm catalog hid fresh references or attempt state", second.Items)
	}
	third := request(path, 200)
	if !third.Items[0].Access.ReviewAccessAllowed || third.Items[0].Access.CalculationStatus != "ready" {
		t.Fatal("warm catalog hid new calculated result status", third.Items)
	}
	fixture.mu.Lock()
	fixture.inactiveDirection = true
	fixture.mu.Unlock()
	fourth := request(path, 200)
	if fourth.Items[0].DirectionActive || fourth.Items[0].Access.OfficialAccessAllowed || fourth.Items[0].Access.OfficialReason != "direction_archived" {
		t.Fatal("cached catalog hid direction archive", fourth.Items)
	}
	request("/v1/tests/divisions/12/catalog-with-access", 403)
	fixture.mu.Lock()
	fixture.inactiveDivision = true
	fixture.mu.Unlock()
	request(path, 404)
	fixture.mu.Lock()
	fixture.inactiveDivision = false
	fixture.actor.Role = "staff_admin"
	fixture.actor.Permissions = map[string]Permission{"tests": {View: false}}
	fixture.mu.Unlock()
	request(path, 403)
	fixture.mu.Lock()
	fixture.revoked = true
	fixture.mu.Unlock()
	request(path, 401)
	fixture.mu.Lock()
	if fixture.authCalls != 8 || fixture.directionCalls != 4 || fixture.coreCalls != 10 {
		t.Fatal("references/authorization were cached or duplicated per row", fixture.authCalls, fixture.directionCalls, fixture.coreCalls)
	}
	fixture.mu.Unlock()
	attemptReads, payloadReads := 0, 0
	for _, command := range *commands {
		if command.name != "find" {
			continue
		}
		filter := command.raw.Lookup("filter").Document()
		if filter.Lookup("organization_id").Int64() != 7 {
			t.Fatal("Mongo query lost organization scope", command.raw)
		}
		if command.raw.Lookup("find").StringValue() == AttemptsCollection {
			attemptReads++
			ids, err := filter.Lookup("test_id").Document().Lookup("$in").Array().Values()
			if err != nil || filter.Lookup("division_id").Int64() != 11 || filter.Lookup("account_id").Int64() != 23 || filter.Lookup("practice").Boolean() || !filter.Lookup("active").Boolean() || len(ids) != 2 {
				t.Fatal("attempt batch lost current-account/page scope", command.raw)
			}
			projection := command.raw.Lookup("projection").Document()
			for _, forbidden := range []string{"answers", "commits", "result", "calculation_history"} {
				if projection.Lookup(forbidden).Type != 0 {
					t.Fatal("attempt projection includes unnecessary data", command.raw)
				}
			}
		} else if command.raw.Lookup("projection").Type == 0 {
			payloadReads++
		}
	}
	if attemptReads != 4 || payloadReads != 1 || len(*commands) != 13 {
		t.Fatal("request batching/cache failed or denied requests read Mongo", attemptReads, payloadReads, len(*commands))
	}
}

func TestCatalogAccessRejectsForeignOrDuplicateAttempt(t *testing.T) {
	division := int64(11)
	a := Actor{OrganizationID: 7, AccountID: 23, DivisionID: &division, Role: "student"}
	id := bson.NewObjectID()
	good := Attempt{ID: bson.NewObjectID(), OrganizationID: 7, DivisionID: 11, AccountID: 23, TestID: id, Active: true}
	for _, field := range []string{"organization", "division", "account", "test", "practice", "inactive", "duplicate"} {
		t.Run(field, func(t *testing.T) {
			badState := good
			switch field {
			case "organization":
				badState.OrganizationID++
			case "division":
				badState.DivisionID++
			case "account":
				badState.AccountID++
			case "test":
				badState.TestID = bson.NewObjectID()
			case "practice":
				badState.Practice = true
			case "inactive":
				badState.Active = false
			}
			states := []any{badState}
			if field == "duplicate" {
				states = append(states, good)
			}
			store, _ := cacheMongoStore(t, ratingMongoBatch(AttemptsCollection, states...))
			_, e := store.catalogAttempts(httptest.NewRequest("GET", "/", nil), a, 11, []bson.ObjectID{id})
			if !errors.Is(e, unavailable) {
				t.Fatal("invalid attempt batch accepted", e)
			}
		})
	}
}

func TestCatalogAccessFiltersMatchExistingCatalog(t *testing.T) {
	d := int64(11)
	a := Actor{OrganizationID: 7, AccountID: 23, DivisionID: &d, Role: "student"}
	r := httptest.NewRequest("GET", "/?q=a%2Bb&direction_id=13&owner_division_id=12&relation=attached&limit=3&offset=6", nil)
	filter, limit, offset, e := catalogFilter(r, a, 11, false)
	if e != nil || limit != 3 || offset != 6 || filter["organization_id"] != int64(7) || filter["placements.division_id"] != int64(11) || filter["owner_division_id"] != int64(12) || filter["is_archived"] != false || filter["title"].(bson.Regex).Pattern != `a\+b` {
		t.Fatal("catalog filters changed", filter, limit, offset, e)
	}
	placements := filter["placements"].(bson.M)["$elemMatch"].(bson.M)
	if placements["division_id"] != int64(11) || placements["settings.published"] != true || len(filter["$and"].(bson.A)) != 1 {
		t.Fatal("student publication/attached filter lost", filter)
	}
	for _, query := range []string{"is_archived=true", "limit=101", "offset=-1", "relation=unknown"} {
		if _, _, _, e := catalogFilter(httptest.NewRequest("GET", "/?"+query, nil), a, 11, false); e == nil {
			t.Fatal("invalid/forbidden query accepted", query)
		}
	}
}

func TestCatalogAccessRouteSchema(t *testing.T) {
	paths := openAPI()["paths"].(map[string]any)
	path, exists := paths["/v1/tests/divisions/{division}/catalog-with-access"]
	if !exists {
		t.Fatal("bulk route absent from OpenAPI")
	}
	raw, _ := json.Marshal(path)
	for _, field := range []string{"access", "official_access_allowed", "attempt_id", "direction_id", "limit", "offset", "relation"} {
		if !strings.Contains(string(raw), `"`+field+`"`) {
			t.Fatal("bulk schema omitted field/query", field)
		}
	}
}
