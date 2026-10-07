# kubectl-kubetest

kubetest from a terminal or a CI pipeline: list Tests, start runs and wait
for their verdict, read logs, download artifacts.

## Install

```sh
make install-cli          # → $(go env GOPATH)/bin/kubectl-kubetest
export PATH="$PATH:$(go env GOPATH)/bin"
kubectl kubetest --help
```

`kubectl` finds any `kubectl-<name>` on the `PATH`; the binary also runs
on its own (`kubectl-kubetest tests`).

## How it connects

Like `kubectl`: the current kubeconfig context (`--context`,
`--kubeconfig`), Tests in the context's namespace (`-n`). It reaches the
kubetest API through the Kubernetes API's service proxy — the same path
Control Center takes — so nothing needs to be exposed:

1. the API Service: found by its labels in `--kubetest-namespace`
   (default `kubetest-alt`), or `--api-service`;
2. the API token (docs/security.md): `$KUBETEST_API_TOKEN` if set, else
   the chart's Secret `<release>-kubetest-alt-api-token` next to the
   Service, or `--api-token-secret`.

So the Kubernetes permissions it needs are `get` and `create` on
`services/proxy` for that Service (reads are GETs; starting and aborting
runs are POSTs) and `get` on that Secret (or, without the latter,
`$KUBETEST_API_TOKEN` from your pipeline's secrets). Runs record
`source: cli` and `created-by` = `$KUBETEST_USER`, else your OS user.

## Commands

| Command | |
|---|---|
| `tests` | Tests of the namespace: tool, schedule, last run |
| `test NAME` | a Test with its templates merged; its parameters and defaults |
| `run TEST [--config k=v]… [--at RFC3339]` | start a run, print its name |
| `run TEST --wait [--timeout 30m]` | … and wait; **exit code = verdict** |
| `run TEST --follow` | … and stream its log while waiting |
| `runs [--test T] [--limit N]` | runs, newest first |
| `runs RUN` | one run: verdict, message, parameters, metrics, commit, report |
| `logs RUN [-f]` | the run's log; `-f` follows a running one |
| `abort RUN [--message …]` | stop a run |
| `artifacts RUN [--download DIR]` | list artifacts, or save them under DIR |

`-o json` / `-o yaml` on the listing and showing commands.

### Exit codes

| Code | When |
|---|---|
| 0 | the run passed (or the command succeeded) |
| 1 | the run **failed** — its tests or thresholds did |
| 2 | the run ended in `error` or `aborted`, `--timeout` expired (the run keeps going), or the CLI itself failed |

## In a pipeline

```yaml
# GitHub Actions: gate a deploy on the smoke test
- name: Smoke test on stage
  env:
    KUBETEST_API_TOKEN: ${{ secrets.KUBETEST_STAGE_TOKEN }}
  run: kubectl kubetest run checkout-smoke -n shop --wait --timeout 15m --follow
```

The step fails on anything but a passed run; `--follow` puts the tool's
output in the job log. After an ArgoCD sync, a `PostSync` hook Job can do
the same with an image that has kubectl and the plugin.
