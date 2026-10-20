package tests

import (
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type ListView struct {
	Items  []TestView `json:"items"`
	Total  int64      `json:"total"`
	Limit  int64      `json:"limit"`
	Offset int64      `json:"offset"`
}

func pagination(r *http.Request) (int64, int64, error) {
	limit, offset := int64(20), int64(0)
	var e error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, e = strconv.ParseInt(raw, 10, 64)
		if e != nil {
			return 0, 0, bad("invalid_pagination")
		}
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, e = strconv.ParseInt(raw, 10, 64)
		if e != nil {
			return 0, 0, bad("invalid_pagination")
		}
	}
	if limit < 1 || limit > 100 || offset < 0 || offset > 100000 {
		return 0, 0, bad("invalid_pagination")
	}
	return limit, offset, nil
}
func (s *Server) target(r *http.Request, a Actor) (int64, error) {
	if raw := r.URL.Query().Get("target_division_id"); raw != "" {
		d, e := numericID(raw)
		if e != nil {
			return 0, e
		}
		if e = s.scope(r, a, d); e != nil {
			return 0, e
		}
		return d, nil
	}
	if a.DivisionID != nil {
		if e := s.scope(r, a, *a.DivisionID); e != nil {
			return 0, e
		}
		return *a.DivisionID, nil
	}
	return 0, nil
}
func catalogFilter(r *http.Request, a Actor, d int64, bank bool) (bson.M, int64, int64, error) {
	if bank && !a.Can(false) || !bank && !a.Can(false) && a.Role != "student" {
		return nil, 0, 0, denied
	}
	limit, offset, e := pagination(r)
	if e != nil {
		return nil, 0, 0, e
	}
	filter := bson.M{"organization_id": a.OrganizationID}
	if bank {
		if !a.OrganizationAdmin {
			visibility := bson.A{bson.M{"bank_available": true, "is_archived": false}}
			if a.DivisionID != nil {
				visibility = append(visibility, bson.M{"placements.division_id": *a.DivisionID})
			}
			filter["$or"] = visibility
		}
	} else {
		filter["placements.division_id"] = d
		if a.Role == "student" {
			filter["is_archived"] = false
			filter["placements"] = bson.M{"$elemMatch": bson.M{"division_id": d, "settings.published": true}}
		}
	}
	query := r.URL.Query()
	if q := strings.TrimSpace(query.Get("q")); q != "" {
		if !validText(q, 100, false) {
			return nil, 0, 0, bad("invalid_search")
		}
		filter["title"] = bson.Regex{Pattern: regexp.QuoteMeta(q), Options: "i"}
	}
	for _, key := range []string{"direction_id", "owner_division_id"} {
		if raw := query.Get(key); raw != "" {
			n, e := numericID(raw)
			if e != nil {
				return nil, 0, 0, e
			}
			filter[key] = n
		}
	}
	if raw := query.Get("is_archived"); raw != "" {
		if raw != "true" && raw != "false" {
			return nil, 0, 0, bad("invalid_archive_filter")
		}
		if a.Role == "student" && raw == "true" {
			return nil, 0, 0, denied
		}
		filter["is_archived"] = raw == "true"
	} else if bank {
		filter["is_archived"] = false
	}
	// Keep explicit owner filters when applying a relation; never overwrite them.
	conditions := bson.A{}
	switch query.Get("relation") {
	case "", "all":
	case "owned":
		if d < 1 {
			return nil, 0, 0, bad("target_division_required")
		}
		conditions = append(conditions, bson.M{"owner_division_id": d})
	case "attached":
		if d < 1 {
			return nil, 0, 0, bad("target_division_required")
		}
		filter["placements.division_id"] = d
		conditions = append(conditions, bson.M{"owner_division_id": bson.M{"$ne": d}})
	case "copy":
		filter["origin"] = bson.M{"$ne": nil}
	default:
		return nil, 0, 0, bad("invalid_relation")
	}
	if len(conditions) > 0 {
		filter["$and"] = conditions
	}
	return filter, limit, offset, nil
}

func (s *Server) list(r *http.Request, a Actor, d int64, bank bool) (any, int, error) {
	if r.URL.Query().Get("view") == "admin" {
		return s.adminCatalog(r, a, d, bank)
	}
	filter, limit, offset, e := catalogFilter(r, a, d, bank)
	if e != nil {
		return nil, 0, e
	}
	total, e := s.Store.Collection.CountDocuments(r.Context(), filter)
	if e != nil {
		return nil, 0, e
	}
	docs, e := s.Store.List(r.Context(), filter, offset, limit)
	if e != nil {
		return nil, 0, e
	}
	items := []TestView{}
	for _, doc := range docs {
		view, e := s.view(r, a, doc, d)
		if e != nil {
			return nil, 0, e
		}
		items = append(items, view)
	}
	return ListView{items, total, limit, offset}, 200, nil
}
func (s *Server) bank(r *http.Request, a Actor, _ int64) (any, int, error) {
	if !a.Can(false) {
		return nil, 0, denied
	}
	d, e := s.target(r, a)
	if e != nil {
		return nil, 0, e
	}
	return s.list(r, a, d, true)
}
func (s *Server) divisionList(r *http.Request, a Actor, d int64) (any, int, error) {
	return s.list(r, a, d, false)
}
func (s *Server) detail(r *http.Request, a Actor, _ int64) (any, int, error) {
	doc, e := s.load(r, a)
	if e != nil {
		return nil, 0, e
	}
	target, e := s.target(r, a)
	if e != nil {
		return nil, 0, e
	}
	if a.Role == "student" {
		p := doc.Placement(target)
		if p == nil || !p.Settings.Published || doc.Archived {
			return nil, 0, missing
		}
	} else if !doc.Readable(a) {
		return nil, 0, missing
	}
	view, e := s.view(r, a, doc, target)
	return view, 200, e
}
func (s *Server) activeReferences(r *http.Request, a Actor, direction int64, division int64) error {
	refs, e := s.Dependencies.References(r.Context(), a.OrganizationID, []int64{division}, &direction)
	if e != nil {
		return e
	}
	if !refs.Divisions[0].Active {
		return missing
	}
	if !refs.Direction.Active {
		return conflict("direction_archived")
	}
	return nil
}
func (s *Server) mutationResult(r *http.Request, a Actor, doc Document, d int64, code int) (any, int, error) {
	view, e := s.view(r, a, doc, d)
	return view, code, e
}
func (s *Server) replay(r *http.Request, a Actor, id bson.ObjectID, hash string, d int64) (any, int, error, bool) {
	old, e := s.Store.Get(r.Context(), a.OrganizationID, id)
	if errors.Is(e, missing) {
		return nil, 0, nil, false
	}
	if e != nil {
		return nil, 0, e, true
	}
	if old.OperationHash != hash {
		return nil, 0, conflict("idempotency_conflict"), true
	}
	if old.OwnerDivisionID != d || !old.Editable(a) {
		return nil, 0, denied, true
	}
	view, e := s.view(r, a, old, d)
	return view, 200, e, true
}
func (s *Server) create(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can(true) {
		return nil, 0, denied
	}
	var in CreateDTO
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	if e := validateMetadata(in); e != nil {
		return nil, 0, e
	}
	if e := validateQuestions(in.Questions, true); e != nil {
		return nil, 0, e
	}
	id, hash, e := operation(r, a, in)
	if e != nil {
		return nil, 0, e
	}
	if body, code, e, found := s.replay(r, a, id, hash, d); found {
		return body, code, e
	}
	if e = s.activeReferences(r, a, in.DirectionID, d); e != nil {
		return nil, 0, e
	}
	now := time.Now().UTC()
	doc := Document{ID: id, OrganizationID: a.OrganizationID, OwnerDivisionID: d, DirectionID: in.DirectionID, Title: strings.TrimSpace(in.Title), Description: strings.TrimSpace(in.Description), Revision: 1, Placements: []Placement{{DivisionID: d, Settings: Settings{}, AddedBy: a.AccountID, AddedAt: now, UpdatedBy: a.AccountID, UpdatedAt: now}}, CreatedBy: a.AccountID, CreatedAt: now, UpdatedBy: a.AccountID, UpdatedAt: now, OperationHash: hash}
	if e = s.initialContent(r.Context(), &doc, in.Questions, a); e != nil {
		return nil, 0, e
	}
	doc, repeated, e := s.Store.Insert(r.Context(), doc)
	if e != nil {
		return nil, 0, e
	}
	code := 201
	if repeated {
		code = 200
	}
	return s.mutationResult(r, a, doc, d, code)
}
func (s *Server) metadata(r *http.Request, a Actor, _ int64) (any, int, error) {
	doc, e := s.load(r, a)
	if e != nil {
		return nil, 0, e
	}
	if !doc.Editable(a) {
		return nil, 0, denied
	}
	if doc.Archived {
		return nil, 0, conflict("test_archived")
	}
	var in MetadataDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	if e = validateMetadata(in.CreateDTO); e != nil {
		return nil, 0, e
	}
	if e = checkRevision(doc.Revision, in.ExpectedRevision); e != nil {
		return nil, 0, e
	}
	if e = s.activeReferences(r, a, in.DirectionID, doc.OwnerDivisionID); e != nil {
		return nil, 0, e
	}
	next := doc
	next.Title = strings.TrimSpace(in.Title)
	next.Description = strings.TrimSpace(in.Description)
	next.DirectionID = in.DirectionID
	if in.Questions != nil {
		if e = validateQuestions(in.Questions, true); e != nil {
			return nil, 0, e
		}
		if len(in.Questions) == 0 {
			for _, p := range doc.Placements {
				if p.Settings.Published {
					return nil, 0, conflict("published_test_requires_questions")
				}
			}
		}
		next.Questions = normalizeQuestions(in.Questions)
	}
	doc, e = s.saveContent(r.Context(), doc, next, in.ExpectedRevision, a)
	if e != nil {
		return nil, 0, e
	}
	return s.mutationResult(r, a, doc, doc.OwnerDivisionID, 200)
}
func (s *Server) setBank(r *http.Request, a Actor, _ int64) (any, int, error) {
	doc, e := s.load(r, a)
	if e != nil {
		return nil, 0, e
	}
	if !doc.Editable(a) {
		return nil, 0, denied
	}
	if doc.Archived {
		return nil, 0, conflict("test_archived")
	}
	var in BankDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	if in.Available == nil {
		return nil, 0, bad("available_required")
	}
	if e = checkRevision(doc.Revision, in.ExpectedRevision); e != nil {
		return nil, 0, e
	}
	doc.BankAvailable = *in.Available
	doc, e = s.Store.Save(r.Context(), doc, in.ExpectedRevision, a.AccountID)
	if e != nil {
		return nil, 0, e
	}
	return s.mutationResult(r, a, doc, doc.OwnerDivisionID, 200)
}
func (s *Server) archive(r *http.Request, a Actor, _ int64) (any, int, error) {
	doc, e := s.load(r, a)
	if e != nil {
		return nil, 0, e
	}
	if !doc.Editable(a) {
		return nil, 0, denied
	}
	var in ArchiveDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	if in.Archived == nil {
		return nil, 0, bad("archive_flag_required")
	}
	if e = checkRevision(doc.Revision, in.ExpectedRevision); e != nil {
		return nil, 0, e
	}
	doc.Archived = *in.Archived
	doc, e = s.Store.Save(r.Context(), doc, in.ExpectedRevision, a.AccountID)
	if e != nil {
		return nil, 0, e
	}
	return s.mutationResult(r, a, doc, doc.OwnerDivisionID, 200)
}
func (s *Server) attach(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can(true) {
		return nil, 0, denied
	}
	doc, e := s.load(r, a)
	if e != nil {
		return nil, 0, e
	}
	if !doc.Readable(a) {
		return nil, 0, missing
	}
	if doc.Archived {
		return nil, 0, conflict("test_archived")
	}
	var in RevisionDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	if in.ExpectedRevision < 1 {
		return nil, 0, bad("expected_revision_required")
	}
	if doc.Placement(d) != nil {
		return s.mutationResult(r, a, doc, d, 200)
	}
	if e = checkRevision(doc.Revision, in.ExpectedRevision); e != nil {
		return nil, 0, e
	}
	if len(doc.Placements) >= 100 {
		return nil, 0, conflict("too_many_divisions")
	}
	if e = s.activeReferences(r, a, doc.DirectionID, d); e != nil {
		return nil, 0, e
	}
	now := time.Now().UTC()
	doc.Placements = append(doc.Placements, Placement{DivisionID: d, AddedBy: a.AccountID, AddedAt: now, UpdatedBy: a.AccountID, UpdatedAt: now})
	doc, e = s.Store.Save(r.Context(), doc, in.ExpectedRevision, a.AccountID)
	if e != nil {
		return nil, 0, e
	}
	return s.mutationResult(r, a, doc, d, 200)
}
func (s *Server) copyTest(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can(true) {
		return nil, 0, denied
	}
	sourceID, e := testID(r)
	if e != nil {
		return nil, 0, e
	}
	var in CopyDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	if in.ExpectedRevision < 1 || !validText(in.Title, 200, false) {
		return nil, 0, bad("invalid_copy")
	}
	id, hash, e := operation(r, a, in)
	if e != nil {
		return nil, 0, e
	}
	if body, code, e, found := s.replay(r, a, id, hash, d); found {
		return body, code, e
	}
	source, e := s.Store.Get(r.Context(), a.OrganizationID, sourceID)
	if e != nil {
		return nil, 0, e
	}
	if !source.Readable(a) {
		return nil, 0, missing
	}
	if source.Archived {
		return nil, 0, conflict("test_archived")
	}
	if e = checkRevision(source.Revision, in.ExpectedRevision); e != nil {
		return nil, 0, e
	}
	if e = s.activeReferences(r, a, source.DirectionID, d); e != nil {
		return nil, 0, e
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = source.Title
	}
	now := time.Now().UTC()
	doc := Document{ID: id, OrganizationID: a.OrganizationID, OwnerDivisionID: d, DirectionID: source.DirectionID, Title: title, Description: source.Description, Revision: 1, Origin: &Origin{source.ID, source.Revision, source.OwnerDivisionID, now}, Placements: []Placement{{DivisionID: d, AddedBy: a.AccountID, AddedAt: now, UpdatedBy: a.AccountID, UpdatedAt: now}}, CreatedBy: a.AccountID, CreatedAt: now, UpdatedBy: a.AccountID, UpdatedAt: now, OperationHash: hash}
	if e = s.initialContent(r.Context(), &doc, source.Questions, a); e != nil {
		return nil, 0, e
	}
	doc, repeated, e := s.Store.Insert(r.Context(), doc)
	if e != nil {
		return nil, 0, e
	}
	code := 201
	if repeated {
		code = 200
	}
	return s.mutationResult(r, a, doc, d, code)
}
func (s *Server) settings(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can(true) {
		return nil, 0, denied
	}
	doc, e := s.load(r, a)
	if e != nil {
		return nil, 0, e
	}
	p := doc.Placement(d)
	if p == nil {
		return nil, 0, missing
	}
	if doc.Archived {
		return nil, 0, conflict("test_archived")
	}
	var in SettingsDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	if e = validateSettings(in.Settings); e != nil {
		return nil, 0, e
	}
	if in.Published && len(doc.Questions) == 0 {
		return nil, 0, conflict("publication_requires_questions")
	}
	if e = checkRevision(doc.Revision, in.ExpectedRevision); e != nil {
		return nil, 0, e
	}
	if e = s.activeReferences(r, a, doc.DirectionID, d); e != nil {
		return nil, 0, e
	}
	p.Settings = in.Settings
	if p.Settings.StartAt != nil {
		start, end := p.Settings.StartAt.UTC(), p.Settings.EndAt.UTC()
		p.Settings.StartAt = &start
		p.Settings.EndAt = &end
	}
	p.UpdatedBy = a.AccountID
	p.UpdatedAt = time.Now().UTC()
	doc, e = s.Store.Save(r.Context(), doc, in.ExpectedRevision, a.AccountID)
	if e != nil {
		return nil, 0, e
	}
	return s.mutationResult(r, a, doc, d, 200)
}
func (s *Server) detach(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can(true) {
		return nil, 0, denied
	}
	doc, e := s.load(r, a)
	if e != nil {
		return nil, 0, e
	}
	if doc.Placement(d) == nil {
		return nil, 0, missing
	}
	if doc.OwnerDivisionID == d {
		return nil, 0, conflict("cannot_detach_owner")
	}
	var in RevisionDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	if e = checkRevision(doc.Revision, in.ExpectedRevision); e != nil {
		return nil, 0, e
	}
	placements := []Placement{}
	for _, p := range doc.Placements {
		if p.DivisionID != d {
			placements = append(placements, p)
		}
	}
	doc.Placements = placements
	doc, e = s.Store.Save(r.Context(), doc, in.ExpectedRevision, a.AccountID)
	if e != nil {
		return nil, 0, e
	}
	return struct {
		TestID     bson.ObjectID `json:"test_id"`
		DivisionID int64         `json:"division_id"`
		Revision   int64         `json:"revision"`
	}{doc.ID, d, doc.Revision}, 200, nil
}
func (s *Server) access(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can(false) && a.Role != "student" {
		return nil, 0, denied
	}
	doc, e := s.load(r, a)
	if e != nil {
		return nil, 0, e
	}
	p := doc.Placement(d)
	if p == nil {
		return nil, 0, missing
	}
	refs, e := s.Dependencies.References(r.Context(), a.OrganizationID, nil, &doc.DirectionID)
	if e != nil {
		return nil, 0, e
	}
	var state *Attempt
	if a.Role == "student" {
		var attempt Attempt
		err := s.Store.Attempts().FindOne(r.Context(), attemptFilter(a, d, doc.ID, false)).Decode(&attempt)
		if err == nil {
			state = &attempt
		} else if !errors.Is(err, mongo.ErrNoDocuments) {
			return nil, 0, err
		}
	}
	return accessState(doc, *p, refs.Direction.Active, time.Now(), a.Role, state), 200, nil
}
