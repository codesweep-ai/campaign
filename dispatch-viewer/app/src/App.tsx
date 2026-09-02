import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { AppShell, Card, Footer, Header, PulseBadge, StatusBadge, ThemeToggle } from "@codesweep-ai/ui";
import type { IndexedEvent, LogEntry, Run } from "./types";
import { dur, fmtT } from "./format";
import { LOGKINDS, kindOf } from "./model";
import { Timeline, Legend } from "./Timeline";
import { Issues } from "./Issues";
import { LogPane } from "./LogPane";
import { Inspector, type DocMode } from "./Inspector";

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
  const [showLog, setShowLog] = useState(false);

  const events: IndexedEvent[] = useMemo(
    () => (run ? run.events.map((e, i) => ({ ...e, i })) : []),
    [run],
  );

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
  const showLogRef = useRef(false);

  const select = useCallback(
    (i: number) => {
      selRef.current = i;
      setSel(i);
      setLogSel(null);
    },
    [],
  );

  const selectLog = useCallback((l: LogEntry) => {
    selRef.current = -1;
    setSel(-1);
    setLogSel(l);
  }, []);

  const onShowLog = useCallback((checked: boolean) => {
    showLogRef.current = checked;
    setShowLog(checked);
  }, []);

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
                    <label className="toggle">
                      <input
                        type="checkbox"
                        id="showlog"
                        checked={showLog}
                        onChange={(e) => onShowLog(e.target.checked)}
                      />{" "}
                      show orchestrator log
                    </label>
                  </span>
                }
              >
                {run.timelineValid ? (
                  <Timeline
                    run={run}
                    events={events}
                    sel={sel}
                    link={link}
                    showLog={showLog}
                    onSelect={select}
                  />
                ) : (
                  <>
                    <div id="tl"></div>
                    <div className="ruler" id="ruler"></div>
                  </>
                )}
                <Legend />
              </Card>
              <Issues run={run} events={events} onSelect={select} />
              <LogPane log={run.log} events={events} onSelect={select} onSelectLog={selectLog} />
            </div>
            <div className="col">
              <Inspector
                run={run}
                event={sel >= 0 ? events[sel] : null}
                logEntry={logSel}
                docMode={docMode}
                onDocMode={setDocMode}
              />
            </div>
          </div>
        </main>
      )}
      <Footer>cs-dispatch-viewer · @codesweep-ai/ui v{__UI_VERSION__}</Footer>
    </AppShell>
  );
}
