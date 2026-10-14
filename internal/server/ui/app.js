"use strict";

// Text that came from outside the server (tickets, model answers) is only
// ever added to the page as text nodes, never as HTML.

const REFRESH_MS = 3000;

// Sizes of the drawing, in pixels before it is scaled to fit the window. The
// workflow flows from left to right: a column per depth, a row per branch end.
const CARD_H = 68;
const CARD_W = 216;
const END_W = 188;   // the way out is a little smaller
const END_H = 56;
const COL = 360;     // distance between columns, which leaves room for a condition on each line
const ROW = 112;     // distance between rows, which leaves room for the line of result under a node
const PAD = 36;
const HANDLE = 56;   // room to the right of the last column for the plus
const ZOOM_MIN = 0.3;
const ZOOM_MAX = 1.5;

const ICONS = {
  trigger: ["M13 2 4 14h7l-1 8 9-12h-7z"],
  decide: ["M6 4a2 2 0 1 0 0 4 2 2 0 0 0 0-4z", "M6 16a2 2 0 1 0 0 4 2 2 0 0 0 0-4z", "M18 7a2 2 0 1 0 0 4 2 2 0 0 0 0-4z", "M6 8v8", "M18 11a6 6 0 0 1-6 6H8"],
  ai: ["M12 3l1.8 4.7 4.7 1.8-4.7 1.8L12 16l-1.8-4.7-4.7-1.8 4.7-1.8z", "M18.5 14.5l.8 2.2 2.2.8-2.2.8-.8 2.2-.8-2.2-2.2-.8 2.2-.8z"],
  http: ["M22 2 11 13", "M22 2 15 22l-4-9-9-4z"],
  flow: ["M21 8 12 3 3 8v8l9 5 9-5z", "M3 8l9 5 9-5", "M12 13v8"],
  moon: ["M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"],
  sun: ["M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8z", "M12 2.5v2.2", "M12 19.3v2.2", "M2.5 12h2.2", "M19.3 12h2.2", "M5.3 5.3l1.6 1.6", "M17.1 17.1l1.6 1.6", "M5.3 18.7l1.6-1.6", "M17.1 6.9l1.6-1.6"],
  runs: ["M4 6h16", "M4 12h10", "M4 18h13"],
  tests: ["M9 3h6", "M10 3v6l-5 9a2 2 0 0 0 2 3h10a2 2 0 0 0 2-3l-5-9V3"],
  chev: ["M6 9l6 6 6-6"],
  chevron: ["M9 6l6 6-6 6"],
  workflow: ["M4 6h6v5H4z", "M14 13h6v5h-6z", "M10 8.5h3a2 2 0 0 1 2 2V13"],
  run: ["M7 4.5v15l12-7.5z"],
  ok: ["M5 12.5l4.5 4.5L19 7.5"],
  failed: ["M6 6l12 12", "M18 6L6 18"],
  skipped: ["M6 12h12"],
  pending: ["M12 5a7 7 0 1 0 0 14 7 7 0 0 0 0-14z"],
  running: ["M12 7v5l3.5 2"],
  close: ["M6 6l12 12", "M18 6L6 18"],
  copy: ["M9 9h10.5v11H9z", "M5.5 15.5V4H16"],
  branch: ["M6 3.5v13", "M6 20.5a2 2 0 1 0 0-4 2 2 0 0 0 0 4z", "M18 3.5a2 2 0 1 0 0 4 2 2 0 0 0 0-4z", "M18 7.5c0 5-12 3-12 9"],
  plus: ["M12 5v14", "M5 12h14"],
  trash: ["M5 7h14", "M10 7V4.5h4V7", "M7 7l.8 12h8.4L17 7", "M10.5 11v5", "M13.5 11v5"],
  minus: ["M5 12h14"],
  response: ["M9 14 4 9l5-5", "M4 9h10a6 6 0 0 1 6 6v5"],
  fit: ["M4 9V4h5", "M20 9V4h-5", "M4 15v5h5", "M20 15v5h-5"],
  // A component's two ends: an arrow into a bar, and an arrow out of one.
  input: ["M4 12h10", "M10.5 7.5 15 12l-4.5 4.5", "M19.5 5v14"],
  output: ["M4.5 5v14", "M9 12h10.5", "M15.5 7.5 20 12l-4.5 4.5"],
  component: ["M12 3 4 7.5v9L12 21l8-4.5v-9z", "M4 7.5 12 12l8-4.5", "M12 12v9"],
  open: ["M8 16 16 8", "M9.5 8H16v6.5"],
};

const KIND_LABEL = { trigger: "Trigger", decide: "Decide", ai: "AI", http: "HTTP", flow: "Flow", response: "Response", input: "Input", output: "Output" };
const KIND_NAMES = {
  trigger: "Trigger: starts the workflow",
  decide: "Decision step: a model answers typed questions",
  ai: "AI step: a model writes structured output",
  http: "HTTP step: sends a request",
  flow: "Flow step: runs a component made of steps",
  response: "Response: answers the caller",
  input: "Component input: what a step gives the component",
  output: "Component output: what it gives back to the step",
};

// A component is drawn like a workflow, with its own two ends. Their ids start
// with @, which no step id can, so they never meet a step called input.
const IN = "@input";
const OUT = "@output";
const isComp = (w) => !!w && !!w.component;
const START = (w) => (isComp(w) ? IN : "trigger");
const nodeName = (id) => (id === IN ? "input" : id === OUT ? "output" : id);
// What a step's reads name, as a node of the drawing.
const readNode = (w, name) => (isComp(w) && name === "input" ? IN : name);
const compTitle = (w) => (w && (w.title || w.name)) || "";
// The kind a component is drawn as: flow when it is made of steps, else its one step's kind.
const compKind = (c) => (c.gives ? "flow" : (c.steps[0] && c.steps[0].kind) || "flow");
const compKindWord = (c) => (c.gives ? "Made of " + plural(c.steps.length, "step") : (KIND_LABEL[compKind(c)] || compKind(c)) + " step");
const inputNames = (w) => (w.inputs || []).map((i) => i.name);
// The names of what a component gives back.
const givenNames = (w) => (w.gives ? w.gives.fields || [] : (w.steps[0] && w.steps[0].outputs) || []);
const usesOf = (w) => (w.usedBy || []).length;
const entryOf = (name) => state.workflows.find((x) => x.name === name) || null;
const STATUS_TEXT = { ok: "Succeeded", failed: "Failed", skipped: "Skipped", running: "Running", pending: "Not reached" };

// Which inner steps of a flow step the person has opened in the details, by run, step and inner step.
const openInner = new Set();

let state = { commit: "", protected: false, workflows: [], runs: [] };
let selected = { workflow: null, run: null, node: null, changes: false, add: null, test: null, make: false, edit: null, list: null };
let detail = null;      // the selected run in full, with trigger and outputs
let latestFull = null;  // the latest run of the selected workflow in full, drawn on it while no run is picked
let shown = "";         // what the page currently shows, to tell whether anything changed
let view = null;        // the workflow and run now drawn, for redrawing on resize and zoom
let zoom = null;        // null fits the drawing to the window
let lastKey = "";       // which workflow, run and step were shown last time
let enteredFor = null;  // the workflow whose cards last slid in
let afterRender = null; // something to do once the page has been drawn, such as moving the focus

const $ = (id) => document.getElementById(id);

// ---- theme: remembered in the browser, and the page works without it ----

const THEME_KEY = "flowmaker-theme";
function remembered(key) {
  try { return localStorage.getItem(key); } catch (err) { return null; }
}
function remember(key, value) {
  try { localStorage.setItem(key, value); } catch (err) { /* the choice just lasts until the page is closed */ }
}
const systemDark = () => !!(window.matchMedia && matchMedia("(prefers-color-scheme: dark)").matches);
// The theme in use: the one picked with the toggle, else the system's.
const currentTheme = () => document.documentElement.dataset.theme || (systemDark() ? "dark" : "light");
{
  const saved = remembered(THEME_KEY);
  if (saved === "dark" || saved === "light") document.documentElement.dataset.theme = saved;
}
let themeButton = null;
function paintTheme() {
  if (!themeButton) return;
  const dark = currentTheme() === "dark";
  themeButton.replaceChildren(icon(dark ? "sun" : "moon", 19));
  themeButton.title = dark ? "Switch to the light theme" : "Switch to the dark theme";
  themeButton.setAttribute("aria-label", themeButton.title);
}
function toggleTheme() {
  const next = currentTheme() === "dark" ? "light" : "dark";
  document.documentElement.dataset.theme = next;
  remember(THEME_KEY, next);
  paintTheme();
}
if (window.matchMedia) matchMedia("(prefers-color-scheme: dark)").addEventListener("change", paintTheme);

function railButton(name, label, onclick, pressed) {
  return el("button", { type: "button", class: "railbtn", "aria-label": label, title: label, "aria-pressed": pressed, onclick }, icon(name, 20));
}
// buildRail fills the rail under the logo: the workflows and the components,
// each a menu to pick one from, and the theme at the bottom. The runs and tests
// of the one picked are its tabs.
const railButtons = {};
function buildRail() {
  railButtons.workflows = railButton("workflow", "Workflows", (e) => openMenu("workflows", e.currentTarget), "false");
  railButtons.components = railButton("component", "Components", (e) => openMenu("components", e.currentTarget), "false");
  themeButton = railButton("moon", "Switch theme", toggleTheme);
  $("rail").append(railButtons.workflows, railButtons.components, el("span", { class: "grow" }), themeButton);
  paintTheme();
}

function append(node, children) {
  for (const child of children.flat(Infinity)) {
    if (child == null || child === false) continue;
    node.append(child.nodeType ? child : document.createTextNode(String(child)));
  }
  return node;
}

function el(tag, props, ...children) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(props || {})) {
    if (value == null || value === false) continue;
    if (key === "class") node.className = value;
    else if (key === "onclick") node.addEventListener("click", value);
    // Set through the style object: the page's policy blocks style attributes.
    else if (key === "css") Object.assign(node.style, value);
    else node.setAttribute(key, value === true ? "" : value);
  }
  return append(node, children);
}

function svg(tag, attrs, ...children) {
  const node = document.createElementNS("http://www.w3.org/2000/svg", tag);
  for (const [key, value] of Object.entries(attrs || {})) node.setAttribute(key, value);
  return append(node, children);
}

function icon(name, size) {
  const bold = name === "ok" || name === "failed" || name === "skipped";
  return svg("svg", {
      viewBox: "0 0 24 24", width: size || 18, height: size || 18, fill: "none", stroke: "currentColor",
      "stroke-width": bold ? 3 : 1.9, "stroke-linecap": "round", "stroke-linejoin": "round", "aria-hidden": "true",
    },
    (ICONS[name] || []).map((d) => svg("path", { d })));
}

function duration(ms) {
  if (ms < 1000) return ms + " ms";
  if (ms < 60000) return (ms / 1000).toFixed(1) + " s";
  return Math.floor(ms / 60000) + " min " + Math.round((ms % 60000) / 1000) + " s";
}

function when(started) {
  const date = new Date(started);
  const time = date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
  if (date.toDateString() === new Date().toDateString()) return time;
  return date.toLocaleDateString([], { month: "short", day: "numeric" }) + ", " + time;
}

const percent = (p) => (p * 100).toFixed(p >= 0.995 || p < 0.005 ? 1 : 0) + "%";
const isWebhook = (w) => (w.trigger || "").startsWith("POST ");

// The address is #/w/<workflow>, optionally followed by /r/<run> and /n/<step>,
// or /a/<step> while a step is being added after that step ("trigger" for the
// first place, "-" for the last). /t/<run> is the form that keeps a run as a
// test, and /t/- the form for a new test from nothing. /m/1 is the form for
// a new workflow, which belongs to no workflow: the one named stays selected
// behind it. /e/<step> is the node editor, the large dialog for one step; a
// step of a workflow this page can edit opens in it from /n/<step> too.
// /v/tests is the list of the workflow's tests, /v/runs the list of its runs.
function readAddress() {
  const parts = location.hash.replace(/^#\/?/, "").split("/").map(decodeURIComponent);
  const found = {};
  for (let i = 0; i + 1 < parts.length; i += 2) found[parts[i]] = parts[i + 1];
  selected = { workflow: found.w || null, run: found.r || null, node: found.n || null, changes: found.c === "1", add: found.a || null, test: found.t || null, make: found.m === "1", edit: found.e || null, list: found.v === "tests" || found.v === "runs" ? found.v : null };
}

function go(change) {
  const next = { ...selected, ...change };
  // Picking anything else leaves the changes view, unless it is asked for.
  if (!("changes" in change)) next.changes = false;
  // Nor does picking anything else keep the add-a-step form open.
  if (!("add" in change)) next.add = null;
  // Nor the form for a test.
  if (!("test" in change)) next.test = null;
  // Nor the form for a new workflow.
  if (!("make" in change)) next.make = false;
  // Nor the node editor.
  if (!("edit" in change)) next.edit = null;
  // Nor the list of tests or runs.
  if (!("list" in change)) next.list = null;
  let hash = "#/w/" + encodeURIComponent(next.workflow);
  if (next.run) hash += "/r/" + encodeURIComponent(next.run);
  if (next.node) hash += "/n/" + encodeURIComponent(next.node);
  if (next.add) hash += "/a/" + encodeURIComponent(next.add);
  if (next.test) hash += "/t/" + encodeURIComponent(next.test);
  if (next.make) hash += "/m/1";
  if (next.edit) hash += "/e/" + encodeURIComponent(next.edit);
  if (next.list) hash += "/v/" + next.list;
  if (next.changes) hash += "/c/1";
  if (location.hash !== hash) location.hash = hash;
}

async function getJSON(path) {
  const response = await fetch(path, { headers: { Accept: "application/json" } });
  if (!response.ok) {
    const err = new Error(path + " answered " + response.status);
    err.status = response.status;
    throw err;
  }
  return response.json();
}

async function refresh() {
  const live = $("live");
  const stamp = epoch;
  try {
    const fresh = await getJSON("/api/state");
    // A save finished while this answer was on its way: it may be older than the file.
    if (stamp !== epoch) return;
    adopt(fresh);
    if (selected.run) {
      const loaded = detail && detail.run === selected.run && detail.status !== "running";
      if (!loaded) detail = await getJSON("/api/runs/" + encodeURIComponent(selected.run)).catch(() => null);
    } else {
      detail = null;
    }
    await loadLatest();
    offline = null;
    live.title = "Last updated " + when(Date.now());
  } catch (err) {
    // The server is there but does not know this browser: its key is gone.
    offline = err.status === 401 ? "Needs the page's key: open the address flowmaker printed" : "Cannot reach the server";
  }
  paintLive();
  render();
}

// paintLive says whether the page hears from the server, and what starts the
// workflow on show: "Live on POST /tickets".
let offline = null;
function paintLive() {
  const w = selected.make ? null : state.workflows.find((x) => x.name === selected.workflow);
  const on = w && !isComp(w) && w.trigger ? "Live on " + w.trigger : "Live";
  $("live").replaceChildren(el("span", { class: "pulse" }), offline || on);
  $("live").className = offline ? "live down" : "live";
}

// loadLatest fetches the latest run of the selected workflow in full, which the
// drawing shows while no run is picked. A component has no runs of its own, so
// for it the latest is its latest test.
async function loadLatest() {
  const w = state.workflows.find((x) => x.name === selected.workflow);
  const last = selected.run || !w ? null : isComp(w) ? state.runs.find((r) => r.workflow === w.name) : latestRun(w.name);
  if (!last) { latestFull = null; return; }
  if (latestFull && latestFull.run === last.run && latestFull.status !== "running") return;
  latestFull = await getJSON("/api/runs/" + encodeURIComponent(last.run)).catch(() => null);
}

// progress is how far a step got in a run: its recorded status, or "pending"
// if the run has not reached it.
function progress(run, id) {
  if (!run) return null;
  if (id === "trigger" || id === IN) return "ok";
  // A component's way out: what it gave back. A test that ended well always did.
  if (id === OUT) return run.gave !== undefined ? "ok" : run.status === "ok" ? "skipped" : "pending";
  // The way out: answered, or not (yet). A run that ended well without a caller
  // waiting never answered, which is neither a success nor a failure.
  if (id === "response") return run.response ? "ok" : run.status === "ok" ? "skipped" : "pending";
  const done = run.steps.find((s) => s.id === id);
  // A test that could not run did not run the step: the drawing does not blame it.
  if (done && done.status === "failed" && couldNotRun(run)) return "pending";
  return done ? done.status : "pending";
}

// innerRun pairs the steps inside a flow step with what a run recorded for
// them. One with no record has not been reached, unless the whole flow step was
// skipped, which skipped them with it.
function innerRun(step, done, status) {
  const recs = done && done.steps ? done.steps : [];
  return (step.inner || []).map((s) => {
    const rec = recs.find((r) => r.id === s.id) || null;
    return { s, rec, status: rec ? rec.status : status === "skipped" ? "skipped" : "pending" };
  });
}

const failedInside = (rows) => rows.find((r) => r.status === "failed") || null;

// The latest run is the latest real one: a test is a run too, but it says
// nothing about what the trigger last did.
const latestRun = (name) => state.runs.find((r) => r.workflow === name && r.source !== "test");

function render() {
  if (!state.workflows.some((w) => w.name === selected.workflow)) {
    const first = state.workflows.find((x) => !x.component) || state.workflows[0];
    selected = { workflow: first ? first.name : null, run: null, node: null, changes: false, add: null, test: null, make: false, edit: null, list: null };
    detail = null;
  }
  // Rebuilding the page drops the reader's focus and text selection, so it is
  // only done when there is something new to show.
  const wanted = state.workflows.find((x) => x.name === selected.workflow);
  // A page that cannot make a workflow has no form for one, whatever the address says.
  if (selected.make && !canMake()) selected = { ...selected, make: false };
  // The server has no changes view for a component.
  if (selected.changes && isComp(wanted)) selected = { ...selected, changes: false };
  if (selected.changes && state.editable && wanted) ensureChanges(wanted);
  const next = JSON.stringify([state, selected, detail, latestFull, uiTick, selected.changes ? changesCache[selected.workflow] : null]);
  if (next === shown) return;
  shown = next;
  const focusId = document.activeElement && document.activeElement.dataset ? document.activeElement.dataset.focus : null;

  // A new workflow is drawn from the form, as a workflow of its own.
  const real = state.workflows.find((x) => x.name === selected.workflow);
  const w = real && selected.make ? draftWorkflow() : real;
  const key = [selected.workflow, selected.run, selected.node, selected.changes, selected.add, selected.test, selected.make, selected.list].join("|");
  const moved = key !== lastKey;
  lastKey = key;

  renderTop(w);
  if (!w) {
    view = null;
    form = null;
    tform = null;
    maker = null;
    insp.body = null;
    closeMenu();
    $("changes").replaceChildren();
    $("inspector").replaceChildren();
    $("canvas").replaceChildren(el("p", { class: "none" }, "No workflows are loaded."));
    paintEditor();
    return;
  }
  const run = detail && detail.run === selected.run && detail.workflow === w.name ? detail : null;
  // While no run is picked the drawing shows the latest one, if it is this workflow's.
  const auto = !run && !w.draft && !selected.run && latestFull && latestFull.workflow === w.name ? latestFull : null;
  view = { w, run, auto };
  paintMenu();
  renderCanvas(moved);
  renderInspector(w, run, moved);
  paintEditor();
  if (focusId) {
    const back = document.querySelector('[data-focus="' + CSS.escape(focusId) + '"]');
    if (back && back !== document.activeElement) back.focus({ preventScroll: true });
  }
  if (afterRender) { const run = afterRender; afterRender = null; run(); }
}

// ---- top bar ----

// paintTabs: the views of a workflow. Editor is the drawing with no run picked,
// Runs the list of its runs, Tests the list of its tests; each with its count.
// A component has no runs of its own: its tab lists its test runs.
function paintTabs(w) {
  const tabs = $("tabs");
  if (!w || w.draft) { tabs.replaceChildren(); return; }
  const comp = isComp(w);
  const mine = state.runs.filter((r) => r.workflow === w.name);
  const runs = mine.filter((r) => !isTestRun(r)), tests = mine.filter(isTestRun);
  const picked = selected.run ? state.runs.find((r) => r.run === selected.run) : null;
  const mode = picked ? (isTestRun(picked) && !comp ? "tests" : "runs") : selected.list || "editor";
  const tab = (name, label, count, onclick) => el("button", {
      type: "button", role: "tab", "aria-selected": mode === name ? "true" : "false", onclick,
    }, label, count == null ? null : el("span", { class: "n" }, count));
  tabs.replaceChildren(...[
    tab("editor", "Editor", null, () => go({ run: null, node: null })),
    tab("runs", comp ? "Test runs" : "Runs", comp ? tests.length : runs.length, () => go({ list: "runs", run: null, node: null })),
    comp ? null : tab("tests", "Tests", typeof w.tests === "number" ? w.tests : (w.cases || []).length, () => go({ list: "tests", run: null, node: null }))].filter(Boolean));
}

function renderTop(w) {
  paintTabs(w);
  paintLive();
  $("changes").replaceChildren(...(state.editable && w && !isComp(w) && !w.draft ? [changesChip(w)] : []));

  const anyFailed = state.runs.some((r) => r.status === "failed" && latestRun(r.workflow) === r);
  document.title = anyFailed ? "flowmaker (a run failed)" : "flowmaker";
  if (!w) {
    $("crumb").replaceChildren();
    return;
  }
  if (w.draft) {
    $("crumb").replaceChildren(...[
      el("span", { class: "crumbkind" }, icon("plus", 14), "New workflow"),
      w.label ? el("strong", {}, w.label) : null].filter(Boolean));
    return;
  }
  if (isComp(w)) {
    $("crumb").replaceChildren(
      el("span", { class: "crumbkind" }, icon("component", 14), "Component"),
      el("button", { type: "button", class: "crumbname", title: "Pick another component", "aria-haspopup": "dialog", onclick: (e) => openMenu("components", e.currentTarget) },
        el("span", { title: w.name }, compTitle(w)), icon("chev", 14)),
      el("span", { class: "pill" }, compKindWord(w)));
    return;
  }
  $("crumb").replaceChildren(
    el("button", { type: "button", class: "crumbname", title: "Pick another workflow", "aria-haspopup": "dialog", onclick: (e) => openMenu("workflows", e.currentTarget) },
      el("span", {}, w.name), icon("chev", 14)));
}

// statusMark is the small round symbol that stands for a status.
function statusMark(status, size) {
  return el("span", { class: "mark " + status }, icon(status, size || 12));
}

// ---- the menu: the workflows, or the components, to pick one from ----

let menu = null;        // the menu on the page: { kind: "workflows" | "components", root, anchor }

// openMenu opens the list of one kind next to the control that asked for it,
// or closes it when it is already open.
function openMenu(kind, anchor) {
  const again = menu && menu.kind === kind;
  closeMenu();
  if (again) return;
  const root = el("div", { class: "menu", role: "dialog", "aria-label": kind === "components" ? "Components" : "Workflows" });
  menu = { kind, root, anchor };
  paintMenu();
  document.body.append(root);
  const at = anchor.getBoundingClientRect();
  // From the rail it opens to the side, from the name below it.
  const side = !!anchor.closest("#rail") && window.innerWidth > 860;
  Object.assign(root.style, side ? { left: at.right + 8 + "px", top: at.top + "px" } : { left: Math.max(8, at.left) + "px", top: at.bottom + 6 + "px" });
  for (const b of Object.values(railButtons)) b.setAttribute("aria-pressed", b === railButtons[kind] ? "true" : "false");
  const first = root.querySelector('[aria-current="true"]') || root.querySelector("button");
  if (first) first.focus({ preventScroll: true });
}

function closeMenu(focusBack) {
  if (!menu) return;
  const { root, anchor } = menu;
  menu = null;
  root.remove();
  for (const b of Object.values(railButtons)) b.setAttribute("aria-pressed", "false");
  if (focusBack && document.contains(anchor)) anchor.focus({ preventScroll: true });
}

// paintMenu fills the menu, and again on every poll, for the flags and the latest runs.
function paintMenu() {
  if (!menu) return;
  const pick = (name) => { closeMenu(); go({ workflow: name, run: null, node: null }); };
  if (menu.kind === "components") {
    const list = state.workflows.filter((x) => x.component);
    menu.root.replaceChildren(
      el("div", { class: "mhead" }, "Components", el("span", { class: "rs-n" }, list.length)),
      ...(list.length ? list.map((x) => componentRow(x, pick)) : [el("p", { class: "mempty" }, "None. A component is a folder with a component.yaml, used by its name.")]));
    return;
  }
  const list = state.workflows.filter((x) => !x.component);
  menu.root.replaceChildren(...[
    el("div", { class: "mhead" }, "Workflows", el("span", { class: "rs-n" }, list.length)),
    list.map((x) => {
      const last = latestRun(x.name);
      return el("button", { class: "row wf", type: "button", "aria-current": x.name === selected.workflow && !selected.make ? "true" : null, onclick: () => pick(x.name) },
        el("span", { class: "dot " + (last ? last.status : ""), title: last ? "Latest run: " + last.status : "No runs yet" }),
        el("span", { class: "name" }, x.name),
        flagFor(x),
        el("span", { class: "sub mono" }, x.trigger || "no trigger"),
        el("span", { class: "aside" }, plural(x.steps.length, "step")));
    }),
    canMake() ? newButton(!!selected.make) : null].flat().filter(Boolean));
}

document.addEventListener("pointerdown", (event) => {
  if (menu && !menu.root.contains(event.target) && !menu.anchor.contains(event.target)) closeMenu();
});
window.addEventListener("resize", () => closeMenu());

// newButton is the control under the workflows that opens the form for a new one.
function newButton(on) {
  return el("button", {
      type: "button", class: "btn small block newwf", "data-focus": "new-workflow", "aria-current": on ? "true" : null,
      title: making && makeTouched() ? "Go on with the workflow you started" : null,
      onclick: () => { closeMenu(); go({ make: true, run: null, node: null }); },
    }, icon("plus", 12), "New workflow");
}

// componentRow is a component in the menu: the silhouette of its kind, its
// own name, what kind it is and how many steps use it.
function componentRow(x, pick) {
  const kind = compKind(x), uses = usesOf(x);
  const test = state.runs.find((r) => r.workflow === x.name && isTestRun(r));
  return el("button", {
      class: "row comp kind-" + kind, type: "button", "aria-current": x.name === selected.workflow ? "true" : null,
      title: x.name + (test ? " · latest test: " + VERDICT[verdictOf(test)].word.toLowerCase() : ""),
      onclick: () => pick(x.name),
    },
    kindTile(kind, 28),
    el("span", { class: "name" }, compTitle(x)),
    flagFor(x),
    el("span", { class: "sub" }, compKindWord(x)),
    el("span", { class: "aside" }, uses ? "used by " + uses : "unused"));
}

// outcome says in a few words what a run did.
function outcome(r) {
  const failed = r.steps.find((s) => s.status === "failed");
  if (failed) {
    const inner = (failed.steps || []).find((s) => s.status === "failed");
    return "Failed at " + failed.id + (inner ? " › " + inner.id : "");
  }
  const ran = r.steps.filter((s) => s.status === "ok").map((s) => s.id);
  if (r.status === "running") return ran.length ? "Done: " + ran.join(", ") : "Starting";
  return ran.length ? ran.join(" → ") : "No step ran";
}

// ---- centre: the drawing ----

// kindTile is a small solid icon tile in the colour of its kind.
function kindTile(kind, size) {
  return el("span", { class: "tile kt kind-" + kind, css: { width: size + "px", height: size + "px" } }, icon(kind, Math.round(size * 0.56)));
}

// stepSubtitle says what a step is in a few words: its kind and the model or
// target, or, for a step that uses a component, which one.
function stepSubtitle(step) {
  const word = KIND_LABEL[step.kind] || step.kind;
  if (step.component) {
    return step.component + " · " + (step.kind === "flow" ? plural((step.inner || []).length, "step") : word + " component");
  }
  return word + (step.subtitle ? " · " + step.subtitle : "");
}

// readable turns a step's condition into words, by a few plain rewrites:
// classify.priority.value == "high" becomes priority is high. The full
// expression stays in the label's title.
function readable(expr) {
  let t = String(expr).trim();
  t = t.replace(/\b[A-Za-z_]\w*\.([A-Za-z_]\w*)\.value\b/g, "$1");
  t = t.replace(/\s*==\s*"([^"]*)"/g, " is $1").replace(/\s*!=\s*"([^"]*)"/g, " is not $1");
  t = t.replace(/\s+in\s+\[([^\]]*)\]/g, (all, list) => " in " + list.split(",").map((s) => s.trim().replace(/^"(.*)"$/, "$1")).join(", "));
  t = t.replace(/&&/g, " and ").replace(/\|\|/g, " or ").replace(/!\(/g, "not (");
  return t.replace(/\s+/g, " ").trim();
}

// layout draws the steps as a tree, left to right. A step hangs from the
// latest earlier step it reads, or from the start if it reads none; the way
// out hangs from the latest step it reads, or the last step. A column is one
// depth; rows are shared out by how many ends a branch has, and a parent
// stands in the middle of its children.
function layout(w) {
  const comp = isComp(w);
  const root = comp
    ? { id: IN, kind: "input", title: "Input", subtitle: inputNames(w).join(", ") || "takes no input" }
    : { id: "trigger", kind: "trigger", title: isWebhook(w) ? "Webhook" : w.trigger ? "Schedule" : "Manual", subtitle: w.trigger || "started by hand" };
  Object.assign(root, { depth: 0, children: [], parent: null, w: CARD_W, h: CARD_H });
  const nodes = [root], byId = { [root.id]: root };
  w.steps.forEach((step, i) => {
    let parent = root, best = 0;
    for (const name of step.reads || []) {
      const from = byId[readNode(w, name)];
      const at = from ? nodes.indexOf(from) : -1;
      if (at >= best) { best = at; parent = from; }
    }
    const node = { id: step.id, kind: step.kind, title: step.id, mono: true, subtitle: stepSubtitle(step), order: i + 1, step, parent, depth: parent.depth + 1, children: [], w: CARD_W, h: CARD_H };
    nodes.push(node);
    byId[step.id] = node;
    parent.children.push(node);
  });
  const end = comp
    ? { id: OUT, kind: "output", title: "Output", subtitle: givenNames(w).join(", ") || "gives back nothing", reads: w.gives ? w.gives.reads || [] : w.steps.map((s) => s.id) }
    : w.respond ? { id: "response", kind: "response", title: "Response", subtitle: "answers " + w.respond.status, reads: w.respond.reads || [] } : null;
  if (end) {
    let parent = null, best = 0;
    for (const name of end.reads) {
      const from = byId[readNode(w, name)];
      const at = from ? nodes.indexOf(from) : -1;
      if (at > best) { best = at; parent = from; }
    }
    if (!parent) parent = nodes[nodes.length - 1];
    const node = { id: end.id, kind: end.kind, title: end.title, subtitle: end.subtitle, parent, depth: parent.depth + 1, children: [], w: END_W, h: END_H };
    nodes.push(node);
    byId[end.id] = node;
    parent.children.push(node);
  }
  let rows = 0;
  const place = (n) => {
    if (!n.children.length) n.row = rows++;
    else {
      n.children.forEach(place);
      n.row = (n.children[0].row + n.children[n.children.length - 1].row) / 2;
    }
  };
  place(root);
  let deepest = 0;
  for (const n of nodes) {
    deepest = Math.max(deepest, n.depth);
    n.x = PAD + n.depth * COL;
    n.cy = PAD + CARD_H / 2 + n.row * ROW;
    n.y = n.cy - n.h / 2;
  }
  return {
    nodes, byId,
    width: PAD * 2 + deepest * COL + CARD_W + HANDLE,
    height: PAD * 2 + (rows - 1) * ROW + CARD_H + 24,
  };
}

// resultOf is the one line under a node about the run on show: for a decision
// the answers it picked and how sure it was, for a request what came back, for
// the rest how long it took. It gives the pieces to draw and the plain text.
function resultOf(node, ran, status, done) {
  if (!ran) return null;
  const plain = (text, tone) => ({ parts: [text], text, tone });
  if (node.id === "trigger") return plain("started " + when(ran.started));
  if (node.id === IN) return null;
  if (node.id === OUT) return plain(ran.gave !== undefined ? "gave back" : ran.status === "running" ? "waiting" : "not reached");
  if (node.id === "response") return plain(ran.response ? "answered " + ran.response.status : answerWait(ran));
  if (status === "skipped") return plain("skipped in this run");
  if (!done) return status === "pending" ? plain(couldNotRun(ran) ? "could not run" : ran.status === "running" ? "waiting" : "not reached") : null;
  if (status === "failed") {
    const brokeAt = node.step && node.kind === "flow" ? failedInside(innerRun(node.step, done, status)) : null;
    return plain("failed" + (brokeAt ? " at " + brokeAt.s.id : done.error ? " · " + done.error : ""), "failed");
  }
  if (status === "running") return plain("running");
  const took = duration(done.ms);
  const out = done.output && typeof done.output === "object" ? done.output : null;
  if (node.kind === "decide" && out) {
    const parts = [], words = [];
    for (const name of Object.keys(out)) {
      const q = out[name];
      if (!q || typeof q !== "object" || q.value === undefined) continue;
      // A yes-or-no answer carries the chance of yes; the others the chance of each option.
      const p = q.probabilities && q.probabilities[q.value] !== undefined ? q.probabilities[q.value]
        : typeof q.probability === "number" && typeof q.value === "boolean" ? (q.value ? q.probability : 1 - q.probability) : q.confidence;
      if (parts.length) parts.push(" · ");
      parts.push(el("span", { class: "pick" }, String(q.value)), typeof p === "number" ? " " + Math.round(p * 100) + "%" : "");
      words.push(q.value + (typeof p === "number" ? " " + Math.round(p * 100) + "%" : ""));
    }
    if (words.length) return { parts, text: words.join(" · ") };
  }
  if (node.kind === "http") {
    const bits = [];
    if (out && out.status !== undefined) bits.push(String(out.status));
    if (done.detail) bits.push(done.detail);
    bits.push(took);
    return plain(bits.join(" · "));
  }
  return plain(took);
}

function renderCanvas(moved) {
  const canvas = $("canvas");
  const { w, run, auto } = view;
  if (selected.changes && state.editable && !isComp(w)) return renderChanges(w);
  const edited = editedNodes(w);
  const old = canvas.querySelector(".scroller");
  const keep = old && !moved ? [old.scrollLeft, old.scrollTop] : null;

  // What the drawing tells about a run: the one picked, or else the latest.
  const ran = run || auto;
  const stat = (id) => {
    if (!ran) return "";
    const s = progress(ran, id) || "";
    // The latest run may be older than the file: a step it never heard of is not said to be missed.
    return auto && s === "pending" && ran.status === "ok" ? "" : s;
  };
  const plan = layout(w);
  const animate = enteredFor !== w.name;
  enteredFor = w.name;

  const lines = [], labels = [];
  for (const node of plan.nodes) {
    if (!node.parent) continue;
    const from = node.parent, status = stat(node.id);
    const x1 = from.x + from.w, y1 = from.cy, x2 = node.x, y2 = node.cy;
    const dx = Math.max(40, (x2 - x1) / 2);
    lines.push(svg("path", { class: "edge " + status, d: `M${x1} ${y1} C${x1 + dx} ${y1}, ${x2 - dx} ${y2}, ${x2} ${y2}` }));
    const cond = node.step && node.step.when;
    // The condition sits where its line has levelled out before the step, so
    // the conditions of steps that branch from one parent do not meet.
    if (cond) labels.push(el("span", { class: "elabel " + status, title: cond, css: { left: x2 - 74 + "px", top: y2 + "px" } }, readable(cond)));
  }

  const adding = canAdd(w) && !run;
  const cards = plan.nodes.map((node, i) => {
    const status = stat(node.id);
    const done = ran ? ran.steps.find((s) => s.id === node.id) : null;
    const cond = node.step && node.step.when;
    const result = resultOf(node, ran, status, done);
    // A flow step holds steps of its own: how many, and in a run where it broke.
    const inside = node.kind === "flow" && node.step ? innerRun(node.step, done, status) : null;
    const brokeAt = inside && status === "failed" ? failedInside(inside) : null;
    const said = node.id === "response" && ran && ran.response ? "answered " + ran.response.status : node.id === OUT && ran && ran.gave !== undefined ? "gave back" : (STATUS_TEXT[status] || status).toLowerCase();
    const label = node.title + ", " + KIND_LABEL[node.kind] + (node.step && node.step.component ? ", uses " + node.step.component : "") + (inside ? ", " + plural(inside.length, "step") + " inside" : "")
      + (node.order ? ", step " + node.order : "") + (node.step && canOpen(w, node.id) ? ", Enter opens the editor" : "") + (cond ? ", runs only when " + readable(cond) : "") + (status ? ", " + said : "") + (brokeAt ? ", failed at " + brokeAt.s.id : "");
    const isEdited = edited.has(node.id);
    const chosen = selected.node === node.id;
    const leaf = !node.children.length;
    return el("div", {
        class: "slot" + (animate ? " enter" : "") + (chosen || selected.add === node.id ? " on" : ""),
        css: { left: node.x + "px", top: node.y + "px", width: node.w + "px", height: node.h + "px", animationDelay: (animate ? i * 70 : 0) + "ms" },
      },
      el("button", {
          class: "card shape-" + node.kind + " kind-" + node.kind + (node.step && node.step.component ? " stacked" : "") + (status ? " " + status : "") + (isEdited ? " edited" : ""),
          type: "button", "data-node": node.id, "data-focus": "n:" + node.id, "data-label": label,
          "aria-pressed": w.draft ? null : chosen ? "true" : "false", "aria-label": label + (isEdited ? ", unsaved changes" : ""),
          title: cond ? "Runs only when " + cond : null,
          // A workflow still being made is only drawn: there is nothing to select yet.
          "aria-disabled": w.draft ? "true" : null,
          onclick: w.draft ? null : () => (canOpen(w, node.id) ? openEditor(node.id) : go({ node: chosen ? null : node.id })),
        },
        node.parent ? el("span", { class: "port pin", "aria-hidden": "true" }) : null,
        el("span", { class: "tile" }, icon(node.kind, node.h < CARD_H ? 18 : 24)),
        el("span", { class: "text" },
          el("span", { class: "title" + (node.mono ? " mono" : "") }, node.title),
          el("span", { class: "sub" }, node.subtitle)),
        !leaf || (adding && node.id !== "response") ? el("span", { class: "port pout", "aria-hidden": "true" }) : null,
        badge(status)),
      result ? el("span", { class: "result" + (result.tone ? " " + result.tone : ""), title: result.text }, result.parts) : null,
      // The place to put a step: right after this one in the file.
      adding && node.id !== "response" ? addButton(node) : null);
  });

  const stage = el("div", { class: "stage", css: { width: plan.width + "px", height: plan.height + "px" } },
    svg("svg", { width: plan.width, height: plan.height, "aria-hidden": "true" }, lines), labels, cards);

  const avail = canvas.clientWidth - 40;
  const scale = zoom == null ? Math.max(0.6, Math.min(1, avail / plan.width)) : zoom;
  stage.style.transform = "scale(" + scale + ")";
  const holder = el("div", { class: "fit", css: { width: Math.ceil(plan.width * scale) + 32 + "px", height: Math.ceil(plan.height * scale) + 32 + "px" } }, stage);
  const scroller = el("div", { class: "scroller" }, holder);
  panOn(scroller);
  const fitAll = () => {
    zoom = Math.min(1, avail / plan.width, (canvas.clientHeight - 150) / plan.height);
    zoom = Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, Math.round(zoom * 100) / 100));
    renderCanvas(false);
  };

  canvas.replaceChildren(...[
    problemBanner(w),
    run ? runBar(run) : w.draft ? el("div", { class: "strip" }, "This is the workflow as the form describes it. Nothing is written until you make it.") : fileStrip(w, auto),
    scroller,
    el("div", { class: "tools", role: "group", "aria-label": "Zoom" },
      tool("plus", "Zoom in", () => setZoom(scale + 0.1)),
      tool("minus", "Zoom out", () => setZoom(scale - 0.1)),
      tool("fit", "Fit to window", fitAll),
      el("span", { class: "zoom", "aria-live": "off" }, Math.round(scale * 100) + "%")),
    canTest(w) && !w.draft && !isComp(w) ? el("div", { class: "fab" }, startButton(w, null, "Run tests", "canvas:test-all", { primary: true, show: true })) : null,
    // The latest runs, under the drawing in the editor: the Runs tab lists them all on the right.
    w.draft || isComp(w) || run || selected.list === "runs" ? null : runStrip(w),
  ].filter(Boolean));

  if (keep) { scroller.scrollLeft = keep[0]; scroller.scrollTop = keep[1]; }
  else if (selected.node) {
    const card = scroller.querySelector('[data-node="' + CSS.escape(selected.node) + '"]');
    if (card) card.scrollIntoView({ block: "nearest", inline: "nearest" });
  }
}

// badge is the round symbol on a node's corner for how its step went: a tick,
// a dash, a cross, or a pulsing dot while it runs. A step not reached has none.
function badge(status) {
  if (!status || status === "pending") return null;
  return el("span", { class: "nbadge " + status, "aria-hidden": "true" }, status === "running" ? null : icon(status, 11));
}

// fileStrip sits over the drawing while no run is picked: the file it is drawn
// from, and how the run drawn on it went, which opens that run.
function fileStrip(w, last) {
  const word = last ? (isTestRun(last) ? "Last test run " : "Last run ") + (last.status === "ok" ? "succeeded" : (STATUS_TEXT[last.status] || last.status).toLowerCase()) : "";
  return el("div", { class: "strip" },
    el("span", { class: "mono path" }, w.path),
    last ? el("button", { type: "button", class: "lastrun " + last.status, "data-focus": "open-latest", title: "Open run " + last.run, onclick: () => go({ run: last.run, node: null }) },
      word + " · " + when(last.started)) : null);
}

// runStrip is the line under the drawing with the latest two runs, each a way
// to open it, and the way to the list of them all.
function runStrip(w) {
  const runs = state.runs.filter((r) => r.workflow === w.name && !isTestRun(r));
  if (!runs.length) return null;
  const said = (r) => (r.status === "failed" ? outcome(r).replace(/^Failed/, "failed") : r.status === "running" ? "running" : "succeeded in " + duration(r.ms)) + " · " + when(r.started);
  return el("div", { class: "runstrip" },
    el("span", { class: "rs-title" }, "Runs"),
    runs.slice(0, 2).map((r) => el("button", {
        type: "button", class: "rs-run " + r.status, "aria-current": r.run === selected.run ? "true" : null,
        onclick: () => go({ run: r.run === selected.run ? null : r.run, node: null }),
      },
      el("span", { class: "dot " + r.status }), el("span", { class: "mono" }, r.run), el("span", { class: "rs-said" }, said(r)))),
    el("span", { class: "grow" }),
    el("button", { type: "button", class: "rs-all", onclick: () => go({ list: "runs", run: null, node: null }) }, "All runs", el("span", { class: "rs-n" }, runs.length)));
}

// panOn lets the drawing be dragged by its background, as well as scrolled.
function panOn(scroller) {
  let from = null;
  scroller.addEventListener("pointerdown", (event) => {
    if (event.button !== 0 || event.target.closest("button, .elabel")) return;
    from = { x: event.clientX, y: event.clientY, left: scroller.scrollLeft, top: scroller.scrollTop };
    scroller.setPointerCapture(event.pointerId);
    scroller.classList.add("panning");
  });
  scroller.addEventListener("pointermove", (event) => {
    if (!from) return;
    scroller.scrollLeft = from.left - (event.clientX - from.x);
    scroller.scrollTop = from.top - (event.clientY - from.y);
  });
  const stop = () => { from = null; scroller.classList.remove("panning"); };
  scroller.addEventListener("pointerup", stop);
  scroller.addEventListener("pointercancel", stop);
}

// canAdd: steps can only be added to a workflow, when the server edits files and the file is sound.
// A component's steps are fixed on this page: it never adds or removes one.
const canAdd = (w) => !!w && state.editable && !w.problem && !w.component && !w.draft;

// addButton is the plus after a node's right port, where a step can be put after it.
function addButton(node) {
  const label = node.id === "trigger" ? "Add a step first, right after the trigger" : "Add a step after " + node.id;
  return el("button", {
      type: "button", class: "addbtn" + (node.children.length ? "" : " leaf"), "data-focus": "add:" + node.id, title: label, "aria-label": label,
      css: { left: node.w + 14 + "px", top: node.h / 2 - 13 + "px" },
      onclick: () => openAdd(node.id),
    }, icon("plus", 13));
}

// openAdd opens the form for a new step after the given step ("trigger" for the
// first place). Without one it follows the selection, or goes last.
function openAdd(after) {
  const w = view && view.w;
  if (!canAdd(w)) return;
  if (after === undefined) after = selected.node && (selected.node === "trigger" || w.steps.some((s) => s.id === selected.node)) ? selected.node : "-";
  go({ add: after, node: null, run: null });
}

// answerWait says why a run has no answer.
function answerWait(run) {
  return run.status === "running" ? "waiting" : run.status === "failed" ? "not reached" : "no caller";
}

function tool(name, label, onclick) {
  return el("button", { type: "button", class: "tool", "aria-label": label, title: label, onclick }, icon(name, 15));
}

function setZoom(value) {
  zoom = Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, Math.round(value * 100) / 100));
  renderCanvas(false);
}

function runBar(run) {
  if (isTestRun(run)) return testBar(run);
  return el("div", { class: "runbar " + run.status },
    el("span", { class: "state " + run.status }, statusMark(run.status, 12), STATUS_TEXT[run.status] || run.status),
    el("span", { class: "mono id" }, run.run),
    el("span", { class: "meta" }, when(run.started)),
    run.status !== "running" ? el("span", { class: "meta" }, "took " + duration(run.ms)) : null,
    el("span", { class: "meta" }, run.source),
    canKeep(view.w, run) ? el("button", { type: "button", class: "ghost keep", "data-focus": "keep-bar", onclick: () => openKeep(run.run) }, icon("plus", 13), "Keep as a test") : null,
    el("button", { type: "button", class: "ghost", onclick: () => go({ run: null, node: null }) }, "Close run", el("kbd", {}, "Esc")));
}

// ---- right: details ----

function section(title, ...children) {
  return el("section", {}, title ? el("h2", {}, title) : null, children);
}

function header(kind, iconName, title, subtitle, closable, extra) {
  return el("header", { class: "kind-" + kind },
    KIND_LABEL[kind] ? kindTile(kind, 38) : el("span", { class: "tile" }, icon(iconName, 20)),
    el("div", { class: "names" }, el("h1", { class: kind === "workflow" ? null : "mono" }, title), el("div", { class: "kindname" }, subtitle)),
    extra || null,
    closable ? el("button", { type: "button", class: "x", "aria-label": "Close details", title: "Close (Esc)", onclick: () => go({ node: null }) }, icon("close", 16)) : null);
}

function pairs(rows) {
  return el("dl", { class: "kv" }, rows.filter(Boolean).map(([label, value]) => [el("dt", {}, label), el("dd", {}, value)]));
}

// jsonView shows a value as indented JSON with the parts told apart by colour.
// The pieces are cut out of the text and added as text nodes.
function jsonView(value) {
  const text = JSON.stringify(value, null, 2);
  const parts = [];
  const re = /("(?:\\.|[^"\\])*")(\s*:)?|\b(true|false|null)\b|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/g;
  let last = 0, m;
  while ((m = re.exec(text))) {
    if (m.index > last) parts.push(text.slice(last, m.index));
    const cls = m[1] ? (m[2] ? "k" : "s") : m[3] ? "b" : "n";
    parts.push(el("span", { class: "j" + cls }, m[1] ? m[1] : m[0]), m[2] || null);
    last = re.lastIndex;
  }
  parts.push(text.slice(last));
  return el("pre", { class: "json" }, parts);
}

// code shows a block of text, with a button that copies it.
function code(text, label) {
  const button = el("button", { type: "button", class: "copy", "aria-label": "Copy " + (label || "text") }, icon("copy", 13), el("span", {}, "Copy"));
  button.addEventListener("click", () => {
    const done = (ok) => {
      button.lastChild.textContent = ok ? "Copied" : "Press Ctrl+C";
      setTimeout(() => { button.lastChild.textContent = "Copy"; }, 1600);
    };
    if (navigator.clipboard) navigator.clipboard.writeText(text).then(() => done(true), () => done(false));
    else done(false);
  });
  return el("div", { class: "code" }, button, el("pre", {}, text));
}

function statusPanel(status, ...rest) {
  return panelTitled(status, STATUS_TEXT[status] || status, ...rest);
}

function panelTitled(status, title, ...rest) {
  return el("div", { class: "status-panel " + status },
    statusMark(status, 14),
    el("div", {}, el("div", { class: "big" }, title), rest));
}

// example is a command that calls a workflow's webhook.
function example(w) {
  const path = w.trigger.slice("POST ".length);
  // A workflow that answers its caller always waits; another is asked to.
  const lines = [
    "curl -X POST '" + location.origin + path + (w.respond ? "" : "?wait=true") + "' \\",
    "  -H 'Content-Type: application/json' \\",
  ];
  if (state.protected) lines.push("  -H \"Authorization: Bearer $FLOWMAKER_TOKEN\" \\");
  // The body of the workflow's first test is one it is known to take.
  const body = w.sample !== undefined ? JSON.stringify(w.sample) : "{\"subject\": \"Hello\", \"message\": \"...\"}";
  lines.push("  -d '" + body.replace(/'/g, "'\\''") + "'");
  return code(lines.join("\n"), "the curl command");
}

// renderInspector fills the panel on the right, which is only there when it has
// something to show: a run, the trigger, the response, a component, the tests,
// a form, or changes not saved yet. A step that can be edited opens in the node
// editor instead, and a workflow with nothing picked is only its drawing.
function renderInspector(w, run, moved) {
  ensureInspector();
  const step = editing(w) ? null : w.steps.find((s) => s.id === selected.node);
  const comp = isComp(w);
  const canEdit = state.editable && !w.problem;
  const onStart = selected.node === START(w);
  const onEnd = comp ? selected.node === OUT : selected.node === "response" && !!w.respond;
  const endEdit = comp ? (w.gives ? w.gives.edit : null) : w.respond ? w.respond.edit : null;
  const fields = !canEdit ? null : onStart ? (comp ? null : w.edit) : onEnd ? endEdit : step ? step.edit : null;
  const hasForm = !!(fields && fields.length);
  const adding = addMode(w);
  const testing = testMode(w);
  const drafting = !!w.draft;
  let parts;
  const source = testing && selected.test !== "-" ? state.runs.find((r) => r.run === selected.test) : null;
  if (drafting) parts = [header("workflow", "plus", "New workflow", "and its first step", true)];
  else if (testing) parts = [header("workflow", "plus", selected.test === "-" ? "New test" : "Keep as a test",
    selected.test === "-" ? "of " + w.name : "from the run at " + (source ? when(source.started) : selected.test) + " of " + w.name, true)];
  else if (adding) parts = [header("workflow", "plus", "Add a step", "to " + w.name, true)];
  else if (onStart) parts = comp ? inputDetails(w, run) : triggerDetails(w, run);
  else if (onEnd) parts = comp ? outputDetails(w, run) : responseDetails(w, run);
  else if (step) parts = stepDetails(w, step, run, hasForm);
  else if (run) parts = runDetails(w, run);
  else if (selected.list === "runs") parts = runsDetails(w);
  else if (comp) parts = componentDetails(w);
  else if (selected.list === "tests") parts = testsDetails(w);
  else if (canEdit && (dirtyCount(w.name) > 0 || (uiOf(w.name).notice && uiOf(w.name).notice.kind !== "saved"))) parts = pendingDetails(w);
  const open = !!parts;
  $("app").dataset.inspector = open ? "open" : "closed";
  if (!open) {
    hideMaker();
    hideAdder();
    hideTester();
    showForm(w, null, null);
    insp.rest.replaceChildren();
    updateBar();
    return;
  }
  const [head, ...sections] = parts.flat(Infinity).filter(Boolean);
  const box = $("inspector");
  const top = box.scrollTop;
  insp.head.replaceWith(head);
  insp.head = head;
  if (drafting) {
    hideAdder();
    hideTester();
    showForm(w, null, null);
    showMaker();
  } else if (testing) {
    hideMaker();
    showForm(w, null, null);
    hideAdder();
    showTester(w);
  } else if (adding) {
    hideMaker();
    hideTester();
    showForm(w, null, null);
    showAdder(w);
  } else {
    hideMaker();
    hideAdder();
    hideTester();
    showForm(w, hasForm ? selected.node : null, fields);
  }
  insp.rest.replaceChildren(...sections);
  updateBar();
  box.scrollTop = moved ? 0 : top;
}

// outline lists the steps as buttons: the same choices as the drawing, for the
// keyboard and for a quick look at the shape of the workflow.
function outline(w) {
  return el("ol", { class: "outline" }, w.steps.map((s, i) =>
    el("li", {}, el("button", { type: "button", class: "kind-" + s.kind + (editedNodes(w).has(s.id) ? " edited" : ""), "data-node": s.id, onclick: () => go({ node: s.id }) },
      el("span", { class: "n" }, i + 1),
      kindTile(s.kind, 26),
      el("span", { class: "name mono" }, s.id),
      el("span", { class: "tag" }, KIND_LABEL[s.kind])),
    // The steps inside a flow step, small and not clickable: they belong to its component.
    s.kind === "flow" && (s.inner || []).length ? el("ol", { class: "nest", "aria-label": "Steps inside " + s.id },
      s.inner.map((x) => el("li", { class: "nrow kind-" + x.kind }, icon(x.kind, 12), el("span", { class: "name mono" }, x.id), el("span", { class: "tag" }, KIND_LABEL[x.kind] || x.kind)))) : null)));
}

// showResponse is a link to the response node, where the answer is shown in full.
function showResponse() {
  return el("button", { type: "button", class: "linkbtn", onclick: () => go({ node: "response" }) }, "Show the response");
}

// testsDetails is the list of a workflow's tests, with the round just run on top.
function testsDetails(w) {
  const v = state.editable ? roundView(w.name) : null;
  return [
    header("workflow", "tests", "Tests", w.name, true),
    v ? section("", roundBox(w, v)) : null,
    state.editable ? testsSection(w, "") : section("", el("p", { class: "muted" }, plural(w.tests || 0, "test") + " in " + w.path + ". Run them with flowmaker test.")),
  ];
}

// runsDetails lists a workflow's runs, newest first: a component's test runs,
// as it has none of its own. A workflow's test runs are with its tests.
function runsDetails(w) {
  const comp = isComp(w);
  const runs = state.runs.filter((r) => r.workflow === w.name && (comp ? isTestRun(r) : !isTestRun(r)));
  const none = comp
    ? (state.editable ? "None yet. Run its tests and each one appears here." : "None. Tests of a component run with flowmaker test.")
    : w.trigger ? "None yet. A run appears here each time the trigger fires." : "The server does not run this workflow: it has no webhook or cron trigger.";
  return [
    header("workflow", "runs", comp ? "Test runs" : "Runs", comp ? compTitle(w) : w.name, true),
    section("", runs.length ? el("div", { class: "runlist" }, runs.map(runRow)) : el("p", { class: "muted" }, none)),
  ];
}

// runRow is one run in the list: how it went, when, how long it took and what it did.
function runRow(r) {
  const test = isTestRun(r), V = test ? VERDICT[verdictOf(r)] : null;
  const tone = test ? rowTone(verdictOf(r)) : r.status;
  return el("button", {
      type: "button", class: "row run " + tone, "data-focus": "run:" + r.run,
      "aria-label": (test ? "Test " + r.test + ", " + V.word : "Run at " + when(r.started) + ", " + r.status) + (test ? ", " + when(r.started) : ""),
      onclick: () => go({ run: r.run, node: null }),
    },
    statusMark(test ? V.mark : r.status, 11),
    el("span", { class: "name" }, test ? r.test : when(r.started)),
    el("span", { class: "aside" }, r.status === "running" ? "running" : test && verdictOf(r) === "unrun" ? "" : duration(r.ms)),
    el("span", { class: "sub" }, test ? testOutcome(r) + " · " + when(r.started) : outcome(r)));
}

// backTo is the way from a run back to the list it was picked from.
function backTo(w, run) {
  const tests = isTestRun(run) && !isComp(w);
  return el("button", { type: "button", class: "btn small", "data-focus": "back-list", onclick: () => go({ list: tests ? "tests" : "runs", run: null, node: null }) }, tests ? "All tests" : "All runs");
}

// pendingDetails is what the panel shows while changes wait to be saved, or
// after a save that failed: the steps changed, each a way back into it. A save
// that worked is told by the Changes chip instead.
function pendingDetails(w) {
  const names = dirtyNodes(w.name);
  return [
    header("workflow", "workflow", names.length ? "Unsaved changes" : "Not saved", w.name, false),
    names.length ? section("Changed", el("div", { class: "chips" }, names.map((id) =>
      el("button", { type: "button", class: "pill link mono", title: "Open " + nodeName(id), onclick: () => (canOpen(w, id) ? openEditor(id) : go({ node: id })) }, nodeName(id))))) : null,
  ];
}

// ---- a component, from the outside in ----

// inputsView lists what a component takes, with the type of each.
function inputsView(w) {
  const inputs = w.inputs || [];
  if (!inputs.length) return el("p", { class: "muted" }, "It takes no input.");
  return el("ul", { class: "io", "aria-label": "Inputs" }, inputs.map((i) =>
    el("li", {}, el("span", { class: "ioname mono" }, i.name), el("span", { class: "iotype" }, i.type))));
}

// usedByView lists the steps that use a component, each a way to that step.
function usedByView(w) {
  const uses = w.usedBy || [];
  if (!uses.length) return el("p", { class: "muted" }, "No step uses it yet. A step uses a component by naming its folder.");
  return el("ul", { class: "usedby", "aria-label": "Steps that use " + compTitle(w) }, uses.map((u) => {
    const x = entryOf(u.workflow), label = x ? (x.component ? compTitle(x) : x.name) : u.workflow;
    return el("li", {}, el("button", {
        type: "button", class: "use", title: "Open step " + u.step + " of " + label,
        onclick: () => go({ workflow: u.workflow, run: null, node: u.step }),
      },
      icon(x && x.component ? "component" : "workflow", 15),
      el("span", { class: "uwho" }, label),
      el("span", { class: "usep", "aria-hidden": "true" }, "›"),
      el("span", { class: "ustep mono" }, u.step),
      x && x.component ? el("span", { class: "utag" }, "component") : null));
  }));
}

// readsChips are links to what a step or an end node reads from.
function readsChips(w, names) {
  return names.length ? section("Reads from", el("div", { class: "chips" }, names.map((name) =>
    el("button", { type: "button", class: "pill link mono", title: "Show " + name, onclick: () => go({ node: readNode(w, name) }) }, name)))) : null;
}

function componentDetails(w) {
  const kind = compKind(w);
  const out = el("button", { type: "button", class: "linkbtn", onclick: () => go({ node: OUT }) }, "Show the output");
  const inn = el("button", { type: "button", class: "linkbtn", onclick: () => go({ node: IN }) }, "Show the input");
  return [
    header(kind, kind, compTitle(w), "Component · " + compKindWord(w)),
    w.description ? section("", el("p", { class: "lede" }, w.description)) : null,
    section("Definition", pairs([
      ["File", mono(w.path)],
      ["Made of", w.gives ? plural(w.steps.length, "step") + " of its own" : "One piece of work: " + (KIND_NAMES[kind] || kind).toLowerCase()],
      ["Tests", w.tests + (w.tests && !state.editable ? ", run with flowmaker test" : "")],
      state.commit ? ["Version", mono(state.commit)] : null,
    ])),
    section("Takes", inputsView(w), el("p", { class: "muted small" }, "A step that uses this component fills each in. ", inn)),
    section("Gives back",
      givenNames(w).length ? el("ul", { class: "io", "aria-label": "Gives back" }, givenNames(w).map((n) => el("li", {}, el("span", { class: "ioname mono" }, n)))) : el("p", { class: "muted" }, "Nothing."),
      el("p", { class: "muted small" }, w.gives ? "What the component says it gives back. " : "A component of one kind of work gives back what that work puts out. ", out)),
    section("Used by", usedByView(w),
      el("p", { class: "muted small" }, usesOf(w) ? (state.editable ? "A change here applies to every one of these steps." : "These steps all get what the component does.") : "Nothing depends on it yet, so a change here affects no workflow.")),
    section(w.gives ? "Steps" : "The work", outline(w),
      state.editable && !w.problem ? el("p", { class: "muted small" }, "Select the input, a step or the output to edit it. Saved changes are written to " + w.path + ".")
        : el("p", { class: "muted small" }, "Components are files in git: edit the YAML and commit it.")),
    testsSection(w),
  ];
}

function inputDetails(w, run) {
  const given = run && run.trigger && run.trigger.input !== undefined ? run.trigger.input : undefined;
  const readers = w.steps.filter((s) => s.reads.includes("input")).map((s) => s.id);
  return [
    header("input", "input", "input", KIND_NAMES.input, true),
    given !== undefined ? section("Given in this test run", jsonView(given), el("p", { class: "muted small" }, "What the test handed to the component.")) : null,
    section("Takes", inputsView(w),
      el("p", { class: "muted small" }, "Each is filled in by the step that uses the component. Inside, a step reads one as ", mono("input.<name>"), ".")),
    readers.length ? section("Read by", el("div", { class: "chips" }, readers.map((id) =>
      el("button", { type: "button", class: "pill link mono", title: "Show " + id, onclick: () => go({ node: id }) }, id)))) : null,
  ];
}

function outputDetails(w, run) {
  const head = header("output", "output", "output", KIND_NAMES.output, true);
  const reads = w.gives ? w.gives.reads || [] : w.steps.map((s) => s.id);
  const chips = readsChips(w, reads);
  const written = w.gives
    ? section("Gives back, as written", jsonView(w.gives.body), el("p", { class: "muted small" }, "Each ${...} is filled in from the steps when they have run."))
    : section("Gives back", el("ul", { class: "io", "aria-label": "Gives back" }, givenNames(w).map((n) => el("li", {}, el("span", { class: "ioname mono" }, n)))),
        el("p", { class: "muted small" }, "A component of one kind of work gives back exactly what its work puts out, so there is nothing separate to write."));
  const about = checksAbout(w, run, OUT);
  if (!run) return [head, written, chips];
  if (run.gave === undefined) {
    const note = run.status === "running" ? "The test has not got this far yet."
      : couldNotRun(run) ? "The test could not run, so nothing was given back."
      : run.status === "failed" ? "A step failed first, so nothing was given back."
      : "This run has no record of what was given back.";
    return [head, section("In this test run", panelTitled(run.status === "ok" ? "skipped" : "pending", run.status === "ok" ? "Not recorded" : "Not reached", el("div", { class: "muted" }, note))), about, chips];
  }
  return [head,
    section("In this test run", panelTitled("ok", "Gave back", el("div", { class: "muted" }, "Handed to the step that uses it when the steps had finished."))),
    about,
    section("What it gave back", jsonView(run.gave)),
    chips];
}

// waterfall draws each step of a run on a shared time axis.
function waterfall(w, run) {
  let offset = 0;
  const total = Math.max(1, run.steps.reduce((sum, s) => sum + s.ms, 0));
  return el("ol", { class: "waterfall" }, w.steps.map((step, i) => {
    const done = run.steps.find((s) => s.id === step.id);
    const status = progress(run, step.id);
    const start = offset;
    if (done) offset += done.ms;
    const ran = done && (status === "ok" || status === "failed" || status === "running");
    // What the steps inside a flow step did, on the same axis, within its span.
    let nested = null;
    if (step.kind === "flow" && ran) {
      let at = start;
      nested = el("ol", { class: "nest", "aria-label": "Steps inside " + step.id }, innerRun(step, done, status).map((r) => {
        const from = at, took = r.rec && r.rec.status !== "skipped" ? r.rec.ms : 0;
        if (r.rec) at += r.rec.ms;
        const went = r.rec && (r.status === "ok" || r.status === "failed" || r.status === "running");
        return el("li", { class: "nrow kind-" + r.s.kind },
          el("span", { class: "name mono" }, r.s.id),
          el("span", { class: "lane " + r.status }, went ? el("span", { class: "bar " + r.status, css: { left: (from / total) * 100 + "%", width: Math.max(1.5, (took / total) * 100) + "%" } }) : null),
          el("span", { class: "ms " + r.status }, went ? duration(r.rec.ms) : innerWord(r.status, run)));
      }));
    }
    return el("li", {}, el("button", { type: "button", class: "kind-" + step.kind + (editedNodes(w).has(step.id) ? " edited" : ""), "data-node": step.id, onclick: () => go({ node: step.id }) },
      el("span", { class: "n" }, i + 1),
      el("span", { class: "name mono" }, step.id),
      el("span", { class: "lane " + status }, ran
        ? el("span", { class: "bar " + status, css: { left: (start / total) * 100 + "%", width: Math.max(1.5, (done.ms / total) * 100) + "%" } })
        : null),
      el("span", { class: "ms " + status }, ran ? duration(done.ms) : status === "pending" ? (run.status === "ok" ? "no record" : "not reached") : status)),
    nested);
  }));
}

function runDetails(w, run) {
  if (isTestRun(run)) return testRunDetails(w, run);
  return [
    header("workflow", "run", run.run, "Run of " + run.workflow, false, backTo(w, run)),
    section("Result",
      statusPanel(run.status, run.status !== "running" ? el("div", { class: "muted" }, "took " + duration(run.ms) + ", started " + when(run.started)) : null),
      run.error ? el("pre", { class: "error" }, run.error) : null,
      canKeep(w, run) ? keepOffer(run) : null,
      // What was answered is shown on the response node: only a line here.
      run.response ? el("p", { class: "muted small" }, "Answered the caller with ", el("span", { class: "mono" }, run.response.status), ". ", showResponse())
        : w.respond && run.status === "failed" && isWebhook(w) ? el("p", { class: "muted small" }, "The caller got 500 with only the run id. ", showResponse()) : null),
    section("Steps", waterfall(w, run), el("p", { class: "muted small" }, "Select a step, here or on the drawing, to see what it produced.")),
    run.trigger !== undefined ? section("Trigger payload", jsonView(run.trigger)) : null,
    section("About this run", pairs([
      ["Run", el("span", { class: "mono" }, run.run)],
      ["Started by", el("span", { class: "mono" }, run.source)],
      run.commit ? ["Version", el("span", { class: "mono" }, run.commit)] : null,
    ])),
  ];
}

function triggerDetails(w, run) {
  let how = "This workflow has no trigger, so the server does not run it. Run it with flowmaker run.";
  if (isWebhook(w)) {
    how = "A POST request to " + w.trigger.slice(5) + " with a JSON body starts the workflow. Steps read the body as trigger.body.";
    if (w.input) how += " The body must match " + w.input + ": a request whose body does not is refused with status 400, and nothing runs.";
    if (w.respond) how += " The request waits for the answer.";
  }
  else if (w.trigger) how = "The workflow starts on the schedule " + w.trigger.slice(5) + ". Steps can read the start time as trigger.time.";
  return [
    header("trigger", "trigger", w.trigger || "manual", KIND_NAMES.trigger, true),
    run && run.trigger !== undefined ? section("Payload in this run", jsonView(run.trigger)) : null,
    section("How it starts", el("p", {}, how)),
    isWebhook(w) ? section("Try it", example(w)) : null,
  ];
}

// innerWord says in a word why a step inside a flow step has no time.
function innerWord(status, run) {
  return status === "skipped" ? "skipped" : run.status === "running" ? "waiting" : run.status === "ok" ? "no record" : "not reached";
}

// insideSection lists the steps inside a flow step. Without a run they are
// plain rows; in a run each shows how it went, and one with an output opens to
// show it. What is open is remembered here, not in the page, so a redraw keeps it.
function insideSection(step, run, done, status) {
  const rows = innerRun(step, done, status);
  const items = rows.map((r, i) => {
    if (!run) {
      return el("li", { class: "irow kind-" + r.s.kind },
        el("div", { class: "ihead plain" }, kindTile(r.s.kind, 24),
          el("span", { class: "iname mono" }, r.s.id),
          el("span", { class: "itag" }, KIND_LABEL[r.s.kind] || r.s.kind)),
        r.s.subtitle ? el("div", { class: "idetail mono" }, r.s.subtitle) : null,
        r.s.when ? el("div", { class: "icond" }, el("b", {}, "IF"), el("span", { class: "mono" }, r.s.when)) : null);
    }
    const key = run.run + "|" + step.id + "|" + r.s.id;
    const out = r.rec && r.rec.output !== undefined ? r.rec.output : undefined;
    const openable = out !== undefined;
    const went = r.rec && (r.status === "ok" || r.status === "failed" || r.status === "running");
    const body = openable ? el("div", { class: "iout", id: "iout-" + step.id + "-" + i, hidden: !openInner.has(key) }, jsonView(out)) : null;
    const head = el(openable ? "button" : "div", {
        class: "ihead",
        type: openable ? "button" : null,
        "aria-expanded": openable ? (openInner.has(key) ? "true" : "false") : null,
        "aria-controls": openable ? "iout-" + step.id + "-" + i : null,
        "data-focus": openable ? "i:" + key : null,
      },
      statusMark(r.status, 10), kindTile(r.s.kind, 24),
      el("span", { class: "iname mono" }, r.s.id),
      el("span", { class: "ims " + r.status }, went ? duration(r.rec.ms) : innerWord(r.status, run)),
      openable ? el("span", { class: "chev" }, icon("chevron", 14)) : el("span", { class: "chev none" }));
    if (openable) head.addEventListener("click", () => {
      const now = !openInner.has(key);
      if (now) openInner.add(key); else openInner.delete(key);
      head.setAttribute("aria-expanded", now ? "true" : "false");
      body.hidden = !now;
    });
    return el("li", { class: "irow kind-" + r.s.kind + " " + r.status },
      head,
      r.rec && r.rec.detail ? el("div", { class: "idetail mono" }, r.rec.detail) : r.status === "pending" && run.status !== "ok" ? el("div", { class: "idetail" }, status === "failed" ? "An earlier step inside failed first." : "The run did not get this far.") : null,
      r.rec && r.rec.error ? el("pre", { class: "error" }, r.rec.error) : null,
      body);
  });
  return section("Steps inside",
    el("ol", { class: "inside" }, items),
    el("p", { class: "muted small" }, "These steps are written in " + step.uses + ", the component's file. ",
      step.target && entryOf(step.target)
        ? [el("button", { type: "button", class: "linkbtn", onclick: () => go({ workflow: step.target, run: null, node: null }) }, "Open " + compTitle(entryOf(step.target))), " to see how they are built and to change them."]
        : "They are changed there.",
      rows.some((r) => r.rec && r.rec.output !== undefined) ? " Open a step below to read what it produced." : ""));
}

function stepDetails(w, step, run, hasForm) {
  const status = progress(run, step.id);
  const done = run ? run.steps.find((s) => s.id === step.id) : null;
  const output = done && done.output !== undefined ? done.output : null;
  const order = w.steps.indexOf(step) + 1;
  return [
    header(step.kind, step.kind, step.id, KIND_NAMES[step.kind] || step.kind, true),
    run ? section("In this run",
      statusPanel(status,
        done && done.status === "ok" ? el("div", { class: "muted" }, "took " + duration(done.ms)) : null,
        done && done.status === "failed" && done.ms > 0 ? el("div", { class: "muted" }, "failed after " + duration(done.ms)) : null,
        done && done.status === "running" ? el("div", { class: "muted" }, "in progress") : null,
        status === "skipped" ? el("div", { class: "muted" }, "Its IF condition was false.") : null,
        status === "pending" ? el("div", { class: "muted" }, couldNotRun(run) ? "The test could not run, so this step was not run." : run.status === "failed" ? "An earlier step failed first." : run.status === "ok" ? "This run has no record of this step: it was recorded before the step was added." : "The run did not get this far.") : null),
      done && done.detail ? el("p", { class: "detail mono" }, done.detail) : null,
      done && done.error ? el("pre", { class: couldNotRun(run) ? "plain" : "error" }, done.error) : null) : null,
    checksAbout(w, run, step.id),
    step.component ? componentLink(step) : null,
    step.kind === "flow" ? insideSection(step, run, done, status) : null,
    step.questions && output ? section("Answers", step.questions.map((q) => question(q, output[q.name]))) : null,
    step.questions && !output && !hasForm ? section("Questions", step.questions.map((q) => question(q, null))) : null,
    output !== null && !step.questions ? section(step.kind === "flow" ? "Gives back" : "Output", jsonView(output),
      step.kind === "flow" ? el("p", { class: "muted small" }, "Only what the component gives back. What each step inside produced is under Steps inside.") : null) : null,
    hasForm ? null : section("Runs when",
      step.when ? el("pre", { class: "cond-pre" }, step.when)
        : el("p", { class: "muted" }, isComp(w) && w.steps.length === 1 ? "Always: this is the work the component does." : "Always, once the steps before it have finished."),
      el("p", { class: "muted small" }, "Step " + order + " of " + w.steps.length + " in file order.")),
    // What a component does is set in its own file, so it is shown, not edited.
    step.component ? section("Component",
      pairs(step.fields.map(([label, value]) => [label, el("span", { class: "mono" }, value)])),
      step.kind === "flow" ? null : el("p", { class: "muted small" }, "Written once in " + step.uses + " and shared by every step that uses it. This step only chooses its input. Change what it does in the component.")) : null,
    step.fields.length && !hasForm && !step.component ? section("Settings", pairs(step.fields.map(([label, value]) => [label, el("span", { class: "mono" }, value)]))) : null,
    readsChips(w, step.reads),
  ];
}

// componentLink is the way from a step to the component it uses: its name, what
// it does and a button that opens it.
function componentLink(step) {
  const x = step.target ? entryOf(step.target) : null;
  if (!x) return null;
  return section("Uses component",
    el("button", { type: "button", class: "compcard kind-" + compKind(x), "data-focus": "open-component", title: "Open the component " + compTitle(x), onclick: () => go({ workflow: x.name, run: null, node: null }) },
      kindTile(compKind(x), 34),
      el("span", { class: "ctext" },
        el("span", { class: "cname mono" }, compTitle(x)),
        el("span", { class: "cdesc" }, x.description || compKindWord(x)),
        el("span", { class: "cmeta" }, compKindWord(x) + " · used by " + plural(usesOf(x), "step"))),
      el("span", { class: "copen" }, "Open", icon("open", 13))));
}

// removeButton asks twice, like Discard: the first click arms it, the second removes.
function removeButton(w, step) {
  const b = el("button", { type: "button", class: "btn danger", "data-focus": "remove:" + step.id }, icon("trash", 14), "Remove step");
  const paint = () => {
    const armed = uiOf(w.name).removeArmed === step.id;
    b.classList.toggle("armed", armed);
    b.replaceChildren(icon("trash", 14), armed ? "Remove " + step.id + "? Click again" : "Remove step");
  };
  paint();
  b.addEventListener("click", () => {
    const st = uiOf(w.name);
    if (st.saving) return;
    if (st.removeArmed !== step.id) {
      st.removeArmed = step.id;
      clearTimeout(st.removeTimer);
      st.removeTimer = setTimeout(() => { if (st.removeArmed === step.id) { st.removeArmed = null; paint(); } }, 4000);
      paint();
      return;
    }
    clearTimeout(st.removeTimer);
    st.removeArmed = null;
    paint();
    removeStep(w.name, step.id);
  });
  b.addEventListener("blur", () => {
    const st = uiOf(w.name);
    if (st.removeArmed === step.id) { st.removeArmed = null; clearTimeout(st.removeTimer); paint(); }
  });
  return b;
}

// responseDetails is the inspector for the way out. Without a run it says what
// the workflow answers with; with one, what was answered.
function responseDetails(w, run) {
  const r = w.respond, got = run ? run.response : null;
  const chips = r.reads && r.reads.length ? section("Reads from", el("div", { class: "chips" }, r.reads.map((name) =>
    el("button", { type: "button", class: "pill link mono", title: "Show " + name, onclick: () => go({ node: name }) }, name)))) : null;
  const head = header("response", "response", "response", KIND_NAMES.response, true);
  const about = checksAbout(w, run, "response");
  if (!run) {
    return [head,
      section("Answers with", pairs([["Status", el("span", { class: "mono" }, r.status)]]),
        el("p", { class: "muted small" }, "The request waits for this answer. " + (w.input ? "A body that does not match " + w.input + " is refused earlier, with 400." : ""))),
      section("Body as written", jsonView(r.body), el("p", { class: "muted small" }, "Each ${...} is filled in from the steps when they have run.")),
      chips];
  }
  if (!got) {
    const failed = run.status === "failed", webhook = run.source.startsWith("webhook");
    const note = run.status === "running" ? "The run has not got this far yet."
      : failed && webhook ? "The run failed first, so the caller got 500 with only the run id."
      : failed ? "The run failed first. It was not started by a request, so nobody was waiting."
      : "This run was not started by a request, so nobody was waiting for an answer.";
    return [head,
      section("In this run", panelTitled(run.status === "ok" ? "skipped" : "pending", run.status === "ok" ? "No caller" : "Not reached", el("div", { class: "muted" }, note))),
      about, chips];
  }
  const headers = got.headers ? Object.entries(got.headers) : [];
  return [head,
    section("In this run", panelTitled("ok", "Answered " + got.status,
      got.status >= 400 ? el("div", { class: "muted" }, "A status the workflow chose itself, not a failure.") : el("div", { class: "muted" }, "Sent to the caller when the steps had finished."))),
    headers.length ? section("Headers", pairs(headers.map(([k, v]) => [k, el("span", { class: "mono" }, String(v))]))) : null,
    about,
    section("Body", got.body !== undefined && got.body !== null ? jsonView(got.body) : el("p", { class: "muted" }, "No body.")),
    chips];
}

// question shows one question of a decision step: what it asks, what it can be
// answered with, and in a run which answer won and how likely each was.
function question(q, a) {
  const scored = q.answers.startsWith("score");
  const yesNo = !(q.options && q.options.length);
  let options = q.options && q.options.length ? q.options : [{ name: "yes", meaning: q.yesIf }, { name: "no", meaning: q.noIf }];
  let probs = a && a.probabilities ? a.probabilities : null;
  if (!probs && a && typeof a.probability === "number" && yesNo) probs = { yes: a.probability, no: 1 - a.probability };
  if (yesNo && probs && !q.yesIf && !q.noIf) options = Object.keys(probs).map((name) => ({ name, meaning: "" }));
  const picked = a ? String(a.value) : null;

  return el("div", { class: "question" },
    el("div", { class: "qhead" },
      el("span", { class: "qname mono" }, q.name),
      el("span", { class: "qkind" }, scored ? "Score" : yesNo ? "Yes or no" : "Choice")),
    el("div", { class: "ask" }, q.ask),
    a ? el("div", { class: "verdict" },
      el("span", { class: "label" }, "Answer"),
      el("span", { class: "picked mono" }, picked),
      typeof a.confidence === "number" ? el("span", { class: "muted" }, "confidence " + percent(a.confidence)) : null,
      typeof a.score === "number" ? el("span", { class: "muted" }, "score " + a.score.toFixed(2)) : null) : null,
    scored && !a ? el("div", { class: "scale" }, "Lowest to highest") : null,
    el(scored ? "ol" : "ul", { class: "options" }, options.map((o) => {
      const p = probs && o.name in probs ? Number(probs[o.name]) : null;
      return el("li", { class: o.name === picked ? "chosen" : null },
        el("div", { class: "line" },
          el("span", { class: "opt mono" }, o.name),
          o.name === picked ? el("span", { class: "tick" }, icon("ok", 11)) : null,
          p != null ? el("span", { class: "pct" }, percent(p)) : null),
        p != null ? el("div", { class: "track" }, el("span", { class: "fill", css: { width: Math.max(0.8, p * 100) + "%" } })) : null,
        o.meaning ? el("div", { class: "meaning" }, o.meaning) : null);
    })));
}

// ---- editing ----
//
// What the person has typed lives in `drafts`, not in the page, so a redraw can
// never lose it. The form is a long-lived piece of the page: when the data
// behind it changes it is updated in place (see syncForm) instead of rebuilt,
// which keeps the focus, the caret and the text being typed where they are.

let epoch = 0;          // counts saves, so an answer older than a save can be told apart
let uiTick = 0;         // bumped when something outside the data changes what is drawn
let formId = 0;
let form = null;        // the form now on the page: { key, workflow, node, fields, controls, root }
let flagSig = "";       // which steps had unsaved changes the last time the flags were drawn
const drafts = {};      // workflow -> { fields: { pathKey: { path, value, node, label, group } }, prompts: { step: text }, schemas: { step: { fields } or { text } } }
const schemaCache = {}; // "workflow|step" -> { state, file, text, rev, fields, wfRev, inflight, error }: an AI step's answer fields
const adds = {};        // workflow -> the step being added: { after, component, id, idTouched, when, values }
let adder = null;       // the add-a-step form now on the page
let making = null;      // the workflow being made: { root, name, trigger, path, pathTouched, cron, step } (step as in adds)
let maker = null;       // the new-workflow form now on the page
const known = {};       // workflow -> the rev this page last saw
const ui = {};          // workflow -> { saving, notice, conflicts, armed }
const promptCache = {}; // "workflow|step" -> { state, file, text, rev, wfRev, inflight, error }
const changesCache = {}; // workflow -> { state, data, error, stamp, inflight }
let changesEpoch = 0;   // bumped by every save, so the diff is fetched again
const insp = { head: null, body: null, formslot: null, rest: null, bar: null };

const pathKey = (path) => JSON.stringify(path);
const draftsOf = (name) => drafts[name] || (drafts[name] = { fields: {}, prompts: {}, schemas: {} });
const uiOf = (name) => ui[name] || (ui[name] = { saving: false, notice: null, conflicts: {}, armed: false, armTimer: 0 });
const workflowByName = (name) => state.workflows.find((x) => x.name === name);
const plural = (n, one, many) => n + " " + (n === 1 ? one : many || one + "s");
const isMac = /Mac|iPhone|iPad/.test(navigator.platform || "");

async function request(method, path, body) {
  const response = await fetch(path, {
    method,
    headers: body ? { Accept: "application/json", "Content-Type": "application/json" } : { Accept: "application/json" },
    body: body ? JSON.stringify(body) : undefined,
  });
  let data = null;
  try { data = await response.json(); } catch (err) { data = null; }
  return { ok: response.ok, status: response.status, body: data || {} };
}

// fieldIndex maps every editable field of a workflow, by path, to the field and
// the node (the trigger or a step) it belongs to.
function fieldIndex(w) {
  const map = new Map();
  for (const [node, fields] of editSets(w)) fields.forEach((f) => map.set(pathKey(f.path), { field: f, node }));
  return map;
}

// editSets lists the editable fields of a workflow or component by the node
// they belong to: the trigger and the response, or the output of a component,
// and each step.
function editSets(w) {
  const sets = [];
  if (isComp(w)) { if (w.gives) sets.push([OUT, w.gives.edit || []]); }
  else {
    sets.push(["trigger", w.edit || []]);
    if (w.respond) sets.push(["response", w.respond.edit || []]);
  }
  w.steps.forEach((s) => sets.push([s.id, s.edit || []]));
  return sets;
}

// pruneDrafts forgets drafts that now say what the file says, and drafts for
// fields that are no longer in the file.
function pruneDrafts(w) {
  const d = draftsOf(w.name), index = fieldIndex(w);
  for (const [k, e] of Object.entries(d.fields)) {
    const f = index.get(k);
    if (!f || f.field.value === e.value) delete d.fields[k];
  }
  for (const [step, text] of Object.entries(d.prompts)) {
    const p = promptCache[w.name + "|" + step];
    if (!w.steps.some((s) => s.id === step) || (p && p.state === "ready" && p.text === text)) delete d.prompts[step];
  }
  for (const [step, draft] of Object.entries(d.schemas)) {
    const c = schemaCache[schemaKey(w.name, step)];
    if (!w.steps.some((s) => s.id === step) || (c && c.state === "ready" && schemaSame(draft, c))) delete d.schemas[step];
  }
}

function dirtyCount(name) {
  const d = drafts[name];
  return d ? Object.keys(d.fields).length + Object.keys(d.prompts).length + Object.keys(d.schemas).length : 0;
}
// draftCountFor counts the unsaved changes that belong to one step.
function draftCountFor(name, node) {
  const d = drafts[name];
  if (!d) return 0;
  return Object.values(d.fields).filter((e) => e.node === node).length + (d.prompts[node] !== undefined ? 1 : 0) + (d.schemas[node] !== undefined ? 1 : 0);
}
function dirtyNodes(name) {
  const d = drafts[name];
  if (!d) return [];
  return [...new Set([...Object.values(d.fields).map((e) => e.node), ...Object.keys(d.prompts), ...Object.keys(d.schemas)])];
}
const editedNodes = (w) => new Set(state.editable ? dirtyNodes(w.name) : []);
const addTouched = (a) => !!a && (a.edited || a.idTouched || a.when.trim() !== "" || Object.values(a.values).some((v) => v.trim() !== ""));
const anyDirty = () => state.editable && (state.workflows.some((w) => dirtyCount(w.name) > 0 || addTouched(adds[w.name])) || Object.values(keeps).some((k) => k.touched) || makeTouched());

// adopt takes in a fresh answer from /api/state. A workflow whose file changed
// without this page saving it keeps the person's drafts, on top of the new text.
function adopt(fresh) {
  const before = state;
  state = fresh;
  for (const w of state.workflows) {
    if (known[w.name] === undefined) known[w.name] = w.rev;
    else if (known[w.name] !== w.rev) {
      known[w.name] = w.rev;
      changedOutside(w.name, before.workflows.find((x) => x.name === w.name), w);
    }
    pruneDrafts(w);
  }
  flagSig = "";
  syncRounds();
}

// A field is known by the step it belongs to, its group and its label, not by
// its position in the file: positions move when a step is added or removed,
// from this page or by someone editing the file. identities lists the fields
// of a workflow both ways, by path and by that identity (the Nth field with
// the same step, group and label counts as a different one).
function identities(w) {
  const byKey = new Map(), byId = new Map(), seen = new Map();
  const add = (node, f) => {
    const base = [node, f.group || "", f.label].join("\u0001");
    const n = (seen.get(base) || 0) + 1;
    seen.set(base, n);
    const id = base + "\u0001" + n, key = pathKey(f.path);
    byKey.set(key, id);
    byId.set(id, { field: f, key, node });
  };
  for (const [node, fields] of editSets(w)) fields.forEach((f) => add(node, f));
  return { byKey, byId };
}

// reattach moves the drafts of a workflow from the paths they had in prev to
// the paths the same fields have in w. A draft whose step or field is gone is
// dropped, and returned so the person is told. Fields whose text changed in
// the file meanwhile are marked as conflicts.
function reattach(name, prev, w) {
  const d = draftsOf(name), st = uiOf(name);
  const dropped = [];
  if (!prev) return dropped;
  const was = identities(prev), now = identities(w);
  const fields = {}, conflicts = {};
  for (const [k, e] of Object.entries(st.conflicts)) {
    const id = was.byKey.get(k), n = id && now.byId.get(id);
    if (n) conflicts[n.key] = e;
    else if (k.startsWith("prompt|")) conflicts[k] = e;
  }
  for (const [k, e] of Object.entries(d.fields)) {
    const id = was.byKey.get(k), n = id && now.byId.get(id);
    if (!n) { dropped.push({ node: e.node, label: e.label }); continue; }
    fields[n.key] = { ...e, path: n.field.path, node: n.node, group: n.field.group };
    const o = was.byId.get(id);
    if (o && o.field.value !== n.field.value) conflicts[n.key] = n.field.value;
  }
  for (const step of Object.keys(d.prompts)) {
    if (!w.steps.some((s) => s.id === step)) { dropped.push({ node: step, label: "Prompt" }); delete d.prompts[step]; }
  }
  for (const step of Object.keys(d.schemas)) {
    if (!w.steps.some((s) => s.id === step)) { dropped.push({ node: step, label: "Answer fields" }); delete d.schemas[step]; }
  }
  d.fields = fields;
  st.conflicts = conflicts;
  return dropped;
}

function changedOutside(name, prev, w) {
  const st = uiOf(name), d = draftsOf(name);
  const dropped = reattach(name, prev, w);
  const kept = dirtyCount(name);
  const lost = [...(st.notice && st.notice.dropped ? st.notice.dropped : []), ...dropped];
  if (st.notice && st.notice.kind === "conflict") st.notice = { ...st.notice, kept, dropped: lost.length ? lost : null };
  else if (kept > 0 || lost.length || (view && view.w.name === name && selected.node)) st.notice = { kind: "external", kept, dropped: lost.length ? lost : null };
}

// ---- the form ----

function autosize(box) {
  if (box.tagName !== "TEXTAREA" || box.classList.contains("prompt")) return;
  box.style.height = "auto";
  box.style.height = box.scrollHeight + 2 + "px";
}

function setValue(box, value) {
  if (box.value === value) return;
  if (box.tagName === "SELECT" && ![...box.options].some((o) => o.value === value)) box.append(el("option", { value }, value));
  let range = null;
  try { if (box.selectionStart != null && document.activeElement === box) range = [box.selectionStart, box.selectionEnd]; } catch (err) { range = null; }
  box.value = value;
  if (range) { try { box.setSelectionRange(Math.min(range[0], value.length), Math.min(range[1], value.length)); } catch (err) { /* not a text box */ } }
  autosize(box);
}

function makeControl(f, field) {
  const id = "f" + (++formId);
  const c = { field, key: pathKey(field.path), kind: field.kind, base: field.value, node: f.node, workflow: f.workflow, id };
  const mono = field.kind === "expression";
  let box;
  if (field.kind === "choice") {
    const options = (field.options || []).slice();
    if (!options.includes(field.value)) options.unshift(field.value);
    box = el("select", { id, class: "in" }, options.map((o) => el("option", { value: o }, o)));
  } else if (field.kind === "prompt") {
    box = el("textarea", { id, class: "in mono prompt", rows: 14, spellcheck: "false", disabled: true });
  } else if (field.value.length > 56 || field.value.includes("\n")) {
    box = el("textarea", { id, class: "in" + (mono ? " mono" : ""), rows: 2, spellcheck: "false" });
  } else {
    // A whole number is still typed as text, so a wrong value can be sent and answered by the server.
    box = el("input", { id, type: "text", class: "in" + (mono ? " mono" : ""), spellcheck: "false", autocomplete: "off", autocapitalize: "off", inputmode: field.kind === "number" ? "numeric" : null });
  }
  c.input = box;

  const hintId = id + "-hint";
  const hintText = field.hint || null;
  if (hintText && field.kind !== "prompt") box.setAttribute("aria-describedby", hintId);
  if (field.optional && field.kind !== "prompt") box.setAttribute("placeholder", "Not set");

  c.revert = el("button", { type: "button", class: "linkbtn revert", hidden: true, title: "Put back what the file says" }, "Revert");
  c.revert.addEventListener("click", () => {
    const d = draftsOf(c.workflow);
    if (c.kind === "prompt") delete d.prompts[c.node]; else delete d.fields[c.key];
    delete uiOf(c.workflow).conflicts[c.key];
    setValue(box, c.base);
    paintRow(c);
    draftChanged(c.workflow);
    box.focus();
  });
  c.conflict = el("p", { class: "conflict", hidden: true });
  c.note = c.kind === "prompt" ? el("p", { class: "promptnote", role: "status" }) : null;
  c.row = el("div", { class: "field" + (field.group ? " grouped" : "") + " k-" + field.kind },
    el("div", { class: "lab" },
      el("label", { for: id }, field.label),
      mono ? el("span", { class: "tag", title: "A CEL expression" }, "CEL") : null,
      field.kind === "prompt" ? el("span", { class: "file mono", title: "The prompt is stored in this file" }, field.value) : null,
      c.revert),
    box,
    c.note,
    hintText ? el("p", { class: "hint-text", id: hintId }, hintText) : null,
    c.conflict);

  const onInput = () => {
    const d = draftsOf(c.workflow), st = uiOf(c.workflow);
    const value = box.value;
    if (c.kind === "prompt") {
      if (value === c.base) delete d.prompts[c.node]; else d.prompts[c.node] = value;
    } else if (value === c.base) delete d.fields[c.key];
    else d.fields[c.key] = { path: field.path, value, node: c.node, label: field.label, group: field.group };
    if (!(c.kind === "prompt" ? d.prompts[c.node] !== undefined : d.fields[c.key])) delete st.conflicts[c.key];
    if (st.notice && (st.notice.kind === "saved" || st.notice.kind === "external") && !(st.notice.dropped && st.notice.dropped.length)) st.notice = null;
    autosize(box);
    paintRow(c);
    draftChanged(c.workflow);
  };
  box.addEventListener("input", onInput);
  return c;
}

function draftOf(c) {
  const d = draftsOf(c.workflow);
  if (c.kind === "prompt") return d.prompts[c.node] === undefined ? null : { value: d.prompts[c.node] };
  return d.fields[c.key] || null;
}

function paintRow(c) {
  const edited = !!draftOf(c);
  c.row.classList.toggle("edited", edited);
  c.revert.hidden = !edited;
  const fresh = uiOf(c.workflow).conflicts[c.key];
  c.conflict.hidden = !(edited && fresh !== undefined);
  if (!c.conflict.hidden) c.conflict.replaceChildren("The file now says: ", el("span", { class: "mono" }, fresh.length > 240 ? fresh.slice(0, 240) + "…" : fresh));
  // The node editor lays some fields out in its own way and repaints them here.
  if (c.after) c.after(c);
}

function buildForm(w, nodeId, fields, key) {
  const f = { key, workflow: w.name, node: nodeId, fields, controls: [], root: null };
  const body = [];
  let group = null, groupName = null;
  for (const field of fields) {
    const c = makeControl(f, field);
    f.controls.push(c);
    if (field.group) {
      if (groupName !== field.group) {
        groupName = field.group;
        group = el("fieldset", { class: "group" }, el("legend", {}, field.group));
        body.push(group);
      }
      group.append(c.row);
    } else {
      groupName = null;
      body.push(c.row);
    }
  }
  f.root = el("section", { class: "editform" }, el("h2", {}, nodeId === OUT ? "Gives back" : "Settings"), body);
  return f;
}

function showForm(w, nodeId, fields) {
  if (!nodeId) {
    if (form) { form = null; insp.formslot.replaceChildren(); }
    return;
  }
  const key = [w.name, nodeId, fields.map((f) => [pathKey(f.path), f.kind, f.label, f.group || "", f.hint || "", (f.options || []).join(",")].join("~")).join("|")].join("##");
  if (form && form.key === key) {
    form.fields = fields;
  } else {
    // The paths of the fields change when a step is added or removed. The form is
    // rebuilt then, and the reader keeps their place in it.
    let back = null;
    if (form && form.workflow === w.name && form.node === nodeId && form.root.contains(document.activeElement)) {
      const i = form.controls.findIndex((c) => c.input === document.activeElement);
      const at = i >= 0 ? document.activeElement : null;
      if (at) { try { back = [i, at.selectionStart, at.selectionEnd]; } catch (err) { back = [i, null, null]; } }
    }
    form = buildForm(w, nodeId, fields, key);
    insp.formslot.replaceChildren(form.root);
    syncForm(w);
    if (back && form.controls[back[0]]) {
      const box = form.controls[back[0]].input;
      box.focus({ preventScroll: true });
      if (back[1] != null) { try { box.setSelectionRange(back[1], back[2]); } catch (err) { /* not a text box */ } }
    }
    return;
  }
  syncForm(w);
}

// syncForm brings the controls up to date with the data and the drafts without
// touching a control whose text already matches.
function syncForm(w, other) {
  const f = other || form;
  if (!f || f.workflow !== w.name) return;
  f.controls.forEach((c, i) => {
    c.field = f.fields[i];
    if (c.kind === "prompt") return syncPrompt(c, w);
    c.base = c.field.value;
    const d = draftOf(c);
    setValue(c.input, d ? d.value : c.base);
    paintRow(c);
  });
}

function syncPrompt(c, w) {
  const key = w.name + "|" + c.node;
  const p = promptCache[key];
  if (!p || (p.wfRev !== w.rev && !p.inflight && p.state !== "error")) loadPrompt(w, c.node);
  const now = promptCache[key];
  c.input.disabled = now.state !== "ready";
  c.note.hidden = now.state === "ready";
  if (now.state === "loading") c.note.replaceChildren("Loading the prompt…");
  else if (now.state === "error") {
    const again = el("button", { type: "button", class: "linkbtn" }, "Try again");
    again.addEventListener("click", () => { promptCache[key] = { state: "loading", wfRev: w.rev }; loadPrompt(w, c.node, true); syncForm(w); });
    c.note.replaceChildren("Could not load the prompt: " + now.error + " ", again);
  }
  if (now.state === "ready") {
    c.base = now.text;
    const d = draftOf(c);
    setValue(c.input, d ? d.value : now.text);
    autosize(c.input);
  }
  paintRow(c);
}

function loadPrompt(w, step, force) {
  const key = w.name + "|" + step;
  const prev = promptCache[key];
  if (prev && prev.inflight && !force) return Promise.resolve();
  promptCache[key] = { ...(prev || { state: "loading" }), inflight: true, wfRev: w.rev };
  return request("GET", "/api/prompt?workflow=" + encodeURIComponent(w.name) + "&step=" + encodeURIComponent(step))
    .then((r) => {
      promptCache[key] = r.ok
        ? { state: "ready", file: r.body.file, text: r.body.text, rev: r.body.rev, wfRev: w.rev }
        : { state: "error", error: r.body.error || "the server answered " + r.status, wfRev: w.rev };
    })
    .catch(() => { promptCache[key] = { state: "error", error: "the server cannot be reached", wfRev: w.rev }; })
    .then(() => {
      const now = workflowByName(w.name);
      if (!now) return;
      pruneDrafts(now);
      if (form && form.workflow === w.name) syncForm(now);
      draftChanged(w.name, true);
    });
}

// draftChanged refreshes what shows unsaved changes, other than the form.
function draftChanged(name, force) {
  syncEditorValues();
  const w = workflowByName(name);
  if (!w || !view || view.w.name !== name) { updateBar(); return; }
  updateBar();
  const sig = name + ":" + dirtyNodes(name).sort().join(",");
  if (sig === flagSig && !force) return;
  flagSig = sig;
  const edited = editedNodes(w);
  for (const node of document.querySelectorAll("#canvas .card[data-node]")) {
    const on = edited.has(node.dataset.node);
    node.classList.toggle("edited", on);
    node.setAttribute("aria-label", node.dataset.label + (on ? ", unsaved changes" : ""));
  }
  for (const node of document.querySelectorAll("#inspector [data-node]")) node.classList.toggle("edited", edited.has(node.dataset.node));
  paintMenu();
  $("changes").replaceChildren(...(state.editable && !isComp(w) ? [changesChip(w)] : []));
  // With nothing picked the panel lists the changed steps, or is there only for them.
  if (!isComp(w) && !selected.node && !selected.run && !selected.add && !selected.test && !selected.make && !selected.list) { uiTick++; render(); }
}

// flagFor marks a workflow in the list on the left: a file problem, or unsaved changes.
function flagFor(x) {
  if (!state.editable) return null;
  if (x.problem) return el("span", { class: "flag problem", title: "The file has a problem: " + x.problem, role: "img", "aria-label": "The file has a problem" }, "!");
  const n = dirtyCount(x.name);
  const testing = roundActive(x.name);
  if (!n && !testing) return null;
  return el("span", { class: "flags" },
    testing ? el("span", { class: "flag testing", title: "Tests are running", role: "img", "aria-label": "Tests are running" }) : null,
    n ? el("span", { class: "flag edit", title: plural(n, "unsaved change"), role: "img", "aria-label": plural(n, "unsaved change") }) : null);
}

// changesChip opens what changed in the files. Right after a save it says so,
// until the changes are looked at.
function changesChip(w) {
  const uncommitted = /uncommitted/.test(state.commit || "");
  const st = uiOf(w.name);
  const saved = !!st.notice && st.notice.kind === "saved";
  return el("button", {
      type: "button", class: "chip changes" + (selected.changes ? " on" : ""), "data-focus": "changes",
      "aria-pressed": selected.changes ? "true" : "false",
      title: "Show what changed in the workflow files, as git sees it" + (state.commit ? ". Running " + state.commit : ""),
      onclick: () => { if (saved) st.notice = null; go({ changes: !selected.changes }); },
    },
    uncommitted ? el("span", { class: "dot edit", title: "There are uncommitted changes" }) : null,
    saved ? (uncommitted ? "Saved · not committed" : "Saved") : "Changes");
}

function problemBanner(w) {
  if (!w.problem) return null;
  const kept = dirtyCount(w.name);
  return el("div", { class: "banner", role: "alert" },
    el("div", { class: "btitle" }, "This workflow's file has a problem"),
    el("p", {}, "The server keeps running the last valid version, and editing is switched off for this workflow until the file is fixed. Fix it in your editor: this page notices by itself."
      + (kept ? " Your " + plural(kept, "unsaved change") + " " + (kept === 1 ? "is" : "are") + " kept." : "")),
    el("pre", { class: "mono" }, w.problem));
}

// ---- the save bar ----

function ensureInspector() {
  if (insp.body) return;
  insp.head = el("header");
  insp.formslot = el("div", { class: "formslot" });
  insp.rest = el("div", { class: "rest" });
  insp.body = el("div", { class: "body" }, insp.formslot, insp.rest);
  insp.bar = buildBar();
  $("inspector").replaceChildren(insp.head, insp.body, insp.bar.root);
}

function buildBar() {
  const b = {};
  b.slot = el("div", { class: "noticeslot" });
  b.status = el("span", { class: "barstatus", role: "status" });
  b.discard = el("button", { type: "button", class: "btn" }, "Discard");
  b.save = el("button", { type: "button", class: "btn primary" }, "Save", el("kbd", {}, isMac ? "⌘S" : "Ctrl+S"));
  // Said once, for a component: what a save here reaches.
  b.shared = el("p", { class: "shared", hidden: true });
  b.root = el("div", { class: "savebar", hidden: true }, b.slot, b.shared, el("div", { class: "barrow" }, b.status, b.discard, b.save));
  b.shownNotice = null;
  b.discard.addEventListener("click", () => discardCurrent());
  b.save.addEventListener("click", () => saveCurrent());
  b.discard.addEventListener("blur", () => { if (view) disarm(view.w.name); });
  return b;
}

function disarm(name) {
  const st = uiOf(name);
  clearTimeout(st.armTimer);
  if (st.armed) { st.armed = false; updateBar(); }
}

function updateBar() {
  paintFoot();
  const b = insp.bar;
  if (!b || !view) return;
  if (view.w.draft) return paintMakeBar(b);
  const w = view.w, st = uiOf(w.name), n = dirtyCount(w.name);
  const adding = addMode(w), testing = testMode(w);
  const on = state.editable && !w.problem && (adding || testing || form || n > 0 || st.notice);
  b.root.hidden = !on;
  if (!on) return;
  const share = isComp(w) && !adding;
  b.shared.hidden = !share;
  if (share) {
    const uses = usesOf(w);
    b.shared.replaceChildren(icon("component", 13), el("span", {}, uses
      ? ["A change here applies to every step that uses ", el("strong", {}, compTitle(w)), ": ", el("strong", {}, plural(uses, "step")), "."]
      : ["No step uses ", el("strong", {}, compTitle(w)), " yet, so a change here reaches no workflow."]));
    b.shared.title = (w.usedBy || []).map((u) => (entryOf(u.workflow) ? compTitle(entryOf(u.workflow)) : u.workflow) + " › " + u.step).join("\n");
  }
  const key = isMac ? "⌘S" : "Ctrl+S";
  if (testing) {
    // The same bar serves the form for a new test: Cancel and Add test.
    const td = keepFor(w), ready = !!td && td.state === "ready";
    const issues = ready ? keepIssues(w, td) : [];
    b.status.replaceChildren(...(st.saving ? ["Adding…"]
      : !td || td.state === "loading" ? ["Reading the run…"]
      : td.state === "error" ? ["This run cannot become a test."]
      : issues.length && !st.notice ? [issues[0].message]
      : ["Writes the test and its fixture file."]));
    b.status.classList.remove("dirty");
    b.save.setAttribute("aria-disabled", st.saving || !ready ? "true" : "false");
    b.discard.setAttribute("aria-disabled", st.saving ? "true" : "false");
    b.discard.classList.remove("armed");
    b.discard.textContent = "Cancel";
    if (b.save.dataset.mode !== "test") { b.save.dataset.mode = "test"; b.save.replaceChildren("Add test", el("kbd", {}, key)); }
    if (b.shownNotice !== st.notice) {
      b.shownNotice = st.notice;
      b.slot.replaceChildren(...(st.notice ? [noticeView(st.notice, w)] : []));
    }
    return;
  }
  if (adding) {
    // The same bar serves the form for a new step: Cancel and Add step.
    const comp = chosenOf(w, addOf(w.name));
    b.status.replaceChildren(...(st.saving ? ["Adding…"] : comp ? comp.kind === "ai" && BUILTIN.ai === comp ? ["Adds the step, with a prompt file and a schema file."] : ["Adds a step to ", el("span", { class: "mono" }, w.path)] : ["Choose what kind of step to add."]));
    b.status.classList.remove("dirty");
    b.save.setAttribute("aria-disabled", st.saving || !comp ? "true" : "false");
    b.discard.setAttribute("aria-disabled", st.saving ? "true" : "false");
    b.discard.classList.remove("armed");
    b.discard.textContent = "Cancel";
    if (b.save.dataset.mode !== "add") { b.save.dataset.mode = "add"; b.save.replaceChildren("Add step", el("kbd", {}, key)); }
    if (b.shownNotice !== st.notice) {
      b.shownNotice = st.notice;
      b.slot.replaceChildren(...(st.notice ? [noticeView(st.notice, w)] : []));
    }
    return;
  }
  if (b.save.dataset.mode !== "save") { b.save.dataset.mode = "save"; b.save.replaceChildren("Save", el("kbd", {}, key)); }
  const names = dirtyNodes(w.name).map(nodeName);
  const where = names.length > 3 ? names.slice(0, 3).join(", ") + " and " + (names.length - 3) + " more" : names.join(", ");
  b.status.replaceChildren(...(st.saving ? ["Saving…"]
    : n ? [plural(n, "unsaved change"), " in ", el("span", { class: "mono" }, where)]
    : ["Saves to ", el("span", { class: "mono" }, w.path)]));
  b.status.classList.toggle("dirty", n > 0 && !st.saving);
  b.save.setAttribute("aria-disabled", st.saving || n === 0 ? "true" : "false");
  b.discard.setAttribute("aria-disabled", st.saving || n === 0 ? "true" : "false");
  b.discard.classList.toggle("armed", st.armed);
  b.discard.textContent = st.armed ? "Discard " + plural(n, "change") + "?" : "Discard";
  if (b.shownNotice !== st.notice) {
    b.shownNotice = st.notice;
    b.slot.replaceChildren(...(st.notice ? [noticeView(st.notice, w)] : []));
  }
}

function dismissNotice(name) {
  uiOf(name).notice = null;
  // The panel may have been there only for the notice.
  uiTick++;
  render();
}

// noticeView says in plain words what happened to a save, near the form.
function noticeView(n, w) {
  const tone = { saved: "ok", invalid: "bad", error: "bad", network: "bad", conflict: "warn", external: "warn" }[n.kind];
  const text = [];
  let title = "";
  // What was being done: a plain save, or adding or removing a step.
  const isTest = n.did === "test", isUntest = n.did === "untest";
  const isMake = n.did === "make";
  const isAdd = n.did === "add" || isTest || isMake, isRemove = n.did === "remove" || isUntest;
  if (n.kind === "saved") {
    title = n.title || "Saved";
    // A component has no changes view of its own: its file is part of the workflow that uses it.
    const home = changesHome(w);
    text.push(el("p", {}, "Written to ", n.files.map((f, i) => [i ? ", " : "", el("span", { class: "mono" }, f)])), el("p", {},
      "These changes are saved in the file but not committed. ",
      !isComp(w) ? el("button", { type: "button", class: "linkbtn", onclick: () => go({ changes: true }) }, "See what changed")
        : home ? el("button", { type: "button", class: "linkbtn", onclick: () => go({ workflow: home.name, run: null, node: null, changes: true }) }, "See what changed in " + home.name) : null),
      isComp(w) && usesOf(w) ? el("p", {}, "It now applies to ", plural(usesOf(w), "step"), " that use" + (usesOf(w) === 1 ? "s" : ""), " ", compTitle(w), ".") : null,
      isUntest ? el("p", {}, "Its fixture file ", mono(n.fixture), " was left in the folder. Delete it yourself if nothing else uses it.") : null,
      isTest ? testNext(n, w) : null,
      droppedView(n));
  } else if (n.kind === "invalid") {
    title = isMake ? "Not made. Nothing was written." : isTest ? "Not added. Nothing was written." : isAdd ? "Not added. The file was left as it was." : isRemove ? "Not removed. The file was left as it was."
      : isComp(w) ? "Not saved. The component was left as it was." : "Not saved. The file was left as it was.";
    if (n.saved && n.saved.length) text.push(el("p", {}, "Already written: ", n.saved.join(", "), "."));
    if (n.problems && n.problems.length) {
      const users = n.did === "remove" ? usersOf(n.problems) : [];
      if (n.did === "remove") text.push(el("p", {}, users.length ? [n.id + " is still used by ", users.join(", "), ". Change those first, then remove it again."] : "Something still depends on " + n.id + ". Change it first, then remove it again."));
      // A component is checked together with everything that uses it.
      if (isComp(w) && !isAdd && !isRemove) {
        text.push(el("p", {}, n.error || "The server did not accept the change."),
          el("p", { class: "muted" }, !usesOf(w) ? "Nothing was written."
            : n.problems.every((p) => /^component "/.test(p.message || ""))
              ? "Nothing was written, so the " + plural(usesOf(w), "step") + " that use" + (usesOf(w) === 1 ? "s" : "") + " " + compTitle(w) + " still work as before."
              : compTitle(w) + " is shared by " + plural(usesOf(w), "step") + ", and the server checks them with it, so a change that breaks one of them is refused. Nothing was written."));
      }
      if ((isMake || n.why === "schema") && n.error) text.push(el("p", {}, n.error));
      text.push(el("p", {}, isRemove ? "What the server found:" : isAdd ? "Fix this and try again:" : "Fix this and save again:"),
        el("ul", { class: "problems" }, n.problems.map((p) => problemItem(p, w, n.did === "add" ? n.id : null))));
    } else text.push(el("p", {}, n.error || "The server did not accept the change."));
  } else if (n.kind === "conflict") {
    title = isAdd ? "The file changed on disk, so nothing was added" : isRemove ? "The file changed on disk, so nothing was removed"
      : "The file changed on disk, so nothing was saved";
    if (isAdd || isRemove) {
      text.push(el("p", {}, "The workflow file was changed after this page read it. The page now shows the file as it is."),
        el("p", {}, isTest ? "Your entries are still in the form. Check them, then add the test again."
          : isAdd ? "Your entries are still in the form. Check the position, then add the step again."
          : isUntest ? "Look at the tests again, then remove it again if it is still what you want."
          : "Look at the steps again, then remove it again if it is still what you want."));
    } else {
      text.push(el("p", {}, (n.what === "prompt" ? "The prompt file was changed after this page read it." : n.what === "schema" ? "The schema file was changed after this page read it." : "The workflow file was changed after this page read it.")
        + " The form now shows what is in the file" + (n.kept ? ", with your " + plural(n.kept, "unsaved edit") + " on top." : ".")),
        el("p", {}, n.kept ? "Check your edits, then save again." : "Nothing of yours was lost."));
    }
    text.push(droppedView(n));
  } else if (n.kind === "external") {
    title = "The file was changed outside this page";
    text.push(el("p", {}, "The form shows what is in the file now" + (n.kept ? ", with your " + plural(n.kept, "unsaved edit") + " kept on top, each on the step it was typed for." : ".")), droppedView(n));
  } else if (n.kind === "network") {
    title = "Could not reach the server";
    text.push(el("p", {}, isAdd ? "Nothing was lost: your entries are still in the form. Try again when it is back."
      : isRemove ? "Nothing was removed. Try again when it is back."
      : "Nothing was lost: your edits are still here. Try saving again when it is back."));
  } else {
    title = isMake ? "Not made" : isAdd ? "Not added" : isRemove ? "Not removed" : "Not saved";
    text.push(el("p", {}, n.error || "The server answered " + n.status + "."));
  }
  const close = el("button", { type: "button", class: "x small", "aria-label": "Dismiss this message", title: "Dismiss" }, icon("close", 13));
  close.addEventListener("click", () => dismissNotice(w.name));
  return el("div", { class: "notice " + tone, role: tone === "ok" ? "status" : "alert" },
    el("div", { class: "nhead" }, el("strong", {}, title), close), text);
}

// droppedView names the unsaved edits that could not be kept because the step
// or field they were typed for is gone.
function droppedView(n) {
  if (!n.dropped || !n.dropped.length) return null;
  return el("p", {}, plural(n.dropped.length, "unsaved edit was", "unsaved edits were"), " dropped, because the step or field they were for is gone: ",
    n.dropped.slice(0, 6).map((x, i) => [i ? ", " : "", el("span", { class: "mono" }, nodeName(x.node) + " · " + x.label)]),
    n.dropped.length > 6 ? " and " + (n.dropped.length - 6) + " more" : "", ".");
}

// changesHome is the workflow whose changes view shows a component's file: the
// first workflow that uses it, directly or through another component.
function changesHome(w) {
  const seen = new Set(), queue = [w];
  while (queue.length) {
    const x = queue.shift();
    if (!isComp(x)) return x;
    if (seen.has(x.name)) continue;
    seen.add(x.name);
    for (const u of x.usedBy || []) { const y = entryOf(u.workflow); if (y) queue.push(y); }
  }
  return null;
}

// usersOf lists the steps and tests a refusal names, once each.
function usersOf(problems) {
  const out = [];
  for (const p of problems) {
    const m = /^(step|test) "([^"]+)"/.exec(p.message || "");
    const name = m ? (m[1] === "step" ? m[2] : "test “" + m[2] + "”") : /^respond\b/.test(p.message || "") ? "the response" : null;
    if (name && !out.includes(name)) out.push(name);
  }
  return out;
}

// A problem from the server reads like: step "summarize": when: Syntax error.
function problemItem(p, w, skip) {
  const m = /^step "([^"]+)"/.exec(p.message || "");
  const node = m && m[1] !== skip && w.steps.some((s) => s.id === m[1]) ? m[1] : w.respond && /^respond\b/.test(p.message || "") ? "response" : null;
  return el("li", {},
    p.line ? el("span", { class: "ln" }, "Line " + p.line) : null,
    el("span", { class: "msg" + (p.plain ? " plain" : "") }, p.message),
    node ? el("button", { type: "button", class: "linkbtn", onclick: () => go({ node }) }, "Show " + node) : null);
}

function replaceWorkflow(next) {
  const i = state.workflows.findIndex((x) => x.name === next.name);
  if (i >= 0) state.workflows[i] = next;
}

// promptKey is the key a prompt's field is known by (its path in the file).
function promptKey(name, step) {
  const w = workflowByName(name), s = w && w.steps.find((x) => x.id === step);
  const f = s && (s.edit || []).find((x) => x.kind === "prompt");
  return f ? pathKey(f.path) : "prompt|" + step;
}

function failureOf(r, what, extra) {
  if (r.status === 422) return { kind: "invalid", error: r.body.error, problems: r.body.problems || [], ...extra };
  if (r.status === 409) return { kind: "conflict", what, ...extra };
  return { kind: "error", status: r.status, error: r.body.error, ...extra };
}

async function saveCurrent() {
  if (!view || !state.editable) return;
  if (view.w.draft) return makeWorkflow();
  if (addMode(view.w)) return addStep();
  if (testMode(view.w)) return addTest();
  const name = view.w.name, st = uiOf(name);
  const w = workflowByName(name);
  if (st.saving || !w || w.problem) return;
  const d = draftsOf(name);
  // The server calls a component's way out "output"; here its id cannot be a step's.
  const fields = Object.values(d.fields).map((e) => ({ path: e.path, value: e.value, node: e.node === OUT ? "output" : e.node }));
  const texts = Object.entries(d.prompts).map(([step, text]) => ({ step, text }));
  const schemas = Object.entries(d.schemas);
  if (!fields.length && !texts.length && !schemas.length) return;

  st.saving = true;
  st.notice = null;
  disarm(name);
  updateBar();
  const files = [];
  let failure = null;
  try {
    if (fields.length) {
      const r = await request("POST", "/api/edit", { workflow: name, rev: w.rev, edits: fields });
      if (r.ok && r.body.workflow) {
        epoch++;
        changesEpoch++;
        replaceWorkflow(r.body.workflow);
        known[name] = r.body.workflow.rev;
        for (const e of fields) {
          const k = pathKey(e.path), cur = d.fields[k];
          if (cur && cur.value === e.value) delete d.fields[k];
          delete st.conflicts[k];
        }
        files.push(r.body.workflow.path || w.path);
      } else failure = failureOf(r, "workflow");
    }
    if (!failure) {
      for (const p of texts) {
        const key = name + "|" + p.step, cached = promptCache[key];
        const r = await request("POST", "/api/prompt", { workflow: name, step: p.step, rev: cached ? cached.rev : "", text: p.text });
        if (r.ok) {
          epoch++;
          changesEpoch++;
          promptCache[key] = { state: "ready", file: r.body.file, text: r.body.text, rev: r.body.rev, wfRev: (workflowByName(name) || w).rev };
          if (d.prompts[p.step] === p.text) delete d.prompts[p.step];
          delete st.conflicts[promptKey(name, p.step)];
          files.push(r.body.path || r.body.file);
        } else {
          failure = failureOf(r, "prompt");
          if (r.status === 409) {
            const before = cached ? cached.text : null;
            await loadPrompt(workflowByName(name), p.step, true);
            const fresh = promptCache[key];
            if (fresh && fresh.state === "ready" && fresh.text !== before) {
              st.conflicts[promptKey(name, p.step)] = fresh.text;
            }
            failure.kept = dirtyCount(name);
          }
          break;
        }
      }
    }
    // The answer's fields go last, once the file that names the schema is as it should be.
    if (!failure) {
      for (const [step, draft] of schemas) {
        const key = schemaKey(name, step), cached = schemaCache[key];
        if (!cached || cached.state !== "ready") { failure = { kind: "error", error: "The answer's fields of " + step + " have not been read yet, so they were not saved." }; break; }
        const body = { workflow: name, step, rev: cached.rev };
        if (draft.text !== undefined) body.text = draft.text; else body.fields = draft.fields;
        const r = await request("POST", "/api/schema", body);
        if (r.ok) {
          epoch++;
          changesEpoch++;
          schemaCache[key] = { state: "ready", file: r.body.file, text: r.body.text, rev: r.body.rev, fields: r.body.fields, wfRev: (workflowByName(name) || w).rev };
          if (d.schemas[step] === draft) delete d.schemas[step];
          files.push(folderOf(w.path) + "/" + r.body.file);
        } else {
          failure = failureOf(r, "schema", { why: "schema" });
          if (r.status === 409) {
            await loadSchema(workflowByName(name) || w, step, true);
            failure.kept = dirtyCount(name);
          }
          break;
        }
      }
    }
  } catch (err) {
    failure = { kind: "network" };
  }
  st.saving = false;
  if (failure) {
    if (files.length) failure.saved = files;
    st.notice = failure;
  } else st.notice = { kind: "saved", files };

  if (failure && failure.kind === "conflict" && failure.what === "workflow") {
    st.notice = { ...failure, kept: dirtyCount(name) };
    await refresh();
    // The poll noticed the new file and counted the edits on top of it.
  } else {
    pruneDrafts(workflowByName(name) || w);
    await refresh();
  }
  uiTick++;
  shown = "";
  flagSig = "";
  render();
  if (form) syncForm(workflowByName(name) || w);
  draftChanged(name, true);
}

function discardCurrent() {
  if (!view) return;
  if (view.w.draft) return cancelMake();
  if (addMode(view.w)) return go({ add: null });
  if (testMode(view.w)) return go({ test: null });
  const name = view.w.name, st = uiOf(name);
  if (st.saving || dirtyCount(name) === 0) return;
  if (!st.armed) {
    st.armed = true;
    clearTimeout(st.armTimer);
    st.armTimer = setTimeout(() => disarm(name), 4000);
    updateBar();
    return;
  }
  clearTimeout(st.armTimer);
  st.armed = false;
  drafts[name] = { fields: {}, prompts: {}, schemas: {} };
  st.conflicts = {};
  st.notice = null;
  if (form) syncForm(workflowByName(name));
  draftChanged(name, true);
}

// ---- adding and removing a step ----
//
// The form for a new step is a long-lived piece of the page, like the form for
// a step's settings: the poll updates it in place (syncAdder) and what has been
// typed lives in `adds`, so nothing typed is lost to a redraw, to a trip to
// another step, or to a change of the file underneath.

const ID_OK = /^[A-Za-z0-9_]+$/;
const addOf = (name) => adds[name] || (adds[name] = {
  after: "", component: null, kind: null, id: "", idTouched: false, when: "", values: {}, edited: false,
  method: "POST", url: "", input: "${trigger.body}", prompt: "",
  models: { decide: "openrouter/cloudflare/clef", ai: "openrouter/deepseek/deepseek-v4.1-flash" },
  questions: [blankQuestion()], fields: [blankField()],
});

// The kinds of step that do their own work. The chooser offers them next to the
// components, and the form for one is made from the same parts.
const BUILTIN = {
  ai: { use: "@ai", kind: "ai", name: "AI", idBase: "ai", group: "Ask a model", text: "Write or extract, in fields you define", inputs: [] },
  decide: { use: "@decide", kind: "decide", name: "Decide", idBase: "decide", group: "Ask a model", text: "Yes or no, one of a list, or a score", inputs: [] },
  http: { use: "@http", kind: "http", name: "HTTP request", idBase: "request", group: "Act", text: "Send to any API", inputs: [] },
};
const METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE"];
// chosenOf is what the form is for: a kind of step that does its own work, or a component.
const chosenOf = (w, a) => (a.kind ? BUILTIN[a.kind] : componentOf(w, a.component));

// ---- the answer's fields and a decision's questions ----
//
// Two editors made of plain rows: the table of fields an AI step answers with,
// which the node editor uses for an existing step as well, and the cards of the
// questions of a decision. Each edits an array of rows in place and says so by
// calling onChange; neither knows where the rows are kept.

const FIELD_TYPES = [["text", "Text"], ["number", "Number"], ["whole number", "Whole number"], ["yes or no", "Yes or no"], ["list of text", "List of text"], ["one of", "One of a list"]];
const QUESTION_TYPES = [["yes or no", "Yes or no", "Is it urgent?"], ["one of", "One of a list", "Which language?"], ["score", "A score", "How risky is it?"]];
const NAME_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;
function blankField() { return { name: "", type: "text", required: true, description: "", options: "" }; }
function blankQuestion() { return { name: "", ask: "", type: "yes or no", yesIf: "", noIf: "", answers: [] }; }
const splitOptions = (text) => String(text || "").split(",").map((x) => x.trim()).filter(Boolean);
const rowOfField = (f) => ({ name: f.name, type: f.type, required: !!f.required, description: f.description || "", options: (f.options || []).join(", ") });
const isBlankField = (r) => r.name.trim() === "" && r.description.trim() === "" && r.options.trim() === "";

// fieldsOut is the rows as the server takes them. A row with nothing in it is left out.
function fieldsOut(list) {
  return list.filter((r) => !isBlankField(r)).map((r) => {
    const f = { name: r.name.trim(), type: r.type, required: !!r.required };
    if (r.description.trim()) f.description = r.description.trim();
    if (r.type === "one of") f.options = splitOptions(r.options);
    return f;
  });
}

// fieldProblems says what the server would refuse, so that it can be told before.
function fieldProblems(list) {
  const out = [], seen = new Set();
  for (const f of fieldsOut(list)) {
    if (!f.name) out.push("A field has no name.");
    else if (!NAME_RE.test(f.name)) out.push("“" + f.name + "” is not a field name: use letters, digits and _, and do not start with a digit.");
    else if (seen.has(f.name)) out.push("There are two fields called " + f.name + ".");
    seen.add(f.name);
    if (f.type === "one of" && f.options.length < 2) out.push((f.name ? f.name : "A field") + " is one of a list, which needs at least two options.");
  }
  return out;
}

function questionProblems(list) {
  const out = [], seen = new Set();
  list.forEach((q, i) => {
    const who = q.name.trim() || "Question " + (i + 1);
    if (!q.name.trim()) out.push(who + " has no name.");
    else if (!NAME_RE.test(q.name.trim())) out.push("“" + q.name.trim() + "” is not a question name: use letters, digits and _, and do not start with a digit.");
    else if (seen.has(q.name.trim())) out.push("There are two questions called " + q.name.trim() + ".");
    seen.add(q.name.trim());
    if (!q.ask.trim()) out.push(who + " has nothing to ask.");
    if (q.type === "yes or no") { if (!!q.yesIf.trim() !== !!q.noIf.trim()) out.push(who + ": give both “Yes if” and “No if”, or neither."); }
    else if (q.answers.filter((a) => a.name.trim()).length < 2) out.push(who + " needs at least two answers.");
  });
  return out;
}

const problemList = (box, problems) => {
  box.hidden = !problems.length;
  box.replaceChildren(...problems.map((t) => el("div", {}, t)));
};

function textIn(props, value, onInput) {
  const box = el("input", { type: "text", class: "in" + (props.mono ? " mono" : ""), spellcheck: "false", autocomplete: "off", autocapitalize: "off", id: props.id, placeholder: props.placeholder, "aria-label": props.label });
  box.value = value;
  box.addEventListener("input", () => onInput(box.value));
  return box;
}

// buildFieldTable: one row for each field of the answer: name, type, whether the
// model must give it, and what goes in it. A type of "One of a list" brings a
// row for its options.
function buildFieldTable(list, onChange, uid) {
  const T = { list };
  let n = 0;
  const body = el("div", { class: "frows" });
  const warn = el("p", { class: "conflict fwarn", hidden: true, role: "status" });
  // What is wrong is not said before anything has been typed.
  let touched = false;
  const repaint = () => problemList(warn, touched ? fieldProblems(list) : []);
  const changed = (kind) => { if (kind === "input") touched = true; repaint(); onChange(kind); };
  const add = el("button", { type: "button", class: "btn small" }, icon("plus", 12), "Add field");
  const row = (r, i) => {
    const id = "ff" + uid + "x" + (++n);
    const name = textIn({ mono: true, id: id + "n", placeholder: "field_name", label: "Name of field " + (i + 1) }, r.name, (v) => { r.name = v; repaint(); changed("input"); });
    const type = el("select", { id: id + "t", class: "in", "aria-label": "Type of field " + (i + 1) }, FIELD_TYPES.map(([v, t]) => el("option", { value: v }, t)));
    type.value = r.type;
    const req = el("input", { type: "checkbox", id: id + "r" });
    req.checked = r.required;
    const desc = textIn({ id: id + "d", placeholder: "What goes in it", label: "What goes in field " + (i + 1) }, r.description, (v) => { r.description = v; repaint(); changed("input"); });
    const opts = textIn({ mono: true, id: id + "o", placeholder: "Options, separated by commas", label: "Options of field " + (i + 1) }, r.options, (v) => { r.options = v; repaint(); changed("input"); });
    const optRow = el("div", { class: "fopts", hidden: r.type !== "one of" }, opts);
    type.addEventListener("change", () => { r.type = type.value; optRow.hidden = r.type !== "one of"; repaint(); changed("input"); });
    req.addEventListener("change", () => { r.required = req.checked; changed("input"); });
    const rm = el("button", { type: "button", class: "fremove", title: "Remove this field", "aria-label": "Remove field " + (i + 1) + (r.name.trim() ? ", " + r.name.trim() : "") }, icon("trash", 14));
    rm.addEventListener("click", () => {
      list.splice(list.indexOf(r), 1);
      T.rebuild();
      changed("struct");
      add.focus();
    });
    return el("div", { class: "frow" }, name, type, el("label", { class: "freq" }, req, el("span", {}, "Required")), desc, rm, optRow);
  };
  T.rebuild = () => { body.replaceChildren(...list.map(row)); repaint(); };
  add.addEventListener("click", () => {
    list.push(blankField());
    T.rebuild();
    const last = body.lastElementChild && body.lastElementChild.querySelector("input");
    if (last) last.focus();
    changed("struct");
  });
  T.root = el("div", { class: "ftable" },
    el("div", { class: "fhead", "aria-hidden": "true" }, el("span", {}, "Field"), el("span", {}, "Type"), el("span", {}, "Required"), el("span", {}, "What goes in it"), el("span")),
    body, el("div", { class: "factions" }, add, el("span", { class: "fnote" }, "The model must answer with these fields and no others.")), warn);
  T.rebuild();
  return T;
}

// buildQuestionList: a card for each question of a decision. It asks, then says
// how it is answered: yes or no, one of a list, or a score, which are
// answers from lowest to highest.
function buildQuestionList(list, onChange, uid) {
  const T = { list };
  let n = 0;
  const holder = el("div", { class: "qcards" });
  const warn = el("p", { class: "conflict fwarn", hidden: true, role: "status" });
  let touched = false;
  const repaint = () => problemList(warn, touched ? questionProblems(list) : []);
  const changed = (kind) => { if (kind === "input") touched = true; repaint(); onChange(kind); };
  const add = el("button", { type: "button", class: "btn small" }, icon("plus", 12), "Add question");
  const card = (q, qi) => {
    const id = "fq" + uid + "x" + (++n);
    const answers = el("div", { class: "qanswers" });
    const yn = el("div", { class: "qyn" });
    const kindBtns = QUESTION_TYPES.map(([v, text]) => {
      const b = el("button", { type: "button", role: "radio", class: "qtype", "data-type": v }, text);
      b.addEventListener("click", () => {
        if (q.type === v) return;
        q.type = v;
        if (v !== "yes or no") while (q.answers.length < 2) q.answers.push({ name: "", meaning: "" });
        paint();
        changed("struct");
        b.focus();
      });
      return b;
    });
    const name = textIn({ mono: true, id: id + "n", placeholder: "angry" }, q.name, (v) => { q.name = v; repaint(); changed("input"); });
    const ask = textIn({ id: id + "a", placeholder: QUESTION_TYPES.find((t) => t[0] === q.type)[2] }, q.ask, (v) => { q.ask = v; repaint(); changed("input"); });
    const yes = textIn({ id: id + "y", placeholder: "For example: they say they will cancel" }, q.yesIf, (v) => { q.yesIf = v; repaint(); changed("input"); });
    const no = textIn({ id: id + "z", placeholder: "For example: they only ask a question" }, q.noIf, (v) => { q.noIf = v; repaint(); changed("input"); });
    const field = (label, box, hint) => el("div", { class: "field" }, el("div", { class: "lab" }, el("label", { for: box.id }, label)), box, hint ? el("p", { class: "hint-text" }, hint) : null);
    yn.append(field("Yes if", yes), field("No if", no), el("p", { class: "hint-text qhint" }, "Optional. Say what means yes and what means no, both or neither."));
    const scored = () => q.type === "score";
    const paintAnswers = () => {
      answers.replaceChildren(
        el("div", { class: "qahead" }, el("span", {}, "Answer"), el("span", {}, scored() ? "Means, lowest to highest" : "Means"), el("span")),
        ...q.answers.map((a, ai) => {
          const aid = id + "a" + ai;
          const nm = textIn({ mono: true, id: aid + "n", placeholder: scored() ? (ai === 0 ? "low" : ai === q.answers.length - 1 ? "high" : "medium") : "answer", label: "Name of answer " + (ai + 1) + " of " + (q.name.trim() || "question " + (qi + 1)) }, a.name, (v) => { a.name = v; repaint(); changed("input"); });
          const mn = textIn({ id: aid + "m", placeholder: "What it means", label: "What answer " + (ai + 1) + " means" }, a.meaning, (v) => { a.meaning = v; changed("input"); });
          const rm = el("button", { type: "button", class: "fremove", title: "Remove this answer", "aria-label": "Remove answer " + (ai + 1) }, icon("trash", 14));
          rm.addEventListener("click", () => { q.answers.splice(ai, 1); paintAnswers(); repaint(); changed("struct"); addA.focus(); });
          return el("div", { class: "qarow" }, nm, mn, rm);
        }),
        addA);
    };
    const addA = el("button", { type: "button", class: "btn small qadd" }, icon("plus", 12), "Add answer");
    addA.addEventListener("click", () => {
      q.answers.push({ name: "", meaning: "" });
      paintAnswers();
      const rows = answers.querySelectorAll(".qarow");
      const last = rows[rows.length - 1];
      if (last) last.querySelector("input").focus();
      repaint();
      changed("struct");
    });
    const paint = () => {
      for (const b of kindBtns) b.setAttribute("aria-checked", b.dataset.type === q.type ? "true" : "false");
      for (const b of kindBtns) b.tabIndex = b.dataset.type === q.type ? 0 : -1;
      yn.hidden = q.type !== "yes or no";
      answers.hidden = q.type === "yes or no";
      ask.placeholder = QUESTION_TYPES.find((t) => t[0] === q.type)[2];
      if (!answers.hidden) paintAnswers();
      repaint();
    };
    const group = el("div", { class: "qtypes", role: "radiogroup", "aria-label": "Answer with, for question " + (qi + 1) }, kindBtns);
    group.addEventListener("keydown", (e) => {
      const d = e.key === "ArrowRight" || e.key === "ArrowDown" ? 1 : e.key === "ArrowLeft" || e.key === "ArrowUp" ? -1 : 0;
      if (!d) return;
      e.preventDefault();
      const at = QUESTION_TYPES.findIndex((t) => t[0] === q.type), next = QUESTION_TYPES[(at + d + 3) % 3][0];
      kindBtns.find((b) => b.dataset.type === next).click();
    });
    const rm = el("button", { type: "button", class: "fremove", title: "Remove this question", "aria-label": "Remove question " + (qi + 1) + (q.name.trim() ? ", " + q.name.trim() : "") }, icon("trash", 14));
    rm.addEventListener("click", () => { list.splice(list.indexOf(q), 1); T.rebuild(); changed("struct"); add.focus(); });
    const el2 = el("div", { class: "ned-q qcard" },
      el("div", { class: "ned-qhead" }, el("span", { class: "qnum" }, "Question " + (qi + 1)), el("span", { class: "grow" }), rm),
      field("Name", name, qi === 0 ? "A later step reads the answer as step_id.name.value." : null),
      field("Ask", ask),
      el("div", { class: "field" }, el("div", { class: "lab" }, el("span", { class: "qlab", id: id + "g" }, "Answer with")), group),
      yn, answers);
    paint();
    return el2;
  };
  T.rebuild = () => { holder.replaceChildren(...list.map(card)); repaint(); };
  add.addEventListener("click", () => {
    list.push(blankQuestion());
    T.rebuild();
    const last = holder.lastElementChild && holder.lastElementChild.querySelector("input");
    if (last) last.focus();
    changed("struct");
  });
  T.root = el("div", { class: "qlist" }, holder, el("div", { class: "factions" }, add), warn);
  T.rebuild();
  return T;
}

// A question with its answers, as the server takes it.
function questionsOut(list) {
  return list.map((q) => {
    const o = { name: q.name.trim(), ask: q.ask.trim(), type: q.type };
    if (q.type === "yes or no") {
      if (q.yesIf.trim() || q.noIf.trim()) { o.yesIf = q.yesIf.trim(); o.noIf = q.noIf.trim(); }
    } else o.answers = q.answers.filter((a) => a.name.trim() || a.meaning.trim()).map((a) => ({ name: a.name.trim(), meaning: a.meaning.trim() }));
    return o;
  });
}
const componentsOf = (w) => w.components || [];
// A workflow is offered components from other folders too, so two can share a
// name: one is told from another by its use, the path it is used by.
const componentOf = (w, use) => componentsOf(w).find((c) => c.use === use) || null;
const addMode = (w) => canAdd(w) && !!selected.add;
const folderOf = (path) => path.replace(/\/[^/]*$/, "");

// freeId suggests an id for a step made from a component that no step has yet.
function freeId(w, base) {
  const taken = new Set(["trigger", "response", ...w.steps.map((s) => s.id)]);
  const stem = base.replace(/[^A-Za-z0-9_]/g, "_") || "step";
  if (!taken.has(stem)) return stem;
  for (let n = 2; ; n++) if (!taken.has(stem + "_" + n)) return stem + "_" + n;
}

// resolveAfter turns what the address says into a step to go after.
function resolveAfter(w, v) {
  if (v === "trigger") return "trigger";
  if (v && v !== "-" && w.steps.some((s) => s.id === v)) return v;
  return w.steps.length ? w.steps[w.steps.length - 1].id : "trigger";
}

// referencesFor lists what a step put after `after` can refer to: the trigger,
// the steps before it, and the server's environment.
function referencesFor(w, after) {
  const rows = [];
  const keys = w.sample && typeof w.sample === "object" && !Array.isArray(w.sample) ? Object.keys(w.sample).slice(0, 6) : [];
  // Where no test shows what a request holds, the field's name is typed.
  rows.push({ id: "trigger", kind: "trigger", chips: isWebhook(w) || !w.trigger
    ? [{ text: "trigger.body" }, ...(keys.length ? keys.map((k) => ({ text: "trigger.body." + k })) : [{ text: "trigger.body.", open: true, short: "body.field" }])]
    : [{ text: "trigger.time" }] });
  const upto = after === "trigger" ? 0 : w.steps.findIndex((s) => s.id === after) + 1;
  for (const s of w.steps.slice(0, upto)) {
    rows.push({ id: s.id, kind: s.kind, chips: s.outputs && s.outputs.length
      ? s.outputs.map((o) => ({ text: s.id + "." + o, short: o }))
      : s.questions && s.questions.length
      ? s.questions.map((q) => ({ text: s.id + "." + q.name + ".value", short: q.name + ".value" }))
      : [{ text: s.id + ".", open: true, short: "field" }] });
  }
  rows.push({ id: "env", kind: null, chips: [{ text: "env.", open: true, short: "NAME" }] });
  return rows;
}

function hideAdder() {
  if (!adder) return;
  adder = null;
  insp.formslot.replaceChildren();
}

function showAdder(w) {
  if (!adder || adder.workflow !== w.name) {
    adder = buildAdder(w);
    // What the last save said is not news in a new form.
    const st = uiOf(w.name);
    if (st.notice && st.notice.kind === "saved") st.notice = null;
    insp.formslot.replaceChildren(adder.root);
    syncAdder(w);
    afterRender = focusAdder;
  } else syncAdder(w);
}

function focusAdder() {
  const A = adder;
  if (!A) return;
  const a = addOf(A.workflow);
  const t = !a.component && !a.kind ? A.search : A.targets[1] || A.idBox;
  if (t) t.focus({ preventScroll: false });
}

function buildAdder(w) {
  const uid = ++formId;
  const A = { workflow: w.name, from: undefined, posSig: "", compSig: "", detailKey: "", refSig: "", target: null, targets: [], uid, idBox: null, query: "", expanded: true, summarySig: "" };
  const posId = "pos" + uid;
  A.pos = el("select", { id: posId, class: "in" });
  A.pos.addEventListener("change", () => {
    addOf(A.workflow).after = A.pos.value;
    const now = workflowByName(A.workflow);
    if (now) syncAdder(now);
    updateBar();
  });
  A.posWarn = el("p", { class: "conflict", hidden: true }, "The step it was to go after is no longer in the workflow. Choose a position.");
  A.posField = el("div", { class: "field" },
    el("div", { class: "lab" }, el("label", { for: posId }, "Position")),
    A.pos,
    el("p", { class: "hint-text" }, "Steps run in the order they have in the file, so this decides when it runs."),
    A.posWarn);

  // The chooser: a search box, then the kinds of step in groups.
  const group = "comp" + uid;
  A.search = el("input", { id: "find" + uid, type: "text", class: "in find", placeholder: "What happens next?", autocomplete: "off", spellcheck: "false" });
  A.search.addEventListener("input", () => { A.query = A.search.value; filterChoices(A); });
  A.search.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.isComposing) {
      e.preventDefault();
      const first = A.groups.querySelector(".choice:not([hidden]) input");
      if (first) first.click();
    }
  });
  A.builtinEls = {};
  const builtinRow = (b) => {
    const input = el("input", { type: "radio", name: group, value: b.use });
    input.addEventListener("change", () => { if (input.checked) pickKind(A.workflow, b.kind); });
    const row = el("label", { class: "choice kind-" + b.kind },
      input, kindTile(b.kind, 38),
      el("span", { class: "ctext" }, el("span", { class: "chead" }, el("span", { class: "cname" }, b.name), el("span", { class: "ckind" }, KIND_LABEL[b.kind])), el("span", { class: "cdesc" }, b.text)));
    A.builtinEls[b.kind] = { row, input };
    return row;
  };
  const cgroup = (title, ...rows) => el("div", { class: "cgroup" }, el("div", { class: "cgroup-h" }, title), el("div", { class: "choices", role: "presentation" }, rows));
  A.gModel = cgroup("Ask a model", builtinRow(BUILTIN.ai), builtinRow(BUILTIN.decide));
  A.gAct = cgroup("Act", builtinRow(BUILTIN.http));
  A.choices = el("div", { class: "choices", role: "presentation" });
  A.noComps = el("p", { class: "muted small nocomps" }, "None yet. A component is a step written once, with named inputs, in components/<name>/component.yaml in the workflow's folder.");
  A.gComp = el("div", { class: "cgroup" }, el("div", { class: "cgroup-h" }, "Your components"), A.choices, A.noComps);
  A.noMatch = el("p", { class: "muted small", hidden: true, role: "status" });
  A.groups = el("div", { class: "cgroups" }, A.gModel, A.gAct, A.gComp, A.noMatch);
  A.finder = el("div", { class: "finder" }, el("label", { class: "vh", for: "find" + uid }, "Find a kind of step"), A.search);
  // Once a kind is chosen the chooser folds into one line, to be opened again by Change.
  A.summary = el("div", { class: "chosen", hidden: true });
  A.picker = el("fieldset", { class: "picker chooser" }, el("legend", {}, "Kind of step"), A.summary, A.finder, A.groups);
  A.detail = el("div", { class: "adddetail" });
  A.root = el("section", { class: "editform addform" }, A.posField, A.picker, A.detail);
  A.pick = (use) => pickComponent(A.workflow, use);
  A.submit = () => addStep();
  return A;
}

// filterChoices hides what the search does not match, and a group with nothing left.
function filterChoices(A) {
  const q = A.query.trim().toLowerCase();
  let shown = 0;
  for (const g of [A.gModel, A.gAct, A.gComp]) {
    let any = 0;
    for (const row of g.querySelectorAll(".choice")) {
      const on = !q || row.textContent.toLowerCase().includes(q);
      row.hidden = !on;
      if (on) any++;
    }
    g.hidden = !any && !(g === A.gComp && !q && !g.querySelector(".choice"));
    shown += any;
  }
  A.noMatch.hidden = !(q && !shown);
  A.noMatch.textContent = "Nothing is called “" + A.query.trim() + "”.";
}

function pickKind(name, kind) {
  const a = addOf(name);
  a.kind = kind;
  a.component = null;
  const w = workflowByName(name);
  if (!a.idTouched && w) a.id = freeId(w, BUILTIN[kind].idBase);
  const A = adder;
  if (A) A.expanded = false;
  if (w) syncAdder(w);
  updateBar();
  if (A && (A.targets[1] || A.idBox)) (A.targets[1] || A.idBox).focus();
}

function pickComponent(name, use) {
  const a = addOf(name);
  a.component = use;
  a.kind = null;
  const w = workflowByName(name);
  const A = adder;
  if (A) A.expanded = false;
  if (w) {
    const comp = componentOf(w, use);
    if (!a.idTouched) a.id = freeId(w, comp ? comp.name : use);
    syncAdder(w);
  }
  updateBar();
  if (A && (A.targets[1] || A.idBox)) (A.targets[1] || A.idBox).focus();
}

// settleChoice keeps the component chosen one that is still offered, and picks
// the only one when there is only one.
function settleChoice(w, comps, a) {
  if (a.component && !comps.some((c) => c.use === a.component)) a.component = null;
  if (!a.component && comps.length === 1) { a.component = comps[0].use; if (!a.idTouched) a.id = freeId(w, comps[0].name); }
}

// syncChoices draws the components to choose from and marks the chosen one.
// A component whose use says it is from another folder, or that `far` says
// shares its name with another, shows where it is from.
function syncChoices(A, comps, current, far) {
  const sig = JSON.stringify(comps.map((c) => [c.use, c.name, c.kind, c.description, (c.inputs || []).map((i) => i.name)]));
  if (sig !== A.compSig) {
    A.compSig = sig;
    const group = "comp" + A.uid;
    A.choiceEls = {};
    A.choices.replaceChildren(...comps.map((c) => {
      const input = el("input", { type: "radio", name: group, value: c.use });
      input.addEventListener("change", () => { if (input.checked) A.pick(c.use); });
      const row = el("label", { class: "choice kind-" + c.kind },
        input, kindTile(c.kind, 38),
        el("span", { class: "ctext" },
          el("span", { class: "chead" }, el("span", { class: "cname mono" }, c.name), el("span", { class: "ckind" }, KIND_LABEL[c.kind] || c.kind)),
          c.description ? el("span", { class: "cdesc" }, c.description) : null,
          far(c) ? el("span", { class: "cin mono" }, "From " + c.use) : null,
          el("span", { class: "cin mono" }, (c.inputs || []).length ? "Takes " + c.inputs.map((i) => i.name).join(", ") : "Takes no input")));
      A.choiceEls[c.use] = { row, input };
      return row;
    }));
  }
  for (const [use, c] of Object.entries(A.choiceEls)) {
    c.input.checked = current === use;
    c.row.classList.toggle("on", current === use);
  }
}

// syncDetail brings the fields of the step up to date with what has been typed.
// `sig` says when what the chips offer has changed.
function syncDetail(A, w, comp, a, sig) {
  const key = comp.use + "|" + JSON.stringify(comp.inputs || []);
  if (key !== A.detailKey) { A.detailKey = key; A.refSig = ""; buildDetail(A, w, comp); }
  if (!a.idTouched) a.id = freeId(w, comp.idBase || comp.name);
  setValue(A.idBox, a.id);
  setValue(A.whenBox, a.when);
  (comp.inputs || []).forEach((inp, i) => setValue(A.targets[i + 1], a.values[inp.name] || ""));
  if (comp.kind && BUILTIN[comp.kind] === comp && A.kindSync) A.kindSync(a);
  idNote(A, w, a);
  if (sig !== A.refSig) { A.refSig = sig; buildRefs(A, w, a.after); }
}

function syncAdder(w) {
  const A = adder, a = addOf(w.name);
  if (!A || A.workflow !== w.name) return;
  const comps = componentsOf(w);
  if (A.from !== selected.add) { A.from = selected.add; a.after = resolveAfter(w, selected.add); }

  // Position.
  const valid = a.after === "trigger" || w.steps.some((s) => s.id === a.after);
  const posSig = w.steps.map((s) => s.id).join(",") + (valid ? "" : "!");
  if (posSig !== A.posSig) {
    A.posSig = posSig;
    A.pos.replaceChildren(
      ...(valid ? [] : [el("option", { value: "", disabled: true }, "Choose a position")]),
      el("option", { value: "trigger" }, w.steps.length ? "At the start, before " + w.steps[0].id : "As the first step"),
      ...w.steps.map((s, i) => el("option", { value: s.id }, "After " + s.id + (i === w.steps.length - 1 ? " (last)" : ""))));
  }
  A.pos.value = valid ? a.after : "";
  A.posWarn.hidden = valid;

  // What to add: a component that is no longer offered is let go.
  if (a.component && !comps.some((c) => c.use === a.component)) a.component = null;
  syncChoices(A, comps, a.component, (c) => c.use.startsWith("../"));
  for (const [kind, c] of Object.entries(A.builtinEls)) {
    c.input.checked = a.kind === kind;
    c.row.classList.toggle("on", a.kind === kind);
  }
  A.noComps.hidden = comps.length > 0;
  const comp = chosenOf(w, a);
  if (!comp) A.expanded = true;
  A.groups.hidden = A.finder.hidden = !A.expanded;
  A.summary.hidden = A.expanded;
  filterChoices(A);
  if (comp) {
    const sig = comp.use + "|" + comp.name + "|" + (comp.kind || "");
    if (sig !== A.summarySig) {
      A.summarySig = sig;
      const change = el("button", { type: "button", class: "linkbtn" }, "Change");
      change.addEventListener("click", () => { A.expanded = true; A.search.value = ""; A.query = ""; const now = workflowByName(A.workflow); if (now) syncAdder(now); A.search.focus(); });
      A.summary.replaceChildren(kindTile(comp.kind, 34),
        el("span", { class: "ctext" }, el("span", { class: "cname" + (comp.use.startsWith("@") ? "" : " mono") }, comp.name), el("span", { class: "ckind" }, comp.use.startsWith("@") ? comp.text : (KIND_LABEL[comp.kind] || comp.kind) + " component")),
        change);
      A.summary.className = "chosen kind-" + comp.kind;
    }
  } else A.summarySig = "";

  // The step itself.
  A.detail.hidden = !comp;
  if (!comp) { A.detailKey = ""; return; }
  syncDetail(A, w, comp, a, w.name + "|" + a.after + "|" + w.steps.map((s) => s.id + ":" + (s.outputs || []).join("+")).join(",") + "|" + JSON.stringify(w.sample || null));
}

// idNote warns, without stopping anything, about an id the server would refuse.
function idNote(A, w, a) {
  const id = a.id.trim();
  let text = "";
  if (id && !ID_OK.test(id)) text = "Use letters, digits and underscore only.";
  else if (id === "trigger" || id === "response" || w.steps.some((s) => s.id === id)) text = "A step called " + id + " already exists in this workflow.";
  A.idWarn.hidden = !text;
  A.idWarn.textContent = text;
}

function plainField(A, label, tag, box, hint) {
  return el("div", { class: "field" },
    el("div", { class: "lab" }, el("label", { for: box.id }, label), tag),
    box,
    hint ? el("p", { class: "hint-text" }, hint) : null);
}

// formBox is a text box of the form for a new step or a new workflow. Typing in
// it is remembered by props.set, and Enter in a one-line box sends the form
// (A.submit); in a larger one it starts a new line.
function formBox(A, id, props) {
  const box = el(props.rows ? "textarea" : "input", { id, ...props.attrs, class: "in" + (props.mono ? " mono" : ""), spellcheck: "false", autocomplete: "off", autocapitalize: "off", rows: props.rows ? 1 : null, type: props.rows ? null : "text" });
  box.addEventListener("input", () => { props.set(box.value); autosize(box); updateBar(); if (A.onInput) A.onInput(); });
  if (!props.rows) box.addEventListener("keydown", (e) => { if (e.key === "Enter" && !e.isComposing) { e.preventDefault(); A.submit(); } });
  return box;
}

function buildDetail(A, w, comp) {
  const a = A.a || addOf(w.name);
  A.targets = [];
  const remember = (box) => box.addEventListener("focus", () => { A.target = box; });
  const textBox = (id, props) => formBox(A, id, props);

  const idId = "sid" + A.uid + "x";
  A.idBox = textBox(idId, { mono: true, set: (v) => { a.id = v; a.idTouched = true; const now = workflowByName(w.name); if (now) idNote(A, now, a); } });
  A.idWarn = el("p", { class: "conflict", hidden: true });
  const idField = el("div", { class: "field" },
    el("div", { class: "lab" }, el("label", { for: idId }, "Step id")),
    A.idBox,
    el("p", { class: "hint-text" }, "Letters, digits and underscore. Other steps use it to read this step's result."),
    A.idWarn);

  const whenId = "swh" + A.uid + "x";
  A.whenBox = textBox(whenId, { rows: true, mono: true, set: (v) => { a.when = v; } });
  A.whenBox.dataset.raw = "1";
  remember(A.whenBox);
  const decide = w.steps.find((s) => s.questions && s.questions.length);
  const whenField = plainField(A, "Run only when", el("span", { class: "tag", title: "A CEL expression" }, "CEL"), A.whenBox,
    "Empty means the step always runs." + (decide ? " For example " + decide.id + "." + decide.questions[0].name + ".value == \"…\"." : ""));
  A.targets.push(A.whenBox);

  A.kindSync = null;
  const fields = BUILTIN[comp.kind] === comp ? [] : (comp.inputs || []).map((inp) => {
    const id = "sin" + A.uid + "x" + inp.name.replace(/[^A-Za-z0-9_]/g, "_");
    // One line, so that Enter adds the step; an expression for an object or a list may need more.
    const long = inp.type === "object" || inp.type === "list" || inp.type === "any";
    const box = textBox(id, { rows: long, mono: inp.type === "object" || inp.type === "list", set: (v) => { a.values[inp.name] = v; },
      attrs: { inputmode: inp.type === "number" ? "numeric" : null } });
    remember(box);
    A.targets.push(box);
    const hint = inp.type === "number" ? "A number, or an expression such as ${trigger.body.count}."
      : inp.type === "boolean" ? "true or false, or an expression."
      : inp.type === "object" || inp.type === "list" ? "Needs an expression, for example ${trigger.body}."
      : null;
    return plainField(A, inp.name, el("span", { class: "tag", title: "Type: " + inp.type }, inp.type), box, hint);
  });
  const inputs = BUILTIN[comp.kind] === comp ? buildKindFields(A, w, a, comp.kind, textBox, remember)
    : fields.length
    ? el("fieldset", { class: "group" }, el("legend", {}, "Inputs"), fields)
    : el("p", { class: "muted small" }, comp.name + " takes no input.");

  A.refs = el("div", { class: "refs" });
  A.detail.replaceChildren(...[
    el("h2", { class: "sub" }, BUILTIN[comp.kind] === comp ? "New " + comp.name + " step" : "New " + (KIND_LABEL[comp.kind] || comp.kind) + " step from " + comp.name),
    comp.kind === "flow" ? el("p", { class: "muted small flownote" }, "Runs steps of its own, written in the component's file. This step only chooses what goes in.") : null,
    idField, A.refs, whenField, inputs].filter(Boolean));
}

// buildKindFields makes the fields of a step that does its own work, and
// A.kindSync, which puts what has been typed (kept in `a`) back into them.
function buildKindFields(A, w, a, kind, textBox, remember) {
  const edit = (apply) => (v) => { apply(v); a.edited = true; };
  const sync = [];
  const field = (label, box, hint, tag) => plainField(A, label, tag || null, box, hint);
  const out = [];
  const modelBox = () => {
    const id = "smd" + A.uid + "x";
    const box = textBox(id, { mono: true, set: edit((v) => { a.models[kind] = v; }) });
    sync.push(() => setValue(box, a.models[kind]));
    return field("Model", box, "As the server names it: provider, then the model.");
  };
  // A field that takes a whole key: a key clicked above replaces what is there.
  const inputBox = () => {
    const id = "sin" + A.uid + "xread";
    const box = textBox(id, { mono: true, set: edit((v) => { a.input = v; }) });
    box.dataset.whole = "1";
    remember(box);
    A.targets.push(box);
    sync.push(() => setValue(box, a.input));
    return field("What it reads", box, "A key, such as ${trigger.body}. Click one above to put it here.");
  };
  if (kind === "http") {
    const mid = "smt" + A.uid + "x";
    const method = el("select", { id: mid, class: "in" }, METHODS.map((m) => el("option", { value: m }, m)));
    method.addEventListener("change", () => { a.method = method.value; a.edited = true; updateBar(); });
    sync.push(() => { method.value = a.method; });
    const uid = "sur" + A.uid + "x";
    const url = textBox(uid, { mono: true, set: edit((v) => { a.url = v; }) });
    remember(url);
    A.targets.push(url);
    sync.push(() => setValue(url, a.url));
    out.push(el("div", { class: "field" }, el("div", { class: "lab" }, el("label", { for: mid }, "Method")), method),
      field("URL", url, "For example ${env.BILLING_URL}. Click a key above to put it here."));
  } else if (kind === "decide") {
    A.questions = buildQuestionList(a.questions, () => { a.edited = true; updateBar(); }, A.uid);
    out.push(modelBox(), inputBox(),
      el("div", { class: "ned-sect" }, el("span", { class: "ned-h" }, "Questions")),
      A.questions.root);
  } else if (kind === "ai") {
    const pid = "spr" + A.uid + "x";
    const prompt = el("textarea", { id: pid, class: "in mono prompt", rows: 10, spellcheck: "false", placeholder: "Tell the model what to do with what it reads." });
    prompt.addEventListener("input", () => { a.prompt = prompt.value; a.edited = true; updateBar(); });
    sync.push(() => setValue(prompt, a.prompt));
    A.fieldTable = buildFieldTable(a.fields, () => { a.edited = true; updateBar(); }, A.uid);
    out.push(modelBox(), inputBox(),
      el("div", { class: "field" }, el("div", { class: "lab" }, el("label", { for: pid }, "Instructions")), prompt,
        el("p", { class: "hint-text" }, "Written to prompts/<step id>.md and sent as it is: the model gets what it reads apart from it, so no ${...} goes here.")),
      el("div", { class: "ned-sect" }, el("span", { class: "ned-h" }, "The answer has these fields")),
      A.fieldTable.root);
  }
  A.kindSync = () => sync.forEach((f) => f());
  return el("div", { class: "kindfields" }, out);
}

// buildRefs shows what can be referred to at the chosen position. A click puts
// the reference into the field being edited.
function buildRefs(A, w, after) {
  if (!A.refs) return;
  const rows = referencesFor(w, after);
  A.refs.replaceChildren(...[
    el("div", { class: "rhead" }, el("strong", {}, "Refer to"), el("span", { class: "muted" }, "Click to put it in the field you are editing")),
    ...rows.map((row) => el("div", { class: "rrow" },
      el("span", { class: "rid mono" }, row.id),
      el("span", { class: "rchips" }, row.chips.map((chip) => {
        const b = el("button", { type: "button", class: "pill link mono", title: (chip.open ? "Put " + chip.text + " there, then type the rest" : "Put " + chip.text + " there") }, chip.short || chip.text.replace(/^trigger\./, ""));
        // Keep the text field's caret and focus while the button is pressed.
        b.addEventListener("mousedown", (e) => e.preventDefault());
        b.addEventListener("click", () => insertRef(A, chip));
        return b;
      })))),
  ]);
}

function insertRef(A, chip) {
  const t = A.target && A.target.isConnected ? A.target : A.targets[1] || A.targets[0];
  if (!t) return;
  const raw = t.dataset.raw === "1";
  const text = raw ? chip.text : "${" + chip.text + "}";
  t.focus();
  // A field that takes one key has it replaced, not added to.
  if (t.dataset.whole === "1" && (t.value.trim() === "" || WHOLE_REF.test(t.value.trim())) && !chip.open) t.select();
  const from = t.selectionStart == null ? t.value.length : t.selectionStart, to = t.selectionEnd == null ? from : t.selectionEnd;
  t.setRangeText(text, from, to, "end");
  // An unfinished reference leaves the caret where the rest is typed.
  if (chip.open && !raw) { const at = from + text.length - 1; t.setSelectionRange(at, at); }
  t.dispatchEvent(new Event("input", { bubbles: true }));
}

async function addStep() {
  if (!view || !state.editable) return;
  const name = view.w.name, w = workflowByName(name), st = uiOf(name), a = addOf(name);
  if (!w || w.problem || st.saving) return;
  const comp = chosenOf(w, a);
  if (!comp) return;
  if (a.after !== "trigger" && !w.steps.some((s) => s.id === a.after)) {
    st.notice = { kind: "error", did: "add", error: "The step this was to go after is no longer in the workflow. Choose a position and add it again." };
    updateBar();
    return;
  }
  const id = a.id.trim();
  const given = {};
  for (const inp of comp.inputs || []) {
    const v = a.values[inp.name] || "";
    if (v.trim() !== "") given[inp.name] = v;
  }
  // A kind that does its own work says what it does; a component says which one and what goes in.
  const add = { id, after: a.after, when: a.when.trim() };
  if (a.kind === "http") Object.assign(add, { kind: "http", method: a.method, url: a.url.trim() });
  else if (a.kind === "decide") Object.assign(add, { kind: "decide", model: a.models.decide.trim(), input: a.input.trim(), questions: questionsOut(a.questions) });
  else if (a.kind === "ai") Object.assign(add, { kind: "ai", model: a.models.ai.trim(), input: a.input.trim(), prompt: a.prompt, fields: fieldsOut(a.fields) });
  else Object.assign(add, { use: comp.use, with: given });
  st.saving = true;
  st.notice = null;
  updateBar();
  let r = null, failure = null;
  try {
    r = await request("POST", "/api/steps", { workflow: name, rev: w.rev, add });
  } catch (err) {
    failure = { kind: "network", did: "add", id };
  }
  st.saving = false;
  if (!failure && r.ok && r.body.workflow) {
    epoch++;
    changesEpoch++;
    const prev = workflowByName(name);
    replaceWorkflow(r.body.workflow);
    known[name] = r.body.workflow.rev;
    const dropped = reattach(name, prev, r.body.workflow);
    delete adds[name];
    st.notice = { kind: "saved", did: "add", title: "Added " + id, files: [r.body.workflow.path || w.path], dropped: dropped.length ? dropped : null };
    afterRender = () => {
      const card = document.querySelector('#canvas [data-node="' + CSS.escape(id) + '"]');
      if (card) card.focus({ preventScroll: true });
    };
    go({ add: null, node: id });
    readAddress();
  } else {
    st.notice = failure || failureOf(r, "workflow", { did: "add", id });
  }
  await refresh();
  uiTick++;
  shown = "";
  render();
}

async function removeStep(name, id) {
  const w = workflowByName(name), st = uiOf(name);
  if (!w || w.problem || st.saving) return;
  st.saving = true;
  st.notice = null;
  updateBar();
  let r = null, failure = null;
  try {
    r = await request("POST", "/api/steps", { workflow: name, rev: w.rev, remove: id });
  } catch (err) {
    failure = { kind: "network", did: "remove", id };
  }
  st.saving = false;
  if (!failure && r.ok && r.body.workflow) {
    epoch++;
    changesEpoch++;
    const prev = workflowByName(name);
    replaceWorkflow(r.body.workflow);
    known[name] = r.body.workflow.rev;
    const dropped = reattach(name, prev, r.body.workflow);
    st.notice = { kind: "saved", did: "remove", title: "Removed " + id, files: [r.body.workflow.path || w.path], dropped: dropped.length ? dropped : null };
    go({ node: null });
    readAddress();
  } else {
    st.notice = failure || failureOf(r, "workflow", { did: "remove", id });
  }
  await refresh();
  uiTick++;
  shown = "";
  render();
}

// ---- making a workflow ----
//
// The form for a new workflow is a sibling of the form for a new step and
// shares its parts: the picker of components, the fields of the step and the
// chips that put a reference into the field being edited. What has been typed
// lives in `making`, so the poll, a trip to another workflow or a redraw loses
// none of it, until the workflow is made or the form is cancelled. The drawing
// in the middle is built from the same draft, by the same code that draws any
// workflow.

const NEW = "\u0000new";                      // what a workflow being made is called in `ui`, where saving and refusals are kept
const NAME_OK = /^[A-Za-z0-9][A-Za-z0-9_-]*$/;
const NO_STEPS = { steps: [] };
const MAKEW = { name: NEW, steps: [], path: "" }; // for the notice, which looks at a workflow to word itself
const canMake = () => !!state.editable && !!state.roots && state.roots.length > 0;
const makeMode = () => canMake() && !!selected.make;
const pathOf = (m) => (m.pathTouched ? m.path : "/" + m.name.trim());
const fileOf = (m) => (m.root ? m.root + "/" : "") + (m.name.trim() || "<name>") + "/workflow.yaml";
const makeTouched = () => !!making && (making.name.trim() !== "" || making.pathTouched || making.cron.trim() !== "" || addTouched(making.step));

function makeOf() {
  if (!making) {
    making = { root: (state.roots || [])[0] || "", name: "", trigger: "webhook", path: "", pathTouched: false, cron: "",
      step: { after: "trigger", component: null, id: "", idTouched: false, when: "", values: {} } };
  }
  return making;
}

// makerComps are the components the first step can use, in the shape the
// picker draws them: by the name the page knows each by (its folder) in `use`.
function makerComps() {
  return state.workflows.filter(isComp).map((x) => ({
    name: compTitle(x), use: x.name, kind: compKind(x), description: x.description || "", inputs: x.inputs || [],
    inner: x.gives ? x.steps.map((s) => ({ id: s.id, kind: s.kind, subtitle: s.subtitle })) : null,
  }));
}

// settleMaker settles the choice of component and the id that goes with it, and
// returns the component chosen, if any.
function settleMaker(m, comps) {
  settleChoice(NO_STEPS, comps, m.step);
  const comp = comps.find((c) => c.use === m.step.component) || null;
  if (comp && !m.step.idTouched) m.step.id = freeId(NO_STEPS, comp.name);
  return comp;
}

// makerBase is the draft as far as the trigger: the workflow the chips refer to.
function makerBase(m) {
  const trigger = m.trigger === "webhook" ? "POST " + pathOf(m).trim() : "cron" + (m.cron.trim() ? " " + m.cron.trim() : "");
  return { name: NEW, draft: true, label: m.name.trim(), trigger, steps: [] };
}

// draftWorkflow is the workflow the form describes, in the shape the server
// gives one, so that it can be drawn like any other.
function draftWorkflow() {
  const m = makeOf(), w = makerBase(m), comp = settleMaker(m, makerComps());
  if (comp) {
    w.steps = [{ id: m.step.id.trim() || "step", kind: comp.kind, subtitle: comp.name, component: comp.name, when: m.step.when.trim(), reads: [], outputs: [], inner: comp.inner || undefined }];
  }
  return w;
}

// redrawMake draws the draft again, in the top bar and in the middle.
function redrawMake() {
  if (!view || !view.w.draft) return;
  const w = draftWorkflow();
  view = { w, run: null };
  renderTop(w);
  renderCanvas(false);
}

// changedMake is what a change in the form brings about, apart from the form itself.
function changedMake() {
  syncMaker();
  redrawMake();
  updateBar();
}

function hideMaker() {
  if (!maker) return;
  maker = null;
  insp.formslot.replaceChildren();
}

function showMaker() {
  makeOf();
  if (!maker) {
    maker = buildMaker();
    insp.formslot.replaceChildren(maker.root);
    syncMaker();
    afterRender = () => { if (maker) maker.nameBox.focus({ preventScroll: false }); };
  } else syncMaker();
}

function buildMaker() {
  const m = making, uid = ++formId;
  const A = { maker: true, workflow: NEW, uid, compSig: "", detailKey: "", refSig: "", rootSig: "", target: null, targets: [], idBox: null, a: m.step };
  A.submit = () => makeWorkflow();
  A.onInput = changedMake;
  A.pick = (use) => {
    const comp = makerComps().find((c) => c.use === use);
    m.step.component = use;
    if (comp && !m.step.idTouched) m.step.id = freeId(NO_STEPS, comp.name);
    changedMake();
  };

  // The name, which is also the folder's.
  const nameId = "mname" + uid;
  A.nameBox = formBox(A, nameId, { mono: true, attrs: { placeholder: "refund-alerts" }, set: (v) => { m.name = v; } });
  A.nameWarn = el("p", { class: "conflict", hidden: true });
  A.fileNote = el("p", { class: "hint-text" });
  A.nameField = el("div", { class: "field" },
    el("div", { class: "lab" }, el("label", { for: nameId }, "Name")),
    A.nameBox,
    el("p", { class: "hint-text" }, "The folder's name too: letters, digits, - and _."),
    A.fileNote,
    A.nameWarn);

  // Where the folder is made, when there is a choice.
  const rootId = "mroot" + uid;
  A.rootBox = el("select", { id: rootId, class: "in" });
  A.rootBox.addEventListener("change", () => { m.root = A.rootBox.value; changedMake(); });
  A.rootField = plainField(A, "Folder", null, A.rootBox, "Where the workflow's folder is made.");

  // What starts it.
  const kinds = [
    ["webhook", "A request arrives", "Webhook", "Runs when something sends a request to a path of this server."],
    ["cron", "On a schedule", "Schedule", "Runs at the times a schedule gives."],
  ];
  A.triggerEls = {};
  const group = "mtrig" + uid;
  const rows = kinds.map(([key, title, kind, text]) => {
    const input = el("input", { type: "radio", name: group, value: key });
    input.addEventListener("change", () => { if (input.checked) { m.trigger = key; changedMake(); } });
    const row = el("label", { class: "choice kind-trigger" },
      input, kindTile("trigger", 38),
      el("span", { class: "ctext" },
        el("span", { class: "chead" }, el("span", { class: "cname" }, title), el("span", { class: "ckind" }, kind)),
        el("span", { class: "cdesc" }, text)));
    A.triggerEls[key] = { row, input };
    return row;
  });
  A.when = el("fieldset", { class: "picker" }, el("legend", {}, "Starts when"), el("div", { class: "choices", role: "presentation" }, rows));
  A.pathBox = formBox(A, "mpath" + uid, { mono: true, attrs: { placeholder: "/path" }, set: (v) => { m.path = v; m.pathTouched = true; } });
  A.pathField = plainField(A, "Path", el("span", { class: "tag", title: "The method of the request" }, "POST"), A.pathBox,
    "Steps read the request's JSON body as trigger.body.");
  A.cronBox = formBox(A, "mcron" + uid, { mono: true, attrs: { placeholder: "0 3 * * *" }, set: (v) => { m.cron = v; } });
  A.cronField = plainField(A, "Schedule", el("span", { class: "tag", title: "A cron schedule" }, "CRON"), A.cronBox,
    "Five fields: minute hour day month weekday. Steps read the time as trigger.time.");

  // The first step.
  A.stepHead = el("h2", { class: "sub" }, "First step");
  A.empty = el("div", { class: "emptystate" },
    el("div", { class: "etitle" }, "No components yet"),
    el("p", {}, "The first step uses a component: a step written once, with named inputs. None exists yet, so no workflow can be made from here."),
    el("p", {}, "A component is written as a ", el("span", { class: "mono" }, "component.yaml"), " file in a folder of its own."),
    el("p", { class: "muted small" }, "Write one in your editor and commit it. This page notices by itself, and it appears here."));
  A.choices = el("div", { class: "choices", role: "presentation" });
  A.picker = el("fieldset", { class: "picker" }, el("legend", {}, "Component"), A.choices);
  A.detail = el("div", { class: "adddetail" });
  A.root = el("section", { class: "editform addform" }, A.nameField, A.rootField, A.when, A.pathField, A.cronField, A.stepHead, A.empty, A.picker, A.detail);
  return A;
}

// nameNote says what the file will be, and warns, without stopping anything,
// about a name the server would refuse.
function nameNote(A, m) {
  const name = m.name.trim();
  let text = "";
  if (name && !NAME_OK.test(name)) text = "Use letters, digits, - and _, and start with a letter or a digit.";
  else if (name && state.workflows.some((x) => !x.component && x.name === name)) text = "A workflow called " + name + " already exists.";
  A.nameWarn.hidden = !text;
  A.nameWarn.textContent = text;
  A.fileNote.replaceChildren("Written to ", mono(fileOf(m)));
}

// syncMaker brings the form up to date with the draft and with the page's data
// without touching a box whose text already matches.
function syncMaker() {
  const A = maker, m = making;
  if (!A || !m) return;
  const roots = state.roots || [];
  if (!roots.includes(m.root)) m.root = roots[0] || "";
  A.rootField.hidden = roots.length < 2;
  if (roots.join("\n") !== A.rootSig) {
    A.rootSig = roots.join("\n");
    A.rootBox.replaceChildren(...roots.map((r) => el("option", { value: r }, r)));
  }
  A.rootBox.value = m.root;

  setValue(A.nameBox, m.name);
  nameNote(A, m);
  for (const [key, c] of Object.entries(A.triggerEls)) {
    c.input.checked = m.trigger === key;
    c.row.classList.toggle("on", m.trigger === key);
  }
  A.pathField.hidden = m.trigger !== "webhook";
  A.cronField.hidden = m.trigger !== "cron";
  setValue(A.pathBox, pathOf(m));
  setValue(A.cronBox, m.cron);

  const comps = makerComps();
  A.empty.hidden = comps.length > 0;
  A.stepHead.hidden = A.picker.hidden = A.detail.hidden = comps.length === 0;
  if (!comps.length) return;
  const comp = settleMaker(m, comps);
  syncChoices(A, comps, m.step.component, (c) => comps.some((o) => o !== c && o.name === c.name));
  A.detail.hidden = !comp;
  if (!comp) { A.detailKey = ""; return; }
  syncDetail(A, makerBase(m), comp, m.step, m.trigger);
}

function cancelMake() {
  const st = uiOf(NEW);
  if (st.saving) return;
  making = null;
  st.notice = null;
  go({ make: false });
  readAddress();
  shown = "";
  render();
}

async function makeWorkflow() {
  if (!state.editable || !making || !maker) return;
  const st = uiOf(NEW), m = making;
  if (st.saving) return;
  const comp = settleMaker(m, makerComps());
  if (!comp) return;
  const given = {};
  for (const inp of comp.inputs || []) {
    const v = m.step.values[inp.name] || "";
    if (v.trim() !== "") given[inp.name] = v;
  }
  const id = m.step.id.trim();
  const body = {
    name: m.name.trim(), root: m.root,
    trigger: m.trigger === "webhook" ? { webhook: pathOf(m).trim() } : { cron: m.cron.trim() },
    step: { id, use: comp.use, when: m.step.when.trim(), with: given },
  };
  st.saving = true;
  st.notice = null;
  updateBar();
  let r = null, failure = null;
  try {
    r = await request("POST", "/api/workflows", body);
  } catch (err) {
    failure = { kind: "network", did: "make" };
  }
  st.saving = false;
  if (!failure && r.ok && r.body.workflow) {
    const made = r.body.workflow;
    epoch++;
    changesEpoch++;
    if (state.workflows.some((x) => x.name === made.name)) replaceWorkflow(made); else state.workflows.push(made);
    known[made.name] = made.rev;
    making = null;
    hideMaker();
    uiOf(made.name).notice = { kind: "saved", did: "make", title: "Made " + made.name, files: [made.path], dropped: null };
    afterRender = () => {
      const card = document.querySelector('#canvas [data-node="' + CSS.escape(id) + '"]');
      if (card) card.focus({ preventScroll: true });
    };
    go({ workflow: made.name, run: null, node: null });
    readAddress();
  } else {
    st.notice = failure || failureOf(r, "workflow", { did: "make" });
  }
  await refresh();
  uiTick++;
  shown = "";
  render();
}

// paintMakeBar is the save bar for the form: Cancel and Make workflow.
function paintMakeBar(b) {
  const st = uiOf(NEW), m = making;
  const comps = makerComps(), comp = m ? comps.find((c) => c.use === m.step.component) : null;
  b.root.hidden = false;
  b.shared.hidden = true;
  b.status.replaceChildren(...(st.saving ? ["Making…"] : comp && m ? ["Writes a new workflow file."]
    : [comps.length ? "Choose a component." : "No component yet."]));
  b.status.classList.remove("dirty");
  b.save.setAttribute("aria-disabled", st.saving || !comp ? "true" : "false");
  b.discard.setAttribute("aria-disabled", st.saving ? "true" : "false");
  b.discard.classList.remove("armed");
  b.discard.textContent = "Cancel";
  const mode = st.saving ? "making" : "make";
  if (b.save.dataset.mode !== mode) { b.save.dataset.mode = mode; b.save.replaceChildren(st.saving ? "Making…" : "Make workflow", el("kbd", {}, isMac ? "⌘S" : "Ctrl+S")); }
  if (b.shownNotice !== st.notice) {
    b.shownNotice = st.notice;
    b.slot.replaceChildren(...(st.notice ? [noticeView(st.notice, MAKEW)] : []));
  }
}

// ---- tests ----
//
// A test is shown as a run (source "test", with the test's name and its
// checks). What this page adds is the round: the tests one press of a button
// started, followed until each has finished. A round lives here, by workflow,
// not in the page, so the poll, a trip to another run or step, or another
// workflow does not lose it. Each test of a round is told apart from the
// others by the run id the server answered with; a run that is not in the
// list of runs yet is still waiting for its turn.

const rounds = {};      // workflow -> { items: [{ run, test, index, last }], rev, unsaved, told }
const testState = {};   // workflow -> { starting, error }

const testsOf = (name) => testState[name] || (testState[name] = { starting: false, error: null });
const isTestRun = (r) => !!r && r.source === "test";
const couldNotRun = (r) => isTestRun(r) && verdictOf(r) === "unrun";
const canTest = (w) => !!w && state.editable && !w.problem && (w.cases || []).length > 0;
const COULD_NOT_RUN = "The test could not run";

// How a test ended is more than ok or failed. A failed test either did not
// get what it expected from steps that all worked, or a step failed, or it
// could not run at all, which says nothing about the workflow or the test.
const VERDICT = {
  waiting: { mark: "pending", word: "Waiting for its turn", short: "waiting" },
  running: { mark: "running", word: "Running", short: "running" },
  passed: { mark: "ok", word: "Passed", short: "passed" },
  unmet: { mark: "failed", word: "Expectation not met", short: "not met" },
  broke: { mark: "failed", word: "A step failed", short: "step failed" },
  unrun: { mark: "skipped", word: "Could not run", short: "could not run" },
};

function verdictOf(run) {
  if (!run) return "waiting";
  if (run.status === "running") return "running";
  if (run.status === "ok") return "passed";
  const checks = run.checks || [];
  if (!checks.length && (run.error || "").startsWith(COULD_NOT_RUN)) return "unrun";
  if (!checks.length || run.steps.some((s) => s.status === "failed")) return "broke";
  return "unmet";
}

const unmetOf = (run) => (run.checks || []).filter((c) => c.failure);
const rowTone = (v) => ({ passed: "ok", unmet: "failed", broke: "failed", unrun: "unrun", running: "running", waiting: "pending" }[v]);

// testOutcome says in a few words what a test run came to.
function testOutcome(r) {
  const v = verdictOf(r), n = (r.checks || []).length;
  if (v === "passed") return n ? (n === 1 ? "The expectation was met" : "All " + n + " expectations met") : "Ran without failing";
  if (v === "unmet") return "Not met: " + unmetOf(r).map((c) => c.key).join(", ");
  if (v === "unrun") return "Could not run";
  return outcome(r);
}

// ---- the round ----

function syncRounds() {
  for (const [name, round] of Object.entries(rounds)) {
    for (const it of round.items) {
      const run = state.runs.find((r) => r.run === it.run);
      if (run && run.status !== "running") it.last = { verdict: verdictOf(run), ms: run.ms };
    }
    const v = roundView(name);
    if (v && !v.active && !round.told) {
      round.told = true;
      announce(name + ": " + roundSummary(v).spoken);
    }
  }
}

// roundView pairs each test of the round with its run, once there is one.
function roundView(name) {
  const round = rounds[name];
  if (!round) return null;
  const rows = round.items.map((it) => {
    const run = state.runs.find((r) => r.run === it.run) || null;
    return { it, run, verdict: run ? verdictOf(run) : it.last ? it.last.verdict : "waiting" };
  });
  return { round, rows, active: rows.some((r) => r.verdict === "waiting" || r.verdict === "running") };
}

const roundActive = (name) => { const v = roundView(name); return !!v && v.active; };

function roundSummary(v) {
  const count = (k) => v.rows.filter((r) => r.verdict === k).length;
  const total = v.rows.length, passed = count("passed"), unmet = count("unmet"), broke = count("broke"), unrun = count("unrun");
  const done = total - count("running") - count("waiting");
  if (v.active) {
    const sub = done + " of " + total + " done" + (passed ? ", " + passed + " passed" : "");
    return { title: "Running tests", sub, mark: "running", tone: "running", spoken: sub };
  }
  if (total === 1) {
    const V = VERDICT[v.rows[0].verdict];
    return { title: V.word, sub: "", mark: V.mark, tone: rowTone(v.rows[0].verdict), spoken: V.word };
  }
  const bits = [];
  if (unmet) bits.push(unmet + (unmet === 1 ? " expectation" : " expectations") + " not met");
  if (broke) bits.push(plural(broke, "step failure"));
  if (unrun) bits.push(unrun + " could not run");
  const title = passed === total ? "All " + total + " passed" : passed + " of " + total + " passed";
  const failed = unmet + broke > 0;
  return { title, sub: bits.join(", "), mark: failed ? "failed" : passed === total ? "ok" : "skipped", tone: failed ? "failed" : passed === total ? "ok" : "unrun", spoken: title + (bits.length ? ", " + bits.join(", ") : "") };
}

// A quiet announcement for screen readers, so that a round finishing is not
// only seen.
let announcer = null;
let announced = "";
function announce(text) {
  if (!announcer) {
    announcer = el("div", { class: "sr", role: "status", "aria-live": "polite" });
    document.body.append(announcer);
  }
  if (text === announced) return;
  announced = text;
  announcer.textContent = text;
}

async function startTests(name, index) {
  const w = workflowByName(name), st = testsOf(name);
  if (!canTest(w) || st.starting || roundActive(name)) return;
  st.starting = true;
  st.error = null;
  uiTick++;
  render();
  let r = null;
  try {
    r = await request("POST", "/api/test", index == null ? { workflow: name } : { workflow: name, test: index });
  } catch (err) {
    r = null;
  }
  st.starting = false;
  if (r && r.ok && Array.isArray(r.body.runs)) {
    rounds[name] = {
      items: r.body.runs.map((x, j) => ({ run: x.run, test: x.test, index: index == null ? j : index, last: null })),
      rev: w.rev, unsaved: dirtyCount(name), told: false,
    };
    // Whoever pressed the button lands on the results.
    afterRender = () => { const box = document.querySelector('[data-focus="round"]'); if (box) box.focus({ preventScroll: true }); };
  } else {
    st.error = r ? r.body.error || "The server answered " + r.status + "." : "Could not reach the server. Nothing was started.";
  }
  uiTick++;
  render();
  refresh();
}

function clearRound(name) {
  delete rounds[name];
  uiTick++;
  afterRender = () => { const b = document.querySelector('[data-focus="ov:test-all"]'); if (b) b.focus({ preventScroll: true }); };
  render();
}

// Tests run the saved file. A person with unsaved changes may well want that
// (to compare), so nothing is blocked: it is said plainly, with the choice to
// save first.
async function saveThenRun(name) {
  if (!view || view.w.name !== name) return;
  await saveCurrent();
  const st = uiOf(name);
  if (dirtyCount(name) > 0 || (st.notice && st.notice.kind !== "saved")) return;
  startTests(name);
}

function startButton(w, index, label, focus, opts) {
  const o = opts || {};
  const busy = testsOf(w.name).starting || roundActive(w.name);
  const b = el("button", {
      type: "button", class: "btn" + (o.primary ? " primary" : "") + (o.block ? " block" : "") + (o.small ? " small" : ""), "data-focus": focus,
      "aria-disabled": busy ? "true" : "false",
      title: busy ? "Tests are running" : "Runs the saved file. Calls the models for real and sends no requests.",
    }, icon("run", 12), label);
  b.addEventListener("click", () => {
    if (busy) return;
    // From the drawing, the list of tests opens so the results can be seen.
    if (o.show) go({ list: "tests", run: null, node: null });
    startTests(w.name, index);
  });
  return b;
}

function unsavedNote(w) {
  const n = dirtyCount(w.name);
  if (!n || !canTest(w)) return null;
  const save = el("button", { type: "button", class: "btn small", "data-focus": "unsaved:" + w.name }, "Save, then run all tests");
  save.addEventListener("click", () => saveThenRun(w.name));
  return el("div", { class: "notice warn" },
    el("div", { class: "nhead" }, el("strong", {}, "Unsaved changes are not tested")),
    el("p", {}, "Tests run the saved file, " + w.path + ". Your " + plural(n, "unsaved change") + (n === 1 ? " is" : " are") + " not in it."),
    // While a step is being added the save bar adds it, so Save is not offered here; nor while tests run.
    selected.add || testsOf(w.name).starting || roundActive(w.name) ? null : save);
}

function testErrorNotice(w) {
  const st = testsOf(w.name);
  if (!st.error) return null;
  const close = el("button", { type: "button", class: "x small", "aria-label": "Dismiss this message", title: "Dismiss" }, icon("close", 13));
  close.addEventListener("click", () => { st.error = null; uiTick++; render(); });
  return el("div", { class: "notice bad", role: "alert" }, el("div", { class: "nhead" }, el("strong", {}, "The tests did not start"), close), el("p", {}, st.error));
}

// ---- the round ----

function roundBox(w, v) {
  const s = roundSummary(v);
  const clear = el("button", { type: "button", class: "x small", "aria-label": "Clear these results", title: "Clear these results", "data-focus": "round-clear" }, icon("close", 13));
  clear.addEventListener("click", () => clearRound(w.name));
  const list = v.rows.map((r) => {
    const V = VERDICT[r.verdict], tone = rowTone(r.verdict);
    const b = el("button", {
        type: "button", class: "row tr " + tone, "data-focus": "round:" + r.it.run,
        "aria-current": r.run && r.run.run === selected.run ? "true" : null,
        "aria-disabled": r.run ? null : "true",
        "aria-label": r.it.test + ", " + V.word,
        title: r.run ? "Open this test's run" : "Waiting for its turn",
      },
      statusMark(V.mark, 11),
      el("span", { class: "name" }, r.it.test),
      // A test that did not pass says why, in a line of its own, which makes the word beside the name unnecessary.
      r.run && (r.verdict === "unmet" || r.verdict === "broke" || r.verdict === "unrun") ? el("span", { class: "sub" }, testOutcome(r.run)) : el("span", { class: "aside" }, V.short));
    b.addEventListener("click", () => { if (r.run) go({ run: r.run.run, node: null }); });
    return b;
  });
  const stale = v.round.rev !== w.rev;
  return el("div", { class: "roundbox " + s.tone, role: "group", tabindex: "-1", "data-focus": "round", "aria-label": "Test results: " + s.spoken },
    el("div", { class: "rbtop" },
      statusMark(s.mark, 13),
      el("div", { class: "rbtext" }, el("div", { class: "rbtitle" }, s.title), s.sub ? el("div", { class: "rbsub" }, s.sub) : null),
      clear),
    el("span", { class: "seg", "aria-hidden": "true" }, v.rows.map((r) => el("i", { class: { passed: "ok", unmet: "failed", broke: "failed", unrun: "skipped", running: "running", waiting: "pending" }[r.verdict] }))),
    el("div", { class: "rlist" }, list),
    v.round.unsaved ? el("p", { class: "tnote" }, "Ran the saved file. " + plural(v.round.unsaved, "unsaved change") + (v.round.unsaved === 1 ? " was" : " were") + " not in it.") : null,
    stale ? el("p", { class: "tnote" }, "The file was saved after this round started.") : null,
    !v.active && canTest(w) ? startButton(w, null, "Run all again", "round:test-all", { small: true }) : null);
}

// ---- the checks ----

// stepOfKey names the step (or the trigger or response) a check is about: its
// key is that id, or starts with it and a dot.
function stepOfKey(w, key) {
  // [the name a key uses, the node it is about]
  const names = isComp(w) ? [["input", IN], ["output", OUT]] : [["trigger", "trigger"]];
  w.steps.forEach((s) => names.push([s.id, s.id]));
  if (w.respond) names.push(["response", "response"]);
  let best = null;
  for (const [name, id] of names) if ((key === name || key.startsWith(name + ".")) && (!best || name.length > best[0].length)) best = [name, id];
  return best ? best[1] : null;
}

const mono = (text) => el("span", { class: "mono" }, text);

function checkRow(w, c, jump) {
  const ok = !c.failure;
  const id = stepOfKey(w, c.key);
  const isStep = id !== null && nodeName(id) === c.key && id !== IN && id !== OUT;
  const want = isStep && typeof c.want === "string" ? c.want : JSON.stringify(c.want);
  // The failure starts with the key it is about: it is not said twice.
  const failure = c.failure && c.failure.startsWith(c.key + ": ") ? c.failure.slice(c.key.length + 2) : c.failure;
  return el("li", { class: "check " + (ok ? "ok" : "failed") },
    statusMark(ok ? "ok" : "failed", 10),
    el("div", { class: "cbody" },
      el("div", { class: "ckey mono" }, el("span", { class: "sr" }, ok ? "Met: " : "Not met: "), c.key),
      ok ? el("div", { class: "cwant" }, isStep ? "Expected it to be " : "Expected ", mono(want)) : el("div", { class: "cfail mono" }, failure)),
    jump && id ? el("button", { type: "button", class: "pill link", title: "Show " + nodeName(id) + " in this run", onclick: () => go({ node: id }) }, "Show " + nodeName(id)) : null);
}

function checkList(w, checks, jump) {
  return el("ul", { class: "checks" }, checks.map((c) => checkRow(w, c, jump)));
}

// checksAbout lists, on a step in a test's run, the checks that are about it.
function checksAbout(w, run, id) {
  if (!isTestRun(run)) return null;
  const mine = (run.checks || []).filter((c) => stepOfKey(w, c.key) === id);
  if (!mine.length) return null;
  return section("Checks on this step", checkList(w, mine, false), el("p", { class: "muted small" }, "From the test ", el("span", { class: "mono" }, run.test), "."));
}

// ---- centre and right: a test's run ----

function testBar(run) {
  const v = verdictOf(run), V = VERDICT[v];
  const allWell = v === "unmet" && !run.steps.some((s) => s.status === "failed");
  return el("div", { class: "runbar " + rowTone(v) },
    el("span", { class: "state " + rowTone(v) }, statusMark(V.mark, 12), V.word),
    el("span", { class: "ttag" }, "Test"),
    el("span", { class: "id" }, run.test),
    allWell ? el("span", { class: "meta" }, "every step succeeded") : null,
    el("span", { class: "meta" }, when(run.started)),
    v !== "running" && v !== "unrun" ? el("span", { class: "meta" }, "took " + duration(run.ms)) : null,
    el("button", { type: "button", class: "ghost", onclick: () => go({ run: null, node: null }) }, "Close run", el("kbd", {}, "Esc")));
}

function testRunDetails(w, run) {
  const v = verdictOf(run), V = VERDICT[v];
  const checks = run.checks || [], unmet = unmetOf(run);
  const failedStep = run.steps.find((s) => s.status === "failed");
  const idx = (w.cases || []).findIndex((c) => c.name === run.test);
  const spec = idx >= 0 ? w.cases[idx] : null;
  const comp = isComp(w);
  const given = comp && run.trigger && run.trigger.input !== undefined ? run.trigger.input : undefined;
  let panel;
  if (v === "passed") panel = panelTitled("ok", "Passed", el("div", { class: "muted" }, (checks.length ? (checks.length === 1 ? "The expectation was met." : "All " + checks.length + " expectations were met.") : "Every step ran.") + " Took " + duration(run.ms) + "."));
  else if (v === "unmet") panel = panelTitled("failed", "Expectation not met", el("div", { class: "muted" },
    unmet.length + " of " + checks.length + (checks.length === 1 ? " expectation was" : " expectations were") + " not met. "
    + (failedStep ? "" : "Every step ran without failing: the " + (comp ? "component" : "workflow") + " did what it does, but not what the test expects.")));
  else if (v === "broke") panel = panelTitled("failed", "A step failed", el("div", { class: "muted" },
    (failedStep ? "Step " + failedStep.id + " failed, so " : "The " + (comp ? "component" : "workflow") + " failed, so ") + "the expectations were not checked."));
  else if (v === "unrun") {
    panel = panelTitled("skipped", "Could not run", el("div", { class: "muted" }, "This says nothing about the " + (comp ? "component" : "workflow") + " or the test: nothing was checked, and nothing here failed."));
    panel.classList.add("unrun");
  } else panel = panelTitled("running", "Running", el("div", { class: "muted" }, plural(run.steps.filter((s) => s.status === "ok").length, "step") + " done"));

  const again = spec && canTest(w) ? startButton(w, idx, "Run this test again", "insp:test-again", { small: true }) : null;
  return [
    header("workflow", "run", run.test, "Test run of " + (comp ? "component " + compTitle(w) : run.workflow), false, backTo(w, run)),
    section("Result", panel,
      run.error ? el("pre", { class: v === "unrun" ? "plain" : "error" }, run.error) : null,
      again ? el("div", { class: "againrow" }, again) : null,
      unsavedNote(w),
      // A test that could not run called no model, and one still running has not finished calling them.
      v === "unrun" ? null : el("p", { class: "muted small" },
        v === "running" ? "The models are called for real. HTTP steps are not sent." : "The models were called for real. HTTP steps were not sent.")),
    checks.length ? section("Expectations" + (checks.length ? " (" + checks.length + ")" : ""), checkList(w, checks, true),
      el("p", { class: "muted small" }, "Select Show to see the step a check is about.")) : null,
    // A component's test is a call: what it was given, and what it gave back.
    given !== undefined ? section("What the test gave it", jsonView(given),
      spec && typeof spec.input === "string" ? el("p", { class: "muted small" }, "Read from ", mono(spec.input), ".") : el("p", { class: "muted small" }, "As written in the test. ",
        el("button", { type: "button", class: "linkbtn", onclick: () => go({ node: IN }) }, "Show the input"))) : null,
    comp && run.gave !== undefined ? section("What it gave back", jsonView(run.gave),
      el("button", { type: "button", class: "linkbtn", onclick: () => go({ node: OUT }) }, "Show the output")) : null,
    section("Steps", waterfall(w, run), el("p", { class: "muted small" }, "Select a step, here or on the drawing, to see what it produced.")),
    !comp && run.trigger !== undefined ? section("Trigger payload", jsonView(run.trigger), spec ? el("p", { class: "muted small" }, "From the fixture ", mono(spec.fixture), ".") : null) : null,
    section("About this run", pairs([
      ["Run", mono(run.run)],
      ["Test", run.test],
      !comp && spec ? ["Fixture", mono(spec.fixture)] : null,
      run.commit ? ["Version", mono(run.commit)] : null,
    ])),
  ];
}

// caseInput says in a few words what a test gives. A workflow's test names a
// fixture; a component's gives its input, written in the test or in a file.
function caseInput(w, c) {
  if (!isComp(w)) return c.fixture;
  if (typeof c.input === "string") return "input from " + c.input;
  const keys = c.input && typeof c.input === "object" ? Object.keys(c.input) : [];
  return keys.length ? "gives " + keys.join(", ") : "gives no input";
}

// ---- right: the tests of a workflow, without running anything ----

function latestTestRun(name, test) {
  let best = null;
  for (const r of state.runs) if (r.workflow === name && isTestRun(r) && r.test === test && (!best || r.started > best.started)) best = r;
  return best;
}

// testsSection lists the tests under the given heading: "Tests" with a
// component's details, none in the list of a workflow's tests.
function testsSection(w, title = "Tests") {
  if (!state.editable) return null;
  const cases = w.cases || [];
  if (!cases.length) {
    return section(title, el("div", { class: "emptystate" },
      el("div", { class: "etitle" }, "No tests yet"),
      canAddTest(w) ? el("p", {}, "Keep a run that did the right thing as a test: open a run and choose ", el("strong", {}, "Keep as a test"), ". Or write one from nothing.") : null,
      canAddTest(w) ? el("p", {}, newTestButton(w)) : null,
      el("p", {}, "Tests are written under ", mono("tests:"), " in ", mono(w.path), ". " + (isComp(w)
        ? "Each gives the component an input, written there or in a file, and says what to expect of its steps and of what it gives back."
        : "Each names a fixture, a sample request, and what to expect of the steps and what they put out.")),
      el("p", { class: "muted small" }, "Write them in your editor and commit. This page notices by itself, and they appear here.")));
  }
  const v = roundView(w.name);
  return section(title,
    el("p", { class: "muted" }, "Tests call the AI models for real, so they check what the model actually answers. No HTTP request is sent. They run the saved file."),
    canTest(w) || canAddTest(w) ? el("div", { class: "testrow" }, canTest(w) ? startButton(w, null, "Run all tests", "ov:test-all", { primary: true }) : null, canAddTest(w) ? newTestButton(w) : null)
      : el("p", { class: "muted small" }, "Tests cannot run while the file has a problem."),
    canAddTest(w) ? el("p", { class: "muted small" }, "To keep a run as a test, open it and choose Keep as a test.") : null,
    unsavedNote(w),
    testErrorNotice(w),
    el("ul", { class: "tcases" }, cases.map((c, i) => testCase(w, c, i, v))));
}

function testCase(w, c, i, v) {
  const item = v ? v.rows.find((r) => r.it.index === i && (r.verdict === "waiting" || r.verdict === "running")) : null;
  const last = latestTestRun(w.name, c.name);
  const verdict = item ? item.verdict : last ? verdictOf(last) : null;
  const V = verdict ? VERDICT[verdict] : null;
  const finished = !item && last && last.status !== "running" ? last : null;
  let line;
  if (!verdict) line = "Not run yet";
  else if (item) line = V.word;
  else line = V.word + " · " + when(last.started);
  const open = last && !item ? el("button", { type: "button", class: "linkbtn", onclick: () => go({ run: last.run, node: null }) }, "Open run") : null;
  const run = canTest(w) ? el("button", {
      type: "button", class: "btn small", "data-focus": "ov:test:" + i, "aria-label": "Run test: " + c.name,
      "aria-disabled": testsOf(w.name).starting || roundActive(w.name) ? "true" : "false",
      title: "Runs the saved file. Calls the models for real and sends no requests.",
      onclick: () => { if (!(testsOf(w.name).starting || roundActive(w.name))) startTests(w.name, i); },
    }, icon("run", 11), "Run") : null;
  const checks = finished ? finished.checks || [] : [];
  return el("li", { class: "tcase " + (verdict ? rowTone(verdict) : "") },
    el("div", { class: "thead" }, statusMark(V ? V.mark : "pending", 12), el("span", { class: "tname" }, c.name), run),
    el("div", { class: "tfix mono" }, caseInput(w, c)),
    el("div", { class: "tlast" }, line, open ? [" · ", open] : null),
    finished && verdict === "unmet" ? el("div", { class: "tlast bad" }, "Not met: " + unmetOf(finished).map((x) => x.key).join(", ")) : null,
    el("ul", { class: "expects", "aria-label": "Expectations of " + c.name }, (c.expect || []).map((e) => {
      const got = checks.find((x) => x.key === e.key);
      const isStep = w.steps.some((s) => s.id === e.key);
      const want = isStep && typeof e.want === "string" ? e.want : JSON.stringify(e.want);
      return el("li", { class: got ? (got.failure ? "failed" : "ok") : "" },
        statusMark(got ? (got.failure ? "failed" : "ok") : "pending", 9),
        el("span", { class: "ek mono" }, got ? el("span", { class: "sr" }, got.failure ? "Not met: " : "Met: ") : null, e.key),
        el("span", { class: "ew mono" }, want));
    })),
    canAddTest(w) ? removeTestButton(w, c, i) : null);
}

// ---- writing tests: from a run, or from nothing ----
//
// The form for a new test is a long-lived piece of the page, like the one for a
// new step: what has been typed lives in `keeps` and in the controls built
// once, the poll only brings them up to date (syncTester), and a trip to
// another step or run leaves it as it was. One form is kept per workflow and
// source (a run, or "-" for nothing), and thrown away when the test is saved.

const keeps = {};       // "workflow|source" -> the test being written
let tform = null;       // the entry of `keeps` now on the page
const canAddTest = (w) => !!w && state.editable && !w.problem && !w.component;
const testMode = (w) => canAddTest(w) && !!selected.test;
const keepKey = (name, source) => name + "|" + source;
const keepFor = (w) => (selected.test && w ? keeps[keepKey(w.name, selected.test)] || null : null);
// An ordinary run that succeeded. A test's own run is not offered: its test and
// its fixture are already files, and keeping it would only copy them.
const canKeep = (w, run) => canAddTest(w) && !!run && !isTestRun(run) && run.status === "ok";

function openKeep(runId) { go({ test: runId, node: null }); }
function openNewTest() { go({ test: "-", run: null, node: null }); }

function keepOffer(run) {
  return el("div", { class: "keepoffer" },
    el("button", { type: "button", class: "btn primary", "data-focus": "keep", onclick: () => openKeep(run.run) }, icon("plus", 14), "Keep as a test"),
    el("p", { class: "muted small" }, "Turns this request, and what the workflow did with it, into a test, so that a later change that alters it is caught."));
}

function newTestButton() {
  return el("button", { type: "button", class: "btn", "data-focus": "new-test", onclick: () => openNewTest() }, icon("plus", 14), "New test");
}

// ---- typed values ----
//
// A wanted value goes out as true or false, a number, or text, and the rule is
// the same wherever it came from: read what is in the box. true and false are
// yes and no, what is written as a JSON number is a number, anything else is
// text. Quotes round the whole value make it text, so "200" is the text 200.
// What will be saved is shown under the box. A step is wanted as called or
// skipped and is chosen, not typed.

const NUMBER = /^-?(0|[1-9]\d*)(\.\d+)?([eE][+-]?\d+)?$/;

function parseWant(text) {
  const t = text.trim();
  if (t === "") return { error: true };
  if (t === "true" || t === "false") return { value: t === "true", type: "true or false", cls: "boolean" };
  if (NUMBER.test(t) && Number.isFinite(Number(t))) return { value: Number(t), type: "number", cls: "number" };
  if (t.length >= 2 && t[0] === '"' && t[t.length - 1] === '"') {
    let v;
    try { v = JSON.parse(t); } catch (err) { v = t.slice(1, -1); }
    if (typeof v === "string") return { value: v, type: "text", cls: "text" };
  }
  return { value: text, type: "text", cls: "text" };
}

// valueText is what a value from the server looks like in the box: the same
// value comes back out of parseWant, so an untouched value keeps its type.
function valueText(v) {
  if (typeof v !== "string") return typeof v === "number" || typeof v === "boolean" ? String(v) : JSON.stringify(v);
  const p = parseWant(v);
  return p.value === v && p.cls === "text" ? v : JSON.stringify(v);
}

const clip = (text, n) => (text.length > n ? text.slice(0, n) + "…" : text);
const isStepKey = (w, key) => w.steps.some((s) => s.id === key);
const isBlankRow = (r) => r.hand && !r.key.trim() && !r.text.trim();

function parseFixture(text) {
  try { return { value: JSON.parse(text) }; } catch (err) { return { error: String(err.message || err) }; }
}

// startFixture is what a test from nothing starts with: a request the workflow
// is known to take, when it has one.
function startFixture(w) {
  if (!isWebhook(w)) return {};
  return { body: w.sample !== undefined ? w.sample : {} };
}

// keySuggestions are the keys worth offering for an expectation: each step, and
// what each puts out.
function keySuggestions(w) {
  const out = [];
  for (const s of w.steps) {
    out.push(s.id);
    for (const o of s.outputs || []) out.push(s.id + "." + o);
  }
  if (w.respond) out.push("response.status");
  return out;
}

// ---- the form's data ----

function keepStart(w, source) {
  const key = keepKey(w.name, source);
  if (keeps[key]) return keeps[key];
  const td = keeps[key] = {
    key, workflow: w.name, source, state: "ready", error: "", name: "", items: [], fixtureText: "",
    touched: false, seq: 0, built: false, shown: "", root: el("section", { class: "editform testform" }),
  };
  if (source === "-") td.fixtureText = JSON.stringify(startFixture(w), null, 2);
  else { td.state = "loading"; loadKeepDraft(td); }
  return td;
}

// loadKeepDraft asks the server what a run would make of a test. Nothing is written.
async function loadKeepDraft(td) {
  td.state = "loading";
  td.error = "";
  td.built = false;
  let r = null;
  try { r = await request("POST", "/api/cases", { workflow: td.workflow, draft: td.source }); } catch (err) { r = null; }
  if (keeps[td.key] !== td) return;
  if (r && r.ok && r.body.draft) {
    const d = r.body.draft;
    td.fixtureText = JSON.stringify(d.fixture === undefined ? {} : d.fixture, null, 2);
    td.items = (d.expect || []).map((e) => ({ id: ++td.seq, key: e.key, text: valueText(e.want), keep: e.keep !== false, hand: false, advice: e.keep !== false, wasStep: false }));
    td.state = "ready";
  } else {
    td.state = "error";
    td.error = r ? r.body.error || "The server answered " + r.status + "." : "Could not reach the server.";
  }
  td.built = false;
  uiTick++;
  shown = "";
  render();
}

// keepIssues lists what stops the test from being added, with the control it is about.
function keepIssues(w, td) {
  const out = [];
  if (!td.name.trim()) out.push({ message: "Give the test a name.", at: td.nameBox });
  const counted = td.items.filter((r) => r.keep && !isBlankRow(r));
  const seen = new Set();
  for (const r of counted) {
    const key = r.key.trim(), E = r.els;
    if (!key) { out.push({ message: "An expectation needs a key: a step id or an expression.", at: E && E.key }); continue; }
    if (seen.has(key)) out.push({ message: key + " is expected twice. Keep one.", at: E && (E.key || E.check) });
    seen.add(key);
    if (!isStepKey(w, key) && parseWant(r.text).error) out.push({ message: key + ": give the wanted value, or \"\" for empty text.", at: E && E.box });
  }
  if (!counted.length) out.push({ message: "Tick or add at least one expectation.", at: td.addBtn });
  const f = parseFixture(td.fixtureText);
  if (f.error) out.push({ message: "The fixture is not valid JSON: " + f.error, at: td.fixBox });
  return out;
}

// ---- the form on the page ----

function hideTester() {
  if (!tform) return;
  tform = null;
  insp.formslot.replaceChildren();
}

function showTester(w) {
  const td = keepStart(w, selected.test);
  const fresh = tform !== td;
  if (fresh) {
    tform = td;
    // What the last save said is not news in a new form.
    const st = uiOf(w.name);
    if (st.notice && (st.notice.kind === "saved" || st.notice.local)) st.notice = null;
  }
  paintTester(w, td);
  if (td.root.parentNode !== insp.formslot) {
    insp.formslot.replaceChildren(td.root);
    afterRender = focusTester;
  }
}

function focusTester() {
  const td = tform;
  if (!td) return;
  const t = td.state === "ready" && td.nameBox ? td.nameBox : insp.head.querySelector(".x");
  if (t) t.focus({ preventScroll: false });
}

function paintTester(w, td) {
  if (td.state !== "ready") {
    const sig = td.state + "|" + td.error;
    if (td.shown !== sig) {
      td.shown = sig;
      td.root.replaceChildren(td.state === "loading"
        ? el("p", { class: "muted", role: "status" }, "Reading the run…")
        : el("div", { class: "emptystate" },
            el("div", { class: "etitle" }, "This run cannot become a test"),
            el("p", {}, td.error),
            el("p", {},
              el("button", { type: "button", class: "linkbtn", onclick: () => { loadKeepDraft(td); } }, "Try again"), " · ",
              el("button", { type: "button", class: "linkbtn", onclick: () => go({ test: null }) }, "Close"))));
    }
    return;
  }
  if (!td.built) {
    const wasLoading = td.shown.startsWith("loading");
    buildTester(w, td);
    td.built = true;
    td.shown = "ready";
    // The form has just replaced "Reading the run": the person goes on from the name.
    if (wasLoading) afterRender = () => { const a = document.activeElement; if (!a || a === document.body || a.closest(".inspector header")) td.nameBox.focus({ preventScroll: true }); };
  }
  syncTester(w, td);
}

// touch notes that the person has changed something.
function touch(td) {
  td.touched = true;
  const st = uiOf(td.workflow);
  if (st.notice && st.notice.local) st.notice = null;
  paintCount(td);
  updateBar();
}

function paintCount(td) {
  const n = td.items.filter((r) => r.keep && !isBlankRow(r)).length;
  td.count.textContent = td.items.length ? n + " of " + plural(td.items.filter((r) => !isBlankRow(r)).length, "expectation") + " will be saved" : "None yet";
}

function buildTester(w, td) {
  const uid = ++formId;
  td.uid = uid;
  const fromRun = td.source !== "-";

  const nameId = "tn" + uid;
  td.nameBox = el("input", { id: nameId, type: "text", class: "in", spellcheck: "false", autocomplete: "off", "aria-describedby": nameId + "h" });
  td.nameBox.value = td.name;
  td.nameBox.addEventListener("input", () => { td.name = td.nameBox.value; touch(td); });
  const nameField = el("div", { class: "field" },
    el("div", { class: "lab" }, el("label", { for: nameId }, "Test name")),
    td.nameBox,
    el("p", { class: "hint-text", id: nameId + "h" }, "Say what the workflow should do with this request. The name also names the fixture file."));

  td.list = el("ul", { class: "xlist", "aria-label": "Expectations" });
  td.count = el("span", { class: "xcount", role: "status" });
  td.keys = el("datalist", { id: "tk" + uid });
  td.addBtn = el("button", { type: "button", class: "btn small", "data-focus": "x-add" }, icon("plus", 12), "Add an expectation");
  td.addBtn.addEventListener("click", () => {
    const row = { id: ++td.seq, key: "", text: "", keep: true, hand: true, advice: true, wasStep: false };
    td.items.push(row);
    td.list.append(buildRow(td, row));
    paintX(td, row);
    touch(td);
    row.els.key.focus();
  });
  td.items.forEach((row) => td.list.append(buildRow(td, row)));

  const xhead = el("div", { class: "xhead" }, el("h2", {}, "Expectations"), td.count);
  const lede = el("p", { class: "hint-text xlede" }, fromRun
    ? "What this run did. Tick what the workflow must keep doing; what is left unticked is not checked."
    : "What the workflow should do with this request: which steps run, what they decide, what it answers.");
  const help = el("div", { class: "xhelp" },
    el("p", {}, el("strong", {}, "Key"), " is a step id, or an expression over ", mono("trigger"), ", the steps and ", mono("response"), ", such as ", mono("classify.category.value"), " or ", mono("response.status"), "."),
    el("p", {}, el("strong", {}, "Value"), " is read as typed: ", mono("true"), " or ", mono("false"), " is yes or no, a plain number is a number, anything else is text. Put quotes round text that looks like one of those: ", mono("\"200\""), " is the text 200."));

  const fixId = "tf" + uid;
  td.fixBox = el("textarea", { id: fixId, class: "in mono prompt fixbox", rows: 12, spellcheck: "false", autocomplete: "off", "aria-describedby": fixId + "e" });
  td.fixBox.value = td.fixtureText;
  td.fixErr = el("p", { class: "conflict bad", id: fixId + "e", "aria-live": "polite", hidden: true });
  td.fixField = el("div", { class: "field" },
    el("div", { class: "lab" }, el("label", { for: fixId }, "Fixture"), el("span", { class: "tag", title: "Edited as JSON" }, "JSON")),
    td.fixBox, td.fixErr);
  td.fixBox.addEventListener("input", () => { td.fixtureText = td.fixBox.value; touch(td); paintFixture(td); });
  const folder = folderOf(w.path) + "/fixtures/";
  const note = el("div", { class: "notice warn fixnote" },
    el("div", { class: "nhead" }, el("strong", {}, fromRun ? "This is the real request" : "Saved to a file in your repository")),
    el("p", {}, "Saving writes it to a file in ", mono(folder), ", which belongs in git. Check it for personal data first, and edit it here if it has any."));

  td.root.replaceChildren(nameField, xhead, lede, td.list, el("div", { class: "xadd" }, td.addBtn), td.keys, help,
    el("h2", { class: "sub" }, "Fixture"), note, td.fixField);
  paintFixture(td);
  td.keySig = "";
  td.stepSig = null;
}

function paintFixture(td) {
  const f = parseFixture(td.fixtureText);
  td.fixErr.hidden = !f.error;
  td.fixErr.textContent = f.error ? "Not valid JSON: " + f.error : "";
  td.fixField.classList.toggle("invalid", !!f.error);
}

// buildRow is one expectation: a tick, its key, and what is wanted. A row from
// the run has its key fixed; one added by hand has a box for the key and a
// button to take it away again.
function buildRow(td, row) {
  const id = "x" + td.uid + "r" + row.id;
  const E = row.els = {};
  E.check = el("input", { type: "checkbox", id: id + "c", "aria-label": row.hand ? "Include this expectation" : null });
  E.check.checked = row.keep;
  E.check.addEventListener("change", () => { row.keep = E.check.checked; touch(td); paintX(td, row); });
  if (row.hand) {
    E.key = el("input", { type: "text", id: id + "k", class: "in mono", list: "tk" + td.uid, spellcheck: "false", autocomplete: "off", autocapitalize: "off", placeholder: "step id or expression", "aria-label": "Key of this expectation" });
    E.key.value = row.key;
    E.key.addEventListener("input", () => { row.key = E.key.value; touch(td); paintX(td, row); });
    E.del = el("button", { type: "button", class: "x small", "aria-label": "Remove this expectation", title: "Remove" }, icon("close", 13));
    E.del.addEventListener("click", () => {
      td.items.splice(td.items.indexOf(row), 1);
      row.el.remove();
      touch(td);
      td.addBtn.focus();
    });
  } else {
    E.label = el("label", { for: id + "c", class: "xkey mono" }, row.key);
  }
  const what = row.hand ? "Wanted value" : "Wanted value of " + row.key;
  const long = row.text.length > 56 || row.text.includes("\n");
  E.box = el(long ? "textarea" : "input", { id: id + "v", class: "in mono", spellcheck: "false", autocomplete: "off", autocapitalize: "off", "aria-label": what, rows: long ? 2 : null, type: long ? null : "text", placeholder: row.hand ? "true, 200, or text" : null });
  E.box.value = row.text;
  E.box.addEventListener("input", () => { row.text = E.box.value; autosize(E.box); touch(td); paintX(td, row); });
  E.sel = el("select", { id: id + "s", class: "in", "aria-label": what }, el("option", { value: "called" }, "called"), el("option", { value: "skipped" }, "skipped"));
  E.sel.addEventListener("change", () => { row.text = E.sel.value; touch(td); paintX(td, row); });
  E.clip = el("div", { class: "xclip mono" });
  E.typed = el("p", { class: "typed" });
  E.why = el("p", { class: "xwhy" });
  row.el = el("li", { class: "xrow" + (row.hand ? " hand" : "") },
    E.check, row.hand ? E.key : E.label, E.del,
    el("div", { class: "xbody" }, E.clip, E.box, E.sel, E.typed, E.why));
  return row.el;
}

// paintX brings one row up to date: what is shown depends on whether it is
// ticked and on whether its key is a step.
function paintX(td, row) {
  const w = workflowByName(td.workflow);
  if (!w || !row.els) return;
  const E = row.els, step = isStepKey(w, row.key.trim()) && !!row.key.trim();
  row.el.classList.toggle("on", row.keep);
  if (E.check.checked !== row.keep) E.check.checked = row.keep;
  // A key that stopped being a step leaves the value it was given as a step.
  if (row.hand && row.wasStep && !step) row.text = "";
  row.wasStep = step;
  if (step && row.text !== "called" && row.text !== "skipped") row.text = "called";
  E.clip.hidden = row.keep;
  E.box.hidden = !row.keep || step;
  E.sel.hidden = !row.keep || !step;
  E.typed.hidden = !row.keep || step;
  E.clip.textContent = row.text.replace(/\s+/g, " ");
  if (step) E.sel.value = row.text;
  else {
    setValue(E.box, row.text);
    if (row.keep) autosize(E.box);
  }
  if (row.keep && !step) {
    const p = parseWant(row.text);
    E.typed.className = "typed" + (p.error ? " bad" : "");
    E.typed.replaceChildren(...(p.error ? ["Give a value, or ", mono("\"\""), " for empty text."]
      : ["Saved as ", el("span", { class: "vt " + p.cls }, p.type), mono(clip(JSON.stringify(p.value), 90))]));
  }
  // The server's advice, in a few words: this is text a model wrote.
  E.why.hidden = row.hand || row.advice;
  if (!E.why.hidden) {
    E.why.className = "xwhy" + (row.keep ? " warn" : "");
    E.why.replaceChildren(el("span", { class: "vt model" }, "Model text"),
      row.keep ? " A model wrote this. The test fails whenever it words it differently." : " Left out: a model wrote it, so it reads differently each time.");
  }
}

// syncTester brings the form up to date with the workflow behind it, without
// touching a control whose text already matches.
function syncTester(w, td) {
  const keys = keySuggestions(w).join("\u0001");
  if (keys !== td.keySig) {
    td.keySig = keys;
    td.keys.replaceChildren(...keySuggestions(w).map((k) => el("option", { value: k })));
  }
  const steps = w.steps.map((s) => s.id).join(",");
  if (steps !== td.stepSig) {
    td.stepSig = steps;
    td.items.forEach((row) => paintX(td, row));
  }
  paintCount(td);
}

// ---- saving ----

async function addTest() {
  if (!view || !state.editable) return;
  const name = view.w.name, w = workflowByName(name), st = uiOf(name);
  const td = keepFor(w);
  if (!w || w.problem || st.saving || !td || td.state !== "ready") return;
  const issues = keepIssues(w, td);
  if (issues.length) {
    // Caught here, before anything is sent.
    st.notice = { kind: "invalid", did: "test", local: true, problems: issues.map((i) => ({ message: i.message, plain: true })) };
    updateBar();
    if (issues[0].at) issues[0].at.focus();
    return;
  }
  const fixture = parseFixture(td.fixtureText).value;
  const expect = td.items.filter((r) => r.keep && !isBlankRow(r)).map((r) => {
    const key = r.key.trim();
    return { key, want: isStepKey(w, key) ? r.text : parseWant(r.text).value };
  });
  st.saving = true;
  st.notice = null;
  updateBar();
  let r = null, failure = null;
  try {
    r = await request("POST", "/api/cases", { workflow: name, rev: w.rev, add: { name: td.name.trim(), fixture, expect } });
  } catch (err) {
    failure = { kind: "network", did: "test" };
  }
  st.saving = false;
  if (!failure && r.ok && r.body.workflow) {
    epoch++;
    changesEpoch++;
    const prev = workflowByName(name);
    replaceWorkflow(r.body.workflow);
    known[name] = r.body.workflow.rev;
    reattach(name, prev, r.body.workflow);
    const next = r.body.workflow, cases = next.cases || [], index = cases.length - 1, made = cases[index];
    const path = next.path || w.path;
    delete keeps[td.key];
    st.notice = { kind: "saved", did: "test", title: "Test added: " + (made ? made.name : td.name.trim()), index,
      files: [path, made && made.fixture ? folderOf(path) + "/" + made.fixture : null].filter(Boolean) };
    afterRender = () => { const b = document.querySelector('[data-focus="notice-run"]'); if (b) b.focus({ preventScroll: true }); };
    go({ test: null });
    readAddress();
  } else {
    st.notice = failure || failureOf(r, "workflow", { did: "test" });
  }
  await refresh();
  uiTick++;
  shown = "";
  render();
}

// testNext is what to do with a test that has just been written: run it, or look at it.
function testNext(n, w) {
  const run = el("button", { type: "button", class: "btn small", "data-focus": "notice-run", title: "Runs the saved file. Calls the models for real and sends no requests." }, icon("run", 11), "Run this test");
  run.addEventListener("click", () => {
    const now = workflowByName(w.name);
    if (now && !(testsOf(now.name).starting || roundActive(now.name))) startTests(now.name, n.index);
  });
  const show = el("button", { type: "button", class: "linkbtn" }, "Show it in Tests");
  show.addEventListener("click", () => showTestCard(w.name, n.index));
  return el("div", { class: "nextrow" }, run, show);
}

// showTestCard goes to the tests of the workflow, with the new one in view.
function showTestCard(name, index) {
  afterRender = () => {
    const b = document.querySelector('[data-focus="ov:test:' + index + '"]');
    if (b) { b.scrollIntoView({ block: "center" }); b.focus({ preventScroll: true }); }
  };
  go({ workflow: name, run: null, node: null });
  readAddress();
  uiTick++;
  shown = "";
  render();
}

// removeTestButton asks twice, like removing a step: the first click arms it.
function removeTestButton(w, c, i) {
  const tag = "test:" + i;
  const b = el("button", { type: "button", class: "btn small danger rmtest", "data-focus": "rm:" + tag, "aria-label": "Remove test: " + c.name }, icon("trash", 12), "Remove");
  const note = el("p", { class: "rmnote", hidden: true }, "Takes the test out of " + w.path + ". Its fixture file ", mono(c.fixture), " stays in the folder.");
  const busy = () => testsOf(w.name).starting || roundActive(w.name);
  const paint = () => {
    const armed = uiOf(w.name).removeArmed === tag;
    b.classList.toggle("armed", armed);
    b.setAttribute("aria-disabled", busy() ? "true" : "false");
    b.replaceChildren(icon("trash", 12), armed ? "Remove this test? Click again" : "Remove");
    note.hidden = !armed;
  };
  paint();
  b.addEventListener("click", () => {
    const st = uiOf(w.name);
    if (st.saving || busy()) return;
    if (st.removeArmed !== tag) {
      st.removeArmed = tag;
      clearTimeout(st.removeTimer);
      st.removeTimer = setTimeout(() => { if (st.removeArmed === tag) { st.removeArmed = null; paint(); } }, 4000);
      paint();
      return;
    }
    clearTimeout(st.removeTimer);
    st.removeArmed = null;
    paint();
    removeTest(w.name, i, c);
  });
  b.addEventListener("blur", () => {
    const st = uiOf(w.name);
    if (st.removeArmed === tag) { st.removeArmed = null; clearTimeout(st.removeTimer); paint(); }
  });
  return el("div", { class: "trow" }, b, note);
}

async function removeTest(name, index, c) {
  const w = workflowByName(name), st = uiOf(name);
  if (!w || w.problem || st.saving) return;
  st.saving = true;
  st.notice = null;
  updateBar();
  let r = null, failure = null;
  try {
    r = await request("POST", "/api/cases", { workflow: name, rev: w.rev, remove: index });
  } catch (err) {
    failure = { kind: "network", did: "untest" };
  }
  st.saving = false;
  if (!failure && r.ok && r.body.workflow) {
    epoch++;
    changesEpoch++;
    const prev = workflowByName(name);
    replaceWorkflow(r.body.workflow);
    known[name] = r.body.workflow.rev;
    reattach(name, prev, r.body.workflow);
    const path = r.body.workflow.path || w.path;
    st.notice = { kind: "saved", did: "untest", title: "Removed test: " + c.name, files: [path], fixture: folderOf(path) + "/" + c.fixture };
  } else {
    st.notice = failure || failureOf(r, "workflow", { did: "untest" });
  }
  await refresh();
  uiTick++;
  shown = "";
  render();
}

// ---- the changes view ----

function ensureChanges(w) {
  const stamp = [state.commit, w.rev, changesEpoch].join("|");
  const c = changesCache[w.name];
  if (c && (c.stamp === stamp || c.inflight)) return;
  changesCache[w.name] = { ...(c || { state: "loading" }), inflight: true };
  const done = (next) => {
    changesCache[w.name] = { ...next, stamp };
    shown = "";
    render();
  };
  request("GET", "/api/changes?workflow=" + encodeURIComponent(w.name))
    .then((r) => done(r.ok ? { state: "ready", data: r.body } : { state: "error", error: r.body.error || "the server answered " + r.status }))
    .catch(() => done({ state: "error", error: "the server cannot be reached" }));
}

const CHANGE_WORD = { modified: "Modified", new: "New", deleted: "Deleted", renamed: "Renamed" };

// diffLines cuts the diff into lines and tells what each one is.
function diffLines(text) {
  const out = [];
  let body = false;
  for (const line of text.replace(/\n$/, "").split("\n")) {
    let kind = "ctx";
    if (line.startsWith("diff --git")) { body = false; kind = "meta"; }
    else if (line.startsWith("@@")) { body = true; kind = "hunk"; }
    else if (!body) kind = "meta";
    else if (line.startsWith("+")) kind = "add";
    else if (line.startsWith("-")) kind = "del";
    out.push([kind, line]);
  }
  return out;
}

function renderChanges(w) {
  const c = changesCache[w.name] || { state: "loading" };
  const lede = el("p", { class: "lede" }, "These changes are saved in the file but not committed. Commit them with git when you are happy with them.");
  let content;
  if (c.state === "error") {
    content = [el("p", { class: "empty" }, "Could not read the changes: " + c.error + ". ",
      el("button", { type: "button", class: "linkbtn", onclick: () => { changesCache[w.name] = { state: "loading" }; shown = ""; render(); } }, "Try again"))];
  } else if (c.state === "loading" || !c.data) {
    content = [el("p", { class: "empty" }, "Reading the changes…")];
  } else if (c.data.git === false) {
    content = [el("p", { class: "empty" }, "This folder is not in a git repository, so there is no diff to show. Edits are still written to the file.")];
  } else if (!c.data.files || !c.data.files.length) {
    content = [el("p", { class: "empty" }, "No uncommitted changes in this workflow's files. What the page shows is what the last commit says.")];
  } else {
    const lines = diffLines(c.data.diff || "");
    const added = lines.filter((l) => l[0] === "add").length, removed = lines.filter((l) => l[0] === "del").length;
    content = [
      el("div", { class: "files-head" },
        el("h2", {}, plural(c.data.files.length, "file"), " changed"),
        el("span", { class: "tally" }, el("b", { class: "plus" }, "+" + added), el("b", { class: "minus" }, "−" + removed))),
      el("ul", { class: "files" }, c.data.files.map((f) =>
        el("li", {}, el("span", { class: "badge " + f.change }, CHANGE_WORD[f.change] || f.change), el("span", { class: "mono path" }, f.path)))),
      c.data.diff ? el("pre", { class: "diff", tabindex: "0", "aria-label": "Diff of the changed files" },
        lines.map(([kind, line]) => el("span", { class: "dl " + kind }, line + "\n"))) : null,
    ];
  }
  $("canvas").replaceChildren(...[
    problemBanner(w),
    el("div", { class: "hint changesbar" },
      el("strong", {}, "Changes in " + w.name),
      el("span", {}, "as git sees them"),
      el("button", { type: "button", class: "ghost", "data-focus": "close-changes", onclick: () => go({ changes: false }) }, "Back to the drawing", el("kbd", {}, "Esc"))),
    el("div", { class: "scroller" }, el("div", { class: "changes-wrap" }, el("div", { class: "changes" }, lede, content))),
  ].filter(Boolean));
}

// ---- the node editor ----
//
// A large dialog over the dimmed drawing, for one step: what the step can read
// on the left, its fields laid out by meaning in the middle, what it put out on
// the right. It is the address /e/<step>. It keeps no copy of the file: what is
// typed goes into `drafts` through the same controls as the details column
// (makeControl), so the two always agree, and its footer is the save bar of the
// page in another place. Only a step of a workflow opens it, on a server that
// edits files; a component's own steps do not.

const WHOLE_REF = /^\$\{\s*([^{}]+?)\s*\}$/;
const SOURCE_WORD = { trigger: "the request", decide: "decided", ai: "wrote", http: "sent", flow: "ran" };

let ed = null;          // the editor now on the page, if any
let dragRef = null;     // the key being dragged: { path, source }
const stepTests = {};   // "workflow|step" -> { state: running | done | error, run, at, rev, unsaved, result, error }

const canOpen = (w, id) => !!w && !!state.editable && !w.problem && !w.draft && !isComp(w) && w.steps.some((s) => s.id === id);
function openEditor(id) { if (view && canOpen(view.w, id)) go({ node: id, edit: id }); }
// Closing the editor leaves the step too: it has no other view to fall back to.
// A save the editor already said was done is not said again behind it.
function closeEditor() {
  if (ed) {
    const st = uiOf(ed.workflow);
    if (st.notice && st.notice.kind === "saved") st.notice = null;
  }
  go({ edit: null, node: null });
}
// editing is the step the editor is open on: the one the address names, or a
// step picked on the drawing, in a run or from a link, when it can be edited.
const editing = (w) => (!w ? null : selected.edit || (selected.node && canOpen(w, selected.node) ? selected.node : null));

const curOf = () => {
  const w = ed ? workflowByName(ed.workflow) : null;
  return { w, step: w ? w.steps.find((s) => s.id === ed.step) || null : null };
};
// The run the editor shows: the one picked, else the latest of the workflow.
const runShown = () => (view ? view.run || view.auto : null);
const testKey = () => ed.workflow + "|" + ed.step;

function aboutStep(step) {
  if (step.kind === "decide") return "Asks a decision model typed questions and gets probabilities back";
  if (step.kind === "ai") return "Asks a model to write structured output";
  if (step.kind === "flow") return "Runs the " + (step.component || "") + " component, made of " + plural((step.inner || []).length, "step");
  if (step.kind === "http") return step.component ? "Sends a request, as the " + step.component + " component describes" : "Sends an HTTP request";
  return KIND_NAMES[step.kind] || step.kind;
}

function ago(at) {
  const s = Math.round((Date.now() - at) / 1000);
  return s < 10 ? "just now" : s < 60 ? s + " s ago" : when(at);
}

const sourceKind = (w, id) => (id === "trigger" ? "trigger" : id === "env" ? "env" : (w.steps.find((s) => s.id === id) || {}).kind || "env");

function seg(label, items, current, pick) {
  return el("div", { class: "seg", role: "group", "aria-label": label }, items.map(([value, text]) => {
    const b = el("button", { type: "button", "aria-pressed": current === value ? "true" : "false" }, text);
    b.addEventListener("click", () => pick(value));
    return b;
  }));
}

function focusables(root) {
  return [...root.querySelectorAll("a[href], button, input, select, textarea, [tabindex]")]
    .filter((n) => !n.disabled && n.getAttribute("tabindex") !== "-1" && !n.closest("[hidden]") && n.getClientRects().length > 0);
}

function sideColumn(which, title) {
  const c = { which };
  c.meta = el("span", { class: "ned-meta" });
  c.seg = el("span", { class: "ned-seg" });
  c.body = el("div", { class: "ned-colbody" });
  c.note = el("div", { class: "ned-note", role: "status" });
  c.fold = el("button", { type: "button", class: "ned-fold", "aria-expanded": "true" }, icon("chev", 13), title);
  c.fold.addEventListener("click", () => {
    ed.fold[which] = !ed.fold[which];
    c.root.classList.toggle("folded", ed.fold[which]);
    c.fold.setAttribute("aria-expanded", ed.fold[which] ? "false" : "true");
  });
  c.root = el("div", { class: "ned-col ned-" + which },
    el("div", { class: "ned-colhead" }, el("span", { class: "ned-cap" }, title), c.fold, c.meta, el("span", { class: "grow" }), c.seg),
    el("div", { class: "ned-colfold" }, c.body, c.note));
  const folded = !!ed.fold[which];
  c.root.classList.toggle("folded", folded);
  c.fold.setAttribute("aria-expanded", folded ? "false" : "true");
  return c;
}

function showEditor(w, step) {
  hideEditor(true);
  const narrow = window.innerWidth <= 900;
  ed = { workflow: w.name, step: step.id, tab: "params", inMode: "schema", outMode: "fields", outSource: null, target: null, form: null, formKey: "", sig: "", diff: false, more: {}, fold: { in: narrow, out: narrow }, shownNotice: null };

  ed.testBtn = el("button", { type: "button", class: "btn primary ned-testbtn", "data-test": "1" }, icon("run", 13), "Test step");
  ed.testBtn.addEventListener("click", () => testStep());
  const close = el("button", { type: "button", class: "ned-x", "aria-label": "Close the editor", title: "Close (Esc)" }, icon("close", 16));
  close.addEventListener("click", () => closeEditor());
  ed.line = el("span", { class: "ned-line" });
  const head = el("header", { class: "ned-head kind-" + step.kind },
    kindTile(step.kind, 42),
    el("div", { class: "ned-names" },
      el("div", { class: "ned-title" }, el("h1", { class: "mono ned-id" }, step.id), el("span", { class: "ned-badge" }, KIND_LABEL[step.kind] || step.kind)),
      ed.line),
    el("span", { class: "grow" }), ed.testBtn, close);

  ed.inCol = sideColumn("in", "Input");
  ed.outCol = sideColumn("out", "Output");

  ed.tabBtns = {};
  const tabs = el("div", { class: "ned-tabs", role: "tablist", "aria-label": "Editing " + step.id });
  for (const [name, label] of [["params", "Parameters"], ["settings", "Settings"]]) {
    const b = el("button", { type: "button", role: "tab", id: "nedtab-" + name, "aria-controls": "nedpanel-" + name }, label);
    b.addEventListener("click", () => setTab(name, true));
    b.addEventListener("keydown", (e) => {
      if (e.key !== "ArrowRight" && e.key !== "ArrowLeft") return;
      e.preventDefault();
      setTab(name === "params" ? "settings" : "params", true);
    });
    ed.tabBtns[name] = b;
    tabs.append(b);
  }
  ed.panels = {
    params: el("div", { class: "ned-panel", id: "nedpanel-params", role: "tabpanel", "aria-labelledby": "nedtab-params" }),
    settings: el("div", { class: "ned-panel", id: "nedpanel-settings", role: "tabpanel", "aria-labelledby": "nedtab-settings" }),
  };
  ed.midBody = el("div", { class: "ned-midbody" }, ed.panels.params, ed.panels.settings);
  ed.midBody.addEventListener("focusin", (e) => {
    const c = ed.form && ed.form.controls.find((x) => x.refable && x.row.contains(e.target));
    if (c) ed.target = c;
  });
  const mid = el("div", { class: "ned-col ned-mid" }, tabs, ed.midBody);

  ed.foot = buildFoot(w, step);
  ed.dlg = el("section", { class: "nodeed", role: "dialog", "aria-modal": "true", "aria-label": "Editing " + step.id, tabindex: "-1" },
    head, el("div", { class: "ned-cols" }, ed.inCol.root, mid, ed.outCol.root), ed.foot.root);
  ed.dlg.addEventListener("keydown", (e) => {
    if (e.key !== "Tab") return;
    const items = focusables(ed.dlg);
    if (!items.length) { e.preventDefault(); return; }
    const first = items[0], last = items[items.length - 1];
    if (e.shiftKey && (document.activeElement === first || document.activeElement === ed.dlg)) { e.preventDefault(); last.focus(); }
    else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
  });
  // A text box is as tall as its text, which depends on how wide the column is.
  ed.watch = new ResizeObserver(() => { if (ed && ed.form) ed.form.controls.forEach((c) => { if (c.input.getClientRects().length) autosize(c.input); }); });
  ed.watch.observe(ed.midBody);
  ed.root = el("div", { class: "nedback" }, ed.dlg);
  // The second click of the double-click that may have opened it does not close it.
  const opened = performance.now();
  ed.root.addEventListener("pointerdown", (e) => { if (e.target === ed.root && performance.now() - opened > 500) closeEditor(); });
  document.body.append(ed.root);
  document.body.classList.add("ned-open");
  $("app").setAttribute("inert", "");
  setTab("params", false);
  ed.dlg.focus({ preventScroll: true });
}

function hideEditor(keepFocus) {
  if (!ed) return;
  const id = ed.step;
  ed.watch.disconnect();
  ed.root.remove();
  document.body.classList.remove("ned-open");
  $("app").removeAttribute("inert");
  ed = null;
  dragRef = null;
  if (!keepFocus) {
    const card = document.querySelector('#canvas .card[data-node="' + CSS.escape(id) + '"]');
    if (card) card.focus({ preventScroll: true });
  }
}

// paintEditor is called by render(): it opens, updates or closes the editor to match the address.
function paintEditor() {
  const w = view && view.w;
  const id = editing(w);
  const step = id ? w.steps.find((s) => s.id === id) : null;
  if (!step || !canOpen(w, step.id)) {
    hideEditor();
    // An address that names a step that cannot be edited here goes back to the drawing.
    if (selected.edit && state.workflows.length) go({ edit: null });
    return;
  }
  if (!ed || ed.workflow !== w.name || ed.step !== step.id) showEditor(w, step);
  syncEditor(w, step);
}

function setTab(name, focus) {
  ed.tab = name;
  for (const [n, b] of Object.entries(ed.tabBtns)) {
    b.setAttribute("aria-selected", n === name ? "true" : "false");
    b.tabIndex = n === name ? 0 : -1;
    ed.panels[n].hidden = n !== name;
  }
  if (ed.form) ed.form.controls.forEach((c) => autosize(c.input));
  if (focus) ed.tabBtns[name].focus();
}

function formKeyOf(w, step) {
  return [w.name, step.id, step.edit.map((f) => [pathKey(f.path), f.kind, f.label, f.group || "", f.hint || "", (f.options || []).join(","), WHOLE_REF.test(f.value) ? "r" : ""].join("~")).join("|")].join("##");
}

function syncEditor(w, step) {
  const key = formKeyOf(w, step);
  if (key !== ed.formKey) rebuildForm(w, step, key);
  else { ed.form.fields = step.edit; syncForm(w, ed.form); }
  paintHead();
  paintSchema();
  // What it reads and what it gave only change when the run or the file does.
  const ran = runShown();
  const sig = JSON.stringify([ran ? [ran.run, ran.status, ran.ms, (ran.steps || []).length] : null, step, w.rev]);
  if (sig !== ed.sig) { ed.sig = sig; paintInput(); paintOutput(); }
  paintFoot();
}

// syncEditorValues brings the editor's controls up to date with the drafts after something else changed them.
function syncEditorValues() {
  if (!ed || !ed.form) return;
  const w = workflowByName(ed.workflow);
  if (w) syncForm(w, ed.form);
  paintHead();
  paintSchema();
}

function rebuildForm(w, step, key) {
  let back = null;
  if (ed.form && ed.midBody.contains(document.activeElement)) {
    const i = ed.form.controls.findIndex((c) => c.input === document.activeElement);
    if (i >= 0) { try { back = [i, document.activeElement.selectionStart, document.activeElement.selectionEnd]; } catch (err) { back = [i, null, null]; } }
  }
  ed.formKey = key;
  ed.target = null;
  ed.form = buildEditorForm(w, step, key);
  ed.panels.params.replaceChildren(...ed.form.params);
  ed.panels.settings.replaceChildren(...ed.form.settings);
  ed.schemaSig = "";
  syncForm(w, ed.form);
  paintSchema();
  if (back && ed.form.controls[back[0]]) {
    const box = ed.form.controls[back[0]].input;
    box.focus({ preventScroll: true });
    if (back[1] != null) { try { box.setSelectionRange(back[1], back[2]); } catch (err) { /* not a text box */ } }
  }
}

// ---- the middle: the fields by meaning ----

const curValue = (c) => { const d = draftOf(c); return d ? d.value : c.base; };

function relabel(c, text) {
  const label = c.row.querySelector("label");
  if (label) label.textContent = text;
}

// setField puts a text in a field as if it was typed.
function setField(c, text) {
  setValue(c.input, text);
  c.input.dispatchEvent(new Event("input", { bubbles: true }));
}

// A field that takes keys: the key dropped or clicked goes in at the caret, or
// takes the place of an empty value, or of the one key already there.
function putRef(c, ref) {
  const text = c.kind === "expression" ? ref.path : "${" + ref.path + "}";
  const box = c.input;
  if (box.hidden || box.value.trim() === "") setField(c, text);
  else {
    const live = document.activeElement === box;
    const from = live ? box.selectionStart : c.caret ? c.caret[0] : box.value.length;
    const to = live ? box.selectionEnd : c.caret ? c.caret[1] : from;
    box.setRangeText(text, from, to, "end");
    box.dispatchEvent(new Event("input", { bubbles: true }));
  }
  c.row.classList.add("flash");
  setTimeout(() => c.row.classList.remove("flash"), 900);
  announce("Put " + text + " in " + c.field.label);
}

function wireDrop(c) {
  const row = c.row;
  const accepts = () => !!dragRef && !!ed && dragRef.source !== ed.step;
  row.addEventListener("dragover", (e) => {
    if (!accepts()) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "copy";
    row.classList.add("drop");
  });
  row.addEventListener("dragleave", (e) => { if (!row.contains(e.relatedTarget)) row.classList.remove("drop"); });
  row.addEventListener("drop", (e) => {
    row.classList.remove("drop");
    if (!accepts()) return;
    e.preventDefault();
    putRef(c, dragRef);
    dragRef = null;
  });
  c.input.addEventListener("blur", () => { try { c.caret = [c.input.selectionStart, c.input.selectionEnd]; } catch (err) { c.caret = null; } });
}

// dataField gives a field that holds a key the Fixed / From data choice: a
// whole ${...} shows as a coloured pill, and Fixed shows the text itself.
function dataField(c) {
  c.data = true;
  const holder = el("div", { class: "ned-val" });
  c.pillbox = el("div", { class: "ned-slot", tabindex: "0", role: "group", "aria-label": c.field.label + ", from data" });
  c.input.replaceWith(holder);
  holder.append(c.input, c.pillbox);
  const pick = (mode) => {
    c.forced = mode;
    paintData(c);
    if (mode === "fixed") c.input.focus(); else c.pillbox.focus();
  };
  c.fixedBtn = el("button", { type: "button" }, "Fixed");
  c.dataBtn = el("button", { type: "button" }, "From data");
  c.fixedBtn.addEventListener("click", () => pick("fixed"));
  c.dataBtn.addEventListener("click", () => pick("data"));
  c.toggle = el("div", { class: "seg ned-kindseg", role: "group", "aria-label": c.field.label + " is" }, c.fixedBtn, c.dataBtn);
  c.row.querySelector(".lab").insertBefore(c.toggle, c.revert);
  c.after = paintData;
}

function paintData(c) {
  const v = curValue(c), m = WHOLE_REF.exec(v);
  const mode = c.forced || (m ? "data" : "fixed");
  c.row.classList.toggle("is-data", mode === "data");
  c.input.hidden = mode === "data";
  c.pillbox.hidden = mode !== "data";
  if (mode === "fixed") autosize(c.input);
  c.fixedBtn.setAttribute("aria-pressed", mode === "fixed" ? "true" : "false");
  c.dataBtn.setAttribute("aria-pressed", mode === "data" ? "true" : "false");
  if (mode !== "data") return;
  const w = workflowByName(c.workflow);
  if (m) {
    const path = m[1], kind = w ? sourceKind(w, path.split(".")[0]) : "env";
    const clear = el("button", { type: "button", class: "ned-unpill", "aria-label": "Remove " + path }, icon("close", 11));
    clear.addEventListener("click", () => { setField(c, ""); c.pillbox.focus(); });
    c.pillbox.replaceChildren(el("span", { class: "ref static kind-" + kind }, el("span", { class: "ned-brace", "aria-hidden": "true" }, "{ }"), path, clear));
    c.pillbox.setAttribute("aria-label", c.field.label + ", from data: " + path);
  } else {
    c.pillbox.replaceChildren(el("span", { class: "ned-empty" }, "Drop a key here, or click one on the left"),
      v.trim() ? el("span", { class: "ned-still" }, "Still the fixed text ", el("span", { class: "mono" }, clip(v, 40)), " until you do.") : null);
    c.pillbox.setAttribute("aria-label", c.field.label + ", from data, no key yet");
  }
}

function questionKind(q) {
  return q.answers.startsWith("score") ? "A score" : q.options && q.options.length ? "One of a list" : "Yes or no";
}

function questionCard(q, ask, meanings) {
  const scored = q.answers.startsWith("score");
  const rows = meanings.map((m) => {
    const c = m.c;
    c.row = el("div", { class: "ned-arow k-text" },
      el("label", { for: c.id, class: "mono ned-aname" }, m.name),
      c.input, c.revert, c.conflict);
    return c.row;
  });
  return el("div", { class: "ned-q" },
    el("div", { class: "ned-qhead" }, el("span", { class: "mono ned-qname" }, q.name), el("span", { class: "ned-qtype" }, questionKind(q))),
    ask ? ask.row : el("p", { class: "ned-plain" }, q.ask),
    rows.length ? el("div", { class: "ned-answers" },
      el("div", { class: "ned-ahead" }, el("span", {}, "Answer"), el("span", {}, scored ? "Means, lowest to highest" : "Means")),
      rows) : null);
}

// buildEditorForm makes one control per editable field of the step, in the
// order of step.edit (syncForm pairs them by position), and lays them out.
function buildEditorForm(w, step, key) {
  const f = { key, workflow: w.name, node: step.id, fields: step.edit, controls: [], root: ed.midBody };
  const cs = step.edit.map((field) => {
    const c = makeControl(f, field);
    c.refable = field.kind === "text" || field.kind === "expression";
    f.controls.push(c);
    return c;
  });
  const role = {}, qs = {}, rest = [];
  for (const c of cs) {
    const p = c.field.path, last = p[p.length - 1];
    if (p.length === 3 && last === "when") role.when = c;
    else if ((step.kind === "decide" || step.kind === "ai") && p.length === 4 && p[2] === step.kind && ["model", "input", "prompt", "output"].includes(last)) role[last] = c;
    else if (step.kind === "decide" && p[3] === "questions" && p.length >= 6) {
      const q = qs[p[4]] || (qs[p[4]] = { ask: null, meanings: [] });
      if (last === "ask") q.ask = c;
      else if (last === "yes_if" || last === "no_if") q.meanings.push({ c, name: last === "yes_if" ? "yes" : "no" });
      else q.meanings.push({ c, name: c.field.label });
    } else rest.push(c);
  }
  // Which fields hold keys: they get the Fixed / From data choice.
  const meaning = new Set(Object.values(qs).flatMap((q) => q.meanings.map((m) => m.c)));
  for (const c of cs) {
    if (!c.refable || c === role.when || meaning.has(c)) continue;
    const isInput = !c.field.group ? c.field.label === "Input" : c.field.group === "Input";
    if (isInput || WHOLE_REF.test(c.field.value)) dataField(c);
  }

  const params = [];
  if (role.model) { relabel(role.model, "Model"); params.push(role.model.row); }
  if (role.input) { relabel(role.input, step.kind === "decide" ? "What it reads" : "Input"); params.push(role.input.row); }
  if (role.prompt) params.push(role.prompt.row);
  // The fields the model answers with are edited here, as a table. Which file holds them is a setting.
  if (role.output) {
    f.schemaBox = el("div", { class: "ned-schema" });
    params.push(f.schemaBox);
  }
  if (step.kind === "decide" && step.questions && step.questions.length) {
    params.push(el("div", { class: "ned-sect" }, el("span", { class: "ned-h" }, "Questions"), el("span", { class: "ned-count" }, String(step.questions.length))));
    for (const q of step.questions) {
      const part = qs[q.name] || { ask: null, meanings: [] };
      if (part.ask) relabel(part.ask, "Ask");
      params.push(questionCard(q, part.ask, part.meanings));
    }
  }
  // Whatever else the step lets the page change keeps the order of the file, grouped as the details column does.
  let group = null, groupName = null;
  for (const c of rest) {
    if (c.field.group) {
      if (groupName !== c.field.group) {
        groupName = c.field.group;
        group = el("fieldset", { class: "group" }, el("legend", {}, c.field.group));
        params.push(group);
      }
      group.append(c.row);
    } else { groupName = null; params.push(c.row); }
  }
  if (!params.length) params.push(el("p", { class: "ned-plain" }, "This step has nothing the page can change. What it does is set in its component."));

  const settings = [];
  if (role.output) {
    relabel(role.output, "Output schema file");
    role.output.row.classList.add("ned-schemafile");
    settings.push(role.output.row, el("p", { class: "ned-words" }, "The JSON Schema file the model's answer must fit. Its fields are edited under Parameters."));
  }
  if (role.when) {
    relabel(role.when, "Run only when");
    const words = el("p", { class: "ned-words", "aria-live": "polite" });
    const paintWords = (c) => {
      const v = curValue(c).trim();
      put(words, v ? ["In words: ", el("strong", {}, readable(v))] : "Without a condition the step runs every time, once the steps before it have finished.");
    };
    role.when.after = paintWords;
    settings.push(role.when.row, words);
  } else settings.push(el("p", { class: "ned-plain" }, "This step has no settings the page can change."));
  settings.push(el("p", { class: "ned-plain" }, "Step " + (w.steps.indexOf(step) + 1) + " of " + w.steps.length + " in file order."));
  // Every field that takes text takes a key dropped on it, once the rows are laid out.
  for (const c of cs) if (c.refable) wireDrop(c);
  f.params = params;
  f.settings = settings;
  return f;
}

function paintHead() {
  const { w, step } = curOf();
  if (!w || !step) return;
  const c = ed.form && ed.form.controls.find((x) => x.field.path.length === 3 && x.field.path[2] === "when");
  const cond = c ? curValue(c).trim() : (step.when || "").trim();
  // What a component does is changed in the component: the way there.
  const comp = step.target ? entryOf(step.target) : null;
  const open = comp ? [" · ", el("button", { type: "button", class: "linkbtn", onclick: () => go({ workflow: comp.name, run: null, node: null }) }, "Open " + compTitle(comp))] : null;
  if (cond) {
    const more = el("button", { type: "button", class: "linkbtn" }, "set in Settings");
    more.addEventListener("click", () => setTab("settings", true));
    ed.line.replaceChildren(...["Runs only when ", el("span", { class: "mono ned-cond", title: cond }, readable(cond)), " · ", more, open].flat().filter(Boolean));
  } else ed.line.replaceChildren(...[aboutStep(step), open].flat().filter(Boolean));
  const t = stepTests[testKey()];
  const running = !!t && t.state === "running";
  ed.testBtn.setAttribute("aria-disabled", running ? "true" : "false");
  ed.testBtn.replaceChildren(icon("run", 13), running ? "Testing…" : "Test step");
}

// ---- the middle: what an AI step answers with ----
//
// The fields of the answer are kept in a schema file of their own. They are
// read from the server when the editor opens a step and edited as drafts, like
// every other field: a draft is { fields } for the table or { text } for the
// file's text, and Save writes it after the workflow's own lines.

const schemaKey = (name, step) => name + "|" + step;
const baseFields = (c) => (c.fields ? fieldsOut(c.fields.map(rowOfField)) : null);
const schemaDraftOf = (name, step) => (drafts[name] && drafts[name].schemas[step]) || null;
function schemaSame(draft, base) {
  return draft.text !== undefined ? draft.text === base.text : !!base.fields && JSON.stringify(draft.fields) === JSON.stringify(baseFields(base));
}

function loadSchema(w, step, force) {
  const key = schemaKey(w.name, step);
  const prev = schemaCache[key];
  if (prev && prev.inflight && !force) return Promise.resolve();
  schemaCache[key] = { ...(prev || { state: "loading" }), inflight: true, wfRev: w.rev };
  return request("GET", "/api/schema?workflow=" + encodeURIComponent(w.name) + "&step=" + encodeURIComponent(step))
    .then((r) => {
      schemaCache[key] = r.ok
        ? { state: "ready", file: r.body.file, text: r.body.text, rev: r.body.rev, fields: r.body.fields || null, wfRev: w.rev }
        : { state: "error", error: r.body.error || "the server answered " + r.status, wfRev: w.rev };
    })
    .catch(() => { schemaCache[key] = { state: "error", error: "the server cannot be reached", wfRev: w.rev }; })
    .then(() => {
      const now = workflowByName(w.name);
      if (!now) return;
      pruneDrafts(now);
      if (ed && ed.workflow === w.name) paintSchema();
      draftChanged(w.name, true);
    });
}

function setSchemaDraft(name, step, draft) {
  const d = draftsOf(name), st = uiOf(name);
  if (draft) d.schemas[step] = draft; else delete d.schemas[step];
  if (st.notice && (st.notice.kind === "saved" || st.notice.kind === "external") && !(st.notice.dropped && st.notice.dropped.length)) st.notice = null;
}

// paintSchema draws the answer's fields of the step being edited. The table is
// only built again when what it shows is no longer what the draft says, so a
// poll does not take the focus out of a box being typed in.
function paintSchema() {
  if (!ed || !ed.form || !ed.form.schemaBox) return;
  const { w, step } = curOf();
  if (!w || !step) return;
  const box = ed.form.schemaBox, key = schemaKey(w.name, step.id);
  let c = schemaCache[key];
  if (!c || (c.wfRev !== w.rev && !c.inflight && c.state !== "error")) { loadSchema(w, step.id); c = schemaCache[key]; }
  const draft = schemaDraftOf(w.name, step.id);
  const ready = c.state === "ready";
  const asText = ready && (!c.fields || ed.schemaText || (draft && draft.text !== undefined));
  const sig = [c.state, asText ? "text" : "fields", ready ? c.rev : c.error || "", ready ? c.file : ""].join("|");
  const shown = draft ? (asText ? draft.text : JSON.stringify(draft.fields)) : ready ? (asText ? c.text : JSON.stringify(baseFields(c))) : "";
  if (sig === ed.schemaSig) {
    if (asText) { if (ed.schemaText$ && ed.schemaText$.value !== shown) setValue(ed.schemaText$, shown); box.classList.toggle("edited", !!draft); return paintSchemaHead(c, draft, asText); }
    if (shown === ed.schemaShown) { box.classList.toggle("edited", !!draft); return paintSchemaHead(c, draft, asText); }
  }
  ed.schemaSig = sig;
  ed.schemaShown = shown;
  ed.schemaText$ = null;
  ed.schemaHead = el("div", { class: "ned-schemahead" });
  const body = el("div", { class: "ned-schemabody" });
  box.classList.toggle("edited", !!draft);
  box.replaceChildren(ed.schemaHead, body);
  if (c.state === "loading") body.append(el("p", { class: "ned-plain", role: "status" }, "Reading the answer's fields…"));
  else if (c.state === "error") {
    const again = el("button", { type: "button", class: "linkbtn" }, "Try again");
    again.addEventListener("click", () => { schemaCache[key] = { state: "loading", wfRev: w.rev }; loadSchema(w, step.id, true); paintSchema(); });
    body.append(el("p", { class: "ned-bad" }, "The answer's fields could not be read"), el("p", { class: "ned-plain" }, c.error + " ", again));
  } else if (asText) {
    const id = "nst" + ++formId;
    const area = el("textarea", { id, class: "in mono prompt ned-schematext", rows: 14, spellcheck: "false", "aria-label": "The schema, as JSON text" });
    area.value = shown;
    const bad = el("p", { class: "conflict", hidden: true, role: "status" });
    const check = () => {
      let msg = "";
      try { const v = JSON.parse(area.value); if (!v || typeof v !== "object" || Array.isArray(v)) msg = "A schema is a JSON object."; } catch (err) { msg = "Not valid JSON yet: " + err.message; }
      bad.hidden = !msg;
      bad.textContent = msg;
    };
    area.addEventListener("input", () => {
      const base = schemaCache[key];
      setSchemaDraft(w.name, step.id, base && area.value !== base.text ? { text: area.value } : null);
      ed.schemaShown = area.value;
      check();
      draftChanged(w.name);
    });
    ed.schemaText$ = area;
    body.append(area, bad, el("p", { class: "ned-plain" }, c.fields ? "This is the file as text. Whatever it says is saved as written." : "This schema says more than a list of fields can, so it is edited as text. Whatever it says is saved as written."));
  } else {
    const list = (draft ? draft.fields : baseFields(c)).map((f) => ({ ...f, options: (f.options || []).join(", "), description: f.description || "" }));
    ed.schemaList = list;
    const table = buildFieldTable(list, () => {
      const base = schemaCache[key], out = fieldsOut(list);
      setSchemaDraft(w.name, step.id, base && base.fields && JSON.stringify(out) === JSON.stringify(baseFields(base)) ? null : { fields: out });
      ed.schemaShown = JSON.stringify(out);
      draftChanged(w.name);
    }, ++formId);
    body.append(table.root);
  }
  paintSchemaHead(c, draft, asText);
}

// The line above the fields: what they are, which file they are in, and how to change the way they are edited.
function paintSchemaHead(c, draft, asText) {
  const { w, step } = curOf();
  if (!w || !step || !ed.schemaHead) return;
  const head = ed.schemaHead, ready = c.state === "ready";
  const parts = [el("span", { class: "ned-h" }, "The answer has these fields")];
  if (ready) parts.push(el("span", { class: "ned-fieldchip mono", title: "The schema file" }, c.file));
  parts.push(el("span", { class: "grow" }));
  if (draft) {
    const back = el("button", { type: "button", class: "linkbtn", title: "Put back what the file says" }, "Revert");
    back.addEventListener("click", () => { setSchemaDraft(w.name, step.id, null); paintSchema(); draftChanged(w.name, true); });
    parts.push(back);
  }
  if (ready && c.fields) {
    const to = el("button", { type: "button", class: "linkbtn" }, asText ? "Edit as fields" : "Edit as text");
    // The table and the text are two views of the same file; a change made in one is not carried to the other.
    if (draft && (asText ? draft.text !== undefined : draft.fields !== undefined)) { to.disabled = true; to.title = "Save or revert what you changed first"; }
    to.addEventListener("click", () => { ed.schemaText = !asText; paintSchema(); });
    parts.push(to);
  }
  head.replaceChildren(...parts);
}

// ---- the left: what the step can read ----

function walkPath(value, path) {
  let v = value;
  for (const part of path.split(".")) {
    if (v == null || typeof v !== "object" || !(part in v)) return undefined;
    v = v[part];
  }
  return v;
}

function previewOf(v) {
  if (v === undefined) return null;
  if (typeof v === "string") return v;
  if (v !== null && typeof v === "object") return Array.isArray(v) ? plural(v.length, "item") : JSON.stringify(v);
  return String(v);
}

function typeTag(v) {
  if (typeof v === "string") return "Aa";
  if (typeof v === "number") return "#";
  if (typeof v === "boolean") return "t/f";
  if (Array.isArray(v)) return "[ ]";
  if (v && typeof v === "object") return "{ }";
  return "·";
}

function flattenBody(prefix, value, depth, out) {
  if (!value || typeof value !== "object" || Array.isArray(value)) return;
  for (const k of Object.keys(value)) {
    if (out.length >= 60) return;
    const v = value[k], path = prefix + "." + k;
    if (v && typeof v === "object" && !Array.isArray(v) && depth < 2 && Object.keys(v).length) flattenBody(path, v, depth + 1, out);
    else out.push({ path, short: path.slice("trigger.body.".length), value: v });
  }
}

// inputSources lists what a step can read, by where it comes from: the request,
// each earlier step (with what it gives, as the run had it) and the environment.
// The ones the step reads come first.
function inputSources(w, step, ran) {
  const idx = w.steps.indexOf(step), reads = new Set(step.reads || []);
  const out = [];
  const trig = { id: "trigger", kind: "trigger", rows: [], read: reads.has("trigger"), note: null };
  if (ran && ran.trigger && typeof ran.trigger === "object") {
    for (const [k, v] of Object.entries(ran.trigger)) {
      if (k === "body") {
        trig.rows.push({ path: "trigger.body", short: "body", value: v });
        flattenBody("trigger.body", v, 0, trig.rows);
      } else trig.rows.push({ path: "trigger." + k, short: k, value: v });
    }
  } else if (isWebhook(w) || !w.trigger) {
    trig.rows.push({ path: "trigger.body", short: "body", value: undefined });
    const sample = w.sample && typeof w.sample === "object" && !Array.isArray(w.sample) ? Object.keys(w.sample) : [];
    for (const k of sample) trig.rows.push({ path: "trigger.body." + k, short: k, value: undefined });
  } else trig.rows.push({ path: "trigger.time", short: "time", value: undefined });
  out.push(trig);
  w.steps.slice(0, idx).forEach((s) => {
    const rec = ran ? ran.steps.find((x) => x.id === s.id) : null;
    const names = s.outputs && s.outputs.length ? s.outputs : (s.questions || []).map((q) => q.name + ".value");
    const src = { id: s.id, kind: s.kind, rows: [], read: reads.has(s.id), note: null };
    if (ran && !rec) src.note = "not in this run";
    else if (rec && rec.status === "skipped") src.note = "skipped in this run";
    else if (rec && rec.status === "failed") src.note = "failed in this run";
    for (const n of names) src.rows.push({ path: s.id + "." + n, short: n, value: rec && rec.output !== undefined ? walkPath(rec.output, n) : undefined });
    out.push(src);
  });
  const env = new Set();
  for (const [, fields] of editSets(w)) for (const f of fields) for (const m of String(f.value).matchAll(/\$\{\s*env\.([A-Za-z_]\w*)/g)) env.add(m[1]);
  if (env.size) out.push({ id: "env", kind: "env", rows: [...env].sort().map((n) => ({ path: "env." + n, short: n, value: undefined })), read: false, note: null, env: true });
  return out;
}

// refPill is a key: coloured by the step it comes from, and dragged into a field.
function refPill(path, source, kind, text, tip) {
  const p = el("span", { class: "ref kind-" + kind, role: "button", tabindex: "0", draggable: "true", "data-path": path, title: tip || "Drag it onto a field, or press Enter to put ${" + path + "} in the field you were editing" }, text || path);
  p.addEventListener("dragstart", (e) => {
    dragRef = { path, source };
    e.dataTransfer.effectAllowed = "copy";
    e.dataTransfer.setData("text/plain", "${" + path + "}");
    p.classList.add("dragging");
  });
  p.addEventListener("dragend", () => { dragRef = null; p.classList.remove("dragging"); });
  return p;
}

// put replaces what a node holds with children, which may be nested lists.
function put(node, ...children) {
  node.replaceChildren();
  return append(node, children);
}

function setNote(col, text) { col.note.textContent = text; }
const INPUT_NOTE = "Drag a key onto a field in Parameters, or click one to put it in the field you last used. Its colour says which step it comes from.";

function useRef(path, source) {
  const c = ed.target && ed.target.row.isConnected && !ed.target.row.closest("[hidden]") ? ed.target : null;
  if (!c) { setNote(ed.inCol, "Click a field in Parameters first, then click a key to put it there."); return; }
  if (source === ed.step) { setNote(ed.inCol, "A step cannot read its own output."); return; }
  putRef(c, { path, source });
  setNote(ed.inCol, "Put " + (c.kind === "expression" ? path : "${" + path + "}") + " in " + c.field.label + ".");
}

function paintInput() {
  const { w, step } = curOf();
  if (!w || !step) return;
  const ran = runShown();
  put(ed.inCol.meta, ran ? ["run ", el("span", { class: "mono" }, ran.run)] : "no run yet");
  ed.inCol.seg.replaceChildren(seg("Show input as", [["schema", "Schema"], ["json", "JSON"]], ed.inMode, (v) => { ed.inMode = v; paintInput(); }));
  const sources = inputSources(w, step, ran);
  const body = [];
  if (ed.inMode === "json") {
    const all = {};
    for (const s of sources) {
      if (s.env) continue;
      if (s.id === "trigger") { if (ran && ran.trigger !== undefined) all.trigger = ran.trigger; continue; }
      const rec = ran ? ran.steps.find((x) => x.id === s.id) : null;
      if (rec && rec.output !== undefined) all[s.id] = rec.output;
    }
    body.push(Object.keys(all).length ? jsonView(all) : el("p", { class: "ned-plain" }, "No data yet: the workflow has not run."));
    ed.inCol.body.replaceChildren(...body);
    setNote(ed.inCol, "This is the data as the run had it. Switch to Schema to drag keys.");
    return;
  }
  let divided = false;
  for (const s of sources) {
    if (!s.read && !divided && s.id !== "trigger" && !s.env) { divided = true; body.push(el("div", { class: "ned-divide" }, "Not read by this step yet")); }
    if (s.env && !divided) body.push(el("div", { class: "ned-divide" }, "From the environment"));
    body.push(el("div", { class: "ned-src kind-" + s.kind + (s.read || s.id === "trigger" ? "" : " unread") },
      el("div", { class: "ned-srchead" },
        s.env ? el("span", { class: "ned-stile" }, "$") : el("span", { class: "ned-stile kind-" + s.kind }, icon(s.kind, 12)),
        el("span", { class: "mono ned-sid" }, s.id),
        el("span", { class: "ned-word" }, s.env ? "names used here" : s.note ? s.note : SOURCE_WORD[s.kind] || ""),
        s.read ? el("span", { class: "ned-reads" }, "read here") : null),
      s.rows.length ? s.rows.map((r) => {
        const shown = previewOf(r.value);
        return el("div", { class: "ned-row" + (r.path.split(".").length > 3 && s.id === "trigger" ? " deep" : "") },
          el("span", { class: "ned-type", "aria-hidden": "true" }, shown === null ? "·" : typeTag(r.value)),
          (() => { const p = refPill(r.path, s.id, s.kind, r.short); p.addEventListener("click", () => useRef(r.path, s.id)); p.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); useRef(r.path, s.id); } }); return p; })(),
          el("span", { class: "ned-val-text" + (shown === null ? " none" : ""), title: shown === null ? null : shown }, shown === null ? (ran ? "—" : "") : shown));
      }) : el("p", { class: "ned-plain" }, "No fields are known for it.")));
    if (s.env) divided = true;
  }
  if (!ran) body.push(el("p", { class: "ned-plain" }, "No run yet, so only the names are shown. Run the workflow and the values appear here."));
  ed.inCol.body.replaceChildren(...body);
  setNote(ed.inCol, INPUT_NOTE);
}

// ---- the right: what it put out ----

function answerView(q, a) {
  const yesNo = !(q.options && q.options.length);
  let probs = a.probabilities && typeof a.probabilities === "object" ? a.probabilities : null;
  if (!probs && typeof a.probability === "number" && yesNo) probs = { yes: a.probability, no: 1 - a.probability };
  const picked = typeof a.value === "boolean" ? (a.value ? "yes" : "no") : String(a.value);
  const names = probs ? Object.keys(probs).sort((x, y) => probs[y] - probs[x]) : [];
  const close = names.length >= 2 && probs[names[0]] - probs[names[1]] < 0.15;
  const more = !!ed.more[q.name];
  const shown = names.length > 5 && !more ? names.slice(0, 4) : names;
  const toggle = names.length > 5 ? el("button", { type: "button", class: "linkbtn" }, more ? "Show fewer" : "Show " + (names.length - 4) + " more: " + names.slice(4).join(", ")) : null;
  if (toggle) toggle.addEventListener("click", () => { ed.more[q.name] = !more; paintOutput(); });
  return el("div", { class: "ned-ans-out" },
    el("div", { class: "ned-qrow" },
      el("span", { class: "mono ned-qn" }, q.name),
      el("span", { class: "grow" }),
      el("span", { class: "ref static kind-decide" }, picked),
      names.length ? el("span", { class: "ned-sure " + (close ? "close" : "clear") }, el("span", { class: "ned-sdot", "aria-hidden": "true" }), close ? "close call" : "clear") : null,
      typeof a.score === "number" ? el("span", { class: "ned-faint" }, "score " + a.score.toFixed(2)) : null),
    shown.map((n) => el("div", { class: "ned-bar" + (n === picked ? " top" : "") },
      el("span", { class: "mono ned-bn" }, n),
      el("span", { class: "ned-track" }, el("span", { class: "ned-fill", css: { width: Math.max(1, Math.round(probs[n] * 100)) + "%" } })),
      el("span", { class: "mono ned-bp" }, Math.round(probs[n] * 100) + "%"))),
    toggle);
}

function outputBody(step, rec, tested) {
  const parts = [];
  if (tested && tested.wouldRun === false) parts.push(el("p", { class: "ned-warn" }, "Its condition is false for this data, so a run would have skipped it; it was tried anyway."));
  if (rec.status === "skipped") parts.push(el("p", { class: "ned-plain" }, "Skipped in this run: its condition was false for that data."));
  if (rec.error || rec.status === "failed") parts.push(el("p", { class: "ned-bad" }, tested ? "Test step failed" : "The step failed in this run"), el("pre", { class: "error" }, rec.error || "No reason was recorded."));
  const out = rec.output;
  if (ed.outMode === "json") {
    parts.push(out !== undefined ? jsonView(out) : el("p", { class: "ned-plain" }, "It has no output."));
  } else if (step.kind === "decide" && out && typeof out === "object" && (step.questions || []).length) {
    for (const q of step.questions) if (out[q.name] && typeof out[q.name] === "object") parts.push(answerView(q, out[q.name]));
  } else if (step.kind === "ai" && out && typeof out === "object" && !Array.isArray(out)) {
    for (const k of Object.keys(out)) {
      const v = out[k];
      parts.push(el("div", { class: "ned-field" },
        el("div", { class: "ned-qrow" }, el("span", { class: "ned-type", "aria-hidden": "true" }, typeTag(v)), el("span", { class: "mono ned-qn" }, k)),
        typeof v === "string" ? el("p", { class: "ned-text" }, v) : jsonView(v)));
    }
  } else if (step.kind === "http") {
    if (out && typeof out === "object" && out.status !== undefined) parts.push(el("div", { class: "ned-qrow" }, el("span", { class: "ned-type", "aria-hidden": "true" }, "#"), el("span", { class: "mono ned-qn" }, "status"), el("span", { class: "ref static kind-http" }, String(out.status))));
    if (out && typeof out === "object" && Object.keys(out).some((k) => k !== "status")) {
      const { status, ...others } = out;
      parts.push(jsonView(others));
    }
  } else if (out !== undefined) parts.push(jsonView(out));
  if (rec.detail) parts.push(el("p", { class: "ned-detail mono" }, rec.detail));
  return parts;
}

function paintOutput() {
  const { w, step } = curOf();
  if (!w || !step) return;
  const ran = runShown();
  const t = stepTests[testKey()] || null;
  const source = t && ed.outSource !== "run" ? "test" : "run";
  const ctl = [];
  if (t) ctl.push(seg("Show", [["run", "Last run"], ["test", "Test step"]], source, (v) => { ed.outSource = v; paintOutput(); }));
  ctl.push(seg("Show output as", [["fields", step.kind === "decide" ? "Answers" : "Fields"], ["json", "JSON"]], ed.outMode, (v) => { ed.outMode = v; paintOutput(); }));
  ed.outCol.seg.replaceChildren(...ctl);

  const body = [];
  if (source === "test") {
    ed.outCol.meta.replaceChildren(t.state === "running" ? "Test step · running…" : "Test step · " + ago(t.at) + (t.state === "done" && t.result.step ? " · " + duration(t.result.step.ms || 0) : ""));
    if (t.state === "running") body.push(el("p", { class: "ned-plain", role: "status" }, "Trying the step with the data of run ", el("span", { class: "mono" }, t.run), ". A model may be called."));
    else if (t.state === "error") body.push(el("p", { class: "ned-bad" }, "Test step did not run"), el("pre", { class: "error" }, t.error));
    else {
      body.push(el("p", { class: "ned-plain" }, "With the data of run ", el("span", { class: "mono" }, t.run), ": the request and the earlier steps' outputs came from it."));
      if (t.unsaved) body.push(el("p", { class: "ned-warn" }, "Tried as saved: your " + plural(t.unsaved, "unsaved change") + " " + (t.unsaved === 1 ? "was" : "were") + " not used."));
      if (t.rev !== w.rev) body.push(el("p", { class: "ned-warn" }, "The workflow was saved after this test. Test again to try the new version."));
      body.push(...outputBody(step, t.result.step || {}, t.result));
    }
  } else {
    const rec = ran ? ran.steps.find((x) => x.id === step.id) : null;
    if (!ran) {
      ed.outCol.meta.replaceChildren("no run yet");
      body.push(el("p", { class: "ned-plain" }, "Nothing has run yet, so there is no output to show. Run the workflow, or send it a request with the curl command under Trigger, then come back."));
    } else if (!rec) {
      ed.outCol.meta.replaceChildren("run ", el("span", { class: "mono" }, ran.run));
      body.push(el("p", { class: "ned-plain" }, "This run has no record of the step: " + (ran.status === "failed" ? "an earlier step failed first." : ran.status === "running" ? "it has not got this far." : "it was recorded before the step was added.")));
    } else {
      ed.outCol.meta.replaceChildren((view.run ? "this run" : "last run") + (rec.status === "ok" || rec.ms ? " · " + duration(rec.ms || 0) : "") + (rec.status === "failed" ? " · failed" : ""));
      body.push(...outputBody(step, rec, null));
    }
  }
  const outs = step.outputs || [];
  body.push(el("div", { class: "ned-later" },
    el("span", { class: "ned-h" }, "Later steps can use"),
    outs.length ? el("div", { class: "ned-pills" }, outs.map((n) => {
      const p = refPill(step.id + "." + n, step.id, step.kind, n, "Click to copy ${" + step.id + "." + n + "}");
      const copy = () => {
        const text = "${" + step.id + "." + n + "}";
        if (navigator.clipboard) navigator.clipboard.writeText(text).then(() => setNote(ed.outCol, "Copied " + text + ". Paste it into a field of a later step."), () => setNote(ed.outCol, "Press Ctrl+C on " + text));
        else setNote(ed.outCol, text + " is what a later step writes to use it.");
      };
      p.addEventListener("click", copy);
      p.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); copy(); } });
      return p;
    })) : el("p", { class: "ned-plain" }, "It gives later steps nothing to read."),
    ));
  ed.outCol.body.replaceChildren(...body);
  if (!ed.outCol.note.textContent) setNote(ed.outCol, "A key keeps this step's colour wherever it is used. Click one to copy it, then paste it into a later step.");
}

async function testStep() {
  if (!ed) return;
  const { w, step } = curOf();
  if (!w || !step) return;
  const key = testKey(), prev = stepTests[key];
  if (prev && prev.state === "running") return;
  const ran = runShown();
  if (!ran) {
    // The endpoint is not called: there is nothing to take data from.
    stepTests[key] = { state: "error", run: "", at: Date.now(), rev: w.rev, unsaved: 0, error: "There is no run to take data from. Run the workflow first, or send it a request with the curl command under Trigger, then test the step." };
    ed.outSource = "test";
    paintHead();
    paintOutput();
    return;
  }
  const mine = ed;
  stepTests[key] = { state: "running", run: ran.run, at: Date.now(), rev: w.rev, unsaved: draftCountFor(w.name, step.id) };
  ed.outSource = "test";
  setNote(ed.outCol, "");
  paintHead();
  paintOutput();
  let r = null;
  try { r = await request("POST", "/api/steps/test", { workflow: w.name, step: step.id, run: ran.run }); } catch (err) { r = null; }
  const t = stepTests[key];
  t.at = Date.now();
  if (!r) { t.state = "error"; t.error = "The server cannot be reached."; }
  else if (!r.ok) { t.state = "error"; t.error = r.body.error || "The server answered " + r.status + "."; }
  else if (!r.body.step) { t.state = "error"; t.error = "The server's answer had no step in it."; }
  else { t.state = "done"; t.result = r.body; if (r.body.from) t.run = r.body.from; }
  if (ed === mine) {
    paintHead();
    paintOutput();
    announce(t.state === "done" ? "Test step finished: " + (r.body.step.status || "") : "Test step did not run: " + t.error);
  }
}

// ---- the footer: the page's save bar, here ----

const wordsFor = (e, step) => {
  if (e.group && e.group !== "Input") return step.kind === "decide" ? "the " + e.group + " question" : e.label.toLowerCase() + " of " + e.group;
  if (e.group) return "the " + e.label + " input";
  return { "Run only when": "the condition", Model: "the model", Input: "the input", "Output schema": "the schema file's name" }[e.label] || "the " + e.label.toLowerCase();
};

function buildFoot(w, step) {
  const b = {};
  b.remove = canAdd(w) ? removeButton(w, step) : null;
  b.slot = el("div", { class: "ned-noticeslot" });
  b.diff = el("pre", { class: "diff ned-diff", tabindex: "0", "aria-label": "What saving would change", hidden: true });
  b.dot = el("span", { class: "ned-dot", "aria-hidden": "true" });
  b.count = el("span", { class: "ned-count2" });
  b.what = el("span", { class: "ned-what" });
  b.writes = el("span", { class: "ned-writes" });
  b.link = el("button", { type: "button", class: "linkbtn ned-difflink", "aria-expanded": "false" }, "See the diff");
  b.link.addEventListener("click", () => { ed.diff = !ed.diff; paintFoot(); });
  b.discard = el("button", { type: "button", class: "btn ned-btn" }, "Discard");
  b.save = el("button", { type: "button", class: "btn primary ned-btn" }, "Save", el("kbd", {}, isMac ? "⌘S" : "Ctrl+S"));
  b.close = el("button", { type: "button", class: "btn ned-btn" }, "Close");
  b.discard.addEventListener("click", () => discardCurrent());
  b.discard.addEventListener("blur", () => { if (ed) disarm(ed.workflow); });
  b.save.addEventListener("click", () => saveCurrent());
  b.close.addEventListener("click", () => closeEditor());
  b.row = el("div", { class: "ned-footrow" }, b.remove, b.dot, b.count, b.what, b.writes, b.link, el("span", { class: "grow" }), b.close, b.discard, b.save);
  b.root = el("footer", { class: "ned-foot" }, b.slot, b.diff, b.row);
  return b;
}

function diffOf(w, step) {
  const out = [];
  const index = fieldIndex(w), d = draftsOf(w.name);
  for (const [k, e] of Object.entries(d.fields)) {
    const was = index.get(k);
    out.push(["hunk", "@@ " + nodeName(e.node) + " · " + (e.group ? e.group + " · " : "") + e.label + " @@"]);
    out.push(["del", "- " + (was ? was.field.value : "")], ["add", "+ " + e.value]);
  }
  for (const [stepId, text] of Object.entries(d.prompts)) {
    const p = promptCache[w.name + "|" + stepId];
    const lines = (s) => plural(s.split("\n").length, "line");
    out.push(["hunk", "@@ " + stepId + " · prompt @@"]);
    if (p && p.state === "ready") out.push(["del", "- " + lines(p.text) + " in " + p.file]);
    out.push(["add", "+ " + lines(text) + (p && p.file ? " in " + p.file : "")]);
  }
  for (const [stepId, draft] of Object.entries(d.schemas)) {
    const c = schemaCache[w.name + "|" + stepId];
    const show = (f) => f.name + " · " + f.type + (f.options ? " (" + f.options.join(", ") + ")" : "") + " · " + (f.required ? "required" : "optional") + (f.description ? " · " + f.description : "");
    out.push(["hunk", "@@ " + stepId + " · the answer's fields" + (c && c.file ? " in " + c.file : "") + " @@"]);
    if (draft.text !== undefined) { out.push(["add", "+ the schema's text, " + plural(draft.text.split("\n").length, "line")]); continue; }
    const was = c && c.state === "ready" ? baseFields(c) || [] : [];
    const has = (list, f) => list.some((x) => JSON.stringify(x) === JSON.stringify(f));
    for (const f of was) if (!has(draft.fields, f)) out.push(["del", "- " + show(f)]);
    for (const f of draft.fields) if (!has(was, f)) out.push(["add", "+ " + show(f)]);
  }
  return out;
}

function paintFoot() {
  if (!ed || !ed.foot) return;
  const { w, step } = curOf();
  if (!w || !step) return;
  const b = ed.foot, st = uiOf(w.name), d = draftsOf(w.name);
  const n = dirtyCount(w.name), nf = Object.keys(d.fields).length, np = Object.keys(d.prompts).length, ns = Object.keys(d.schemas).length;
  b.root.classList.toggle("dirty", n > 0);
  if (b.shownNotice !== st.notice) {
    b.shownNotice = st.notice;
    b.slot.replaceChildren(...(st.notice ? [noticeView(st.notice, w)] : []));
  }
  if (n === 0 && !st.saving) {
    b.dot.hidden = true;
    b.count.textContent = "Nothing changed";
    b.what.textContent = "";
    b.writes.textContent = "";
    b.link.hidden = true;
    ed.diff = false;
  } else {
    const here = [...new Set([
      ...Object.values(d.fields).filter((e) => e.node === step.id).map((e) => wordsFor(e, step)),
      ...(d.prompts[step.id] !== undefined ? ["the prompt"] : []),
      ...(d.schemas[step.id] !== undefined ? ["the answer's fields"] : [])])];
    const elsewhere = n - draftCountFor(w.name, step.id);
    b.dot.hidden = false;
    b.count.textContent = st.saving ? "Saving…" : plural(n, "change");
    b.what.textContent = [here.slice(0, 3).join(", ") + (here.length > 3 ? " and " + (here.length - 3) + " more" : ""), elsewhere ? plural(elsewhere, "change") + " in other steps" : ""].filter(Boolean).join(" and ");
    const file = (w.path || "workflow.yaml").split("/").pop();
    put(b.writes, "Saving writes ", [nf ? plural(nf, "line") + " of " : null, nf ? el("span", { class: "mono" }, file) : null, nf && (np || ns) ? " and " : null,
      np ? plural(np, "prompt file") : null, np && ns ? " and " : null, ns ? plural(ns, "schema file") : null], " and puts it live.");
    b.link.hidden = false;
  }
  b.link.setAttribute("aria-expanded", ed.diff ? "true" : "false");
  b.link.textContent = ed.diff ? "Hide the diff" : "See the diff";
  b.diff.hidden = !(ed.diff && n > 0);
  if (!b.diff.hidden) b.diff.replaceChildren(...diffOf(w, step).map(([kind, line]) => el("span", { class: "dl " + kind }, line + "\n")));
  b.close.hidden = n > 0;
  b.discard.hidden = n === 0;
  b.save.hidden = n === 0;
  b.save.setAttribute("aria-disabled", st.saving || n === 0 ? "true" : "false");
  b.discard.setAttribute("aria-disabled", st.saving || n === 0 ? "true" : "false");
  b.discard.classList.toggle("armed", st.armed);
  b.discard.textContent = st.armed ? "Discard " + plural(n, "change") + "?" : "Discard";
}

window.addEventListener("hashchange", () => {
  readAddress();
  if (!selected.run) detail = null;
  refresh();
});
window.addEventListener("keydown", (event) => {
  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "s" && event.target.closest && event.target.closest(".editform, .nodeed")) {
    event.preventDefault();
    saveCurrent();
    return;
  }
  const typing = event.target.closest && event.target.closest("input, textarea, select, [contenteditable]");
  if ((event.key === "a" || event.key === "A") && !event.metaKey && !event.ctrlKey && !event.altKey && !typing && view && canAdd(view.w) && !selected.add && !selected.test && !selected.changes && !ed) {
    event.preventDefault();
    openAdd();
    return;
  }
  if (event.key !== "Escape") return;
  if (menu) { event.preventDefault(); closeMenu(true); return; }
  // The node editor is on top of everything else: Escape closes it, and what was typed stays as a draft.
  if (ed) { event.preventDefault(); closeEditor(); return; }
  // In a form field Escape only leaves the field.
  if (event.target.closest && event.target.closest(".editform")) { event.target.blur(); return; }
  if (selected.make) go({ make: false });
  else if (selected.add) go({ add: null });
  else if (selected.test) go({ test: null });
  else if (selected.changes) go({ changes: false });
  else if (selected.node) go({ node: null });
  else if (selected.run) go({ run: null });
});

// The width of the window decides how wide the boxes are drawn.
let resizing = 0, drawnWidth = 0;
new ResizeObserver(() => {
  const width = $("canvas").clientWidth;
  if (!view || Math.abs(width - drawnWidth) < 2) return;
  drawnWidth = width;
  cancelAnimationFrame(resizing);
  resizing = requestAnimationFrame(() => view && renderCanvas(false));
}).observe($("canvas"));

// The width of the details decides how tall a text box must be to show its text.
let formWidth = 0;
new ResizeObserver(() => {
  const width = $("inspector").clientWidth;
  if (Math.abs(width - formWidth) < 2) return;
  formWidth = width;
  if (form) form.controls.forEach((c) => autosize(c.input));
}).observe($("inspector"));

buildRail();
readAddress();
refresh();
setInterval(refresh, REFRESH_MS);

window.addEventListener("beforeunload", (event) => {
  if (!anyDirty()) return;
  event.preventDefault();
  event.returnValue = "";
});
