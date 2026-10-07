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

package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// Client talks to one kubetest API server. The base URL is either the
// server itself (in-cluster) or its Kubernetes service-proxy path
// (https://<k8s-api>/api/v1/namespaces/<ns>/services/http:<svc>:<port>/proxy)
// with an authenticated http.Client — see ServiceProxyURL.
type Client struct {
	base  *url.URL
	http  *http.Client
	user  string
	token string
}

// New returns a Client for baseURL. httpClient carries authentication
// (nil → http.DefaultClient).
func New(baseURL string, httpClient *http.Client) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("apiclient: base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("apiclient: base URL %q must be http(s)", baseURL)
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{base: u, http: httpClient}, nil
}

// ServiceProxyURL is the base URL that reaches a Service through the
// Kubernetes API server's proxy — how Control Center reaches each
// cluster's kubetest API server without exposing it.
func ServiceProxyURL(k8sServer, namespace, service string, port int) string {
	return fmt.Sprintf("%s/api/v1/namespaces/%s/services/http:%s:%d/proxy",
		strings.TrimRight(k8sServer, "/"), url.PathEscape(namespace), url.PathEscape(service), port)
}

// WithToken returns a copy that sends the API token (HeaderToken) — the
// API server's --auth-token-file. The token is the client's credential;
// keep it out of logs.
func (c *Client) WithToken(token string) *Client {
	cp := *c
	cp.token = token
	return &cp
}

// AsUser returns a copy whose requests are attributed to user (HeaderUser).
func (c *Client) AsUser(user string) *Client {
	cp := *c
	cp.user = user
	return &cp
}

// APIError is a non-2xx response.
type APIError struct {
	Status  int
	Reason  string
	Message string
}

func (e *APIError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("kubetest API %d %s: %s", e.Status, e.Reason, e.Message)
	}
	return fmt.Sprintf("kubetest API %d: %s", e.Status, e.Message)
}

// IsNotFound reports a 404.
func IsNotFound(err error) bool { return statusIs(err, http.StatusNotFound) }

// IsConflict reports a 409 (finished run, GitOps-locked Test, …).
func IsConflict(err error) bool { return statusIs(err, http.StatusConflict) }

func statusIs(err error, status int) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == status
}

// ---- Tests -----------------------------------------------------------

// ListTests lists Tests in namespace ("" = all, on a cluster-wide server).
func (c *Client) ListTests(ctx context.Context, namespace string) ([]testsv1alpha1.Test, error) {
	var out []testsv1alpha1.Test
	err := c.getJSON(ctx, "/tests", ns(namespace), &out)
	return out, err
}

// GetTest returns one Test.
func (c *Client) GetTest(ctx context.Context, namespace, name string) (*testsv1alpha1.Test, error) {
	var out testsv1alpha1.Test
	if err := c.getJSON(ctx, "/tests/"+url.PathEscape(name), ns(namespace), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// QueryDryRun asks for a write to be admitted but not saved. Not
// "dryRun": the Kubernetes service proxy refuses that parameter.
const QueryDryRun = "dry-run"

// WriteOptions modify a Test write.
type WriteOptions struct {
	// DryRun admits the write (schema, admission webhooks) without saving.
	DryRun bool
}

func (o WriteOptions) query(namespace string) url.Values {
	q := ns(namespace)
	if o.DryRun {
		q.Set(QueryDryRun, "true")
	}
	return q
}

// CreateTest creates a Test, managed in the GUI (the API server sets
// app.kubernetes.io/managed-by=ui).
func (c *Client) CreateTest(ctx context.Context, t *testsv1alpha1.Test, o WriteOptions) (*testsv1alpha1.Test, error) {
	var out testsv1alpha1.Test
	if err := c.sendJSON(ctx, http.MethodPost, "/tests", o.query(t.Namespace), t, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PatchTest applies a JSON merge patch to a Test and returns the result.
// Only Tests managed in the GUI (app.kubernetes.io/managed-by=ui) can be
// patched; any other is a 409 (IsConflict) — its definition lives in Git.
func (c *Client) PatchTest(ctx context.Context, namespace, name string, patch any) (*testsv1alpha1.Test, error) {
	return c.PatchTestWith(ctx, namespace, name, patch, WriteOptions{})
}

// PatchTestWith is PatchTest with options.
func (c *Client) PatchTestWith(ctx context.Context, namespace, name string, patch any, o WriteOptions) (*testsv1alpha1.Test, error) {
	var out testsv1alpha1.Test
	if err := c.sendJSON(ctx, http.MethodPatch, "/tests/"+url.PathEscape(name), o.query(namespace), patch, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteTest deletes a GUI-managed Test (409 for any other). Its run
// history stays.
func (c *Client) DeleteTest(ctx context.Context, namespace, name string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/tests/"+url.PathEscape(name), ns(namespace), nil)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// ListTemplates lists the TestTemplates of namespace — the tool catalog.
func (c *Client) ListTemplates(ctx context.Context, namespace string) ([]testsv1alpha1.TestTemplate, error) {
	var out []testsv1alpha1.TestTemplate
	err := c.getJSON(ctx, "/templates", ns(namespace), &out)
	return out, err
}

// SetTestSchedule sets Test.spec.schedule (a cron expression), or clears
// it when schedule is "".
func (c *Client) SetTestSchedule(ctx context.Context, namespace, name, schedule string) (*testsv1alpha1.Test, error) {
	var value any // JSON null removes the field
	if schedule != "" {
		value = schedule
	}
	return c.PatchTest(ctx, namespace, name, map[string]any{"spec": map[string]any{"schedule": value}})
}

// GetResolvedTest returns the Test merged with its templates.
func (c *Client) GetResolvedTest(ctx context.Context, namespace, name string) (*ResolvedTest, error) {
	var out ResolvedTest
	if err := c.getJSON(ctx, "/tests/"+url.PathEscape(name)+"/resolved", ns(namespace), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- Runs ------------------------------------------------------------

// ListRunsOptions filters GET /runs. Zero values are unset.
type ListRunsOptions struct {
	Namespace     string
	Test          string
	Phase         string
	Source        string
	Parent        string // a composite run's children only
	Limit         int
	After         string     // RunPage.NextCursor of the previous page
	FinishedAfter *time.Time // finished runs only
}

// RunPage is one page of runs: live runs (first page only) then finished
// runs newest first. NextCursor is "" on the last page.
type RunPage struct {
	Runs       []Run
	NextCursor string
}

// ListRuns returns one page of runs.
func (c *Client) ListRuns(ctx context.Context, o ListRunsOptions) (*RunPage, error) {
	q := ns(o.Namespace)
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	set("test", o.Test)
	set("phase", o.Phase)
	set("source", o.Source)
	set("parent", o.Parent)
	set("after", o.After)
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.FinishedAfter != nil {
		q.Set("finishedAfter", o.FinishedAfter.UTC().Format(time.RFC3339))
	}
	resp, err := c.do(ctx, http.MethodGet, "/runs", q, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	page := &RunPage{NextCursor: resp.Header.Get(HeaderNextCursor)}
	if err := json.NewDecoder(resp.Body).Decode(&page.Runs); err != nil {
		return nil, fmt.Errorf("apiclient: decode runs: %w", err)
	}
	return page, nil
}

// GetRun returns a run by TestRun name (live) or UID (archived).
func (c *Client) GetRun(ctx context.Context, namespace, id string) (*Run, error) {
	var out Run
	if err := c.getJSON(ctx, "/runs/"+url.PathEscape(id), ns(namespace), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateRun starts a run. The server sets source=ui and records the
// AsUser identity as the creator.
func (c *Client) CreateRun(ctx context.Context, namespace string, run *testsv1alpha1.TestRun) (*testsv1alpha1.TestRun, error) {
	var out testsv1alpha1.TestRun
	if err := c.sendJSON(ctx, http.MethodPost, "/runs", ns(namespace), run, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AbortRun asks a live run to stop (idempotent).
func (c *Client) AbortRun(ctx context.Context, namespace, name, message string) (*Run, error) {
	var out Run
	err := c.sendJSON(ctx, http.MethodPost, "/runs/"+url.PathEscape(name)+"/abort", ns(namespace),
		AbortOptions{Message: message}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteRun removes a finished run (objects, CR, history row).
func (c *Client) DeleteRun(ctx context.Context, namespace, id string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/runs/"+url.PathEscape(id), ns(namespace), nil)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// SetComment sets (replaces) the comment of a finished run, attributed to
// the AsUser identity.
func (c *Client) SetComment(ctx context.Context, namespace, id, text string) (*Comment, error) {
	var out Comment
	err := c.sendJSON(ctx, http.MethodPut, "/runs/"+url.PathEscape(id)+"/comment", ns(namespace),
		CommentOptions{Text: text}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteComment removes a run's comment (no error when there was none).
func (c *Client) DeleteComment(ctx context.Context, namespace, id string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/runs/"+url.PathEscape(id)+"/comment", ns(namespace), nil)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// ---- Test cases ------------------------------------------------------------

// RunTestCases lists a finished run's JUnit test cases; failedOnly keeps
// failed and errored ones.
func (c *Client) RunTestCases(ctx context.Context, namespace, id string, failedOnly bool) ([]TestCase, error) {
	q := ns(namespace)
	if failedOnly {
		q.Set("status", "failed")
	}
	var out []TestCase
	err := c.getJSON(ctx, "/runs/"+url.PathEscape(id)+"/testcases", q, &out)
	return out, err
}

// TestCaseStats aggregates a Test's cases over its last `runs` runs
// (0 = server default).
func (c *Client) TestCaseStats(ctx context.Context, namespace, test string, runs int) ([]CaseStats, error) {
	q := ns(namespace)
	if runs > 0 {
		q.Set("runs", strconv.Itoa(runs))
	}
	var out []CaseStats
	err := c.getJSON(ctx, "/tests/"+url.PathEscape(test)+"/testcases", q, &out)
	return out, err
}

// TestCaseHistory lists one case's results over a Test's runs, newest
// first (limit 0 = server default).
func (c *Client) TestCaseHistory(ctx context.Context, namespace, test, caseKey string, limit int) ([]CaseRun, error) {
	q := ns(namespace)
	q.Set("case", caseKey)
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []CaseRun
	err := c.getJSON(ctx, "/tests/"+url.PathEscape(test)+"/testcases/history", q, &out)
	return out, err
}

// ---- Audit -----------------------------------------------------------------

// ListAuditOptions filters GET /audit. Zero values are unset.
type ListAuditOptions struct {
	Namespace string
	Actor     string
	Action    string
	Limit     int
	Before    string // AuditPage.NextCursor of the previous page
}

// AuditPage is one page of audit entries, newest first. NextCursor is ""
// on the last page.
type AuditPage struct {
	Entries    []AuditEntry
	NextCursor string
}

// ListAudit returns one page of the audit log.
func (c *Client) ListAudit(ctx context.Context, o ListAuditOptions) (*AuditPage, error) {
	q := ns(o.Namespace)
	for k, v := range map[string]string{"actor": o.Actor, "action": o.Action, "before": o.Before} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	resp, err := c.do(ctx, http.MethodGet, "/audit", q, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	page := &AuditPage{NextCursor: resp.Header.Get(HeaderNextCursor)}
	if err := json.NewDecoder(resp.Body).Decode(&page.Entries); err != nil {
		return nil, fmt.Errorf("apiclient: decode audit: %w", err)
	}
	return page, nil
}

// ---- Logs + artifacts --------------------------------------------------

// OpenLogs streams the run's stored log (text). Caller closes it.
func (c *Client) OpenLogs(ctx context.Context, namespace, id string) (io.ReadCloser, error) {
	return c.OpenLogsFrom(ctx, namespace, id, 0)
}

// OpenLogsFrom streams the run's stored log from byte offset on — what a
// client following a live run hasn't read yet. Caller closes it.
func (c *Client) OpenLogsFrom(ctx context.Context, namespace, id string, offset int64) (io.ReadCloser, error) {
	q := ns(namespace)
	if offset > 0 {
		q.Set("offset", strconv.FormatInt(offset, 10))
	}
	resp, err := c.do(ctx, http.MethodGet, "/runs/"+url.PathEscape(id)+"/logs.txt", q, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// ListArtifacts lists the run's artifacts.
func (c *Client) ListArtifacts(ctx context.Context, namespace, id string) ([]Artifact, error) {
	var out []Artifact
	err := c.getJSON(ctx, "/runs/"+url.PathEscape(id)+"/artifacts", ns(namespace), &out)
	return out, err
}

// ArtifactStream is an open artifact download.
type ArtifactStream struct {
	Body        io.ReadCloser
	ContentType string
	Length      int64 // -1 when unknown
}

// OpenArtifact streams one artifact. path is relative ("results/x.xml").
func (c *Client) OpenArtifact(ctx context.Context, namespace, id, path string) (*ArtifactStream, error) {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	resp, err := c.do(ctx, http.MethodGet,
		"/runs/"+url.PathEscape(id)+"/artifacts/"+strings.Join(segs, "/"), ns(namespace), nil)
	if err != nil {
		return nil, err
	}
	return &ArtifactStream{Body: resp.Body, ContentType: resp.Header.Get("Content-Type"), Length: resp.ContentLength}, nil
}

// OpenLiveView opens GET /runs/{id}/live/{path}?{rawQuery}: the run's live
// web UI (spec.liveView), passed through as is — HTML, assets or an event
// stream. The caller reads and closes the body. A run that hasn't started
// is a 503, a finished one a 410 (ReasonGone).
func (c *Client) OpenLiveView(ctx context.Context, namespace, id, path, rawQuery string) (*http.Response, error) {
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil, fmt.Errorf("apiclient: live view query: %w", err)
	}
	maps.Copy(q, ns(namespace))
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		segs[i] = url.PathEscape(seg)
	}
	return c.do(ctx, http.MethodGet, "/runs/"+url.PathEscape(id)+"/live/"+strings.Join(segs, "/"), q, nil)
}

// RunEvents returns the Kubernetes events of a run's objects, oldest first.
func (c *Client) RunEvents(ctx context.Context, namespace, id string) ([]RunEvent, error) {
	var out []RunEvent
	err := c.getJSON(ctx, "/runs/"+url.PathEscape(id)+"/events", ns(namespace), &out)
	return out, err
}

// Healthz checks the server is reachable.
func (c *Client) Healthz(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodGet, "/healthz", nil, nil)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// ---- plumbing ------------------------------------------------------------

func ns(namespace string) url.Values {
	q := url.Values{}
	if namespace != "" {
		q.Set(QueryNamespace, namespace)
	}
	return q
}

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	return c.sendJSON(ctx, http.MethodGet, path, q, nil, out)
}

func (c *Client) sendJSON(ctx context.Context, method, path string, q url.Values, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	resp, err := c.do(ctx, method, path, q, body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("apiclient: decode %s %s: %w", method, path, err)
	}
	return nil
}

// do sends a request and returns the response for 2xx; anything else is
// an *APIError (body closed).
func (c *Client) do(ctx context.Context, method, path string, q url.Values, body io.Reader) (*http.Response, error) {
	// path is already escaped segment by segment; keep that exact encoding
	// in RawPath (so an escaped "/" in a name stays one segment).
	u := *c.base
	u.RawPath = c.base.EscapedPath() + path
	unescaped, err := url.PathUnescape(u.RawPath)
	if err != nil {
		return nil, fmt.Errorf("apiclient: path %q: %w", path, err)
	}
	u.Path = unescaped
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.user != "" {
		req.Header.Set(HeaderUser, c.user)
	}
	if c.token != "" {
		req.Header.Set(HeaderToken, c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: %s %s: %w", method, path, err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	apiErr := &APIError{Status: resp.StatusCode}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var e Error
	if json.Unmarshal(b, &e) == nil && e.Message != "" {
		apiErr.Reason, apiErr.Message = e.Reason, e.Message
	} else {
		apiErr.Message = strings.TrimSpace(string(b))
	}
	return nil, apiErr
}
