@echo off
chcp 65001 >nul
title Stop ^& Copot Auto-Start Telegram Remote Agent
color 0E

echo ============================================================================
echo   🛑 HENTIKAN ^& KELOLA AUTO-START TELEGRAM REMOTE AGENT
echo ============================================================================
echo.

echo [1] Menghentikan proses Remote Agent yang sedang berjalan di background...
taskkill /F /IM remote-agent-bg.exe >nul 2>&1
taskkill /F /IM remote-agent.exe >nul 2>&1
echo     [OK] Proses Remote Agent sudah dihentikan.
echo.

set "SHORTCUT_PATH=%APPDATA%\Microsoft\Windows\Start Menu\Programs\Startup\TelegramRemoteAgent.lnk"
if exist "%SHORTCUT_PATH%" (
    choice /C YN /M "Apakah Anda juga ingin mencopot fitur Auto-Start saat laptop menyala (Y/N)?"
    if errorlevel 2 goto :done
    if errorlevel 1 (
        del /F /Q "%SHORTCUT_PATH%"
        echo     [OK] Auto-Start berhasil dicopot dari Windows Startup.
    )
) else (
    echo     [Info] Auto-Start saat ini tidak terdaftar di Windows Startup.
)

:done
echo.
echo Selesai!
pause
