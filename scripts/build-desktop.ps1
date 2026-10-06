# Builds the Windows desktop client and dev relay into dist\windows-<arch>\,
# including the signed wintun.dll the virtual adapter needs.
param([ValidateSet('amd64', 'arm64')][string]$Arch = 'amd64')
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'common.ps1')

$savedEnv = @{ GOOS = $env:GOOS; GOARCH = $env:GOARCH; CGO_ENABLED = $env:CGO_ENABLED }
$exitCode = 0
try {
    Set-Location (Join-Path $PSScriptRoot '..')
    $go = Find-Go
    $version = Get-BuildVersion
    $out = "dist\windows-$Arch"
    New-Item -ItemType Directory -Force $out | Out-Null

    $env:GOOS = 'windows'; $env:GOARCH = $Arch; $env:CGO_ENABLED = '0'
    foreach ($cmd in 'opentether', 'opentether-relay') {
        Write-Host "Building $cmd ($version, windows/$Arch)..."
        Invoke-Native $go @('build', '-trimpath', "-ldflags=-s -w -X main.version=$version", '-o', "$out\$cmd.exe", ".\cmd\$cmd")
    }

    if (-not (Test-Path "$out\wintun.dll")) {
        Write-Host 'Downloading Wintun...'
        [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
        $tmp = Join-Path ([IO.Path]::GetTempPath()) "opentether-wintun-$PID"
        New-Item -ItemType Directory -Force $tmp | Out-Null
        try {
            $zip = Join-Path $tmp 'wintun.zip'
            Invoke-WebRequest -UseBasicParsing -Uri 'https://www.wintun.net/builds/wintun-0.14.1.zip' -OutFile $zip
            Expand-Archive -Force $zip $tmp
            Copy-Item "$tmp\wintun\bin\$Arch\wintun.dll" $out -Force
        } finally {
            Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
        }
    }

    # libusb is only used by the experimental USB accessory mode (--experimental-aoa).
    if (-not (Test-Path "$out\libusb-1.0.dll")) {
        if ($Arch -ne 'amd64') {
            Write-Host "No prebuilt libusb for windows/$Arch; USB will use adb only." -ForegroundColor Yellow
        } else {
            Write-Host 'Downloading libusb...'
            [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
            $tmp = Join-Path ([IO.Path]::GetTempPath()) "opentether-libusb-$PID"
            New-Item -ItemType Directory -Force $tmp | Out-Null
            try {
                $archive = Join-Path $tmp 'libusb.7z'
                Invoke-WebRequest -UseBasicParsing -Uri 'https://github.com/libusb/libusb/releases/download/v1.0.30/libusb-1.0.30.7z' -OutFile $archive
                # Windows' bundled tar (libarchive) reads 7z.
                Invoke-Native "$env:SystemRoot\System32\tar.exe" @('-xf', $archive, '-C', $tmp, 'VS2019/MS64/dll/libusb-1.0.dll')
                Copy-Item "$tmp\VS2019\MS64\dll\libusb-1.0.dll" $out -Force
            } finally {
                Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
            }
        }
    }

    Write-Host ''
    Write-Host "Built $((Resolve-Path $out).Path)" -ForegroundColor Green
    Write-Host 'Run opentether.exe from an Administrator terminal, e.g.:  .\opentether.exe usb --stats'
} catch {
    Write-Host ''
    Write-Host "Build failed: $($_.Exception.Message)" -ForegroundColor Red
    $exitCode = 1
} finally {
    # Don't leave GOOS/GOARCH set in the caller's terminal.
    foreach ($k in $savedEnv.Keys) {
        if ($null -eq $savedEnv[$k]) { Remove-Item "env:$k" -ErrorAction SilentlyContinue }
        else { Set-Item "env:$k" -Value $savedEnv[$k] }
    }
}
Wait-IfLaunchedFromExplorer
exit $exitCode
