// Browser plumbing: puppeteer from the app's node_modules, Chrome from the
// environment, fresh storage per page, and a settle that waits for the app to
// mount rather than for a timer.
import { createRequire } from "node:module";
import { existsSync } from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { SEL } from "./selectors.mjs";

export const HERE = path.dirname(new URL(import.meta.url).pathname);
export const APP_DIR = path.resolve(HERE, "..", "app");
const appRequire = createRequire(path.join(APP_DIR, "package.json"));

/** Resolves a package from dispatch-viewer/app's node_modules (or null). */
export function resolveFromApp(spec) {
  try {
    return appRequire.resolve(spec);
  } catch {
    return null;
  }
}

/** The browser executable: DISPATCH_FIXTURES_BROWSER, CHROME_BIN, PUPPETEER_EXECUTABLE_PATH. */
export function browserExecutable() {
  for (const k of ["DISPATCH_FIXTURES_BROWSER", "CHROME_BIN", "PUPPETEER_EXECUTABLE_PATH"]) {
    const v = process.env[k];
    if (v) {
      if (!existsSync(v)) throw new Error(`${k}=${v} does not exist`);
      return { path: v, from: k };
    }
  }
  return { path: undefined, from: "puppeteer's bundled Chrome" };
}

/** axe-core: DISPATCH_FIXTURES_AXE, else the vendored copy (vendor/axe-core). */
export const VENDORED_AXE = path.join(HERE, "vendor", "axe-core", "axe.min.js");
export function axeScript() {
  const env = process.env.DISPATCH_FIXTURES_AXE;
  if (env) {
    if (!existsSync(env)) throw new Error(`DISPATCH_FIXTURES_AXE=${env} does not exist`);
    return env;
  }
  return existsSync(VENDORED_AXE) ? VENDORED_AXE : null;
}

export async function launchBrowser() {
  const mod = resolveFromApp("puppeteer");
  if (!mod) {
    throw new Error(`puppeteer is not installed under ${APP_DIR}; run \`npm ci\` there first`);
  }
  const puppeteer = (await import(pathToFileURL(mod).href)).default;
  const exe = browserExecutable();
  const base = { headless: true, executablePath: exe.path };
  try {
    const browser = await puppeteer.launch(base);
    return { browser, executable: exe, sandbox: true };
  } catch (e) {
    // Hosts that disable unprivileged user namespaces refuse Chrome's sandbox
    // ("No usable sandbox"); retry without it, and say so.
    const browser = await puppeteer.launch({
      ...base,
      args: ["--no-sandbox", "--disable-setuid-sandbox"],
    });
    return { browser, executable: exe, sandbox: false, sandboxError: String(e).split("\n")[0] };
  }
}

/**
 * Opens url in a fresh browser context (its own localStorage), with the OS
 * colour scheme emulated and optional storage seeded before the first script
 * runs. Returns { page, close, log } where log collects every non-file
 * request, page error and console error for the page's lifetime.
 */
export async function openPage(browser, url, { scheme = "light", storage = null } = {}) {
  const ctx = await browser.createBrowserContext();
  const page = await ctx.newPage();
  await page.setViewport({ width: 1400, height: 900 });
  await page.emulateMediaFeatures([{ name: "prefers-color-scheme", value: scheme }]);
  const log = { requests: [], pageErrors: [], consoleErrors: [] };
  page.on("request", (r) => {
    const u = r.url();
    if (!u.startsWith("file:") && !u.startsWith("data:") && !u.startsWith("blob:") && !u.startsWith("about:")) {
      log.requests.push(u);
    }
  });
  page.on("pageerror", (e) => log.pageErrors.push(String(e).split("\n")[0]));
  page.on("console", (m) => {
    if (m.type() === "error") log.consoleErrors.push(m.text().split("\n")[0]);
  });
  if (storage) {
    await page.evaluateOnNewDocument((kv) => {
      try {
        for (const [k, v] of Object.entries(kv)) {
          if (v === null) localStorage.removeItem(k);
          else localStorage.setItem(k, v);
        }
      } catch {
        /* storage may be blocked; the probe reads back what happened */
      }
    }, storage);
  }
  await page.goto(url, { waitUntil: "load" });
  await settle(page);
  return { page, log, close: () => ctx.close() };
}

/** Waits for the app to mount (data or banner), then a beat for toasts. */
export async function settle(page) {
  await page.waitForFunction(
    (ready, banner) => !!(document.querySelector(ready) || document.querySelector(banner)),
    { timeout: 10_000 },
    SEL.ready,
    SEL.noDataBanner,
  );
  await new Promise((r) => setTimeout(r, 200));
}

export const fileUrl = (p, query = "") => pathToFileURL(path.resolve(p)).href + query;
