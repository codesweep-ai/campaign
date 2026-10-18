import { useEffect, useMemo, useState } from "react";
import { AppShell, Card, Header, SectionedTree, ThemeToggle } from "@codesweep-ai/ui";
import { changesSince, fileAt, readPacket } from "./model";
import {
  AccessMatrix,
  ChangesView,
  FileView,
  Findings,
  MembersTable,
  MemberView,
  PageHead,
  PlanFacts,
  PlanView,
  ProfileView,
  TreesTable,
} from "./views";

export const THEME_KEY = "cs-campaign-packet-theme";

// The view lives in the hash, so a link to one member's page survives a reload
// and the browser's back button walks the reader's own path.
function useView(): [string, (v: string) => void] {
  const read = () => decodeURIComponent(location.hash.slice(1)) || "overview";
  const [view, setView] = useState(read);
  useEffect(() => {
    const on = () => setView(read());
    addEventListener("hashchange", on);
    return () => removeEventListener("hashchange", on);
  }, []);
  return [
    view,
    (v) => {
      location.hash = encodeURIComponent(v).replace(/%2F/g, "/");
      document.querySelector("main")?.scrollTo(0, 0);
    },
  ];
}

export default function App() {
  const loaded = useMemo(readPacket, []);
  const [view, go] = useView();
  const p = typeof loaded === "string" ? null : loaded;
  const changes = useMemo(() => (p ? changesSince(p) : []), [p]);
  useEffect(() => {
    if (p) document.title = `${p.campaign.name} · campaign packet`;
  }, [p]);

  if (!p) {
    return (
      <AppShell>
        <Header title="cs-campaign packet" titleHref={null} actions={<ThemeToggle storageKey={THEME_KEY} />} />
        <main className="scroller">
          <div className="banner">
            <Card variant="danger">{loaded as string}</Card>
          </div>
        </main>
      </AppShell>
    );
  }

  const sections = [
    {
      id: "campaign",
      label: "Campaign",
      nodes: [
        { id: "overview", name: "Overview", type: "leaf" as const },
        ...p.files
          .filter((f) => !f.path.startsWith("roles/"))
          .map((f) => ({ id: "file/" + f.path, name: f.path, type: "leaf" as const })),
        { id: "profile", name: p.profile.path, type: "leaf" as const },
        { id: "plan", name: "Plan", type: "leaf" as const },
        ...(p.since ? [{ id: "changes", name: "What changed", type: "leaf" as const }] : []),
      ],
    },
    {
      id: "members",
      label: "Members",
      nodes: p.members.map((m) => ({ id: "member/" + m.name, name: m.name, type: "leaf" as const })),
    },
  ];

  const member = view.startsWith("member/") ? p.members.find((m) => "member/" + m.name === view) : undefined;
  const file = view.startsWith("file/") ? fileAt(p, view.slice("file/".length)) : undefined;
  let body;
  if (member) body = <MemberView key={member.name} p={p} m={member} />;
  else if (file) body = <FileView p={p} f={file} />;
  else if (view === "profile") body = <ProfileView p={p} />;
  else if (view === "plan") body = <PlanView p={p} />;
  else if (view === "changes" && p.since) body = <ChangesView p={p} changes={changes} />;
  else
    body = (
      <div className="stack">
        <Findings p={p} />
        <Card header={<span className="section-title">Members</span>}>
          <MembersTable p={p} go={go} />
        </Card>
        <Card header={<span className="section-title">Who can reach what</span>}>
          <AccessMatrix p={p} />
        </Card>
        <Card header={<span className="section-title">Repositories and snapshots</span>}>
          <TreesTable p={p} />
        </Card>
        <Card header={<span className="section-title">Plan</span>}>
          <PlanFacts p={p} />
        </Card>
      </div>
    );

  return (
    <AppShell>
      <Header title="cs-campaign packet" titleHref="#overview" actions={<ThemeToggle storageKey={THEME_KEY} />} />
      <main className="scroller">
        <div className="packet">
          <nav className="packet-nav" aria-label="Packet contents">
            <SectionedTree
              sections={sections}
              selectedId={member || file || ["profile", "plan", "changes"].includes(view) ? view : "overview"}
              onSelect={(node) => go(node.id)}
              filterable={false}
              expandAllControl={false}
              labelOverflow="truncate"
            />
          </nav>
          <div className="packet-main">
            <PageHead p={p} changes={changes.length} />
            {body}
          </div>
        </div>
      </main>
    </AppShell>
  );
}
