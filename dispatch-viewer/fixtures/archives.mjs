// Synthetic run archives for the fixture suite, ported one for one from
// dispatch-viewer/internal/frames/frames_test.go (fixture(), TestReplyNameMismatch,
// TestCreatePhaseNoMission). Event times in an archive are file mtimes, so
// every file is written and then stamped, exactly as the Go tests do.
//
// Also builds the corrupt page: the committed shell with an unreadable payload
// spliced in where cli.go's assemble() would put the real one.
import { mkdirSync, readFileSync, utimesSync, writeFileSync } from "node:fs";
import path from "node:path";

function writer(root) {
  return (rel, body, at) => {
    const p = path.join(root, rel);
    mkdirSync(path.dirname(p), { recursive: true });
    writeFileSync(p, body);
    utimesSync(p, at, at);
  };
}

const minutes = (base, m) => new Date(base.getTime() + m * 60_000);

function fixture(root, clobbered) {
  const write = writer(root);
  const base = new Date("2026-08-18T07:00:00Z");
  const at = (m) => (clobbered ? minutes(base, 120) : minutes(base, m));
  write(
    "campaign.json",
    `{
	  "name":"fix1","id":"fix1-1234","createdAt":"2026-08-18T07:00:00Z","updatedAt":"2026-08-18T08:00:00Z",
	  "policy":{"continueAttempts":2,"restarts":1},
	  "members":[
	    {"name":"dev","role":"agent","cli":"opencode"},
	    {"name":"orchestrator","role":"orchestrator","cli":"codex"}
	  ]}`,
    at(0),
  );
  const o = "orchestrator";
  const d = path.join("agents", "dev");
  write(path.join(o, "input", "d001.md"), "# readback", at(1));
  write(path.join(o, "input", "m1.md"), "# mission", at(2));
  write(
    path.join(o, "output", "replies", "d001.json"),
    `{"dispatch":"d001","phase":"done","note":"rb","at":"2026-08-18T07:03:00Z"}`,
    at(3),
  );
  write(
    path.join(o, "output", "log.jsonl"),
    `{"at":"2026-08-18T07:05:00Z","kind":"plan","text":"# plan"}
{"at":"2026-08-18T07:02:00Z","kind":"accepted","text":"d001"}
{"at":"2026-08-18T07:30:00Z","kind":"accepted","text":"dev/d002"}
{"at":"2026-08-18T07:31:00Z","kind":"accepted","text":"d999"}
{"at":"2026-08-18T07:32:00Z","kind":"accepted","text":"dev/d002"}
{"at":"2026-08-18T07:41:00Z","kind":"accepted","text":"orchestrator/m1"}
`,
    at(31),
  );
  write(
    path.join(o, "output", "replies", "m1.json"),
    `{"dispatch":"m1","phase":"done","outcome":"campaign-met","note":"met","at":"2026-08-18T07:40:00Z"}`,
    at(40),
  );
  write(path.join(d, "input", "d001.md"), "# readback", at(1));
  write(
    path.join(d, "output", "replies", "d001.json"),
    `{"dispatch":"d001","phase":"done","note":"rb","at":"2026-08-18T07:04:00Z"}`,
    at(4),
  );
  write(path.join(d, "input", "d002.md"), "# work", at(10));
  write(path.join(d, "input", "d002.001.md"), "continue", at(25));
  write(
    path.join(d, "output", "replies", "d002.json"),
    `{"dispatch":"d002","phase":"done","note":"did it","at":"2026-08-18T07:25:00Z"}`,
    at(25),
  );
  write(path.join(d, "input", "d003.md"), "# more work, never answered", at(35));
  write(path.join(d, "input", "d003.001.md"), "c1", at(36));
  write(path.join(d, "input", "d003.002.md"), "c2", at(37));
  write(path.join(d, "input", "d003.003.md"), "c3", at(38));
}

function nameMismatch(root) {
  fixture(root, false);
  const write = writer(root);
  const at = new Date("2026-08-18T07:38:00Z");
  write(
    path.join("agents", "dev", "output", "replies", "d003.json"),
    `{"dispatch":"d002","phase":"done","note":"mislabeled","at":"2026-08-18T07:38:00Z"}`,
    at,
  );
  write(
    path.join("agents", "dev", "output", "replies", "d005.json"),
    `{"dispatch":"d005","phase":"done","note":"orphan","at":"2026-08-18T07:39:00Z"}`,
    at,
  );
}

function createPhase(root) {
  const write = writer(root);
  const base = new Date("2026-08-18T09:00:00Z");
  const at = (m) => minutes(base, m);
  write(
    "campaign.json",
    `{
	  "name":"cf1","id":"cf1-1","createdAt":"2026-08-18T09:00:00Z","updatedAt":"2026-08-18T09:30:00Z",
	  "members":[{"name":"orchestrator","role":"orchestrator","cli":"claude"},{"name":"dev","role":"agent","cli":"opencode"}]}`,
    at(0),
  );
  write(path.join("orchestrator", "input", "d001.md"), "# readback", at(1));
  write(path.join("agents", "dev", "input", "d001.md"), "# readback", at(1));
  write(
    path.join("agents", "dev", "output", "replies", "d001.json"),
    `{"dispatch":"d001","phase":"done","note":"rb","at":"2026-08-18T09:03:00Z"}`,
    at(3),
  );
  write(path.join("agents", "dev", "input", "d002.md"), "# readback again", at(10));
  write(
    path.join("agents", "dev", "output", "replies", "d002.json"),
    `{"dispatch":"d002","phase":"done","note":"rb2","at":"2026-08-18T09:12:00Z"}`,
    at(12),
  );
}

/** The synthetic archives, in the order the suite reports them. */
export const SYNTHETIC = ["healthy", "clobbered", "name-mismatch", "create-phase"];

/** Writes every synthetic archive under dir/<name>; returns { name: path }. */
export function writeSyntheticArchives(dir) {
  const out = {};
  const builders = {
    healthy: (r) => fixture(r, false),
    clobbered: (r) => fixture(r, true),
    "name-mismatch": nameMismatch,
    "create-phase": createPhase,
  };
  for (const name of SYNTHETIC) {
    const root = path.join(dir, name);
    mkdirSync(root, { recursive: true });
    builders[name](root);
    out[name] = root;
  }
  return out;
}

/**
 * The wide-finding archive: the healthy fixture plus a FLEET-ANOMALY.txt whose
 * body is one long unbroken token. That is the shape real archives produce
 * (the finding message carries the file body verbatim), and it is what makes
 * the issues table's Finding column unable to wrap — the condition the 600 px
 * containment gate (CF-60) exists to hold. Synthetic and invented; kept out of
 * SYNTHETIC so it adds no unit to the per-archive rows — only CF-60 renders it.
 */
export function writeWideFindingArchive(dir) {
  const root = path.join(dir, "wide-finding");
  mkdirSync(root, { recursive: true });
  fixture(root, false);
  const write = writer(root);
  write(
    "FLEET-ANOMALY.txt",
    "collector saw a duplicated sequence marker and resumed at the next heartbeat; first seen under " +
      "agents/synth-node/output/replies/".repeat(9) +
      "d000.json",
    new Date("2026-08-18T08:05:00Z"),
  );
  return root;
}

/**
 * The markdown archive: one minimal campaign whose single dispatch body carries
 * a table and three links — an ordinary https link, a `javascript:` URL and a
 * `data:` URL. Rendered only by CF-61 (the way the corrupt page serves only
 * CF-19), so the per-archive rows gain no unit. Synthetic and invented.
 */
export function writeMarkdownArchive(dir) {
  const root = path.join(dir, "markdown");
  mkdirSync(root, { recursive: true });
  const write = writer(root);
  const base = new Date("2026-08-19T07:00:00Z");
  const at = (m) => new Date(base.getTime() + m * 60_000);
  write(
    "campaign.json",
    `{
	  "name":"md1","id":"md1-1","createdAt":"2026-08-19T07:00:00Z","updatedAt":"2026-08-19T07:30:00Z",
	  "members":[{"name":"orchestrator","role":"orchestrator","cli":"codex"},{"name":"dev","role":"agent","cli":"opencode"}]}`,
    at(0),
  );
  write(path.join("orchestrator", "input", "d001.md"), "# readback", at(1));
  write(
    path.join("agents", "dev", "input", "d001.md"),
    `# Links and a table

[safe link](https://example.com/spec) then [javascript link](javascript:alert(1)) then [data link](data:text/plain;base64,AAAA)

| column | value |
| ------ | ----- |
| alpha  | one   |
`,
    at(1),
  );
  write(
    path.join("agents", "dev", "output", "replies", "d001.json"),
    `{"dispatch":"d001","phase":"done","note":"rb","at":"2026-08-19T07:03:00Z"}`,
    at(3),
  );
  return root;
}

/**
 * The corrupt page: the shell with a payload the app cannot parse. Uses the
 * same marker and block shape as cli.go assemble(), so what is under test is
 * the page's reaction, not the splice.
 */
export function writeCorruptPage(shellPath, outPath) {
  const shell = readFileSync(shellPath, "utf8");
  const marker = "<!--RUN-DATA-->";
  if (!shell.includes(marker)) throw new Error(`shell has no ${marker} marker: ${shellPath}`);
  const block = `<script type="application/json" id="run-data">{"schemaVersion":1,"campaign":{"name":"corrupt"</script>`;
  writeFileSync(outPath, shell.replace(marker, block));
  return outPath;
}
