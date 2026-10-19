$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$versionLine = Select-String -Path (Join-Path $root 'fpk\manifest') -Pattern '^\s*version\s*=' | Select-Object -First 1
$version = ($versionLine.Line -split '=', 2)[1].Trim()

$binDir = Join-Path $root 'fpk\app\bin'
New-Item -ItemType Directory -Force -Path $binDir | Out-Null

Write-Host "Building smartunzip $version (linux/amd64, linux/arm64)"
$env:CGO_ENABLED = '0'
$env:GOOS = 'linux'
$env:GOARCH = 'amd64'
go build -trimpath -ldflags "-s -w -X main.appVersion=$version" -o (Join-Path $binDir 'smartunzip-linux-amd64') .
$env:GOARCH = 'arm64'
go build -trimpath -ldflags "-s -w -X main.appVersion=$version" -o (Join-Path $binDir 'smartunzip-linux-arm64') .
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED

Copy-Item (Join-Path $root 'tools\7z-x64\7zz') (Join-Path $binDir '7zz-linux-amd64') -Force
Copy-Item (Join-Path $root 'tools\7z-arm64\7zz') (Join-Path $binDir '7zz-linux-arm64') -Force
Copy-Item (Join-Path $root 'tools\7z-x64\License.txt') (Join-Path $binDir '7zz-License.txt') -Force

$oldFpk = Join-Path $root 'fpk\com.smartunzip.app.fpk'
if (Test-Path $oldFpk) { Remove-Item $oldFpk -Force }

Push-Location (Join-Path $root 'fpk')
try {
    & (Join-Path $root 'tools\fnpack.exe') build
    if ($LASTEXITCODE -ne 0) { throw "fnpack build failed: $LASTEXITCODE" }
} finally {
    Pop-Location
}

$distDir = Join-Path $root 'dist'
New-Item -ItemType Directory -Force -Path $distDir | Out-Null
$out = Join-Path $distDir "com.smartunzip.app-$version.fpk"
python (Join-Path $root 'scripts\fix_fpk.py') $oldFpk $out
if ($LASTEXITCODE -ne 0) { throw "fix_fpk failed: $LASTEXITCODE" }

Write-Host "FPK ready: $out"
Get-Item $out | Select-Object Name, Length
