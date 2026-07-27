param(
    [switch]$SkipTests
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$bin = Join-Path $root "bin"
$loaderManifest = Join-Path $root "runtime\loader\Cargo.toml"
$loaderTarget = Join-Path $root "runtime\loader\target\release"

Push-Location $root
try {
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        throw "Go is required to build cinlan-qq-bot."
    }
    if (-not (Get-Command cargo -ErrorAction SilentlyContinue)) {
        throw "Rust/Cargo is required to build the self-owned QQNT loader."
    }

    New-Item -ItemType Directory -Force -Path $bin | Out-Null

    & cargo build --release --manifest-path $loaderManifest
    if ($LASTEXITCODE -ne 0) {
        throw "Cargo build failed with exit code $LASTEXITCODE."
    }
    Copy-Item -Force `
        (Join-Path $loaderTarget "cinlan-qq-loader.exe") `
        (Join-Path $bin "cinlan-qq-loader.exe")
    Copy-Item -Force `
        (Join-Path $loaderTarget "cinlan_qq_hook.dll") `
        (Join-Path $bin "cinlan-qq-hook.dll")

    if (-not $SkipTests) {
        & go test ./...
        if ($LASTEXITCODE -ne 0) {
            throw "Go tests failed with exit code $LASTEXITCODE."
        }
        & go vet ./...
        if ($LASTEXITCODE -ne 0) {
            throw "Go vet failed with exit code $LASTEXITCODE."
        }
        & cargo test --manifest-path $loaderManifest
        if ($LASTEXITCODE -ne 0) {
            throw "Cargo tests failed with exit code $LASTEXITCODE."
        }
        if (Get-Command node -ErrorAction SilentlyContinue) {
            foreach ($script in @("runtime\qqnt\load-cinlan.cjs", "runtime\qqnt\runtime.cjs")) {
                & node --check (Join-Path $root $script)
                if ($LASTEXITCODE -ne 0) {
                    throw "Syntax check failed for $script with exit code $LASTEXITCODE."
                }
            }
        }
        else {
            Write-Warning "node not found; skipped QQNT runtime syntax check."
        }
    }

    & go build -o (Join-Path $bin "cinlan-qq-bot.exe") .\cmd\cinlan-qq-bot
    if ($LASTEXITCODE -ne 0) {
        throw "Go build failed with exit code $LASTEXITCODE."
    }

    Write-Host "Built:"
    Write-Host "  bin\cinlan-qq-bot.exe"
    Write-Host "  bin\cinlan-qq-loader.exe"
    Write-Host "  bin\cinlan-qq-hook.dll"
}
finally {
    Pop-Location
}
