package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ============================================================================
// 🌉 JEMBATAN KONSEP (LARAVEL / YII -> GOLANG DAEMON)
// ============================================================================
// 1. Long Polling (getUpdates) = Mirip `php artisan queue:work` yang terus
//    berjaga mengambil tugas dari antrean Telegram tanpa butuh IP Publik/Ngrok.
// 2. SessionManager            = Mirip Auth Guard + Session Timeout di Laravel.
// 3. isPathAllowed             = Mirip Policy / Middleware Chroot agar user
//    tidak bisa keluar dari folder yang diizinkan (ALLOWED_ROOTS).
// ============================================================================

// SessionManager mengelola status kunci PIN & waktu kadaluarsa sesi (Auto-Lock)
type SessionManager struct {
	mu          sync.Mutex
	pin         string
	timeout     time.Duration
	unlocked    bool
	lastActive  time.Time
	failedTries int
}

func NewSessionManager(pin string, timeout time.Duration) *SessionManager {
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	return &SessionManager{
		pin:     pin,
		timeout: timeout,
	}
}

func (sm *SessionManager) IsUnlocked(now time.Time) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if !sm.unlocked {
		return false
	}
	if now.Sub(sm.lastActive) > sm.timeout {
		sm.unlocked = false
		return false
	}
	return true
}

func (sm *SessionManager) Unlock(inputPIN string, now time.Time) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if strings.TrimSpace(inputPIN) == sm.pin && sm.pin != "" {
		sm.unlocked = true
		sm.lastActive = now
		sm.failedTries = 0
		return true
	}
	sm.failedTries++
	return false
}

func (sm *SessionManager) Touch(now time.Time) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.unlocked {
		sm.lastActive = now
	}
}

func (sm *SessionManager) Lock() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.unlocked = false
}

func (sm *SessionManager) RemainingMinutes(now time.Time) int {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if !sm.unlocked {
		return 0
	}
	rem := sm.timeout - now.Sub(sm.lastActive)
	if rem <= 0 {
		return 0
	}
	return int(rem.Minutes()) + 1
}

// isPathAllowed memastikan targetPath berada di dalam salah satu allowedRoots
func isPathAllowed(targetPath string, allowedRoots []string) bool {
	cleanTarget, err := filepath.Abs(filepath.Clean(targetPath))
	if err != nil {
		return false
	}
	cleanTargetLower := strings.ToLower(cleanTarget)

	for _, root := range allowedRoots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		cleanRoot, err := filepath.Abs(filepath.Clean(root))
		if err != nil {
			continue
		}
		cleanRootLower := strings.ToLower(cleanRoot)

		// Pastikan jika root bukan root drive (seperti "d:\"), tambahkan separator agar tidak bocor prefix
		if cleanTargetLower == cleanRootLower {
			return true
		}
		prefix := cleanRootLower
		if !strings.HasSuffix(prefix, `\`) && !strings.HasSuffix(prefix, `/`) {
			prefix += string(filepath.Separator)
		}
		if strings.HasPrefix(cleanTargetLower, prefix) {
			return true
		}
	}
	return false
}

// isDestructivePrompt memblokir perintah berbahaya yang berpotensi merusak sistem/OS
func isDestructivePrompt(prompt string) (bool, string) {
	lower := strings.ToLower(prompt)
	forbiddenPatterns := []struct {
		pattern string
		reason  string
	}{
		{"format c:", "Perintah format drive sistem dilarang keras."},
		{"format d:", "Perintah format drive dilarang keras."},
		{"rm -rf /", "Perintah hapus paksa seluruh root dilarang."},
		{"rm -rf *", "Perintah hapus massal dilarang."},
		{"del /f /s /q", "Perintah hapus paksa seluruh folder dilarang."},
		{"del /s /q c:", "Perintah hapus sistem Windows dilarang."},
		{"remove-item -recurse -force c:", "Perintah hapus rekursif drive sistem dilarang."},
		{"cipher /w", "Perintah wipe disk dilarang."},
		{"reg delete", "Modifikasi hapus registry Windows dilarang."},
	}

	for _, item := range forbiddenPatterns {
		if strings.Contains(lower, item.pattern) {
			return true, item.reason
		}
	}
	return false, ""
}

// isTopicAllowed memeriksa apakah pesan berasal dari Topik (message_thread_id) yang sesuai
func isTopicAllowed(msgTopicID int, configuredTopicID int) bool {
	if configuredTopicID <= 0 {
		return true
	}
	return msgTopicID == configuredTopicID
}

// ============================================================================
// STRUKTUR DATA TELEGRAM API
// ============================================================================

type TelegramUpdateResponse struct {
	OK     bool             `json:"ok"`
	Result []TelegramUpdate `json:"result"`
}

type TelegramUpdate struct {
	UpdateID int              `json:"update_id"`
	Message  *TelegramMessage `json:"message"`
}

type TelegramMessage struct {
	MessageID       int               `json:"message_id"`
	MessageThreadID int               `json:"message_thread_id"`
	From            *TelegramUser     `json:"from"`
	Chat            TelegramChat      `json:"chat"`
	Date            int64             `json:"date"`
	Text            string            `json:"text"`
	Caption         string            `json:"caption"`
	Document        *TelegramDocument `json:"document"`
}

type TelegramUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"first_name"`
}

type TelegramChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type TelegramDocument struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size"`
}

type TelegramFileResponse struct {
	OK     bool `json:"ok"`
	Result struct {
		FilePath string `json:"file_path"`
	} `json:"result"`
}

// ============================================================================
// KONFIGURASI & STATE BOT
// ============================================================================

type BotApp struct {
	botToken      string
	allowedChatID int64
	topicID       int
	session       *SessionManager
	currentDir    string
	allowedRoots  []string
	geminiPath    string
	exeDir        string
	logFile       string
}

func main() {
	exePath, _ := os.Executable()
	exeDir := filepath.Dir(exePath)
	loadEnv(exeDir)

	botToken := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	chatIDStr := strings.TrimSpace(os.Getenv("TELEGRAM_ALLOWED_CHAT_ID"))
	if chatIDStr == "" {
		chatIDStr = strings.TrimSpace(os.Getenv("TELEGRAM_CHAT_ID"))
	}
	topicID, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("TELEGRAM_TOPIC_ID")))
	pin := strings.TrimSpace(os.Getenv("AGENT_PIN"))
	if pin == "" {
		pin = "8899"
	}

	timeoutMin, err := strconv.Atoi(strings.TrimSpace(os.Getenv("SESSION_TIMEOUT_MINUTES")))
	if err != nil || timeoutMin <= 0 {
		timeoutMin = 15
	}

	defaultDir := strings.TrimSpace(os.Getenv("DEFAULT_WORKDIR"))
	if defaultDir == "" {
		defaultDir = exeDir
	}

	rootsRaw := strings.TrimSpace(os.Getenv("ALLOWED_ROOTS"))
	if rootsRaw == "" {
		rootsRaw = `D:\;C:\Users\MSI GAMING\Documents;C:\Users\MSI GAMING\Desktop`
	}
	allowedRoots := strings.Split(rootsRaw, ";")

	geminiPath := resolveGeminiCLI(strings.TrimSpace(os.Getenv("GEMINI_CLI_PATH")))

	if botToken == "" || strings.Contains(botToken, "ISI_TOKEN") || chatIDStr == "" || strings.Contains(chatIDStr, "ISI_CHAT_ID") {
		fmt.Println("==================================================================")
		fmt.Println("⚠️  [Telegram Remote Agent] Konfigurasi .env Belum Lengkap!")
		fmt.Println("==================================================================")
		fmt.Println("Silakan buka file .env dan isi:")
		fmt.Println("1. TELEGRAM_BOT_TOKEN (dari @BotFather)")
		fmt.Println("2. TELEGRAM_ALLOWED_CHAT_ID (ID Telegram pribadi Anda dari @userinfobot)")
		fmt.Println("3. AGENT_PIN (PIN rahasia untuk /unlock)")
		os.Exit(1)
	}

	allowedChatID, err := strconv.ParseInt(chatIDStr, 10, 64)
	if err != nil {
		fmt.Printf("❌ TELEGRAM_ALLOWED_CHAT_ID harus berupa angka (contoh: 123456789), saat ini: %s\n", chatIDStr)
		os.Exit(1)
	}

	// Pastikan folder default valid
	if info, err := os.Stat(defaultDir); err != nil || !info.IsDir() {
		defaultDir = exeDir
	}

	app := &BotApp{
		botToken:      botToken,
		allowedChatID: allowedChatID,
		topicID:       topicID,
		session:       NewSessionManager(pin, time.Duration(timeoutMin)*time.Minute),
		currentDir:    defaultDir,
		allowedRoots:  allowedRoots,
		geminiPath:    geminiPath,
		exeDir:        exeDir,
		logFile:       filepath.Join(exeDir, "agent-activity.log"),
	}

	app.writeLog("🚀 Telegram Remote Agent dimulai. Folder kerja awal: %s (TopicID: %d)", app.currentDir, app.topicID)
	fmt.Println("==================================================================")
	fmt.Println("🤖 TELEGRAM REMOTE AGENT AKTIF & SIAP MENERIMA PERINTAH")
	fmt.Println("==================================================================")
	fmt.Printf("💬 Chat ID Grup   : %d (Topik ID: %d)\n", app.allowedChatID, app.topicID)
	fmt.Printf("📂 Folder Aktif   : %s\n", app.currentDir)
	fmt.Printf("🛡️ Allowed Roots  : %s\n", strings.Join(app.allowedRoots, " | "))
	fmt.Printf("⏱️ Auto-Lock PIN  : %d Menit\n", timeoutMin)
	fmt.Printf("🧠 Gemini CLI     : %s\n", app.geminiPath)
	fmt.Println("------------------------------------------------------------------")

	// 1. Buang pesan lama yang tertunda saat laptop mati (Keamanan Anti-Perintah Basi)
	offset := app.flushPendingUpdates()

	// 2. Kirim pesan sapaan saat laptop menyala (jika topicID sudah diketahui atau di chat pribadi)
	if app.topicID > 0 || app.allowedChatID > 0 {
		startupMsg := fmt.Sprintf(
			"🟢 <b>Laptop Menyala — Remote Agent Siap!</b>\n"+
				"━━━━━━━━━━━━━━━━━━━━━━\n"+
				"🔒 <b>Status Sesi:</b> <code>TERKUNCI (LOCKED)</code>\n"+
				"📂 <b>Folder Aktif:</b> <code>%s</code>\n\n"+
				"💡 Ketik <code>/unlock &lt;PIN&gt;</code> untuk membuka kunci sesi selama %d menit.\n"+
				"<i>(Pesan PIN Anda akan otomatis dihapus dari layar chat demi keamanan).</i>",
			escapeHTML(app.currentDir), timeoutMin,
		)
		_ = app.sendMessage(app.allowedChatID, startupMsg)
	}

	// 3. Jalankan Long-Polling Loop
	for {
		updates, err := app.getUpdates(offset, 25)
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}

		for _, u := range updates {
			offset = u.UpdateID + 1
			if u.Message != nil {
				app.handleMessage(u.Message)
			}
		}
	}
}

// handleMessage memproses setiap pesan masuk dari Telegram dengan pengaman berlapis
func (app *BotApp) handleMessage(msg *TelegramMessage) {
	now := time.Now()

	// LAPIS KEAMANAN 0: Whitelist Chat ID (Hanya grup/chat milik pemilik asli yang dilayani)
	if msg.Chat.ID != app.allowedChatID {
		app.writeLog("⛔ Percobaan akses ditolak dari ChatID asing: %d (User: %v)", msg.Chat.ID, msg.From)
		return
	}

	// FILTER TOPIK SUPERGROUP (Agar terpisah rapi dari topik # Leon Git Notif)
	if !isTopicAllowed(msg.MessageThreadID, app.topicID) {
		return
	}

	// AUTO-DETECT TOPIK ID: Jika TELEGRAM_TOPIC_ID di .env masih kosong dan Anda chat di topik "💻 Remote Agent",
	// maka bot otomatis mengunci dirinya ke Topik ID tersebut dan menyimpannya ke file .env!
	if app.topicID == 0 && msg.MessageThreadID > 0 {
		app.topicID = msg.MessageThreadID
		app.saveTopicIDToEnv(app.topicID)
		app.writeLog("📌 Otomatis mengunci TELEGRAM_TOPIC_ID ke: %d", app.topicID)
	}

	text := strings.TrimSpace(msg.Text)
	if text == "" && msg.Caption != "" {
		text = strings.TrimSpace(msg.Caption)
	}
	text = stripBotMentionInCommand(text)

	// LAPIS KEAMANAN 1: Perintah /unlock <PIN> + Auto-Delete Pesan PIN dari Chat Telegram
	if strings.HasPrefix(strings.ToLower(text), "/unlock") {
		// Langsung hapus pesan yang berisi PIN dari layar Telegram agar tidak bisa diintip!
		_ = app.deleteMessage(msg.Chat.ID, msg.MessageID)

		parts := strings.Fields(text)
		if len(parts) < 2 {
			_ = app.sendMessage(msg.Chat.ID, "⚠️ Format: <code>/unlock PIN_ANDA</code>\n<i>(Pesan Anda barusan sudah dihapus otomatis).</i>")
			return
		}

		inputPIN := parts[1]
		if app.session.Unlock(inputPIN, now) {
			app.writeLog("🔓 Sesi berhasil dibuka dengan PIN.")
			_ = app.sendMessage(msg.Chat.ID, fmt.Sprintf(
				"🔓 <b>Sesi Berhasil Dibuka!</b>\n"+
					"🗑️ <i>Pesan PIN Anda sudah dihapus otomatis dari riwayat chat.</i>\n"+
					"━━━━━━━━━━━━━━━━━━━━━━\n"+
					"📂 <b>Folder Aktif:</b> <code>%s</code>\n"+
					"⏱️ <b>Auto-Lock:</b> %d menit sejak pesan terakhir\n\n"+
					"Silakan kirim perintah langsung (contoh: <i>\"Buat file rekap.xlsx...\"</i>) atau ketik <code>/help</code> untuk daftar navigasi folder.",
				escapeHTML(app.currentDir), app.session.RemainingMinutes(now),
			))
		} else {
			app.writeLog("❌ Gagal unlock: PIN salah.")
			_ = app.sendMessage(msg.Chat.ID, "❌ <b>PIN Salah!</b> Sesi tetap terkunci (🗑️ Pesan PIN sudah dihapus).")
		}
		return
	}

	// Perintah /lock bisa dijalankan kapan saja
	if strings.EqualFold(text, "/lock") {
		app.session.Lock()
		app.writeLog("🔒 Sesi dikunci manual oleh user.")
		_ = app.sendMessage(msg.Chat.ID, "🔒 <b>Sesi Dikunci Kembali.</b>\nKetik <code>/unlock &lt;PIN&gt;</code> jika ingin menggunakan Agent lagi.")
		return
	}

	// Perintah /start atau /help
	if strings.EqualFold(text, "/start") || strings.EqualFold(text, "/help") {
		status := "🔒 TERKUNCI"
		if app.session.IsUnlocked(now) {
			status = fmt.Sprintf("🔓 TERBUKA (Sisa %d menit)", app.session.RemainingMinutes(now))
		}
		helpMsg := fmt.Sprintf(
			"🤖 <b>Panduan Telegram Remote Agent</b>\n"+
				"━━━━━━━━━━━━━━━━━━━━━━\n"+
				"🔐 <b>Status Sesi:</b> %s\n"+
				"📂 <b>Folder Aktif:</b> <code>%s</code>\n\n"+
				"<b>Perintah Keamanan:</b>\n"+
				"• <code>/unlock &lt;PIN&gt;</code> — Buka kunci sesi (pesan PIN otomatis dihapus)\n"+
				"• <code>/lock</code> — Kunci sesi sekarang juga\n\n"+
				"<b>Perintah Navigasi &amp; File:</b>\n"+
				"• <code>/pwd</code> — Lihat lokasi folder aktif saat ini\n"+
				"• <code>/ls</code> — Lihat isi file &amp; sub-folder di lokasi aktif\n"+
				"• <code>/cd &lt;path&gt;</code> — Pindah ke folder lain (misal: <code>/cd D:\\Dokumen</code> atau <code>/cd ..</code>)\n"+
				"• <code>/ambil &lt;nama_file&gt;</code> — Download/kirim file dari laptop ke chat Telegram ini\n"+
				"• <code>/buka &lt;nama_file&gt;</code> — Buka tampilkan file langsung di layar laptop\n\n"+
				"<b>Perintah Eksekusi AI Agent:</b>\n"+
				"Cukup ketik perintah normal (tanpa garis miring), misalnya:\n"+
				"• <i>\"Rapikan kolom di file Laporan.xlsx dan buat header tabel warna merah\"</i>\n"+
				"• <i>\"Buat catatan rapat di Rapat.md berisi...\"</i>\n"+
				"• Atau <b>upload/teruskan file Excel/Word/MD</b> ke chat ini beserta instruksi di caption-nya!",
			status, escapeHTML(app.currentDir),
		)
		_ = app.sendMessage(msg.Chat.ID, helpMsg)
		return
	}

	// Cek apakah sesi sudah di-unlock
	if !app.session.IsUnlocked(now) {
		_ = app.sendMessage(msg.Chat.ID,
			"🔒 <b>Sesi Masih Terkunci (atau Sudah Expired).</b>\n"+
				"Ketik <code>/unlock &lt;PIN&gt;</code> terlebih dahulu untuk menjalankan perintah.\n"+
				"<i>(Fitur pengaman anti-maling aktif).</i>")
		return
	}

	// Perbarui waktu aktif sesi (Touch)
	app.session.Touch(now)

	// Jika user mengirim/upload file (Document) dari HP ke Laptop
	if msg.Document != nil {
		app.handleDocumentUpload(msg)
		return
	}

	// Navigasi Folder: /pwd
	if strings.EqualFold(text, "/pwd") {
		_ = app.sendMessage(msg.Chat.ID, fmt.Sprintf(
			"📂 <b>Folder Kerja Saat Ini:</b>\n<code>%s</code>\n\n⏱️ Sisa waktu sesi: %d menit.",
			escapeHTML(app.currentDir), app.session.RemainingMinutes(now),
		))
		return
	}

	// Navigasi Folder: /ls
	if strings.EqualFold(text, "/ls") || strings.EqualFold(text, "/dir") {
		app.handleListFiles(msg.Chat.ID)
		return
	}

	// Navigasi Folder: /cd <path>
	if strings.HasPrefix(strings.ToLower(text), "/cd ") || strings.EqualFold(text, "/cd") {
		target := strings.TrimSpace(text[3:])
		app.handleChangeDir(msg.Chat.ID, target)
		return
	}

	// Ambil File ke Telegram: /ambil <nama_file>
	if strings.HasPrefix(strings.ToLower(text), "/ambil ") {
		fileName := strings.TrimSpace(text[7:])
		app.handleSendFile(msg.Chat.ID, fileName)
		return
	}

	// Buka File di Layar Laptop: /buka <nama_file>
	if strings.HasPrefix(strings.ToLower(text), "/buka ") {
		fileName := strings.TrimSpace(text[6:])
		app.handleOpenFileOnLaptop(msg.Chat.ID, fileName)
		return
	}

	// Selain command di atas -> Eksekusi sebagai Prompt ke AI Agent!
	if text != "" {
		app.handleAgentPrompt(msg.Chat.ID, text)
	}
}

// handleListFiles menampilkan isi folder kerja aktif saat ini
func (app *BotApp) handleListFiles(chatID int64) {
	entries, err := os.ReadDir(app.currentDir)
	if err != nil {
		_ = app.sendMessage(chatID, fmt.Sprintf("❌ Gagal membaca folder: <code>%s</code>", escapeHTML(err.Error())))
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📂 <b>Isi Folder:</b> <code>%s</code>\n", escapeHTML(app.currentDir)))
	sb.WriteString("━━━━━━━━━━━━━━━━━━━━━━\n")

	if len(entries) == 0 {
		sb.WriteString("<i>(Folder kosong)</i>")
	} else {
		count := 0
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".agent-backup") {
				continue
			}
			count++
			if count > 45 {
				sb.WriteString(fmt.Sprintf("\n<i>...dan %d item lainnya.</i>", len(entries)-45))
				break
			}
			if entry.IsDir() {
				sb.WriteString(fmt.Sprintf("📁 <code>%s/</code>\n", escapeHTML(entry.Name())))
			} else {
				info, _ := entry.Info()
				sizeStr := ""
				if info != nil {
					sizeStr = fmt.Sprintf(" <i>(%s)</i>", formatByteSize(info.Size()))
				}
				sb.WriteString(fmt.Sprintf("📄 <code>%s</code>%s\n", escapeHTML(entry.Name()), sizeStr))
			}
		}
	}

	sb.WriteString("━━━━━━━━━━━━━━━━━━━━━━\n")
	sb.WriteString("💡 Tips: Ketik <code>/cd nama_folder</code> untuk masuk, atau langsung suruh Agent mengedit file di atas.")
	_ = app.sendMessage(chatID, sb.String())
}

// handleChangeDir memindahkan folder aktif selama masih di dalam ALLOWED_ROOTS
func (app *BotApp) handleChangeDir(chatID int64, target string) {
	if target == "" {
		_ = app.sendMessage(chatID, fmt.Sprintf(
			"📂 Folder saat ini: <code>%s</code>\n\nContoh pindah folder:\n• <code>/cd ..</code> (naik 1 folder)\n• <code>/cd NamaSubFolder</code>\n• <code>/cd D:\\Project Sistem</code>",
			escapeHTML(app.currentDir),
		))
		return
	}

	target = strings.Trim(target, `"'`)
	var newPath string
	if filepath.IsAbs(target) {
		newPath = filepath.Clean(target)
	} else {
		newPath = filepath.Clean(filepath.Join(app.currentDir, target))
	}

	if !isPathAllowed(newPath, app.allowedRoots) {
		app.writeLog("⛔ Percobaan pindah ke luar pagar aman ditolak: %s", newPath)
		_ = app.sendMessage(chatID, fmt.Sprintf(
			"⛔ <b>Akses Folder Ditolak (Di Luar Pagar Aman)!</b>\n"+
				"Target: <code>%s</code>\n"+
				"Pagar yang diizinkan: <code>%s</code>",
			escapeHTML(newPath), escapeHTML(strings.Join(app.allowedRoots, " ; ")),
		))
		return
	}

	info, err := os.Stat(newPath)
	if err != nil || !info.IsDir() {
		_ = app.sendMessage(chatID, fmt.Sprintf("❌ Folder tidak ditemukan: <code>%s</code>", escapeHTML(newPath)))
		return
	}

	app.currentDir = newPath
	app.writeLog("📂 Pindah folder aktif ke: %s", app.currentDir)
	_ = app.sendMessage(chatID, fmt.Sprintf("✅ <b>Berhasil Pindah Folder!</b>\n📂 Lokasi aktif sekarang:\n<code>%s</code>", escapeHTML(app.currentDir)))
	app.handleListFiles(chatID)
}

// handleSendFile mengirimkan file dari laptop ke chat Telegram user
func (app *BotApp) handleSendFile(chatID int64, fileName string) {
	fileName = strings.Trim(fileName, `"'`)
	var fullPath string
	if filepath.IsAbs(fileName) {
		fullPath = filepath.Clean(fileName)
	} else {
		fullPath = filepath.Clean(filepath.Join(app.currentDir, fileName))
	}

	if !isPathAllowed(fullPath, app.allowedRoots) {
		_ = app.sendMessage(chatID, "⛔ File berada di luar folder yang diizinkan.")
		return
	}

	info, err := os.Stat(fullPath)
	if err != nil || info.IsDir() {
		_ = app.sendMessage(chatID, fmt.Sprintf("❌ File tidak ditemukan: <code>%s</code>", escapeHTML(fullPath)))
		return
	}

	_ = app.sendMessage(chatID, fmt.Sprintf("📤 Mengirim file <code>%s</code> ke Telegram Anda...", escapeHTML(filepath.Base(fullPath))))
	if err := app.sendDocument(chatID, fullPath, fmt.Sprintf("📄 File dari laptop: %s", filepath.Base(fullPath))); err != nil {
		_ = app.sendMessage(chatID, fmt.Sprintf("❌ Gagal mengirim file: %s", escapeHTML(err.Error())))
	}
}

// handleOpenFileOnLaptop membuka file langsung di layar desktop laptop menggunakan aplikasi bawaan Windows (Excel/Word/Notepad)
func (app *BotApp) handleOpenFileOnLaptop(chatID int64, fileName string) {
	fileName = strings.Trim(fileName, `"'`)
	var fullPath string
	if filepath.IsAbs(fileName) {
		fullPath = filepath.Clean(fileName)
	} else {
		fullPath = filepath.Clean(filepath.Join(app.currentDir, fileName))
	}

	if !isPathAllowed(fullPath, app.allowedRoots) {
		_ = app.sendMessage(chatID, "⛔ File berada di luar folder yang diizinkan.")
		return
	}

	info, err := os.Stat(fullPath)
	if err != nil || info.IsDir() {
		_ = app.sendMessage(chatID, fmt.Sprintf("❌ File tidak ditemukan: <code>%s</code>", escapeHTML(fullPath)))
		return
	}

	cmd := exec.Command("cmd", "/c", "start", "", fullPath)
	cmd.Dir = app.currentDir
	if err := cmd.Start(); err != nil {
		_ = app.sendMessage(chatID, fmt.Sprintf("❌ Gagal membuka file di layar laptop: %s", escapeHTML(err.Error())))
		return
	}

	app.writeLog("🖥️ Membuka file di layar laptop: %s", fullPath)
	_ = app.sendMessage(chatID, fmt.Sprintf("🖥️ <b>File Dibuka di Layar Laptop!</b>\n<code>%s</code> sekarang sudah tampil di layar laptop.", escapeHTML(filepath.Base(fullPath))))
}

// handleDocumentUpload menerima file yang dikirim user lewat Telegram, menyimpannya ke folder aktif, dan mengeksekusi caption jika ada
func (app *BotApp) handleDocumentUpload(msg *TelegramMessage) {
	doc := msg.Document
	safeName := filepath.Base(doc.FileName)
	destPath := filepath.Join(app.currentDir, safeName)

	_ = app.sendMessage(msg.Chat.ID, fmt.Sprintf("📥 Menerima file <code>%s</code>, menyimpan ke folder <code>%s</code>...", escapeHTML(safeName), escapeHTML(app.currentDir)))

	if err := app.downloadTelegramFile(doc.FileID, destPath); err != nil {
		_ = app.sendMessage(msg.Chat.ID, fmt.Sprintf("❌ Gagal mengunduh file dari Telegram: %s", escapeHTML(err.Error())))
		return
	}

	app.writeLog("📥 File disimpan dari Telegram: %s", destPath)

	caption := strings.TrimSpace(msg.Caption)
	if caption == "" {
		_ = app.sendMessage(msg.Chat.ID, fmt.Sprintf(
			"✅ <b>File Berhasil Disimpan di Laptop!</b>\nLokasi: <code>%s</code>\n\nKetik instruksi Anda jika ingin Agent mengedit file ini.",
			escapeHTML(destPath),
		))
		return
	}

	// Jika user menyertakan caption (misal: "Rapikan kolom di file ini"), langsung jalankan Agent!
	fullPrompt := fmt.Sprintf("Pada file %q yang baru saja disimpan di folder kerja saat ini: %s", safeName, caption)
	app.handleAgentPrompt(msg.Chat.ID, fullPrompt)
}

// handleAgentPrompt mengeksekusi perintah bahasa alami menggunakan Gemini CLI di folder kerja saat ini
func (app *BotApp) handleAgentPrompt(chatID int64, userPrompt string) {
	if blocked, reason := isDestructivePrompt(userPrompt); blocked {
		app.writeLog("⛔ Prompt destruktif diblokir: %s (%s)", userPrompt, reason)
		_ = app.sendMessage(chatID, fmt.Sprintf("⛔ <b>Perintah Diblokir oleh Sabuk Pengaman:</b>\n%s", escapeHTML(reason)))
		return
	}

	app.writeLog("🤖 Mengeksekusi prompt di [%s]: %s", app.currentDir, userPrompt)

	_ = app.sendMessage(chatID, fmt.Sprintf(
		"⏳ <b>Agent Sedang Bekerja di Balik Layar...</b>\n"+
			"📂 <b>Folder:</b> <code>%s</code>\n"+
			"💬 <b>Perintah:</b> <i>%s</i>\n\n"+
			"<i>Mohon tunggu sebentar, notifikasi akan dikirim begitu selesai.</i>",
		escapeHTML(app.currentDir), escapeHTML(userPrompt),
	))

	// 1. Catat snapshot waktu modifikasi file sebelum eksekusi & buat backup otomatis untuk file dokumen kerja
	beforeSnapshot := app.snapshotAndBackupDir(app.currentDir)
	startTime := time.Now()

	// 2. Susun prompt terstruktur untuk Gemini CLI
	systemContextPrompt := fmt.Sprintf(
		"Kamu adalah Remote AI Agent yang bekerja di komputer Windows milik user pada direktori kerja: %s.\n"+
			"ATURAN PENTING:\n"+
			"1. Kerjakan instruksi user secara langsung hingga tuntas pada file di direktori ini.\n"+
			"2. Jika diminta membuat atau mengedit file Excel (.xlsx), kamu BISA menjalankan script Python singkat menggunakan library `openpyxl` (sudah terinstal di Python sistem) untuk mengatur warna sel/header (PatternFill), font, border, rumus, dan auto-fit lebar kolom.\n"+
			"3. Jika diminta membuat atau mengedit file Word (.docx), gunakan library Python `python-docx` (sudah terinstal).\n"+
			"4. Jika diminta membuat/mengedit file Markdown (.md), Note (.txt), CSV, HTML, atau kode program, edit langsung filenya dengan rapi.\n"+
			"5. Setelah memodifikasi dengan script Python sementara, hapus file script Python sementara jika tidak diperlukan lagi agar folder user tetap bersih.\n"+
			"6. DILARANG menghapus file user yang tidak diminta atau menyentuh folder sistem di luar %s.\n"+
			"7. Jawab dengan ringkas dalam Bahasa Indonesia apa saja perubahan yang sudah berhasil kamu lakukan.\n\n"+
			"INSTRUKSI USER:\n%s",
		app.currentDir, app.currentDir, userPrompt,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var cmd *exec.Cmd
	if strings.HasSuffix(strings.ToLower(app.geminiPath), ".ps1") {
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", app.geminiPath, "-p", systemContextPrompt, "-y", "--skip-trust")
	} else {
		cmd = exec.CommandContext(ctx, app.geminiPath, "-p", systemContextPrompt, "-y", "--skip-trust")
	}
	cmd.Dir = app.currentDir
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	outBytes, err := cmd.CombinedOutput()
	duration := time.Since(startTime).Round(time.Second)
	cleanOutput := cleanGeminiOutput(string(outBytes))

	// 3. Cek file apa saja yang baru dibuat atau berubah
	changedFiles := app.detectChangedFiles(app.currentDir, beforeSnapshot)

	if err != nil {
		app.writeLog("❌ Error eksekusi Agent (%v): %s", err, cleanOutput)
		errMsg := fmt.Sprintf(
			"⚠️ <b>Eksekusi Selesai dengan Catatan Error (%s)</b>\n"+
				"━━━━━━━━━━━━━━━━━━━━━━\n"+
				"<pre>%s</pre>",
			duration, escapeHTML(truncateText(cleanOutput, 2500)),
		)
		_ = app.sendMessage(chatID, errMsg)
		return
	}

	app.writeLog("✅ Eksekusi selesai dalam %s. File berubah: %v", duration, changedFiles)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("✅ <b>Eksekusi Selesai! (%s)</b>\n", duration))
	sb.WriteString("━━━━━━━━━━━━━━━━━━━━━━\n")
	sb.WriteString(fmt.Sprintf("📂 <b>Folder:</b> <code>%s</code>\n", escapeHTML(app.currentDir)))

	if len(changedFiles) > 0 {
		sb.WriteString("\n📝 <b>File yang Dibuat / Diperbarui:</b>\n")
		for _, f := range changedFiles {
			sb.WriteString(fmt.Sprintf("• <code>%s</code>\n", escapeHTML(f)))
		}
	}

	if cleanOutput != "" {
		sb.WriteString("\n💬 <b>Laporan Agent:</b>\n")
		sb.WriteString(escapeHTML(truncateText(cleanOutput, 2500)))
		sb.WriteString("\n")
	}

	sb.WriteString("━━━━━━━━━━━━━━━━━━━━━━\n")
	sb.WriteString("💡 Ketik <code>/buka nama_file</code> untuk menampilkannya di layar laptop, atau <code>/ambil nama_file</code> untuk mengunduhnya ke HP.")

	_ = app.sendMessage(chatID, sb.String())

	// Otomatis kirimkan salinan file dokumen yang baru diedit (maksimal 3 file ukuran < 15MB) langsung ke Telegram user!
	sentCount := 0
	for _, f := range changedFiles {
		if sentCount >= 3 {
			break
		}
		ext := strings.ToLower(filepath.Ext(f))
		if ext == ".xlsx" || ext == ".docx" || ext == ".md" || ext == ".txt" || ext == ".csv" || ext == ".pdf" {
			fullPath := filepath.Join(app.currentDir, f)
			if info, err := os.Stat(fullPath); err == nil && !info.IsDir() && info.Size() < 15*1024*1024 {
				_ = app.sendDocument(chatID, fullPath, fmt.Sprintf("📎 Hasil terbaru: %s", f))
				sentCount++
			}
		}
	}
}

// snapshotAndBackupDir mencatat modtime file di folder aktif & mem-backup file dokumen sebelum diedit Agent
func (app *BotApp) snapshotAndBackupDir(dir string) map[string]time.Time {
	snapshot := make(map[string]time.Time)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return snapshot
	}

	backupDir := filepath.Join(dir, ".agent-backup")
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		snapshot[entry.Name()] = info.ModTime()

		// Backup otomatis untuk dokumen penting yang berukuran < 20MB
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if (ext == ".xlsx" || ext == ".docx" || ext == ".md" || ext == ".txt" || ext == ".csv") && info.Size() < 20*1024*1024 {
			_ = os.MkdirAll(backupDir, 0755)
			src := filepath.Join(dir, entry.Name())
			dst := filepath.Join(backupDir, entry.Name()+".bak")
			_ = copyFile(src, dst)
		}
	}
	return snapshot
}

// detectChangedFiles membandingkan kondisi folder setelah Agent selesai bekerja
func (app *BotApp) detectChangedFiles(dir string, before map[string]time.Time) []string {
	var changed []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return changed
	}

	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".agent-backup") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		oldTime, existed := before[entry.Name()]
		if !existed || info.ModTime().After(oldTime) {
			changed = append(changed, entry.Name())
		}
	}
	return changed
}

// cleanGeminiOutput membersihkan warning internal Node/Gemini CLI yang tidak relevan bagi user
func cleanGeminiOutput(raw string) string {
	lines := strings.Split(raw, "\n")
	var clean []string
	skipStack := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "Critical failure reading project registry") ||
			strings.Contains(trimmed, "Early cleanup failed") ||
			strings.Contains(trimmed, "Tool output cleanup failed") ||
			strings.Contains(trimmed, "YOLO mode is enabled") ||
			strings.Contains(trimmed, "Loaded cached credentials") {
			skipStack = true
			continue
		}
		if skipStack {
			if strings.HasPrefix(trimmed, "at ") ||
				strings.HasPrefix(trimmed, "errno:") ||
				strings.HasPrefix(trimmed, "code:") ||
				strings.HasPrefix(trimmed, "syscall:") ||
				strings.HasPrefix(trimmed, "path:") ||
				trimmed == "}" {
				continue
			}
			skipStack = false
		}
		clean = append(clean, line)
	}
	return strings.TrimSpace(strings.Join(clean, "\n"))
}

// ============================================================================
// HELPER TELEGRAM HTTP API
// ============================================================================

func (app *BotApp) flushPendingUpdates() int {
	updates, err := app.getUpdates(0, 1)
	if err != nil || len(updates) == 0 {
		return 0
	}
	lastID := updates[len(updates)-1].UpdateID
	// Konfirmasi ke server Telegram bahwa semua pesan lama sudah di-drop
	_, _ = app.getUpdates(lastID+1, 0)
	return lastID + 1
}

func (app *BotApp) getUpdates(offset int, timeoutSec int) ([]TelegramUpdate, error) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?offset=%d&timeout=%d", app.botToken, offset, timeoutSec)
	client := &http.Client{Timeout: time.Duration(timeoutSec+10) * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var parsed TelegramUpdateResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	return parsed.Result, nil
}

func (app *BotApp) sendMessage(chatID int64, text string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", app.botToken)
	payload := map[string]interface{}{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	if app.topicID > 0 {
		payload["message_thread_id"] = app.topicID
	}
	body, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (app *BotApp) deleteMessage(chatID int64, messageID int) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/deleteMessage", app.botToken)
	payload := map[string]interface{}{
		"chat_id":    chatID,
		"message_id": messageID,
	}
	body, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (app *BotApp) sendDocument(chatID int64, filePath string, caption string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	_ = writer.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	if app.topicID > 0 {
		_ = writer.WriteField("message_thread_id", strconv.Itoa(app.topicID))
	}
	if caption != "" {
		_ = writer.WriteField("caption", caption)
	}

	part, err := writer.CreateFormFile("document", filepath.Base(filePath))
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, file); err != nil {
		return err
	}
	writer.Close()

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendDocument", app.botToken)
	req, err := http.NewRequest("POST", url, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (app *BotApp) downloadTelegramFile(fileID string, destPath string) error {
	infoURL := fmt.Sprintf("https://api.telegram.org/bot%s/getFile?file_id=%s", app.botToken, fileID)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(infoURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var fileResp TelegramFileResponse
	if err := json.NewDecoder(resp.Body).Decode(&fileResp); err != nil || !fileResp.OK {
		return fmt.Errorf("gagal mendapatkan metadata file dari Telegram")
	}

	downloadURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", app.botToken, fileResp.Result.FilePath)
	dlResp, err := client.Get(downloadURL)
	if err != nil {
		return err
	}
	defer dlResp.Body.Close()

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, dlResp.Body)
	return err
}

// ============================================================================
// UTILITAS UMUM
// ============================================================================

func resolveGeminiCLI(configured string) string {
	if configured != "" {
		if _, err := os.Stat(configured); err == nil {
			return configured
		}
	}
	appData := os.Getenv("APPDATA")
	candidates := []string{
		filepath.Join(appData, "npm", "gemini.cmd"),
		filepath.Join(appData, "npm", "gemini.ps1"),
		`C:\Users\MSI GAMING\AppData\Roaming\npm\gemini.cmd`,
		`C:\Users\MSI GAMING\AppData\Roaming\npm\gemini.ps1`,
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return "gemini"
}

func loadEnv(exeDir string) {
	paths := []string{
		filepath.Join(exeDir, ".env"),
		".env",
	}
	for _, p := range paths {
		content, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(content), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				k := strings.TrimSpace(parts[0])
				v := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
				if os.Getenv(k) == "" {
					_ = os.Setenv(k, v)
				}
			}
		}
		break
	}
}

func (app *BotApp) writeLog(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	line := fmt.Sprintf("[%s] %s\n", timestamp, msg)
	fmt.Print(line)

	f, err := os.OpenFile(app.logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err == nil {
		defer f.Close()
		_, _ = f.WriteString(line)
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func truncateText(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "\n... (dipotong karena terlalu panjang)"
}

func formatByteSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// stripBotMentionInCommand mengubah "/ls@NamaBot arg" menjadi "/ls arg" saat di grup
func stripBotMentionInCommand(text string) string {
	if !strings.HasPrefix(text, "/") {
		return text
	}
	parts := strings.SplitN(text, " ", 2)
	cmdPart := parts[0]
	if atIdx := strings.Index(cmdPart, "@"); atIdx != -1 {
		cmdPart = cmdPart[:atIdx]
	}
	if len(parts) == 2 {
		return cmdPart + " " + parts[1]
	}
	return cmdPart
}

// saveTopicIDToEnv menyimpan TELEGRAM_TOPIC_ID yang terdeteksi otomatis ke file .env
func (app *BotApp) saveTopicIDToEnv(topicID int) {
	envPath := filepath.Join(app.exeDir, ".env")
	contentBytes, err := os.ReadFile(envPath)
	if err != nil {
		return
	}
	content := string(contentBytes)
	newLine := fmt.Sprintf("TELEGRAM_TOPIC_ID=%d", topicID)
	if strings.Contains(content, "TELEGRAM_TOPIC_ID=") {
		lines := strings.Split(content, "\n")
		for i, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), "TELEGRAM_TOPIC_ID=") {
				lines[i] = newLine
			}
		}
		content = strings.Join(lines, "\n")
	} else {
		content = strings.TrimRight(content, "\r\n") + "\n" + newLine + "\n"
	}
	_ = os.WriteFile(envPath, []byte(content), 0644)
}
