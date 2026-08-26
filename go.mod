module github.com/codesweep-ai/campaign

go 1.27.0

require (
	github.com/spf13/cobra v1.10.2
	go.yaml.in/yaml/v3 v3.0.4
)

require (
	github.com/codesweep-ai/ledger v0.0.0-20260826052602-c645f1744ac6 // indirect
	github.com/codesweep-ai/lint v0.0.0-20260826044750-ad09a633ab9d // indirect
	github.com/codesweep-ai/sandbox v0.0.0-20260826054622-d2cb928d89b9 // indirect
	github.com/codesweep-ai/vcr v0.0.0-20260826054048-c8c01a821efd // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	golang.org/x/sys v0.47.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

tool (
	github.com/codesweep-ai/ledger/cmd/cs-ledger
	github.com/codesweep-ai/lint/cmd/cs-lint
	github.com/codesweep-ai/sandbox/cmd/cs-sandbox
	github.com/codesweep-ai/vcr/cmd/cs-vcr
)
