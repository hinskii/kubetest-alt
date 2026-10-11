import { useState, type ReactNode } from "react";

import type { Param, TestForm, ToolTemplate } from "@/api/client";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";

// Fields the wizard and the Test's Settings share. Errors come from the
// API's build ("<Label>: what is wrong") and sit beside their field.

/** errorsFor: the build's errors that belong to a field (by its label). */
export function errorsFor(errors: string[], ...labels: string[]): string[] {
  return errors.filter((e) => labels.some((l) => e.startsWith(l + ":") || e.startsWith(l + " ")));
}

export function Field({
  id,
  label,
  hint,
  errors = [],
  children,
}: {
  id: string;
  label: string;
  hint?: ReactNode;
  errors?: string[];
  children: ReactNode;
}) {
  return (
    <div className="space-y-1.5">
      <Label htmlFor={id}>
        {label} {hint && <span className="text-muted-foreground font-normal">{hint}</span>}
      </Label>
      {children}
      {errors.map((e) => (
        <p key={e} className="text-destructive text-sm">
          {e}
        </p>
      ))}
    </div>
  );
}

/** TextField: one line of text, or several (rows) in a monospace box. */
export function TextField({
  id,
  label,
  hint,
  value,
  onChange,
  errors,
  placeholder,
  rows,
  mono,
  onBlur,
}: {
  id: string;
  label: string;
  hint?: ReactNode;
  value?: string;
  onChange: (v: string) => void;
  errors?: string[];
  placeholder?: string;
  rows?: number;
  mono?: boolean;
  onBlur?: () => void;
}) {
  return (
    <Field id={id} label={label} hint={hint} errors={errors}>
      {rows ? (
        <Textarea
          id={id}
          rows={rows}
          className="font-mono"
          value={value ?? ""}
          placeholder={placeholder}
          onChange={(e) => onChange(e.target.value)}
          onBlur={onBlur}
        />
      ) : (
        <Input
          id={id}
          className={cn(mono && "font-mono")}
          value={value ?? ""}
          placeholder={placeholder}
          autoComplete="off"
          onChange={(e) => onChange(e.target.value)}
          onBlur={onBlur}
        />
      )}
    </Field>
  );
}

/** ParamFields: a template's parameters, typed (enum and boolean as lists). */
export function ParamFields({
  tmpl,
  params,
  onChange,
  errors,
  onBlur,
}: {
  tmpl: ToolTemplate;
  params?: Record<string, string>;
  onChange: (params: Record<string, string>) => void;
  errors: (p: Param) => string[];
  onBlur?: (p: Param) => void;
}) {
  if (tmpl.params.length === 0) {
    return <p className="text-muted-foreground text-sm">{tmpl.name} has no parameters.</p>;
  }
  return (
    <>
      <p className="text-muted-foreground text-sm">
        Parameters of {tmpl.name} — a run can still override them.
      </p>
      {tmpl.params.map((p) => {
        const id = `param-${p.name}`;
        const value = params?.[p.name] ?? "";
        const setValue = (v: string) => onChange({ ...params, [p.name]: v });
        const main = p.name === tmpl.mainParam;
        return (
          <Field
            key={p.name}
            id={id}
            label={p.name}
            hint={
              main
                ? "where the tests are — empty: the path in the repository, or the pasted script"
                : `${p.type}${p.required ? ", set at run time if empty" : ""}`
            }
            errors={errors(p)}
          >
            {p.enum?.length || p.type === "boolean" ? (
              <Select value={value || p.default || ""} onValueChange={setValue}>
                <SelectTrigger id={id} className="w-full">
                  <SelectValue placeholder="—" />
                </SelectTrigger>
                <SelectContent>
                  {(p.enum?.length ? p.enum : ["true", "false"]).map((v) => (
                    <SelectItem key={v} value={v}>
                      {v}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            ) : (
              <Input
                id={id}
                className={cn(main && "font-mono")}
                type={p.type === "integer" || p.type === "number" ? "number" : "text"}
                step={p.type === "number" ? "any" : undefined}
                value={value}
                placeholder={p.default}
                onChange={(e) => setValue(e.target.value)}
                onBlur={() => onBlur?.(p)}
              />
            )}
            {p.description && !main && <p className="text-muted-foreground text-sm">{p.description}</p>}
          </Field>
        );
      })}
    </>
  );
}

/**
 * GitFields: repository, path, branch and — behind "Private repository?" —
 * the token's Secret. advanced adds the sparse paths.
 */
export function GitFields({
  form,
  set,
  tmpl,
  errors,
  onBlur,
  advanced,
}: {
  form: TestForm;
  set: (patch: Partial<TestForm>) => void;
  tmpl?: ToolTemplate;
  errors: (key: string, ...labels: string[]) => string[];
  onBlur?: (key: string) => void;
  advanced?: boolean;
}) {
  const [privateRepo, setPrivateRepo] = useState(!!form.gitSecret);
  const project = tmpl?.mainKind === "directory";
  return (
    <>
      {project && (
        <p className="text-muted-foreground text-sm">
          {tmpl.name} runs a project — its code comes from a Git repository.
        </p>
      )}
      <TextField
        id="git-uri"
        label="Repository"
        value={form.gitUri}
        placeholder="https://github.com/org/repo"
        onChange={(gitUri) => set({ gitUri })}
        onBlur={() => onBlur?.("gitUri")}
        errors={errors("gitUri", "Git repository")}
      />
      <TextField
        id="git-path"
        label="Path in the repository"
        hint={project ? "the folder with your tests; “.” for the root" : "the test file or its folder"}
        mono
        value={form.gitPath}
        placeholder={project ? "e2e" : "perf/checkout.js"}
        onChange={(gitPath) => set({ gitPath })}
        onBlur={() => onBlur?.("gitPath")}
        errors={errors("gitPath", "Path in the repository")}
      />
      <TextField
        id="git-revision"
        label="Branch"
        hint="or a tag or commit; empty: the default branch"
        value={form.gitRevision}
        placeholder="main"
        onChange={(gitRevision) => set({ gitRevision })}
      />
      {advanced && (
        <TextField
          id="git-paths"
          label="Sparse paths"
          hint="advanced, one per line; empty: from the path above"
          rows={2}
          value={form.gitPaths}
          onChange={(gitPaths) => set({ gitPaths })}
        />
      )}
      <div className="flex items-center gap-2">
        <Checkbox
          id="private-repo"
          checked={privateRepo}
          onCheckedChange={(v) => {
            setPrivateRepo(v === true);
            if (v !== true) set({ gitSecret: "", gitSecretKey: "" });
          }}
        />
        <Label htmlFor="private-repo" className="font-normal">
          Private repository?
        </Label>
      </div>
      {privateRepo && (
        <>
          <TextField
            id="git-secret"
            label="Secret with the token"
            hint="in the Test's namespace"
            value={form.gitSecret}
            placeholder="git-token"
            onChange={(gitSecret) => set({ gitSecret })}
          />
          <TextField
            id="git-secret-key"
            label="Key in the Secret"
            hint="empty: token"
            value={form.gitSecretKey}
            placeholder="token"
            onChange={(gitSecretKey) => set({ gitSecretKey })}
          />
        </>
      )}
    </>
  );
}
