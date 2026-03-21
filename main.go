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
	"strconv"
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

// scanFolder returns all regular files inside folder whose modification time
// falls within the window [now - maxAge, now - minAge].
func scanFolder(folder string, minAge, maxAge time.Duration) ([]FileInfo, error) {
	if _, err := os.Stat(folder); err != nil {
		return nil, fmt.Errorf("folder %q: %w", folder, err)
	}

	now := time.Now()
	oldest := now.Add(-maxAge)
	newest := now.Add(-minAge)

	var matched []FileInfo
	err := filepath.Walk(folder, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Printf("warning: cannot access %q: %v", path, err)
			return nil // continue walking
		}
		if info.IsDir() {
			return nil
		}
		mod := info.ModTime()
		if !mod.Before(oldest) && !mod.After(newest) {
			matched = append(matched, FileInfo{
				Path:    path,
				Size:    info.Size(),
				ModTime: mod,
			})
		}
		return nil
	})
	return matched, err
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

// telegramAPIError holds the structured error response returned by the Telegram Bot API.
type telegramAPIError struct {
	OK          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
}

// telegramUpdate represents a single update from the Telegram Bot API getUpdates endpoint.
type telegramUpdate struct {
	Message *telegramUpdateMessage `json:"message"`
}

// telegramUpdateMessage holds message and sender details from a Telegram update.
type telegramUpdateMessage struct {
	Chat struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	From *struct {
		ID int64 `json:"id"`
	} `json:"from"`
}

// telegramHTTPClient is used for outgoing Telegram API requests with a fixed timeout.
var telegramHTTPClient = &http.Client{Timeout: 10 * time.Second}

// findChatIDFromUpdates calls the getUpdates endpoint and returns the first chat ID
// found where the sender's user ID matches targetUserID. Returns 0 if not found.
func findChatIDFromUpdates(baseURL string, targetUserID int64) int64 {
	resp, err := telegramHTTPClient.Get(baseURL + "/getUpdates") //nolint:noctx
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("warning: reading getUpdates response: %v", err)
		return 0
	}

	var result struct {
		OK     bool             `json:"ok"`
		Result []telegramUpdate `json:"result"`
	}
	if json.Unmarshal(body, &result) != nil || !result.OK {
		return 0
	}
	for _, u := range result.Result {
		if u.Message != nil && u.Message.From != nil && u.Message.From.ID == targetUserID {
			return u.Message.Chat.ID
		}
	}
	return 0
}

// sendTelegramMessage sends a Markdown-formatted message via the Telegram Bot API.
func sendTelegramMessage(botToken, chatID, message string) error {
	baseURL := fmt.Sprintf("https://api.telegram.org/bot%s", url.PathEscape(botToken))
	return sendTelegramMessageWithBase(baseURL, chatID, message)
}

// sendTelegramMessageWithBase sends a message using the given API base URL.
// The base URL must include the full path up to (but not including) the method name,
// e.g. "https://api.telegram.org/bot<TOKEN>".
// It is separated from sendTelegramMessage to allow substituting a test HTTP server.
func sendTelegramMessageWithBase(baseURL, chatID, message string) error {
	return doSendTelegramMessage(baseURL, chatID, message, true)
}

// doSendTelegramMessage performs the actual HTTP send. When tryFallback is true and
// the API returns "chat not found", it queries getUpdates to resolve the chat ID for
// the given user ID and retries once. This handles the common case where the caller
// configures their Telegram user ID as chat_id.
func doSendTelegramMessage(baseURL, chatID, message string, tryFallback bool) error {
	apiURL := baseURL + "/sendMessage"

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
		var apiErr telegramAPIError
		if json.Unmarshal(respBody, &apiErr) == nil && strings.Contains(apiErr.Description, "chat not found") {
			if tryFallback {
				if userID, parseErr := strconv.ParseInt(strings.TrimSpace(chatID), 10, 64); parseErr == nil {
					if resolvedChatID := findChatIDFromUpdates(baseURL, userID); resolvedChatID != 0 {
						log.Printf("chat_id %q not found directly; retrying with chat_id %d found in getUpdates",
							chatID, resolvedChatID)
						return doSendTelegramMessage(baseURL, strconv.FormatInt(resolvedChatID, 10), message, false)
					}
				}
			}
			return fmt.Errorf("Telegram API returned HTTP %d: %s\n"+
				"Hint: chat_id %q was not found. "+
				"If you are using your personal user ID, you must first send /start to your bot "+
				"so that it can initiate a conversation with you. "+
				"You can then retrieve the correct chat_id by calling: "+
				"https://api.telegram.org/bot<YOUR_BOT_TOKEN>/getUpdates",
				resp.StatusCode, string(respBody), chatID)
		}
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
