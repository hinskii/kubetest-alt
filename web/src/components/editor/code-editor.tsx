import { javascript } from "@codemirror/lang-javascript";
import { python } from "@codemirror/lang-python";
import { xml } from "@codemirror/lang-xml";
import { yaml } from "@codemirror/lang-yaml";
import { lintGutter, setDiagnostics, type Diagnostic } from "@codemirror/lint";
import { indentUnit } from "@codemirror/language";
import { githubDark, githubLight } from "@uiw/codemirror-theme-github";
import CodeMirror, { EditorView, type Extension, type ReactCodeMirrorRef } from "@uiw/react-codemirror";
import { useEffect, useMemo, useRef } from "react";

import { useTheme } from "@/lib/theme";

/** languageOf picks the editor's language from a file name. */
export function languageOf(fileName: string): Extension[] {
  const ext = fileName.toLowerCase().split(".").pop() ?? "";
  switch (ext) {
    case "js":
    case "mjs":
    case "cjs":
      return [javascript()];
    case "ts":
      return [javascript({ typescript: true })];
    case "py":
      return [python()];
    case "xml":
    case "jmx":
      return [xml()];
    case "yaml":
    case "yml":
      return [yaml()];
    default:
      return [];
  }
}

/** linesOf finds "line N" in server messages (YAML parse, strict decode). */
export function linesOf(messages: string[]): { line: number; message: string }[] {
  const out: { line: number; message: string }[] = [];
  for (const m of messages) {
    const hit = /\bline (\d+)\b/.exec(m);
    if (hit) out.push({ line: Number(hit[1]), message: m });
  }
  return out;
}

/**
 * CodeEditor: CodeMirror for a file or YAML — line numbers, folding, search,
 * Tab = two spaces, undo; the server's errors marked at their lines.
 */
export default function CodeEditor({
  value,
  onChange,
  fileName,
  label,
  errors = [],
  minHeight = "16rem",
}: {
  value: string;
  onChange: (v: string) => void;
  fileName: string;
  label: string;
  errors?: string[];
  minHeight?: string;
}) {
  const theme = useTheme();
  const ref = useRef<ReactCodeMirrorRef>(null);
  const extensions = useMemo(
    () => [...languageOf(fileName), indentUnit.of("  "), lintGutter(), EditorView.lineWrapping],
    [fileName],
  );
  useEffect(() => {
    const view = ref.current?.view;
    if (!view) return;
    const doc = view.state.doc;
    const diagnostics: Diagnostic[] = linesOf(errors)
      .filter((e) => e.line >= 1 && e.line <= doc.lines)
      .map((e) => {
        const line = doc.line(e.line);
        return { from: line.from, to: line.to, severity: "error", message: e.message };
      });
    view.dispatch(setDiagnostics(view.state, diagnostics));
  }, [errors, value]);
  return (
    <CodeMirror
      ref={ref}
      value={value}
      onChange={onChange}
      theme={theme === "dark" ? githubDark : githubLight}
      extensions={extensions}
      minHeight={minHeight}
      aria-label={label}
      basicSetup={{ tabSize: 2 }}
      className="overflow-hidden rounded-lg border text-sm"
    />
  );
}
