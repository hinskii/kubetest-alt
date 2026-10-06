//go:build integration

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

// One conformance suite, run against a real MinIO (S3 backend) and
// fake-gcs-server (GCS backend) in containers, so both backends are held to
// the same Backend contract. Run with: make test-integration.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	testcontainers "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const conformanceBucket = "kubetest"

// Same digest as test/e2e/run.sh.
const minioImage = "cgr.dev/chainguard/minio@sha256:a05a4497e8dce3cb7a7a1bf1872ba5d30ea988f1e8c22c9e0920503761c4b5f1"

const fakeGCSImage = "fsouza/fake-gcs-server:1.56.1"

func TestBackend_S3_MinIO(t *testing.T) {
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        minioImage,
			Cmd:          []string{"server", "/data"},
			Env:          map[string]string{"MINIO_ROOT_USER": "minioadmin", "MINIO_ROOT_PASSWORD": "minioadmin"},
			ExposedPorts: []string{"9000/tcp"},
			WaitingFor:   wait.ForHTTP("/minio/health/ready").WithPort("9000/tcp").WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	endpoint, err := c.PortEndpoint(ctx, "9000/tcp", "")
	require.NoError(t, err)

	b, err := New(ctx, Config{Type: TypeS3, Bucket: conformanceBucket, S3: S3Config{
		Endpoint: endpoint, AccessKey: "minioadmin", SecretKey: "minioadmin",
	}})
	require.NoError(t, err)
	require.NoError(t, b.(*S3).client.MakeBucket(ctx, conformanceBucket, minio.MakeBucketOptions{}))

	runConformance(t, b, true)
}

func TestBackend_GCS_FakeServer(t *testing.T) {
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        fakeGCSImage,
			Cmd:          []string{"-scheme", "http", "-port", "4443", "-backend", "memory"},
			ExposedPorts: []string{"4443/tcp"},
			WaitingFor:   wait.ForHTTP("/storage/v1/b").WithPort("4443/tcp").WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	endpoint, err := c.PortEndpoint(ctx, "4443/tcp", "http")
	require.NoError(t, err)
	createGCSBucket(t, endpoint, conformanceBucket)

	b, err := New(ctx, Config{Type: TypeGCS, Bucket: conformanceBucket, GCS: GCSConfig{Endpoint: endpoint}})
	require.NoError(t, err)
	// Presigning needs real signing credentials; the emulator has none.
	runConformance(t, b, false)
}

func createGCSBucket(t *testing.T, endpoint, name string) {
	t.Helper()
	resp, err := http.Post(endpoint+"/storage/v1/b?project=test", "application/json",
		strings.NewReader(fmt.Sprintf(`{"name":%q}`, name)))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Less(t, resp.StatusCode, 300, "create bucket: %s", resp.Status)
}

func runConformance(t *testing.T, b Backend, presign bool) {
	ctx := context.Background()
	keys := ForRun("ns", "uid-1")
	other := ForRun("ns", "uid-10") // prefix-lookalike of uid-1's subtree name

	put := func(key, body string) {
		t.Helper()
		require.NoError(t, b.Put(ctx, conformanceBucket, key, strings.NewReader(body), int64(len(body)), "text/plain"))
	}
	put(keys.Result(), `{"phase":"passed"}`)
	put(keys.LogChunk(10), "c10")
	put(keys.LogChunk(2), "c2")
	put(keys.Artifact("reports/junit.xml"), "<testsuite/>")
	put(other.LogChunk(0), "other")
	// A large-ish object exercises the multipart / resumable paths.
	big := bytes.Repeat([]byte("x"), 6<<20)
	require.NoError(t, b.Put(ctx, conformanceBucket, keys.Artifact("big.bin"), bytes.NewReader(big), int64(len(big)), ""))

	t.Run("get", func(t *testing.T) {
		rc, err := b.Get(ctx, conformanceBucket, keys.Result())
		require.NoError(t, err)
		got, _ := io.ReadAll(rc)
		_ = rc.Close()
		assert.Equal(t, `{"phase":"passed"}`, string(got))

		rc, err = b.Get(ctx, conformanceBucket, keys.Artifact("big.bin"))
		require.NoError(t, err)
		n, _ := io.Copy(io.Discard, rc)
		_ = rc.Close()
		assert.Equal(t, int64(len(big)), n)
	})
	t.Run("missing object is ErrNotFound", func(t *testing.T) {
		_, err := b.Get(ctx, conformanceBucket, keys.Prefix()+"nope")
		assert.ErrorIs(t, err, ErrNotFound)
	})
	t.Run("list is sorted and prefix-isolated", func(t *testing.T) {
		got, err := b.List(ctx, conformanceBucket, keys.Logs())
		require.NoError(t, err)
		assert.Equal(t, []string{keys.LogChunk(2), keys.LogChunk(10)}, got)
		_, err = b.List(ctx, conformanceBucket, "")
		assert.Error(t, err, "bucket-wide list refused")
	})
	if presign {
		t.Run("presign", func(t *testing.T) {
			u, err := b.PresignGetURL(ctx, conformanceBucket, keys.Result(), time.Minute)
			require.NoError(t, err)
			resp, err := http.Get(u) // #nosec G107 -- test-only presigned URL
			require.NoError(t, err)
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, `{"phase":"passed"}`, string(body))
		})
	}
	t.Run("remove prefix", func(t *testing.T) {
		require.Error(t, b.RemovePrefix(ctx, conformanceBucket, ""), "bucket-wide delete refused")
		require.NoError(t, b.RemovePrefix(ctx, conformanceBucket, keys.Prefix()))
		left, err := b.List(ctx, conformanceBucket, keys.Prefix())
		require.NoError(t, err)
		assert.Empty(t, left)
		kept, err := b.List(ctx, conformanceBucket, other.Prefix())
		require.NoError(t, err)
		assert.Equal(t, []string{other.LogChunk(0)}, kept, "lookalike prefix untouched")
		assert.NoError(t, b.RemovePrefix(ctx, conformanceBucket, keys.Prefix()), "idempotent")
	})
}
