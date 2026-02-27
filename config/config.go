package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// RoomIDEntry describes a Matrix room the bot should monitor.
type RoomIDEntry struct {
	ID              string   `json:"id"`
	Comment         string   `json:"comment"`
	Hook            string   `json:"hook,omitempty"`
	Key             string   `json:"key,omitempty"`
	SendUser        bool     `json:"sendUser,omitempty"`
	SendTopic       bool     `json:"sendTopic,omitempty"`
	AllowedCommands []string `json:"allowedCommands,omitempty"`
}

// Config holds all application configuration loaded from config.json.
type Config struct {
	Homeserver    string        `json:"MATRIX_HOMESERVER"`
	User          string        `json:"MATRIX_USER"`
	Password      string        `json:"MATRIX_PASSWORD"`
	RecoveryKey   string        `json:"MATRIX_RECOVERY_KEY"`
	RoomIDs       []RoomIDEntry `json:"MATRIX_ROOM_ID"`
	DBPath        string        `json:"DB_PATH"`
	MetaDBPath    string        `json:"META_DB_PATH"`
	LinksPath     string        `json:"LINKS_JSON_PATH"`
	BotConfigPath string        `json:"BOT_CONFIG_PATH"`
	BotReplyLabel string        `json:"BOT_REPLY_LABEL,omitempty"`
	LinkstashURL  string        `json:"LINKSTASH_URL,omitempty"`
	GroqAPIKey    string        `json:"GROQ_API_KEY,omitempty"`
	SyncTimeoutMS int           `json:"SYNC_TIMEOUT_MS"`
	Debug         bool          `json:"DEBUG"`
	DryRun        bool          `json:"DRY_RUN"`
	DeviceName    string        `json:"MATRIX_DEVICE_NAME"`
	OptOutTag     string        `json:"OPT_OUT_TAG"`
	Timezone      string        `json:"TIMEZONE,omitempty"`
}

// LoadConfig reads and parses the config.json file.
func LoadConfig() (*Config, error) {
	var cfg Config
	jsonFile, err := os.Open("config.json")
	if err != nil {
		return nil, fmt.Errorf("open config.json: %w", err)
	}
	defer jsonFile.Close()
	dec := json.NewDecoder(jsonFile)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode config.json: %w", err)
	}
	return &cfg, nil
}
