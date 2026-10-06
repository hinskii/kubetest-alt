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
	"net/http"
	"slices"
	"strings"
	"time"

	gcs "cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// GCS is the Google Cloud Storage backend (native JSON API). Credentials
// are Application Default Credentials: on GKE, Workload Identity for the
// pod's Kubernetes service account. Zero value isn't usable — use NewGCS.
type GCS struct {
	client *gcs.Client
}

// NewGCS builds the GCS backend. cfg.Endpoint is only for emulators
// (fake-gcs-server): it switches off authentication.
func NewGCS(ctx context.Context, cfg GCSConfig) (*GCS, error) {
	var opts []option.ClientOption
	if cfg.Endpoint != "" {
		endpoint := strings.TrimRight(cfg.Endpoint, "/")
		if !strings.Contains(endpoint, "://") {
			endpoint = "http://" + endpoint
		}
		opts = append(opts,
			option.WithEndpoint(endpoint+"/storage/v1/"),
			option.WithoutAuthentication(),
			// Reads default to the XML API, where emulators take the Host
			// header for a virtual-hosted bucket name and 404. JSON reads
			// address the bucket in the path.
			gcs.WithJSONReads(),
		)
	}
	client, err := gcs.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("storage: new gcs client: %w", err)
	}
	return &GCS{client: client}, nil
}

// Put implements Uploader.
func (g *GCS) Put(ctx context.Context, bucket, key string, r io.Reader, _ int64, contentType string) error {
	w := g.client.Bucket(bucket).Object(key).NewWriter(ctx)
	w.ContentType = contentType
	if _, err := io.Copy(w, r); err != nil {
		_ = w.Close()
		return fmt.Errorf("storage: put %s/%s: %w", bucket, key, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("storage: put %s/%s: %w", bucket, key, err)
	}
	return nil
}

// Get implements Downloader.
func (g *GCS) Get(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	rc, err := g.client.Bucket(bucket).Object(key).NewReader(ctx)
	if err != nil {
		if errors.Is(err, gcs.ErrObjectNotExist) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("storage: get %s/%s: %w", bucket, key, err)
	}
	return rc, nil
}

// List implements Lister. Keys come back sorted (GCS lists in
// lexicographic order already; sorting keeps the contract explicit).
func (g *GCS) List(ctx context.Context, bucket, prefix string) ([]string, error) {
	if prefix == "" {
		return nil, errors.New("storage: List requires a non-empty prefix")
	}
	var out []string
	it := g.client.Bucket(bucket).Objects(ctx, &gcs.Query{Prefix: prefix})
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("storage: list %s/%s: %w", bucket, prefix, err)
		}
		out = append(out, attrs.Name)
	}
	slices.Sort(out)
	return out, nil
}

// RemovePrefix implements Remover. GCS has no bulk delete; objects are
// deleted one by one, continuing past failures so a retry converges.
func (g *GCS) RemovePrefix(ctx context.Context, bucket, prefix string) error {
	keys, err := g.List(ctx, bucket, prefix)
	if err != nil {
		return err
	}
	var firstErr error
	for _, k := range keys {
		err := g.client.Bucket(bucket).Object(k).Delete(ctx)
		if err != nil && !errors.Is(err, gcs.ErrObjectNotExist) && firstErr == nil {
			firstErr = fmt.Errorf("storage: remove %s/%s: %w", bucket, k, err)
		}
	}
	return firstErr
}

// PresignGetURL implements Presigner (V4 signed URL). Signing needs a
// signer: a service-account key, or — with Workload Identity — the IAM
// signBlob API (the GSA needs roles/iam.serviceAccountTokenCreator on
// itself). Only used for ?presign=1; the API server streams by default.
func (g *GCS) PresignGetURL(_ context.Context, bucket, key string, expiry time.Duration) (string, error) {
	if expiry <= 0 {
		return "", errors.New("storage: PresignGetURL requires positive expiry")
	}
	u, err := g.client.Bucket(bucket).SignedURL(key, &gcs.SignedURLOptions{
		Method:  http.MethodGet,
		Expires: time.Now().Add(expiry),
		Scheme:  gcs.SigningSchemeV4,
	})
	if err != nil {
		return "", fmt.Errorf("storage: presign %s/%s: %w", bucket, key, err)
	}
	return u, nil
}
