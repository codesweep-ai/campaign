package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/protocol"
	"github.com/codesweep-ai/campaign/internal/store"
	"github.com/spf13/cobra"
)

func validName(name string) error {
	if len(name) > 32 || !dnsName.MatchString(name) {
		return fmt.Errorf("invalid campaign name %q (use a DNS label of at most 32 characters)", name)
	}
	return nil
}

// idFor derives a deterministic campaign ID from the name and the fully
// resolved profile, so replanning identical inputs yields identical group and
// sandbox names. 64 hash bits, per the collision-resistance promise.
func idFor(name string, profile model.Profile) string {
	return fmt.Sprintf("%s-%x", name, campaignHash(profile)[:8])
}

func campaignHash(profile model.Profile) []byte {
	encoded, _ := json.Marshal(profile)
	sum := sha256.Sum256(encoded)
	return sum[:]
}

// discriminator is the short campaign-scoped suffix carried by the group and by
// every member sandbox name. It is 32 bits rather than the ID's 64 because both
// names land in a Unix socket path bounded at 108 bytes (see memberPathBudget),
// and the group directory already spends the campaign name once. Two campaigns
// collide here only if they share a name AND a 32-bit profile hash — and two
// campaigns cannot share a name, since host state is keyed by it and `create`
// refuses an existing record.
func discriminator(profile model.Profile) string {
	return hex.EncodeToString(campaignHash(profile)[:4])
}

// groupNetwork mirrors cs-sandbox's own derivation (state.NetworkName): a
// group's network is computed from its name, never configured, so the two can
// never disagree. Recorded in state because the design requires the resolved
// network to be inspectable without asking cs-sandbox.
func groupNetwork(group string) string { return "cs-sandbox-" + group }

func buildCampaign(name string, profile model.Profile, profilePath, digest string, now time.Time) *model.Campaign {
	applyDefaults(&profile)
	id := idFor(name, profile)
	short := discriminator(profile)
	group := name + "-" + short
	// The elapsed backstop's stated default is the campaign deadline
	// (SPEC.md §6.1); only when the profile declares neither does the
	// compiled-in day apply.
	// Derived here, at create, so the recorded policy is the resolved number —
	// never recomputed from the deadline later. An explicit elapsedSeconds
	// always wins. The parse cannot fail on any validated profile; a caller
	// that skipped validation gets the compiled-in fallback, the safe
	// direction.
	pol := profile.Defaults.Policy
	var deadlineAt time.Time
	if profile.Defaults.Deadline != "" {
		if d, err := time.ParseDuration(profile.Defaults.Deadline); err == nil && d > 0 {
			deadlineAt = now.UTC().Add(d)
			if pol.ElapsedSeconds == 0 {
				pol.ElapsedSeconds = int(d / time.Second)
			}
		}
	}
	campaign := &model.Campaign{
		Version: store.CurrentVersion, ID: id, Name: name,
		Group:     group,
		Network:   groupNetwork(group),
		Engine:    profile.Defaults.Engine,
		Policy:    pol.Resolve(),
		Deadline:  deadlineAt,
		CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
		ProfilePath: profilePath, ProfileDigest: digest,
	}
	// Sandbox names carry the campaign discriminator rather than being bare
	// member names. It is no longer needed for branch uniqueness — cs-sandbox
	// now group-scopes the branch itself — but it still keeps session names,
	// remote-tool log files and archive paths distinct between campaigns, none
	// of which any group scopes.
	//
	// The suffix is the short discriminator, not the full campaign ID: the ID
	// is already the group directory's name, and repeating it in the member
	// name overflows the socket-path budget for every campaign name there is.
	orchestratorSandbox := "orchestrator-" + short
	campaign.Members = append(campaign.Members, model.Member{
		Name: "orchestrator", Role: "orchestrator", CLI: profile.Orchestrator.CLI,
		Sandbox: orchestratorSandbox, Ref: memberRef(orchestratorSandbox, group),
		Branch:       memberBranch(group, orchestratorSandbox),
		Session:      model.Session{Name: id + "-orchestrator"},
		Profile:      profile.Orchestrator,
		StallSeconds: resolveStall(profile, profile.Orchestrator, true),
	})
	for _, agentName := range sortedNames(profile.Agents) {
		agent := profile.Agents[agentName]
		sandbox := agentName + "-" + short
		campaign.Members = append(campaign.Members, model.Member{
			Name: agentName, Role: "agent", CLI: agent.CLI,
			Sandbox: sandbox, Ref: memberRef(sandbox, group),
			Branch: memberBranch(group, sandbox), Solo: true,
			// Session names live in the HOST's ~/.cs-<cli>-remote-{logs,pids,
			// sessions}, which no group scopes, so they keep the full campaign
			// ID. They are not path components of a socket.
			Session:      model.Session{Name: id + "-" + agentName},
			Profile:      agent,
			StallSeconds: resolveStall(profile, agent, false),
		})
	}
	return campaign
}

// resolveStall resolves one seat's CS_<CLI>_STALL_SECS: member override,
// else campaign policy, else the compiled-in default — which is far higher
// for the orchestrator, where long quiet is normal and a misjudged stall
// stalls the whole campaign until a human notices.
func resolveStall(profile model.Profile, member model.MemberProfile, orchestrator bool) int {
	if member.Policy.StallSeconds > 0 {
		return member.Policy.StallSeconds
	}
	if profile.Defaults.Policy.StallSeconds > 0 {
		return profile.Defaults.Policy.StallSeconds
	}
	if orchestrator {
		return protocol.DefaultOrchestratorStallSeconds
	}
	return protocol.DefaultPolicy().StallSeconds
}

// memberRef renders the host-global reference cs-sandbox accepts.
func memberRef(sandbox, group string) string { return sandbox + "." + group }

// memberBranch is the branch a member's clone is EXPECTED to get. It is only a
// prediction: `plan` must show something before any sandbox exists, and the rule
// that really spells it lives in cs-sandbox. Create replaces this with the
// recorded value (see adoptMemberBranch), so nothing downstream depends on the
// guess being right — which matters, because it silently stopped being right
// the day cs-sandbox started qualifying the branch with the group.
func memberBranch(group, sandbox string) string {
	return "cs-sandbox/" + memberRef(sandbox, group)
}

func validateCampaignMappings(campaign *model.Campaign) error {
	// The group name becomes a Podman network, a key directory, an ssh alias
	// suffix and a state directory upstream, so it carries the same single-DNS
	// label restriction a sandbox name does.
	if len(campaign.Group) > 63 || !dnsName.MatchString(campaign.Group) {
		return fmt.Errorf("generated campaign group %q is not a valid DNS label; shorten the campaign name", campaign.Group)
	}
	for _, member := range campaign.Members {
		if len(member.Sandbox) > 63 || !dnsName.MatchString(member.Sandbox) {
			return fmt.Errorf("generated sandbox name %q is not a valid DNS label; shorten the campaign or member name", member.Sandbox)
		}
	}
	return memberPathBudget(campaign)
}

// sunPathMax is the Linux AF_UNIX sun_path limit, including its terminator.
const sunPathMax = 108

// longestInstanceSocket mirrors cs-sandbox's own state.longestSocketName: the
// longest per-instance socket basename it creates under
// <instances>/<group>/<name>/ — fwd.sock and vm.vsock, both 8 characters. Kept
// identical to upstream's on purpose, in both directions: a shorter value here
// passes plans that cs-sandbox then rejects at create, and a longer one refuses
// campaigns that would have worked.
const longestInstanceSocket = "vm.vsock"

// memberPathBudget refuses a plan whose member sockets would not fit in
// sun_path. cs-sandbox now rejects this at create too, so this is not the only
// guard — but it is the earlier one: it fails during `plan`, before any member
// exists, rather than part-way through provisioning a fleet.
//
// Only Firecracker keeps sockets in the instance directory, matching upstream's
// own scope; a podman campaign is unconstrained.
func memberPathBudget(campaign *model.Campaign) error {
	if campaign.Engine != "firecracker" {
		return nil
	}
	root := sandboxInstancesDir()
	for _, member := range campaign.Members {
		path := filepath.Join(root, campaign.Group, member.Sandbox, longestInstanceSocket)
		if len(path) < sunPathMax {
			continue
		}
		over := len(path) - sunPathMax + 1
		return fmt.Errorf("member %q would need a %d-byte socket path, %d over the %d-byte AF_UNIX limit (%s); "+
			"shorten the campaign name by %d characters, or set CS_SANDBOX_HOME to a shorter directory",
			member.Name, len(path), over, sunPathMax, path, over)
	}
	return nil
}

// sandboxInstancesDir mirrors cs-sandbox's own paths.Instances() derivation.
// Duplicated rather than imported because that package is internal to the other
// module; it is small, documented, and only feeds a pre-flight budget check
// whose worst failure is a false refusal the operator can read and act on.
func sandboxInstancesDir() string {
	if d := os.Getenv("CS_SANDBOX_INSTANCES_DIR"); d != "" {
		return d
	}
	if h := os.Getenv("CS_SANDBOX_HOME"); h != "" {
		return filepath.Join(h, "instances")
	}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".local", "share", "cs-sandbox", "instances")
		}
		if runtime.GOOS == "darwin" {
			data = filepath.Join(home, "Library", "Application Support")
		} else {
			data = filepath.Join(home, ".local", "share")
		}
	}
	return filepath.Join(data, "cs-sandbox", "instances")
}

type createOpts struct {
	profile, orchestrator, agentCLI, repo string
	agents                                []string
	count                                 int
	dry                                   bool
	sets                                  []string
	acceptUpstream                        bool
}

func (a *app) createCmd(plan bool) *cobra.Command {
	opts := new(createOpts)
	use, short := "create <name>", "Create a campaign"
	if plan {
		use, short = "plan <name>", "Print the resolved creation plan"
	}
	cmd := &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		planning := plan || opts.dry
		campaign, profile, err := a.planCampaign(*opts, args[0], planning)
		if err != nil {
			return err
		}
		// Briefs are resolved before planning returns, so `plan` and `--dry-run`
		// fail on a missing mission or role file exactly as `create` does. The
		// point of the check is to be free and early; deferring it to create
		// would mean discovering it after the upstream gate and the first VM.
		profileForInputs := opts.profile
		if profileForInputs != "" {
			profileForInputs = expandPath(profileForInputs)
		}
		inputs, err := loadCampaignInputs(profileForInputs, profile)
		if err != nil {
			return err
		}
		if planning {
			return writeJSON(c.OutOrStdout(), campaign)
		}
		if err = a.gateUpstream(c.Context(), c.OutOrStdout(), campaign, opts.acceptUpstream); err != nil {
			return err
		}
		return a.executeCreate(c.Context(), c.OutOrStdout(), campaign, &profile, inputs)
	}}
	flags := cmd.Flags()
	flags.StringVar(&opts.profile, "profile", "", "campaign profile YAML")
	flags.StringVar(&opts.orchestrator, "orchestrator", "", "orchestrator CLI")
	flags.StringSliceVar(&opts.agents, "agent", nil, "agent name=cli (repeatable)")
	flags.StringVar(&opts.agentCLI, "agent-cli", "", "CLI for homogeneous agents")
	flags.IntVar(&opts.count, "agents", 0, "number of homogeneous agents")
	flags.StringVar(&opts.repo, "repo", "", "repository cloned into every member")
	flags.BoolVar(&opts.dry, "dry-run", false, "resolve only; create nothing")
	flags.StringArrayVar(&opts.sets, "set", nil, "override a supported profile path (path=value, repeatable)")
	flags.BoolVar(&opts.acceptUpstream, "accept-upstream-change", false, "proceed despite an upstream deviation, recording it on the campaign")
	return cmd
}

// planCampaign resolves flags or a profile into a validated campaign record
// without mutating anything. Planning runs use a zero timestamp so plan
// output stays deterministic.
func (a *app) planCampaign(opts createOpts, name string, planning bool) (*model.Campaign, model.Profile, error) {
	if err := validName(name); err != nil {
		return nil, model.Profile{}, err
	}
	profile, digest, err := a.resolveProfile(opts)
	if err != nil {
		return nil, model.Profile{}, err
	}
	if err = applySets(&profile, opts.sets); err != nil {
		return nil, model.Profile{}, err
	}
	applyDefaults(&profile)
	if err = resolveRepoRefs(&profile); err != nil {
		return nil, model.Profile{}, err
	}
	now := time.Now()
	if planning {
		now = time.Time{}
	}
	profilePath := opts.profile
	if profilePath != "" {
		profilePath = expandPath(profilePath)
	}
	campaign := buildCampaign(name, profile, profilePath, digest, now)
	if err = validateCampaignMappings(campaign); err != nil {
		return nil, model.Profile{}, err
	}
	campaign.Overrides = append([]string(nil), opts.sets...)
	return campaign, profile, nil
}

// executeCreate provisions the planned campaign under the campaign lock:
// initialize host repositories, adopt a resumable prior attempt, refuse a
// colliding group, provision each member with a journal save per step, and
// finally install the orchestrator's scoped controls.
func (a *app) executeCreate(ctx context.Context, out io.Writer, campaign *model.Campaign, profile *model.Profile, inputs campaignInputs) error {
	unlock, err := a.store.Lock(campaign.Name)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	if err = initializeRepos(profile); err != nil {
		return err
	}
	if campaign, err = a.adoptResumableCreate(campaign); err != nil {
		return err
	}
	live, err := a.sandbox.list(ctx)
	if err != nil {
		return fmt.Errorf("cs-sandbox compatibility check failed (requires ls --json): %w", err)
	}
	if err = refuseForeignGroup(campaign, live); err != nil {
		return err
	}
	campaign.Provisioning = "creating"
	if err = a.store.Save(campaign); err != nil {
		return err
	}
	for i := range campaign.Members {
		if err = a.provisionMember(ctx, campaign, &campaign.Members[i], live, inputs); err != nil {
			return err
		}
	}
	// Record the group's entrance now that it exists, so `status` can print it
	// without the operator having to ask cs-sandbox separately.
	a.adoptGatewayPort(ctx, campaign)
	if err = a.sandbox.configureOrchestrator(ctx, campaign); err != nil {
		return a.failCreate(campaign, fmt.Errorf("configure orchestrator: %w", err))
	}
	// Verify the instantiation before anything is dispatched.
	if err = a.campaignDoctor(ctx, out, campaign); err != nil {
		campaign.Provisioning = "create-failed"
		campaign.UpdatedAt = time.Now().UTC()
		_ = a.store.Save(campaign)
		return err
	}
	// The readback is dispatch #1 on every node — not a pre-flight oddity
	// riding outside the protocol. It is the only check that a member read its
	// orientation and knows who it is, it proves the whole turn path on a
	// working credential, and it is answered by an ordinary reply whose
	// existence the ordinary ladder awaits. Structural, not skippable: whether
	// it ran is visible (the d001 reply exists or it does not).
	if err = a.runReadback(ctx, out, campaign); err != nil {
		campaign.Provisioning = "create-failed"
		campaign.UpdatedAt = time.Now().UTC()
		_ = a.store.Save(campaign)
		return err
	}
	// The mission is dispatch #2 to the orchestrator, held open for the
	// campaign's whole duration: the campaign is running for exactly as long
	// as this dispatch is open, and its reply is the campaign's verdict.
	var orchestrator *model.Member
	for i := range campaign.Members {
		if campaign.Members[i].Role == "orchestrator" {
			orchestrator = &campaign.Members[i]
		}
	}
	if err = a.openMission(ctx, *orchestrator, missionDispatchBody(inputs)); err != nil {
		campaign.Provisioning = "create-failed"
		campaign.UpdatedAt = time.Now().UTC()
		_ = a.store.Save(campaign)
		return err
	}
	campaign.Provisioning = ""
	campaign.UpdatedAt = time.Now().UTC()
	if err = a.store.Save(campaign); err != nil {
		return err
	}
	fmt.Fprintf(out, "campaign %s created — mission %s opened on the orchestrator (group %s)\n", campaign.Name, protocol.MissionID, campaign.Group)
	return nil
}

// missionDispatchBody is the product-authored mission dispatch: it points at
// the operator's mission text (seeded beside it) and carries the reply
// obligation and the outcome vocabulary — the one place judgment's vocabulary
// is stated where the judge will read it every campaign.
func missionDispatchBody(inputs campaignInputs) string {
	missionRef := "your input channel"
	if inputs.Mission.Name != "" {
		missionRef = "~/" + guestInputDir + "/" + inputs.Mission.Name
	}
	return fmt.Sprintf(`This dispatch is the campaign's mission, and it stays open until you reply to it.

The mission itself is stated in %s — read it with your brief and your teammates' briefs, plan, and run the campaign per your orientation: dispatch work with `+"`cs-campaign-member send`"+`, block in `+"`cs-campaign-member wait`"+` between judgments, judge every reply (fetch the branch — the reply carries measured tree-vs-base evidence), and record plan, acceptances and assessments with `+"`accept`"+` and `+"`note`"+`.

Reply ONLY when the campaign is concluded — your reply ends it. It must carry exactly one outcome:

  --outcome campaign-met        every stated criterion is satisfied (if you can name something unmet, it is not met)
  --outcome campaign-converged  criteria remain unmet and more effort from this fleet would not close them
  --outcome campaign-exhausted  budget or clock ran out while progress was still being made
  --outcome campaign-blocked    effort was never the problem — an obstacle no iteration resolves (a lost machine, a mission impossible as written)

Every outcome except campaign-met must name what remains unmet: --unmet "<item>" per item.

  cs-campaign-member reply --file <your-summary.md> --outcome <one-of-the-four> [--unmet "..."]...

Run that reply as a command of its own. Chained after anything else — a test
run, a git command, a shell that stops on the first error — it is the last
link, so ANY earlier link that fails takes the reply with it. Your turn then
ends with the campaign still open and your verdict unsent, which from the
outside cannot be told apart from a judge who never reached one. Verify first,
in as many commands as you like; then reply in one of its own.

Do not end your turn without either calling wait or replying.`, missionRef)
}

// adoptResumableCreate returns the saved record when a prior create attempt
// can be resumed, and refuses to overwrite a campaign in any other state.
func (a *app) adoptResumableCreate(campaign *model.Campaign) (*model.Campaign, error) {
	saved, err := a.store.Load(campaign.Name)
	if err != nil {
		return campaign, nil
	}
	if saved.Provisioning != "creating" && saved.Provisioning != "create-failed" {
		return nil, fmt.Errorf("campaign %q already exists", campaign.Name)
	}
	// Re-anchor the deadline to THIS attempt: the saved instant was derived
	// from the aborted attempt's start and can already be in the past by the
	// time a resume completes (seen live: an 8m deadline expired before m1
	// even opened). The derived elapsedSeconds is a duration and stays as
	// recorded; the fresh instant reaches every guest because a resumed
	// create reruns configureChannels, which rewrites member.json.
	saved.Deadline = campaign.Deadline
	return saved, nil
}

// refuseForeignGroup rejects creation when the generated group already carries
// sandboxes that are not this campaign's own members. An empty group is not a
// collision: `create --group X` brings the group up implicitly, so a resumed
// create legitimately finds its own group already there.
func refuseForeignGroup(campaign *model.Campaign, live []model.Sandbox) error {
	memberRefs := map[string]bool{}
	for _, member := range campaign.Members {
		memberRefs[member.Ref] = true
	}
	for _, sandbox := range live {
		if sandbox.Group == campaign.Group && !memberRefs[sandbox.Ref] {
			return fmt.Errorf("generated group %q already holds a foreign sandbox %q", campaign.Group, sandbox.Ref)
		}
	}
	return nil
}

// provisionMember creates or restarts one member sandbox, applies its
// authentication and channel configuration, and journals the completed step.
func (a *app) provisionMember(ctx context.Context, campaign *model.Campaign, member *model.Member, live []model.Sandbox, inputs campaignInputs) error {
	present, liveStatus := false, ""
	for _, sandbox := range live {
		if sandbox.Ref == member.Ref {
			if sandbox.Group != campaign.Group {
				return fmt.Errorf("sandbox %s belongs to group %s", sandbox.Ref, sandbox.Group)
			}
			present, liveStatus = true, sandbox.Status
			break
		}
	}
	if !present {
		if err := a.sandbox.run(ctx, createArgs(campaign, *member)...); err != nil {
			return a.failCreate(campaign, err)
		}
	} else if liveStatus != "running" {
		if err := a.sandbox.run(ctx, "start", member.Ref); err != nil {
			return a.failCreate(campaign, fmt.Errorf("restart existing member %s: %w", member.Name, err))
		}
	}
	// Reconcile branch and address from what cs-sandbox actually created.
	// Planning had to predict the branch — nothing existed yet — but from here
	// on it is read, so the manifest handed to the orchestrator, the campaign
	// record and the archive all carry what exists rather than a guess.
	if err := a.adoptMemberRecord(ctx, member); err != nil {
		return a.failCreate(campaign, err)
	}
	if err := a.sandbox.prepareAuth(ctx, *member); err != nil {
		return a.failCreate(campaign, fmt.Errorf("prepare %s authentication: %w", member.Name, err))
	}
	if err := a.sandbox.applyModelConfig(ctx, *member); err != nil {
		return a.failCreate(campaign, fmt.Errorf("apply %s model config: %w", member.Name, err))
	}
	// Recorded only once the write succeeded, so the campaign record and the
	// archive carry what this member was actually configured with rather than
	// what was asked for.
	member.Model, member.Effort = member.Profile.Model, member.Profile.Effort
	if err := a.sandbox.configureChannels(ctx, campaign, *member, inputs); err != nil {
		return a.failCreate(campaign, fmt.Errorf("configure %s channels: %w", member.Name, err))
	}
	member.SeededInputs = inputs.digests(*member)
	campaign.UpdatedAt = time.Now().UTC()
	return a.store.Save(campaign)
}

// adoptMemberRecord replaces planned values with what cs-sandbox actually
// created: the branch (spelled by a rule that lives over there) and the member's
// address on the campaign network (which the gateway needs, since it reaches
// members by address rather than by name).
//
// cs-sandbox spells one branch per sandbox rather than per repository, so the
// first entry is authoritative for all of them; a member with no repositories
// keeps its planned branch, which is unused.
func (a *app) adoptMemberRecord(ctx context.Context, member *model.Member) error {
	inst, err := a.sandbox.inspect(ctx, member.Ref)
	if err != nil {
		return fmt.Errorf("read %s record: %w", member.Name, err)
	}
	member.IP = inst.IP
	if len(member.Profile.Repos) == 0 {
		return nil
	}
	for _, repo := range inst.Repos {
		if repo.Branch != "" {
			member.Branch = repo.Branch
			return nil
		}
	}
	return nil
}

// failCreate checkpoints a failed create so a later run can resume it, then
// returns the original error.
func (a *app) failCreate(campaign *model.Campaign, err error) error {
	campaign.Provisioning = "create-failed"
	campaign.UpdatedAt = time.Now().UTC()
	_ = a.store.Save(campaign)
	return err
}

func (a *app) resolveProfile(opts createOpts) (model.Profile, string, error) {
	if opts.profile != "" {
		if opts.orchestrator != "" || len(opts.agents) > 0 || opts.agentCLI != "" || opts.count > 0 || opts.repo != "" {
			return model.Profile{}, "", errors.New("--profile is mutually exclusive with fleet flags")
		}
		return readProfile(expandPath(opts.profile))
	}
	profile, err := profileFromFlags(opts.orchestrator, opts.agents, opts.agentCLI, opts.count, opts.repo)
	return profile, "", err
}

func (a *app) validateCmd() *cobra.Command {
	var profilePath string
	cmd := &cobra.Command{Use: "validate [profile]", Short: "Validate a campaign profile without changing state", Args: cobra.MaximumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		path := profilePath
		if len(args) == 1 {
			if path != "" {
				return errors.New("pass the profile either as an argument or via --profile, not both")
			}
			path = args[0]
		}
		if path == "" {
			return errors.New("a profile path is required: positional argument or --profile")
		}
		profile, digest, err := readProfile(path)
		if err != nil {
			return err
		}
		applyDefaults(&profile)
		// Validate repositories with the same read-only resolution create
		// uses, so a profile validates if and only if it would create: real
		// refs resolve, initializable (absent/empty) paths are accepted, and
		// non-git content is rejected. Nothing on disk is modified.
		if err = resolveRepoRefs(&profile); err != nil {
			return err
		}
		// Every declared member needs a written purpose and the campaign needs a
		// mission. Checked here because this is the last point it costs nothing:
		// after create, a missing brief is a member that boots without knowing
		// what it is for, and the bill has already started.
		inputs, err := loadCampaignInputs(expandPath(path), profile)
		if err != nil {
			return err
		}
		fmt.Fprintf(c.OutOrStdout(), "valid CampaignProfile %s\n", digest[:12])
		fmt.Fprintf(c.OutOrStdout(), "mission %s, %d role briefs\n", inputs.Mission.Digest[:12], len(inputs.Roles))
		return nil
	}}
	cmd.Flags().StringVar(&profilePath, "profile", "", "campaign profile YAML (same as the positional argument)")
	return cmd
}
