import { useSyncExternalStore } from "react";

// The theme lives on <html data-theme> (set before first paint by
// index.html) and in localStorage "theme" — the same key the
// server-rendered pages use, so both kinds of page agree.

export type Theme = "light" | "dark";

const listeners = new Set<() => void>();

export function getTheme(): Theme {
  return document.documentElement.getAttribute("data-theme") === "dark" ? "dark" : "light";
}

export function setTheme(theme: Theme) {
  document.documentElement.setAttribute("data-theme", theme);
  try {
    localStorage.setItem("theme", theme);
  } catch {
    // storage blocked: the choice lasts for this page only
  }
  listeners.forEach((l) => l());
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function useTheme(): Theme {
  return useSyncExternalStore(subscribe, getTheme);
}
