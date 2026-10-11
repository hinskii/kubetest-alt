import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Plus, Trash2 } from "lucide-react";
import { lazy, Suspense, useCallback, useEffect, useState, type ReactNode } from "react";
import { toast } from "sonner";

import { ApiError, type Schedule, type TestForm, type TestFormOf } from "@/api/client";
import {
  FormRefused,
  templatesQuery,
  testFormQuery,
  testQuery,
  updateTest,
  type TestKey,
} from "@/api/queries";
import { Field, GitFields, ParamFields, TextField } from "@/components/editor/fields";
import { usePreview, YamlPanel, type Built } from "@/components/editor/live-yaml";
import { fileNameFor, sourcesFor } from "@/components/editor/wizard";
import { ErrorView } from "@/components/error-view";
import { SchedulePanel } from "@/components/test/schedule-panel";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";

const CodeEditor = lazy(() => import("@/components/editor/code-editor"));

type Key = keyof TestForm;
type TestData = NonNullable<TestForm["testData"]>[number];

/** norm makes "", [], {}, undefined and empty map values compare equal. */
function norm(v: unknown): unknown {
  if (v == null || v === "") return "";
  if (Array.isArray(v)) return v.length ? v : "";
  if (typeof v === "object") {
    const kept = Object.entries(v).filter(([, x]) => x !== "" && x != null);
    return kept.length ? Object.fromEntries(kept.sort(([a], [b]) => a.localeCompare(b))) : "";
  }
  return v;
}
export const same = (a: unknown, b: unknown) => JSON.stringify(norm(a)) === JSON.stringify(norm(b));

/** itemsToRows / rowsToItems: test data's "key=/path" lines as rows. */
export function itemsToRows(items?: string): { key: string; path: string }[] {
  return (items ?? "")
    .split("\n")
    .filter((l) => l.trim())
    .map((l) => {
      const i = l.indexOf("=");
      return i < 0 ? { key: l.trim(), path: "" } : { key: l.slice(0, i).trim(), path: l.slice(i + 1).trim() };
    });
}
export const rowsToItems = (rows: { key: string; path: string }[]) =>
  rows
    .filter((r) => r.key || r.path)
    .map((r) => `${r.key}=${r.path}`)
    .join("\n");

/**
 * Section is one part of the Test's settings: it edits a draft of its own
 * fields and saves them alone — the rest of the form as the server sent it,
 * the save conditional on the version shown (a 409 when it changed).
 */
function Section({
  k,
  base,
  id,
  title,
  fields,
  editable,
  onDraft,
  children,
}: {
  k: TestKey;
  base: TestFormOf;
  id: string;
  title: string;
  fields: Key[];
  editable: boolean;
  /** onDraft gets the section's unsaved fields (undefined: none). */
  onDraft: (id: string, picked?: Partial<TestForm>) => void;
  children: (draft: TestForm, set: (patch: Partial<TestForm>) => void, errors: string[]) => ReactNode;
}) {
  const qc = useQueryClient();
  const [draft, setDraft] = useState<TestForm>(base.form);
  const [shown, setShown] = useState(base.form);
  const dirty = fields.some((f) => !same(draft[f], base.form[f]));
  // A newer version from the server: an untouched section follows it.
  if (shown !== base.form) {
    setShown(base.form);
    if (!fields.some((f) => !same(draft[f], shown[f]))) setDraft(base.form);
  }
  const picked = JSON.stringify(Object.fromEntries(fields.map((f) => [f, draft[f]])));
  useEffect(() => {
    onDraft(id, dirty ? (JSON.parse(picked) as Partial<TestForm>) : undefined);
  }, [onDraft, id, dirty, picked]);
  const [errors, setErrors] = useState<string[]>([]);
  const [conflict, setConflict] = useState(false);
  const save = useMutation({
    mutationFn: () => {
      const picked = Object.fromEntries(fields.map((f) => [f, draft[f]]));
      return updateTest(k, { ...base.form, ...picked, mode: "form" });
    },
    onMutate: () => {
      setErrors([]);
      setConflict(false);
    },
    onSuccess: async () => {
      toast.success(`${title} saved.`);
      await qc.invalidateQueries({ queryKey: testQuery(k).queryKey });
      const fresh = qc.getQueryData(testFormQuery(k).queryKey);
      if (fresh) setDraft(fresh.form);
    },
    onError: (e) => {
      setConflict(e instanceof ApiError && e.status === 409);
      setErrors(e instanceof FormRefused && e.errors.length ? e.errors : [e.message]);
    },
  });
  const set = (patch: Partial<TestForm>) => setDraft((d) => ({ ...d, ...patch }));

  return (
    <section className="bg-card space-y-4 rounded-xl border p-5" aria-labelledby={`${id}-title`} id={id}>
      <h2 id={`${id}-title`} className="text-lg font-semibold">
        {title}
      </h2>
      <fieldset disabled={!editable} className="space-y-4">
        {children(draft, set, errors)}
      </fieldset>
      {errors.length > 0 && (
        <div role="alert" className="text-destructive space-y-2 text-sm">
          <strong>Not saved:</strong>
          <ul className="list-disc pl-5">
            {errors.map((e) => (
              <li key={e}>{e}</li>
            ))}
          </ul>
          {conflict && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                setErrors([]);
                setConflict(false);
                void qc.refetchQueries({ queryKey: testFormQuery(k).queryKey }).then(() => {
                  const fresh = qc.getQueryData(testFormQuery(k).queryKey);
                  if (fresh) setDraft(fresh.form);
                });
              }}
            >
              Reload (drops this section's changes)
            </Button>
          )}
        </div>
      )}
      {editable && (
        <div className="flex items-center gap-2">
          <Button disabled={!dirty || save.isPending} onClick={() => save.mutate()}>
            Save {title.toLowerCase()}
          </Button>
          {dirty && (
            <>
              <Button variant="outline" onClick={() => setDraft(base.form)}>
                Discard
              </Button>
              <span className="text-muted-foreground text-sm">Unsaved changes</span>
            </>
          )}
        </div>
      )}
    </section>
  );
}

function TestDataRows({ rows, onChange }: { rows: TestData[]; onChange: (rows: TestData[]) => void }) {
  const update = (i: number, patch: Partial<TestData>) =>
    onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  return (
    <div className="space-y-4">
      {rows.length === 0 && (
        <p className="text-muted-foreground text-sm">
          None. Test data comes from ConfigMaps and Secrets of the namespace, as read-only files.
        </p>
      )}
      {rows.map((r, i) => {
        const items = itemsToRows(r.items);
        return (
          <div key={i} className="space-y-3 rounded-lg border p-3" data-testid="test-data-row">
            <div className="flex flex-wrap items-end gap-2">
              <div className="space-y-1.5">
                <Label htmlFor={`td-kind-${i}`}>Kind</Label>
                <Select
                  value={r.kind}
                  onValueChange={(kind) => update(i, { kind: kind as TestData["kind"] })}
                >
                  <SelectTrigger id={`td-kind-${i}`} className="w-36">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="configMap">ConfigMap</SelectItem>
                    <SelectItem value="secret">Secret</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div className="min-w-40 flex-1 space-y-1.5">
                <Label htmlFor={`td-name-${i}`}>Name</Label>
                <Input
                  id={`td-name-${i}`}
                  className="font-mono"
                  value={r.name}
                  placeholder="products-stage"
                  onChange={(e) => update(i, { name: e.target.value })}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor={`td-where-${i}`}>Where</Label>
                <Select
                  value={r.where || "default"}
                  onValueChange={(w) => update(i, { where: w === "default" ? "" : (w as TestData["where"]) })}
                >
                  <SelectTrigger id={`td-where-${i}`} className="w-56">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="default">A folder of its own</SelectItem>
                    <SelectItem value="dir">All keys in a folder I choose</SelectItem>
                    <SelectItem value="files">Some keys as files I choose</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <Button
                variant="ghost"
                size="icon"
                aria-label={`Remove ${r.name || "this test data"}`}
                onClick={() => onChange(rows.filter((_, j) => j !== i))}
              >
                <Trash2 />
              </Button>
            </div>
            {(r.where || "default") === "default" && (
              <p className="text-muted-foreground text-sm">
                Each key is a file in <code className="font-mono">/data/testdata/{r.name || "<name>"}/</code>.
              </p>
            )}
            {r.where === "dir" && (
              <TextField
                id={`td-dir-${i}`}
                label="Folder"
                hint="absolute; empty or new — never over files of the code"
                mono
                value={r.mountPath}
                placeholder="/data/repo/e2e/data"
                onChange={(mountPath) => update(i, { mountPath })}
              />
            )}
            {r.where === "files" && (
              <div className="space-y-2">
                <p className="text-sm font-medium">Files — each replaces that one file</p>
                {[...items, { key: "", path: "" }].map((it, j) => (
                  <div key={j} className="flex gap-2">
                    <Input
                      aria-label={`Key ${j + 1}`}
                      className="w-40 font-mono"
                      value={it.key}
                      placeholder="env"
                      onChange={(e) => {
                        const next = [...items];
                        next[j] = { ...it, key: e.target.value };
                        update(i, { items: rowsToItems(next) });
                      }}
                    />
                    <span className="self-center">→</span>
                    <Input
                      aria-label={`Path ${j + 1}`}
                      className="font-mono"
                      value={it.path}
                      placeholder="/data/repo/e2e/.env"
                      onChange={(e) => {
                        const next = [...items];
                        next[j] = { ...it, path: e.target.value };
                        update(i, { items: rowsToItems(next) });
                      }}
                    />
                  </div>
                ))}
              </div>
            )}
          </div>
        );
      })}
      <Button
        variant="outline"
        onClick={() => onChange([...rows, { kind: "configMap", name: "", where: "" }])}
      >
        <Plus /> Add test data
      </Button>
    </div>
  );
}

/**
 * TestSettings: the Test's settings in sections (step 20j), each saved on
 * its own. Git-managed Tests (locked) and viewers see them read-only.
 */
export function TestSettings({
  k,
  canEdit,
  schedule,
}: {
  k: TestKey;
  canEdit: boolean;
  schedule?: Schedule;
}) {
  const q = useQuery(testFormQuery(k));
  if (q.isError) return <ErrorView error={q.error} />;
  if (!q.data) return <Skeleton className="h-64 w-full" />;
  return <SettingsForm k={k} base={q.data} canEdit={canEdit} schedule={schedule} />;
}

/**
 * previewForm is what the YAML beside the sections shows: the Test as
 * saved with every section's unsaved changes — or, while the YAML is being
 * edited, that YAML.
 */
export function previewForm(
  base: TestFormOf,
  drafts: Record<string, Partial<TestForm>>,
  yaml?: string,
): TestForm {
  if (yaml !== undefined) return { ...base.form, mode: "yaml", yaml };
  if (base.yamlOnly) return { ...base.form, mode: "yaml" };
  return Object.values(drafts).reduce<TestForm>((f, d) => ({ ...f, ...d }), { ...base.form, mode: "form" });
}

/**
 * YamlSide is the Test's YAML beside the sections: read-only, following
 * the unsaved changes; "Edit YAML" turns it into the editor for the whole
 * Test (what the sections don't cover), saved on its own.
 */
function YamlSide({
  k,
  base,
  built,
  editing,
  yaml,
  onEdit,
  onYaml,
  canEdit,
  blocked,
}: {
  k: TestKey;
  base: TestFormOf;
  built: Built;
  editing: boolean;
  yaml: string;
  onEdit: (on: boolean) => void;
  onYaml: (yaml: string) => void;
  canEdit: boolean;
  blocked: boolean;
}) {
  const qc = useQueryClient();
  const [errors, setErrors] = useState<string[]>([]);
  const save = useMutation({
    mutationFn: () => updateTest(k, { ...base.form, mode: "yaml", yaml }),
    onMutate: () => setErrors([]),
    onSuccess: async () => {
      toast.success("YAML saved.");
      await qc.invalidateQueries({ queryKey: testQuery(k).queryKey });
      const fresh = qc.getQueryData(testFormQuery(k).queryKey);
      if (base.yamlOnly) onYaml(fresh?.form.yaml ?? yaml);
      else onEdit(false);
    },
    onError: (e) => setErrors(e instanceof FormRefused && e.errors.length ? e.errors : [e.message]),
  });
  if (!editing) {
    return (
      <div className="space-y-3">
        <YamlPanel
          name={k.name}
          built={built}
          title="as saved, with your unsaved changes"
          label="The Test now"
        />
        {canEdit && (
          <div className="flex items-center gap-2">
            <Button variant="outline" disabled={blocked} onClick={() => onEdit(true)}>
              Edit YAML
            </Button>
            {blocked && (
              <span className="text-muted-foreground text-sm">
                Save or discard the sections' changes first.
              </span>
            )}
          </div>
        )}
      </div>
    );
  }
  const dirty = yaml !== (base.form.yaml ?? "");
  return (
    <section aria-label="Edit YAML" className="space-y-3">
      <h2 className="font-semibold">
        YAML{" "}
        <span className="text-muted-foreground text-sm font-normal">
          the whole Test — also what the sections don't cover; name and namespace stay
        </span>
      </h2>
      <Suspense fallback={<Skeleton className="h-96" />}>
        <CodeEditor
          value={yaml}
          onChange={onYaml}
          fileName="test.yaml"
          label="The Test as YAML"
          errors={[...errors, ...built.errors]}
          minHeight="28rem"
        />
      </Suspense>
      {(errors.length > 0 || built.errors.length > 0) && (
        <div role="alert" className="text-destructive text-sm">
          {errors.length > 0 && <strong>Not saved:</strong>}
          <ul className="list-disc pl-5">
            {[...new Set([...errors, ...built.errors])].map((e) => (
              <li key={e}>{e}</li>
            ))}
          </ul>
        </div>
      )}
      <div className="flex items-center gap-2">
        <Button disabled={!dirty || save.isPending} onClick={() => save.mutate()}>
          Save YAML
        </Button>
        {!base.yamlOnly && (
          <Button variant="outline" onClick={() => onEdit(false)}>
            {dirty ? "Discard" : "Close"}
          </Button>
        )}
        {dirty && base.yamlOnly && (
          <Button variant="outline" onClick={() => onYaml(base.form.yaml ?? "")}>
            Discard
          </Button>
        )}
      </div>
    </section>
  );
}

function SettingsForm({
  k,
  base,
  canEdit,
  schedule,
}: {
  k: TestKey;
  base: TestFormOf;
  canEdit: boolean;
  schedule?: Schedule;
}) {
  const templates = useQuery(templatesQuery(k.cluster, k.ns));
  const [drafts, setDrafts] = useState<Record<string, Partial<TestForm>>>({});
  const onDraft = useCallback((id: string, picked?: Partial<TestForm>) => {
    const key = id.replace(/^settings-/, "");
    setDrafts((d) => {
      if (!picked && !(key in d)) return d;
      const next = { ...d };
      if (picked) next[key] = picked;
      else delete next[key];
      return next;
    });
  }, []);
  const editable = canEdit && !base.locked;
  // The YAML editor: always for a Test only YAML can show.
  const [yamlDraft, setYamlDraft] = useState<string | undefined>(
    editable && base.yamlOnly ? (base.form.yaml ?? "") : undefined,
  );
  const editingYaml = yamlDraft !== undefined;
  const built = usePreview(k.cluster, previewForm(base, drafts, yamlDraft), k);
  const s = { k, base, editable: editable && !editingYaml, onDraft };
  const tools = templates.data?.templates ?? [];
  const tmplOf = (f: TestForm) => tools.find((t) => t.name === f.template);

  return (
    <div className="grid gap-8 lg:grid-cols-[34rem_minmax(0,1fr)]">
      <div className="min-w-0 space-y-6">
        {base.locked && (
          <p role="status" className="text-muted-foreground text-sm">
            Managed in Git: read-only here. Change it in its repository, or{" "}
            <Link
              to="/clusters/$cluster/tests/new"
              params={{ cluster: k.cluster }}
              search={{ namespace: k.ns, from: k.name }}
              className="underline"
            >
              duplicate it
            </Link>{" "}
            to make a copy managed in the GUI.
          </p>
        )}
        {base.yamlOnly ? (
          <p className="text-muted-foreground text-sm">{base.yamlOnly}</p>
        ) : (
          <>
            <Section {...s} id="settings-general" title="General" fields={["template", "tool"]}>
              {(f, set) => (
                <>
                  <Field id="test-name" label="Name">
                    <p id="test-name" className="font-mono text-sm">
                      {base.form.namespace}/{base.form.name}
                    </p>
                  </Field>
                  <Field id="settings-tool" label="Tool">
                    <Select
                      value={f.template || "__own"}
                      onValueChange={(v) => set({ template: v === "__own" ? "" : v })}
                    >
                      <SelectTrigger id="settings-tool" className="w-full">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {tools.map((t) => (
                          <SelectItem key={t.name} value={t.name}>
                            {t.name}
                          </SelectItem>
                        ))}
                        {f.template && !tools.some((t) => t.name === f.template) && (
                          <SelectItem value={f.template}>{f.template}</SelectItem>
                        )}
                        <SelectItem value="__own">Own image — any container</SelectItem>
                      </SelectContent>
                    </Select>
                  </Field>
                  <TextField
                    id="tool-label"
                    label="Tool label"
                    hint="kubetest.io/tool — groups the Test in the list; a template sets it"
                    value={f.tool}
                    placeholder="pytest"
                    onChange={(tool) => set({ tool })}
                  />
                </>
              )}
            </Section>

            <Section
              {...s}
              id="settings-source"
              title="Source"
              fields={[
                "source",
                "gitUri",
                "gitPath",
                "gitRevision",
                "gitPaths",
                "gitSecret",
                "gitSecretKey",
                "files",
              ]}
            >
              {(f, set, errors) => {
                const tmpl = tmplOf(f);
                const sources = sourcesFor(f.template ? tmpl : undefined);
                const files = f.files ?? [];
                const errs = (_: string, ...labels: string[]) =>
                  errors.filter((e) => labels.some((l) => e.startsWith(l + ":")));
                return (
                  <>
                    <RadioGroup
                      aria-label="Where the code comes from"
                      value={f.source ?? ""}
                      onValueChange={(v) => set({ source: v as TestForm["source"] })}
                      className="flex flex-wrap gap-4"
                    >
                      {[...new Set([...sources, f.source ?? ""])].map((src) => (
                        <div key={src || "none"} className="flex items-center gap-2">
                          <RadioGroupItem value={src} id={`settings-source-${src || "none"}`} />
                          <Label htmlFor={`settings-source-${src || "none"}`} className="font-normal">
                            {src === "git"
                              ? "Git repository"
                              : src === "inline"
                                ? "Files written here"
                                : "None"}
                          </Label>
                        </div>
                      ))}
                    </RadioGroup>
                    {f.source === "git" && (
                      <GitFields form={f} set={set} tmpl={tmpl} errors={errs} advanced />
                    )}
                    {f.source === "inline" && (
                      <div className="space-y-4">
                        {files.map((file, i) => (
                          <div key={i} className="space-y-2" data-testid="file-row">
                            <div className="flex gap-2">
                              <Input
                                aria-label={`File ${i + 1} name`}
                                className="font-mono"
                                value={file.path}
                                onChange={(e) =>
                                  set({
                                    files: files.map((x, j) =>
                                      j === i ? { ...x, path: e.target.value } : x,
                                    ),
                                  })
                                }
                              />
                              <Button
                                variant="ghost"
                                size="icon"
                                aria-label={`Remove ${file.path || `file ${i + 1}`}`}
                                onClick={() => set({ files: files.filter((_, j) => j !== i) })}
                              >
                                <Trash2 />
                              </Button>
                            </div>
                            <Suspense fallback={<Skeleton className="h-40" />}>
                              <CodeEditor
                                value={file.content}
                                onChange={(content) =>
                                  set({ files: files.map((x, j) => (j === i ? { ...x, content } : x)) })
                                }
                                fileName={file.path}
                                label={`Content of ${file.path || `file ${i + 1}`}`}
                                readOnly={!editable}
                                minHeight="10rem"
                              />
                            </Suspense>
                          </div>
                        ))}
                        <p className="text-muted-foreground text-sm">
                          The first file is the test unless a parameter names another. Up to 512 KB in all.
                        </p>
                        <Button
                          variant="outline"
                          onClick={() =>
                            set({
                              files: [...files, { path: files.length ? "" : fileNameFor(tmpl), content: "" }],
                            })
                          }
                        >
                          <Plus /> Add a file
                        </Button>
                      </div>
                    )}
                  </>
                );
              }}
            </Section>

            <Section {...s} id="settings-test-data" title="Test data" fields={["testData"]}>
              {(f, set) => (
                <TestDataRows rows={f.testData ?? []} onChange={(testData) => set({ testData })} />
              )}
            </Section>

            <Section {...s} id="settings-parameters" title="Parameters" fields={["params"]}>
              {(f, set, errors) => {
                const tmpl = tmplOf(f);
                return tmpl ? (
                  <ParamFields
                    tmpl={tmpl}
                    params={f.params}
                    onChange={(params) => set({ params })}
                    errors={(p) => errors.filter((e) => e.startsWith(p.name + ":"))}
                  />
                ) : (
                  <p className="text-muted-foreground text-sm">
                    {f.template
                      ? "The tool catalog doesn't list this Test's template."
                      : "An own image has no parameters."}{" "}
                    Its arguments can use <code className="font-mono">{"{{ config.name }}"}</code> — edit them
                    in YAML.
                  </p>
                );
              }}
            </Section>

            <Section
              {...s}
              id="settings-container"
              title="Container"
              fields={["image", "command", "args", "env"]}
            >
              {(f, set) => (
                <>
                  <TextField
                    id="image"
                    label="Image"
                    hint={f.template ? "empty: the template's" : "required"}
                    mono
                    value={f.image}
                    onChange={(image) => set({ image })}
                  />
                  <TextField
                    id="command"
                    label="Command"
                    hint={f.template ? "one item per line; empty: the template's" : "one item per line"}
                    rows={2}
                    value={f.command}
                    onChange={(command) => set({ command })}
                  />
                  <TextField
                    id="args"
                    label="Arguments"
                    hint="one per line; {{ config.name }} works"
                    rows={3}
                    value={f.args}
                    onChange={(args) => set({ args })}
                  />
                  <TextField
                    id="env"
                    label="Environment"
                    hint="NAME=value per line"
                    rows={3}
                    value={f.env}
                    onChange={(env) => set({ env })}
                  />
                </>
              )}
            </Section>

            <Section
              {...s}
              id="settings-resources"
              title="Resources"
              fields={["cpuRequest", "memoryRequest", "cpuLimit", "memoryLimit"]}
            >
              {(f, set) => (
                <div className="grid gap-4 sm:grid-cols-2">
                  <TextField
                    id="cpu-request"
                    label="CPU request"
                    value={f.cpuRequest}
                    placeholder="500m"
                    onChange={(cpuRequest) => set({ cpuRequest })}
                  />
                  <TextField
                    id="memory-request"
                    label="Memory request"
                    value={f.memoryRequest}
                    placeholder="512Mi"
                    onChange={(memoryRequest) => set({ memoryRequest })}
                  />
                  <TextField
                    id="cpu-limit"
                    label="CPU limit"
                    value={f.cpuLimit}
                    placeholder="2"
                    onChange={(cpuLimit) => set({ cpuLimit })}
                  />
                  <TextField
                    id="memory-limit"
                    label="Memory limit"
                    value={f.memoryLimit}
                    placeholder="2Gi"
                    onChange={(memoryLimit) => set({ memoryLimit })}
                  />
                </div>
              )}
            </Section>

            <Section
              {...s}
              id="settings-pod"
              title="Pod"
              fields={["podAnnotations", "podLabels", "serviceAccount"]}
            >
              {(f, set) => (
                <>
                  <TextField
                    id="pod-annotations"
                    label="Pod annotations"
                    hint="key=value per line, e.g. sidecar.istio.io/inject=false"
                    rows={3}
                    value={f.podAnnotations}
                    onChange={(podAnnotations) => set({ podAnnotations })}
                  />
                  <TextField
                    id="pod-labels"
                    label="Pod labels"
                    hint="key=value per line"
                    rows={3}
                    value={f.podLabels}
                    onChange={(podLabels) => set({ podLabels })}
                  />
                  <TextField
                    id="service-account"
                    label="Service account"
                    hint="empty: default"
                    value={f.serviceAccount}
                    onChange={(serviceAccount) => set({ serviceAccount })}
                  />
                </>
              )}
            </Section>

            <section
              className="bg-card space-y-4 rounded-xl border p-5"
              aria-labelledby="settings-schedule-title"
              id="settings-schedule"
            >
              <h2 id="settings-schedule-title" className="text-lg font-semibold">
                Schedule
              </h2>
              <SchedulePanel key={schedule?.expr ?? ""} k={k} schedule={schedule} editable={editable} />
            </section>

            <Section {...s} id="settings-timeout" title="Timeout" fields={["timeout"]}>
              {(f, set) => (
                <TextField
                  id="timeout"
                  label="Timeout"
                  hint="e.g. 15m, 1h30m; empty: the template's or none"
                  value={f.timeout}
                  placeholder="30m"
                  onChange={(timeout) => set({ timeout })}
                />
              )}
            </Section>
          </>
        )}
      </div>
      <aside className="lg:sticky lg:top-4 lg:self-start">
        <YamlSide
          k={k}
          base={base}
          built={built}
          editing={editingYaml}
          yaml={yamlDraft ?? ""}
          onEdit={(on) => setYamlDraft(on ? (base.form.yaml ?? "") : undefined)}
          onYaml={setYamlDraft}
          canEdit={editable}
          blocked={Object.keys(drafts).length > 0}
        />
      </aside>
    </div>
  );
}
