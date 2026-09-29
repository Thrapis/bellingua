# Builds the web UI (static export) and embeds it into bellingua.exe.
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Push-Location (Join-Path $root "web")
try {
    if (-not (Test-Path node_modules)) { npm ci }
    npm run build
    if ($LASTEXITCODE -ne 0) { throw "web build failed" }
} finally { Pop-Location }
$dist = Join-Path $root "internal/ui/dist"
Get-ChildItem $dist -Force | Where-Object Name -ne ".gitkeep" | Remove-Item -Recurse -Force
Copy-Item -Recurse -Force (Join-Path $root "web/out/*") $dist
Push-Location $root
try {
    go build -trimpath -ldflags "-s -w" -o bellingua.exe ./cmd/bellingua
    if ($LASTEXITCODE -ne 0) { throw "go build failed" }
    Write-Host "built $(Join-Path $root 'bellingua.exe')"
} finally { Pop-Location }
