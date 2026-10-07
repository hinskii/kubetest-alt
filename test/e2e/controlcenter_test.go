//go:build e2e
// +build e2e

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

package e2e

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// Control Center as the chart installs it: Google sign-in through the
// oauth2-proxy sidecar (CC_PROXY_URL, the Service), the UI on the pod's
// localhost (CC_URL, reached by port-forward with the header oauth2-proxy
// would set). The run uses the k6 catalog template, so its live view goes
// the whole real way: Control Center → Kubernetes API service proxy →
// kubetest API server → the test pod's IP.

const ccDeveloper = "dev@e2e.test"

var liveFrame = regexp.MustCompile(`<iframe class="live-view" src="(/live/[^"]+)"`)

// noRedirect returns redirects as responses.
var noRedirect = &http.Client{
	Timeout:       30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func ccRequest(t *testing.T, method, rawURL, user string, form url.Values) *http.Response {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, rawURL, body)
	require.NoError(t, err)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	if user != "" {
		req.Header.Set("X-Forwarded-Email", user)
	}
	resp, err := noRedirect.Do(req)
	require.NoError(t, err)
	return resp
}

func ccPage(t *testing.T, rawURL, user string) string {
	t.Helper()
	resp := ccRequest(t, http.MethodGet, rawURL, user, nil)
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s: %s", rawURL, b)
	return string(b)
}

func scenarioControlCenter(t *testing.T, ctx context.Context, c client.Client) {
	proxy, ui := os.Getenv("CC_PROXY_URL"), os.Getenv("CC_URL")
	if proxy == "" || ui == "" {
		t.Skip("CC_PROXY_URL / CC_URL not set (run via test/e2e/run.sh)")
	}

	// 1. Sign-in: without a Google session the proxy sends people to
	//    Google, a forged email header changes nothing.
	resp := ccRequest(t, http.MethodGet, proxy+"/clusters/local", "admin@e2e.test", nil)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	assert.True(t, strings.HasPrefix(resp.Header.Get("Location"), "https://accounts.google.com/"), resp.Header.Get("Location"))

	// 2. The UI reaches this cluster's kubetest through the service proxy.
	home := ccPage(t, ui+"/", ccDeveloper)
	assert.Contains(t, home, "connected", "Control Center → service proxy → API server")

	// 3. A k6 Test from the catalog template, long enough to watch live,
	//    made with the wizard: the review is a dry run through the real
	//    admission webhooks, then the Test is created, managed in the GUI.
	wizard := url.Values{
		"mode": {"form"}, "namespace": {workloadNS}, "name": {"e2e-cc-k6"}, "template": {"k6"},
		"param.k6.dashboardPeriod": {"1s"},
		// Just the inline file: the wizard points k6's script at it.
		"useFiles": {"1"}, "file.path": {"live.js"},
		"file.content": {"import { sleep } from 'k6';\nexport const options = { vus: 1, duration: '45s' };\nexport default function () { sleep(0.5); }\n"},
	}
	newPage := ccPage(t, ui+"/clusters/local/tests/new?namespace="+workloadNS, ccDeveloper)
	assert.Contains(t, newPage, `name="template" value="k6"`, "the catalog of the namespace")
	wizard.Set("action", "preview")
	review := ccRequest(t, http.MethodPost, ui+"/clusters/local/tests/new", ccDeveloper, wizard)
	reviewBody, _ := io.ReadAll(review.Body)
	_ = review.Body.Close()
	require.Contains(t, string(reviewBody), "The API would admit this Test", string(reviewBody))
	bad := url.Values{"mode": {"form"}, "namespace": {workloadNS}, "name": {"e2e-cc-bad"}, "template": {"k6"},
		"podAnnotations": {"x=y"}, "serviceAccount": {"kube-system-admin"}, "action": {"preview"}}
	refused := ccRequest(t, http.MethodPost, ui+"/clusters/local/tests/new", ccDeveloper, bad)
	refusedBody, _ := io.ReadAll(refused.Body)
	_ = refused.Body.Close()
	assert.Contains(t, string(refusedBody), "The API would refuse it", "the webhook's pod policy, before anything exists")
	wizard.Set("action", "save")
	resp = ccRequest(t, http.MethodPost, ui+"/clusters/local/tests/new", ccDeveloper, wizard)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	test := &testsv1alpha1.Test{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: workloadNS, Name: "e2e-cc-k6"}, test))
	assert.Equal(t, "ui", test.Labels["app.kubernetes.io/managed-by"])
	assert.Equal(t, "live.js", test.Spec.Config["script"].Default)

	tests := ccPage(t, ui+"/clusters/local", ccDeveloper)
	assert.Contains(t, tests, ">e2e-cc-k6<")

	// 4. Start it from Control Center, as a developer.
	resp = ccRequest(t, http.MethodPost, ui+"/clusters/local/tests/"+workloadNS+"/e2e-cc-k6/run", ccDeveloper, url.Values{})
	_ = resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	runPath := strings.SplitN(resp.Header.Get("Location"), "?", 2)[0]
	require.True(t, strings.HasPrefix(runPath, "/clusters/local/runs/"+workloadNS+"/e2e-cc-k6-"), runPath)
	runName := runPath[strings.LastIndex(runPath, "/")+1:]

	// 5. Live view, through the proxy without a session (signed link).
	waitForPhase(t, ctx, c, runName, testsv1alpha1.PhaseRunning, 3*time.Minute)
	var src string
	require.Eventually(t, func() bool {
		if m := liveFrame.FindStringSubmatch(ccPage(t, ui+runPath, ccDeveloper)); m != nil {
			src = strings.ReplaceAll(m[1], "&amp;", "&")
			return true
		}
		return false
	}, time.Minute, 2*time.Second, "the run page shows the live view")
	var dashboard string
	require.Eventually(t, func() bool {
		resp := ccRequest(t, http.MethodGet, proxy+src, "", nil)
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		dashboard = string(b)
		return resp.StatusCode == http.StatusOK && strings.Contains(dashboard, "k6 dashboard")
	}, time.Minute, 2*time.Second, "the k6 dashboard through oauth2-proxy → CC → service proxy → API server → pod")
	assert.Equal(t, "sandbox allow-scripts; frame-ancestors 'self'", headerOf(t, proxy+src, "Content-Security-Policy"))

	events, err := http.Get(proxy + strings.TrimSuffix(src, "ui/?endpoint=../") + "events")
	require.NoError(t, err)
	defer func() { _ = events.Body.Close() }()
	line := make(chan string, 1)
	go func() {
		l, _ := bufio.NewReader(events.Body).ReadString('\n')
		line <- l
	}()
	select {
	case l := <-line:
		assert.True(t, strings.HasPrefix(l, "id:") || strings.HasPrefix(l, "event:") || strings.HasPrefix(l, "data:"),
			"a server-sent event arrives live: %q", l)
	case <-time.After(30 * time.Second):
		t.Fatal("no live event through the whole chain within 30s (buffered?)")
	}

	// 6. Finished: the run page shows the verdict and the stored log.
	waitForPhase(t, ctx, c, runName, testsv1alpha1.PhasePassed, 3*time.Minute)
	page := ccPage(t, ui+runPath, ccDeveloper)
	assert.Contains(t, page, ">passed<")
	assert.NotContains(t, page, "live-view", "no live view once the run is over")
	// The pod's Kubernetes events (the API server's events RBAC).
	assert.Contains(t, page, "Kubernetes events")
	assert.Contains(t, page, ">Scheduled<", "the scheduler's event for the run's pod")
	logs := ccPage(t, ui+runPath+"/logs.txt", ccDeveloper)
	assert.Contains(t, logs, "live.js", "the run's log through Control Center")
	history := ccPage(t, ui+"/clusters/local/tests/"+workloadNS+"/e2e-cc-k6", ccDeveloper)
	assert.Contains(t, history, ">"+runName+"<", "the run in the Test's history")

	// 7. An admin deletes the Test; its run history stays.
	resp = ccRequest(t, http.MethodPost, ui+"/clusters/local/tests/"+workloadNS+"/e2e-cc-k6/delete", "admin@e2e.test", url.Values{})
	_ = resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Location"), "deleted")
	err = c.Get(ctx, client.ObjectKey{Namespace: workloadNS, Name: "e2e-cc-k6"}, &testsv1alpha1.Test{})
	assert.True(t, apierrors.IsNotFound(err), "the Test is gone")
	assert.Contains(t, ccPage(t, ui+runPath, ccDeveloper), ">passed<", "its run stays")
}

func headerOf(t *testing.T, rawURL, header string) string {
	t.Helper()
	resp, err := noRedirect.Get(rawURL)
	require.NoError(t, err)
	_ = resp.Body.Close()
	return resp.Header.Get(header)
}
