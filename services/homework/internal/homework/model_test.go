package homework

import (
	"context"
	"encoding/json"
	"github.com/minio/minio-go/v7"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCalendarDateValidation(t *testing.T) {
	for _, c := range []struct {
		day  string
		good bool
	}{{"2024-02-29", true}, {"2026-02-29", false}, {"0000-01-01", false}, {"0999-12-31", false}, {"1000-01-01", true}, {"9999-12-31", true}, {"2026-10-7", false}} {
		if _, e := parseDay(c.day); (e == nil) != c.good {
			t.Errorf("%s valid=%v", c.day, c.good)
		}
	}
}
func TestStorageOutageIsNotMissingUpload(t *testing.T) {
	for _, c := range []struct {
		code string
		want int
	}{{"NoSuchKey", 409}, {"NoSuchObject", 409}, {"AccessDenied", 503}, {"NoSuchBucket", 503}, {"InternalError", 503}} {
		if e := sourceHeadError(minio.ErrorResponse{Code: c.code}, "missing"); asError(e).Code != c.want {
			t.Errorf("%s got %v", c.code, e)
		}
	}
}

func TestMoscowLateScore(t *testing.T) {
	deadline := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		at   string
		want int
	}{{"2026-10-06T21:00:00Z", 100}, {"2026-10-07T20:59:59Z", 100}, {"2026-10-07T21:00:00Z", 95}, {"2026-10-26T21:00:00Z", 0}, {"2026-09-01T00:00:00Z", 100}, {"2027-10-07T00:00:00Z", 0}} {
		at, _ := time.Parse(time.RFC3339, c.at)
		if n := suggested(deadline, at); n != c.want {
			t.Errorf("%s got %d want %d", c.at, n, c.want)
		}
	}
}
func TestPolicyBounds(t *testing.T) {
	def := DefaultSettings()
	if def.Validate() != nil {
		t.Fatal("invalid defaults")
	}
	for _, field := range []struct {
		name   string
		set    func(*Settings, int)
		lo, hi int
	}{{"source", func(s *Settings, n int) { s.MaxSourceBytes = int64(n) }, 1, 100 << 20}, {"output", func(s *Settings, n int) { s.MaxOutputBytes = int64(n) }, 1, 100 << 20}, {"pages", func(s *Settings, n int) { s.MaxPages = n }, 1, 200}, {"dpi", func(s *Settings, n int) { s.DPI = n }, 72, 300}, {"jpeg", func(s *Settings, n int) { s.JPEGQuality = n }, 35, 95}, {"presign", func(s *Settings, n int) { s.PresignTTLSeconds = n; s.UploadStaleSeconds = 2 * n }, 60, 900}, {"stale", func(s *Settings, n int) { s.UploadStaleSeconds = n }, 600, 86400}} {
		for _, n := range []int{field.lo - 1, field.lo, field.hi, field.hi + 1} {
			t.Run(field.name+"/"+strconv.Itoa(n), func(t *testing.T) {
				s := def
				field.set(&s, n)
				good := n >= field.lo && n <= field.hi
				if (s.Validate() == nil) != good {
					t.Errorf("value %d expected valid=%v", n, good)
				}
			})
		}
	}
}
func TestPermissionsAndGroupBoundary(t *testing.T) {
	g1, g2 := int64(1), int64(2)
	student := Member{AccountID: 7, Role: "student", GroupID: &g1}
	for _, c := range []struct {
		name               string
		a                  Actor
		access, view, edit bool
	}{{"own", Actor{Role: "student", AccountID: 7}, true, false, false}, {"foreign student", Actor{Role: "student", AccountID: 8}, false, false, false}, {"own group", Actor{Role: "proctor", GroupID: &g1}, true, false, false}, {"foreign group", Actor{Role: "proctor", GroupID: &g2}, false, false, false}, {"no group", Actor{Role: "proctor"}, false, false, false}, {"full admin", Actor{Role: "admin", OrganizationAdmin: true}, true, true, true}, {"staff read", Actor{Role: "staff_admin", Permissions: map[string]Permission{"assignments": {View: true}}}, true, true, false}, {"staff edit without view", Actor{Role: "staff_admin", Permissions: map[string]Permission{"assignments": {Edit: true}}}, true, false, false}, {"supervisor", Actor{Role: "supervisor"}, false, false, false}} {
		t.Run(c.name, func(t *testing.T) {
			if c.a.Access(student) != c.access || c.a.Can("assignments", false) != c.view || c.a.Can("assignments", true) != c.edit {
				t.Fatal("permission boundary violated")
			}
		})
	}
}
func TestStrictJSONRequest(t *testing.T) {
	for _, c := range []struct {
		body, ctype string
		valid       bool
	}{{`{"name":"x"}`, "application/json", true}, {`{"name":"x"}`, "application/json; charset=utf-8", true}, {`{"name":"x","extra":1}`, "application/json", false}, {`{"name":"x"} {}`, "application/json", false}, {`{`, "application/json", false}, {`{"name":"x"}`, "text/plain", false}} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(c.body))
		r.Header.Set("Content-Type", c.ctype)
		var v TypeDTO
		if (decode(r, &v) == nil) != c.valid {
			t.Errorf("unexpected validation for %s", c.body)
		}
	}
}
func TestFilenames(t *testing.T) {
	s := fileName("a/b\n", "c\\d\r", time.Date(2026, 10, 7, 22, 0, 0, 0, time.UTC))
	if strings.ContainsAny(s, "/\\\r\n") || !strings.HasSuffix(s, "08.10.2026.pdf") {
		t.Fatal(s)
	}
}
func TestIntrospectionNeverTrustsCallerRole(t *testing.T) {
	for _, c := range []struct {
		name, body string
		code, want int
	}{{"active", `{"status":true,"active":true,"user":{"account_id":1,"id":5,"organization_id":9,"role":"student","organization_admin":true}}`, 200, 0}, {"revoked", `{}`, 401, 401}, {"auth down", `{}`, 503, 503}, {"inactive", `{"status":true,"active":false}`, 200, 503}, {"malformed", `{`, 200, 503}} {
		t.Run(c.name, func(t *testing.T) {
			auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/v1/auth/introspect" {
					t.Error("wrong auth request")
				}
				w.WriteHeader(c.code)
				w.Write([]byte(c.body))
			}))
			defer auth.Close()
			d := Dependencies{AuthURL: auth.URL, HTTP: auth.Client()}
			r := httptest.NewRequest("GET", "/", nil)
			r.Header.Set("Authorization", "Bearer dev")
			r.Header.Set("X-Role", "admin")
			a, e := d.Identify(r)
			if c.want == 0 {
				if e != nil || a.OrganizationAdmin || a.AccountID != 1 || a.ProfileID != 5 {
					t.Fatal("trusted caller or profile/account mixed")
				}
			} else if e == nil || asError(e).Code != c.want {
				t.Fatalf("want %d got %v", c.want, e)
			}
		})
	}
}
func TestCoreReferencesScope(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":true,"data":{"organization_id":99,"division_id":2}}`))
	}))
	defer core.Close()
	d := Dependencies{CoreURL: core.URL, HTTP: core.Client()}
	if _, e := d.References(context.Background(), 1, 2); e == nil {
		t.Fatal("foreign scope accepted")
	}
}
func TestResultContractFencing(t *testing.T) {
	p := ProcessResult{JobID: uuid(), Generation: 1, OrganizationID: 1, DivisionID: 2, SizeBytes: 8, PageCount: 1, SHA256: strings.Repeat("a", 64), ClaimToken: uuid()}
	p.OutputKey = "homework/1/2/processed/" + p.JobID + "/1.pdf"
	ev := resultEnvelope{MessageID: "filework:" + p.JobID + ":1:" + p.ClaimToken + ":result", MessageType: resultRoute, SchemaVersion: 1, Producer: "file-work-service", OccurredAt: time.Now().UTC(), Payload: p}
	marshal := func(e resultEnvelope) []byte { b, _ := json.Marshal(e); return b }
	if _, e := decodeResult(marshal(ev)); e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*resultEnvelope){func(e *resultEnvelope) { e.Producer = "other" }, func(e *resultEnvelope) { e.SchemaVersion = 2 }, func(e *resultEnvelope) { e.Payload.ClaimToken = "" }, func(e *resultEnvelope) { e.Payload.OutputKey = "homework/other.pdf" }, func(e *resultEnvelope) { e.Payload.SizeBytes = 0 }, func(e *resultEnvelope) { e.Payload.SHA256 = "invalid" }, func(e *resultEnvelope) { e.MessageID = "old-attempt" }, func(e *resultEnvelope) { e.Payload.ErrorCode = "invented" }} {
		copy := ev
		mutate(&copy)
		if _, e := decodeResult(marshal(copy)); e == nil {
			t.Fatal("invalid result accepted")
		}
	}
	for _, code := range []string{"corrupt_pdf", "invalid_pdf_signature", "processed_too_large", "metadata_not_removed", "source_not_found", "encrypted_pdf", "active_content", "too_many_pages", "source_changed", "processing_failed"} {
		copy := ev
		copy.Payload.ErrorCode = code
		copy.Payload.SizeBytes = 0
		copy.Payload.PageCount = 0
		copy.Payload.SHA256 = ""
		if _, e := decodeResult(marshal(copy)); e != nil {
			t.Errorf("worker error %s rejected", code)
		}
	}
}
func TestRecoveryFencesOldGeneration(t *testing.T) {
	h := map[string]any{"deleted_at": nil}
	sub := map[string]any{"state": "processing", "generation": int64(2)}
	j := map[string]any{"generation": int64(1)}
	if currentJob(h, sub, j) {
		t.Fatal("old generation active")
	}
	j["generation"] = int64(2)
	if !currentJob(h, sub, j) {
		t.Fatal("current generation blocked")
	}
	sub["state"] = "submitted"
	if currentJob(h, sub, j) {
		t.Fatal("submitted changed by worker")
	}
	sub["state"] = "processing"
	h["deleted_at"] = time.Now()
	if currentJob(h, sub, j) {
		t.Fatal("deleted changed by worker")
	}
}
