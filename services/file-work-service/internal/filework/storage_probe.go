package filework

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"time"

	"github.com/minio/minio-go/v7"
)

const (
	probeTimeout        = 5 * time.Second
	probeCleanupTimeout = 3 * time.Second
)

type probeObjects interface {
	putProbe(context.Context, string, []byte) (string, error)
	readProbe(context.Context, string, string, int64) ([]byte, error)
	deleteProbe(context.Context, string, string) error
}

func (s *S3Storage) Probe(ctx context.Context, instanceID string) error {
	return probeCapability(ctx, s, instanceID, probeTimeout, probeCleanupTimeout)
}

// Only this synthetic, per-instance key can be touched by capability checks.
// Unique bytes prevent a previous probe from masking an ambiguous failed write.
func probeCapability(ctx context.Context, objects probeObjects, instanceID string, timeout, cleanupTimeout time.Duration) error {
	if !uuidPattern.MatchString(instanceID) || objects == nil || ctx.Err() != nil {
		return errors.New("storage_probe_failed")
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return errors.New("storage_probe_failed")
	}
	marker := []byte("file-work-service capability probe v1\n" + hex.EncodeToString(nonce[:]))
	key := "file-work-health/" + instanceID + "/probe"
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	versionID, operationErr := objects.putProbe(operationCtx, key, marker)
	if operationErr == nil {
		body, readErr := objects.readProbe(operationCtx, key, versionID, int64(len(marker)+1))
		if readErr != nil || !bytes.Equal(body, marker) {
			operationErr = errors.New("storage_probe_failed")
		}
	}
	operationExpired := operationCtx.Err() != nil
	// A write can have succeeded remotely even if the response was lost. Cleanup
	// therefore also runs on Put/Get failure, outside their expired context.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cleanupCancel()
	cleanupErr := objects.deleteProbe(cleanupCtx, key, versionID)
	if operationErr != nil || operationExpired || cleanupErr != nil || cleanupCtx.Err() != nil {
		return errors.New("storage_probe_failed")
	}
	return nil
}

func (s *S3Storage) putProbe(ctx context.Context, key string, marker []byte) (string, error) {
	info, err := s.Client.PutObject(ctx, s.Bucket, key, bytes.NewReader(marker), int64(len(marker)), minio.PutObjectOptions{ContentType: "application/octet-stream", DisableMultipart: true})
	return info.VersionID, err
}

func (s *S3Storage) readProbe(ctx context.Context, key, versionID string, limit int64) ([]byte, error) {
	object, err := s.Client.GetObject(ctx, s.Bucket, key, minio.GetObjectOptions{VersionID: versionID})
	if err != nil {
		return nil, err
	}
	body, readErr := io.ReadAll(io.LimitReader(object, limit))
	closeErr := object.Close()
	if readErr != nil {
		return nil, readErr
	}
	return body, closeErr
}

func (s *S3Storage) deleteProbe(ctx context.Context, key, versionID string) error {
	return s.Client.RemoveObject(ctx, s.Bucket, key, minio.RemoveObjectOptions{VersionID: versionID})
}
