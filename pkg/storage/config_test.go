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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_Validate(t *testing.T) {
	assert.NoError(t, Config{}.Validate(), "disabled is valid")
	assert.NoError(t, Config{Type: TypeS3, Bucket: "b"}.Validate())
	assert.NoError(t, Config{Type: TypeGCS, Bucket: "b"}.Validate())
	assert.ErrorContains(t, Config{Type: "minio", Bucket: "b"}.Validate(), "unknown type")
	assert.ErrorContains(t, Config{Type: TypeS3}.Validate(), "bucket is required")
	_, err := New(context.Background(), Config{})
	assert.Error(t, err, "New on a disabled config is an error, not a nil backend")
}

// The operator renders Env into the wrapper pod; the wrapper reads it back
// with FromEnv. Credentials never travel this way.
func TestConfig_EnvRoundTrip(t *testing.T) {
	for _, c := range []Config{
		{Type: TypeS3, Bucket: "b", S3: S3Config{Endpoint: "minio:9000", UseSSL: true, Region: "eu-west-1"}},
		{Type: TypeS3, Bucket: "b"},
		{Type: TypeGCS, Bucket: "g", GCS: GCSConfig{Endpoint: "fake-gcs:4443"}},
		{Type: TypeGCS, Bucket: "g"},
	} {
		t.Run(c.Type+"/"+c.S3.Endpoint+c.GCS.Endpoint, func(t *testing.T) {
			for _, e := range c.Env() {
				t.Setenv(e.Name, e.Value)
			}
			assert.Equal(t, c, FromEnv())
		})
	}
	assert.Nil(t, Config{}.Env())
}

func TestConfig_EnvCarriesNoSecrets(t *testing.T) {
	c := Config{Type: TypeS3, Bucket: "b", S3: S3Config{AccessKey: "AKIA", SecretKey: "s3cr3t"}}
	for _, e := range c.Env() {
		assert.NotContains(t, e.Value, "AKIA")
		assert.NotContains(t, e.Value, "s3cr3t")
	}
}

func TestFromEnv_StaticS3CredsFromAWSVars(t *testing.T) {
	t.Setenv(EnvType, TypeS3)
	t.Setenv(EnvBucket, "b")
	t.Setenv("AWS_ACCESS_KEY_ID", "id")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "key")
	c := FromEnv()
	assert.Equal(t, "id", c.S3.AccessKey)
	assert.Equal(t, "key", c.S3.SecretKey)
}

func TestNewS3_DefaultsToAWSEndpoint(t *testing.T) {
	b, err := NewS3(S3Config{})
	require.NoError(t, err)
	assert.Equal(t, "s3.amazonaws.com", b.client.EndpointURL().Host)
}
