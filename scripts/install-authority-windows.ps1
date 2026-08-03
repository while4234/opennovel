param(
    [Parameter(Mandatory = $true)]
    [string]$ServiceAccount,
    [string]$AinovelCli = "ainovel-cli.exe"
)

$ErrorActionPreference = "Stop"
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw "Run this bootstrap from an elevated Administrator shell."
}

$base = Join-Path $env:ProgramData "AINovel"
$root = Join-Path $base "publication-authority-v1"
$installation = Join-Path $base "publication-authority-installation-v1"
New-Item -ItemType Directory -Force -Path $root, $installation | Out-Null

# Protect both the anchor and its parent from service-account delete/rename.
& icacls.exe $base /inheritance:r /grant:r "SYSTEM:(OI)(CI)F" "BUILTIN\Administrators:(OI)(CI)F" "${ServiceAccount}:(RX)" | Out-Null
& icacls.exe $installation /inheritance:r /grant:r "SYSTEM:(OI)(CI)F" "BUILTIN\Administrators:(OI)(CI)F" "${ServiceAccount}:(OI)(CI)RX" | Out-Null
# The service may replace files below the writable root, but it must not hold
# DELETE/WRITE_DAC/WRITE_OWNER on the root object itself. Inherited-only Modify
# lets child files participate in atomic replace without making the trust root
# removable or re-ACLable by the runtime account.
& icacls.exe $root /inheritance:r /grant:r "SYSTEM:(OI)(CI)F" "BUILTIN\Administrators:(OI)(CI)F" "${ServiceAccount}:(RX,W,DC)" "${ServiceAccount}:(OI)(CI)(IO)M" | Out-Null

$pin = Join-Path $installation "trust-pin.json"
if (-not (Test-Path -LiteralPath $pin)) {
    & $AinovelCli authority init
    if ($LASTEXITCODE -ne 0) { throw "authority bootstrap failed with exit code $LASTEXITCODE" }
} else {
    Write-Host "Existing release-managed trust pin retained; no replacement bootstrap performed."
}

& icacls.exe $installation /inheritance:r /grant:r "SYSTEM:(OI)(CI)F" "BUILTIN\Administrators:(OI)(CI)F" "${ServiceAccount}:(OI)(CI)RX" | Out-Null
Write-Host "Authority bootstrap complete. Runtime discovery: $root and $installation"
