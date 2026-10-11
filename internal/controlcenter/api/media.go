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

package api

import (
	"path"
	"strings"

	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// Screenshots and videos among a run's artifacts (step 18-2f): shown on
// the run page by content type or extension, whichever tool wrote them
// (Cypress cypress/screenshots + videos, Playwright test-results, …).

// Common screenshot and video types.
const (
	typePNG = "image/png"
	typeMP4 = "video/mp4"
)

// MaxMedia bounds the gallery; the full list stays under Artifacts.
const MaxMedia = 24

// MediaTypes maps the extensions browsers show inline. Go's built-in MIME
// table lacks video types, and the type an artifact got recorded with
// depends on the tool image's /etc/mime.types.
var MediaTypes = map[string]string{
	".png": typePNG, ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".mp4": typeMP4, ".webm": "video/webm",
}

// mediaType is the inline type of an artifact: its recorded type when
// that is an image or video, else by extension; "" for anything else.
func mediaType(p, recorded string) string {
	if strings.HasPrefix(recorded, "image/") && recorded != "image/svg+xml" || strings.HasPrefix(recorded, "video/") {
		return recorded
	}
	return MediaTypes[strings.ToLower(path.Ext(p))]
}

// Media is one gallery entry: a screenshot or a video.
type Media struct {
	Path  string `json:"path" required:"true"`
	Type  string `json:"type" required:"true"`
	Video bool   `json:"video" required:"true"`
}

// mediaOf picks the run's screenshots and videos (SVG excluded: it is a
// document, not a picture, and is served sandboxed as an artifact).
func mediaOf(arts []apiclient.Artifact) (items []Media, more int) {
	items = []Media{}
	for _, a := range arts {
		t := mediaType(a.Path, a.ContentType)
		if t == "" {
			continue
		}
		if len(items) == MaxMedia {
			more++
			continue
		}
		items = append(items, Media{Path: a.Path, Type: t, Video: strings.HasPrefix(t, "video/")})
	}
	return items, more
}
