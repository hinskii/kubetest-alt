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

import "encoding/json"

// OpenAPISpec describes the API for the UI's generated TypeScript client
// (web/src/api/schema.d.ts); openapi/control-center.json is this,
// zero-diff checked.
func OpenAPISpec() map[string]any {
	const typ, errField = "type", "error"
	scalar := func(t string) map[string]any { return map[string]any{typ: t} }
	str, boolean := scalar("string"), scalar("boolean")
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	object := func(required []string, props map[string]any) map[string]any {
		return map[string]any{typ: "object", "required": required, "properties": props}
	}
	ok := func(desc string, schema map[string]any) map[string]any {
		return map[string]any{"description": desc,
			"content": map[string]any{"application/json": map[string]any{"schema": schema}}}
	}
	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":   "kubetest Control Center",
			"version": "v1",
			"description": "Control Center's backend for its UI. Signed-in identity comes from the " +
				"sign-in proxy's header; clusters are reached through the Kubernetes service proxy.",
		},
		"paths": map[string]any{
			"/api/v1/me": map[string]any{"get": map[string]any{
				"operationId": "getMe", "summary": "The signed-in user and what their role allows.",
				"responses": map[string]any{"200": ok("The user.", ref("Me"))},
			}},
			"/api/v1/clusters": map[string]any{"get": map[string]any{
				"operationId": "listClusters", "summary": "Configured clusters, each probed now.",
				"responses": map[string]any{"200": ok("The clusters, in config order.",
					map[string]any{typ: "array", "items": ref("Cluster")})},
			}},
		},
		"components": map[string]any{"schemas": map[string]any{
			"Me": object([]string{"email", "role", "can"}, map[string]any{
				"email": str,
				"role":  map[string]any{typ: "string", "enum": []string{"viewer", "developer", "admin"}},
				"can":   ref("Capabilities"),
			}),
			"Capabilities": object([]string{"run", "delete"}, map[string]any{"run": boolean, "delete": boolean}),
			"Cluster": object([]string{"name", "displayName", "reachable"},
				map[string]any{"name": str, "displayName": str, "reachable": boolean, "error": str}),
			"Error": object([]string{errField}, map[string]any{errField: str}),
		}},
	}
}

// OpenAPIJSON is OpenAPISpec, indented.
func OpenAPIJSON() ([]byte, error) { return json.MarshalIndent(OpenAPISpec(), "", "  ") }
