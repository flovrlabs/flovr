// Records the page for docs/media: makes a few runs from the examples'
// fixtures, records the main demo as frames, and takes the two pictures.
// record.sh runs it with a server already serving a copy of the examples.
//
// Environment: RECORD_URL (the server), FLOVR_PAGE_KEY, RECORD_SITE (the
// copy it serves), RECORD_WORK (where frames and pictures go) and
// RECORD_SINK_PORT (where the examples' notifications are sent).
import { execFileSync } from "node:child_process";
import { createServer } from "node:http";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { open, sleep } from "./cdp.mjs";

const url = process.env.RECORD_URL;
const key = process.env.FLOVR_PAGE_KEY;
const site = process.env.RECORD_SITE;
const work = process.env.RECORD_WORK;

// The pager and billing of triage-ticket: every request gets 200.
const sink = createServer((req, res) => { req.resume(); req.on("end", () => res.end()); });
await new Promise((r) => sink.listen(Number(process.env.RECORD_SINK_PORT), "127.0.0.1", r));

try {
  for (let i = 0; i < 50; i++) {
    try { if ((await fetch(`${url}/healthz`)).ok) break; } catch {}
    await sleep(200);
  }

  // Runs to colour the drawing with, oldest first.
  const runs = [
    ["tickets", "triage-ticket", "refund"], ["tickets", "triage-ticket", "outage"],
    ["tickets", "triage-ticket", "urgent-refund"],
    ["moderate", "moderate-comment", "spam"], ["moderate", "moderate-comment", "criticism"],
  ];
  for (const [path, workflow, fixture] of runs) {
    const { body } = JSON.parse(readFileSync(join(site, "examples", workflow, "fixtures", `${fixture}.json`), "utf8"));
    const res = await fetch(`${url}/${path}?wait=true`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    if (!res.ok) throw new Error(`run of ${workflow} with ${fixture}: ${res.status}`);
    await sleep(1000);
  }

  // The main demo: open a workflow, look into a step, add a field to the
  // response, see it as a one-line git diff, and run the tests.
  const page = await open({ profile: join(work, "chrome") });
  try {
    await page.go(`${url}/?key=${key}#/workflows`, 1500);
    await page.cursor();
    await sleep(300);
    const stop = await page.record(join(work, "frames"));
    await sleep(1200);
    await page.click("a.hname", "moderate-comment");
    await sleep(1600);
    await page.click("button.card.kind-decide");
    await sleep(2600);
    await page.click("button.ned-x");
    await sleep(500);
    await page.click("button.card.kind-response");
    await sleep(1100);
    await page.click('input[placeholder="name"]');
    await page.type("checked_by");
    await page.click('input[placeholder^="a value"]');
    await page.type("flovr");
    await sleep(300);
    await page.click("button.btn.small", "Add a field");
    await sleep(1500);
    await page.click("button.ned-x");
    await sleep(500);
    await page.click("button.chip.changes");
    await sleep(3000);
    await page.click("button", "Back to the drawing");
    await sleep(600);
    await page.click("button.btn.primary", "Run tests");
    await sleep(3200);
    console.log(`demo: ${await stop()} frames`);
  } finally {
    await page.close();
  }
  execFileSync("git", ["-C", site, "checkout", "--", "examples"]);

  // The drawing coloured by the last run, in dark, and the editor of a step,
  // in light.
  for (const dark of [true, false]) {
    const page = await open({ profile: join(work, "chrome"), dark });
    try {
      await page.go(`${url}/?key=${key}#/w/triage-ticket`, 1500);
      if (dark) {
        await page.shot(join(work, "canvas.png"));
      } else {
        await page.eval(`document.querySelector("button.card.kind-ai").click()`);
        await sleep(1200);
        await page.shot(join(work, "editor.png"));
      }
    } finally {
      await page.close();
    }
    await sleep(500);
  }
} finally {
  sink.close();
}
