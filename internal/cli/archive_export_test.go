package cli

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/model"
)

// execHereSandbox is a fake cs-sandbox that runs the collection command on
// this machine, the shape memberOutput calls it with: `exec <ref> sh -lc
// <command>`. Directories named in extraPath go in front of PATH, which is how
// a test puts a stub python3 where the guest command finds it.
func execHereSandbox(t *testing.T, extraPath ...string) sandboxCLI {
	t.Helper()
	tool := filepath.Join(t.TempDir(), "fake-sandbox")
	script := "#!/bin/sh\n[ \"$1\" = exec ] || exit 1\n"
	if len(extraPath) > 0 {
		script += "PATH=" + strings.Join(extraPath, ":") + ":$PATH\n"
	}
	script += "exec sh -c \"$5\"\n"
	if err := os.WriteFile(tool, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return sandboxCLI{Bin: tool}
}

// An opencode store dump that never returns must not cost the run its
// transcript tarball.
//
// The dump and the tar share one collection bound and the dump runs first, so
// an unbounded one leaves no cli-evidence.tgz at all — and verifyFleetArchive
// reads a missing or empty tarball as "declared opencode but produced no
// transcript; another CLI did the work". That is the audit's most serious
// finding, and a member that was only slow must not be able to earn it.
//
// The stub python3 hangs, which is the shape a saturated guest produces.
// Running the real command through a real sh also checks the quoting: the
// dump is a heredoc inside `timeout ...` and a mistake there would break
// every opencode archive rather than only a slow one.
func TestOpencodeExportCannotCostTheTarball(t *testing.T) {
	// The collection runs in a Linux guest. Where there is no GNU timeout,
	// as on macOS, the bounded dump never starts, so there is nothing to test.
	if _, err := exec.LookPath("timeout"); err != nil {
		t.Skip("timeout is not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	// The evidence that must survive: the raw db the audit reads.
	writeOpenCodeStore(t, home, []string{"ses_one"}, nil)

	// A python3 whose dump never returns. It records that it was asked,
	// because a command that never reached the dump would pass this test
	// without bounding anything.
	stub := t.TempDir()
	asked := filepath.Join(t.TempDir(), "asked")
	if err := os.WriteFile(filepath.Join(stub, "python3"),
		[]byte("#!/bin/sh\necho \"$*\" >> "+asked+"\nsleep 600\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := &app{sandbox: execHereSandbox(t, stub), exportBound: 300 * time.Millisecond}

	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "transcript"), 0o700); err != nil {
		t.Fatal(err)
	}
	member := model.Member{Name: "dev", Role: "agent", CLI: "opencode", Sandbox: "box"}

	start := time.Now()
	if err := a.archiveTranscripts(t.Context(), base, member); err != nil {
		t.Fatalf("archiveTranscripts: %v", err)
	}
	elapsed := time.Since(start)

	marker := filepath.Join(base, "transcript", "INCOMPLETE.txt")
	if body, err := os.ReadFile(marker); err == nil {
		t.Fatalf("a hung dump left the transcript INCOMPLETE rather than collecting the db: %s", body)
	}
	tgz := filepath.Join(base, "transcript", "cli-evidence.tgz")
	info, err := os.Stat(tgz)
	if err != nil {
		t.Fatalf("no cli-evidence.tgz: %v — audit reads that as the wrong CLI doing the work", err)
	}
	if info.Size() == 0 {
		t.Fatal("empty cli-evidence.tgz — audit reads that as the wrong CLI doing the work")
	}
	entries, err := tgzEntryCount(tgz)
	if err != nil || entries == 0 {
		t.Fatalf("tarball carries nothing (%d entries): %v", entries, err)
	}
	// The dump is bounded well inside the collection bound; without the
	// bound this would have run for the stub's full 600s.
	if elapsed >= archiveCollectBound {
		t.Fatalf("collection took %s, past its own %s bound", elapsed, archiveCollectBound)
	}
	got, _ := os.ReadFile(asked)
	if !strings.Contains(string(got), "file:"+home+"/"+opencodeDB+"?mode=ro") {
		t.Fatalf("the command did not ask python3 to dump the store read-only: %q", got)
	}
}

// The session files are the store's own rows, on either generation of the
// store, and every session is written, sub-agents' included.
//
// OpenCode 2 keeps a message's parts inside its `data.content`; OpenCode 1
// keeps them as rows of `part`. The file carries both as they are, parsed,
// and names the generation so cs-tracer can tell which it is reading. The
// CLI's own `session export` is not used: on OpenCode 2 it carries text alone.
func TestOpencodeExportWritesEverySessionFromTheStore(t *testing.T) {
	if _, err := exec.LookPath("timeout"); err != nil {
		t.Skip("timeout is not installed")
	}
	t.Run("opencode 2", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		writeOpenCodeStore(t, home, []string{"ses_root", "ses_child"}, [][2]string{
			{"user", `{"text":"read the briefing"}`},
			{"assistant", `{"agent":"build","model":{"id":"kimi-k3","providerID":"fireworks-ai"},"content":[{"type":"tool","name":"read","state":{"status":"completed","input":{"path":"x"}}}],"cost":0.5}`},
		})
		files := opencodeSessionFiles(t)
		root := files["ses_root"]
		if got := root["store"].(map[string]any); got["generation"] != 2.0 || got["version"] != "2.2.5" {
			t.Fatalf("store = %v, want generation 2 of 2.2.5", got)
		}
		if got := root["session"].(map[string]any)["id"]; got != "ses_root" {
			t.Fatalf("session.id = %v", got)
		}
		messages := root["messages"].([]any)
		if len(messages) != 2 {
			t.Fatalf("messages = %d, want the 2 rows", len(messages))
		}
		reply := messages[1].(map[string]any)
		if reply["type"] != "assistant" || reply["seq"] != 1.0 {
			t.Fatalf("second message = %v, want the assistant row in seq order", reply)
		}
		data, ok := reply["data"].(map[string]any)
		if !ok {
			t.Fatalf("data was not parsed: %T", reply["data"])
		}
		tool := data["content"].([]any)[0].(map[string]any)
		if tool["name"] != "read" {
			t.Fatalf("the tool call did not survive: %v", data)
		}
		if parts := reply["parts"].([]any); len(parts) != 0 {
			t.Fatalf("an OpenCode 2 message has no part rows, got %v", parts)
		}
		child := files["ses_child"]
		if got := child["session"].(map[string]any)["parent_id"]; got != "ses_root" {
			t.Fatalf("child's parent_id = %v, want ses_root", got)
		}
	})
	t.Run("opencode 1", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		writeOpenCodeStoreV1(t, home)
		files := opencodeSessionFiles(t)
		doc := files["ses_old"]
		if got := doc["store"].(map[string]any); got["generation"] != 1.0 || got["version"] != "1.18.32" {
			t.Fatalf("store = %v, want generation 1 of 1.18.32", got)
		}
		messages := doc["messages"].([]any)
		if len(messages) != 2 {
			t.Fatalf("messages = %d, want 2", len(messages))
		}
		reply := messages[1].(map[string]any)
		if got := reply["data"].(map[string]any)["role"]; got != "assistant" {
			t.Fatalf("messages are not in time order: second is %v", got)
		}
		parts := reply["parts"].([]any)
		if len(parts) != 2 {
			t.Fatalf("parts = %d, want the message's 2 part rows", len(parts))
		}
		if got := parts[1].(map[string]any)["data"].(map[string]any)["tool"]; got != "read" {
			t.Fatalf("the tool part did not survive in order: %v", parts)
		}
	})
}

// writeOpenCodeStoreV1 writes an OpenCode 1 store: one session, a user
// message and an assistant message with a text part and a tool part.
func writeOpenCodeStoreV1(t *testing.T, home string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ".cs-opencode"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", "-c", `
import sqlite3, sys
db = sqlite3.connect(sys.argv[1])
db.execute("CREATE TABLE session (id TEXT PRIMARY KEY, parent_id TEXT, version TEXT, time_created INTEGER)")
db.execute("CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, time_updated INTEGER, data TEXT)")
db.execute("CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT, time_created INTEGER, time_updated INTEGER, data TEXT)")
db.execute("INSERT INTO session VALUES ('ses_old', NULL, '1.18.32', 1)")
db.execute("INSERT INTO message VALUES ('msg_b', 'ses_old', 20, 20, '{\"role\":\"assistant\",\"modelID\":\"kimi-k3\",\"providerID\":\"fireworks-ai\"}')")
db.execute("INSERT INTO message VALUES ('msg_a', 'ses_old', 10, 10, '{\"role\":\"user\"}')")
db.execute("INSERT INTO part VALUES ('prt_2', 'msg_b', 'ses_old', 22, 22, '{\"type\":\"tool\",\"tool\":\"read\",\"state\":{\"status\":\"completed\"}}')")
db.execute("INSERT INTO part VALUES ('prt_1', 'msg_b', 'ses_old', 21, 21, '{\"type\":\"text\",\"text\":\"hello\"}')")
db.execute("INSERT INTO part VALUES ('prt_0', 'msg_a', 'ses_old', 11, 11, '{\"type\":\"text\",\"text\":\"read it\"}')")
db.commit()
`, filepath.Join(home, opencodeDB))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("write the opencode 1 store: %v\n%s", err, out)
	}
}

// opencodeSessionFiles archives an opencode member whose store is under the
// test's HOME and returns the session files the evidence tarball carries, by
// session id.
func opencodeSessionFiles(t *testing.T) map[string]map[string]any {
	t.Helper()
	a := &app{sandbox: execHereSandbox(t)}
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "transcript"), 0o700); err != nil {
		t.Fatal(err)
	}
	member := model.Member{Name: "dev", Role: "agent", CLI: "opencode", Sandbox: "box"}
	if err := a.archiveTranscripts(t.Context(), base, member); err != nil {
		t.Fatalf("archiveTranscripts: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(base, "transcript", "INCOMPLETE.txt")); err == nil {
		t.Fatalf("transcript INCOMPLETE: %s", body)
	}
	f, err := os.Open(filepath.Join(base, "transcript", "cli-evidence.tgz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]map[string]any{}
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(header.Name)
		if header.Typeflag != tar.TypeReg || !strings.HasPrefix(header.Name, ".cs-opencode/export/") {
			continue
		}
		if strings.HasSuffix(name, ".tmp") {
			t.Fatalf("a half-written file reached the archive: %s", header.Name)
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatalf("%s is not JSON: %v", header.Name, err)
		}
		files[strings.TrimSuffix(name, ".json")] = doc
	}
	if len(files) == 0 {
		t.Fatal("the tarball carries no session file")
	}
	return files
}
