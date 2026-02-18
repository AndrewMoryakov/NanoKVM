@echo off
setlocal EnableExtensions EnableDelayedExpansion
chcp 65001 >nul

where wsl >nul 2>&1
if errorlevel 1 (
  echo [ERROR] wsl.exe not found. Install/enable WSL first.
  pause
  exit /b 1
)

set "SCRIPT_DIR=%~dp0"
for %%I in ("%SCRIPT_DIR%..") do set "REPO_WIN=%%~fI"

for /f "usebackq delims=" %%I in (`wsl wslpath -a "%REPO_WIN%"`) do set "REPO_WSL=%%I"
if not defined REPO_WSL (
  echo [ERROR] Failed to convert repository path to WSL path.
  pause
  exit /b 1
)

set "DEFAULT_HOST=192.168.0.36"
set "DEFAULT_USER=root"
set "DEFAULT_PORT=22"
set "DEFAULT_PASS=72453722"

set "HOST="
set /p HOST=Host [%DEFAULT_HOST%]:
if not defined HOST set "HOST=%DEFAULT_HOST%"

set "USER_NAME="
set /p USER_NAME=User [%DEFAULT_USER%]:
if not defined USER_NAME set "USER_NAME=%DEFAULT_USER%"

set "PORT="
set /p PORT=SSH port [%DEFAULT_PORT%]:
if not defined PORT set "PORT=%DEFAULT_PORT%"

set "PASS="
set /p PASS=Password [****]:
if not defined PASS set "PASS=%DEFAULT_PASS%"

echo.
echo Launching optimize_kvm.sh via WSL...
echo.
wsl bash -lc "bash '%REPO_WSL%/scripts/optimize_kvm.sh' --host '%HOST%' --user '%USER_NAME%' --port '%PORT%' --pass '%PASS%'"

echo.
pause
endlocal
exit /b 0
