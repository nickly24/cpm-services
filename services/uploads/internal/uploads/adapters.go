package uploads

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
)

func numeric(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	}
	return 0
}
func ref(raw json.RawMessage) (string, int64, error) {
	var v map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&v) != nil || v == nil {
		return "", 0, api(503, "owner_invalid_reference")
	}
	id := v["session_id"]
	if id == nil {
		id = v["id"]
	}
	text := ""
	switch x := id.(type) {
	case string:
		text = x
	case json.Number:
		n, e := x.Int64()
		if e != nil || n < 1 {
			return "", 0, api(503, "owner_invalid_reference")
		}
		text = strconv.FormatInt(n, 10)
	}
	if !keyPattern.MatchString(text) {
		if _, e := parseID(text); e != nil {
			return "", 0, api(503, "owner_invalid_reference")
		}
	}
	rev := int64(1)
	if value, present := v["revision"]; present {
		n, ok := value.(json.Number)
		if !ok {
			return "", 0, api(503, "owner_invalid_reference")
		}
		var e error
		rev, e = n.Int64()
		if e != nil || rev < 1 {
			return "", 0, api(503, "owner_invalid_reference")
		}
	}
	return text, rev, nil
}
func (s *Server) ownerPath(ss Session) (string, string, error) {
	switch {
	case ss.Type == "users.credentials":
		return s.Config.CoreURL, "/v1/core/divisions/" + strconv.FormatInt(ss.DivisionID, 10) + "/imports/" + ss.OwnerSessionID, nil
	case ss.Type == "users.legacy":
		return s.Config.CoreURL, "/v1/core/divisions/" + strconv.FormatInt(ss.DivisionID, 10) + "/legacy-imports/" + ss.OwnerSessionID, nil
	case ss.Type == "cards.xlsx" || ss.Type == "cards.transform":
		return s.Config.CardsURL, "/v1/cards/sessions/" + ss.OwnerSessionID, nil
	case strings.HasPrefix(ss.Type, "exams."):
		return s.Config.ExamsURL, "/v1/exams/" + ss.TargetID + "/imports/" + strings.TrimPrefix(ss.Type, "exams.") + "/" + ss.OwnerSessionID, nil
	}
	return "", "", api(501, "owner_adapter_pending")
}
func (s *Server) ownerCreate(ctx context.Context, ss Session, token, key, ct string, body []byte) (json.RawMessage, error) {
	if stagedType(ss.Type) {
		return s.createStagedPreview(ctx, ss, token, body)
	}
	base, path := "", ""
	switch {
	case ss.Type == "users.credentials":
		base = s.Config.CoreURL
		path = "/v1/core/divisions/" + strconv.FormatInt(ss.DivisionID, 10) + "/imports"
	case ss.Type == "users.legacy":
		base = s.Config.CoreURL
		path = "/v1/core/divisions/" + strconv.FormatInt(ss.DivisionID, 10) + "/legacy-imports"
	case ss.Type == "cards.xlsx":
		base = s.Config.CardsURL
		path = "/v1/cards/imports/parse"
	case ss.Type == "cards.transform":
		base = s.Config.CardsURL
		path = "/v1/cards/transforms"
	case strings.HasPrefix(ss.Type, "exams."):
		base = s.Config.ExamsURL
		path = "/v1/exams/" + ss.TargetID + "/imports/" + strings.TrimPrefix(ss.Type, "exams.")
	default:
		return nil, api(501, "owner_adapter_pending")
	}
	var extra map[string]string
	if ss.Type == "users.legacy" && s.Config.OperationsEnabled {
		extra = map[string]string{"X-CPM-Import-Source-Key": s.Config.OperationDeliveryKeys["core"], "X-CPM-Import-Source-SHA256": ss.SourceHash}
	}
	return s.callHeaders(ctx, "POST", base, path, token, key, ct, body, extra)
}
func (s *Server) ownerGet(ctx context.Context, ss Session, token string) (json.RawMessage, error) {
	if stagedType(ss.Type) {
		return s.getStagedPreview(ctx, ss, token)
	}
	base, path, e := s.ownerPath(ss)
	if e != nil {
		return nil, e
	}
	if !strings.HasPrefix(ss.Type, "exams.") {
		return s.call(ctx, "GET", base, path, token, "", "", nil)
	}
	var first map[string]json.RawMessage
	var all []json.RawMessage
	var errors []json.RawMessage
	var revision int64
	var state string
	var total int
	var pageFingerprint string
	for offset := 0; ; {
		raw, e := s.call(ctx, "GET", base, path+"?limit=200&offset="+strconv.Itoa(offset), token, "", "", nil)
		if e != nil {
			return nil, e
		}
		var page struct {
			ID       string            `json:"id"`
			Revision int64             `json:"revision"`
			State    string            `json:"state"`
			Total    int               `json:"total"`
			Offset   int               `json:"offset"`
			Limit    int               `json:"limit"`
			Rows     []json.RawMessage `json:"rows"`
			Errors   []json.RawMessage `json:"error_report"`
		}
		if json.Unmarshal(raw, &page) != nil || page.ID != ss.OwnerSessionID || page.Revision < 1 || page.Total < 0 || page.Total > 5000 || page.Offset != offset || page.Limit != 0 && page.Limit != 200 {
			return nil, api(503, "owner_invalid_preview_page")
		}
		if offset == 0 {
			if json.Unmarshal(raw, &first) != nil {
				return nil, api(503, "owner_invalid_preview_page")
			}
			revision, state, total = page.Revision, page.State, page.Total
		} else if revision != page.Revision || state != page.State || total != page.Total {
			return nil, api(409, "owner_preview_changed_during_read")
		}
		var values map[string]json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return nil, api(503, "owner_invalid_preview_page")
		}
		stable := map[string]json.RawMessage{}
		for _, key := range []string{"validation_hash", "source_hash", "exam_revision", "connected_division_ids", "filename", "organization_id", "division_id", "account_id", "exam_id", "kind", "expires_at", "summary", "result"} {
			stable[key] = values[key]
		}
		encoded, _ := json.Marshal(stable)
		fingerprint := digest(encoded)
		if offset == 0 {
			pageFingerprint = fingerprint
		} else if pageFingerprint != fingerprint {
			return nil, api(409, "owner_preview_changed_during_read")
		}
		if len(page.Rows) > 200 || offset+len(page.Rows) > total || (len(page.Rows) == 0 && offset < total) {
			return nil, api(503, "owner_partial_preview")
		}
		all = append(all, page.Rows...)
		errors = append(errors, page.Errors...)
		offset += len(page.Rows)
		if offset == total {
			break
		}
	}
	first["rows"], _ = json.Marshal(all)
	first["error_report"], _ = json.Marshal(errors)
	first["limit"], _ = json.Marshal(total)
	first["offset"] = json.RawMessage(`0`)
	first["has_more"] = json.RawMessage(`false`)
	first["next_offset"] = json.RawMessage(`null`)
	out, e := json.Marshal(first)
	if e != nil || len(out) > 16<<20 {
		return nil, api(413, "preview_size_limit")
	}
	return out, nil
}
func (s *Server) ownerCommit(ctx context.Context, ss Session, token, key string) (json.RawMessage, error) {
	base, path, e := s.ownerPath(ss)
	if e != nil {
		return nil, e
	}
	b, _ := json.Marshal(map[string]any{"expected_revision": ss.OwnerRevision})
	if ss.Type == "users.credentials" {
		b = []byte(`{}`)
	}
	return s.call(ctx, "POST", base, path+"/commit", token, key, "application/json", b)
}
func ownerState(raw json.RawMessage) (int64, string) {
	var v struct {
		Revision int64  `json:"revision"`
		State    string `json:"state"`
		Status   string `json:"status"`
	}
	json.Unmarshal(raw, &v)
	if v.Revision < 1 {
		v.Revision = 1
	}
	if v.State == "" {
		v.State = v.Status
	}
	return v.Revision, v.State
}
func (s *Server) ownerFile(ctx context.Context, f File, token string) (json.RawMessage, error) {
	d := strconv.FormatInt(f.DivisionID, 10)
	switch f.Type {
	case "homework.pdf":
		return s.call(ctx, "GET", s.Config.HomeworkURL, "/v1/homework/divisions/"+d+"/jobs/"+f.OwnerID, token, "", "", nil)
	case "attendance.zap-images":
		raw, e := s.call(ctx, "GET", s.Config.AttendanceURL, "/v1/attendance/divisions/"+d+"/zaps/"+f.TargetID, token, "", "", nil)
		if e != nil {
			return nil, e
		}
		var envelope struct {
			Value json.RawMessage `json:"value"`
		}
		if json.Unmarshal(raw, &envelope) != nil || len(envelope.Value) == 0 {
			return nil, api(503, "invalid_attendance_response")
		}
		return envelope.Value, nil
	}
	return nil, api(400, "invalid_owner_reference")
}
