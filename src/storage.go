package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// MinioClient wraps the MinIO SDK and manages custom song file lifecycle.
type MinioClient struct {
	client *minio.Client
	bucket string
}

// InitMinio connects to MinIO and ensures the target bucket exists.
func InitMinio(endpoint, accessKey, secretKey, bucket string, useSSL bool) (*MinioClient, error) {
	mc, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("initializing minio client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	exists, err := mc.BucketExists(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("checking minio bucket '%s': %w", bucket, err)
	}
	if !exists {
		log.Printf("MinIO bucket '%s' does not exist — creating…", bucket)
		if err := mc.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("creating minio bucket '%s': %w", bucket, err)
		}
	}

	log.Printf("MinIO connected. Bucket: %s", bucket)
	return &MinioClient{client: mc, bucket: bucket}, nil
}

// UploadPending stores an audio file under the pending/ prefix.
func (m *MinioClient) UploadPending(ctx context.Context, filename string, reader io.Reader, size int64) error {
	_, err := m.client.PutObject(ctx, m.bucket, "pending/"+filename, reader, size, minio.PutObjectOptions{
		ContentType: "audio/mpeg",
	})
	if err != nil {
		return fmt.Errorf("uploading pending object: %w", err)
	}
	return nil
}

// ApproveSong copies the file from pending/ to library/ and removes the original.
func (m *MinioClient) ApproveSong(ctx context.Context, filename string) error {
	src := minio.CopySrcOptions{Bucket: m.bucket, Object: "pending/" + filename}
	dst := minio.CopyDestOptions{Bucket: m.bucket, Object: "library/" + filename}

	if _, err := m.client.CopyObject(ctx, dst, src); err != nil {
		return fmt.Errorf("copying object in minio: %w", err)
	}

	if err := m.client.RemoveObject(ctx, m.bucket, "pending/"+filename, minio.RemoveObjectOptions{}); err != nil {
		log.Printf("Warning: failed to delete pending source '%s': %v", filename, err)
	}
	return nil
}

// RejectSong deletes a file from the pending/ prefix.
func (m *MinioClient) RejectSong(ctx context.Context, filename string) error {
	return m.client.RemoveObject(ctx, m.bucket, "pending/"+filename, minio.RemoveObjectOptions{})
}

// GetLibrarySong opens a read stream for an approved song in the library/ prefix.
func (m *MinioClient) GetLibrarySong(ctx context.Context, filename string) (io.ReadCloser, error) {
	obj, err := m.client.GetObject(ctx, m.bucket, "library/"+filename, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("fetching library object: %w", err)
	}
	return obj, nil
}

// LibrarySongExists checks whether an approved song is present in the library.
func (m *MinioClient) LibrarySongExists(ctx context.Context, filename string) (bool, error) {
	_, err := m.client.StatObject(ctx, m.bucket, "library/"+filename, minio.StatObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return false, nil
		}
		return false, fmt.Errorf("checking library object: %w", err)
	}
	return true, nil
}
