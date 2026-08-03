$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

. (Join-Path $PSScriptRoot "project-go-env.ps1")

if ($args.Count -eq 0) {
    Write-Host "Usage: .\go-project.cmd <go arguments>"
    Write-Host "Example: .\go-project.cmd test ./..."
    exit 2
}

$repoRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot "..")).Path
$environment = Initialize-AINovelGoEnvironment -RepoRoot $repoRoot
$goProjectTemp = Join-Path $environment.CacheRoot "go-project-temp"
[System.IO.Directory]::CreateDirectory($goProjectTemp) | Out-Null
$environment.TEMP = $goProjectTemp
$env:TEMP = $goProjectTemp
$env:TMP = $goProjectTemp
$goExecutable = Resolve-AINovelGoExecutable
$goArguments = @($args)
$isTestCommand = $goArguments.Count -gt 0 -and $goArguments[0] -eq "test"
$exitCode = 1

Write-Host "[ainovel-go] GOCACHE=$($environment.GOCACHE)"
Write-Host "[ainovel-go] GOTMPDIR=$($environment.GOTMPDIR)"
Write-Host "[ainovel-go] GOMODCACHE=$($environment.GOMODCACHE)"
Write-Host "[ainovel-go] TEMP=$($environment.TEMP)"

Push-Location $repoRoot
try {
    & $goExecutable @goArguments
    $exitCode = $LASTEXITCODE
    if ($null -eq $exitCode) {
        $exitCode = 0
    }
} finally {
    Pop-Location
    try {
        if ($isTestCommand) {
            & $goExecutable clean -cache -testcache
            if ($LASTEXITCODE -ne 0) {
                throw "go clean failed with exit code $LASTEXITCODE"
            }
        }
        Clear-AINovelTransientStorage -Environment $environment
        if ($isTestCommand) {
            Write-Host "[ainovel-go] Test build cache and transient files cleaned."
        } else {
            Write-Host "[ainovel-go] Transient files cleaned."
        }
    } catch {
        Write-Warning "AINovel cache cleanup failed: $($_.Exception.Message)"
        if ($exitCode -eq 0) {
            $exitCode = 1
        }
    }
}

exit $exitCode
