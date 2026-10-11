# TODOs

Ideas agreed on but not scheduled as a step yet. Each one becomes a step
of its own (or part of one) when it is picked up.

---

## One report for a parallel run

Today each worker of a parallel run has its own report (Report tab, Open
report); test counts and artifacts add up for the run, but there is no
report of the whole run. Most tools can merge their own results — use
that, not an HTML stitcher of ours:

- **Playwright:** `merge-reports` (made for shards): workers write the
  blob reporter, the merge makes one HTML report;
- **JMeter:** the workers' JTL files concatenated, one report from them
  (`jmeter -g merged.jtl -o report`) — the usual distributed-JMeter way;
- **Gatling:** every worker's `simulation.log` in one folder, then
  `gatling.sh -ro` — the official distributed-Gatling way;
- **k6:** no HTML merge; the workers' JSON output replayed into one
  dashboard (`k6 dashboard replay`) — counts and response times hold, the
  VUs chart is off (say so in the template);
- **Locust, ZAP, Gradle:** nothing sensible to merge — reports stay per
  worker.

The platform stays tool-agnostic (CLAUDE.md §0, §3):

- **the template declares the merge** — e.g. `parallel.merge: { command:
  [...], report: ... }` on the Test/template (CRD field, webhook checks);
- **operator:** once every worker has finished, one more Job, "Merge
  reports", in the tool's image; the content fetcher downloads the
  workers' artifacts (`workers/<i>/artifacts/`) from object storage into
  it; its report becomes the run's (`spec.artifacts.report`). The same
  pattern as Testkube's `parallel.fetch` + a step after the workers;
- **UI:** a "Merge" stage in the run's flow (with its log); "All workers"
  first in the Report tab's picker and in Open report, each worker's
  report below it;
- **tests:** operator envtest (the merge Job after the workers, its
  failure doesn't fail a passed run but marks the merge stage), the
  fetcher pulling worker artifacts, Vitest, and the catalog e2e running
  each template with merge in parallel on kind.

Order: Playwright, JMeter, Gatling first (native and exact), then k6.

A cheaper complement, no new Job: a workers comparison in Control Center
— one row per worker, its metrics side by side (p95, error rate, counts,
duration) from what is parsed already.
