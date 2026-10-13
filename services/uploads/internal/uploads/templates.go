package uploads

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Templates are served through the same verified actor/org/division and subject
// gate as source access. No template contains generated credentials or real rows.
func (s *Server) template(w http.ResponseWriter, r *http.Request, a Actor, sc Scope) error {
	t, e := findType(r.PathValue("type"))
	if e != nil {
		return e
	}
	if e = permission(a, t, t.ID == "users.legacy"); e != nil {
		return e
	}
	if !t.Template {
		return api(400, "template_not_applicable")
	}
	query, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		return api(400, "invalid_template_query")
	}
	allowed := map[string]bool{}
	if strings.HasPrefix(t.ID, "exams.") || t.ID == "tests.external-results" {
		allowed["target_id"] = true
	} else if t.ID == "tests.json" {
		allowed["direction_id"], allowed["format"] = true, true
	}
	for name, values := range query {
		if !allowed[name] || len(values) != 1 {
			return api(400, "invalid_template_query")
		}
	}
	var base, path, ext, ct string
	ct = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	switch {
	case t.ID == "users.credentials":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="users-credentials-template.csv"`)
		_, e = w.Write([]byte("full_name,login,password,class,tg_name,school_id,group_id,role\n"))
		return e
	case t.ID == "users.legacy":
		base = s.Config.CoreURL
		path = "/v1/core/divisions/" + strconv.FormatInt(sc.DivisionID, 10) + "/legacy-imports/template"
		ext = "xlsx"
	case t.ID == "cards.xlsx":
		base = s.Config.CardsURL
		path = "/v1/cards/imports/template"
		ext = "xlsx"
	case t.ID == "tests.json":
		direction, err := parseID(query.Get("direction_id"))
		if err != nil {
			return api(400, "template_direction_required")
		}
		format := query.Get("format")
		if format == "" {
			format = "cpm_json"
		}
		switch format {
		case "cpm_json":
			ext, ct = "json", "application/json"
		case "online_test_pad":
			ext, ct = "js", "text/javascript"
		default:
			return api(400, "invalid_template_format")
		}
		base = s.Config.TestsURL
		nativeQuery := url.Values{"direction_id": {strconv.FormatInt(direction, 10)}, "format": {format}}
		path = "/v1/tests/divisions/" + strconv.FormatInt(sc.DivisionID, 10) + "/imports/json/template?" + nativeQuery.Encode()
	case t.ID == "tests.external-results":
		target, err := stagedTarget(query.Get("target_id"))
		if err != nil {
			return err
		}
		base = s.Config.TestsURL
		path = "/v1/tests/divisions/" + strconv.FormatInt(sc.DivisionID, 10) + "/external-tests/" + target + "/imports/results/template"
		ext = "xlsx"
	case strings.HasPrefix(t.ID, "exams."):
		target, err := parseID(query.Get("target_id"))
		if err != nil {
			return api(400, "exam_target_required")
		}
		base = s.Config.ExamsURL
		path = "/v1/exams/" + strconv.FormatInt(target, 10) + "/imports/" + strings.TrimPrefix(t.ID, "exams.") + "/template"
		ext = "xlsx"
	default:
		return api(400, "template_not_applicable")
	}
	req, e := http.NewRequestWithContext(r.Context(), "GET", base+path, nil)
	if e != nil {
		return api(503, "owner_unavailable")
	}
	req.Header.Set("Authorization", r.Header.Get("Authorization"))
	res, e := s.HTTP.Do(req)
	if e != nil {
		return api(503, "owner_unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		status := res.StatusCode
		if status != 401 && status != 403 && status != 404 && status != 410 {
			status = 503
		}
		return api(status, "owner_template_unavailable")
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, maxFile+1))
	if e != nil || len(b) > maxFile {
		return api(503, "owner_invalid_template")
	}
	if e = validFile(b, ext, t.MaxRows); e != nil {
		return api(503, "owner_invalid_template")
	}
	ownerMIME, mimeParams, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || ownerMIME != ct && !(ext == "xlsx" && ownerMIME == "application/octet-stream") {
		return api(503, "owner_invalid_template")
	}
	if (ext == "json" || ext == "js") && !utf8.Valid(b) {
		return api(503, "owner_invalid_template")
	}
	if charset := mimeParams["charset"]; (ext == "json" || ext == "js") && charset != "" && !strings.EqualFold(charset, "utf-8") {
		return api(503, "owner_invalid_template")
	}
	if ext == "js" {
		if !literalTemplateScript(b) {
			return api(503, "owner_invalid_template")
		}
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(t.ID, ".", "-")+"-template."+ext+`"`)
	_, e = w.Write(b)
	return e
}

// A downloadable example is one literal assignment, unlike an uploaded legacy
// script which may include unrelated wrappers. Neither path executes JavaScript.
func literalTemplateScript(content []byte) bool {
	source := bytes.TrimSpace(bytes.TrimPrefix(content, []byte{0xef, 0xbb, 0xbf}))
	marker := onlineTestPadMarker.FindIndex(source)
	if marker == nil || marker[0] != 0 {
		return false
	}
	object := bytes.TrimSpace(source[marker[1]:])
	object = bytes.TrimSpace(bytes.TrimSuffix(object, []byte(";")))
	return len(object) > 0 && object[0] == '{' && json.Valid(object)
}
