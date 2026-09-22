import type { EventShape, EventToken } from "@codesweep-ai/ui";
import type { IndexedEvent, Run, Session, Span, Step } from "./types";

// Timeline model: event-indexed columns over all events — the marks, kinds
// and labels are the pre-React viewer's, now expressed as EventLanes data.

/** The event's categorical kind — the key into the palette and the census. */
export type Kind =
  | "open"
  | "continue"
  | "restart"
  | "reply-done"
  | "reply-bad"
  | "verdict-ok"
  | "verdict-bad"
  | "accept"
  | "plan"
  | "assessment"
  | StepKind;

/** The tracer's event kinds, plus the two the page draws as hatched spans:
 *  idle (a turn that ended inside a box) and wait (the orchestrator's own
 *  wait calls). */
export type StepKind =
  | "user"
  | "assistant"
  | "thinking"
  | "tool_call"
  | "system"
  | "meta"
  | "turn_end"
  | "idle"
  | "wait"
  | "error";

// One map, token names only (CP-20): this replaces the old sqClass → .sq.*
// CSS block → MANUAL colour table triplication. EventLanes resolves each name
// as var(...) at paint time and repaints on theme change.
export const palette: Record<Kind, EventToken> = {
  open: "--color-neutral",
  continue: "--color-warning",
  restart: "--color-cat-8-mid",
  "reply-done": "--color-success",
  "reply-bad": "--color-error",
  "verdict-ok": "--color-link",
  "verdict-bad": "--color-severe",
  accept: "--color-accent",
  plan: "--color-cat-1",
  assessment: "--color-cat-4",
  // The tracer's own kind colours (tracer palette.ts), so a step reads the
  // same here and on its trace page.
  user: "--color-cat-9",
  assistant: "--color-cat-7",
  thinking: "--color-cat-3",
  tool_call: "--color-cat-5",
  system: "--muted",
  meta: "--color-structural",
  turn_end: "--muted",
  // Waiting is drawn in the rule colour, a tenth of black, so a hatched span
  // reads as a light absence beside the columns rather than as a dark bar.
  idle: "--border",
  wait: "--border",
  // A step that errored is a column in the error colour rather than a cross:
  // the cross draws at the mark size and dwarfs a thin column.
  error: "--color-error",
};

export const STEPKINDS: ReadonlySet<Kind> = new Set<Kind>([
  "user", "assistant", "thinking", "tool_call", "system", "meta", "turn_end", "idle", "wait", "error",
]);

// The tracer's bar scale (EventStrip.tsx): a step's bar is the log of its time
// with a 2-minute ceiling.
export const WORK_CEILING_MS = 2 * 60_000;
export function logFraction(ms: number, ceiling: number): number {
  return Math.log2(1 + Math.min(ms, ceiling) / 1000) / Math.log2(1 + ceiling / 1000);
}

/** One mark drawn from a trace: a step, or the idle after it. Marks are
 *  numbered after the run's events so `i` stays unique. */
export interface StepMark {
  i: number;
  session: Session;
  step: Step;
  idle: boolean;
}

/** Every trace mark, numbered from `base` upward: two slots per step, the
 *  second used only when the step carries an idle interval. */
export function stepMarks(run: Run, base: number): StepMark[] {
  const out: StepMark[] = [];
  if (!run.traces) return out;
  let n = base;
  for (const session of run.traces.sessions) {
    for (const step of session.strip) {
      if (!step.ts) {
        n += 2;
        continue;
      }
      out.push({ i: n, session, step, idle: false });
      if (step.idleMs != null && step.idleMs > 0) out.push({ i: n + 1, session, step, idle: true });
      n += 2;
    }
  }
  return out;
}

export const stepKindOf = (m: StepMark): StepKind =>
  m.idle ? "idle" : m.step.wait ? "wait" : m.step.error ? "error" : (m.step.kind as StepKind);

export const stepLabel = (m: StepMark): string => {
  if (m.idle) return "idle between turns";
  const k = m.step.wait ? "wait call" : m.step.kind === "tool_call" ? "tool" : m.step.kind.replace("_", " ");
  return m.step.label ? k + " · " + m.step.label : k;
};

// The verdict carries a permanent halo (the old "blue, glowing" square);
// EventLanes paints it below the selection/linked halos, so selection wins.
export const verdictHalo: Record<"verdict-ok" | "verdict-bad", EventToken> = {
  "verdict-ok": "--color-accent-bg",
  "verdict-bad": "--color-severe-bg",
};

export function kindOf(e: IndexedEvent): Kind {
  switch (e.type) {
    case "open":
      return "open";
    case "continue":
      return "continue";
    case "restart":
      return "restart";
    case "reply":
      return e.phase === "done" ? "reply-done" : "reply-bad";
    case "accept":
      return "accept";
    case "plan":
      return "plan";
    case "assessment":
      return "assessment";
    case "verdict":
      return e.outcome === "campaign-met" ? "verdict-ok" : "verdict-bad";
  }
  return "open";
}

/* Log-derived marks are circles; squares are reserved for channel artifacts.
   Accept is the hollow circle. */
/** Every protocol and log mark is round, so it reads apart from the trace
 *  steps, which are columns in the same colours. An accept is hollow. */
export function shapeOf(e: IndexedEvent): EventShape {
  if (e.type === "accept") return "hollow-circle";
  return "circle";
}

// The traces view removes every mark whose only evidence is log.jsonl, and
// the channel marks too: a box's edges are its opening and its reply.
export const LOGKINDS: ReadonlySet<Kind> = new Set(["accept", "plan", "assessment"]);
export const PROTOCOLKINDS: ReadonlySet<Kind> = new Set([
  "open", "continue", "restart", "reply-done", "reply-bad", "verdict-ok", "verdict-bad",
]);

export const spanOf = (run: Run, e: IndexedEvent): Span | undefined =>
  (run.spans || []).find((s) => s.node === e.node && s.id === e.dispatch);

// Dispatch IDs are per-node sequences, so a bare dNNN is ambiguous across
// lanes — every label carries its node qualifier (accepts name the accepted
// node, which is not the lane they sit on).
export const qual = (e: IndexedEvent): string => e.node + "/" + e.dispatch;

export const typeLabel = (e: IndexedEvent): string =>
  e.type === "reply"
    ? "reply · " + qual(e) + " · " + (e.phase || "?")
    : e.type === "verdict"
      ? "verdict · " + qual(e) + " · " + e.outcome
      : e.type === "accept"
        ? "accept · " + (e.text ? e.text + "/" : "") + e.dispatch
        : e.type + (e.dispatch ? " · " + qual(e) : "");

// Log-derived marks live on their own sub-lane under the orchestrator, so the
// orchestrator lane stays purely channel traffic (it is a node too).
export const LOGTYPES = new Set(["plan", "assessment", "accept"]);

/** The lane id an event sits in; LOGTYPES events belong to the orchestrator. */
export const laneOf = (e: IndexedEvent, orch: string | undefined): string =>
  orch !== undefined && e.node === orch && LOGTYPES.has(e.type) ? "log" : e.node;
