# Builds the desktop client and dev relay into dist\<os>-<arch>\ for every
# supported desktop platform. Windows builds also get the signed wintun.dll
# the virtual adapter needs (and libusb for the experimental accessory mode).
#
#   scripts\build-desktop.ps1                                  # all targets
#   scripts\build-desktop.ps1 -Targets windows/amd64           # just one
#   scripts\build-desktop.ps1 -Targets linux/arm64,darwin/arm64
param(
    [string[]]$Targets = @('windows/amd64', 'linux/amd64', 'linux/arm64', 'darwin/amd64', 'darwin/arm64')
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'common.ps1')

$supported = @('windows/amd64', 'windows/arm64', 'linux/amd64', 'linux/arm64', 'darwin/amd64', 'darwin/arm64')

# Downloads an archive once per run and returns the folder it was extracted to.
$downloads = @{}
function Get-Archive([string]$Url) {
    if ($downloads.ContainsKey($Url)) { return $downloads[$Url] }
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    $dir = Join-Path ([IO.Path]::GetTempPath()) "opentether-build-$PID-$($downloads.Count)"
    New-Item -ItemType Directory -Force $dir | Out-Null
    $file = Join-Path $dir ([IO.Path]::GetFileName($Url))
    Write-Host "  downloading $([IO.Path]::GetFileName($Url))..."
    Invoke-WebRequest -UseBasicParsing -Uri $Url -OutFile $file
    # Windows' bundled tar (libarchive) extracts both zip and 7z.
    Invoke-Native "$env:SystemRoot\System32\tar.exe" @('-xf', $file, '-C', $dir)
    $downloads[$Url] = $dir
    return $dir
}

$savedEnv = @{ GOOS = $env:GOOS; GOARCH = $env:GOARCH; CGO_ENABLED = $env:CGO_ENABLED }
$exitCode = 0
$built = @()
try {
    Set-Location (Join-Path $PSScriptRoot '..')
    $go = Find-Go
    $version = Get-BuildVersion

    foreach ($target in $Targets) {
        if ($supported -notcontains $target) {
            throw "unsupported target '$target' (choose from: $($supported -join ', '))"
        }
        $os, $arch = $target.Split('/')
        $out = "dist\$os-$arch"
        $ext = if ($os -eq 'windows') { '.exe' } else { '' }
        New-Item -ItemType Directory -Force $out | Out-Null

        Write-Host "Building $target ($version)..."
        $env:GOOS = $os; $env:GOARCH = $arch; $env:CGO_ENABLED = '0'
        foreach ($cmd in 'opentether', 'opentether-relay') {
            Invoke-Native $go @('build', '-trimpath', "-ldflags=-s -w -X main.version=$version", '-o', "$out\$cmd$ext", ".\cmd\$cmd")
        }

        if ($os -eq 'windows') {
            # Wintun provides the virtual adapter; its signed DLL must sit next to the exe.
            if (-not (Test-Path "$out\wintun.dll")) {
                $wintun = Get-Archive 'https://www.wintun.net/builds/wintun-0.14.1.zip'
                Copy-Item "$wintun\wintun\bin\$arch\wintun.dll" $out -Force
            }
            # libusb is only used by the experimental USB accessory mode
            # (--experimental-aoa); only an x64 build is published.
            if ($arch -eq 'amd64' -and -not (Test-Path "$out\libusb-1.0.dll")) {
                $libusb = Get-Archive 'https://github.com/libusb/libusb/releases/download/v1.0.30/libusb-1.0.30.7z'
                Copy-Item "$libusb\VS2019\MS64\dll\libusb-1.0.dll" $out -Force
            }
        }
        $built += (Resolve-Path $out).Path
    }

    Write-Host ''
    Write-Host 'Built:' -ForegroundColor Green
    $built | ForEach-Object { Write-Host "  $_" }
    Write-Host ''
    Write-Host 'Run the client as Administrator (Windows) or with sudo (Linux/macOS), e.g.:  opentether usb --stats'
    Write-Host 'macOS builds are unsigned: clear the quarantine flag with  xattr -d com.apple.quarantine opentether'
} catch {
    Write-Host ''
    Write-Host "Build failed: $($_.Exception.Message)" -ForegroundColor Red
    $exitCode = 1
} finally {
    foreach ($dir in $downloads.Values) { Remove-Item -Recurse -Force $dir -ErrorAction SilentlyContinue }
    # Don't leave GOOS/GOARCH set in the caller's terminal.
    foreach ($k in $savedEnv.Keys) {
        if ($null -eq $savedEnv[$k]) { Remove-Item "env:$k" -ErrorAction SilentlyContinue }
        else { Set-Item "env:$k" -Value $savedEnv[$k] }
    }
}
Wait-IfLaunchedFromExplorer
exit $exitCode
