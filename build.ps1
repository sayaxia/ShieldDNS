# ShieldDNS 一键构建脚本（Windows / PowerShell 5.1+）
# 用法：
#   .\build.ps1                # 构建核心 + 面板，产物在 dist\
#   .\build.ps1 -SkipCore      # 只重建 Go 面板（核心已编译过）
#   .\build.ps1 -DebugCore     # 构建 debug 核心，便于排查问题

param(
    [switch]$SkipCore,
    [switch]$DebugCore
)

$ErrorActionPreference = 'Stop'
$Root = Split-Path -Parent $MyInvocation.MyCommand.Path
$CoreDir = Join-Path $Root 'core'
$PanelDir = Join-Path $Root 'panel'
$DistDir = Join-Path $Root 'dist'

function Find-MinGW {
    if ($env:SHIELDDNS_MINGW -and (Test-Path (Join-Path $env:SHIELDDNS_MINGW 'gcc.exe'))) {
        return $env:SHIELDDNS_MINGW
    }
    $candidates = @()
    $wingetRoot = Join-Path $env:LOCALAPPDATA 'Microsoft\WinGet\Packages'
    if (Test-Path $wingetRoot) {
        $candidates += Get-ChildItem $wingetRoot -Directory -Filter '*WinLibs*' -ErrorAction SilentlyContinue |
            ForEach-Object { Join-Path $_.FullName 'mingw64\bin' }
    }
    $candidates += 'C:\mingw64\bin', 'C:\msys64\mingw64\bin', 'C:\ProgramData\mingw64\bin'
    foreach ($c in $candidates) {
        if ($c -and (Test-Path (Join-Path $c 'gcc.exe'))) { return $c }
    }
    throw ' 找不到 MinGW（gcc.exe）。请安装 WinLibs 或设置 $env:SHIELDDNS_MINGW 指向 mingw64\bin。'
}

function Find-Go {
    $go = Join-Path $env:USERPROFILE 'sdk\go\bin\go.exe'
    if (Test-Path $go) { return $go }
    $cmd = Get-Command go -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    throw ' 找不到 Go（go.exe）。请安装 Go 1.21+ 或把 go.exe 放进 PATH。'
}

Write-Host '== ShieldDNS 构建 ==' -ForegroundColor Cyan
Write-Host "项目根目录: $Root"

$mingw = Find-MinGW
$goExe = Find-Go
$cargoBin = Join-Path $env:USERPROFILE '.cargo\bin'
$env:Path = "$cargoBin;$mingw;$(Split-Path -Parent $goExe);" + $env:Path

if (-not (Get-Command cargo -ErrorAction SilentlyContinue)) {
    throw ' 找不到 cargo。请先安装 Rust：https://rustup.rs（推荐 stable-x86_64-pc-windows-gnu 工具链）。'
}

# 1. Rust 核心
if (-not $SkipCore) {
    $profile = if ($DebugCore) { 'dev' } else { 'release' }
    Write-Host "[1/3] 编译 Rust 核心（$profile）..." -ForegroundColor Yellow
    Push-Location $CoreDir
    try {
        if ($DebugCore) {
            cargo build --bin shielddns-core
        } else {
            cargo build --release --bin shielddns-core
        }
        if ($LASTEXITCODE -ne 0) { throw 'cargo build 失败' }
    } finally {
        Pop-Location
    }
}

# 2. 图标 / 清单资源（windres）
Write-Host '[2/3] 编译图标与 DPI 清单资源...' -ForegroundColor Yellow
$windres = Join-Path $mingw 'windres.exe'
if (-not (Test-Path $windres)) { throw " 找不到 windres.exe：$windres" }
Push-Location $PanelDir
try {
    & $windres -I $PanelDir -i app.rc -O coff -o app.syso
    if ($LASTEXITCODE -ne 0) { throw 'windres 失败' }
} finally {
    Pop-Location
}

# 3. Go 面板
Write-Host '[3/3] 编译 Go 面板...' -ForegroundColor Yellow
New-Item -ItemType Directory -Force -Path (Join-Path $DistDir 'core') | Out-Null
Push-Location $PanelDir
try {
    & $goExe build -trimpath -ldflags '-H windowsgui -s -w' -o (Join-Path $DistDir 'ShieldDNS.exe') .
    if ($LASTEXITCODE -ne 0) { throw 'go build 失败' }
} finally {
    Pop-Location
}

# 组装产物
$coreExe = if ($DebugCore) {
    Join-Path $CoreDir 'target\debug\shielddns-core.exe'
} else {
    Join-Path $CoreDir 'target\release\shielddns-core.exe'
}
Copy-Item -LiteralPath $coreExe -Destination (Join-Path $DistDir 'core\shielddns-core.exe') -Force
# 离线手动更新器：跟着产物一起放，GitHub 连不上的用户解压后右键管理员运行即可
Copy-Item -LiteralPath (Join-Path $Root 'scripts\update.cmd') -Destination (Join-Path $DistDir 'update.cmd') -Force

Write-Host ''
Write-Host '构建完成，产物：' -ForegroundColor Green
Get-ChildItem -LiteralPath $DistDir -Recurse -File | ForEach-Object {
    '{0,-42} {1,10:N0} 字节' -f $_.FullName.Substring($DistDir.Length + 1), $_.Length
}
Write-Host ''
Write-Host "直接运行：$(Join-Path $DistDir 'ShieldDNS.exe')" -ForegroundColor Cyan
