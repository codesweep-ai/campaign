#!/usr/bin/env node
// Check C — consumer parity for the dispatch viewer rewrite.
//
// For every rendered archive (old viewer vs new viewer), compares as sets:
//   - lane labels and the event count per lane
//   - the findings list (code + severity)
//   - the selection payload (inspector text) of every event
//   - theme-cycling end states (data-theme, persisted mode, button label)
//   - the log-toggle end states (body.blind, log list visibility)
// and runs @codesweep-ai/ui/testing's snapshot comparison for the record.
//
// usage: node scripts/check-c.mjs <old-dir> <new-dir> <out-dir> [archive ...]
// <old-dir>/<new-dir> hold <name>.html renders of the same archives.
// Exit 0 when every compared set matches; 1 otherwise.
import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { compareSnapshots, renderAndSnapshot } from "@codesweep-ai/ui/testing";

const [oldDir, newDir, outDir, ...only] = process.argv.slice(2);
if (!oldDir || !newDir || !outDir) {
  console.error("usage: node scripts/check-c.mjs <old-dir> <new-dir> <out-dir> [archive ...]");
  process.exit(2);
}

const setEq = (a, b) => {
  const sa = new Set(a), sb = new Set(b);
  return {
    equal: sa.size === sb.size && [...sa].every((x) => sb.has(x)),
    onlyOld: [...sa].filter((x) => !sb.has(x)).sort(),
    onlyNew: [...sb].filter((x) => !sa.has(x)).sort(),
  };
};

// One prepare hook does all interacting and stashes the extraction in a
// closure; renderAndSnapshot then captures the settled page for the record.
async function probe(url) {
  const probe = { selections: {}, theme: [], logToggle: [] };
  const snapshot = await renderAndSnapshot(url, {
    settleMs: 150,
    prepare: async (page) => {
      // Log-derived marks hide until the overlay is on; reveal them so every
      // square is clickable, exactly as a user would.
      await page.evaluate(() => {
        const box = document.querySelector("#showlog");
        if (box && !box.checked) box.click();
      });
      probe.structure = await page.evaluate(() => ({
        // EventLanes DOM: lane labels in the gutter, events in the flat census.
        lanes: [...document.querySelectorAll("#tl [data-event-lane-label]")].map((l) => ({
          label: (l.textContent || "").trim(),
          cls: l.className.replace(/\s+/g, " "),
          events: document.querySelectorAll(
            `#tl [data-event-index][data-event-lane="${(l.textContent || "").trim()}"]`,
          ).length,
        })),
        issues: [...document.querySelectorAll("#issues [data-table-row]")].map((d) => ({
          severity: d.querySelector("[data-severity]")?.getAttribute("data-severity") || "",
          code: d.querySelector(".code")?.textContent || "",
        })),
        issueCount: document.querySelector("#issue-count")?.textContent || "",
        banner: (document.querySelector("#banner")?.innerText || "").trim(),
        tlHidden:
          getComputedStyle(document.querySelector("#tl-card") ?? document.body).display === "none",
        legendRows: [...document.querySelectorAll("#legend .lrow")].map((r) =>
          (r.innerText || "").trim().replace(/\s+/g, " "),
        ),
        squares: [...document.querySelectorAll("[data-event-index]")].map((s) => ({
          i: s.getAttribute("data-event-index"),
          cls: s.getAttribute("data-event-kind") || "",
          title: s.getAttribute("aria-label") || "",
        })),
      }));
      // Selection payload of every event. Events are reached the contract's
      // way: focus the listbox, Home, then ArrowRight forward (ids ascend)
      // and Enter — canvas pixels and the clipped census are not clickable.
      const ids = await page.$$eval("[data-event-index]", (sqs) =>
        [
          ...new Set(
            sqs
              .map((s) => s.getAttribute("data-event-index")),
          ),
        ].sort((a, b) => +a - +b),
      );
      await page.focus("#tl [data-event-lanes-scroller]");
      await page.keyboard.press("Home");
      for (let k = 0; k < ids.length; k++) {
        const i = ids[k];
        if (k > 0) await page.keyboard.press("ArrowRight");
        await page.keyboard.press("Enter");
        probe.selections[i] = await page.evaluate(() => {
          const lines = (document.querySelector("#inspector")?.innerText || "")
            .split("\n")
            .map((s) => s.trim().replace(/\s+/g, " "))
            .filter(Boolean);
          return [...new Set(lines)].sort();
        });
      }
      // Theme cycling: boot state, then one click per mode transition.
      const themeState = () =>
        page.evaluate(() => ({
          mode: (() => { try { return localStorage.getItem("dispatch-viewer-theme"); } catch { return null; } })(),
          resolved: document.documentElement.getAttribute("data-theme"),
          label: document.querySelector('[data-component="ThemeToggle"]')?.getAttribute("aria-label") || "",
        }));
      probe.theme.push(await themeState());
      for (let k = 0; k < 3; k++) {
        await page.click('[data-component="ThemeToggle"]');
        probe.theme.push(await themeState());
      }
      // Log toggle end states: currently on (probe began), so off then on.
      // When the timeline card is hidden (clobbered mtimes) the checkbox is
      // not clickable; record that as the end state instead.
      const toggleVisible = await page.evaluate(() => {
        const box = document.querySelector("#showlog");
        return box ? box.checkVisibility() : false;
      });
      if (!toggleVisible) {
        probe.logToggle.push({ hidden: true });
      } else {
        await page.click("#showlog");
        probe.logToggle.push(await page.evaluate(() => ({
          blind: document.body.classList.contains("blind"),
          // Card unmounts its children when collapsed, so #log-list can be
          // absent rather than hidden now that the pane is collapsible. Absent
          // means not shown; without the guard this threw a TypeError instead
          // of reporting a result.
          logShown: (() => { const el = document.querySelector("#log-list");
            return !!el && getComputedStyle(el).display !== "none"; })(),
        })));
        await page.click("#showlog");
        probe.logToggle.push(await page.evaluate(() => ({
          blind: document.body.classList.contains("blind"),
          // Card unmounts its children when collapsed, so #log-list can be
          // absent rather than hidden now that the pane is collapsible. Absent
          // means not shown; without the guard this threw a TypeError instead
          // of reporting a result.
          logShown: (() => { const el = document.querySelector("#log-list");
            return !!el && getComputedStyle(el).display !== "none"; })(),
        })));
      }
    },
  });
  return { probe, snapshot };
}

await mkdir(outDir, { recursive: true });
const names = only.length
  ? only
  : ["healthy", "clobbered", "name-mismatch", "create-phase"];

let failures = 0;
const report = {};
for (const name of names) {
  const oldUrl = "file://" + path.resolve(oldDir, name + ".html");
  const newUrl = "file://" + path.resolve(newDir, name + ".html");
  const oldR = await probe(oldUrl);
  const newR = await probe(newUrl);

  const checks = {};
  checks.lanes = setEq(
    oldR.probe.structure.lanes.map((l) => `${l.label}|${l.cls}|${l.events}`),
    newR.probe.structure.lanes.map((l) => `${l.label}|${l.cls}|${l.events}`),
  );
  checks.findings = setEq(
    oldR.probe.structure.issues.map((i) => `${i.severity}|${i.code}`),
    newR.probe.structure.issues.map((i) => `${i.severity}|${i.code}`),
  );
  checks.issueCount = { equal: oldR.probe.structure.issueCount === newR.probe.structure.issueCount };
  checks.banner = { equal: oldR.probe.structure.banner === newR.probe.structure.banner };
  checks.tlHidden = { equal: oldR.probe.structure.tlHidden === newR.probe.structure.tlHidden };
  checks.legend = setEq(oldR.probe.structure.legendRows, newR.probe.structure.legendRows);
  checks.squares = setEq(
    oldR.probe.structure.squares.map((s) => `${s.i}|${s.cls}|${s.title}`),
    newR.probe.structure.squares.map((s) => `${s.i}|${s.cls}|${s.title}`),
  );
  const allIds = [...new Set([...Object.keys(oldR.probe.selections), ...Object.keys(newR.probe.selections)])].sort((a, b) => +a - +b);
  checks.selections = {};
  for (const i of allIds) {
    const cmp = setEq(oldR.probe.selections[i] || [], newR.probe.selections[i] || []);
    if (!cmp.equal) checks.selections[i] = cmp;
  }
  checks.selectionsEqual = { equal: Object.keys(checks.selections).length === 0 };
  checks.themeStates = { equal: JSON.stringify(oldR.probe.theme) === JSON.stringify(newR.probe.theme) };
  checks.logToggleStates = {
    equal: JSON.stringify(oldR.probe.logToggle) === JSON.stringify(newR.probe.logToggle),
  };

  const snap = compareSnapshots(oldR.snapshot, newR.snapshot, 0.02);
  const ok = Object.entries(checks)
    .filter(([k]) => !["selections"].includes(k))
    .every(([, v]) => v.equal);
  if (!ok) failures++;
  report[name] = {
    ok,
    checks,
    snapshot: {
      equal: snap.equal,
      screenshotRatio: snap.screenshot.ratio,
      textOnlyOld: snap.text.onlyLeft.length,
      textOnlyNew: snap.text.onlyRight.length,
      semanticsOnlyOld: snap.semantics.onlyLeft.length,
      semanticsOnlyNew: snap.semantics.onlyRight.length,
    },
  };
  console.log(
    `${name}: ${ok ? "PASS" : "FAIL"} (lanes ${checks.lanes.equal}, findings ${checks.findings.equal}, selections ${checks.selectionsEqual.equal}, theme ${checks.themeStates.equal}, log ${checks.logToggleStates.equal}; screenshot ratio ${snap.screenshot.ratio.toFixed(4)})`,
  );
}
await writeFile(path.join(outDir, "check-c-report.json"), JSON.stringify(report, null, 2) + "\n");
console.log(`report written to ${path.join(outDir, "check-c-report.json")}`);
process.exit(failures ? 1 : 0);
