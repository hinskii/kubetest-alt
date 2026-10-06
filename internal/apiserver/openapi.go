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

// OpenAPI spec: hand-written struct is the source of truth. openapi/openapi.json
// on disk is the committed artifact. `make openapi` regenerates the disk file;
// CI (`make openapi-check`) asserts zero diff.
//
// Rationale for not auto-generating from route handlers: our handlers are
// small (~10 endpoints) and their schemas cross package boundaries
// (testsv1alpha1.Test, store.Row). Codegen tools like swaggo/swag would
// need annotation churn on every handler. A code-driven struct captures
// exactly what we want to document — routes + reason envelope + auth
// story — without adding a build-time dep.
package apiserver

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"

	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// OpenAPISpec returns the full OpenAPI 3.1 document as a Go map. Serialized
// via json.MarshalIndent so the on-disk artifact has stable byte-for-byte
// output across Go versions.
func OpenAPISpec() map[string]any {
	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":   "kubetest-alt API",
			"version": "v1alpha1",
			"description": "Thin REST/WS API the GUI consumes. Reads flow through a " +
				"controller-runtime shared informer cache; writes go directly to the " +
				"k8s API server (§CLAUDE.md §7). §7 managed-by policy is enforced " +
				"here for PATCH/DELETE on Tests; TestRun creation is always allowed " +
				"regardless of the referenced Test's ownership.",
		},
		"paths": map[string]any{
			"/tests": map[string]any{
				"get": routeOp("List Tests in ?namespace= (all namespaces when omitted on a cluster-wide server).",
					nil, jsonArrayOf("#/components/schemas/Test"), errorResp()),
				"post": routeOp(
					"Create a Test. The server sets app.kubernetes.io/managed-by=ui; "+
						"payloads spoofing any other value are rejected 400.",
					jsonRef("#/components/schemas/Test"), jsonRef("#/components/schemas/Test"), errorResp()),
				"parameters": []any{namespaceParam()},
			},
			"/tests/{name}": map[string]any{
				"get":    routeOp("Get a Test by name.", nil, jsonRef("#/components/schemas/Test"), errorResp()),
				"patch":  routeOp("JSON merge patch (RFC 7396) of a Test: objects merge, arrays and scalars replace, null deletes. 409 unless managed-by=ui, including when the label is missing (§7); 400 if the patch touches managed-by.", jsonRef("#/components/schemas/Test"), jsonRef("#/components/schemas/Test"), errorResp()),
				"delete": routeOp("Delete a Test. Blocked 409 for managed-by!=ui (§7).", nil, nil, errorResp()),
				"parameters": []any{
					pathParam("name", "Test name."),
					namespaceParam(),
				},
			},
			"/tests/{name}/resolved": map[string]any{
				"get": map[string]any{
					"summary": "The Test merged with its TestTemplates (spec.use), for building run " +
						"forms: full parameter schema incl. template-only parameters, tool, GitOps lock. " +
						"Expressions are not evaluated. 422 when a referenced template is missing.",
					"responses": map[string]any{
						"200": jsonResponse(map[string]any{
							"type":     "object",
							"required": []string{"name", "namespace", "gitopsLocked", "spec"},
							"properties": map[string]any{
								"name":         map[string]any{"type": "string"},
								"namespace":    map[string]any{"type": "string"},
								"labels":       map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
								"tool":         map[string]any{"type": "string"},
								"gitopsLocked": map[string]any{"type": "boolean"},
								"templates":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
								"spec":         map[string]any{"type": "object", "description": "Merged TestSpec."},
							},
						}),
						"400": errorSchema(),
						"404": errorSchema(),
						"422": errorSchema(),
					},
				},
				"parameters": []any{pathParam("name", "Test name."), namespaceParam()},
			},
			"/runs": map[string]any{
				"post": routeOp(
					"Create a TestRun. spec.source is set server-side to \"ui\" — "+
						"payload override rejected 400. Allowed regardless of the "+
						"referenced Test's managed-by label (§7 lets GUI trigger runs "+
						"on gitops-owned Tests).",
					jsonRef("#/components/schemas/TestRun"), jsonRef("#/components/schemas/TestRun"), errorResp()),
				"get": map[string]any{
					"summary": "Live runs first (first page only), then finished runs newest-first " +
						"by (finishedAt, uid), keyset-paginated: pass X-Next-Cursor back as ?after=. " +
						"Cluster and archive are merged and deduped by UID (cluster wins).",
					"parameters": []any{
						queryParam("test", "Filter by Test name."),
						queryParam("phase", "Filter by phase."),
						queryParam("source", "Filter by source (ui, api, cli, cron, trigger, gitops)."),
						queryParam("limit", "Finished runs per page (default 50, max 500)."),
						queryParam("after", "Opaque cursor from the previous page's X-Next-Cursor."),
						queryParam("finishedAfter", "RFC 3339; only runs finished at or after it (excludes live runs)."),
					},
					"responses": map[string]any{
						"200": map[string]any{
							"description": "Runs.",
							"headers": map[string]any{HeaderNextCursor: map[string]any{
								"description": "Cursor for the next page; absent on the last page.",
								"schema":      map[string]any{"type": "string"},
							}},
							"content": map[string]any{"application/json": map[string]any{
								"schema": jsonArrayOf("#/components/schemas/RunEnvelope"),
							}},
						},
						"400": errorSchema(),
					},
				},
				"parameters": []any{namespaceParam()},
			},
			"/runs/{id}": map[string]any{
				"get": routeOp(
					"Get a single run by CR name (active) or UID (archived).",
					nil, jsonRef("#/components/schemas/RunEnvelope"), errorResp()),
				"delete": map[string]any{
					"summary": "Delete a finished run from history: its objects (logs, artifacts, " +
						"result.json), its CR if present, and its store row. 409 while the run is live " +
						"(abort it first). Idempotent per step; safe to retry.",
					"responses": map[string]any{
						"204": map[string]any{"description": "Deleted."},
						"400": errorSchema(),
						"404": errorSchema(),
						"409": errorSchema(),
						"503": errorSchema(),
					},
				},
				"parameters": []any{pathParam("id", "TestRun name (cluster) or UID (archive)."), namespaceParam()},
			},
			"/runs/{id}/abort": map[string]any{
				"post": map[string]any{
					"summary": "Abort a live run. Sets spec.abort (reason User, requestedBy from " +
						"the X-Kubetest-User header); the controller kills the Job, records the " +
						"run as aborted, persists it and fires webhooks. Idempotent. Allowed on " +
						"runs of GitOps-managed Tests (runs are not definitions, §7).",
					"requestBody": map[string]any{
						"required": false,
						"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"message": map[string]any{"type": "string", "maxLength": maxAbortMessageLen},
							},
						}}},
					},
					"responses": map[string]any{
						"202": jsonResponse(jsonRef("#/components/schemas/RunEnvelope")),
						"400": errorSchema(),
						"404": errorSchema(),
						"409": errorSchema(),
					},
				},
				"parameters": []any{pathParam("id", "TestRun name."), namespaceParam()},
			},
			"/runs/{id}/comment": map[string]any{
				"put": map[string]any{
					"summary": "Set (replace) the comment of a finished run. The comment lives on the " +
						"run-history row and is deleted with the run. Author from X-Kubetest-User.",
					"requestBody": map[string]any{
						"required": true,
						"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{
							"type":     "object",
							"required": []string{"text"},
							"properties": map[string]any{
								"text": map[string]any{"type": "string", "minLength": 1, "maxLength": apiclient.MaxCommentLen},
							},
						}}},
					},
					"responses": map[string]any{
						"200": jsonResponse(jsonRef("#/components/schemas/Comment")),
						"400": errorSchema(),
						"404": errorSchema(),
						"409": map[string]any{"description": "Run still live, or not in run history yet."},
						"503": map[string]any{"description": "Run history store not configured."},
					},
				},
				"delete": map[string]any{
					"summary": "Remove the comment of a finished run. Idempotent.",
					"responses": map[string]any{
						"204": map[string]any{"description": "Removed (or there was none)."},
						"404": errorSchema(),
						"409": map[string]any{"description": "Run still live."},
						"503": map[string]any{"description": "Run history store not configured."},
					},
				},
				"parameters": []any{pathParam("id", "TestRun name or UID."), namespaceParam()},
			},
			"/audit": map[string]any{
				"get": map[string]any{
					"summary": "User actions recorded by this API server, newest first: runs created, " +
						"aborted, deleted and commented; Tests created, updated, deleted. Actor from " +
						"X-Kubetest-User. Paging: pass X-Next-Cursor as ?before=.",
					"parameters": []any{
						namespaceParam(),
						queryParam("actor", "Only this actor."),
						queryParam("action", "Only this action, e.g. run.delete."),
						queryParam("before", "Entries with an id lower than this (X-Next-Cursor)."),
						queryParam("limit", "Page size (default 50, max 500)."),
					},
					"responses": map[string]any{
						"200": jsonResponse(jsonArrayOf("#/components/schemas/AuditEntry")),
						"400": errorSchema(),
						"503": map[string]any{"description": "Run history store not configured."},
					},
				},
			},
			"/runs/{id}/logs": map[string]any{
				"get": map[string]any{
					"summary": "Stream logs over WebSocket. Live runs poll object storage " +
						"chunk prefix every ~1s; archived runs stream all chunks then " +
						"close. Binary WS frames = raw log bytes.",
					"responses": map[string]any{
						"101": map[string]any{"description": "Switching Protocols (WebSocket)."},
						"400": map[string]any{"description": "Namespace missing or outside the server's scope."},
						"404": map[string]any{"description": "Run not found in the cluster or the archive."},
						"503": map[string]any{"description": "Log storage not configured."},
					},
				},
				"parameters": []any{pathParam("id", "TestRun name or UID."), namespaceParam()},
			},
			"/runs/{id}/logs.txt": map[string]any{
				"get": map[string]any{
					"summary": "The run's whole stored log as text/plain (live runs: what has been " +
						"flushed so far, no follow). ?download=1 adds Content-Disposition: attachment.",
					"responses": map[string]any{
						"200": map[string]any{"description": "Log text.", "content": map[string]any{
							"text/plain": map[string]any{"schema": map[string]any{"type": "string"}}}},
						"400": errorSchema(),
						"404": errorSchema(),
						"503": errorSchema(),
					},
				},
				"parameters": []any{pathParam("id", "TestRun name or UID."), namespaceParam()},
			},
			"/runs/{id}/artifacts": map[string]any{
				"get": map[string]any{
					"summary": "List the run's artifacts (recorded refs; falls back to listing the " +
						"run's artifacts/ prefix when none were recorded).",
					"responses": map[string]any{
						"200": jsonResponse(map[string]any{
							"type": "array",
							"items": map[string]any{
								"type":     "object",
								"required": []string{"path"},
								"properties": map[string]any{
									"path":        map[string]any{"type": "string"},
									"sizeBytes":   map[string]any{"type": "integer"},
									"contentType": map[string]any{"type": "string"},
								},
							},
						}),
						"400": errorSchema(),
						"404": errorSchema(),
					},
				},
				"parameters": []any{pathParam("id", "TestRun name or UID."), namespaceParam()},
			},
			"/runs/{id}/artifacts/{path}": map[string]any{
				"get": map[string]any{
					"summary": "Stream an artifact's bytes (default; served with nosniff + CSP sandbox, " +
						"?download=1 for attachment), or with ?presign=1 return a presigned object-store " +
						"URL. Path traversal (../, absolute paths, backslashes) rejected 400.",
					"responses": map[string]any{
						"200": map[string]any{
							"description": "Artifact bytes, or {url, expiresIn} with ?presign=1.",
							"content": map[string]any{
								"application/octet-stream": map[string]any{"schema": map[string]any{"type": "string", "format": "binary"}},
								"application/json": map[string]any{"schema": map[string]any{
									"type":     "object",
									"required": []string{"url", "expiresIn"},
									"properties": map[string]any{
										"url":       map[string]any{"type": "string", "format": "uri"},
										"expiresIn": map[string]any{"type": "integer"},
									},
								}},
							},
						},
						"400": errorSchema(),
						"404": errorSchema(),
						"503": errorSchema(),
					},
				},
				"parameters": []any{
					pathParam("id", "TestRun name or UID."),
					pathParam("path", "Relative artifact path (e.g. results/junit.xml)."),
					namespaceParam(),
				},
			},
			"/healthz": map[string]any{
				"get": map[string]any{
					"summary":   "Liveness probe.",
					"responses": map[string]any{"200": map[string]any{"description": "OK"}},
				},
			},
			"/openapi.json": map[string]any{
				"get": map[string]any{
					"summary":   "This spec.",
					"responses": map[string]any{"200": map[string]any{"description": "OpenAPI 3.1 spec."}},
				},
			},
		},
		"components": map[string]any{
			"schemas": map[string]any{
				// We deliberately reference these as opaque objects — full CRD
				// schemas are >1500 lines each; the GUI relies on the k8s API
				// docs for field-level detail. Keeps this spec browsable.
				"Test":    objectShape("A Test CRD object.", "spec", "metadata"),
				"TestRun": objectShape("A TestRun CRD object.", "spec", "metadata"),
				"RunEnvelope": map[string]any{
					"type":     "object",
					"required": []string{"uid", "name", "namespace", "testRef", "phase", "origin"},
					"properties": map[string]any{
						"uid":        map[string]any{"type": "string"},
						"name":       map[string]any{"type": "string"},
						"namespace":  map[string]any{"type": "string"},
						"testRef":    map[string]any{"type": "string"},
						"phase":      map[string]any{"type": "string"},
						"source":     map[string]any{"type": "string"},
						"queuedAt":   map[string]any{"type": "string", "format": "date-time"},
						"startedAt":  map[string]any{"type": "string", "format": "date-time"},
						"finishedAt": map[string]any{"type": "string", "format": "date-time"},
						"durationMs": map[string]any{"type": "integer"},
						"message":    map[string]any{"type": "string"},
						"origin": map[string]any{
							"type": "string", "enum": []string{"cluster", "archive"},
							"description": "Where the record came from — cluster runs are still mutable via kubectl; archive runs are read-only history.",
						},
						"tool":      map[string]any{"type": "string", "description": "kubetest.io/tool identity."},
						"parentRun": map[string]any{"type": "string", "description": "Composite parent run name, if any."},
						"tags":      map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
						"config": map[string]any{
							"type": "object", "additionalProperties": map[string]any{"type": "string"},
							"description": "Effective parameters: declared defaults overlaid with the run's overrides.",
						},
						"testCounts": map[string]any{"type": "object", "properties": map[string]any{
							"total": map[string]any{"type": "integer"}, "passed": map[string]any{"type": "integer"},
							"failed": map[string]any{"type": "integer"}, "skipped": map[string]any{"type": "integer"},
						}},
						"metrics": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "number"}},
						"steps": map[string]any{"type": "object", "additionalProperties": map[string]any{
							"type": "object", "properties": map[string]any{
								"phase":      map[string]any{"type": "string"},
								"startedAt":  map[string]any{"type": "string", "format": "date-time"},
								"finishedAt": map[string]any{"type": "string", "format": "date-time"},
								"message":    map[string]any{"type": "string"},
							},
						}},
						"abort": map[string]any{"type": "object", "properties": map[string]any{
							"reason":      map[string]any{"type": "string", "enum": []string{"User", "Concurrency", "Parent"}},
							"message":     map[string]any{"type": "string"},
							"requestedBy": map[string]any{"type": "string"},
						}},
						"notBefore": map[string]any{
							"type": "string", "format": "date-time",
							"description": "Scheduled start (spec.notBefore); the run waits in queued until then.",
						},
						"comment": jsonRef("#/components/schemas/Comment"),
					},
				},
				"Comment": map[string]any{
					"type":     "object",
					"required": []string{"text", "at"},
					"properties": map[string]any{
						"text": map[string]any{"type": "string"},
						"by":   map[string]any{"type": "string"},
						"at":   map[string]any{"type": "string", "format": "date-time"},
					},
				},
				"AuditEntry": map[string]any{
					"type":     "object",
					"required": []string{"id", "at", "action"},
					"properties": map[string]any{
						"id":        map[string]any{"type": "integer"},
						"at":        map[string]any{"type": "string", "format": "date-time"},
						"actor":     map[string]any{"type": "string"},
						"action":    map[string]any{"type": "string"},
						"namespace": map[string]any{"type": "string"},
						"target":    map[string]any{"type": "string"},
						"details":   map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
					},
				},
				"Error": map[string]any{
					"type":     "object",
					"required": []string{"message"},
					"properties": map[string]any{
						"reason": map[string]any{
							"type":        "string",
							"description": "Machine-readable classifier: NotFound, BadRequest, Conflict, ManagedByGitOps, Internal, ServiceUnavailable.",
						},
						"message": map[string]any{"type": "string"},
					},
				},
			},
		},
	}
}

// OpenAPIJSON returns the spec as pretty-printed JSON bytes. Deterministic
// (sorted map keys via json.Marshal — Go's encoding/json sorts map keys
// alphabetically by design).
func OpenAPIJSON() ([]byte, error) {
	return json.MarshalIndent(OpenAPISpec(), "", "  ")
}

// getOpenAPI serves the spec at /openapi.json.
func (s *Server) getOpenAPI(w http.ResponseWriter, _ *http.Request) {
	b, err := OpenAPIJSON()
	if err != nil {
		writeError(w, http.StatusInternalServerError, ReasonInternal, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(b)
}

// ---------------------------------------------------------------------------
// spec-building helpers — keep the spec itself concise above.
// ---------------------------------------------------------------------------

func routeOp(summary string, req, resp any, errResp map[string]any) map[string]any {
	op := map[string]any{
		"summary":   summary,
		"responses": defaultResponses(resp, errResp),
	}
	if req != nil {
		op["requestBody"] = map[string]any{
			"required": true,
			"content":  map[string]any{"application/json": map[string]any{"schema": req}},
		}
	}
	return op
}

func defaultResponses(success any, errResp map[string]any) map[string]any {
	r := map[string]any{}
	if success != nil {
		r["200"] = jsonResponse(success)
		// POST endpoints also return 201; documenting both keeps the spec honest.
		r["201"] = jsonResponse(success)
	} else {
		r["204"] = map[string]any{"description": "No content."}
	}
	// Merge in shared error responses.
	maps.Copy(r, errResp)
	// Deterministic marshalling of "responses" is not guaranteed by
	// encoding/json for nested maps' iteration order, but json.Marshal sorts
	// map keys — so the emitted JSON stays byte-stable.
	return r
}

func jsonResponse(schema any) map[string]any {
	return map[string]any{
		"description": "OK",
		"content":     map[string]any{"application/json": map[string]any{"schema": schema}},
	}
}

func jsonRef(ref string) map[string]any {
	return map[string]any{"$ref": ref}
}

func jsonArrayOf(itemRef string) map[string]any {
	return map[string]any{"type": "array", "items": jsonRef(itemRef)}
}

func pathParam(name, description string) map[string]any {
	return map[string]any{
		"name":        name,
		"in":          "path",
		"required":    true,
		"description": description,
		"schema":      map[string]any{"type": "string"},
	}
}

func queryParam(name, description string) map[string]any {
	return map[string]any{
		"name": name, "in": "query", "required": false,
		"description": description, "schema": map[string]any{"type": "string"},
	}
}

// namespaceParam documents ?namespace=. Required for single-object routes
// on a cluster-wide server; on a scoped server it may be omitted and must
// equal the server's namespace when given.
func namespaceParam() map[string]any {
	return map[string]any{
		"name":     QueryNamespace,
		"in":       "query",
		"required": false,
		"description": "Target namespace. Required for single-object requests when the " +
			"server runs cluster-wide; optional (and must match) when it is scoped. " +
			"On list endpoints, omitting it on a cluster-wide server lists all namespaces.",
		"schema": map[string]any{"type": "string"},
	}
}

func errorResp() map[string]any {
	return map[string]any{
		"400": errorSchema(),
		"404": errorSchema(),
		"409": errorSchema(),
		"500": errorSchema(),
	}
}

func errorSchema() map[string]any {
	return jsonResponse(jsonRef("#/components/schemas/Error"))
}

func objectShape(desc string, required ...string) map[string]any {
	slices.Sort(required)
	return map[string]any{
		"type":        "object",
		"description": desc,
		"required":    required,
	}
}
