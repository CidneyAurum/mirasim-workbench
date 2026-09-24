param(
    [string]$GoPath = 'go',
    [string]$Version = '1.0.2',
    [string[]]$PrivateMarkers = @()
)
$ErrorActionPreference = 'Stop'
$repoPath = Split-Path -Parent $PSScriptRoot
$runRoot = Join-Path $repoPath ('dist\release-' + (Get-Date -Format 'yyyyMMdd-HHmmss'))
if (Test-Path -LiteralPath $runRoot) { throw 'Use a fresh release directory.' }
$sourceRoot = Join-Path $runRoot 'source'
$binaryRoot = Join-Path $runRoot "mirasim-workbench-v$Version-windows-amd64"
$archiveRoot = Join-Path $runRoot 'archives'
foreach ($dir in @($sourceRoot,$binaryRoot,$archiveRoot)) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }

# Export an allowlist, never a recursive copy of the live installation.
$rootFiles = @('.gitignore','.dockerignore','.gitattributes','README.md','WORKBENCH.md','SECURITY.md','THIRD_PARTY_NOTICES.md','RELEASE_NOTES.md','go.mod','go.sum','start-workbench.vbs','stop-workbench.vbs')
$sourceDirs = @('cmd','internal','web','workbench','docs','deploy','tools')
$extensions = @('.go','.mod','.sum','.md','.html','.js','.mjs','.css','.svg','.ps1','.vbs','.yml','.yaml')
$sources = @($rootFiles | ForEach-Object { Get-Item -LiteralPath (Join-Path $repoPath $_) })
foreach ($dir in $sourceDirs) {
    $sources += @(Get-ChildItem -LiteralPath (Join-Path $repoPath $dir) -Recurse -File | Where-Object {
        ($_.Extension -in $extensions -or $_.Name -in @('Dockerfile','.env.example')) -and
        $_.FullName -notmatch '[\\/](data|logs|auths|backups|dist|node_modules|\.cache|\.git)[\\/]'
    })
}
foreach ($file in $sources) {
    if ($file.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Source links are not allowed in the release.' }
    $relative = $file.FullName.Substring($repoPath.Length + 1)
    $target = [IO.Path]::GetFullPath((Join-Path $sourceRoot $relative))
    if (-not $target.StartsWith($sourceRoot + [IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase)) { throw 'Source path escaped staging.' }
    New-Item -ItemType Directory -Path (Split-Path -Parent $target) -Force | Out-Null
    Copy-Item -LiteralPath $file.FullName -Destination $target
}

# Check only staged content, not account databases or private logs.
$rules = @{
    github_token = '(?:gh[pousr]_[A-Za-z0-9_]{30,}|github_pat_[A-Za-z0-9_]{30,})'
    api_key = 'sk-[A-Za-z0-9_-]{30,}'
    private_key = '-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----'
    literal_jwt = 'eyJ[A-Za-z0-9_-]{15,}\.[A-Za-z0-9_-]{15,}\.[A-Za-z0-9_-]{15,}'
    personal_path = '[A-Za-z]:[\\/]Users[\\/][A-Za-z0-9_.-]+[\\/]'
}
$findings = @()
foreach ($file in Get-ChildItem -LiteralPath $sourceRoot -Recurse -File) {
    $body = [IO.File]::ReadAllText($file.FullName)
    foreach ($rule in $rules.Keys) {
        if ([regex]::IsMatch($body,$rules[$rule])) { $findings += $file.FullName.Substring($sourceRoot.Length+1) + ': ' + $rule }
    }
    foreach ($marker in $PrivateMarkers) {
        if ($marker -and $body.IndexOf($marker,[StringComparison]::OrdinalIgnoreCase) -ge 0) { $findings += $file.FullName.Substring($sourceRoot.Length+1) + ': private marker' }
    }
}
if ($findings.Count) { throw ('Sensitive content candidates found: ' + ($findings -join ', ')) }

# Build from sanitized sources, not from the deployed binaries.
& (Join-Path $sourceRoot 'tools\build.ps1') -GoPath $GoPath -OutputDirectory $binaryRoot
if ($LASTEXITCODE -ne 0) { throw 'Build failed.' }
foreach ($name in @('start-workbench.vbs','stop-workbench.vbs','WORKBENCH.md','SECURITY.md','THIRD_PARTY_NOTICES.md','README.md')) {
    Copy-Item -LiteralPath (Join-Path $sourceRoot $name) -Destination (Join-Path $binaryRoot $name)
}
New-Item -ItemType Directory -Path (Join-Path $binaryRoot 'tools') | Out-Null
Copy-Item -LiteralPath (Join-Path $sourceRoot 'tools\create-shortcut.ps1') -Destination (Join-Path $binaryRoot 'tools\create-shortcut.ps1')
& (Join-Path $sourceRoot 'tools\create-shortcut.ps1') -TargetDirectory $binaryRoot -IconOnly

# Preserve notices supplied by each Go dependency without publishing cache paths.
$licenseRoot = Join-Path $binaryRoot 'third-party-licenses'
New-Item -ItemType Directory -Path $licenseRoot | Out-Null
$modules = @{}
foreach ($moduleRoot in @($sourceRoot,(Join-Path $sourceRoot 'workbench'))) {
    Push-Location $moduleRoot
    try {
        $rows = & $GoPath list -m -f '{{.Path}}|{{.Dir}}' all
        if ($LASTEXITCODE -ne 0) { throw 'Dependency inventory failed.' }
        foreach ($row in $rows) { $parts = $row.Split('|',2); if ($parts.Length -eq 2 -and $parts[1] -and $parts[0] -notin @('mirasim2api','mirasim-workbench')) { $modules[$parts[0]]=$parts[1] } }
    } finally { Pop-Location }
}
foreach ($name in $modules.Keys) {
    $licenses = @(Get-ChildItem -LiteralPath $modules[$name] -File | Where-Object { $_.Name -match '^(LICENSE|LICENCE|COPYING|NOTICE|PATENTS)(\..*)?$' })
    foreach ($file in $licenses) {
        $targetDir=Join-Path $licenseRoot ($name -replace '[/\\]','_')
        New-Item -ItemType Directory -Path $targetDir -Force | Out-Null
        Copy-Item -LiteralPath $file.FullName -Destination (Join-Path $targetDir $file.Name)
    }
}
$goRoot = (& $GoPath env GOROOT).Trim()
Copy-Item -LiteralPath (Join-Path $goRoot 'LICENSE') -Destination (Join-Path $licenseRoot 'Go-LICENSE')
foreach ($exe in Get-ChildItem -LiteralPath $binaryRoot -Filter '*.exe') {
    $bytes = [IO.File]::ReadAllBytes($exe.FullName)
    $ascii = [Text.Encoding]::ASCII.GetString($bytes)
    $wide = [Text.Encoding]::Unicode.GetString($bytes)
    foreach ($marker in $PrivateMarkers) {
        if ($marker -and ($ascii.IndexOf($marker,[StringComparison]::OrdinalIgnoreCase) -ge 0 -or $wide.IndexOf($marker,[StringComparison]::OrdinalIgnoreCase) -ge 0)) { throw ('Private marker in binary: '+$exe.Name) }
    }
}

$bad = @(Get-ChildItem -LiteralPath $binaryRoot -Recurse -File | Where-Object { $_.Extension -in @('.db','.dpapi','.key','.log','.pem','.pfx','.bak') -or $_.Name -eq '.env' })
if ($bad.Count) { throw 'Runtime credentials must not be packaged.' }
$windowsZip = Join-Path $archiveRoot "mirasim-workbench-v$Version-windows-amd64.zip"
$sourceZip = Join-Path $archiveRoot "mirasim-workbench-v$Version-source.zip"
Compress-Archive -LiteralPath $binaryRoot -DestinationPath $windowsZip -CompressionLevel Optimal
Add-Type -AssemblyName System.IO.Compression.FileSystem
[IO.Compression.ZipFile]::CreateFromDirectory($sourceRoot,$sourceZip,[IO.Compression.CompressionLevel]::Optimal,$false)
$sums = @($windowsZip,$sourceZip) | ForEach-Object { $hash=Get-FileHash -LiteralPath $_ -Algorithm SHA256; $hash.Hash.ToLowerInvariant()+'  '+[IO.Path]::GetFileName($_) }
[IO.File]::WriteAllLines((Join-Path $archiveRoot 'SHA256SUMS.txt'),$sums,[Text.UTF8Encoding]::new($false))
$report = [ordered]@{version=$Version;source_files=@($sources).Count;binary_files=@(Get-ChildItem -LiteralPath $binaryRoot -Recurse -File).Count;source_allowlist=$true;fresh_build=$true;trimpath=$true;vcs_metadata=$false;private_marker_findings=0;credential_pattern_findings=0;runtime_data_included=$false;archives=@([IO.Path]::GetFileName($windowsZip),[IO.Path]::GetFileName($sourceZip))}
[IO.File]::WriteAllText((Join-Path $archiveRoot 'SANITIZATION_REPORT.json'),($report | ConvertTo-Json -Depth 4),[Text.UTF8Encoding]::new($false))
Write-Host ('Release ready: '+$runRoot)
