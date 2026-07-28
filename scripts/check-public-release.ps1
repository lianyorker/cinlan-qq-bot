$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
$gitCommand = Get-Command git -ErrorAction SilentlyContinue
if ($gitCommand) {
    $git = $gitCommand.Source
} else {
    $candidates = @(
        (Join-Path ${env:ProgramFiles} "Git\bin\git.exe"),
        "D:\code-tools\Git\bin\git.exe"
    )
    $git = $candidates | Where-Object { Test-Path -LiteralPath $_ } | Select-Object -First 1
}
if (-not $git) {
    throw "git is required; add Git to PATH before running this check"
}

Push-Location $root
try {
    $tracked = @(& $git ls-files)
    if ($LASTEXITCODE -ne 0) {
        throw "git ls-files failed"
    }

    $staged = @(& $git diff --cached --name-only --diff-filter=ACMR)
    if ($LASTEXITCODE -ne 0) {
        throw "git diff --cached failed"
    }

    $untracked = @(& $git ls-files --others --exclude-standard)
    if ($LASTEXITCODE -ne 0) {
        throw "git ls-files --others failed"
    }

    $paths = @($tracked + $staged + $untracked) |
        Where-Object { $_ -and (Test-Path -LiteralPath $_ -PathType Leaf) } |
        Sort-Object -Unique

    $patterns = @(
        '(?i)\bAKIA[0-9A-Z]{16}\b',
        '(?i)-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----',
        '(?im)^[ \t]*["'']?(?:api[_-]?key|access[_-]?token|session[_-]?encryption[_-]?key|admin[_-]?password)["'']?[ \t]*=[ \t]*(?!<|replace-|your-|example-|test-|$)\S+'
    )

    $privatePatternFile = Join-Path $root ".release-private-patterns.txt"
    if (Test-Path -LiteralPath $privatePatternFile -PathType Leaf) {
        foreach ($line in Get-Content -LiteralPath $privatePatternFile -Encoding utf8) {
            $value = $line.Trim()
            if ($value -and -not $value.StartsWith("#")) {
                $patterns += [Regex]::Escape($value)
            }
        }
    }

    $findings = @()
    foreach ($path in $paths) {
        if ($path -eq "scripts/check-public-release.ps1") {
            continue
        }
        $content = Get-Content -LiteralPath $path -Raw -Encoding utf8 -ErrorAction SilentlyContinue
        if ($null -eq $content) {
            continue
        }
        foreach ($pattern in $patterns) {
            if ($content -match $pattern) {
                $findings += "$path matches $pattern"
                break
            }
        }
    }

    $privatePaths = @(
        "基本业务.md",
        ".env",
        "data/",
        "exports/",
        ".release-private-patterns.txt"
    )
    foreach ($path in $privatePaths) {
        $trackedPrivate = if ($path.EndsWith("/")) {
            $tracked | Where-Object { $_.StartsWith($path, [StringComparison]::OrdinalIgnoreCase) }
        } else {
            $tracked | Where-Object { $_.Equals($path, [StringComparison]::OrdinalIgnoreCase) }
        }
        if ($trackedPrivate) {
            $findings += "private path is tracked: $path"
        }
    }

    if ($findings.Count -gt 0) {
        Write-Error ("Public release check failed:`n" + ($findings -join "`n"))
    }

    Write-Output "Public release check passed: $($paths.Count) tracked/staged files scanned."
}
finally {
    Pop-Location
}
