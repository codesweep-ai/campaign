module github.com/codesweep-ai/campaign

go 1.27.0

require (
	github.com/spf13/cobra v1.10.2
	go.yaml.in/yaml/v3 v3.0.4
)

require (
	github.com/codesweep-ai/ledger v0.0.0-20260826154712-f3d4cf8989eb // indirect
	github.com/codesweep-ai/lint v0.0.0-20260826152054-3acef36b8e16 // indirect
	github.com/codesweep-ai/sandbox v0.0.0-20260827001716-910b73da3b6c // indirect
	github.com/codesweep-ai/tracer v0.0.0-20260826154852-c266382e4233 // indirect
	github.com/codesweep-ai/vcr v0.0.0-20260826160252-bd9e6f2b8ab6 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	golang.org/x/mod v0.40.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

tool (
	github.com/codesweep-ai/ledger/cmd/cs-ledger
	github.com/codesweep-ai/lint/cmd/cs-lint
	github.com/codesweep-ai/sandbox/cmd/cs-sandbox
	github.com/codesweep-ai/tracer/cmd/cs-tracer
	github.com/codesweep-ai/vcr/cmd/cs-vcr
)
