#!/usr/bin/env node
// Move each @codesweep-ai pin in one package.json to its project's newest
// build, as `make repin` does for the Go tools.
//
//   node scripts/repin-npm.mjs DIR          the newer of the last CI build and the newest local one
//   LOCAL=0 node scripts/repin-npm.mjs DIR  the last CI build only
//
// A project's newest build is the newer, by UTC commit time, of two:
//
//   - the first commit in `built` of the ci-status.json its Pages site
//     publishes, which GitHub CI built and passed (codesweep-ai/dashboards
//     SPEC.md, "The status file");
//   - the newest commit the build store of this repository's owner holds for
//     it, which a clean `make ci` or `npm run ci` passed on this machine
//     (SPEC.md, "The local build store"). scripts/record-build.sh names it.
//
// The time is the one each npm version carries, which every build renders in
// UTC. A tie goes to the CI build, which can be pushed. A local build of a clean
// commit is byte-identical to what CI publishes for it, so a lockfile written
// against one stays valid once CI builds that commit. Where the status file
// lists no npm version, as when npmjs.com had not yet listed the one just
// published, the build's npm image carries the same version, and that is taken.
// A pin whose project lists neither build stays where it is, and says so. A
// range is left alone: moving one is a third-party upgrade, not a repin. Where
// the project's own checkout sits beside this one, a local build of a commit it
// holds on no branch, as after a rebase, is left out, and a newer one left out
// is named. Without such a checkout the store is taken as it stands.
//
// The install runs through scripts/with-npmrevs.sh, which serves the images CI
// publishes and the store's packages. The one exception is @codesweep-ai/npmrevs
// in a project with no go.mod, such as ui. There with-npmrevs.sh runs that very
// package, installed from npmjs.com before the registry it starts is up, so that
// pin moves only to a CI build npmjs.com holds, and never to a local one.
// $CS_STATUS_SITE, or the older $OSS_STATUS_SITE, is read in place of each
// project's Pages site, as <site>/<name>/ci-status.json, file:// included.
//
// The same file is in ledger, tracer, campaign and ui, so a fix made in one is
// copied to the others rather than rewritten there.

import { spawnSync } from "node:child_process";
import { existsSync, readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const SCOPE = "@codesweep-ai/";
const SITE = "https://codesweep-ai.github.io";
const NPMJS = "https://registry.npmjs.org";
const SECTIONS = ["dependencies", "devDependencies"];
const EXACT = /^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/;
const TIMEOUT = 30_000;

// The 14 digits of UTC commit time an npm version carries: 0.0.0-<stamp>-<sha12>
// for the Go wrappers, 0.3.1-dev.<stamp>.<sha7> for ui.
export function stampOf(version) {
  return /(?:^|[-.])(\d{14})[-.][0-9a-f]{7,}$/.exec(version ?? "")?.[1] ?? null;
}

// What each @codesweep-ai pin in `pkg` does, in the order the manifest lists
// them. `readStatus(name)` resolves to the project's parsed status file, or
// null where it cannot be read. `readLocal(name)` resolves to the newest local
// build naming an npm version for it, as { commit, version, recorded }, or null.
// `onNpmjs(name, version)` resolves to whether npmjs.com holds that version.
// `goMod` says whether the project has a go.mod, and `local` whether local
// builds are considered at all.
//
// Each step has a `line` to print, and a `kind`: "move" for a pin that moves to
// `to`, "current" for one already there, "held" and "left" for one that stays.
// A step that takes a local build says so in `local`.
export async function plan(pkg, { readStatus, readLocal = async () => null, onNpmjs, goMod, local = true }) {
  const steps = [];
  for (const section of SECTIONS) {
    for (const [name, from] of Object.entries(pkg[section] ?? {})) {
      if (!name.startsWith(SCOPE)) continue;
      const step = { name, section, from };
      steps.push(step);
      if (!EXACT.test(from)) {
        Object.assign(step, { kind: "left", line: `${name}: left at ${from}, a range rather than a pin` });
        continue;
      }

      // The last CI build, where its status file names one with a version.
      const status = await readStatus(name);
      const last = Array.isArray(status?.built) ? status.built[0] : undefined;
      let ci = null;
      let why = "its status file lists no build";
      if (/^[0-9a-f]{40}$/.test(last?.commit ?? "")) {
        const version = last.versions?.npm?.[name] ?? last.versions?.images?.[`npm/${name.slice(SCOPE.length)}`];
        if (version) ci = { commit: last.commit, version, stamp: stampOf(version) };
        else why = `its last build, ${last.commit.slice(0, 7)}, names no npm version`;
      }

      // The newest local build, unless this is the npmrevs a project with no
      // go.mod installs from npmjs.com, where no local build ever is.
      const bootstrap = name === `${SCOPE}npmrevs` && !goMod;
      const found = local && !bootstrap ? await readLocal(name) : null;
      const mine = found && stampOf(found.version) ? { ...found, stamp: stampOf(found.version) } : null;

      const takeLocal = mine && (!ci || (ci.stamp && mine.stamp > ci.stamp));
      const chosen = takeLocal ? mine : ci;
      if (!chosen) {
        const also = local && !bootstrap ? ", and the build store holds none" : "";
        Object.assign(step, { kind: "held", line: `${name}: held, as ${why}${also}` });
        continue;
      }
      const commit = chosen.commit.slice(0, 7);
      const line = takeLocal
        ? `${name}: ${commit}, a local build recorded ${(chosen.recorded ?? "").replace("T", " ")}`
        : `${name}: ${commit}, the last commit its CI built`;
      if (chosen.version === from) {
        Object.assign(step, { kind: "current", to: from, local: Boolean(takeLocal), line });
        continue;
      }
      if (bootstrap && !(await onNpmjs(name, chosen.version))) {
        Object.assign(step, {
          kind: "held",
          line:
            `${name}: held, as its last build, ${commit}, is only an image, ` +
            "and scripts/with-npmrevs.sh installs this package from npmjs.com",
        });
        continue;
      }
      Object.assign(step, { kind: "move", to: chosen.version, local: Boolean(takeLocal), line });
    }
  }
  return steps;
}

async function get(url) {
  try {
    return await fetch(url, { signal: AbortSignal.timeout(TIMEOUT) });
  } catch {
    return null;
  }
}

async function readStatus(name) {
  const site = process.env.CS_STATUS_SITE || process.env.OSS_STATUS_SITE || SITE;
  const url = `${site}/${name.slice(SCOPE.length)}/ci-status.json`;
  try {
    if (url.startsWith("file:")) return JSON.parse(readFileSync(fileURLToPath(url), "utf8"));
    const res = await get(url);
    return res?.ok ? await res.json() : null;
  } catch {
    return null;
  }
}

// Whether the checkout of `name`'s project beside `root` holds `commit` on a
// branch. True where there is no such checkout, since nothing then says it is
// gone: a campaign member, say, holds the store without the source.
export function siblingHolds(root, name, commit) {
  const short = name.slice(SCOPE.length);
  const dir = path.join(path.dirname(root), short);
  let ours = false;
  try {
    ours = JSON.parse(readFileSync(path.join(dir, "package.json"), "utf8")).name === name;
  } catch {
    // no package.json, or not one that parses
  }
  if (!ours) {
    try {
      const mod = /^module\s+(\S+)/m.exec(readFileSync(path.join(dir, "go.mod"), "utf8"));
      ours = Boolean(mod && mod[1].split("/").pop() === short);
    } catch {
      // no go.mod either: not the project's checkout
    }
  }
  if (!ours) return true;
  const r = spawnSync("git", ["-C", dir, "for-each-ref", "--count=1", "--contains", commit, "refs/heads"], {
    encoding: "utf8",
  });
  return r.status === 0 && r.stdout.trim() !== "";
}

// The newest build of `name` in `store`, the build store scripts/record-build.sh
// names, as { commit, version, recorded }, among those `holds(commit)` keeps.
// A newer build left out is said on stderr.
export function newestLocal(store, name, holds = () => true) {
  let entries;
  try {
    entries = readdirSync(path.join(store, "status", name.slice(SCOPE.length)));
  } catch {
    return null;
  }
  let best = null;
  let gone = null;
  for (const f of entries) {
    if (!f.endsWith(".json")) continue;
    let e;
    try {
      e = JSON.parse(readFileSync(path.join(store, "status", name.slice(SCOPE.length), f), "utf8"));
    } catch {
      continue;
    }
    const version = e.versions?.npm?.[name];
    const stamp = stampOf(version);
    if (!stamp || !/^[0-9a-f]{40}$/.test(e.commit ?? "")) continue;
    if (!holds(e.commit)) {
      if (!gone || stamp > stampOf(gone.version)) gone = { commit: e.commit, version };
      continue;
    }
    if (!best || stamp > stampOf(best.version)) best = { commit: e.commit, version, recorded: e.recorded };
  }
  if (gone && (!best || stampOf(gone.version) > stampOf(best.version))) {
    const short = name.slice(SCOPE.length);
    console.error(`${name}: ${gone.commit.slice(0, 7)}, a newer local build, is left out: ../${short} holds that commit on no branch`);
  }
  return best;
}

async function onNpmjs(name, version) {
  const res = await get(`${NPMJS}/${name.replace("/", "%2f")}/${encodeURIComponent(version)}`);
  return Boolean(res?.ok);
}

async function main() {
  const dir = process.argv[2];
  if (!dir || process.argv.length > 3) {
    console.error("usage: repin-npm.mjs DIR");
    process.exit(2);
  }
  const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
  const pkg = JSON.parse(readFileSync(path.join(dir, "package.json"), "utf8"));
  const record = spawnSync(path.join(root, "scripts", "record-build.sh"), ["store"], { encoding: "utf8" });
  const store = record.status === 0 ? record.stdout.trim() : null;
  const steps = await plan(pkg, {
    readStatus,
    readLocal: async (name) => (store ? newestLocal(store, name, (c) => siblingHolds(root, name, c)) : null),
    onNpmjs,
    goMod: existsSync(path.join(root, "go.mod")),
    local: process.env.LOCAL !== "0",
  });
  for (const step of steps) console.log(step.line);
  for (const section of SECTIONS) {
    const specs = steps.filter((s) => s.kind === "move" && s.section === section).map((s) => `${s.name}@${s.to}`);
    if (specs.length === 0) continue;
    const save = section === "devDependencies" ? "--save-dev" : "--save-prod";
    const args = ["npm", "install", "--no-audit", "--no-fund", "--save-exact", save, ...specs];
    const run = spawnSync(path.join(root, "scripts", "with-npmrevs.sh"), args, { cwd: dir, stdio: "inherit" });
    if (run.status !== 0) {
      console.error(`repin-npm: ${args.join(" ")} failed in ${dir}`);
      process.exit(run.status || 1);
    }
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  await main();
}
