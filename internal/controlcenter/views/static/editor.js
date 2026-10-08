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

  // The chosen template's parameters only; the image hint follows it.
  var radios = Array.prototype.slice.call(form.querySelectorAll('input[name="template"]'));
  var image = form.querySelector('input[name="image"]');
  var hint = form.querySelector('[data-hint="image"]');
  function chooseTemplate() {
    var chosen = radios.filter(function (r) { return r.checked; })[0];
    var name = chosen ? chosen.value : "";
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
  radios.forEach(function (r) { r.addEventListener("change", chooseTemplate); });
  chooseTemplate();

  // Sections behind a checkbox (git, files).
  form.querySelectorAll("[data-toggles]").forEach(function (box) {
    var target = form.querySelector('[data-toggled="' + box.getAttribute("data-toggles") + '"]');
    function sync() { target.hidden = !box.checked; }
    box.addEventListener("change", sync);
    sync();
  });

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
  form.querySelectorAll("[data-copy]").forEach(function (btn) {
    var src = document.getElementById(btn.getAttribute("data-copy"));
    if (!src || !navigator.clipboard) {
      return;
    }
    btn.hidden = false;
    btn.addEventListener("click", function () {
      navigator.clipboard.writeText(src.textContent).then(function () {
        btn.textContent = "Copied";
        setTimeout(function () { btn.textContent = "Copy YAML"; }, 1500);
      });
    });
  });
})();
