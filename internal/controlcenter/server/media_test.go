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

package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
	"github.com/hinskii/kubetest-alt/pkg/storage"
)

func TestMediaOf(t *testing.T) {
	items, more := mediaOf([]apiclient.Artifact{
		{Path: "results/junit.xml", ContentType: "application/xml"},
		{Path: "cypress/screenshots/checkout.cy.js/pays (failed).png", ContentType: "image/png"},
		{Path: "cypress/videos/checkout.cy.js.mp4", ContentType: "application/octet-stream"},
		{Path: "test-results/login/video.webm"},
		{Path: "report/logo.svg", ContentType: "image/svg+xml"},
	})
	assert.Zero(t, more)
	require.Len(t, items, 3, "no XML, no SVG")
	assert.Equal(t, mediaItem{Path: "cypress/screenshots/checkout.cy.js/pays (failed).png", Type: "image/png"}, items[0])
	assert.Equal(t, mediaItem{Path: "cypress/videos/checkout.cy.js.mp4", Type: "video/mp4", Video: true}, items[1])
	assert.True(t, items[2].Video)

	many := make([]apiclient.Artifact, 0, maxMedia+5)
	for i := range maxMedia + 5 {
		many = append(many, apiclient.Artifact{Path: fmt.Sprintf("s/%d.png", i)})
	}
	items, more = mediaOf(many)
	assert.Len(t, items, maxMedia)
	assert.Equal(t, 5, more)
}

func TestRunPage_Gallery(t *testing.T) {
	run := runOf("smoke-abcde", testsv1alpha1.PhaseFailed)
	run.Status.ArtifactRefs = []testsv1alpha1.ArtifactRef{
		{Path: "cypress/screenshots/pay (failed).png", ContentType: "image/png"},
		{Path: "cypress/videos/pay.mp4"},
	}
	w := newWorld(t, smokeTest(), run)
	body := w.get(t, "/clusters/dev/runs/team-a/smoke-abcde", "").Body.String()
	assert.Contains(t, body, "Screenshots &amp; videos")
	assert.Contains(t, body, `<img loading="lazy" alt="cypress/screenshots/pay (failed).png" src="/clusters/dev/runs/team-a/smoke-abcde/artifacts/cypress/screenshots/pay%20%28failed%29.png">`)
	assert.Contains(t, body, `<video controls preload="metadata" src="/clusters/dev/runs/team-a/smoke-abcde/artifacts/cypress/videos/pay.mp4">`)

	// A video stored without a usable type still plays inline.
	key := storage.ForRun("team-a", runUID).Artifact("cypress/videos/pay.mp4")
	require.NoError(t, w.objects.Put(context.Background(), bucket, key, strings.NewReader("mp4"), 3, "application/octet-stream"))
	rec := w.get(t, "/clusters/dev/runs/team-a/smoke-abcde/artifacts/cypress/videos/pay.mp4", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "video/mp4", rec.Header().Get("Content-Type"))
}
