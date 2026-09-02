// The measurements. Every probe takes a settled page (see browser.mjs) and
// returns plain JSON; run.mjs decides what each value means. Nothing here
// knows a selector by name — everything goes through SEL (selectors.mjs).
import { SEL, squareAt, docModeButton } from "./selectors.mjs";

// Helpers installed into the page once per open; probes call window.__fx.
const HELPERS = `window.__fx = (() => {
  const $ = (s, r = document) => r.querySelector(s);
  const $$ = (s, r = document) => [...r.querySelectorAll(s)];
  // Text nodes joined by spaces: what the DOM says, independent of CSS
  // text-transform (innerText would report the uppercase the stylesheet paints).
  const txt = (el) => {
    if (!el) return "";
    const w = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
    const parts = [];
    while (w.nextNode()) parts.push(w.currentNode.nodeValue);
    return parts.join(" ").trim().replace(/\\s+/g, " ");
  };
  const kindOf = (el, kinds, dflt) => (Object.entries(kinds).find(([, s]) => el.matches(s)) || [dflt])[0];
  const visible = (el) => !!el && el.checkVisibility();
  const idx = (el, SEL) => (el ? el.getAttribute(SEL.squareIndexAttr) : null);
  const selected = (SEL) => { const v = idx($(SEL.squareSelected), SEL); return v === null ? null : +v; };
  return { $, $$, txt, kindOf, visible, idx, selected };
})();`;

export async function install(page) {
  await page.evaluate(HELPERS);
}

const ev = (page, fn, ...args) => page.evaluate(fn, SEL, ...args);

/** Checks or unchecks "show orchestrator log"; false when the control is not visible. */
export async function setLog(page, on) {
  return ev(
    page,
    (SEL, on) => {
      const box = __fx.$(SEL.showLog);
      if (!__fx.visible(box)) return false;
      if (box.checked !== on) box.click();
      return true;
    },
    on,
  );
}

/** Reach an event the contract's way: focus the listbox, Home, ArrowRight to
    its position among the visible events, Enter. False when the event is not
    visible (the census holds only visible events). */
export async function selectEvent(page, i) {
  const pos = await ev(
    page,
    (SEL, i) => __fx.$$(SEL.square).map((o) => +o.getAttribute(SEL.squareIndexAttr)).indexOf(i),
    i,
  );
  if (pos < 0) return false;
  await page.focus(SEL.timelineListbox);
  await page.keyboard.press("Home");
  for (let k = 0; k < pos; k++) await page.keyboard.press("ArrowRight");
  await page.keyboard.press("Enter");
  return true;
}

export async function structure(page) {
  return ev(page, (SEL) => {
    const { $, $$, txt, kindOf, visible } = __fx;
    const squares = $$(SEL.square);
    const card = $(SEL.timelineCard);
    return {
      head: { name: txt($(SEL.headName)), verdict: txt($(SEL.headVerdict)), meta: txt($(SEL.headMeta)) },
      banner: txt($(SEL.banner)),
      timelineHidden: card ? !visible(card) : null,
      // EventLanes keeps lane labels in a flat gutter and events in a flat
      // census; the label text is the lane id.
      lanes: $$(SEL.lane).map((l) => ({
        label: txt(l),
        kind: kindOf(l, SEL.laneKinds, "member"),
        events: $$(`#tl [${SEL.squareIndexAttr}][data-event-lane="${txt(l)}"]`).length,
      })),
      squares: squares.length,
      kinds: [...new Set(squares.map((s) => kindOf(s, SEL.squareKinds, "?")))].sort(),
      connectors: $$(SEL.connector).length,
      issues: {
        count: txt($(SEL.issueCount)),
        rows: $$(SEL.issueRow).map((r) => txt($(SEL.issueCode, r)) + ":" + kindOf(r, SEL.issueSeverities, "?")),
      },
      legend: $$(SEL.legendRow).map(txt),
      sections: $$(SEL.sectionTitle).map(txt),
      logRows: $$(SEL.logRow).length,
      theme: document.documentElement.getAttribute(SEL.themeAttr),
    };
  });
}

export async function scriptCount(page) {
  return page.evaluate(() => document.querySelectorAll("script").length);
}

/** What the inspector shows for the current selection, as one line. */
async function selectionLine(page, i) {
  return ev(
    page,
    (SEL, i) => {
      const { $, $$, txt, kindOf, idx } = __fx;
      const kv = $(SEL.inspectorKv);
      const terms = kv ? $$(SEL.inspectorTerm, kv).map(txt) : [];
      const values = kv ? $$(SEL.inspectorValue, kv).map(txt) : [];
      const pairs = terms.map((t, k) => `${t}=${values[k] ?? ""}`);
      const docs = $$(SEL.docPanel).map((d) => kindOf(d, SEL.docKinds, "raw"));
      return `${i}|sel=${idx($(SEL.squareSelected), SEL)}|${pairs.join("|")}|docs=${docs.join("+") || "-"}|link=${idx($(SEL.squareLinked), SEL)}`;
    },
    i,
  );
}

/** Select every event (log shown so all are visible); one line per event.
    Selection goes through the listbox keyboard path (see selectEvent). */
export async function selections(page) {
  await setLog(page, true);
  const n = await ev(page, (SEL) => __fx.$$(SEL.square).length);
  const out = [];
  for (let i = 0; i < n; i++) {
    await selectEvent(page, i);
    out.push(await selectionLine(page, i));
  }
  return out;
}

const selState = (page) =>
  ev(page, (SEL) => {
    const s = __fx.$(SEL.squareSelected);
    return { sel: s ? +__fx.idx(s, SEL) : null, visible: s ? __fx.visible(s) : null };
  });

/** ArrowRight/ArrowLeft `presses` times from the current state; the index sequence. */
export async function arrowSequence(page, key, presses) {
  const seq = [];
  for (let k = 0; k < presses; k++) {
    await page.keyboard.press(key);
    const s = await selState(page);
    seq.push(s.sel === null ? null : s.visible ? s.sel : `${s.sel}(hidden)`);
  }
  return seq;
}

/** ArrowRight from a fresh page with the log hidden until the selection stops moving. */
export async function arrowWalkHidden(page) {
  await setLog(page, false);
  const hidden = [];
  let prev = null;
  let steps = 0;
  for (let k = 0; k < 2000; k++) {
    await page.keyboard.press("ArrowRight");
    const s = await selState(page);
    if (s.sel === prev) break;
    prev = s.sel;
    steps++;
    if (s.visible === false) hidden.push(s.sel);
  }
  return { steps, hiddenLandings: hidden };
}

/** Click each issue row; which event ends up selected (null: no jump). */
export async function issueClicks(page) {
  await setLog(page, true);
  const n = await ev(page, (SEL) => __fx.$$(SEL.square).length);
  const codes = await ev(page, (SEL) => __fx.$$(SEL.issueRow).map((r) => __fx.txt(__fx.$(SEL.issueCode, r))));
  const out = [];
  for (let k = 0; k < codes.length; k++) {
    // Pre-select a known square so a no-op is distinguishable from a jump to it.
    let result = null;
    for (const pre of [n - 1, 0]) {
      await selectEvent(page, pre);
      const rows = await page.$$(SEL.issueRow);
      await rows[k].click();
      const s = await selState(page);
      if (s.sel !== pre) {
        result = s.sel;
        break;
      }
    }
    out.push(`${k}:${codes[k]}->${result}`);
  }
  return out;
}

/** The log toggle's end states on a fresh page (log hidden by default). */
export async function logToggle(page) {
  const state = () =>
    ev(page, (SEL) => {
      const { $, $$, visible } = __fx;
      return {
        logHidden: document.body.classList.contains(SEL.logHiddenClass),
        checked: !!$(SEL.showLog)?.checked,
        logListVisible: visible($(SEL.logList)),
        noticeVisible: visible($(SEL.logHiddenNotice)),
        logRows: $$(SEL.logRow).length,
        visibleSquares: $$(SEL.square).filter(visible).map((s) => +__fx.idx(s, SEL)),
      };
    });
  const off = await state();
  const toggled = await setLog(page, true);
  if (!toggled) return "no-timeline";
  const on = await state();
  await setLog(page, false);
  const offAgain = await state();
  const pick = ({ visibleSquares, ...rest }) => ({ ...rest, visibleSquares: visibleSquares.length });
  return {
    off: pick(off),
    on: pick(on),
    offAgain: pick(offAgain),
    revealedByLog: on.visibleSquares.filter((i) => !off.visibleSquares.includes(i)),
  };
}

/** The rendered/raw segment around a reply event. */
export async function docMode(page) {
  await setLog(page, true);
  const before = await ev(page, (SEL) => __fx.visible(__fx.$(SEL.docMode)));
  const target = await ev(page, (SEL) => {
    const s = __fx.$(SEL.squareKinds["reply-done"] + "," + SEL.squareKinds["reply-bad"]);
    return s ? +__fx.idx(s, SEL) : null;
  });
  if (target === null) return { segmentVisibleBeforeSelection: before, event: null };
  const read = () =>
    ev(page, (SEL) => {
      const { $, $$, visible, kindOf } = __fx;
      const active = $(SEL.docModeActive);
      return {
        segmentVisible: visible($(SEL.docMode)),
        buttons: $$(SEL.docModeButton).map((b) => b.getAttribute(SEL.docModeAttr) || __fx.txt(b)),
        active: active ? active.getAttribute(SEL.docModeAttr) || __fx.txt(active) : null,
        panels: $$(SEL.docPanel).map((d) => kindOf(d, SEL.docKinds, "raw")),
      };
    });
  await selectEvent(page, target);
  const rendered = await read();
  await page.click(docModeButton("raw"));
  const raw = await read();
  await page.click(docModeButton("rendered"));
  const renderedAgain = await read();
  return { segmentVisibleBeforeSelection: before, event: target, rendered, raw, renderedAgain };
}

const themeState = (page) =>
  ev(page, (SEL) => {
    let stored = null;
    try {
      stored = localStorage.getItem(SEL.themeStorageKey);
    } catch {
      stored = "(blocked)";
    }
    return { attr: document.documentElement.getAttribute(SEL.themeAttr), stored };
  });

/** Theme toggle cycling and persistence, OS scheme light, empty storage. */
export async function themeCycle(page) {
  const out = { initial: await themeState(page) };
  await page.click(SEL.themeToggle);
  out.click1 = await themeState(page);
  await page.reload({ waitUntil: "load" });
  await page.waitForSelector(SEL.themeToggle);
  await install(page);
  out.reloadAfterClick1 = await themeState(page);
  await page.click(SEL.themeToggle);
  out.click2 = await themeState(page);
  await page.click(SEL.themeToggle);
  out.click3 = await themeState(page);
  return out;
}

export { themeState };

/** Error surfaces on the corrupt page. */
export async function corrupt(page, log) {
  return ev(
    page,
    (SEL, errors) => {
      const { $, $$, txt } = __fx;
      const banners = $$(SEL.noDataBanner);
      const toasts = $$(SEL.toast);
      return {
        mounted: !!$(SEL.ready),
        banners: banners.length,
        toasts: toasts.length,
        surfaces: banners.length + toasts.length,
        message: txt(banners[0] || toasts[0]),
        pageErrors: errors,
      };
    },
    log.pageErrors,
  );
}

/** CF-61: the markdown fixture — select the dev dispatch (its body carries
    the links and the table) and read the rendered panel: unsafe schemes must
    come out with an empty href, the ordinary link survives, and the table is
    a table rather than paragraphs of pipes. */
export async function markdownDoc(page) {
  const target = await ev(page, (SEL) => {
    const opt = __fx.$(`${SEL.squareKinds.open}[data-event-lane="dev"]`);
    return opt ? +opt.getAttribute(SEL.squareIndexAttr) : null;
  });
  if (target === null) return { panel: false };
  await selectEvent(page, target);
  return ev(page, (SEL) => {
    const panel = __fx.$(SEL.docMdPanel);
    if (!panel) return { panel: false };
    // The reads are rooted at the viewer's documented content article hook;
    // the links/table themselves are the rendered form of our own content. A
    // missing hook collapses every count, which fails the frozen value loudly.
    const content = __fx.$("[data-markdown-content]", panel);
    if (!content) return { panel: true, unsafeLinks: 0, droppedLinks: 0, safeLinks: 0, tables: 0, literalPipes: 0 };
    const hrefs = [...content.querySelectorAll("a")].map((a) => a.getAttribute("href") ?? "");
    return {
      panel: true,
      unsafeLinks: hrefs.filter((h) => /^(javascript|data):/i.test(h)).length,
      droppedLinks: hrefs.filter((h) => h === "").length,
      safeLinks: hrefs.filter((h) => /^https?:\/\//i.test(h)).length,
      tables: content.querySelectorAll("table").length,
      literalPipes: ((content.textContent || "").match(/\|/g) || []).length,
    };
  });
}

/** axe-core violations by rule id for the page's current state. */
export async function axe(page, axePath) {
  await page.addScriptTag({ path: axePath });
  return page.evaluate(async () => {
    const res = await window.axe.run(document, { resultTypes: ["violations"] });
    const out = {};
    for (const v of res.violations.sort((a, b) => a.id.localeCompare(b.id))) {
      out[v.id] = { impact: v.impact, nodes: v.nodes.length };
    }
    return { version: window.axe.version, violations: out };
  });
}

/** The first n Tab stops from a fresh page. */
export async function tabStops(page, n) {
  const stops = [];
  for (let k = 0; k < n; k++) {
    await page.keyboard.press("Tab");
    stops.push(
      await ev(page, (SEL) => {
        const el = document.activeElement;
        if (!el || el === document.body) return "body";
        let d = el.tagName.toLowerCase();
        if (el.id) d += "#" + el.id;
        const comp = el.getAttribute("data-component") || el.closest("[data-component]")?.getAttribute("data-component");
        if (comp) d += `[${comp}]`;
        if (el.tagName === "A" && el.getAttribute("href")) d += `(${el.getAttribute("href")})`;
        if (el.matches(SEL.square)) d += `:square ${__fx.idx(el, SEL)}`;
        else if (el.matches(SEL.timelineListbox)) {
          // The listbox is the timeline's keyboard stop (d005): annotate it as
          // the square stop, with the active descendant's index.
          const opt = document.getElementById(el.getAttribute("aria-activedescendant") || "");
          d += `:square ${opt ? __fx.idx(opt, SEL) : "?"}`;
        } else if (el.matches(SEL.issueRow)) d += ":issue-row";
        else if (el.matches(SEL.logRow)) d += ":log-row";
        return d;
      }),
    );
  }
  return stops;
}

/** Issue rows under Table's roving tab stop (U1): exactly one row is in the
    Tab order; ArrowDown/ArrowUp walk it through every row, and Home/End jump
    to the ends. Replaces the old "every row focusable" probe — per-row
    tabIndex was precisely what the roving tab stop removed. */
export async function issueRowKeyboard(page) {
  const base = await ev(page, (SEL) => {
    const rows = __fx.$$(SEL.issueRow);
    return { total: rows.length, tabStops: rows.filter((r) => r.tabIndex >= 0).length };
  });
  if (!base.total || base.tabStops !== 1) return { ...base, reached: 0, home: null, end: null };
  const activeKey = () =>
    ev(page, (SEL) => {
      const a = document.activeElement;
      return a && a.matches(SEL.issueRow) ? a.getAttribute(SEL.issueRowKeyAttr) : null;
    });
  await ev(page, (SEL) => __fx.$$(SEL.issueRow).find((r) => r.tabIndex >= 0)?.focus());
  // The roving stop moves focus in a requestAnimationFrame, so each press must
  // wait for the active row to actually change before the next one lands.
  const seen = new Set();
  const first = await activeKey();
  if (first !== null) seen.add(first);
  const step = async (key, prev) => {
    await page.keyboard.press(key);
    try {
      await page.waitForFunction(
        (sel, attr, p) => {
          const a = document.activeElement;
          return a && a.matches(sel) && a.getAttribute(attr) !== p;
        },
        { timeout: 1000 },
        SEL.issueRow,
        SEL.issueRowKeyAttr,
        prev,
      );
      return await activeKey();
    } catch {
      return prev; // walked off an end: focus stayed put
    }
  };
  let cur = first;
  const last = String(base.total - 1);
  for (let i = 0; i < base.total - 1 && cur !== null && cur !== last; i++) {
    cur = await step("ArrowDown", cur);
    seen.add(cur);
  }
  for (let i = 0; i < base.total - 1 && cur !== null && cur !== "0"; i++) cur = await step("ArrowUp", cur);
  const end = await step("End", cur);
  const home = await step("Home", end);
  return { ...base, reached: seen.size, home, end };
}

/** Amended CF-31 (d005): the timeline exposes exactly one Tab stop (the
    EventLanes listbox; its census options are deliberately non-tabbable), and
    every visible event is reachable from it by keyboard — Home, then
    ArrowRight once per step to the end, visiting every visible index. */
export async function timelineKeyboard(page) {
  await setLog(page, true);
  const expected = await ev(page, (SEL) =>
    __fx.$$(SEL.square).map((o) => +o.getAttribute(SEL.squareIndexAttr)),
  );
  // The tabbable census is rooted at the documented scroller hook and counts
  // the scroller itself — the one element the listbox contract makes tabbable.
  const tabStops = await ev(page, (SEL) => {
    const root = document.querySelector(SEL.timelineListbox);
    return root ? [root, ...root.querySelectorAll("*")].filter((e) => e.tabIndex >= 0).length : 0;
  });
  if (!expected.length) return { tabStops, total: 0, steps: 0, missing: [] };
  const readActive = () =>
    ev(page, (SEL) => {
      const lb = __fx.$(SEL.timelineListbox);
      const opt = lb && document.getElementById(lb.getAttribute("aria-activedescendant") || "");
      return opt ? +opt.getAttribute(SEL.squareIndexAttr) : null;
    });
  await page.focus(SEL.timelineListbox);
  await page.keyboard.press("Home");
  const seen = [await readActive()];
  for (let k = 1; k < expected.length; k++) {
    await page.keyboard.press("ArrowRight");
    seen.push(await readActive());
  }
  return {
    tabStops,
    total: expected.length,
    steps: seen.length,
    missing: expected.filter((i) => !seen.includes(i)),
  };
}

/** Escape, Home, End and active-descendant placement around a selected event. */
export async function selectionKeys(page) {
  await setLog(page, true);
  const n = await ev(page, (SEL) => __fx.$$(SEL.square).length);
  const start = Math.min(5, n - 1);
  await selectEvent(page, start);
  // Amended target (d005): under the listbox contract DOM focus stays on the
  // listbox; the active descendant follows the selection and is the option
  // carrying aria-selected.
  const activeDescendantFollows = await ev(
    page,
    (SEL, start) => {
      const lb = __fx.$(SEL.timelineListbox);
      const opt = lb && document.getElementById(lb.getAttribute("aria-activedescendant") || "");
      return (
        !!opt &&
        opt.getAttribute("aria-selected") === "true" &&
        +opt.getAttribute(SEL.squareIndexAttr) === start
      );
    },
    start,
  );
  await page.keyboard.press("Escape");
  const afterEscape = (await selState(page)).sel;
  await selectEvent(page, start);
  await page.keyboard.press("Home");
  const afterHome = (await selState(page)).sel;
  await selectEvent(page, start);
  await page.keyboard.press("End");
  const afterEnd = (await selState(page)).sel;
  return { start, last: n - 1, activeDescendantFollows, afterEscape, afterHome, afterEnd };
}
