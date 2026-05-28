package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config holds all application configuration.
type Config struct {
	TelegramToken  string
	Transport      string // "http" or "stdio"
	Port           string
	AuthToken      string
	RelayFile      string
	AllowedUserIDs []int64
}

// Load reads configuration from environment variables.
func Load() (*Config, error) {
	_ = godotenv.Load()

	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("TELEGRAM_BOT_TOKEN is required")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	relayFile := os.Getenv("RELAY_FILE")
	if relayFile == "" {
		relayFile = "/tmp/cc-bot-relay.json"
	}

	var allowedIDs []int64
	if raw := os.Getenv("ALLOWED_USER_IDS"); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			id, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				log.Printf("[config] warning: invalid user ID in ALLOWED_USER_IDS: %q", s)
				continue
			}
			allowedIDs = append(allowedIDs, id)
		}
	}

	return &Config{
		TelegramToken:  token,
		Transport:      strings.ToLower(os.Getenv("MCP_TRANSPORT")),
		Port:           port,
		AuthToken:      os.Getenv("MCP_AUTH_TOKEN"),
		RelayFile:      relayFile,
		AllowedUserIDs: allowedIDs,
	}, nil
}
