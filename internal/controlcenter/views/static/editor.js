// The test editor: one step at a time, the chosen template's parameters
// only, more file rows, copying the YAML. Without JavaScript every step
// shows at once and the form still works — the server does the rest.
(function () {
  "use strict";
  var form = document.getElementById("wizard");
  if (!form || form.getAttribute("data-mode") !== "form") {
    return;
  }
  var steps = Array.prototype.slice.call(form.querySelectorAll(".wizard-step"));
  var crumbs = Array.prototype.slice.call(form.querySelectorAll(".steps li"));
  var back = form.querySelector('[data-nav="back"]');
  var next = form.querySelector('[data-nav="next"]');
  var review = form.querySelector('[data-nav="review"]');
  var current = parseInt(form.getAttribute("data-step"), 10) || 1;

  function show(n) {
    current = Math.max(1, Math.min(steps.length, n));
    steps.forEach(function (s) {
      s.hidden = parseInt(s.getAttribute("data-step"), 10) !== current;
    });
    crumbs.forEach(function (c) {
      var k = parseInt(c.getAttribute("data-goto"), 10);
      c.classList.toggle("current", k === current);
      c.classList.toggle("done", k < current);
    });
    back.hidden = current === 1;
    next.hidden = current >= steps.length - 1;
    // "Review" builds the YAML on the server; on the review step itself
    // it checks again after edits.
    review.textContent = current === steps.length ? "Check again" : "Review";
    review.hidden = current < steps.length - 1;
  }
  back.addEventListener("click", function () { show(current - 1); });
  next.addEventListener("click", function () { show(current + 1); });
  crumbs.forEach(function (c) {
    c.addEventListener("click", function () { show(parseInt(c.getAttribute("data-goto"), 10)); });
  });
  form.classList.add("js");
  show(current);

  // The chosen tool's parameters only; the image hint follows it.
  var tool = document.getElementById("tool");
  var toolHint = document.getElementById("tool-hint");
  var image = form.querySelector('input[name="image"]');
  var hint = form.querySelector('[data-hint="image"]');
  function chosenTool() {
    return tool && tool.selectedIndex >= 0 ? tool.options[tool.selectedIndex] : null;
  }
  function chooseTemplate() {
    var chosen = chosenTool();
    var name = chosen ? chosen.value : "";
    if (toolHint) {
      toolHint.textContent = chosen && chosen.getAttribute("data-image") ? "image " + chosen.getAttribute("data-image") : "";
    }
    form.querySelectorAll("fieldset.params").forEach(function (fs) {
      var mine = fs.getAttribute("data-template") === name;
      fs.hidden = !mine;
      fs.disabled = !mine;
    });
    if (image) {
      image.placeholder = (chosen && chosen.getAttribute("data-image")) || "registry/image:tag";
    }
    if (hint) {
      hint.textContent = name ? "empty: the template's" : "required";
    }
  }
  if (tool) tool.addEventListener("change", chooseTemplate);
  chooseTemplate();

  // The code's source: git, inline files or none — one at a time. A
  // template that runs a project (main path a directory) takes git only.
  var sources = Array.prototype.slice.call(form.querySelectorAll('input[name="source"]'));
  var inlineChoice = document.getElementById("source-inline");
  var projectNote = document.getElementById("project-note");
  function syncSource() {
    var chosen = chosenTool();
    var project = chosen && chosen.getAttribute("data-main-kind") === "directory";
    var inline = inlineChoice && inlineChoice.querySelector("input");
    if (inline) {
      inline.disabled = project;
      if (project && inline.checked) {
        sources.filter(function (r) { return r.value === "git"; })[0].checked = true;
      }
    }
    if (projectNote) projectNote.hidden = !project;
    var current = (sources.filter(function (r) { return r.checked; })[0] || {}).value || "none";
    form.querySelectorAll("[data-source]").forEach(function (el) {
      el.hidden = el.getAttribute("data-source") !== current;
    });
  }
  sources.forEach(function (r) { r.addEventListener("change", syncSource); });
  if (tool) tool.addEventListener("change", syncSource);
  syncSource();

  // The YAML beside the form, rebuilt as you type: the server builds it
  // with the code Create uses (no copy of the rules here). With problems
  // it answers 422 and the problems; the last good YAML stays.
  var panel = document.getElementById("yaml-panel");
  var live = document.getElementById("yaml-live");
  if (panel && live && window.fetch) {
    var timer = null;
    var seq = 0;
    var update = function () {
      var mine = ++seq;
      var body = new URLSearchParams(new FormData(form));
      body.delete("action");
      fetch(panel.getAttribute("data-src"), {
        method: "POST", credentials: "same-origin", body: body,
        headers: { "Content-Type": "application/x-www-form-urlencoded" },
      }).then(function (resp) {
        return resp.text().then(function (html) { return { ok: resp.ok, status: resp.status, html: html }; });
      }).then(function (res) {
        if (mine !== seq) return; // a newer edit is on its way
        var fresh = document.createElement("div");
        fresh.innerHTML = res.html;
        if (res.ok) {
          live.innerHTML = fresh.innerHTML;
        } else if (res.status === 422) {
          live.querySelectorAll(".panel-errors, .panel-warnings").forEach(function (el) { el.remove(); });
          Array.prototype.slice.call(fresh.querySelectorAll(".panel-errors, .panel-warnings")).reverse().forEach(function (el) {
            live.insertBefore(el, live.firstChild);
          });
        }
        var copy = panel.querySelector("[data-copy]");
        if (copy) copy.hidden = !document.getElementById("yaml-live-raw");
      }).catch(function () { /* keep what is shown */ });
    };
    var schedule = function (e) {
      if (e && e.target && e.target.name === "yaml") return;
      clearTimeout(timer);
      timer = setTimeout(update, 300);
    };
    form.addEventListener("input", schedule);
    form.addEventListener("change", schedule);
    update();
  }

  // A test-data row shows the field its "Where" needs: a directory, or
  // the key=path lines — nothing for the default directory.
  function syncWhere(row) {
    var where = row.querySelector('select[name="td.where"]').value;
    row.querySelectorAll("[data-where]").forEach(function (el) {
      el.hidden = el.getAttribute("data-where") !== where;
    });
  }
  form.addEventListener("change", function (e) {
    if (e.target.name === "td.where") syncWhere(e.target.closest(".data-row"));
  });
  form.querySelectorAll(".data-row").forEach(syncWhere);

  // More test-data rows.
  var data = document.getElementById("test-data");
  var addData = document.getElementById("add-data");
  if (data && addData) {
    addData.hidden = false;
    addData.addEventListener("click", function () {
      var rows = data.querySelectorAll(".data-row");
      var row = rows[rows.length - 1].cloneNode(true);
      row.querySelectorAll("input, textarea").forEach(function (el) { el.value = ""; });
      row.querySelectorAll("select").forEach(function (el) { el.selectedIndex = 0; });
      data.insertBefore(row, addData);
      syncWhere(row);
      row.querySelector('input[name="td.name"]').focus();
    });
  }

  // Another namespace means another catalog: offer to load it.
  var ns = form.querySelector('input[name="namespace"][data-loaded]');
  var load = document.getElementById("load-catalog");
  if (ns && load) {
    function syncLoad() { load.hidden = ns.value === ns.getAttribute("data-loaded") && ns.value !== ""; }
    ns.addEventListener("input", syncLoad);
    syncLoad();
  }

  // More file rows.
  var files = document.getElementById("files");
  var add = document.getElementById("add-file");
  if (files && add) {
    add.hidden = false;
    add.addEventListener("click", function () {
      var rows = files.querySelectorAll(".file-row");
      var row = rows[rows.length - 1].cloneNode(true);
      row.querySelectorAll("input, textarea").forEach(function (el) { el.value = ""; });
      files.insertBefore(row, add);
      row.querySelector("input").focus();
    });
  }

  // The main path parameter follows the path in the repository unless
  // typed: show what it will be.
  var gitPath = document.getElementById("git-path");
  function mirrorPath() {
    form.querySelectorAll("[data-main-path]").forEach(function (el) {
      el.placeholder = gitPath && gitPath.value ? gitPath.value : "";
    });
  }
  if (gitPath) {
    gitPath.addEventListener("input", mirrorPath);
    mirrorPath();
  }

  // Copy the YAML for Git.
  document.querySelectorAll("[data-copy]").forEach(function (btn) {
    if (!navigator.clipboard) return;
    var label = btn.textContent;
    // Looked up on click: the live panel replaces its YAML as you type.
    var source = function () { return document.getElementById(btn.getAttribute("data-copy")); };
    btn.hidden = !source();
    btn.addEventListener("click", function () {
      var src = source();
      if (!src) return;
      navigator.clipboard.writeText(src.value !== undefined ? src.value : src.textContent).then(function () {
        btn.textContent = "Copied";
        setTimeout(function () { btn.textContent = label; }, 1500);
      });
    });
  });
})();
