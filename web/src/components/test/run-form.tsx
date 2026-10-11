import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Controller, useForm, type Resolver } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";

import type { Param } from "@/api/client";
import { startRun, type TestKey } from "@/api/queries";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

const NOT_BEFORE = "__notBefore";

/** schemaOf validates the run form as the API does: required, pattern, numbers. */
export function schemaOf(params: Param[]) {
  const shape: Record<string, z.ZodType<string>> = {};
  for (const p of params) {
    let f = z.string().trim();
    if (p.required) f = f.min(1, `${p.name} is required`);
    if (p.pattern) {
      const re = new RegExp(p.pattern);
      f = f.refine((v) => v === "" || re.test(v), `must match ${p.pattern}`) as unknown as typeof f;
    }
    if (p.type === "integer") {
      f = f.refine((v) => v === "" || /^-?\d+$/.test(v), "must be a whole number") as unknown as typeof f;
    } else if (p.type === "number") {
      f = f.refine((v) => v === "" || Number.isFinite(Number(v)), "must be a number") as unknown as typeof f;
    }
    shape[p.name] = f;
  }
  shape[NOT_BEFORE] = z.string();
  return z.object(shape);
}

type Values = Record<string, string>;

/** RunForm starts a run: parameters (defaults filled in) and an optional UTC start. */
export function RunForm({ k, params }: { k: TestKey; params: Param[] }) {
  const navigate = useNavigate();
  const form = useForm<Values>({
    // The schema is built at run time from the parameters: its static type is wide.
    resolver: zodResolver(schemaOf(params)) as unknown as Resolver<Values>,
    defaultValues: Object.fromEntries([
      ...params.map((p) => [p.name, p.default ?? (p.type === "boolean" ? "false" : "")]),
      [NOT_BEFORE, ""],
    ]),
  });
  const run = useMutation({
    mutationFn: (v: Values) => {
      const { [NOT_BEFORE]: at, ...config } = v;
      // datetime-local has no zone; the field is UTC.
      return startRun(k, { config, notBefore: at ? `${at}:00Z` : undefined });
    },
    onSuccess: (started) => {
      toast.success(
        started.notBefore
          ? `Run scheduled for ${started.notBefore.slice(0, 16).replace("T", " ")} UTC.`
          : "Run started.",
      );
      return navigate({
        to: "/clusters/$cluster/runs/$ns/$id",
        params: { cluster: k.cluster, ns: started.namespace, id: started.name },
      });
    },
    onError: (e) => toast.error(`Could not start the run: ${e.message}`),
  });

  return (
    <form
      onSubmit={form.handleSubmit((v) => run.mutate(v))}
      className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3"
      noValidate
      aria-label="Run"
    >
      {params.map((p) => {
        const id = `param-${p.name}`;
        const err = form.formState.errors[p.name]?.message;
        const choices = p.enum ?? (p.type === "boolean" ? ["true", "false"] : undefined);
        return (
          <div key={p.name} className="grid gap-1.5">
            <Label htmlFor={id}>
              {p.name}
              <span className="text-muted-foreground font-normal">
                {p.type}
                {p.required && ", required"}
              </span>
            </Label>
            {choices ? (
              <Controller
                control={form.control}
                name={p.name}
                render={({ field }) => (
                  <Select value={field.value} onValueChange={field.onChange}>
                    <SelectTrigger id={id} className="w-full">
                      <SelectValue placeholder="choose…" />
                    </SelectTrigger>
                    <SelectContent>
                      {choices.map((c) => (
                        <SelectItem key={c} value={c}>
                          {c}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              />
            ) : (
              <Input
                id={id}
                inputMode={p.type === "integer" || p.type === "number" ? "decimal" : undefined}
                aria-invalid={!!err}
                {...form.register(p.name)}
              />
            )}
            {err && <p className="text-danger text-xs">{err}</p>}
          </div>
        );
      })}
      <div className="grid gap-1.5">
        <Label htmlFor="not-before">
          Start at <span className="text-muted-foreground font-normal">UTC, optional — empty starts now</span>
        </Label>
        <Input id="not-before" type="datetime-local" {...form.register(NOT_BEFORE)} />
      </div>
      <div className="flex items-end sm:col-span-2 lg:col-span-3">
        <Button type="submit" disabled={run.isPending}>
          {run.isPending ? "Starting…" : "Run test"}
        </Button>
      </div>
    </form>
  );
}
