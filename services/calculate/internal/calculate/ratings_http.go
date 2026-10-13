package calculate

import (
	"net/http"
	"strconv"
)

func (s *Server) listRatingMessages(w http.ResponseWriter, r *http.Request) {
	f := Filter{JobID: r.URL.Query().Get("job_id"), Limit: 20, MetadataOnly: true}
	if f.JobID != "" && !uuidPattern.MatchString(f.JobID) {
		fail(w, http.StatusBadRequest, "invalid_filter")
		return
	}
	var err error
	if v := r.URL.Query().Get("account_id"); v != "" {
		f.AccountID, err = strconv.ParseInt(v, 10, 64)
		if err != nil || f.AccountID < 1 {
			fail(w, http.StatusBadRequest, "invalid_filter")
			return
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		f.Limit, err = strconv.ParseInt(v, 10, 64)
		if err != nil || f.Limit < 1 || f.Limit > 100 {
			fail(w, http.StatusBadRequest, "invalid_pagination")
			return
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		f.Offset, err = strconv.ParseInt(v, 10, 64)
		if err != nil || f.Offset < 0 || f.Offset > 100000 {
			fail(w, http.StatusBadRequest, "invalid_pagination")
			return
		}
	}
	items, total, err := s.RatingsStore.List(r.Context(), f)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	for i := range items {
		items[i].Output = nil
	}
	reply(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": f.Limit, "offset": f.Offset})
}
