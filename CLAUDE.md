# CLAUDE.md — In-House Kubernetes-Native Test Execution Platform ("Kubetest-alt")

> Project context/spec for AI-assisted development. Terse, concrete, code-example-heavy. Author: senior platform/SRE engineer (Go, kubebuilder/controller-runtime, Istio, ArgoCD). Informed by a deep read of Testkube (github.com/kubeshop/testkube, testkube-operator, classic testkube-executor-* repos) + docs.testkube.io. Each "Lessons from Testkube" note is tagged **[SRC]** (verified in source code), **[DOC]** (docs only), or **[INFER]** (inferred).

## 0. Project Overview & Goals

Build an open-source-style in-house alternative to Testkube's OSS agent — but with the GUI/dashboard that Testkube keeps behind its paid Control Plane.

**Non-negotiables:**
- **CRDs are the single source of truth.** Both ArgoCD (GitOps) and our own GUI create/patch the *same* CRs. No separate database-of-record for definitions.
- **Operator in Go** with kubebuilder/controller-runtime.
- **Thin API server** for the GUI (read models + mutating actions that write CRs). GUI never holds private state that isn't reconstructable from cluster + Postgres run history.
- **Postgres** for run history/results (NOT Mongo — see §9).
- **MinIO/S3** for artifacts + logs.
- **Any containerized tool** — a Test is an image + command. Verdict = process exit code + a small set of declarative `verdictFrom` processors (JUnit, JTL) for tools whose exit codes lie. Curated `TestTemplate` catalog names the tools (`kubetest.io/tool` label); the operator itself is tool-agnostic.
- Multi-namespace, GitOps-compatible, Istio-aware.
- **No workload-level hacks for infra concerns.** The CRD exposes a fully generic pod annotations/labels passthrough (see §8) — the platform hardcodes NO specific annotations, and infra behavior (e.g. mesh) is never handled via in-wrapper hacks like quitquitquit.

**Core CRDs (our design):** `Test` (definition), `TestRun` (execution), `TestTemplate` (reusable building blocks), `TestTrigger` (k8s-event triggering). Keep the surface small.

**Operational safety rules (Claude Code):**
- Destructive filesystem/git operations (`rm -rf`, `git reset --hard`, `git clean -f[d]`, `git push --force`, `git branch -D`) ALWAYS require explicit confirmation from the user, with the full resolved absolute path shown in the request. `.claude/settings.local.json` mirrors this via `deny` rules — if a session ever gets past those, the code path still stops here.
- Never delete `.git` at the workspace root. If a zero-diff check needs a scratch git repo, use a temp dir OUTSIDE the workspace (e.g. `mktemp -d`) and rsync the tree — never `git init` inside the workspace and then remove `.git`.
- After each step's gates pass, commit to git AND push to remote. History-of-steps is the point; a workspace with 6 uncommitted steps is one `rm` away from a full rewind.

---

## 1. Testkube Architecture: OSS Agent vs Paid Control Plane

**The split (know this before copying anything):**

- Testkube = **Agent** (100% OSS, runs in your cluster) + **Control Plane** (commercial, holds the Dashboard). **[SRC]** `ARCHITECTURE.md`.
- OSS Agent "Standalone Mode": manages CRDs, schedules/executes TestWorkflows, collects logs/artifacts into its own storage, listens for k8s events, emits webhooks. **No dashboard** — CLI/REST only. **[SRC]** ARCHITECTURE.md + feature-comparison table.
- "Connected Mode" was introduced in **Testkube 2.7.0** ("An improved resource management architecture and a new GitOps Agent"); the agent splits into capability-agents — **Runner, Listener, GitOps, Webhook** — and resources are stored in the Control Plane, not as CRDs. **[DOC]** (agents-overview).
- **Dashboard, RBAC/user mgmt, SSO, SCIM, multi-agent/multi-cluster, concurrency & queueing, insights/flakiness, JUnit report viz, audit logs, AI features** are all Control-Plane-only. **[SRC]** feature-comparison table.
- **What's NOT functional in OSS even though execution works:** complex orchestration via `execute` (test suites), `parallel` parallelization, `matrix`/sharding, `services` sidecars, and `concurrency` policies are documented as Control-Plane-gated in Standalone. **[DOC]** (The TestWorkflow *schema* still contains these fields; they're just not honored standalone.)

**Agent internals (in-cluster):** API server (REST 8088 + gRPC 8089), controller-runtime controllers watching CRDs, TestWorkflow execution runtime, storage layer (Mongo/Postgres + MinIO + NATS). **[SRC]** ARCHITECTURE.md + subagent verification of ports.

**Agent↔Control-Plane comms:** gRPC (agent dials out) + websockets; NGINX ingress needs gRPC+websocket annotations. **[DOC]**

**Licensing history:** Repo is **dual-licensed: MIT + Testkube Community License (TCL)**, declared per-file in headers. Core execution/agent = MIT; commercial-gated features embedded in the agent = TCL. **[SRC]** `LICENSE` + licensing FAQ. The dashboard was never in the current OSS agent repo — it lives in the Control Plane. Classic `Test`/`TestSuite`/`Executor` CRDs were deprecated **"as of the December 2025 release of both the Testkube Control Plane and the Open Source Agent"** in favor of TestWorkflows. **[DOC]** (crds/executor.testkube.io-v1).

> **Lesson → our design:** Collapse Testkube's Agent/Control-Plane split. One operator + one thin API + one GUI, all reading CRDs. Everything MIT (or Apache-2.0). No feature gating.

---

## 2. Testkube CRD Inventory

**Current (TestWorkflow era), group `testworkflows.testkube.io/v1`:** **[SRC]** ARCHITECTURE.md + generated CRD schema.
- `TestWorkflow` — definition: `content`, `container`, `pod`, `job`, `steps`, `setup`, `after`, `services`, `config`, `events` (cron), `use` (templates), `system`.
- `TestWorkflowTemplate` — reusable, parameterized (`config` → `ParameterSchema`), included via `use`.
- `TestWorkflowExecution` — an execution request/record; watched by a controller.

**Webhook group `executor.testkube.io/v1`:** `Webhook`, `WebhookTemplate`. **[SRC]**
**Trigger group `tests.testkube.io/v1`:** `TestTrigger`. **[SRC]**
**Deprecated (kept only to avoid deleting live resources):** `Test` (v1/v2/v3), `TestExecution`, `TestSource`, `TestSuite` (v1/v2/v3), `TestSuiteExecution`, `Executor`, `Template`, `Script` (v1/v2). **[SRC]** ARCHITECTURE.md.

### Classic `Test` CRD (tests.testkube.io/v3) — what to learn from
`spec.type` (e.g. `postman/collection`, `k6/script`, `cypress/project`, `curl/test`, `soapui/xml`), `spec.content` (`type: string|file-uri|git`, with `repository.{type,uri,branch,commit,path,username,token,...}`), `spec.executionRequest` (`args`, `variables`, `command`, `image`, `jobTemplate`, `activeDeadlineSeconds`, `artifactRequest`, cron schedule). **[SRC/DOC]** Note: variables files < 128KB are inlined on the CRD; larger uploaded to object storage. **[DOC]**

### TestWorkflow spec (deep) — the important one
Verified against generated CRD schema (`testworkflows.testkube.io/v1`). Key structures **[SRC]**:

- **`content`** (`Content`): `git` (`ContentGit`: `uri`, `revision`, `username`/`usernameFrom`, `token`/`tokenFrom`, `sshKey`/`sshKeyFrom`, `authType: basic|header`, `mountPath`, `paths[]` sparse checkout, `cone`), `files[]` (`ContentFile`: `path`, inline `content`, `contentFrom` secret/configMapKeyRef, `mode`), `tarball[]` (`ContentTarball`: `url`, `path`, `mount`). Git default mount `/data/repo`; shared `/data` emptyDir across steps.
- **`container`** (`ContainerConfig`, global defaults, per-step overridable): `workingDir`, `image`, `imagePullPolicy`, `env[]`, `envFrom[]`, `command[]`, `args[]`, `resources` (`requests`/`limits`, incl. `nvidia.com/gpu`), `securityContext`, `volumeMounts[]`.
- **`pod`** (`PodConfig`): `serviceAccountName`, `imagePullSecrets`, `nodeSelector`, `labels`, `annotations`, `volumes[]` (emptyDir/hostPath/secret/configMap/PVC), `affinity`, `tolerations`, `securityContext`, `topologySpreadConstraints`, DNS, priority — most of PodSpec.
- **`job`** (`JobConfig`): `labels`, `annotations`, `namespace` (per-workflow execution namespace!), `activeDeadlineSeconds`.
- **`steps[]`** (`Step`): `name`, `condition` (expr; default `passed`, artifacts default `always`), `pure` (merge flag), `negative` (expected to fail), `optional` (failure doesn't fail workflow), `paused` (interactive), `retry` (`RetryPolicy{count, until}`), `timeout` (e.g. `30m`), `delay`, `use[]`/`template`, `content`, `services{}`, `container`, `workingDir`, `setup[]`, `shell`, `run` (`StepRun`), `execute` (`StepExecute`), `artifacts` (`StepArtifacts`), `parallel` (`StepParallel`), `steps[]` (nesting).
- **`services{}`** (`ServiceSpec`): sidecar-like dependent services as separate pods — `image`, `env`, `readinessProbe`, `timeout`, `logs`, `matrix`/`shards`/`count`/`maxCount`, `restartPolicy`. Accessed via `services.<name>.<index>.ip`. **[DOC/SRC]**
- **`execute`** (`StepExecute`): `parallelism`, `async`, `tests[]`, `workflows[]` (`StepExecuteWorkflow` w/ `config`, `matrix`, `count`, `maxCount`, `shards`, `selector`). This is how "test suites" work now — a step running other workflows in parallel/sequence. **[SRC]**
- **`parallel`** (`StepParallel`): `matrix{}`, `count`/`maxCount`, `shards{}`, `transfer[]` (copy files in), `fetch[]` (pull results out), `logs`, nested `steps`. Each worker = its own Job/Pod. **[SRC/DOC]**
- **`matrix`/`shards`** (`StepExecuteStrategy`): `matrix` = cartesian combos (`matrix.<key>`), `shards` = split data across `count`/`maxCount` instances (`shard.<key>`); combinable. **[DOC]**
- **`artifacts`** (`StepArtifacts`): `workingDir`, `paths[]` (glob via doublestar), `compress{name}`. **[SRC/DOC]**
- **`config`** (`map[string]ParameterSchema`): typed params (`string|integer|number|boolean`, `enum`, `default`, `pattern`, min/max, `format`) referenced via `{{ config.x }}`. Missing `default` ⇒ required. **[SRC]**
- **`events[]`** (`Event`): `cronjob` (`CronJobConfig{cron, labels, annotations}`). **[SRC]**
- **`system`**: `isolatedContainers`, `pureByDefault`. **[DOC/SRC]**
- **Expression language:** `{{ }}` with `config.*`, `env.*`, `matrix.*`, `shard.*`, `services.*`, `execution.id`, `workflow.name`, `self.passed`/`self.failed` (retry conditions), counters (`index`, `count`). **[DOC]**

### TestWorkflow status subresource **[SRC]** (`model_test_workflow_execution.go` + generated CRD)
`TestWorkflowExecution` carries: `id`, `name`, `number`, `scheduledAt`, `assignedAt`, `statusAt`, `signature[]` (step tree), `result *TestWorkflowResult`, `output[]`, `reports[]`, `resourceAggregations`, `workflow`, `resolvedWorkflow`, `tags`, `runningContext`, `configParams`, `runtime`.

`TestWorkflowResult`: `status`, `predictedStatus`, `queuedAt`, `startedAt`, `finishedAt`, `duration`/`durationMs`, `totalDuration`/`totalDurationMs`, `pausedMs`, `pauses[]` (`ref`, `pausedAt`, `resumedAt`), `initialization` (step result), `steps{}` (map ref→step result). Per-step: `status`, `queuedAt`, `startedAt`, `finishedAt`.

**Status enum (`TestWorkflowStatus`):** `queued`, `running`, `paused`, `passed`, `failed`, `aborted`. **[SRC]** (CRD enum). `skipped`/`timeout` belong to the *legacy* `ExecutionStatus`, not TestWorkflows.

`TestWorkflowExecutionStatus` (CRD status): `generation`, `latestExecution`. `TestWorkflow.status` uses `TestWorkflowStatusSummary` tracking latest execution + health. **[SRC]**

> **Lesson → our CRDs:** Keep `Test` (definition) and `TestRun` (execution) split like TestWorkflow/TestWorkflowExecution — clean GitOps story (definitions in git, runs ephemeral). Steal the status result shape almost verbatim (phase enum + queued/started/finished timestamps + per-step map). Steal `config` typed-parameter schema. Steal `content.git` auth model wholesale.

---

## 3. Executor Model

### Classic executors (deprecated but instructive) **[SRC]**
- `Executor` CRD (`executor.testkube.io/v1`): `executor_type: job`, `image`, `types[]` (e.g. `example/test`), `content_types[]` (`string|file-uri|git`), `features[]` (`artifacts`, `junit-report`), `meta` (icon/docs/tooltips). The API pairs a Test's `type` to the Executor that handles it. **[SRC]** prebuilt-executor doc + Executor CRD.
- **Runner contract (the key interface):** **[SRC]** `pkg/executor/runner/interface.go`
  ```go
  type Runner interface {
      // Run takes Execution data and returns execution result
      Run(execution testkube.Execution) (result testkube.ExecutionResult, err error)
  }
  ```
  Real runners also implement `Validate(execution) error` and `GetType() runner.Type`. **[SRC]** (artillery/curl/soapui/init runners).
- **JSON contract:** API passes a `testkube.Execution` JSON as the first arg to the container binary; executor writes **JSON lines to STDOUT**, each wrapped in `testkube.ExecutorOutput`. `ExecutionResult` has `Status` (`passed`/`failed`/…), `Output`, `ErrorMessage`, step results. **[SRC]** prebuilt-executor doc + curl/soapui runner source.
- **Content delivery:** test definitions mounted as k8s Volumes before executor starts; path from `RUNNER_DATADIR`. Sources: string, URI, git-file, git-dir (sparse checkout for monorepos). **[SRC]**
- **Init container pattern:** `testkube-executor-init` runner fetches content, places files, `chmod`s artifact dir, returns pending result — separate init step before the main executor. **[SRC]** init runner source.
- **Artifact scraping (classic):** executor scrapes files → MinIO bucket named by execution ID; default mount `/data/artifacts`. Container executors provide an artifact PVC (`ReadWriteMany`, e.g. NFS) for distributed JMeter slaves sharing a volume. `--artifact-sidecar-scraper` runs scraper as a pod sidecar. **[DOC/SRC]**

### Official executors (classic) **[SRC/DOC]** — from `testkube get executor` + integrations page:
`k6`, `cypress`, `postman` (newman), `soapui`, `curl`, `artillery`, `jmeter` + `jmeterd` (distributed), `gradle`, `maven`, `ginkgo`, `playwright`, `kubepug`, `tracetest`, `zap`. Auto-registered by default: `postman/collection`, `cypress/project`, `curl/test`, `k6/script`, `soapui/xml`. **No official classic "locust" executor** — Locust runs via a TestWorkflow container step. **[DOC]**

### Why they migrated OFF classic executors → TestWorkflows (design gold) **[DOC]**
Testkube's own post-mortem ("The Future of Testkube: Transitioning to Test Workflows"): three overlapping mechanisms (prebuilt Go executors, container executors, workflows) = confusing UX + high maintenance. Prebuilt executors were explicitly **"difficult to change and maintain"** (each = a Go repo + Docker image + result mapper). Limitations that drove the move:
- One rigid execution model (single job, fixed command/args); hard to do multi-step, setup/teardown, tool-version pinning.
- No native parallelism/sharding/matrix, no service dependencies, no per-step containers.
- Every new tool = a new Go runner + mapper + maintained image.

### TestWorkflow engine (the replacement) **[SRC/DOC]** `test-workflows-high-level-architecture`
- Agent server **compiles a TestWorkflow into native k8s resources**: ConfigMaps/Secrets for data, one **Job → Pod** for execution. Resources deleted after finish.
- **Sequential steps in one pod via Init Containers** (share filesystem); the *last* step runs in `containers`. General containers run in parallel, so init containers = the sequencing trick.
- **`/init` process (toolkit):** every command is wrapped like `["/init", "curl", ...]`. The init process prepares env, runs the real command, cleans up, streams logs, scrapes artifacts. Binaries in `cmd/testworkflow-init` + `cmd/testworkflow-toolkit`. **[SRC]**
- **Container merging:** to survive Istio-style injected sidecars (which break init-container sequencing on older k8s), Testkube *merges* consecutive compatible operations into one container. `pure: true` marks a step side-effect-free & mergeable (same image/volumes/user). `system.isolatedContainers: true` opts out. **[DOC/SRC]** — **directly relevant to his Istio requirement.**
- **Image metadata fetching:** engine pulls image config from the registry to learn `WORKDIR`/`ENTRYPOINT`/`CMD`/`USER` (k8s doesn't expose these). Needs registry creds; can be bypassed by specifying `workingDir`+`securityContext`+explicit `command`. **[DOC]**
- **Parallel/services:** the main execution pod talks directly to the k8s API to create/watch/destroy Jobs+Pods for each parallel worker/service. **[DOC]**

> **Lesson → our executor model:** Adopt the TestWorkflow container-step model in full — including the "no Runner interface" part we initially skipped. A Test is an `image` + `command|args`, nothing more (see §10). One generic `/entry` wrapper is injected into every image via a shared emptyDir (init container `cp`s it from the content-fetcher image). `/entry` runs the tool's argv verbatim; verdict comes from the exit code, optionally overridden by a declarative `verdictFrom` processor (JUnit / JTL — the two shapes worth first-class support because they fix the "exit code lies" cases at admission-time rather than in per-tool code). No Go `Runner` interface, no per-tool wrapper image, no `spec.type` in the CRD. Tool identity lives in the `kubetest.io/tool` label — set by templates or users, ignored by the operator except for propagation to Job/Pod labels for kubectl selectors. See §11.

> **Platform-image exceptions (step 15B + mini-closing):** The catalog default is "every tool template points at a vendor-published image", with one-and-only-one platform image (`executors/content-fetcher/`) supplying `/entry`. Three tools break the rule:
>
> 1. **Gatling** (`executors/gatling/Dockerfile`) — eclipse-temurin + Maven Central bundle zip + a `gatling-run` wrapper that reads `<results>/*/js/assertions.json` and exits non-zero on any `"result": false`. Reason: Gatling 3.9.x's `gatling.sh` LOCAL run mode returns exit=0 even when assertions fail (only Enterprise `--wait` maps failures), AND no arm64-native community image existed at commit time. Wrapper restores "exit code honest" for the template.
>
> 2. **SoapUI** (`executors/soapui/Dockerfile`) — eclipse-temurin + SoapUI OSS 5.7.2 bundle from eviware.com. Reason: mini-15B closing — dropped the coupling to `kubeshop/testkube-soapui-executor` (a testkube-vendored image with their own runner wrapper). SoapUI OSS is the last public release (SmartBear pushes Ready! API); bundling ourselves keeps the catalog independent of another test-execution platform's lifecycle.
>
> 3. **kubepug** (`executors/kubepug/Dockerfile`) — alpine + kubepug's static Go binary from GitHub releases. Reason: same mini-15B rationale — dropped `kubeshop/testkube-kubepug-executor` (vendored wrapper, quirky `/.kubepug//kubepug` binary path). Trivial image with `kubepug` on standard PATH.
>
> Rule going forward: platform images are the LAST resort. Vendor-published upstream image → community-standard image → own bundle (with file-header rationale + smoke evidence). Never a runtime hack in the operator.

---

## 4. Execution Flow (end-to-end) **[SRC/DOC]**

Classic path (deprecated): trigger → API server creates a k8s **Job** → init container fetches content → executor container runs, emits JSON-line output → API tails logs & parses result → scraper pushes artifacts to MinIO → result persisted to Mongo.

TestWorkflow path (current):
1. Trigger (CLI/REST/gRPC/CR/cron/event) → API server creates a `TestWorkflowExecution` record + compiles workflow.
2. Agent server creates ConfigMaps/Secrets + a **Job/Pod**; controller watches status. **[SRC]** `TestWorkflowExecutionController` (`ENABLE_K8S_CONTROLLERS=true`, controller-runtime).
3. `/init` toolkit runs each step (init-container sequencing + merging), streams logs, scrapes artifacts per-step (glob).
4. **Logs:** persisted to **MinIO bucket `testkube-logs`** (configurable `logs.storage: minio|mongo`, per-execution folder = execution ID). Live streaming to UI/CLI via **WebSockets** (CLI `-f` follow). Toolkit does "log streaming and aggregation". *A dedicated NATS-backed `pkg/logs` log server exists historically but its exact current wiring for standalone TestWorkflow logs was NOT source-confirmed — treat as uncertain.* **[SRC for MinIO bucket + toolkit; INFER for websocket/NATS]**
5. **Artifacts:** scraped to **MinIO bucket `testkube-artifacts`** (glob paths, optional compress). **[SRC/DOC]**
6. **Results:** persisted to Mongo (default, being deprecated) or Postgres (preview). Repos under `pkg/repository/testworkflow/{mongo,postgres}`. **[SRC]**
7. Resources torn down; execution recoverable after agent restart while k8s resources persist. **[DOC]**

**Scheduling (cron):** `spec.events[].cronjob.cron` (standard k8s cron format + tz). **Standalone agent uses a built-in RPC-based cron scheduler** (NOT one k8s CronJob per workflow) — "eliminates the need to create separate CronJob pods… significantly reduces resource usage." **[DOC]** — important scaling insight.

**Event triggers:** `TestTrigger` (§5) watches k8s resources via Listener; leader-elected git-informer for git-based triggers. **[SRC]** ARCHITECTURE.md.

**NATS:** async job processing + event bus (`pkg/event/bus`). Event listeners: webhooks, k8s events, CD events, websockets. **[SRC]**

---

## 5. TestTrigger CRD (k8s-event-based triggering) **[SRC/DOC]**

`tests.testkube.io/v1`, `TestTriggerSpec`:
- `resource` (e.g. `deployment`, `pod`, or any CRD by GVK since a recent release),
- `resourceSelector` (`name`/`nameRegex`/`namespace`/`namespaceRegex`/`labelSelector`),
- `event` (e.g. `modified`, `created`, `deleted`),
- `conditionSpec` (`conditions[]` of `{type,status,reason,ttl}`, `timeout`, `delay`),
- `probeSpec` (HTTP probes),
- `action: run`, `execution: workflow|test`,
- `concurrencyPolicy: allow|...`,
- `testSelector`/target (name or labelSelector),
- `actionParameters` (`config`, `tags`, `target`).

**ArgoCD synergy:** a trigger on `resource: deployment` + `event: modified` fires on ArgoCD sync. Assign ArgoCD Sync Waves so triggers sync after their target workflows. Can also watch Argo Rollouts CR and fire on `Healthy` phase. **[DOC]**

```yaml
apiVersion: tests.testkube.io/v1
kind: TestTrigger
metadata: { name: testtrigger-example, namespace: default }
spec:
  resource: deployment
  resourceSelector: { labelSelector: { matchLabels: { app.kubernetes.io/tier: backend } } }
  event: modified
  conditionSpec:
    timeout: 100
    delay: 2
    conditions:
      - { type: Progressing, status: "True", reason: NewReplicaSetAvailable, ttl: 60 }
      - { type: Available, status: "True" }
  action: run
  execution: workflow
  concurrencyPolicy: allow
  testSelector: { name: frontend-sanity-tests }
```

> **Lesson → our TestTrigger:** Copy near-verbatim. The `conditionSpec` (wait for k8s status conditions before firing) + ArgoCD sync-wave pattern is exactly what he wants for GitOps.

---

## 6. Feature Inventory: copy / skip

**Copy:**
- CRD-as-source-of-truth + separate execution CR (`TestRun`).
- TestWorkflow `content` model (git/files/tarball, sparse checkout, secret-backed auth).
- Typed `config` parameters + expression interpolation.
- Step model: `retry`, `timeout`, `condition`, `optional`, `negative`, `artifacts` glob scraping.
- Container-step executors (not Go-per-tool).
- Status result shape (phase enum + timestamps + per-step map).
- TestTrigger with condition gating.
- Built-in RPC cron scheduler (avoid CronJob-per-workflow sprawl).
- Prometheus `/metrics` endpoint. **[SRC]** (`internal/app/api/metrics`, `promhttp.Handler()` at `/metrics`).
- MinIO artifact + log storage with per-execution folders; JUnit auto-parse.
- kubectl plugin CLI architecture: client abstraction, `~/.testkube` contexts. **[SRC]**

**Copy but simplify:**
- `parallel`/`matrix`/`shards`/`services` — powerful but complex; model from day 1 (unlike Testkube which gates them behind Control Plane) and land incrementally.
- Webhooks/notifications (`Webhook` CRD + event emitter).

**Consciously SKIP (v1):**
- Container-merging/`pure` optimization — only needed because of init-container sequencing + Istio sidecar injection. We avoid the problem structurally instead (§8).
- Image-metadata registry fetching — require explicit `command`/`workingDir`/`securityContext` in step spec instead (simpler, no registry creds). Testkube offers this exact bypass anyway. **[DOC]**
- Multi-agent/multi-cluster, RBAC/SSO/SCIM, AI features — out of scope.
- Mongo. Postgres only.

**Auto-parsing to copy:** per `kubeshop/testkube-docs` (test-workflows-artifacts.md): *"Testkube automatically scans all artifacts for .xml files that are valid JUnit XML reports and parses their contents… Testkube also scans uploaded JSON artifacts for supported performance-test reports and can ingest their aggregated values as Performance Metrics."* Glob path matching uses the **doublestar** library. Copy both behaviors. **[SRC]**

**OpenTelemetry:** Testkube ships **Prometheus metrics + zap logging + product telemetry (Segment/GA4)**; no first-class OTel tracing found. **[SRC]** → add OTel ourselves if wanted.

---

## 7. GUI-vs-GitOps conflict handling (the core UX problem)

Both ArgoCD and the GUI write the same CRs. Testkube's own answer (Control Plane era) uses a GitOps-Agent that syncs CRDs→Control Plane, with a **skip-sync-style annotation** to block reconciliation for manually-edited resources. **[DOC]**

> **Our approach (concrete):**
> - **`app.kubernetes.io/managed-by` label** on every CR: `gitops` (ArgoCD-owned, read-only in GUI) vs `ui` (GUI-owned, mutable).
> - GUI refuses to PATCH `managed-by: gitops` resources except to *create a TestRun* (runs are always allowed — ephemeral children, not definitions).
> - For GitOps-owned `Test`s, GUI "Run" creates a `TestRun` CR referencing the `Test` by name — never mutates the `Test`. Keeps ArgoCD from showing drift.
> - Optional `kubetest.io/skip-reconcile: "true"` annotation for temporary manual overrides (mirrors Testkube). ArgoCD `ignoreDifferences` on `status` + on annotated fields.
> - Definitions live in git; `TestRun`s are created imperatively and NOT committed (exclude from ArgoCD via resource exclusions or a separate namespace).

---

## 8. Pod metadata passthrough & sidecar policy **[DESIGN DECISION]**

**Decision: `Test.spec.pod.annotations` / `.labels` is a fully generic, unopinionated passthrough to the execution Pod. The platform hardcodes NO annotations — no defaults, no special-casing of any key.** Whatever the user defines lands verbatim on the pod. Istio is the motivating example, not a platform feature:

  ```yaml
  spec:
    pod:
      annotations:
        sidecar.istio.io/inject: "false"   # user's choice, not platform policy
  ```

- Merge order: `Test.spec.pod.annotations` → optional `TestRun.spec.pod` overrides (TestRun wins on key conflict). The operator injects nothing beyond its own tracking labels in a reserved prefix (`kubetest.io/run-id`, `app.kubernetes.io/managed-by`), which cannot be overridden.
- **No in-wrapper mesh hacks** (no `quitquitquit` calls, no `pkill istio-proxy`, no `scuttle`-style wrappers): the wrapper never talks to sidecars. Hacks couple test images to mesh implementation details, break on Istio upgrades, and hide infra policy inside workload code. If sidecar lifecycle matters, it's solved declaratively — via pod annotations or cluster config (native sidecar containers: beta on-by-default k8s v1.29, GA v1.33; Istio `values.pilot.env.ENABLE_NATIVE_SIDECARS=true` makes `istio-proxy` a restartable init container, so Jobs complete normally with zero workload cooperation).
- Operational guidance for THIS cluster (docs/templates, NOT operator logic): tests that don't need mesh traffic typically set `sidecar.istio.io/inject: "false"` themselves — an injected proxy otherwise keeps the Job pod Running after the test exits (§15.1). A shared `TestTemplate` can carry this annotation for convenience; the operator remains annotation-agnostic.
- Because we run step-per-pod (not Testkube's multi-init-container single pod), sidecar injection never breaks step sequencing — Testkube's container-merging machinery is unnecessary for us.

---

## 9. Storage & Retention **[SRC/DOC]**

- Testkube default = **Mongo**, moving to **Postgres** (Postgres still "Preview" in current docs; Mongo "will be deprecated"). Repos: `pkg/repository/testworkflow/{mongo,postgres}`, factories `mongo_factory.go`/`postgres_factory.go`, migrations `pkg/dbmigrator`. **[SRC]**
- **Known Mongo pain:** Testkube GitHub issue **#6389** (2025-05-06) — unbounded growth of `testworkflowresults`: 218,361 docs; after a manual 7-day TTL index count dropped to 10,671 and performance recovered. No built-in retention config at the time. **[SRC]**

> **Lesson → our retention:** Postgres from day 1. Build retention in from the start:
> - Partition `test_runs` by created month; drop old partitions via a CronJob.
> - Configurable `retentionDays` (default 30), per-namespace or global.
> - Separate lifecycle for MinIO artifacts/logs: bucket lifecycle rules (expire objects after N days) keyed by execution-ID prefix.
> - Store large logs/artifacts in MinIO, only pointers + summary + step results in Postgres. Never store big blobs in the DB.

---

## 10. Proposed CRD Go Types (kubebuilder sketches)

```go
// api/v1alpha1/test_types.go
package v1alpha1

import (
    corev1 "k8s.io/api/core/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Tool",type=string,JSONPath=`.metadata.labels['kubetest\.io/tool']`
// +kubebuilder:printcolumn:name="LastRun",type=string,JSONPath=`.status.latestRun.phase`
type Test struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    Spec   TestSpec   `json:"spec,omitempty"`
    Status TestStatus `json:"status,omitempty"`
}

// TestSpec has NO `type` field — see §3 lesson block. A Test is
// image + command; tool identity lives in the `kubetest.io/tool` label
// on ObjectMeta (added by templates or users, propagated to Job/Pod
// labels by the compiler for kubectl selectors).
type TestSpec struct {
    Content   Content                `json:"content,omitempty"`
    // Container: image REQUIRED, at least one of command|args REQUIRED
    // (both enforced by the validating webhook — the wrapper needs
    // SOMETHING to run).
    Container ContainerConfig        `json:"container,omitempty"`
    Pod       *PodConfig             `json:"pod,omitempty"`
    Config    map[string]Parameter   `json:"config,omitempty"`    // typed params
    Artifacts *ArtifactSpec          `json:"artifacts,omitempty"`
    Timeout   *metav1.Duration       `json:"timeout,omitempty"`
    Retry     *RetryPolicy           `json:"retry,omitempty"`
    Schedule  string                 `json:"schedule,omitempty"`  // cron; empty = manual
    Services  map[string]ServiceSpec `json:"services,omitempty"`  // dependent sidecar pods
    Parallel  *ParallelSpec          `json:"parallel,omitempty"`
    // Verdict overrides the default exit-code rule. Empty (or From=
    // exitCode) trusts the process exit code. From=junit|jtl runs the
    // matching processor AFTER the tool and OVERRIDES the exit-code
    // verdict in both directions. See §11 + §15.2.
    Verdict *VerdictSpec `json:"verdict,omitempty"`
    // +kubebuilder:validation:Enum=Allow;Forbid;Replace
    ConcurrencyPolicy string          `json:"concurrencyPolicy,omitempty"` // default Allow
}

// VerdictSpec — small on purpose (one enum, one string). Tool-specific
// knobs (JMeter threads, k6 summary keys) belong in container.args or a
// TestTemplate, not here.
type VerdictSpec struct {
    // +kubebuilder:validation:Enum=exitCode;junit;jtl
    // +kubebuilder:default=exitCode
    From string `json:"from,omitempty"`
    // ErrorRateMax is the JTL error-rate threshold as a decimal string
    // (e.g. "0", "0.01"). Only valid when From=jtl; webhook enforces.
    ErrorRateMax string `json:"errorRateMax,omitempty"`
}

// PodConfig is a generic, unopinionated passthrough to the execution Pod.
// Annotations/labels land verbatim on the pod — the platform hardcodes none
// and special-cases none (see §8).
type PodConfig struct {
    Labels             map[string]string           `json:"labels,omitempty"`
    Annotations        map[string]string           `json:"annotations,omitempty"`
    ServiceAccountName string                      `json:"serviceAccountName,omitempty"`
    NodeSelector       map[string]string           `json:"nodeSelector,omitempty"`
    Tolerations        []corev1.Toleration         `json:"tolerations,omitempty"`
    Affinity           *corev1.Affinity            `json:"affinity,omitempty"`
    SecurityContext    *corev1.PodSecurityContext  `json:"securityContext,omitempty"`
    Volumes            []corev1.Volume             `json:"volumes,omitempty"`
    ImagePullSecrets   []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
}

type Content struct {
    Git     *GitContent   `json:"git,omitempty"`
    Files   []FileContent `json:"files,omitempty"`
    Tarball []Tarball     `json:"tarball,omitempty"`
}

type GitContent struct {
    URI       string   `json:"uri"`
    Revision  string   `json:"revision,omitempty"`
    Paths     []string `json:"paths,omitempty"` // sparse checkout
    MountPath string   `json:"mountPath,omitempty"`
    // +kubebuilder:validation:Enum=basic;header;ssh
    AuthType     string               `json:"authType,omitempty"`
    UsernameFrom *corev1.EnvVarSource `json:"usernameFrom,omitempty"`
    TokenFrom    *corev1.EnvVarSource `json:"tokenFrom,omitempty"`
    SSHKeyFrom   *corev1.EnvVarSource `json:"sshKeyFrom,omitempty"`
}

type Parameter struct {
    // +kubebuilder:validation:Enum=string;integer;number;boolean
    Type    string   `json:"type"`
    Default string   `json:"default,omitempty"` // empty => required
    Enum    []string `json:"enum,omitempty"`
    Pattern string   `json:"pattern,omitempty"`
}

type ArtifactSpec struct {
    Paths    []string `json:"paths,omitempty"` // globs (doublestar)
    Compress string   `json:"compress,omitempty"`
}

type RetryPolicy struct {
    // +kubebuilder:validation:Minimum=1
    Count int32  `json:"count"`
    Until string `json:"until,omitempty"` // expr, default "passed"
}

type TestStatus struct {
    ObservedGeneration int64              `json:"observedGeneration,omitempty"`
    LatestRun          *RunReference      `json:"latestRun,omitempty"`
    Conditions         []metav1.Condition `json:"conditions,omitempty"`
}
type RunReference struct {
    Name       string       `json:"name,omitempty"`
    Phase      Phase        `json:"phase,omitempty"`
    FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
}
```

```go
// api/v1alpha1/testrun_types.go

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Test",type=string,JSONPath=`.spec.testRef`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Started",type=date,JSONPath=`.status.startedAt`
type TestRun struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    Spec   TestRunSpec   `json:"spec,omitempty"`
    Status TestRunStatus `json:"status,omitempty"`
}

type TestRunSpec struct {
    TestRef string            `json:"testRef"`           // Test name in same namespace
    Config  map[string]string `json:"config,omitempty"` // param overrides
    Pod     *PodConfig        `json:"pod,omitempty"`    // per-run pod metadata overrides — see §8
    Tags    map[string]string `json:"tags,omitempty"`
    // +kubebuilder:validation:Enum=ui;api;cli;cron;trigger;gitops
    Source  string            `json:"source,omitempty"` // provenance
}

// Phase mirrors Testkube's TestWorkflowStatus enum (verified in source),
// plus `error` for infra failures (OOMKill, ImagePullBackOff, missing result) — see §15.
// +kubebuilder:validation:Enum=queued;running;paused;passed;failed;aborted;error
type Phase string

const (
    PhaseQueued  Phase = "queued"
    PhaseRunning Phase = "running"
    PhasePaused  Phase = "paused"
    PhasePassed  Phase = "passed"
    PhaseFailed  Phase = "failed"  // test executed, assertions/thresholds failed
    PhaseAborted Phase = "aborted" // user/system cancelled
    PhaseError   Phase = "error"   // infra/tooling failure — test verdict unknown
)

type TestRunStatus struct {
    Phase        Phase                 `json:"phase,omitempty"`
    QueuedAt     *metav1.Time          `json:"queuedAt,omitempty"`
    StartedAt    *metav1.Time          `json:"startedAt,omitempty"`
    FinishedAt   *metav1.Time          `json:"finishedAt,omitempty"`
    DurationMs   int64                 `json:"durationMs,omitempty"`
    JobName      string                `json:"jobName,omitempty"`
    ResolvedSpec string                `json:"resolvedSpec,omitempty"` // snapshot of Test spec at start (JSON) — see §15
    Steps        map[string]StepResult `json:"steps,omitempty"`
    LogsRef      string                `json:"logsRef,omitempty"`      // MinIO object key
    ArtifactRefs []ArtifactRef         `json:"artifactRefs,omitempty"`
    Message      string                `json:"message,omitempty"`
    Conditions   []metav1.Condition    `json:"conditions,omitempty"`
}

type StepResult struct {
    Phase      Phase        `json:"phase,omitempty"`
    QueuedAt   *metav1.Time `json:"queuedAt,omitempty"`
    StartedAt  *metav1.Time `json:"startedAt,omitempty"`
    FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
}
```

---

## 11. Executor Contract (container + `/entry` + verdict processors)

**One layer.** Container contract, language-agnostic. There is **no Go
`Runner` interface** — the workflows-model refactor (step 11) deleted it.

### The three moving parts

1. **Any container image.** `spec.container.image` lands verbatim on
   the pod's main container. No per-tool wrapper images, no
   `DefaultExecutorImages` map, no `ImageRegistry` prefix on the main
   image (prefix only applies to the content-fetcher).
2. **One generic `/entry` binary**, injected via a shared `emptyDir`.
   An install init container `cp`s `/entry` from the content-fetcher
   image into `/kubetest-bin/`, then the main container runs
   `/kubetest-bin/entry` with `spec.container.{command⊕args}` as its
   argv. Works with any base image (alpine musl, glibc debian,
   distroless static) because `/entry` is `CGO_ENABLED=0`.
3. **Declarative `verdictFrom` processors.** Default verdict = process
   exit code. `spec.verdict.from ∈ {junit, jtl}` runs the corresponding
   processor AFTER the tool exits, and it OVERRIDES the exit-code
   verdict in both directions (jmeter exit 0 + failing JTL → failed;
   flaky non-zero + clean JUnit → passed).

### Wire types

```go
// pkg/executor/types.go
type ExecutionRequest struct {
    RunID          string            `json:"runId"`
    TestRef        string            `json:"testRef"`
    DataDir        string            `json:"dataDir"`
    WorkingDir     string            `json:"workingDir,omitempty"`
    Command        []string          `json:"command,omitempty"`
    Args           []string          `json:"args,omitempty"`
    Env            map[string]string `json:"env,omitempty"`
    Config         map[string]string `json:"config,omitempty"`
    Artifacts      ArtifactSpec      `json:"artifacts,omitzero"`
    TimeoutSeconds int64             `json:"timeoutSeconds,omitempty"`
    Verdict        VerdictSpec       `json:"verdict,omitzero"`
}

type VerdictSpec struct {
    From         string `json:"from,omitempty"`         // exitCode|junit|jtl
    ErrorRateMax string `json:"errorRateMax,omitempty"` // jtl only
}
```

### `/entry` verdict matrix (the contract in one table)

| base exit | verdictFrom | processor outcome        | phase written |
|-----------|-------------|--------------------------|---------------|
| 0         | (none)      | —                        | **passed**    |
| ≠0        | (none)      | —                        | **failed** ("exit code N") |
| exec-fail | (none)      | —                        | **error** (binary missing) |
| 0         | jtl         | errorRate ≤ max          | **passed**    |
| 0         | jtl         | errorRate > max          | **failed** (JTL override) |
| ≠0        | jtl         | errorRate ≤ max          | **passed** (JTL override) |
| ≠0        | jtl         | errorRate > max          | **failed** |
| 0         | junit       | all pass                 | **passed**    |
| 0         | junit       | any fail                 | **failed** |
| ≠0        | junit       | all pass                 | **passed** (JUnit override) |
| ≠0        | junit       | any fail                 | **failed** |
| any       | junit       | no report / malformed    | **error** — NEVER silently passed |
| any       | jtl         | missing / malformed JTL  | **error** |

Container contract otherwise unchanged: operator projects
`request.json` at `/etc/kubetest/request.json`; content pre-mounted at
`$KUBETEST_DATADIR` by the content-fetcher init; `/entry` streams stdout
(operator tails via k8s API); on exit writes `result.json` to
`$KUBETEST_RESULTDIR` and scrapes `artifacts.paths` → MinIO; SIGTERM
flushes partial state.

### Per-tool exit-code notes (were here as a table — moved to §15.2 as catalog guidance)

Per-tool quirks (JMeter exit 0 lies, Cypress exit code capped at 255,
Locust always exit 0 unless flags set) are documented in §15.2 as
CATALOG guidance — the mitigation is `verdictFrom` (or tool flags in
the template), not runner code. See also `plan/step-15-tool-catalog.md`
for the curated template list.

---

## 12. Artifact sidecar & log streaming pattern **[SRC/DOC-informed]**

- **Init container** (`content-fetcher`): clones git (sparse), unpacks tarballs, writes inline files → shared `emptyDir` at `/data`. Analog of `testkube-executor-init`. **[SRC]**
- **Main container**: the tool wrapper. Shares `/data`.
- **Artifact scraping**: prefer a **post-step scrape in the wrapper** (glob → MinIO) for single-pod runs. For distributed runs (JMeter slaves) needing shared storage, use a `ReadWriteMany` PVC (NFS) — exactly Testkube's distributed-JMeter pattern. **[DOC]** Provide `--artifact-sidecar` mode (scraper as sidecar container) for tools that write continuously.
- **Log streaming**: operator watches pod, tails logs via k8s client **from pod start** and flushes to MinIO continuously (kubelet log rotation — see §15), fans out to (a) websocket subscribers (live GUI) and (b) MinIO `kubetest-logs/<runID>/`. Keep a small ring-buffer in the API server for reconnects. (Testkube uses websockets for live UI logs + MinIO bucket `testkube-logs`; a NATS log-server exists but we don't need NATS — direct k8s tail + websocket is simpler.) **[SRC for buckets; INFER for exact TK protocol]**

---

## 13. Proposed Repo Layout

```
kubetest-alt/
├── api/v1alpha1/                 # CRD types: test_types.go, testrun_types.go,
│                                 #   testtemplate_types.go, testtrigger_types.go,
│                                 #   groupversion_info.go, zz_generated.deepcopy.go
├── cmd/
│   ├── operator/main.go          # manager: controllers + webhooks
│   ├── apiserver/main.go         # thin REST/gRPC API for GUI
│   ├── entry/main.go             # in-container wrapper (/entry) + content fetcher
│   └── cli/main.go               # kubectl-kubetest plugin
├── internal/
│   ├── controller/
│   │   ├── testrun_controller.go     # compiles TestRun -> Job/Pod, tracks status
│   │   ├── test_controller.go        # cron scheduling, latestRun status
│   │   └── testtrigger_controller.go # watches k8s events -> creates TestRun
│   ├── compiler/                 # Test(+Template) -> k8s Job/Pod/ConfigMap/Secret
│   ├── scheduler/                # built-in RPC cron (NOT CronJob-per-test)
│   ├── logstream/                # k8s log tail -> websocket + MinIO flush
│   ├── scraper/                  # glob artifacts -> MinIO; JUnit/perf parse
│   └── store/                    # Postgres run-history repo + retention
├── pkg/
│   ├── executor/                 # Runner interface + ExecutionRequest/Result
│   ├── expr/                     # {{ }} expression engine (config/env/matrix/shard)
│   └── apis/                     # generated clientset (for API server + CLI)
├── executors/                    # wrapper Dockerfiles: k6/ cypress/ newman/ locust/ jmeter/
├── config/                       # kustomize: crd/ rbac/ manager/ webhook/
├── charts/kubetest-alt/          # Helm: operator, apiserver, GUI, minio, postgres subcharts
├── web/                          # GUI (SPA) -> talks only to apiserver
└── test/                         # e2e (envtest + kind)
```

---

## 14. Build Order (staged)

1. **CRDs + operator skeleton** (`Test`, `TestRun`) + `testrun_controller` that creates a single Job/Pod and writes status. Single k6 wrapper. envtest.
2. **Content fetcher init container** (git/files/tarball) + **artifact scraper** (glob→MinIO) + **log streaming** (tail→websocket→MinIO). Postgres run store + retention.
3. **Thin API server + GUI**: list/detail Tests & TestRuns, live logs, artifact download, "Run" button (creates TestRun). `managed-by` label enforcement.
4. **Remaining executors** (cypress/newman/locust/jmeter) + JUnit/perf auto-parse.
5. **TestTrigger** controller (k8s-event + condition gating, ArgoCD sync-wave friendly) + **cron scheduler**.
6. **Templates** + `config` params + expression engine. Then `parallel`/`matrix`/`shards`/`services` incrementally.
7. Webhooks/notifications, Prometheus metrics, optional OTel.

**Benchmarks that change the plan:**
- If run volume > ~100k rows/week → prioritize Postgres partitioning + retention *before* anything else (Testkube hit a Mongo perf wall at 218,361 rows — issue #6389).
- If GUI/GitOps drift becomes noisy in ArgoCD → enforce `managed-by` + `status` ignoreDifferences before adding more CR fields.

---

## 15. Edge Cases & Failure Modes (must-handle)

Priority order for this cluster: **Istio pod policy → JMeter exit-0 → log rotation → resolved-spec snapshot.** Implement these four in stage 1–2, not later.

### 15.1 Istio / mesh
- Injected `istio-proxy` keeps a Job's pod Running forever after the test exits (sidecar never terminates). **Handled declaratively by the user, not by the platform (§8):** tests that don't need mesh set `sidecar.istio.io/inject: "false"` in `spec.pod.annotations` (or inherit it from a shared `TestTemplate`); tests that DO need mesh rely on native sidecar containers (k8s ≥1.29 beta / 1.33 GA + Istio `ENABLE_NATIVE_SIDECARS=true`), which makes Job completion correct with zero cooperation from the test container. The operator itself injects no annotations and contains no Istio-specific logic. **Out of the executor contract:** quitquitquit calls, pkill of the proxy, scuttle-style wrappers.
- Pod metadata merge order: Test → TestRun override; only reserved `kubetest.io/*` tracking labels come from the operator.

### 15.2 Exit codes — every tool lies differently (catalog guidance)

The wrapper is generic (step 11): base verdict = process exit code. Tools
whose exit codes lie get a `verdictFrom` processor OR a tool flag in the
template — mitigation lives in the catalog, NOT in Go runner code.

- **JMeter: exit 0 even when 100% of requests fail.** Regression-guarded
  in the `jtl` processor tests (`TestEntry_Verdict_ExitZeroButJTL100Pct
  Errors_ShouldFail`). Template MUST set `spec.verdict.from: jtl` +
  `errorRateMax: "0"` (or looser). Without `verdictFrom: jtl` every
  JMeter run reports passed regardless of failures — the whole reason
  `verdictFrom` exists.
- **k6:** 0 = passed; 99 = thresholds not met → failed by default
  exit-code rule; other non-zero (107/108 script/panic) → failed. Loses
  the failed-vs-error split for script bugs (a broken script reports
  `failed` not `error`); acceptable trade-off — the alternative was a k6
  Go runner, which we retired.
- **Cypress:** exit code = number of failed tests, capped at 255.
  Template sets `spec.verdict.from: junit`; the JUnit processor reads
  real counts from `results.xml` and overrides the capped exit code.
  Regression-guarded in `TestClassify_ExitCodeCapAt255_CountsComeFromJUnit`
  (moved to catalog-side once step 15 lands).
- **Locust:** exits 0 by default regardless of failures unless
  `--exit-code-on-error` / `--check-fail-ratio` are set. Template MUST
  either (a) add `--exit-code-on-error 1` to args, or (b) once we grow
  a `csv` verdict processor, wire `spec.verdict.from: csv`. First
  approach ships in step 15; second is future work if the flag path
  proves inadequate.
- **Missing `result.json`** (wrapper crash, OOM, SIGKILL): fallback
  path = container exit code + pod
  `containerStatuses[].lastState.terminated` → phase `error` with
  reason. Never assume result.json exists.

### 15.3 Job/Pod lifecycle
- **OOMKilled (137):** no result.json, no artifacts. Read `terminated.reason=OOMKilled` from pod status → phase `error` ("OOMKilled — raise spec.container.resources"), NOT `failed`. **Cypress-specific /dev/shm requirement** moved to the cypress TestTemplate (step 15): the template supplies a memory-backed emptyDir via `spec.pod.volumes`. The compiler no longer has a per-tool branch for this.
- **`activeDeadlineSeconds` kills with SIGKILL — the scraper won't run.** Contract: wrapper enforces its own `TimeoutSeconds` (SIGTERM-trappable, flush partial results + artifacts), and the operator sets Job `activeDeadlineSeconds = test timeout + 60s` as the outer hard limit. Wrapper timeout < Job deadline, always.
- **TTL vs operator restart:** if `ttlSecondsAfterFinished` deletes the Job before the operator records status, the TestRun hangs `running` forever. Policy: operator deletes the Job itself *after* persisting status; TTL is only a safety net (long, e.g. 3600s). Reconciler includes **orphan detection**: TestRun in `running`/`queued` with no matching Job → `error`/`aborted` with message.
- **ImagePullBackOff / scheduling failures:** pod never starts, deadline eventually fires. Surface as `error` ("infra: ImagePullBackOff <image>"), not a test failure — read pod events/conditions in the reconciler.

### 15.4 Logs
- **Kubelet rotates container logs (default ~10MB)** — verbose k6/JMeter output overruns rotation and loses the beginning if logs are read post-mortem. Therefore: tail from pod start, flush to MinIO continuously (§12), never rely on end-of-run `GetLogs`.
- **Watch disconnects:** API server drops watches ~every 5 min; reconnect with `resourceVersion`, on `410 Gone` re-list. Reconciler must be idempotent — duplicate events WILL arrive. (controller-runtime handles this; don't hand-roll raw watches in the API server either — use the shared cache.)

### 15.5 CRD / GitOps
- **Snapshot the resolved Test spec into `TestRun.status.resolvedSpec` at start.** Otherwise, editing a Test mid-run makes historical results correspond to no recorded definition. GUI shows the snapshot for finished runs.
- **Owner references:** Job → ownerRef → TestRun (cascade delete OK). **NO ownerRef Test → TestRun** — deleting a definition must not erase run history. Add a **finalizer on TestRun**: delete during `running` = kill Job + cleanup MinIO stream + then remove finalizer.
- **ArgoCD prune:** imperatively-created TestRuns inside an Argo-managed namespace show OutOfSync/get pruned. Exclude `TestRun` via Argo project `resource.exclusions` (or run executions in a dedicated namespace Argo doesn't own).

### 15.6 Scheduling & concurrency
- **Cron double-fire / missed-fire on leader failover:** deterministic TestRun names derived from scheduled time (`{test}-{unixTimestamp}`) — the duplicate `create` fails with AlreadyExists, making fires idempotent.
- **Thundering herd:** N users launching heavy load tests concurrently = node pressure. v1: namespace `ResourceQuota` + per-Test `concurrencyPolicy: Allow|Forbid|Replace` (CronJob semantics). If it outgrows this → Kueue, not a hand-rolled queue.

### 15.7 Content
- **Size limits:** etcd object ≈1.5MB, ConfigMap 1MB. Validation webhook rejects inline content above a threshold (e.g. 512KB) with a clear "use git/tarball source" error — don't let the API server produce a cryptic failure.
- **Git edge cases:** submodules, LFS, sparse-checkout path that doesn't exist, wrong auth — content-fetcher must fail fast with a human-readable message in TestRun status (`error`, reason `ContentFetchFailed`), never hang.

---

## 16. Verification Ledger (what was checked in source vs docs)

**Source-verified [SRC]:**
- `ARCHITECTURE.md` — agent components, storage layer (Mongo/Postgres/MinIO/NATS), controllers, CRD inventory, deprecated CRD list, MinIO buckets `testkube-artifacts`/`testkube-logs`.
- `pkg/executor/runner/interface.go` — `Runner.Run(execution testkube.Execution) (testkube.ExecutionResult, error)`; plus `Validate`/`GetType` on real runners (artillery/curl/soapui/init).
- `pkg/api/v1/testkube/model_test_workflow_execution.go` — TestWorkflowExecution + TestWorkflowResult status/result fields.
- Generated CRD schema `testworkflows.testkube.io/v1` — full step/content/service/parameter schema; status enum `[queued running paused passed failed aborted]`.
- GitHub issue #6389 — Mongo `testworkflowresults` unbounded growth (218,361 → 10,671 via 7-day TTL index), opened 2025-05-06.
- `LICENSE` + licensing FAQ — MIT + TCL dual license.
- Feature-comparison table — OSS-vs-Control-Plane gating.
- `testkube-docs` test-workflows-artifacts.md — JUnit `.xml` auto-scan + performance-JSON ingest + doublestar glob.

**Docs-only [DOC]:** TestWorkflow high-level architecture (init/toolkit, container merging, `pure`, image-metadata fetch, parallel/services pod-to-API behavior); prebuilt-executor JSON-line contract details; cron RPC scheduler; TestTrigger + ArgoCD sync-wave patterns; Connected-Mode 2.7.0 capability-agents; classic executor list; Dec-2025 legacy deprecation.

**Inferred / NOT fully source-confirmed [INFER]:** exact live-log wire protocol for TestWorkflows (WebSocket most likely); whether a NATS-backed `pkg/logs` log server is wired into the current standalone TestWorkflow log path — flagged uncertain. Direct k8s-tail + websocket is our recommended simpler substitute regardless.

**External-fact enrichment:** k8s native sidecar containers — alpha v1.28 (Aug 2023), beta/on-by-default v1.29, **GA/stable v1.33 (23 April 2025)**, per kubernetes.io/docs/concepts/workloads/pods/sidecar-containers. Tool exit-code semantics (§15.2) are from tool docs/behavior, verify against pinned tool versions during stage-4 wrapper implementation.
