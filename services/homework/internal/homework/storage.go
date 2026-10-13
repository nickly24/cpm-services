package homework

import (
	"context"
	"fmt"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"net/url"
	"strings"
	"time"
)

type Storage struct {
	Client, Public *minio.Client
	Bucket         string
}

func NewStorage(c Config) (*Storage, error) {
	makeClient := func(base string) (*minio.Client, error) {
		u, e := url.Parse(base)
		if e != nil {
			return nil, e
		}
		return minio.New(u.Host, &minio.Options{Creds: credentials.NewStaticV4(c.S3Access, c.S3Secret, ""), Secure: u.Scheme == "https", Region: c.S3Region, BucketLookup: minio.BucketLookupPath})
	}
	client, e := makeClient(c.S3Endpoint)
	if e != nil {
		return nil, e
	}
	public, e := makeClient(c.S3PublicEndpoint)
	return &Storage{client, public, c.S3Bucket}, e
}
func (s *Storage) Upload(ctx context.Context, key string, account int64, limits Settings) (any, error) {
	p := minio.NewPostPolicy()
	for _, e := range []error{p.SetBucket(s.Bucket), p.SetKey(key), p.SetExpires(time.Now().UTC().Add(time.Duration(limits.PresignTTLSeconds) * time.Second)), p.SetContentType("application/pdf"), p.SetContentLengthRange(1, limits.MaxSourceBytes), p.SetUserMetadata("account-id", fmt.Sprint(account))} {
		if e != nil {
			return nil, e
		}
	}
	u, fields, e := s.Public.PresignedPostPolicy(ctx, p)
	if e != nil {
		return nil, &APIError{503, "storage_unavailable"}
	}
	return map[string]any{"method": "POST", "url": u.String(), "fields": fields}, nil
}
func (s *Storage) Head(ctx context.Context, key string) (minio.ObjectInfo, error) {
	return s.Client.StatObject(ctx, s.Bucket, key, minio.StatObjectOptions{})
}
func sourceHeadError(e error, message string) error {
	r := minio.ToErrorResponse(e)
	if r.Code == "NoSuchKey" || r.Code == "NoSuchObject" {
		return conflict(message)
	}
	return &APIError{503, "storage_unavailable"}
}
func (s *Storage) Download(ctx context.Context, key, name string, ttl int, download bool) (string, error) {
	q := url.Values{}
	mode := "inline"
	if download {
		mode = "attachment"
	}
	q.Set("response-content-type", "application/pdf")
	q.Set("response-content-disposition", mode+"; filename*=UTF-8''"+url.PathEscape(name))
	u, e := s.Public.PresignedGetObject(ctx, s.Bucket, key, time.Duration(ttl)*time.Second, q)
	if e != nil {
		return "", &APIError{503, "storage_unavailable"}
	}
	return u.String(), nil
}
func metadata(info minio.ObjectInfo, key string) string {
	for k, v := range info.UserMetadata {
		if strings.EqualFold(strings.ReplaceAll(k, "_", "-"), strings.ReplaceAll(key, "_", "-")) {
			return v
		}
	}
	for k, v := range info.Metadata {
		if strings.EqualFold(k, "x-amz-meta-"+key) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}
