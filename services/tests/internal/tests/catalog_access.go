package tests

import (
	"net/http"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// TestWithAccessView extends the existing catalog row without nesting the test.
// Access is always computed from live scope/reference/attempt state.
type TestWithAccessView struct {
	TestView
	Access AccessView `json:"access"`
}
type CatalogWithAccessView struct {
	Items  []TestWithAccessView `json:"items"`
	Total  int64                `json:"total"`
	Limit  int64                `json:"limit"`
	Offset int64                `json:"offset"`
}

func (s *Server) catalogAccessSpec() routeSpec {
	return routeSpec{"GET /v1/tests/divisions/{division}/catalog-with-access", "Каталог подразделения с актуальной доступностью; ученику только опубликованные тесты", s.catalogWithAccess, nil, CatalogWithAccessView{}}
}

func (s *Server) catalogWithAccess(r *http.Request, a Actor, division int64) (any, int, error) {
	filter, limit, offset, e := catalogFilter(r, a, division, false)
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
	items := []TestWithAccessView{}
	if len(docs) == 0 {
		return CatalogWithAccessView{items, total, limit, offset}, 200, nil
	}
	owners := []int64{}
	seenOwners := map[int64]bool{}
	directionIDs := []int64{}
	seenDirections := map[int64]bool{}
	testIDs := make([]bson.ObjectID, 0, len(docs))
	for _, doc := range docs {
		if doc.OrganizationID != a.OrganizationID || doc.Placement(division) == nil {
			return nil, 0, unavailable
		}
		testIDs = append(testIDs, doc.ID)
		if !seenOwners[doc.OwnerDivisionID] {
			seenOwners[doc.OwnerDivisionID] = true
			owners = append(owners, doc.OwnerDivisionID)
		}
		if !seenDirections[doc.DirectionID] {
			seenDirections[doc.DirectionID] = true
			directionIDs = append(directionIDs, doc.DirectionID)
		}
	}
	refs, e := s.Dependencies.ReferencesBatch(r.Context(), a.OrganizationID, owners, directionIDs)
	if e != nil {
		return nil, 0, e
	}
	ownerByID := map[int64]Reference{}
	for _, owner := range refs.Divisions {
		ownerByID[owner.ID] = owner
	}
	// One request-scoped batch keeps renames and archive/restore changes live
	// without making a Core round trip for each direction on the page.
	directions := map[int64]Reference{}
	for _, ref := range refs.Directions {
		directions[ref.ID] = ref
	}
	attempts := map[bson.ObjectID]Attempt{}
	if a.Role == "student" {
		attempts, e = s.Store.catalogAttempts(r, a, division, testIDs)
		if e != nil {
			return nil, 0, e
		}
	}
	now := time.Now()
	for _, doc := range docs {
		var attempt *Attempt
		if state, found := attempts[doc.ID]; found {
			attempt = &state
		}
		direction := directions[doc.DirectionID]
		view := buildTestView(a, doc, division, ownerByID[doc.OwnerDivisionID], direction)
		access := accessState(doc, *doc.Placement(division), direction.Active, now, a.Role, attempt)
		items = append(items, TestWithAccessView{view, access})
	}
	return CatalogWithAccessView{items, total, limit, offset}, 200, nil
}

func (s *Store) catalogAttempts(r *http.Request, a Actor, division int64, testIDs []bson.ObjectID) (map[bson.ObjectID]Attempt, error) {
	filter := bson.M{"organization_id": a.OrganizationID, "division_id": division, "account_id": a.AccountID, "test_id": bson.M{"$in": testIDs}, "practice": false, "active": true}
	projection := bson.M{"_id": 1, "organization_id": 1, "division_id": 1, "account_id": 1, "test_id": 1, "practice": 1, "active": 1, "status": 1, "answer_deadline": 1, "upload_deadline": 1, "submission_id": 1, "calculation_status": 1}
	cur, e := s.Attempts().Find(r.Context(), filter, options.Find().SetProjection(projection))
	if e != nil {
		return nil, e
	}
	defer cur.Close(r.Context())
	states := []Attempt{}
	if e = cur.All(r.Context(), &states); e != nil {
		return nil, e
	}
	allowed := map[bson.ObjectID]bool{}
	for _, id := range testIDs {
		allowed[id] = true
	}
	out := map[bson.ObjectID]Attempt{}
	for _, state := range states {
		if state.OrganizationID != a.OrganizationID || state.DivisionID != division || state.AccountID != a.AccountID || state.Practice || !state.Active || !allowed[state.TestID] {
			return nil, unavailable
		}
		if _, duplicate := out[state.TestID]; duplicate {
			return nil, unavailable
		}
		out[state.TestID] = state
	}
	return out, nil
}

func accessState(doc Document, p Placement, directionActive bool, now time.Time, role string, state *Attempt) AccessView {
	out := availability(doc, p, directionActive, now)
	out.Scope = "test_content_and_attempt"
	out.AttemptStateChecked, out.ContentChecked = true, true
	if len(doc.Questions) == 0 && out.OfficialAccessAllowed {
		out.OfficialAccessAllowed = false
		out.OfficialReason = "test_has_no_questions"
	}
	if role != "student" {
		return out
	}
	if state != nil {
		out.AttemptID = &state.ID
		out.SubmissionID = state.SubmissionID
		out.CalculationStatus = state.CalculationStatus
		if state.Status == "accepted" {
			out.OfficialAccessAllowed = false
			out.OfficialReason = "already_submitted"
			out.ReviewAccessAllowed = out.ReviewAccessAllowed && state.CalculationStatus == "ready"
			if state.CalculationStatus != "ready" && out.ReviewReason == "allowed" {
				out.ReviewReason = "calculation_pending"
			}
		} else if !doc.Archived && directionActive && p.Settings.Published {
			out.ReviewAccessAllowed = false
			if out.ReviewReason == "allowed" {
				out.ReviewReason = "no_submission"
			}
			out.OfficialAccessAllowed = !now.After(state.AnswerDeadline) && !now.After(state.UploadDeadline)
			if now.After(state.UploadDeadline) {
				out.OfficialReason = "upload_deadline_passed"
			} else if out.OfficialAccessAllowed {
				out.OfficialReason = "resume_attempt"
			} else {
				out.OfficialReason = "pending_upload"
			}
		}
	}
	if state == nil || state.Status != "accepted" {
		out.ReviewAccessAllowed = false
		if out.ReviewReason == "allowed" {
			out.ReviewReason = "no_submission"
		}
	}
	out.PracticeAccessAllowed = !doc.Archived && directionActive && p.Settings.Published && p.Settings.AnswersVisible && (out.SubmissionID != nil || p.Settings.EndAt != nil && now.After(*p.Settings.EndAt)) && len(doc.Questions) > 0
	if out.PracticeAccessAllowed {
		out.PracticeReason = "allowed"
	} else {
		out.PracticeReason = "practice_unavailable"
	}
	return out
}
