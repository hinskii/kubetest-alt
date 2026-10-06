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
	"flag"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func parseFlags(t *testing.T, args ...string) Config {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	f := BindFlags(fs)
	require.NoError(t, fs.Parse(args))
	return f.Config()
}

func TestFlags_DefaultsDisableStorage(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	c := parseFlags(t)
	assert.False(t, c.Enabled())
	assert.Equal(t, DefaultBucket, c.Bucket)
	_, err := New(t.Context(), c)
	require.ErrorContains(t, err, "not configured")
}

func TestFlags_S3(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIA")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	c := parseFlags(t, "--storage-type=s3", "--storage-bucket=runs",
		"--s3-endpoint=minio:9000", "--s3-use-ssl", "--s3-region=eu-west-1")
	assert.Equal(t, Config{
		Type: TypeS3, Bucket: "runs",
		S3: S3Config{Endpoint: "minio:9000", UseSSL: true, Region: "eu-west-1", AccessKey: "AKIA", SecretKey: "secret"},
	}, c)
	b, err := New(t.Context(), c)
	require.NoError(t, err)
	assert.IsType(t, &S3{}, b)
}

func TestFlags_GCS(t *testing.T) {
	c := parseFlags(t, "--storage-type=gcs", "--storage-bucket=runs", "--gcs-endpoint=fake-gcs:4443")
	assert.Equal(t, TypeGCS, c.Type)
	assert.Equal(t, GCSConfig{Endpoint: "fake-gcs:4443"}, c.GCS)
	// An emulator endpoint needs no credentials, so this builds offline.
	b, err := New(t.Context(), c)
	require.NoError(t, err)
	assert.IsType(t, &GCS{}, b)
}

func TestNew_RejectsInvalidConfig(t *testing.T) {
	_, err := New(t.Context(), parseFlags(t, "--storage-type=azure"))
	require.ErrorContains(t, err, `unknown type "azure"`)
	_, err = New(t.Context(), parseFlags(t, "--storage-type=s3", "--storage-bucket="))
	require.ErrorContains(t, err, "bucket is required")
}

// Guards both backends enforce before touching the network: an empty
// prefix would list (or remove) the whole bucket.
func TestBackends_GuardsBeforeNetwork(t *testing.T) {
	s3, err := NewS3(S3Config{Endpoint: "127.0.0.1:1"})
	require.NoError(t, err)
	gcsB, err := NewGCS(t.Context(), GCSConfig{Endpoint: "127.0.0.1:1"})
	require.NoError(t, err)

	for name, b := range map[string]Backend{"s3": s3, "gcs": gcsB} {
		t.Run(name, func(t *testing.T) {
			_, err := b.List(t.Context(), "runs", "")
			require.ErrorContains(t, err, "non-empty prefix")
			require.ErrorContains(t, b.RemovePrefix(t.Context(), "runs", ""), "non-empty prefix")
			_, err = b.PresignGetURL(t.Context(), "runs", "k", 0)
			require.ErrorContains(t, err, "positive expiry")
		})
	}
}
