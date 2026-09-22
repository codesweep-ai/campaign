// Formatting and rendering helpers, ported one for one from the pre-React
// viewer so the rendered text is byte-identical to what it produced.

export const fmtT = (iso: string | undefined): string =>
  iso ? iso.replace("T", " ").replace(/(\.\d+)?Z?$/, "") + "Z" : "";

export const shortT = (iso: string | undefined): string => (iso ? iso.slice(11, 19) : "");

export const dur = (a: string | undefined, b: string | undefined): string => {
  const ms = +new Date(b as string) - +new Date(a as string);
  if (!(ms >= 0)) return "";
  const m = Math.floor(ms / 60000);
  const s = Math.floor(ms / 1000) % 60;
  return m + "m" + String(s).padStart(2, "0") + "s";
};

// Markdown rendering moved to @codesweep-ai/ui's MarkdownViewer (the
// lightweight markdown entry): it covers tables and drops unsafe link schemes,
// which the hand-rolled renderer this comment replaces never did.

/** Seconds since the campaign began, as h:mm:ss (or m:ss under an hour). */
export const fmtElapsed = (sec: number): string => {
  const s = Math.max(0, Math.floor(sec));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const r = s % 60;
  return h > 0
    ? h + ":" + String(m).padStart(2, "0") + ":" + String(r).padStart(2, "0")
    : m + ":" + String(r).padStart(2, "0");
};

/** h:mm for the axis. */
export const fmtHM = (sec: number): string => {
  const s = Math.max(0, Math.round(sec));
  return Math.floor(s / 3600) + ":" + String(Math.floor((s % 3600) / 60)).padStart(2, "0");
};

/** Elapsed seconds as h:mm:ss, the form an address carries. */
export const fmtClock = (sec: number): string => {
  const s = Math.max(0, Math.round(sec));
  return Math.floor(s / 3600) + ":" + String(Math.floor((s % 3600) / 60)).padStart(2, "0") + ":" + String(s % 60).padStart(2, "0");
};

/** h:mm:ss, m:ss or plain seconds back to seconds, or null. */
export const parseClock = (text: string): number | null => {
  if (!/^\d+(:\d{1,2}){0,2}$/.test(text)) return null;
  return text.split(":").reduce((acc, part) => acc * 60 + Number(part), 0);
};

/** A duration in ms as 1h02m, 4m30s, 12s or 350ms. */
export const fmtMs = (ms: number): string => {
  if (ms < 1000) return Math.round(ms) + "ms";
  const s = Math.round(ms / 1000);
  if (s < 60) return s + "s";
  const m = Math.floor(s / 60);
  if (m < 60) return m + "m" + String(s % 60).padStart(2, "0") + "s";
  return Math.floor(m / 60) + "h" + String(m % 60).padStart(2, "0") + "m";
};
