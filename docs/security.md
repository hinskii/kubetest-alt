# Security model and known risks

## Access to the kubetest API server

The API server has no authentication of its own. It is a ClusterIP
Service with no Ingress; Control Center reaches it through the Kubernetes
API service proxy (`services/proxy`), so who may call it is decided by
Kubernetes RBAC on that one Service. `X-Kubetest-User` (run creator,
abort requester, audit actor) is attribution, not authentication: anyone
who can reach the Service can set it.

## Artifacts in Control Center

Artifacts are test output written by test code, so Control Center never
serves them as its own pages: every artifact response carries
`Content-Security-Policy: sandbox allow-scripts`. Scripts run — HTML
reports (JMeter, Gatling, Playwright, k6) need them — but in a sandbox
without `allow-same-origin`, which gives the document an opaque origin:
it can't read Control Center's cookies, storage or pages, submit forms,
open popups or navigate the top window. Requests it makes to Control
Center count as cross-site: they carry no session cookie as long as
oauth2-proxy's cookie isn't `SameSite=None` (its default sets none, which
browsers treat as Lax), and its POSTs fail the cross-origin check. Downloads (`?download=1`) are served as attachments.

## Known risk (fixes.md #1) — deferred

Decision (2026-10-06): recorded, to be closed before a production rollout.

- **Anything that can reach the Service can use the API.** Pods in the
  cluster can reach a ClusterIP Service directly, not only through the
  service proxy. The chart's NetworkPolicy (`networkPolicy.enabled`) is
  off by default, and when on it admits every pod in the release
  namespace.
- **`spec.pod` is passed through unchecked.** A caller who can create a
  Test can run a pod with any `serviceAccountName` and any volume,
  including `hostPath`, in any namespace the API server can write to.
  The API server's ClusterRole lets it create Tests and TestRuns
  cluster-wide.

Mitigations available today, without code changes:

- enable the NetworkPolicy and restrict its ingress to the control-plane
  range (the source of service-proxy traffic on your platform);
- label namespaces that run tests with Pod Security Admission
  `pod-security.kubernetes.io/enforce: baseline` (blocks `hostPath`,
  privileged pods, host namespaces);
- run the API server namespaced (`apiserver.namespace`) so it can only
  write to one namespace.

Planned fix: NetworkPolicy on by default; an allowlist for
`serviceAccountName` and volume types in the Test webhook.
