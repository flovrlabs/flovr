"use strict";

// Text that came from outside the server (tickets, model answers) is only
// ever added to the page as text nodes, never as HTML.

const REFRESH_MS = 3000;

// Sizes of the drawing, in pixels before it is scaled to fit the window. The
// workflow flows from top to bottom, so it stays narrow: branches sit side by
// side and the page never needs to squeeze it to make room for the details.
const CARD_H = 88;
const W_MIN = 214;
const W_MAX = 272;
const GAP_X = 28;
const GAP_Y = 46;
const PAD = 28;
const FOOT = 28;     // height of the strip at the bottom of a box
const LEAN = CARD_H * 0.18; // how far an HTTP box leans
const ZOOM_MIN = 0.5;
const ZOOM_MAX = 1.5;

const ICONS = {
  trigger: ["M13 2 4.5 13.5H11L10 22l8.5-11.5H12z"],
  decide: ["M6 3v18", "M6 15c8 0 12-2 12-9", "M14.5 8.5 18 5l3.5 3.5"],
  ai: ["M11 4l1.9 5.1L18 11l-5.1 1.9L11 18l-1.9-5.1L4 11l5.1-1.9z", "M18.5 15.5l.7 1.8 1.8.7-1.8.7-.7 1.8-.7-1.8-1.8-.7 1.8-.7z"],
  http: ["M4 12h15", "M13 6l6 6-6 6"],
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
  minus: ["M5 12h14"],
  fit: ["M4 9V4h5", "M20 9V4h-5", "M4 15v5h5", "M20 15v5h-5"],
};

const KIND_LABEL = { trigger: "Trigger", decide: "Decide", ai: "AI", http: "HTTP" };
const KIND_NAMES = {
  trigger: "Trigger: starts the workflow",
  decide: "Decision step: a model answers typed questions",
  ai: "AI step: a model writes structured output",
  http: "HTTP step: sends a request",
};
const STATUS_TEXT = { ok: "Succeeded", failed: "Failed", skipped: "Skipped", running: "Running", pending: "Not reached" };

let state = { commit: "", protected: false, workflows: [], runs: [] };
let selected = { workflow: null, run: null, node: null, changes: false };
let detail = null;      // the selected run in full, with trigger and outputs
let shown = "";         // what the page currently shows, to tell whether anything changed
let view = null;        // the workflow and run now drawn, for redrawing on resize and zoom
let zoom = null;        // null fits the drawing to the window
let lastKey = "";       // which workflow, run and step were shown last time
let enteredFor = null;  // the workflow whose cards last slid in

const $ = (id) => document.getElementById(id);

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

// The address is #/w/<workflow>, optionally followed by /r/<run> and /n/<step>.
function readAddress() {
  const parts = location.hash.replace(/^#\/?/, "").split("/").map(decodeURIComponent);
  const found = {};
  for (let i = 0; i + 1 < parts.length; i += 2) found[parts[i]] = parts[i + 1];
  selected = { workflow: found.w || null, run: found.r || null, node: found.n || null, changes: found.c === "1" };
}

function go(change) {
  const next = { ...selected, ...change };
  // Picking anything else leaves the changes view, unless it is asked for.
  if (!("changes" in change)) next.changes = false;
  let hash = "#/w/" + encodeURIComponent(next.workflow);
  if (next.run) hash += "/r/" + encodeURIComponent(next.run);
  if (next.node) hash += "/n/" + encodeURIComponent(next.node);
  if (next.changes) hash += "/c/1";
  if (location.hash !== hash) location.hash = hash;
}

async function getJSON(path) {
  const response = await fetch(path, { headers: { Accept: "application/json" } });
  if (!response.ok) throw new Error(path + " answered " + response.status);
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
    live.replaceChildren(el("span", { class: "pulse" }), "Live");
    live.title = "Last updated " + when(Date.now());
    live.className = "live";
  } catch (err) {
    live.replaceChildren(el("span", { class: "pulse" }), "Cannot reach the server");
    live.className = "live down";
  }
  render();
}

// progress is how far a step got in a run: its recorded status, or "pending"
// if the run has not reached it.
function progress(run, id) {
  if (!run) return null;
  if (id === "trigger") return "ok";
  const done = run.steps.find((s) => s.id === id);
  return done ? done.status : "pending";
}

const latestRun = (name) => state.runs.find((r) => r.workflow === name);

function render() {
  if (!state.workflows.some((w) => w.name === selected.workflow)) {
    selected = { workflow: state.workflows.length ? state.workflows[0].name : null, run: null, node: null, changes: false };
    detail = null;
  }
  // Rebuilding the page drops the reader's focus and text selection, so it is
  // only done when there is something new to show.
  const wanted = state.workflows.find((x) => x.name === selected.workflow);
  if (selected.changes && state.editable && wanted) ensureChanges(wanted);
  const next = JSON.stringify([state, selected, detail, uiTick, selected.changes ? changesCache[selected.workflow] : null]);
  if (next === shown) return;
  shown = next;
  const focusId = document.activeElement && document.activeElement.dataset ? document.activeElement.dataset.focus : null;

  const w = state.workflows.find((x) => x.name === selected.workflow);
  const key = [selected.workflow, selected.run, selected.node, selected.changes].join("|");
  const moved = key !== lastKey;
  lastKey = key;

  renderTop(w);
  if (!w) {
    view = null;
    form = null;
    insp.body = null;
    $("side").replaceChildren();
    $("changes").replaceChildren();
    $("inspector").replaceChildren();
    $("canvas").replaceChildren(el("p", { class: "none" }, "No workflows are loaded."));
    return;
  }
  const run = detail && detail.run === selected.run && detail.workflow === w.name ? detail : null;
  view = { w, run };
  renderSide(w);
  renderCanvas(moved);
  renderInspector(w, run, moved);
  if (focusId) {
    const back = document.querySelector('[data-focus="' + CSS.escape(focusId) + '"]');
    if (back && back !== document.activeElement) back.focus({ preventScroll: true });
  }
}

// ---- top bar ----

function renderTop(w) {
  $("commit").replaceChildren(...(state.commit ? [icon("branch", 13), el("span", {}, state.commit)] : []));

  $("changes").replaceChildren(...(state.editable && w ? [changesChip()] : []));

  const last = w ? latestRun(w.name) : null;
  const anyFailed = state.runs.some((r) => r.status === "failed" && latestRun(r.workflow) === r);
  document.title = anyFailed ? "aia (a run failed)" : "aia";
  if (!w) {
    $("crumb").replaceChildren();
    $("latest").replaceChildren();
    return;
  }
  $("crumb").replaceChildren(
    el("span", { class: "sep", "aria-hidden": "true" }, "/"),
    el("strong", {}, w.name),
    w.trigger ? el("span", { class: "pill mono" }, w.trigger) : null);

  if (!last) {
    $("latest").className = "latest none";
    $("latest").replaceChildren(el("span", { class: "chip quiet" }, "No runs yet"));
    return;
  }
  const text = last.status === "failed" ? "Latest run failed" : last.status === "running" ? "Latest run is running" : "Latest run succeeded";
  $("latest").className = "latest";
  $("latest").replaceChildren(el("button", {
      class: "chip " + last.status, type: "button",
      title: "Open run " + last.run,
      onclick: () => go({ run: last.run, node: null }),
    },
    statusMark(last.status, 12), text,
    el("span", { class: "chip-meta" }, when(last.started))));
}

// statusMark is the small round symbol that stands for a status.
function statusMark(status, size) {
  return el("span", { class: "mark " + status }, icon(status, size || 12));
}

// ---- left: workflows and runs ----

function renderSide(w) {
  const workflows = state.workflows.map((x) => {
    const last = latestRun(x.name);
    return el("button", {
        class: "row wf", type: "button", "aria-current": x.name === w.name ? "true" : null,
        onclick: () => go({ workflow: x.name, run: null, node: null }),
      },
      el("span", { class: "dot " + (last ? last.status : ""), title: last ? "Latest run: " + last.status : "No runs yet" }),
      el("span", { class: "name" }, x.name),
      flagFor(x),
      el("span", { class: "sub mono" }, x.trigger || "no trigger"),
      el("span", { class: "aside" }, x.steps.length + (x.steps.length === 1 ? " step" : " steps")));
  });

  const runs = state.runs.filter((r) => r.workflow === w.name);
  const rows = runs.map((r) => el("button", {
      class: "row run " + r.status, type: "button", "aria-current": r.run === selected.run ? "true" : null,
      "aria-label": "Run at " + when(r.started) + ", " + r.status,
      onclick: () => go({ run: r.run === selected.run ? null : r.run, node: null }),
    },
    statusMark(r.status, 11),
    el("span", { class: "name" }, when(r.started)),
    el("span", { class: "aside" }, r.status === "running" ? "running" : duration(r.ms)),
    el("span", { class: "seg", "aria-hidden": "true" }, w.steps.map((s) => el("i", { class: progress(r, s.id) }))),
    el("span", { class: "sub" }, outcome(r))));

  $("side").replaceChildren(
    el("h2", {}, "Workflows"),
    el("div", { class: "list" }, workflows),
    el("h2", {}, "Runs", runs.length ? el("span", { class: "count" }, runs.length) : null),
    runs.length
      ? el("div", { class: "list runs" }, rows)
      : el("p", { class: "empty" }, w.trigger ? "None yet. A run appears here each time the trigger fires." : "The server does not run this workflow: it has no webhook or cron trigger."),
    el("p", { class: "note" }, state.editable
      ? "Edits are written to the workflow's YAML file. Committing stays in git."
      : "Read-only. Workflows are files in git: edit the YAML, commit it, restart the server."));
}

// outcome says in a few words what a run did.
function outcome(r) {
  const failed = r.steps.find((s) => s.status === "failed");
  if (failed) return "Failed at " + failed.id;
  const ran = r.steps.filter((s) => s.status === "ok").map((s) => s.id);
  if (r.status === "running") return ran.length ? "Done: " + ran.join(", ") : "Starting";
  return ran.length ? ran.join(" → ") : "No step ran";
}

// ---- centre: the drawing ----

// Each kind of node has its own silhouette, drawn from the same rounded
// corners and the same stroke so they read as one family: a trigger is a pill,
// a decision a hexagon with pointed ends, an AI step a card with a cut corner,
// an HTTP step a box leaning in the direction of the flow, like the
// input/output box of a flowchart.
function roundedPath(points, radii) {
  const n = points.length;
  let d = "";
  points.forEach((p, i) => {
    const a = points[(i + n - 1) % n], b = points[(i + 1) % n];
    const toward = (q) => {
      const len = Math.hypot(q[0] - p[0], q[1] - p[1]);
      const t = Math.min(radii[i], len / 2) / len;
      return [p[0] + (q[0] - p[0]) * t, p[1] + (q[1] - p[1]) * t];
    };
    const from = toward(a), to = toward(b);
    d += (i ? "L" : "M") + from[0].toFixed(2) + " " + from[1].toFixed(2) + "Q" + p[0] + " " + p[1] + " " + to[0].toFixed(2) + " " + to[1].toFixed(2);
  });
  return d + "Z";
}

// silhouette gives the outline path of a box w by h, and the two ends of the
// line that cuts the footer strip off, which follows the outline.
function silhouette(kind, w, h) {
  const m = 1, cut = h - FOOT;
  if (kind === "trigger") {
    const r = h / 2 - m, dy = cut - h / 2;
    const inset = h / 2 - Math.sqrt(Math.max(0, (h / 2) ** 2 - dy * dy));
    return { d: `M${h / 2} ${m}H${w - h / 2}A${r} ${r} 0 0 1 ${w - h / 2} ${h - m}H${h / 2}A${r} ${r} 0 0 1 ${h / 2} ${m}Z`, sep: [inset + 2, w - inset - 2] };
  }
  if (kind === "decide") {
    const p = h / 4;
    return { d: roundedPath([[m, h / 2], [p, m], [w - p, m], [w - m, h / 2], [w - p, h - m], [p, h - m]], [9, 10, 10, 9, 10, 10]),
      sep: [(p * (cut - h / 2)) / (h / 2) + 1, w - (p * (cut - h / 2)) / (h / 2) - 1] };
  }
  if (kind === "ai") {
    const c = h * 0.3;
    return { d: roundedPath([[m, m], [w - c, m], [w - m, c], [w - m, h - m], [m, h - m]], [12, 5, 5, 12, 12]), sep: [1, w - 1] };
  }
  if (kind === "http") {
    return { d: roundedPath([[LEAN, m], [w - m, m], [w - LEAN, h - m], [m, h - m]], [11, 11, 11, 11]),
      sep: [(LEAN * (h - cut)) / h + 1, w - (LEAN * cut) / h - 1] };
  }
  return { d: roundedPath([[m, m], [w - m, m], [w - m, h - m], [m, h - m]], [12, 12, 12, 12]), sep: [1, w - 1] };
}

// portShift is how far the middle of a box's top or bottom edge is from the
// middle of its bounding box, so edges and ports meet the edge, not the box.
function portShift(kind, side) {
  return kind === "http" ? (side === "in" ? LEAN / 2 : -LEAN / 2) : 0;
}

// shapeSVG draws a node's body: a halo for selection and failure, a ring for
// keyboard focus, the filled outline, and the line above the footer.
function shapeSVG(kind, w, h) {
  const s = silhouette(kind, w, h);
  return svg("svg", { class: "shape", width: w, height: h, viewBox: "0 0 " + w + " " + h, "aria-hidden": "true" },
    svg("path", { class: "halo", d: s.d }),
    svg("path", { class: "ring", d: s.d }),
    svg("path", { class: "bg", d: s.d }),
    svg("path", { class: "sep", d: "M" + s.sep[0] + " " + (h - FOOT) + "H" + s.sep[1] }));
}

// kindTile is a small icon tile in the silhouette of its kind.
function kindTile(kind, size) {
  const s = silhouette(kind, size, size);
  return el("span", { class: "tile kt" },
    svg("svg", { class: "kshape", width: size, height: size, viewBox: "0 0 " + size + " " + size, "aria-hidden": "true" }, svg("path", { d: s.d })),
    icon(kind, Math.round(size * 0.52)));
}

// layout places the trigger and the steps in rows. A step goes one row below
// the latest thing it reads from, so steps that depend on the same earlier
// step sit next to each other as branches.
function layout(w, width) {
  const nodes = [{
    id: "trigger", kind: "trigger", depth: 0,
    title: isWebhook(w) ? "Webhook" : w.trigger ? "Schedule" : "Manual",
    subtitle: w.trigger || "started by hand",
  }];
  const byId = { trigger: nodes[0] };
  w.steps.forEach((step, i) => {
    let parent = i === 0 ? "trigger" : w.steps[i - 1].id;
    if (step.reads.length) {
      parent = step.reads[0];
      for (const name of step.reads) if (byId[name].depth >= byId[parent].depth) parent = name;
    }
    const node = { id: step.id, kind: step.kind, title: step.id, subtitle: step.subtitle, order: i + 1, parent, depth: byId[parent].depth + 1, step };
    nodes.push(node);
    byId[step.id] = node;
  });

  const rows = [];
  for (const node of nodes) (rows[node.depth] = rows[node.depth] || []).push(node);
  const widest = Math.max(...rows.map((r) => r.length));
  const nodeW = Math.max(W_MIN, Math.min(W_MAX, Math.floor((width - PAD * 2 - (widest - 1) * GAP_X) / widest)));
  const total = widest * nodeW + (widest - 1) * GAP_X;
  rows.forEach((row, depth) => {
    const rowW = row.length * nodeW + (row.length - 1) * GAP_X;
    row.forEach((node, i) => {
      node.x = PAD + (total - rowW) / 2 + i * (nodeW + GAP_X);
      node.y = PAD + depth * (CARD_H + GAP_Y);
      node.hasOut = false;
    });
  });
  for (const node of nodes) if (node.parent) byId[node.parent].hasOut = true;
  return { nodes, byId, nodeW, width: PAD * 2 + total, height: PAD * 2 + rows.length * CARD_H + (rows.length - 1) * GAP_Y };
}

function renderCanvas(moved) {
  const canvas = $("canvas");
  const { w, run } = view;
  if (selected.changes && state.editable) return renderChanges(w);
  const edited = editedNodes(w);
  const old = canvas.querySelector(".scroller");
  const keep = old && !moved ? [old.scrollLeft, old.scrollTop] : null;

  const plan = layout(w, canvas.clientWidth || 800);
  const mid = plan.nodeW / 2;
  const animate = enteredFor !== w.name;
  enteredFor = w.name;

  const lines = [];
  for (const node of plan.nodes) {
    if (!node.parent) continue;
    const from = plan.byId[node.parent];
    const x1 = from.x + mid + portShift(from.kind, "out"), y1 = from.y + CARD_H, x2 = node.x + mid + portShift(node.kind, "in"), y2 = node.y;
    const status = progress(run, node.id) || "";
    const k = (y2 - y1) * 0.55;
    lines.push(
      svg("path", { class: "edge " + status, d: `M${x1} ${y1} C${x1} ${y1 + k}, ${x2} ${y2 - k}, ${x2} ${y2 - 8}` }),
      svg("path", { class: "head " + status, d: `M${x2} ${y2 - 0.5} l-4.5 -8 h9 z` }));
  }

  const cards = plan.nodes.map((node, i) => {
    const status = progress(run, node.id);
    const done = run ? run.steps.find((s) => s.id === node.id) : null;
    const cond = node.step && node.step.when;
    let took = "";
    if (node.id === "trigger") took = run ? when(run.started) : "";
    else if (done && done.status === "ok") took = duration(done.ms);
    else if (done) took = done.status === "failed" ? "failed" : done.status;
    else if (run) took = run.status === "running" ? "waiting" : "not reached";
    const label = node.title + ", " + KIND_LABEL[node.kind] + (node.order ? ", step " + node.order : "") + (status ? ", " + (STATUS_TEXT[status] || status).toLowerCase() : "");
    const isEdited = edited.has(node.id);
    return el("div", {
        class: "slot" + (animate ? " enter" : ""),
        css: { left: node.x + "px", top: node.y + "px", width: plan.nodeW + "px", height: CARD_H + "px", animationDelay: (animate ? i * 70 : 0) + "ms" },
      },
      el("button", {
          class: "card shape-" + node.kind + " kind-" + node.kind + (status ? " " + status : "") + (node.parent ? " has-in" : "") + (node.hasOut ? " has-out" : "") + (isEdited ? " edited" : ""),
          type: "button", "data-node": node.id, "data-focus": "n:" + node.id, "data-label": label,
          "aria-pressed": selected.node === node.id ? "true" : "false", "aria-label": label + (isEdited ? ", unsaved changes" : ""),
          onclick: () => go({ node: selected.node === node.id ? null : node.id }),
        },
        shapeSVG(node.kind, plan.nodeW, CARD_H),
        el("span", { class: "head" },
          el("span", { class: "tile" }, icon(node.kind, 20), node.order ? el("i", { class: "num", title: "Runs " + ordinal(node.order) }, node.order) : null),
          el("span", { class: "text" },
            el("span", { class: "eyebrow" }, KIND_LABEL[node.kind]),
            el("span", { class: "title" }, node.title),
            el("span", { class: "sub mono" }, node.subtitle)),
          status && node.id !== "trigger" ? statusMark(status, 12) : null),
        el("span", { class: "foot", title: cond || null },
          cond ? [el("b", {}, "IF"), el("span", { class: "cond mono" }, cond)] : el("span", { class: "always" }, node.id === "trigger" ? "Entry point" : "Always runs"),
          took ? el("span", { class: "took" }, took) : null)));
  });

  const stage = el("div", { class: "stage", css: { width: plan.width + "px", height: plan.height + "px" } },
    svg("svg", { width: plan.width, height: plan.height }, lines), cards);

  const scale = zoom == null ? Math.max(0.7, Math.min(1, (canvas.clientWidth - 40) / plan.width)) : zoom;
  stage.style.transform = "scale(" + scale + ")";
  const holder = el("div", { class: "fit", css: { width: Math.ceil(plan.width * scale) + 32 + "px", height: Math.ceil(plan.height * scale) + 32 + "px" } }, stage);
  const scroller = el("div", { class: "scroller" }, holder);

  canvas.replaceChildren(...[
    problemBanner(w),
    run ? runBar(run) : el("div", { class: "hint" },
      el("span", {}, "Steps run top to bottom in the order numbered."),
      el("span", {}, state.editable && !w.problem ? "Select a step to edit it, or a run on the left to see what happened." : "Pick a run on the left to see what happened.")),
    scroller,
    el("div", { class: "tools", role: "group", "aria-label": "Zoom" },
      tool("minus", "Zoom out", () => setZoom(scale - 0.1)),
      el("span", { class: "zoom", "aria-live": "off" }, Math.round(scale * 100) + "%"),
      tool("plus", "Zoom in", () => setZoom(scale + 0.1)),
      tool("fit", "Fit to window", () => { zoom = null; renderCanvas(false); })),
    run ? el("div", { class: "key", "aria-hidden": "true" },
      el("div", { class: "line" }, ["ok", "failed", "running", "skipped", "pending"].map((s) => el("span", {}, el("span", { class: "dot " + s }), s === "pending" ? "Not reached" : STATUS_TEXT[s])))) : null,
  ].filter(Boolean));

  if (keep) { scroller.scrollLeft = keep[0]; scroller.scrollTop = keep[1]; }
  else if (selected.node) {
    const card = scroller.querySelector('[data-node="' + CSS.escape(selected.node) + '"]');
    if (card) card.scrollIntoView({ block: "nearest", inline: "nearest" });
  }
}

const ordinal = (n) => "step " + n + " in file order";

function tool(name, label, onclick) {
  return el("button", { type: "button", class: "tool", "aria-label": label, title: label, onclick }, icon(name, 15));
}

function setZoom(value) {
  zoom = Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, Math.round(value * 100) / 100));
  renderCanvas(false);
}

function runBar(run) {
  return el("div", { class: "runbar " + run.status },
    el("span", { class: "state " + run.status }, statusMark(run.status, 12), STATUS_TEXT[run.status] || run.status),
    el("span", { class: "mono id" }, run.run),
    el("span", { class: "meta" }, when(run.started)),
    run.status !== "running" ? el("span", { class: "meta" }, "took " + duration(run.ms)) : null,
    el("span", { class: "meta" }, run.source),
    el("button", { type: "button", class: "ghost", onclick: () => go({ run: null, node: null }) }, "Close run", el("kbd", {}, "Esc")));
}

// ---- right: details ----

function section(title, ...children) {
  return el("section", {}, title ? el("h2", {}, title) : null, children);
}

function header(kind, iconName, title, subtitle, closable) {
  return el("header", { class: "kind-" + kind },
    KIND_LABEL[kind] ? kindTile(kind, 38) : el("span", { class: "tile" }, icon(iconName, 20)),
    el("div", { class: "names" }, el("h1", { class: kind === "workflow" ? null : "mono" }, title), el("div", { class: "kindname" }, subtitle)),
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
  return el("div", { class: "status-panel " + status },
    statusMark(status, 14),
    el("div", {}, el("div", { class: "big" }, STATUS_TEXT[status] || status), rest));
}

// example is a command that calls a workflow's webhook.
function example(w) {
  const path = w.trigger.slice("POST ".length);
  const lines = [
    "curl -X POST '" + location.origin + path + "?wait=true' \\",
    "  -H 'Content-Type: application/json' \\",
  ];
  if (state.protected) lines.push("  -H \"Authorization: Bearer $AIA_TOKEN\" \\");
  lines.push("  -d '{\"subject\": \"Hello\", \"message\": \"...\"}'");
  return code(lines.join("\n"), "the curl command");
}

function renderInspector(w, run, moved) {
  ensureInspector();
  const step = w.steps.find((s) => s.id === selected.node);
  const canEdit = state.editable && !w.problem;
  const fields = !canEdit ? null : selected.node === "trigger" ? w.edit : step ? step.edit : null;
  const hasForm = !!(fields && fields.length);
  let parts;
  if (selected.node === "trigger") parts = triggerDetails(w, run);
  else if (step) parts = stepDetails(w, step, run, hasForm);
  else if (run) parts = runDetails(w, run);
  else parts = workflowDetails(w);
  const [head, ...sections] = parts.flat(Infinity).filter(Boolean);
  const box = $("inspector");
  const top = box.scrollTop;
  insp.head.replaceWith(head);
  insp.head = head;
  showForm(w, hasForm ? selected.node : null, fields);
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
      el("span", { class: "tag" }, KIND_LABEL[s.kind])))));
}

function workflowDetails(w) {
  return [
    header("workflow", "workflow", w.name, "Workflow"),
    section("Definition", pairs([
      ["File", el("span", { class: "mono" }, w.path)],
      ["Trigger", el("span", { class: "mono" }, w.trigger || "none: the server does not run it")],
      ["Steps", w.steps.length],
      ["Tests", w.tests + (w.tests ? ", run with aia test" : "")],
      state.commit ? ["Version", el("span", { class: "mono" }, state.commit)] : null,
    ])),
    section("Steps", outline(w), el("p", { class: "muted small" }, "A step with an IF condition is skipped when the condition is false."),
      state.editable && !w.problem ? el("p", { class: "muted small" }, "Select the trigger or a step to edit it. Saved changes are written to " + w.path + ".") : null),
    isWebhook(w) ? section("Try it", example(w), el("p", { class: "muted small" }, "With ?wait=true the answer is the finished run. Without it the server answers at once and the run appears in the list.")) : null,
  ];
}

// waterfall draws each step of a run on a shared time axis.
function waterfall(w, run) {
  let offset = 0;
  const total = Math.max(1, run.steps.reduce((sum, s) => sum + s.ms, 0));
  return el("ol", { class: "waterfall" }, w.steps.map((step, i) => {
    const done = run.steps.find((s) => s.id === step.id);
    const status = done ? done.status : "pending";
    const start = offset;
    if (done) offset += done.ms;
    const ran = done && (status === "ok" || status === "failed" || status === "running");
    return el("li", {}, el("button", { type: "button", class: "kind-" + step.kind + (editedNodes(w).has(step.id) ? " edited" : ""), "data-node": step.id, onclick: () => go({ node: step.id }) },
      el("span", { class: "n" }, i + 1),
      el("span", { class: "name mono" }, step.id),
      el("span", { class: "lane " + status }, ran
        ? el("span", { class: "bar " + status, css: { left: (start / total) * 100 + "%", width: Math.max(1.5, (done.ms / total) * 100) + "%" } })
        : null),
      el("span", { class: "ms " + status }, ran ? duration(done.ms) : status === "pending" ? "not reached" : status)));
  }));
}

function runDetails(w, run) {
  return [
    header("workflow", "run", run.run, "Run of " + run.workflow),
    section("Result",
      statusPanel(run.status, run.status !== "running" ? el("div", { class: "muted" }, "took " + duration(run.ms) + ", started " + when(run.started)) : null),
      run.error ? el("pre", { class: "error" }, run.error) : null),
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
  let how = "This workflow has no trigger, so the server does not run it. Run it with aia run.";
  if (isWebhook(w)) how = "A POST request to " + w.trigger.slice(5) + " with a JSON body starts the workflow. Steps read the body as trigger.body.";
  else if (w.trigger) how = "The workflow starts on the schedule " + w.trigger.slice(5) + ". Steps can read the start time as trigger.time.";
  return [
    header("trigger", "trigger", w.trigger || "manual", KIND_NAMES.trigger, true),
    run && run.trigger !== undefined ? section("Payload in this run", jsonView(run.trigger)) : null,
    section("How it starts", el("p", {}, how)),
    isWebhook(w) ? section("Try it", example(w)) : null,
  ];
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
        status === "pending" ? el("div", { class: "muted" }, run.status === "failed" ? "An earlier step failed first." : "The run did not get this far.") : null),
      done && done.detail ? el("p", { class: "detail mono" }, done.detail) : null,
      done && done.error ? el("pre", { class: "error" }, done.error) : null) : null,
    step.questions && output ? section("Answers", step.questions.map((q) => question(q, output[q.name]))) : null,
    step.questions && !output && !hasForm ? section("Questions", step.questions.map((q) => question(q, null))) : null,
    output !== null && !step.questions ? section("Output", jsonView(output)) : null,
    hasForm ? null : section("Runs when",
      step.when ? el("pre", { class: "cond-pre" }, step.when) : el("p", { class: "muted" }, "Always, once the steps before it have finished."),
      el("p", { class: "muted small" }, "Step " + order + " of " + w.steps.length + " in file order.")),
    // What a component does is set in its own file, so it is shown, not edited.
    step.component ? section("Component",
      pairs(step.fields.map(([label, value]) => [label, el("span", { class: "mono" }, value)])),
      el("p", { class: "muted small" }, "Written once in " + step.uses + " and shared by every step that uses it. This step only chooses its input.")) : null,
    step.fields.length && !hasForm && !step.component ? section("Settings", pairs(step.fields.map(([label, value]) => [label, el("span", { class: "mono" }, value)]))) : null,
    step.reads.length ? section("Reads from", el("div", { class: "chips" }, step.reads.map((name) =>
      el("button", { type: "button", class: "pill link mono", title: "Show " + name, onclick: () => go({ node: name }) }, name)))) : null,
  ];
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
const drafts = {};      // workflow -> { fields: { pathKey: { path, value, node, label } }, prompts: { step: text } }
const known = {};       // workflow -> the rev this page last saw
const ui = {};          // workflow -> { saving, notice, conflicts, armed }
const promptCache = {}; // "workflow|step" -> { state, file, text, rev, wfRev, inflight, error }
const changesCache = {}; // workflow -> { state, data, error, stamp, inflight }
let changesEpoch = 0;   // bumped by every save, so the diff is fetched again
const insp = { head: null, body: null, formslot: null, rest: null, bar: null };

const pathKey = (path) => JSON.stringify(path);
const draftsOf = (name) => drafts[name] || (drafts[name] = { fields: {}, prompts: {} });
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
  (w.edit || []).forEach((f) => map.set(pathKey(f.path), { field: f, node: "trigger" }));
  w.steps.forEach((s) => (s.edit || []).forEach((f) => map.set(pathKey(f.path), { field: f, node: s.id })));
  return map;
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
}

function dirtyCount(name) {
  const d = drafts[name];
  return d ? Object.keys(d.fields).length + Object.keys(d.prompts).length : 0;
}
function dirtyNodes(name) {
  const d = drafts[name];
  if (!d) return [];
  return [...new Set([...Object.values(d.fields).map((e) => e.node), ...Object.keys(d.prompts)])];
}
const editedNodes = (w) => new Set(state.editable ? dirtyNodes(w.name) : []);
const anyDirty = () => state.editable && state.workflows.some((w) => dirtyCount(w.name) > 0);

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
}

function changedOutside(name, prev, w) {
  const st = uiOf(name), d = draftsOf(name);
  const was = prev ? fieldIndex(prev) : new Map(), now = fieldIndex(w);
  let kept = Object.keys(d.prompts).length;
  for (const [k, e] of Object.entries(d.fields)) {
    const n = now.get(k);
    if (!n) { delete d.fields[k]; continue; }
    kept++;
    const o = was.get(k);
    if (o && o.field.value !== n.field.value) st.conflicts[k] = n.field.value;
  }
  if (st.notice && st.notice.kind === "conflict") st.notice = { ...st.notice, kept };
  else if (kept > 0 || (view && view.w.name === name && selected.node)) st.notice = { kind: "external", kept };
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
    box = el("input", { id, type: "text", class: "in" + (mono ? " mono" : ""), spellcheck: "false", autocomplete: "off", autocapitalize: "off" });
  }
  c.input = box;

  const hintId = id + "-hint";
  const hintText = field.label === "Run only when" ? "Empty means the step always runs."
    : field.optional ? "Optional. Leave it empty to remove it from the file." : null;
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
    else d.fields[c.key] = { path: field.path, value, node: c.node, label: field.label };
    if (!(c.kind === "prompt" ? d.prompts[c.node] !== undefined : d.fields[c.key])) delete st.conflicts[c.key];
    if (st.notice && (st.notice.kind === "saved" || st.notice.kind === "external")) st.notice = null;
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
  f.root = el("section", { class: "editform" }, el("h2", {}, "Settings"), body);
  return f;
}

function showForm(w, nodeId, fields) {
  if (!nodeId) {
    if (form) { form = null; insp.formslot.replaceChildren(); }
    return;
  }
  const key = [w.name, nodeId, fields.map((f) => [pathKey(f.path), f.kind, f.label, f.group || "", (f.options || []).join(",")].join("~")).join("|")].join("##");
  if (form && form.key === key) {
    form.fields = fields;
  } else {
    form = buildForm(w, nodeId, fields, key);
    insp.formslot.replaceChildren(form.root);
  }
  syncForm(w);
}

// syncForm brings the controls up to date with the data and the drafts without
// touching a control whose text already matches.
function syncForm(w) {
  if (!form || form.workflow !== w.name) return;
  form.controls.forEach((c, i) => {
    c.field = form.fields[i];
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
  renderSide(w);
  $("changes").replaceChildren(...(state.editable ? [changesChip()] : []));
}

// flagFor marks a workflow in the list on the left: a file problem, or unsaved changes.
function flagFor(x) {
  if (!state.editable) return null;
  if (x.problem) return el("span", { class: "flag problem", title: "The file has a problem: " + x.problem, role: "img", "aria-label": "The file has a problem" }, "!");
  const n = dirtyCount(x.name);
  return n ? el("span", { class: "flag edit", title: plural(n, "unsaved change"), role: "img", "aria-label": plural(n, "unsaved change") }) : null;
}

function changesChip() {
  const uncommitted = /uncommitted/.test(state.commit || "");
  return el("button", {
      type: "button", class: "chip changes" + (selected.changes ? " on" : ""), "data-focus": "changes",
      "aria-pressed": selected.changes ? "true" : "false",
      title: "Show what changed in the workflow files, as git sees it",
      onclick: () => go({ changes: !selected.changes }),
    },
    uncommitted ? el("span", { class: "dot edit", title: "There are uncommitted changes" }) : null,
    "Changes");
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
  b.root = el("div", { class: "savebar", hidden: true }, b.slot, el("div", { class: "barrow" }, b.status, b.discard, b.save));
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
  const b = insp.bar;
  if (!b || !view) return;
  const w = view.w, st = uiOf(w.name), n = dirtyCount(w.name);
  const on = state.editable && !w.problem && (form || n > 0 || st.notice);
  b.root.hidden = !on;
  if (!on) return;
  const names = dirtyNodes(w.name);
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
  updateBar();
}

// noticeView says in plain words what happened to a save, near the form.
function noticeView(n, w) {
  const tone = { saved: "ok", invalid: "bad", error: "bad", network: "bad", conflict: "warn", external: "warn" }[n.kind];
  const text = [];
  let title = "";
  if (n.kind === "saved") {
    title = "Saved";
    text.push(el("p", {}, "Written to ", n.files.map((f, i) => [i ? ", " : "", el("span", { class: "mono" }, f)])), el("p", {},
      "These changes are saved in the file but not committed. ",
      el("button", { type: "button", class: "linkbtn", onclick: () => go({ changes: true }) }, "See what changed")));
  } else if (n.kind === "invalid") {
    title = "Not saved. The file was left as it was.";
    if (n.saved && n.saved.length) text.push(el("p", {}, "Already written: ", n.saved.join(", "), "."));
    if (n.problems && n.problems.length) {
      text.push(el("p", {}, "Fix this and save again:"), el("ul", { class: "problems" }, n.problems.map((p) => problemItem(p, w))));
    } else text.push(el("p", {}, n.error || "The server did not accept the change."));
  } else if (n.kind === "conflict") {
    title = "The file changed on disk, so nothing was saved";
    text.push(el("p", {}, (n.what === "prompt" ? "The prompt file was changed after this page read it." : "The workflow file was changed after this page read it.")
      + " The form now shows what is in the file" + (n.kept ? ", with your " + plural(n.kept, "unsaved edit") + " on top." : ".")),
      el("p", {}, n.kept ? "Check your edits, then save again." : "Nothing of yours was lost."));
  } else if (n.kind === "external") {
    title = "The file was changed outside this page";
    text.push(el("p", {}, "The form shows what is in the file now" + (n.kept ? ", with your " + plural(n.kept, "unsaved edit") + " kept on top." : ".")));
  } else if (n.kind === "network") {
    title = "Could not reach the server";
    text.push(el("p", {}, "Nothing was lost: your edits are still here. Try saving again when it is back."));
  } else {
    title = "Not saved";
    text.push(el("p", {}, n.error || "The server answered " + n.status + "."));
  }
  const close = el("button", { type: "button", class: "x small", "aria-label": "Dismiss this message", title: "Dismiss" }, icon("close", 13));
  close.addEventListener("click", () => dismissNotice(w.name));
  return el("div", { class: "notice " + tone, role: tone === "ok" ? "status" : "alert" },
    el("div", { class: "nhead" }, el("strong", {}, title), close), text);
}

// A problem from the server reads like: step "summarize": when: Syntax error.
function problemItem(p, w) {
  const m = /^step "([^"]+)"/.exec(p.message || "");
  const node = m && w.steps.some((s) => s.id === m[1]) ? m[1] : null;
  return el("li", {},
    p.line ? el("span", { class: "ln" }, "Line " + p.line) : null,
    el("span", { class: "msg" }, p.message),
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

function failureOf(r, what) {
  if (r.status === 422) return { kind: "invalid", error: r.body.error, problems: r.body.problems || [] };
  if (r.status === 409) return { kind: "conflict", what };
  return { kind: "error", status: r.status, error: r.body.error };
}

async function saveCurrent() {
  if (!view || !state.editable) return;
  const name = view.w.name, st = uiOf(name);
  const w = workflowByName(name);
  if (st.saving || !w || w.problem) return;
  const d = draftsOf(name);
  const fields = Object.values(d.fields).map((e) => ({ path: e.path, value: e.value }));
  const texts = Object.entries(d.prompts).map(([step, text]) => ({ step, text }));
  if (!fields.length && !texts.length) return;

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
  } catch (err) {
    failure = { kind: "network" };
  }
  st.saving = false;
  if (failure) {
    if (files.length) failure.saved = files;
    st.notice = failure;
  } else st.notice = { kind: "saved", files };

  if (failure && failure.kind === "conflict" && failure.what === "workflow") {
    st.notice = { ...failure, kept: Object.keys(d.fields).length + Object.keys(d.prompts).length };
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
  drafts[name] = { fields: {}, prompts: {} };
  st.conflicts = {};
  st.notice = null;
  if (form) syncForm(workflowByName(name));
  draftChanged(name, true);
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

window.addEventListener("hashchange", () => {
  readAddress();
  if (!selected.run) detail = null;
  refresh();
});
window.addEventListener("keydown", (event) => {
  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "s" && event.target.closest && event.target.closest(".editform")) {
    event.preventDefault();
    saveCurrent();
    return;
  }
  if (event.key !== "Escape") return;
  // In a form field Escape only leaves the field.
  if (event.target.closest && event.target.closest(".editform")) { event.target.blur(); return; }
  if (selected.changes) go({ changes: false });
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

readAddress();
refresh();
setInterval(refresh, REFRESH_MS);

window.addEventListener("beforeunload", (event) => {
  if (!anyDirty()) return;
  event.preventDefault();
  event.returnValue = "";
});
