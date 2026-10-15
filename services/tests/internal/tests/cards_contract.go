package tests

import (
	"crypto/subtle"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"net/http"
	"strconv"
	"time"
)

// Cards reads live content only after tests has authorized answer disclosure.
// Authorized divisions come exclusively from current auth introspection.
type CardsProjection struct {
	TestID      bson.ObjectID `json:"test_id"`
	DirectionID int64         `json:"direction_id"`
	Title       string        `json:"title"`
	VersionID   bson.ObjectID `json:"content_version_id"`
	CardsCount  int           `json:"cards_count"`
	Visible     bool          `json:"visible"`
	Questions   []Question    `json:"questions,omitempty"`
}

func cardsActor(a Actor) bool {
	if a.OrganizationAdmin && a.Role == "admin" {
		return true
	}
	if a.Role == "student" {
		return true
	}
	for _, key := range []string{"cards", "training", "train"} {
		p := a.Permissions[key]
		if a.Role == "staff_admin" && (p.View || p.Edit) {
			return true
		}
	}
	return false
}
func cardsDivisions(a Actor) []int64 {
	if a.Role == "student" || a.OrganizationAdmin {
		return a.DivisionIDs
	}
	if a.DivisionID != nil {
		return []int64{*a.DivisionID}
	}
	return nil
}
func (s *Server) cardsAllowed(r *http.Request, a Actor, t Document) (bool, error) {
	if !cardsActor(a) || t.OrganizationID != a.OrganizationID {
		return false, nil
	}
	for _, d := range cardsDivisions(a) {
		p := t.Placement(d)
		if p == nil {
			continue
		}
		refs, e := s.Dependencies.References(r.Context(), a.OrganizationID, []int64{d}, &t.DirectionID)
		if e != nil {
			return false, e
		}
		if !refs.Divisions[0].Active {
			continue
		}
		allowed, e := s.practiceEligible(r, a.AccountID, a.OrganizationID, d, t, *p, refs.Direction.Active)
		if e != nil {
			return false, e
		}
		if allowed {
			return true, nil
		}
	}
	return false, nil
}
func (s *Server) cardsRoutes(mux *http.ServeMux) {
	secure := func(fn handler) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if len(s.Config.ServiceKey) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Service-Key")), []byte(s.Config.ServiceKey)) != 1 {
				fail(w, &APIError{401, "invalid_service_key"})
				return
			}
			a, e := s.Dependencies.Identify(r)
			if e != nil {
				fail(w, e)
				return
			}
			if !cardsActor(a) {
				fail(w, denied)
				return
			}
			out, code, e := fn(r, a, 0)
			if e != nil {
				fail(w, e)
				return
			}
			reply(w, code, Envelope{true, out})
		}
	}
	mux.HandleFunc("POST /internal/tests/cards/digests", secure(s.cardsDigests))
	mux.HandleFunc("GET /internal/tests/cards/catalog", secure(func(r *http.Request, a Actor, _ int64) (any, int, error) {
		out := []CardsProjection{}
		if r.URL.Query().Get("admin") == "true" && a.Role == "student" {
			return nil, 0, denied
		}
		if len(cardsDivisions(a)) == 0 {
			return out, 200, nil
		}
		filter := bson.M{"organization_id": a.OrganizationID, "is_archived": false, "placements.division_id": bson.M{"$in": cardsDivisions(a)}}
		if raw := r.URL.Query().Get("direction_id"); raw != "" {
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || id < 1 {
				return nil, 0, bad("invalid_direction")
			}
			filter["direction_id"] = id
		}
		opts := options.Find().SetLimit(1001).SetSort(bson.D{{Key: "_id", Value: 1}})
		metadata := r.URL.Query().Get("metadata") == "true"
		if metadata {
			opts.SetProjection(bson.M{"_id": 1, "organization_id": 1, "direction_id": 1, "title": 1, "placements": 1, "content_version_id": 1, "is_archived": 1})
		}
		cur, e := s.Store.Collection.Find(r.Context(), filter, opts)
		if e != nil {
			return nil, 0, e
		}
		defer cur.Close(r.Context())
		docs := []Document{}
		for cur.Next(r.Context()) {
			var doc Document
			if e = cur.Decode(&doc); e != nil {
				return nil, 0, e
			}
			docs = append(docs, doc)
			if len(docs) > 1000 {
				return nil, 0, bad("catalog_limit_exceeded")
			}
		}
		if e = cur.Err(); e != nil {
			return nil, 0, e
		}
		if r.URL.Query().Get("admin") != "true" {
			projections, err := s.cardsCatalog(r, a, docs, !metadata && r.URL.Query().Get("include_questions") == "true")
			return projections, 200, err
		}
		for _, t := range docs {
			published, visible := false, false
			for _, d := range cardsDivisions(a) {
				if p := t.Placement(d); p != nil && p.Settings.Published {
					published = true
					visible = visible || p.Settings.AnswersVisible
				}
			}
			if published {
				out = append(out, CardsProjection{TestID: t.ID, DirectionID: t.DirectionID, Title: t.Title, VersionID: t.ContentVersionID, CardsCount: len(t.Questions), Visible: visible})
			}
		}
		return out, 200, cur.Err()
	}))
	mux.HandleFunc("GET /internal/tests/cards/tests/{test}", secure(func(r *http.Request, a Actor, _ int64) (any, int, error) {
		t, e := s.load(r, a)
		if e != nil {
			return nil, 0, e
		}
		ok, e := s.cardsAllowed(r, a, t)
		if e != nil {
			return nil, 0, e
		}
		if !ok {
			return nil, 0, &APIError{403, "answers_hidden"}
		}
		return CardsProjection{TestID: t.ID, DirectionID: t.DirectionID, Title: t.Title, VersionID: t.ContentVersionID, CardsCount: len(t.Questions), Visible: true, Questions: t.Questions}, 200, nil
	}))
	mux.HandleFunc("POST /internal/tests/cards/drafts", secure(func(r *http.Request, a Actor, _ int64) (any, int, error) {
		if !a.Can(true) || a.DivisionID == nil {
			return nil, 0, denied
		}
		d := *a.DivisionID
		if e := s.scope(r, a, d); e != nil {
			return nil, 0, e
		}
		var in DraftDTO
		if e := decode(r, &in); e != nil {
			return nil, 0, e
		}
		if in.DirectionID < 1 {
			return nil, 0, bad("invalid_direction")
		}
		refs, e := s.Dependencies.References(r.Context(), a.OrganizationID, []int64{d}, &in.DirectionID)
		if e != nil {
			return nil, 0, e
		}
		if !refs.Direction.Active {
			return nil, 0, missing
		}
		return s.insertDraft(r, a, d, in, nil, in)
	}))
}

// Access is fresh and request-scoped. Batch the same references and official
// attempt states used by cardsAllowed; never cache one learner's eligibility.
func (s *Server) cardsCatalog(r *http.Request, a Actor, docs []Document, includeQuestions bool) ([]CardsProjection, error) {
	out := []CardsProjection{}
	if len(docs) == 0 {
		return out, nil
	}
	divisions := cardsDivisions(a)
	directions := []int64{}
	seen := map[int64]bool{}
	for _, doc := range docs {
		if doc.OrganizationID != a.OrganizationID {
			return nil, unavailable
		}
		if !seen[doc.DirectionID] {
			directions = append(directions, doc.DirectionID)
			seen[doc.DirectionID] = true
		}
	}
	refs, err := s.Dependencies.ReferencesBatch(r.Context(), a.OrganizationID, divisions, directions)
	if err != nil {
		return nil, err
	}
	activeDirections := map[int64]bool{}
	for _, ref := range refs.Directions {
		activeDirections[ref.ID] = ref.Active
	}
	allowed := map[bson.ObjectID]bool{}
	now := time.Now()
	for _, division := range refs.Divisions {
		if !division.Active {
			continue
		}
		candidates := []bson.ObjectID{}
		for _, doc := range docs {
			p := doc.Placement(division.ID)
			if p == nil || !availability(doc, *p, activeDirections[doc.DirectionID], now).ReviewAccessAllowed {
				continue
			}
			if p.Settings.EndAt != nil && now.After(*p.Settings.EndAt) {
				allowed[doc.ID] = true
				continue
			}
			candidates = append(candidates, doc.ID)
		}
		if len(candidates) == 0 {
			continue
		}
		attempts, err := s.Store.catalogAttempts(r, a, division.ID, candidates)
		if err != nil {
			return nil, err
		}
		for id, attempt := range attempts {
			if attempt.Status == "accepted" {
				allowed[id] = true
			}
		}
	}
	for _, doc := range docs {
		if !allowed[doc.ID] {
			continue
		}
		item := CardsProjection{TestID: doc.ID, DirectionID: doc.DirectionID, Title: doc.Title, VersionID: doc.ContentVersionID, CardsCount: len(doc.Questions), Visible: true}
		if includeQuestions {
			item.Questions = doc.Questions
		}
		out = append(out, item)
	}
	return out, nil
}
