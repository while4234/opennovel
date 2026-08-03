[CmdletBinding()]
param(
    [switch]$IncludeLegacyCDriveCache
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

. (Join-Path $PSScriptRoot "project-go-env.ps1")

$repoRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot "..")).Path
$environment = Initialize-AINovelGoEnvironment -RepoRoot $repoRoot
Clear-AINovelTransientStorage -Environment $environment -IncludeBuildCache -IncludeModuleCache
Write-Host "[ainovel-go] Project cache cleaned: $($environment.CacheRoot)"

if ($IncludeLegacyCDriveCache) {
    $localAppData = [Environment]::GetFolderPath("LocalApplicationData")
    $legacyCache = Join-Path $localAppData "go-build"
    $expectedLegacyCache = [System.IO.Path]::GetFullPath($legacyCache)
    $actualLegacyCache = [System.IO.Path]::GetFullPath((Join-Path $localAppData "go-build"))
    if (-not $actualLegacyCache.Equals($expectedLegacyCache, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "Legacy Go cache path validation failed: $actualLegacyCache"
    }

    if (Test-Path -LiteralPath $actualLegacyCache -PathType Container) {
        Remove-Item -LiteralPath $actualLegacyCache -Recurse -Force -ErrorAction Stop
        Write-Host "[ainovel-go] Legacy C-drive Go cache removed: $actualLegacyCache"
    } else {
        Write-Host "[ainovel-go] Legacy C-drive Go cache is already absent: $actualLegacyCache"
    }
}
