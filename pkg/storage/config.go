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
	"fmt"
	"os"
	"strconv"
)

// Backend types.
const (
	// TypeS3 is any S3-compatible store: AWS S3, MinIO, Ceph RGW, R2, …
	TypeS3 = "s3"
	// TypeGCS is Google Cloud Storage via its native API, authenticated by
	// Application Default Credentials (Workload Identity on GKE).
	TypeGCS = "gcs"
)

// Backend is everything kubetest does with object storage.
type Backend interface {
	Uploader
	Downloader
	Lister
	Remover
	Presigner
}

// Config selects and configures the backend. The zero value (Type "")
// means "no object storage": no artifacts, no streamed logs, verdicts
// from pod state only.
type Config struct {
	Type   string
	Bucket string
	S3     S3Config
	GCS    GCSConfig
}

// S3Config configures the S3 backend.
type S3Config struct {
	// Endpoint is host[:port], no scheme. Empty → s3.amazonaws.com.
	Endpoint string
	UseSSL   bool
	// Region is required by AWS for some buckets; MinIO ignores it.
	Region string
	// AccessKey/SecretKey: static credentials. When empty the standard AWS
	// chain is used (AWS_* env, web identity / IRSA, instance metadata).
	AccessKey string
	SecretKey string
}

// GCSConfig configures the GCS backend. Credentials always come from
// Application Default Credentials (Workload Identity, or
// GOOGLE_APPLICATION_CREDENTIALS pointing at a key file).
type GCSConfig struct {
	// Endpoint overrides the API endpoint — only for emulators
	// (fake-gcs-server); disables authentication.
	Endpoint string
}

// Enabled reports whether object storage is configured.
func (c Config) Enabled() bool { return c.Type != "" }

// Validate checks the config is usable.
func (c Config) Validate() error {
	switch c.Type {
	case "":
		return nil
	case TypeS3, TypeGCS:
	default:
		return fmt.Errorf("storage: unknown type %q (want %q or %q)", c.Type, TypeS3, TypeGCS)
	}
	if c.Bucket == "" {
		return fmt.Errorf("storage: bucket is required for type %q", c.Type)
	}
	return nil
}

// New builds the configured backend.
func New(ctx context.Context, c Config) (Backend, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	switch c.Type {
	case TypeS3:
		return NewS3(c.S3)
	case TypeGCS:
		return NewGCS(ctx, c.GCS)
	}
	return nil, fmt.Errorf("storage: not configured")
}

// Environment the operator injects into the wrapper container (the wrapper
// reads it back with FromEnv). Credentials are never here: S3 keys arrive
// via an envFrom Secret as AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY, GCS
// uses the pod's service account.
const (
	EnvType        = "KUBETEST_STORAGE_TYPE"
	EnvBucket      = "KUBETEST_STORAGE_BUCKET"
	EnvS3Endpoint  = "KUBETEST_S3_ENDPOINT"
	EnvS3UseSSL    = "KUBETEST_S3_USE_SSL"
	EnvS3Region    = "KUBETEST_S3_REGION"
	EnvGCSEndpoint = "KUBETEST_GCS_ENDPOINT"
)

// Env renders c (minus credentials) as wrapper environment variables.
// Empty values are omitted.
func (c Config) Env() []EnvVar {
	if !c.Enabled() {
		return nil
	}
	out := []EnvVar{{EnvType, c.Type}, {EnvBucket, c.Bucket}}
	add := func(name, value string) {
		if value != "" {
			out = append(out, EnvVar{name, value})
		}
	}
	switch c.Type {
	case TypeS3:
		add(EnvS3Endpoint, c.S3.Endpoint)
		if c.S3.UseSSL {
			add(EnvS3UseSSL, "true")
		}
		add(EnvS3Region, c.S3.Region)
	case TypeGCS:
		add(EnvGCSEndpoint, c.GCS.Endpoint)
	}
	return out
}

// EnvVar is a name/value pair (kept free of k8s types so the wrapper
// binary doesn't depend on them).
type EnvVar struct{ Name, Value string }

// FromEnv is the wrapper-side inverse of Env. S3 static credentials come
// from the standard AWS variables when set.
func FromEnv() Config {
	useSSL, _ := strconv.ParseBool(os.Getenv(EnvS3UseSSL))
	return Config{
		Type:   os.Getenv(EnvType),
		Bucket: os.Getenv(EnvBucket),
		S3: S3Config{
			Endpoint:  os.Getenv(EnvS3Endpoint),
			UseSSL:    useSSL,
			Region:    os.Getenv(EnvS3Region),
			AccessKey: os.Getenv("AWS_ACCESS_KEY_ID"),
			SecretKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
		},
		GCS: GCSConfig{Endpoint: os.Getenv(EnvGCSEndpoint)},
	}
}
