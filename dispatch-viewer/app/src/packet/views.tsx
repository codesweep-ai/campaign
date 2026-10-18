import { Fragment, useState } from "react";
import { Card, StatusBadge, Table, type TableColumn } from "@codesweep-ai/ui";
import { CodeBlock } from "@codesweep-ai/ui/code";
import { MarkdownViewer } from "@codesweep-ai/ui/markdown";
import diff from "highlight.js/lib/languages/diff";
import json from "highlight.js/lib/languages/json";
import yaml from "highlight.js/lib/languages/yaml";
import {
  type Change,
  type Packet,
  type PacketFile,
  type PacketMember,
  type Seed,
  briefOf,
  byteLength,
  count,
  fileAt,
  givenTo,
  home,
  notGiven,
  plural,
  planned,
  sameTree,
  seededBytes,
  short,
  size,
  treeColumns,
  words,
} from "./model";

export type Go = (view: string) => void;

const Title = ({ children }: { children: React.ReactNode }) => <span className="section-title">{children}</span>;

function Absent({ path, why }: { path: string; why: string }) {
  return (
    <p className="absent">
      <StatusBadge label="not written" status="error" size="sm" /> <code>{path}</code> does not exist yet. {why}
    </p>
  );
}

export function PageHead({ p, changes }: { p: Packet; changes: number }) {
  const refusals = p.findings.filter((f) => f.level === "refusal").length;
  const warnings = p.findings.length - refusals;
  const agents = p.members.length - 1;
  return (
    <div className="pagehead">
      <h1>
        <span className="pname">{p.campaign.name}</span>
        {refusals ? <StatusBadge label={plural(refusals, "refusal")} status="error" /> : null}
        {warnings ? <StatusBadge label={plural(warnings, "warning")} status="warning" /> : null}
        {!p.findings.length ? <StatusBadge label="ready to create" status="success" /> : null}
        {p.since ? <StatusBadge label={`${plural(changes, "change")} since last packet`} status="info" /> : null}
      </h1>
      <div className="pmeta">
        1 orchestrator and {plural(agents, "agent")}
        {p.deadline ? <> · deadline {p.deadline}</> : null} · group <code>{p.campaign.group}</code> · profile{" "}
        <code>{short(p.campaign.profileDigest)}</code> · written {new Date(p.generatedAt).toLocaleString()} by cs-campaign{" "}
        {p.tool}
      </div>
    </div>
  );
}

export function Findings({ p }: { p: Packet }) {
  const repos = treeColumns(p).filter((t) => t.kind === "repo").length;
  const snaps = treeColumns(p).length - repos;
  return (
    <Card
      header={<Title>Before you create</Title>}
      variant={p.findings.some((f) => f.level === "refusal") ? "danger" : p.findings.length ? "warning" : "default"}
    >
      <ul className="checks">
        <li>
          <StatusBadge label="resolved" status="success" size="sm" /> <code>plan</code> resolves {plural(p.members.length, "member")},{" "}
          {plural(repos, "repository", "repositories")} and {plural(snaps, "snapshot")}.
        </li>
        {p.findings.map((f, k) => (
          <li key={k}>
            <StatusBadge label={f.level} status={f.level === "refusal" ? "error" : "warning"} size="sm" /> {f.text}
            {f.detail ? <div className="detail">{f.detail}</div> : null}
          </li>
        ))}
        {!p.findings.length ? (
          <li>
            <StatusBadge label="clear" status="success" size="sm" /> Every input is written, and create raises no refusal or warning.
          </li>
        ) : null}
      </ul>
    </Card>
  );
}

export function MembersTable({ p, go }: { p: Packet; go: Go }) {
  const cols: TableColumn<PacketMember>[] = [
    {
      id: "name",
      header: "Member",
      cell: (m) => (
        <span>
          <b>{m.name}</b>
          {planned(p, m.name)?.role === "orchestrator" ? <span className="muted"> · orchestrator</span> : null}
        </span>
      ),
    },
    { id: "cli", header: "CLI", cell: (m) => planned(p, m.name)?.cli ?? "" },
    { id: "model", header: "Model", cell: (m) => <code>{planned(p, m.name)?.profile?.model || "CLI default"}</code>, wrap: true },
    { id: "stall", header: "Stall", align: "right", cell: (m) => `${planned(p, m.name)?.stallSeconds ?? "?"}s` },
    {
      id: "res",
      header: "CPU · memory",
      align: "right",
      cell: (m) => {
        const r = planned(p, m.name)?.profile?.resources;
        return r?.cpus || r?.memoryMiB ? `${r.cpus ?? "default"} · ${r.memoryMiB ? `${r.memoryMiB / 1024} GiB` : "default"}` : "default";
      },
    },
    {
      id: "seeded",
      header: "Seeded at create",
      align: "right",
      cell: (m) =>
        m.seeds.some((s) => fileAt(p, s.source)?.absent) ? (
          <span>
            {plural(m.seeds.length, "file")} · <StatusBadge label="not written" status="error" size="sm" />
          </span>
        ) : (
          `${plural(m.seeds.length, "file")} · ${size(seededBytes(p, m))}`
        ),
    },
  ];
  return (
    <div className="wide">
      <Table columns={cols} data={p.members} rowKey={(m) => m.name} onRowClick={(m) => go("member/" + m.name)} />
    </div>
  );
}

export function AccessMatrix({ p }: { p: Packet }) {
  const cols = treeColumns(p);
  if (!cols.length) return <p className="muted">No member is given a repository or a snapshot.</p>;
  const held = p.members.flatMap((m) =>
    m.trees.filter((t) => t.withhold?.length).map((t) => ({ member: m.name, tree: t })),
  );
  return (
    <>
      <div className="matrix-wrap">
        <table className="matrix">
          <thead>
            <tr>
              <th scope="col">
                <span className="visually-hidden">Member</span>
              </th>
              {cols.map((c) => (
                <th key={c.kind + c.path} scope="col" title={c.path}>
                  <span className="muted">{c.kind === "repo" ? "repository" : "snapshot"}</span>
                  <br />~/{c.name}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {p.members.map((m) => (
              <tr key={m.name}>
                <th scope="row">{m.name}</th>
                {cols.map((c) => {
                  const t = m.trees.find((x) => sameTree(x, c));
                  if (!t)
                    return (
                      <td key={c.kind + c.path} className="cell-none">
                        <span aria-label="no access">—</span>
                      </td>
                    );
                  return (
                    <td key={c.kind + c.path} className={t.kind === "repo" ? "cell-repo" : "cell-snap"}>
                      {t.kind === "repo" ? "clones, commits" : "reads"}
                      {t.withhold?.length ? <span className="held"> · {count(t.withhold.length)} withheld</span> : null}
                    </td>
                  );
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {held.length ? (
        <ul className="withheld">
          {held.map(({ member, tree }) => (
            <li key={member + tree.path}>
              Withheld from <b>{member}</b> in <code>~/{tree.name}</code>:{" "}
              {tree.withhold!.map((w, k) => (
                <span key={w}>
                  {k ? ", " : ""}
                  <code>{w}</code>
                </span>
              ))}
            </li>
          ))}
        </ul>
      ) : null}
    </>
  );
}

export function TreesTable({ p }: { p: Packet }) {
  const rows = treeColumns(p);
  if (!rows.length) return null;
  const cols: TableColumn<(typeof rows)[number]>[] = [
    { id: "kind", header: "Kind", cell: (t) => (t.kind === "repo" ? "repository" : "snapshot") },
    { id: "name", header: "In a member's home", cell: (t) => <code>~/{t.name}</code> },
    { id: "path", header: "On this host", cell: (t) => <code>{t.path}</code>, wrap: true },
    {
      id: "base",
      header: "Base",
      cell: (t) =>
        t.kind === "snapshot" ? (
          <span className="muted">frozen copy</span>
        ) : t.created ? (
          <span>not created yet; create makes it</span>
        ) : (
          <span>
            {t.ref ? <code>{t.ref}</code> : null} {t.ref ? "at " : ""}
            <code>{short(t.commit)}</code>
          </span>
        ),
      wrap: true,
    },
  ];
  return (
    <div className="wide">
      <Table columns={cols} data={rows} rowKey={(t) => t.kind + t.path} />
    </div>
  );
}

export function PlanFacts({ p }: { p: Packet }) {
  return (
    <dl className="facts">
      <dt>Campaign</dt>
      <dd>
        <code>{p.campaign.id}</code>
      </dd>
      <dt>Group</dt>
      <dd>
        <code>{p.campaign.group}</code> on network <code>{p.campaign.network}</code>
      </dd>
      {p.deadline ? (
        <>
          <dt>Deadline</dt>
          <dd>{p.deadline} from create</dd>
        </>
      ) : null}
      {Object.entries(p.campaign.policy ?? {}).map(([k, v]) => (
        <Fragment key={k}>
          <dt className="key">
            <code>{k}</code>
          </dt>
          <dd>{count(v)}</dd>
        </Fragment>
      ))}
    </dl>
  );
}

function auth(p: Packet, name: string) {
  const a = planned(p, name)?.profile?.auth ?? {};
  const grants = Object.entries(a)
    .filter(([k]) => k !== "credentials")
    .map(([k, v]) => `${k} ${Array.isArray(v) ? v.join(", ") : String(v)}`);
  const verb = typeof a.credentials === "string" ? a.credentials : "";
  return (grants.join("; ") || "none") + (verb ? ` · ${verb}` : "");
}

export function MemberView({ p, m }: { p: Packet; m: PacketMember }) {
  const [open, setOpen] = useState(false);
  const pm = planned(p, m.name);
  const brief = briefOf(p, m);
  const missing = notGiven(p, m);
  const rows: (Seed & { orientation?: boolean })[] = [{ guest: p.orientationGuest, source: "", orientation: true }, ...m.seeds];
  const cols: TableColumn<(typeof rows)[number]>[] = [
    { id: "n", header: "#", cell: (s) => String(rows.indexOf(s) + 1), width: "2.5rem" },
    { id: "guest", header: "In its home", cell: (s) => <code>{home(s.guest)}</code>, wrap: true },
    {
      id: "src",
      header: "From",
      cell: (s) =>
        s.orientation ? (
          <span className="muted">written by cs-campaign</span>
        ) : fileAt(p, s.source)?.absent ? (
          <span>
            <code>{s.source}</code> <StatusBadge label="not written" status="error" size="sm" />
          </span>
        ) : (
          <code>{s.source}</code>
        ),
      wrap: true,
    },
    {
      id: "words",
      header: "Words",
      align: "right",
      cell: (s) => count(words(s.orientation ? m.orientation.content : (fileAt(p, s.source)?.content ?? ""))),
    },
    {
      id: "digest",
      header: "sha256",
      cell: (s) => <code className="muted">{short(s.orientation ? m.orientation.digest : fileAt(p, s.source)?.digest)}</code>,
    },
  ];
  return (
    <div className="stack">
      <Card header={<Title>{m.name} · {pm?.role}</Title>}>
        <dl className="facts">
          <dt>CLI</dt>
          <dd>
            {pm?.cli} · <code>{pm?.profile?.model || "CLI default model"}</code>
            {pm?.profile?.effort ? ` · effort ${pm.profile.effort}` : ""}
          </dd>
          <dt>Branch</dt>
          <dd>
            <code>{pm?.branch}</code> <span className="muted">(predicted; create reads the real one back)</span>
          </dd>
          <dt>Stall</dt>
          <dd>{pm?.stallSeconds ?? "?"} seconds without output before it is prodded</dd>
          <dt>Credential</dt>
          <dd>{auth(p, m.name)}</dd>
          <dt>Trees</dt>
          <dd>
            {m.trees.length
              ? m.trees.map((t, k) => (
                  <span key={t.kind + t.path}>
                    {k ? ", " : ""}
                    <code>~/{t.name}</code> ({t.kind === "repo" ? "clone" : "read-only"}
                    {t.withhold?.length ? `, ${count(t.withhold.length)} withheld` : ""})
                  </span>
                ))
              : "none"}
          </dd>
          <dt>Environment</dt>
          <dd>
            {(pm?.profile?.env ?? []).map((e) => e.split("=")[0]).join(", ") || "none"}
            {pm?.profile?.env?.length ? <span className="muted"> (values in the profile)</span> : null}
          </dd>
        </dl>
      </Card>
      <Card header={<Title>What {m.name} is given at create</Title>}>
        <p className="lede">
          Its orientation and {plural(m.seeds.length, "seeded file")}.{" "}
          {missing.length ? (
            <>
              It is not given{" "}
              {missing.map((f, k) => (
                <span key={f}>
                  {k ? (k === missing.length - 1 ? " or " : ", ") : ""}
                  <code>{f}</code>
                </span>
              ))}
              .
            </>
          ) : (
            "It is given every authored file."
          )}
        </p>
        <div className="wide">
          <Table columns={cols} data={rows} rowKey={(s) => s.guest} />
        </div>
      </Card>
      <Card header={<Title>Brief · {brief?.path ?? `roles/${m.name}.md`}{brief && !brief.absent ? ` · ${plural(words(brief.content), "word")}` : ""}</Title>}>
        {!brief || brief.absent ? (
          <Absent path={`roles/${m.name}.md`} why="create refuses a member with no brief." />
        ) : (
          <MarkdownViewer content={brief.content} outline />
        )}
      </Card>
      <Card
        collapsible
        collapsed={!open}
        onToggle={() => setOpen((v) => !v)}
        header={<Title>Orientation · {plural(words(m.orientation.content), "word")}, written by cs-campaign</Title>}
      >
        <MarkdownViewer content={m.orientation.content} />
      </Card>
    </div>
  );
}

export function FileView({ p, f }: { p: Packet; f: PacketFile }) {
  const to = givenTo(p, f.path);
  return (
    <Card
      header={
        <Title>
          {f.path}
          {f.absent ? "" : ` · ${plural(words(f.content), "word")} · ${size(byteLength(f.content))} · sha256 ${short(f.digest)}`} · given to{" "}
          {to.join(", ") || "no member"}
        </Title>
      }
    >
      {f.absent ? <Absent path={f.path} why="create refuses a campaign without it." /> : <MarkdownViewer content={f.content} outline />}
    </Card>
  );
}

export function ProfileView({ p }: { p: Packet }) {
  return <CodeBlock code={p.profile.content} language="yaml" languages={{ yaml }} source={`${p.profile.path} · sha256 ${short(p.profile.digest)}`} />;
}

export function PlanView({ p }: { p: Packet }) {
  return (
    <div className="stack">
      <p className="lede">
        The campaign record <code>plan</code> resolves, as <code>cs-campaign plan</code> prints it. Names, the branch and the
        timestamps are predictions; <code>create</code> records the real ones.
      </p>
      <CodeBlock code={JSON.stringify(p.campaign, null, 2)} language="json" languages={{ json }} source="cs-campaign plan" />
    </div>
  );
}

export function ChangesView({ p, changes }: { p: Packet; changes: Change[] }) {
  const [open, setOpen] = useState(changes[0]?.path);
  const sel = changes.find((c) => c.path === open);
  return (
    <Card header={<Title>Changed since the packet written {new Date(p.since!.generatedAt).toLocaleString()}</Title>}>
      {!changes.length ? (
        <p className="lede">Nothing changed: the profile, every authored file and every orientation are as they were.</p>
      ) : (
        <>
          <ul className="changes">
            {changes.map((c) => (
              <li key={c.path} className={c.path === open ? "on" : ""}>
                <button type="button" className="linkish" onClick={() => setOpen(c.path)}>
                  {c.path}
                </button>
                {c.kind !== "changed" ? <span className="muted">{c.kind}</span> : null}
                <span className="add">+{c.added}</span>
                <span className="del">−{c.removed}</span>
              </li>
            ))}
          </ul>
          {sel ? (
            <div className="changes-diff">
              <CodeBlock code={sel.diff} language="diff" languages={{ diff }} source={sel.path} maxHeight="36rem" />
            </div>
          ) : null}
        </>
      )}
    </Card>
  );
}
