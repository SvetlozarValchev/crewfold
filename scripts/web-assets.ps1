$ErrorActionPreference = "Stop"

function Get-CrewfoldWebSourceHash {
    param([Parameter(Mandatory = $true)][string]$RepositoryRoot)

    $webRoot = Join-Path $RepositoryRoot "web"
    $files = @(
        Get-ChildItem -LiteralPath (Join-Path $webRoot "src") -File -Recurse | ForEach-Object { $_.FullName }
        @(
            "index.html", "package.json", "pnpm-lock.yaml", "tsconfig.json",
            "tsconfig.app.json", "tsconfig.node.json", "vite.config.ts"
        ) | ForEach-Object { Join-Path $webRoot $_ }
    )
    $files = @($files | Sort-Object { $_.Substring($RepositoryRoot.Length + 1).Replace('\', '/') })

    $lines = [Text.StringBuilder]::new()
    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        foreach ($file in $files) {
            $stream = [IO.File]::OpenRead($file)
            try {
                $digest = $sha.ComputeHash($stream)
            } finally {
                $stream.Dispose()
            }
            [void]$lines.Append(([BitConverter]::ToString($digest).Replace("-", "").ToLowerInvariant()))
            [void]$lines.Append("`n")
        }
        $combined = [Text.Encoding]::UTF8.GetBytes($lines.ToString())
        return [BitConverter]::ToString($sha.ComputeHash($combined)).Replace("-", "").ToLowerInvariant()
    } finally {
        $sha.Dispose()
    }
}

function Invoke-CrewfoldCommand {
    param(
        [Parameter(Mandatory = $true)][string]$Command,
        [Parameter(Mandatory = $true)][string[]]$Arguments
    )
    & $Command @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$Command failed with exit code $LASTEXITCODE"
    }
}
