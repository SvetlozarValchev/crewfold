[CmdletBinding()]
param(
    [Parameter(Mandatory = $true, Position = 0)]
    [ValidateNotNullOrEmpty()]
    [string]$OutputDirectory
)

$ErrorActionPreference = "Stop"
$scriptDirectory = Split-Path -Parent $MyInvocation.MyCommand.Path
$repositoryRoot = Split-Path -Parent $scriptDirectory
$outputParentInput = Split-Path -Parent $OutputDirectory
if ([string]::IsNullOrWhiteSpace($outputParentInput)) {
    $outputParentInput = "."
}
$outputName = Split-Path -Leaf $OutputDirectory
if ([string]::IsNullOrWhiteSpace($outputName)) {
    throw "package output directory must have a name"
}
$outputParentItem = Get-Item -LiteralPath $outputParentInput
if (-not $outputParentItem.PSIsContainer -or ($outputParentItem.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
    throw "package output parent must be an existing real directory"
}
$outputParent = $outputParentItem.FullName
$outputPath = Join-Path $outputParent $outputName
if (Test-Path -LiteralPath $outputPath) {
    throw "package output directory already exists: $outputPath"
}

$stage = Join-Path $outputParent (".crewfold-package-" + [Guid]::NewGuid().ToString("N"))
$outputCreated = $false
try {
    $rootName = "crewfold-windows-amd64"
    $archiveName = "$rootName.zip"
    $packageRoot = Join-Path $stage $rootName
    New-Item -ItemType Directory -Path $packageRoot | Out-Null

    $goVersion = (Get-Content -LiteralPath (Join-Path $repositoryRoot ".go-version") -TotalCount 1).Trim()
    $goBinary = Join-Path $HOME ".local\share\crewfold-dev\toolchains\go$goVersion\bin\go.exe"
    if (-not (Test-Path -LiteralPath $goBinary -PathType Leaf)) {
        throw "pinned Go toolchain is unavailable: $goBinary"
    }

    $previousCGO = $env:CGO_ENABLED
    $previousGOOS = $env:GOOS
    $previousGOARCH = $env:GOARCH
    $previousToolchain = $env:GOTOOLCHAIN
    $previousProxy = $env:GOPROXY
    try {
        $env:CGO_ENABLED = "0"
        $env:GOOS = "windows"
        $env:GOARCH = "amd64"
        $env:GOTOOLCHAIN = "local"
        $env:GOPROXY = "off"
        Push-Location $repositoryRoot
        try {
            & $goBinary build -trimpath -buildvcs=false '-ldflags=-buildid=' -o (Join-Path $packageRoot "crewfold.exe") ./cmd/crewfold
            if ($LASTEXITCODE -ne 0) {
                throw "Go build failed with exit code $LASTEXITCODE"
            }
        } finally {
            Pop-Location
        }
    } finally {
        $env:CGO_ENABLED = $previousCGO
        $env:GOOS = $previousGOOS
        $env:GOARCH = $previousGOARCH
        $env:GOTOOLCHAIN = $previousToolchain
        $env:GOPROXY = $previousProxy
    }

    Copy-Item -LiteralPath (Join-Path $repositoryRoot "README.md") -Destination (Join-Path $packageRoot "README.md")

    $archivePath = Join-Path $stage $archiveName
    Add-Type -AssemblyName System.IO.Compression
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $archive = [IO.Compression.ZipFile]::Open($archivePath, [IO.Compression.ZipArchiveMode]::Create)
    try {
        $files = Get-ChildItem -LiteralPath $packageRoot -File -Recurse | Sort-Object FullName
        foreach ($file in $files) {
            $relative = $file.FullName.Substring($stage.Length + 1).Replace('\', '/')
            $entry = $archive.CreateEntry($relative, [IO.Compression.CompressionLevel]::Optimal)
            $entry.LastWriteTime = [DateTimeOffset]::new(1980, 1, 1, 0, 0, 0, [TimeSpan]::Zero)
            $input = $file.OpenRead()
            $output = $entry.Open()
            try {
                $input.CopyTo($output)
            } finally {
                $output.Dispose()
                $input.Dispose()
            }
        }
    } finally {
        $archive.Dispose()
    }

    $checksum = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    $checksumPath = "$archivePath.sha256"
    [IO.File]::WriteAllText($checksumPath, "$checksum  $archiveName`n", [Text.UTF8Encoding]::new($false))

    New-Item -ItemType Directory -Path $outputPath | Out-Null
    $outputCreated = $true
    Move-Item -LiteralPath $archivePath -Destination $outputPath
    Move-Item -LiteralPath $checksumPath -Destination $outputPath
    $outputCreated = $false

    Write-Output (Join-Path $outputPath $archiveName)
    Write-Output (Join-Path $outputPath "$archiveName.sha256")
} finally {
    if (Test-Path -LiteralPath $stage) {
        Remove-Item -LiteralPath $stage -Recurse -Force
    }
    if ($outputCreated -and (Test-Path -LiteralPath $outputPath)) {
        Remove-Item -LiteralPath $outputPath -Recurse -Force
    }
}
