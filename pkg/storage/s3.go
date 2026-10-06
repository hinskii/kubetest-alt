/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3 is the backend for any S3-compatible store (AWS S3, MinIO, Ceph,
// R2, …) via minio-go. Zero value isn't usable — use NewS3.
type S3 struct {
	client *minio.Client
}

// NewS3 builds the S3 backend. Static keys when given; otherwise the AWS
// credential chain (AWS_* env, IRSA web identity, instance metadata).
// minio-go also recognises storage.googleapis.com, but the GCS backend is
// the supported way to use Google Cloud Storage.
func NewS3(cfg S3Config) (*S3, error) {
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "s3.amazonaws.com"
	}
	creds := credentials.NewChainCredentials([]credentials.Provider{
		&credentials.EnvAWS{},
		&credentials.IAM{},
	})
	if cfg.AccessKey != "" || cfg.SecretKey != "" {
		creds = credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, "")
	}
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  creds,
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("storage: new s3 client: %w", err)
	}
	return &S3{client: client}, nil
}

// Put implements Uploader.
func (m *S3) Put(ctx context.Context, bucket, key string, r io.Reader, size int64, contentType string) error {
	opts := minio.PutObjectOptions{ContentType: contentType}
	if _, err := m.client.PutObject(ctx, bucket, key, r, size, opts); err != nil {
		return fmt.Errorf("storage: put %s/%s: %w", bucket, key, err)
	}
	return nil
}

// Get implements Downloader. Translates minio-go's NoSuchKey → ErrNotFound
// so callers can errors.Is-check without importing minio-go.
func (m *S3) Get(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	obj, err := m.client.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("storage: get %s/%s: %w", bucket, key, err)
	}
	// minio-go's GetObject doesn't hit the network until first Read/Stat.
	// Probe with Stat() so ErrNotFound surfaces here, not on the first Read
	// (callers may not even attempt Read if they defer close first).
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("storage: stat %s/%s: %w", bucket, key, err)
	}
	return obj, nil
}

// List implements Lister. Streams ListObjects → sorted []string. Bounded by
// the caller: pass a specific prefix (never "").
func (m *S3) List(ctx context.Context, bucket, prefix string) ([]string, error) {
	if prefix == "" {
		return nil, errors.New("storage: List requires a non-empty prefix")
	}
	var out []string
	for obj := range m.client.ListObjects(ctx, bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	}) {
		if obj.Err != nil {
			return nil, fmt.Errorf("storage: list %s/%s: %w", bucket, prefix, obj.Err)
		}
		out = append(out, obj.Key)
	}
	slices.Sort(out)
	return out, nil
}

// PresignGetURL implements Presigner. Delegates to minio-go's
// PresignedGetObject; the returned URL is opaque and self-contained.
func (m *S3) PresignGetURL(ctx context.Context, bucket, key string, expiry time.Duration) (string, error) {
	if expiry <= 0 {
		return "", errors.New("storage: PresignGetURL requires positive expiry")
	}
	u, err := m.client.PresignedGetObject(ctx, bucket, key, expiry, url.Values{})
	if err != nil {
		return "", fmt.Errorf("storage: presign %s/%s: %w", bucket, key, err)
	}
	return u.String(), nil
}

// RemovePrefix implements Remover. Pipes ListObjects → RemoveObjects; both
// are streaming so a large prefix doesn't buffer entire object lists in
// memory. Missing prefix is a no-op (ListObjects yields zero results).
func (m *S3) RemovePrefix(ctx context.Context, bucket, prefix string) error {
	if prefix == "" {
		return errors.New("storage: RemovePrefix requires a non-empty prefix")
	}
	objectsCh := make(chan minio.ObjectInfo)
	var listErr error // written by the lister goroutine before it closes objectsCh
	go func() {
		defer close(objectsCh)
		for obj := range m.client.ListObjects(ctx, bucket, minio.ListObjectsOptions{
			Prefix:    prefix,
			Recursive: true,
		}) {
			if obj.Err != nil {
				// Stop and report: a failed listing used to be skipped
				// silently, so RemovePrefix "succeeded" with objects left.
				listErr = fmt.Errorf("storage: list %s/%s: %w", bucket, prefix, obj.Err)
				return
			}
			select {
			case objectsCh <- obj:
			case <-ctx.Done():
				return
			}
		}
	}()
	var firstErr error
	for rerr := range m.client.RemoveObjects(ctx, bucket, objectsCh, minio.RemoveObjectsOptions{}) {
		if rerr.Err != nil && firstErr == nil {
			firstErr = fmt.Errorf("storage: remove %s/%s: %w", bucket, rerr.ObjectName, rerr.Err)
		}
	}
	// RemoveObjects drains objectsCh until it is closed, so listErr is set
	// (if at all) before we get here.
	if firstErr == nil {
		firstErr = listErr
	}
	return firstErr
}
