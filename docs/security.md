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
- Each proxied request ends after 15 s. An open event stream would keep
  the tool alive — k6 does not exit while a dashboard client is connected
  — so watching a run must not hold it at running; the browser's
  EventSource reconnects by itself and k6 resends its state.

## API token (fixes.md #1 — closed)

The API server is a ClusterIP Service: any pod in the cluster can open a
connection to it, not only the Kubernetes service proxy. So every request
but `/healthz`, `/readyz` and `/metrics` must carry the API token in
`X-Kubetest-Token` (`--auth-token-file`, compared in constant time); a
missing or wrong one is a 401. Holding the token is what lets a client in,
and because only token holders get through, `X-Kubetest-User`
attribution is as trustworthy as they are (Control Center sets it from
the signed-in email).

- The chart generates the token once (`<release>-kubetest-alt-api-token`,
  key `token`, 48 random characters) and keeps it across upgrades, or uses
  `apiserver.auth.existingSecret`. The API server refuses to start with a
  token under 32 characters; without `--auth-token-file` it runs open and
  says so — development only.
- Control Center of the same release mounts the Secret for its `local`
  cluster; for other clusters, copy each cluster's token into a Secret
  next to Control Center and list it in `controlCenter.apiTokenSecrets`
  (docs/control-center.md).
- Anyone else — scripts, CI — reads it from the Secret, so who may call
  the API is who may `get` that Secret (Kubernetes RBAC):
  `kubectl -n kubetest-alt get secret <release>-kubetest-alt-api-token -o jsonpath='{.data.token}' | base64 -d`.
- **Rotation:** replace the Secret's `token` and restart the API server
  and Control Center (`kubectl rollout restart`).

## Test-pod policy (fixes.md #1 — closed)

`spec.pod` and `spec.container` pass through to the test pod (CLAUDE.md
§8), so whoever can create a Test or TestRun — through the API or with
kubectl — could otherwise pick any service account, mount the node's disk
or run privileged. The platform's policy (chart `testPods`):

- **service accounts:** `default`, plus `testPods.allowedServiceAccounts`
  (e.g. one bound to a Google service account for GCS);
- **volumes:** emptyDir, configMap, secret, projected, downwardAPI,
  persistentVolumeClaim, ephemeral, csi; `hostPath` only with
  `testPods.allowHostPath`; anything else (nfs, iscsi, …) never;
- **containers:** no `privileged`, no capabilities beyond Pod Security
  Standards *baseline*.

The operator checks every run's resolved spec — templates and the
TestRun's pod override included — before a pod exists: a violation ends
the run with `error`, reason `PolicyDenied`, and a message naming each
field. The admission webhooks refuse Tests and TestRuns that break it at
`kubectl apply` time (a template's pod settings are caught at run time).

## Remaining risks

- **One shared token.** It is a credential, not an identity per caller;
  the audit log records the email Control Center passes, not which token
  holder made a request.
- **The API server's ClusterRole is cluster-wide** (create Tests and
  TestRuns anywhere). A token holder can start runs in any namespace; set
  `apiserver.namespace` to confine it to one.
- **The policy covers what a Test can ask for, not everything a pod
  can do.** Running as root inside the container, for example, is allowed.
  For defence in depth, label test namespaces with Pod Security Admission
  `pod-security.kubernetes.io/enforce: baseline`.
- **NetworkPolicy** stays opt-in (`networkPolicy.enabled`); with the token
  it limits exposure further but is no longer what keeps the API closed.
