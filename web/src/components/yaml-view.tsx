import { yaml } from "@codemirror/lang-yaml";
import { githubDark, githubLight } from "@uiw/codemirror-theme-github";
import CodeMirror, { EditorView } from "@uiw/react-codemirror";

import { useTheme } from "@/lib/theme";

const extensions = [yaml(), EditorView.lineWrapping];

/** YamlView shows YAML read-only, highlighted (CodeMirror, github themes). */
export default function YamlView({ value, label }: { value: string; label: string }) {
  const theme = useTheme();
  return (
    <CodeMirror
      value={value.trimEnd()}
      editable={false}
      readOnly
      theme={theme === "dark" ? githubDark : githubLight}
      extensions={extensions}
      basicSetup={{ foldGutter: true, highlightActiveLine: false, highlightActiveLineGutter: false }}
      aria-label={label}
      className="overflow-hidden rounded-lg border text-sm"
    />
  );
}
