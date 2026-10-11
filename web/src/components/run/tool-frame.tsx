import { useState } from "react";

import { Button } from "@/components/ui/button";
import { useTheme } from "@/lib/theme";

const KEY = "frame-colors"; // shared with the server-rendered pages

function savedOriginal(): boolean {
  try {
    return localStorage.getItem(KEY) === "original";
  } catch {
    return false;
  }
}

/**
 * useFrameColors: whether tool frames show their original colours in the
 * dark theme (remembered), and the button that switches it.
 */
export function useFrameColors() {
  const theme = useTheme();
  const [original, setOriginal] = useState(savedOriginal);
  const toggle = () => {
    const next = !original;
    setOriginal(next);
    try {
      localStorage.setItem(KEY, next ? "original" : "match");
    } catch {
      // storage blocked: for this page only
    }
  };
  const button =
    theme === "dark" ? (
      <Button variant="outline" size="sm" onClick={toggle}>
        {original ? "Match the dark theme" : "Original colors"}
      </Button>
    ) : null;
  return { original, button };
}

/**
 * ToolFrame shows a tool's page (report, live dashboard) sandboxed: its
 * scripts run in an opaque origin, apart from Control Center.
 */
export function ToolFrame({ src, title, original }: { src: string; title: string; original: boolean }) {
  return (
    <div className="tool-frame" data-original={original || undefined}>
      <iframe src={src} title={title} sandbox="allow-scripts" referrerPolicy="no-referrer" />
    </div>
  );
}
