package bff

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

// Public operations only. The selected cabinet is injected from live auth;
// immutable calculation inputs and service-key callbacks never cross the BFF.
const ratingUUID = `[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}`

var ratingRoutes = regexp.MustCompile(`^(GET (jobs|jobs/` + ratingUUID + `|ratings|me|accounts/[1-9][0-9]{0,18}|versions|report)|POST (jobs|jobs/` + ratingUUID + `/(cancel|retry)))$`)
var ratingStatusURL = regexp.MustCompile(`^/v1/ratings/divisions/[1-9][0-9]*/jobs/(` + ratingUUID + `)$`)

func ratingsRoute(method, tail string) string {
	if !ratingRoutes.MatchString(method + " " + tail) {
		return ""
	}
	return "/v1/ratings/divisions/%d/" + tail
}

func ratingsQuery(method, tail string, query url.Values) bool {
	allowed := map[string]bool{}
	if method == "GET" {
		switch {
		case tail == "jobs":
			allowed["limit"], allowed["offset"] = true, true
		case tail == "ratings":
			allowed["q"], allowed["limit"], allowed["offset"], allowed["version"] = true, true, true, true
		case tail == "me" || tail == "report" || strings.HasPrefix(tail, "accounts/"):
			allowed["version"] = true
		}
	}
	for key, values := range query {
		if !allowed[key] || len(values) != 1 {
			return false
		}
	}
	return true
}

func ratingsBrowserLinks(raw []byte) []byte {
	var env map[string]json.RawMessage
	if json.Unmarshal(raw, &env) != nil {
		return raw
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(env["data"], &data) != nil {
		return raw
	}
	var link string
	if json.Unmarshal(data["status_url"], &link) != nil {
		return raw
	}
	match := ratingStatusURL.FindStringSubmatch(link)
	if len(match) == 0 {
		return raw
	}
	data["status_url"], _ = json.Marshal("/bff/ratings/jobs/" + match[1])
	env["data"], _ = json.Marshal(data)
	result, err := json.Marshal(env)
	if err != nil {
		return raw
	}
	return result
}
