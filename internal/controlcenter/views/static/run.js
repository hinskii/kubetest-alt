// Run page: show the stored log and, while the run is live, poll for what
// was added (the server answers from ?offset on, with the run's phase).
// When the phase changes (queued → running, running → passed) the page
// reloads to show it; while queued it also reloads every 15 s so "Now:"
// (the newest Kubernetes event — pulling the image, waiting for a node)
// stays current.
(function () {
  var pre = document.getElementById("log");
  if (!pre) return;
  var src = pre.getAttribute("data-src");
  var live = pre.getAttribute("data-live") === "true";
  var shownPhase = pre.getAttribute("data-phase") || "";
  if (live && (shownPhase === "queued" || shownPhase === "")) {
    setTimeout(function () { window.location.reload(); }, 15000);
  }
  var offset = 0;
  var finalPhases = { passed: true, failed: true, error: true, aborted: true };
  var placeholder = true; // pre shows a status line, not log text

  function atBottom() {
    return pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 20;
  }

  function poll() {
    fetch(src + "?offset=" + offset, { credentials: "same-origin", cache: "no-store" })
      .then(function (resp) {
        if (!resp.ok) throw new Error("HTTP " + resp.status);
        var phase = resp.headers.get("X-Run-Phase");
        var next = parseInt(resp.headers.get("X-Next-Offset") || "0", 10);
        var more = resp.headers.get("X-More") === "1";
        return resp.text().then(function (text) {
          var follow = atBottom();
          if (text) {
            if (placeholder) {
              pre.textContent = "";
              placeholder = false;
            }
            pre.appendChild(document.createTextNode(text));
          } else if (placeholder) {
            pre.textContent = live ? "Waiting for output…" : "No log stored.";
          }
          if (follow) pre.scrollTop = pre.scrollHeight;
          offset = next;
          if (more) return poll();
          if (live && phase && phase !== shownPhase) {
            window.location.reload();
            return;
          }
          if (live) setTimeout(poll, 2000);
        });
      })
      .catch(function (err) {
        if (placeholder) pre.textContent = "Log unavailable: " + err.message;
        if (live) setTimeout(poll, 5000);
      });
  }
  poll();
})();
