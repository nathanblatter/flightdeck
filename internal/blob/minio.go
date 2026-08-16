package blob

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// MinIO is the production Store: any S3-compatible endpoint (the shared-stack
// MinIO in practice).
type MinIO struct {
	client *minio.Client
	bucket string
}

// NewFromEnv builds a MinIO store from FLIGHTDECK_S3_* env vars, creating the
// bucket if missing. Returns (nil, nil) when FLIGHTDECK_S3_ENDPOINT is unset —
// the instance simply runs without attachment storage.
func NewFromEnv(ctx context.Context) (*MinIO, error) {
	endpoint := os.Getenv("FLIGHTDECK_S3_ENDPOINT")
	if endpoint == "" {
		return nil, nil
	}
	access := os.Getenv("FLIGHTDECK_S3_ACCESS_KEY")
	secret := os.Getenv("FLIGHTDECK_S3_SECRET_KEY")
	bucket := os.Getenv("FLIGHTDECK_S3_BUCKET")
	if bucket == "" {
		bucket = "flightdeck-attachments"
	}
	useSSL := os.Getenv("FLIGHTDECK_S3_USE_SSL") == "true"

	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(access, secret, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("blob: minio client: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	exists, err := client.BucketExists(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("blob: bucket check %q: %w", bucket, err)
	}
	if !exists {
		if err := client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("blob: create bucket %q: %w", bucket, err)
		}
	}
	return &MinIO{client: client, bucket: bucket}, nil
}

func (m *MinIO) Bucket() string { return m.bucket }

func (m *MinIO) Put(ctx context.Context, key, contentType string, size int64, r io.Reader) error {
	_, err := m.client.PutObject(ctx, m.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	return err
}

func (m *MinIO) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := m.client.GetObject(ctx, m.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	// GetObject is lazy; surface a missing object as an error now so the API
	// can 404 instead of failing mid-stream.
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		return nil, err
	}
	return obj, nil
}

func (m *MinIO) Delete(ctx context.Context, key string) error {
	return m.client.RemoveObject(ctx, m.bucket, key, minio.RemoveObjectOptions{})
}
