import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { AppShell, Card, Footer, Header, PulseBadge, SegmentedControl, StatusBadge, ThemeToggle } from "@codesweep-ai/ui";
import type { IndexedEvent, LogEntry, Run } from "./types";
import { dur, fmtClock, fmtT, parseClock } from "./format";
import { LOGKINDS, kindOf, stepMarks, type StepMark } from "./model";
import { Timeline, Legend, type ViewRequest, type ShownView } from "./Timeline";
import { Issues } from "./Issues";
import { LogPane } from "./LogPane";
import { Inspector, type DocMode } from "./Inspector";

/** The timeline's two views. */
export type View = "protocol" | "traces";

// The payload arrives as a JSON script block the Go side splices in for the
// <!--RUN-DATA--> marker at the top of <body> (cli.go assemble).
function loadRun(): Run | null {
  const el = document.getElementById("run-data");
  if (!el || !el.textContent) return null;
  try {
    const run = JSON.parse(el.textContent) as Run;
    return run && run.schemaVersion === 1 ? run : null;
  } catch {
    return null;
  }
}

export default function App() {
  const run = useMemo(loadRun, []);
  const [sel, setSel] = useState(-1);
  const [logSel, setLogSel] = useState<LogEntry | null>(null);
  const [docMode, setDocMode] = useState<DocMode>("rendered");
  const traced = !!(run && run.traces && run.traces.sessions.length);
  // The timeline has two views. Protocol is the run as the channels and the
  // orchestrator's log show it: boxes, marks and claims. Traces is what each
  // member did inside its boxes: the log row empties, the marks go, and the
  // steps appear. A page with no traces has only the first.
  const [view, setView] = useState<View>("protocol");
  const showLog = view === "protocol";
  const showDetail = traced && view === "traces";
  const [errorsOnly, setErrorsOnly] = useState(false);
  // A dispatch the timeline should zoom to, as its span id "<member>/<dNNN>".
  const [zoomSpan, setZoomSpan] = useState<string | null>(null);
  // A view the address asks for, applied whenever a new one is set.
  const [viewRequest, setViewRequest] = useState<ViewRequest>(null);
  // The view the timeline shows, kept for the address.
  const shownRef = useRef<ShownView | null>(null);

  const events: IndexedEvent[] = useMemo(
    () => (run ? run.events.map((e, i) => ({ ...e, i })) : []),
    [run],
  );
  // Trace marks are numbered after the run's events, so one selection index
  // names either an event or a step.
  const marks: StepMark[] = useMemo(() => (run ? stepMarks(run, run.events.length) : []), [run]);
  const markOf = useMemo(() => new Map(marks.map((m) => [m.i, m])), [marks]);

  useEffect(() => {
    document.body.classList.toggle("blind", !showLog);
  }, [showLog]);

  // Acceptance is shown by linkage, not static decoration: selecting an
  // accept circle lights its reply, selecting an accepted reply lights the
  // circle that accepted it.
  let link = -1;
  if (sel >= 0 && events[sel]) {
    const e = events[sel];
    if (e.type === "accept") {
      link = events.findIndex(
        (x) =>
          (x.type === "reply" || x.type === "verdict") &&
          x.dispatch === e.dispatch &&
          (!e.text || x.node === e.text),
      );
    } else if (e.type === "reply" || e.type === "verdict") {
      link = events.findIndex(
        (x) =>
          x.type === "accept" && x.dispatch === e.dispatch && (!x.text || x.text === e.node),
      );
    }
  }

  // Selection state mirrored into refs so the document-level key handler is
  // deterministic no matter when a key arrives relative to React's commit and
  // passive effects (the fixture suite presses keys at synthetic speed).
  const selRef = useRef(-1);
  const showLogRef = useRef(true);

  const select = useCallback(
    (i: number) => {
      selRef.current = i;
      setSel(i);
      setLogSel(null);
      writeHash(address(i, events, shownRef.current), true);
    },
    [events],
  );

  // The view follows the selection into the address, rewritten in place and
  // a little after the last change: a scroll reports a view every frame, and
  // a browser may refuse more than a hundred address writes in half a minute.
  const viewTimer = useRef<number | undefined>(undefined);
  const onViewShown = useCallback(
    (shown: ShownView) => {
      shownRef.current = shown;
      window.clearTimeout(viewTimer.current);
      viewTimer.current = window.setTimeout(() => {
        // A view the address already names, give or take the second a whole
        // pixel of scroll can move it, is left as written.
        const named = readHash(location.hash, events)?.view;
        if (named && named !== "run" && Math.abs(named.start - shown.start) <= 2 && Math.abs(named.end - shown.end) <= 2) return;
        writeHash(address(selRef.current, events, shown), false);
      }, 300);
    },
    [events],
  );

  // The address names the selection, so a view can be handed to someone:
  // #m/<member> is the member's first dispatch, #m/<member>/<dNNN> is that
  // dispatch's opening, zoomed to, and #e/<n> is any event or step by the
  // index this page gives it. Each selection is a history entry, so the
  // browser's back and forward walk the selections made, and an address
  // typed or pasted applies without a reload.
  useEffect(() => {
    if (!run) return;
    const apply = (fromHistory: boolean) => {
      const target = readHash(location.hash, events);
      if (!target) {
        // Back to an entry with no selection clears it, and an entry with no
        // view is the run view; a bad address on first load is left alone.
        if (fromHistory && location.hash === "") {
          selRef.current = -1;
          setSel(-1);
          setLogSel(null);
          setViewRequest("run");
        }
        return;
      }
      if (target.i !== undefined) {
        selRef.current = target.i;
        setSel(target.i);
        setLogSel(null);
        // A step is on the timeline only in the traces view.
        if (target.i >= events.length && traced) {
          showLogRef.current = false;
          setView("traces");
        }
      } else if (fromHistory) {
        // An entry with no selection had none.
        selRef.current = -1;
        setSel(-1);
        setLogSel(null);
      }
      // An explicit view outranks the zoom a dispatch implies. With neither,
      // an entry reached through history is the run view.
      if (target.view === "run") {
        setZoomSpan(null);
        setViewRequest("run");
      } else if (target.view) {
        setZoomSpan(null);
        setViewRequest({ ...target.view });
      } else if (target.zoom) {
        setZoomSpan(target.zoom);
      } else if (fromHistory) {
        setViewRequest("run");
      }
    };
    apply(false);
    const onPop = () => apply(true);
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, [run, events, traced]);

  const selectLog = useCallback((l: LogEntry) => {
    selRef.current = -1;
    setSel(-1);
    setLogSel(l);
  }, []);

  const onView = useCallback(
    (v: string) => {
      const next: View = v === "traces" && traced ? "traces" : "protocol";
      showLogRef.current = next === "protocol";
      setView(next);
    },
    [traced],
  );

  // Arrow keys step through visible events (blind mode drops the log kinds —
  // the same set EventLanes' listbox walks), Home/End jump to the ends, and
  // Escape clears the selection. When the EventLanes listbox has focus it
  // handles these keys itself and stopPropagation keeps this listener quiet;
  // this handler covers the page when focus is anywhere else. The
  // SegmentedControl owns arrows inside its radiogroup.
  useEffect(() => {
    const onKey = (ev: KeyboardEvent) => {
      if (ev.key === "Escape") {
        select(-1);
        // The timeline's listbox keeps focus after a click, and with it the
        // focus ring; Escape hands focus back to the page.
        (document.activeElement as HTMLElement | null)?.blur?.();
        return;
      }
      if ((ev.target as HTMLElement | null)?.closest?.('[role="radiogroup"]')) return;
      if (!events.length) return;
      const vis = events
        .filter((e) => showLogRef.current || !LOGKINDS.has(kindOf(e)))
        .map((e) => e.i);
      if (!vis.length) return;
      const sel = selRef.current;
      const pos = vis.indexOf(sel);
      let next: number | undefined;
      if (ev.key === "ArrowRight")
        next = pos < 0 ? vis[0] : vis[Math.min(pos + 1, vis.length - 1)];
      else if (ev.key === "ArrowLeft") next = pos < 0 ? undefined : vis[Math.max(pos - 1, 0)];
      else if (ev.key === "Home") next = vis[0];
      else if (ev.key === "End") next = vis[vis.length - 1];
      if (next !== undefined && next !== sel) select(next);
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [events, select]);

  const outcome = run?.campaign.outcome || "no verdict";

  return (
    <AppShell>
      <Header
        title="cs-dispatch-viewer"
        // "?" reloads this same file with the query and hash dropped. It is the
        // only honest destination: cs-dispatch-viewer emits ONE self-contained
        // page (viewer.html, or -o), so the index.html both of these pointed at
        // is never written, and served from a directory without one it is a
        // plain 404 under every scheme (ui OPEN.md §7.18).
        titleHref="?"
        navItems={[{ label: "Campaign Dispatches", href: "?", active: true }]}
        actions={<ThemeToggle storageKey="dispatch-viewer-theme" />}
      />
      {run === null ? (
        <div id="banner">
          <Card variant="danger">
            This page holds no readable run data (schema mismatch or corrupt payload).
          </Card>
        </div>
      ) : (
        <main className="scroller">
          <div className="pagehead">
            <h1>
              <span className="pname" id="h-name">
                {run.campaign.name}
              </span>
              <span id="h-verdict">
                <StatusBadge
                  label={outcome}
                  status={outcome === "campaign-met" ? "success" : "error"}
                />
              </span>
              {!run.campaign.outcome ? (
                <PulseBadge aria-label="No verdict recorded — the campaign may still be running" />
              ) : null}
            </h1>
            <div className="pmeta" id="h-meta">
              {fmtT(run.campaign.createdAt) +
                (run.campaign.outcomeAt
                  ? " · " + dur(run.campaign.createdAt, run.campaign.outcomeAt)
                  : "")}
            </div>
          </div>
          <div id="banner">
            {!run.timelineValid ? (
              <Card variant="danger">
                <b>No timeline.</b>{" "}
                {(run.issues.find((i) => i.code === "mtimes-clobbered") || { message: "" })
                  .message || "archive is not honestly renderable"}
              </Card>
            ) : null}
          </div>
          <div className="layout">
            <div className="col">
              <Card
                id="tl-card"
                style={run.timelineValid ? undefined : { display: "none" }}
                header={
                  <span className="chead-row">
                    <span className="section-title">Timeline</span>
                    <span className="spacer"></span>
                    {traced ? (
                      <SegmentedControl
                        id="view"
                        aria-label="Timeline view"
                        options={[
                          { value: "protocol", label: "protocol" },
                          { value: "traces", label: "member traces" },
                        ]}
                        value={view}
                        onChange={onView}
                      />
                    ) : null}
                    <label className="toggle">
                      <input
                        type="checkbox"
                        id="errorsonly"
                        checked={errorsOnly}
                        disabled={!showDetail}
                        onChange={(e) => setErrorsOnly(e.target.checked)}
                      />{" "}
                      errors only
                    </label>
                  </span>
                }
              >
                {run.timelineValid ? (
                  <Timeline
                    run={run}
                    events={events}
                    marks={marks}
                    sel={sel}
                    link={link}
                    showLog={showLog}
                    showDetail={showDetail}
                    errorsOnly={errorsOnly}
                    zoomSpan={zoomSpan}
                    viewRequest={viewRequest}
                    onViewShown={onViewShown}
                    onSelect={select}
                  />
                ) : (
                  <>
                    <div id="tl"></div>
                    <div className="ruler" id="ruler"></div>
                  </>
                )}
                <Legend detail={showDetail} />
              </Card>
              <Issues run={run} events={events} onSelect={select} />
              {showLog ? (
                <LogPane log={run.log} events={events} onSelect={select} onSelectLog={selectLog} />
              ) : null}
            </div>
            <div className="col">
              <Inspector
                run={run}
                event={sel >= 0 && sel < events.length ? events[sel] : null}
                mark={sel >= events.length ? markOf.get(sel) || null : null}
                marks={marks}
                logEntry={logSel}
                docMode={docMode}
                onDocMode={setDocMode}
                onSelect={select}
              />
            </div>
          </div>
        </main>
      )}
      <Footer>cs-dispatch-viewer · @codesweep-ai/ui v{__UI_VERSION__}</Footer>
    </AppShell>
  );
}

/** What an address names: a selection, the dispatch to zoom to when the
 *  selection is one, and a view as "@start-end" in elapsed h:mm:ss, or
 *  "@run" for the whole run. */
function readHash(
  hash: string,
  events: IndexedEvent[],
): { i?: number; zoom?: string; view?: { start: number; end: number } | "run" } | null {
  const [head, ...rest] = decodeURIComponent(hash.replace(/^#/, "")).split("@");
  // A view that does not parse is dropped, and the selection kept.
  let view: { start: number; end: number } | "run" | undefined;
  if (rest.length === 1 && rest[0] === "run") view = "run";
  else if (rest.length === 1) {
    const [a, b] = rest[0].split("-").map(parseClock);
    if (a != null && b != null && b > a) view = { start: a, end: b };
  }
  const parts = head.split("/");
  if (head === "") return view ? { view } : null;
  if (parts[0] === "e" && parts.length === 2) {
    const i = Number(parts[1]);
    return Number.isInteger(i) && i >= 0 ? { i, view } : null;
  }
  if (parts[0] === "m" && (parts.length === 2 || parts.length === 3)) {
    const [, member, dispatch] = parts;
    const open = events.find((e) => e.node === member && e.type === "open" && (!dispatch || e.dispatch === dispatch));
    if (!open) return null;
    return dispatch ? { i: open.i, zoom: member + "/" + dispatch, view } : { i: open.i, view };
  }
  return null;
}

/** The address for a selection and the view shown. The whole run is the
 *  view an address without one means, so it is written as nothing, except
 *  after a dispatch, where no view means the dispatch's box. */
function address(i: number, events: IndexedEvent[], shown: ShownView | null): string {
  const e = i >= 0 ? events[i] : undefined;
  const dispatch = !!(e && e.type === "open" && e.dispatch);
  const selection = i < 0 ? "" : dispatch ? `m/${e!.node}/${e!.dispatch}` : `e/${i}`;
  const view = !shown ? "" : shown.whole ? (dispatch ? "@run" : "") : `@${fmtClock(shown.start)}-${fmtClock(shown.end)}`;
  return selection || view ? "#" + selection + view : "";
}

/** Writes the address: as a new history entry for a selection, so back
 *  returns to the one before, and in place for a view. */
function writeHash(hash: string, entry: boolean) {
  if (location.hash === hash) return;
  const url = location.pathname + location.search + hash;
  if (entry) history.pushState(null, "", url);
  else history.replaceState(null, "", url);
}
