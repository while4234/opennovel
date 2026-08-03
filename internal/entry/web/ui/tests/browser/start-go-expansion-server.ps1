$ErrorActionPreference = 'Stop'
$repo = Resolve-Path (Join-Path $PSScriptRoot '..\..\..\..\..\..')
. (Join-Path $repo 'scripts\project-go-env.ps1')
$goEnvironment = Initialize-AINovelGoEnvironment -RepoRoot $repo
$go = Resolve-AINovelGoExecutable
Push-Location $repo
try {
  $layout = Join-Path ([System.IO.Path]::GetTempPath()) 'ainovel-expansion-release-layout'
  New-Item -ItemType Directory -Force -Path $layout | Out-Null
  $auditor = Join-Path $layout 'expansion-auditor.exe'
  $server = Join-Path $layout 'expansion-browser-server.exe'
  & $go build -o $auditor ./cmd/expansion-auditor
  if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
  & $go build -tags acceptance -o $server ./cmd/expansion-browser-server
  if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
  Remove-Item Env:AINOVEL_EXPANSION_AUDITOR -ErrorAction SilentlyContinue
  & $server
  exit $LASTEXITCODE
} finally {
  Pop-Location
  if ($layout -and (Test-Path -LiteralPath $layout)) {
    Remove-Item -LiteralPath $layout -Recurse -Force -ErrorAction SilentlyContinue
  }
  & $go clean -cache -testcache
  if ($LASTEXITCODE -ne 0) {
    Write-Warning "go clean failed with exit code $LASTEXITCODE"
  }
  Clear-AINovelTransientStorage -Environment $goEnvironment
}
