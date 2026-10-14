package filework

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/minio/minio-go/v7"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Model an HTTP gateway that removes underscore headers before S3 verifies the
// signature. All credentials and bytes in this test are synthetic and local.
func TestS3UploadThroughUnderscoreStrippingGateway(t *testing.T) {
	storage := testS3(t, func(w http.ResponseWriter, r *http.Request) {
		for name := range r.Header {
			if strings.Contains(name, "_") {
				r.Header.Del(name)
			}
		}
		if !validSyntheticS3Signature(r) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, "<Error><Code>SignatureDoesNotMatch</Code><Message>synthetic signature mismatch</Message></Error>")
			return
		}
		if r.Header.Get("X-Amz-Meta-Job-Id") != validCommand().JobID || r.Header.Get("X-Amz-Meta-Page-Count") != "1" {
			t.Error("gateway lost scoped artifact metadata")
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
		}
		w.Header().Set("ETag", "\"synthetic-etag\"")
	})
	path := filepath.Join(t.TempDir(), "output.pdf")
	if err := os.WriteFile(path, []byte("%PDF-output"), 0600); err != nil {
		t.Fatal(err)
	}
	legacy := map[string]string{"job_id": validCommand().JobID, "organization_id": "11", "division_id": "12", "page_count": "1", "generation": "1", "sha256": strings.Repeat("a", 64)}
	_, err := storage.Client.FPutObject(context.Background(), storage.Bucket, validCommand().OutputKey, path, minio.PutObjectOptions{ContentType: "application/pdf", UserMetadata: legacy, DisableMultipart: true})
	if minio.ToErrorResponse(err).Code != "SignatureDoesNotMatch" {
		t.Fatalf("legacy signed headers must fail at the gateway: %v", err)
	}
	if err := storage.Upload(context.Background(), validCommand(), Artifact{Path: path, SizeBytes: 11, PageCount: 1, SHA256: strings.Repeat("a", 64)}); err != nil {
		t.Fatalf("hyphenated signed metadata failed: %v", err)
	}
}

// Verify the SigV4 request using its original SignedHeaders list, including
// missing headers as empty. Re-signing the surviving list would hide the bug.
func validSyntheticS3Signature(r *http.Request) bool {
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ")
	fields := map[string]string{}
	for _, part := range strings.Split(auth, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok {
			fields[k] = v
		}
	}
	credential := strings.Split(fields["Credential"], "/")
	if len(credential) != 5 || credential[0] != "test-access" || fields["SignedHeaders"] == "" {
		return false
	}
	var headers strings.Builder
	for _, name := range strings.Split(fields["SignedHeaders"], ";") {
		val := r.Header.Get(name)
		if name == "host" {
			val = r.Host
		}
		headers.WriteString(name + ":" + strings.Join(strings.Fields(val), " ") + "\n")
	}
	canonical := strings.Join([]string{r.Method, r.URL.EscapedPath(), r.URL.Query().Encode(), headers.String(), fields["SignedHeaders"], r.Header.Get("X-Amz-Content-Sha256")}, "\n")
	sum := sha256.Sum256([]byte(canonical))
	scope := strings.Join(credential[1:], "/")
	message := "AWS4-HMAC-SHA256\n" + r.Header.Get("X-Amz-Date") + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	mac := func(key []byte, value string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(value))
		return h.Sum(nil)
	}
	key := []byte("AWS4test-secret")
	for _, part := range credential[1:] {
		key = mac(key, part)
	}
	return hmac.Equal([]byte(hex.EncodeToString(mac(key, message))), []byte(fields["Signature"]))
}
