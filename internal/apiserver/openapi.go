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
				"get": routeOp("List Tests in the configured namespace.",
					nil, jsonArrayOf("#/components/schemas/Test"), errorResp()),
				"post": routeOp(
					"Create a Test. The server sets app.kubernetes.io/managed-by=ui; "+
						"payloads spoofing any other value are rejected 400.",
					jsonRef("#/components/schemas/Test"), jsonRef("#/components/schemas/Test"), errorResp()),
			},
			"/tests/{name}": map[string]any{
				"get":    routeOp("Get a Test by name.", nil, jsonRef("#/components/schemas/Test"), errorResp()),
				"patch":  routeOp("Merge-patch a Test. Blocked 409 for managed-by!=ui (§7).", jsonRef("#/components/schemas/Test"), jsonRef("#/components/schemas/Test"), errorResp()),
				"delete": routeOp("Delete a Test. Blocked 409 for managed-by!=ui (§7).", nil, nil, errorResp()),
				"parameters": []any{
					pathParam("name", "Test name."),
				},
			},
			"/runs": map[string]any{
				"post": routeOp(
					"Create a TestRun. spec.source is set server-side to \"ui\" — "+
						"payload override rejected 400. Allowed regardless of the "+
						"referenced Test's managed-by label (§7 lets GUI trigger runs "+
						"on gitops-owned Tests).",
					jsonRef("#/components/schemas/TestRun"), jsonRef("#/components/schemas/TestRun"), errorResp()),
				"get": routeOp(
					"Merged list of active (cluster) and archived (store) runs, "+
						"deduped by UID, sorted by startedAt DESC.",
					nil, jsonArrayOf("#/components/schemas/RunEnvelope"), errorResp()),
			},
			"/runs/{id}": map[string]any{
				"get": routeOp(
					"Get a single run by CR name (active) or UID (archived).",
					nil, jsonRef("#/components/schemas/RunEnvelope"), errorResp()),
				"parameters": []any{pathParam("id", "TestRun name (cluster) or UID (archive).")},
			},
			"/runs/{id}/logs": map[string]any{
				"get": map[string]any{
					"summary": "Stream logs over WebSocket. Live runs poll the MinIO " +
						"chunk prefix every ~1s; archived runs stream all chunks then " +
						"close. Binary WS frames = raw log bytes.",
					"responses": map[string]any{
						"101": map[string]any{"description": "Switching Protocols (WebSocket)."},
						"503": map[string]any{"description": "Log storage not configured."},
					},
				},
				"parameters": []any{pathParam("id", "TestRun name or UID.")},
			},
			"/runs/{id}/artifacts/{path}": map[string]any{
				"get": map[string]any{
					"summary": "Presigned MinIO URL for an artifact. Path traversal (../, absolute paths, backslashes) rejected 400.",
					"responses": map[string]any{
						"200": jsonResponse(map[string]any{
							"type":     "object",
							"required": []string{"url", "expiresIn"},
							"properties": map[string]any{
								"url":       map[string]any{"type": "string", "format": "uri"},
								"expiresIn": map[string]any{"type": "integer"},
							},
						}),
						"400": errorSchema(),
						"503": errorSchema(),
					},
				},
				"parameters": []any{
					pathParam("id", "TestRun name or UID."),
					pathParam("path", "Relative artifact path (e.g. results/junit.xml)."),
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
