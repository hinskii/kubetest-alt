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

// Tool frames (live view, report) in the dark theme: inverted to match,
// or the tool's original colors — the choice is remembered.
(function () {
  var buttons = document.querySelectorAll("[data-frame-colors]");
  if (!buttons.length) return;
  var original = false;
  try { original = localStorage.getItem("frame-colors") === "original"; } catch (e) { /* storage blocked */ }
  var root = document.documentElement;
  function apply() {
    var dark = root.getAttribute("data-theme") === "dark";
    document.querySelectorAll(".tool-frame").forEach(function (f) { f.classList.toggle("original", original); });
    buttons.forEach(function (b) {
      b.hidden = !dark;
      b.textContent = original ? "Match the dark theme" : "Original colors";
    });
  }
  buttons.forEach(function (b) {
    b.addEventListener("click", function () {
      original = !original;
      try { localStorage.setItem("frame-colors", original ? "original" : "match"); } catch (e) { /* storage blocked */ }
      apply();
    });
  });
  // The theme toggle in the top bar changes data-theme.
  new MutationObserver(apply).observe(root, { attributes: true, attributeFilter: ["data-theme"] });
  apply();
})();
