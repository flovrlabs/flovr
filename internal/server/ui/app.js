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
let selected = { workflow: null, run: null, node: null };
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
  selected = { workflow: found.w || null, run: found.r || null, node: found.n || null };
}

function go(change) {
  const next = { ...selected, ...change };
  let hash = "#/w/" + encodeURIComponent(next.workflow);
  if (next.run) hash += "/r/" + encodeURIComponent(next.run);
  if (next.node) hash += "/n/" + encodeURIComponent(next.node);
  if (location.hash !== hash) location.hash = hash;
}

async function getJSON(path) {
  const response = await fetch(path, { headers: { Accept: "application/json" } });
  if (!response.ok) throw new Error(path + " answered " + response.status);
  return response.json();
}

async function refresh() {
  const live = $("live");
  try {
    state = await getJSON("/api/state");
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
    selected = { workflow: state.workflows.length ? state.workflows[0].name : null, run: null, node: null };
    detail = null;
  }
  // Rebuilding the page drops the reader's focus and text selection, so it is
  // only done when there is something new to show.
  const next = JSON.stringify([state, selected, detail]);
  if (next === shown) return;
  shown = next;

  const w = state.workflows.find((x) => x.name === selected.workflow);
  const key = [selected.workflow, selected.run, selected.node].join("|");
  const moved = key !== lastKey;
  lastKey = key;

  renderTop(w);
  if (!w) {
    view = null;
    $("side").replaceChildren();
    $("inspector").replaceChildren();
    $("canvas").replaceChildren(el("p", { class: "none" }, "No workflows are loaded."));
    return;
  }
  const run = detail && detail.run === selected.run && detail.workflow === w.name ? detail : null;
  view = { w, run };
  renderSide(w);
  renderCanvas(moved);
  renderInspector(w, run, moved);
}

// ---- top bar ----

function renderTop(w) {
  $("commit").replaceChildren(...(state.commit ? [icon("branch", 13), el("span", {}, state.commit)] : []));

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
    el("p", { class: "note" }, "Read-only. Workflows are files in git: edit the YAML, commit it, restart the server."));
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
    return el("div", {
        class: "slot" + (animate ? " enter" : ""),
        css: { left: node.x + "px", top: node.y + "px", width: plan.nodeW + "px", height: CARD_H + "px", animationDelay: (animate ? i * 70 : 0) + "ms" },
      },
      el("button", {
          class: "card shape-" + node.kind + " kind-" + node.kind + (status ? " " + status : "") + (node.parent ? " has-in" : "") + (node.hasOut ? " has-out" : ""),
          type: "button", "data-node": node.id,
          "aria-pressed": selected.node === node.id ? "true" : "false", "aria-label": label,
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
    run ? runBar(run) : el("div", { class: "hint" },
      el("span", {}, "Steps run top to bottom in the order numbered."),
      el("span", {}, "Pick a run on the left to see what happened.")),
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
  const step = w.steps.find((s) => s.id === selected.node);
  let parts;
  if (selected.node === "trigger") parts = triggerDetails(w, run);
  else if (step) parts = stepDetails(w, step, run);
  else if (run) parts = runDetails(w, run);
  else parts = workflowDetails(w);
  const [head, ...sections] = parts.flat(Infinity).filter(Boolean);
  const box = $("inspector");
  const top = box.scrollTop;
  box.replaceChildren(head, el("div", { class: "body" }, sections));
  box.scrollTop = moved ? 0 : top;
}

// outline lists the steps as buttons: the same choices as the drawing, for the
// keyboard and for a quick look at the shape of the workflow.
function outline(w) {
  return el("ol", { class: "outline" }, w.steps.map((s, i) =>
    el("li", {}, el("button", { type: "button", class: "kind-" + s.kind, onclick: () => go({ node: s.id }) },
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
    section("Steps", outline(w), el("p", { class: "muted small" }, "A step with an IF condition is skipped when the condition is false.")),
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
    return el("li", {}, el("button", { type: "button", class: "kind-" + step.kind, onclick: () => go({ node: step.id }) },
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

function stepDetails(w, step, run) {
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
    step.questions && !output ? section("Questions", step.questions.map((q) => question(q, null))) : null,
    output !== null && !step.questions ? section("Output", jsonView(output)) : null,
    section("Runs when",
      step.when ? el("pre", { class: "cond-pre" }, step.when) : el("p", { class: "muted" }, "Always, once the steps before it have finished."),
      el("p", { class: "muted small" }, "Step " + order + " of " + w.steps.length + " in file order.")),
    step.fields.length ? section("Settings", pairs(step.fields.map(([label, value]) => [label, el("span", { class: "mono" }, value)]))) : null,
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

window.addEventListener("hashchange", () => {
  readAddress();
  if (!selected.run) detail = null;
  refresh();
});
window.addEventListener("keydown", (event) => {
  if (event.key !== "Escape") return;
  if (selected.node) go({ node: null });
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

readAddress();
refresh();
setInterval(refresh, REFRESH_MS);
