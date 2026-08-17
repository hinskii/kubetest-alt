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

package controller

// FinalizerName gates deletion of a TestRun so the controller can synchronously
// tear down the Job (and, later, its log stream) before k8s reaps the object.
// See CLAUDE.md §15.5: "delete during running = kill Job + cleanup MinIO
// stream + then remove finalizer".
const FinalizerName = "kubetest.io/testrun-finalizer"
