#requires -Version 5.1
[CmdletBinding(SupportsShouldProcess = $true)]
param(
    [Parameter(Mandatory = $true)]
    [ValidateNotNullOrEmpty()]
    [string]$BaseUrl,

    [Parameter(Mandatory = $true)]
    [ValidateNotNullOrEmpty()]
    [string]$RuntimeRoot,

    [string]$SimulationSourceDir = "D:\AINovel\novel\simulation",
    [string]$SimulationUploadPath = "/api/libraries/simulation/upload",
    [string]$SimulationListPath = "/api/libraries/simulation",
    [string]$SimulationUploadField = "files",
    [string]$RepoRoot = "",

    [switch]$Apply,
    [switch]$VerifyOnly,
    [switch]$PlanOnly,
    [switch]$SkipSimulation,
    [switch]$SkipNovels,
    [switch]$Force,

    [ValidateRange(5, 600)]
    [int]$TimeoutSec = 60
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$ExcludedSeedKeys = @("lhk")

function ConvertTo-FullPath {
    param([Parameter(Mandatory = $true)][string]$Path)
    return [System.IO.Path]::GetFullPath($Path)
}

function Test-ExcludedSeedPath {
    param([Parameter(Mandatory = $true)][string]$Path)
    $segments = (ConvertTo-FullPath $Path) -split "[\\/]+"
    foreach ($segment in $segments) {
        foreach ($excluded in $ExcludedSeedKeys) {
            if ($segment -ieq $excluded) {
                return $true
            }
        }
    }
    return $false
}

function Assert-NotExcludedSeedPath {
    param([Parameter(Mandatory = $true)][string]$Path)
    if (Test-ExcludedSeedPath $Path) {
        throw "Refusing to seed excluded source path: $Path"
    }
}

function Assert-DirectoryExists {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Description
    )
    $item = Get-Item -LiteralPath $Path -ErrorAction Stop
    if (-not $item.PSIsContainer) {
        throw "$Description is not a directory: $Path"
    }
}

function Assert-JsonFile {
    param([Parameter(Mandatory = $true)][string]$Path)
    try {
        $text = Get-Content -LiteralPath $Path -Raw -Encoding UTF8
        $null = $text | ConvertFrom-Json
    }
    catch {
        throw "Invalid JSON file '$Path': $($_.Exception.Message)"
    }
}

function Get-SimulationSeeds {
    if ($SkipSimulation) {
        return @()
    }

    Assert-DirectoryExists -Path $SimulationSourceDir -Description "Simulation source directory"
    $files = Get-ChildItem -LiteralPath $SimulationSourceDir -Filter "*.json" |
        Where-Object { -not $_.PSIsContainer } |
        Sort-Object Name
    $seeds = New-Object System.Collections.Generic.List[object]
    foreach ($file in $files) {
        if ($file.BaseName -ieq "lhk") {
            Write-Warning "Skipping excluded simulation profile: $($file.FullName)"
            continue
        }
        Assert-NotExcludedSeedPath -Path $file.FullName
        Assert-JsonFile -Path $file.FullName
        $seeds.Add([pscustomobject]@{
            Key      = $file.BaseName
            FileName = $file.Name
            FullName = $file.FullName
        })
    }
    return $seeds.ToArray()
}

function Join-ApiUrl {
    param(
        [Parameter(Mandatory = $true)][string]$Root,
        [Parameter(Mandatory = $true)][string]$Path
    )
    return $Root.TrimEnd("/") + "/" + $Path.TrimStart("/")
}

function Read-ApiErrorBody {
    param([Parameter(Mandatory = $true)][System.Management.Automation.ErrorRecord]$ErrorRecord)
    $response = $ErrorRecord.Exception.Response
    if ($null -eq $response) {
        return $ErrorRecord.Exception.Message
    }
    try {
        $stream = $response.GetResponseStream()
        if ($null -eq $stream) {
            return $ErrorRecord.Exception.Message
        }
        $reader = New-Object System.IO.StreamReader($stream)
        try {
            $body = $reader.ReadToEnd()
        }
        finally {
            $reader.Dispose()
        }
        if ([string]::IsNullOrWhiteSpace($body)) {
            return $ErrorRecord.Exception.Message
        }
        return $body
    }
    catch {
        return $ErrorRecord.Exception.Message
    }
}

function Invoke-ApiJson {
    param(
        [Parameter(Mandatory = $true)]
        [ValidateSet("GET")]
        [string]$Method,

        [Parameter(Mandatory = $true)]
        [string]$Path
    )

    $uri = Join-ApiUrl -Root $BaseUrl -Path $Path
    try {
        return Invoke-RestMethod -Uri $uri -Method $Method -TimeoutSec $TimeoutSec -Headers @{ Accept = "application/json" }
    }
    catch {
        $bodyText = Read-ApiErrorBody -ErrorRecord $_
        throw "$Method $uri failed: $bodyText"
    }
}

function Invoke-ApiMultipartUpload {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$FieldName,
        [Parameter(Mandatory = $true)][string]$FilePath
    )

    Add-Type -AssemblyName System.Net.Http
    $uri = Join-ApiUrl -Root $BaseUrl -Path $Path
    $client = New-Object System.Net.Http.HttpClient
    $client.Timeout = [TimeSpan]::FromSeconds($TimeoutSec)
    $form = New-Object System.Net.Http.MultipartFormDataContent
    $stream = [System.IO.File]::OpenRead($FilePath)

    try {
        $content = New-Object System.Net.Http.StreamContent($stream)
        $content.Headers.ContentType = [System.Net.Http.Headers.MediaTypeHeaderValue]::Parse("application/json")
        $form.Add($content, $FieldName, [System.IO.Path]::GetFileName($FilePath))
        $response = $client.PostAsync($uri, $form).GetAwaiter().GetResult()
        $responseText = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
        if (-not $response.IsSuccessStatusCode) {
            throw "POST $uri failed: $([int]$response.StatusCode) $($response.ReasonPhrase) $responseText"
        }
        if ([string]::IsNullOrWhiteSpace($responseText)) {
            return $null
        }
        return $responseText | ConvertFrom-Json
    }
    finally {
        $stream.Dispose()
        $form.Dispose()
        $client.Dispose()
    }
}

function ConvertTo-SearchableJson {
    param([object]$Value)
    if ($null -eq $Value) {
        return ""
    }
    return $Value | ConvertTo-Json -Depth 50 -Compress
}

function Test-JsonContainsAnyTerm {
    param(
        [Parameter(Mandatory = $true)][string]$Json,
        [Parameter(Mandatory = $true)][string[]]$Terms
    )
    foreach ($term in $Terms) {
        if ([string]::IsNullOrWhiteSpace($term)) {
            continue
        }
        if ($Json.IndexOf($term, [System.StringComparison]::OrdinalIgnoreCase) -ge 0) {
            return $true
        }
    }
    return $false
}

function Assert-RuntimeRootMatches {
    $runtime = Invoke-ApiJson -Method GET -Path "/api/runtime"
    $reportedRoot = [string]$runtime.runtime_root
    if ([string]::IsNullOrWhiteSpace($reportedRoot)) {
        throw "GET /api/runtime did not return runtime_root."
    }

    $expected = ConvertTo-FullPath $RuntimeRoot
    $actual = ConvertTo-FullPath $reportedRoot
    if ($expected -ine $actual) {
        throw "Runtime root mismatch. API reports '$actual', expected '$expected'."
    }
    Write-Host "Runtime root verified: $actual"
}

function Get-SimulationTerms {
    param([Parameter(Mandatory = $true)][object]$Seed)
    return @($Seed.FileName, $Seed.Key)
}

function Write-SeedPlan {
    param([object[]]$SimulationSeeds)
    Write-Host "Seed plan"
    Write-Host "  Base URL: $BaseUrl"
    Write-Host "  Runtime root: $RuntimeRoot"
    Write-Host ""
    Write-Host "Simulation profiles:"
    if ($SimulationSeeds.Count -eq 0) {
        Write-Host "  none"
    }
    foreach ($seed in $SimulationSeeds) {
        Write-Host "  - $($seed.FileName) -> $SimulationUploadPath"
    }
    Write-Host ""
    Write-Host "Novel library entries:"
    if ($SkipNovels) {
        Write-Host "  skipped"
    }
    else {
        Write-Host "  - xfk -> 大学刑法课"
        Write-Host "  - gaz -> 诡案组"
        Write-Host "  - jqmq_1 -> 娇妻美妾任君尝"
        Write-Host "  - mzdnh -> 梦中的女孩"
        Write-Host "  - nsgl -> 女神攻略"
        Write-Host "  - lhk is excluded"
    }
}

function Get-SimulationLibraryJson {
    try {
        return ConvertTo-SearchableJson (Invoke-ApiJson -Method GET -Path $SimulationListPath)
    }
    catch {
        throw "Unable to list simulation library through '$SimulationListPath'. $($_.Exception.Message)"
    }
}

function Upload-SimulationSeeds {
    param([object[]]$Seeds)
    if ($Seeds.Count -eq 0) {
        return
    }

    $existingJson = Get-SimulationLibraryJson
    foreach ($seed in $Seeds) {
        if (-not $Force -and (Test-JsonContainsAnyTerm -Json $existingJson -Terms (Get-SimulationTerms $seed))) {
            Write-Host "Simulation profile already present, skipping: $($seed.FileName)"
            continue
        }
        if ($PSCmdlet.ShouldProcess($seed.FullName, "Upload simulation profile through $SimulationUploadPath")) {
            $null = Invoke-ApiMultipartUpload -Path $SimulationUploadPath -FieldName $SimulationUploadField -FilePath $seed.FullName
            Write-Host "Uploaded simulation profile: $($seed.FileName)"
        }
    }
}

function Assert-SimulationSeedsPresent {
    param([object[]]$Seeds)
    if ($Seeds.Count -eq 0) {
        return
    }
    $json = Get-SimulationLibraryJson
    foreach ($seed in $Seeds) {
        if (-not (Test-JsonContainsAnyTerm -Json $json -Terms (Get-SimulationTerms $seed))) {
            throw "Simulation library verification failed for '$($seed.FileName)' via GET $SimulationListPath."
        }
    }
    Write-Host "Verified simulation library entries: $($Seeds.Count)"
}

function Invoke-NovelSeedCommand {
    param([switch]$Verify)
    if ($SkipNovels) {
        return
    }
    Assert-DirectoryExists -Path $RepoRoot -Description "Repository root"
    $args = @("run", ".\cmd\seed-web-libraries", "-runtime-root", $RuntimeRoot)
    if ($Verify) {
        $args += "-verify-only"
    }
    if ($Force -and -not $Verify) {
        $args += "-force"
    }
    Push-Location $RepoRoot
    try {
        & go @args
        if ($LASTEXITCODE -ne 0) {
            throw "go $($args -join ' ') failed with exit code $LASTEXITCODE"
        }
    }
    finally {
        Pop-Location
    }
}

function Assert-ValidMode {
    if ($PlanOnly -and $VerifyOnly) {
        throw "Use only one of -PlanOnly or -VerifyOnly."
    }
    if ($PlanOnly -and $Apply) {
        throw "Use only one of -PlanOnly or -Apply."
    }
    if ($VerifyOnly -and $Apply) {
        throw "Use only one of -VerifyOnly or -Apply."
    }
}

Assert-ValidMode
if ([string]::IsNullOrWhiteSpace($RepoRoot)) {
    $RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
}
$simulationSeeds = @(Get-SimulationSeeds)
Write-SeedPlan -SimulationSeeds $simulationSeeds

if ($PlanOnly) {
    Write-Host ""
    Write-Host "Plan only. No Web API or local seed calls were made."
    return
}

Write-Host ""
Assert-RuntimeRootMatches

if ($VerifyOnly) {
    Assert-SimulationSeedsPresent -Seeds $simulationSeeds
    Invoke-NovelSeedCommand -Verify
    return
}

if (-not $Apply) {
    Write-Host "Dry run. Use -Apply to seed, or -VerifyOnly to verify existing library entries."
    Assert-SimulationSeedsPresent -Seeds $simulationSeeds
    Invoke-NovelSeedCommand -Verify
    return
}

Upload-SimulationSeeds -Seeds $simulationSeeds
Invoke-NovelSeedCommand
Assert-SimulationSeedsPresent -Seeds $simulationSeeds
Invoke-NovelSeedCommand -Verify
