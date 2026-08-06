[CmdletBinding()]
param(
    [int]$Port = 9999,

    [Alias("Host")]
    [string]$BindAddress = "127.0.0.1",

    [string]$RuntimeRoot = "",

    [int[]]$StopPorts = @(),

    [switch]$NoBuild,

    [Alias("Open")]
    [switch]$OpenBrowser
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

function Write-Step {
    param([Parameter(Mandatory = $true)][string]$Message)
    Write-Host "[ainovel-web] $Message"
}

function Normalize-PathValue {
    param(
        [Parameter(Mandatory = $true)][string]$PathValue,
        [Parameter(Mandatory = $true)][string]$BasePath
    )

    $expanded = [Environment]::ExpandEnvironmentVariables($PathValue.Trim())
    if ($expanded -eq "~") {
        $expanded = $HOME
    } elseif ($expanded.StartsWith("~\") -or $expanded.StartsWith("~/")) {
        $expanded = Join-Path $HOME $expanded.Substring(2)
    }

    if (-not [System.IO.Path]::IsPathRooted($expanded)) {
        $expanded = Join-Path $BasePath $expanded
    }

    return [System.IO.Path]::GetFullPath($expanded).TrimEnd(
        [System.IO.Path]::DirectorySeparatorChar,
        [System.IO.Path]::AltDirectorySeparatorChar
    )
}

function Test-PathInside {
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

function Resolve-RepoRoot {
    $scriptDirectory = $PSScriptRoot
    if ([string]::IsNullOrWhiteSpace($scriptDirectory)) {
        $scriptDirectory = Split-Path -Parent $MyInvocation.MyCommand.Path
    }

    $repoRoot = Split-Path -Parent $scriptDirectory
    $goModPath = Join-Path $repoRoot "go.mod"
    if (-not (Test-Path -LiteralPath $goModPath -PathType Leaf)) {
        throw "Cannot locate repository root from script path: $scriptDirectory"
    }

    return (Resolve-Path -LiteralPath $repoRoot).Path
}

function Resolve-RuntimeRoot {
    param(
        [string]$RequestedRuntimeRoot = "",
        [Parameter(Mandatory = $true)][string]$RepoRoot
    )

    $candidate = $RequestedRuntimeRoot.Trim()
    if ([string]::IsNullOrWhiteSpace($candidate) -and -not [string]::IsNullOrWhiteSpace($env:AINOVEL_WEB_RUNTIME_ROOT)) {
        $candidate = $env:AINOVEL_WEB_RUNTIME_ROOT
    }
    if ([string]::IsNullOrWhiteSpace($candidate) -and -not [string]::IsNullOrWhiteSpace($env:AINOVEL_RUNTIME_ROOT)) {
        $candidate = $env:AINOVEL_RUNTIME_ROOT
    }
    if ([string]::IsNullOrWhiteSpace($candidate)) {
        $previewRoot = Join-Path $HOME ".ainovel\novels-preview"
        if (Test-Path -LiteralPath $previewRoot -PathType Container) {
            $candidate = $previewRoot
        }
    }
    if ([string]::IsNullOrWhiteSpace($candidate)) {
        return ""
    }

    $fullPath = Normalize-PathValue -PathValue $candidate -BasePath $RepoRoot
    if (Test-PathInside -CandidatePath $fullPath -RootPath $RepoRoot) {
        throw "Runtime root must stay outside the repository: $fullPath"
    }

    [System.IO.Directory]::CreateDirectory($fullPath) | Out-Null
    return (Resolve-Path -LiteralPath $fullPath).Path
}

function Require-Command {
    param([Parameter(Mandatory = $true)][string]$Name)

    $command = Get-Command $Name -ErrorAction SilentlyContinue
    if ($null -eq $command) {
        throw "Missing required command '$Name'. Install it or add it to PATH before restarting Web."
    }

    return $command.Source
}

function Invoke-Checked {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [Parameter(Mandatory = $true)][string]$WorkingDirectory
    )

    Push-Location $WorkingDirectory
    try {
        & $FilePath @Arguments
        $exitCode = $LASTEXITCODE
        if ($null -eq $exitCode) {
            $exitCode = 0
        }
        if ($exitCode -ne 0) {
            throw "'$FilePath $($Arguments -join ' ')' failed with exit code $exitCode"
        }
    } finally {
        Pop-Location
    }
}

function Get-NetstatListeningProcessIds {
    param([Parameter(Mandatory = $true)][int]$PortValue)

    $ids = @()
    $lines = & netstat -ano -p tcp 2>$null
    foreach ($line in $lines) {
        $trimmed = $line.Trim()
        if (-not $trimmed.StartsWith("TCP")) {
            continue
        }

        $parts = $trimmed -split "\s+"
        if ($parts.Count -lt 5) {
            continue
        }

        $localAddress = $parts[1]
        $state = $parts[3]
        $processIdText = $parts[4]
        if ($state -ne "LISTENING") {
            continue
        }
        if (-not ($localAddress.EndsWith(":$PortValue") -or $localAddress.EndsWith(".$PortValue"))) {
            continue
        }

        $processIdValue = 0
        if ([int]::TryParse($processIdText, [ref]$processIdValue)) {
            $ids += $processIdValue
        }
    }

    return $ids
}

function Get-ListeningProcessIds {
    param([Parameter(Mandatory = $true)][int]$PortValue)

    $ids = @()
    if ($null -ne (Get-Command Get-NetTCPConnection -ErrorAction SilentlyContinue)) {
        try {
            $ids += Get-NetTCPConnection -LocalPort $PortValue -State Listen -ErrorAction SilentlyContinue |
                Select-Object -ExpandProperty OwningProcess -Unique
        } catch {
            $ids = @()
        }
    }

    if ($ids.Count -eq 0) {
        $ids += Get-NetstatListeningProcessIds -PortValue $PortValue
    }

    return @($ids | Where-Object { $_ -gt 0 } | Sort-Object -Unique)
}

function Stop-ListeningPorts {
    param([Parameter(Mandatory = $true)][int[]]$Ports)

    foreach ($portValue in $Ports) {
        $processIds = @(Get-ListeningProcessIds -PortValue $portValue)
        if ($processIds.Count -eq 0) {
            Write-Step "No listener on port $portValue."
            continue
        }

        foreach ($processIdValue in $processIds) {
            if ($processIdValue -eq $PID) {
                throw "Refusing to stop the current PowerShell process on port $portValue."
            }

            $process = Get-Process -Id $processIdValue -ErrorAction SilentlyContinue
            if ($null -eq $process) {
                continue
            }

            Write-Step "Stopping port $portValue listener: $($process.ProcessName) pid=$processIdValue"
            Stop-Process -Id $processIdValue -Force -ErrorAction Stop
        }
    }

    foreach ($portValue in $Ports) {
        for ($attempt = 0; $attempt -lt 20; $attempt++) {
            if (@(Get-ListeningProcessIds -PortValue $portValue).Count -eq 0) {
                break
            }
            Start-Sleep -Milliseconds 250
        }

        $remaining = @(Get-ListeningProcessIds -PortValue $portValue)
        if ($remaining.Count -ne 0) {
            throw "Port $portValue is still in use by pid(s): $($remaining -join ', ')"
        }
    }
}

function Quote-ProcessArgument {
    param([Parameter(Mandatory = $true)][string]$Argument)

    if ($Argument.Length -eq 0) {
        return '""'
    }
    if ($Argument -notmatch '[\s"]') {
        return $Argument
    }

    return '"' + ($Argument -replace '"', '\"') + '"'
}

function Get-LogTail {
    param([Parameter(Mandatory = $true)][string]$Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        return ""
    }

    return ((Get-Content -LiteralPath $Path -Tail 20 -ErrorAction SilentlyContinue) -join "`n")
}

function Wait-WebReady {
    param(
        [Parameter(Mandatory = $true)][string]$BaseUrl,
        [Parameter(Mandatory = $true)]$Process,
        [Parameter(Mandatory = $true)][string]$StdoutLog,
        [Parameter(Mandatory = $true)][string]$StderrLog
    )

    $lastError = ""
    for ($attempt = 0; $attempt -lt 40; $attempt++) {
        if ($Process.HasExited) {
            $lastError = "process exited early with code $($Process.ExitCode)"
            break
        }

        try {
            return Invoke-RestMethod -Uri "$BaseUrl/api/runtime" -TimeoutSec 2
        } catch {
            $lastError = $_.Exception.Message
            Start-Sleep -Milliseconds 500
        }
    }

    $stderrTail = Get-LogTail -Path $StderrLog
    if (-not [string]::IsNullOrWhiteSpace($stderrTail)) {
        $lastError = "$lastError`nLast stderr lines:`n$stderrTail"
    }

    throw "Web did not become ready at $BaseUrl. $lastError`nLogs: $StdoutLog ; $StderrLog"
}

if ($Port -le 0 -or $Port -gt 65535) {
    throw "-Port must be between 1 and 65535."
}

$repoRoot = Resolve-RepoRoot
. (Join-Path $PSScriptRoot "project-go-env.ps1")
$goEnvironment = Initialize-AINovelGoEnvironment -RepoRoot $repoRoot
$goExecutable = Resolve-AINovelGoExecutable
$uiDir = Join-Path $repoRoot "internal\entry\web\ui"
$exePath = Join-Path $repoRoot "ainovel-cli.exe"
$tempExePath = Join-Path $repoRoot "ainovel-cli.restart.tmp.exe"
$auditorPath = Join-Path $repoRoot "expansion-auditor.exe"
$tempAuditorPath = Join-Path $repoRoot "expansion-auditor.restart.tmp.exe"
$completionAuditorPath = Join-Path $repoRoot "manuscript-completion-auditor.exe"
$tempCompletionAuditorPath = Join-Path $repoRoot "manuscript-completion-auditor.restart.tmp.exe"
$runtimeRootValue = Resolve-RuntimeRoot -RequestedRuntimeRoot $RuntimeRoot -RepoRoot $repoRoot

$stopPortValues = @()
if ($null -ne $StopPorts) {
    $stopPortValues += $StopPorts
}
$stopPortValues += $Port
$stopPortValues = @($stopPortValues | Where-Object { $_ -gt 0 -and $_ -le 65535 } | Sort-Object -Unique)
if ($stopPortValues.Count -eq 0) {
    throw "No valid ports to stop."
}

Write-Step "Repository: $repoRoot"
Write-Step "Go cache: $($goEnvironment.CacheRoot)"
if ([string]::IsNullOrWhiteSpace($runtimeRootValue)) {
    Write-Step "Runtime root: CLI default/config"
} else {
    Write-Step "Runtime root: $runtimeRootValue"
}

if ($NoBuild) {
    if (-not (Test-Path -LiteralPath $exePath -PathType Leaf)) {
        throw "Cannot use -NoBuild because executable is missing: $exePath"
    }
    if (-not (Test-Path -LiteralPath $auditorPath -PathType Leaf)) {
        throw "Cannot use -NoBuild because independent expansion auditor is missing: $auditorPath"
    }
    if (-not (Test-Path -LiteralPath $completionAuditorPath -PathType Leaf)) {
        throw "Cannot use -NoBuild because independent completion auditor is missing: $completionAuditorPath"
    }
    Write-Step "Skipping build because -NoBuild was passed."
} else {
    Require-Command "npm.cmd" | Out-Null

    if (-not (Test-Path -LiteralPath $uiDir -PathType Container)) {
        throw "Missing Web UI directory: $uiDir"
    }
    if (Test-Path -LiteralPath $tempExePath) {
        Remove-Item -LiteralPath $tempExePath -Force
    }
    if (Test-Path -LiteralPath $tempAuditorPath) {
        Remove-Item -LiteralPath $tempAuditorPath -Force
    }
    if (Test-Path -LiteralPath $tempCompletionAuditorPath) {
        Remove-Item -LiteralPath $tempCompletionAuditorPath -Force
    }

    Write-Step "Building Web UI..."
    Invoke-Checked -FilePath "npm.cmd" -Arguments @("run", "build") -WorkingDirectory $uiDir

    Write-Step "Building Go executable..."
    Invoke-Checked -FilePath $goExecutable -Arguments @("build", "-o", $tempExePath, ".\cmd\ainovel-cli") -WorkingDirectory $repoRoot
    Write-Step "Building independent expansion auditor..."
    Invoke-Checked -FilePath $goExecutable -Arguments @("build", "-o", $tempAuditorPath, ".\cmd\expansion-auditor") -WorkingDirectory $repoRoot
    Write-Step "Building independent completion auditor..."
    Invoke-Checked -FilePath $goExecutable -Arguments @("build", "-o", $tempCompletionAuditorPath, ".\cmd\manuscript-completion-auditor") -WorkingDirectory $repoRoot
}

Stop-ListeningPorts -Ports $stopPortValues

if (-not $NoBuild) {
    Write-Step "Replacing executable: $exePath"
    Move-Item -LiteralPath $tempExePath -Destination $exePath -Force
    Write-Step "Replacing independent expansion auditor: $auditorPath"
    Move-Item -LiteralPath $tempAuditorPath -Destination $auditorPath -Force
    Write-Step "Replacing independent completion auditor: $completionAuditorPath"
    Move-Item -LiteralPath $tempCompletionAuditorPath -Destination $completionAuditorPath -Force
}

$logRoot = Join-Path ([System.IO.Path]::GetTempPath()) "ainovel-cli-web"
[System.IO.Directory]::CreateDirectory($logRoot) | Out-Null
$stdoutLog = Join-Path $logRoot "web-$Port.stdout.log"
$stderrLog = Join-Path $logRoot "web-$Port.stderr.log"

$webArgs = @("web", "--host", $BindAddress, "--port", [string]$Port)
if (-not [string]::IsNullOrWhiteSpace($runtimeRootValue)) {
    $webArgs += @("--runtime-root", $runtimeRootValue)
}
if ($OpenBrowser) {
    $webArgs += "--open"
}
$argumentLine = (($webArgs | ForEach-Object { Quote-ProcessArgument $_ }) -join " ")

Write-Step "Starting Web server on $BindAddress`:$Port..."
$process = Start-Process `
    -FilePath $exePath `
    -ArgumentList $argumentLine `
    -WorkingDirectory $repoRoot `
    -WindowStyle Hidden `
    -RedirectStandardOutput $stdoutLog `
    -RedirectStandardError $stderrLog `
    -PassThru

$requestHost = $BindAddress
if ($requestHost -eq "0.0.0.0" -or $requestHost -eq "::" -or $requestHost -eq "[::]") {
    $requestHost = "127.0.0.1"
}
$baseUrl = "http://${requestHost}:$Port"
$runtimeInfo = Wait-WebReady -BaseUrl $baseUrl -Process $process -StdoutLog $stdoutLog -StderrLog $stderrLog

if (-not [string]::IsNullOrWhiteSpace($runtimeRootValue)) {
    $runtimeRootProperty = $runtimeInfo.PSObject.Properties["runtime_root"]
    if ($null -ne $runtimeRootProperty) {
        $actualRuntimeRoot = Normalize-PathValue -PathValue ([string]$runtimeRootProperty.Value) -BasePath $repoRoot
        $expectedRuntimeRoot = Normalize-PathValue -PathValue $runtimeRootValue -BasePath $repoRoot
        if (-not $actualRuntimeRoot.Equals($expectedRuntimeRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
            throw "Runtime root mismatch. Expected '$expectedRuntimeRoot', got '$actualRuntimeRoot'."
        }
    }
}

try {
    $projectsResponse = Invoke-RestMethod -Uri "$baseUrl/api/projects" -TimeoutSec 2
    $projectsProperty = $projectsResponse.PSObject.Properties["projects"]
    if ($null -ne $projectsProperty) {
        Write-Step "Projects visible: $(@($projectsProperty.Value).Count)"
    }
} catch {
    Write-Step "Projects check skipped: $($_.Exception.Message)"
}

Write-Step "Ready: $baseUrl"
Write-Step "PID: $($process.Id)"
Write-Step "Logs: $stdoutLog ; $stderrLog"
