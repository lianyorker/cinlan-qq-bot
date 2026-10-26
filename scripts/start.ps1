$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$envFile = Join-Path $root ".env"
$exampleFile = Join-Path $root ".env.example"
$executable = Join-Path $root "bin\cinlan-qq-bot.exe"

function Import-DotEnv([string]$Path) {
    foreach ($line in Get-Content -LiteralPath $Path -Encoding utf8) {
        $trimmed = $line.Trim()
        if ($trimmed.Length -eq 0 -or $trimmed.StartsWith("#")) {
            continue
        }
        $separator = $trimmed.IndexOf("=")
        if ($separator -lt 1) {
            continue
        }
        $name = $trimmed.Substring(0, $separator).Trim()
        $value = $trimmed.Substring($separator + 1).Trim()
        if ($value.Length -ge 2 -and (
            ($value.StartsWith('"') -and $value.EndsWith('"')) -or
            ($value.StartsWith("'") -and $value.EndsWith("'"))
        )) {
            $value = $value.Substring(1, $value.Length - 2)
        }
        if (-not [Environment]::GetEnvironmentVariable($name, "Process")) {
            [Environment]::SetEnvironmentVariable($name, $value, "Process")
        }
    }
}

Push-Location $root
try {
    if (-not (Test-Path -LiteralPath $envFile)) {
        Copy-Item -LiteralPath $exampleFile -Destination $envFile
        Write-Host "Created .env from .env.example."
        Write-Host "Set AGENT_API_URL and QQ_GROUP_ALLOWLIST, then run start.cmd again."
        exit 2
    }
    Import-DotEnv $envFile
    if (-not $env:AGENT_API_KEY) {
        $userAgentApiKey = [Environment]::GetEnvironmentVariable("AGENT_API_KEY", "User")
        if ($userAgentApiKey) {
            $env:AGENT_API_KEY = $userAgentApiKey
        }
    }
    if (-not $env:SESSION_ENCRYPTION_KEY) {
        $userSessionKey = [Environment]::GetEnvironmentVariable("SESSION_ENCRYPTION_KEY", "User")
        if ($userSessionKey) {
            $env:SESSION_ENCRYPTION_KEY = $userSessionKey
        }
    }

    if (-not $env:AGENT_API_URL -and -not $env:PROVIDERS_FILE) {
        throw "AGENT_API_URL or PROVIDERS_FILE is required in .env."
    }
    if (-not $env:QQ_GROUP_ALLOWLIST) {
        throw "QQ_GROUP_ALLOWLIST is required in .env."
    }
    if ($env:SESSION_STORE_PATH -and -not $env:SESSION_ENCRYPTION_KEY) {
        throw "SESSION_ENCRYPTION_KEY is required when SESSION_STORE_PATH is enabled."
    }

    if (-not (Test-Path -LiteralPath $executable)) {
        & (Join-Path $PSScriptRoot "build.ps1")
    }

    & $executable
    exit $LASTEXITCODE
}
finally {
    Pop-Location
}
