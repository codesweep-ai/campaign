import type { EventShape, EventToken } from "@codesweep-ai/ui";
import type { IndexedEvent, Run, Span } from "./types";

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
  | "assessment";

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
export function shapeOf(e: IndexedEvent): EventShape {
  if (e.type === "accept") return "hollow-circle";
  if (e.type === "plan" || e.type === "assessment") return "circle";
  return "square";
}

// Blind mode ("show orchestrator log" off) removes every mark whose only
// evidence is log.jsonl.
export const LOGKINDS: ReadonlySet<Kind> = new Set(["accept", "plan", "assessment"]);

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
