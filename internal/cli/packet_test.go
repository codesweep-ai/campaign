package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign/internal/covmap"
	"github.com/codesweep-ai/campaign/internal/store"
)

func runPacket(t *testing.T, asJSON bool, args ...string) (string, error) {
	t.Helper()
	a := &app{store: store.Store{Dir: t.TempDir()}, json: asJSON}
	var out, errOut bytes.Buffer
	cmd := a.packetCmd()
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// writeCampaign lays out a campaign the way the convention finds it: the
// profile, then each named file beside it. Every member clones a repository
// that does not exist yet, so plan pins a first commit and creates nothing.
func writeCampaign(t *testing.T, agents string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	repo := filepath.Join(dir, "app")
	profile := "apiVersion: codesweep.ai/v1alpha1\nkind: CampaignProfile\n" +
		"orchestrator:\n  cli: codex\n  auth:\n    apiKey: [openai]\n  repos:\n    - path: " + repo + "\n" +
		"agents:\n" + strings.ReplaceAll(agents, "REPO", repo)
	files["profile.yaml"] = profile
	for rel, body := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "profile.yaml")
}

// The packet is a review of what create will do, so it must be drawn from what
// create does: the files seedFiles delivers, the orientation buildOrientation
// renders, and the authored text byte for byte. A page that described create
// from a second copy of its rules would be a review of the copy.
func TestThePacketHoldsWhatCreateWouldDeliver(t *testing.T) {
	covmap.ProveCoreOnPass(t, "inspection", covmap.TierUnit)
	root, err := covmap.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	profile := filepath.Join(root, "testdata", "example-campaign", "profile.yaml")
	page := filepath.Join(t.TempDir(), "review.html")
	out, err := runPacket(t, false, "example", "--profile", profile, "--file", page)
	if err != nil {
		t.Fatal(err)
	}
	if out != "wrote "+page+": 2 members, 0 refusals, 0 warnings\n" {
		t.Errorf("packet reported %q", out)
	}
	data, err := readPacket(page)
	if err != nil {
		t.Fatalf("the page does not carry readable packet data: %v", err)
	}

	a := &app{store: store.Store{Dir: t.TempDir()}}
	campaign, decoded, err := a.planCampaign(createOpts{profile: profile}, "example", true)
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := loadCampaignInputs(profile, decoded)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range campaign.Members {
		i := slices.IndexFunc(data.Members, func(pm packetMember) bool { return pm.Name == m.Name })
		if i < 0 {
			t.Fatalf("the packet has no page for %s", m.Name)
		}
		pm := data.Members[i]
		var seeded, listed []string
		for _, f := range inputs.seedFiles(m) {
			seeded = append(seeded, f.Path)
		}
		for _, s := range pm.Seeds {
			listed = append(listed, s.Guest)
		}
		if !slices.Equal(seeded, listed) {
			t.Errorf("%s: create seeds %v, and the packet lists %v", m.Name, seeded, listed)
		}
		text, _, err := buildOrientation(campaign, m, inputs)
		if err != nil {
			t.Fatal(err)
		}
		if pm.Orientation.Content != text || pm.Orientation.Digest != digestOf(text) {
			t.Errorf("%s: the packet's orientation is not the text create renders", m.Name)
		}
		if len(pm.Trees) != 1 || pm.Trees[0].Name != "report-generator" || !pm.Trees[0].Created {
			t.Errorf("%s: the example's one repository, not yet created, is not shown: %+v", m.Name, pm.Trees)
		}
	}
	for _, f := range data.Files {
		disk, err := os.ReadFile(filepath.Join(root, "testdata", "example-campaign", f.Path))
		if err != nil {
			t.Fatal(err)
		}
		if f.Content != string(disk) || f.Digest != digestOf(string(disk)) || f.Absent {
			t.Errorf("%s is not carried byte for byte with its digest", f.Path)
		}
	}
	if len(data.Findings) != 0 {
		t.Errorf("the example raises nothing at create, and the packet found %+v", data.Findings)
	}
}

// A packet is most useful while a campaign is still being written, so it must
// render one that create would refuse, and say what create would refuse and
// warn about in the tool's own words.
func TestThePacketShowsWhatCreateWouldRefuseAndWarn(t *testing.T) {
	covmap.ProveCoreOnPass(t, "inspection", covmap.TierUnit)
	t.Setenv("CS_PACKET_TEST_KEY", "")
	os.Unsetenv("CS_PACKET_TEST_KEY")
	profile := writeCampaign(t,
		"  builder:\n    cli: codex\n    auth:\n      inheritApiKeyFromEnv: [CS_PACKET_TEST_KEY]\n    repos:\n      - path: REPO\n"+
			"  checker:\n    cli: codex\n    auth:\n      apiKey: [openai]\n    repos:\n      - path: REPO\n",
		map[string]string{
			"mission.md":            "# Mission\n\nEnd with `campaign-finished`.\n",
			"roles/orchestrator.md": "# Orchestrator\n",
			"roles/builder.md":      "# Builder\n",
		})
	page := filepath.Join(t.TempDir(), "packet.html")
	if _, err := runPacket(t, false, "draft", "--profile", profile, "--file", page); err != nil {
		t.Fatalf("a campaign create would refuse must still render: %v", err)
	}
	data, err := readPacket(page)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"campaign inputs are missing beside":    "refusal",
		"no credential for builder":             "refusal",
		`names the outcome "campaign-finished"`: "warning",
		"builder has no credential":             "warning",
		"builder hold credentials":              "warning",
	}
	for text, level := range want {
		if !slices.ContainsFunc(data.Findings, func(f packetFinding) bool {
			return f.Level == level && strings.Contains(f.Text, text)
		}) {
			t.Errorf("no %s containing %q in %+v", level, text, data.Findings)
		}
	}
	i := slices.IndexFunc(data.Files, func(f packetFile) bool { return f.Path == "roles/checker.md" })
	if i < 0 || !data.Files[i].Absent {
		t.Fatalf("the unwritten brief is not listed as absent: %+v", data.Files)
	}
	j := slices.IndexFunc(data.Members, func(m packetMember) bool { return m.Name == "checker" })
	if j < 0 || !slices.ContainsFunc(data.Members[j].Seeds, func(s packetSeed) bool { return s.Source == "roles/checker.md" }) {
		t.Errorf("checker's page does not say where its unwritten brief will land")
	}
}

// A brief is arbitrary text, and the page carries it inside a script element.
// None of it may end that element early.
func TestABriefCannotEscapeThePage(t *testing.T) {
	covmap.ProveCoreOnPass(t, "inspection", covmap.TierUnit)
	hostile := "# Worker\n\n</script><script>document.title='x'</script>\n<!--PACKET-DATA-->\n"
	profile := writeCampaign(t, "  worker:\n    cli: codex\n    auth:\n      apiKey: [openai]\n    repos:\n      - path: REPO\n",
		map[string]string{"mission.md": "# M\n", "roles/orchestrator.md": "# O\n", "roles/worker.md": hostile})
	page := filepath.Join(t.TempDir(), "packet.html")
	if _, err := runPacket(t, false, "hostile", "--profile", profile, "--file", page); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(page)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "</script><script>document.title") {
		t.Error("the brief's markup reached the page unescaped")
	}
	data, err := readPacket(page)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(data.Files, func(f packetFile) bool { return f.Path == "roles/worker.md" })
	if i < 0 || data.Files[i].Content != hostile {
		t.Error("the brief did not survive the page byte for byte")
	}
}

// --since compares against what was reviewed before, so it has to read an
// earlier packet back, and refuse one it cannot compare.
func TestSinceComparesWithAnEarlierPacketOfTheSameCampaign(t *testing.T) {
	covmap.ProveCoreOnPass(t, "inspection", covmap.TierUnit)
	files := map[string]string{"mission.md": "# M\n", "roles/orchestrator.md": "# O\n", "roles/worker.md": "# Worker\n\nfirst\n"}
	profile := writeCampaign(t, "  worker:\n    cli: codex\n    auth:\n      apiKey: [openai]\n    repos:\n      - path: REPO\n", files)
	dir := t.TempDir()
	first := filepath.Join(dir, "first.html")
	if _, err := runPacket(t, false, "acme", "--profile", profile, "--file", first); err != nil {
		t.Fatal(err)
	}
	brief := filepath.Join(filepath.Dir(profile), "roles", "worker.md")
	if err := os.WriteFile(brief, []byte("# Worker\n\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(dir, "second.html")
	if _, err := runPacket(t, false, "acme", "--profile", profile, "--file", second, "--since", first); err != nil {
		t.Fatal(err)
	}
	data, err := readPacket(second)
	if err != nil {
		t.Fatal(err)
	}
	if data.Since == nil || data.Since.Since != nil {
		t.Fatalf("the earlier packet is not carried, or carries its own history: %+v", data.Since)
	}
	i := slices.IndexFunc(data.Since.Files, func(f packetFile) bool { return f.Path == "roles/worker.md" })
	if i < 0 || data.Since.Files[i].Content != "# Worker\n\nfirst\n" {
		t.Error("the earlier packet's brief is not what was reviewed then")
	}

	if _, err := runPacket(t, false, "other", "--profile", profile, "--file", filepath.Join(dir, "x.html"), "--since", first); err == nil ||
		!strings.Contains(err.Error(), "another campaign") {
		t.Errorf("a packet of another campaign was compared: %v", err)
	}
	if _, err := runPacket(t, false, "acme", "--profile", profile, "--file", filepath.Join(dir, "y.html"), "--since", profile); err == nil ||
		!strings.Contains(err.Error(), "not a page written by cs-campaign packet") {
		t.Errorf("a file that is not a packet was compared: %v", err)
	}
}

// --json is the same data for a program, and writes no page.
func TestPacketJSONWritesNoPage(t *testing.T) {
	covmap.ProveCoreOnPass(t, "inspection", covmap.TierUnit)
	profile := writeCampaign(t, "  worker:\n    cli: codex\n    auth:\n      apiKey: [openai]\n    repos:\n      - path: REPO\n",
		map[string]string{"mission.md": "# M\n", "roles/orchestrator.md": "# O\n", "roles/worker.md": "# W\n"})
	t.Chdir(t.TempDir())
	out, err := runPacket(t, true, "acme", "--profile", profile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"schema": 1`) || !strings.Contains(out, `"orientationGuest"`) {
		t.Errorf("--json did not print the packet data:\n%.300s", out)
	}
	if _, err := os.Stat("packet.html"); !os.IsNotExist(err) {
		t.Errorf("--json wrote a page: %v", err)
	}
}

func TestPacketNeedsAProfile(t *testing.T) {
	covmap.ProveCoreOnPass(t, "inspection", covmap.TierUnit)
	if _, err := runPacket(t, false, "acme"); err == nil || !strings.Contains(err.Error(), "--profile") {
		t.Errorf("a packet without a profile was not refused by naming --profile: %v", err)
	}
}
