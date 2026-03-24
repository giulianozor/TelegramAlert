package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds the application configuration loaded from config.yml.
type Config struct {
	Monitor  MonitorConfig  `yaml:"monitor"`
	Telegram TelegramConfig `yaml:"telegram"`
}

// MonitorConfig holds folder monitoring settings.
type MonitorConfig struct {
	Folder               string `yaml:"folder"`
	MinAgeMinutes        int    `yaml:"min_age_minutes"`
	MaxAgeMinutes        int    `yaml:"max_age_minutes"`
	CheckIntervalMinutes int    `yaml:"check_interval_minutes"`
}

// TelegramConfig holds Telegram bot settings.
type TelegramConfig struct {
	BotToken string `yaml:"bot_token"`
	ChatID   string `yaml:"chat_id"`
}

// FileInfo holds details about a matched file.
type FileInfo struct {
	Path    string
	Size    int64
	ModTime time.Time
}

// loadConfig reads and parses the YAML configuration file at the given path.
func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %q: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file %q: %w", path, err)
	}
	return &cfg, nil
}

// validateConfig checks that the required configuration values are present and
// that numeric bounds are logically consistent.
func validateConfig(cfg *Config) error {
	if cfg.Monitor.Folder == "" {
		return fmt.Errorf("monitor.folder must not be empty")
	}
	if cfg.Monitor.MinAgeMinutes < 0 {
		return fmt.Errorf("monitor.min_age_minutes must be >= 0")
	}
	if cfg.Monitor.MaxAgeMinutes <= 0 {
		return fmt.Errorf("monitor.max_age_minutes must be > 0")
	}
	if cfg.Monitor.MinAgeMinutes >= cfg.Monitor.MaxAgeMinutes {
		return fmt.Errorf("monitor.min_age_minutes (%d) must be less than monitor.max_age_minutes (%d)",
			cfg.Monitor.MinAgeMinutes, cfg.Monitor.MaxAgeMinutes)
	}
	if cfg.Monitor.CheckIntervalMinutes <= 0 {
		return fmt.Errorf("monitor.check_interval_minutes must be > 0")
	}
	if cfg.Telegram.BotToken == "" {
		return fmt.Errorf("telegram.bot_token must not be empty")
	}
	if cfg.Telegram.ChatID == "" {
		return fmt.Errorf("telegram.chat_id must not be empty")
	}
	return nil
}

// scanFolder returns all regular files directly inside folder (non-recursive)
// whose modification time falls within the window [now - maxAge, now - minAge].
func scanFolder(folder string, minAge, maxAge time.Duration) ([]FileInfo, error) {
	if _, err := os.Stat(folder); err != nil {
		return nil, fmt.Errorf("folder %q: %w", folder, err)
	}

	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil, fmt.Errorf("reading folder %q: %w", folder, err)
	}

	now := time.Now()
	oldest := now.Add(-maxAge)
	newest := now.Add(-minAge)

	var matched []FileInfo
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			log.Printf("warning: cannot access %q: %v", entry.Name(), err)
			continue
		}
		mod := info.ModTime()
		if !mod.Before(oldest) && !mod.After(newest) {
			matched = append(matched, FileInfo{
				Path:    filepath.Join(folder, entry.Name()),
				Size:    info.Size(),
				ModTime: mod,
			})
		}
	}
	return matched, nil
}

// buildMessage formats the list of matched files into a Telegram message string.
func buildMessage(files []FileInfo, folder string, minAge, maxAge time.Duration) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🔔 *TelegramAlert*\n"))
	sb.WriteString(fmt.Sprintf("Folder: `%s`\n", folder))
	sb.WriteString(fmt.Sprintf("Window: %v – %v ago\n\n", minAge, maxAge))
	sb.WriteString(fmt.Sprintf("%d matching file(s):\n", len(files)))
	for _, f := range files {
		age := time.Since(f.ModTime).Round(time.Second)
		sb.WriteString(fmt.Sprintf("• `%s`\n  Size: %d bytes | Age: %v\n", f.Path, f.Size, age))
	}
	return sb.String()
}

// sendTelegramMessage sends a Markdown-formatted message via the Telegram Bot API.
func sendTelegramMessage(botToken, chatID, message string) error {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", url.PathEscape(botToken))

	payload := map[string]string{
		"chat_id":    chatID,
		"text":       message,
		"parse_mode": "Markdown",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshalling payload: %w", err)
	}

	resp, err := http.Post(apiURL, "application/json", bytes.NewReader(body)) //nolint:noctx
	if err != nil {
		return fmt.Errorf("posting to Telegram API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Telegram API returned HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// runCheck performs one scan-and-alert cycle.
func runCheck(cfg *Config) {
	minAge := time.Duration(cfg.Monitor.MinAgeMinutes) * time.Minute
	maxAge := time.Duration(cfg.Monitor.MaxAgeMinutes) * time.Minute

	log.Printf("scanning %q for files modified between %v and %v ago …",
		cfg.Monitor.Folder, minAge, maxAge)

	files, err := scanFolder(cfg.Monitor.Folder, minAge, maxAge)
	if err != nil {
		log.Printf("error scanning folder: %v", err)
		return
	}

	if len(files) == 0 {
		log.Println("no matching files found")
		return
	}

	log.Printf("found %d matching file(s); sending Telegram alert", len(files))
	msg := buildMessage(files, cfg.Monitor.Folder, minAge, maxAge)
	if err := sendTelegramMessage(cfg.Telegram.BotToken, cfg.Telegram.ChatID, msg); err != nil {
		log.Printf("error sending Telegram message: %v", err)
		return
	}
	log.Println("Telegram alert sent successfully")
}

func main() {
	configPath := flag.String("config", "config.yml", "path to configuration file")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}
	if err := validateConfig(cfg); err != nil {
		log.Fatalf("invalid config: %v", err)
	}

	interval := time.Duration(cfg.Monitor.CheckIntervalMinutes) * time.Minute
	log.Printf("TelegramAlert started – monitoring %q every %v", cfg.Monitor.Folder, interval)

	// Run an immediate check, then repeat on the configured interval.
	runCheck(cfg)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Wait for a termination signal so the process can be managed by init.d.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case <-ticker.C:
			runCheck(cfg)
		case sig := <-sigCh:
			log.Printf("received signal %v; shutting down", sig)
			return
		}
	}
}
