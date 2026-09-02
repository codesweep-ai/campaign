// The ONE place the fixture suite knows the viewer's DOM.
//
// When the page is rebuilt on @codesweep-ai/ui components (EventLanes, Card,
// Table, ...) the selectors below are what changes — never the values in
// expectations.json. Every entry is a CSS selector string (passed into the
// page verbatim) except the few documented helpers at the bottom, which build
// selector strings on the Node side.
//
// Conventions:
//   - "kinds" maps are { name: selector }; an element's kind is the first entry
//     whose selector it `matches`. Names are part of the oracle (they appear in
//     expectations.json), selectors are not.
//   - Anything read as text is trimmed and whitespace-collapsed by the probes.
//
// B1: the timeline is EventLanes. Its canvas is not queryable; the queryable
// surface is the census (one role="option" node per VISIBLE event, carrying
// data-event-index/-kind/-lane, plus one node per span carrying
// data-span-lane/-from/-to), the lane-label gutter, and the listbox scroller.

export const SEL = {
  // Page states
  ready: "main.scroller", // the app has mounted with readable run data
  noDataBanner: '#banner [data-component="Card"]', // the "no readable run data" banner (Card variant="danger")
  toast: '[data-component="Toast"]', // toast surfaces (corrupt payload)

  // Page head and chrome
  headName: "#h-name",
  headVerdict: "#h-verdict", // wrapper around the verdict StatusBadge
  headMeta: "#h-meta",
  banner: "#banner", // the in-page banner slot (clobbered archives)
  sectionTitle: ".section-title", // one per section card (Card header slot)
  header: "header",
  footer: "footer",
  themeToggle: '[data-component="ThemeToggle"]',
  themeStorageKey: "dispatch-viewer-theme",
  themeAttr: "data-theme", // on <html>

  // Timeline (EventLanes). lane labels live in the gutter; the events live in
  // the flat census — no DOM nests events under their lane. Lane labels are
  // reached through the documented data-event-lane-label hook plus the app's
  // own className, never a cs-component-* class.
  timelineCard: "#tl-card",
  lane: "#tl [data-event-lane-label]",
  laneKinds: {
    orchestrator: "[data-event-lane-label].orch",
    log: "[data-event-lane-label].loglane",
  }, // anything else: member
  timelineListbox: "#tl [data-event-lanes-scroller]", // the timeline's single Tab stop (role is still listbox)
  square: "#tl [data-event-index]", // one census option per visible event
  squareIndexAttr: "data-event-index",
  squareSelected: '#tl [data-event-index][aria-selected="true"]',
  squareLinked: "#tl [data-event-linked]", // census options carry data-event-linked (ui 0.2.0, a95bd05)
  squareKinds: {
    open: '#tl [data-event-kind="open"]',
    continue: '#tl [data-event-kind="continue"]',
    restart: '#tl [data-event-kind="restart"]',
    "reply-done": '#tl [data-event-kind="reply-done"]',
    "reply-bad": '#tl [data-event-kind="reply-bad"]',
    "verdict-ok": '#tl [data-event-kind="verdict-ok"]',
    "verdict-bad": '#tl [data-event-kind="verdict-bad"]',
    accept: '#tl [data-event-kind="accept"]',
    plan: '#tl [data-event-kind="plan"]',
    assessment: '#tl [data-event-kind="assessment"]',
  },
  connector: "#tl [data-span-lane]", // one census node per span
  legendRow: "#legend .lrow",
  showLog: "#showlog", // the "show orchestrator log" checkbox
  logHiddenClass: "blind", // class on <body> while the log is hidden

  // Issues
  issuesWrap: "#issues", // the app's scroll wrapper around the issues Table
  issueCount: "#issue-count",
  issueRow: "#issues [data-table-row]", // Table rows, via the documented hook
  issueRowKeyAttr: "data-table-row",
  issueCode: ".code",
  issueSeverities: {
    error: '#issues [data-table-row]:has([data-severity="error"])',
    severe: '#issues [data-table-row]:has([data-severity="severe"])',
    warning: '#issues [data-table-row]:has([data-severity="warning"])',
    info: '#issues [data-table-row]:has([data-severity="info"])',
  },

  // Orchestrator log pane
  logList: "#log-list",
  logRow: "#log-list [data-table-row]",
  logHiddenNotice: "#log-hidden",

  // Inspector
  inspector: "#inspector",
  inspectorKv: "#inspector dl:first-of-type", // the node / event / at rows
  inspectorTerm: "dt",
  inspectorValue: "dd",
  docPanel: "#inspector .doc",
  docKinds: { md: ".doc.md", json: ".doc.json" }, // anything else: raw
  docMdPanel: "#inspector .doc.md", // the rendered-markdown panel
  docMode: "#doc-mode", // the rendered/raw SegmentedControl
  docModeButton: "#doc-mode [data-segmented-option]",
  docModeActive: "#doc-mode [data-segmented-active]",
  docModeAttr: "data-segmented-option", // the option's value hook is the mode
};

/** Selector for the census option of event i. */
export const squareAt = (i) => `#tl [${SEL.squareIndexAttr}="${i}"]`;

/** Selector for a doc-mode option, addressed by its value hook — never by position. */
export const docModeButton = (mode) => `#doc-mode [data-segmented-option="${mode}"]`;
