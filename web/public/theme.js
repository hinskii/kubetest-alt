// Light/dark theme before first paint: the saved choice, else the OS
// preference. A classic script in <head> (blocking) so the page never
// flashes the wrong theme; CSP allows no inline script. src/lib/theme.ts
// takes over once React runs.
(function () {
  var root = document.documentElement;
  var saved = null;
  try {
    saved = localStorage.getItem("theme");
  } catch {
    /* storage blocked */
  }
  var prefersDark = window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches;
  root.setAttribute("data-theme", saved || (prefersDark ? "dark" : "light"));
})();
