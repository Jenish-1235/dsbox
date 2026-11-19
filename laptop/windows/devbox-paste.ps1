#Requires -Version 5.1
#Requires -PSEdition Desktop
<#
devbox-paste.ps1 - send a screenshot to the devbox and copy its path, so you
can paste it into Claude Code running there (Ctrl+V in the terminal).

  devbox-paste.ps1              the image on the clipboard (e.g. Win+Shift+S)
  devbox-paste.ps1 shot.png     an image file instead (PNG, JPEG, GIF or BMP; sent as PNG)

It uploads to devbox:~/.devbox/inbox/shot-<time>.png (kept 7 days; ds inbox
lists them) and puts that file's full path on the clipboard. install.ps1 binds
it to Ctrl+Alt+V. Needs `ssh devbox` to work (Windows' OpenSSH + ~/.ssh/config).
Runs in Windows PowerShell 5.1 (powershell.exe), which has Get-Clipboard -Format Image.
#>
param(
    [string]$File,
    [string]$HostName = 'devbox'
)

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Drawing, System.Windows.Forms

# A short Windows notification; also printed when run from a console.
function Show-Note([string]$Text, [switch]$IsError) {
    Write-Host $Text
    try {
        $null = [Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime]
        $null = [Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime]
        $xml = New-Object Windows.Data.Xml.Dom.XmlDocument
        $escaped = [Security.SecurityElement]::Escape($Text)
        $xml.LoadXml("<toast><visual><binding template=`"ToastGeneric`"><text>devbox paste</text><text>$escaped</text></binding></visual></toast>")
        # Windows PowerShell's own app id, so the toast needs no registration.
        $appId = '{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe'
        [Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($appId).Show(
            [Windows.UI.Notifications.ToastNotification]::new($xml))
    } catch {
        # Older Windows or toasts blocked: a tray balloon instead.
        $icon = New-Object System.Windows.Forms.NotifyIcon
        $icon.Icon = [System.Drawing.SystemIcons]::Information
        $icon.Visible = $true
        $tipIcon = if ($IsError) { 'Error' } else { 'Info' }
        $icon.ShowBalloonTip(4000, 'devbox paste', $Text, $tipIcon)
        Start-Sleep -Seconds 4
        $icon.Dispose()
    }
}

function Stop-WithNote([string]$Text) {
    Show-Note $Text -IsError
    exit 1
}

# Runs ssh or scp and returns its exit code and output. Native stderr must not
# trip $ErrorActionPreference = 'Stop' in Windows PowerShell 5.1.
function Invoke-Ssh([string]$Exe, [string[]]$ArgList) {
    $ErrorActionPreference = 'Continue'
    $out = & $Exe @ArgList 2>&1 | ForEach-Object { "$_" }
    return @{ Code = $LASTEXITCODE; Out = (($out | Where-Object { $_ }) -join ' ').Trim() }
}

# Writes an image file to $tmp as PNG, so the upload really is the PNG its name
# says. GDI+ reads PNG, JPEG, GIF and BMP (not WebP).
function Save-AsPng([string]$Path) {
    try {
        $img = [System.Drawing.Image]::FromFile($Path)
    } catch {
        Stop-WithNote "Not a PNG, JPEG, GIF or BMP image: $Path"
    }
    try {
        $img.Save($tmp, [System.Drawing.Imaging.ImageFormat]::Png)
    } finally {
        $img.Dispose()
    }
}

$name = 'shot-{0:yyyyMMdd-HHmmss}.png' -f (Get-Date)
$tmp = Join-Path $env:TEMP $name
$noImage = 'No image on the clipboard. Take a screenshot with Win+Shift+S first.'

try {
    if ($File) {
        if (-not (Test-Path -LiteralPath $File -PathType Leaf)) { Stop-WithNote "No such file: $File" }
        Save-AsPng (Resolve-Path -LiteralPath $File).ProviderPath
    } else {
        $image = Get-Clipboard -Format Image
        if (-not $image) {
            # An image file copied in Explorer counts too.
            $files = @(Get-Clipboard -Format FileDropList)
            if ($files.Count -eq 0) { Stop-WithNote $noImage }
            $copied = $files | Where-Object { $_.Extension -match '^\.(png|jpe?g|gif|bmp)$' } | Select-Object -First 1
            if (-not $copied) { Stop-WithNote "Not a PNG, JPEG, GIF or BMP image: $($files[0].Name)" }
            Save-AsPng $copied.FullName
        } else {
            $image.Save($tmp, [System.Drawing.Imaging.ImageFormat]::Png)
            $image.Dispose()
        }
    }

    # Make the inbox and learn its full path (Claude needs an absolute path).
    $opts = @('-o', 'BatchMode=yes', '-o', 'ConnectTimeout=10')
    $r = Invoke-Ssh 'ssh' ($opts + @($HostName, 'mkdir -p ~/.devbox/inbox && cd ~/.devbox/inbox && pwd'))
    if ($r.Code -ne 0) { Stop-WithNote "Couldn't reach ${HostName}: $($r.Out)" }
    $dir = ($r.Out -split '\s+')[-1]
    if ($dir -notmatch '^/') { Stop-WithNote "Unexpected answer from ${HostName}: $($r.Out)" }

    # A bare file name, from its own folder: scp reads "C:\..." as a host name.
    Push-Location $env:TEMP
    try {
        $r = Invoke-Ssh 'scp' (@('-q') + $opts + @($name, "${HostName}:$dir/$name"))
    } finally {
        Pop-Location
    }
    if ($r.Code -ne 0) { Stop-WithNote "Upload to $HostName failed: $($r.Out)" }

    Set-Clipboard -Value "$dir/$name"
    Show-Note "Path copied: $dir/$name. Paste it into Claude."
} finally {
    Remove-Item -LiteralPath $tmp -ErrorAction SilentlyContinue
}
