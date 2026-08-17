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

package test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// TestManagerConstructsWithCoreScheme is the step-01 smoke test: the manager
// wires up against envtest with a scheme containing corev1 + batchv1 (the
// resources the compiler/controller will need in step 03+), and exposes a
// non-nil client. No CRDs yet — that lands in step 02.
func TestManagerConstructsWithCoreScheme(t *testing.T) {
	cfg, _ := Start(t, EnvtestOptions{})

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
		HealthProbeBindAddress: "0",
		LeaderElection:         false,
	})
	require.NoError(t, err, "manager should construct against envtest")
	assert.NotNil(t, mgr.GetClient(), "manager client must be non-nil")
	assert.NotNil(t, mgr.GetScheme(), "manager scheme must be non-nil")
}
