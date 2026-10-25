# Windows: asks for the photo folder (a folder dialog) and writes it to .env; called by start.bat / change-folder.bat.
# A network folder (\\nas\share or a mapped drive) becomes NAS_SHARE + the NAS login (Docker can't use \\ paths).
# Exit 1 = nothing chosen. -Path skips the dialog (tests).
param([switch]$Force, [string]$Path)
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms
$root = Split-Path -Parent $PSScriptRoot
$envFile = Join-Path $root '.env'
$utf8 = New-Object System.Text.UTF8Encoding($false)  # no BOM: docker compose would read it as part of the first name
if (-not (Test-Path $envFile)) { Copy-Item (Join-Path $root '.env.example') $envFile }
$lines = @([IO.File]::ReadAllLines($envFile, $utf8))

function Get-Value($name) {
    foreach ($l in $lines) { if ($l -match "^$name=(.*)$") { return $Matches[1].Trim().Trim("'", '"') } }
    ''
}
function Set-Value($name, $value) {
    $line = if ($value) { "$name='$value'" } else { "$name=" }  # single quotes: taken literally by docker compose
    $found = $false
    $script:lines = @(foreach ($l in $script:lines) { if ($l -match "^$name=") { $found = $true; $line } else { $l } })
    if (-not $found) { $script:lines += $line }
}
function Has-Media($dir) {
    $ext = '.jpg', '.jpeg', '.png', '.heic', '.heif', '.webp', '.tif', '.tiff', '.bmp', '.cr3', '.cr2', '.nef', '.arw', '.dng',
           '.mp4', '.mov', '.m4v', '.avi', '.mkv', '.3gp', '.mts', '.m2ts', '.wmv', '.webm'
    try {
        foreach ($f in [IO.Directory]::EnumerateFiles($dir, '*', 'AllDirectories')) {
            if ($ext -contains [IO.Path]::GetExtension($f).ToLower()) { return $true }
        }
    } catch {}  # a folder we may not read: decide by what was seen so far
    $false
}

$cur = Get-Value 'PHOTOS'
if (-not $Force -and -not $Path -and ((Get-Value 'NAS_SHARE') -or ($cur -and (Test-Path -LiteralPath $cur -PathType Container)))) {
    exit 0  # already set up
}

$owner = New-Object System.Windows.Forms.Form -Property @{ TopMost = $true }  # dialog on top of the console
$dlg = New-Object System.Windows.Forms.FolderBrowserDialog
$dlg.Description = 'Choose the folder with your photos and videos (subfolders are included)'
$dlg.ShowNewFolderButton = $false
while ($true) {
    if ($Path) { $dir = $Path }
    elseif ($dlg.ShowDialog($owner) -eq 'OK') { $dir = $dlg.SelectedPath }
    else { Write-Host 'No folder chosen.'; exit 1 }
    if ($Path -or (Has-Media $dir)) { break }
    $answer = [System.Windows.Forms.MessageBox]::Show($owner, "No photos or videos found in`n$dir`n`nUse this folder anyway?",
                                                       'Mediasorter', 'YesNo', 'Warning')
    if ($answer -eq 'Yes') { break }
}

$unc = $null
if ($dir.StartsWith('\\')) {
    $unc = $dir
} else {
    $drive = [IO.Path]::GetPathRoot($dir).TrimEnd('\')
    $disk = Get-CimInstance Win32_LogicalDisk -Filter "DeviceID='$drive'"
    if ($disk -and $disk.DriveType -eq 4) { $unc = $disk.ProviderName + $dir.Substring($drive.Length) }  # mapped drive
}

if ($unc) {
    $share = $unc.TrimEnd('\') -replace '\\', '/'
    $cred = Get-Credential -Message "Network folder $unc`nLogin and password of your NAS account"
    if (-not $cred) { Write-Host 'No login given.'; exit 1 }
    Set-Value 'NAS_SHARE' $share
    Set-Value 'NAS_USER' $cred.UserName
    Set-Value 'NAS_PASSWORD' $cred.GetNetworkCredential().Password
    Write-Host "Photos: $share (network folder)"
} else {
    Set-Value 'PHOTOS' ($dir -replace '\\', '/')
    Set-Value 'NAS_SHARE' ''
    Write-Host "Photos: $dir"
}
[IO.File]::WriteAllLines($envFile, $lines, $utf8)
