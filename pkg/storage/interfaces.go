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

// Package storage abstracts the object store the wrapper writes to and the
// operator reads from. Small interfaces so tests use in-memory fakes and
// production wires a MinIO/S3 client (minio-go).
//
// Layout convention (step 07): bucket = kubetest-artifacts (configurable),
// keys = "<runID>/<relpath>" for scraped files, "<runID>/result.json" for
// the wrapper's terminal ExecutionResult.
package storage

import (
	"context"
	"errors"
	"io"
)

// Uploader writes objects. The wrapper's scraper uses this.
//
// Contract:
//   - Put MUST be safely retriable — implementations should either succeed
//     idempotently or return a distinct error caller can react to.
//   - contentType may be empty; callers that care set it (application/xml,
//     application/json etc.) so downstream tooling doesn't have to sniff.
type Uploader interface {
	// Put writes r to bucket/key. size is the exact number of bytes to read
	// from r (many object stores need this up front; -1 means "unknown, read
	// to EOF" but callers should provide it when they can).
	Put(ctx context.Context, bucket, key string, r io.Reader, size int64, contentType string) error
}

// Downloader reads objects. The controller's ResultReader uses this.
//
// Contract:
//   - Get returns ErrNotFound (below) when the key is absent — callers
//     distinguish "wrapper crashed before writing" from "network flake".
//   - The returned io.ReadCloser MUST be closed by the caller.
type Downloader interface {
	Get(ctx context.Context, bucket, key string) (io.ReadCloser, error)
}

// ErrNotFound is the sentinel Downloader implementations return when the
// requested key doesn't exist. errors.Is-friendly.
var ErrNotFound = errors.New("storage: object not found")

// Config carries the parameters both real MinIO and fake implementations
// accept. Kept small — auth details are per-implementation.
type Config struct {
	// Endpoint is host:port for MinIO / S3 API (no scheme).
	Endpoint string

	// Bucket is where objects land. Layout §12: "kubetest-artifacts".
	Bucket string

	// UseSSL toggles https:// for MinIO/S3 client. Default false for in-cluster
	// MinIO (cheaper); operators terminate TLS at ingress if needed.
	UseSSL bool

	// AccessKey / SecretKey are the S3-standard credentials. In production
	// these come from the wrapper container's envFrom secret (the compiler
	// injects the secret ref); locally tests set them directly.
	AccessKey string
	SecretKey string
}
