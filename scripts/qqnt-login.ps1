param(
    [string]$BaseUrl = "http://127.0.0.1:18080",
    [switch]$Refresh
)

$ErrorActionPreference = "Stop"
if (-not $env:ADMIN_API_TOKEN) { throw "ADMIN_API_TOKEN is required in the process environment." }
$headers = @{ Authorization = "Bearer $env:ADMIN_API_TOKEN" }
$endpoint = "$BaseUrl/api/v1/login/qr"
if ($Refresh) { $endpoint += "?refresh=true" }
$qr = Invoke-RestMethod -Uri $endpoint -Headers $headers
if ($qr.status -eq "ready") { Write-Output "QQNT is already ready."; return }
if ($qr.status -ne "available" -or -not $qr.png_base64) {
    throw "QQNT QR unavailable: $($qr.status) $($qr.last_error)"
}
$root = Split-Path -Parent $PSScriptRoot
$output = Join-Path $root "exports/qqnt/login.png"
New-Item -ItemType Directory -Force -Path (Split-Path -Parent $output) | Out-Null
[IO.File]::WriteAllBytes($output, [Convert]::FromBase64String($qr.png_base64))
Write-Output "QR retrieved. Cache expires at $($qr.expires_at). Remove exports/qqnt/login.png after use."
$browser = @(
    "$env:ProgramFiles/Microsoft/Edge/Application/msedge.exe",
    "${env:ProgramFiles(x86)}/Microsoft/Edge/Application/msedge.exe",
    "$env:ProgramFiles/Google/Chrome/Application/chrome.exe"
) | Where-Object { Test-Path -LiteralPath $_ } | Select-Object -First 1
if (-not $browser) { throw "Browser not found. QR is at $output" }
Start-Process -FilePath $browser -ArgumentList ('"' + $output + '"') -WindowStyle Hidden
