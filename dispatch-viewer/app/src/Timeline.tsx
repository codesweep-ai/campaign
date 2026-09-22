import { useCallback, useEffect, useMemo, useState } from "react";
import {
  EventLanes,
  Legend as UiLegend,
  SegmentedControl,
  type EventLane,
  type EventLaneEvent,
  type EventLaneLink,
  type EventLaneSpan,
  type EventLanesRulerContext,
  type EventLanesView,
  type EventLanesViewState,
} from "@codesweep-ai/ui";
import type { IndexedEvent, Run, Session, Step } from "./types";
import {
  LOGKINDS,
  LOGTYPES,
  WORK_CEILING_MS,
  kindOf,
  laneOf,
  logFraction,
  palette,
  shapeOf,
  stepKindOf,
  stepLabel,
  typeLabel,
  verdictHalo,
  type Kind,
  type StepMark,
} from "./model";
import { fmtElapsed, fmtHM, fmtMs, fmtT } from "./format";

/** A view the page asks for: a range, the run, or nothing. */
export type ViewRequest = EventLanesView | "run" | null;

/** The view the timeline shows, and whether it is the whole run. */
export interface ShownView {
  start: number;
  end: number;
  whole: boolean;
}

interface TimelineProps {
  run: Run;
  events: IndexedEvent[];
  marks: StepMark[];
  sel: number;
  link: number;
  showLog: boolean;
  showDetail: boolean;
  errorsOnly: boolean;
  /** A dispatch to zoom to, as its span id "<member>/<dNNN>", applied when it changes. */
  zoomSpan?: string | null;
  /** A view to show, applied when it changes. */
  viewRequest?: ViewRequest;
  /** The view shown, whenever it changes. */
  onViewShown?: (shown: ShownView) => void;
  onSelect: (i: number) => void;
}

/* The axis is elapsed time: seconds since the campaign was created. Across
   the axis, x is time: every dispatch box and every link sit at real
   moments. Inside a box, x is order and height is time: each step gets
   a column of its own, and its bar rises by the log of the time it took, on
   the tracer's scale. The steps that anchor a link, and every turn start and
   turn end, are pinned to their real time; the steps between two pins share
   that interval evenly.

   Each member is one timeline of three rows, one per vocabulary. The protocol
   row carries what the channel proves: the dispatch box and its marks. The
   steps row carries what the agent did, in the tracer's columns, with a
   forked session on a row of its own. Time the agent spent waiting is a
   hatched span on that same row, at its real time and its full height: a
   wait call of the orchestrator's, or a turn that ended inside a box before
   the member replied. Idle between two boxes is the gap between them, and
   is not drawn again. Each member's rows share a band, alternating from
   member to member, with a gap before the next; the selected member's band
   is the accent. The box frames every row of the timeline, and the open and
   reply marks sit at its ends at every zoom. Rows keep their height at every
   zoom, so the run view is the same picture with smaller boxes. With member
   traces off, a traced member keeps its rows, empty, so nothing moves when
   they come back. */

/** Plumbing the tracer draws in muted ink; left out of the boxes here. A
 *  turn end is not drawn either, since the idle band below says where a turn
 *  ended, but it stays in the column order as a pin, so the last steps of a
 *  turn sit before it rather than spread across the idle that follows. */
const PLUMBING = new Set(["meta", "system"]);
const UNDRAWN = new Set(["turn_end"]);

const PRESETS: { value: string; label: string; span?: number }[] = [
  { value: "run", label: "run" },
  { value: "3600", label: "1h", span: 3600 },
  { value: "900", label: "15m", span: 900 },
  { value: "300", label: "5m", span: 300 },
];

export function Timeline({ run, events, marks, sel, link, showLog, showDetail, errorsOnly, zoomSpan, viewRequest, onViewShown, onSelect }: TimelineProps) {
  const origin = +new Date(run.campaign.createdAt);
  const pos = (iso: string | undefined): number => (+new Date(iso as string) - origin) / 1000;

  // The view the page asks for, and the one the component reports.
  const [view, setView] = useState<EventLanesView | undefined>(undefined);
  const [shown, setShown] = useState<EventLanesViewState | null>(null);
  const [preset, setPreset] = useState("run");

  const markOf = useMemo(() => new Map(marks.map((m) => [m.i, m])), [marks]);

  // The orchestrator log's claims, in time order, each pushed right until it
  // clears the one before by a mark's width at the current scale. They are
  // meant to be clicked, so they are spread rather than squeezed.
  const scaleKey = shown ? Math.round(shown.scale * 1e4) : 0;
  const logEvents = useMemo(() => {
    const orch = run.nodes.find((n) => n.role === "orchestrator")?.name;
    const scale = scaleKey / 1e4;
    const gap = scale > 0 ? 12 / scale : 0;
    const list = events
      .filter((e) => e.node === orch && LOGTYPES.has(e.type))
      .map((e) => ({
        i: e.i,
        lane: "log",
        kind: kindOf(e),
        shape: shapeOf(e),
        label: typeLabel(e),
        at: fmtT(e.at),
        position: pos(e.at),
      }))
      .sort((a, b) => a.position - b.position);
    let prev = -Infinity;
    for (const m of list) {
      m.position = Math.max(m.position, prev + gap);
      prev = m.position;
    }
    return list as EventLaneEvent<Kind>[];
  }, [run, events, scaleKey, origin]);


  const anchorSet = useMemo(() => {
    const s = new Set<string>();
    for (const a of run.traces?.anchors || []) s.add(a.session + ":" + a.i);
    return s;
  }, [run]);

  const selectedNode = useMemo(() => {
    const e = sel >= 0 ? events[sel] : undefined;
    if (e) return e.type === "accept" && e.text ? e.text : e.node;
    return sel >= 0 ? markOf.get(sel)?.session.node ?? null : null;
  }, [sel, events, markOf]);

  const { lanes, laneEvents, spans, linksFor, extent } = useMemo(() => {
    const lanes: EventLane[] = [];
    const spans: EventLaneSpan[] = [];
    const orch = run.nodes.find((n) => n.role === "orchestrator")?.name;
    const traced = new Set((run.traces?.sessions || []).map((s) => s.node));
    const last = events.length ? pos(events[events.length - 1].at) : 0;
    let extent = last;
    const stepLane = (node: string, session?: Session): string =>
      session && session.parent ? node + "/steps/" + session.id : node + "/steps";
    for (const [k, n] of run.nodes.entries()) {
      const isOrch = n.role === "orchestrator";
      // A traced member keeps its trace rows whether or not the traces are
      // shown; the rows are empty when they are not.
      const rows = traced.has(n.name);
      const boxes = showDetail && rows;
      // Members alternate a shade across the whole timeline, so their rows
      // read as one band each, with a gap between members; the selected
      // member's band is the accent.
      const shade = selectedNode === n.name ? "--color-accent-bg" : k % 2 ? "--color-bg-muted" : undefined;
      // The protocol row: the box and the channel's marks.
      lanes.push({
        id: n.name,
        label: n.name,
        title: n.name,
        description: `${n.role} (${n.cli})`,
        className: isOrch ? "orch" : "name",
        group: n.name,
        height: 24,
        gapBefore: k > 0 ? 8 : undefined,
        shade,
        // The protocol marks are sized by their own gaps: a step follows each
        // within seconds, which would make every one a sliver.
        widthBy: "lane",
      });
      if (rows) {
        lanes.push({
          id: stepLane(n.name),
          label: "",
          title: n.name + ": steps",
          description: `${n.name}: what the agent did, one column per step`,
          className: "steplane",
          group: n.name,
          bars: "up",
          height: 34,
          shade,
        });
        for (const f of (run.traces?.sessions || []).filter((f) => f.node === n.name && f.parent)) {
          lanes.push({
            id: stepLane(n.name, f),
            label: "↳ fork",
            title: n.name + ": forked session " + f.id,
            description: `${n.name}: a session it forked`,
            className: "forklane",
            group: n.name,
            bars: "up",
            height: 22,
            shade,
          });
        }
      }
      // Each dispatch is a box from its opening to its reply; one never
      // replied to runs to the last event. Acceptance is a log claim, so it
      // stays on the orchestrator log row and in the inspector.
      for (const s of (run.spans || []).filter((s) => s.node === n.name && s.openedAt)) {
        const to = s.repliedAt ? pos(s.repliedAt) : last;
        extent = Math.max(extent, to);
        spans.push({
          lane: n.name,
          from: pos(s.openedAt),
          to: Math.max(to, pos(s.openedAt)),
          id: n.name + "/" + s.id,
          label: s.id,
        });
      }
    }
    // The orchestrator's log claims sit on one row at the bottom, always
    // present and empty until the log is shown, so nothing on the page moves
    // when it is. It is a timeline of its own: the accept anchor a link
    // reaches is the orchestrator's accept call, not the log's claim.
    if (orch) {
      lanes.push({
        id: "log",
        label: "orchestrator log",
        title: "orchestrator log",
        description: "orchestrator log claims (log.jsonl)",
        className: "loglane",
        height: 18,
        gapBefore: 8,
      });
    }
    const laneEvents: EventLaneEvent<Kind>[] = events
      // Log claims are drawn separately, spread by time so they never
      // overlap; the DOM timeline dropped log-typed events on other nodes.
      .filter((e) => !LOGTYPES.has(e.type))
      .map((e) => {
        const kind = kindOf(e);
        return {
          i: e.i,
          lane: laneOf(e, orch),
          kind,
          shape: shapeOf(e),
          label: typeLabel(e),
          at: fmtT(e.at),
          position: pos(e.at),
          // An opening sits just before its box, as the reply sits just after.
          anchor: e.type === "open" ? "end" : undefined,
          halo: kind === "verdict-ok" || kind === "verdict-bad" ? verdictHalo[kind] : undefined,
        };
      });
    const byAnchor = new Map<string, number>();
    if (showDetail) {
      // Columns per session: the marks of one session in step order, each at
      // the position the pins and the spread between them give it.
      const bySession = new Map<Session, StepMark[]>();
      for (const m of marks) {
        if (!traced.has(m.session.node) || m.idle || PLUMBING.has(m.step.kind)) continue;
        if (!bySession.has(m.session)) bySession.set(m.session, []);
        bySession.get(m.session)!.push(m);
      }
      for (const [session, list] of bySession) {
        // A wait call is pinned at its own time, and so is the step after
        // it, which the tracer stamps at or after the result. The columns
        // before the call then sit before it and the columns after it start
        // where it returned, so the blank between them is the wait itself.
        const positions = columns(
          list,
          (m) => pos(m.step.ts),
          (m, k) => isPin(m.step, anchorSet.has(session.id + ":" + m.step.i)) || !!m.step.wait || (k > 0 && !!list[k - 1].step.wait),
        );
        list.forEach((m, k) => {
          const kind = stepKindOf(m);
          const ms = m.step.workMs ?? 0;
          if (UNDRAWN.has(m.step.kind)) return;
          byAnchor.set(session.id + ":" + m.step.i, m.i);
          if (kind === "wait") {
            // A tool call is stamped when it is issued, and its time runs
            // forward from there to its result (tracer rule R70). The wait is
            // a hatched span from the call for as long as it took, at the
            // row's full height, so the blank it leaves in the columns reads
            // as waiting rather than as sparse work.
            laneEvents.push({
              i: m.i,
              lane: stepLane(session.node, session),
              kind,
              shape: "hatched",
              label: stepLabel(m),
              at: fmtT(m.step.ts),
              position: pos(m.step.ts),
              extent: ms / 1000,
              magnitude: 1,
            });
          } else {
            laneEvents.push({
              i: m.i,
              lane: stepLane(session.node, session),
              kind,
              shape: "square",
              label: stepLabel(m),
              at: fmtT(m.step.ts),
              position: positions[k],
              // With "errors only" on, a failed step is a full bar with a
              // marker on top in the error colour, so it is found at any
              // zoom; otherwise it keeps its own height in the error colour.
              magnitude: kind === "error" && errorsOnly ? 1 : ms > 0 ? logFraction(ms, WORK_CEILING_MS) : undefined,
              clipped: ms > WORK_CEILING_MS || undefined,
              marker: kind === "error" && errorsOnly ? "error" : m.step.subtask && m.step.childSessionId ? "spawn" : undefined,
              markerToken: kind === "error" && errorsOnly ? "--color-error" : undefined,
            });
          }
        });
        // Idle is the time from a turn's end to the next turn's start
        // (tracer rule R68). Between two boxes it is the gap the boxes
        // already show, so it is drawn only when the turn ended inside a
        // box, before the member replied, and then as far as the box goes.
        const boxes = (run.spans || [])
          .filter((s) => s.node === session.node && s.openedAt)
          .map((s) => [pos(s.openedAt), s.repliedAt ? pos(s.repliedAt) : last] as const);
        for (const m of marks) {
          if (!m.idle || m.session !== session) continue;
          const from = pos(m.step.ts);
          const box = boxes.find(([a, b]) => from >= a && from < b);
          if (!box) continue;
          laneEvents.push({
            i: m.i,
            lane: stepLane(session.node, session),
            kind: "idle",
            shape: "hatched",
            label: stepLabel(m),
            at: fmtT(m.step.ts),
            position: from,
            extent: Math.min(from + (m.step.idleMs ?? 0) / 1000, box[1]) - from,
            magnitude: 1,
          });
        }
      }
    }
    // A dispatch's hand-off is a solid link from the orchestrator's send call
    // to the member's turn that carries it; its reply is a dashed link from
    // the member's reply call to the orchestrator's acceptance.
    const linksFor = new Map<string, EventLaneLink[]>();
    if (showDetail && run.traces) {
      const at = (a: { session: string; i: number } | undefined) =>
        a ? byAnchor.get(a.session + ":" + a.i) : undefined;
      const byDispatch = new Map<string, Map<string, { session: string; i: number }>>();
      for (const a of run.traces.anchors) {
        const k = a.node + "/" + a.dispatch;
        if (!byDispatch.has(k)) byDispatch.set(k, new Map());
        byDispatch.get(k)!.set(a.kind, a);
      }
      for (const [k, m] of byDispatch) {
        const links: EventLaneLink[] = [];
        const sent = at(m.get("sent")), arrived = at(m.get("arrived"));
        const replied = at(m.get("replied")), accepted = at(m.get("accepted"));
        if (sent !== undefined && arrived !== undefined) links.push({ from: sent, to: arrived, emphasized: true });
        if (replied !== undefined && accepted !== undefined)
          links.push({ from: replied, to: accepted, style: "dashed", emphasized: true });
        linksFor.set(k, links);
      }
    }
    return { lanes, laneEvents, spans, linksFor, extent };
  }, [run, events, marks, showDetail, anchorSet, selectedNode, errorsOnly]);

  const allEvents = useMemo(() => laneEvents.concat(logEvents), [laneEvents, logEvents]);

  const ruler = useMemo(() => (ctx: EventLanesRulerContext) => <Ruler ctx={ctx} />, []);

  // Opening the page is the run preset.
  useEffect(() => {
    setView({ start: 0, end: runEnd(extent) });
  }, [extent]);

  const linked = useMemo(() => (link >= 0 ? new Set([link]) : new Set<number>()), [link]);

  const selectedSpan = useMemo(() => {
    const e = sel >= 0 ? events[sel] : undefined;
    if (e) return e.dispatch ? (e.type === "accept" && e.text ? e.text : e.node) + "/" + e.dispatch : null;
    const m = sel >= 0 ? markOf.get(sel) : undefined;
    if (!m || !m.step.ts) return null;
    const t = pos(m.step.ts);
    const box = spans.find((s) => s.id?.startsWith(m.session.node + "/") && s.from <= t && t <= s.to);
    return box ? String(box.id) : null;
  }, [sel, events, markOf, spans, origin]);

  // "errors only" dims everything but the marks that carry an error.
  const emphasis = useMemo(() => {
    if (!errorsOnly) return undefined;
    return new Set(allEvents.filter((e) => e.kind === "error" || e.kind === "reply-bad").map((e) => e.i));
  }, [errorsOnly, allEvents]);

  const links = useMemo(
    () => (selectedSpan ? linksFor.get(selectedSpan) || [] : []),
    [selectedSpan, linksFor],
  );

  const hidden = useMemo(() => {
    const h = new Set<Kind>();
    if (!showLog) for (const k of LOGKINDS) h.add(k);
    return h.size ? h : undefined;
  }, [showLog]);

  // Presets: the run, or a fixed span. With a selection the span is centred
  // on it; otherwise it starts where the current view starts, so run then 1h
  // shows the first hour. Zoom-to-dispatch fits one box to the width.
  const applyPreset = useCallback(
    (value: string) => {
      setPreset(value);
      const p = PRESETS.find((p) => p.value === value);
      if (!p || !p.span) {
        setView({ start: 0, end: runEnd(extent) });
        return;
      }
      const e = sel >= 0 ? events[sel] : undefined;
      const m = sel >= 0 ? markOf.get(sel) : undefined;
      const at = e ? pos(e.at) : m && m.step.ts ? pos(m.step.ts) : undefined;
      const start = at !== undefined ? at - p.span / 2 : shown ? shown.start : 0;
      const s = Math.max(0, Math.min(start, extent - p.span));
      setView({ start: s, end: s + p.span });
    },
    [sel, events, markOf, shown, extent, origin],
  );

  const zoomToSpan = useCallback((s: EventLaneSpan) => {
    const end = s.to;
    const pad = Math.max(30, (end - s.from) * 0.05);
    setPreset("");
    setView({ start: s.from - pad, end: end + pad });
  }, []);

  // An address naming a dispatch zooms to it once the boxes exist. Declared
  // after the run-view effect above, so on first load it has the last word.
  useEffect(() => {
    if (!zoomSpan) return;
    const s = spans.find((s) => s.id === zoomSpan);
    if (s) zoomToSpan(s);
  }, [zoomSpan, spans, zoomToSpan]);

  // An address naming a view shows it, outranking the dispatch's zoom.
  useEffect(() => {
    if (!viewRequest) return;
    if (viewRequest === "run") applyPreset("run");
    else {
      setPreset("");
      setView({ start: viewRequest.start, end: viewRequest.end });
    }
    // applyPreset is stable enough here: "run" reads only the extent.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [viewRequest]);

  // The page keeps the view shown for its address. The whole run is the
  // view from the first position to the last, give or take a second or two
  // of rounding at either end.
  const firstPos = useMemo(() => Math.min(...allEvents.map((e) => e.position ?? Infinity), Infinity), [allEvents]);
  useEffect(() => {
    if (!shown || !onViewShown) return;
    const whole = shown.start <= firstPos + 2 && shown.end >= extent - 2;
    onViewShown({ start: shown.start, end: shown.end, whole });
  }, [shown, extent, firstPos, onViewShown]);

  return (
    <div>
      <div className="zoombar">
        <span className="zoomlabel">zoom</span>
        <SegmentedControl
          id="zoom"
          aria-label="Zoom"
          options={PRESETS.map((p) => ({ value: p.value, label: p.label }))}
          value={preset}
          onChange={applyPreset}
        />
        <span className="zoomhint">
          {selectedSpan ? (
            <button
              type="button"
              className="linkish"
              onClick={() => {
                const s = spans.find((s) => s.id === selectedSpan);
                if (s) zoomToSpan(s);
              }}
            >
              zoom to {selectedSpan}
            </button>
          ) : null}
          {shown ? " · showing " + fmtHM(shown.start) + " to " + fmtHM(shown.end) : ""}
        </span>
      </div>
      <EventLanes
        id="tl"
        aria-label="Dispatch timeline"
        layout="position"
        lanes={lanes}
        events={allEvents}
        spans={spans}
        links={links}
        palette={palette}
        selected={sel >= 0 ? sel : null}
        linked={linked}
        emphasis={emphasis}
        hiddenKinds={hidden}
        cellWidth={10}
        view={view}
        onViewChange={setShown}
        wheelZoom={false}
        wheelScroll={false}
        overview
        overviewContent="both"
        overviewPlacement="above"
        scrollbar="overview"
        ruler={ruler}
        rulerLabel="elapsed"
        selectedSpan={selectedSpan}
        onSelectSpan={(s) => {
          const [node, id] = String(s.id).split("/");
          const open = events.find((e) => e.node === node && e.dispatch === id && e.type === "open");
          if (open) onSelect(open.i);
        }}
        renderTooltip={(e) => {
          const m = markOf.get(e.i);
          const ms = m ? (m.idle ? m.step.idleMs : m.step.workMs) ?? 0 : 0;
          const when = m && m.step.ts ? pos(m.step.ts) : (e.position ?? 0);
          return (
            <>
              {e.label} · {fmtElapsed(when)}
              {ms > 0 ? " · " + fmtMs(ms) : ""}
              {e.clipped ? " (bar stops at the ceiling)" : ""}
              <br />
              {e.at}
            </>
          );
        }}
        onSelect={(e) => onSelect(e.i)}
      />
    </div>
  );
}

/** The run view: from the campaign's creation to the last position. The
 *  component keeps the last mark and its halo inside the viewport. */
const runEnd = (extent: number): number => extent;

/** A step that sits at its real time: an anchor, a turn start or a turn end. */
function isPin(s: Step, anchored: boolean): boolean {
  return anchored || s.kind === "user" || !!s.turnEnd;
}

/** Positions for a session's marks in order: pins at their time, the first
 *  and last mark always pinned, later pins never before earlier ones, and
 *  the marks between two pins spread evenly across their interval. */
function columns<M>(list: M[], time: (m: M) => number, pin: (m: M, k: number) => boolean): number[] {
  const n = list.length;
  const out = new Array<number>(n);
  if (n === 0) return out;
  const pinned = list.map((m, k) => k === 0 || k === n - 1 || pin(m, k));
  let prev = -Infinity;
  const pinAt = new Array<number>(n);
  for (let k = 0; k < n; k++) {
    if (!pinned[k]) continue;
    const t = Math.max(time(list[k]), prev);
    pinAt[k] = t;
    prev = t;
  }
  let a = 0;
  for (let k = 1; k < n; k++) {
    if (!pinned[k]) continue;
    const pa = pinAt[a], pb = pinAt[k];
    for (let j = a; j < k; j++) out[j] = pa + ((j - a) / (k - a)) * (pb - pa);
    a = k;
  }
  out[n - 1] = pinAt[n - 1];
  return out;
}

/* Ticks in elapsed h:mm at a step that keeps labels about 80 px apart. */
const STEPS = [60, 300, 600, 900, 1800, 3600, 7200, 14400, 28800];
function Ruler({ ctx }: { ctx: EventLanesRulerContext }) {
  const p = ctx.position;
  if (!p) return <div className="ruler" style={{ width: ctx.width + "px" }} />;
  const step = STEPS.find((s) => s * p.scale >= 80) ?? STEPS[STEPS.length - 1];
  // Ticks stay inside the axis. A tick placed past its end widened the
  // scroller's content, and every scroll into that room placed another, so
  // a four-hour run once scrolled on to nineteen hours.
  const ticks: number[] = [];
  const first = Math.max(0, Math.floor(p.visibleStart / step) * step);
  for (let t = first; t <= Math.min(p.visibleEnd + step, p.end); t += step) ticks.push(t);
  return (
    <div className="ruler" style={{ width: ctx.width + "px" }} data-axis-end={Math.round(p.end)} data-axis-origin={Math.round(p.origin)}>
      {ticks.map((t) => (
        <div key={t} className="tickgrp" style={{ left: p.xForPosition(t) + "px" }}>
          <div className="tick" />
          <div className="lab">{fmtHM(t)}</div>
        </div>
      ))}
    </div>
  );
}

export function Legend({ detail }: { detail: boolean }) {
  // Two fixed rows split by evidence source, so toggling the log never
  // reflows the legend: channel artifacts survive "hide orchestrator log";
  // the log row holds everything that exists only in log.jsonl — the
  // orchestrator's claims, not channel traffic.
  const row = (label: string, cls: string, kinds: [Kind, string][]) => (
    <div className={"lrow " + cls}>
      <span className="lsrc">{label}</span>
      <UiLegend
        items={kinds.map(([k, label]) => ({ id: k, label, color: palette[k] }))}
      />
    </div>
  );
  return (
    <div className="legend" id="legend">
      {row("channels", "chan", [
        ["open", "dispatch open"],
        ["continue", "continue"],
        ["restart", "restart"],
        ["reply-done", "reply done"],
        ["reply-bad", "reply blocked/needs-input"],
        ["verdict-ok", "verdict"],
      ])}
      {row("orchestrator log", "log", [
        ["accept", "accept (log)"],
        ["plan", "plan"],
        ["assessment", "assessment"],
      ])}
      {detail
        ? row("trace steps", "steps", [
            ["user", "user turn"],
            ["assistant", "assistant"],
            ["thinking", "thinking"],
            ["tool_call", "tool call"],
            ["error", "step failed"],
            ["wait", "waiting"],
          ])
        : null}
    </div>
  );
}
