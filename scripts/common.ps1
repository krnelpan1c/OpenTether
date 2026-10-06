# Helpers shared by the Windows build scripts. Dot-source this file.

# True when the script was started from Explorer ("Run with PowerShell"),
# where the window closes as soon as the script ends.
function Test-LaunchedFromExplorer {
    try {
        $parentId = (Get-CimInstance Win32_Process -Filter "ProcessId=$PID").ParentProcessId
        return (Get-Process -Id $parentId -ErrorAction Stop).ProcessName -eq 'explorer'
    } catch {
        return $false
    }
}

function Wait-IfLaunchedFromExplorer {
    if (Test-LaunchedFromExplorer) {
        Read-Host 'Press Enter to close' | Out-Null
    }
}

# Returns the path to go.exe, adding its folder to PATH for this process.
function Find-Go {
    $cmd = Get-Command go -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    foreach ($candidate in @(
        "$env:ProgramFiles\Go\bin\go.exe",
        "$env:LOCALAPPDATA\Programs\Go\bin\go.exe",
        "$env:USERPROFILE\scoop\apps\go\current\bin\go.exe"
    )) {
        if (Test-Path $candidate) {
            $env:Path = "$(Split-Path $candidate);$env:Path"
            return $candidate
        }
    }
    throw "Go is not installed (or not on PATH). Install Go 1.26 or newer from https://go.dev/dl/ or run:  winget install GoLang.Go  then open a new terminal."
}

# Runs a native command and throws if it exits non-zero. Pass the arguments
# as an array so flags like -o are not mistaken for PowerShell parameters.
function Invoke-Native {
    param([string]$Exe, [string[]]$Arguments)
    & $Exe @Arguments
    if ($LASTEXITCODE -ne 0) { throw "$(Split-Path -Leaf $Exe) $($Arguments -join ' ') failed (exit code $LASTEXITCODE)" }
}

# Best-effort version string from git; 'dev' outside a git checkout.
function Get-BuildVersion {
    if (-not (Get-Command git -ErrorAction SilentlyContinue)) { return 'dev' }
    # Native stderr must not become a terminating error under -ErrorAction Stop.
    $version = & {
        $ErrorActionPreference = 'Continue'
        git describe --tags --always 2>$null
    }
    if ($LASTEXITCODE -eq 0 -and $version) { return "$version".Trim() }
    return 'dev'
}
