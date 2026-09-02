# Dispatch viewer fixtures

Behavioural fixtures for `cs-dispatch-viewer`: the oracle a rebuild of the
viewer page is held to. Pixel parity is not a gate here. What is measured is
what the page *does* — structure, interaction end states, accessibility,
keyboard reach, size and hygiene — on run archives rendered by the real binary.

```sh
cd dispatch-viewer/app && npm ci          # once: puppeteer (no Chrome download needed)
npm run fixtures                          # from dispatch-viewer/app
make fixtures                             # the same, from the repo root
```

The runner builds `cs-dispatch-viewer` from the checkout (so it embeds the
`viewer.html` the tree has), writes the synthetic archives, renders one page per
archive, drives a headless Chrome over each, and compares what it measured with
`expectations.json`.

## Inputs

| What | Where it comes from |
|---|---|
| Browser | `DISPATCH_FIXTURES_BROWSER`, then `CHROME_BIN`, then `PUPPETEER_EXECUTABLE_PATH`; else puppeteer's bundled Chrome. Relaunched with `--no-sandbox` when the host refuses the sandbox, and says so. |
| Renderer | `--viewer <bin>` / `DISPATCH_FIXTURES_VIEWER`; else `go build ./dispatch-viewer/cmd/cs-dispatch-viewer`. |
| Synthetic archives | `archives.mjs` — `healthy`, `clobbered`, `name-mismatch`, `create-phase`, ported line for line from `dispatch-viewer/internal/frames/frames_test.go` (event times are file mtimes, stamped at write). |
| Wide-finding archive | `archives.mjs` — the healthy fixture plus a `FLEET-ANOMALY.txt` whose body is one long unbroken token, which is what makes the Finding column unable to wrap. Rendered only by CF-60 so the per-archive rows gain no unit. |
| Markdown archive | `archives.mjs` — one dispatch whose body carries a table and three links (`https:`, `javascript:`, `data:`). Rendered only by CF-61, for the same reason. |
| Corrupt page | the shell with an unparseable `run-data` block spliced in the way `cli.go assemble()` does. |
| axe-core | `vendor/axe-core/axe.min.js` (4.13.0, MPL-2.0, unmodified, LICENSE beside it) unless `DISPATCH_FIXTURES_AXE` names another. CF-20 never skips for lack of axe. |

Flags: `--strict` (see below), `--update` (re-record `expectations.json`),
`--approve <dispatch-id>` + `--reason "<one sentence>"` (authorise a gated
`--update`; see *Re-recording*), `--out <dir>` / `--keep-out` (keep the
renders, archives and `report.json`).

A guest with only the repo, `npm ci` done in `app/`, Go, and `$CHROME_BIN` can
run the whole suite: nothing else is fetched or looked up outside the checkout.

## Files

- `run.mjs` — the runner and the check registry (ids, statuses, targets, summaries).
- `selectors.mjs` — **the only file that knows the DOM.** When the page moves to
  `EventLanes`, `Card`, `Table` and friends, change the selectors here. Never
  the values in `expectations.json`.
- `probes.mjs` — the measurements, written against `SEL` only.
- `archives.mjs` — the synthetic archives and the corrupt page.
- `vendor/axe-core/` — axe-core as shipped, with its LICENSE and a NOTICE.
- `browser.mjs` — puppeteer from `app/node_modules`, fresh storage per page, settle-on-mount.
- `expectations.json` — one record per check:
  `{id, title, status, target?, budget?, value, note}`.

## Every check is reproducible

All 29 checks measure only archives this suite generates, so a clone reproduces
every value in `expectations.json`. Nothing is asserted that a reader cannot
re-measure.

The suite once also rendered a real campaign archive, kept outside the
repository because it is agent-session data and this repo is public. Its rows
were marked `hostOnly` and its frozen values stayed in the file — assertions
nobody who cloned the repo could verify, refresh, or notice going stale, carried
forward untouched by every `--update`. That archive and the `hostOnly` mechanism
are both gone. To look at a real archive, render it with `cs-dispatch-viewer`,
which is what the binary is for.

## Statuses

- `keep` — the value is the contract. Any difference fails the run (exit 1).
  Size checks carry a `budget` instead: the run fails only when a field exceeds it.
- `must-change` — the value is today's baseline and the `target` says where it
  has to get to. The runner reports `UNCHANGED`, `PROGRESS` or `MET` per
  check and never fails on these — unless `--strict`, which turns every unmet
  target, every skipped input and a missing axe-core into failures. `--strict`
  is the bar for calling the rebuild done.

Per-archive values are compared archive by archive, so a mismatch names the
archive and the first differing field.

## Checks

| Group | Ids | What is measured |
|---|---|---|
| A. Structure (both themes) | CF-01..06 | lanes in order with kind and per-lane event count; squares, kinds present, connectors; page head, banner, timeline hidden; issues count and `code:severity` rows; legend rows and section headers; dark theme yields the same structure |
| B. Interaction | CF-10..19 | click every square → inspector rows, doc panels, accept↔reply link; ArrowRight/ArrowLeft sequences; hidden-square landings with the log off; issue row → selection; log toggle end states; rendered/raw segment; theme cycle + persistence; `?theme=`; corrupt payload |
| C. Accessibility | CF-20 | axe-core violations by rule, per theme × state (load; log shown + event selected) |
| D. Keyboard | CF-30..35 | first 20 Tab stops; the timeline's single listbox stop; issue rows under the table's roving tab stop (arrows/Home/End reach every row); Escape; Home/End; focus follows a click |
| E. Size | CF-40..41 | `viewer.html` bytes and gzip against a 300,000-byte budget; rendered page bytes, gzip, payload share |
| F. Hygiene | CF-50..51 | no non-`file:` requests, page errors or console errors across every page opened; `<script>` elements in the rendered page |
| G. Layout | CF-60 | the issues table stays inside its card at a 600 px viewport, against the wide-finding archive; containment, not a pixel width |
| H. Markdown | CF-61 | rendered markdown drops `javascript:`/`data:` links (empty href), keeps an ordinary link, and renders tables as tables |

## Re-recording

`--update` rewrites `expectations.json` from the current page, copying status,
target and note from the registry in `run.mjs`. Do it only for a deliberate
change of contract, with the diff reviewed; a `keep` value that changed because
the page changed is a finding, not a reason to re-record.

The write path is gated. Changing a `keep` row's `value` or a `must-change`
row's `target` requires `--approve <dispatch-id>` and `--reason "<one
sentence>"` (both or neither — one alone is an error), and the change is
recorded on the row as an `"approval"` block (`id`, `reason`, `previous`,
`measured`) so the diff carries its own permission slip. A write that changes
neither needs no flags. A new row is never refused; it is recorded with an
`"origin"` block instead of an approval, whose `id` is the `--approve` id when
the flags are supplied and the member name (`campaign`) otherwise — never a
placeholder — and whose `reason` is the `--reason` text when supplied, else
one sentence naming why the row exists. Passing the flags on a write with
nothing gated is allowed and is how a real dispatch id gets stamped onto a new
row. A row the run could not
measure keeps its existing value and approval verbatim; a row that can neither
be measured nor carried forward is an error, never a blank. When the gate
refuses, it writes nothing at all, exits non-zero, names every gated row, and
prints the authorising command.
