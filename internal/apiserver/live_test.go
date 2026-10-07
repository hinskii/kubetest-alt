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

package apiserver

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// livePod plays a test pod serving a live UI; it records what reached it.
type livePod struct {
	srv          *httptest.Server
	gotPath      string
	gotQuery     string
	gotCookie    string
	gotUser      string
	gotAuthorize string
	disconnected chan struct{}
}

func newLivePod(t *testing.T) *livePod {
	t.Helper()
	p := &livePod{disconnected: make(chan struct{}, 8)}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.gotPath, p.gotQuery = r.URL.Path, r.URL.RawQuery
		p.gotCookie, p.gotUser, p.gotAuthorize = r.Header.Get("Cookie"), r.Header.Get(HeaderUser), r.Header.Get("Authorization")
		if r.URL.Path == "/events" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "data: first\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done() // a stream stays open
			p.disconnected <- struct{}{}
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "ui", Value: "x"}) // #nosec G124 -- a test pod's cookie, asserted to be dropped
		_, _ = fmt.Fprint(w, "<html>dashboard</html>")
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *livePod) runningRun(t *testing.T, name string) *testsv1alpha1.TestRun {
	t.Helper()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(p.srv.URL, "http://"))
	require.NoError(t, err)
	run := liveRun(name, testsv1alpha1.PhaseRunning)
	run.Status.PodIP = host
	run.Status.ResolvedSpec = `{"liveView":{"port":` + port + `,"path":"/ui/?endpoint=../"}}`
	return run
}

func TestLiveView_ProxiesToTheRunsPod(t *testing.T) {
	pod := newLivePod(t)
	s, _, _ := mkStorageServer(t, nil, pod.runningRun(t, "load"))
	h := s.Handler()

	req := httptest.NewRequest(http.MethodGet, "/runs/load/live/ui/?endpoint=../&namespace=default", nil)
	req.Header.Set("Cookie", "_oauth2_proxy=secret")
	req.Header.Set(HeaderUser, "alice@example.com")
	req.Header.Set("Authorization", "Bearer k8s-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "<html>dashboard</html>", rec.Body.String())
	assert.Equal(t, "/ui/", pod.gotPath)
	assert.Equal(t, "endpoint=..%2F", pod.gotQuery, "the UI's query, without ours")
	assert.Empty(t, pod.gotCookie, "no session cookie reaches the test pod")
	assert.Empty(t, pod.gotUser)
	assert.Empty(t, pod.gotAuthorize)
	assert.Empty(t, rec.Header().Values("Set-Cookie"), "the UI can't set cookies")
}

func TestLiveView_StreamsEvents(t *testing.T) {
	pod := newLivePod(t)
	s, _, _ := mkStorageServer(t, nil, pod.runningRun(t, "load"))
	api := httptest.NewServer(s.Handler())
	t.Cleanup(api.Close)

	resp, err := http.Get(api.URL + "/runs/load/live/events")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	line := make(chan string, 1)
	go func() {
		l, _ := bufio.NewReader(resp.Body).ReadString('\n')
		line <- l
	}()
	select {
	case l := <-line:
		assert.Equal(t, "data: first\n", l, "an event arrives while the stream is still open")
	case <-time.After(5 * time.Second):
		t.Fatal("the event was buffered instead of flushed")
	}
}

// An open event stream must not keep the tool alive: the proxy ends it
// after liveStreamMax, and the tool sees its client go away.
func TestLiveView_StreamsEndSoTheToolCanExit(t *testing.T) {
	old := liveStreamMax
	liveStreamMax = 300 * time.Millisecond
	t.Cleanup(func() { liveStreamMax = old })
	pod := newLivePod(t)
	s, _, _ := mkStorageServer(t, nil, pod.runningRun(t, "load"))
	api := httptest.NewServer(s.Handler())
	t.Cleanup(api.Close)

	start := time.Now()
	resp, err := http.Get(api.URL + "/runs/load/live/events")
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body) // returns once the proxy ends the stream
	_ = resp.Body.Close()
	assert.Contains(t, string(body), "data: first")
	assert.Less(t, time.Since(start), 5*time.Second, "the stream ended instead of staying open")
	select {
	case <-pod.disconnected:
	case <-time.After(5 * time.Second):
		t.Fatal("the test pod never saw its client go away")
	}
}

func TestLiveView_OnlyWhileRunning(t *testing.T) {
	pod := newLivePod(t)
	queued := pod.runningRun(t, "waiting")
	queued.Status.Phase, queued.Status.PodIP = testsv1alpha1.PhaseQueued, ""
	done := pod.runningRun(t, "done")
	done.Status.Phase = testsv1alpha1.PhasePassed
	plain := liveRun("plain", testsv1alpha1.PhaseRunning)
	plain.Status.ResolvedSpec, plain.Status.PodIP = `{}`, "127.0.0.1"
	s, _, _ := mkStorageServer(t, nil, queued, done, plain)
	h := s.Handler()

	get := func(name string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/runs/"+url.PathEscape(name)+"/live/", nil))
		return rec
	}
	rec := get("waiting")
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "2", rec.Header().Get("Retry-After"))
	assert.Equal(t, http.StatusGone, get("done").Code)
	assert.Equal(t, http.StatusNotFound, get("plain").Code, "no spec.liveView, nothing to reach")
	assert.Equal(t, http.StatusNotFound, get("nope").Code)

	assert.Equal(t, "/ui/?endpoint=../", runEnvelopeFromCR(pod.runningRun(t, "x")).LiveView)
	assert.Empty(t, runEnvelopeFromCR(done).LiveView, "finished runs have none")
}
