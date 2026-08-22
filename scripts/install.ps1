[CmdletBinding()]
param(
    [ValidateSet("Install", "Uninstall", "Doctor")]
    [string]$Action = "Install",
    [string]$Source = ".\fleetctl.exe",
    [string]$Checksums = "",
    [string]$InstallDir = "$env:LOCALAPPDATA\FleetScope\bin",
    [string]$DataDir = "$env:LOCALAPPDATA\FleetScope\data",
    [string]$Address = "127.0.0.1:8770",
    [string]$ConfirmPurge = "",
    [switch]$NoStart,
    [switch]$NoShortcut,
    [switch]$PurgeData
)

$ErrorActionPreference = "Stop"
$InstallDir = [System.IO.Path]::GetFullPath($InstallDir)
$DataDir = [System.IO.Path]::GetFullPath($DataDir)
$Target = Join-Path $InstallDir "fleetctl.exe"
$PidFile = Join-Path $DataDir "installer.pid"
$ShortcutDir = Join-Path $env:APPDATA "Microsoft\Windows\Start Menu\Programs"
$ShortcutPath = Join-Path $ShortcutDir "FleetScope.lnk"
$BaseUrl = "http://$Address"

function Test-FleetScopeHealth {
    try {
        $response = Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/api/v1/health" -TimeoutSec 2
        return $response.StatusCode -eq 200
    } catch {
        return $false
    }
}

function Stop-FleetScopeProcess {
    $ids = @()
    if (Test-Path -LiteralPath $PidFile -PathType Leaf) {
        $saved = (Get-Content -LiteralPath $PidFile -Raw).Trim()
        if ($saved -match '^\d+$') { $ids += [int]$saved }
    }
    Get-Process -Name 'fleetctl' -ErrorAction SilentlyContinue | ForEach-Object {
        try {
            if ($_.Path -and [System.IO.Path]::GetFullPath($_.Path) -eq $Target) { $ids += $_.Id }
        } catch {}
    }
    foreach ($id in ($ids | Select-Object -Unique)) {
        $process = Get-Process -Id $id -ErrorAction SilentlyContinue
        if (-not $process) { continue }
        try {
            if (-not $process.Path -or [System.IO.Path]::GetFullPath($process.Path) -ne $Target) { continue }
            Stop-Process -Id $id -Force
            Wait-Process -Id $id -Timeout 5 -ErrorAction SilentlyContinue
        } catch {}
    }
    Remove-Item -LiteralPath $PidFile -Force -ErrorAction SilentlyContinue
}

switch ($Action) {
    "Install" {
        $sourcePath = [System.IO.Path]::GetFullPath($Source)
        if (-not (Test-Path -LiteralPath $sourcePath -PathType Leaf)) {
            throw "Binary not found: $sourcePath"
        }
        if ($Checksums) {
            if (-not (Test-Path -LiteralPath $Checksums -PathType Leaf)) { throw "Checksum file not found: $Checksums" }
            $name = Split-Path -Leaf $sourcePath
            $entry = Get-Content -LiteralPath $Checksums | Where-Object { $_ -match ('^[0-9a-fA-F]{64}\s+\*?' + [regex]::Escape($name) + '$') } | Select-Object -First 1
            if (-not $entry) { throw "Checksum entry not found for $name" }
            $expected = ($entry -split '\s+')[0].ToLowerInvariant()
            $actual = (Get-FileHash -LiteralPath $sourcePath -Algorithm SHA256).Hash.ToLowerInvariant()
            if ($actual -ne $expected) { throw "SHA-256 verification failed for $sourcePath" }
        }
        New-Item -ItemType Directory -Force -Path $InstallDir, $DataDir | Out-Null
        Copy-Item -LiteralPath $sourcePath -Destination $Target -Force
        if (-not $NoShortcut) {
            $shell = New-Object -ComObject WScript.Shell
            $shortcut = $shell.CreateShortcut($ShortcutPath)
            $shortcut.TargetPath = $Target
            $shortcut.Arguments = "up --addr $Address --data `"$DataDir`""
            $shortcut.WorkingDirectory = $DataDir
            $shortcut.Description = "Start FleetScope native monitoring"
            $shortcut.WindowStyle = 7
            $shortcut.Save()
        }
        if (-not $NoStart -and -not (Test-FleetScopeHealth)) {
            $process = Start-Process -FilePath $Target -ArgumentList @("up", "--addr", $Address, "--data", "`"$DataDir`"", "--open=false") -WindowStyle Hidden -PassThru
            [System.IO.File]::WriteAllText($PidFile, "$($process.Id)`n", (New-Object System.Text.UTF8Encoding($false)))
        }
        if (-not $NoStart) {
            $ready = $false
            foreach ($attempt in 1..20) {
                if (Test-FleetScopeHealth) { $ready = $true; break }
                Start-Sleep -Milliseconds 500
            }
            if (-not $ready) {
                Stop-FleetScopeProcess
                throw "FleetScope did not become healthy at $BaseUrl"
            }
            & $Target doctor --url $BaseUrl --data $DataDir
            if ($LASTEXITCODE -ne 0) {
                Stop-FleetScopeProcess
                throw "FleetScope doctor failed with exit code $LASTEXITCODE"
            }
        }
        Write-Host "FleetScope installed: $Target"
        Write-Host "Console: $BaseUrl/"
        if (-not $NoShortcut) { Write-Host "Start menu shortcut: $ShortcutPath" }
    }
    "Doctor" {
        if (-not (Test-Path -LiteralPath $Target -PathType Leaf)) {
            throw "FleetScope is not installed at $Target"
        }
        & $Target doctor --url $BaseUrl --data $DataDir
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    }
    "Uninstall" {
        Stop-FleetScopeProcess
        if (-not $NoShortcut -and (Test-Path -LiteralPath $ShortcutPath)) {
            Remove-Item -LiteralPath $ShortcutPath -Force
        }
        if (Test-Path -LiteralPath $Target) {
            Remove-Item -LiteralPath $Target -Force
        }
        if ((Test-Path -LiteralPath $InstallDir -PathType Container) -and -not (Get-ChildItem -LiteralPath $InstallDir -Force | Select-Object -First 1)) {
            Remove-Item -LiteralPath $InstallDir -Force
        }
        if ($PurgeData -and (Test-Path -LiteralPath $DataDir)) {
            $resolvedData = [System.IO.Path]::GetFullPath($DataDir)
            if ($resolvedData -eq [System.IO.Path]::GetPathRoot($resolvedData)) {
                throw "Refusing to purge a filesystem root"
            }
            if ($ConfirmPurge -ne $resolvedData) {
                throw "Pass -ConfirmPurge with the exact data path: $resolvedData"
            }
            Remove-Item -LiteralPath $resolvedData -Recurse -Force
            Write-Host "FleetScope and its data were removed."
        } else {
            Write-Host "FleetScope was removed. Data was preserved at $DataDir."
        }
    }
}
