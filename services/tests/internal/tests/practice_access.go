package tests

import (
	"errors"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"net/http"
	"time"
)

func (s *Server) practiceEligible(r *http.Request, account, organization, division int64, test Document, placement Placement, directionActive bool) (bool, error) {
	if !availability(test, placement, directionActive, time.Now()).ReviewAccessAllowed {
		return false, nil
	}
	var official Attempt
	e := s.Store.Attempts().FindOne(r.Context(), attemptFilter(Actor{AccountID: account, OrganizationID: organization}, division, test.ID, false)).Decode(&official)
	if e != nil && !errors.Is(e, mongo.ErrNoDocuments) {
		return false, e
	}
	return e == nil && official.Status == "accepted" || placement.Settings.EndAt != nil && time.Now().After(*placement.Settings.EndAt), nil
}
func (s *Server) checkPracticeAccess(r *http.Request, a Attempt) error {
	test, e := s.Store.Get(r.Context(), a.OrganizationID, a.TestID)
	if e != nil {
		return e
	}
	placement := test.Placement(a.DivisionID)
	if placement == nil {
		return missing
	}
	refs, e := s.Dependencies.References(r.Context(), a.OrganizationID, []int64{a.DivisionID}, &test.DirectionID)
	if e != nil {
		return e
	}
	allowed, e := s.practiceEligible(r, a.AccountID, a.OrganizationID, a.DivisionID, test, *placement, refs.Direction.Active)
	if e != nil {
		return e
	}
	if !allowed {
		return denied
	}
	return nil
}
