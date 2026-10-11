import { Copy } from "lucide-react";
import { toast } from "sonner";

import { Panel } from "@/components/run/sections";
import { Button } from "@/components/ui/button";

/** sh quotes a word for a POSIX shell when it needs it. */
export function sh(word: string): string {
  return /^[A-Za-z0-9._/:=@%+-]+$/.test(word) ? word : `'${word.replace(/'/g, `'\\''`)}'`;
}

/** cliCommands are the kubectl kubetest commands for a Test (step 20e). */
export function cliCommands(ns: string, name: string, config: Record<string, string> = {}) {
  const t = `${sh(name)} -n ${sh(ns)}`;
  const params = Object.entries(config)
    .filter(([, v]) => v !== "")
    .map(([k, v]) => ` --config ${sh(`${k}=${v}`)}`)
    .join("");
  return [
    { what: "Run it and follow the log", cmd: `kubectl kubetest run ${t} --follow` },
    ...(params
      ? [{ what: "Run it with these parameters", cmd: `kubectl kubetest run ${t}${params} --follow` }]
      : []),
    { what: "Its runs", cmd: `kubectl kubetest runs --test ${sh(name)} -n ${sh(ns)}` },
    { what: "A run's log, followed", cmd: `kubectl kubetest logs <run> -n ${sh(ns)} -f` },
    { what: "A run's artifacts", cmd: `kubectl kubetest artifacts <run> -n ${sh(ns)} --download ./out` },
    { what: "Stop a run", cmd: `kubectl kubetest abort <run> -n ${sh(ns)}` },
    { what: "The Test as YAML", cmd: `kubectl get test ${t} -o yaml` },
  ];
}

/** CliPanel: ready commands for the Test, each with a copy button. */
export function CliPanel({
  ns,
  name,
  config,
}: {
  ns: string;
  name: string;
  config?: Record<string, string>;
}) {
  return (
    <Panel id="cli" title="CLI commands">
      <p className="text-muted-foreground text-sm">
        With the kubectl plugin (docs/cli.md). The kube context is yours (
        <code className="font-mono">--context</code>); the API token comes from{" "}
        <code className="font-mono">$KUBETEST_API_TOKEN</code> or the chart&apos;s Secret.
      </p>
      <ul className="space-y-3">
        {cliCommands(ns, name, config).map((c) => (
          <li key={c.what} className="space-y-1">
            <div className="text-sm font-medium">{c.what}</div>
            <div className="bg-muted flex items-center gap-2 rounded-lg border px-3 py-2">
              <code className="min-w-0 flex-1 font-mono text-sm [overflow-wrap:anywhere]">{c.cmd}</code>
              <Button
                size="icon"
                variant="ghost"
                aria-label={`Copy: ${c.what}`}
                onClick={() => {
                  void navigator.clipboard
                    .writeText(c.cmd)
                    .then(() => toast("Copied."))
                    .catch(() => toast.error("The browser didn't allow copying."));
                }}
              >
                <Copy />
              </Button>
            </div>
          </li>
        ))}
      </ul>
    </Panel>
  );
}
