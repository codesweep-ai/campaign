package covmap

import (
	"fmt"
	"html"
	"strings"

	"github.com/codesweep-ai/campaign/internal/model"
)

// Render produces covmap/covmap.html as a pure function of the rubric
// and the run-results — no wall-clock, so the output is byte-stable for a
// given results state. Cells are colored only by executed proofs; every
// unproven cell renders as a dashed gap.
func Render(reg *Registry, res *Results) string {
	var b strings.Builder
	b.WriteString(htmlHead)

	b.WriteString("<main>\n<h1>cs-campaign behavioral coverage — run results</h1>\n")
	b.WriteString("<p class=\"gen\">GENERATED — rubric: <code>covmap/behaviors.json</code>, results: <code>covmap/results.json</code>; " +
		"regenerate with <code>make covmap</code>. Every colored cell is backed by a test that actually executed its proving " +
		"assertion (the record carries the commit and time it ran). Dashed cells are unproven — the visible backlog.</p>\n")

	b.WriteString("<div class=\"legend\">\n")
	for _, t := range TierOrder {
		fmt.Fprintf(&b, "  <span class=\"chip\"><span class=\"swatch tier-%s\">%s</span> %s</span>\n",
			t, TierLetters[t], html.EscapeString(TierLabels[t]))
	}
	b.WriteString("  <span class=\"chip\"><span class=\"swatch gap\"></span> no run-result</span>\n</div>\n")

	// Adapter × role heatmap over the full declared universe.
	b.WriteString("<h2>Adapter behaviors</h2>\n<div class=\"scroll\"><table>\n<thead>\n<tr><th rowspan=\"2\" class=\"rowhead\">behavior</th>")
	for _, a := range model.AdapterCLIs {
		fmt.Fprintf(&b, "<th colspan=\"2\" class=\"adapter\">%s</th>", html.EscapeString(a))
	}
	b.WriteString("</tr>\n<tr>")
	for range model.AdapterCLIs {
		b.WriteString("<th class=\"role\">orch</th><th class=\"role\">agent</th>")
	}
	b.WriteString("</tr>\n</thead>\n<tbody>\n")
	for _, behavior := range reg.Rows("adapter") {
		fmt.Fprintf(&b, "<tr><td class=\"rowhead\" title=\"%s\">%s</td>",
			html.EscapeString(behavior.DesignRef), html.EscapeString(behavior.Title))
		for _, a := range model.AdapterCLIs {
			for _, r := range model.Roles {
				tier := res.CellTier(behavior.ID, a, r)
				if tier == "" {
					fmt.Fprintf(&b, "<td class=\"cell gap\" title=\"%s\"></td>",
						html.EscapeString(fmt.Sprintf("%s — %s/%s: no run-result", behavior.Title, a, r)))
					continue
				}
				fmt.Fprintf(&b, "<td class=\"cell tier-%s\" title=\"%s\">%s</td>",
					tier, html.EscapeString(cellTitle(res, behavior, a, r)), TierLetters[tier])
			}
		}
		b.WriteString("</tr>\n")
	}
	b.WriteString("</tbody>\n</table></div>\n")

	// Per-behavior evidence: the actual run records.
	b.WriteString("<h2>Run records</h2>\n")
	for _, behavior := range reg.Rows("adapter") {
		fmt.Fprintf(&b, "<details><summary>%s <span class=\"muted\">(%s)</span></summary>\n<ul>\n",
			html.EscapeString(behavior.Title), html.EscapeString(behavior.DesignRef))
		any := false
		for _, a := range model.AdapterCLIs {
			for _, r := range model.Roles {
				records := dedupeRoleless(res.CellRecords(behavior.ID, a, r), r)
				if len(records) == 0 {
					continue
				}
				any = true
				fmt.Fprintf(&b, "<li><b>%s / %s</b>", html.EscapeString(a), html.EscapeString(r))
				for _, rec := range records {
					fmt.Fprintf(&b, "<br><span class=\"tiertag tier-%s\">%s</span> %s", rec.Tier, TierLetters[rec.Tier], html.EscapeString(recordLine(rec)))
				}
				b.WriteString("</li>\n")
			}
		}
		if !any {
			b.WriteString("<li class=\"muted\">no run-results yet</li>\n")
		}
		b.WriteString("</ul>\n</details>\n")
	}

	// Core behaviors.
	b.WriteString("<h2>Core behaviors (adapter-agnostic)</h2>\n<div class=\"scroll\"><table>\n<thead><tr><th class=\"rowhead\">behavior</th><th>proven</th><th>run records</th></tr></thead>\n<tbody>\n")
	for _, behavior := range reg.Rows("core") {
		records := res.CellRecords(behavior.ID, "", "")
		tier := res.CellTier(behavior.ID, "", "")
		cell := "<td class=\"cell gap\"></td>"
		if tier != "" {
			cell = fmt.Sprintf("<td class=\"cell tier-%s\">%s</td>", tier, TierLetters[tier])
		}
		var parts []string
		for _, rec := range records {
			parts = append(parts, recordLine(rec))
		}
		ev := "no run-results yet"
		if len(parts) > 0 {
			ev = strings.Join(parts, " · ")
		}
		fmt.Fprintf(&b, "<tr><td class=\"rowhead\" title=\"%s\">%s</td>%s<td class=\"ev\">%s</td></tr>\n",
			html.EscapeString(behavior.DesignRef), html.EscapeString(behavior.Title), cell, html.EscapeString(ev))
	}
	b.WriteString("</tbody>\n</table></div>\n</main>\n</body>\n</html>\n")
	return b.String()
}

// dedupeRoleless keeps role-agnostic records from repeating identically in
// both role listings: they render under the first role only, tagged.
func dedupeRoleless(records []Record, role string) []Record {
	if role == model.Roles[0] {
		return records
	}
	var out []Record
	for _, r := range records {
		if r.Role != "" {
			out = append(out, r)
		}
	}
	return out
}

func cellTitle(res *Results, behavior Behavior, adapter, role string) string {
	var parts []string
	for _, rec := range res.CellRecords(behavior.ID, adapter, role) {
		parts = append(parts, TierLetters[rec.Tier]+": "+recordLine(rec))
	}
	return fmt.Sprintf("%s — %s/%s\n%s", behavior.Title, adapter, role, strings.Join(parts, "\n"))
}

// recordLine renders one run record as text: test (repo) @ commit, date.
func recordLine(rec Record) string {
	day := rec.Time
	if len(day) >= 10 {
		day = day[:10]
	}
	s := fmt.Sprintf("%s (%s) @ %s, %s", rec.Test, rec.Repo, rec.Commit, day)
	if rec.Role == "" && rec.Adapter != "" {
		s += " [both roles]"
	}
	return s
}

// htmlHead: system sans, palette roles as CSS custom properties (a validated
// three-step ordinal single-hue ramp, stepped separately for the dark
// surface), recessive chrome, and per-tier cell ink.
const htmlHead = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>cs-campaign behavioral coverage</title>
<style>
:root {
  color-scheme: light dark;
  --surface: #fcfcfb; --plane: #f9f9f7;
  --ink: #0b0b0b; --ink-2: #52514e; --muted: #898781;
  --grid: #e1e0d9; --ring: rgba(11,11,11,0.10);
  --tier-unit-bg: #86b6ef;    --tier-unit-ink: #0b0b0b;
  --tier-scripts-bg: #2a78d6; --tier-scripts-ink: #ffffff;
  --tier-live-bg: #184f95;    --tier-live-ink: #ffffff;
}
@media (prefers-color-scheme: dark) {
  :root {
    --surface: #1a1a19; --plane: #0d0d0d;
    --ink: #ffffff; --ink-2: #c3c2b7; --muted: #898781;
    --grid: #2c2c2a; --ring: rgba(255,255,255,0.10);
    --tier-unit-bg: #184f95;    --tier-unit-ink: #ffffff;
    --tier-scripts-bg: #2a78d6; --tier-scripts-ink: #ffffff;
    --tier-live-bg: #86b6ef;    --tier-live-ink: #0b0b0b;
  }
}
html { background: var(--plane); }
body { margin: 0; font: 14px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif; color: var(--ink); }
main { max-width: 980px; margin: 0 auto; padding: 24px 16px 48px; }
h1 { font-size: 20px; margin: 0 0 4px; }
h2 { font-size: 16px; margin: 28px 0 8px; }
.gen { color: var(--ink-2); margin: 4px 0 16px; }
.legend { display: flex; flex-wrap: wrap; gap: 12px 20px; margin: 12px 0 4px; }
.chip { display: inline-flex; align-items: center; gap: 6px; color: var(--ink-2); }
.swatch { display: inline-flex; align-items: center; justify-content: center;
  width: 22px; height: 18px; border-radius: 4px; font-size: 11px; font-weight: 600; }
.scroll { overflow-x: auto; background: var(--surface); border: 1px solid var(--ring);
  border-radius: 8px; padding: 12px; }
table { border-collapse: collapse; width: 100%; }
th { font-weight: 600; color: var(--ink-2); text-align: left; padding: 4px 8px; }
th.adapter { text-align: center; border-bottom: 1px solid var(--grid); }
th.role { text-align: center; font-weight: 400; color: var(--muted); font-size: 12px; }
td.rowhead, th.rowhead { padding: 6px 10px 6px 0; border-top: 1px solid var(--grid); min-width: 220px; }
td.cell { width: 44px; min-width: 44px; height: 26px; text-align: center; vertical-align: middle;
  border-top: 1px solid var(--grid); font-size: 12px; font-weight: 600; padding: 3px;
  border-left: 2px solid var(--surface); border-right: 2px solid var(--surface); }
td.cell.tier-unit, .swatch.tier-unit, .tiertag.tier-unit { background: var(--tier-unit-bg); color: var(--tier-unit-ink); }
td.cell.tier-scripts, .swatch.tier-scripts, .tiertag.tier-scripts { background: var(--tier-scripts-bg); color: var(--tier-scripts-ink); }
td.cell.tier-live, .swatch.tier-live, .tiertag.tier-live { background: var(--tier-live-bg); color: var(--tier-live-ink); }
td.cell.gap, .swatch.gap { background: transparent; outline: 1px dashed var(--muted); outline-offset: -3px; }
td.ev { color: var(--ink-2); border-top: 1px solid var(--grid); padding: 6px 8px; }
.tiertag { display: inline-flex; align-items: center; justify-content: center;
  min-width: 18px; height: 16px; border-radius: 3px; font-size: 11px; font-weight: 600; padding: 0 3px; }
details { background: var(--surface); border: 1px solid var(--ring); border-radius: 8px;
  padding: 8px 12px; margin: 8px 0; }
summary { cursor: pointer; font-weight: 600; }
details ul { margin: 8px 0 4px; padding-left: 18px; }
details li { margin: 6px 0; color: var(--ink-2); }
details li b { color: var(--ink); font-weight: 600; }
.muted { color: var(--muted); }
code { font-size: 12.5px; }
</style>
</head>
<body>
`
