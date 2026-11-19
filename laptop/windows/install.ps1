#Requires -Version 5.1
<#
install.ps1 - install devbox-paste.ps1 and bind it to Ctrl+Alt+V.

  powershell -ExecutionPolicy Bypass -File install.ps1

Copies devbox-paste.ps1 (from next to this file, else from the devbox's copy
of the repo) to %USERPROFILE%\devbox\, and adds a Start-menu shortcut
"devbox paste" with the global hotkey Ctrl+Alt+V that runs it hidden.
Run it again to update the script.
#>
param(
    [string]$HostName = 'devbox',
    [string]$Hotkey = 'Ctrl+Alt+V'
)

$ErrorActionPreference = 'Stop'

$dest = Join-Path $env:USERPROFILE 'devbox'
$script = Join-Path $dest 'devbox-paste.ps1'
New-Item -ItemType Directory -Force -Path $dest | Out-Null

$local = Join-Path $PSScriptRoot 'devbox-paste.ps1'
if (Test-Path -LiteralPath $local) {
    Copy-Item -LiteralPath $local -Destination $script -Force
} else {
    # scp reads "C:\..." as a host name, so copy into the folder by bare name.
    Push-Location $dest
    try {
        & scp -q -o BatchMode=yes "${HostName}:.devbox/src/laptop/windows/devbox-paste.ps1" 'devbox-paste.ps1'
        if ($LASTEXITCODE -ne 0) { throw "Couldn't copy devbox-paste.ps1 from $HostName (does 'ssh $HostName' work?)" }
    } finally {
        Pop-Location
    }
}

# Shortcuts in the Start menu can carry a global hotkey.
$lnkPath = Join-Path ([Environment]::GetFolderPath('Programs')) 'devbox paste.lnk'
$shell = New-Object -ComObject WScript.Shell
$lnk = $shell.CreateShortcut($lnkPath)
$lnk.TargetPath = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
$lnk.Arguments = "-NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File `"$script`" -HostName $HostName"
$lnk.WorkingDirectory = $dest
$lnk.WindowStyle = 7 # minimized, so no window flashes up
$lnk.Hotkey = $Hotkey
$lnk.Description = 'Send the clipboard screenshot to the devbox and copy its path'
$lnk.Save()

Write-Host "Installed $script"
Write-Host "Shortcut: $lnkPath ($Hotkey)"
Write-Host ''
Write-Host "Try it: Win+Shift+S, select an area, press $Hotkey, then Ctrl+V in Claude on the devbox."
Write-Host 'If the hotkey does nothing, sign out of Windows and back in once.'
