package tests

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type ExternalCreateDTO struct {
	Name        string `json:"name"`
	DirectionID int64  `json:"direction_id"`
	Date        string `json:"date"`
}
type ExternalMetadataDTO struct {
	ExpectedRevision int64 `json:"expected_revision"`
	ExternalCreateDTO
}
type ExternalDeleteDTO struct {
	ExpectedRevision  int64  `json:"expected_revision"`
	ConfirmationToken string `json:"confirmation_token"`
}
type ExternalResultDTO struct {
	ExpectedRevision       int64    `json:"expected_revision"`
	ExpectedResultRevision int64    `json:"expected_result_revision"`
	Score                  *float64 `json:"score"`
}

func decodeExternalResult(r *http.Request, in *ExternalResultDTO) error {
	var fields map[string]json.RawMessage
	if e := decode(r, &fields); e != nil {
		return e
	}
	for key := range fields {
		if key != "expected_revision" && key != "expected_result_revision" && key != "score" {
			return bad("invalid_request")
		}
	}
	if r.Method == "PUT" {
		if _, present := fields["score"]; !present {
			return bad("external_score_required")
		}
	}
	raw, e := json.Marshal(fields)
	if e != nil || json.Unmarshal(raw, in) != nil {
		return bad("invalid_request")
	}
	return nil
}

type ExternalListView struct {
	Items  []ExternalTestView `json:"items"`
	Total  int64              `json:"total"`
	Limit  int64              `json:"limit"`
	Offset int64              `json:"offset"`
}
type ExternalDeletePreview struct {
	Test              ExternalTestView `json:"test"`
	ResultsCount      int              `json:"results_count"`
	ConfirmationToken string           `json:"confirmation_token"`
	ExpiresAt         time.Time        `json:"expires_at"`
}

func actorUpload(a Actor, edit bool) bool {
	if a.Role == "admin" && a.OrganizationAdmin {
		return true
	}
	p := a.Permissions["upload"]
	return a.Role == "staff_admin" && p.View && (!edit || p.Edit)
}
func externalReadable(a Actor) bool {
	return a.Role == "student" || a.Can(false) || actorResults(a, false) || actorUpload(a, false)
}
func validateExternal(in ExternalCreateDTO) error {
	date, e := time.Parse(time.DateOnly, in.Date)
	if !validText(in.Name, 100, true) || in.DirectionID < 1 || e != nil || date.Year() < 1 || date.Format(time.DateOnly) != in.Date {
		return bad("invalid_external_metadata")
	}
	return nil
}
func validExternalScore(v *float64) bool {
	return v == nil || !math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= 0 && *v <= 100
}
func (s *Server) externalLoad(r *http.Request, a Actor) (ExternalTestDocument, error) {
	id, e := bson.ObjectIDFromHex(r.PathValue("external"))
	if e != nil {
		return ExternalTestDocument{}, bad("invalid_id")
	}
	d, e := s.Store.ExternalGet(r.Context(), a.OrganizationID, id)
	if e != nil {
		return d, e
	}
	if raw := r.PathValue("division"); raw != "" {
		division, e := numericID(raw)
		if e != nil {
			return d, e
		}
		if division != d.DivisionID {
			return d, missing
		}
	}
	if !externalReadable(a) {
		return d, denied
	}
	if e = s.scope(r, a, d.DivisionID); e != nil {
		return d, e
	}
	return d, nil
}
func (s *Server) externalView(r *http.Request, a Actor, d ExternalTestDocument) (ExternalTestView, error) {
	refs, e := s.Dependencies.References(r.Context(), a.OrganizationID, []int64{d.DivisionID}, &d.DirectionID)
	if e != nil {
		return ExternalTestView{}, e
	}
	return externalViewWithDirection(a, d, refs.Direction.Name), nil
}
func externalViewWithDirection(a Actor, d ExternalTestDocument, direction string) ExternalTestView {
	v := ExternalTestView{ID: d.ID, Revision: d.Revision, OrganizationID: d.OrganizationID, DivisionID: d.DivisionID, Name: d.Name, DirectionID: d.DirectionID, DirectionName: direction, Date: d.Date, Deleted: d.Deleted}
	v.Actions = ExternalActions{CanEdit: !d.Deleted && a.Can(true), CanDelete: !d.Deleted && a.Can(true) && (len(d.Results) == 0 || actorResults(a, true)), CanViewResults: actorResults(a, false) || a.Role == "student"}
	if a.Role == "student" {
		v.Result = d.result(a.AccountID)
	}
	return v
}
func (s *Server) externalCreate(r *http.Request, a Actor, division int64) (any, int, error) {
	if !a.Can(true) {
		return nil, 0, denied
	}
	var in ExternalCreateDTO
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	in.Name = strings.TrimSpace(in.Name)
	if e := validateExternal(in); e != nil {
		return nil, 0, e
	}
	refs, e := s.Dependencies.References(r.Context(), a.OrganizationID, []int64{division}, &in.DirectionID)
	if e != nil {
		return nil, 0, e
	}
	if !refs.Direction.Active {
		return nil, 0, conflict("direction_archived")
	}
	id, hash, e := operation(r, a, in)
	if e != nil {
		return nil, 0, e
	}
	if old, e := s.Store.ExternalGet(r.Context(), a.OrganizationID, id); e == nil {
		if old.OperationHash != hash || old.DivisionID != division {
			return nil, 0, conflict("idempotency_conflict")
		}
		view, e := s.externalView(r, a, old)
		return view, 200, e
	} else if !errors.Is(e, missing) {
		return nil, 0, e
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	d := ExternalTestDocument{ID: id, OrganizationID: a.OrganizationID, DivisionID: division, Name: in.Name, DirectionID: in.DirectionID, Date: in.Date, Revision: 1, Results: []ExternalResult{}, HistoryIDs: []bson.ObjectID{}, Commands: []ExternalCommandReceipt{}, OperationHash: hash, CreatedBy: a.AccountID, UpdatedBy: a.AccountID, CreatedAt: now, UpdatedAt: now}
	h := ExternalHistory{ID: bson.NewObjectID(), TestID: d.ID, OrganizationID: d.OrganizationID, DivisionID: division, Revision: 1, Action: "created", ActorID: a.AccountID, ChangedAt: now, After: d.metadata(), Changes: []ExternalResultChange{}}
	if _, e = s.Store.ExternalVersions().InsertOne(r.Context(), h); e != nil {
		return nil, 0, e
	}
	d.HistoryIDs = append(d.HistoryIDs, h.ID)
	_, e = s.Store.External().InsertOne(r.Context(), d)
	if mongo.IsDuplicateKeyError(e) {
		old, err := s.Store.ExternalGet(r.Context(), a.OrganizationID, id)
		if err != nil {
			return nil, 0, err
		}
		if old.OperationHash != hash || old.DivisionID != division {
			return nil, 0, conflict("idempotency_conflict")
		}
		view, err := s.externalView(r, a, old)
		return view, 200, err
	}
	if e != nil {
		return nil, 0, e
	}
	return externalViewWithDirection(a, d, refs.Direction.Name), 201, nil
}
func externalFilter(r *http.Request, a Actor, division int64) (bson.M, error) {
	f := bson.M{"organization_id": a.OrganizationID, "division_id": division, "is_deleted": false}
	q := r.URL.Query()
	if raw := q.Get("direction_id"); raw != "" {
		id, e := numericID(raw)
		if e != nil {
			return nil, e
		}
		f["direction_id"] = id
	}
	if value := strings.TrimSpace(q.Get("q")); value != "" {
		if !validText(value, 100, false) {
			return nil, bad("invalid_search")
		}
		f["name"] = bson.Regex{Pattern: regexp.QuoteMeta(value), Options: "i"}
	}
	for _, k := range []string{"date_from", "date_to"} {
		if raw := q.Get(k); raw != "" {
			d, e := time.Parse(time.DateOnly, raw)
			if e != nil || d.Format(time.DateOnly) != raw {
				return nil, bad("invalid_date")
			}
			bounds, ok := f["date"].(bson.M)
			if !ok {
				bounds = bson.M{}
			}
			op := "$gte"
			if k == "date_to" {
				op = "$lte"
			}
			bounds[op] = raw
			f["date"] = bounds
		}
	}
	if q.Get("date_from") != "" && q.Get("date_to") != "" && q.Get("date_from") > q.Get("date_to") {
		return nil, bad("invalid_date_range")
	}
	return f, nil
}
func (s *Server) externalList(r *http.Request, a Actor, division int64) (any, int, error) {
	if !externalReadable(a) {
		return nil, 0, denied
	}
	limit, offset, e := pagination(r)
	if e != nil {
		return nil, 0, e
	}
	f, e := externalFilter(r, a, division)
	if e != nil {
		return nil, 0, e
	}
	total, e := s.Store.External().CountDocuments(r.Context(), f)
	if e != nil {
		return nil, 0, e
	}
	projection := bson.M{"commands": 0, "history_ids": 0}
	if a.Role == "student" {
		projection = bson.M{"_id": 1, "organization_id": 1, "division_id": 1, "name": 1, "direction_id": 1, "date": 1, "revision": 1, "is_deleted": 1, "results": bson.M{"$elemMatch": bson.M{"account_id": a.AccountID}}}
	}
	cur, e := s.Store.External().Find(r.Context(), f, options.Find().SetSort(bson.D{{Key: "date", Value: -1}, {Key: "_id", Value: -1}}).SetSkip(offset).SetLimit(limit).SetProjection(projection))
	if e != nil {
		return nil, 0, e
	}
	defer cur.Close(r.Context())
	docs := []ExternalTestDocument{}
	if e = cur.All(r.Context(), &docs); e != nil {
		return nil, 0, e
	}
	out := ExternalListView{Items: []ExternalTestView{}, Total: total, Limit: limit, Offset: offset}
	names := map[int64]string{}
	for _, d := range docs {
		name, ok := names[d.DirectionID]
		if !ok {
			refs, err := s.Dependencies.References(r.Context(), a.OrganizationID, []int64{division}, &d.DirectionID)
			if err != nil {
				return nil, 0, err
			}
			name = refs.Direction.Name
			names[d.DirectionID] = name
		}
		out.Items = append(out.Items, externalViewWithDirection(a, d, name))
	}
	return out, 200, nil
}
func (s *Server) externalDetail(r *http.Request, a Actor, _ int64) (any, int, error) {
	d, e := s.externalLoad(r, a)
	if e != nil {
		return nil, 0, e
	}
	if d.Deleted {
		return nil, 0, missing
	}
	v, e := s.externalView(r, a, d)
	return v, 200, e
}
func (s *Server) externalMetadata(r *http.Request, a Actor, _ int64) (any, int, error) {
	if !a.Can(true) {
		return nil, 0, denied
	}
	d, e := s.externalLoad(r, a)
	if e != nil {
		return nil, 0, e
	}
	if d.Deleted {
		return nil, 0, missing
	}
	var in ExternalMetadataDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	if e = checkRevision(d.Revision, in.ExpectedRevision); e != nil {
		return nil, 0, e
	}
	in.Name = strings.TrimSpace(in.Name)
	if e = validateExternal(in.ExternalCreateDTO); e != nil {
		return nil, 0, e
	}
	refs, e := s.Dependencies.References(r.Context(), a.OrganizationID, []int64{d.DivisionID}, &in.DirectionID)
	if e != nil {
		return nil, 0, e
	}
	if !refs.Direction.Active {
		return nil, 0, conflict("direction_archived")
	}
	after := d
	after.Name = in.Name
	after.DirectionID = in.DirectionID
	after.Date = in.Date
	after, e = s.externalSave(r.Context(), d, after, a, "metadata_updated")
	if e != nil {
		return nil, 0, e
	}
	return externalViewWithDirection(a, after, refs.Direction.Name), 200, nil
}
func (s *Server) externalDeleteToken(a Actor, d ExternalTestDocument, expires int64) string {
	body := strings.Join([]string{"external-delete", strconv.FormatInt(a.OrganizationID, 10), strconv.FormatInt(d.DivisionID, 10), d.ID.Hex(), strconv.FormatInt(d.Revision, 10), strconv.FormatInt(a.AccountID, 10), strconv.FormatInt(expires, 10)}, ":")
	mac := hmac.New(sha256.New, []byte(s.Config.ServiceKey))
	mac.Write([]byte(body))
	return strconv.FormatInt(expires, 10) + "." + hex.EncodeToString(mac.Sum(nil))
}
func (s *Server) externalCheckToken(a Actor, d ExternalTestDocument, token string) error {
	p := strings.Split(token, ".")
	if len(p) != 2 {
		return bad("delete_confirmation_required")
	}
	expires, e := strconv.ParseInt(p[0], 10, 64)
	if e != nil || expires < time.Now().Unix() || expires > time.Now().Add(11*time.Minute).Unix() {
		return conflict("delete_confirmation_expired")
	}
	want := s.externalDeleteToken(a, d, expires)
	if subtle.ConstantTimeCompare([]byte(want), []byte(token)) != 1 {
		return conflict("delete_confirmation_mismatch")
	}
	return nil
}
func (s *Server) externalDeletePreview(r *http.Request, a Actor, _ int64) (any, int, error) {
	d, e := s.externalLoad(r, a)
	if e != nil {
		return nil, 0, e
	}
	if d.Deleted {
		return nil, 0, missing
	}
	if !a.Can(true) || len(d.Results) > 0 && !actorResults(a, true) {
		return nil, 0, denied
	}
	v, e := s.externalView(r, a, d)
	if e != nil {
		return nil, 0, e
	}
	expires := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Second)
	return ExternalDeletePreview{v, len(d.Results), s.externalDeleteToken(a, d, expires.Unix()), expires}, 200, nil
}
func (s *Server) externalDelete(r *http.Request, a Actor, _ int64) (any, int, error) {
	d, e := s.externalLoad(r, a)
	if e != nil {
		return nil, 0, e
	}
	if !a.Can(true) || len(d.Results) > 0 && !actorResults(a, true) {
		return nil, 0, denied
	}
	var in ExternalDeleteDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	if d.Deleted {
		previous := d
		previous.Revision--
		if in.ExpectedRevision != previous.Revision {
			return nil, 0, conflict("stale_revision")
		}
		if e = s.externalCheckToken(a, previous, in.ConfirmationToken); e != nil {
			return nil, 0, e
		}
		return map[string]any{"test_id": d.ID, "results_deleted": len(d.Results)}, 200, nil
	}
	if e = checkRevision(d.Revision, in.ExpectedRevision); e != nil {
		return nil, 0, e
	}
	if e = s.externalCheckToken(a, d, in.ConfirmationToken); e != nil {
		return nil, 0, e
	}
	after := d
	after.Deleted = true
	if _, e = s.externalSave(r.Context(), d, after, a, "deleted"); e != nil {
		// A concurrent identical deletion may already have selected the
		// tombstone. Verify its exact revision and the original confirmation.
		fresh, readErr := s.Store.ExternalGet(r.Context(), a.OrganizationID, d.ID)
		if readErr == nil && fresh.DivisionID == d.DivisionID && fresh.Deleted && fresh.Revision == in.ExpectedRevision+1 {
			previous := fresh
			previous.Revision--
			if tokenErr := s.externalCheckToken(a, previous, in.ConfirmationToken); tokenErr == nil {
				return map[string]any{"test_id": fresh.ID, "results_deleted": len(fresh.Results)}, 200, nil
			}
		}
		return nil, 0, e
	}
	return map[string]any{"test_id": d.ID, "results_deleted": len(d.Results)}, 200, nil
}
func (s *Server) externalResultList(r *http.Request, a Actor, _ int64) (any, int, error) {
	d, e := s.externalLoad(r, a)
	if e != nil {
		return nil, 0, e
	}
	if d.Deleted {
		return nil, 0, missing
	}
	if a.Role != "student" && !actorResults(a, false) {
		return nil, 0, denied
	}
	limit, offset, e := pagination(r)
	if e != nil {
		return nil, 0, e
	}
	account, e := externalAccountFilter(r, a)
	if e != nil {
		return nil, 0, e
	}
	rows := []ExternalResult{}
	for _, row := range d.Results {
		if account == 0 || row.AccountID == account {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].AccountID < rows[j].AccountID })
	total := int64(len(rows))
	end := offset + limit
	if end > total {
		end = total
	}
	if offset > total {
		offset = total
	}
	return map[string]any{"items": rows[offset:end], "total": total, "limit": limit, "offset": offset}, 200, nil
}
func externalAccountFilter(r *http.Request, a Actor) (int64, error) {
	var account int64
	var e error
	if raw := r.URL.Query().Get("account_id"); raw != "" {
		account, e = numericID(raw)
		if e != nil {
			return 0, e
		}
	}
	if a.Role == "student" {
		if account != 0 && account != a.AccountID {
			return 0, denied
		}
		account = a.AccountID
	}
	return account, nil
}
func (s *Server) externalResultWrite(r *http.Request, a Actor, _ int64) (any, int, error) {
	if !actorResults(a, true) {
		return nil, 0, denied
	}
	d, e := s.externalLoad(r, a)
	if e != nil {
		return nil, 0, e
	}
	if d.Deleted {
		return nil, 0, missing
	}
	account, e := numericID(r.PathValue("account"))
	if e != nil {
		return nil, 0, e
	}
	var in ExternalResultDTO
	if e = decodeExternalResult(r, &in); e != nil {
		return nil, 0, e
	}
	if !validExternalScore(in.Score) {
		return nil, 0, bad("invalid_external_score")
	}
	key := r.Header.Get("Idempotency-Key")
	if !operationKey.MatchString(key) {
		return nil, 0, bad("idempotency_key_required")
	}
	command := "manual:" + key
	hash := hashJSON([]any{a.OrganizationID, d.DivisionID, d.ID, a.AccountID, r.Method, account, in})
	for _, receipt := range d.Commands {
		if receipt.CommandID == command {
			if receipt.InputHash != hash || receipt.ActorID != a.AccountID {
				return nil, 0, conflict("idempotency_conflict")
			}
			v, e := s.externalView(r, a, d)
			return v, 200, e
		}
	}
	if e = checkRevision(d.Revision, in.ExpectedRevision); e != nil {
		return nil, 0, e
	}
	old := d.result(account)
	revision := int64(0)
	if old != nil {
		revision = old.Revision
	}
	if revision != in.ExpectedResultRevision {
		return nil, 0, conflict("stale_result_revision")
	}
	var person *ExternalPerson
	if r.Method != "DELETE" {
		people, err := s.externalPeople(r.Context(), a.OrganizationID, d.DivisionID)
		if err != nil {
			return nil, 0, err
		}
		for i := range people {
			if people[i].AccountID == account {
				person = &people[i]
				break
			}
		}
		if person == nil {
			return nil, 0, missing
		}
	}
	after := d
	after.Results = append([]ExternalResult{}, d.Results...)
	action := "result_updated"
	if r.Method == "DELETE" {
		if old == nil {
			return nil, 0, missing
		}
		action = "result_deleted"
		rows := []ExternalResult{}
		for _, row := range after.Results {
			if row.AccountID != account {
				rows = append(rows, row)
			}
		}
		after.Results = rows
	} else {
		result := ExternalResult{ID: bson.NewObjectID(), Revision: revision + 1, AccountID: account, StudentID: person.StudentID, FullName: person.FullName, Score: in.Score}
		if old != nil {
			result = *old
			result.Revision++
			result.Score = in.Score
			result.FullName = person.FullName
			result.StudentID = person.StudentID
		}
		found := false
		for i := range after.Results {
			if after.Results[i].AccountID == account {
				after.Results[i] = result
				found = true
			}
		}
		if !found {
			after.Results = append(after.Results, result)
		}
	}
	after.Commands = append(append([]ExternalCommandReceipt{}, d.Commands...), ExternalCommandReceipt{CommandID: command, InputHash: hash, ActorID: a.AccountID, SourceRevision: d.Revision + 1, Results: []ExternalResultReceipt{}})
	after, e = s.externalSave(r.Context(), d, after, a, action)
	if e != nil {
		// Resolve a concurrent retry from the winner's durable receipt;
		// never apply this mutation again or accept a conflicting command.
		fresh, readErr := s.Store.ExternalGet(r.Context(), a.OrganizationID, d.ID)
		if readErr == nil && fresh.DivisionID == d.DivisionID {
			_, replay, replayErr := externalImportReplay(fresh, command, hash, a.AccountID)
			if replayErr != nil {
				return nil, 0, replayErr
			}
			if replay {
				v, viewErr := s.externalView(r, a, fresh)
				return v, 200, viewErr
			}
		}
		return nil, 0, e
	}
	v, e := s.externalView(r, a, after)
	return v, 200, e
}
func (s *Server) externalHistory(r *http.Request, a Actor, _ int64) (any, int, error) {
	if !a.Can(false) && !actorResults(a, false) {
		return nil, 0, denied
	}
	d, e := s.externalLoad(r, a)
	if e != nil {
		return nil, 0, e
	}
	limit, offset, e := pagination(r)
	if e != nil {
		return nil, 0, e
	}
	cur, e := s.Store.ExternalVersions().Find(r.Context(), bson.M{"_id": bson.M{"$in": d.HistoryIDs}, "organization_id": a.OrganizationID, "division_id": d.DivisionID, "test_id": d.ID}, options.Find().SetSort(bson.D{{Key: "revision", Value: -1}}).SetSkip(offset).SetLimit(limit))
	if e != nil {
		return nil, 0, e
	}
	defer cur.Close(r.Context())
	items := []ExternalHistory{}
	if e = cur.All(r.Context(), &items); e != nil {
		return nil, 0, e
	}
	if !actorResults(a, false) {
		for i := range items {
			items[i].Changes = []ExternalResultChange{}
		}
	}
	return map[string]any{"items": items, "total": len(d.HistoryIDs), "limit": limit, "offset": offset}, 200, nil
}

type externalFlatResult struct {
	Test   ExternalTestDocument `bson:"test"`
	Result ExternalResult       `bson:"result"`
}

func (s *Server) externalResults(r *http.Request, a Actor, division int64) (any, int, error) {
	if a.Role != "student" && !actorResults(a, false) {
		return nil, 0, denied
	}
	account, e := externalAccountFilter(r, a)
	if e != nil {
		return nil, 0, e
	}
	limit, offset, e := pagination(r)
	if e != nil {
		return nil, 0, e
	}
	filter, e := externalFilter(r, a, division)
	if e != nil {
		return nil, 0, e
	}
	pipeline := mongo.Pipeline{{{Key: "$match", Value: filter}}, {{Key: "$unwind", Value: "$results"}}}
	if account > 0 {
		pipeline = append(pipeline, bson.D{{Key: "$match", Value: bson.M{"results.account_id": account}}})
	}
	projection := bson.M{"_id": 0, "result": "$results", "test": bson.M{"_id": "$_id", "organization_id": "$organization_id", "division_id": "$division_id", "name": "$name", "direction_id": "$direction_id", "date": "$date", "revision": "$revision", "is_deleted": "$is_deleted"}}
	rows := bson.A{bson.M{"$sort": bson.D{{Key: "date", Value: -1}, {Key: "_id", Value: -1}, {Key: "results.account_id", Value: 1}}}, bson.M{"$skip": offset}, bson.M{"$limit": limit}, bson.M{"$project": projection}}
	pipeline = append(pipeline, bson.D{{Key: "$facet", Value: bson.M{"rows": rows, "count": bson.A{bson.M{"$count": "total"}}}}})
	cur, e := s.Store.External().Aggregate(r.Context(), pipeline)
	if e != nil {
		return nil, 0, e
	}
	defer cur.Close(r.Context())
	var out struct {
		Rows  []externalFlatResult `bson:"rows"`
		Count []struct {
			Total int64 `bson:"total"`
		} `bson:"count"`
	}
	if !cur.Next(r.Context()) {
		if cur.Err() != nil {
			return nil, 0, cur.Err()
		}
		return nil, 0, unavailable
	}
	if e = cur.Decode(&out); e != nil {
		return nil, 0, e
	}
	total := int64(0)
	if len(out.Count) > 0 {
		total = out.Count[0].Total
	}
	items := []any{}
	names := map[int64]string{}
	for _, row := range out.Rows {
		d := row.Test
		// The projection represents a test with at least this result. Retain
		// that fact when deriving result-sensitive actions for the list view.
		d.Results = []ExternalResult{row.Result}
		name, ok := names[d.DirectionID]
		if !ok {
			refs, err := s.Dependencies.References(r.Context(), a.OrganizationID, []int64{division}, &d.DirectionID)
			if err != nil {
				return nil, 0, err
			}
			name = refs.Direction.Name
			names[d.DirectionID] = name
		}
		items = append(items, map[string]any{"test": externalViewWithDirection(a, d, name), "result": row.Result})
	}
	return map[string]any{"items": items, "total": total, "limit": limit, "offset": offset}, 200, nil
}
func (s *Server) externalImportAccess(r *http.Request, a Actor, org, division int64, edit bool) error {
	if org != a.OrganizationID || !actorUpload(a, edit) || !actorResults(a, edit) {
		return denied
	}
	return s.scope(r, a, division)
}
func (s *Server) externalInternalRoutes(mux *http.ServeMux) {
	secure := func(fn func(*http.Request) (any, int, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if hasImportCarrier(r.Header) {
				if e := s.operationDelivery(r); e != nil {
					fail(w, e)
					return
				}
			} else if len(s.Config.ServiceKey) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Service-Key")), []byte(s.Config.ServiceKey)) != 1 {
				fail(w, &APIError{401, "service_authentication_required"})
				return
			}
			// The domain handler performs live user introspection as well as
			// checking the requested operation, organization and division.
			data, code, e := fn(r)
			if e != nil {
				fail(w, e)
				return
			}
			reply(w, code, Envelope{true, data})
		}
	}
	mux.HandleFunc("POST /internal/tests/external-results/validate", secure(s.externalValidate))
	mux.HandleFunc("POST /internal/tests/external-results/commit", secure(s.externalCommit))
	mux.HandleFunc("POST /internal/tests/external-results/receipt", secure(s.externalImportReceiptInternal))
	mux.HandleFunc("POST /internal/tests/imports/json/validate", secure(s.jsonImportValidateInternal))
	mux.HandleFunc("POST /internal/tests/imports/json/commit", secure(s.jsonImportCommitInternal))
	mux.HandleFunc("POST /internal/tests/imports/json/receipt", secure(s.jsonImportReceiptInternal))
	mux.HandleFunc("POST /internal/tests/imports/json/prepare", s.importPrepareRoute(s.jsonImportPrepare))
	mux.HandleFunc("POST /internal/tests/external-results/prepare", s.importPrepareRoute(s.externalImportPrepare))
}
