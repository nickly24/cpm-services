package bff

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const attendanceID = `[1-9][0-9]{0,18}`
const attendanceDivision = `divisions/` + attendanceID

var attendanceReceipt = regexp.MustCompile(`^command-receipts/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var attendanceRoutes = regexp.MustCompile(`^(GET (calendars|schedule|me/(schedule|attendance)|resources/rooms|audit|` + attendanceDivision + `/(calendar|schedule|attendance-types|class-days|class-days/` + attendanceID + `|class-days/` + attendanceID + `/attendance|attendance-report|students|schedule-references|zaps|zaps/` + attendanceID + `|zaps/` + attendanceID + `/images/` + attendanceID + `))|PUT (` + attendanceDivision + `/(calendar|schedule/` + attendanceID + `|attendance-types/` + attendanceID + `|class-days/` + attendanceID + `))|POST (schedule/bulk|resources/rooms|` + attendanceDivision + `/(schedule|attendance-types|class-days|class-days/` + attendanceID + `/(attendance|scan|qr)|zaps|zaps/` + attendanceID + `/process|zaps/` + attendanceID + `/dates/[0-9]{4}-[0-9]{2}-[0-9]{2}/retry))|DELETE (` + attendanceDivision + `/(schedule/` + attendanceID + `|attendance-types/` + attendanceID + `|class-days/` + attendanceID + `|class-days/` + attendanceID + `/attendance/` + attendanceID + `|zaps/` + attendanceID + `/dates/[0-9]{4}-[0-9]{2}-[0-9]{2})))$`)
var attendanceImage = regexp.MustCompile(`^divisions/` + attendanceID + `/zaps/` + attendanceID + `/images/` + attendanceID + `$`)

func attendanceRoute(method, tail string) string {
	if method == "GET" && attendanceReceipt.MatchString(tail) {
		return "/v1/attendance/" + tail
	}
	if !attendanceRoutes.MatchString(method + " " + tail) {
		return ""
	}
	return "/v1/attendance/" + tail
}
func attendanceQuery(method, tail string, q url.Values) bool {
	allowed := map[string]bool{}
	if method == "GET" {
		if tail == "schedule" || tail == "me/schedule" || tail == "me/attendance" || strings.HasSuffix(tail, "/schedule") || strings.HasSuffix(tail, "/class-days") || strings.HasSuffix(tail, "/attendance-report") {
			allowed["date_from"] = true
			allowed["date_to"] = true
		}
		if tail == "schedule" {
			allowed["division_ids"] = true
		}
		if strings.HasSuffix(tail, "/attendance") && tail != "me/attendance" || strings.HasSuffix(tail, "/attendance-report") {
			allowed["account_id"] = true
			allowed["group_id"] = true
		}
		if tail == "audit" {
			allowed["after_revision"] = true
		}
	}
	for k, v := range q {
		if !allowed[k] || len(v) != 1 || v[0] == "" {
			return false
		}
	}
	return true
}

// Public image reads keep opaque-cookie auth and the same live session fence.
// JSON adaptation avoids browser image URLs without X-CPM-Context headers.
func (s *Server) attendanceImageResponse(w http.ResponseWriter, r *http.Request, path, token, rid string) {
	req, e := http.NewRequestWithContext(r.Context(), "GET", s.config.Attendance+path, nil)
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
	b, e := io.ReadAll(io.LimitReader(res.Body, (512<<10)+1))
	if e != nil || len(b) > 512<<10 {
		failure(w, 502, "invalid_upstream_response")
		return
	}
	if res.StatusCode != 200 {
		if res.StatusCode >= 300 && res.StatusCode < 400 {
			failure(w, 502, "invalid_upstream_response")
			return
		}
		upstream(w, res.StatusCode, b)
		return
	}
	mime := res.Header.Get("Content-Type")
	if mime != "image/png" && mime != "image/jpeg" {
		failure(w, 502, "invalid_upstream_response")
		return
	}
	reply(w, 200, map[string]any{"status": true, "data": map[string]any{"mime": mime, "data": base64.StdEncoding.EncodeToString(b)}})
}
