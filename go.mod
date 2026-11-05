module github.com/codesweep-ai/campaign

go 1.27.3

require (
	github.com/spf13/cobra v1.10.2
	go.yaml.in/yaml/v3 v3.0.6
)

require (
	github.com/bmatcuk/doublestar/v4 v4.10.4 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/codesweep-ai/ledger v0.0.0-20261030042257-ac350b6e5e5f // indirect
	github.com/codesweep-ai/lint v0.0.0-20261028214306-6d35a8ef43e8 // indirect
	github.com/codesweep-ai/npmrevs v0.0.0-20261030043132-3f90da1ef993 // indirect
	github.com/codesweep-ai/sandbox v0.0.0-20261105223528-1c29e0a1afb9 // indirect
	github.com/codesweep-ai/tracer v0.0.0-20261102220649-8bb7a1e921c3 // indirect
	github.com/codesweep-ai/vcr v0.0.0-20261102071722-082eb23b36c0 // indirect
	github.com/docker/cli v29.9.2+incompatible // indirect
	github.com/docker/docker-credential-helpers v0.9.12 // indirect
	github.com/fatih/color v1.19.0 // indirect
	github.com/google/go-containerregistry v0.22.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/klauspost/compress v1.20.3 // indirect
	github.com/mattn/go-colorable v0.1.16 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mattn/go-runewidth v0.0.31 // indirect
	github.com/mattn/go-shellwords v1.0.16 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.1 // indirect
	github.com/rhysd/actionlint v1.7.12 // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	github.com/sirupsen/logrus v1.10.2 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	go.yaml.in/yaml/v4 v4.0.0-rc.3 // indirect
	golang.org/x/crypto/x509roots/fallback v0.0.0-20260916154351-b853e4b620cd // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/telemetry v0.0.0-20261021172232-6cd571d43fbb // indirect
	golang.org/x/tools v0.52.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

tool (
	github.com/codesweep-ai/ledger/cmd/cs-ledger
	github.com/codesweep-ai/lint/cmd/cs-lint
	github.com/codesweep-ai/npmrevs/cmd/cs-npmrevs
	github.com/codesweep-ai/sandbox/cmd/cs-sandbox
	github.com/codesweep-ai/tracer/cmd/cs-tracer
	github.com/codesweep-ai/vcr/cmd/cs-vcr
	github.com/rhysd/actionlint/cmd/actionlint
	golang.org/x/tools/cmd/deadcode
)
