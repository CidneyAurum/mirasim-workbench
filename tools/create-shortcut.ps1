param([string]$TargetDirectory = '', [switch]$IconOnly)
$ErrorActionPreference = 'Stop'
$repoPath = if ($TargetDirectory) { [IO.Path]::GetFullPath($TargetDirectory) } else { Split-Path -Parent $PSScriptRoot }
# Build an original geometric M icon for the desktop shortcut and native window.
Add-Type -AssemblyName System.Drawing
$bmp = New-Object System.Drawing.Bitmap 64,64
$g = [System.Drawing.Graphics]::FromImage($bmp)
$g.SmoothingMode = 'AntiAlias'
$g.Clear([System.Drawing.Color]::FromArgb(15,38,40))
$brush = New-Object System.Drawing.SolidBrush ([System.Drawing.Color]::FromArgb(100,216,193))
$g.FillEllipse($brush,3,3,58,58)
$points = [System.Drawing.PointF[]]@([System.Drawing.PointF]::new(15,45),[System.Drawing.PointF]::new(15,19),[System.Drawing.PointF]::new(22,19),[System.Drawing.PointF]::new(32,33),[System.Drawing.PointF]::new(42,19),[System.Drawing.PointF]::new(49,19),[System.Drawing.PointF]::new(49,45),[System.Drawing.PointF]::new(41,45),[System.Drawing.PointF]::new(41,32),[System.Drawing.PointF]::new(32,44),[System.Drawing.PointF]::new(23,32),[System.Drawing.PointF]::new(23,45))
$dark = New-Object System.Drawing.SolidBrush ([System.Drawing.Color]::FromArgb(15,38,40))
$g.FillPolygon($dark,$points)
$iconHandle = $bmp.GetHicon()
$icon = [System.Drawing.Icon]::FromHandle($iconHandle)
$iconPath = Join-Path $repoPath 'workbench.ico'
$stream = [System.IO.File]::Create($iconPath)
try { $icon.Save($stream) } finally { $stream.Dispose(); $icon.Dispose(); $g.Dispose(); $brush.Dispose(); $dark.Dispose(); $bmp.Dispose() }
if ($IconOnly) { Write-Host 'Icon generated.'; return }
$desktopDir = [Environment]::GetFolderPath('Desktop')
$shortcutPath = Join-Path $desktopDir 'Mirasim Workbench.lnk'
if (Test-Path -LiteralPath $shortcutPath) { Write-Host "Shortcut exists: $shortcutPath"; exit 0 }
$shell = New-Object -ComObject WScript.Shell
$shortcut = $shell.CreateShortcut($shortcutPath)
$shortcut.TargetPath = Join-Path $repoPath 'mirasim-workbench.exe'
$shortcut.WorkingDirectory = $repoPath
$shortcut.Description = 'Mirasim local model workbench'
$shortcut.IconLocation = $iconPath
$shortcut.Save()
Write-Host "Created: $shortcutPath"
