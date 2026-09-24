#!/usr/bin/env node
// Move each @codesweep-ai pin in one package.json to the last build of the
// project that publishes it, as `make repin` does for the Go tools.
//
//   node scripts/repin-npm.mjs DIR
//
// Every project's Pages site serves a ci-status.json whose `built` lists the
// commits its CI built and passed, newest first, each with the npm version it
// was published under (codesweep-ai/dashboards SPEC.md). A pin moves to the
// version of the first, so it never lands on a commit CI failed, is still
// building, or never built because it changed only the ledger. Where the file
// lists no npm version, as when npmjs.com had not yet listed the one just
// published, the build's npm image carries the same version, and that is taken.
// A pin whose project's file cannot be read or lists no build stays where it
// is, and says so. A range is left alone: moving one is a third-party upgrade,
// not a repin.
//
// The install runs through scripts/with-npmrevs.sh, because a build that has
// not been released reaches npm only as an image. The one exception is
// @codesweep-ai/npmrevs in a project with no go.mod, such as ui. There
// with-npmrevs.sh runs that very package, installed from npmjs.com before the
// registry it starts is up, so that pin moves only to a version npmjs.com holds.
//
// The same file is in ledger, tracer, campaign and ui, so a fix made in one is
// copied to the others rather than rewritten there.

import { spawnSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const SCOPE = "@codesweep-ai/";
const SITE = "https://codesweep-ai.github.io";
const NPMJS = "https://registry.npmjs.org";
const SECTIONS = ["dependencies", "devDependencies"];
const EXACT = /^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/;
const TIMEOUT = 30_000;

// What each @codesweep-ai pin in `pkg` does, in the order the manifest lists
// them. `readStatus(name)` resolves to the project's parsed status file, or
// null where it cannot be read. `onNpmjs(name, version)` resolves to whether
// npmjs.com holds that version. `goMod` says whether the project has a go.mod.
//
// Each step has a `line` to print, and a `kind`: "move" for a pin that moves to
// `to`, "current" for one already there, "held" and "left" for one that stays.
export async function plan(pkg, { readStatus, onNpmjs, goMod }) {
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
      const status = await readStatus(name);
      const last = Array.isArray(status?.built) ? status.built[0] : undefined;
      if (!/^[0-9a-f]{40}$/.test(last?.commit ?? "")) {
        Object.assign(step, { kind: "held", line: `${name}: held, as its status file lists no build` });
        continue;
      }
      const commit = last.commit.slice(0, 7);
      const to = last.versions?.npm?.[name] ?? last.versions?.images?.[`npm/${name.slice(SCOPE.length)}`];
      if (!to) {
        Object.assign(step, {
          kind: "held",
          line: `${name}: held, as its last build, ${commit}, names no npm version`,
        });
        continue;
      }
      if (to === from) {
        Object.assign(step, { kind: "current", to, line: `${name}: ${commit}, the last commit its CI built` });
        continue;
      }
      if (name === `${SCOPE}npmrevs` && !goMod && !(await onNpmjs(name, to))) {
        Object.assign(step, {
          kind: "held",
          line:
            `${name}: held, as its last build, ${commit}, is only an image, ` +
            "and scripts/with-npmrevs.sh installs this package from npmjs.com",
        });
        continue;
      }
      Object.assign(step, { kind: "move", to, line: `${name}: ${commit}, the last commit its CI built` });
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
  const res = await get(`${SITE}/${name.slice(SCOPE.length)}/ci-status.json`);
  if (!res?.ok) return null;
  try {
    return await res.json();
  } catch {
    return null;
  }
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
  const steps = await plan(pkg, { readStatus, onNpmjs, goMod: existsSync(path.join(root, "go.mod")) });
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
