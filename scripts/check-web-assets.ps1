[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
$scriptDirectory = Split-Path -Parent $MyInvocation.MyCommand.Path
$repositoryRoot = Split-Path -Parent $scriptDirectory
. (Join-Path $scriptDirectory "web-assets.ps1")

$dist = Join-Path $repositoryRoot "web\dist"
$indexPath = Join-Path $dist "index.html"
$hashPath = Join-Path $dist ".source-sha256"
if (-not (Test-Path -LiteralPath $indexPath -PathType Leaf) -or -not (Test-Path -LiteralPath $hashPath -PathType Leaf)) {
    throw "embedded room console assets are missing; run scripts\build-web.ps1"
}

$expected = Get-CrewfoldWebSourceHash -RepositoryRoot $repositoryRoot
$actual = (Get-Content -LiteralPath $hashPath -TotalCount 1).Trim()
if ($actual -ne $expected) {
    throw "embedded room console assets are stale; run scripts\build-web.ps1"
}

$index = Get-Content -LiteralPath $indexPath -Raw
$assets = @([regex]::Matches($index, '/assets/[^"<]*') | ForEach-Object { $_.Value } | Select-Object -Unique)
if ($assets.Count -lt 2) {
    throw "embedded room console index does not reference its content-hashed assets"
}
foreach ($asset in $assets) {
    $assetPath = Join-Path $dist $asset.TrimStart('/').Replace('/', '\')
    if (-not (Test-Path -LiteralPath $assetPath -PathType Leaf)) {
        throw "embedded room console asset is missing: $asset"
    }
}

$files = @(Get-ChildItem -LiteralPath $dist -File -Recurse)
$unsafe = @(Get-ChildItem -LiteralPath $dist -Recurse -Force | Where-Object { $_.Attributes -band [IO.FileAttributes]::ReparsePoint })
if ($unsafe.Count -ne 0) {
    throw "embedded room console tree contains an unsafe reparse-point entry"
}
if (@($files | Where-Object { $_.Extension -eq ".map" }).Count -ne 0) {
    throw "embedded production room console contains source maps"
}
$totalBytes = ($files | Measure-Object -Property Length -Sum).Sum
if ($null -eq $totalBytes) {
    $totalBytes = 0
}
if ($totalBytes -gt 5MB) {
    throw "embedded room console exceeds the 5 MiB asset limit: $totalBytes bytes"
}
Write-Output "Embedded room console assets: PASS ($totalBytes bytes)"
