// The data `cs-campaign packet` writes into the page (internal/cli/packet.go),
// and the small derivations every view shares.

export interface PacketFile {
  path: string;
  content: string;
  digest?: string;
  absent?: boolean;
}

export interface Seed {
  guest: string;
  source: string;
}

export interface Tree {
  kind: "repo" | "snapshot";
  name: string;
  path: string;
  ref?: string;
  commit?: string;
  created?: boolean;
  withhold?: string[];
}

export interface Finding {
  level: "refusal" | "warning";
  text: string;
  detail?: string;
}

export interface PacketMember {
  name: string;
  orientation: PacketFile;
  seeds: Seed[];
  trees: Tree[];
}

// The fields of the campaign record (the JSON `plan` prints) that the page
// reads. The record carries more, and the Plan view shows all of it.
export interface PlannedMember {
  name: string;
  role: string;
  cli: string;
  sandbox?: string;
  branch?: string;
  stallSeconds?: number;
  profile?: {
    model?: string;
    effort?: string;
    resources?: { cpus?: number; memoryMiB?: number };
    auth?: Record<string, unknown>;
    env?: string[];
  };
}

export interface Packet {
  schema: number;
  tool: string;
  generatedAt: string;
  deadline?: string;
  campaign: {
    name: string;
    id: string;
    group: string;
    network: string;
    profileDigest: string;
    policy?: Record<string, number>;
    members: PlannedMember[];
  };
  orientationGuest: string;
  profile: PacketFile;
  files: PacketFile[];
  members: PacketMember[];
  findings: Finding[];
  since?: Packet;
}

export const SCHEMA = 1;

export function readPacket(): Packet | string {
  const el = document.getElementById("packet-data");
  if (!el) return "This page holds no packet data.";
  try {
    const p = JSON.parse(el.textContent || "") as Packet;
    if (p.schema !== SCHEMA) return `This page holds schema ${p.schema} packet data, and it draws schema ${SCHEMA}.`;
    return p;
  } catch {
    return "This page's packet data does not parse.";
  }
}

export const words = (s: string) => (s.trim() ? s.trim().split(/\s+/).length : 0);
export const byteLength = (s: string) => new TextEncoder().encode(s).length;
export const short = (digest?: string) => (digest ? digest.slice(0, 12) : "");
export const count = (n: number) => n.toLocaleString("en-US");
export const plural = (n: number, one: string, many = one + "s") => `${count(n)} ${n === 1 ? one : many}`;
export const home = (guest: string) => "~/" + guest;
// A view as a link. Slashes stay readable, because a view names a path.
export const toHash = (view: string) => "#" + encodeURIComponent(view).replace(/%2F/g, "/");

// Whole KiB, as the tool's own seed warning counts them.
export function size(bytes: number) {
  return bytes < 1024 ? `${bytes} B` : `${Math.floor(bytes / 1024)} KiB`;
}

export function planned(p: Packet, name: string): PlannedMember | undefined {
  return p.campaign.members.find((m) => m.name === name);
}

export function fileAt(p: Packet, path: string): PacketFile | undefined {
  return p.files.find((f) => f.path === path);
}

export function seededBytes(p: Packet, m: PacketMember) {
  return m.seeds.reduce((sum, s) => sum + byteLength(fileAt(p, s.source)?.content ?? ""), 0);
}

// The authored files a member is not given, read off the seeds rather than
// restated from the rule that decides them.
export function notGiven(p: Packet, m: PacketMember): string[] {
  const given = new Set(m.seeds.map((s) => s.source));
  return p.files.map((f) => f.path).filter((path) => !given.has(path));
}

export function givenTo(p: Packet, path: string): string[] {
  return p.members.filter((m) => m.seeds.some((s) => s.source === path)).map((m) => m.name);
}

// One column per distinct tree, in the order members declare them.
export function treeColumns(p: Packet): Tree[] {
  const seen = new Map<string, Tree>();
  for (const kind of ["repo", "snapshot"] as const) {
    for (const m of p.members) {
      for (const t of m.trees) {
        const key = `${t.kind}\u0000${t.path}`;
        if (t.kind === kind && !seen.has(key)) seen.set(key, t);
      }
    }
  }
  return [...seen.values()];
}

export const sameTree = (a: Tree, b: Tree) => a.kind === b.kind && a.path === b.path;

export interface Change {
  path: string;
  kind: "changed" | "added" | "removed";
  added: number;
  removed: number;
  diff: string;
}

// lineDiff is a longest-common-subsequence diff over lines, printed as a
// unified diff with three lines of context. The packet's files are briefs and
// profiles, so the quadratic table stays small; past the cap it reports the
// counts without the hunks.
export function lineDiff(before: string, after: string): Omit<Change, "path" | "kind"> {
  const a = before ? before.split("\n") : [];
  const b = after ? after.split("\n") : [];
  const n = a.length;
  const m = b.length;
  if (n * m > 16_000_000) {
    return { added: m, removed: n, diff: "(too large to diff in the page)" };
  }
  const w = m + 1;
  const lcs = new Uint32Array((n + 1) * w);
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      lcs[i * w + j] = a[i] === b[j] ? lcs[(i + 1) * w + j + 1] + 1 : Math.max(lcs[(i + 1) * w + j], lcs[i * w + j + 1]);
    }
  }
  type Op = { t: " " | "-" | "+"; s: string; i: number; j: number };
  const ops: Op[] = [];
  let i = 0;
  let j = 0;
  while (i < n || j < m) {
    if (i < n && j < m && a[i] === b[j]) ops.push({ t: " ", s: a[i], i: i++, j: j++ });
    else if (i < n && (j >= m || lcs[(i + 1) * w + j] >= lcs[i * w + j + 1])) ops.push({ t: "-", s: a[i], i: i++, j });
    else ops.push({ t: "+", s: b[j], i, j: j++ });
  }
  const added = ops.filter((o) => o.t === "+").length;
  const removed = ops.filter((o) => o.t === "-").length;
  // Changed lines closer than two contexts apart share one hunk.
  const ctx = 3;
  const groups: [number, number][] = [];
  ops.forEach((o, k) => {
    if (o.t === " ") return;
    const last = groups[groups.length - 1];
    if (last && k - last[1] <= ctx * 2 + 1) last[1] = k;
    else groups.push([k, k]);
  });
  const out: string[] = [];
  for (const [first, last] of groups) {
    const hunk = ops.slice(Math.max(0, first - ctx), Math.min(ops.length, last + ctx + 1));
    const oldLen = hunk.filter((o) => o.t !== "+").length;
    const newLen = hunk.filter((o) => o.t !== "-").length;
    out.push(`@@ -${hunk[0].i + 1},${oldLen} +${hunk[0].j + 1},${newLen} @@`);
    for (const o of hunk) out.push(o.t + o.s);
  }
  return { added, removed, diff: out.join("\n") };
}

// changesSince compares this packet with an earlier one: the profile, every
// authored file, and every member's orientation.
export function changesSince(p: Packet): Change[] {
  const before = p.since;
  if (!before) return [];
  const pairs: [string, string | undefined, string | undefined][] = [
    [p.profile.path, before.profile.content, p.profile.content],
  ];
  const paths = new Set([...before.files.map((f) => f.path), ...p.files.map((f) => f.path)]);
  for (const path of paths) {
    const was = before.files.find((f) => f.path === path);
    const now = p.files.find((f) => f.path === path);
    pairs.push([path, was && !was.absent ? was.content : undefined, now && !now.absent ? now.content : undefined]);
  }
  const names = new Set([...before.members.map((m) => m.name), ...p.members.map((m) => m.name)]);
  for (const name of names) {
    const was = before.members.find((m) => m.name === name);
    const now = p.members.find((m) => m.name === name);
    pairs.push([`${p.orientationGuest.split("/").pop()} for ${name}`, was?.orientation.content, now?.orientation.content]);
  }
  const changes: Change[] = [];
  for (const [path, was, now] of pairs) {
    if (was === now) continue;
    const kind = was === undefined ? "added" : now === undefined ? "removed" : "changed";
    changes.push({ path, kind, ...lineDiff(was ?? "", now ?? "") });
  }
  return changes;
}
