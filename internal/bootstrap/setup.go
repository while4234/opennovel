package bootstrap

import _ "embed"

// exampleConfig remains embedded for config documentation and regression
// checks. First-run configuration is now completed exclusively in the Web UI.
//
//go:embed config.example.jsonc
var exampleConfig string
