package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// ---- loadConfig / validateConfig ----

func TestLoadConfig_ValidFile(t *testing.T) {
	content := `
monitor:
  folder: /tmp/test
  min_age_minutes: 5
  max_age_minutes: 60
  check_interval_minutes: 10
telegram:
  bot_token: "token123"
  chat_id: "chat456"
`
	f := writeTempFile(t, content)
	cfg, err := loadConfig(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Monitor.Folder != "/tmp/test" {
		t.Errorf("folder: got %q, want /tmp/test", cfg.Monitor.Folder)
	}
	if cfg.Monitor.MinAgeMinutes != 5 {
		t.Errorf("min_age_minutes: got %d, want 5", cfg.Monitor.MinAgeMinutes)
	}
	if cfg.Monitor.MaxAgeMinutes != 60 {
		t.Errorf("max_age_minutes: got %d, want 60", cfg.Monitor.MaxAgeMinutes)
	}
	if cfg.Monitor.CheckIntervalMinutes != 10 {
		t.Errorf("check_interval_minutes: got %d, want 10", cfg.Monitor.CheckIntervalMinutes)
	}
	if cfg.Telegram.BotToken != "token123" {
		t.Errorf("bot_token: got %q, want token123", cfg.Telegram.BotToken)
	}
	if cfg.Telegram.ChatID != "chat456" {
		t.Errorf("chat_id: got %q, want chat456", cfg.Telegram.ChatID)
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	_, err := loadConfig("/nonexistent/config.yml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	f := writeTempFile(t, "{unclosed: [bracket")
	_, err := loadConfig(f)
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

func TestValidateConfig(t *testing.T) {
	valid := &Config{
		Monitor: MonitorConfig{
			Folder:               "/tmp",
			MinAgeMinutes:        5,
			MaxAgeMinutes:        60,
			CheckIntervalMinutes: 10,
		},
		Telegram: TelegramConfig{
			BotToken: "tok",
			ChatID:   "123",
		},
	}

	if err := validateConfig(valid); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"empty folder", func(c *Config) { c.Monitor.Folder = "" }},
		{"negative min age", func(c *Config) { c.Monitor.MinAgeMinutes = -1 }},
		{"zero max age", func(c *Config) { c.Monitor.MaxAgeMinutes = 0 }},
		{"min >= max", func(c *Config) { c.Monitor.MinAgeMinutes = 60; c.Monitor.MaxAgeMinutes = 60 }},
		{"zero interval", func(c *Config) { c.Monitor.CheckIntervalMinutes = 0 }},
		{"empty bot token", func(c *Config) { c.Telegram.BotToken = "" }},
		{"empty chat id", func(c *Config) { c.Telegram.ChatID = "" }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Deep copy so mutations don't bleed between cases.
			cfg := *valid
			cfg.Monitor = valid.Monitor
			cfg.Telegram = valid.Telegram
			tc.mutate(&cfg)
			if err := validateConfig(&cfg); err == nil {
				t.Errorf("expected error for case %q, got nil", tc.name)
			}
		})
	}
}

// ---- scanFolder ----

func TestScanFolder_MatchesFilesInWindow(t *testing.T) {
	dir := t.TempDir()

	// Create a file whose modification time is 10 minutes ago.
	old := filepath.Join(dir, "old.log")
	writeFile(t, old, "data")
	tenMinAgo := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(old, tenMinAgo, tenMinAgo); err != nil {
		t.Fatal(err)
	}

	// File is 10 min old → should match window [5 min, 60 min].
	files, err := scanFolder(dir, 5*time.Minute, 60*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Path != old {
		t.Errorf("path: got %q, want %q", files[0].Path, old)
	}
}

func TestScanFolder_ExcludesTooNew(t *testing.T) {
	dir := t.TempDir()
	recent := filepath.Join(dir, "recent.log")
	writeFile(t, recent, "data")
	// Leave the file with its just-created modtime (< 5 min old) → excluded.

	files, err := scanFolder(dir, 5*time.Minute, 60*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 files, got %d", len(files))
	}
}

func TestScanFolder_ExcludesTooOld(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "stale.log")
	writeFile(t, stale, "data")
	veryOld := time.Now().Add(-120 * time.Minute)
	if err := os.Chtimes(stale, veryOld, veryOld); err != nil {
		t.Fatal(err)
	}

	// Window is [5 min, 60 min]; 120 min old → excluded.
	files, err := scanFolder(dir, 5*time.Minute, 60*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 files, got %d", len(files))
	}
}

func TestScanFolder_IgnoresSubfolders(t *testing.T) {
	dir := t.TempDir()

	// Create a file in a subdirectory with a matching mod time.
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	subFile := filepath.Join(sub, "nested.log")
	writeFile(t, subFile, "data")
	tenMinAgo := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(subFile, tenMinAgo, tenMinAgo); err != nil {
		t.Fatal(err)
	}

	// Window [5 min, 60 min] would match the file if subdirs were scanned.
	files, err := scanFolder(dir, 5*time.Minute, 60*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 files (subdir must be ignored), got %d: %v", len(files), files)
	}
}

func TestScanFolder_NonexistentFolder(t *testing.T) {
	_, err := scanFolder("/nonexistent/folder/xyz", 5*time.Minute, 60*time.Minute)
	if err == nil {
		t.Fatal("expected error for nonexistent folder, got nil")
	}
}

// ---- buildMessage ----

func TestBuildMessage_ContainsExpectedFields(t *testing.T) {
	files := []FileInfo{
		{Path: "/var/log/app/server.log", Size: 1024, ModTime: time.Now().Add(-10 * time.Minute)},
	}
	msg := buildMessage(files, "/var/log/app", 5*time.Minute, 60*time.Minute)

	for _, want := range []string{
		"TelegramAlert",
		"/var/log/app",
		"1 matching file",
		"server.log",
		"1024 bytes",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q\nmessage: %s", want, msg)
		}
	}
}

// ---- buildMessage: truncation ----

func TestBuildMessage_TruncatesLongOutput(t *testing.T) {
	var files []FileInfo
	for i := 0; i < 2000; i++ {
		files = append(files, FileInfo{
			Path:    "/var/log/app/" + strings.Repeat("x", 200) + ".log",
			Size:    12345,
			ModTime: time.Now().Add(-10 * time.Minute),
		})
	}
	msg := buildMessage(files, "/var/log/app", 5*time.Minute, 60*time.Minute)
	if len(msg) > telegramMaxMessageBytes {
		t.Fatalf("message length %d exceeds limit %d", len(msg), telegramMaxMessageBytes)
	}
	if !strings.Contains(msg, "2000 matching file(s)") {
		t.Errorf("expected original file count in header, got: %.100s", msg)
	}
}

func TestTruncateMessage_RuneBoundary(t *testing.T) {
	// A multi-byte rune (🔔 = 4 bytes) placed exactly on the boundary.
	in := strings.Repeat("a", telegramMaxMessageBytes-2) + "🔔"
	out := truncateMessage(in)
	if len(out) > telegramMaxMessageBytes {
		t.Fatalf("output length %d exceeds limit %d", len(out), telegramMaxMessageBytes)
	}
	if !strings.Contains(out, "truncated") {
		t.Errorf("expected truncation marker, got: %s", out)
	}
	// No invalid UTF-8 in output.
	if !utf8.ValidString(out) {
		t.Error("output contains invalid UTF-8")
	}
}

func TestTruncateMessage_NoOpUnderLimit(t *testing.T) {
	in := "short message"
	if got := truncateMessage(in); got != in {
		t.Errorf("expected unchanged message, got %q", got)
	}
}

// ---- runCheck: dedup ----

func TestRunCheck_DeduplicatesFiles(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "dup.log")
	writeFile(t, f, "data")
	tenMinAgo := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(f, tenMinAgo, tenMinAgo); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{
		Monitor: MonitorConfig{
			Folder:               dir,
			MinAgeMinutes:        5,
			MaxAgeMinutes:        60,
			CheckIntervalMinutes: 10,
		},
		Telegram: TelegramConfig{BotToken: "tok", ChatID: "123"},
	}

	// First run: file is new and should be scheduled for an alert, so runCheck
	// must record it in the alerted set regardless of send result. We stub the
	// sender to capture the message and always succeed.
	var sent []string
	sendTelegramMessage = func(_, _, msg string) error {
		sent = append(sent, msg)
		return nil
	}
	defer func() { sendTelegramMessage = sendTelegramMessageImpl }()

	alerted := make(map[fileKey]struct{})
	runCheck(cfg, alerted)
	if len(sent) != 1 {
		t.Fatalf("expected 1 send on first run, got %d", len(sent))
	}
	if len(alerted) != 1 {
		t.Fatalf("expected 1 recorded file, got %d", len(alerted))
	}

	// Second run: same file must be skipped (no new alert).
	runCheck(cfg, alerted)
	if len(sent) != 1 {
		t.Fatalf("expected no additional send on second run, got %d sends", len(sent))
	}
}

func TestRunCheck_RetriesAfterFailedSend(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "retry.log")
	writeFile(t, f, "data")
	tenMinAgo := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(f, tenMinAgo, tenMinAgo); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{
		Monitor: MonitorConfig{
			Folder:               dir,
			MinAgeMinutes:        5,
			MaxAgeMinutes:        60,
			CheckIntervalMinutes: 10,
		},
		Telegram: TelegramConfig{BotToken: "tok", ChatID: "123"},
	}

	var sends int
	sendTelegramMessage = func(_, _, _ string) error {
		sends++
		return errors.New("telegram down")
	}
	defer func() { sendTelegramMessage = sendTelegramMessageImpl }()

	alerted := make(map[fileKey]struct{})

	// First run: send fails, so the file must NOT be recorded as alerted.
	runCheck(cfg, alerted)
	if sends != 1 {
		t.Fatalf("expected 1 failed send, got %d", sends)
	}
	if len(alerted) != 0 {
		t.Fatalf("expected file not recorded after failed send, got %d recorded", len(alerted))
	}

	// Second run: send still fails but the file must be attempted again.
	runCheck(cfg, alerted)
	if sends != 2 {
		t.Fatalf("expected a retry send on second run, got %d sends", sends)
	}
}

func TestRunCheck_PrunesExpiredEntries(t *testing.T) {
	cfg := &Config{
		Monitor: MonitorConfig{
			Folder:               "/does/not/matter",
			MinAgeMinutes:        1,
			MaxAgeMinutes:        10,
			CheckIntervalMinutes: 5,
		},
		Telegram: TelegramConfig{BotToken: "tok", ChatID: "123"},
	}

	// A file whose mtime is well beyond maxAge must be pruned on the next scan.
	alerted := map[fileKey]struct{}{
		{path: "/old/file.log", modNano: time.Now().Add(-2 * time.Hour).UnixNano()}: {},
	}

	// scanFolder will fail since the folder doesn't exist, but the pruning loop
	// runs before scanning returns, so the expired entry should be gone.
	runCheck(cfg, alerted)
	if len(alerted) != 0 {
		t.Fatalf("expected expired entries to be pruned, got %d remaining: %v", len(alerted), alerted)
	}
}

// ---- helpers ----

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "config-*.yml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	return f.Name()
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
