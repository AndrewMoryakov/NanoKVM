@echo off
setlocal EnableExtensions EnableDelayedExpansion
chcp 65001 >nul

echo ==========================================
echo  Windows Network Diagnostics for NanoKVM
echo ==========================================
echo.

set "KVM_IP=192.168.0.36"
set /p KVM_IP=KVM IP [%KVM_IP%]:

echo.
echo === Windows IP Configuration ===
ipconfig | findstr /i "IPv4 Subnet Default"
echo.

echo === Routing table for KVM ===
route print -4 | findstr "%KVM_IP%"
echo.

echo === Persistent routes ===
route print -4 | findstr /C:"Persistent"
route print -4 | findstr /C:"%KVM_IP%"
echo.

echo === ARP table for local subnet ===
arp -a | findstr "192.168.0"
echo.

echo === VPN interfaces ===
route print -4 | findstr /C:"0.0.0.0" | findstr /C:"128.0.0.0"
echo.

echo === Ping KVM from Windows ===
ping -n 3 -w 2000 %KVM_IP%
echo.

echo === Ping KVM from WSL ===
wsl bash -lc "ping -c 3 -W 2 %KVM_IP% 2>&1"
echo.

echo === Ping gateway from WSL ===
wsl bash -lc "ping -c 1 -W 2 192.168.0.1 2>&1 | tail -2"
echo.

echo === NetBird status (Windows) ===
where netbird >nul 2>&1 && (netbird status 2>&1) || echo netbird not installed
echo.

pause
endlocal
exit /b 0
