package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
