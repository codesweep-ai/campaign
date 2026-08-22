package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/covmap"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/store"
)

// All three sandbox-supported adapters are accepted, in either role.
func TestProfileAcceptsOpenCode(t *testing.T) {
	covmap.ProveCoreOnPass(t, "profile-validation", covmap.TierUnit)
	if _, err := profileFromFlags("opencode", []string{"worker=opencode"}, "", 0, ""); err != nil {
		t.Fatalf("opencode members should validate: %v", err)
	}
	if err := validCLI("bogus"); err == nil {
		t.Fatal("unknown CLI should still be rejected")
	}
}

func TestProfileFromExplicitAgents(t *testing.T) {
	covmap.ProveCoreOnPass(t, "profile-validation", covmap.TierUnit)
	p, err := profileFromFlags("codex", []string{"frontend=codex", "backend=claude"}, "", 0, "/src")
	if err != nil {
		t.Fatal(err)
	}
	if got := sortedNames(p.Agents); !slices.Equal(got, []string{"backend", "frontend"}) {
		t.Fatalf("names: %v", got)
	}
	if p.Agents["backend"].Repos[0].Path != "/src" {
		t.Fatal("repo was not applied")
	}
}

func TestProfileAndFlagsResolveEquivalently(t *testing.T) {
	covmap.ProveCoreOnPass(t, "profile-validation", covmap.TierUnit)
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "campaign.yaml")
	profileYAML := `apiVersion: codesweep.ai/v1alpha1
kind: CampaignProfile
defaults:
  engine: firecracker
orchestrator:
  cli: codex
agents:
  backend:
    cli: claude
  frontend:
    cli: codex
`
	if err := os.WriteFile(profilePath, []byte(profileYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &app{}
	fromYAML, _, err := a.resolveProfile(createOpts{profile: profilePath})
	if err != nil {
		t.Fatal(err)
	}
	fromFlags, _, err := a.resolveProfile(createOpts{orchestrator: "codex", agents: []string{"frontend=codex", "backend=claude"}})
	if err != nil {
		t.Fatal(err)
	}
	applyDefaults(&fromYAML)
	applyDefaults(&fromFlags)
	if !reflect.DeepEqual(fromYAML, fromFlags) {
		t.Fatalf("profile and flags differ:\nYAML:  %+v\nflags: %+v", fromYAML, fromFlags)
	}
}

func TestPlanAndDryRunDoNotCreateState(t *testing.T) {
	covmap.ProveCoreOnPass(t, "plan-determinism", covmap.TierUnit)
	for _, tc := range []struct {
		name string
		plan bool
		args []string
	}{
		{"plan", true, []string{"demo", "--orchestrator", "codex", "--agent", "worker=codex"}},
		{"dry-run", false, []string{"demo", "--dry-run", "--orchestrator", "codex", "--agent", "worker=codex"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := filepath.Join(t.TempDir(), "state-must-not-exist")
			a := &app{store: store.Store{Dir: stateDir}}
			cmd := a.createCmd(tc.plan)
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetArgs(tc.args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
				t.Fatalf("read-only command created state path: %v", err)
			}
		})
	}
}

func TestValidateDoesNotCreateState(t *testing.T) {
	covmap.ProveCoreOnPass(t, "plan-determinism", covmap.TierUnit)
	dir := t.TempDir()
	profile := filepath.Join(dir, "campaign.yaml")
	if err := os.WriteFile(profile, []byte("apiVersion: codesweep.ai/v1alpha1\nkind: CampaignProfile\norchestrator:\n  cli: codex\nagents:\n  worker:\n    cli: codex\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seedBriefsFor(t, profile, "worker")
	stateDir := filepath.Join(dir, "state-must-not-exist")
	a := &app{store: store.Store{Dir: stateDir}}
	cmd := a.validateCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{profile})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("validate created state path: %v", err)
	}
}

func TestEmptyRepositoryPlanIsNonMutatingAndCreateIsDeterministic(t *testing.T) {
	covmap.ProveCoreOnPass(t, "plan-determinism", covmap.TierUnit)
	root := t.TempDir()
	repo := filepath.Join(root, "new-app")
	p, err := profileFromFlags("codex", []string{"worker=codex"}, "", 0, repo)
	if err != nil {
		t.Fatal(err)
	}
	applyDefaults(&p)
	if err := resolveRepoRefs(&p); err != nil {
		t.Fatal(err)
	}
	want := initialRepoCommit()
	if !p.Orchestrator.Repos[0].Initialize || p.Orchestrator.Repos[0].ResolvedCommit != want || p.Agents["worker"].Repos[0].ResolvedCommit != want {
		t.Fatalf("empty repository plan not pinned: %+v", p)
	}
	if _, err := os.Stat(repo); !os.IsNotExist(err) {
		t.Fatalf("planning created repository path: %v", err)
	}
	if err := initializeRepos(&p); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil || string(bytes.TrimSpace(out)) != want {
		t.Fatalf("initialized HEAD = %q, %v; want %s", out, err, want)
	}
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != ".git" {
			t.Fatalf("initial repository is not empty: found %s", entry.Name())
		}
	}
}

func TestNonEmptyNonGitRepositoryIsRejected(t *testing.T) {
	covmap.ProveCoreOnPass(t, "repo-adoption", covmap.TierUnit)
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "existing.txt"), []byte("do not adopt silently\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := profileFromFlags("codex", []string{"worker=codex"}, "", 0, repo)
	if err != nil {
		t.Fatal(err)
	}
	applyDefaults(&p)
	if err := resolveRepoRefs(&p); err == nil {
		t.Fatal("non-empty non-Git directory was accepted")
	}
}

func TestUnbornGitRepositoryIsAdoptedOnMain(t *testing.T) {
	covmap.ProveCoreOnPass(t, "repo-adoption", covmap.TierUnit)
	repo := filepath.Join(t.TempDir(), "unborn")
	if out, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	p, err := profileFromFlags("codex", []string{"worker=codex"}, "", 0, repo)
	if err != nil {
		t.Fatal(err)
	}
	applyDefaults(&p)
	if err := resolveRepoRefs(&p); err != nil {
		t.Fatal(err)
	}
	if err := initializeRepos(&p); err != nil {
		t.Fatal(err)
	}
	head, err := exec.Command("git", "-C", repo, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(head)) != "main" {
		t.Fatalf("adopted HEAD = %q, %v", head, err)
	}
}

func TestHomogeneousFleet(t *testing.T) {
	covmap.ProveCoreOnPass(t, "profile-validation", covmap.TierUnit)
	p, err := profileFromFlags("codex", nil, "claude", 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := sortedNames(p.Agents); !slices.Equal(got, []string{"agent-01", "agent-02"}) {
		t.Fatalf("names: %v", got)
	}
}

func TestCreateArgsPreserveIsolation(t *testing.T) {
	covmap.ProveCoreOnPass(t, "create-resume", covmap.TierUnit)
	c := &model.Campaign{Engine: "firecracker", Group: "campaign-grp", Network: groupNetwork("campaign-grp")}
	m := model.Member{Sandbox: "box", Solo: true, Profile: model.MemberProfile{
		Resources: model.Resources{CPUS: 2, MemoryMiB: 2048},
		Auth:      model.Auth{InheritAgentLogin: []string{"codex"}},
		Repos:     []model.Repo{{Path: "/src/app", ResolvedCommit: "abc123", Name: "app"}},
		Snapshots: []model.Snapshot{{Path: "/src/specs", Name: "specs"}},
	}}
	got := createArgs(c, m)
	// The positional name is bare and the group is passed separately: create is
	// the one command that takes a name plus --group rather than a ref.
	want := []string{"create", "box", "--engine", "firecracker", "--type", "agent", "--group", "campaign-grp", "--yolo", "--solo", "--cpus", "2", "--mem", "2048", "--repo", "/src/app@abc123:app", "--snapshot", "/src/specs:specs", "--inherit-agent-login", "codex"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}

func TestAPIKeyTakesPrecedenceWithoutExposingValue(t *testing.T) {
	covmap.ProveOnPass(t, "auth-provisioning", "codex", "", covmap.TierUnit)
	t.Setenv("TEST_OPENAI_API_KEY", "do-not-put-this-in-argv")
	c := &model.Campaign{Engine: "firecracker", Group: "campaign-grp", Network: groupNetwork("campaign-grp")}
	m := model.Member{Sandbox: "box", Profile: model.MemberProfile{Auth: model.Auth{APIKeyFromEnv: []string{"TEST_OPENAI_API_KEY"}, InheritAgentLogin: []string{"codex"}}}}
	got := createArgs(c, m)
	if slices.Contains(got, "do-not-put-this-in-argv") || !slices.Contains(got, "TEST_OPENAI_API_KEY") {
		t.Fatalf("unsafe or missing env reference: %#v", got)
	}
	if slices.Contains(got, "codex") {
		t.Fatalf("login inheritance should be fallback only: %#v", got)
	}
}

func TestUnknownProfileFieldRejected(t *testing.T) {
	covmap.ProveCoreOnPass(t, "profile-validation", covmap.TierUnit)
	p := t.TempDir() + "/profile.yaml"
	write := []byte("apiVersion: codesweep.ai/v1alpha1\nkind: CampaignProfile\norchestrator:\n  cli: codex\n  typo: true\nagents:\n  one:\n    cli: claude\n")
	if err := os.WriteFile(p, write, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readProfile(p); err == nil {
		t.Fatal("expected strict decoding error")
	}
}

func TestResolveRepoRefsPinsOneCommit(t *testing.T) {
	covmap.ProveCoreOnPass(t, "plan-determinism", covmap.TierUnit)
	dir := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if b, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, b)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "README"}, {"commit", "-m", "base"}} {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if b, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, b)
		}
	}
	p := model.Profile{Orchestrator: model.MemberProfile{Repos: []model.Repo{{Path: dir}}}, Agents: map[string]model.MemberProfile{"one": {Repos: []model.Repo{{Path: dir}}}}}
	if err := resolveRepoRefs(&p); err != nil {
		t.Fatal(err)
	}
	if p.Orchestrator.Repos[0].ResolvedCommit == "" || p.Orchestrator.Repos[0].ResolvedCommit != p.Agents["one"].Repos[0].ResolvedCommit {
		t.Fatalf("refs not pinned equally: %+v", p)
	}
}

// The elapsed backstop's stated default is the campaign deadline (§1.7); the
// compiled-in day applies only when the profile declares neither. Derivation
// happens at create so the recorded policy is the resolved number.
func TestDeadlineDerivesElapsedBound(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	p, _ := profileFromFlags("codex", []string{"worker=codex"}, "", 0, "")

	// deadline set, elapsedSeconds unset: derive.
	p.Defaults.Deadline = "90m"
	c := buildCampaign("demo", p, "", "", now)
	if c.Policy.ElapsedSeconds != 5400 {
		t.Fatalf("elapsed must derive from the deadline: %d", c.Policy.ElapsedSeconds)
	}
	if !c.Deadline.Equal(now.Add(90 * time.Minute)) {
		t.Fatalf("the resolved instant must be recorded: %s", c.Deadline)
	}

	// explicit elapsedSeconds wins over the deadline.
	p.Defaults.Policy.ElapsedSeconds = 7200
	if c = buildCampaign("demo", p, "", "", now); c.Policy.ElapsedSeconds != 7200 {
		t.Fatalf("an explicit bound must win: %d", c.Policy.ElapsedSeconds)
	}

	// neither: the compiled-in day.
	p.Defaults.Deadline, p.Defaults.Policy.ElapsedSeconds = "", 0
	if c = buildCampaign("demo", p, "", "", now); c.Policy.ElapsedSeconds != 86400 || !c.Deadline.IsZero() {
		t.Fatalf("no deadline falls back to the default: %d %s", c.Policy.ElapsedSeconds, c.Deadline)
	}

	// and the instant reaches member.json, where the orchestrator can read it.
	p.Defaults.Deadline = "6h"
	c = buildCampaign("demo", p, "", "", now)
	_, doc, err := buildOrientation(c, c.Members[0], campaignInputs{})
	if err != nil {
		t.Fatal(err)
	}
	if !doc.Deadline.Equal(now.Add(6 * time.Hour)) {
		t.Fatalf("member.json must carry the deadline: %s", doc.Deadline)
	}
}

// A resumed create must re-anchor the deadline instant to the resuming
// attempt: the saved instant was derived from the aborted attempt's start and
// can already be in the past by the time the resume completes (seen live).
func TestResumedCreateReanchorsDeadline(t *testing.T) {
	a := &app{store: store.Store{Dir: t.TempDir()}}
	p, _ := profileFromFlags("codex", []string{"worker=codex"}, "", 0, "")
	p.Defaults.Deadline = "8m"

	firstAttempt := time.Date(2026, 8, 18, 5, 46, 0, 0, time.UTC)
	saved := buildCampaign("demo", p, "", "", firstAttempt)
	saved.Provisioning = "creating"
	if err := a.store.Save(saved); err != nil {
		t.Fatal(err)
	}

	resumeAt := firstAttempt.Add(14 * time.Minute) // past the saved instant
	adopted, err := a.adoptResumableCreate(buildCampaign("demo", p, "", "", resumeAt))
	if err != nil {
		t.Fatal(err)
	}
	if !adopted.Deadline.Equal(resumeAt.Add(8 * time.Minute)) {
		t.Fatalf("the deadline must anchor to the resuming attempt, got %s", adopted.Deadline)
	}
	if adopted.Policy.ElapsedSeconds != 480 {
		t.Fatalf("the derived duration must survive the resume: %d", adopted.Policy.ElapsedSeconds)
	}

	// A completed campaign still refuses adoption outright.
	saved.Provisioning = ""
	if err := a.store.Save(saved); err != nil {
		t.Fatal(err)
	}
	if _, err := a.adoptResumableCreate(buildCampaign("demo", p, "", "", resumeAt)); err == nil {
		t.Fatal("a completed campaign must not be adoptable")
	}
}

// A malformed or non-positive deadline must be refused at validate, not
// silently dropped at create.
func TestValidateProfileRejectsBadDeadline(t *testing.T) {
	p, _ := profileFromFlags("codex", []string{"worker=codex"}, "", 0, "")
	for _, bad := range []string{"2 fortnights", "-30m", "0s"} {
		p.Defaults.Deadline = bad
		if err := validateProfile(p); err == nil || !strings.Contains(err.Error(), "deadline") {
			t.Fatalf("deadline %q must be refused: %v", bad, err)
		}
	}
	p.Defaults.Deadline = "45m"
	if err := validateProfile(p); err != nil {
		t.Fatalf("a sane deadline must validate: %v", err)
	}
}

func TestPlanIdentityIsDeterministic(t *testing.T) {
	covmap.ProveCoreOnPass(t, "plan-determinism", covmap.TierUnit)
	p, _ := profileFromFlags("codex", []string{"worker=codex"}, "", 0, "")
	a := buildCampaign("demo", p, "", "", time.Time{})
	b := buildCampaign("demo", p, "", "", time.Time{})
	if a.ID != b.ID || a.Network != b.Network || !a.CreatedAt.IsZero() {
		t.Fatalf("plans differ: %+v %+v", a, b)
	}
	if len(a.ID)-len("demo-") != 16 {
		t.Fatalf("campaign identity does not contain 64 hash bits: %s", a.ID)
	}
}

func TestGeneratedMappingRejectsOverlongMemberName(t *testing.T) {
	covmap.ProveCoreOnPass(t, "create-resume", covmap.TierUnit)
	p, err := profileFromFlags("codex", []string{"this-agent-name-is-far-too-long=codex"}, "", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	campaign := buildCampaign("a-thirty-two-character-campaign-x", p, "", "", time.Time{})
	if err := validateCampaignMappings(campaign); err == nil || !strings.Contains(err.Error(), "shorten") {
		t.Fatalf("mapping validation error = %v", err)
	}
}

// A member socket path over the 108-byte AF_UNIX limit must be refused while
// planning. Left to run, socat truncates the path silently, binds the shortened
// name, and the only symptom is the microVM never becoming ready — two minutes
// later, blaming a serial log that was never written. Every label involved is
// individually legal, so nothing upstream can catch it.
func TestGeneratedMappingRejectsOverlongSocketPath(t *testing.T) {
	covmap.ProveCoreOnPass(t, "member-addressing", covmap.TierUnit)
	p, err := profileFromFlags("codex", []string{"worker=codex"}, "", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	// A deep instances root leaves no budget, whatever the names are.
	t.Setenv("CS_SANDBOX_INSTANCES_DIR", "/"+strings.Repeat("d/", 45))
	campaign := buildCampaign("demo", p, "", "", time.Time{})
	err = validateCampaignMappings(campaign)
	if err == nil || !strings.Contains(err.Error(), "AF_UNIX limit") {
		t.Fatalf("over-long socket path error = %v", err)
	}
	// A short root leaves plenty, and the same plan is accepted.
	t.Setenv("CS_SANDBOX_INSTANCES_DIR", "/i")
	if err := validateCampaignMappings(campaign); err != nil {
		t.Fatalf("short root rejected: %v", err)
	}
}

// The realistic case the port had to fix: an ordinary campaign name with the
// reserved `orchestrator` member, under the default instances root.
func TestOrdinaryCampaignFitsTheSocketBudget(t *testing.T) {
	covmap.ProveCoreOnPass(t, "member-addressing", covmap.TierUnit)
	p, err := profileFromFlags("codex", []string{"worker-a=codex"}, "", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CS_SANDBOX_INSTANCES_DIR", "/home/user/.local/share/cs-sandbox/instances")
	campaign := buildCampaign("portcheck", p, "", "", time.Time{})
	if err := validateCampaignMappings(campaign); err != nil {
		t.Fatalf("an ordinary campaign must fit: %v", err)
	}
}

// The planned branch is a prediction shown by `plan`, before any sandbox exists
// to ask. It should still look like what cs-sandbox produces — a plan that
// displays an implausible branch is misleading — but nothing downstream depends
// on it: create replaces it with the recorded value
// (TestCreateAdoptsTheBranchSandboxActuallyMade).
func TestPlannedBranchLooksLikeTheSandboxDerivation(t *testing.T) {
	covmap.ProveCoreOnPass(t, "member-addressing", covmap.TierUnit)
	p, err := profileFromFlags("codex", []string{"backend=codex"}, "", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	campaign := buildCampaign("demo", p, "", "", time.Time{})
	for _, m := range campaign.Members {
		// cs-sandbox: "cs-sandbox/" + <name>.<group>, for any non-default group.
		want := "cs-sandbox/" + m.Sandbox + "." + campaign.Group
		if m.Branch != want {
			t.Errorf("member %s branch = %q, want %q", m.Name, m.Branch, want)
		}
		// And it carries the group, so two campaigns cannot collide on one ref
		// in a shared host source repository.
		if !strings.Contains(m.Branch, campaign.Group) {
			t.Errorf("member %s branch %q does not carry the campaign group", m.Name, m.Branch)
		}
	}
	// Two campaigns with the same member name produce different branches.
	other := buildCampaign("other", p, "", "", time.Time{})
	if campaign.Members[1].Branch == other.Members[1].Branch {
		t.Fatalf("two campaigns collide on %q", campaign.Members[1].Branch)
	}
}

func TestTypedSetOverridesAndRejectsUnknown(t *testing.T) {
	covmap.ProveCoreOnPass(t, "profile-validation", covmap.TierUnit)
	p, _ := profileFromFlags("codex", []string{"worker=codex"}, "", 0, "")
	if err := applySets(&p, []string{"defaults.resources.cpus=8", "agents.worker.cli=claude"}); err != nil {
		t.Fatal(err)
	}
	if p.Defaults.Resources.CPUS != 8 || p.Agents["worker"].CLI != "claude" {
		t.Fatalf("overrides not applied: %+v", p)
	}
	if err := applySets(&p, []string{"agents.worker.typo=x"}); err == nil {
		t.Fatal("unknown override accepted")
	}
}

// A member's declared environment reaches its sandbox, and does so whichever
// way the auth branch goes: the two are independent, and an agent pointed at a
// proxy with ANTHROPIC_BASE_URL still has to authenticate.
func TestDeclaredEnvReachesTheSandboxAlongsideAuth(t *testing.T) {
	covmap.ProveCoreOnPass(t, "create-resume", covmap.TierUnit)
	c := &model.Campaign{Engine: "firecracker", Group: "grp", Network: groupNetwork("grp")}
	m := model.Member{Sandbox: "box", Profile: model.MemberProfile{
		Env:  []string{"ANTHROPIC_BASE_URL=http://10.89.9.1:8080"},
		Auth: model.Auth{InheritAgentLogin: []string{"claude"}},
	}}
	got := createArgs(c, m)
	if !slices.Contains(got, "ANTHROPIC_BASE_URL=http://10.89.9.1:8080") {
		t.Fatalf("declared env missing: %#v", got)
	}
	if !slices.Contains(got, "--inherit-agent-login") {
		t.Fatalf("env must not displace the auth grant: %#v", got)
	}
}

// Defaults apply to a member that declares no environment, and are replaced
// rather than merged by one that does — a member naming its own environment is
// describing it completely.
func TestDefaultEnvAppliesOnlyWhereAMemberDeclaresNone(t *testing.T) {
	covmap.ProveCoreOnPass(t, "profile-validation", covmap.TierUnit)
	p := &model.Profile{
		Defaults:     model.Defaults{Env: []string{"SHARED=1"}},
		Orchestrator: model.MemberProfile{CLI: "claude"},
		Agents: map[string]model.MemberProfile{
			"own": {CLI: "claude", Env: []string{"OWN=2"}},
		},
	}
	applyDefaults(p)
	if !slices.Equal(p.Orchestrator.Env, []string{"SHARED=1"}) {
		t.Errorf("orchestrator env = %v, want the default", p.Orchestrator.Env)
	}
	if !slices.Equal(p.Agents["own"].Env, []string{"OWN=2"}) {
		t.Errorf("agent env = %v, want its own declaration untouched", p.Agents["own"].Env)
	}
}
