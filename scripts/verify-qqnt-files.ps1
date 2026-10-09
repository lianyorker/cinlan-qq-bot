param(
    [Parameter(Mandatory)][string]$QQRoot,
    [string]$Baseline = "exports/qqnt/official-hashes.json",
    [switch]$Compare
)

$ErrorActionPreference = "Stop"
$root = (Resolve-Path -LiteralPath $QQRoot).Path
$active = (Get-Content -LiteralPath (Join-Path $root "versions/config.json") -Raw | ConvertFrom-Json).curVersion
if (-not $active -or [IO.Path]::GetFileName($active) -ne $active -or $active -eq "..") { throw "Invalid QQNT curVersion" }
$version = Join-Path $root "versions/$active"
$paths = @("resources/app/package.json", "resources/app/wrapper.node", "QQNT.dll")
$current = @($paths | ForEach-Object {
    $hash = Get-FileHash -LiteralPath (Join-Path $version $_) -Algorithm SHA256
    [pscustomobject]@{ Path = $hash.Path; SHA256 = $hash.Hash }
})
if ($Compare) {
    $before = @(Get-Content -LiteralPath $Baseline -Raw | ConvertFrom-Json)
    $diff = Compare-Object $before $current -Property Path, SHA256
    if ($diff) { throw "Official QQ files or activated version changed; compare the baseline before continuing." }
    Write-Output "Official package.json, wrapper.node and QQNT.dll hashes are unchanged."
} else {
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent ([IO.Path]::GetFullPath($Baseline))) | Out-Null
    $current | ConvertTo-Json | Set-Content -LiteralPath $Baseline -Encoding utf8
    Write-Output "Official QQ hash baseline written to $Baseline (version $active)."
}
