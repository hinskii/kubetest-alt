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
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

func TestLiveSigner(t *testing.T) {
	s, err := newLiveSigner([]byte("key-1"))
	require.NoError(t, err)
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	claims := liveClaims{Cluster: "dev", Namespace: "team-a", Run: "load-1", Exp: now.Add(time.Hour).Unix()}
	tok := s.sign(claims)

	got, err := s.verify(tok, now)
	require.NoError(t, err)
	assert.Equal(t, claims, got)

	_, err = s.verify(tok, now.Add(2*time.Hour))
	require.ErrorIs(t, err, errBadLiveToken, "expired")
	other, _ := newLiveSigner([]byte("key-2"))
	_, err = other.verify(tok, now)
	require.ErrorIs(t, err, errBadLiveToken, "another key")
	payload, sig, _ := strings.Cut(tok, ".")
	forged := s.sign(liveClaims{Cluster: "dev", Namespace: "team-a", Run: "someone-else", Exp: claims.Exp})
	forgedPayload, _, _ := strings.Cut(forged, ".")
	_, err = s.verify(forgedPayload+"."+sig, now)
	require.ErrorIs(t, err, errBadLiveToken, "a payload swapped under a valid signature")
	for _, bad := range []string{"", "x", payload, payload + ".", "." + sig} {
		_, err = s.verify(bad, now)
		require.ErrorIs(t, err, errBadLiveToken, bad)
	}

	random1, _ := newLiveSigner(nil)
	random2, _ := newLiveSigner(nil)
	_, err = random2.verify(random1.sign(claims), now)
	require.ErrorIs(t, err, errBadLiveToken, "no key configured: a random one per process")
}

// liveUpstream plays the test pod's live UI.
func liveUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/events":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "data: tick\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		default:
			http.SetCookie(w, &http.Cookie{Name: "ui", Value: "x"}) // #nosec G124 -- a test pod's cookie, asserted to be dropped
			w.Header().Set("Content-Type", "text/html")
			// #nosec G705 -- a test upstream echoing what reached it.
			_, _ = fmt.Fprintf(w, "<html>dashboard %s?%s cookie=%q</html>", r.URL.Path, r.URL.RawQuery, r.Header.Get("Cookie"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func liveRunFor(t *testing.T, upstream *httptest.Server, phase testsv1alpha1.Phase) *testsv1alpha1.TestRun {
	t.Helper()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))
	require.NoError(t, err)
	run := runOf("load-1", phase)
	run.Status.PodIP = host
	run.Status.ResolvedSpec = `{"liveView":{"port":` + port + `,"path":"/ui/?endpoint=../"}}`
	return run
}

var liveSrc = regexp.MustCompile(`<iframe class="live-view" src="(/live/[^"]+)"`)

func TestLiveView_EndToEnd(t *testing.T) {
	upstream := liveUpstream(t)
	w := newWorld(t, smokeTest(), liveRunFor(t, upstream, testsv1alpha1.PhaseRunning))

	page := w.get(t, "/clusters/dev/runs/team-a/load-1", "").Body.String()
	m := liveSrc.FindStringSubmatch(page)
	require.NotNil(t, m, "a running run with spec.liveView shows its live view:\n%s", page)
	assert.Contains(t, page, `sandbox="allow-scripts"`)
	src := strings.ReplaceAll(m[1], "&amp;", "&")
	assert.True(t, strings.HasSuffix(src, "/ui/?endpoint=../"), src)

	// No session needed (oauth2-proxy lets /live/ through): the link is it.
	req := httptest.NewRequest(http.MethodGet, src, nil)
	req.Header.Set("Cookie", "_oauth2_proxy=secret")
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `dashboard /ui/?endpoint=..%2F cookie=""`, "path and query pass, the session cookie doesn't")
	h := rec.Header()
	assert.Equal(t, "sandbox allow-scripts; frame-ancestors 'self'", h.Get("Content-Security-Policy"))
	assert.NotContains(t, h.Get("Content-Security-Policy"), "allow-same-origin")
	assert.Equal(t, "*", h.Get("Access-Control-Allow-Origin"), "the sandboxed page's own requests are cross-origin")
	assert.Equal(t, "no-referrer", h.Get("Referrer-Policy"), "the token must not leak via Referer")
	assert.Empty(t, h.Values("Set-Cookie"))

	// The event stream arrives as it happens.
	cc := httptest.NewServer(w.h)
	t.Cleanup(cc.Close)
	base := strings.TrimSuffix(src, "ui/?endpoint=../")
	resp, err := http.Get(cc.URL + base + "events")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	line := make(chan string, 1)
	go func() {
		l, _ := bufio.NewReader(resp.Body).ReadString('\n')
		line <- l
	}()
	select {
	case l := <-line:
		assert.Equal(t, "data: tick\n", l)
	case <-time.After(5 * time.Second):
		t.Fatal("events were buffered, not streamed")
	}
}

func TestLiveView_Messages(t *testing.T) {
	upstream := liveUpstream(t)
	get := func(w *world, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		w.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	link := func(w *world) string {
		m := liveSrc.FindStringSubmatch(w.get(t, "/clusters/dev/runs/team-a/load-1", "").Body.String())
		require.NotNil(t, m)
		return strings.ReplaceAll(m[1], "&amp;", "&")
	}

	queued := newWorld(t, smokeTest(), liveRunFor(t, upstream, testsv1alpha1.PhaseQueued))
	rec := get(queued, link(queued))
	assert.Contains(t, rec.Body.String(), "Waiting for the run to start")
	assert.Contains(t, rec.Body.String(), `http-equiv="refresh"`)

	finished := newWorld(t, smokeTest(), liveRunFor(t, upstream, testsv1alpha1.PhasePassed))
	assert.NotContains(t, finished.get(t, "/clusters/dev/runs/team-a/load-1", "").Body.String(), "live-view",
		"finished runs have no live view")

	rec = get(finished, "/live/forged.token/ui/")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid or has expired")
	assert.Equal(t, liveCSP, rec.Header().Get("Content-Security-Policy"), "messages are sandboxed too")
}
