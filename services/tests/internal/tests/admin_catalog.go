package tests

import (
	"net/http"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// Opt-in read model for the administration cards. Both test kinds share one
// database page; no questions, external results or history are hydrated.
type AdminCatalogItem struct {
	Test     *TestView         `json:"test,omitempty"`
	External *ExternalTestView `json:"external_test,omitempty"`
}

type AdminCatalogView struct {
	Items        []AdminCatalogItem `json:"items"`
	Total        int64              `json:"total"`
	Limit        int64              `json:"limit"`
	Offset       int64              `json:"offset"`
	StatusCounts map[string]int64   `json:"status_counts"`
	ServerTime   time.Time          `json:"server_time"`
}

type adminCatalogQuery struct {
	Online, External bson.M
	Dates            bson.A
	Limit, Offset    int64
	Status           string
	IncludeExternal  bool
}

func adminCatalogFilters(r *http.Request, a Actor, division int64, bank bool) (adminCatalogQuery, error) {
	q := adminCatalogQuery{}
	if !a.Can(false) {
		return q, denied
	}
	filterRequest := r
	if r.URL.Query().Get("is_archived") == "all" {
		copyRequest, copyURL := *r, *r.URL
		query := copyURL.Query()
		query.Del("is_archived")
		copyURL.RawQuery = query.Encode()
		copyRequest.URL = &copyURL
		filterRequest = &copyRequest
	}
	f, limit, offset, err := catalogFilter(filterRequest, a, division, bank)
	if err != nil {
		return q, err
	}
	if r.URL.Query().Get("is_archived") == "all" {
		delete(f, "is_archived")
	}
	external, err := externalFilter(r, a, division)
	if err != nil {
		return q, err
	}
	query := r.URL.Query()
	q = adminCatalogQuery{Online: f, External: external, Limit: limit, Offset: offset, Status: query.Get("status")}
	switch q.Status {
	case "", "all", "active", "upcoming", "ended", "external", "hidden", "archived", "bank":
	default:
		return q, bad("invalid_status")
	}
	include := query.Get("include_external")
	if include != "" && include != "true" && include != "false" {
		return q, bad("invalid_include_external")
	}
	q.IncludeExternal = include == "true" && !bank && division > 0 &&
		query.Get("is_archived") != "true" &&
		(query.Get("relation") == "" || query.Get("relation") == "all" || query.Get("relation") == "owned") &&
		(query.Get("owner_division_id") == "" || external["division_id"] == f["owner_division_id"])
	zone := time.FixedZone("MSK", 3*60*60)
	for _, field := range []struct{ key, path, op string }{
		{"date_from", "selected.settings.start_at", "$gte"},
		{"date_to", "selected.settings.end_at", "$lt"},
	} {
		if raw := query.Get(field.key); raw != "" {
			date, err := time.ParseInLocation(time.DateOnly, raw, zone)
			if err != nil {
				return q, bad("invalid_date")
			}
			if field.key == "date_to" {
				date = date.AddDate(0, 0, 1)
			}
			q.Dates = append(q.Dates, bson.M{"$or": bson.A{bson.M{field.path: nil}, bson.M{field.path: bson.M{field.op: date}}}})
		}
	}
	return q, nil
}

func adminCatalogStatus(now time.Time) bson.M {
	branch := func(condition any, value string) bson.M { return bson.M{"case": condition, "then": value} }
	present := func(path string) bson.M { return catalogExpr("$ne", catalogExpr("$ifNull", path, nil), nil) }
	return bson.M{"$switch": bson.M{"branches": bson.A{
		branch(catalogExpr("$eq", "$is_archived", true), "archived"),
		branch(catalogExpr("$eq", catalogExpr("$ifNull", "$selected", nil), nil), "bank"),
		branch(catalogExpr("$eq", catalogExpr("$ifNull", "$selected.settings.published", false), false), "hidden"),
		branch(catalogExpr("$and", present("$selected.settings.start_at"), catalogExpr("$gt", "$selected.settings.start_at", now)), "upcoming"),
		branch(catalogExpr("$and", present("$selected.settings.end_at"), catalogExpr("$lt", "$selected.settings.end_at", now)), "ended"),
	}, "default": "active"}}
}

func adminCatalogPipeline(q adminCatalogQuery, division int64, now time.Time) mongo.Pipeline {
	projection := bson.M{"organization_id": 1, "owner_division_id": 1, "direction_id": 1, "title": 1,
		"description": 1, "revision": 1, "content_version_id": 1, "is_archived": 1, "bank_available": 1,
		"origin": 1, "placements": 1, "created_at": 1, "updated_at": 1, "created_by": 1, "updated_by": 1,
		"question_count": catalogSize("$questions"), "sort_kind": catalogLiteral(0), "sort_time": "$created_at",
		"selected": bson.M{"$arrayElemAt": bson.A{bson.M{"$filter": bson.M{"input": catalogExpr("$ifNull", "$placements", bson.A{}), "as": "p", "cond": catalogExpr("$eq", "$$p.division_id", division)}}, 0}}}
	pipeline := mongo.Pipeline{catalogStage("$match", q.Online), catalogStage("$project", projection)}
	if len(q.Dates) > 0 {
		pipeline = append(pipeline, catalogStage("$match", bson.M{"$and": q.Dates}))
	}
	pipeline = append(pipeline, catalogStage("$set", bson.M{"status": adminCatalogStatus(now)}), catalogStage("$unset", "selected"))
	if q.IncludeExternal {
		external := mongo.Pipeline{catalogStage("$match", q.External), catalogStage("$project", bson.M{
			"organization_id": 1, "division_id": 1, "direction_id": 1, "name": 1, "date": 1, "revision": 1,
			"created_at": 1, "updated_at": 1, "created_by": 1, "updated_by": 1,
			"sort_kind": catalogLiteral(1), "sort_time": "$date", "status": catalogLiteral("external"),
			"results_count": catalogSize("$results"),
		})}
		pipeline = append(pipeline, catalogStage("$unionWith", bson.M{"coll": ExternalCollection, "pipeline": external}))
	}
	selected := mongo.Pipeline{}
	if q.Status != "" && q.Status != "all" {
		selected = append(selected, catalogStage("$match", bson.M{"status": q.Status}))
	}
	total := append(append(mongo.Pipeline{}, selected...), catalogStage("$count", "total"))
	page := append(append(mongo.Pipeline{}, selected...),
		catalogStage("$sort", bson.D{{Key: "sort_kind", Value: 1}, {Key: "sort_time", Value: -1}, {Key: "_id", Value: -1}}),
		catalogStage("$skip", q.Offset), catalogStage("$limit", q.Limit))
	return append(pipeline, catalogStage("$facet", bson.M{
		"totals": total, "items": page,
		"counts": mongo.Pipeline{catalogStage("$group", bson.M{"_id": "$status", "count": bson.M{"$sum": 1}})},
	}))
}

type adminCatalogRow struct {
	Document      `bson:",inline"`
	DivisionID    int64  `bson:"division_id"`
	Name          string `bson:"name"`
	Date          string `bson:"date"`
	QuestionCount int    `bson:"question_count"`
	ResultsCount  int    `bson:"results_count"`
	Status        string `bson:"status"`
}

func (s *Server) adminCatalog(r *http.Request, a Actor, division int64, bank bool) (any, int, error) {
	q, err := adminCatalogFilters(r, a, division, bank)
	if err != nil {
		return nil, 0, err
	}
	now := time.Now().UTC()
	type result struct {
		Items  []adminCatalogRow       `bson:"items"`
		Totals []struct{ Total int64 } `bson:"totals"`
		Counts []struct {
			Status string `bson:"_id"`
			Count  int64  `bson:"count"`
		} `bson:"counts"`
	}
	var page result
	var total int64
	// A deletion may empty the last page. Clamp once on the server; never fetch
	// preceding pages to reconstruct a list in either the client or Go.
	for pass := 0; pass < 2; pass++ {
		cur, err := s.Store.Collection.Aggregate(r.Context(), adminCatalogPipeline(q, division, now))
		if err != nil {
			return nil, 0, err
		}
		var rows []result
		err = cur.All(r.Context(), &rows)
		cur.Close(r.Context())
		if err != nil {
			return nil, 0, err
		}
		page, total = result{}, 0
		if len(rows) > 0 {
			page = rows[0]
			if len(page.Totals) > 0 {
				total = page.Totals[0].Total
			}
		}
		if total > 0 && q.Offset >= total && pass == 0 {
			q.Offset = ((total - 1) / q.Limit) * q.Limit
			continue
		}
		if total == 0 {
			q.Offset = 0
		}
		break
	}
	out := AdminCatalogView{Items: []AdminCatalogItem{}, Total: total, Limit: q.Limit, Offset: q.Offset, StatusCounts: map[string]int64{"all": 0}, ServerTime: now}
	for _, count := range page.Counts {
		out.StatusCounts[count.Status] = count.Count
		out.StatusCounts["all"] += count.Count
	}
	if len(page.Items) == 0 {
		return out, 200, nil
	}
	divisions, directions := []int64{}, []int64{}
	seenDivisions, seenDirections := map[int64]bool{}, map[int64]bool{}
	for _, row := range page.Items {
		owner := row.OwnerDivisionID
		if row.Status == "external" {
			owner = row.DivisionID
		}
		if !seenDivisions[owner] {
			divisions = append(divisions, owner)
			seenDivisions[owner] = true
		}
		if !seenDirections[row.DirectionID] {
			directions = append(directions, row.DirectionID)
			seenDirections[row.DirectionID] = true
		}
	}
	refs, err := s.Dependencies.ReferencesBatch(r.Context(), a.OrganizationID, divisions, directions)
	if err != nil {
		return nil, 0, err
	}
	owners, names := map[int64]Reference{}, map[int64]Reference{}
	for _, ref := range refs.Divisions {
		owners[ref.ID] = ref
	}
	for _, ref := range refs.Directions {
		names[ref.ID] = ref
	}
	for _, row := range page.Items {
		ownerID := row.OwnerDivisionID
		if row.Status == "external" {
			ownerID = row.DivisionID
		}
		if owners[ownerID].ID != ownerID || names[row.DirectionID].ID != row.DirectionID {
			return nil, 0, unavailable
		}
		if row.Status == "external" {
			d := ExternalTestDocument{ID: row.ID, OrganizationID: row.OrganizationID, DivisionID: row.DivisionID, DirectionID: row.DirectionID, Name: row.Name, Date: row.Date, Revision: row.Revision, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
			view := externalViewWithDirection(a, d, names[row.DirectionID].Name)
			view.Actions.CanDelete = a.Can(true) && (row.ResultsCount == 0 || actorResults(a, true))
			out.Items = append(out.Items, AdminCatalogItem{External: &view})
		} else {
			view := buildTestView(a, row.Document, division, owners[row.OwnerDivisionID], names[row.DirectionID])
			view.QuestionCount = row.QuestionCount
			out.Items = append(out.Items, AdminCatalogItem{Test: &view})
		}
	}
	return out, 200, nil
}
