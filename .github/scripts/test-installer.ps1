# Silent install -> launch -> silent uninstall check for the NSIS installer.
# Usage: test-installer.ps1 -Installer <path> [-InstallDir <dir>]
param(
  [Parameter(Mandatory)][string]$Installer,
  [string]$InstallDir = (Join-Path ([IO.Path]::GetTempPath()) "myvnc-installer-test")
)
$ErrorActionPreference = 'Stop'
$key = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\dev.kyya.myvnc'
if (Test-Path $key) { throw "MyVNC is already installed for this user; refusing to touch it." }
if (Test-Path $InstallDir) { Remove-Item $InstallDir -Recurse -Force }

# /D must be last and unquoted.
$p = Start-Process $Installer -ArgumentList '/S', "/D=$InstallDir" -Wait -PassThru
if ($p.ExitCode -ne 0) { throw "installer exit code $($p.ExitCode)" }
$exe = Join-Path $InstallDir 'MyVNC.exe'
$uninst = Join-Path $InstallDir 'Uninstall.exe'
if (-not (Test-Path $exe)) { throw "missing $exe" }
if (-not (Test-Path $uninst)) { throw "missing $uninst" }
if (-not (Test-Path $key)) { throw "missing uninstall registry key" }
Write-Host "installed: $exe"

try {
  $app = Start-Process $exe -PassThru
  Start-Sleep -Seconds 3
  $app.Refresh()
  if ($app.HasExited) { throw "MyVNC exited early with code $($app.ExitCode)" }
  Write-Host "launched, alive after 3s (pid $($app.Id))"
} finally {
  if ($app -and -not $app.HasExited) { Stop-Process -Id $app.Id -Force; $app.WaitForExit() }
}

# _?= makes the NSIS uninstaller run in place and synchronously; it then leaves itself behind.
$u = Start-Process $uninst -ArgumentList '/S', "_?=$InstallDir" -Wait -PassThru
if ($u.ExitCode -ne 0) { throw "uninstaller exit code $($u.ExitCode)" }
if (Test-Path $exe) { throw "$exe still exists after uninstall" }
if (Test-Path $key) { throw "uninstall registry key still present" }
Remove-Item $InstallDir -Recurse -Force -ErrorAction SilentlyContinue
Write-Host "uninstalled cleanly"
