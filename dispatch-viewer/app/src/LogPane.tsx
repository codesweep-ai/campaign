import { useState } from "react";
import { Card, Table, type TableColumn } from "@codesweep-ai/ui";
import type { IndexedEvent, LogEntry } from "./types";
import { fmtT } from "./format";

interface LogPaneProps {
  log: LogEntry[];
  events: IndexedEvent[];
  onSelect: (i: number) => void;
  onSelectLog: (entry: LogEntry) => void;
}

type LogRow = LogEntry & { k: number };

export function LogPane({ log, events, onSelect, onSelectLog }: LogPaneProps) {
  // The row you are reading, so the log does not lose your place while you scroll.
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  // Collapsible for the same reason as Issues: this pane and the findings list
  // share a column with the Timeline, and the log is the longest of the three.
  const [collapsed, setCollapsed] = useState(false);
  const entries = log || [];

  const onClickRow = (l: LogRow) => {
    setSelectedKey(String(l.k));
    const i = events.findIndex(
      (e) =>
        e.at === new Date(l.at).toISOString().replace(/\.\d+Z$/, "Z") &&
        (e.type === l.kind || e.type === "accept"),
    );
    if (i >= 0) onSelect(i);
    else onSelectLog(l);
  };

  // Rows carry their index so the key stays unique when two entries share a
  // kind and timestamp.
  const rows: LogRow[] = entries.map((l, k) => ({ ...l, k }));

  // Same shape as the Issues table directly above: this pane used to hand-roll
  // its rows with tabIndex={0} on every one, which made a 40-entry log 40 Tab
  // stops and left the arrow keys to scroll the container instead of walking
  // rows. Table owns the roving tab stop, so the two panes in this column now
  // behave identically.
  const columns: TableColumn<LogRow>[] = [
    {
      id: "kind",
      header: "Kind",
      cell: (l) => <span className="kind">{l.kind}</span>,
    },
    {
      id: "at",
      header: "Time",
      cell: (l) => <span className="logtime">{fmtT(l.at)}</span>,
    },
    {
      id: "text",
      header: "Entry",
      cell: (l) => <span className="snip">{(l.text || "").slice(0, 160)}</span>,
      wrap: true,
    },
  ];

  return (
    <Card
      collapsible
      collapsed={collapsed}
      onToggle={() => setCollapsed((v) => !v)}
      header={
        <span className="chead-row">
          <span className="section-title">Orchestrator log</span>
          <span className="spacer"></span>
          <span className="logcount">{entries.length ? entries.length + " entries" : ""}</span>
        </span>
      }
    >
      <div className="scroll-y" id="log-list">
        <Table
          columns={columns}
          data={rows}
          rowKey={(l) => String(l.k)}
          selectedKey={selectedKey}
          onRowClick={onClickRow}
          emptyMessage="No log entries."
        />
      </div>
      <div className="none" id="log-hidden">
        Showing the file channels only — the dispatch/reply record any observer can verify. Check
        "show orchestrator log" to overlay the orchestrator's recorded claims: plan, assessments,
        accepts.
      </div>
    </Card>
  );
}
