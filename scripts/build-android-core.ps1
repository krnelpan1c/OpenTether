# Builds the Go relay core into android/app/libs/opentether-core.aar.
#
# Requires: Go, a JDK, the Android SDK (ANDROID_HOME) and NDK (ANDROID_NDK_HOME,
# or an NDK installed under $env:ANDROID_HOME\ndk).
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'common.ps1')

$exitCode = 0
try {
    Set-Location (Join-Path $PSScriptRoot '..')
    $go = Find-Go

    if (-not $env:ANDROID_HOME) { $env:ANDROID_HOME = Join-Path $env:LOCALAPPDATA 'Android\Sdk' }
    if (-not (Test-Path $env:ANDROID_HOME)) { throw "Android SDK not found at $env:ANDROID_HOME. Install Android Studio or set ANDROID_HOME." }
    if (-not $env:ANDROID_NDK_HOME) {
        $ndk = Get-ChildItem (Join-Path $env:ANDROID_HOME 'ndk') -Directory -ErrorAction SilentlyContinue |
            Sort-Object { [version]($_.Name -replace '[^0-9.].*$', '') } | Select-Object -Last 1
        if (-not $ndk) { throw 'Android NDK not found: install it from Android Studio > SDK Manager > SDK Tools > NDK, or set ANDROID_NDK_HOME.' }
        $env:ANDROID_NDK_HOME = $ndk.FullName
    }
    if (-not $env:JAVA_HOME -and (Test-Path 'C:\Program Files\Android\Android Studio\jbr')) {
        $env:JAVA_HOME = 'C:\Program Files\Android\Android Studio\jbr'
    }
    if ($env:JAVA_HOME) { $env:Path = "$env:JAVA_HOME\bin;$env:Path" }

    Write-Host 'Installing gomobile...'
    Invoke-Native $go @('install', 'golang.org/x/mobile/cmd/gomobile@latest', 'golang.org/x/mobile/cmd/gobind@latest')
    $gobin = (& $go env GOPATH).Trim() + '\bin'
    $env:Path = "$gobin;$env:Path"

    New-Item -ItemType Directory -Force android\app\libs | Out-Null
    Write-Host 'Binding the Go core (this takes a few minutes)...'
    Invoke-Native "$gobin\gomobile.exe" @(
        'bind',
        '-target=android/arm64,android/arm,android/amd64',
        '-androidapi', '29',
        '-javapkg', 'org.opentether.core',
        '-ldflags=-s -w',
        '-trimpath',
        '-o', 'android\app\libs\opentether-core.aar',
        '.\mobile'
    )
    Write-Host 'Built android\app\libs\opentether-core.aar' -ForegroundColor Green
} catch {
    Write-Host ''
    Write-Host "Build failed: $($_.Exception.Message)" -ForegroundColor Red
    $exitCode = 1
}
Wait-IfLaunchedFromExplorer
exit $exitCode
