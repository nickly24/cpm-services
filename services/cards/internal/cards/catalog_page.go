package cards

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type CatalogEntry struct {
	Kind        string `json:"kind"`
	Ref         string `json:"refId"`
	Name        string `json:"name"`
	DirectionID int64  `json:"direction_id"`
	Stats       Stats  `json:"stats"`
}
type CatalogStatsRequest struct {
	Sections []CatalogKey `json:"sections"`
}
type CatalogKey struct {
	Kind string `json:"kind"`
	Ref  string `json:"refId"`
}

func (s *Server) catalogDirections(r *http.Request, a Actor, scope Scope) (any, error) {
	if a.Role != "student" {
		return nil, denied
	}
	return map[string]any{"directions": scope.Directions}, nil
}
func (s *Server) catalogMetadata(r *http.Request, a Actor, scope Scope) (any, error) {
	if a.Role != "student" {
		return nil, denied
	}
	id, err := number(r.PathValue("direction"))
	if err != nil {
		return nil, err
	}
	if !scope.Allows(id) {
		return nil, missing
	}
	rows, err := s.DB.QueryContext(r.Context(), "SELECT id,name FROM cards_themes_v1 WHERE organization_id=? AND direction_id=? ORDER BY id LIMIT 1001", a.OrganizationID, id)
	if err != nil {
		return nil, err
	}
	out := []CatalogEntry{}
	for rows.Next() {
		var ref int64
		var name string
		if err = rows.Scan(&ref, &name); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, CatalogEntry{Kind: "manual", Ref: fmt.Sprint(ref), Name: name, DirectionID: id})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(out) > 1000 {
		return nil, bad("catalog_limit_exceeded")
	}
	var tests struct {
		Status bool         `json:"status"`
		Data   []Projection `json:"data"`
	}
	err = s.Deps.call(r, "GET", s.Deps.Config.TestsURL+"/internal/tests/cards/catalog?metadata=true&direction_id="+url.QueryEscape(fmt.Sprint(id)), nil, &tests, "")
	if err != nil {
		return nil, err
	}
	if !tests.Status {
		return nil, unavailable
	}
	for _, p := range tests.Data {
		if p.DirectionID != id || !testIDPattern.MatchString(p.TestID) || !p.Visible {
			return nil, unavailable
		}
		out = append(out, CatalogEntry{Kind: "test", Ref: p.TestID, Name: p.Title, DirectionID: id})
	}
	return out, nil
}
func (s *Server) catalogStats(r *http.Request, a Actor, scope Scope) (any, error) {
	if a.Role != "student" {
		return nil, denied
	}
	var in CatalogStatsRequest
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	if len(in.Sections) < 1 || len(in.Sections) > 50 {
		return nil, bad("invalid_page_size")
	}
	tests := []string{}
	seen := map[string]bool{}
	// The page owner reauthorizes every key; client metadata is never authority.
	for _, k := range in.Sections {
		key := k.Kind + ":" + k.Ref
		if seen[key] {
			return nil, bad("duplicate_section")
		}
		seen[key] = true
		switch k.Kind {
		case "manual":
			if _, err := number(k.Ref); err != nil {
				return nil, err
			}
		case "test":
			if !testIDPattern.MatchString(k.Ref) {
				return nil, bad("invalid_test_id")
			}
			tests = append(tests, k.Ref)
		default:
			return nil, bad("unsupported_section_kind")
		}
	}
	type digest struct {
		Ref         string `json:"card_ref"`
		Fingerprint string `json:"content_fingerprint"`
	}
	byTest := map[string][]digest{}
	if len(tests) > 0 {
		var result struct {
			Status bool `json:"status"`
			Data   []struct {
				ID          string   `json:"test_id"`
				DirectionID int64    `json:"direction_id"`
				Cards       []digest `json:"cards"`
			} `json:"data"`
		}
		if err := s.Deps.call(r, "POST", s.Deps.Config.TestsURL+"/internal/tests/cards/digests", map[string]any{"test_ids": tests}, &result, ""); err != nil {
			return nil, err
		}
		if !result.Status || len(result.Data) != len(tests) {
			return nil, unavailable
		}
		for _, v := range result.Data {
			if !seen["test:"+v.ID] || !scope.Allows(v.DirectionID) {
				return nil, unavailable
			}
			if _, exists := byTest[v.ID]; exists {
				return nil, unavailable
			}
			byTest[v.ID] = v.Cards
		}
	}
	// Scope the SQL read to the selected keys, not all of this learner's progress.
	args := []any{a.AccountID, a.OrganizationID}
	tuples := []string{}
	for _, k := range in.Sections {
		tuples = append(tuples, "(?,?)")
		args = append(args, k.Kind, k.Ref)
	}
	rows, err := s.DB.QueryContext(r.Context(), "SELECT section_kind,section_ref,card_ref,fingerprint FROM cards_progress_v1 WHERE account_id=? AND organization_id=? AND (section_kind,section_ref) IN ("+strings.Join(tuples, ",")+")", args...)
	if err != nil {
		return nil, err
	}
	progress := map[string]map[string]string{}
	for rows.Next() {
		var kind, ref, card, fp string
		if err = rows.Scan(&kind, &ref, &card, &fp); err != nil {
			rows.Close()
			return nil, err
		}
		key := kind + ":" + ref
		if progress[key] == nil {
			progress[key] = map[string]string{}
		}
		progress[key][card] = fp
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []CatalogEntry{}
	for _, k := range in.Sections {
		cards := []Card{}
		if k.Kind == "manual" {
			id, _ := strconv.ParseInt(k.Ref, 10, 64)
			if _, err = theme(r.Context(), s.DB, a.OrganizationID, id, scope, false); err != nil {
				return nil, err
			}
			cards, err = manual(r.Context(), s.DB, a.OrganizationID, id)
			if err != nil {
				return nil, err
			}
		} else {
			for _, d := range byTest[k.Ref] {
				if d.Fingerprint == "" || !strings.HasPrefix(d.Ref, "test:"+k.Ref+":") {
					return nil, unavailable
				}
				cards = append(cards, Card{Ref: d.Ref, Fingerprint: d.Fingerprint, Status: "unlearned"})
			}
		}
		for i := range cards {
			if fp, ok := progress[k.Kind+":"+k.Ref][cards[i].Ref]; ok {
				cards[i].Status = "answer_changed"
				if fp == cards[i].Fingerprint {
					cards[i].Status = "learned"
				}
			}
		}
		out = append(out, CatalogEntry{Kind: k.Kind, Ref: k.Ref, Stats: stats(cards)})
	}
	return out, nil
}
