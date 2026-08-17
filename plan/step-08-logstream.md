# Step 08 — Log streaming

## Goal
`internal/logstream`: tail pod logs from start, fan-out to websocket subscribers, continuous flush to MinIO (CLAUDE.md §12, §15.4).

## Tasks
- Start tail at pod Running (follow=true), from beginning; flush chunks to `kubetest-logs/<runID>/` continuously — never a single post-mortem GetLogs (§15.4 kubelet rotation).
- Ring buffer per active run (configurable, e.g. 64KB) for late-joining/reconnecting subscribers; subscribers get buffer replay + live tail.
- Slow-client policy: bounded per-subscriber queue, drop-with-marker on overflow (never block the tail).
- Reconnect handling on watch/stream errors: resume, dedupe by byte offset.

## Unit test requirements (no cluster — fake io.ReadCloser as log source)
- Fan-out: 3 subscribers receive identical byte streams; late joiner gets ring-buffer prefix + live continuation (assert exact bytes).
- Ring buffer: overflow keeps newest N bytes; replay marker indicates truncation.
- Slow client: blocked subscriber gets dropped/marked after queue limit; other subscribers unaffected (timing-safe test via channels, no sleeps).
- Flush: chunks uploaded at size/interval thresholds; on source EOF final flush contains full tail; simulated stream error mid-run → resume produces no duplicate/lost bytes (offset dedupe test).
- Race detector mandatory: concurrent subscribe/unsubscribe/publish under `-race`.

## Acceptance
- envtest/kind smoke: live logs visible via API while pod runs; MinIO object complete after finish.
