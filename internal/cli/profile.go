package cli

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/codesweep-ai/campaign/internal/model"
	"go.yaml.in/yaml/v3"
)

var dnsName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// modelToken bounds what may reach a guest's config file. The value is written
// into shell, TOML and JSON depending on the adapter, so it is restricted here
// rather than escaped three ways: every real model slug and effort level is in
// this class, from "claude-opus-5" to a fully qualified
// "fireworks-ai/accounts/fireworks/models/kimi-k3".
var modelToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

func readProfile(path string) (model.Profile, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return model.Profile{}, "", err
	}
	var p model.Profile
	d := yaml.NewDecoder(strings.NewReader(string(b)))
	d.KnownFields(true)
	if err = d.Decode(&p); err != nil {
		return p, "", fmt.Errorf("decode profile: %w", err)
	}
	h := sha256.Sum256(b)
	return p, hex.EncodeToString(h[:]), validateProfile(p)
}
func validateProfile(p model.Profile) error {
	if p.APIVersion != model.APIVersion {
		return fmt.Errorf("apiVersion must be %q", model.APIVersion)
	}
	if p.Kind != "CampaignProfile" {
		return errors.New("kind must be CampaignProfile")
	}
	if err := validCLI(p.Orchestrator.CLI); err != nil {
		return fmt.Errorf("orchestrator: %w", err)
	}
	if err := validateAuth(p.Orchestrator.Auth); err != nil {
		return fmt.Errorf("orchestrator: %w", err)
	}
	if err := validateModelConfig(p.Orchestrator); err != nil {
		return fmt.Errorf("orchestrator: %w", err)
	}
	if err := validateMemberPolicy(p.Orchestrator); err != nil {
		return fmt.Errorf("orchestrator: %w", err)
	}
	for name, m := range p.Agents {
		if !dnsName.MatchString(name) || name == "orchestrator" {
			return fmt.Errorf("invalid or reserved agent name %q", name)
		}
		if err := validCLI(m.CLI); err != nil {
			return fmt.Errorf("agent %s: %w", name, err)
		}
		if err := validateAuth(m.Auth); err != nil {
			return fmt.Errorf("agent %s: %w", name, err)
		}
		if err := validateModelConfig(m); err != nil {
			return fmt.Errorf("agent %s: %w", name, err)
		}
		if err := validateMemberPolicy(m); err != nil {
			return fmt.Errorf("agent %s: %w", name, err)
		}
	}
	if len(p.Agents) == 0 {
		return errors.New("at least one agent is required")
	}
	if p.Defaults.Deadline != "" {
		d, err := time.ParseDuration(p.Defaults.Deadline)
		if err != nil {
			return fmt.Errorf("defaults.deadline: not a duration (want e.g. \"90m\", \"6h\"): %w", err)
		}
		if d <= 0 {
			return fmt.Errorf("defaults.deadline: must be positive, got %s", p.Defaults.Deadline)
		}
	}
	return nil
}

// validateMemberPolicy refuses a policy number a member cannot have. One
// resolved set governs the campaign, and stallSeconds is the only number
// carried per seat — so a member declaring pollSeconds would run on the
// campaign's value with its own profile saying otherwise, and nothing would
// ever report the difference. Same rule as validateModelConfig below: fail on
// a declaration that cannot be honoured, rather than write it and let the
// member run on something else.
func validateMemberPolicy(m model.MemberProfile) error {
	fields := m.Policy.CampaignOnlyFields()
	if len(fields) == 0 {
		return nil
	}
	return fmt.Errorf("a member's policy: block carries stallSeconds alone; move %s under defaults.policy, which is where the dispatch machine reads it",
		strings.Join(fields, ", "))
}

// validateModelConfig fails closed on a declaration this CLI cannot honour,
// rather than writing it and letting the member run on something else.
func validateModelConfig(m model.MemberProfile) error {
	if m.Model != "" && !modelToken.MatchString(m.Model) {
		return fmt.Errorf("invalid model %q", m.Model)
	}
	if m.Effort == "" {
		return nil
	}
	if !modelToken.MatchString(m.Effort) {
		return fmt.Errorf("invalid effort %q", m.Effort)
	}
	// opencode attaches reasoning options to a specific model rather than to the
	// session, so there is nothing to hang the effort on without knowing which
	// model it applies to. Requiring the pair is the narrow rule; refusing
	// opencode effort outright would block the pairs that do honour it.
	//
	// Which pairs those are is deliberately NOT decided here: opencode exposes
	// reasoning variants for some model/transport combinations and not others,
	// and encoding that table would mean re-deriving an upstream model list that
	// only agrees until it changes.
	if m.CLI == "opencode" && m.Model == "" {
		return errors.New("effort requires model for opencode: reasoning options attach to a named model, not to the session")
	}
	return nil
}

func validateAuth(a model.Auth) error {
	for _, key := range a.APIKeyFromEnv {
		if !envName.MatchString(key) {
			return fmt.Errorf("invalid API-key environment name %q", key)
		}
	}
	return nil
}
func validCLI(s string) error {
	if model.ValidAdapterCLI(s) {
		return nil
	}
	return fmt.Errorf("unsupported CLI %q", s)
}
func expandPath(s string) string {
	if strings.HasPrefix(s, "~/") {
		if h, e := os.UserHomeDir(); e == nil {
			return filepath.Join(h, s[2:])
		}
	}
	a, e := filepath.Abs(s)
	if e == nil {
		return a
	}
	return s
}
func applyDefaults(p *model.Profile) {
	if p.Defaults.Engine == "" {
		p.Defaults.Engine = "firecracker"
	}
	apply := func(m *model.MemberProfile) {
		if m.Resources.CPUS == 0 {
			m.Resources.CPUS = p.Defaults.Resources.CPUS
		}
		if m.Resources.MemoryMiB == 0 {
			m.Resources.MemoryMiB = p.Defaults.Resources.MemoryMiB
		}
		// All or nothing, not merged: a member that names its own environment
		// is describing it completely, and silently folding the defaults back
		// in would make a member impossible to opt out.
		if len(m.Env) == 0 {
			m.Env = p.Defaults.Env
		}
		for i := range m.Repos {
			m.Repos[i].Path = expandPath(m.Repos[i].Path)
		}
		for i := range m.Snapshots {
			m.Snapshots[i].Path = expandPath(m.Snapshots[i].Path)
		}
	}
	apply(&p.Orchestrator)
	for n, m := range p.Agents {
		apply(&m)
		p.Agents[n] = m
	}
}
func resolveRepoRefs(p *model.Profile) error {
	resolve := func(m *model.MemberProfile) error {
		for i := range m.Repos {
			r := &m.Repos[i]
			ref := r.Ref
			if ref == "" {
				ref = "HEAD"
			}
			b, err := exec.Command("git", "-C", r.Path, "rev-parse", "--verify", ref+"^{commit}").Output()
			if err != nil {
				if ref == "HEAD" {
					ok, initErr := canInitializeRepo(r.Path)
					if initErr != nil {
						return initErr
					}
					if ok {
						r.Initialize = true
						r.ResolvedCommit = initialRepoCommit()
						continue
					}
				}
				return fmt.Errorf("resolve repo %s ref %s: %w", r.Path, ref, err)
			}
			r.ResolvedCommit = strings.TrimSpace(string(b))
		}
		return nil
	}
	if err := resolve(&p.Orchestrator); err != nil {
		return err
	}
	for n, m := range p.Agents {
		if err := resolve(&m); err != nil {
			return fmt.Errorf("agent %s: %w", n, err)
		}
		p.Agents[n] = m
	}
	return nil
}

const initialRepoCommitBody = "tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\n" +
	"author cs-campaign <cs-campaign@localhost> 0 +0000\n" +
	"committer cs-campaign <cs-campaign@localhost> 0 +0000\n\n" +
	"Initial empty campaign repository\n"

func initialRepoCommit() string {
	body := []byte(initialRepoCommitBody)
	h := sha1.New()
	fmt.Fprintf(h, "commit %d%c", len(body), 0)
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

func canInitializeRepo(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect repository path %s: %w", path, err)
	}
	if len(entries) == 0 || (len(entries) == 1 && entries[0].Name() == ".git" && entries[0].IsDir()) {
		return true, nil
	}
	return false, nil
}

func initializeRepos(p *model.Profile) error {
	seen := map[string]bool{}
	all := append([]model.MemberProfile{p.Orchestrator}, values(p.Agents)...)
	for _, member := range all {
		for _, repo := range member.Repos {
			if !repo.Initialize || seen[repo.Path] {
				continue
			}
			seen[repo.Path] = true
			ok, err := canInitializeRepo(repo.Path)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("cannot initialize non-empty repository path %s", repo.Path)
			}
			if err = os.MkdirAll(repo.Path, 0o700); err != nil {
				return err
			}
			if _, statErr := os.Stat(filepath.Join(repo.Path, ".git")); os.IsNotExist(statErr) {
				if out, initErr := exec.Command("git", "init", "-b", "main", repo.Path).CombinedOutput(); initErr != nil {
					return fmt.Errorf("initialize repository %s: %w: %s", repo.Path, initErr, out)
				}
			}
			cmd := exec.Command("git", "-C", repo.Path, "hash-object", "-t", "commit", "-w", "--stdin")
			cmd.Stdin = bytes.NewBufferString(initialRepoCommitBody)
			out, hashErr := cmd.Output()
			if hashErr != nil {
				return fmt.Errorf("write initial commit for %s: %w", repo.Path, hashErr)
			}
			got := strings.TrimSpace(string(out))
			if got != initialRepoCommit() {
				return fmt.Errorf("initial commit mismatch for %s: got %s want %s", repo.Path, got, initialRepoCommit())
			}
			if out, err = exec.Command("git", "-C", repo.Path, "update-ref", "refs/heads/main", got).CombinedOutput(); err != nil {
				return fmt.Errorf("set initial branch for %s: %w: %s", repo.Path, err, out)
			}
			if out, err = exec.Command("git", "-C", repo.Path, "symbolic-ref", "HEAD", "refs/heads/main").CombinedOutput(); err != nil {
				return fmt.Errorf("select initial branch for %s: %w: %s", repo.Path, err, out)
			}
		}
	}
	return nil
}
func profileFromFlags(orchestrator string, agents []string, agentCLI string, count int, repo string) (model.Profile, error) {
	p := model.Profile{APIVersion: model.APIVersion, Kind: "CampaignProfile", Defaults: model.Defaults{Engine: "firecracker"}, Agents: map[string]model.MemberProfile{}}
	p.Orchestrator.CLI = orchestrator
	if repo != "" {
		p.Orchestrator.Repos = []model.Repo{{Path: repo}}
	}
	if len(agents) > 0 && (agentCLI != "" || count > 0) {
		return p, errors.New("--agent and --agent-cli/--agents are mutually exclusive")
	}
	for _, v := range agents {
		a := strings.SplitN(v, "=", 2)
		if len(a) != 2 {
			return p, errors.New("--agent must be name=cli")
		}
		m := model.MemberProfile{CLI: a[1]}
		if repo != "" {
			m.Repos = []model.Repo{{Path: repo}}
		}
		p.Agents[a[0]] = m
	}
	if agentCLI != "" || count > 0 {
		if agentCLI == "" || count < 1 {
			return p, errors.New("--agent-cli and positive --agents must be used together")
		}
		for i := 1; i <= count; i++ {
			n := fmt.Sprintf("agent-%02d", i)
			m := model.MemberProfile{CLI: agentCLI}
			if repo != "" {
				m.Repos = []model.Repo{{Path: repo}}
			}
			p.Agents[n] = m
		}
	}
	return p, validateProfile(p)
}
func sortedNames(m map[string]model.MemberProfile) []string {
	out := make([]string, 0, len(m))
	for n := range m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
func applySets(p *model.Profile, sets []string) error {
	for _, raw := range sets {
		parts := strings.SplitN(raw, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("--set must be path=value: %q", raw)
		}
		path, value := parts[0], parts[1]
		switch path {
		case "defaults.engine":
			if value != "firecracker" && value != "podman" {
				return fmt.Errorf("invalid engine %q", value)
			}
			p.Defaults.Engine = value
		case "defaults.resources.cpus":
			n, e := strconv.Atoi(value)
			if e != nil || n < 1 {
				return fmt.Errorf("invalid cpus %q", value)
			}
			p.Defaults.Resources.CPUS = n
		case "defaults.resources.memoryMiB":
			n, e := strconv.Atoi(value)
			if e != nil || n < 128 {
				return fmt.Errorf("invalid memoryMiB %q", value)
			}
			p.Defaults.Resources.MemoryMiB = n
		case "orchestrator.cli":
			if e := validCLI(value); e != nil {
				return e
			}
			p.Orchestrator.CLI = value
		default:
			if strings.HasPrefix(path, "agents.") && strings.HasSuffix(path, ".cli") {
				name := strings.TrimSuffix(strings.TrimPrefix(path, "agents."), ".cli")
				m, ok := p.Agents[name]
				if !ok {
					return fmt.Errorf("unknown agent %q", name)
				}
				if e := validCLI(value); e != nil {
					return e
				}
				m.CLI = value
				p.Agents[name] = m
			} else {
				return fmt.Errorf("unsupported --set path %q", path)
			}
		}
	}
	return validateProfile(*p)
}
