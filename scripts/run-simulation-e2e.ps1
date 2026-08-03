param(
    [string]$ReportPath = ".cache/simulation-e2e/report.json"
)

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
& (Join-Path $repoRoot "go-project.cmd") run ./cmd/simulation-eval `
    -fixture (Join-Path $repoRoot "testdata/simulation-e2e") `
    -json (Join-Path $repoRoot $ReportPath)
exit $LASTEXITCODE
