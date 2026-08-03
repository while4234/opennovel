$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

. (Join-Path $PSScriptRoot "project-go-env.ps1")

$repoRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot "..")).Path
$environment = Initialize-AINovelGoEnvironment -RepoRoot $repoRoot
$goExecutable = Resolve-AINovelGoExecutable

# go env -w refuses to persist a value that is also set in the current process.
Remove-Item Env:GOCACHE -ErrorAction SilentlyContinue
Remove-Item Env:GOTMPDIR -ErrorAction SilentlyContinue
Remove-Item Env:GOMODCACHE -ErrorAction SilentlyContinue

& $goExecutable env -w `
    "GOCACHE=$($environment.GOCACHE)" `
    "GOTMPDIR=$($environment.GOTMPDIR)" `
    "GOMODCACHE=$($environment.GOMODCACHE)"
if ($LASTEXITCODE -ne 0) {
    throw "go env -w failed with exit code $LASTEXITCODE"
}

Write-Host "AINovel Go cache configuration saved:"
& $goExecutable env GOCACHE GOTMPDIR GOMODCACHE
if ($LASTEXITCODE -ne 0) {
    throw "go env verification failed with exit code $LASTEXITCODE"
}
