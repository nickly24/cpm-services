package homework

import (
	"github.com/minio/minio-go/v7"
	"testing"
)

func TestArtifactMetadataAcceptsHistoricalAndHTTPHeaderNames(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values map[string]string
	}{
		{"historical", map[string]string{"Job_id": "job", "Organization_id": "11", "Division_id": "12", "Page_count": "3", "Generation": "2", "Sha256": "hash"}},
		{"http_headers", map[string]string{"Job-Id": "job", "Organization-Id": "11", "Division-Id": "12", "Page-Count": "3", "Generation": "2", "Sha256": "hash"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := minio.ObjectInfo{UserMetadata: tc.values}
			for key, want := range map[string]string{"job_id": "job", "organization_id": "11", "division_id": "12", "page_count": "3", "generation": "2", "sha256": "hash"} {
				if got := metadata(info, key); got != want {
					t.Errorf("%s: got %q want %q", key, got, want)
				}
			}
		})
	}
}
