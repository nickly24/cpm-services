package bff

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

var testIDPath = regexp.MustCompile(`^[a-fA-F0-9]{24}$`)

// Browser routes never accept a division in their path. The session supplies it.
// The second return value is an owner read used to fence persisted work by scope.
func testsRoute(method, tail string) (string, string) {
	if external := externalTestsRoute(method, tail); external != "" {
		return external, ""
	}
	division := "/v1/tests/divisions/%d/"
	global := "/v1/tests/"
	key := method + " " + tail
	switch key {
	case "GET bank":
		return global + tail, ""
	case "GET catalog", "POST catalog":
		return division + "tests", ""
	case "GET catalog-with-access", "GET student-catalog":
		return division + tail, ""
	case "GET drafts", "POST drafts", "GET attempts", "GET submissions", "GET submissions/stats":
		return division + tail, ""
	}
	parts := strings.Split(tail, "/")
	if len(parts) == 1 && testIDPath.MatchString(parts[0]) && (method == "GET" || method == "PUT") {
		return global + tail, ""
	}
	if len(parts) == 2 && testIDPath.MatchString(parts[0]) {
		action := parts[1]
		if method == "GET" && (action == "content" || action == "history") || method == "PUT" && (action == "content" || action == "bank" || action == "archive") {
			return global + tail, ""
		}
		if method == "POST" && (action == "attach" || action == "copy" || action == "attempts" || action == "draft") || method == "PUT" && action == "settings" || method == "GET" && action == "access" {
			return division + "tests/" + tail, ""
		}
		if method == "DELETE" && action == "attachment" {
			return division + "tests/" + parts[0], ""
		}
	}
	if len(parts) < 2 || !testIDPath.MatchString(parts[1]) {
		return "", ""
	}
	if parts[0] == "drafts" {
		if len(parts) == 2 && (method == "GET" || method == "PUT" || method == "DELETE") || len(parts) == 3 && (parts[2] == "lock" && (method == "POST" || method == "DELETE") || parts[2] == "publish" && method == "POST") {
			return division + tail, ""
		}
	}
	if parts[0] == "attempts" {
		parent := global + strings.Join(parts[:2], "/")
		if len(parts) == 2 && (method == "GET" || method == "DELETE") {
			return parent, parent
		}
		if len(parts) == 3 && (method == "PATCH" && (parts[2] == "answer" || parts[2] == "answers") || method == "POST" && (parts[2] == "commits" || parts[2] == "finalize" || parts[2] == "submit" || parts[2] == "force-submit" || parts[2] == "practice-answer")) {
			return global + tail, parent
		}
	}
	if parts[0] == "submissions" {
		parent := global + strings.Join(parts[:2], "/")
		if len(parts) == 2 && method == "GET" || len(parts) == 3 && (method == "GET" && (parts[2] == "review" || parts[2] == "calculations") || method == "POST" && parts[2] == "retry-calculation") {
			return global + tail, parent
		}
	}
	return "", ""
}

func inSelectedDivision(raw []byte, selected *int64) bool {
	var response struct {
		Data struct {
			DivisionID int64 `json:"division_id"`
		} `json:"data"`
	}
	return selected != nil && json.Unmarshal(raw, &response) == nil && response.Data.DivisionID == *selected
}

// Status links belong to the browser contract, never to an owner service URL.
func testsBrowserLinks(raw []byte) []byte {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return raw
	}
	var rewrite func(any)
	rewrite = func(node any) {
		switch v := node.(type) {
		case map[string]any:
			for key, child := range v {
				if key == "status_url" {
					if url, ok := child.(string); ok && strings.HasPrefix(url, "/v1/tests/submissions/") {
						v[key] = strings.Replace(url, "/v1/tests/", "/bff/tests/", 1)
					}
				} else {
					rewrite(child)
				}
			}
		case []any:
			for _, child := range v {
				rewrite(child)
			}
		}
	}
	rewrite(value)
	result, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return result
}
