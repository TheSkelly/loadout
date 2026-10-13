# Builds Loadout.exe and Loadout-Setup-<version>.exe.
#   .\build.ps1            test, build the exe and the installer
#   .\build.ps1 -NoTest    skip the tests
#   .\build.ps1 -NoInstaller
param([switch]$NoTest, [switch]$NoInstaller)
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

# tools installed after this window opened aren't on its PATH yet
$env:Path = [Environment]::GetEnvironmentVariable('Path', 'Machine') + ';' + [Environment]::GetEnvironmentVariable('Path', 'User')

function Run($exe) {
    & $exe @args
    if ($LASTEXITCODE -ne 0) { throw "$exe failed (exit $LASTEXITCODE)" }
}

$appVer = [regex]::Match((Get-Content app.go -Raw), 'const appVersion = "([^"]+)"').Groups[1].Value
$nsiVer = [regex]::Match((Get-Content installer.nsi -Raw), '!define VERSION "([^"]+)"').Groups[1].Value
if ($appVer -ne $nsiVer) { throw "Version mismatch: app.go says $appVer, installer.nsi says $nsiVer" }
Write-Host "Building Loadout $appVer"

if (-not $NoTest) {
    Write-Host "Running tests..."
    Run go test ./...
}

Write-Host "Embedding icon and manifest..."
Run go run github.com/akavel/rsrc@v0.10.2 -manifest app.manifest -ico app.ico -arch amd64 -o rsrc_windows_amd64.syso

Write-Host "Building Loadout.exe..."
Run go build -trimpath -ldflags "-s -w -H windowsgui" -o Loadout.exe .

if (-not $NoInstaller) {
    Write-Host "Building installer..."
    Run "${env:ProgramFiles(x86)}\NSIS\makensis.exe" /V2 installer.nsi
    Write-Host "Done: Loadout-Setup-$appVer.exe"
} else {
    Write-Host "Done: Loadout.exe"
}
