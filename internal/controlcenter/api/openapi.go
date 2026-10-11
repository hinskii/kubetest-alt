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
	"encoding/json"
	"net/http"
	"reflect"

	"github.com/swaggest/jsonschema-go"
	"github.com/swaggest/openapi-go"
	"github.com/swaggest/openapi-go/openapi31"

	"github.com/hinskii/kubetest-alt/internal/controlcenter/auth"
)

// route is one API endpoint: registered on the mux and described in the
// OpenAPI spec from the same table, so neither can drift from the other.
type route struct {
	Method, Path string
	// ID is the operationId (the generated client's name for it).
	ID, Summary string
	// Role is the least role allowed (viewer: everyone signed in).
	Role auth.Role
	// In are the request's path/query parameters and body, as structs
	// tagged path:, query: and json:.
	In []any
	// Out is the success response body (nil: none) with its status.
	Out    any
	Status int
	// Errors are the error statuses it answers with (body: Error, or
	// ErrorBodies' for that status).
	Errors      []int
	ErrorBodies map[int]any
	handler     http.HandlerFunc
}

type clusterParams struct {
	Cluster string `path:"cluster"`
}

type testParams struct {
	Cluster   string `path:"cluster"`
	Namespace string `path:"ns"`
	Name      string `path:"name"`
}

type runParams struct {
	Cluster   string `path:"cluster"`
	Namespace string `path:"ns"`
	ID        string `path:"id"`
}

type logParams struct {
	Cluster   string `path:"cluster"`
	Namespace string `path:"ns"`
	ID        string `path:"id"`
	// Offset is where to read from (X-Next-Offset of the previous chunk).
	Offset int64 `query:"offset"`
}

// LogChunk is the log text from offset on, with headers.
type LogChunk struct {
	Text  string `contentType:"text/plain"`
	Phase string `header:"X-Run-Phase" description:"The run's phase now."`
	Next  int64  `header:"X-Next-Offset" description:"Where the next chunk starts."`
	More  string `header:"X-More" description:"1: there is more right away; ask again at once."`
}

type runsParams struct {
	Cluster   string `path:"cluster"`
	Namespace string `path:"ns"`
	Name      string `path:"name"`
	// After is the cursor of the page (RunPage.next of the previous one).
	After string `query:"after"`
}

const (
	testPath = Prefix + "/clusters/{cluster}/tests/{ns}/{name}"
	runPath  = Prefix + "/clusters/{cluster}/runs/{ns}/{id}"
)

func (s *Server) routes() []route {
	h := func(min auth.Role, f http.HandlerFunc) http.HandlerFunc {
		if min == auth.RoleViewer {
			return f
		}
		return requireRole(min, f)
	}
	rs := []route{
		{Method: http.MethodGet, Path: Prefix + "/me", ID: "getMe",
			Summary: "The signed-in user and what their role allows.", Out: Me{}, handler: s.me},
		{Method: http.MethodGet, Path: Prefix + "/clusters", ID: "listClusters",
			Summary: "Configured clusters, each probed now, in config order.", Out: []Cluster{}, handler: s.clusters},
		{Method: http.MethodGet, Path: Prefix + "/clusters/{cluster}/schedules", ID: "listSchedules",
			Summary: "Every recurring schedule and every run waiting for its start time.", In: []any{clusterParams{}},
			Out: ScheduleList{}, Errors: []int{404, 502}, handler: s.listSchedules},
		{Method: http.MethodGet, Path: Prefix + "/clusters/{cluster}/namespaces", ID: "listNamespaces",
			Summary: "Namespaces that have Tests or a tool catalog.", In: []any{clusterParams{}},
			Out: NamespaceList{}, Errors: []int{404}, handler: s.listNamespaces},
		{Method: http.MethodGet, Path: Prefix + "/clusters/{cluster}/templates", ID: "listTemplates",
			Summary: "A namespace's tool catalog (TestTemplates), as the editor offers it.", In: []any{templatesParams{}},
			Out: TemplateList{}, Errors: []int{400, 404}, handler: s.listTemplates},
		{Method: http.MethodPost, Path: Prefix + "/clusters/{cluster}/tests/preview", ID: "previewTest",
			Summary: "The Test a form describes, as YAML, with what is wrong — the same build as createTest.",
			In:      []any{clusterParams{}, TestForm{}}, Out: Preview{}, Errors: []int{400, 404}, handler: s.preview(false)},
		{Method: http.MethodPost, Path: Prefix + "/clusters/{cluster}/tests", ID: "createTest", Role: auth.RoleDeveloper,
			Summary: "Create the form's Test (managed by the GUI), or with dry-run=true check it.",
			In:      []any{writeParams{}, TestForm{}}, Out: SavedTest{}, Status: http.StatusCreated,
			Errors: []int{400, 403, 404, 409, 502}, ErrorBodies: map[int]any{400: FormError{}}, handler: s.write(false)},
		{Method: http.MethodGet, Path: Prefix + "/clusters/{cluster}/tests", ID: "listTests",
			Summary: "Every Test in the cluster, by namespace and name.", In: []any{clusterParams{}},
			Out: TestList{}, Errors: []int{404, 502}, handler: s.listTests},
		{Method: http.MethodGet, Path: testPath, ID: "getTest",
			Summary: "A Test with its templates merged: parameters, schedule, definition.", In: []any{testParams{}},
			Out: TestDetail{}, Errors: []int{404, 502}, handler: s.getTest},
		{Method: http.MethodGet, Path: testPath + "/form", ID: "getTestForm",
			Summary: "A Test as the editor shows it (edit, duplicate).", In: []any{testParams{}},
			Out: TestFormOf{}, Errors: []int{404, 502}, handler: s.testForm},
		{Method: http.MethodPost, Path: testPath + "/preview", ID: "previewTestEdit",
			Summary: "The edited Test a form describes, as YAML, with what is wrong.", In: []any{testParams{}, TestForm{}},
			Out: Preview{}, Errors: []int{400, 404, 502}, handler: s.preview(true)},
		{Method: http.MethodPut, Path: testPath, ID: "updateTest", Role: auth.RoleDeveloper,
			Summary: "Save the form over a GUI-managed Test (a merge patch, conditional on the form's resourceVersion), or with dry-run=true check it.",
			In:      []any{writeTestParams{}, TestForm{}}, Out: SavedTest{}, Errors: []int{400, 403, 404, 409, 502},
			ErrorBodies: map[int]any{400: FormError{}}, handler: s.write(true)},
		{Method: http.MethodDelete, Path: testPath, ID: "deleteTest", Role: auth.RoleAdmin,
			Summary: "Delete a GUI-managed Test; its run history stays.", In: []any{testParams{}},
			Status: http.StatusNoContent, Errors: []int{403, 404, 409, 502}, handler: s.deleteTest},
		{Method: http.MethodPut, Path: testPath + "/schedule", ID: "setSchedule", Role: auth.RoleDeveloper,
			Summary: "Set (or, with \"\", remove) a GUI-managed Test's recurring schedule.",
			In:      []any{testParams{}, ScheduleUpdate{}}, Out: ScheduleResult{},
			Errors: []int{400, 403, 404, 409, 502}, handler: s.setSchedule},
		{Method: http.MethodGet, Path: testPath + "/analytics", ID: "getAnalytics",
			Summary: "A Test's last 10/30/100 finished runs: summary, trends, cases.", In: []any{casesParams{}},
			Out: Analytics{}, Errors: []int{404, 502}, handler: s.analytics},
		{Method: http.MethodGet, Path: testPath + "/analytics/compare", ID: "compareRuns",
			Summary: "Runs of the Test side by side (run=, repeated, at most 10), oldest first, with Markdown.",
			In:      []any{compareRunsParams{}}, Out: Comparison{}, Errors: []int{400, 404, 502}, handler: s.compareRuns},
		{Method: http.MethodGet, Path: testPath + "/cases", ID: "listCases",
			Summary: "A Test's JUnit cases over its last runs, most failures first, with flakiness.",
			In:      []any{casesParams{}}, Out: CaseList{}, Errors: []int{404, 502}, handler: s.listCases},
		{Method: http.MethodGet, Path: testPath + "/cases/history", ID: "caseHistory",
			Summary: "One case's result in each of the Test's runs, newest first.", In: []any{caseHistoryParams{}},
			Out: CaseHistory{}, Errors: []int{400, 404, 502}, handler: s.caseHistory},
		{Method: http.MethodGet, Path: runPath + "/compare", ID: "compareRun",
			Summary: "A run's test cases against another run of the same Test (with=); without it, the runs to pick from.",
			In:      []any{compareParams{}}, Out: RunCaseDiff{}, Errors: []int{404, 502}, handler: s.compareRun},
		{Method: http.MethodGet, Path: testPath + "/cleanup", ID: "previewCleanup", Role: auth.RoleDeveloper,
			Summary: "The finished runs a cleanup by age (mode=older) or date range (mode=range) would delete, oldest first.",
			In:      []any{cleanupParams{}}, Out: CleanupPreview{}, Errors: []int{400, 403, 404, 502}, handler: s.previewCleanup},
		{Method: http.MethodPost, Path: testPath + "/cleanup", ID: "cleanup", Role: auth.RoleAdmin,
			Summary: "Delete these finished runs of the Test (a preview's matches) with their logs and artifacts.",
			In:      []any{testParams{}, CleanupRequest{}}, Out: CleanupResult{}, Errors: []int{400, 403, 404, 502},
			handler: s.cleanup},
		{Method: http.MethodGet, Path: testPath + "/runs", ID: "listTestRuns",
			Summary: "One page of a Test's runs, newest first.", In: []any{runsParams{}},
			Out: RunPage{}, Errors: []int{404, 502}, handler: s.listTestRuns},
		{Method: http.MethodPost, Path: testPath + "/runs", ID: "startRun", Role: auth.RoleDeveloper,
			Summary: "Start a run of the Test, now or at notBefore.", In: []any{testParams{}, RunRequest{}},
			Out: StartedRun{}, Status: http.StatusCreated, Errors: []int{400, 403, 404, 502}, handler: s.startRun},
		{Method: http.MethodGet, Path: runPath, ID: "getRun",
			Summary: "A run with its steps, children, artifacts, events and failed cases.", In: []any{runParams{}},
			Out: RunDetail{}, Errors: []int{404, 502}, handler: s.getRun},
		{Method: http.MethodGet, Path: runPath + "/log", ID: "getRunLog",
			Summary: "The run's log from offset on, as text.",
			In:      []any{logParams{}}, Out: LogChunk{}, Errors: []int{404, 502}, handler: s.runLog},
		{Method: http.MethodPost, Path: runPath + "/abort", ID: "abortRun", Role: auth.RoleDeveloper,
			Summary: "Ask a run that hasn't finished to stop.", In: []any{runParams{}, AbortRequest{}},
			Status: http.StatusNoContent, Errors: []int{400, 403, 404, 409, 502}, handler: s.abortRun},
		{Method: http.MethodPost, Path: runPath + "/rerun", ID: "rerun", Role: auth.RoleDeveloper,
			Summary: "Start the run's Test again with the parameters this run used.", In: []any{runParams{}},
			Out: StartedRun{}, Status: http.StatusCreated, Errors: []int{403, 404, 409, 502}, handler: s.rerun},
		{Method: http.MethodPut, Path: runPath + "/comment", ID: "setComment", Role: auth.RoleDeveloper,
			Summary: "Set a finished run's comment.", In: []any{runParams{}, CommentUpdate{}},
			Out: RunComment{}, Errors: []int{400, 403, 404, 409, 502}, handler: s.setComment},
		{Method: http.MethodDelete, Path: runPath + "/comment", ID: "deleteComment", Role: auth.RoleDeveloper,
			Summary: "Remove a finished run's comment.", In: []any{runParams{}},
			Status: http.StatusNoContent, Errors: []int{403, 404, 409, 502}, handler: s.deleteComment},
		{Method: http.MethodDelete, Path: runPath, ID: "deleteRun", Role: auth.RoleAdmin,
			Summary: "Delete a finished run with its logs and artifacts.", In: []any{runParams{}},
			Status: http.StatusNoContent, Errors: []int{403, 404, 409, 502}, handler: s.deleteRun},
	}
	for i := range rs {
		rs[i].handler = h(rs[i].Role, rs[i].handler)
	}
	return rs
}

// OpenAPISpec describes the API for the UI's generated TypeScript client
// (web/src/api/schema.d.ts); openapi/control-center.json is this,
// zero-diff checked. Schemas are reflected from the Go types.
func OpenAPISpec() (*openapi31.Spec, error) {
	r := openapi31.NewReflector()
	r.Spec.Info.WithTitle("kubetest Control Center").WithVersion("v1").
		WithDescription("Control Center's backend for its UI. Signed-in identity comes from the " +
			"sign-in proxy's header; clusters are reached through the Kubernetes service proxy.")
	// Schemas by type name, without the package prefix.
	r.JSONSchemaReflector().DefaultOptions = append(r.JSONSchemaReflector().DefaultOptions,
		jsonschema.InterceptDefName(func(t reflect.Type, _ string) string { return t.Name() }))
	for _, rt := range (&Server{}).routes() {
		oc, err := r.NewOperationContext(rt.Method, rt.Path)
		if err != nil {
			return nil, err
		}
		oc.SetID(rt.ID)
		oc.SetSummary(rt.Summary)
		if rt.Role != auth.RoleViewer {
			oc.SetDescription("Requires the " + rt.Role.String() + " role.")
		}
		for _, in := range rt.In {
			oc.AddReqStructure(in)
		}
		status := rt.Status
		if status == 0 {
			status = http.StatusOK
		}
		oc.AddRespStructure(rt.Out, openapi.WithHTTPStatus(status))
		for _, code := range rt.Errors {
			body, ok := rt.ErrorBodies[code]
			if !ok {
				body = Error{}
			}
			oc.AddRespStructure(body, openapi.WithHTTPStatus(code))
		}
		if err := r.AddOperation(oc); err != nil {
			return nil, err
		}
	}
	return r.Spec, nil
}

// OpenAPIJSON is OpenAPISpec, indented.
func OpenAPIJSON() ([]byte, error) {
	spec, err := OpenAPISpec()
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	var v any // re-marshal for stable, indented output
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return json.MarshalIndent(v, "", "  ")
}
