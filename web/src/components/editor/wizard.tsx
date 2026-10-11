import { useMutation, useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { lazy, Suspense, useState } from "react";
import { toast } from "sonner";

import type { TestForm, ToolTemplate } from "@/api/client";
import { createTest, FormRefused, namespacesQuery, templatesQuery } from "@/api/queries";
import { downloadYAML, usePreview, YamlPanel } from "@/components/editor/live-yaml";
import { errorsFor, Field, GitFields, ParamFields } from "@/components/editor/fields";
import { NamespaceCombobox } from "@/components/editor/namespace-combobox";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";

const CodeEditor = lazy(() => import("@/components/editor/code-editor"));

type Source = NonNullable<TestForm["source"]>;
type Step = 1 | 2 | 3;

const OWN = "__own";
const steps: { n: Step; title: string }[] = [
  { n: 1, title: "What do you test?" },
  { n: 2, title: "Where is the test?" },
  { n: 3, title: "Parameters" },
];

/**
 * sourcesFor: where a tool's code may come from (step 20j). A project tool
 * takes git only; a single-file tool a pasted script or git; a tool
 * without files nothing (or git); an own image any of them.
 */
export { errorsFor };

export function sourcesFor(t: ToolTemplate | undefined): Source[] {
  if (!t) return ["", "git", "inline"];
  switch (t.mainKind) {
    case "directory":
      return ["git"];
    case "file":
      return ["inline", "git"];
    default:
      return ["", "git"];
  }
}

const sourceLabel: Record<Source, string> = {
  inline: "Paste the script",
  git: "From Git",
  "": "None — the image needs no files",
};

/** fileNameFor guesses the script's name from the tool's "e.g." example. */
export function fileNameFor(t: ToolTemplate | undefined): string {
  const p = t?.params.find((p) => p.name === t.mainParam);
  const eg = /e\.g\. (\S+\.\w+)/.exec(p?.description ?? "")?.[1];
  return eg?.split("/").pop() ?? "script";
}

/** renamed sets a manifest's metadata.name (Duplicate of a YAML-only Test). */
export function renamed(yaml: string, from: string, to: string): string {
  return yaml.replace(
    new RegExp(`^(  name: )${from.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`, "m"),
    `$1${to}`,
  );
}

/**
 * TestWizard creates a Test in three short steps (step 20j): what, where,
 * parameters — the YAML building beside it, the same build as Create.
 */
export function TestWizard({
  cluster,
  initial,
  yamlOnly,
}: {
  cluster: string;
  initial: TestForm;
  yamlOnly?: string;
}) {
  const navigate = useNavigate();
  const [form, setForm] = useState<TestForm>(initial);
  const [step, setStep] = useState<Step>(1);
  const [shown, setShown] = useState<Set<string>>(new Set());
  const [submitted, setSubmitted] = useState(false);
  const [showYaml, setShowYaml] = useState(false);
  const [checked, setChecked] = useState<{ ok?: boolean; errors: string[] }>();
  const [toolChosen, setToolChosen] = useState(!!initial.template || !!initial.image);

  const namespaces = useQuery(namespacesQuery(cluster));
  const templates = useQuery({ ...templatesQuery(cluster, form.namespace), enabled: !!form.namespace });
  const tmpl = templates.data?.templates.find((t) => t.name === form.template);
  const built = usePreview(cluster, form);
  const yamlMode = form.mode === "yaml";

  const set = (patch: Partial<TestForm>) => {
    setForm((f) => ({ ...f, ...patch }));
    setChecked(undefined);
  };
  const touch = (key: string) => setShown((s) => (s.has(key) ? s : new Set(s).add(key)));
  const errs = (key: string, ...labels: string[]) =>
    submitted || shown.has(key) ? errorsFor(built.errors, ...labels) : [];

  const pickTool = (value: string) => {
    const t = templates.data?.templates.find((t) => t.name === value);
    setToolChosen(true);
    const source = sourcesFor(t)[0]!;
    set({
      template: t?.name ?? "",
      params: {},
      source,
      files: source === "inline" ? [{ path: fileNameFor(t), content: "" }] : [],
    });
  };
  const pickSource = (source: Source) =>
    set({
      source,
      files:
        source === "inline"
          ? form.files?.length
            ? form.files
            : [{ path: fileNameFor(tmpl), content: "" }]
          : [],
    });

  const save = useMutation({
    mutationFn: (dryRun: boolean) => createTest(cluster, form, dryRun),
    onMutate: () => setChecked(undefined),
    onSuccess: (saved) => {
      if (saved.dryRun) {
        setChecked({ ok: true, errors: [] });
        return;
      }
      toast(`Test ${saved.name} created.`);
      void navigate({
        to: "/clusters/$cluster/tests/$ns/$name",
        params: { cluster, ns: saved.namespace, name: saved.name },
      });
    },
    onError: (e) => {
      setSubmitted(true);
      setChecked({ ok: false, errors: e instanceof FormRefused && e.errors.length ? e.errors : [e.message] });
    },
  });

  // The step's fields: Next shows their problems rather than moving on.
  const stepFields: Record<Step, { key: string; labels: string[] }[]> = {
    1: [
      { key: "name", labels: ["Name"] },
      { key: "namespace", labels: ["Namespace"] },
      { key: "template", labels: ["Template"] },
    ],
    2: [
      { key: "image", labels: ["Image"] },
      { key: "command", labels: ["Command or arguments"] },
      { key: "gitUri", labels: ["Git repository"] },
      { key: "gitPath", labels: ["Path in the repository"] },
      { key: "files", labels: ["Inline files", "Files"] },
    ],
    3: [],
  };
  const next = () => {
    const fields = stepFields[step];
    if (errorsFor(built.errors, ...fields.flatMap((f) => f.labels)).length > 0) {
      setShown((s) => new Set([...s, ...fields.map((f) => f.key)]));
      return;
    }
    setStep((s) => (s < 3 ? ((s + 1) as Step) : s));
  };

  const tools = templates.data?.templates ?? [];
  const sources = sourcesFor(form.template ? tmpl : undefined);
  const ownImage = toolChosen && !form.template;

  const actions = (
    <div className="space-y-3">
      {checked?.ok && (
        <p role="status" className="text-sm text-green-700 dark:text-green-400">
          The API would admit this Test (schema, webhooks, pod policy).
        </p>
      )}
      {checked && !checked.ok && (
        <div role="alert" className="text-destructive text-sm">
          <strong>Not created:</strong>
          <ul className="list-disc pl-5">
            {checked.errors.map((e) => (
              <li key={e}>{e}</li>
            ))}
          </ul>
        </div>
      )}
      <div className="flex flex-wrap gap-2">
        <Button disabled={save.isPending} onClick={() => save.mutate(false)}>
          Create test
        </Button>
        <Button variant="outline" disabled={save.isPending} onClick={() => save.mutate(true)}>
          Check
        </Button>
        <Button variant="outline" disabled={!built.yaml} onClick={() => downloadYAML(form.name, built.yaml)}>
          Download YAML
        </Button>
        <Button variant="outline" className="lg:hidden" onClick={() => setShowYaml((v) => !v)}>
          {showYaml ? "Hide YAML" : "Show YAML"}
        </Button>
      </div>
    </div>
  );

  return (
    <div className="grid gap-8 lg:grid-cols-[34rem_minmax(0,1fr)]">
      <div className="space-y-6">
        {yamlMode ? (
          <section aria-label="YAML editor" className="space-y-4">
            {yamlOnly && <p className="text-muted-foreground text-sm">{yamlOnly}</p>}
            <p className="text-muted-foreground text-sm">
              The whole Test: anything the form doesn't cover (composite steps, services, parallel workers,
              volumes, verdict) is written here.
            </p>
            <Suspense fallback={<Skeleton className="h-96" />}>
              <CodeEditor
                value={form.yaml ?? ""}
                onChange={(yaml) => set({ yaml })}
                fileName="test.yaml"
                label="The Test as YAML"
                errors={built.errors}
                minHeight="28rem"
              />
            </Suspense>
            {!yamlOnly && (
              <Button variant="outline" onClick={() => set({ mode: "form" })}>
                Back to the form
              </Button>
            )}
            {actions}
          </section>
        ) : (
          <>
            <ol aria-label="Steps" className="flex flex-wrap gap-2 text-sm">
              {steps.map((s) => (
                <li key={s.n}>
                  <button
                    type="button"
                    aria-current={s.n === step ? "step" : undefined}
                    onClick={() => setStep(s.n)}
                    className={cn(
                      "rounded-full border px-3 py-1",
                      s.n === step ? "bg-primary text-primary-foreground" : "text-muted-foreground",
                    )}
                  >
                    {s.n}. {s.title}
                  </button>
                </li>
              ))}
            </ol>

            {step === 1 && (
              <section aria-label="What do you test?" className="space-y-4">
                <Field id="name" label="Name" hint="lowercase, digits, -" errors={errs("name", "Name")}>
                  <Input
                    id="name"
                    value={form.name}
                    placeholder="checkout-smoke"
                    autoComplete="off"
                    maxLength={200}
                    onChange={(e) => set({ name: e.target.value })}
                    onBlur={() => touch("name")}
                  />
                </Field>
                <Field
                  id="namespace"
                  label="Namespace"
                  hint="the tool catalog follows it"
                  errors={errs("namespace", "Namespace")}
                >
                  <NamespaceCombobox
                    id="namespace"
                    value={form.namespace}
                    namespaces={namespaces.data?.namespaces ?? []}
                    onChange={(namespace) => {
                      touch("namespace");
                      set({ namespace });
                    }}
                  />
                </Field>
                <Field id="tool" label="Tool" errors={errs("template", "Template")}>
                  <Select
                    value={toolChosen ? form.template || OWN : ""}
                    onValueChange={(v) => pickTool(v === OWN ? "" : v)}
                    disabled={!form.namespace}
                  >
                    <SelectTrigger id="tool" className="w-full">
                      <SelectValue
                        placeholder={form.namespace ? "Choose a tool" : "Pick a namespace first"}
                      />
                    </SelectTrigger>
                    <SelectContent>
                      {tools.map((t) => (
                        <SelectItem key={t.name} value={t.name}>
                          {t.name}
                        </SelectItem>
                      ))}
                      <SelectItem value={OWN}>Own image — any container</SelectItem>
                    </SelectContent>
                  </Select>
                  <p className="text-muted-foreground text-sm" data-testid="tool-hint">
                    {templates.data?.error
                      ? `The tool catalog could not be listed: ${templates.data.error}`
                      : !toolChosen
                        ? form.namespace && "The namespace's tool catalog (TestTemplates)."
                        : tmpl
                          ? `${tmpl.image} — ${
                              tmpl.mainKind === "directory"
                                ? "runs a project from a Git repository"
                                : tmpl.mainKind === "file"
                                  ? "runs one file: paste it or take it from Git"
                                  : "needs no files of yours"
                            }`
                          : "Any container: you give the image and the command."}
                  </p>
                </Field>
                {toolChosen && sources.length > 1 && (
                  <div className="space-y-1.5">
                    <Label id="source-label">Where the code comes from</Label>
                    <RadioGroup
                      aria-labelledby="source-label"
                      value={form.source ?? ""}
                      onValueChange={(v) => pickSource(v as Source)}
                      className="flex flex-wrap gap-4"
                    >
                      {sources.map((s) => (
                        <div key={s || "none"} className="flex items-center gap-2">
                          <RadioGroupItem value={s} id={`source-${s || "none"}`} />
                          <Label htmlFor={`source-${s || "none"}`} className="font-normal">
                            {sourceLabel[s]}
                          </Label>
                        </div>
                      ))}
                    </RadioGroup>
                  </div>
                )}
              </section>
            )}

            {step === 2 && (
              <section aria-label="Where is the test?" className="space-y-4">
                {ownImage && (
                  <>
                    <Field id="image" label="Image" errors={errs("image", "Image")}>
                      <Input
                        id="image"
                        className="font-mono"
                        value={form.image ?? ""}
                        placeholder="python:3.13-slim"
                        autoComplete="off"
                        onChange={(e) => set({ image: e.target.value })}
                        onBlur={() => touch("image")}
                      />
                    </Field>
                    <Field
                      id="command"
                      label="Command"
                      hint="one item per line"
                      errors={errs("command", "Command or arguments")}
                    >
                      <Textarea
                        id="command"
                        rows={2}
                        className="font-mono"
                        value={form.command ?? ""}
                        onChange={(e) => set({ command: e.target.value })}
                        onBlur={() => touch("command")}
                      />
                    </Field>
                    <Field id="args" label="Arguments" hint="one per line; {{ config.name }} works">
                      <Textarea
                        id="args"
                        rows={3}
                        className="font-mono"
                        value={form.args ?? ""}
                        onChange={(e) => set({ args: e.target.value })}
                      />
                    </Field>
                  </>
                )}
                {form.source === "inline" && (
                  <>
                    <Field id="file-name" label="File name" errors={errs("files", "Inline files", "Files")}>
                      <Input
                        id="file-name"
                        className="font-mono"
                        value={form.files?.[0]?.path ?? ""}
                        autoComplete="off"
                        onChange={(e) =>
                          set({ files: [{ path: e.target.value, content: form.files?.[0]?.content ?? "" }] })
                        }
                        onBlur={() => touch("files")}
                      />
                    </Field>
                    <div className="space-y-1.5">
                      <p className="text-sm font-medium">The script</p>
                      <Suspense fallback={<Skeleton className="h-64" />}>
                        <CodeEditor
                          value={form.files?.[0]?.content ?? ""}
                          onChange={(content) =>
                            set({ files: [{ path: form.files?.[0]?.path ?? fileNameFor(tmpl), content }] })
                          }
                          fileName={form.files?.[0]?.path ?? ""}
                          label="The script"
                        />
                      </Suspense>
                      <p className="text-muted-foreground text-sm">Up to 512 KB.</p>
                    </div>
                  </>
                )}
                {form.source === "git" && (
                  <GitFields form={form} set={set} tmpl={tmpl} errors={errs} onBlur={touch} />
                )}
                {!ownImage && !form.source && (
                  <p className="text-muted-foreground text-sm">
                    Nothing to set here — the tool needs no files.
                  </p>
                )}
              </section>
            )}

            {step === 3 && (
              <section aria-label="Parameters" className="space-y-4">
                {tmpl ? (
                  <ParamFields
                    tmpl={tmpl}
                    params={form.params}
                    onChange={(params) => set({ params })}
                    errors={(p) => errs(`param-${p.name}`, p.name)}
                    onBlur={(p) => touch(`param-${p.name}`)}
                  />
                ) : (
                  <p className="text-muted-foreground text-sm">An own image has no template parameters.</p>
                )}
                <p className="text-muted-foreground text-sm">
                  Everything else — test data, resources, pod, schedule, timeout — is in the Test's Settings
                  once it exists.{" "}
                  <button
                    type="button"
                    className="underline"
                    onClick={() => set({ mode: "yaml", yaml: built.yaml || form.yaml })}
                  >
                    Edit as YAML
                  </button>{" "}
                  to write it now.
                </p>
                {actions}
              </section>
            )}

            {step < 3 && (
              <div className="flex gap-2">
                {step > 1 && (
                  <Button variant="outline" onClick={() => setStep((s) => (s - 1) as Step)}>
                    Back
                  </Button>
                )}
                <Button onClick={next}>Next</Button>
              </div>
            )}
          </>
        )}
      </div>
      <aside className={cn("lg:sticky lg:top-4 lg:block lg:self-start", !showYaml && "hidden")}>
        <YamlPanel name={form.name} built={built} />
      </aside>
    </div>
  );
}
