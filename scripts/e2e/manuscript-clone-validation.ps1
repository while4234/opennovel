param(
    [Parameter(Mandatory = $true)]
    [string]$Config,
    [string]$Go = ""
)

$ErrorActionPreference = "Stop"
$repo = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$configPath = (Resolve-Path $Config).Path
if (-not $Go) {
    $localGo = Join-Path $repo ".codex\tools\go1.25.5\go\bin\go.exe"
    $Go = if (Test-Path $localGo) { $localGo } else { "go" }
}

& $Go run "$repo\cmd\manuscript-clone-validator" --config $configPath
if ($LASTEXITCODE -ne 0) {
    throw "manuscript clone validation preparation failed with exit code $LASTEXITCODE"
}
