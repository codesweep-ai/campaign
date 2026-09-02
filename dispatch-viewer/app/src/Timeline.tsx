import { useMemo } from "react";
import {
  EventLanes,
  Legend as UiLegend,
  type EventLane,
  type EventLaneEvent,
  type EventLaneSpan,
  type EventLanesRulerContext,
} from "@codesweep-ai/ui";
import type { IndexedEvent, Run } from "./types";
import {
  LOGKINDS,
  LOGTYPES,
  kindOf,
  laneOf,
  palette,
  shapeOf,
  typeLabel,
  verdictHalo,
  type Kind,
} from "./model";
import { dur, fmtT, shortT } from "./format";

interface TimelineProps {
  run: Run;
  events: IndexedEvent[];
  sel: number;
  link: number;
  showLog: boolean;
  onSelect: (i: number) => void;
}

export function Timeline({ run, events, sel, link, showLog, onSelect }: TimelineProps) {
  const { lanes, laneEvents, spans } = useMemo(() => {
    const lanes: EventLane[] = [];
    const spans: EventLaneSpan[] = [];
    for (const n of run.nodes) {
      const isOrch = n.role === "orchestrator";
      // title: the full lane name as a native tooltip (the gutter truncates at
      // narrow widths); description: role/cli context in option announcements.
      lanes.push({
        id: n.name,
        label: n.name,
        title: n.name,
        description: `${n.role} (${n.cli})`,
        className: isOrch ? "orch" : undefined,
      });
      // Open→reply connectors; a dispatch with no reply spans to the last
      // event, the same fallback the DOM timeline drew.
      for (const s of (run.spans || []).filter((s) => s.node === n.name && s.openedAt)) {
        const a = events.findIndex(
          (e) => e.node === s.node && e.type === "open" && e.dispatch === s.id,
        );
        let b = events.findIndex(
          (e) =>
            e.node === s.node &&
            (e.type === "reply" || e.type === "verdict") &&
            e.dispatch === s.id,
        );
        if (b < 0) b = events.length - 1;
        if (a < 0) continue;
        spans.push({ lane: n.name, from: a, to: b });
      }
      if (isOrch) {
        const logEv = events.filter((e) => e.node === n.name && LOGTYPES.has(e.type));
        if (logEv.length)
        lanes.push({
          id: "log",
          label: "log",
          title: "log",
          description: "orchestrator log claims (log.jsonl)",
          className: "loglane",
        });
      }
    }
    const orch = run.nodes.find((n) => n.role === "orchestrator")?.name;
    const laneEvents: EventLaneEvent<Kind>[] = events
      // The DOM timeline dropped log-typed events on non-orchestrator nodes;
      // keep the semantics identical.
      .filter((e) => e.node === orch || !LOGTYPES.has(e.type))
      .map((e) => {
        const kind = kindOf(e);
        return {
          i: e.i,
          lane: laneOf(e, orch),
          kind,
          shape: shapeOf(e),
          label: typeLabel(e),
          at: fmtT(e.at),
          halo: kind === "verdict-ok" || kind === "verdict-bad" ? verdictHalo[kind] : undefined,
        };
      });
    return { lanes, laneEvents, spans };
  }, [run, events]);

  const ruler = useMemo(() => {
    if (events.length === 0) return undefined;
    return ({ width }: EventLanesRulerContext) => <Ruler events={events} width={width} />;
  }, [events]);

  const linked = useMemo(() => (link >= 0 ? new Set([link]) : new Set<number>()), [link]);

  return (
    <EventLanes
      id="tl"
      aria-label="Dispatch timeline"
      lanes={lanes}
      events={laneEvents}
      spans={spans}
      palette={palette}
      selected={sel >= 0 ? sel : null}
      linked={linked}
      hiddenKinds={showLog ? undefined : LOGKINDS}
      cellWidth={22}
      ruler={ruler}
      renderTooltip={(e) => (
        <>
          {e.label} · {e.at}
        </>
      )}
      onSelect={(e) => onSelect(e.i)}
    />
  );
}

/* The ruler shows the same events at true wall-clock spacing — ticks are
   deliberately not aligned with the event cells above. */
function Ruler({ events, width }: { events: IndexedEvent[]; width: number }) {
  if (events.length <= 1) {
    return <div className="ruler" style={{ width: width + "px" }} />;
  }
  const t0 = +new Date(events[0].at);
  const t1 = +new Date(events[events.length - 1].at) || t0 + 1;
  return (
    <div className="ruler" style={{ width: width + "px" }}>
      {events.map((e) => (
        <div
          key={e.i}
          className="tick"
          style={{ left: ((+new Date(e.at) - t0) / (t1 - t0)) * width + "px" }}
        />
      ))}
      <div className="lab" style={{ left: 0 }}>
        {shortT(events[0].at)}
      </div>
      <div className="lab" style={{ right: 0 }}>
        {shortT(events[events.length - 1].at) +
          " (" +
          dur(events[0].at, events[events.length - 1].at) +
          ")"}
      </div>
    </div>
  );
}

export function Legend() {
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
    </div>
  );
}
