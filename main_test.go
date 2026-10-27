package main

import (
	"testing"
	"time"
)

func TestSessionManager_UnlockAndExpiry(t *testing.T) {
	sm := NewSessionManager("8899", 15*time.Minute)
	now := time.Now()

	// 1. Default harus terkunci (LOCKED)
	if sm.IsUnlocked(now) {
		t.Fatalf("expected session to start locked")
	}

	// 2. PIN salah harus ditolak
	if sm.Unlock("1234", now) {
		t.Fatalf("expected wrong PIN to be rejected")
	}
	if sm.IsUnlocked(now) {
		t.Fatalf("expected session to remain locked after wrong PIN")
	}

	// 3. PIN benar harus membuka kunci
	if !sm.Unlock("8899", now) {
		t.Fatalf("expected correct PIN to unlock session")
	}
	if !sm.IsUnlocked(now.Add(5 * time.Minute)) {
		t.Fatalf("expected session to still be unlocked after 5 minutes")
	}

	// 4. Lewat dari 15 menit harus otomatis terkunci kembali (Auto-Lock)
	if sm.IsUnlocked(now.Add(16 * time.Minute)) {
		t.Fatalf("expected session to auto-lock after 16 minutes")
	}

	// 5. Unlock lagi lalu Lock manual
	sm.Unlock("8899", now)
	sm.Lock()
	if sm.IsUnlocked(now) {
		t.Fatalf("expected session to be locked after manual Lock()")
	}
}

func TestIsPathAllowed(t *testing.T) {
	allowedRoots := []string{
		`D:\`,
		`C:\Users\MSI GAMING\Documents`,
	}

	validPaths := []string{
		`D:\Project Sistem\Project Web Apps\Update AI`,
		`d:\laporan\rekap.xlsx`,
		`C:\Users\MSI GAMING\Documents\Kerja\kontrak.docx`,
	}

	for _, p := range validPaths {
		if !isPathAllowed(p, allowedRoots) {
			t.Errorf("expected path %q to be allowed", p)
		}
	}

	invalidPaths := []string{
		`C:\Windows\System32`,
		`C:\Users\MSI GAMING\AppData\Local`,
		`C:\Users\MSI GAMING\Documents\..\..\..\Windows\System32`,
	}

	for _, p := range invalidPaths {
		if isPathAllowed(p, allowedRoots) {
			t.Errorf("expected path %q to be rejected", p)
		}
	}
}

func TestIsDestructivePrompt(t *testing.T) {
	safePrompts := []string{
		"Rapikan kolom pada file Laporan.xlsx dan buat header tabel berwarna merah",
		"Tolong buatkan ringkasan di file catatan.md",
		"Edit dokumen surat.docx dan ubah font judul menjadi ukuran 14 bold",
	}

	for _, p := range safePrompts {
		if blocked, _ := isDestructivePrompt(p); blocked {
			t.Errorf("expected prompt %q to be safe", p)
		}
	}

	dangerousPrompts := []string{
		"tolong jalankan format c: sekarang",
		"hapus semua file dengan rm -rf /",
		"jalankan del /f /s /q c:\\windows",
	}

	for _, p := range dangerousPrompts {
		if blocked, _ := isDestructivePrompt(p); !blocked {
			t.Errorf("expected prompt %q to be blocked as destructive", p)
		}
	}
}
