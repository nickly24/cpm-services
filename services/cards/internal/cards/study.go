package cards

import (
	"database/sql"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var testIDPattern = regexp.MustCompile(`^[a-f0-9]{24}$`)

func (s *Server) loadView(r *http.Request, a Actor, scope Scope, kind, ref string) (View, error) {
	v := View{Kind: kind, Ref: ref, Settings: Settings{BatchSize: 10, Mode: "unlearned"}, Cards: []Card{}}
	switch kind {
	case "manual":
		id, e := number(ref)
		if e != nil {
			return v, e
		}
		v.Ref = strconv.FormatInt(id, 10)
		ref = v.Ref
		t, e := theme(r.Context(), s.DB, a.OrganizationID, id, scope, false)
		if e != nil {
			return v, e
		}
		v.Name = t.Name
		v.DirectionID = t.DirectionID
		v.Cards, e = manual(r.Context(), s.DB, a.OrganizationID, id)
		if e != nil {
			return v, e
		}
	case "test":
		if !testIDPattern.MatchString(ref) {
			return v, bad("invalid_test_id")
		}
		p, e := s.Deps.projection(r, ref)
		if e != nil {
			return v, e
		}
		if !scope.Allows(p.DirectionID) {
			return v, missing
		}
		v.Name = p.Title
		v.Version = p.Version
		v.DirectionID = p.DirectionID
		v.Cards = projectionCards(p)
	default:
		return v, bad("unsupported_section_kind")
	}
	rows, e := s.DB.QueryContext(r.Context(), "SELECT card_ref,fingerprint FROM cards_progress_v1 WHERE account_id=? AND organization_id=? AND section_kind=? AND section_ref=?", a.AccountID, a.OrganizationID, kind, ref)
	if e != nil {
		return v, e
	}
	progress := map[string]string{}
	for rows.Next() {
		var ref, fp string
		if e = rows.Scan(&ref, &fp); e != nil {
			rows.Close()
			return v, e
		}
		progress[ref] = fp
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return v, e
	}
	for i := range v.Cards {
		c := &v.Cards[i]
		if old, ok := progress[c.Ref]; ok {
			c.Status = "answer_changed"
			if old == c.Fingerprint {
				c.Status = "learned"
			}
		}
	}
	e = s.DB.QueryRowContext(r.Context(), "SELECT batch_size,study_mode,last_batch_index FROM cards_settings_v1 WHERE account_id=? AND organization_id=? AND section_kind=? AND section_ref=?", a.AccountID, a.OrganizationID, kind, ref).Scan(&v.Settings.BatchSize, &v.Settings.Mode, &v.Settings.LastBatch)
	if e != nil && e != sql.ErrNoRows {
		return v, e
	}
	v.Stats = stats(v.Cards)
	v.Batches = batches(v.Cards, v.Settings.BatchSize)
	return v, nil
}
func (s *Server) study(r *http.Request, a Actor, scope Scope) (any, error) {
	return s.loadView(r, a, scope, r.PathValue("kind"), r.PathValue("section"))
}
func (s *Server) batch(r *http.Request, a Actor, scope Scope) (any, error) {
	v, e := s.loadView(r, a, scope, r.PathValue("kind"), r.PathValue("section"))
	if e != nil {
		return nil, e
	}
	i, e := strconv.Atoi(r.PathValue("batch"))
	if e != nil || i < 0 || i >= len(v.Batches) {
		return nil, missing
	}
	mode := r.URL.Query().Get("study_mode")
	if mode == "" {
		mode = v.Settings.Mode
	}
	if !modeValid(mode) {
		return nil, bad("invalid_study_mode")
	}
	b := v.Batches[i]
	cards := filtered(v.Cards[b.From-1:b.To], mode)
	return map[string]any{"batch_index": i, "batch": b, "study_mode": mode, "cards": cards, "count": len(cards)}, nil
}
func (s *Server) settings(r *http.Request, a Actor, scope Scope) (any, error) {
	if a.Role != "student" {
		return nil, denied
	}
	var in Settings
	if e := decode(r, &in); e != nil {
		return nil, e
	}
	if (in.BatchSize != 10 && in.BatchSize != 20 && in.BatchSize != 30) || !modeValid(in.Mode) || in.LastBatch != nil && (*in.LastBatch < 0 || *in.LastBatch > 100000) {
		return nil, bad("invalid_settings")
	}
	v, e := s.loadView(r, a, scope, r.PathValue("kind"), r.PathValue("section"))
	if e != nil {
		return nil, e
	}
	e = s.tx(r.Context(), func(tx *sql.Tx) error {
		if v.Kind == "manual" {
			id, _ := number(v.Ref)
			if _, e := theme(r.Context(), tx, a.OrganizationID, id, scope, true); e != nil {
				return e
			}
		}
		_, e := tx.ExecContext(r.Context(), "INSERT INTO cards_settings_v1(account_id,organization_id,section_kind,section_ref,batch_size,study_mode,last_batch_index) VALUES(?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE batch_size=VALUES(batch_size),study_mode=VALUES(study_mode),last_batch_index=VALUES(last_batch_index)", a.AccountID, a.OrganizationID, v.Kind, v.Ref, in.BatchSize, in.Mode, in.LastBatch)
		return e
	})
	return in, e
}
func (s *Server) mark(r *http.Request, a Actor, scope Scope) (any, error) {
	return s.progress(r, a, scope, false)
}
func (s *Server) unmark(r *http.Request, a Actor, scope Scope) (any, error) {
	return s.progress(r, a, scope, true)
}
func (s *Server) progress(r *http.Request, a Actor, scope Scope, remove bool) (any, error) {
	if a.Role != "student" {
		return nil, denied
	}
	var in MarkDTO
	if e := decode(r, &in); e != nil {
		return nil, e
	}
	v, e := s.loadView(r, a, scope, r.PathValue("kind"), r.PathValue("section"))
	if e != nil {
		return nil, e
	}
	var found *Card
	for i := range v.Cards {
		if v.Cards[i].Ref == in.CardRef {
			found = &v.Cards[i]
			break
		}
	}
	if found == nil {
		return nil, missing
	}
	if !remove && in.Fingerprint != found.Fingerprint {
		return nil, &APIError{409, "content_changed"}
	}
	e = s.tx(r.Context(), func(tx *sql.Tx) error {
		if v.Kind == "manual" {
			tid, _ := number(v.Ref)
			if _, e := theme(r.Context(), tx, a.OrganizationID, tid, scope, true); e != nil {
				return e
			}
			var q, ans string
			if e := tx.QueryRowContext(r.Context(), "SELECT question,answer FROM cards_items_v1 WHERE organization_id=? AND theme_id=? AND id=? FOR UPDATE", a.OrganizationID, tid, found.ID).Scan(&q, &ans); e != nil {
				return e
			}
			if !remove && fingerprint(q, ans) != in.Fingerprint {
				return &APIError{409, "content_changed"}
			}
		}
		if remove {
			_, e := tx.ExecContext(r.Context(), "DELETE FROM cards_progress_v1 WHERE account_id=? AND organization_id=? AND card_ref=?", a.AccountID, a.OrganizationID, in.CardRef)
			return e
		}
		_, e := tx.ExecContext(r.Context(), "INSERT INTO cards_progress_v1(account_id,organization_id,section_kind,section_ref,card_ref,fingerprint) VALUES(?,?,?,?,?,?) ON DUPLICATE KEY UPDATE fingerprint=VALUES(fingerprint),learned_at=CURRENT_TIMESTAMP", a.AccountID, a.OrganizationID, v.Kind, v.Ref, in.CardRef, in.Fingerprint)
		return e
	})
	status := "learned"
	if remove {
		status = "unlearned"
	}
	return map[string]any{"card_ref": in.CardRef, "status": status}, e
}

type Node struct {
	Kind        string `json:"kind"`
	Ref         string `json:"refId"`
	Name        string `json:"name"`
	DirectionID int64  `json:"direction_id"`
	Stats       Stats  `json:"stats"`
}
type DirectionView struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Sections []Node `json:"sections"`
	Stats    Stats  `json:"stats"`
}

func (s *Server) nodes(r *http.Request, a Actor, scope Scope) ([]Node, error) {
	rows, e := s.DB.QueryContext(r.Context(), "SELECT id,direction_id,name FROM cards_themes_v1 WHERE organization_id=? ORDER BY name,id", a.OrganizationID)
	if e != nil {
		return nil, e
	}
	themes := []Theme{}
	for rows.Next() {
		var t Theme
		if e = rows.Scan(&t.ID, &t.DirectionID, &t.Name); e != nil {
			rows.Close()
			return nil, e
		}
		if scope.Allows(t.DirectionID) {
			themes = append(themes, t)
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	out := []Node{}
	for _, t := range themes {
		v, e := s.loadView(r, a, scope, "manual", fmt.Sprint(t.ID))
		if e != nil {
			return nil, e
		}
		out = append(out, Node{"manual", v.Ref, v.Name, t.DirectionID, v.Stats})
	}
	tests, e := s.Deps.catalog(r)
	if e != nil {
		return nil, e
	}
	progress := map[string]map[string]string{}
	if len(tests) > 0 {
		progress, e = s.catalogTestProgress(r, a)
		if e != nil {
			return nil, e
		}
	}
	for _, t := range tests {
		if !scope.Allows(t.DirectionID) {
			continue
		}
		cards := projectionCards(t)
		for i := range cards {
			if previous, ok := progress[t.TestID][cards[i].Ref]; ok {
				cards[i].Status = "answer_changed"
				if previous == cards[i].Fingerprint {
					cards[i].Status = "learned"
				}
			}
		}
		out = append(out, Node{"test", t.TestID, t.Title, t.DirectionID, stats(cards)})
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}
func (s *Server) tree(r *http.Request, a Actor, scope Scope) (any, error) {
	nodes, e := s.nodes(r, a, scope)
	if e != nil {
		return nil, e
	}
	out := []DirectionView{}
	for _, d := range scope.Directions {
		v := DirectionView{ID: d.ID, Name: d.Name, Sections: []Node{}}
		for _, n := range nodes {
			if n.DirectionID == d.ID {
				v.Sections = append(v.Sections, n)
				v.Stats.Total += n.Stats.Total
				v.Stats.Learned += n.Stats.Learned
				v.Stats.Changed += n.Stats.Changed
				v.Stats.Unlearned += n.Stats.Unlearned
			}
		}
		if len(v.Sections) > 0 || a.Can(false) {
			if v.Stats.Total > 0 {
				v.Stats.Percent = percent(v.Stats.Learned, v.Stats.Total)
			}
			out = append(out, v)
		}
	}
	return map[string]any{"directions": out}, nil
}
func percent(n, d int) int { return statsForCounts(n, d) }
func (s *Server) sections(r *http.Request, a Actor, scope Scope) (any, error) {
	id, e := number(r.PathValue("direction"))
	if e != nil {
		return nil, e
	}
	if !scope.Allows(id) {
		return nil, missing
	}
	nodes, e := s.nodes(r, a, scope)
	if e != nil {
		return nil, e
	}
	q := r.URL.Query()
	kind, progress := q.Get("kind"), q.Get("progress")
	if kind != "" && kind != "all" && kind != "manual" && kind != "test" || progress != "" && progress != "all" && progress != "in_progress" && progress != "learned" {
		return nil, bad("invalid_filter")
	}
	search := strings.ToLower(strings.TrimSpace(q.Get("search")))
	out := []Node{}
	for _, n := range nodes {
		learned := n.Stats.Total > 0 && n.Stats.Learned == n.Stats.Total
		if n.DirectionID != id || kind != "" && kind != "all" && kind != n.Kind || progress == "learned" && !learned || progress == "in_progress" && learned || !strings.Contains(strings.ToLower(n.Name), search) {
			continue
		}
		out = append(out, n)
	}
	page, limit := 1, 6
	if q.Get("page") != "" {
		page, e = strconv.Atoi(q.Get("page"))
		if e != nil || page < 1 {
			return nil, bad("invalid_page")
		}
	}
	if q.Get("limit") != "" {
		limit, e = strconv.Atoi(q.Get("limit"))
		if e != nil || limit < 1 || limit > 100 {
			return nil, bad("invalid_limit")
		}
	}
	total := len(out)
	pages := max(1, (total+limit-1)/limit)
	page = min(page, pages)
	start := (page - 1) * limit
	return map[string]any{"sections": out[start:min(start+limit, total)], "pagination": map[string]any{"page": page, "limit": limit, "total": total, "pages": pages, "has_next": page < pages, "has_prev": page > 1}}, nil
}

func (s *Server) adminCatalog(r *http.Request, a Actor, scope Scope) (any, error) {
	if !a.Can(false) {
		return nil, denied
	}
	var out struct {
		Status bool         `json:"status"`
		Data   []Projection `json:"data"`
	}
	if e := s.Deps.call(r, "GET", s.Deps.Config.TestsURL+"/internal/tests/cards/catalog?admin=true", nil, &out, ""); e != nil {
		return nil, e
	}
	if !out.Status {
		return nil, unavailable
	}
	rows, e := s.DB.QueryContext(r.Context(), "SELECT t.id,t.direction_id,t.name,t.revision,COUNT(c.id) FROM cards_themes_v1 t LEFT JOIN cards_items_v1 c ON c.organization_id=t.organization_id AND c.theme_id=t.id WHERE t.organization_id=? GROUP BY t.id,t.direction_id,t.name,t.revision ORDER BY t.name,t.id", a.OrganizationID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	nodes := map[int64][]map[string]any{}
	for rows.Next() {
		var id, did int64
		var name string
		var count, revision int
		if e = rows.Scan(&id, &did, &name, &revision, &count); e != nil {
			return nil, e
		}
		if scope.Allows(did) {
			nodes[did] = append(nodes[did], map[string]any{"kind": "manual", "id": id, "name": name, "direction_id": did, "cards_count": count, "revision": revision, "visible": true})
		}
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	for _, p := range out.Data {
		if scope.Allows(p.DirectionID) {
			nodes[p.DirectionID] = append(nodes[p.DirectionID], map[string]any{"kind": "test", "test_id": p.TestID, "name": p.Title, "direction_id": p.DirectionID, "cards_count": p.CardsCount, "visible": p.Visible})
		}
	}
	directions := []map[string]any{}
	for _, d := range scope.Directions {
		sections := nodes[d.ID]
		if sections == nil {
			sections = []map[string]any{}
		}
		sort.SliceStable(sections, func(i, j int) bool {
			return strings.ToLower(sections[i]["name"].(string)) < strings.ToLower(sections[j]["name"].(string))
		})
		count := 0
		for _, n := range sections {
			count += n["cards_count"].(int)
		}
		directions = append(directions, map[string]any{"id": d.ID, "name": d.Name, "sections": sections, "topics_count": len(sections), "cards_count": count})
	}
	return map[string]any{"directions": directions}, nil
}

func projectionCards(p Projection) []Card {
	out := []Card{}
	sort.Slice(p.Questions, func(i, j int) bool { return p.Questions[i].ID < p.Questions[j].ID })
	for _, q := range p.Questions {
		ans := answer(q)
		if strings.TrimSpace(q.Text) != "" && ans != "" {
			out = append(out, Card{QuestionID: q.ID, Ref: fmt.Sprintf("test:%s:%d", p.TestID, q.ID), Question: q.Text, Answer: ans, Order: q.ID, Fingerprint: fingerprint(q.Text, ans), Status: "unlearned"})
		}
	}
	return out
}

// Catalog needs statistics, not per-section study settings. Read the learner's
// test progress once, using exactly the same account/organization keys as study.
func (s *Server) catalogTestProgress(r *http.Request, a Actor) (map[string]map[string]string, error) {
	rows, err := s.DB.QueryContext(r.Context(), "SELECT section_ref,card_ref,fingerprint FROM cards_progress_v1 WHERE account_id=? AND organization_id=? AND section_kind='test'", a.AccountID, a.OrganizationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]string{}
	for rows.Next() {
		var section, ref, fp string
		if err = rows.Scan(&section, &ref, &fp); err != nil {
			return nil, err
		}
		if out[section] == nil {
			out[section] = map[string]string{}
		}
		out[section][ref] = fp
	}
	return out, rows.Err()
}
