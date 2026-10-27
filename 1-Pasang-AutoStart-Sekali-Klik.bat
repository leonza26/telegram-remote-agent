@echo off
chcp 65001 >nul
title Setup Auto-Start Telegram Remote Agent (1x Klik)
color 0A

echo ============================================================================
echo   🚀 INSTALASI 1x KLIK: TELEGRAM REMOTE AGENT (AUTO-START WINDOWS)
echo ============================================================================
echo.

cd /d "%~dp0"

:: 1. Pastikan Python openpyxl & python-docx tersedia untuk edit Excel & Word
echo [1/4] Memeriksa dukungan manipulasi MS Excel (.xlsx) ^& MS Word (.docx)...
python -c "import openpyxl, docx" 2>nul
if %errorlevel% neq 0 (
    echo       Menginstal library Python openpyxl ^& python-docx...
    pip install openpyxl python-docx --quiet
) else (
    echo       [OK] Library Excel ^& Word sudah siap!
)
echo.

:: 2. Pastikan binary remote-agent-bg.exe tersedia
echo [2/4] Memeriksa file binary remote-agent-bg.exe...
if not exist "%~dp0remote-agent-bg.exe" (
    echo       Mengompilasi ulang remote-agent-bg.exe dari main.go...
    go build -ldflags="-H=windowsgui" -o remote-agent-bg.exe .
    go build -o remote-agent.exe .
)
echo       [OK] Binary siap digunakan!
echo.

:: 3. Daftarkan ke folder Windows Startup (shell:startup)
echo [3/4] Mendaftarkan ke Windows Startup agar otomatis jalan saat laptop nyala...
set "STARTUP_DIR=%APPDATA%\Microsoft\Windows\Start Menu\Programs\Startup"
set "SHORTCUT_PATH=%STARTUP_DIR%\TelegramRemoteAgent.lnk"

powershell -NoProfile -ExecutionPolicy Bypass -Command ^
  "$ws = New-Object -ComObject WScript.Shell; ^
   $sc = $ws.CreateShortcut('%SHORTCUT_PATH%'); ^
   $sc.TargetPath = '%~dp0remote-agent-bg.exe'; ^
   $sc.WorkingDirectory = '%~dp0'; ^
   $sc.Description = 'Telegram Remote Agent Background Service'; ^
   $sc.Save()"

if exist "%SHORTCUT_PATH%" (
    echo       [OK] Berhasil dipasang di Windows Startup!
    echo       Lokasi: %SHORTCUT_PATH%
) else (
    echo       [!] Gagal membuat shortcut Startup.
)
echo.

:: 4. Jalankan langsung di background jika .env sudah diisi
echo [4/4] Memeriksa konfigurasi .env...
findstr /C:"ISI_TOKEN" "%~dp0.env" >nul
if %errorlevel% equ 0 (
    echo.
    echo ⚠️  CATATAN PENTING:
    echo     Auto-Start sudah terpasang, TETAPI file .env Anda belum diisi Token ^& Chat ID.
    echo     Silakan isi file .env terlebih dahulu, lalu klik file ini 1x lagi.
    notepad "%~dp0.env"
) else (
    taskkill /F /IM remote-agent-bg.exe >nul 2>&1
    taskkill /F /IM remote-agent.exe >nul 2>&1
    start "" "%~dp0remote-agent-bg.exe"
    echo       [OK] Telegram Remote Agent sekarang sudah AKTIF di belakang layar!
    echo       Cek aplikasi Telegram di HP Anda, bot sudah mengirim pesan sapaan.
)

echo.
echo ============================================================================
echo   ✅ SELESAI! Anda TIDAK PERLU mengklik file ini lagi setiap buka laptop.
echo      Setiap laptop menyala, bot otomatis aktif secara diam-diam di background.
echo ============================================================================
pause
