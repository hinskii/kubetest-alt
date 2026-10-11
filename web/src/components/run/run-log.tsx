import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useLayoutEffect, useRef, useState } from "react";

import { runLogChunk, runQuery, type RunKey } from "@/api/queries";
import { Button } from "@/components/ui/button";
import { goPage } from "@/lib/paths";

const FINAL = new Set(["passed", "failed", "error", "aborted"]);

/**
 * useRunLog reads the stored log and, while the run is live, polls for
 * what was added (from the last offset). A phase change refreshes the run.
 */
export function useRunLog(k: RunKey, live: boolean, phase: string) {
  const qc = useQueryClient();
  const [text, setText] = useState("");
  const [state, setState] = useState<"loading" | "ok" | "error">("loading");
  const [error, setError] = useState("");
  const offset = useRef(0);
  const shownPhase = useRef(phase);
  useEffect(() => {
    shownPhase.current = phase;
  }, [phase]);

  useEffect(() => {
    const ctl = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    let polling = live;
    const poll = async () => {
      try {
        const chunk = await runLogChunk(k, offset.current, ctl.signal);
        if (chunk.text) setText((t) => t + chunk.text);
        offset.current = chunk.next;
        setState("ok");
        if (chunk.more) return void poll();
        if (chunk.phase && chunk.phase !== shownPhase.current) {
          void qc.invalidateQueries({ queryKey: runQuery(k).queryKey });
          if (FINAL.has(chunk.phase)) polling = false;
        }
        if (polling) timer = setTimeout(poll, 2_000);
      } catch (e) {
        if (ctl.signal.aborted) return;
        setError(e instanceof Error ? e.message : String(e));
        setState((s) => (s === "loading" ? "error" : s));
        if (polling) timer = setTimeout(poll, 5_000);
      }
    };
    void poll();
    return () => {
      ctl.abort();
      clearTimeout(timer);
    };
    // One poller per run; live only switches it off (finished runs read once).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [k.cluster, k.ns, k.id, live]);

  return { text, state, error };
}

/** RunLog shows the run's log, following the end while you are at the bottom. */
export function RunLog({ k, live, phase }: { k: RunKey; live: boolean; phase: string }) {
  const { text, state, error } = useRunLog(k, live, phase);
  const pre = useRef<HTMLPreElement>(null);
  const follow = useRef(true);
  useLayoutEffect(() => {
    if (pre.current && follow.current) pre.current.scrollTop = pre.current.scrollHeight;
  }, [text]);
  const placeholder =
    state === "loading"
      ? "Loading…"
      : state === "error"
        ? `Log unavailable: ${error}`
        : text === ""
          ? live
            ? "Waiting for output…"
            : "No log stored."
          : null;
  return (
    <pre
      ref={pre}
      data-testid="run-log"
      aria-label="Log"
      onScroll={(e) => {
        const el = e.currentTarget;
        follow.current = el.scrollTop + el.clientHeight >= el.scrollHeight - 20;
      }}
      className="bg-muted max-h-[60vh] overflow-auto rounded-lg border p-3 font-mono text-xs whitespace-pre-wrap"
    >
      {placeholder ?? text}
    </pre>
  );
}

/** LogDownload saves the whole stored log. */
export function LogDownload({ k }: { k: RunKey }) {
  return (
    <Button variant="outline" size="sm" asChild>
      <a href={goPage.logs(k.cluster, k.ns, k.id)}>Download</a>
    </Button>
  );
}
