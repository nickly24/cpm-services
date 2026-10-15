package bff

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

type cardsCatalogStats struct {
	Total     int `json:"total"`
	Learned   int `json:"learned"`
	Changed   int `json:"answer_changed"`
	Unlearned int `json:"unlearned"`
	Percent   int `json:"progress_percent"`
}
type cardsCatalogItem struct {
	Kind        string            `json:"kind"`
	Ref         string            `json:"refId"`
	Name        string            `json:"name"`
	DirectionID int64             `json:"direction_id"`
	Stats       cardsCatalogStats `json:"stats"`
	ExamID      int64             `json:"exam_id,omitempty"`
	Parts       int               `json:"parts_count,omitempty"`
	Start       *string           `json:"start_at,omitempty"`
	End         *string           `json:"end_at,omitempty"`
}

// This aggregation runs after the existing live Auth/session revision fence.
// Domain owners authorize metadata and page stats; no content reaches this list.
func (s *Server) cardsCatalogPage(w http.ResponseWriter, r *http.Request, path, token, rid string, division *int64) {
	if !cardsQuery(r.Method, strings.TrimPrefix(path, "cards/"), r.URL.Query()) {
		failure(w, 400, "invalid_scope_parameter")
		return
	}
	parts := strings.Split(path, "/")
	direction, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || direction < 1 {
		failure(w, 400, "invalid_direction")
		return
	}
	q := r.URL.Query()
	page, limit := 1, 12
	for k, p := range map[string]*int{"page": &page, "limit": &limit} {
		if q.Has(k) {
			v, e := strconv.Atoi(q.Get(k))
			if e != nil || v < 1 {
				failure(w, 400, "invalid_pagination")
				return
			}
			*p = v
		}
	}
	if limit > 50 || page > 100000 {
		failure(w, 400, "invalid_pagination")
		return
	}
	kind, progress := q.Get("kind"), q.Get("progress")
	if kind != "" && kind != "all" && kind != "manual" && kind != "test" && kind != "exam" || progress != "" && progress != "all" && progress != "learned" && progress != "in_progress" {
		failure(w, 400, "invalid_filter")
		return
	}
	fetch := func(base, method, endpoint string, body any, out any) bool {
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		code, data, retry := s.callResponse(r.Context(), base, method, endpoint, token, raw, rid)
		if code != 200 {
			upstream(w, code, data, retry)
			return false
		}
		var envelope struct {
			Status bool            `json:"status"`
			Data   json.RawMessage `json:"data"`
		}
		if json.Unmarshal(data, &envelope) != nil || !envelope.Status || json.Unmarshal(envelope.Data, out) != nil {
			failure(w, 502, "invalid_upstream_response")
			return false
		}
		return true
	}
	all := []cardsCatalogItem{}
	// Always validate the selected direction against Cards/Core, even exam-only.
	if !fetch(s.config.Cards, "GET", fmt.Sprintf("/v1/cards/directions/%d/catalog", direction), nil, &all) {
		return
	}
	if kind == "" || kind == "all" || kind == "exam" {
		if division == nil {
			failure(w, 409, "division_required")
			return
		}
		exams := []cardsCatalogItem{}
		if s.config.Exams == "" {
			failure(w, 501, "section_not_connected")
			return
		}
		if !fetch(s.config.Exams, "GET", "/v1/exams/training/catalog?direction_id="+url.QueryEscape(fmt.Sprint(direction)), nil, &exams) {
			return
		}
		all = append(all, exams...)
	}
	filtered := []cardsCatalogItem{}
	search := strings.ToLower(strings.TrimSpace(q.Get("search")))
	seen := map[string]bool{}
	for _, item := range all {
		if item.DirectionID != direction || item.Ref == "" || (item.Kind != "manual" && item.Kind != "test" && item.Kind != "exam") || seen[item.Kind+":"+item.Ref] {
			failure(w, 502, "invalid_upstream_response")
			return
		}
		seen[item.Kind+":"+item.Ref] = true
		if kind != "" && kind != "all" && item.Kind != kind || !strings.Contains(strings.ToLower(item.Name), search) {
			continue
		}
		filtered = append(filtered, item)
	}
	sort.Slice(filtered, func(i, j int) bool {
		a, b := strings.ToLower(filtered[i].Name), strings.ToLower(filtered[j].Name)
		if a != b {
			return a < b
		}
		if filtered[i].Kind != filtered[j].Kind {
			return filtered[i].Kind < filtered[j].Kind
		}
		return filtered[i].Ref < filtered[j].Ref
	})
	hydrate := func(items []cardsCatalogItem) bool {
		keys := []map[string]string{}
		positions := map[string]int{}
		for i, item := range items {
			positions[item.Kind+":"+item.Ref] = i
			if item.Kind != "exam" {
				keys = append(keys, map[string]string{"kind": item.Kind, "refId": item.Ref})
			}
		}
		if len(keys) > 0 {
			summaries := []cardsCatalogItem{}
			if !fetch(s.config.Cards, "POST", "/v1/cards/catalog/stats", map[string]any{"sections": keys}, &summaries) {
				return false
			}
			if len(summaries) != len(keys) {
				failure(w, 502, "invalid_upstream_response")
				return false
			}
			applied := map[string]bool{}
			for _, summary := range summaries {
				key := summary.Kind + ":" + summary.Ref
				index, exists := positions[key]
				if !exists || applied[key] {
					failure(w, 502, "invalid_upstream_response")
					return false
				}
				applied[key] = true
				items[index].Stats = summary.Stats
			}
		}
		for i, item := range items {
			if item.Kind == "exam" {
				var summary cardsCatalogItem
				if !fetch(s.config.Exams, "GET", "/v1/exams/"+item.Ref+"/training/summary", nil, &summary) {
					return false
				}
				if summary.Kind != "exam" || summary.Ref != item.Ref {
					failure(w, 502, "invalid_upstream_response")
					return false
				}
				items[i].Stats = summary.Stats
			}
		}
		return true
	}
	// Progress filtering must happen before counting/pagination. This explicit
	// filter reads bounded digest batches, never text, and cannot use stale caches.
	hasProgress := progress == "learned" || progress == "in_progress"
	if hasProgress {
		for offset := 0; offset < len(filtered); offset += 50 {
			if !hydrate(filtered[offset:min(offset+50, len(filtered))]) {
				return
			}
		}
		candidates := []cardsCatalogItem{}
		for _, item := range filtered {
			learned := item.Stats.Total > 0 && item.Stats.Total == item.Stats.Learned
			if learned == (progress == "learned") {
				candidates = append(candidates, item)
			}
		}
		filtered = candidates
	}
	total := len(filtered)
	pages := max(1, (total+limit-1)/limit)
	page = min(page, pages)
	start := (page - 1) * limit
	items := filtered[start:min(start+limit, total)]
	if !hasProgress && !hydrate(items) {
		return
	}
	reply(w, 200, map[string]any{"status": true, "data": map[string]any{"direction_id": direction, "sections": items, "pagination": map[string]any{"page": page, "limit": limit, "total": total, "pages": pages, "has_next": page < pages, "has_prev": page > 1}}})
}
