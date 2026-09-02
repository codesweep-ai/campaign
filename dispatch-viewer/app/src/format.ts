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
