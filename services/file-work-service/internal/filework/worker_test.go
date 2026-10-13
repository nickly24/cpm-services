package filework

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

func validCommand() Command {
	return Command{JobID: "00000000-0000-4000-8000-000000000001", Generation: 1, OrganizationID: 11, DivisionID: 12, SourceKey: "homework/11/12/staging/one.pdf", SourceETag: "etag1", OutputKey: "homework/11/12/processed/00000000-0000-4000-8000-000000000001/1.pdf", Limits: Limits{MaxSourceBytes: 10 * 1024 * 1024, MaxOutputBytes: 5 * 1024 * 1024, MaxPages: 35, DPI: 120, JPEGQuality: 75}}
}
func eventBytes(c Command) []byte {
	b, _ := json.Marshal(Envelope[Command]{MessageID: "message1", MessageType: CommandRoute, SchemaVersion: 1, OccurredAt: time.Now().UTC(), Producer: "homework", CorrelationID: "correlation1", Payload: c})
	return b
}

func TestCommandScopeAndResourcePolicy(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Command)
	}{
		{"zero_org", func(c *Command) { c.OrganizationID = 0 }}, {"zero_division", func(c *Command) { c.DivisionID = 0 }}, {"generation", func(c *Command) { c.Generation = 0 }}, {"job_uuid", func(c *Command) { c.JobID = "../../etc/passwd" }},
		{"foreign_source_org", func(c *Command) { c.SourceKey = "homework/10/12/staging/one.pdf" }}, {"foreign_source_division", func(c *Command) { c.SourceKey = "homework/11/13/staging/one.pdf" }}, {"source_traversal", func(c *Command) { c.SourceKey += "/../secret" }}, {"source_control", func(c *Command) { c.SourceKey += "\n" }}, {"source_not_staging", func(c *Command) { c.SourceKey = "homework/11/12/processed/one.pdf" }},
		{"missing_etag", func(c *Command) { c.SourceETag = "" }}, {"etag_header_injection", func(c *Command) { c.SourceETag = "etag\r\n" }}, {"foreign_output", func(c *Command) { c.OutputKey = "homework/11/13/processed/one.pdf" }}, {"output_generation", func(c *Command) { c.OutputKey = strings.Replace(c.OutputKey, "/1.pdf", "/2.pdf", 1) }},
		{"source_zero", func(c *Command) { c.Limits.MaxSourceBytes = 0 }}, {"source_ceiling", func(c *Command) { c.Limits.MaxSourceBytes = 101 * 1024 * 1024 }}, {"output_zero", func(c *Command) { c.Limits.MaxOutputBytes = 0 }}, {"output_ceiling", func(c *Command) { c.Limits.MaxOutputBytes = 101 * 1024 * 1024 }}, {"pages_zero", func(c *Command) { c.Limits.MaxPages = 0 }}, {"pages_ceiling", func(c *Command) { c.Limits.MaxPages = 201 }}, {"dpi_low", func(c *Command) { c.Limits.DPI = 71 }}, {"dpi_high", func(c *Command) { c.Limits.DPI = 301 }}, {"jpeg_low", func(c *Command) { c.Limits.JPEGQuality = 34 }}, {"jpeg_high", func(c *Command) { c.Limits.JPEGQuality = 96 }},
	}
	if _, err := DecodeCommand(eventBytes(validCommand())); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validCommand()
			tc.change(&c)
			if _, err := DecodeCommand(eventBytes(c)); err == nil {
				t.Fatal("invalid command accepted")
			}
		})
	}
}
func TestEnvelopeStrictDecoding(t *testing.T) {
	base := eventBytes(validCommand())
	cases := map[string][]byte{"unknown": []byte(strings.Replace(string(base), `"schema_version":1`, `"unknown":1,"schema_version":1`, 1)), "schema": []byte(strings.Replace(string(base), `"schema_version":1`, `"schema_version":2`, 1)), "producer": []byte(strings.Replace(string(base), `"producer":"homework"`, `"producer":"browser"`, 1)), "type": []byte(strings.Replace(string(base), CommandRoute, "bad", 1)), "trailing": append(append([]byte{}, base...), []byte(` {}`)...), "oversized": []byte(strings.Repeat(" ", 33*1024)), "missing": []byte(`{}`)}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeCommand(b); err == nil {
				t.Fatal("invalid envelope accepted")
			}
		})
	}
}

type fakeClaim struct {
	run        bool
	err        error
	calls      int
	releaseErr error
	releases   int
}

func (c *fakeClaim) Claim(context.Context, Command) (bool, string, error) {
	c.calls++
	return c.run, "00000000-0000-4000-8000-000000000002", c.err
}
func (c *fakeClaim) Release(context.Context, Command, string) error {
	c.releases++
	return c.releaseErr
}

type fakeStorage struct {
	downloadErr error
	uploadErr   error
	downloads   int
	uploads     int
	path        string
}

func (s *fakeStorage) Download(ctx context.Context, c Command, path string) error {
	s.downloads++
	s.path = path
	if s.downloadErr != nil {
		return s.downloadErr
	}
	return os.WriteFile(path, []byte("%PDF-1.6"), 0600)
}
func (s *fakeStorage) Upload(context.Context, Command, Artifact) error {
	s.uploads++
	return s.uploadErr
}

type fakeCompressor struct {
	err   error
	calls int
	block bool
}

func (c *fakeCompressor) Compress(ctx context.Context, source, dir string, l Limits) (Artifact, error) {
	c.calls++
	if c.block {
		<-ctx.Done()
		return Artifact{}, ctx.Err()
	}
	if c.err != nil {
		return Artifact{}, c.err
	}
	path := filepath.Join(dir, "output.pdf")
	if err := os.WriteFile(path, []byte("%PDF-processed"), 0600); err != nil {
		return Artifact{}, err
	}
	return Artifact{Path: path, SizeBytes: 14, PageCount: 1, SHA256: strings.Repeat("a", 64)}, nil
}
func engineFixtures(t *testing.T) (Engine, *fakeClaim, *fakeStorage, *fakeCompressor) {
	t.Helper()
	claim := &fakeClaim{run: true}
	storage := &fakeStorage{}
	compressor := &fakeCompressor{}
	return Engine{Claim: claim, Storage: storage, Compressor: compressor, TempDir: t.TempDir(), Timeout: time.Second}, claim, storage, compressor
}

func TestEngineClaimAndCleanup(t *testing.T) {
	e, _, s, p := engineFixtures(t)
	result, run, err := e.Process(context.Background(), validCommand())
	if err != nil || !run || result.SizeBytes != 14 || result.PageCount != 1 || s.uploads != 1 || p.calls != 1 {
		t.Fatalf("incorrect processing result: %+v %v %v", result, run, err)
	}
	if _, err = os.Stat(filepath.Dir(s.path)); !os.IsNotExist(err) {
		t.Fatal("temporary directory was retained")
	}
}
func TestEngineTerminalGenerationDoesNoFileWork(t *testing.T) {
	e, c, s, p := engineFixtures(t)
	c.run = false
	_, run, err := e.Process(context.Background(), validCommand())
	if err != nil || run || s.downloads != 0 || p.calls != 0 || s.uploads != 0 {
		t.Fatal("stale generation performed file work")
	}
}
func TestEngineFailureBoundaries(t *testing.T) {
	for _, stage := range []string{"claim", "download", "compress", "upload", "timeout"} {
		t.Run(stage, func(t *testing.T) {
			e, c, s, p := engineFixtures(t)
			switch stage {
			case "claim":
				c.err = errors.New("unavailable")
			case "download":
				s.downloadErr = reject("source_changed")
			case "compress":
				p.err = reject("active_content")
			case "upload":
				s.uploadErr = errors.New("unavailable")
			case "timeout":
				e.Timeout = 10 * time.Millisecond
				p.block = true
			}
			_, _, err := e.Process(context.Background(), validCommand())
			if err == nil {
				t.Fatal("failure swallowed")
			}
			if stage != "upload" && s.uploads != 0 {
				t.Fatal("failed pipeline uploaded output")
			}
			files, _ := os.ReadDir(e.TempDir)
			if len(files) != 0 {
				t.Fatal("temporary files leaked")
			}
		})
	}
}
func TestClaimHTTPContract(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		run    bool
		fails  bool
	}{"allow": {200, `{"status":true,"data":{"run":true,"claim_token":"00000000-0000-4000-8000-000000000002"}}`, true, false}, "terminal": {200, `{"status":true,"data":{"run":false}}`, false, false}, "busy": {409, `{}`, false, true}, "denied": {401, `{}`, false, true}, "missing_run": {200, `{"status":true,"data":{}}`, false, true}, "missing_token": {200, `{"status":true,"data":{"run":true}}`, false, true}, "invalid_token": {200, `{"status":true,"data":{"run":true,"claim_token":"invalid"}}`, false, true}, "failed_status": {200, `{"status":false,"data":{"run":false}}`, false, true}, "redirect": {302, `{}`, false, true}} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/internal/homework/file-jobs/"+validCommand().JobID+"/claim" || r.Header.Get("X-Service-Key") != "key" {
					t.Error("invalid service contract")
				}
				body, _ := io.ReadAll(r.Body)
				if string(body) != `{"generation":1}` {
					t.Error("wrong generation")
				}
				if tc.status == 302 {
					w.Header().Set("Location", "http://127.0.0.1:1")
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			run, _, err := (HomeworkClient{URL: server.URL, Key: "key"}).Claim(context.Background(), validCommand())
			if run != tc.run || (err != nil) != tc.fails {
				t.Fatalf("run %v err %v", run, err)
			}
		})
	}
}

func TestReleaseHTTPContract(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		fails  bool
	}{
		{"released", 200, `{"status":true,"data":{"ok":true}}`, false}, {"denied", 401, `{}`, true}, {"outage", 503, `{}`, true}, {"invalid_response", 200, `{"status":false}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/internal/homework/file-jobs/"+validCommand().JobID+"/release" || r.Header.Get("X-Service-Key") != "key" {
					t.Error("invalid release request")
				}
				var body struct {
					Generation int64  `json:"generation"`
					ClaimToken string `json:"claim_token"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil || body.Generation != 1 || body.ClaimToken != "00000000-0000-4000-8000-000000000002" {
					t.Error("release token/generation lost")
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			err := (HomeworkClient{URL: server.URL, Key: "key"}).Release(context.Background(), validCommand(), "00000000-0000-4000-8000-000000000002")
			if (err != nil) != tc.fails {
				t.Fatalf("release err %v", err)
			}
		})
	}
}

type fakeAcknowledger struct {
	acks  int
	nacks int
}

func (a *fakeAcknowledger) Ack(uint64, bool) error        { a.acks++; return nil }
func (a *fakeAcknowledger) Nack(uint64, bool, bool) error { a.nacks++; return nil }
func (a *fakeAcknowledger) Reject(uint64, bool) error     { a.nacks++; return nil }

type published struct {
	exchange string
	route    string
	message  amqp.Publishing
}
type fakePublisher struct {
	messages  []published
	err       error
	failAfter int
}

func (p *fakePublisher) Publish(ctx context.Context, exchange, route string, m amqp.Publishing) error {
	p.messages = append(p.messages, published{exchange, route, m})
	if p.err != nil && (p.failAfter == 0 || len(p.messages) >= p.failAfter) {
		return p.err
	}
	return nil
}
func delivery(c Command, attempt int) (amqp.Delivery, *fakeAcknowledger) {
	a := &fakeAcknowledger{}
	return amqp.Delivery{Acknowledger: a, DeliveryTag: 1, Body: eventBytes(c), Headers: amqp.Table{"filework-attempt": int32(attempt)}}, a
}
func brokerFixture(t *testing.T) (*BrokerWorker, *fakeClaim, *fakeStorage, *fakeCompressor) {
	e, c, s, p := engineFixtures(t)
	return &BrokerWorker{Engine: e, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, c, s, p
}

func TestDeliveryResultConfirmedBeforeAck(t *testing.T) {
	w, _, _, _ := brokerFixture(t)
	d, a := delivery(validCommand(), 0)
	p := &fakePublisher{}
	if err := w.deliver(context.Background(), p, d); err != nil {
		t.Fatal(err)
	}
	if a.acks != 1 || len(p.messages) != 1 || p.messages[0].route != ResultRoute {
		t.Fatal("result not published")
	}
	var event Envelope[Result]
	if json.Unmarshal(p.messages[0].message.Body, &event) != nil || event.Payload.ErrorCode != "" || event.Payload.Generation != 1 || event.Payload.OutputKey != validCommand().OutputKey || event.Producer != "file-work-service" {
		t.Fatal("wrong completion envelope")
	}
}
func TestDeliveryPublishFailureNeverAck(t *testing.T) {
	for _, stage := range []string{"success", "rejection", "retry", "invalid", "dead_letter"} {
		t.Run(stage, func(t *testing.T) {
			w, _, s, c := brokerFixture(t)
			d, a := delivery(validCommand(), 0)
			p := &fakePublisher{err: errors.New("not_confirmed")}
			switch stage {
			case "rejection":
				c.err = reject("encrypted_pdf")
			case "retry":
				s.downloadErr = errors.New("s3_failure")
			case "invalid":
				d.Body = []byte(`{}`)
			case "dead_letter":
				s.downloadErr = errors.New("s3_failure")
				d.Headers["filework-attempt"] = int32(3)
				p.failAfter = 2
			}
			if err := w.deliver(context.Background(), p, d); err == nil {
				t.Fatal("publish failure swallowed")
			}
			if a.acks != 0 {
				t.Fatal("acknowledged before confirmed publish")
			}
		})
	}
}
func TestDeliveryBoundedRetriesAndDeadLetter(t *testing.T) {
	for attempt := 0; attempt <= 3; attempt++ {
		t.Run(string(rune('0'+attempt)), func(t *testing.T) {
			w, _, s, _ := brokerFixture(t)
			s.downloadErr = errors.New("unavailable")
			d, a := delivery(validCommand(), attempt)
			p := &fakePublisher{}
			if err := w.deliver(context.Background(), p, d); err != nil {
				t.Fatal(err)
			}
			if a.acks != 1 {
				t.Fatal("input not acknowledged after durable retry")
			}
			if attempt < 3 {
				if len(p.messages) != 1 || p.messages[0].route != retryQueue(attempt) || p.messages[0].message.Headers["filework-attempt"] != int32(attempt+1) {
					t.Fatal("retry routed incorrectly")
				}
			} else {
				if len(p.messages) != 2 || p.messages[0].route != ResultRoute || p.messages[1].route != DeadQueue {
					t.Fatal("exhausted failure missing terminal result or DLQ")
				}
				var e Envelope[Result]
				json.Unmarshal(p.messages[0].message.Body, &e)
				if e.Payload.ErrorCode != "processing_failed" {
					t.Fatal("wrong terminal error")
				}
			}
		})
	}
}
func TestDeliveryRejectDoesNotRetryAndStaleDoesNotPublish(t *testing.T) {
	t.Run("reject", func(t *testing.T) {
		w, _, _, c := brokerFixture(t)
		c.err = reject("active_content")
		d, a := delivery(validCommand(), 0)
		p := &fakePublisher{}
		if err := w.deliver(context.Background(), p, d); err != nil || a.acks != 1 || len(p.messages) != 1 || p.messages[0].route != ResultRoute {
			t.Fatal("rejected content retried")
		}
	})
	t.Run("stale", func(t *testing.T) {
		w, c, _, _ := brokerFixture(t)
		c.run = false
		d, a := delivery(validCommand(), 0)
		p := &fakePublisher{}
		if err := w.deliver(context.Background(), p, d); err != nil || a.acks != 1 || len(p.messages) != 0 {
			t.Fatal("stale output published")
		}
	})
}
func TestDeliveryShutdownDoesNotAck(t *testing.T) {
	w, _, _, p := brokerFixture(t)
	p.block = true
	d, a := delivery(validCommand(), 0)
	publisher := &fakePublisher{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.deliver(ctx, publisher, d); err == nil || a.acks != 0 || len(publisher.messages) != 0 {
		t.Fatal("shutdown acknowledged input")
	}
}

func TestDangerousContentAndMetadata(t *testing.T) {
	for key := range dangerousKeys {
		if !dangerous(map[string]any{"object": map[string]any{"value": map[string]any{key: nil}}}) {
			t.Fatal("unsafe key not detected", key)
		}
	}
	for action := range dangerousActions {
		if !dangerous(map[string]any{"/S": action}) {
			t.Fatal("unsafe action not detected", action)
		}
	}
	for _, subtype := range []string{"/EmbeddedFile", "/RichMedia", "/Movie", "/Sound", "/3D"} {
		if !dangerous(map[string]any{"/Subtype": subtype}) {
			t.Fatal("unsafe subtype not detected", subtype)
		}
	}
	if dangerous(map[string]any{"/Text": "u:/JavaScript is plain text", "/S": "/GoTo", "/Info": "1 0 R"}) {
		t.Fatal("plain text/local link rejected")
	}
	doc := map[string]any{"trailer": map[string]any{"/Info": "1 0 R"}, "stream": map[string]any{"dict": map[string]any{"/Metadata": "2 0 R", "/Other": true}}}
	removeMetadata(doc)
	if containsMetadata(doc) {
		t.Fatal("metadata survived")
	}
	if doc["stream"].(map[string]any)["dict"].(map[string]any)["/Other"] != true {
		t.Fatal("content changed")
	}
}
