import { useState } from "react";
import { Card, StatusBadge, Table, type TableColumn } from "@codesweep-ai/ui";
import type { IndexedEvent, Issue, Run } from "./types";

interface IssuesProps {
  run: Run;
  events: IndexedEvent[];
  onSelect: (i: number) => void;
}

// The data-severity hook is what the fixture oracle reads the severity from.
const SEV_STATUS = { error: "error", severe: "severe", warning: "warning", info: "info" } as const;

export function Issues({ run, events, onSelect }: IssuesProps) {
  const issues = run.issues || [];
  // Which finding you are reading. The row drives the timeline selection, so it
  // has to show that it is the current one; without this the table gives no
  // feedback at all and you lose your place in a 17-row list. Kept here rather
  // than derived from the selected event, because several findings can point at
  // the same event and deriving backwards would light up all of them.
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  // Collapsible so the Timeline above stays on screen: Issues and the
  // orchestrator log share this column with it, and a long findings list
  // pushes the timeline out of view.
  const [collapsed, setCollapsed] = useState(false);

  const onClickIssue = (it: Issue & { k: number }) => {
    setSelectedKey(String(it.k));
    if (!it.dispatch) return;
    // The reply is the evidence for most findings (reply-not-accepted,
    // reply-name-mismatch); fall back to the open square when the whole
    // point is that no reply exists.
    const of = (t: string[]) =>
      events.findIndex(
        (e) => e.dispatch === it.dispatch && (!it.node || e.node === it.node) && t.includes(e.type),
      );
    const i = of(["reply", "verdict"]) >= 0 ? of(["reply", "verdict"]) : of(["open"]);
    if (i >= 0) onSelect(i);
  };

  // Rows carry their index so the key stays unique when two findings share
  // code and message.
  const rows = issues.map((it, k) => ({ ...it, k }));

  const columns: TableColumn<Issue & { k: number }>[] = [
    {
      id: "severity",
      header: "Severity",
      cell: (it) => (
        <span data-severity={it.severity}>
          <StatusBadge
            label={it.severity}
            status={SEV_STATUS[it.severity as keyof typeof SEV_STATUS] ?? "neutral"}
          />
        </span>
      ),
    },
    {
      id: "code",
      header: "Code",
      cell: (it) => <span className="code">{it.code}</span>,
    },
    {
      id: "message",
      header: "Finding",
      cell: (it) => (
        <span className="finding" title={(run.issueDefs || {})[it.code] || ""}>
          {it.message}
        </span>
      ),
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
          <span className="section-title">Issues</span>
          <span className="spacer"></span>
          <span id="issue-count">{issues.length ? issues.length + " found" : ""}</span>
        </span>
      }
    >
      <div className="scroll-y" id="issues">
        <Table
          columns={columns}
          data={rows}
          rowKey={(it) => String(it.k)}
          selectedKey={selectedKey}
          onRowClick={onClickIssue}
          emptyMessage="No findings — every check passed."
        />
      </div>
    </Card>
  );
}
