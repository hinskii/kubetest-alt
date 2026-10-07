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
	"path"
	"strings"

	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// Screenshots and videos among a run's artifacts (step 18-2f): shown on
// the run page by content type or extension, whichever tool wrote them
// (Cypress cypress/screenshots + videos, Playwright test-results, …).

// typePNG is the most common screenshot type.
const typePNG = "image/png"

// maxMedia bounds the gallery; the full list stays under Artifacts.
const maxMedia = 24

// mediaTypes maps the extensions browsers show inline. Go's built-in MIME
// table lacks video types, and the type an artifact got recorded with
// depends on the tool image's /etc/mime.types.
var mediaTypes = map[string]string{
	".png": typePNG, ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".mp4": "video/mp4", ".webm": "video/webm",
}

// mediaType is the inline type of an artifact: its recorded type when
// that is an image or video, else by extension; "" for anything else.
func mediaType(p, recorded string) string {
	if strings.HasPrefix(recorded, "image/") && recorded != "image/svg+xml" || strings.HasPrefix(recorded, "video/") {
		return recorded
	}
	return mediaTypes[strings.ToLower(path.Ext(p))]
}

// mediaItem is one gallery entry.
type mediaItem struct {
	Path, Type string
	Video      bool
}

// mediaOf picks the run's screenshots and videos (SVG excluded: it is a
// document, not a picture, and is served sandboxed as an artifact).
func mediaOf(arts []apiclient.Artifact) (items []mediaItem, more int) {
	for _, a := range arts {
		t := mediaType(a.Path, a.ContentType)
		if t == "" {
			continue
		}
		if len(items) == maxMedia {
			more++
			continue
		}
		items = append(items, mediaItem{Path: a.Path, Type: t, Video: strings.HasPrefix(t, "video/")})
	}
	return items, more
}
