import { Copy, Download } from "lucide-react";
import { lazy, Suspense, useEffect, useState } from "react";
import { toast } from "sonner";

import type { TestForm } from "@/api/client";
import { previewTest, type TestKey } from "@/api/queries";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";

const YamlView = lazy(() => import("@/components/yaml-view"));

export type Built = { yaml: string; errors: string[]; warnings: string[] };

/**
 * usePreview builds the form's YAML on the server as it changes (debounced,
 * the save's own build — no copy of the rules here). The YAML keeps its
 * last good state while the form can't be built.
 */
export function usePreview(cluster: string, form: TestForm, edit?: TestKey, delay = 300): Built {
  const [built, setBuilt] = useState<Built>({ yaml: "", errors: [], warnings: [] });
  const body = JSON.stringify(form);
  const editKey = edit ? `${edit.ns}/${edit.name}` : "";
  useEffect(() => {
    const ctl = new AbortController();
    const timer = setTimeout(() => {
      previewTest(cluster, JSON.parse(body) as TestForm, edit, ctl.signal)
        .then((p) =>
          setBuilt((was) => ({ yaml: p.yaml || was.yaml, errors: p.errors, warnings: p.warnings })),
        )
        .catch((e: unknown) => {
          if (ctl.signal.aborted) return;
          setBuilt((was) => ({ ...was, errors: [e instanceof Error ? e.message : String(e)] }));
        });
    }, delay);
    return () => {
      clearTimeout(timer);
      ctl.abort();
    };
    // edit is identified by editKey
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cluster, body, editKey, delay]);
  return built;
}

/** downloadYAML saves the YAML as <name>.yaml in the browser. */
export function downloadYAML(name: string, yaml: string) {
  const url = URL.createObjectURL(new Blob([yaml], { type: "application/yaml" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = `${name || "test"}.yaml`;
  a.click();
  URL.revokeObjectURL(url);
}

/** YamlPanel: the Test's YAML as it builds, with what is still missing. */
export function YamlPanel({ name, built }: { name: string; built: Built }) {
  return (
    <section aria-label="YAML" aria-live="polite" className="space-y-3">
      <div className="flex items-center gap-2">
        <h2 className="mr-auto font-semibold">
          YAML <span className="text-muted-foreground text-sm font-normal">builds as you type</span>
        </h2>
        <Button
          size="sm"
          variant="outline"
          disabled={!built.yaml}
          onClick={() => {
            void navigator.clipboard
              .writeText(built.yaml)
              .then(() => toast("YAML copied."))
              .catch(() => toast.error("The browser didn't allow copying."));
          }}
        >
          <Copy /> Copy
        </Button>
        <Button
          size="sm"
          variant="outline"
          disabled={!built.yaml}
          onClick={() => downloadYAML(name, built.yaml)}
        >
          <Download /> Download
        </Button>
      </div>
      {built.errors.length > 0 && (
        <details className="text-sm" data-testid="yaml-missing">
          <summary className="cursor-pointer">
            {built.errors.length} to fill in before it can be created
          </summary>
          <ul className="text-destructive mt-1 list-disc pl-5">
            {built.errors.map((e) => (
              <li key={e}>{e}</li>
            ))}
          </ul>
        </details>
      )}
      {built.warnings.length > 0 && (
        <ul className="list-disc pl-5 text-sm text-amber-700 dark:text-amber-400">
          {built.warnings.map((w) => (
            <li key={w}>{w}</li>
          ))}
        </ul>
      )}
      {built.yaml ? (
        <Suspense fallback={<Skeleton className="h-64" />}>
          <YamlView value={built.yaml} label="The Test's YAML" />
        </Suspense>
      ) : (
        built.errors.length === 0 && (
          <p className="text-muted-foreground text-sm">Fill in the form — the Test's YAML builds here.</p>
        )
      )}
    </section>
  );
}
