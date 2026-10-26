param(
    [switch]$SkipTests
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$bin = Join-Path $root "bin"
$goTags = $env:GO_BUILD_TAGS

function GoArgs([string]$Command, [string[]]$Arguments) {
    $result = @($Command)
    if ($goTags) {
        $result += @("-tags", $goTags)
    }
    return $result + $Arguments
}

Push-Location $root
try {
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        throw "Go is required to build cinlan-qq-bot."
    }

    New-Item -ItemType Directory -Force -Path $bin | Out-Null

    if (-not $SkipTests) {
        & go @(GoArgs "test" @("./..."))
        if ($LASTEXITCODE -ne 0) {
            throw "Go tests failed with exit code $LASTEXITCODE."
        }
        & go @(GoArgs "vet" @("./..."))
        if ($LASTEXITCODE -ne 0) {
            throw "Go vet failed with exit code $LASTEXITCODE."
        }
    }

    & go @(GoArgs "build" @(
        "-o",
        (Join-Path $bin "cinlan-qq-bot.exe"),
        ".\cmd\cinlan-qq-bot"
    ))
    if ($LASTEXITCODE -ne 0) {
        throw "Go build failed with exit code $LASTEXITCODE."
    }

    Write-Host "Built:"
    Write-Host "  bin\cinlan-qq-bot.exe"
}
finally {
    Pop-Location
}
