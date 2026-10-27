# 🤖 Telegram Remote Agent (`telegram-remote-agent`)

Kontrol dan perintahkan AI Agent di laptop Anda dari jarak jauh melalui **Chat Pribadi Telegram** untuk membuat, merapikan, atau mengedit file **MS Excel (`.xlsx`)**, **MS Word (`.docx`)**, **Markdown (`.md`)**, **Catatan (`.txt`)**, maupun kode program — **tanpa perlu membuka aplikasi Antigravity Desktop**.

---

## 🌉 Jembatan Konsep (Dari Sudut Pandang Laravel / Yii)

| Konsep di Proyek Ini (Golang) | Padanan di Laravel / PHP | Penjelasan Fungsi |
| :--- | :--- | :--- |
| **Long Polling (`getUpdates`)** | `php artisan queue:work` | Berjaga di *background* mengambil pesan dari server Telegram tanpa perlu IP Publik, VPS, atau Ngrok. |
| **`SessionManager` (`/unlock <PIN>`)** | `Auth::attempt()` + Session Timeout | Sabuk pengaman anti-maling: sesi terkunci secara *default* dan otomatis mengunci lagi setelah 15 menit tidak aktif. |
| **Auto-Delete PIN (`deleteMessage`)** | One-Time Password (OTP) Scrubbing | Begitu Anda mengetik `/unlock 8899`, pesan berisi PIN tersebut langsung dihapus otomatis dari layar Telegram dalam 1 detik agar tidak bisa diintip. |
| **`isPathAllowed` (`ALLOWED_ROOTS`)** | Middleware Policy / Chroot Jail | Membatasi ruang gerak perpindahan folder (`/cd`) agar hanya boleh di dalam folder kerja yang Anda izinkan. |
| **`snapshotAndBackupDir`** | Database Transaction / Backup | Otomatis membuat salinan cadangan di `.agent-backup/` sebelum Agent memodifikasi file dokumen penting. |

---

## 🚀 Cara Pakai (Cukup 1x Klik Saat Instalasi Awal!)

### Langkah 1: Buat Bot Baru di Telegram (1 Menit)
1. Buka Telegram, cari **`@BotFather`**, lalu ketik `/newbot`.
2. Beri nama bot Anda (misal: `Remote Agent Pribadi`), lalu salin **HTTP API Token**-nya.
3. Cari **`@userinfobot`** di Telegram, klik **Start**, lalu salin angka **ID** akun Telegram pribadi Anda.

### Langkah 2: Isi File `.env`
Buka file [`.env`](file:///d:/Project%20Sistem/Project%20Web%20Apps/Update%20AI/telegram-remote-agent/.env) dan sesuaikan 3 baris utama ini:
```env
TELEGRAM_BOT_TOKEN=123456789:AAHfiqksKZ8WmR2z...
TELEGRAM_ALLOWED_CHAT_ID=987654321
AGENT_PIN=8899
```

### Langkah 3: Klik 1x [`1-Pasang-AutoStart-Sekali-Klik.bat`](file:///d:/Project%20Sistem/Project%20Web%20Apps/Update%20AI/telegram-remote-agent/1-Pasang-AutoStart-Sekali-Klik.bat)
- Klik 2x file **`1-Pasang-AutoStart-Sekali-Klik.bat`**.
- Script ini akan:
  1. Memastikan library Python `openpyxl` (Excel) & `python-docx` (Word) siap.
  2. Mendaftarkan `remote-agent-bg.exe` ke **Windows Startup** (`shell:startup`).
  3. Menyalakan Agent secara diam-diam di *background* (tanpa jendela hitam CMD yang mengganggu).
- **Selesai!** Mulai besok dan seterusnya, setiap kali adik Anda atau Anda menyalakan laptop, bot otomatis langsung aktif dan mengirim pesan sapaan ke Telegram Anda!

---

## 💬 Daftar Perintah di Chat Telegram

| Perintah di Telegram | Fungsi |
| :--- | :--- |
| `/unlock 8899` | Membuka kunci sesi selama 15 menit *(pesan PIN otomatis dihapus dari layar chat)*. |
| `/lock` | Mengunci kembali sesi saat itu juga. |
| `/pwd` | Melihat lokasi folder kerja aktif saat ini dan sisa waktu sesi. |
| `/ls` | Melihat daftar file dan sub-folder di lokasi aktif saat ini. |
| `/cd <path>` | Pindah ke folder lain (contoh: `/cd ..`, `/cd NamaFolder`, atau `/cd D:\Dokumen Kerja`). |
| `/ambil <nama_file>` | Mengirim/download file dari laptop langsung ke chat Telegram di HP Anda. |
| `/buka <nama_file>` | Membuka tampilkan file langsung di layar laptop (misal agar langsung terlihat oleh orang di rumah). |
| **Ketik Kalimat Biasa** | Langsung dieksekusi oleh AI Agent! Contoh:<br>• *"Buat file Laporan_Kas.xlsx berisi tabel pemasukan & pengeluaran dengan header warna merah dan auto-fit kolom"*<br>• *"Rapikan kolom pada file Data.xlsx"*<br>• *"Tambahkan poin kesimpulan pada file Catatan.md"* |
| **Kirim/Upload File** | Kirim file Excel/Word/MD dari HP ke chat bot disertai instruksi di *caption*-nya, bot akan menyimpannya di laptop dan langsung mengeksekusi perintah Anda! |
