# TelegramAlert

A lightweight Go daemon that monitors a folder for files whose modification time
falls within a configurable age window and sends a Telegram alert when matching
files are found.

## Features

- Monitors a directory recursively for files modified within a configurable time window
- Sends formatted Telegram messages via the Bot API
- Runs as a background service with init.d support
- Configurable check interval
- Custom config file path via the `-config` flag

## Installation

### Prerequisites

- Go 1.21 or later
- A [Telegram Bot token](https://core.telegram.org/bots#botfather) and a chat ID

### Build

```bash
git clone https://github.com/giulianozor/TelegramAlert.git
cd TelegramAlert
go build -o telegram-alert .
```

## Configuration

Copy the example configuration file and edit it with your settings:

```bash
cp config.yml.example config.yml
```

```yaml
monitor:
  # Folder path to monitor for file changes
  folder: /var/log/myapp

  # Minimum file age in minutes (files must be at least this old)
  min_age_minutes: 5

  # Maximum file age in minutes (files must have been modified within this window)
  max_age_minutes: 60

  # How often to repeat the check, in minutes
  check_interval_minutes: 10

telegram:
  # Telegram Bot API token (get from @BotFather)
  bot_token: "YOUR_BOT_TOKEN_HERE"

  # Telegram chat ID to send alerts to (can be a user or group chat ID).
  # To find your personal chat_id:
  #   1. Open Telegram and send /start to your bot.
  #   2. Visit https://api.telegram.org/bot<YOUR_BOT_TOKEN>/getUpdates
  #   3. Look for "chat": {"id": <number>} in the response – that number is your chat_id.
  # For a group, add the bot to the group, send any message, then use the same getUpdates URL.
  chat_id: "YOUR_CHAT_ID_HERE"
```

## Usage

By default the application looks for `config.yml` in the current directory.
Use the `-config` flag to specify a different path:

```bash
# Use the default config.yml in the current directory
./telegram-alert

# Specify a custom config file path
./telegram-alert -config /etc/telegram-alert/config.yml

# Show available flags
./telegram-alert -help
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-config` | `config.yml` | Path to the YAML configuration file |

## Running as a Service

An init.d script is provided in the `init.d` file. Install and configure it
according to your distribution's init system to run TelegramAlert as a daemon
that starts automatically on boot.

## Development

Run the test suite:

```bash
go test ./...
```