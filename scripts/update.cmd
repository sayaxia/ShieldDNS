@echo off
setlocal
chcp 936 >nul
title ShieldDNS 手动更新（离线包）

rem ============================================================
rem 给「GitHub 打不开、只能手动下载 zip」的用户用：
rem   1) 把 Release 的 zip 解压出来（这个 update.cmd 就在解压目录里）
rem   2) 右键本文件 → 以管理员身份运行
rem 它会：停掉正在运行的面板与后台助手 → 旧文件改名备份 → 复制新文件 → 启动新版本
rem 正在运行的 exe 不能直接覆盖，但可以改名，所以先改名再写，不会出现「文件正在使用」。
rem ============================================================

net session >nul 2>&1
if errorlevel 1 (
  echo [错误] 需要管理员权限：请对这个文件点右键，选择「以管理员身份运行」。
  echo.
  pause
  exit /b 1
)

set "SRC=%~dp0"
set "DST=%ProgramFiles%\ShieldDNS"
if not "%~1"=="" set "DST=%~1"

if not exist "%SRC%ShieldDNS.exe" (
  echo [错误] 这个 update.cmd 要和 ShieldDNS.exe 放在同一层目录。
  echo        请先把 zip 完整解压，再运行解压目录里的 update.cmd。
  echo.
  pause
  exit /b 1
)

if not exist "%DST%\ShieldDNS.exe" (
  echo 默认安装目录里没有找到 ShieldDNS.exe：%DST%
  set /p DST=请输入 ShieldDNS 安装目录（例如 C:\Program Files\ShieldDNS）: 
)
if not exist "%DST%\ShieldDNS.exe" (
  echo [错误] 目录里没有 ShieldDNS.exe：%DST%
  echo.
  pause
  exit /b 1
)

echo 安装目录：%DST%
echo.
echo [1/4] 停止 ShieldDNS（面板 + SYSTEM 后台助手）...
taskkill /F /IM ShieldDNS.exe >nul 2>&1
choice /t 4 /d y /n >nul

set "STAMP=%RANDOM%%RANDOM%"
echo [2/4] 备份并替换主程序...
attrib -R "%DST%\ShieldDNS.exe" >nul 2>&1
move /Y "%DST%\ShieldDNS.exe" "%DST%\ShieldDNS.exe.old-%STAMP%" >nul 2>&1
copy /Y "%SRC%ShieldDNS.exe" "%DST%\ShieldDNS.exe" >nul
if not exist "%DST%\ShieldDNS.exe" (
  echo [错误] 主程序复制失败。若是杀软拦截，请放行后重试。
  echo.
  pause
  exit /b 1
)

echo [3/4] 替换 DNS 核心...
if not exist "%DST%\core" mkdir "%DST%\core"
if exist "%SRC%core\shielddns-core.exe" (
  if exist "%DST%\core\shielddns-core.exe" (
    attrib -R "%DST%\core\shielddns-core.exe" >nul 2>&1
    move /Y "%DST%\core\shielddns-core.exe" "%DST%\core\shielddns-core.exe.old-%STAMP%" >nul 2>&1
  )
  copy /Y "%SRC%core\shielddns-core.exe" "%DST%\core\shielddns-core.exe" >nul
)

echo [4/4] 启动新版本...
del /Q "%DST%\ShieldDNS.exe~" >nul 2>&1
del /Q "%DST%\core\shielddns-core.exe~" >nul 2>&1
start "" /d "%DST%" "%DST%\ShieldDNS.exe"

echo.
echo 完成。新版本已启动，防护默认是「待命」状态，需要手动打开。
echo 旧文件备份在 ShieldDNS.exe.old-%STAMP%，确认新版本正常后可以删除。
echo 如果装了后台助手，请在「设置」里点一次「重启」，让它也换成新版本。
echo.
pause
