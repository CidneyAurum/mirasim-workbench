param([string]$GoPath = '', [string]$OutputDirectory = '', [switch]$SkipTests)
$ErrorActionPreference = 'Stop'
$repoPath = Split-Path -Parent $PSScriptRoot
if (-not $OutputDirectory) { $OutputDirectory = $repoPath }
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
New-Item -ItemType Directory -Path $OutputDirectory -Force | Out-Null
if (-not $GoPath) {
    $goCommand = Get-Command go -ErrorAction SilentlyContinue
    if ($goCommand) { $GoPath = $goCommand.Source }
    else { throw 'Go 1.27.1+ is required. Pass -GoPath <go.exe>.' }
}
Push-Location $repoPath
try {
    if (-not $SkipTests) {
        & $GoPath test ./...
        if ($LASTEXITCODE -ne 0) { throw 'Upstream tests failed' }
    }
    & $GoPath build -trimpath -buildvcs=false -ldflags '-s -w' -o (Join-Path $OutputDirectory 'mirasim2api.exe') ./cmd/server
    if ($LASTEXITCODE -ne 0) { throw 'Gateway build failed' }
    Push-Location (Join-Path $repoPath 'workbench')
    try {
        if (-not $SkipTests) {
            & $GoPath test ./...
            if ($LASTEXITCODE -ne 0) { throw 'Workbench tests failed' }
        }
        & $GoPath build -trimpath -buildvcs=false -ldflags '-H windowsgui -s -w' -o (Join-Path $OutputDirectory 'mirasim-workbench.exe') .
        if ($LASTEXITCODE -ne 0) { throw 'Workbench build failed' }
    } finally { Pop-Location }
} finally { Pop-Location }
Write-Host 'Build complete. Double-click start-workbench.vbs.'
