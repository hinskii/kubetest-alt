# Control Center

The web UI for kubetest: Tests and runs of one or more clusters, live
logs and live views, reports and artifacts, test cases and flaky tests,
analytics and run comparison, schedules, cleanup. It keeps **no state of
its own** — no database: everything it shows comes from each cluster's
kubetest API server (TestRuns, run history, object storage), and
everything it changes goes there.

## Install

Control Center is part of the chart, off by default. The usual setup:
Google sign-in by an oauth2-proxy sidecar, in the same cluster as
kubetest, behind an Ingress.

1. **OAuth client.** Google Cloud Console → APIs & Services →
   Credentials → Create credentials → OAuth client ID → Web application.
   Authorized redirect URI: `https://<your host>/oauth2/callback`.
2. **Secret** with the client secret and a cookie secret, in the
   release's namespace:

   ```sh
   kubectl -n kubetest-alt create secret generic cc-google \
     --from-literal=client-secret='<client secret>' \
     --from-literal=cookie-secret="$(openssl rand -base64 32 | tr -- '+/' '-_')"
   ```
3. **Values:**

   ```yaml
   controlCenter:
     enabled: true
     auth:
       google:
         clientID: "1234567890-abc.apps.googleusercontent.com"
         existingSecret: cc-google
         allowedEmails: [viewer@firma.pl]   # who may sign in
         allowedDomains: []                 # or whole domains: [firma.pl]
     rbac:
       admins: [jan.kowalski@firma.pl]      # always allowed to sign in
       developers: [anna.nowak@firma.pl]
     ingress:
       enabled: true
       className: nginx
       host: kubetest.firma.pl
       tls: [{secretName: kubetest-tls, hosts: [kubetest.firma.pl]}]
   ```

The chart refuses to render a sign-in nobody could pass (no emails, no
domains) and a Google setup without a client or a Secret.

## Who can do what

Roles come from email addresses (`controlCenter.rbac`):

| Role | Can |
|---|---|
| viewer — anyone else who signed in | browse Tests and runs, logs, reports, artifacts, test cases, analytics, schedules |
| developer | + start runs (now or at a time), run again, abort, comment, set a GUI-managed Test's schedule, preview cleanups |
| admin | + delete runs, clean up runs |

Who may sign in at all is `allowedEmails` + `allowedDomains` + both rbac
lists. Tests applied from Git (no `app.kubernetes.io/managed-by: ui`)
stay read-only in the UI — runs are always allowed (CLAUDE.md §7).

## How sign-in works

`auth.mode: google` (default) runs oauth2-proxy v7 as a sidecar.
Control Center listens on the pod's **localhost only**, so the proxy is the
only way in, and reads the signed-in email from `X-Forwarded-Email` — the
header oauth2-proxy sets itself and strips from client requests (checked:
it does not strip `X-Auth-Request-Email`, which is why the sidecar setup
never reads that one). Cookies are `Secure` and `SameSite=Lax`, so the
host must be HTTPS. Set `auth.oauth2Proxy.trustedProxyCIDRs` to your
ingress controller's pod range so only it may set `X-Forwarded-For/Proto`.
Other providers (Azure, GitHub, any OIDC): `auth.oauth2Proxy.extraArgs`
(they override `--provider` and friends).

`auth.mode: external` leaves sign-in to you, e.g. oauth2-proxy behind an
ingress-nginx `auth-url` / `auth-response-headers` setup. Control Center
then listens on `:8080` and trusts `auth.emailHeader`
(`X-Auth-Request-Email` by default). **Whatever sits in front must strip
that header from client requests, and nothing else may reach the pod** —
whoever can send that header is whoever they claim to be.

## Clusters

With `controlCenter.clusters` empty, Control Center manages the kubetest
of its own release, as `local`. It reaches the API server through the
Kubernetes API's service proxy; the chart gives it exactly that (a Role
with `services/proxy` on that one Service).

More clusters — e.g. one Control Center for dev, stage and prod GKE
clusters:

```yaml
controlCenter:
  clusters:
    - name: dev
      displayName: Development
      auth: {type: serviceaccount}            # the cluster it runs in
      apiServer: {namespace: kubetest-alt, service: kt-kubetest-alt-apiserver, port: 8080}
    - name: prod
      displayName: Production
      auth: {type: gcp}                       # Workload Identity
      server: https://34.118.0.10             # the GKE control plane
      caFile: /etc/control-center/ca-prod.pem
      apiServer:
        namespace: kubetest-alt
        service: kt-kubetest-alt-apiserver
        port: 8080
        tokenFile: /var/run/kubetest/tokens/prod/token
  apiTokenSecrets:
    prod: kubetest-prod-api-token   # a copy of prod's <release>-kubetest-alt-api-token
  caBundles:
    prod: |
      -----BEGIN CERTIFICATE-----
      ...
      -----END CERTIFICATE-----
serviceAccounts:
  controlCenter:
    annotations:
      iam.gke.io/gcp-service-account: kubetest-cc@PROJECT.iam.gserviceaccount.com
```

Every kubetest API requires its token (docs/security.md): copy each
remote cluster's `<release>-kubetest-alt-api-token` Secret into Control
Center's namespace and list it in `apiTokenSecrets`. The `local` cluster's
is mounted automatically; list `local` explicitly and give it
`tokenFile: /var/run/kubetest/api-token/token`.

For a `gcp` cluster, the Google service account needs access to that
cluster (`roles/container.clusterViewer`) and, in it, Kubernetes RBAC for
`services/proxy` on the kubetest API server Service — the same Role the
chart creates locally, bound to the Google identity.

## Live view

A running run of a Test with `spec.liveView` (k6 in the catalog) shows the
tool's dashboard on its page. It is served under `/live/<signed link>/`,
sandboxed, without the user's session (docs/security.md). The chart sets
the pieces it needs: oauth2-proxy lets `GET /live/` through
(`--skip-auth-route`), and a Secret holds the key signing the links
(`controlCenter.liveView.existingSecret` to bring your own), shared by all
replicas.

## Run page: what Kubernetes says

A run that isn't finished shows its newest Kubernetes event under the
status — "Now: Pulling — Pulling image …", "FailedScheduling — 0/3 nodes
are available …" — which says why a run sits in `queued`. The page reloads
when the phase changes, and every 15 s while queued. Below the log, the
events of the run's Job and pods (`GET /runs/{id}/events`); Kubernetes
keeps them for about an hour, so older runs have none. A composite run
lists its child runs, each linked.

## Operations

- Port `9090` (Service port `ops`): `/healthz`, `/readyz` and `/metrics`,
  without sign-in — for kubelet probes and Prometheus. The UI is on the
  Service's `http` port only.
- Metrics: `controlcenter_http_requests_total{code,method}`,
  `controlcenter_cluster_up{cluster}` (1 when the cluster's API server
  answered the last probe).
- With `networkPolicy.enabled`, admit the Kubernetes control plane's
  address range in `networkPolicy.apiServerProxyCIDRs`, or Control Center
  can't reach the API server (its traffic arrives through the service
  proxy, from the control plane, not from a pod).

## Troubleshooting

- **A cluster card says "unreachable"**: the message is the API server's
  or the proxy's answer — RBAC (`services/proxy`), the Service name/port in
  `apiServer`, or a NetworkPolicy (see above).
- **Sign-in loops back to Google**: the host isn't HTTPS (cookies are
  `Secure`), or the redirect URI in the OAuth client doesn't match
  `https://<ingress.host>/oauth2/callback`.
- **"403 Forbidden" after signing in**: the address is not in
  `allowedEmails`, `allowedDomains` or the rbac lists.
- **The live view stays on "Waiting for the run to start…"**: the run is
  queued; it starts when the pod runs. "Live view unavailable" with a
  connection error: the tool doesn't listen on the pod IP, or a
  NetworkPolicy in the test namespace blocks the API server.
