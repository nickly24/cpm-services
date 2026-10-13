package tests

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net/http"
	"reflect"
	"sort"
	"time"
	_ "time/tzdata"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	ratingsMaxTests    = 1000
	ratingsMaxAccounts = 2000
	ratingsMaxResults  = 100000
	ratingsMaxBytes    = 16 << 20
)

// AsOf is the common rating request time, not a cutoff for test selection.
// Legacy ratings select tests by their Moscow start date, including future starts.
type RatingSnapshotRequest struct {
	OrganizationID int64  `json:"organization_id"`
	DivisionID     int64  `json:"division_id"`
	DateFrom       string `json:"date_from"`
	DateTo         string `json:"date_to"`
	AsOf           string `json:"as_of"`
}

type RatingTestResult struct {
	AccountID           int64    `json:"account_id"`
	SubmissionID        string   `json:"submission_id"`
	CalculationRevision int64    `json:"calculation_revision"`
	CalculationStatus   string   `json:"calculation_status"`
	Score               *float64 `json:"score"`
}

type RatingTest struct {
	TestID         string             `json:"test_id"`
	Title          string             `json:"title"`
	DirectionID    int64              `json:"direction_id"`
	DirectionName  string             `json:"direction_name"`
	StartAt        time.Time          `json:"start_at"`
	Published      bool               `json:"published"`
	Archived       bool               `json:"is_archived"`
	SourceRevision int64              `json:"source_revision,omitempty"`
	Results        []RatingTestResult `json:"results"`
}

type RatingSnapshot struct {
	Source                string               `json:"source"`
	OrganizationID        int64                `json:"organization_id"`
	DivisionID            int64                `json:"division_id"`
	Tests                 []RatingTest         `json:"tests"`
	ExternalTests         []RatingTest         `json:"external_tests"`
	ExternalSourceVersion string               `json:"external_source_version"`
	ExternalDomain        ExternalDomainStatus `json:"external_domain"`
	InputHash             string               `json:"input_hash"`
}

// A successful external read proves owner initialization before this status is
// included in a rating snapshot. It is scoped metadata, not a fallback flag.
type ExternalDomainStatus struct {
	SchemaVersion  int    `json:"schema_version"`
	Status         string `json:"status"`
	OrganizationID int64  `json:"organization_id"`
	DivisionID     int64  `json:"division_id"`
}

func makeExternalDomainStatus(organizationID, divisionID int64) ExternalDomainStatus {
	return ExternalDomainStatus{SchemaVersion: 1, Status: "ready", OrganizationID: organizationID, DivisionID: divisionID}
}

// External catalogue/results are read together from the domain owner. A result
// with a nil score is a recorded unknown value, unlike an absent result row.
// Both contribute zero to the legacy rating, but keep distinct source identity.
type RatingExternalResult struct {
	AccountID int64    `json:"account_id"`
	ResultID  string   `json:"result_id"`
	Revision  int64    `json:"revision"`
	Score     *float64 `json:"score"`
}

type RatingExternalTest struct {
	OrganizationID int64                  `json:"organization_id"`
	DivisionID     int64                  `json:"division_id"`
	TestID         string                 `json:"test_id"`
	Title          string                 `json:"title"`
	DirectionID    int64                  `json:"direction_id"`
	Date           string                 `json:"date"`
	Revision       int64                  `json:"revision"`
	Results        []RatingExternalResult `json:"results"`
}

func ratingDateBounds(in RatingSnapshotRequest) (time.Time, time.Time, error) {
	moscow, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		return time.Time{}, time.Time{}, unavailable
	}
	from, e1 := time.ParseInLocation(time.DateOnly, in.DateFrom, moscow)
	to, e2 := time.ParseInLocation(time.DateOnly, in.DateTo, moscow)
	asOf, e3 := time.Parse(time.RFC3339Nano, in.AsOf)
	_, offset := asOf.Zone()
	if in.OrganizationID < 1 || in.DivisionID < 1 || e1 != nil || e2 != nil || e3 != nil || offset != 0 || from.After(to) {
		return time.Time{}, time.Time{}, bad("invalid_rating_snapshot_request")
	}
	return from.UTC(), to.AddDate(0, 0, 1).UTC(), nil
}

func (s *Server) ratingSnapshot(r *http.Request) (any, int, error) {
	var in RatingSnapshotRequest
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	from, until, e := ratingDateBounds(in)
	if e != nil {
		return nil, 0, e
	}
	out, e := stableRatingSnapshot(r.Context(), in, from, until, mongoRatingSource{s.Store, s.Dependencies})
	return out, http.StatusOK, e
}

// The interface keeps the optimistic read protocol testable without database
// writes. It does not claim a transaction across Mongo and core.
type ratingSource interface {
	catalog(context.Context, RatingSnapshotRequest, time.Time, time.Time) ([]Document, error)
	submissions(context.Context, RatingSnapshotRequest, []bson.ObjectID) ([]Attempt, error)
	references(context.Context, int64, []int64, *int64) (References, error)
	externalCatalog(context.Context, RatingSnapshotRequest, time.Time, time.Time) ([]RatingExternalTest, error)
}

type mongoRatingSource struct {
	store *Store
	deps  *Dependencies
}

func (s mongoRatingSource) references(ctx context.Context, org int64, divisions []int64, direction *int64) (References, error) {
	return s.deps.References(ctx, org, divisions, direction)
}

func ratingCatalogFilter(in RatingSnapshotRequest, from, until time.Time) bson.M {
	return bson.M{
		"organization_id": in.OrganizationID,
		"is_archived":     bson.M{"$ne": true},
		"placements": bson.M{"$elemMatch": bson.M{
			"division_id": in.DivisionID, "settings.published": true,
			"settings.start_at": bson.M{"$gte": from, "$lt": until},
		}},
	}
}

func (s mongoRatingSource) catalog(ctx context.Context, in RatingSnapshotRequest, from, until time.Time) ([]Document, error) {
	cursor, e := s.store.Collection.Find(ctx, ratingCatalogFilter(in, from, until), options.Find().
		SetLimit(ratingsMaxTests+1).SetSort(bson.D{{Key: "_id", Value: 1}}).
		SetProjection(bson.M{"_id": 1, "organization_id": 1, "direction_id": 1, "title": 1, "is_archived": 1,
			"placements": bson.M{"$elemMatch": bson.M{"division_id": in.DivisionID}}}))
	if e != nil {
		return nil, e
	}
	defer cursor.Close(ctx)
	out := []Document{}
	size := 0
	for cursor.Next(ctx) {
		size += len(cursor.Current)
		if len(out) == ratingsMaxTests || size > ratingsMaxBytes {
			return nil, ratingSnapshotTooLarge()
		}
		var d Document
		if e = cursor.Decode(&d); e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, cursor.Err()
}

func ratingSubmissionFilter(in RatingSnapshotRequest, ids []bson.ObjectID) bson.M {
	return bson.M{"organization_id": in.OrganizationID, "division_id": in.DivisionID,
		"test_id": bson.M{"$in": ids}, "active": true, "practice": false, "status": "accepted"}
}

func (s mongoRatingSource) submissions(ctx context.Context, in RatingSnapshotRequest, ids []bson.ObjectID) ([]Attempt, error) {
	if len(ids) == 0 {
		return []Attempt{}, nil
	}
	cursor, e := s.store.Attempts().Find(ctx, ratingSubmissionFilter(in, ids), options.Find().
		SetLimit(ratingsMaxResults+1).SetSort(bson.D{{Key: "test_id", Value: 1}, {Key: "account_id", Value: 1}}).
		SetProjection(bson.M{"test_id": 1, "organization_id": 1, "division_id": 1, "account_id": 1,
			"active": 1, "practice": 1, "status": 1, "submission_id": 1,
			"calculation_revision": 1, "calculation_status": 1, "result.score": 1}))
	if e != nil {
		return nil, e
	}
	defer cursor.Close(ctx)
	out := []Attempt{}
	size := 0
	for cursor.Next(ctx) {
		size += len(cursor.Current)
		if len(out) == ratingsMaxResults || size > ratingsMaxBytes {
			return nil, ratingSnapshotTooLarge()
		}
		var a Attempt
		if e = cursor.Decode(&a); e != nil {
			return nil, e
		}
		if a.CalculationStatus == "ready" && !cursor.Current.Lookup("result", "score").IsNumber() {
			// Decoding a missing numeric field into int would otherwise invent 0.
			return nil, conflict("rating_source_incomplete")
		}
		out = append(out, a)
	}
	return out, cursor.Err()
}

func ratingSnapshotTooLarge() error {
	return &APIError{http.StatusRequestEntityTooLarge, "rating_snapshot_too_large"}
}

func ratingSnapshotOpenAPI() map[string]any {
	input := schema(reflect.TypeOf(RatingSnapshotRequest{}))
	input["required"] = []string{"organization_id", "division_id", "date_from", "date_to", "as_of"}
	properties := input["properties"].(map[string]any)
	for _, field := range []string{"date_from", "date_to"} {
		properties[field].(map[string]any)["format"] = "date"
	}
	properties["as_of"].(map[string]any)["format"] = "date-time"
	output := schema(reflect.TypeOf(RatingSnapshot{}))
	output["required"] = []string{"source", "organization_id", "division_id", "tests", "external_tests", "external_source_version", "external_domain", "input_hash"}
	domain := output["properties"].(map[string]any)["external_domain"].(map[string]any)
	domain["required"] = []string{"schema_version", "status", "organization_id", "division_id"}
	domainProperties := domain["properties"].(map[string]any)
	domainProperties["schema_version"].(map[string]any)["const"] = 1
	domainProperties["status"].(map[string]any)["const"] = "ready"
	responses := map[string]any{"200": map[string]any{"description": "Текущие тесты и официальные результаты подразделения", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "properties": map[string]any{"status": map[string]any{"const": true}, "data": output}}}}}}
	for code, description := range map[string]string{"400": "Неверный DTO или даты", "401": "Требуется служебный ключ", "404": "Организация, подразделение или направление отсутствует/чужое", "409": "Источник изменился при чтении или содержит неполные данные", "413": "Снимок превышает защитный предел", "415": "Требуется JSON", "500": "Ошибка чтения", "503": "Core или внешний домен недоступен"} {
		responses[code] = map[string]any{"description": description, "content": map[string]any{"application/json": map[string]any{"schema": schema(reflect.TypeOf(ErrorDTO{}))}}}
	}
	return map[string]any{"post": map[string]any{
		"summary":  "Снимок для рейтинга; service key, две сверяемые выборки, pending не равен нулю",
		"security": []any{map[string]any{"serviceKey": []string{}}}, "responses": responses,
		"requestBody": map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": input}}},
	}}
}

func stableRatingSnapshot(ctx context.Context, in RatingSnapshotRequest, from, until time.Time, source ratingSource) (RatingSnapshot, error) {
	first, e := collectRatingSnapshot(ctx, in, from, until, source)
	if e != nil {
		return RatingSnapshot{}, e
	}
	second, e := collectRatingSnapshot(ctx, in, from, until, source)
	if e != nil {
		return RatingSnapshot{}, e
	}
	if first.InputHash != second.InputHash {
		return RatingSnapshot{}, conflict("rating_source_changed")
	}
	return second, nil
}

func collectRatingSnapshot(ctx context.Context, in RatingSnapshotRequest, from, until time.Time, source ratingSource) (RatingSnapshot, error) {
	// References confirms the organization/division relation even for an empty
	// catalog. Archived references remain usable for historical ratings.
	if _, e := source.references(ctx, in.OrganizationID, []int64{in.DivisionID}, nil); e != nil {
		return RatingSnapshot{}, e
	}
	documents, e := source.catalog(ctx, in, from, until)
	if e != nil {
		return RatingSnapshot{}, e
	}
	if len(documents) > ratingsMaxTests {
		return RatingSnapshot{}, ratingSnapshotTooLarge()
	}
	out := RatingSnapshot{Source: "tests", OrganizationID: in.OrganizationID, DivisionID: in.DivisionID, Tests: []RatingTest{}, ExternalTests: []RatingTest{}}
	indices := map[bson.ObjectID]int{}
	directions := map[int64]string{}
	ids := []bson.ObjectID{}
	sort.Slice(documents, func(i, j int) bool { return documents[i].ID.Hex() < documents[j].ID.Hex() })
	for _, d := range documents {
		p := d.Placement(in.DivisionID)
		if d.OrganizationID != in.OrganizationID || d.Archived || p == nil || !p.Settings.Published || p.Settings.StartAt == nil || p.Settings.StartAt.Before(from) || !p.Settings.StartAt.Before(until) {
			continue
		}
		if d.ID.IsZero() || d.DirectionID < 1 {
			return RatingSnapshot{}, conflict("rating_source_incomplete")
		}
		if _, exists := indices[d.ID]; exists {
			return RatingSnapshot{}, conflict("rating_source_incomplete")
		}
		name, exists := directions[d.DirectionID]
		if !exists {
			refs, err := source.references(ctx, in.OrganizationID, []int64{in.DivisionID}, &d.DirectionID)
			if err != nil {
				return RatingSnapshot{}, err
			}
			if refs.Direction == nil || refs.Direction.ID != d.DirectionID {
				return RatingSnapshot{}, unavailable
			}
			name = refs.Direction.Name
			directions[d.DirectionID] = name
		}
		indices[d.ID] = len(out.Tests)
		ids = append(ids, d.ID)
		out.Tests = append(out.Tests, RatingTest{TestID: d.ID.Hex(), Title: d.Title, DirectionID: d.DirectionID, DirectionName: name, StartAt: p.Settings.StartAt.UTC(), Published: true, Results: []RatingTestResult{}})
	}
	attempts, e := source.submissions(ctx, in, ids)
	if e != nil {
		return RatingSnapshot{}, e
	}
	if len(attempts) > ratingsMaxResults {
		return RatingSnapshot{}, ratingSnapshotTooLarge()
	}
	accounts := map[int64]bool{}
	seen := map[bson.ObjectID]map[int64]bool{}
	for _, a := range attempts {
		index, exists := indices[a.TestID]
		if !exists || a.OrganizationID != in.OrganizationID || a.DivisionID != in.DivisionID || !a.Active || a.Practice || a.Status != "accepted" {
			continue
		}
		if a.AccountID < 1 || a.SubmissionID == nil || a.SubmissionID.IsZero() || a.CalculationRevision < 1 {
			return RatingSnapshot{}, conflict("rating_source_incomplete")
		}
		if seen[a.TestID] == nil {
			seen[a.TestID] = map[int64]bool{}
		}
		if seen[a.TestID][a.AccountID] {
			return RatingSnapshot{}, conflict("rating_source_incomplete")
		}
		seen[a.TestID][a.AccountID] = true
		accounts[a.AccountID] = true
		if len(accounts) > ratingsMaxAccounts {
			return RatingSnapshot{}, ratingSnapshotTooLarge()
		}
		result := RatingTestResult{AccountID: a.AccountID, SubmissionID: a.SubmissionID.Hex(), CalculationRevision: a.CalculationRevision, CalculationStatus: a.CalculationStatus}
		switch a.CalculationStatus {
		case "ready":
			if a.Result == nil || a.Result.Score < 0 || a.Result.Score > 100 {
				return RatingSnapshot{}, conflict("rating_source_incomplete")
			}
			score := float64(a.Result.Score)
			result.Score = &score
		case "queued", "processing", "retrying", "failed":
			// A previous grade can never stand in for an unfinished regrade.
		default:
			return RatingSnapshot{}, conflict("rating_source_incomplete")
		}
		out.Tests[index].Results = append(out.Tests[index].Results, result)
	}
	for i := range out.Tests {
		sort.Slice(out.Tests[i].Results, func(a, b int) bool { return out.Tests[i].Results[a].AccountID < out.Tests[i].Results[b].AccountID })
	}
	external, e := source.externalCatalog(ctx, in, from, until)
	if e != nil {
		return RatingSnapshot{}, e
	}
	if external == nil {
		return RatingSnapshot{}, conflict("rating_source_incomplete")
	}
	if len(out.Tests)+len(external) > ratingsMaxTests {
		return RatingSnapshot{}, ratingSnapshotTooLarge()
	}
	moscow, e := time.LoadLocation("Europe/Moscow")
	if e != nil {
		return RatingSnapshot{}, unavailable
	}
	sort.Slice(external, func(i, j int) bool { return external[i].TestID < external[j].TestID })
	seenExternal := map[string]bool{}
	resultCount := len(attempts)
	for _, d := range external {
		date, dateErr := time.ParseInLocation(time.DateOnly, d.Date, moscow)
		if d.OrganizationID != in.OrganizationID || d.DivisionID != in.DivisionID || d.TestID == "" || d.DirectionID < 1 || d.Revision < 1 || dateErr != nil || date.Before(from) || !date.Before(until) || seenExternal[d.TestID] || d.Results == nil {
			return RatingSnapshot{}, conflict("rating_source_incomplete")
		}
		seenExternal[d.TestID] = true
		name, exists := directions[d.DirectionID]
		if !exists {
			refs, err := source.references(ctx, in.OrganizationID, []int64{in.DivisionID}, &d.DirectionID)
			if err != nil {
				return RatingSnapshot{}, err
			}
			if refs.Direction == nil || refs.Direction.ID != d.DirectionID {
				return RatingSnapshot{}, unavailable
			}
			name = refs.Direction.Name
			directions[d.DirectionID] = name
		}
		test := RatingTest{TestID: d.TestID, Title: d.Title, DirectionID: d.DirectionID, DirectionName: name, StartAt: date.UTC(), Published: true, SourceRevision: d.Revision, Results: []RatingTestResult{}}
		seenResults := map[int64]bool{}
		for _, r := range d.Results {
			if r.AccountID < 1 || r.ResultID == "" || r.Revision < 1 || seenResults[r.AccountID] || r.Score != nil && (math.IsNaN(*r.Score) || math.IsInf(*r.Score, 0) || *r.Score < 0 || *r.Score > 100) {
				return RatingSnapshot{}, conflict("rating_source_incomplete")
			}
			seenResults[r.AccountID] = true
			accounts[r.AccountID] = true
			resultCount++
			if resultCount > ratingsMaxResults || len(accounts) > ratingsMaxAccounts {
				return RatingSnapshot{}, ratingSnapshotTooLarge()
			}
			status := "missing"
			if r.Score != nil {
				status = "ready"
			}
			test.Results = append(test.Results, RatingTestResult{AccountID: r.AccountID, SubmissionID: r.ResultID, CalculationRevision: r.Revision, CalculationStatus: status, Score: r.Score})
		}
		sort.Slice(test.Results, func(a, b int) bool { return test.Results[a].AccountID < test.Results[b].AccountID })
		out.ExternalTests = append(out.ExternalTests, test)
	}
	// Include catalogue and result revisions in the source fingerprint, even
	// when an edit restores the same numerical score. Array order is canonical.
	externalHash := sha256.Sum256(canonicalJSON(out.ExternalTests))
	out.ExternalSourceVersion = hex.EncodeToString(externalHash[:])
	out.ExternalDomain = makeExternalDomainStatus(in.OrganizationID, in.DivisionID)
	payload := struct {
		Source                string               `json:"source"`
		OrganizationID        int64                `json:"organization_id"`
		DivisionID            int64                `json:"division_id"`
		Tests                 []RatingTest         `json:"tests"`
		ExternalTests         []RatingTest         `json:"external_tests"`
		ExternalSourceVersion string               `json:"external_source_version"`
		ExternalDomain        ExternalDomainStatus `json:"external_domain"`
	}{out.Source, out.OrganizationID, out.DivisionID, out.Tests, out.ExternalTests, out.ExternalSourceVersion, out.ExternalDomain}
	raw := canonicalJSON(payload)
	if len(raw) > ratingsMaxBytes {
		return RatingSnapshot{}, ratingSnapshotTooLarge()
	}
	hash := sha256.Sum256(raw)
	out.InputHash = hex.EncodeToString(hash[:])
	return out, nil
}
