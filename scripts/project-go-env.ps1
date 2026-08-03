function Test-AINovelPathInside {
    param(
        [Parameter(Mandatory = $true)][string]$CandidatePath,
        [Parameter(Mandatory = $true)][string]$RootPath
    )

    $candidateFull = [System.IO.Path]::GetFullPath($CandidatePath).TrimEnd(
        [System.IO.Path]::DirectorySeparatorChar,
        [System.IO.Path]::AltDirectorySeparatorChar
    )
    $rootFull = [System.IO.Path]::GetFullPath($RootPath).TrimEnd(
        [System.IO.Path]::DirectorySeparatorChar,
        [System.IO.Path]::AltDirectorySeparatorChar
    )

    if ($candidateFull.Equals($rootFull, [System.StringComparison]::OrdinalIgnoreCase)) {
        return $true
    }

    return $candidateFull.StartsWith(
        $rootFull + [System.IO.Path]::DirectorySeparatorChar,
        [System.StringComparison]::OrdinalIgnoreCase
    )
}

function Initialize-AINovelGoEnvironment {
    param([Parameter(Mandatory = $true)][string]$RepoRoot)

    $resolvedRepoRoot = (Resolve-Path -LiteralPath $RepoRoot).Path
    $repoDrive = [System.IO.Path]::GetPathRoot($resolvedRepoRoot)
    if ($repoDrive.Equals("C:\", [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "AINovel build and test storage must not use the C drive: $resolvedRepoRoot"
    }

    $cacheRoot = Join-Path $resolvedRepoRoot ".cache"
    $paths = [ordered]@{
        GOCACHE = Join-Path $cacheRoot "go-build"
        GOTMPDIR = Join-Path $cacheRoot "go-tmp"
        GOMODCACHE = Join-Path $cacheRoot "go-mod"
        TEMP = Join-Path $cacheRoot "go-temp"
    }

    foreach ($path in $paths.Values) {
        [System.IO.Directory]::CreateDirectory($path) | Out-Null
    }

    $env:GOCACHE = $paths.GOCACHE
    $env:GOTMPDIR = $paths.GOTMPDIR
    $env:GOMODCACHE = $paths.GOMODCACHE
    $env:TEMP = $paths.TEMP
    $env:TMP = $paths.TEMP

    return [pscustomobject]@{
        RepoRoot = $resolvedRepoRoot
        CacheRoot = $cacheRoot
        GOCACHE = $paths.GOCACHE
        GOTMPDIR = $paths.GOTMPDIR
        GOMODCACHE = $paths.GOMODCACHE
        TEMP = $paths.TEMP
    }
}

function Resolve-AINovelGoExecutable {
    if (-not [string]::IsNullOrWhiteSpace($env:AINOVEL_GO)) {
        if (-not (Test-Path -LiteralPath $env:AINOVEL_GO -PathType Leaf)) {
            throw "AINOVEL_GO does not point to a Go executable: $env:AINOVEL_GO"
        }
        return (Resolve-Path -LiteralPath $env:AINOVEL_GO).Path
    }

    $command = Get-Command go -ErrorAction SilentlyContinue
    if ($null -ne $command) {
        return $command.Source
    }

    $bundledGo = Join-Path ([Environment]::GetFolderPath("UserProfile")) ".codex\tools\go1.25.5\go\bin\go.exe"
    if (Test-Path -LiteralPath $bundledGo -PathType Leaf) {
        return $bundledGo
    }

    throw "Go toolchain not found. Set AINOVEL_GO to the absolute path of go.exe."
}

function Clear-AINovelCacheDirectory {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$CacheRoot
    )

    if (-not (Test-AINovelPathInside -CandidatePath $Path -RootPath $CacheRoot)) {
        throw "Refusing to clean a path outside the AINovel cache root: $Path"
    }

    if (Test-Path -LiteralPath $Path -PathType Container) {
        for ($attempt = 1; $attempt -le 5; $attempt++) {
            try {
                Get-ChildItem -LiteralPath $Path -Force -ErrorAction Stop |
                    Remove-Item -Recurse -Force -ErrorAction Stop
                break
            } catch {
                if ($attempt -eq 5) {
                    throw
                }
                Start-Sleep -Milliseconds 250
            }
        }
    }
    [System.IO.Directory]::CreateDirectory($Path) | Out-Null
}

function Clear-AINovelTransientStorage {
    param(
        [Parameter(Mandatory = $true)]$Environment,
        [switch]$IncludeBuildCache,
        [switch]$IncludeModuleCache
    )

    Clear-AINovelCacheDirectory -Path $Environment.GOTMPDIR -CacheRoot $Environment.CacheRoot
    Clear-AINovelCacheDirectory -Path $Environment.TEMP -CacheRoot $Environment.CacheRoot
    if ($IncludeBuildCache) {
        Clear-AINovelCacheDirectory -Path $Environment.GOCACHE -CacheRoot $Environment.CacheRoot
    }
    if ($IncludeModuleCache) {
        Clear-AINovelCacheDirectory -Path $Environment.GOMODCACHE -CacheRoot $Environment.CacheRoot
    }
}
