package frames

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/codesweep-ai/campaign/internal/protocol"
)

// Traces is what the tracer adds to a run: every member's sessions, drawn as
// the tracer's own strip, and the anchors that join each dispatch to exact
// events in those sessions. The archive holds each member's raw CLI store in
// transcript/cli-evidence.tgz; cs-tracer normalize turns each store into the
// trajectory format its SPEC fixes, and this file reads only that.
type Traces struct {
	// Tool is the tracer's version line, so a page says what read the stores.
	Tool     string    `json:"tool"`
	Sessions []Session `json:"sessions"`
	Anchors  []Anchor  `json:"anchors"`
}

// Session is one trajectory, owned by the member whose store it came from.
type Session struct {
	ID     string `json:"id"`
	Node   string `json:"node"`
	Source string `json:"source"` // claude-code | codex | opencode
	// Page is the trace page's path relative to the dispatch page, set only
	// when the site was exported beside it.
	Page             string `json:"page,omitempty"`
	Parent           string `json:"parent,omitempty"`
	ParentEventIndex *int   `json:"parentEventIndex,omitempty"`
	StartedAt        string `json:"startedAt,omitempty"`
	EndedAt          string `json:"endedAt,omitempty"`
	Model            string `json:"model,omitempty"`
	Events           int    `json:"events"`
	Strip            []Step `json:"strip"`
	// events holds the chunk documents while anchors are joined and facts
	// are counted; the page gets the strip and the anchors, never the event
	// bodies. summary and raw are the tracer's own documents, kept so a site
	// can write the trajectory as the tracer wrote it.
	events  []tracerEvent
	totals  tracerTotals
	summary json.RawMessage
	raw     []json.RawMessage
}

// Step is one strip entry, the tracer's per-event shape, plus a short text
// for the hover and the wait flag the page draws as a hatched span.
type Step struct {
	I        int    `json:"i"`
	Kind     string `json:"kind"`
	TS       string `json:"ts,omitempty"`
	WorkMs   *int64 `json:"workMs,omitempty"`
	IdleMs   *int64 `json:"idleMs,omitempty"`
	ActiveMs *int64 `json:"activeMs,omitempty"`
	TurnEnd  bool   `json:"turnEnd,omitempty"`
	Error    bool   `json:"error,omitempty"`
	Label    string `json:"label,omitempty"`
	Subtask  bool   `json:"subtask,omitempty"`
	Child    string `json:"childSessionId,omitempty"`
	// Text is the first line of the event, or the tool command, cut short.
	Text string `json:"text,omitempty"`
	// Wait marks a harness wait call: time the orchestrator spent waiting
	// for members rather than working.
	Wait bool `json:"wait,omitempty"`
	// Addr is the step's address on the dispatch page and IdleAddr the
	// address of the idle mark after it (addresses, in frames.go).
	Addr     string `json:"addr,omitempty"`
	IdleAddr string `json:"idleAddr,omitempty"`
}

// Anchor joins one dispatch to one event in one session. Kind is sent
// (the orchestrator's send call), arrived (the member's user turn that
// carries the dispatch), replied (the member's reply call) or accepted (the
// orchestrator's accept call).
type Anchor struct {
	Node     string `json:"node"`
	Dispatch string `json:"dispatch"`
	Kind     string `json:"kind"`
	Session  string `json:"session"`
	I        int    `json:"i"`
}

// TraceOptions says how to reach the tracer and where a site goes.
type TraceOptions struct {
	// Tracer is the cs-tracer binary. Empty means look on PATH.
	Tracer string
	// Site, when set, is the directory the dispatch page will live in; the
	// tracer's export goes under Site/tracer and every session gets a Page.
	// A site cannot be built without a tracer.
	Site string
	// Stderr receives the tracer's own diagnostics, and the warning when
	// there is no tracer.
	Stderr io.Writer
}

// TrajectorySchema is the tracer's schemaVersion this reads. The tracer's
// SPEC (R7) has a consumer refuse any other rather than render what it can,
// and a tracer that writes another is stopped at, never read.
const TrajectorySchema = 3

// TracerInstall is the command every tracer message names.
const TracerInstall = "go install github.com/codesweep-ai/tracer/cmd/cs-tracer@latest"

var traceIssueDefs = map[string]string{
	"tracer-absent":  "no cs-tracer was found, so the page draws dispatches without their traces",
	"tracer-failed":  "cs-tracer could not read a member's store; that member has no trace",
	"trace-missing":  "a member's archive holds no transcript, so it has no trace",
	"trace-unlinked": "a dispatch anchor was not found in any trace; the link is left out rather than guessed",
}

func init() {
	maps.Copy(issueDefs, traceIssueDefs)
}

// AttachTraces runs the tracer over every member's transcript and joins the
// result to the run. A member whose store cannot be read is a finding, and the
// page draws what it has. Three things stop it instead: a site with no tracer,
// a --tracer that cannot be run, and a tracer whose output this does not read,
// since that would put wrong data on the page.
func AttachTraces(ctx context.Context, run *Run, dir string, opts TraceOptions) error {
	root := archiveRoot(dir)
	stderr := opts.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	tracer := opts.Tracer
	if tracer == "" {
		p, err := exec.LookPath("cs-tracer")
		if err != nil {
			if opts.Site != "" {
				return fmt.Errorf("a site needs cs-tracer, and none is on PATH; install it with %s, or name one with --tracer", TracerInstall)
			}
			fmt.Fprintf(stderr, "warning: cs-tracer is not on PATH, so this page has no traces view.\n  install it with %s, or name one with --tracer\n", TracerInstall)
			run.Issues = append(run.Issues, Issue{Severity: "warning", Code: "tracer-absent",
				Message: "cs-tracer is not on PATH and --tracer was not given; dispatches are drawn without their traces. Install it with " + TracerInstall})
			return nil
		}
		tracer = p
	}
	version, err := checkTracer(ctx, tracer)
	if err != nil {
		return err
	}
	scratch, err := os.MkdirTemp("", "cs-dispatch-viewer-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)

	tr := &Traces{Tool: version}
	stores := filepath.Join(scratch, "stores")
	var stored []string
	for _, n := range run.Nodes {
		base := filepath.Join(root, "agents", n.Name)
		if n.Role == "orchestrator" {
			base = filepath.Join(root, "orchestrator")
		}
		tgz := filepath.Join(base, "transcript", "cli-evidence.tgz")
		store := filepath.Join(stores, n.Name)
		if err := untar(tgz, store); err != nil {
			run.Issues = append(run.Issues, Issue{Severity: "warning", Code: "trace-missing",
				Message: fmt.Sprintf("%s: %v", n.Name, err), Node: n.Name})
			continue
		}
		norm := filepath.Join(scratch, "norm", n.Name)
		cmd := exec.CommandContext(ctx, tracer, "normalize", store, "--out", norm)
		cmd.Stderr = stderr
		if out, err := cmd.Output(); err != nil {
			run.Issues = append(run.Issues, Issue{Severity: "warning", Code: "tracer-failed",
				Message: fmt.Sprintf("%s: cs-tracer normalize: %v %s", n.Name, err, strings.TrimSpace(string(out))), Node: n.Name})
			continue
		}
		sessions, err := readNormalized(norm, n.Name)
		if schema, ok := errors.AsType[*schemaError](err); ok {
			return fmt.Errorf("%s writes trajectories at schemaVersion %d, and this cs-dispatch-viewer reads %d; install a matching cs-tracer with %s", version, schema.got, TrajectorySchema, TracerInstall)
		}
		if err != nil {
			run.Issues = append(run.Issues, Issue{Severity: "warning", Code: "tracer-failed",
				Message: fmt.Sprintf("%s: %v", n.Name, err), Node: n.Name})
			continue
		}
		tr.Sessions = append(tr.Sessions, sessions...)
		stored = append(stored, n.Name)
	}
	if len(stored) == 0 {
		run.Traces = tr
		return nil
	}
	if opts.Site != "" {
		site := filepath.Join(opts.Site, "tracer")
		if err := os.RemoveAll(site); err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, tracer, stores, "--split", "-o", site)
		cmd.Stderr = stderr
		if out, err := cmd.Output(); err != nil {
			return fmt.Errorf("cs-tracer --split: %v %s", err, strings.TrimSpace(string(out)))
		}
		pages, err := sitePages(site)
		if err != nil {
			return err
		}
		for i := range tr.Sessions {
			tr.Sessions[i].Page = pages[tr.Sessions[i].ID]
		}
	}
	joinAnchors(run, tr)
	run.Traces = tr
	addresses(run)
	return nil
}

// checkTracer runs the tracer once before it is trusted with a store: it must
// run, and its usage must name the two commands this calls. It returns the
// tracer's version line. The schema a tracer writes is checked on its output.
func checkTracer(ctx context.Context, tracer string) (string, error) {
	out, err := exec.CommandContext(ctx, tracer, "version").Output()
	if err != nil {
		return "", fmt.Errorf("cannot run the tracer %s: %v; install cs-tracer with %s", tracer, err, TracerInstall)
	}
	version := strings.TrimSpace(string(out))
	help, _ := exec.CommandContext(ctx, tracer, "help").CombinedOutput()
	for _, want := range []string{"normalize", "--split"} {
		if !strings.Contains(string(help), want) {
			return "", fmt.Errorf("%s has no %s command, which this cs-dispatch-viewer needs; install a matching cs-tracer with %s", version, want, TracerInstall)
		}
	}
	return version, nil
}

// schemaError is a tracer document at a schemaVersion this does not read.
type schemaError struct {
	file string
	got  int
}

func (e *schemaError) Error() string {
	return fmt.Sprintf("%s: schemaVersion %d, want %d", e.file, e.got, TrajectorySchema)
}

// WriteTrajectories writes each session the tracer normalized as one file,
// dir/<session>.json: the tracer's summary document with the member's name
// and every event added, so one file answers for one session.
func WriteTrajectories(tr *Traces, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, s := range tr.Sessions {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(s.summary, &doc); err != nil {
			return fmt.Errorf("%s: %w", s.ID, err)
		}
		doc["node"], _ = json.Marshal(s.Node)
		events := s.raw
		if events == nil {
			events = []json.RawMessage{}
		}
		doc["events"], _ = json.Marshal(events)
		out, err := json.MarshalIndent(doc, "", " ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, s.ID+".json"), append(out, '\n'), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func archiveRoot(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, "campaign.json")); err != nil {
		return filepath.Join(dir, "archive")
	}
	return dir
}

// untar unpacks a gzipped tar into dir, refusing any entry that would land
// outside it. The archive was written by the harness, but a store is a
// member's own home state and gets no more trust than that.
func untar(tgz, dir string) error {
	f, err := os.Open(tgz)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("%s: %w", tgz, err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	n := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("%s: %w", tgz, err)
		}
		clean := filepath.Clean(h.Name)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%s: entry %q escapes the store", tgz, h.Name)
		}
		target := filepath.Join(dir, clean)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			w, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(w, tr); err != nil {
				w.Close()
				return err
			}
			w.Close()
			n++
		}
	}
	if n == 0 {
		return errors.New("the transcript holds no files")
	}
	return nil
}

// The subset of the tracer's summary and chunk documents this reads. Field
// names follow schema/trajectory.v1.json in the tracer repository.
type tracerIndex struct {
	SchemaVersion int `json:"schemaVersion"`
	Trajectories  []struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	} `json:"trajectories"`
}

type tracerSummary struct {
	SchemaVersion int `json:"schemaVersion"`
	Meta          struct {
		Source           string `json:"source"`
		SessionID        string `json:"sessionId"`
		ParentSessionID  string `json:"parentSessionId"`
		ParentEventIndex *int   `json:"parentEventIndex"`
		Model            string `json:"model"`
		StartedAt        string `json:"startedAt"`
		EndedAt          string `json:"endedAt"`
	} `json:"meta"`
	Totals     tracerTotals `json:"totals"`
	ChunkCount int          `json:"chunkCount"`
	Strip      []Step       `json:"strip"`
}

// tracerTotals is the part of a summary's totals the facts count: output is
// the tokens the model wrote, which is not the bytes its tools returned.
// time is the tracer's own work and idle for the session (its R77): work is
// the union of its steps' intervals, so tool calls running at once count
// once, and summing the strip's workMs would not give it.
type tracerTotals struct {
	Events    int `json:"events"`
	ToolCalls int `json:"toolCalls"`
	Output    int `json:"output"`
	Time      struct {
		ElapsedMs int64 `json:"elapsedMs"`
		IdleMs    int64 `json:"idleMs"`
		WorkMs    int64 `json:"workMs"`
	} `json:"time"`
}

type tracerChunk struct {
	Events []json.RawMessage `json:"events"`
}

type tracerEvent struct {
	I      int           `json:"i"`
	Kind   string        `json:"kind"`
	Text   string        `json:"text"`
	Tool   *tracerTool   `json:"tool"`
	Result *tracerResult `json:"result"`
}

type tracerTool struct {
	Name    string          `json:"name"`
	Command string          `json:"command"`
	Input   json.RawMessage `json:"input"`
}

type tracerResult struct {
	Text    string `json:"text"`
	IsError bool   `json:"isError"`
}

func readNormalized(norm, node string) ([]Session, error) {
	raw, err := os.ReadFile(filepath.Join(norm, "index.json"))
	if err != nil {
		return nil, err
	}
	var idx tracerIndex
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, fmt.Errorf("index.json: %w", err)
	}
	if idx.SchemaVersion != TrajectorySchema {
		return nil, &schemaError{"index.json", idx.SchemaVersion}
	}
	var out []Session
	for _, t := range idx.Trajectories {
		dir := filepath.Join(norm, t.Path)
		raw, err := os.ReadFile(filepath.Join(dir, "summary.json"))
		if err != nil {
			return nil, err
		}
		var sum tracerSummary
		if err := json.Unmarshal(raw, &sum); err != nil {
			return nil, fmt.Errorf("%s/summary.json: %w", t.Path, err)
		}
		if sum.SchemaVersion != TrajectorySchema {
			return nil, &schemaError{t.Path + "/summary.json", sum.SchemaVersion}
		}
		s := Session{ID: t.ID, Node: node, Source: sum.Meta.Source, Parent: sum.Meta.ParentSessionID,
			ParentEventIndex: sum.Meta.ParentEventIndex, StartedAt: sum.Meta.StartedAt,
			EndedAt: sum.Meta.EndedAt, Model: sum.Meta.Model, Events: sum.Totals.Events, Strip: sum.Strip,
			totals: sum.Totals, summary: raw}
		byI := map[int]*Step{}
		for i := range s.Strip {
			byI[s.Strip[i].I] = &s.Strip[i]
		}
		rawEvents, err := readChunks(dir)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", t.Path, err)
		}
		events := make([]tracerEvent, len(rawEvents))
		for i, r := range rawEvents {
			if err := json.Unmarshal(r, &events[i]); err != nil {
				return nil, fmt.Errorf("%s: event %d: %w", t.Path, i, err)
			}
		}
		for _, e := range events {
			st := byI[e.I]
			if st == nil {
				continue
			}
			st.Text = hoverText(e)
			if e.Tool != nil && isWaitCall(e) {
				st.Wait = true
			}
		}
		s.events = events
		s.raw = rawEvents
		out = append(out, s)
	}
	return out, nil
}

func readChunks(dir string) ([]json.RawMessage, error) {
	names, err := filepath.Glob(filepath.Join(dir, "chunks", "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var all []json.RawMessage
	for _, name := range names {
		raw, err := os.ReadFile(name)
		if err != nil {
			return nil, err
		}
		var c tracerChunk
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(name), err)
		}
		all = append(all, c.Events...)
	}
	return all, nil
}

const hoverLimit = 96

func hoverText(e tracerEvent) string {
	text := e.Text
	if e.Tool != nil {
		text = e.Tool.Command
		if text == "" {
			text = e.Tool.Name + " " + string(e.Tool.Input)
		}
	}
	text = strings.TrimSpace(text)
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	if len(text) > hoverLimit {
		text = text[:hoverLimit] + "…"
	}
	return text
}

// isWaitCall reads the harness's own verb out of the call: cs-campaign-member
// wait is the orchestrator blocking on its members. The verb is the word
// after the first mention of the binary, so a send whose message talks about
// waiting is not one. A recorded call id from the harness would make this
// exact; until then the command text is what the archive has.
func isWaitCall(e tracerEvent) bool {
	body := e.Tool.Command
	if body == "" {
		body = string(e.Tool.Input)
	}
	_, rest, ok := strings.Cut(body, "cs-campaign-member")
	if !ok {
		return false
	}
	fields := strings.Fields(rest)
	return len(fields) > 0 && fields[0] == "wait"
}

// sitePages maps each session id to its page path relative to the site root,
// read from the manifest the tracer writes beside its pages.
func sitePages(site string) (map[string]string, error) {
	raw, err := os.ReadFile(filepath.Join(site, ".cs-tracer.json"))
	if err != nil {
		return nil, err
	}
	var m struct {
		Files []string `json:"files"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf(".cs-tracer.json: %w", err)
	}
	pages := map[string]string{}
	for _, f := range m.Files {
		if strings.HasPrefix(f, "traces/") && strings.HasSuffix(f, ".html") {
			id := strings.TrimSuffix(strings.TrimPrefix(f, "traces/"), ".html")
			pages[id] = filepath.ToSlash(filepath.Join(filepath.Base(site), f))
		}
	}
	return pages, nil
}

// joinAnchors finds the four anchors of every dispatch in the text the
// harness printed: the member's user turn carries "Dispatch ID: dNNN." by
// construction; the other three are the receipts of send, reply and accept,
// which survive only when the agent let the tool's output through.
func joinAnchors(run *Run, tr *Traces) {
	type key struct{ node, dispatch, kind string }
	found := map[key]bool{}
	add := func(node, dispatch, kind, session string, i int) {
		k := key{node, dispatch, kind}
		if found[k] {
			return
		}
		found[k] = true
		tr.Anchors = append(tr.Anchors, Anchor{Node: node, Dispatch: dispatch, Kind: kind, Session: session, I: i})
	}
	orch := orchestratorName(run)
	for _, s := range tr.Sessions {
		for _, e := range s.events {
			switch {
			case e.Kind == "user":
				if id, ok := after(e.Text, "Dispatch ID: ", "."); ok {
					add(s.Node, id, "arrived", s.ID, e.I)
				}
			case e.Tool != nil && e.Result != nil:
				out := e.Result.Text
				if id, ok := after(out, "replied to ", " "); ok {
					add(s.Node, id, "replied", s.ID, e.I)
				}
				if s.Node == orch {
					for _, sp := range run.Spans {
						if strings.Contains(out, sp.Node+"/"+sp.ID+" opened") {
							add(sp.Node, sp.ID, "sent", s.ID, e.I)
						}
						if strings.Contains(out, "accepted "+sp.ID+" from "+sp.Node) {
							add(sp.Node, sp.ID, "accepted", s.ID, e.I)
						}
					}
				}
			}
		}
	}
	// Anchors are owed only where the archive says the step happened: a
	// dispatch that was opened has a sent and an arrived; one that was replied
	// to has a replied; one that was accepted has an accepted. The host writes
	// the orchestrator's own channel and every readback (createPhase), so
	// neither has a send call to find.
	readback := createPhase(run)
	for _, sp := range run.Spans {
		if sp.Node == orch {
			continue
		}
		want := []string{"arrived"}
		if sp.OpenedAt != "" && !readback(sp.Node, sp.ID, sp.OpenedAt) {
			want = append(want, "sent")
		}
		if sp.RepliedAt != "" {
			want = append(want, "replied")
		}
		if sp.AcceptedAt != "" {
			want = append(want, "accepted")
		}
		for _, kind := range want {
			if !found[key{sp.Node, sp.ID, kind}] {
				run.Issues = append(run.Issues, Issue{Severity: "info", Code: "trace-unlinked",
					Message: fmt.Sprintf("%s/%s: no %s anchor was found in the traces", sp.Node, sp.ID, kind),
					Node:    sp.Node, Dispatch: sp.ID})
			}
		}
	}
	sort.SliceStable(tr.Anchors, func(i, j int) bool {
		a, b := tr.Anchors[i], tr.Anchors[j]
		if a.Node != b.Node {
			return a.Node < b.Node
		}
		if a.Dispatch != b.Dispatch {
			return a.Dispatch < b.Dispatch
		}
		return a.Kind < b.Kind
	})
	sort.SliceStable(run.Issues, func(i, j int) bool { return sevRank(run.Issues[i].Severity) < sevRank(run.Issues[j].Severity) })
}

// after returns the dispatch id that follows prefix and precedes stop, when
// it looks like one: dNNN, or the mission's own id.
func after(text, prefix, stop string) (string, bool) {
	_, rest, ok := strings.Cut(text, prefix)
	if !ok {
		return "", false
	}
	id, _, ok := strings.Cut(rest, stop)
	if !ok {
		return "", false
	}
	if id == protocol.MissionID {
		return id, true
	}
	if len(id) != 4 || id[0] != 'd' {
		return "", false
	}
	for _, c := range id[1:] {
		if c < '0' || c > '9' {
			return "", false
		}
	}
	return id, true
}
