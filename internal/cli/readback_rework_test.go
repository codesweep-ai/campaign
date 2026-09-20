package cli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/store"
)

const goodReadback = `{"member":"dev","role":"agent","branch":"","missing":[],"goal":"build the thing","scope":"my branch only","obligations":"commit, and reply to every dispatch"}`

// readbackMemberApp fakes one member that answers each readback dispatch in
// turn with the next note in notes. It keeps the member's channels as files,
// so the probe, the delivery and the reply read are all served from one state.
func readbackMemberApp(t *testing.T, notes ...string) (*app, string) {
	t.Helper()
	dir := t.TempDir()
	state := filepath.Join(dir, "member")
	for _, d := range []string{"input", "replies", "notes"} {
		if err := os.MkdirAll(filepath.Join(state, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for i, n := range notes {
		if err := os.WriteFile(filepath.Join(state, "notes", strconv.Itoa(i+1)), []byte(n), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tool := filepath.Join(dir, "fake-sandbox")
	body := `#!/bin/sh
S="` + state + `"
cmd=""; for a in "$@"; do cmd="$a"; done
case "$cmd" in
  *DRIVERS*)
    # stat -c is GNU and stat -f is BSD: a macOS runner has only the second.
    for f in "$S"/input/*.md; do [ -e "$f" ] && echo "MSG $(stat -c %Y "$f" 2>/dev/null || stat -f %m "$f") $(basename "$f")"; done
    for f in "$S"/replies/*.json; do [ -e "$f" ] && echo "REPLY $(basename "$f" .json)"; done
    echo "DRIVERS 0"; echo "AGENT idle" ;;
  *"base64 -d"*)
    t=${cmd#*base64 -d > }; t=${t%% *}; name=${t##*/}
    base64 -d > "$S/input/$name"
    id=${name%%.*}; n=$(ls "$S"/replies | wc -l); n=$((n+1))
    note=$(cat "$S/notes/$n" 2>/dev/null)
    python3 - "$id" "$note" > "$S/replies/$id.json" <<'PY'
import json, sys
print(json.dumps({"dispatch": sys.argv[1], "phase": "done", "note": sys.argv[2]}))
PY
    ;;
  *replies/*.json*) id=$(printf '%s' "$cmd" | sed -n 's|.*replies/\(d[0-9]*\)\.json.*|\1|p'); cat "$S/replies/$id.json" ;;
esac
exit 0
`
	if err := os.WriteFile(tool, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	// The turn launcher: the fake member answers on delivery, so this only has to exist.
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "cs-codex-remote"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", dir)
	prev := readbackBound
	readbackBound = 20 * time.Second // a fake that never answers fails the test and does not hang it
	t.Cleanup(func() { readbackBound = prev })
	return &app{store: store.Store{Dir: filepath.Join(dir, "state")}, sandbox: sandboxCLI{Bin: tool}}, state
}

func readbackCampaign() (*model.Campaign, model.Member) {
	m := model.Member{Name: "dev", Role: "agent", CLI: "codex", Sandbox: "box", Ref: "dev.g", SeededInputs: map[string]string{"roles/dev.md": "brief"}}
	m.Session.Name = "probe-dev"
	c := &model.Campaign{Name: "probe", Members: []model.Member{m}}
	c.Policy.PollSeconds, c.Policy.SettlingSeconds = 1, 1
	return c, m
}

// A model that leaves one brace out of its readback has not misread its
// briefing. It gets the parser's words and one more try, as any worker whose
// reply was not good enough does, and a team that is up and lent its keys is
// not thrown away over a character.
func TestAMalformedReadbackIsAskedForOnceMore(t *testing.T) {
	truncated := strings.TrimSuffix(goodReadback, "}")
	a, state := readbackMemberApp(t, truncated, goodReadback)
	campaign, member := readbackCampaign()

	var out strings.Builder
	detail, report := a.readbackOne(context.Background(), &out, campaign, member)
	if detail != "" {
		t.Fatalf("a readback that was right the second time still failed: %s\n%s", detail, out.String())
	}
	if report.Goal != "build the thing" {
		t.Fatalf("the accepted readback is not the second one: %+v", report)
	}
	rework, err := os.ReadFile(filepath.Join(state, "input", "d002.md"))
	if err != nil {
		t.Fatalf("no rework dispatch was sent: %v", err)
	}
	for _, want := range []string{"not valid JSON", `"member":"dev"`, "Do not perform any of the work"} {
		if !strings.Contains(string(rework), want) {
			t.Errorf("the rework message must carry %q:\n%s", want, rework)
		}
	}
	if !strings.Contains(out.String(), "asked once more") {
		t.Errorf("the operator must be told a readback was asked for again:\n%s", out.String())
	}
}

// The check stays exactly as strict: a second bad readback fails the member,
// and nothing is asked a third time.
func TestASecondMalformedReadbackStillFails(t *testing.T) {
	truncated := strings.TrimSuffix(goodReadback, "}")
	a, state := readbackMemberApp(t, truncated, truncated, goodReadback)
	campaign, member := readbackCampaign()

	detail, _ := a.readbackOne(context.Background(), &strings.Builder{}, campaign, member)
	if !strings.Contains(detail, "not valid JSON") || !strings.Contains(detail, "twice") {
		t.Fatalf("two bad readbacks must fail and say it was tried twice; got %q", detail)
	}
	if _, err := os.Stat(filepath.Join(state, "input", "d003.md")); err == nil {
		t.Fatal("a third readback was asked for")
	}
}

// What a second try cannot cure is not retried: a member that reports its
// seeded files absent has not been briefed, and asking again changes nothing.
func TestASubstantiveReadbackFailureIsNotAskedAgain(t *testing.T) {
	missing := strings.Replace(goodReadback, `"missing":[]`, `"missing":["roles/dev.md"]`, 1)
	a, state := readbackMemberApp(t, missing, goodReadback)
	campaign, member := readbackCampaign()

	detail, _ := a.readbackOne(context.Background(), &strings.Builder{}, campaign, member)
	if !strings.Contains(detail, "absent") {
		t.Fatalf("want the missing briefing reported; got %q", detail)
	}
	if _, err := os.Stat(filepath.Join(state, "input", "d002.md")); err == nil {
		t.Fatal("a member that was never briefed was asked to read back again")
	}
}
