// Keeps lists current without a reload: every element with data-refresh
// and an id is replaced by its counterpart from a fresh copy of the same
// page, fetched in the background while the tab is visible. Only what
// changed is touched — filters, forms, scroll position stay. Without
// JavaScript, or when the fetch fails (e.g. the session expired), the
// page simply stays as it was.
(function () {
  "use strict";
  var INTERVAL = 10000;
  var parts = document.querySelectorAll("[data-refresh][id]");
  if (!parts.length || !window.fetch || !window.DOMParser) return;
  var busy = false;

  function refresh() {
    if (busy || document.hidden) return;
    busy = true;
    fetch(window.location.href, { credentials: "same-origin", cache: "no-store", redirect: "error" })
      .then(function (resp) {
        if (!resp.ok) throw new Error("HTTP " + resp.status);
        return resp.text();
      })
      .then(function (html) {
        var fresh = new DOMParser().parseFromString(html, "text/html");
        document.querySelectorAll("[data-refresh][id]").forEach(function (el) {
          var next = fresh.getElementById(el.id);
          // Something being typed into or focused inside stays put.
          if (!next || el.contains(document.activeElement) && document.activeElement !== document.body) return;
          if (next.innerHTML !== el.innerHTML) el.innerHTML = next.innerHTML;
        });
      })
      .catch(function () { /* keep what is shown */ })
      .then(function () { busy = false; });
  }

  setInterval(refresh, INTERVAL);
  // Back to the tab: catch up at once.
  document.addEventListener("visibilitychange", function () { if (!document.hidden) refresh(); });
})();
