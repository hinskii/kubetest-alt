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
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// Live view (step 18g): a tool's own web UI during a run (spec.liveView —
// k6's dashboard). Its HTML and scripts come from the test image, which the
// Test's author picks, so they must never run as Control Center:
//
//   - pages under /live/ carry "Content-Security-Policy: sandbox
//     allow-scripts" (no allow-same-origin): the UI runs in an opaque
//     origin, without Control Center's cookies, storage or pages;
//   - such a page's own requests (assets, its event stream) are cross-site
//     and carry no session, so /live/ is reached with a capability instead:
//     a signed, expiring token for one run, minted when someone allowed to
//     see the run opens its page. oauth2-proxy lets /live/ through
//     (--skip-auth-route=GET=^/live/); the token is the authorization;
//   - responses allow any origin (CORS *: the opaque origin is "null"),
//     never set cookies and send no Referer (the token is in the URL).
//
// Control Center keeps no state: the token says which run it is for.

// liveTokenTTL bounds a live-view link — longer than any sane load test.
const liveTokenTTL = 12 * time.Hour

// liveCSP sandboxes the tool's UI and lets only Control Center frame it.
const liveCSP = "sandbox allow-scripts; frame-ancestors 'self'"

var errBadLiveToken = errors.New("invalid or expired live-view link")

// liveClaims is what a token grants: one run's live view, until Exp.
type liveClaims struct {
	Cluster   string `json:"c"`
	Namespace string `json:"n"`
	Run       string `json:"r"`
	Exp       int64  `json:"e"`
}

// liveSigner mints and checks live-view tokens (HMAC-SHA256).
type liveSigner struct{ key []byte }

// newLiveSigner uses key, or a random one when key is empty — links then
// stop working when Control Center restarts, and every replica needs the
// same key (CC_LIVE_VIEW_KEY).
func newLiveSigner(key []byte) (*liveSigner, error) {
	if len(key) == 0 {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
	}
	return &liveSigner{key: key}, nil
}

func (s *liveSigner) mac(payload string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (s *liveSigner) sign(c liveClaims) string {
	b, _ := json.Marshal(c)
	payload := base64.RawURLEncoding.EncodeToString(b)
	return payload + "." + s.mac(payload)
}

func (s *liveSigner) verify(token string, now time.Time) (liveClaims, error) {
	payload, sig, ok := strings.Cut(token, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(s.mac(payload))) {
		return liveClaims{}, errBadLiveToken
	}
	b, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return liveClaims{}, errBadLiveToken
	}
	var c liveClaims
	if json.Unmarshal(b, &c) != nil || now.Unix() > c.Exp {
		return liveClaims{}, errBadLiveToken
	}
	return c, nil
}

// liveURL is the live view's entry URL for a run, valid for liveTokenTTL.
func (s *Server) liveURL(cluster, ns, run, entry string, now time.Time) string {
	tok := s.live.sign(liveClaims{Cluster: cluster, Namespace: ns, Run: run, Exp: now.Add(liveTokenTTL).Unix()})
	return "/live/" + tok + "/" + strings.TrimPrefix(entry, "/")
}

func liveHeaders(h http.Header) {
	h.Set("Content-Security-Policy", liveCSP)
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Del("Set-Cookie")
}

// liveHeadersPassed are the upstream headers a live view keeps.
var liveHeadersPassed = []string{"Content-Type", "Cache-Control", "Last-Modified", "ETag"}

// liveView serves GET /live/{token}/{path...}: the token's run's live UI,
// streamed from the kubetest API server.
func (s *Server) liveView(w http.ResponseWriter, r *http.Request) {
	liveHeaders(w.Header())
	claims, err := s.live.verify(r.PathValue("token"), time.Now())
	if err != nil {
		livePage(w, http.StatusForbidden, "This live-view link is invalid or has expired. Open the run in Control Center again.", false)
		return
	}
	c := s.Clusters.Get(claims.Cluster)
	if c == nil {
		livePage(w, http.StatusNotFound, "The cluster of this run is no longer configured.", false)
		return
	}
	resp, err := c.API.OpenLiveView(r.Context(), claims.Namespace, claims.Run, r.PathValue("path"), r.URL.RawQuery)
	if err != nil {
		var apiErr *apiclient.APIError
		switch {
		case errors.As(err, &apiErr) && apiErr.Status == http.StatusServiceUnavailable:
			livePage(w, http.StatusOK, "Waiting for the run to start…", true)
		case errors.As(err, &apiErr) && apiErr.Status == http.StatusGone:
			livePage(w, http.StatusOK, "The run has finished, so its live view is gone. Its report and artifacts are on the run page.", false)
		default:
			livePage(w, http.StatusBadGateway, "Live view unavailable: "+messageOf(err), false)
		}
		return
	}
	defer func() { _ = resp.Body.Close() }()
	for _, k := range liveHeadersPassed {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	copyFlushing(w, resp.Body)
}

// copyFlushing copies an upstream body, flushing after every read so an
// event stream reaches the browser as it happens.
func copyFlushing(w http.ResponseWriter, body io.Reader) {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

var livePageTmpl = template.Must(template.New("live").Parse(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">{{if .Retry}}<meta http-equiv="refresh" content="3">{{end}}
<title>Live view</title>
<style>body{font-family:system-ui,sans-serif;margin:0;padding:2rem;color:#64748b;background:transparent}</style>
</head><body><p>{{.Message}}</p></body></html>`))

// livePage is the live view's own message page (inside the sandbox too).
func livePage(w http.ResponseWriter, status int, msg string, retry bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := livePageTmpl.Execute(w, struct {
		Message string
		Retry   bool
	}{msg, retry}); err != nil {
		_, _ = fmt.Fprint(w, template.HTMLEscapeString(msg))
	}
}
