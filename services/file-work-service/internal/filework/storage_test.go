package filework

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func testS3(t *testing.T, handler http.HandlerFunc) *S3Storage {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	storage, err := NewS3Storage(Config{S3Endpoint: server.URL, S3Region: "us-east-1", S3Bucket: "test-bucket", S3AccessKey: "test-access", S3SecretKey: "test-secret"})
	if err != nil {
		t.Fatal(err)
	}
	return storage
}
func TestS3SourceETagAndStreamingLimit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		etag   string
		length int
		status int
		code   string
	}{
		{"valid", "etag1", 8, 200, ""}, {"changed", "etag2", 8, 200, "source_changed"}, {"conditional", "etag1", 8, 412, "source_changed"}, {"oversize", "etag1", 11 * 1024 * 1024, 200, "source_too_large"}, {"missing", "etag1", 8, 404, "source_not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gets atomic.Int32
			storage := testS3(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					gets.Add(1)
					if r.Header.Get("If-Match") != "\"etag1\"" {
						t.Error("source download missing immutable ETag")
					}
				}
				if tc.status != 200 {
					w.WriteHeader(tc.status)
					code := "PreconditionFailed"
					if tc.status == 404 {
						code = "NoSuchKey"
					}
					fmt.Fprintf(w, "<Error><Code>%s</Code><Message>test</Message></Error>", code)
					return
				}
				w.Header().Set("ETag", "\""+tc.etag+"\"")
				w.Header().Set("Content-Length", fmt.Sprint(tc.length))
				w.Header().Set("Content-Type", "application/pdf")
				w.Header().Set("Last-Modified", "Wed, 07 Oct 2026 00:00:00 GMT")
				if r.Method == "GET" {
					w.Write([]byte("%PDF-1.6"))
				}
			})
			err := storage.Download(context.Background(), validCommand(), filepath.Join(t.TempDir(), "source.pdf"))
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				code, ok := permanentCode(err)
				if !ok || code != tc.code {
					t.Fatalf("expected %s got %v", tc.code, err)
				}
			}
			if gets.Load() == 0 && tc.status == 200 && tc.code == "" {
				t.Fatal("download not requested")
			}
		})
	}
}
func TestS3UploadDeterministicKeyAndIntegrityMetadata(t *testing.T) {
	var uploads atomic.Int32
	storage := testS3(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.Path != "/test-bucket/"+validCommand().OutputKey {
			t.Errorf("unexpected upload route %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "%PDF-output") {
			t.Error("PDF body lost")
		}
		if r.Header.Get("X-Amz-Meta-Sha256") != strings.Repeat("a", 64) || r.Header.Get("X-Amz-Meta-Page-Count") != "1" || r.Header.Get("Content-Type") != "application/pdf" {
			t.Error("integrity metadata missing")
		}

		for name, value := range map[string]string{
			"X-Amz-Meta-Job-Id":          validCommand().JobID,
			"X-Amz-Meta-Organization-Id": fmt.Sprint(validCommand().OrganizationID),
			"X-Amz-Meta-Division-Id":     fmt.Sprint(validCommand().DivisionID),
			"X-Amz-Meta-Generation":      fmt.Sprint(validCommand().Generation),
		} {
			if r.Header.Get(name) != value {
				t.Errorf("missing scoped metadata %s", name)
			}
		}
		for name := range r.Header {
			if strings.HasPrefix(strings.ToLower(name), "x-amz-meta-") && strings.Contains(name, "_") {
				t.Errorf("S3 proxy-incompatible metadata header: %s", name)
			}
		}
		uploads.Add(1)
		w.Header().Set("ETag", "\"etag2\"")
	})
	path := filepath.Join(t.TempDir(), "output.pdf")
	os.WriteFile(path, []byte("%PDF-output"), 0600)
	if err := storage.Upload(context.Background(), validCommand(), Artifact{Path: path, SizeBytes: 11, PageCount: 1, SHA256: strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	if uploads.Load() != 1 {
		t.Fatal("upload missing")
	}
}
func TestBusyLeaseWaitAndReleaseFailure(t *testing.T) {
	for _, mode := range []string{"busy", "release_failed", "released"} {
		t.Run(mode, func(t *testing.T) {
			w, claim, storage, _ := brokerFixture(t)
			storage.downloadErr = fmt.Errorf("temporary")
			if mode == "busy" {
				claim.err = fmt.Errorf("job_busy")
			} else if mode == "release_failed" {
				claim.releaseErr = fmt.Errorf("release_failed")
			}
			d, a := delivery(validCommand(), 1)
			publisher := &fakePublisher{}
			if err := w.deliver(context.Background(), publisher, d); err != nil {
				t.Fatal(err)
			}
			if a.acks != 1 || len(publisher.messages) != 1 {
				t.Fatal("retry missing")
			}
			message := publisher.messages[0]
			if mode == "released" {
				if message.route != retryQueue(1) || claim.releases != 1 {
					t.Fatal("released job not retried")
				}
			} else if message.route != leaseWaitQueue {
				t.Fatal("active lease not respected")
			}
			expected := int32(2)
			if mode == "busy" || mode == "release_failed" {
				expected = 1
			}
			if message.message.Headers["filework-attempt"] != expected {
				t.Fatal("busy lease consumed processing retry")
			}
		})
	}
}
