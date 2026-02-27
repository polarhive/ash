package app

import (
	"strings"
	"testing"

	"github.com/polarhive/ash/bot"
	"github.com/polarhive/ash/config"
)

func TestResolveReplyLabel(t *testing.T) {
	tests := []struct {
		name   string
		cfg    *config.Config
		botCfg *bot.BotConfig
		want   string
	}{
		{"both nil", nil, nil, "> "},
		{"config label", &config.Config{BotReplyLabel: "[bot] "}, nil, "[bot] "},
		{"bot config label", &config.Config{}, &bot.BotConfig{Label: "🤖 "}, "🤖 "},
		{"config takes precedence", &config.Config{BotReplyLabel: "[bot] "}, &bot.BotConfig{Label: "🤖 "}, "[bot] "},
		{"empty config, empty bot", &config.Config{}, &bot.BotConfig{}, "> "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveReplyLabel(tt.cfg, tt.botCfg)
			if got != tt.want {
				t.Errorf("ResolveReplyLabel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGenerateHelpMessage(t *testing.T) {
	botCfg := &bot.BotConfig{
		Commands: map[string]bot.BotCommand{
			"hello":   {Type: "http"},
			"deepfry": {Type: "exec"},
			"gork":    {Type: "ai"},
		},
	}

	// No filter
	msg := GenerateHelpMessage(botCfg, nil)
	if !strings.Contains(msg, "deepfry") || !strings.Contains(msg, "gork") || !strings.Contains(msg, "hello") {
		t.Errorf("GenerateHelpMessage missing commands: %s", msg)
	}

	// With filter
	msg = GenerateHelpMessage(botCfg, []string{"hello", "gork"})
	if !strings.Contains(msg, "hello") || !strings.Contains(msg, "gork") {
		t.Errorf("GenerateHelpMessage with filter missing commands: %s", msg)
	}
	if strings.Contains(msg, "deepfry") {
		t.Errorf("GenerateHelpMessage should not include filtered-out command: %s", msg)
	}
}
