[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
$scriptDirectory = Split-Path -Parent $MyInvocation.MyCommand.Path
$repositoryRoot = Split-Path -Parent $scriptDirectory
. (Join-Path $scriptDirectory "web-assets.ps1")

Push-Location $repositoryRoot
try {
    Push-Location "web"
    try {
        Invoke-CrewfoldCommand -Command "corepack" -Arguments @("pnpm", "install", "--frozen-lockfile")
        Invoke-CrewfoldCommand -Command "corepack" -Arguments @("pnpm", "run", "check")
        Invoke-CrewfoldCommand -Command "corepack" -Arguments @("pnpm", "run", "build")
    } finally {
        Pop-Location
    }

    $sourceHash = Get-CrewfoldWebSourceHash -RepositoryRoot $repositoryRoot
    $hashPath = Join-Path $repositoryRoot "web\dist\.source-sha256"
    [IO.File]::WriteAllText($hashPath, "$sourceHash`n", [Text.UTF8Encoding]::new($false))
    Write-Output "Crewfold room console assets built: $sourceHash"
} finally {
    Pop-Location
}
