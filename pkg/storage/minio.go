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

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// MinIO wraps a minio-go client as both Uploader and Downloader. Zero value
// isn't usable — use NewMinIO.
type MinIO struct {
	client *minio.Client
}

// NewMinIO builds a MinIO storage client from Config. Kept trivial so tests
// covering error branches can skip this constructor entirely (tests use Fake
// instead — minio-go's own network paths aren't retested here).
func NewMinIO(cfg Config) (*MinIO, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("storage: MinIO endpoint is required")
	}
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("storage: new minio client: %w", err)
	}
	return &MinIO{client: client}, nil
}

// Put implements Uploader.
func (m *MinIO) Put(ctx context.Context, bucket, key string, r io.Reader, size int64, contentType string) error {
	opts := minio.PutObjectOptions{ContentType: contentType}
	if _, err := m.client.PutObject(ctx, bucket, key, r, size, opts); err != nil {
		return fmt.Errorf("storage: put %s/%s: %w", bucket, key, err)
	}
	return nil
}

// Get implements Downloader. Translates minio-go's NoSuchKey → ErrNotFound
// so callers can errors.Is-check without importing minio-go.
func (m *MinIO) Get(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
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
