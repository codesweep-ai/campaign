# Installing cs-campaign

`cs-campaign` installs two host binaries: `cs-campaign`, which an operator drives, and
`cs-dispatch-viewer`, which renders a finished run. A third binary, `cs-campaign-member`, is
compiled into `cs-campaign` and installed into each member's Firecracker microVM at create time,
so the two ends of every channel always ship in lockstep.

At runtime `cs-campaign` needs two things it does not contain: a group-aware
[`cs-sandbox`](https://github.com/codesweep-ai/sandbox) to build the microVMs, and the
per-CLI agent tools `cs-sandbox` installs. `cs-campaign doctor` gates on both and names what is
missing, so run it first when a step here fails.

Once it works, read the [README](README.md) for what a campaign is, and [MANUAL.md](MANUAL.md) for
every command.

## 1. Install the binaries

### Download a release

From the releases page take the archive for your OS and architecture
(`cs-campaign_<version>_<os>_<arch>.tar.gz`) and `checksums.txt`, verify, then install:

```bash
sha256sum -c --ignore-missing checksums.txt      # releases are checksummed and cosign-signed
tar xzf cs-campaign_*.tar.gz cs-campaign cs-dispatch-viewer
install -m755 cs-campaign cs-dispatch-viewer ~/.local/bin/   # anywhere on your PATH
```

To verify the cosign signature as well:

```bash
cosign verify-blob checksums.txt \
  --signature checksums.txt.sig --certificate checksums.txt.pem \
  --certificate-identity-regexp 'https://github.com/codesweep-ai/campaign/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

**No version has been tagged yet, so the releases page is empty today.** This route starts working
at the first tag, which is what cuts the archives, the checksum file and the signature. Until then,
build from a clone.

### Or build from source

Needs **Go 1.27 or newer**. `goreleaser` is optional and only stamps the version.

```bash
git clone https://github.com/codesweep-ai/campaign && cd campaign
make build                          # -> bin/cs-campaign, bin/cs-dispatch-viewer
make install PREFIX="$HOME/.local"  # -> $PREFIX/bin
```

Ensure `$PREFIX/bin` is on your `PATH`. Re-run `make install` after every rebuild: the installed
file is a copy, so a stale binary silently runs a team without the code you think it has.

**`go install` is not a supported route for `cs-campaign`.** The guest binary is embedded in the
host binary, and `make build` compiles it first. A plain `go build` or `go install` embeds the
committed placeholder instead, which is enough for `validate`, `plan`, `doctor` and `ls`, and
`create` refuses it by name:

```console
$ cs-campaign create acme --profile acme/profile.yaml
Error: the embedded guest binary is the committed placeholder (112 bytes); build with `make build`,
which compiles cmd/cs-campaign-member first
```

However you got them, check what you installed:

```console
$ cs-campaign version
cs-campaign 95e65d0 (linux/amd64, go1.26.2)
$ cs-dispatch-viewer version
cs-dispatch-viewer 95e65d0 (linux/amd64, go1.26.2)
$ cs-campaign manual | less        # the full reference, carried inside the binary
```

## 2. Install and set up cs-sandbox

`cs-campaign` shells out to `cs-sandbox` for every microVM, the campaign network, the SSH
trust material and the gateway. It needs a **group-aware** build: one whose `group ls --json` works
and whose identity is `(group, name)`. Campaign isolation is a group, so there is no fallback for an
older build.

Follow [the sandbox installation guide](https://github.com/codesweep-ai/sandbox/blob/main/INSTALL.md)
in full. It covers the host packages, the Firecracker prerequisites, the rootless container setup and
the guest image build, none of which `cs-campaign` can do for you. Then confirm the two checks
`cs-campaign` depends on:

```bash
cs-sandbox group ls --json     # must succeed
cs-sandbox ls --json           # must report ref and group per row
```

Campaigns run on Linux with KVM. `cs-sandbox` decides that, not `cs-campaign`.

## 3. Install the agent tools

Each supported CLI has a family of host tools that `cs-campaign` invokes to start a turn, deliver a
prompt, read a transcript and restart a session. The three families are `claude`, `codex` and
`opencode`, and `cs-sandbox` ships and installs all of them:

```bash
cs-sandbox install-agent-tools
```

That places 21 tools on your `PATH`: for each family, `cs-<cli>`, `cs-<cli>-remote`,
`cs-<cli>-remote-forget`, `cs-<cli>-remote-output`, `cs-<cli>-remote-sessions`,
`cs-<cli>-remote-status` and `cs-<cli>-turn`. A missing one fails at create with
`required agent tool <name> not found on PATH`.

You also need the coding agents themselves, and a way for each to authenticate. A member either
inherits a host login for its family, or is granted an API key by environment-variable name. Sign in
on the host for the families whose logins you plan to inherit.

## 4. Install the cs-sandbox this build names

There is nothing to record. Every `cs-campaign` binary carries its own `go.mod`, embedded at build
time, and that manifest names the `cs-sandbox` it was built against. `doctor` and `create` compare
your `PATH` against it, and `create` refuses a surface it does not name.

Run `doctor`. If your `cs-sandbox` is a different build, it prints the exact command to install the
right one:

```console
$ cs-campaign doctor
upstream surface is not the one this cs-campaign was built against:
  cs-sandbox on PATH is v0.0.0-20260801120000-aaaaaaaaaaaa, this build was made against
  v0.0.0-20260826171442-c36e1fe91606 - install the one this build names:
  go install github.com/codesweep-ai/sandbox/cmd/cs-sandbox@v0.0.0-20260826171442-c36e1fe91606
```

Paste that command and the two agree. Moving the surface on purpose is a `go.mod` change in this
repository, followed by a rebuild and a reinstall.

## 5. Verify the installation

`cs-campaign doctor` is the fastest path through this page. Run it first, and read every line:

```console
$ cs-campaign doctor
ok  cs-sandbox version v0.0.0-20260826171442-c36e1fe91606
ok  cs-sandbox supports ls --json
ok  cs-sandbox supports sandbox groups
ok  claude remote tool family
ok  codex remote tool family
ok  opencode remote tool family
ok  not on PATH (fine - a campaign needs none of them): cs-lint cs-ledger cs-tracer
ok  cs-sandbox on PATH is the one this build names: v0.0.0-20260826171442-c36e1fe91606
ok  state directory: /home/user/.config/cs-campaign/campaigns
```

Then scaffold a campaign and check it, which allocates nothing and costs nothing:

```console
$ cs-campaign init hello --orchestrator codex --agent worker=codex
wrote hello/mission.md
wrote hello/profile.yaml
wrote hello/roles/orchestrator.md
wrote hello/roles/worker.md

Fill in the blanks, then:
  cs-campaign validate hello/profile.yaml
```

Fill in a mission, a brief per member and an `auth` block per member, then:

```console
$ cs-campaign validate hello/profile.yaml
valid CampaignProfile fef2d2336dc4
mission 31423f5a4cb7, 2 role briefs
$ cs-campaign plan hello --profile hello/profile.yaml | head -8
{
  "version": 2,
  "id": "hello-1f0c2d4a9b73e551",
  "name": "hello",
  "group": "hello-1f0c2d4a",
  "network": "cs-sandbox-hello-1f0c2d4a",
  "engine": "firecracker",
  "createdAt": "0001-01-01T00:00:00Z",
```

`plan` resolving cleanly means every name fits, every path exists and every grant is declared. The
next step, `cs-campaign create`, boots real microVMs and starts spending model tokens, so
take it when you mean to run a campaign.

Nothing above created a campaign, so there is nothing to tear down. Remove the scaffold with
`rm -rf hello`.

## Shell completion

`cs-campaign` generates its own completion script.

```bash
# bash, per user, no sudo; needs the bash-completion package
cs-campaign completion bash > ~/.local/share/bash-completion/completions/cs-campaign

# zsh
cs-campaign completion zsh > "${fpath[1]}/_cs-campaign"

# fish
cs-campaign completion fish > ~/.config/fish/completions/cs-campaign.fish

# powershell
cs-campaign completion powershell | Out-String | Invoke-Expression

# or ad hoc, for this shell only
source <(cs-campaign completion bash)
```

## Where state lives

| Path | What |
|---|---|
| `$XDG_CONFIG_HOME/cs-campaign/campaigns/` | One JSON record per campaign, plus its lock file. |
| wherever you point `--output` | Archives. Nothing is written under the config directory. |

`CS_CAMPAIGN_STATE_DIR` moves the first. Everything else lives in the sandboxes, which `cs-sandbox`
owns. The upstream surface is named by the `go.mod` inside the binary, so it needs no file and no
path of its own.

No credential is stored in any of these. A profile names environment variables and login families;
the values stay in your environment and in the members that were granted them.

## Upgrading

```bash
cd campaign && git pull
make check          # the gate: it must pass before you install
make install
cs-campaign version # must match the working tree's HEAD
```

**Never upgrade while a campaign is live.** `make install` replaces the binary underneath a running
team, and the guest binary in the members is the one that was embedded at create.

When `cs-sandbox` or an agent tool moves, the order is build, then validate, then bump the `go.mod`
pin. [CONTRIBUTING.md](CONTRIBUTING.md) has the full procedure.

## Uninstalling

```bash
cs-campaign ls                         # destroy any campaign still listed
make uninstall PREFIX="$HOME/.local"   # or rm the two binaries by hand
rm -rf ~/.config/cs-campaign           # campaign records
```

Archives live wherever you wrote them and are not touched.
