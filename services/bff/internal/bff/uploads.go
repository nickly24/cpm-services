package bff

import (
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Uploads orchestrates owner sessions. Only this fixed public surface is exposed;
// the selected division and bearer remain on the server side.
const uploadID = `[a-f0-9]{32}`
const uploadCommand = `[a-zA-Z0-9:_-]{8,128}`

var studentUploadBatch = regexp.MustCompile(`^student-imports/[a-f0-9]{32}/batches/[0-9]{1,7}$`)
var uploadsRoutes = regexp.MustCompile(`^(GET (catalog|sessions|sessions/` + uploadID + `(/job)?|jobs|jobs/` + uploadID + `(/report)?|files/` + uploadID + `(/download)?|receipts/` + uploadCommand + `|events)|POST (sessions|sessions/` + uploadID + `/(commit|reconcile)|jobs/` + uploadID + `/(retry|recover|revoke)|files/register)|PUT sessions/` + uploadID + `)$`)
var uploadsDownload = regexp.MustCompile(`^files/` + uploadID + `/download$`)
var uploadsTemplate = regexp.MustCompile(`^templates/(users\.(credentials|legacy)|cards\.xlsx|tests\.(json|external-results)|exams\.(questions|assignments|outside))$`)
var uploadsTargetID = regexp.MustCompile(`^` + coreID + `$`)
var uploadsExternalTargetID = regexp.MustCompile(`^[a-f0-9]{24}$`)

func uploadsRoute(method, tail string) string {
	if rest, ok := strings.CutPrefix(tail, "divisions/"); ok {
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) != 2 || !uploadsTargetID.MatchString(parts[0]) {
			return ""
		}
		if !(method == "POST" && studentUploadBatch.MatchString(parts[1]) || regexp.MustCompile(`^(GET (sessions/[a-f0-9]{32}(/job)?|jobs/[a-f0-9]{32})|POST (sessions/[a-f0-9]{32}/commit|jobs/[a-f0-9]{32}/recover))$`).MatchString(method+" "+parts[1])) {
			return ""
		}
		route := uploadsRoute(method, parts[1])
		if route == "" {
			return ""
		}
		return strings.Replace(route, "%d", parts[0], 1)
	}
	if method == "POST" && studentUploadBatch.MatchString(tail) {
		return "/v1/uploads/divisions/%d/" + tail
	}

	if uploadsRoutes.MatchString(method+" "+tail) || method == "GET" && uploadsTemplate.MatchString(tail) {
		return "/v1/uploads/divisions/%d/" + tail
	}
	return ""
}

func uploadsQuery(method, tail string, q url.Values) bool {
	if rest, ok := strings.CutPrefix(tail, "divisions/"); ok {
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) != 2 {
			return false
		}
		tail = parts[1]
	}
	for key, values := range q {
		if method == "GET" && len(values) == 1 {
			if tail == "templates/tests.external-results" && key == "target_id" && uploadsExternalTargetID.MatchString(values[0]) {
				continue
			}
			if tail == "templates/tests.json" && (key == "direction_id" && uploadsTargetID.MatchString(values[0]) || key == "format" && (values[0] == "cpm_json" || values[0] == "online_test_pad" || values[0] == "question_table")) {
				continue
			}
		}
		if method == "GET" && uploadsTemplate.MatchString(tail) && strings.HasPrefix(tail, "templates/exams.") && key == "target_id" && len(values) == 1 && uploadsTargetID.MatchString(values[0]) {
			continue
		}
		if method != "GET" || (tail != "sessions" && tail != "jobs" && tail != "events") || key != "limit" || len(values) != 1 || values[0] == "" {
			return false
		}
	}
	return true
}

func (s *Server) uploadsFileResponse(w http.ResponseWriter, r *http.Request, path, token, rid string) {
	req, e := http.NewRequestWithContext(r.Context(), "GET", s.config.Uploads+path, nil)
	if e != nil {
		failure(w, 503, "upstream_unavailable")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Request-ID", rid)
	res, e := s.client.Do(req)
	if e != nil {
		failure(w, 503, "upstream_unavailable")
		return
	}
	defer res.Body.Close()
	b, e := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if e != nil || len(b) > 8<<20 || res.StatusCode >= 300 && res.StatusCode < 400 {
		failure(w, 502, "invalid_upstream_response")
		return
	}
	if res.StatusCode != 200 {
		upstream(w, res.StatusCode, b)
		return
	}
	ct, _, e := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if e != nil || (ct != "application/octet-stream" && ct != "image/png" && ct != "image/jpeg" && ct != "image/webp") {
		failure(w, 502, "invalid_upstream_response")
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `attachment; filename="upload"`)
	w.WriteHeader(200)
	_, _ = w.Write(b)
}
