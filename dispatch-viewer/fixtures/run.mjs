#!/usr/bin/env node
// Behavioural fixtures for cs-dispatch-viewer — the oracle the run-2 rebuild
// on @codesweep-ai/ui is held to. README.md says what each check covers;
// expectations.json holds the measured values; selectors.mjs is the only file
// that knows the DOM.
//
// usage: node run.mjs [--viewer <cs-dispatch-viewer>]
//                     [--out <dir>] [--keep-out] [--strict] [--update]
//                     [--approve <dispatch-id> --reason "<one sentence>"]
//
// --update rewrites expectations.json from what was just measured. Frozen
// values are gated: changing a `keep` row's value or a `must-change` row's
// target requires --approve <dispatch-id> and --reason "<one sentence>"
// (both or neither), and the change is recorded on the row as an "approval"
// block. A write that changes neither needs no flags. A new row is never
// gated; it is disclosed with an "origin" block whose id is the --approve id
// when the flags are supplied and the member name otherwise, never a
// placeholder. Rows the run could not measure keep their existing value and
// approval verbatim; a row that can neither be measured nor carried forward
// is an error. Refusal writes nothing at all and exits non-zero.
//
// Every archive this suite renders is generated on the fly by archives.mjs, so
// the suite asserts only what any clone can reproduce and needs no data from
// outside the repository.
//
// env:   DISPATCH_FIXTURES_VIEWER   a built cs-dispatch-viewer (else `go build`)
//        DISPATCH_FIXTURES_BROWSER  Chrome/Chromium executable (then CHROME_BIN,
//                                   then PUPPETEER_EXECUTABLE_PATH)
//        DISPATCH_FIXTURES_AXE      axe.min.js (else the vendored vendor/axe-core)
//
// exit:  0 green · 1 a `keep` value changed (or, with --strict, a `must-change`
//        target is unmet / an input is missing) · 2 environment problem
import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { gzipSync } from "node:zlib";
import { writeSyntheticArchives, writeCorruptPage, writeWideFindingArchive, writeMarkdownArchive } from "./archives.mjs";
import { HERE, axeScript, fileUrl, launchBrowser, openPage } from "./browser.mjs";
import * as P from "./probes.mjs";
import { SEL } from "./selectors.mjs";

const REPO = path.resolve(HERE, "..", "..");
const SHELL = path.join(REPO, "dispatch-viewer", "internal", "cli", "shell", "viewer.html");
const EXPECTATIONS = path.join(HERE, "expectations.json");
// The member name is the fallback origin id for a new row written without
// --approve (the id must always name somebody; a placeholder names nobody).
const MEMBER = "campaign";
// Raised from 300_000 when the inspector's JSON branch moved from a
// hand-rolled regex highlighter to the shared CodeBlock component: using
// the design system is worth a bounded size penalty, and a budget squeaked
// under is a budget that fails on someone else's unrelated commit.
const SHELL_BUDGET = 500_000;

// ---------------------------------------------------------------- arguments
const args = process.argv.slice(2);
const flag = (name) => {
  const i = args.indexOf(name);
  if (i < 0) return null;
  const v = args[i + 1];
  if (!v || v.startsWith("--")) die(2, `${name} needs a value`);
  return v;
};
const has = (name) => args.includes(name);
if (has("--help") || has("-h")) {
  // The header comment above is the usage text.
  const src = readFileSync(new URL(import.meta.url), "utf8").split("\n");
  console.log(src.slice(1, src.indexOf("import { spawnSync } from \"node:child_process\";")).map((l) => l.replace(/^\/\/ ?/, "")).join("\n"));
  process.exit(0);
}
const opts = {
  viewer: flag("--viewer") || process.env.DISPATCH_FIXTURES_VIEWER || null,
  out: flag("--out"),
  keepOut: has("--keep-out") || !!flag("--out"),
  strict: has("--strict"),
  update: has("--update"),
  approve: flag("--approve"),
  reason: flag("--reason"),
};
if ((opts.approve === null) !== (opts.reason === null)) {
  die(2, "--approve and --reason are a pair: pass both or neither");
}
function die(code, msg) {
  console.error(`fixtures: ${msg}`);
  process.exit(code);
}

// ---------------------------------------------------------------- registry
// id → how to judge it. `status`/`target`/`note` are copied into
// expectations.json by --update; at run time the JSON is authoritative.
// `meets(value)` is the --strict predicate for must-change checks; for
// per-archive values it runs per archive.
const perArchive = (f) => (v) => Object.entries(v).map(([a, x]) => `${a} ${f(x)}`).join(" · ");
const CHECKS = [
  // A. structure
  { id: "CF-01", title: "lanes: count, labels in order (orchestrator first), kind, events per lane", status: "keep", perArchive: true,
    summary: perArchive((v) => `${v.length} lanes [${v.map((l) => `${l.label}:${l.events}`).join(" ")}]`) },
  { id: "CF-02", title: "marks: total squares, event kinds present, connectors", status: "keep", perArchive: true,
    summary: perArchive((v) => `${v.squares} sq, ${v.connectors} conn, kinds ${v.kinds.join("/")}`) },
  { id: "CF-03", title: "page head: campaign name, verdict text, meta; banner; timeline hidden", status: "keep", perArchive: true,
    summary: perArchive((v) => `${v.head.name} · ${v.head.verdict} · ${v.head.meta}${v.timelineHidden ? " · NO TIMELINE" : ""}`) },
  { id: "CF-04", title: "issues: count text and code:severity rows in order", status: "keep", perArchive: true,
    summary: perArchive((v) => `${v.rows.length} (${v.count || "none"})`) },
  { id: "CF-05", title: "legend rows and the section headers", status: "keep", perArchive: true,
    summary: perArchive((v) => `${v.legend.length} legend rows, sections ${v.sections.join("/")}`) },
  { id: "CF-06", title: "structure identical in the dark theme (CF-01..05 measured under ?theme=dark)", status: "keep", perArchive: true,
    summary: perArchive((v) => (v === true ? "same" : JSON.stringify(v).slice(0, 60))) },
  // B. interaction
  { id: "CF-10", title: "click square i → inspector shows its node/event/at rows, doc panels, accept↔reply link", status: "keep", perArchive: true,
    summary: perArchive((v) => (Array.isArray(v) ? `${v.length} events` : v)) },
  { id: "CF-11", title: "ArrowRight ×10 from a fresh page, log shown: selection index sequence", status: "keep", perArchive: true,
    summary: perArchive((v) => (Array.isArray(v) ? `[${v.join(",")}]` : v)) },
  { id: "CF-12", title: "ArrowRight walk with the log hidden: landings on hidden squares", status: "must-change", perArchive: true,
    target: "hiddenLandings is empty — stepping never selects a square the timeline does not show",
    meets: (v) => v === "no-timeline" || v.hiddenLandings.length === 0,
    summary: perArchive((v) => (v === "no-timeline" ? v : `${v.steps} steps, ${v.hiddenLandings.length} hidden [${v.hiddenLandings.slice(0, 8).join(",")}${v.hiddenLandings.length > 8 ? ",…" : ""}]`)) },
  { id: "CF-13", title: "ArrowLeft ×10 from the last square, log shown: selection index sequence", status: "keep", perArchive: true,
    summary: perArchive((v) => (Array.isArray(v) ? `[${v.join(",")}]` : v)) },
  { id: "CF-14", title: "click issue row k → the timeline selection jumps to its evidence event", status: "keep", perArchive: true,
    summary: perArchive((v) => (Array.isArray(v) ? `${v.filter((s) => !s.endsWith("->null")).length}/${v.length} jump` : v)) },
  { id: "CF-15", title: "show-orchestrator-log toggle: log list/notice visibility, log row count, squares revealed", status: "keep", perArchive: true,
    summary: perArchive((v) => (v === "no-timeline" ? v : `${v.on.logRows} log rows, squares ${v.off.visibleSquares}→${v.on.visibleSquares}`)) },
  { id: "CF-16", title: "rendered/raw segment: hidden until a selection; raw shows the artifact, rendered the doc panels", status: "keep", perArchive: true,
    summary: perArchive((v) => (v === "no-timeline" ? v : `ev ${v.event}: rendered ${v.rendered?.panels.join("+")} · raw ${v.raw?.panels.join("+")}`)) },
  { id: "CF-17", title: "theme toggle cycles and persists (localStorage dispatch-viewer-theme), OS light, empty storage", status: "keep",
    summary: (v) => ["initial", "click1", "reloadAfterClick1", "click2", "click3"].map((k) => `${v[k].attr}/${v[k].stored}`).join(" → ") },
  { id: "CF-18", title: "?theme= honoured on load and not persisted; stored mode beats the OS scheme", status: "keep",
    summary: (v) => Object.entries(v).map(([k, s]) => `${k}=${s.attr}/${s.stored}`).join(" ") },
  { id: "CF-19", title: "corrupt payload: one error surface, app does not mount, no page errors", status: "must-change",
    target: "surfaces === 1 (banner or toast, not both); pageErrors empty",
    meets: (v) => v.surfaces === 1 && v.pageErrors.length === 0 && !v.mounted,
    summary: (v) => `${v.surfaces} surfaces (${v.banners} banner, ${v.toasts} toast), ${v.pageErrors.length} page errors` },
  // C. accessibility
  { id: "CF-20", title: "axe-core violations by rule, per theme × state (load, log shown + event 5 selected)", status: "must-change", perArchive: true,
    target: "no serious or critical violations in any theme/state",
    meets: (v) => Object.values(v).every((s) => Object.values(s).every((r) => !["serious", "critical"].includes(r.impact))),
    summary: perArchive((v) => Object.entries(v).map(([s, rules]) => `${s}:${Object.entries(rules).map(([r, x]) => `${r}×${x.nodes}`).join(",") || "clean"}`).join(" ")) },
  // D. keyboard
  { id: "CF-30", title: "first 20 Tab stops from a fresh page", status: "must-change", perArchive: true,
    target: "a timeline square and an issue row are reached within the first 20 stops",
    meets: (v) => v.some((s) => s.includes(":square")) && v.some((s) => s.includes(":issue-row")),
    summary: perArchive((v) => `${new Set(v).size} distinct [${[...new Set(v)].slice(0, 5).join(", ")}…]`) },
  { id: "CF-31", title: "squares focusable (tabIndex ≥ 0)", status: "must-change", perArchive: true,
    // Amended (d005): per-square tabIndex contradicts the accepted EventLanes
    // listbox contract (one Tab stop, non-tabbable options, aria-activedescendant).
    target: "the timeline exposes exactly one Tab stop, and every visible event is reachable from it by keyboard (Home, then ArrowRight to the end)",
    meets: (v) => v.tabStops === 1 && v.total > 0 && v.steps === v.total && v.missing.length === 0,
    summary: perArchive((v) => (v.tabStops === undefined
      ? `${v.focusable}/${v.total}`
      : `${v.tabStops} tab stop, ${v.steps}/${v.total} reached${v.missing.length ? `, missing ${v.missing.length}` : ""}`)) },
  { id: "CF-32", title: "issue rows under the roving tab stop: one row in the Tab order, arrows/Home/End reach every row", status: "must-change", perArchive: true,
    // Amended (U1): Table moved from per-row tab stops to a roving tab stop
    // with ArrowUp/ArrowDown/Home/End between rows — "every row focusable"
    // (12 tab stops on a 12-row table) became exactly one.
    target: "exactly one issue-row Tab stop; ArrowDown/ArrowUp walk it through every row; Home/End jump to the first/last",
    meets: (v) => v.tabStops === 1 && v.total > 0 && v.reached === v.total && v.home === "0" && v.end === String(v.total - 1),
    summary: perArchive((v) => (v.tabStops === undefined
      ? `${v.focusable}/${v.total}`
      : `${v.tabStops} tab stop, ${v.reached}/${v.total} reached`)) },
  { id: "CF-33", title: "Escape clears the selection", status: "must-change", perArchive: true,
    target: "afterEscape === null", meets: (v) => v.afterEscape === null,
    summary: perArchive((v) => `sel ${v.start} → ${v.afterEscape}`) },
  { id: "CF-34", title: "Home/End select the first/last event", status: "must-change", perArchive: true,
    target: "afterHome === 0, afterEnd === last", meets: (v) => v.afterHome === 0 && v.afterEnd === v.last,
    summary: perArchive((v) => `Home→${v.afterHome} End→${v.afterEnd} (last ${v.last})`) },
  { id: "CF-35", title: "clicking a square moves keyboard focus to it", status: "must-change", perArchive: true,
    // Amended (d005): DOM focus stays on the listbox; the active descendant follows.
    target: "after selecting an event, aria-activedescendant names that event's option and that option carries aria-selected=\"true\"",
    meets: (v) => v.activeDescendantFollows === true,
    summary: perArchive((v) => String(v.activeDescendantFollows ?? v.focusFollowsSelection)) },
  // E. size
  { id: "CF-40", title: "viewer.html (the shell) bytes and gzip bytes", status: "keep", budget: { bytes: SHELL_BUDGET },
    summary: (v) => `${v.bytes} B (gzip ${v.gzip}) ≤ ${SHELL_BUDGET}` },
  { id: "CF-41", title: "rendered page bytes, gzip, payload; shell share within budget", status: "keep", perArchive: true, budget: { shellBytes: SHELL_BUDGET },
    summary: perArchive((v) => `${v.bytes} B (gzip ${v.gzip}, payload ${v.payloadBytes})`) },
  // G. layout
  { id: "CF-60", title: "the issues table stays inside its card at a 600 px viewport (wide-finding archive)", status: "keep",
    summary: (v) => (v.contained ? `contained, ${v.pageErrors} page errors` : `NOT contained: ${JSON.stringify(v)}`) },
  // H. markdown
  { id: "CF-61", title: "rendered markdown drops javascript:/data: links (empty href), keeps an ordinary link, and renders tables", status: "keep",
    summary: (v) => (v.panel === false
      ? "no md panel"
      : `${v.unsafeLinks} unsafe, ${v.droppedLinks} dropped, ${v.safeLinks} safe, ${v.tables} tables, ${v.literalPipes} pipes, ${v.pageErrors} errors`) },
  // F. hygiene
  { id: "CF-50", title: "no external requests on file://, no page errors, no console errors (all probe pages)", status: "keep", perArchive: true,
    summary: perArchive((v) => `${v.requests.length} req, ${v.pageErrors.length} err, ${v.consoleErrors.length} console`) },
  { id: "CF-51", title: "<script> elements in the rendered page (boot, run-data, module)", status: "keep", perArchive: true,
    summary: perArchive((v) => String(v)) },
];
const NOTES = {
  "CF-06": "structure is theme-independent; the value is `true` per archive, else the first differing field",
  "CF-10": "one line per event: index|selected index|kv rows|doc panel kinds|linked square; measured with the log shown",
  "CF-12": "baseline: the arrow handler steps through every event, including log-lane events the timeline hides (CP-08)",
  "CF-14": "null: the row has no evidence event to jump to (no dispatch)",
  "CF-16": "the first reply square; rendered = md/json panels, raw = the artifact verbatim",
  "CF-17": "the toggle cycles system → light → dark → system; the stored value is the mode, the attribute the resolved theme",
  "CF-18": "`?theme=system` with a stored dark mode resolves to the OS scheme after mount (CP-03); the final state is what is recorded",
  "CF-19": "baseline shows the same message as a banner and as a toast (CP-07)",
  "CF-20": "rule ids and impacts are axe-core's; node counts are per rule. CP-04/CP-08/CP-19 name the causes",
  "CF-30": "stops are tag#id[data-component]; scroll containers appear because Chromium focuses them",
  "CF-40": "budget 300,000 bytes for the shell; gzip is zlib's default level",
  "CF-41": "shellBytes = bytes − the run-data block + the marker; budgeted like CF-40. Payload size is the archive's, not the viewer's",
  "CF-50": "aggregated over every page the suite opened for the archive",
  "CF-60": "containment, not a width: rows stay within the card, the card within the page, and no horizontal scroll is needed. The wide-finding archive's Finding message is one long unbroken token, which is what the four shared archives cannot produce",
  "CF-61": "the link allowlist lives in the viewer and applies to whichever parser entry is plugged in; anything off it becomes an empty href. The tables/pipes counts guard against a silent fallback to paragraphised pipes",
};

// ---------------------------------------------------------------- inputs
const out = opts.out ? path.resolve(opts.out) : mkdtempSync(path.join(os.tmpdir(), "dispatch-fixtures-"));
mkdirSync(out, { recursive: true });
const log = (...a) => console.log(...a);
log(`out: ${out}`);

if (!existsSync(SHELL)) die(2, `no shell at ${SHELL}`);
let viewer = opts.viewer;
if (viewer) {
  if (!existsSync(viewer)) die(2, `viewer ${viewer} does not exist`);
} else {
  viewer = path.join(out, "cs-dispatch-viewer");
  const r = spawnSync("go", ["build", "-trimpath", "-o", viewer, "./dispatch-viewer/cmd/cs-dispatch-viewer"], {
    cwd: REPO, encoding: "utf8", env: { ...process.env, CGO_ENABLED: "0" },
  });
  if (r.status !== 0) die(2, `go build failed (pass --viewer or DISPATCH_FIXTURES_VIEWER):\n${r.stderr || r.error}`);
  log(`viewer: built from ${REPO} (embeds ${path.relative(REPO, SHELL)})`);
}

const archives = writeSyntheticArchives(path.join(out, "archives"));

function render(name, dir) {
  const html = path.join(out, `${name}.html`);
  const r = spawnSync(viewer, [dir, "-o", html], { encoding: "utf8" });
  if (r.status !== 0) die(2, `render ${name} failed: ${r.stderr}`);
  const m = r.stdout.match(/\((\d+) bytes, (\d+) events, (\d+) issues\)/);
  return { html, events: +m[2], issues: +m[3] };
}
const pages = {};
for (const [name, dir] of Object.entries(archives)) {
  pages[name] = render(name, dir);
  log(`render: ${name} ← ${dir} (${pages[name].events} events, ${pages[name].issues} issues)`);
}
const corruptHtml = writeCorruptPage(SHELL, path.join(out, "corrupt.html"));
const axePath = axeScript();
if (!axePath) log("warn: axe-core not found (DISPATCH_FIXTURES_AXE unset and vendor/axe-core missing); CF-20 is skipped");
else log(`axe: ${path.relative(REPO, axePath)}`);

// ---------------------------------------------------------------- measure
const { browser, executable, sandbox, sandboxError } = await launchBrowser();
log(`browser: ${executable.path || "(bundled)"} from ${executable.from}${sandbox ? "" : ` — relaunched with --no-sandbox (${sandboxError})`}`);

const measured = {};
const set = (id, archive, value) => {
  if (archive === null) measured[id] = value;
  else (measured[id] ??= {})[archive] = value;
};
const hygiene = {};
const sizes = {};
let axeVersion = null;

async function withPage(archive, url, fn, opts = {}) {
  const { page, log: plog, close } = await openPage(browser, url, opts);
  try {
    await P.install(page);
    return await fn(page, plog);
  } finally {
    const h = (hygiene[archive] ??= { requests: [], pageErrors: [], consoleErrors: [] });
    for (const k of Object.keys(h)) h[k].push(...plog[k]);
    await close();
  }
}

const structurePart = (s) => ({
  "CF-01": s.lanes,
  "CF-02": { squares: s.squares, kinds: s.kinds, connectors: s.connectors },
  "CF-03": { head: s.head, banner: s.banner, timelineHidden: s.timelineHidden },
  "CF-04": s.issues,
  "CF-05": { legend: s.legend, sections: s.sections },
});
const firstDiff = (a, b) => {
  for (const k of Object.keys(a)) if (JSON.stringify(a[k]) !== JSON.stringify(b[k])) return { field: k, light: a[k], dark: b[k] };
  return true;
};

for (const [name, { html }] of Object.entries(pages)) {
  const url = fileUrl(html);
  process.stdout.write(`measure: ${name} `);
  // Structure, light and dark; axe in the load state.
  const light = await withPage(name, url, async (page) => {
    set("CF-51", name, await P.scriptCount(page));
    if (axePath) {
      const a = await P.axe(page, axePath);
      axeVersion = a.version;
      set("CF-20", name, { "light/load": a.violations });
    }
    // The EventLanes census holds only visible events, and CF-01/CF-02's frozen
    // counts include the log-derived ones — so count with the log shown, the
    // idiom CF-10's note already documents (d005 ruling). axe's load state is
    // measured first so "load" keeps its meaning.
    await P.setLog(page, true);
    return await P.structure(page);
  });
  const dark = await withPage(name, url + "?theme=dark", async (page) => {
    if (axePath) measured["CF-20"][name]["dark/load"] = (await P.axe(page, axePath)).violations;
    await P.setLog(page, true);
    return await P.structure(page);
  });
  for (const [id, v] of Object.entries(structurePart(light))) set(id, name, v);
  set("CF-06", name, firstDiff(structurePart(light), structurePart(dark)));
  if (dark.theme !== "dark" || light.theme !== "light") log(`\nwarn: ${name} themes resolved to ${light.theme}/${dark.theme}`);
  process.stdout.write(".");

  const timeline = light.timelineHidden === false;
  if (!timeline) {
    for (const id of ["CF-10", "CF-11", "CF-12", "CF-13", "CF-14", "CF-15", "CF-16"]) set(id, name, "no-timeline");
  } else {
    // Fresh page: log toggle end states, then the hidden-landing walk.
    await withPage(name, url, async (page) => {
      set("CF-15", name, await P.logToggle(page));
      set("CF-12", name, await P.arrowWalkHidden(page));
    });
    process.stdout.write(".");
    // Fresh page: arrow sequences, selection keys, focusability.
    await withPage(name, url, async (page) => {
      await P.setLog(page, true);
      set("CF-11", name, await P.arrowSequence(page, "ArrowRight", 10));
      await P.selectEvent(page, light.squares - 1);
      set("CF-13", name, await P.arrowSequence(page, "ArrowLeft", 10));
      const keys = await P.selectionKeys(page);
      set("CF-33", name, { start: keys.start, afterEscape: keys.afterEscape });
      set("CF-34", name, { start: keys.start, last: keys.last, afterHome: keys.afterHome, afterEnd: keys.afterEnd });
      set("CF-35", name, { activeDescendantFollows: keys.activeDescendantFollows });
      set("CF-31", name, await P.timelineKeyboard(page));
      set("CF-32", name, await P.issueRowKeyboard(page));
    });
    process.stdout.write(".");
    // Fresh page (no selection yet): the doc-mode segment, then every selection and issue row.
    await withPage(name, url, async (page) => {
      set("CF-16", name, await P.docMode(page));
      set("CF-10", name, await P.selections(page));
      set("CF-14", name, await P.issueClicks(page));
    });
    process.stdout.write(".");
    // Fresh pages: axe with the log shown and event 5 selected, both themes.
    if (axePath) {
      for (const theme of ["light", "dark"]) {
        await withPage(name, url + `?theme=${theme}`, async (page) => {
          await P.setLog(page, true);
          await P.selectEvent(page, Math.min(5, light.squares - 1));
          measured["CF-20"][name][`${theme}/log+selected`] = (await P.axe(page, axePath)).violations;
        });
      }
    }
    // Fresh page: Tab walk.
    await withPage(name, url, async (page) => set("CF-30", name, await P.tabStops(page, 20)));
    process.stdout.write(".");
  }
  // Size.
  const buf = readFileSync(html);
  const text = buf.toString("utf8");
  const start = text.indexOf('<script type="application/json" id="run-data">');
  const end = text.indexOf("</script>", start) + "</script>".length;
  const payloadBytes = start >= 0 ? Buffer.byteLength(text.slice(start, end)) : 0;
  set("CF-41", name, { bytes: buf.length, gzip: gzipSync(buf).length, payloadBytes, shellBytes: buf.length - payloadBytes + "<!--RUN-DATA-->".length });
  log(" done");
}
for (const [name, h] of Object.entries(hygiene)) set("CF-50", name, h);

// Theme behaviour and the corrupt page are page-level: measured once, on the
// largest archive available.
const themeName = "healthy";
const themeUrl = fileUrl(pages[themeName].html);
set("CF-17", null, await withPage(themeName, themeUrl, (page) => P.themeCycle(page)));
const themeCase = (query, storage, scheme) =>
  withPage(themeName, themeUrl + query, (page) => P.themeState(page), { scheme, storage: { [SEL.themeStorageKey]: storage } });
set("CF-18", null, {
  "?theme=dark|stored:none|os:light": await themeCase("?theme=dark", null, "light"),
  "?theme=light|stored:dark|os:light": await themeCase("?theme=light", "dark", "light"),
  "?theme=system|stored:dark|os:light": await themeCase("?theme=system", "dark", "light"),
  "no-param|stored:light|os:dark": await themeCase("", "light", "dark"),
  "no-param|stored:none|os:dark": await themeCase("", null, "dark"),
});
set("CF-19", null, await withPage("corrupt", fileUrl(corruptHtml), (page, plog) => P.corrupt(page, plog)));
// The 600 px containment gate renders its own archive (one long unbroken
// Finding token — the four shared archives never produce one) and asserts
// containment, not a pixel width, so it survives legitimate layout change.
// Its page is not one of CF-50's probe pages, so its hygiene bucket is
// dropped; page errors are asserted inside the value instead.
{
  const wideHtml = path.join(out, "wide-finding.html");
  const wr = spawnSync(viewer, [writeWideFindingArchive(path.join(out, "archives")), "-o", wideHtml], { encoding: "utf8" });
  if (wr.status !== 0) die(2, `render wide-finding failed: ${wr.stderr}`);
  set("CF-60", null, await withPage("wide-finding", fileUrl(wideHtml), async (page, plog) => {
    await page.setViewport({ width: 600, height: 900 });
    await new Promise((r) => setTimeout(r, 200));
    const m = await page.evaluate((sel) => {
      const wrap = document.querySelector(sel.issuesWrap);
      const card = wrap.closest('[data-component="Card"]');
      const tableRoot = wrap.querySelector('[data-component="Table"]');
      const rows = [...wrap.querySelectorAll("[data-table-row]")];
      const vw = document.documentElement.clientWidth;
      const cr = card.getBoundingClientRect();
      return {
        rowsWithinCard: rows.length > 0 && rows.every((r) => {
          const b = r.getBoundingClientRect();
          return b.right <= cr.right + 0.5 && b.left >= cr.left - 0.5;
        }),
        noHorizontalScroll: tableRoot.scrollWidth <= tableRoot.clientWidth + 1,
        cardWithinPage: cr.right <= vw + 0.5,
      };
    }, SEL);
    return { contained: m.rowsWithinCard && m.noHorizontalScroll && m.cardWithinPage, pageErrors: plog.pageErrors.length };
  }));
  delete hygiene["wide-finding"];
}
// CF-61's archive is likewise rendered only for this check, so the
// per-archive rows gain no unit from it either.
{
  const mdHtml = path.join(out, "markdown.html");
  const mr = spawnSync(viewer, [writeMarkdownArchive(path.join(out, "archives")), "-o", mdHtml], { encoding: "utf8" });
  if (mr.status !== 0) die(2, `render markdown failed: ${mr.stderr}`);
  set("CF-61", null, await withPage("markdown", fileUrl(mdHtml), async (page, plog) => {
    const m = await P.markdownDoc(page);
    return { ...m, pageErrors: plog.pageErrors.length };
  }));
  delete hygiene["markdown"];
}
{
  const buf = readFileSync(SHELL);
  set("CF-40", null, { bytes: buf.length, gzip: gzipSync(buf).length });
}
await browser.close();

// ---------------------------------------------------------------- judge
const stable = (v) => JSON.stringify(v, (_, x) => (x && typeof x === "object" && !Array.isArray(x) ? Object.fromEntries(Object.keys(x).sort().map((k) => [k, x[k]])) : x));
const byId = Object.fromEntries(CHECKS.map((c) => [c.id, c]));

if (opts.update) {
  // The write path is gated: changing a `keep` row's value or a `must-change`
  // row's target requires --approve + --reason, and the change is recorded on
  // the row as an "approval" block so the diff carries its own permission
  // slip. A new row is not an approved write but is still disclosed, with an
  // "origin" block instead. Without the flags the runner refuses: it writes
  // nothing at all, exits non-zero, names every gated row, and prints the
  // authorising command.
  const prior = existsSync(EXPECTATIONS)
    ? Object.fromEntries(JSON.parse(readFileSync(EXPECTATIONS, "utf8")).map((r) => [r.id, r]))
    : {};
  const gated = []; // rows whose write needs --approve/--reason
  const disclosed = []; // approval/origin blocks being written, for the log
  const rows = CHECKS.map((c) => {
    const was = prior[c.id];
    let value = measured[c.id];
    if (value === undefined) {
      // Carry forward what this run could not measure: the row keeps its
      // existing value and approval verbatim. It is an error, never a blank,
      // when there is nothing to carry.
      if (!was) die(2, `${c.id}: not measured by this run and no prior row to carry forward`);
      value = was.value;
    }
    const row = {
      id: c.id, title: c.title, status: c.status, ...(c.target ? { target: c.target } : {}), ...(c.budget ? { budget: c.budget } : {}),
      value, note: NOTES[c.id] || "",
      // A carried-forward row keeps its approval verbatim.
      ...(measured[c.id] === undefined && was?.approval ? { approval: was.approval } : {}),
    };
    if (!was) {
      // A new row is not an approved write and is never refused; it is
      // disclosed with an "origin" block. The id is the --approve id when
      // the flags are supplied, else the member name; the reason is the
      // --reason text when supplied, else one sentence naming why the row
      // exists. Passing the flags on a write with nothing gated is allowed
      // and is how a real dispatch id gets stamped onto a new row.
      row.origin = {
        id: opts.approve ?? MEMBER,
        reason: opts.reason ?? `First recording of this check: ${c.title}.`,
      };
      disclosed.push(`${c.id}: origin ${JSON.stringify(row.origin)}`);
    } else if (measured[c.id] !== undefined) {
      const valueChanged = stable(was.value) !== stable(value);
      const targetChanged = (was.target ?? null) !== (c.target ?? null);
      const needs =
        (c.status === "keep" && valueChanged) || (c.status === "must-change" && targetChanged);
      if (needs) {
        const previous = c.status === "keep" ? was.value : was.target;
        const measured_ = c.status === "keep" ? value : c.target;
        if (opts.approve) {
          row.approval = { id: opts.approve, reason: opts.reason, previous, measured: measured_ };
          disclosed.push(`${c.id}: approval ${JSON.stringify(previous)} -> ${JSON.stringify(measured_)}`);
        } else {
          gated.push({ id: c.id, previous, value: measured_ });
        }
      }
    }
    return row;
  });
  if (gated.length) {
    console.error("fixtures: --update would change frozen values; refusing, nothing written:");
    for (const g of gated) {
      console.error(`  ${g.id}: ${JSON.stringify(g.previous)} -> ${JSON.stringify(g.value)}`);
    }
    console.error(`authorise with: node run.mjs ${args.join(" ")} --approve <dispatch-id> --reason "<one sentence>"`);
    process.exit(1);
  }
  writeFileSync(EXPECTATIONS, JSON.stringify(rows, null, 2) + "\n");
  for (const d of disclosed) log(`disclosed: ${d}`);
  log(`\nexpectations written: ${EXPECTATIONS}${axeVersion ? ` (axe-core ${axeVersion})` : ""}`);
}
if (!existsSync(EXPECTATIONS)) die(2, `no ${EXPECTATIONS}; run with --update to record one`);
const expected = JSON.parse(readFileSync(EXPECTATIONS, "utf8"));

const results = [];
let failures = 0;
for (const e of expected) {
  const c = byId[e.id];
  if (!c) {
    results.push({ id: e.id, status: e.status, result: "UNKNOWN", detail: "no such check in run.mjs" });
    failures++;
    continue;
  }
  const got = measured[e.id];
  const units = c.perArchive ? Object.keys(e.value) : [null];
  const problems = [];
  const skipped = [];
  const progress = [];
  for (const u of units) {
    const want = u === null ? e.value : e.value[u];
    const have = u === null ? got : got?.[u];
    const tag = u === null ? "" : `${u}: `;
    if (have === undefined) {
      skipped.push(`${tag}not measured`);
      continue;
    }
    if (c.budget) {
      for (const [field, limit] of Object.entries(e.budget || c.budget)) {
        if (have[field] > limit) problems.push(`${tag}${field} ${have[field]} > ${limit}`);
      }
      continue;
    }
    if (e.status === "keep") {
      if (stable(want) !== stable(have)) problems.push(`${tag}${diffSummary(want, have)}`);
    } else {
      const met = c.meets ? c.meets(have) : false;
      const changed = stable(want) !== stable(have);
      progress.push(`${tag || ": "}${met ? "met" : changed ? "changed, target unmet" : "unchanged"}`.replace(/^: /, "all: "));
      if (opts.strict && !met) problems.push(`${tag}target unmet`);
    }
  }
  if (opts.strict && skipped.length) problems.push(...skipped.map((s) => `${s} (strict)`));
  if (opts.strict && e.id === "CF-20" && !axePath) problems.push("axe-core unavailable (strict)");
  let result;
  if (problems.length) {
    result = "FAIL";
    failures++;
  } else if (!progress.length && skipped.length === units.length) {
    result = "SKIP";
  } else if (e.status === "must-change") {
    const states = progress.map((p) => p.slice(p.indexOf(": ") + 2));
    result = states.every((x) => x === "met") ? "MET" : states.some((x) => x.startsWith("changed")) ? "PROGRESS" : "UNCHANGED";
  } else result = skipped.length ? "PARTIAL" : "OK";
  results.push({
    id: e.id, status: e.status, result,
    value: c.summary(got ?? e.value),
    target: e.target || (e.budget ? Object.entries(e.budget).map(([k, v]) => `${k} ≤ ${v}`).join(", ") : ""),
    detail: [...problems, ...skipped, ...(e.status === "must-change" ? progress : [])].join("; "),
  });
}

function diffSummary(want, have) {
  if (Array.isArray(want) && Array.isArray(have)) {
    if (want.length !== have.length) return `length ${want.length} → ${have.length}`;
    const k = want.findIndex((x, i) => stable(x) !== stable(have[i]));
    return `[${k}] ${JSON.stringify(want[k]).slice(0, 50)} → ${JSON.stringify(have[k]).slice(0, 50)}`;
  }
  if (want && have && typeof want === "object" && typeof have === "object") {
    for (const k of new Set([...Object.keys(want), ...Object.keys(have)])) {
      if (stable(want[k]) !== stable(have[k])) return `${k}: ${JSON.stringify(want[k]).slice(0, 40)} → ${JSON.stringify(have[k]).slice(0, 40)}`;
    }
  }
  return `${JSON.stringify(want).slice(0, 40)} → ${JSON.stringify(have).slice(0, 40)}`;
}

// ---------------------------------------------------------------- report
writeFileSync(path.join(out, "report.json"), JSON.stringify({ measured, results, axeVersion, viewer, archives }, null, 2) + "\n");
const cols = ["id", "status", "result", "value"];
const width = Object.fromEntries(cols.map((k) => [k, Math.max(k.length, ...results.map((r) => String(r[k] ?? "").length))]));
width.value = Math.min(width.value, 96);
const cell = (r, k) => String(r[k] ?? "").slice(0, width[k]).padEnd(width[k]);
log("");
log(cols.map((k) => k.toUpperCase().padEnd(width[k])).join("  ") + "  TARGET");
for (const r of results) {
  log(cols.map((k) => cell(r, k)).join("  ") + "  " + (r.target || "—"));
  if (r.detail && (r.result !== "OK" || r.status === "must-change")) log(`${"".padEnd(width.id)}  ↳ ${r.detail}`);
}
const tally = (k) => results.filter((r) => r.result === k).length;
log(`\n${tally("OK")} ok · ${tally("FAIL")} fail · ${tally("PARTIAL")} partial · ${tally("SKIP")} skipped · must-change: ${tally("MET")} met, ${tally("PROGRESS")} progress, ${tally("UNCHANGED")} unchanged${opts.strict ? " (strict)" : ""}`);
if (opts.keepOut) log(`report: ${path.join(out, "report.json")} (renders and archives beside it)`);
else rmSync(out, { recursive: true, force: true });
process.exit(failures ? 1 : 0);
