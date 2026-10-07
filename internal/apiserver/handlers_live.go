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
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"time"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/controller"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// liveView proxies GET /runs/{id}/live/{path...} to the run's live web UI
// (spec.liveView) at the test pod's IP, while the run is running. Only the
// declared port of the run's own pod is reachable — the pod IP comes from
// the TestRun status the operator writes, never from the request. Streams
// (k6's server-sent events) are flushed as they come.
//
// Every proxied request ends after liveStreamMax: a live UI's open event
// stream must not keep the tool alive — k6 doesn't exit while a dashboard
// client is connected ("Stopping outputs…"), so watching a run would hold
// it at running until the Job's deadline. Browsers' EventSource reconnects
// by itself, and the tool sends its state again on a new connection.
//
// Callers never pass credentials through: cookies, Authorization and the
// attribution header are dropped, and the UI's cookies are not returned.
// liveStreamMax bounds one proxied live-view request (see liveView).
var liveStreamMax = 15 * time.Second

func (s *Server) liveView(w http.ResponseWriter, r *http.Request) {
	ns, err := s.targetNamespace(r, "")
	if err != nil {
		writeLookupError(w, err)
		return
	}
	ref, err := s.findRun(r.Context(), ns, r.PathValue("id"))
	if err != nil {
		writeLookupError(w, err)
		return
	}
	run := ref.CR
	if run == nil || controller.IsTerminalPhase(run.Status.Phase) {
		writeError(w, http.StatusGone, apiclient.ReasonGone,
			"the run has finished — its live view is gone; see its report and artifacts")
		return
	}
	var spec testsv1alpha1.TestSpec
	if err := json.Unmarshal([]byte(run.Status.ResolvedSpec), &spec); err != nil || spec.LiveView == nil {
		writeError(w, http.StatusNotFound, ReasonNotFound, "this run's Test declares no live view (spec.liveView)")
		return
	}
	if run.Status.Phase != testsv1alpha1.PhaseRunning || run.Status.PodIP == "" {
		w.Header().Set("Retry-After", "2")
		writeError(w, http.StatusServiceUnavailable, ReasonServiceUnavail, "the run hasn't started yet")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), liveStreamMax)
	defer cancel()
	r = r.WithContext(ctx)
	target := net.JoinHostPort(run.Status.PodIP, strconv.Itoa(int(spec.LiveView.Port)))
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host = "http", target
			pr.Out.URL.Path, pr.Out.URL.RawPath = "/"+r.PathValue("path"), ""
			q := pr.In.URL.Query()
			q.Del(QueryNamespace) // ours, not the UI's
			pr.Out.URL.RawQuery = q.Encode()
			pr.Out.Host = target
			for _, h := range []string{"Cookie", "Authorization", "Proxy-Authorization", HeaderUser} {
				pr.Out.Header.Del(h)
			}
		},
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Del("Set-Cookie")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			if req.Context().Err() != nil {
				return // our deadline or the client went away — the stream just ends
			}
			writeError(w, http.StatusBadGateway, ReasonServiceUnavail, "live view unreachable: "+err.Error())
		},
	}
	proxy.ServeHTTP(w, r)
}
