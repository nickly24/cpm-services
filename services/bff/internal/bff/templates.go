package bff

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"unicode/utf8"
)

const xlsxType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

// The OTP download is data wrapped in the owner's fixed assignment, never
// executed. Other JavaScript and JSON responses are not download templates.
func validTestTemplate(data []byte, otp bool) bool {
	if !utf8.Valid(data) {
		return false
	}
	data = bytes.TrimSpace(data)
	if otp {
		if !bytes.HasPrefix(data, []byte("var test = ")) || !bytes.HasSuffix(data, []byte(";")) {
			return false
		}
		data = bytes.TrimSpace(data[len("var test = ") : len(data)-1])
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(data, &value) != nil || value == nil || value["error"] != nil {
		return false
	}
	name, questions := "title", "questions"
	if otp {
		name, questions = "Name", "Questions"
	}
	var title string
	var rows []json.RawMessage
	return json.Unmarshal(value[name], &title) == nil && title != "" &&
		json.Unmarshal(value[questions], &rows) == nil && len(rows) > 0 && len(rows) <= 5000
}

// Called only after the live session, context fence and public route/query
// checks. The owner authorizes the template; browser service keys are dropped.
func (s *Server) templateResponse(w http.ResponseWriter, r *http.Request, base, path, token, rid string) {
	req, e := http.NewRequestWithContext(r.Context(), "GET", base+path, nil)
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
	data, e := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if e != nil || len(data) > 8<<20 || res.StatusCode >= 300 && res.StatusCode < 400 {
		failure(w, 502, "invalid_upstream_response")
		return
	}
	if res.StatusCode != 200 {
		upstream(w, res.StatusCode, data)
		return
	}
	contentType, _, e := mime.ParseMediaType(res.Header.Get("Content-Type"))
	testsJSON := r.URL.Path == "/bff/uploads/templates/tests.json"
	otp := testsJSON && r.URL.Query().Get("format") == "online_test_pad"
	jsonTemplate := testsJSON && !otp && contentType == "application/json" && validTestTemplate(data, false)
	jsTemplate := otp && contentType == "text/javascript" && validTestTemplate(data, true)
	if e != nil || testsJSON && !jsonTemplate && !jsTemplate || !testsJSON && contentType != xlsxType && contentType != "text/csv" {
		failure(w, 502, "invalid_upstream_response")
		return
	}
	name := "import-template.xlsx"
	if contentType == "text/csv" {
		name = "import-template.csv"
		contentType += "; charset=utf-8"
	}
	if jsonTemplate || jsTemplate {
		name = "import-template.json"
		if jsTemplate {
			name = "import-template.js"
		}
		contentType += "; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	_, _ = w.Write(data)
}
