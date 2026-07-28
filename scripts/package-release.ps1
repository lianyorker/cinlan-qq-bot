param(
    [string]$Version = "dev",
    [switch]$SkipTests
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$dist = Join-Path $root "dist"
$stageRoot = Join-Path $root ".tmp\release-packages"
$go = Get-Command go -ErrorAction SilentlyContinue
$cargo = Get-Command cargo -ErrorAction SilentlyContinue
$git = Get-Command git -ErrorAction SilentlyContinue
if (-not $git) {
    $git = @(
        (Join-Path ${env:ProgramFiles} "Git\bin\git.exe"),
        "D:\code-tools\Git\bin\git.exe"
    ) | Where-Object { Test-Path -LiteralPath $_ } | Select-Object -First 1
}
elseif ($git.Source) {
    $git = $git.Source
}

if ($Version -notmatch '^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$') {
    throw "Version may contain only letters, numbers, dots, underscores, and hyphens."
}
if (-not $go) {
    throw "Go is required to package cinlan-qq-bot."
}
if (-not $cargo) {
    throw "Rust/Cargo is required to package the Windows QQNT loader."
}
if (-not $git) {
    throw "Git is required to select public package files."
}

function Copy-TrackedFiles(
    [string]$Destination,
    [string[]]$Paths
) {
    $files = @(& $git ls-files -- @Paths)
    if ($LASTEXITCODE -ne 0) {
        throw "git ls-files failed while preparing package files."
    }
    foreach ($relative in $files) {
        $target = Join-Path $Destination $relative
        $targetDirectory = Split-Path -Parent $target
        New-Item -ItemType Directory -Force -Path $targetDirectory | Out-Null
        Copy-Item -LiteralPath (Join-Path $root $relative) -Destination $target
    }
}

function Copy-CommonFiles([string]$Destination) {
    Copy-TrackedFiles $Destination @(
        "README.md",
        ".env.example",
        "docs",
        "examples",
        "files/replace-with-your.sql"
    )
}

function Build-GoBinary(
    [string]$GoOS,
    [string]$GoArch,
    [string]$Output
) {
    $savedGOOS = $env:GOOS
    $savedGOARCH = $env:GOARCH
    $savedCGO = $env:CGO_ENABLED
    $savedGOFLAGS = $env:GOFLAGS
    try {
        $env:GOOS = $GoOS
        $env:GOARCH = $GoArch
        $env:CGO_ENABLED = "0"
        $env:GOFLAGS = ""
        & $go.Source build -trimpath -ldflags="-s -w" -o $Output .\cmd\cinlan-qq-bot
        if ($LASTEXITCODE -ne 0) {
            throw "Go build failed for $GoOS/$GoArch with exit code $LASTEXITCODE."
        }
    }
    finally {
        $env:GOOS = $savedGOOS
        $env:GOARCH = $savedGOARCH
        $env:CGO_ENABLED = $savedCGO
        $env:GOFLAGS = $savedGOFLAGS
    }
}

Push-Location $root
try {
    & (Join-Path $PSScriptRoot "check-public-release.ps1")

    if (-not $SkipTests) {
        & $go.Source test ./...
        if ($LASTEXITCODE -ne 0) {
            throw "Go tests failed with exit code $LASTEXITCODE."
        }
        & $go.Source vet ./...
        if ($LASTEXITCODE -ne 0) {
            throw "Go vet failed with exit code $LASTEXITCODE."
        }
        if (Get-Command node -ErrorAction SilentlyContinue) {
            & node --test runtime\qqnt\runtime.test.cjs
            if ($LASTEXITCODE -ne 0) {
                throw "QQNT runtime tests failed with exit code $LASTEXITCODE."
            }
        }
    }

    New-Item -ItemType Directory -Force -Path $dist | Out-Null
    if (Test-Path -LiteralPath $stageRoot) {
        $resolvedRoot = [IO.Path]::GetFullPath($root)
        $resolvedStage = [IO.Path]::GetFullPath($stageRoot)
        if (-not $resolvedStage.StartsWith(
            $resolvedRoot + [IO.Path]::DirectorySeparatorChar,
            [StringComparison]::OrdinalIgnoreCase
        )) {
            throw "Release staging path escaped the repository root."
        }
        Remove-Item -LiteralPath $stageRoot -Recurse -Force
    }
    New-Item -ItemType Directory -Force -Path $stageRoot | Out-Null

    & $cargo.Source build --release --manifest-path runtime\loader\Cargo.toml
    if ($LASTEXITCODE -ne 0) {
        throw "Windows loader build failed with exit code $LASTEXITCODE."
    }

    $windowsName = "cinlan-qq-bot-$Version-windows-amd64"
    $windowsStage = Join-Path $stageRoot $windowsName
    New-Item -ItemType Directory -Force -Path $windowsStage | Out-Null
    Build-GoBinary "windows" "amd64" (Join-Path $windowsStage "cinlan-qq-bot.exe")
    Copy-Item -LiteralPath runtime\loader\target\release\cinlan-qq-loader.exe `
        -Destination $windowsStage
    Copy-Item -LiteralPath runtime\loader\target\release\cinlan_qq_hook.dll `
        -Destination (Join-Path $windowsStage "cinlan-qq-hook.dll")
    Copy-TrackedFiles $windowsStage @(
        "runtime/qqnt",
        "start.cmd",
        "scripts/start.ps1"
    )
    Copy-CommonFiles $windowsStage
    Compress-Archive -Path (Join-Path $windowsStage "*") `
        -DestinationPath (Join-Path $dist "$windowsName.zip") -Force

    foreach ($arch in @("amd64", "arm64")) {
        $darwinName = "cinlan-qq-bot-$Version-darwin-$arch"
        $darwinStage = Join-Path $stageRoot $darwinName
        New-Item -ItemType Directory -Force -Path $darwinStage | Out-Null
        Build-GoBinary "darwin" $arch (Join-Path $darwinStage "cinlan-qq-bot")
        Copy-CommonFiles $darwinStage
        Compress-Archive -Path (Join-Path $darwinStage "*") `
            -DestinationPath (Join-Path $dist "$darwinName.zip") -Force
    }

    $archives = Get-ChildItem -LiteralPath $dist -Filter "cinlan-qq-bot-$Version-*.zip"
    $checksums = foreach ($archive in $archives) {
        $hash = Get-FileHash -LiteralPath $archive.FullName -Algorithm SHA256
        "$($hash.Hash.ToLowerInvariant())  $($archive.Name)"
    }
    Set-Content -LiteralPath (Join-Path $dist "SHA256SUMS-$Version.txt") `
        -Value $checksums -Encoding ascii

    Write-Host "Packages:"
    $archives | ForEach-Object { Write-Host "  dist\$($_.Name)" }
    Write-Host "  dist\SHA256SUMS-$Version.txt"
}
finally {
    Pop-Location
}
