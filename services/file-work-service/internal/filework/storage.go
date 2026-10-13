package filework

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Storage interface {
	Download(context.Context, Command, string) error
	Upload(context.Context, Command, Artifact) error
}
type S3Storage struct {
	Client *minio.Client
	Bucket string
}

func NewS3Storage(c Config) (*S3Storage, error) {
	u, err := url.Parse(c.S3Endpoint)
	if err != nil {
		return nil, err
	}
	client, err := minio.New(u.Host, &minio.Options{Creds: credentials.NewStaticV4(c.S3AccessKey, c.S3SecretKey, ""), Secure: u.Scheme == "https", Region: c.S3Region})
	if err != nil {
		return nil, err
	}
	return &S3Storage{Client: client, Bucket: c.S3Bucket}, nil
}

func (s *S3Storage) Download(ctx context.Context, c Command, target string) error {
	options := minio.GetObjectOptions{}
	if err := options.SetMatchETag(strings.Trim(c.SourceETag, "\"")); err != nil {
		return reject("source_changed")
	}
	object, err := s.Client.GetObject(ctx, s.Bucket, c.SourceKey, options)
	if err != nil {
		return classifyS3(err)
	}
	defer object.Close()
	info, err := object.Stat()
	if err != nil {
		return classifyS3(err)
	}
	if info.Size > c.Limits.MaxSourceBytes {
		return reject("source_too_large")
	}
	if info.Size < 5 {
		return reject("invalid_pdf_signature")
	}
	if strings.Trim(info.ETag, "\"") != strings.Trim(c.SourceETag, "\"") {
		return reject("source_changed")
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	n, err := io.Copy(file, io.LimitReader(object, c.Limits.MaxSourceBytes+1))
	closeErr := file.Close()
	if err != nil {
		return classifyS3(err)
	}
	if closeErr != nil {
		return closeErr
	}
	if n > c.Limits.MaxSourceBytes {
		return reject("source_too_large")
	}
	if n != info.Size {
		return errors.New("source_incomplete")
	}
	return nil
}
func (s *S3Storage) Upload(ctx context.Context, c Command, a Artifact) error {
	_, err := s.Client.FPutObject(ctx, s.Bucket, c.OutputKey, a.Path, minio.PutObjectOptions{ContentType: "application/pdf", UserMetadata: artifactMetadata(c, a), DisableMultipart: true})
	return classifyS3(err)
}
func classifyS3(err error) error {
	if err == nil {
		return nil
	}
	r := minio.ToErrorResponse(err)
	switch r.Code {
	case "NoSuchKey", "NoSuchObject":
		return reject("source_not_found")
	case "PreconditionFailed":
		return reject("source_changed")
	}
	return errors.New("storage_unavailable")
}
