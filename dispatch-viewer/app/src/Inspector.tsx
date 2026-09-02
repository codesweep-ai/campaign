import type { ReactNode } from "react";
import { Card, SegmentedControl } from "@codesweep-ai/ui";
import { MarkdownViewer } from "@codesweep-ai/ui/markdown";
import { CodeBlock } from "@codesweep-ai/ui/code";
import json from "highlight.js/lib/languages/json";
import type { IndexedEvent, LogEntry, Run } from "./types";
import { dur, fmtT } from "./format";
import { spanOf, typeLabel } from "./model";

export type DocMode = "rendered" | "raw";

interface InspectorProps {
  run: Run;
  event: IndexedEvent | null;
  logEntry: LogEntry | null;
  docMode: DocMode;
  onDocMode: (m: DocMode) => void;
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

export function Inspector({ run, event, logEntry, docMode, onDocMode }: InspectorProps) {
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
          <EventBody run={run} e={event} docMode={docMode} />
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

function EventBody({ run, e, docMode }: { run: Run; e: IndexedEvent; docMode: DocMode }) {
  const rows: [string, string][] = [
    ["node", e.node],
    ["event", typeLabel(e)],
    ["at", fmtT(e.at)],
  ];
  const s = spanOf(run, e);
  if (s) {
    const d = s.openedAt && s.repliedAt ? dur(s.openedAt, s.repliedAt) : "";
    if (d) rows.push(["open for", d]);
    if (s.acceptedAt) rows.push(["accepted at", fmtT(s.acceptedAt)]);
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

  return (
    <>
      <dl className="kv">
        {rows.map(([k, v]) => (
          <KvRow key={k} k={k} v={v} />
        ))}
      </dl>
      {body}
    </>
  );
}

function KvRow({ k, v }: { k: string; v: string }) {
  return (
    <>
      <dt>{k}</dt>
      <dd>{v}</dd>
    </>
  );
}
