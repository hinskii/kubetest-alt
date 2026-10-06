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
	"os"
)

// Flags are the object-storage command-line flags shared by the operator
// and the API server — registered in one place so the two binaries can't
// drift apart (they once read logs from different buckets).
type Flags struct {
	typ, bucket          string
	s3Endpoint, s3Region string
	s3UseSSL             bool
	gcsEndpoint          string
}

// BindFlags registers the storage flags on fs.
func BindFlags(fs *flag.FlagSet) *Flags {
	f := &Flags{}
	fs.StringVar(&f.typ, "storage-type", "",
		`Object storage backend: "s3" (any S3-compatible store: AWS S3, MinIO, …) or "gcs". `+
			"Empty disables artifacts, stored logs and result.json (verdicts from pod state only).")
	fs.StringVar(&f.bucket, "storage-bucket", DefaultBucket,
		"Bucket holding logs, artifacts and results (runs/<namespace>/<runUID>/…).")
	fs.StringVar(&f.s3Endpoint, "s3-endpoint", "",
		"S3 endpoint host[:port], no scheme. Empty = AWS (s3.amazonaws.com).")
	fs.BoolVar(&f.s3UseSSL, "s3-use-ssl", false, "Use https for the S3 endpoint.")
	fs.StringVar(&f.s3Region, "s3-region", "", "S3 region (AWS); ignored by MinIO.")
	fs.StringVar(&f.gcsEndpoint, "gcs-endpoint", "",
		"GCS API endpoint override — emulators only (disables authentication).")
	return f
}

// Config returns the parsed configuration. This process's own S3
// credentials come from AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY when set,
// else the AWS chain; GCS uses Application Default Credentials.
func (f *Flags) Config() Config {
	return Config{
		Type:   f.typ,
		Bucket: f.bucket,
		S3: S3Config{
			Endpoint:  f.s3Endpoint,
			UseSSL:    f.s3UseSSL,
			Region:    f.s3Region,
			AccessKey: os.Getenv("AWS_ACCESS_KEY_ID"),
			SecretKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
		},
		GCS: GCSConfig{Endpoint: f.gcsEndpoint},
	}
}
