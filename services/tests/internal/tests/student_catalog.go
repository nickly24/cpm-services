package tests

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// The student list is a read model, not an attempt envelope. In particular no
// question content, answer values, commit ledger or grading keys leave Mongo.
type StudentAttemptSummary struct {
	ID                bson.ObjectID  `json:"id" bson:"_id"`
	Status            string         `json:"-" bson:"status"`
	AnswerDeadline    time.Time      `json:"answer_deadline" bson:"answer_deadline"`
	UploadDeadline    time.Time      `json:"upload_deadline" bson:"upload_deadline"`
	AnsweredCount     int            `json:"answered_count" bson:"answered_count"`
	TotalQuestions    int            `json:"total_questions" bson:"total_questions"`
	SubmissionID      *bson.ObjectID `json:"-" bson:"submission_id"`
	AcceptedAt        *time.Time     `json:"-" bson:"accepted_at"`
	CalculationStatus string         `json:"-" bson:"calculation_status"`
	Score             *int           `json:"-" bson:"score"`
}

type StudentSessionSummary struct {
	ID                bson.ObjectID `json:"id"`
	CompletedAt       *time.Time    `json:"completed_at"`
	Score             *int          `json:"score"`
	CalculationStatus string        `json:"calculation_status"`
}

type StudentCatalogItem struct {
	ID               bson.ObjectID          `json:"id"`
	OrganizationID   int64                  `json:"organization_id"`
	DivisionID       int64                  `json:"division_id"`
	AccountID        int64                  `json:"account_id"`
	DirectionID      int64                  `json:"direction_id"`
	DirectionName    string                 `json:"direction_name"`
	Title            string                 `json:"title"`
	StartAt          *time.Time             `json:"start_at"`
	EndAt            *time.Time             `json:"end_at"`
	TimeLimitMinutes int                    `json:"time_limit_minutes"`
	Status           string                 `json:"status"`
	Access           *AccessView            `json:"access"`
	ActiveAttempt    *StudentAttemptSummary `json:"active_attempt"`
	Session          *StudentSessionSummary `json:"session"`
	External         bool                   `json:"is_external"`
	Date             string                 `json:"date,omitempty"`
	HasResult        bool                   `json:"has_result"`
	Rate             *float64               `json:"rate"`
}

type StudentCatalogView struct {
	Items      []StudentCatalogItem `json:"items"`
	Total      int64                `json:"total"`
	Limit      int64                `json:"limit"`
	Offset     int64                `json:"offset"`
	ServerTime time.Time            `json:"server_time"`
}

type studentCatalogRow struct {
	ID             bson.ObjectID          `bson:"_id"`
	OrganizationID int64                  `bson:"organization_id"`
	DivisionID     int64                  `bson:"division_id"`
	AccountID      int64                  `bson:"account_id"`
	DirectionID    int64                  `bson:"direction_id"`
	Title          string                 `bson:"title"`
	Revision       int64                  `bson:"revision"`
	Settings       Settings               `bson:"settings"`
	QuestionCount  int                    `bson:"question_count"`
	Status         string                 `bson:"status"`
	Attempt        *StudentAttemptSummary `bson:"attempt"`
	External       bool                   `bson:"is_external"`
	Date           string                 `bson:"date"`
	HasResult      bool                   `bson:"has_result"`
	Rate           *float64               `bson:"rate"`
}

type studentCatalogQuery struct {
	Online, External          bson.M
	Limit, Offset             int64
	Status                    string
	Overview, IncludeExternal bool
}

func (s *Server) studentCatalogSpec() routeSpec {
	return routeSpec{"GET /v1/tests/divisions/{division}/student-catalog", "Компактный каталог ученика: доступ, попытка и результат; фильтры и единая пагинация официальных и внешних тестов на сервере", s.studentCatalog, nil, StudentCatalogView{}}
}

func studentCatalogFilters(r *http.Request, a Actor, division int64) (studentCatalogQuery, error) {
	if a.Role != "student" || !a.InDivision(division) {
		return studentCatalogQuery{}, denied
	}
	f, limit, offset, e := catalogFilter(r, a, division, false)
	if e != nil {
		return studentCatalogQuery{}, e
	}
	external, e := externalFilter(r, a, division)
	if e != nil {
		return studentCatalogQuery{}, e
	}
	q := studentCatalogQuery{Online: f, External: external, Limit: limit, Offset: offset, Status: r.URL.Query().Get("status")}
	switch q.Status {
	case "", "all", "available", "upcoming", "completed", "pending_upload", "upload_window_closed", "missed", "external", "running", "checking", "calculation_failed":
	default:
		return q, bad("invalid_status")
	}
	for _, field := range []struct {
		name   string
		target *bool
	}{{"overview", &q.Overview}, {"include_external", &q.IncludeExternal}} {
		if raw := r.URL.Query().Get(field.name); raw != "" {
			if raw != "true" && raw != "false" {
				return q, bad("invalid_" + field.name)
			}
			*field.target, _ = strconv.ParseBool(raw)
		}
	}
	// Calendar filters are evaluated consistently in Moscow, including fractional
	// seconds on the final day. Missing schedule dates remain visible as before.
	zone := time.FixedZone("MSK", 3*60*60)
	conditions, _ := f["$and"].(bson.A)
	for _, field := range []struct{ query, path, op string }{{"date_from", "placement.settings.start_at", "$gte"}, {"date_to", "placement.settings.end_at", "$lt"}} {
		if raw := r.URL.Query().Get(field.query); raw != "" {
			date, err := time.ParseInLocation(time.DateOnly, raw, zone)
			if err != nil {
				return q, bad("invalid_date")
			}
			if field.query == "date_to" {
				date = date.AddDate(0, 0, 1)
			}
			conditions = append(conditions, bson.M{"$or": bson.A{bson.M{field.path: nil}, bson.M{field.path: bson.M{field.op: date}}}})
		}
	}
	// Date predicates refer to the projected selected placement, not any other
	// division's settings, and are applied after the initial catalog index match.
	if len(conditions) > 0 {
		q.Online["$and"] = conditions
	}
	return q, nil
}

func catalogStage(key string, value any) bson.D    { return bson.D{{Key: key, Value: value}} }
func catalogExpr(key string, values ...any) bson.M { return bson.M{key: bson.A(values)} }
func catalogLiteral(value any) bson.M              { return bson.M{"$literal": value} }
func catalogSize(path string) bson.M               { return bson.M{"$size": catalogExpr("$ifNull", path, bson.A{})} }

func (q studentCatalogQuery) onlineMatch() bson.M {
	f := bson.M{}
	for k, v := range q.Online {
		if k != "$and" {
			f[k] = v
		}
	}
	// Relation predicates from catalogFilter still belong to the initial match.
	if conditions, ok := q.Online["$and"].(bson.A); ok {
		for _, raw := range conditions {
			condition := raw.(bson.M)
			if _, date := condition["$or"]; !date {
				f["$and"] = appendCondition(f["$and"], condition)
			}
		}
	}
	if q.Status == "external" {
		f["_id"] = bson.M{"$in": bson.A{}}
	}
	return f
}
func appendCondition(value any, condition bson.M) bson.A {
	conditions, _ := value.(bson.A)
	return append(conditions, condition)
}
func (q studentCatalogQuery) externalIncluded() bool {
	return q.IncludeExternal && (q.Status == "" || q.Status == "all" || q.Status == "external")
}
func (q studentCatalogQuery) filtersAttempts() bool {
	return q.Overview || q.Status != "" && q.Status != "all" && q.Status != "external"
}

func studentCatalogPipeline(q studentCatalogQuery, a Actor, division int64, inactive []int64, now time.Time) mongo.Pipeline {
	if inactive == nil {
		inactive = []int64{}
	}
	placement := bson.M{"$arrayElemAt": bson.A{bson.M{"$filter": bson.M{"input": "$placements", "as": "p", "cond": catalogExpr("$eq", "$$p.division_id", division)}}, 0}}
	pipeline := mongo.Pipeline{
		catalogStage("$match", q.onlineMatch()),
		catalogStage("$project", bson.M{"organization_id": 1, "direction_id": 1, "title": 1, "revision": 1, "created_at": 1, "placement": placement, "question_count": catalogSize("$questions")}),
	}
	if conditions, ok := q.Online["$and"].(bson.A); ok {
		dates := bson.A{}
		for _, raw := range conditions {
			if condition := raw.(bson.M); condition["$or"] != nil {
				dates = append(dates, condition)
			}
		}
		if len(dates) > 0 {
			pipeline = append(pipeline, catalogStage("$match", bson.M{"$and": dates}))
		}
	}
	pipeline = append(pipeline, catalogStage("$set", bson.M{"settings": "$placement.settings", "division_id": division, "account_id": a.AccountID, "is_external": false}))
	filterByAttempt := q.filtersAttempts()
	if filterByAttempt {
		pipeline = append(pipeline, studentCatalogAttemptStages(a, division, inactive, now)...)
	}
	if q.Overview {
		pipeline = append(pipeline, catalogStage("$match", bson.M{"status": bson.M{"$in": bson.A{"available", "running", "pending_upload", "upload_window_closed", "completed", "checking", "calculation_failed"}}}))
	}
	if q.Status != "" && q.Status != "all" {
		pipeline = append(pipeline, catalogStage("$match", bson.M{"status": q.Status}))
	}
	pipeline = append(pipeline, catalogStage("$project", bson.M{"organization_id": 1, "division_id": 1, "account_id": 1, "direction_id": 1, "title": 1, "revision": 1, "settings": 1, "question_count": 1, "attempt": 1, "status": 1, "is_external": 1, "sort_kind": catalogLiteral(0), "sort_time": "$created_at"}))
	if q.externalIncluded() {
		pipeline = append(pipeline, catalogStage("$unionWith", bson.M{"coll": ExternalCollection, "pipeline": studentExternalPipeline(q, a)}))
	}
	page := mongo.Pipeline{catalogStage("$sort", bson.D{{Key: "sort_kind", Value: 1}, {Key: "sort_time", Value: -1}, {Key: "_id", Value: -1}}), catalogStage("$skip", q.Offset), catalogStage("$limit", q.Limit)}
	// Most catalog pages do not filter by attempt status: paginate first, then
	// read summaries for only those IDs. History length cannot amplify this read.
	if !filterByAttempt {
		page = append(page, studentCatalogAttemptStages(a, division, inactive, now)...)
	}
	page = append(page, catalogStage("$unset", bson.A{"sort_kind", "sort_time", "attempts", "direction_active"}))
	pipeline = append(pipeline, catalogStage("$facet", bson.M{"totals": mongo.Pipeline{catalogStage("$count", "total")}, "items": page}))
	return pipeline
}

func studentCatalogAttemptStages(a Actor, division int64, inactive []int64, now time.Time) mongo.Pipeline {
	return mongo.Pipeline{
		catalogStage("$lookup", bson.M{"from": AttemptsCollection, "let": bson.M{"test": "$_id", "external": "$is_external"}, "pipeline": mongo.Pipeline{
			catalogStage("$match", bson.M{"organization_id": a.OrganizationID, "division_id": division, "account_id": a.AccountID, "practice": false, "active": true, "$expr": catalogExpr("$and", catalogExpr("$eq", "$test_id", "$$test"), catalogExpr("$eq", "$$external", false))}),
			catalogStage("$project", bson.M{"status": 1, "answer_deadline": 1, "upload_deadline": 1, "submission_id": 1, "accepted_at": 1, "calculation_status": 1, "score": "$result.score", "answered_count": catalogSize("$answers"), "total_questions": catalogSize("$question_order")}),
		}, "as": "attempts"}),
		catalogStage("$set", bson.M{"attempt": catalogExpr("$ifNull", bson.M{"$arrayElemAt": bson.A{"$attempts", 0}}, nil), "direction_active": catalogExpr("$not", catalogExpr("$in", "$direction_id", inactive))}),
		catalogStage("$set", bson.M{"status": bson.M{"$cond": bson.A{"$is_external", "external", studentCatalogStatusExpression(now)}}}),
	}
}

func studentCatalogStatusExpression(now time.Time) bson.M {
	accepted := catalogExpr("$ne", catalogExpr("$ifNull", "$attempt.submission_id", nil), nil)
	hasAttempt := catalogExpr("$ne", "$attempt", nil)
	usableAttempt := catalogExpr("$and", hasAttempt, "$direction_active")
	schedule := catalogExpr("$and", catalogExpr("$ne", catalogExpr("$ifNull", "$settings.start_at", nil), nil), catalogExpr("$ne", catalogExpr("$ifNull", "$settings.end_at", nil), nil), catalogExpr("$gte", "$settings.time_limit_minutes", 1))
	branch := func(condition any, status string) bson.M { return bson.M{"case": condition, "then": status} }
	return bson.M{"$switch": bson.M{"branches": bson.A{
		bson.M{"case": accepted, "then": bson.M{"$switch": bson.M{"branches": bson.A{branch(catalogExpr("$eq", "$attempt.calculation_status", "ready"), "completed"), branch(catalogExpr("$eq", "$attempt.calculation_status", "failed"), "calculation_failed")}, "default": "checking"}}},
		branch(catalogExpr("$and", usableAttempt, catalogExpr("$gt", now, "$attempt.upload_deadline")), "upload_window_closed"),
		branch(catalogExpr("$and", usableAttempt, catalogExpr("$gt", now, "$attempt.answer_deadline")), "pending_upload"),
		branch(usableAttempt, "running"),
		branch(catalogExpr("$and", "$direction_active", schedule, catalogExpr("$lt", now, "$settings.start_at")), "upcoming"),
		branch(catalogExpr("$and", "$direction_active", schedule, catalogExpr("$lte", now, "$settings.end_at"), catalogExpr("$gt", "$question_count", 0)), "available"),
	}, "default": "missed"}}
}

func studentExternalPipeline(q studentCatalogQuery, a Actor) mongo.Pipeline {
	result := bson.M{"$filter": bson.M{"input": catalogExpr("$ifNull", "$results", bson.A{}), "as": "r", "cond": catalogExpr("$eq", "$$r.account_id", a.AccountID)}}
	p := mongo.Pipeline{catalogStage("$match", q.External), catalogStage("$project", bson.M{"organization_id": 1, "division_id": 1, "direction_id": 1, "date": 1, "title": "$name", "mine": result})}
	if q.Overview {
		p = append(p, catalogStage("$match", bson.M{"mine.0": bson.M{"$exists": true}}))
	}
	return append(p, catalogStage("$project", bson.M{"organization_id": 1, "division_id": 1, "direction_id": 1, "date": 1, "title": 1, "account_id": catalogLiteral(a.AccountID), "is_external": catalogLiteral(true), "status": catalogLiteral("external"), "has_result": catalogExpr("$gt", catalogSize("$mine"), 0), "rate": catalogExpr("$ifNull", bson.M{"$arrayElemAt": bson.A{"$mine.score", 0}}, nil), "sort_kind": catalogLiteral(1), "sort_time": "$date"}))
}

func (s *Server) studentCatalogDirections(ctx context.Context, q studentCatalogQuery, a Actor) (map[int64]Reference, error) {
	pipeline := mongo.Pipeline{catalogStage("$match", q.onlineMatch()), catalogStage("$project", bson.M{"direction_id": 1})}
	if q.externalIncluded() {
		pipeline = append(pipeline, catalogStage("$unionWith", bson.M{"coll": ExternalCollection, "pipeline": mongo.Pipeline{catalogStage("$match", q.External), catalogStage("$project", bson.M{"direction_id": 1})}}))
	}
	pipeline = append(pipeline, catalogStage("$group", bson.M{"_id": "$direction_id"}))
	cur, e := s.Store.Collection.Aggregate(ctx, pipeline)
	if e != nil {
		return nil, e
	}
	defer cur.Close(ctx)
	var rows []struct {
		ID int64 `bson:"_id"`
	}
	if e = cur.All(ctx, &rows); e != nil {
		return nil, e
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		if row.ID < 1 {
			return nil, unavailable
		}
		ids = append(ids, row.ID)
	}
	return s.studentDirectionReferences(ctx, a.OrganizationID, ids)
}

func (s *Server) studentDirectionReferences(ctx context.Context, org int64, ids []int64) (map[int64]Reference, error) {
	out := map[int64]Reference{}
	for offset := 0; offset < len(ids); offset += 1000 {
		refs, err := s.Dependencies.ReferencesBatch(ctx, org, nil, ids[offset:min(offset+1000, len(ids))])
		if err != nil {
			return nil, err
		}
		for _, ref := range refs.Directions {
			out[ref.ID] = ref
		}
	}
	return out, nil
}

func (s *Server) studentCatalog(r *http.Request, a Actor, division int64) (any, int, error) {
	q, e := studentCatalogFilters(r, a, division)
	if e != nil {
		return nil, 0, e
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	directions := map[int64]Reference{}
	if q.filtersAttempts() {
		directions, e = s.studentCatalogDirections(r.Context(), q, a)
		if e != nil {
			return nil, 0, e
		}
	}
	inactive := []int64{}
	for id, ref := range directions {
		if !ref.Active {
			inactive = append(inactive, id)
		}
	}
	pipeline := studentCatalogPipeline(q, a, division, inactive, now)
	cur, e := s.Store.Collection.Aggregate(r.Context(), pipeline)
	if e != nil {
		return nil, 0, e
	}
	defer cur.Close(r.Context())
	var pages []struct {
		Items  []studentCatalogRow `bson:"items"`
		Totals []struct {
			Total int64 `bson:"total"`
		} `bson:"totals"`
	}
	if e = cur.All(r.Context(), &pages); e != nil {
		return nil, 0, e
	}
	if len(pages) != 1 {
		return nil, 0, unavailable
	}
	if !q.filtersAttempts() {
		ids, seen := []int64{}, map[int64]bool{}
		for _, row := range pages[0].Items {
			if row.DirectionID < 1 {
				return nil, 0, unavailable
			}
			if !seen[row.DirectionID] {
				seen[row.DirectionID] = true
				ids = append(ids, row.DirectionID)
			}
		}
		directions, e = s.studentDirectionReferences(r.Context(), a.OrganizationID, ids)
		if e != nil {
			return nil, 0, e
		}
	}
	out := StudentCatalogView{Items: []StudentCatalogItem{}, Limit: q.Limit, Offset: q.Offset, ServerTime: now}
	if len(pages[0].Totals) == 1 {
		out.Total = pages[0].Totals[0].Total
	}
	for _, row := range pages[0].Items {
		ref, found := directions[row.DirectionID]
		if !found || row.OrganizationID != a.OrganizationID || row.DivisionID != division || row.AccountID != a.AccountID {
			return nil, 0, unavailable
		}
		item := buildStudentCatalogItem(row, ref, now)
		if q.filtersAttempts() && item.Status != row.Status {
			return nil, 0, unavailable
		}
		out.Items = append(out.Items, item)
	}
	return out, 200, nil
}

func buildStudentCatalogItem(row studentCatalogRow, direction Reference, now time.Time) StudentCatalogItem {
	item := StudentCatalogItem{ID: row.ID, OrganizationID: row.OrganizationID, DivisionID: row.DivisionID, AccountID: row.AccountID, DirectionID: row.DirectionID, DirectionName: direction.Name, Title: row.Title, StartAt: row.Settings.StartAt, EndAt: row.Settings.EndAt, TimeLimitMinutes: row.Settings.TimeLimitMinutes, External: row.External, Date: row.Date, HasResult: row.HasResult, Rate: row.Rate}
	if row.External {
		item.Status = "external"
		return item
	}
	doc := Document{ID: row.ID, Revision: row.Revision}
	if row.QuestionCount > 0 {
		doc.Questions = []Question{{}}
	}
	placement := Placement{DivisionID: row.DivisionID, Settings: row.Settings}
	var attempt *Attempt
	if row.Attempt != nil {
		summary := row.Attempt
		attempt = &Attempt{ID: summary.ID, Status: summary.Status, AnswerDeadline: summary.AnswerDeadline, UploadDeadline: summary.UploadDeadline, SubmissionID: summary.SubmissionID, CalculationStatus: summary.CalculationStatus}
	}
	access := accessState(doc, placement, direction.Active, now, "student", attempt)
	item.Access = &access
	item.Status = studentCatalogAccessStatus(access)
	if row.Attempt != nil && access.SubmissionID == nil {
		item.ActiveAttempt = row.Attempt
	}
	if row.Attempt != nil && access.SubmissionID != nil {
		summary := row.Attempt
		item.Session = &StudentSessionSummary{ID: *access.SubmissionID, CompletedAt: summary.AcceptedAt, CalculationStatus: summary.CalculationStatus}
		if summary.CalculationStatus == "ready" {
			item.Session.Score = summary.Score
		}
	}
	return item
}

func studentCatalogAccessStatus(access AccessView) string {
	if access.SubmissionID != nil {
		if access.CalculationStatus == "ready" {
			return "completed"
		}
		if access.CalculationStatus == "failed" {
			return "calculation_failed"
		}
		return "checking"
	}
	switch access.OfficialReason {
	case "resume_attempt":
		return "running"
	case "test_upcoming":
		return "upcoming"
	case "pending_upload":
		return "pending_upload"
	case "upload_deadline_passed":
		return "upload_window_closed"
	}
	if access.OfficialAccessAllowed {
		return "available"
	}
	return "missed"
}
