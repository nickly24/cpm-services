package bff

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func TestUploadsOpenAPIPublicBoundary(t *testing.T) {
	var document struct {
		Paths map[string]map[string]json.RawMessage
	}
	if e := json.Unmarshal(openAPI, &document); e != nil {
		t.Fatal(e)
	}
	params := regexp.MustCompile(`\{[^}]+\}`)
	operations := 0
	for path, methods := range document.Paths {
		if !strings.HasPrefix(path, "/bff/uploads/") {
			continue
		}
		tail := strings.ReplaceAll(strings.TrimPrefix(path, "/bff/uploads/"), "{type}", "cards.xlsx")
		tail = params.ReplaceAllString(tail, strings.Repeat("a", 32))
		for method := range methods {
			if !oneHTTPMethod(method) {
				continue
			}
			operations++
			if uploadsRoute(strings.ToUpper(method), tail) == "" {
				t.Errorf("unroutable uploads public contract: %s %s", method, path)
			}
		}
	}
	if operations != 19 {
		t.Fatalf("uploads public operations=%d, want19", operations)
	}
	for _, action := range []string{"recover", "revoke"} {
		if _, ok := document.Paths["/bff/uploads/jobs/{job}/"+action]["post"]; !ok {
			t.Errorf("missing public background job action: %s", action)
		}
	}
	for _, tail := range []string{"internal/jobs", "divisions/{division}/jobs", "homework/{target}/{action}"} {
		if _, ok := document.Paths["/bff/uploads/"+tail]; ok {
			t.Errorf("nonpublic or unavailable route documented: %s", tail)
		}
	}
}
