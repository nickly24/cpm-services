package tests

import (
	"crypto/subtle"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"net/http"
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
	mux.HandleFunc("GET /internal/tests/cards/catalog", secure(func(r *http.Request, a Actor, _ int64) (any, int, error) {
		out := []CardsProjection{}
		if r.URL.Query().Get("admin") == "true" && a.Role == "student" {
			return nil, 0, denied
		}
		if len(cardsDivisions(a)) == 0 {
			return out, 200, nil
		}
		cur, e := s.Store.Collection.Find(r.Context(), bson.M{"organization_id": a.OrganizationID, "is_archived": false, "placements.division_id": bson.M{"$in": cardsDivisions(a)}}, options.Find().SetLimit(1001).SetSort(bson.D{{Key: "_id", Value: 1}}))
		if e != nil {
			return nil, 0, e
		}
		defer cur.Close(r.Context())
		count := 0
		for cur.Next(r.Context()) {
			count++
			if count > 1000 {
				return nil, 0, bad("catalog_limit_exceeded")
			}
			var t Document
			if e = cur.Decode(&t); e != nil {
				return nil, 0, e
			}
			if r.URL.Query().Get("admin") == "true" {
				if a.Role == "student" {
					return nil, 0, denied
				}
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
				continue
			}
			ok, e := s.cardsAllowed(r, a, t)
			if e != nil {
				return nil, 0, e
			}
			if ok {
				out = append(out, CardsProjection{TestID: t.ID, DirectionID: t.DirectionID, Title: t.Title, VersionID: t.ContentVersionID, CardsCount: len(t.Questions), Visible: true})
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
