@echo off
chcp 65001 >nul
title Mode Konsol Interaktif - Telegram Remote Agent
color 0B

cd /d "%~dp0"

echo Menghentikan background process sementara agar tidak bentrok polling...
taskkill /F /IM remote-agent-bg.exe >nul 2>&1

echo Menjalankan Telegram Remote Agent dalam mode layar log...
echo (Tekan Ctrl+C atau tutup jendela ini untuk berhenti)
echo.
"%~dp0remote-agent.exe"
pause
