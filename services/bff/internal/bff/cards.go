package bff

import (
	"net/url"
	"regexp"
	"strings"
)

const cardsID = `[1-9][0-9]{0,18}`
const cardsSection = `sections/(manual/` + cardsID + `|test/[a-f0-9]{24})`
const cardsSession = `sessions/[a-f0-9]{32}`

// Imports belong to the excluded Upload screen. Sessions here support the
// existing card-to-test transformation; each owner also checks its creator.
var cardsRoutes = regexp.MustCompile(`^(GET (admin/catalog|catalog|tree|directions/` + cardsID + `/sections|` + cardsSection + `(/batches/[0-9]{1,6})?|themes/` + cardsID + `/cards|` + cardsSession + `)|POST (themes|themes/` + cardsID + `/cards|` + cardsSection + `/learned|transforms|` + cardsSession + `/commit)|PUT (themes/` + cardsID + `|themes/` + cardsID + `/cards/` + cardsID + `|` + cardsSection + `/settings|` + cardsSession + `)|DELETE (themes/` + cardsID + `|themes/` + cardsID + `/cards/` + cardsID + `|` + cardsSection + `/learned))$`)

func cardsRoute(method, tail string) string {
	if !cardsRoutes.MatchString(method + " " + tail) {
		return ""
	}
	return "/v1/cards/" + tail
}

func cardsQuery(method, tail string, q url.Values) bool {
	allowed := map[string]bool{}
	if method == "GET" {
		if strings.HasPrefix(tail, "directions/") {
			for _, key := range []string{"search", "kind", "progress", "page", "limit"} {
				allowed[key] = true
			}
		}
		if strings.Contains(tail, "/batches/") {
			allowed["study_mode"] = true
		}
	}
	for key, values := range q {
		if !allowed[key] || len(values) != 1 {
			return false
		}
	}
	return true
}
