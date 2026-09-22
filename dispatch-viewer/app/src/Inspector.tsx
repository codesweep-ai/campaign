import type { ReactNode } from "react";
import { Card, SegmentedControl } from "@codesweep-ai/ui";
import { MarkdownViewer } from "@codesweep-ai/ui/markdown";
import { CodeBlock } from "@codesweep-ai/ui/code";
import json from "highlight.js/lib/languages/json";
import type { Anchor, IndexedEvent, LogEntry, Run } from "./types";
import { dur, fmtElapsed, fmtMs, fmtT } from "./format";
import { spanOf, stepLabel, typeLabel, type StepMark } from "./model";

export type DocMode = "rendered" | "raw";

interface InspectorProps {
  run: Run;
  event: IndexedEvent | null;
  mark: StepMark | null;
  marks: StepMark[];
  logEntry: LogEntry | null;
  docMode: DocMode;
  onDocMode: (m: DocMode) => void;
  onSelect: (i: number) => void;
}

function DocBlock({ kind, content, docMode }: { kind: "md" | "json"; content: unknown; docMode: DocMode }) {
  // kind: md | json ; raw mode shows the untouched source either way
  if (docMode === "raw") {
    const raw = kind === "json" ? JSON.stringify(content, null, 2) : String(content);
    return (
      <div className="doc">
        <pre>{raw}</pre>
      </div>
    );
  }
  if (kind === "json") {
    // Was a hand-rolled regex highlighter injected with dangerouslySetInnerHTML.
    // CodeBlock tokenises properly and renders React elements; `languages` is
    // required because CodeBlock registers no grammars by default.
    return (
      <div className="doc json">
        <CodeBlock code={JSON.stringify(content, null, 2)} language="json" languages={{ json }} inline />
      </div>
    );
  }
  return (
    <div className="doc md">
      <MarkdownViewer content={String(content)} inline />
    </div>
  );
}

export function Inspector({ run, event, mark, marks, logEntry, docMode, onDocMode, onSelect }: InspectorProps) {
  const selected = event !== null || logEntry !== null;
  return (
    <Card
      header={
        <span className="chead-row">
          <span className="section-title">Inspector</span>
          <span className="spacer"></span>
          <SegmentedControl
            id="doc-mode"
            aria-label="Document view"
            style={selected ? undefined : { display: "none" }}
            options={[
              { value: "rendered", label: "rendered" },
              { value: "raw", label: "raw" },
            ]}
            value={docMode}
            onChange={(m) => onDocMode(m as DocMode)}
          />
        </span>
      }
    >
      <div id="inspector">
        {event !== null ? (
          <EventBody run={run} e={event} docMode={docMode} marks={marks} onSelect={onSelect} />
        ) : mark !== null ? (
          <StepBody run={run} m={mark} />
        ) : logEntry !== null ? (
          <>
            <dl className="kv">
              <dt>log</dt>
              <dd>{logEntry.kind}</dd>
              <dt>at</dt>
              <dd>{fmtT(logEntry.at)}</dd>
            </dl>
            <DocBlock kind="md" content={logEntry.text || ""} docMode={docMode} />
          </>
        ) : (
          <div className="none">Select a square on the timeline.</div>
        )}
      </div>
    </Card>
  );
}

const elapsedOf = (run: Run, iso: string | undefined): string =>
  iso ? fmtElapsed((+new Date(iso) - +new Date(run.campaign.createdAt)) / 1000) : "";

function EventBody({
  run, e, docMode, marks, onSelect,
}: { run: Run; e: IndexedEvent; docMode: DocMode; marks: StepMark[]; onSelect: (i: number) => void }) {
  const rows: [string, ReactNode][] = [
    ["node", e.node],
    ["event", typeLabel(e)],
    ["at", elapsedOf(run, e.at) + " · " + fmtT(e.at)],
  ];
  const s = spanOf(run, e);
  if (s) {
    const d = s.openedAt && s.repliedAt ? dur(s.openedAt, s.repliedAt) : "";
    if (d) rows.push(["open for", d]);
    if (s.acceptedAt) {
      const wait = s.repliedAt ? dur(s.repliedAt, s.acceptedAt) : "";
      rows.push(["accepted at", fmtT(s.acceptedAt) + (wait ? " · " + wait + " after the reply" : "")]);
    }
    if (s.continues || s.restarts)
      rows.push(["recovery", s.continues + " cont · " + s.restarts + " restarts"]);
  }

  let body: ReactNode = null;
  if (e.type === "reply" || e.type === "verdict") {
    const r = ((run.replies || {})[e.node] || {})[e.dispatch as string];
    if (r && docMode === "raw" && r.raw) {
      // Raw mode shows the artifact itself: the reply file verbatim,
      // dispatch stamp and all — not raw fragments of the decomposition.
      body = (
        <div className="doc">
          <pre>{r.raw}</pre>
        </div>
      );
    } else if (r) {
      // A note that is itself JSON (the d001 readback restatement) reads as
      // data, not prose.
      let noteKind: "md" | "json" = "md";
      let note: unknown = r.note || "(empty note)";
      const t = String(note).trim();
      if (t.startsWith("{") || t.startsWith("[")) {
        try {
          note = JSON.parse(t);
          noteKind = "json";
        } catch {
          /* prose after all */
        }
      }
      body = (
        <>
          <dl className="kv">
            <dt>phase</dt>
            <dd>{r.phase}</dd>
            {r.outcome ? (
              <>
                <dt>outcome</dt>
                <dd>{r.outcome}</dd>
              </>
            ) : null}
            {r.unmet && r.unmet.length ? (
              <>
                <dt>unmet</dt>
                <dd>{r.unmet.join(" · ")}</dd>
              </>
            ) : null}
          </dl>
          <DocBlock kind={noteKind} content={note} docMode={docMode} />
          {r.repos ? (
            <>
              <div style={{ height: "var(--space-2)" }}></div>
              <DocBlock kind="json" content={r.repos} docMode={docMode} />
            </>
          ) : null}
        </>
      );
    }
  } else if (e.type === "open" || e.type === "continue" || e.type === "restart") {
    const m = ((run.messages || {})[e.node] || {})[e.file as string];
    body = <DocBlock kind="md" content={m || "(message body unavailable)"} docMode={docMode} />;
  } else if (e.type === "plan" || e.type === "assessment") {
    body = <DocBlock kind="md" content={e.text || ""} docMode={docMode} />;
  } else if (e.type === "accept") {
    body = (
      <div className="none">
        The orchestrator recorded acceptance of {(e.text ? e.text + "/" : "") + e.dispatch} in its
        log. Dispatch IDs are per-node sequences, so the node qualifier names which node's dispatch
        this is.
      </div>
    );
  }

  const node = e.type === "accept" && e.text ? e.text : e.node;
  const anchors = e.dispatch && run.traces
    ? run.traces.anchors.filter((a) => a.node === node && a.dispatch === e.dispatch)
    : [];
  // With no anchor to land on, the link opens the session the node was
  // running at that moment, or its longest session when none spans it.
  if (!anchors.length && run.traces) {
    const mine = run.traces.sessions.filter((s) => s.node === e.node && s.page);
    const at = +new Date(e.at);
    const live = mine.find((s) => s.startedAt && s.endedAt && +new Date(s.startedAt) <= at && at <= +new Date(s.endedAt));
    const pick = live ?? [...mine].sort((a, b) => b.events - a.events)[0];
    if (pick)
      rows.push([
        "trace",
        <a href={pick.page} target="_blank" rel="noopener">
          open {e.node}'s trace ↗
        </a>,
      ]);
  }

  return (
    <>
      <dl className="kv">
        {rows.map(([k, v]) => (
          <KvRow key={k} k={k} v={v} />
        ))}
      </dl>
      {anchors.length ? <Anchors run={run} anchors={anchors} marks={marks} onSelect={onSelect} /> : null}
      {body}
    </>
  );
}

const ANCHOR_ORDER = ["sent", "arrived", "replied", "accepted"];

/* Where this dispatch touches the traces: one row per anchor, each a jump on
   the timeline and a link into the tracer's page for that event. */
function Anchors({
  run, anchors, marks, onSelect,
}: { run: Run; anchors: Anchor[]; marks: StepMark[]; onSelect: (i: number) => void }) {
  const sorted = [...anchors].sort((a, b) => ANCHOR_ORDER.indexOf(a.kind) - ANCHOR_ORDER.indexOf(b.kind));
  return (
    <dl className="kv anchors">
      {sorted.map((a) => {
        const m = marks.find((m) => !m.idle && m.session.id === a.session && m.step.i === a.i);
        const session = run.traces?.sessions.find((s) => s.id === a.session);
        const page = session?.page;
        // Whose trace the anchor is in: the send and accept calls are the
        // orchestrator's, the arrival and reply are the member's.
        const who = session?.node ?? "?";
        return (
          <KvRow
            key={a.kind}
            k={a.kind}
            v={
              <>
                {m ? (
                  <button type="button" className="linkish" onClick={() => onSelect(m.i)}>
                    {elapsedOf(run, m.step.ts)} · {stepLabel(m)}
                  </button>
                ) : (
                  "trace event #" + a.i
                )}
                {" "}
                <span className="who">in {who}</span>
                {page ? (
                  <>
                    {" "}
                    <a href={page + "#ev-" + a.i} target="_blank" rel="noopener">
                      open {who}'s trace ↗
                    </a>
                  </>
                ) : null}
              </>
            }
          />
        );
      })}
    </dl>
  );
}

function StepBody({ run, m }: { run: Run; m: StepMark }) {
  const s = m.step;
  const ms = m.idle ? s.idleMs : s.workMs;
  const rows: [string, ReactNode][] = [
    ["node", m.session.node],
    ["session", m.session.id.slice(0, 20) + (m.session.parent ? " (fork)" : "")],
    ["step", stepLabel(m) + " · #" + s.i],
    ["at", elapsedOf(run, s.ts) + " · " + fmtT(s.ts)],
  ];
  if (ms != null && ms > 0) rows.push([m.idle ? "idle" : "took", fmtMs(ms)]);
  // The round trip to the model less its generation is the queue for it
  // (tracer rule R79): a different fact from waiting on the row.
  if (!m.idle && s.activeMs != null && s.workMs != null)
    rows.push(["of which", "queued " + fmtMs(s.workMs - s.activeMs) + ", generated " + fmtMs(s.activeMs)]);
  if (s.error) rows.push(["result", <span className="bad">error</span>]);
  if (m.session.page)
    rows.push([
      "tracer",
      <a href={m.session.page + "#ev-" + s.i} target="_blank" rel="noopener">
        open event #{s.i} in {m.session.node}'s trace ↗
      </a>,
    ]);
  return (
    <>
      <dl className="kv">
        {rows.map(([k, v]) => (
          <KvRow key={k} k={k} v={v} />
        ))}
      </dl>
      {s.text ? (
        <div className="doc">
          <pre>{s.text}</pre>
        </div>
      ) : null}
    </>
  );
}

function KvRow({ k, v }: { k: string; v: ReactNode }) {
  return (
    <>
      <dt>{k}</dt>
      <dd>{v}</dd>
    </>
  );
}
