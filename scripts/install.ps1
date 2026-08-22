[CmdletBinding()]
param(
    [ValidateSet("Install", "Uninstall", "Doctor")]
    [string]$Action = "Install",
    [string]$Source = ".\fleetctl.exe",
    [string]$InstallDir = "$env:LOCALAPPDATA\FleetScope\bin",
    [string]$DataDir = "$env:LOCALAPPDATA\FleetScope\data",
    [switch]$NoStart,
    [switch]$PurgeData
)

$ErrorActionPreference = "Stop"
$InstallDir = [System.IO.Path]::GetFullPath($InstallDir)
$DataDir = [System.IO.Path]::GetFullPath($DataDir)
$Target = Join-Path $InstallDir "fleetctl.exe"
$ShortcutDir = Join-Path $env:APPDATA "Microsoft\Windows\Start Menu\Programs"
$ShortcutPath = Join-Path $ShortcutDir "FleetScope.lnk"

function Test-FleetScopeHealth {
    try {
        $response = Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:8770/api/v1/health" -TimeoutSec 2
        return $response.StatusCode -eq 200
    } catch {
        return $false
    }
}

switch ($Action) {
    "Install" {
        $sourcePath = [System.IO.Path]::GetFullPath($Source)
        if (-not (Test-Path -LiteralPath $sourcePath -PathType Leaf)) {
            throw "Binary not found: $sourcePath"
        }
        New-Item -ItemType Directory -Force -Path $InstallDir, $DataDir | Out-Null
        Copy-Item -LiteralPath $sourcePath -Destination $Target -Force
        $shell = New-Object -ComObject WScript.Shell
        $shortcut = $shell.CreateShortcut($ShortcutPath)
        $shortcut.TargetPath = $Target
        $shortcut.Arguments = "up --data `"$DataDir`""
        $shortcut.WorkingDirectory = $DataDir
        $shortcut.Description = "Start FleetScope native monitoring"
        $shortcut.WindowStyle = 7
        $shortcut.Save()
        if (-not $NoStart -and -not (Test-FleetScopeHealth)) {
            Start-Process -FilePath $Target -ArgumentList @("up", "--data", "`"$DataDir`"") -WindowStyle Hidden
            Start-Sleep -Seconds 2
        }
        Write-Host "FleetScope installed: $Target"
        Write-Host "Console: http://127.0.0.1:8770/"
        Write-Host "Start menu shortcut: $ShortcutPath"
    }
    "Doctor" {
        if (-not (Test-Path -LiteralPath $Target -PathType Leaf)) {
            throw "FleetScope is not installed at $Target"
        }
        & $Target doctor --data $DataDir
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    }
    "Uninstall" {
        if (Test-Path -LiteralPath $ShortcutPath) {
            Remove-Item -LiteralPath $ShortcutPath -Force
        }
        if (Test-Path -LiteralPath $Target) {
            Remove-Item -LiteralPath $Target -Force
        }
        if (Test-Path -LiteralPath $InstallDir) {
            $resolvedInstall = [System.IO.Path]::GetFullPath($InstallDir)
            if ($resolvedInstall -ne [System.IO.Path]::GetPathRoot($resolvedInstall)) {
                Remove-Item -LiteralPath $resolvedInstall -Recurse -Force
            }
        }
        if ($PurgeData -and (Test-Path -LiteralPath $DataDir)) {
            $resolvedData = [System.IO.Path]::GetFullPath($DataDir)
            if ($resolvedData -eq [System.IO.Path]::GetPathRoot($resolvedData)) {
                throw "Refusing to purge a filesystem root"
            }
            Remove-Item -LiteralPath $resolvedData -Recurse -Force
            Write-Host "FleetScope and its data were removed."
        } else {
            Write-Host "FleetScope was removed. Data was preserved at $DataDir."
        }
    }
}
