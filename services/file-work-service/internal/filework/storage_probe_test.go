package filework

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

const testInstanceID = "00000000-0000-4000-8000-000000000099"

type fakeProbeObjects struct {
	putErr, readErr, deleteErr error
	transform                  func([]byte) []byte
	putFunc                    func(context.Context)
	readFunc                   func(context.Context)
	deleteFunc                 func(context.Context)
	marker                     []byte
	keys                       []string
	versions                   []string
	puts, reads, deletes       int
	limit                      int64
}

func (f *fakeProbeObjects) putProbe(ctx context.Context, key string, marker []byte) (string, error) {
	f.puts++
	f.keys = append(f.keys, key)
	f.marker = append([]byte(nil), marker...)
	if f.putFunc != nil {
		f.putFunc(ctx)
	}
	return "probe-version", f.putErr
}
func (f *fakeProbeObjects) readProbe(ctx context.Context, key, version string, limit int64) ([]byte, error) {
	f.reads++
	f.keys = append(f.keys, key)
	f.versions = append(f.versions, version)
	f.limit = limit
	if f.readFunc != nil {
		f.readFunc(ctx)
	}
	body := append([]byte(nil), f.marker...)
	if f.transform != nil {
		body = f.transform(body)
	}
	return body, f.readErr
}
func (f *fakeProbeObjects) deleteProbe(ctx context.Context, key, version string) error {
	f.deletes++
	f.keys = append(f.keys, key)
	f.versions = append(f.versions, version)
	if f.deleteFunc != nil {
		f.deleteFunc(ctx)
	}
	return f.deleteErr
}

func TestProbeCapabilityFailuresAndCleanup(t *testing.T) {
	for _, mode := range []string{"ok", "put_failed", "read_failed", "short", "long", "mismatch", "delete_failed"} {
		t.Run(mode, func(t *testing.T) {
			f := &fakeProbeObjects{}
			switch mode {
			case "put_failed":
				f.putErr = errors.New("ambiguous write")
			case "read_failed":
				f.readErr = errors.New("read failed")
			case "short":
				f.transform = func(b []byte) []byte { return b[:len(b)-1] }
			case "long":
				f.transform = func(b []byte) []byte { return append(b, 'x') }
			case "mismatch":
				f.transform = func(b []byte) []byte { b[0] = 'x'; return b }
			case "delete_failed":
				f.deleteErr = errors.New("delete failed")
			}
			err := probeCapability(context.Background(), f, testInstanceID, time.Second, time.Second)
			if (err == nil) != (mode == "ok") {
				t.Fatalf("mode %s got %v", mode, err)
			}
			if f.puts != 1 || f.deletes != 1 {
				t.Fatalf("missing write/cleanup: %#v", f)
			}
			if mode == "put_failed" && f.reads != 0 {
				t.Fatal("read after failed write")
			}
			for _, key := range f.keys {
				if key != "file-work-health/"+testInstanceID+"/probe" {
					t.Fatal("probe escaped isolated key")
				}
			}
			for _, version := range f.versions {
				if version != "probe-version" {
					t.Fatal("wrong version accessed")
				}
			}
			if mode != "put_failed" && f.limit != int64(len(f.marker)+1) {
				t.Fatal("unbounded probe read")
			}
		})
	}
}

func TestProbeCapabilityFreshBytesAndUUIDScope(t *testing.T) {
	f := &fakeProbeObjects{}
	if err := probeCapability(context.Background(), f, testInstanceID, time.Second, time.Second); err != nil {
		t.Fatal(err)
	}
	first := append([]byte(nil), f.marker...)
	if err := probeCapability(context.Background(), f, testInstanceID, time.Second, time.Second); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, f.marker) {
		t.Fatal("old bytes could satisfy a new probe")
	}
	for _, instance := range []string{"", "../homework", testInstanceID + "/../data"} {
		f := &fakeProbeObjects{}
		if err := probeCapability(context.Background(), f, instance, time.Second, time.Second); err == nil || f.puts+f.deletes != 0 {
			t.Fatal("invalid instance touched storage")
		}
	}
}

func TestProbeCapabilityTimeoutAndIndependentCleanup(t *testing.T) {
	for _, phase := range []string{"put", "read", "delete", "parent_cancelled"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f := &fakeProbeObjects{}
			block := func(ctx context.Context) { <-ctx.Done() }
			if phase == "put" {
				f.putFunc = block
			}
			if phase == "read" {
				f.readFunc = block
			}
			if phase == "delete" {
				f.deleteFunc = block
			} else {
				f.deleteFunc = func(cleanupCtx context.Context) {
					if cleanupCtx.Err() != nil {
						t.Error("cleanup inherited canceled operation")
					}
				}
			}
			if phase == "parent_cancelled" {
				f.readFunc = func(context.Context) { cancel() }
			}
			started := time.Now()
			if err := probeCapability(ctx, f, testInstanceID, 10*time.Millisecond, 10*time.Millisecond); err == nil {
				t.Fatal("expired probe counted as success")
			}
			if f.deletes != 1 || time.Since(started) > time.Second {
				t.Fatal("cleanup missing or unbounded")
			}
		})
	}
}

func TestS3CapabilityProbeExactRequestSequence(t *testing.T) {
	for _, mode := range []string{"ok", "read_http_failed", "read_mismatch", "delete_failed"} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			var marker []byte
			var methods []string
			storage := testS3(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				methods = append(methods, r.Method)
				if r.URL.Path != "/test-bucket/file-work-health/"+testInstanceID+"/probe" {
					t.Error("health probe touched business object")
				}
				switch r.Method {
				case "PUT":
					body, _ := io.ReadAll(r.Body)
					// SDK streaming signature surrounds the tiny payload in chunks.
					start := bytes.Index(body, []byte("file-work-service capability probe v1\n"))
					markerLength := len("file-work-service capability probe v1\n") + 64
					if start < 0 || start+markerLength > len(body) {
						t.Error("synthetic payload missing")
						w.WriteHeader(400)
						return
					}
					marker = append([]byte(nil), body[start:start+markerLength]...)
					w.Header().Set("ETag", "\"probe-etag\"")
					w.Header().Set("X-Amz-Version-Id", "health-version")
				case "GET":
					if r.URL.Query().Get("versionId") != "health-version" {
						t.Error("probe did not read exact new version")
					}
					if mode == "read_http_failed" {
						w.WriteHeader(403)
						fmt.Fprint(w, "<Error><Code>AccessDenied</Code></Error>")
						return
					}
					body := append([]byte(nil), marker...)
					if mode == "read_mismatch" {
						body[0] = 'x'
					}
					w.Header().Set("ETag", "\"probe-etag\"")
					w.Header().Set("Last-Modified", "Fri, 09 Oct 2026 00:00:00 GMT")
					w.Header().Set("Content-Length", fmt.Sprint(len(body)))
					w.Write(body)
				case "DELETE":
					if r.URL.Query().Get("versionId") != "health-version" {
						t.Error("probe version retained after delete")
					}
					if mode == "delete_failed" {
						w.WriteHeader(403)
						fmt.Fprint(w, "<Error><Code>AccessDenied</Code></Error>")
						return
					}
					w.WriteHeader(204)
				default:
					t.Errorf("unexpected capability request %s", r.Method)
				}
			})
			err := storage.Probe(context.Background(), testInstanceID)
			if (err == nil) != (mode == "ok") {
				t.Fatalf("mode %s got %v", mode, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if fmt.Sprint(methods) != "[PUT GET DELETE]" {
				t.Fatalf("cleanup/request sequence %v", methods)
			}
		})
	}
}
