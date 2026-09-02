[CmdletBinding()]
param(
    [string]$CrewfoldBinary,
    [switch]$KeepArtifacts
)

$ErrorActionPreference = "Continue"
$previousConsoleEncoding = [Console]::OutputEncoding
$previousPipelineEncoding = $OutputEncoding
$utf8Encoding = [Text.UTF8Encoding]::new($false)
[Console]::OutputEncoding = $utf8Encoding
$OutputEncoding = $utf8Encoding
if (-not $IsWindows -and $env:OS -ne "Windows_NT") {
    throw "this integration test requires native Windows"
}

$scriptDirectory = Split-Path -Parent $MyInvocation.MyCommand.Path
$repositoryRoot = Split-Path -Parent $scriptDirectory
$testRoot = Join-Path $env:TEMP ("crewfold-windows-workflow-" + [Guid]::NewGuid().ToString("N"))
$dataDirectory = Join-Path $testRoot "state"
$firstDirectory = Join-Path $testRoot "participant alpha"
$unicodeDirectorySuffix = -join @([char]0x30E6, [char]0x30CB, [char]0x30B3, [char]0x30FC, [char]0x30C9)
$secondDirectory = Join-Path $testRoot ("participant-beta-" + $unicodeDirectorySuffix)
$pipeName = "\\.\pipe\crewfold-workflow-" + [Guid]::NewGuid().ToString("N")
$daemon = $null
$previousSocket = $env:CREWFOLD_SOCKET

function Invoke-Crewfold {
    param(
        [Parameter(Mandatory = $true)][string]$WorkingDirectory,
        [Parameter(Mandatory = $true)][string[]]$Arguments
    )
    Push-Location $WorkingDirectory
    try {
        $output = @(& $script:CrewfoldBinary @Arguments 2>&1)
        if ($LASTEXITCODE -ne 0) {
            throw "crewfold $($Arguments -join ' ') failed:`n$($output -join "`n")"
        }
        return $output -join "`n"
    } finally {
        Pop-Location
    }
}

function Invoke-CrewfoldJson {
    param(
        [Parameter(Mandatory = $true)][string]$WorkingDirectory,
        [Parameter(Mandatory = $true)][string[]]$Arguments
    )
    $allArguments = @($Arguments) + @("--output", "json")
    $text = Invoke-Crewfold -WorkingDirectory $WorkingDirectory -Arguments $allArguments
    return $text | ConvertFrom-Json
}

function Start-IsolatedDaemon {
    $stdout = Join-Path $script:testRoot ("daemon-" + [Guid]::NewGuid().ToString("N") + ".stdout.log")
    $stderr = Join-Path $script:testRoot ("daemon-" + [Guid]::NewGuid().ToString("N") + ".stderr.log")
    $script:daemon = Start-Process -FilePath $script:CrewfoldBinary -ArgumentList @(
        "daemon", "run", "--data-dir", $script:dataDirectory, "--socket", $script:pipeName,
        "--web-address", "127.0.0.1:0"
    ) -RedirectStandardOutput $stdout -RedirectStandardError $stderr -PassThru -ErrorAction Stop
    foreach ($attempt in 1..80) {
        $ignored = @(& $script:CrewfoldBinary status 2>$null)
        if ($LASTEXITCODE -eq 0) { return }
        if ($script:daemon.HasExited) {
            throw "isolated daemon exited during startup:`n$(Get-Content -LiteralPath $stderr -Raw -ErrorAction SilentlyContinue)"
        }
        Start-Sleep -Milliseconds 125
    }
    throw "isolated daemon did not become ready"
}

function Stop-IsolatedDaemon {
    if ($null -eq $script:daemon) { return }
    if (-not $script:daemon.HasExited) {
        Invoke-Crewfold -WorkingDirectory $script:testRoot -Arguments @("daemon", "shutdown") | Out-Null
        if (-not $script:daemon.WaitForExit(10000)) {
            Stop-Process -Id $script:daemon.Id -Force -ErrorAction SilentlyContinue
            throw "isolated daemon did not shut down gracefully"
        }
    }
    $script:daemon = $null
}

try {
    New-Item -ItemType Directory -Path $testRoot, $firstDirectory, $secondDirectory -ErrorAction Stop | Out-Null
    if ([string]::IsNullOrWhiteSpace($CrewfoldBinary)) {
        $CrewfoldBinary = Join-Path $testRoot "crewfold.exe"
        $goCommand = Get-Command go.exe -ErrorAction SilentlyContinue
        if ($null -ne $goCommand) {
            $goBinary = $goCommand.Source
        } else {
            $goVersion = (Get-Content -LiteralPath (Join-Path $repositoryRoot ".go-version") -TotalCount 1).Trim()
            $goBinary = Join-Path $HOME ".local\share\crewfold-dev\toolchains\go$goVersion\bin\go.exe"
        }
        if (-not (Test-Path -LiteralPath $goBinary -PathType Leaf)) { throw "go.exe is required when -CrewfoldBinary is not provided" }
        Push-Location $repositoryRoot
        try {
            & $goBinary build -trimpath -o $CrewfoldBinary ./cmd/crewfold
            if ($LASTEXITCODE -ne 0) { throw "Crewfold build failed" }
        } finally {
            Pop-Location
        }
    } else {
        $CrewfoldBinary = (Resolve-Path -LiteralPath $CrewfoldBinary -ErrorAction Stop).Path
    }
    $script:CrewfoldBinary = $CrewfoldBinary
    $env:CREWFOLD_SOCKET = $pipeName

    Start-IsolatedDaemon
    $created = Invoke-CrewfoldJson $testRoot @("room", "create", "windows-workflow", "--title", "Windows workflow", "--topic", "Disposable native integration test")
    if ($created.room.slug -ne "windows-workflow") { throw "room creation returned the wrong room" }

    $alpha = Invoke-CrewfoldJson $firstDirectory @("room", "join", "windows-workflow", "--handle", "alpha", "--delivery", "none")
    $beta = Invoke-CrewfoldJson $secondDirectory @("room", "join", "windows-workflow", "--handle", "beta", "--delivery", "none")
    if ($alpha.handle -ne "alpha" -or $beta.handle -ne "beta") { throw "participants did not join" }

    Invoke-CrewfoldJson $firstDirectory @("room", "send", "windows-workflow", "Message from alpha & 100% complete") | Out-Null
    Invoke-CrewfoldJson $secondDirectory @("room", "context", "windows-workflow", "Reviewing Unicode and persistence") | Out-Null
    Invoke-CrewfoldJson $secondDirectory @("room", "send", "windows-workflow", "@alpha acknowledgement from beta") | Out-Null

    $documentName = "r" + [char]0x00E9 + "sum" + [char]0x00E9 + " & findings.txt"
    $documentPath = Join-Path $secondDirectory $documentName
    $documentContent = "Crewfold Windows document round-trip " + [char]0x2713 + "`n"
    [IO.File]::WriteAllText($documentPath, $documentContent, [Text.UTF8Encoding]::new($false))
    $uploaded = Invoke-CrewfoldJson $secondDirectory @("room", "upload", "windows-workflow", $documentPath, "--caption", "Unicode document")
    if ($uploaded.document.name -ne $documentName) { throw "document upload returned the wrong name" }

    $snapshot = Invoke-CrewfoldJson $firstDirectory @("room", "read", "windows-workflow", "--after", "0")
    if ($snapshot.participants.Count -ne 2 -or $snapshot.documents.Count -ne 1) { throw "canonical snapshot is incomplete" }
    if (-not ($snapshot.messages | Where-Object { $_.body -eq "@alpha acknowledgement from beta" })) { throw "room message was not preserved" }
    $through = [Int64]$snapshot.room.last_sequence
    Invoke-CrewfoldJson $firstDirectory @("room", "ack", "windows-workflow", "--through", $through.ToString()) | Out-Null

    $downloadPath = Join-Path $firstDirectory ("downloaded " + [char]0x00E9 + ".txt")
    Invoke-Crewfold $firstDirectory @("room", "document", "windows-workflow", $documentName, "--to", $downloadPath) | Out-Null
    if ([IO.File]::ReadAllText($downloadPath) -ne $documentContent) { throw "document round-trip changed content" }

    Stop-IsolatedDaemon
    Start-IsolatedDaemon
    $persisted = Invoke-CrewfoldJson $testRoot @("room", "show", "windows-workflow")
    if ($persisted.room.last_sequence -ne $through -or $persisted.documents.Count -ne 1 -or $persisted.participants.Count -ne 2) {
        throw "room state did not survive daemon restart"
    }
    $alphaAfterRestart = $persisted.participants | Where-Object { $_.handle -eq "alpha" } | Select-Object -First 1
    if ($alphaAfterRestart.last_read_sequence -ne $through) { throw "acknowledgement cursor did not survive restart" }

    $archived = Invoke-CrewfoldJson $testRoot @("room", "archive", "windows-workflow")
    if ($archived.status -ne "archived") { throw "room was not archived" }
    Write-Output "Disposable native Windows workflow: PASS"
    Write-Output "room messages: $($persisted.messages.Count); documents: $($persisted.documents.Count); cursor: $through"
} finally {
    try { Stop-IsolatedDaemon } catch { Write-Warning $_ }
    if ($null -eq $previousSocket) { Remove-Item Env:CREWFOLD_SOCKET -ErrorAction SilentlyContinue } else { $env:CREWFOLD_SOCKET = $previousSocket }
    [Console]::OutputEncoding = $previousConsoleEncoding
    $OutputEncoding = $previousPipelineEncoding
    if ($KeepArtifacts) {
        Write-Output "Artifacts retained at $testRoot"
    } else {
        Remove-Item -LiteralPath $testRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
}
