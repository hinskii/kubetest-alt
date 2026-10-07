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

## Live view

A Test can declare its tool's live web UI (`spec.liveView`, k6's
dashboard in the catalog). That UI is HTML and JavaScript from the test
image — which the Test's author chooses — so it never runs as Control
Center:

- Control Center serves it under `/live/<token>/…` with
  `Content-Security-Policy: sandbox allow-scripts; frame-ancestors 'self'`.
  Without `allow-same-origin` the UI runs in an opaque origin: no access to
  Control Center's cookies, storage or pages, no forms, no popups, no top
  navigation. The run page frames it with `sandbox="allow-scripts"` too.
- The UI's own requests (assets, its event stream) therefore carry no
  session. Access is a capability instead: a signed (HMAC-SHA256),
  expiring (12 h) token for one run's live view, minted when someone who
  may see the run opens its page. **oauth2-proxy must let these requests
  through:** `--skip-auth-route=GET=^/live/`. Without it the frame stays
  on oauth2-proxy's sign-in redirect.
- `CC_LIVE_VIEW_KEY` signs the tokens; set the same value on every replica
  (a Secret). Unset, each process picks a random key: links stop working on
  restart and across replicas.
- Responses send `Access-Control-Allow-Origin: *` (the opaque origin is
  `null`; no credentials are involved), `Referrer-Policy: no-referrer` (the
  token is in the URL) and never a cookie.
- The kubetest API server proxies `GET /runs/{id}/live/…` only to the run's
  own pod — the IP the operator wrote to `status.podIP` — on the declared
  port, only while the run is running, dropping cookies, `Authorization`
  and the attribution header. It needs no pod RBAC. A NetworkPolicy in a
  test namespace must admit the API server on that port.

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
