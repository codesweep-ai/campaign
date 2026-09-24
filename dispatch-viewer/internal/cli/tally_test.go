package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A tally step: its kind, its time on the run's day, and the minutes of work
// before it and idle after it. A tool call carries its command and result.
type tallyStep struct {
	kind, at      string
	work, idle    float64
	err           bool
	label, text   string
	tool, command string
	result        string
}

func call(at string, work float64, tool, command, result string) tallyStep {
	return tallyStep{kind: "tool_call", at: at, work: work, tool: tool, command: command, result: result}
}

// The invented run's three sessions. Each holds something a question about a
// run turns on: the orchestrator's turn ends on a provider error and its
// session is resumed 52 minutes later; tester hits a rate limit and is
// resumed; dev has a
// guard refusal and a failed command; the orchestrator's wait repeats five
// times; tester and dev sit idle for over an hour.
var tallySessions = []struct {
	node, id, source string
	steps            []tallyStep
}{
	{"orchestrator", "orch-4c1e", "claude-code", []tallyStep{
		{kind: "user", at: "09:05", text: "Dispatch ID: m1. Read mission.md and run the campaign."},
		{kind: "assistant", at: "09:05:40", work: 0.6, text: "Reading the mission."},
		call("09:07:55", 2.2, "Bash", "cs-campaign-member send dev d002 --file briefs/dev-d002.md", "dev/d002 opened"),
		call("09:08:55", 1, "Bash", "cs-campaign-member send tester d002 --file briefs/tester-d002.md", "tester/d002 opened"),
		call("09:20", 11, "Bash", "cs-campaign-member wait", "no reply yet"),
		call("09:31:10", 11, "Bash", "cs-campaign-member wait", "tester/d002 replied"),
		call("09:33", 1.8, "Bash", "cs-campaign-member accept tester d002", "accepted d002 from tester"),
		call("09:40:10", 7, "Bash", "cs-campaign-member wait", "dev/d002 replied"),
		call("09:42", 1.8, "Bash", "cs-campaign-member accept dev d002", "accepted d002 from dev"),
		{kind: "assistant", at: "09:52", work: 10, err: true, label: "server_error", text: "API Error: 529 overloaded_error"},
		{kind: "turn_end", at: "09:52", idle: 52, text: "turn ended"},
		{kind: "user", at: "10:44", text: "resume"},
		{kind: "assistant", at: "10:45", work: 1, text: "Next: Unicode text."},
		call("10:46:50", 1.8, "Bash", "cs-campaign-member send dev d003 --file briefs/dev-d003.md", "dev/d003 opened"),
		call("10:47:50", 1, "Bash", "cs-campaign-member send tester d003 --file briefs/tester-d003.md", "tester/d003 opened"),
		call("11:00:10", 12.3, "Bash", "cs-campaign-member wait", "tester/d003 replied"),
		call("11:02", 1.8, "Bash", "cs-campaign-member accept tester d003", "accepted d003 from tester"),
		call("11:11:10", 9.2, "Bash", "cs-campaign-member wait", "dev/d003 replied"),
		call("11:13", 1.8, "Bash", "cs-campaign-member accept dev d003", "accepted d003 from dev"),
		{kind: "turn_end", at: "11:16", text: "turn ended"},
	}},
	{"dev", "ses_dev0001", "opencode", []tallyStep{
		{kind: "user", at: "09:01", text: "Dispatch ID: d001. Restate your brief."},
		call("09:03:50", 2.8, "bash", "cs-campaign-member reply d001 --file readback.md", "replied to d001 "),
		{kind: "turn_end", at: "09:04", idle: 4, text: "turn ended"},
		{kind: "user", at: "09:08", text: "Dispatch ID: d002. Implement tally."},
		call("09:18", 10, "bash", "go test ./...", "--- FAIL: TestCountWords\nFAIL\nexit status 1"),
		call("09:30", 12, "bash", "git push --force origin main", "PermissionDenied: the bash command is denied by this member's permission rules"),
		call("09:36", 6, "bash", "go test ./...", "ok  \ttally/internal/count"),
		call("09:39:50", 3.8, "bash", "cs-campaign-member reply d002 --file reply.md", "replied to d002 "),
		{kind: "turn_end", at: "09:40", idle: 67, text: "turn ended"},
		{kind: "user", at: "10:47", text: "Dispatch ID: d003. Count words in Unicode text."},
		call("11:05", 18, "bash", "go test ./...", "ok  \ttally/internal/count"),
		call("11:10:50", 5.8, "bash", "cs-campaign-member reply d003 --file reply.md", "replied to d003 "),
		{kind: "turn_end", at: "11:11", text: "turn ended"},
	}},
	{"tester", "rollout-tester-0001", "codex", []tallyStep{
		{kind: "user", at: "09:01", text: "Dispatch ID: d001. Restate your brief."},
		call("09:03:50", 2.8, "exec", "cs-campaign-member reply d001 --file readback.md", "replied to d001 "),
		{kind: "turn_end", at: "09:04", idle: 5, text: "turn ended"},
		{kind: "user", at: "09:09", text: "Dispatch ID: d002. Write acceptance tests."},
		call("09:14", 5, "exec", "go vet ./acceptance/...", ""),
		{kind: "turn_end", at: "09:15", work: 1, idle: 7, err: true, label: "rate_limit_exceeded", text: "429 Rate limit reached"},
		{kind: "user", at: "09:22", text: "resume"},
		call("09:30:50", 8.8, "exec", "cs-campaign-member reply d002 --file reply.md", "replied to d002 "),
		{kind: "turn_end", at: "09:31", idle: 77, text: "turn ended"},
		{kind: "user", at: "10:48", text: "Dispatch ID: d003. Add Unicode tests."},
		call("10:59:50", 11.8, "exec", "cs-campaign-member reply d003 --file reply.md", "replied to d003 "),
		{kind: "turn_end", at: "11:00", text: "turn ended"},
	}},
}

// tallyArchive writes the invented run, and a fake cs-tracer that hands back
// each member's trajectory in the tracer's normalized shape and writes one
// page per session for a site, and puts the tracer first on PATH.
func tallyArchive(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	day := func(hm string) time.Time {
		if len(hm) == 5 {
			hm += ":00"
		}
		ts, err := time.Parse(time.RFC3339, "2026-08-24T"+hm+"Z")
		if err != nil {
			t.Fatal(err)
		}
		return ts
	}
	iso := func(hm string) string { return day(hm).Format(time.RFC3339) }
	write := func(rel, body, hm string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if hm != "" {
			if err := os.Chtimes(p, day(hm), day(hm)); err != nil {
				t.Fatal(err)
			}
		}
	}
	reply := func(d, hm string) string {
		return fmt.Sprintf(`{"dispatch":%q,"phase":"done","note":"done","at":%q}`, d, iso(hm))
	}

	a := "archive/"
	write(a+"campaign.json", `{"name":"tally","id":"tally-7f3a","policy":{"continueAttempts":2,"restarts":1,"stallSeconds":360,"providerWaitSeconds":3600},
	  "members":[{"name":"orchestrator","role":"orchestrator","cli":"claude"},{"name":"dev","role":"agent","cli":"opencode"},{"name":"tester","role":"agent","cli":"codex"}]}`, "09:00")
	o, d, q := a+"orchestrator/", a+"agents/dev/", a+"agents/tester/"
	for m, r := range map[string]string{o: "09:03", d: "09:04", q: "09:04"} {
		write(m+"input/d001.md", "# readback", "09:01")
		write(m+"output/replies/d001.json", reply("d001", r), r)
	}
	write(o+"input/m1.md", "# mission", "09:05")
	write(d+"input/d002.md", "# d002", "09:08")
	write(q+"input/d002.md", "# d002", "09:09")
	write(q+"input/d002.001.resume.md", "resume", "09:22")
	write(q+"output/replies/d002.json", reply("d002", "09:31"), "09:31")
	write(d+"output/replies/d002.json", reply("d002", "09:40"), "09:40")
	write(o+"input/m1.001.resume.md", "resume", "10:44")
	write(d+"input/d003.md", "# d003", "10:47")
	write(q+"input/d003.md", "# d003", "10:48")
	write(q+"output/replies/d003.json", reply("d003", "11:00"), "11:00")
	write(d+"output/replies/d003.json", reply("d003", "11:11"), "11:11")
	var log strings.Builder
	for _, e := range [][2]string{{"09:33", "tester/d002"}, {"09:42", "dev/d002"}, {"11:02", "tester/d003"}, {"11:13", "dev/d003"}} {
		fmt.Fprintf(&log, `{"at":%q,"kind":"accepted","text":%q}`+"\n", iso(e[0]), e[1])
	}
	write(o+"output/log.jsonl", log.String(), "11:16")
	write(o+"output/replies/m1.json", `{"dispatch":"m1","phase":"done","outcome":"campaign-met","note":"met","at":"`+iso("11:16")+`"}`, "11:16")

	var files []string
	for _, s := range tallySessions {
		var strip, events []map[string]any
		var work, idle float64
		var pageRows strings.Builder
		for i, st := range s.steps {
			step := map[string]any{"i": i, "kind": st.kind, "ts": iso(st.at)}
			if st.work > 0 {
				step["workMs"] = int64(st.work * 60000)
				work += st.work * 60000
			}
			if st.idle > 0 {
				step["idleMs"] = int64(st.idle * 60000)
				idle += st.idle * 60000
			}
			if st.kind == "turn_end" {
				step["turnEnd"] = true
			}
			if st.err {
				step["error"] = true
			}
			if st.label != "" {
				step["label"] = st.label
			}
			ev := map[string]any{"i": i, "kind": st.kind, "ts": iso(st.at), "text": st.text}
			if st.tool != "" {
				step["label"] = st.tool
				step["error"] = strings.Contains(st.result, "FAIL") || strings.HasPrefix(st.result, "PermissionDenied")
				ev = map[string]any{"i": i, "kind": st.kind, "ts": iso(st.at),
					"tool": map[string]string{"name": st.tool, "command": st.command}, "result": map[string]string{"text": st.result}}
			}
			strip = append(strip, step)
			events = append(events, ev)
			fmt.Fprintf(&pageRows, "<li id=\"ev-%d\">%s</li>\n", i, html.EscapeString(st.kind+" "+st.command+st.text))
		}
		sum := map[string]any{"schemaVersion": 3, "meta": map[string]string{"source": s.source, "sessionId": s.id},
			"totals":     map[string]any{"events": len(strip), "time": map[string]int64{"workMs": int64(work), "idleMs": int64(idle)}},
			"chunkCount": 1, "strip": strip}
		norm := "norm/" + s.node + "/"
		for rel, doc := range map[string]any{
			norm + "index.json":              map[string]any{"schemaVersion": 3, "trajectories": []map[string]string{{"id": s.id, "path": s.id}}},
			norm + s.id + "/summary.json":    sum,
			norm + s.id + "/chunks/000.json": map[string]any{"events": events},
		} {
			b, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			write(rel, string(b), "")
		}
		write("split/traces/"+s.id+".html", "<ol>\n"+pageRows.String()+"</ol>\n", "")
		files = append(files, "traces/"+s.id+".html")

		var tgz bytes.Buffer
		writeStore(t, &tgz)
		base := a + "agents/" + s.node
		if s.node == "orchestrator" {
			base = a + "orchestrator"
		}
		write(base+"/transcript/cli-evidence.tgz", tgz.String(), "11:16")
	}
	index, _ := json.Marshal(map[string][]string{"files": files})
	write("split/.cs-tracer.json", string(index), "")

	write("bin/cs-tracer", `#!/bin/sh
here='`+root+`'
case "$1" in
version) echo fake-tracer 1 ;;
help) echo "normalize --split" ;;
normalize) mkdir -p "$4" && cp -R "$here/norm/$(basename "$2")/." "$4/" ;;
*) mkdir -p "$4" && cp -R "$here/split/." "$4/" ;;
esac
`, "")
	if err := os.Chmod(filepath.Join(root, "bin", "cs-tracer"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	return root
}

// writeStore writes the smallest store the viewer will unpack. The fake
// tracer never reads it.
func writeStore(t *testing.T, w *bytes.Buffer) {
	t.Helper()
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "s.jsonl", Mode: 0o600, Size: 3, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte("{}\n"))
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}

func tallySite(t *testing.T) (site string, facts map[string]any) {
	t.Helper()
	root := tallyArchive(t)
	site = filepath.Join(root, "site")
	var stdout, stderr bytes.Buffer
	if code := Main([]string{root, "--site", site}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	raw, err := os.ReadFile(filepath.Join(site, "facts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &facts); err != nil {
		t.Fatal(err)
	}
	return site, facts
}

// The questions an agent is asked about a run turn on these facts, so a run
// that holds each of them has to show each of them, classed and linked.
func TestFactsShowWhatQuestionsAboutARunTurnOn(t *testing.T) {
	_, f := tallySite(t)
	get := func(v any, path ...any) any {
		for _, p := range path {
			switch k := p.(type) {
			case string:
				v = v.(map[string]any)[k]
			case int:
				v = v.([]any)[k]
			}
		}
		return v
	}
	classes := map[string]string{}
	for _, s := range get(f, "failedSteps").([]any) {
		classes[get(s, "class").(string)] = get(s, "node").(string)
	}
	if want := map[string]string{"provider": "orchestrator", "account": "tester", "guard": "dev", "command": "dev"}; fmt.Sprint(classes) != fmt.Sprint(want) {
		t.Errorf("failed steps by class = %v, want %v", classes, want)
	}
	var recovery []string
	for _, e := range get(f, "recovery", "events").([]any) {
		recovery = append(recovery, fmt.Sprintf("%s %s after %s, ended by %s", get(e, "node"), get(e, "type"),
			get(e, "sinceLastStep", "hms"), get(e, "turnError", "label")))
	}
	if got, want := strings.Join(recovery, "; "), "tester resume after 0:07:00, ended by rate_limit_exceeded; orchestrator resume after 0:52:00, ended by server_error"; got != want {
		t.Errorf("recovery = %s\nwant %s", got, want)
	}
	// A resume spends no rung, so it is not a continue.
	for _, dt := range get(f, "dispatches").([]any) {
		if get(dt, "node") == "tester" && get(dt, "dispatch") == "d002" && (get(dt, "continues") != 0.0 || get(dt, "resumes") != 1.0) {
			t.Errorf("tester/d002 = %v, want 0 continues and 1 resume", dt)
		}
	}
	if rc := get(f, "repeatedCalls", 0); get(rc, "node") != "orchestrator" || get(rc, "count") != 5.0 || get(rc, "polling") != true {
		t.Errorf("first repeated call = %v, want the orchestrator's wait, five times, polling", rc)
	}
	if g := get(f, "idleGaps", 0); get(g, "after", "node") != "tester" || get(g, "idle", "hms") != "1:17:00" {
		t.Errorf("longest idle gap = %v, want tester's 1:17:00", g)
	}
}

// Every step the facts name can be cited: it has an address on the page, or,
// when the page does not draw it, a place on its trace page. Every address
// the facts carry is one the page's data carries, and every trace page link
// opens a page in the site on an event that page holds.
func TestEveryStepTheFactsNameHasALinkThatResolves(t *testing.T) {
	site, f := tallySite(t)
	var run map[string]any
	raw, _ := os.ReadFile(filepath.Join(site, "run-data.json"))
	if err := json.Unmarshal(raw, &run); err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	var collect func(v any)
	collect = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for _, k := range []string{"addr", "idleAddr"} {
				if s, ok := x[k].(string); ok && s != "" {
					known[s] = true
				}
			}
			for _, c := range x {
				collect(c)
			}
		case []any:
			for _, c := range x {
				collect(c)
			}
		}
	}
	collect(run)

	steps := 0
	var check func(path string, v any)
	check = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			_, isStep := x["kind"]
			if _, hasAt := x["at"]; isStep && hasAt {
				steps++
				if x["addr"] == "" && x["page"] == nil {
					t.Errorf("%s names a step with neither addr nor page: %v", path, x)
				}
			}
			for k, c := range x {
				switch s, _ := c.(string); {
				case s != "" && (k == "addr" || k == "idleAddr" || k == "fromAddr" || k == "toAddr") && !known[s]:
					t.Errorf("%s.%s = %s, which the page's data does not carry", path, k, s)
				case s != "" && k == "page":
					file, anchor, _ := strings.Cut(s, "#")
					body, err := os.ReadFile(filepath.Join(site, file))
					if err != nil || anchor != "" && !strings.Contains(string(body), `id="`+anchor+`"`) {
						t.Errorf("%s.page = %s, which opens no event in the site", path, s)
					}
				}
				check(path+"."+k, c)
			}
		case []any:
			for i, c := range x {
				if s, ok := c.(string); ok && strings.HasSuffix(path, "addrs") && !known[s] {
					t.Errorf("%s[%d] = %s, which the page's data does not carry", path, i, s)
				}
				check(fmt.Sprintf("%s[%d]", path, i), c)
			}
		}
	}
	check("facts", f)
	if steps == 0 {
		t.Fatal("the facts named no step, so nothing was checked")
	}
}
