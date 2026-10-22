// A small driver for headless Chrome over the DevTools protocol: a drawn
// cursor that glides to what it clicks, typing, screenshots, and a
// screencast kept as frames. It needs nothing but Node 22 and Chrome.
import { spawn } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function chromePath() {
  if (process.env.CHROME) return process.env.CHROME;
  if (process.platform === "darwin") return "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
  return "google-chrome";
}

export async function open({ profile, width = 1120, height = 700, scale = 2, dark = true }) {
  rmSync(profile, { recursive: true, force: true });
  mkdirSync(profile, { recursive: true });
  // Port 0 lets Chrome pick a free port, which it writes to a file.
  const chrome = spawn(chromePath(), [
    "--headless=new", "--remote-debugging-port=0", `--user-data-dir=${profile}`,
    "--no-first-run", "--no-default-browser-check", "--hide-scrollbars", "--disable-gpu",
    `--window-size=${width},${height}`, "about:blank",
  ], { stdio: "ignore" });
  const portFile = join(profile, "DevToolsActivePort");
  let target;
  for (let i = 0; i < 100 && !target; i++) {
    await sleep(100);
    if (!existsSync(portFile)) continue;
    const port = readFileSync(portFile, "utf8").split("\n")[0];
    try {
      const list = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
      target = list.find((t) => t.type === "page");
    } catch {}
  }
  if (!target) {
    chrome.kill();
    throw new Error(`Chrome did not start; set CHROME to its path (tried ${chromePath()})`);
  }
  const ws = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((resolve, reject) => { ws.onopen = resolve; ws.onerror = reject; });
  let next = 0;
  const waiting = new Map();
  const listeners = [];
  ws.onmessage = (m) => {
    const msg = JSON.parse(m.data);
    if (msg.id && waiting.has(msg.id)) {
      const { resolve, reject } = waiting.get(msg.id);
      waiting.delete(msg.id);
      msg.error ? reject(new Error(msg.error.message)) : resolve(msg.result);
    } else if (msg.method) {
      for (const l of listeners) l(msg);
    }
  };
  const send = (method, params = {}) => new Promise((resolve, reject) => {
    const id = ++next;
    waiting.set(id, { resolve, reject });
    ws.send(JSON.stringify({ id, method, params }));
  });
  await send("Page.enable");
  await send("Runtime.enable");
  await send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: scale, mobile: false });
  await send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: dark ? "dark" : "light" }] });

  const page = {
    async eval(expr) {
      const r = await send("Runtime.evaluate", { expression: expr, awaitPromise: true, returnByValue: true });
      if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description || r.exceptionDetails.text);
      return r.result.value;
    },
    async go(url, settle = 900) {
      await send("Page.navigate", { url });
      await sleep(settle);
    },
    // where finds the centre of the first visible element that matches the
    // selector and, if given, holds the text.
    async where(selector, text = "") {
      for (let i = 0; i < 40; i++) {
        const p = await page.eval(`(() => {
          const el = [...document.querySelectorAll(${JSON.stringify(selector)})]
            .filter((e) => e.textContent.includes(${JSON.stringify(text)}))
            .find((e) => { const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0; });
          if (!el) return null;
          const r = el.getBoundingClientRect();
          return { x: r.x + r.width / 2, y: r.y + r.height / 2 };
        })()`);
        if (p) return p;
        await sleep(100);
      }
      throw new Error(`nothing on the page matches ${selector} ${text}`);
    },
    // cursor draws a pointer on the page, which headless Chrome does not.
    async cursor() {
      await page.eval(`(() => {
        if (document.getElementById("__cursor")) return;
        const c = document.createElement("div");
        c.id = "__cursor";
        Object.assign(c.style, { position: "fixed", left: "0", top: "0", width: "26px", height: "26px", zIndex: "2147483647",
          pointerEvents: "none", transform: "translate(${Math.round(width * 0.62)}px, ${Math.round(height * 0.72)}px)",
          transition: "transform 0.7s cubic-bezier(0.3, 0.7, 0.2, 1)" });
        c.innerHTML = '<svg width="26" height="26" viewBox="0 0 24 24"><path d="M5 3l14 8.2-6.1 1.4L10.2 19z" fill="#ffffff" stroke="#16180f" stroke-width="1.6" stroke-linejoin="round"/></svg>';
        document.body.appendChild(c);
      })()`);
    },
    async click(selector, text = "", pause = 250) {
      const p = await page.where(selector, text);
      await page.eval(`document.getElementById("__cursor").style.transform = "translate(${p.x - 5}px, ${p.y - 3}px)"`);
      await sleep(750);
      await page.eval(`(() => { const c = document.getElementById("__cursor").firstChild; c.style.transition = "transform 0.12s"; c.style.transform = "scale(0.82)"; setTimeout(() => c.style.transform = "", 140); })()`);
      await send("Input.dispatchMouseEvent", { type: "mouseMoved", x: p.x, y: p.y });
      await send("Input.dispatchMouseEvent", { type: "mousePressed", x: p.x, y: p.y, button: "left", clickCount: 1 });
      await send("Input.dispatchMouseEvent", { type: "mouseReleased", x: p.x, y: p.y, button: "left", clickCount: 1 });
      await sleep(pause);
    },
    async type(text, ms = 70) {
      for (const ch of text) {
        await send("Input.insertText", { text: ch });
        await sleep(ms);
      }
    },
    async shot(file) {
      const r = await send("Page.captureScreenshot", { format: "png" });
      writeFileSync(file, Buffer.from(r.data, "base64"));
    },
    // record keeps every frame the page paints until the function it
    // returns is called, which writes frames.txt: the frames, each shown
    // until the next, as ffmpeg's concat format reads them.
    async record(dir) {
      rmSync(dir, { recursive: true, force: true });
      mkdirSync(dir, { recursive: true });
      const frames = [];
      listeners.push((msg) => {
        if (msg.method !== "Page.screencastFrame") return;
        const { data, metadata, sessionId } = msg.params;
        const name = `f${String(frames.length).padStart(5, "0")}.jpg`;
        writeFileSync(join(dir, name), Buffer.from(data, "base64"));
        frames.push({ name, t: metadata.timestamp });
        send("Page.screencastFrameAck", { sessionId }).catch(() => {});
      });
      await send("Page.startScreencast", { format: "jpeg", quality: 95, everyNthFrame: 1 });
      return async (hold = 2.5) => {
        await send("Page.stopScreencast");
        await sleep(200);
        let list = "";
        frames.forEach((f, i) => {
          const d = i + 1 < frames.length ? frames[i + 1].t - f.t : hold;
          list += `file '${f.name}'\nduration ${Math.max(d, 0.001).toFixed(3)}\n`;
        });
        list += `file '${frames[frames.length - 1].name}'\n`;
        writeFileSync(join(dir, "frames.txt"), list);
        return frames.length;
      };
    },
    // close waits for Chrome to exit, so that the next page can use the
    // same profile.
    async close() {
      ws.close();
      const exited = new Promise((resolve) => chrome.once("exit", resolve));
      chrome.kill();
      await exited;
    },
  };
  return page;
}
