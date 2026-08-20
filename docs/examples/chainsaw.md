# chainsaw — Kyverno's k8s testing framework (example-only)

Chainsaw runs `apply → assert → error → delete` cycles against a real
Kubernetes cluster. That's a fundamentally different execution model
from every other tool in the catalog: the "test target" is the cluster
the operator itself runs in.

We ship no template because:

1. Chainsaw needs cluster access — a ServiceAccount + RBAC role that
   the OPERATOR grants per-Test. There's no way to encode "give this
   Test list/get/patch on Deployments in namespace X" in a shared
   template without leaking the RBAC decision.
2. Chainsaw exit-code is honest (verified as of chainsaw 0.2.x) — no
   verdict-processor mitigation needed, so the template would be
   little more than pinning the image tag.
3. The "cluster manifests to test" content is usually already in the
   caller's git repo — templates don't reduce boilerplate here.

## Reference raw Test

```yaml
apiVersion: tests.kubetest.io/v1alpha1
kind: Test
metadata:
  name: rollout-chainsaw
  namespace: default
  labels:
    kubetest.io/tool: chainsaw
spec:
  container:
    image: ghcr.io/kyverno/chainsaw:v0.2.12
    command: ["chainsaw"]
    args:
      - "test"
      - "/data/repo/chainsaw-tests"
      - "--report-format"
      - "JUNIT"
      - "--report-path"
      - "/data/repo/results/"
  content:
    git:
      uri: https://github.com/example/rollout-manifests.git
      revision: main
  pod:
    # Grant this Test the RBAC it needs. Users MUST create the
    # ServiceAccount + RoleBinding themselves — the operator doesn't
    # auto-provision it (would violate least-privilege).
    serviceAccountName: chainsaw-runner
  artifacts:
    paths:
      - "results/**/*.xml"
```

## Companion RBAC (once, per namespace)

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: chainsaw-runner
  namespace: default
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: chainsaw-runner
  namespace: default
rules:
  # Scope to exactly what your chainsaw suite touches — don't grant '*'.
  - apiGroups: ["apps"]
    resources: ["deployments"]
    verbs: ["get", "list", "create", "patch", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: chainsaw-runner
  namespace: default
subjects:
  - kind: ServiceAccount
    name: chainsaw-runner
roleRef:
  kind: Role
  name: chainsaw-runner
  apiGroup: rbac.authorization.k8s.io
```

## When a template WOULD make sense

If a cluster consistently runs chainsaw against the same resource kinds
in the same namespace, a project-local (not catalog-shipped)
TestTemplate encoding those defaults is fine — keep it in the same
namespace as your Tests, not in the shared config/templates/ pool.
