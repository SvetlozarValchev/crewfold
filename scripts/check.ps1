[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
$scriptDirectory = Split-Path -Parent $MyInvocation.MyCommand.Path
$repositoryRoot = Split-Path -Parent $scriptDirectory
. (Join-Path $scriptDirectory "web-assets.ps1")

$goVersion = (Get-Content -LiteralPath (Join-Path $repositoryRoot ".go-version") -TotalCount 1).Trim()
$goCommand = Get-Command go.exe -ErrorAction SilentlyContinue
if ($null -ne $goCommand) {
    $goBinary = $goCommand.Source
} else {
    $goBinary = Join-Path $HOME ".local\share\crewfold-dev\toolchains\go$goVersion\bin\go.exe"
}
if (-not (Test-Path -LiteralPath $goBinary -PathType Leaf)) {
    throw "Go $goVersion is required but no Go executable was found"
}

$env:GOTOOLCHAIN = "local"
$env:GOPROXY = "off"
Push-Location $repositoryRoot
try {
    Write-Output "Embedded room console asset consistency"
    & (Join-Path $scriptDirectory "check-web-assets.ps1")

    $goRoot = (& $goBinary env GOROOT).Trim()
    if ($LASTEXITCODE -ne 0) { throw "go env GOROOT failed" }
    $goFormat = Join-Path $goRoot "bin\gofmt.exe"
    $goFiles = @(Get-ChildItem cmd, internal -Filter "*.go" -File -Recurse | ForEach-Object { $_.FullName })
    $unformatted = @(& $goFormat -l @goFiles)
    if ($LASTEXITCODE -ne 0) { throw "gofmt inspection failed" }
    if ($unformatted.Count -ne 0) {
        throw "The following Go files need gofmt:`n$($unformatted -join "`n")"
    }

    Write-Output "TypeScript room console"
    Invoke-CrewfoldCommand -Command "corepack" -Arguments @("pnpm", "--dir", "web", "run", "check")

    Write-Output "go vet ./..."
    Invoke-CrewfoldCommand -Command $goBinary -Arguments @("vet", "./...")
    Write-Output "go test ./..."
    Invoke-CrewfoldCommand -Command $goBinary -Arguments @("test", "./...")

    $cgoEnabled = (& $goBinary env CGO_ENABLED).Trim()
    if ($cgoEnabled -eq "1" -and $null -ne (Get-Command gcc.exe -ErrorAction SilentlyContinue)) {
        Write-Output "go test -race ./..."
        Invoke-CrewfoldCommand -Command $goBinary -Arguments @("test", "-race", "./...")
    } else {
        Write-Output "go test -race ./... skipped: race detector prerequisites unavailable"
    }

    Write-Output "production binary"
    $buildDirectory = Join-Path ([IO.Path]::GetTempPath()) ("crewfold-check-" + [Guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Path $buildDirectory | Out-Null
    try {
        Invoke-CrewfoldCommand -Command $goBinary -Arguments @("build", "-o", (Join-Path $buildDirectory "crewfold.exe"), "./cmd/crewfold")
    } finally {
        Remove-Item -LiteralPath $buildDirectory -Recurse -Force -ErrorAction SilentlyContinue
    }
} finally {
    Pop-Location
}
