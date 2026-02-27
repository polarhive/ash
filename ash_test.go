package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestExtractLinks(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"no links", "hello world", nil},
		{"single http", "check http://example.com out", []string{"http://example.com"}},
		{"single https", "visit https://example.com/page", []string{"https://example.com/page"}},
		{"multiple links", "see https://a.com and http://b.com/path",
			[]string{"https://a.com", "http://b.com/path"}},
		{"link with query", "go to https://example.com/search?q=test&page=1",
			[]string{"https://example.com/search?q=test&page=1"}},
		{"case insensitive", "HTTPS://EXAMPLE.COM", []string{"HTTPS://EXAMPLE.COM"}},
		{"empty string", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractLinks(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("ExtractLinks(%q) = %v, want %v", tt.input, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("ExtractLinks(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestIsBlacklisted(t *testing.T) {
	blacklist, err := LoadBlacklist("blacklist.json")
	if err != nil {
		t.Skipf("skipping blacklist test (no blacklist.json): %v", err)
	}
	// Just verify it doesn't crash with a normal URL
	_ = IsBlacklisted("https://example.com", blacklist)
}

func TestExtractJSONPath(t *testing.T) {
	root := map[string]interface{}{
		"a": map[string]interface{}{
			"b": "value",
			"c": 42.0,
		},
		"top": "hello",
	}
	tests := []struct {
		path string
		want interface{}
	}{
		{"", root},
		{"top", "hello"},
		{"a.b", "value"},
		{"a.c", 42.0},
		{"missing", nil},
		{"a.missing", nil},
		{"a.b.deep", nil},
	}
	for _, tt := range tests {
		t.Run("path="+tt.path, func(t *testing.T) {
			got := extractJSONPath(root, tt.path)
			if tt.want == nil && got != nil {
				t.Errorf("extractJSONPath(_, %q) = %v, want nil", tt.path, got)
			} else if tt.want != nil {
				if s, ok := tt.want.(string); ok {
					if gs, ok := got.(string); !ok || gs != s {
						t.Errorf("extractJSONPath(_, %q) = %v, want %q", tt.path, got, s)
					}
				}
			}
		})
	}
}

func TestFormatPosts(t *testing.T) {
	posts := []interface{}{
		map[string]interface{}{"title": "Post 1", "url": "https://a.com"},
		map[string]interface{}{"title": "Post 2", "url": "https://b.com"},
	}
	result := formatPosts(posts, "https://linkstash.example.com")
	if result == "" {
		t.Error("formatPosts returned empty string")
	}
	if !contains(result, "Post 1") || !contains(result, "Post 2") {
		t.Errorf("formatPosts missing post titles: %s", result)
	}
	if !contains(result, "https://linkstash.example.com") {
		t.Errorf("formatPosts missing linkstash URL: %s", result)
	}
}

func TestFormatPostsLimit(t *testing.T) {
	// More than 5 posts should be capped
	posts := make([]interface{}, 10)
	for i := range posts {
		posts[i] = map[string]interface{}{
			"title": "Post",
			"url":   "https://example.com",
		}
	}
	result := formatPosts(posts, "https://linkstash.example.com")
	// Count lines with "- " prefix (capped at 5)
	lines := 0
	for _, line := range splitLines(result) {
		if len(line) > 0 && line[0] == '-' {
			lines++
		}
	}
	if lines != 5 {
		t.Errorf("formatPosts should cap at 5 posts, got %d", lines)
	}
}

func TestTruncateText(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		tokenLimit int
		wantMax    int // max chars in result
	}{
		{"short text", "hello", 100, 5},
		{"at limit", "hello world", 100, 11},
		{"over limit", string(make([]byte, 10000)), 10, 40},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateText(tt.text, tt.tokenLimit)
			if len(got) > tt.wantMax+10 { // small buffer for boundary rounding
				t.Errorf("truncateText len = %d, want <= %d", len(got), tt.wantMax)
			}
		})
	}
}

func TestStripCommandPrefix(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/bot gork what is life", "what is life"},
		{"/bot gork", ""},
		{"@gork hello world", "hello world"},
		{"@gork: explain this", "explain this"},
		{"plain text", "plain text"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := stripCommandPrefix(tt.input)
			if got != tt.want {
				t.Errorf("stripCommandPrefix(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestResolveReplyLabel(t *testing.T) {
	tests := []struct {
		name   string
		cfg    *Config
		botCfg *BotConfig
		want   string
	}{
		{"both nil", nil, nil, "> "},
		{"config label", &Config{BotReplyLabel: "[bot] "}, nil, "[bot] "},
		{"bot config label", &Config{}, &BotConfig{Label: "🤖 "}, "🤖 "},
		{"config takes precedence", &Config{BotReplyLabel: "[bot] "}, &BotConfig{Label: "🤖 "}, "[bot] "},
		{"empty config, empty bot", &Config{}, &BotConfig{}, "> "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveReplyLabel(tt.cfg, tt.botCfg)
			if got != tt.want {
				t.Errorf("resolveReplyLabel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInSlice(t *testing.T) {
	slice := []string{"a", "b", "c"}
	if !inSlice(slice, "b") {
		t.Error("inSlice should find 'b'")
	}
	if inSlice(slice, "d") {
		t.Error("inSlice should not find 'd'")
	}
	if inSlice(nil, "a") {
		t.Error("inSlice should return false for nil slice")
	}
}

func TestTruncate(t *testing.T) {
	if truncate("hello", 10) != "hello" {
		t.Error("truncate should not truncate short string")
	}
	got := truncate("hello world", 5)
	if got != "hello..." {
		t.Errorf("truncate = %q, want %q", got, "hello...")
	}
}

func TestGenerateHelpMessage(t *testing.T) {
	botCfg := &BotConfig{
		Commands: map[string]BotCommand{
			"hello":   {Type: "http"},
			"deepfry": {Type: "exec"},
			"gork":    {Type: "ai"},
		},
	}

	// No filter
	msg := generateHelpMessage(botCfg, nil)
	if !contains(msg, "deepfry") || !contains(msg, "gork") || !contains(msg, "hello") {
		t.Errorf("generateHelpMessage missing commands: %s", msg)
	}

	// With filter
	msg = generateHelpMessage(botCfg, []string{"hello", "gork"})
	if !contains(msg, "hello") || !contains(msg, "gork") {
		t.Errorf("generateHelpMessage with filter missing commands: %s", msg)
	}
	if contains(msg, "deepfry") {
		t.Errorf("generateHelpMessage should not include filtered-out command: %s", msg)
	}
}

func TestLoadBotConfig(t *testing.T) {
	cfg, err := LoadBotConfig("bot.json")
	if err != nil {
		t.Skipf("skipping (no bot.json): %v", err)
	}
	if cfg.Commands == nil {
		t.Fatal("Commands map is nil")
	}
	// Verify required commands exist
	for _, name := range []string{"hi", "summary", "gork"} {
		if _, ok := cfg.Commands[name]; !ok {
			t.Errorf("required command %q not found", name)
		}
	}
	// Verify each command has a valid type or static response
	for name, cmd := range cfg.Commands {
		if cmd.Response != "" {
			continue
		}
		switch cmd.Type {
		case "http", "exec", "ai", "builtin":
		default:
			t.Errorf("command %q has invalid type %q", name, cmd.Type)
		}
	}
}

// helpers

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func TestUwuify(t *testing.T) {
	tests := []struct {
		name  string
		input string
		check func(string) bool
		desc  string
	}{
		{
			"replaces r/l with w",
			"really cool",
			func(s string) bool { return strings.Contains(s, "w") },
			"should replace r and l with w",
		},
		{
			"replaces th with d",
			"the weather",
			func(s string) bool { return strings.Contains(s, "da") && strings.Contains(s, "wead") },
			"should replace 'the ' with 'da ' and 'th' with 'd'",
		},
		{
			"replaces love with wuv",
			"I love you",
			func(s string) bool { return strings.Contains(s, "wuv") },
			"should replace love with wuv",
		},
		{
			"appends kaomoji",
			"hello world",
			func(s string) bool {
				faces := []string{"uwu", "owo", ">w<", "^w^", "◕ᴗ◕✿", "✧w✧", "~nyaa"}
				for _, f := range faces {
					if strings.HasSuffix(s, f) {
						return true
					}
				}
				return false
			},
			"should end with a kaomoji",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := uwuify(tt.input)
			if !tt.check(got) {
				t.Errorf("uwuify(%q) = %q: %s", tt.input, got, tt.desc)
			}
		})
	}
}

func TestQueryTopYappers(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS messages (
		id TEXT PRIMARY KEY,
		room_id TEXT,
		sender TEXT,
		ts_ms INTEGER,
		body TEXT,
		msgtype TEXT,
		raw_json TEXT
	)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	now := time.Now().UnixMilli()
	room := "!testroom:example.com"

	// Insert test messages: alice=5, bob=3, carol=1, plus some bot messages that should be excluded.
	for i := 0; i < 5; i++ {
		_, _ = db.Exec(`INSERT INTO messages(id, room_id, sender, ts_ms, body, msgtype) VALUES (?, ?, ?, ?, ?, ?)`,
			fmt.Sprintf("alice-%d", i), room, "@alice:example.com", now-int64(i*1000), fmt.Sprintf("hello %d", i), "m.text")
	}
	for i := 0; i < 3; i++ {
		_, _ = db.Exec(`INSERT INTO messages(id, room_id, sender, ts_ms, body, msgtype) VALUES (?, ?, ?, ?, ?, ?)`,
			fmt.Sprintf("bob-%d", i), room, "@bob:example.com", now-int64(i*1000), fmt.Sprintf("hey %d", i), "m.text")
	}
	_, _ = db.Exec(`INSERT INTO messages(id, room_id, sender, ts_ms, body, msgtype) VALUES (?, ?, ?, ?, ?, ?)`,
		"carol-0", room, "@carol:example.com", now, "sup", "m.text")

	// Bot messages — should be excluded.
	_, _ = db.Exec(`INSERT INTO messages(id, room_id, sender, ts_ms, body, msgtype) VALUES (?, ?, ?, ?, ?, ?)`,
		"bot-1", room, "@bot:example.com", now, "[BOT] hello", "m.text")
	_, _ = db.Exec(`INSERT INTO messages(id, room_id, sender, ts_ms, body, msgtype) VALUES (?, ?, ?, ?, ?, ?)`,
		"bot-2", room, "@bot:example.com", now, "/bot help", "m.text")

	// Old message — should be excluded (>24h ago).
	_, _ = db.Exec(`INSERT INTO messages(id, room_id, sender, ts_ms, body, msgtype) VALUES (?, ?, ?, ?, ?, ?)`,
		"old-1", room, "@old:example.com", now-100000000, "ancient msg", "m.text")

	// Different room — should be excluded.
	_, _ = db.Exec(`INSERT INTO messages(id, room_id, sender, ts_ms, body, msgtype) VALUES (?, ?, ?, ?, ?, ?)`,
		"other-1", "!otherroom:example.com", "@other:example.com", now, "wrong room", "m.text")

	ev := &event.Event{
		RoomID: id.RoomID(room),
	}

	ctx := context.Background()

	// Test default (top 5).
	result, err := queryTopYappers(ctx, db, nil, ev, "", "", false)
	if err != nil {
		t.Fatalf("queryTopYappers: %v", err)
	}
	if !strings.Contains(result, "alice") {
		t.Errorf("expected alice in result, got: %s", result)
	}
	if !strings.Contains(result, "10 words") {
		t.Errorf("expected '10 words' for alice, got: %s", result)
	}
	if !strings.Contains(result, "bob") {
		t.Errorf("expected bob in result, got: %s", result)
	}
	// alice should be ranked #1.
	if !strings.Contains(result, "1. alice") {
		t.Errorf("expected alice at rank 1, got: %s", result)
	}

	// Test with limit.
	result2, err := queryTopYappers(ctx, db, nil, ev, "2", "", false)
	if err != nil {
		t.Fatalf("queryTopYappers with limit: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(result2), "\n")
	// Header line + 2 results.
	if len(lines) != 3 {
		t.Errorf("expected 3 lines (header + 2 results), got %d: %s", len(lines), result2)
	}

	// Bot and old messages should not appear.
	if strings.Contains(result, "bot") {
		t.Errorf("bot messages should be excluded, got: %s", result)
	}
	if strings.Contains(result, "old") {
		t.Errorf("old messages should be excluded, got: %s", result)
	}
	if strings.Contains(result, "other") {
		t.Errorf("messages from other rooms should be excluded, got: %s", result)
	}
}

func TestQueryYapGuess(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS messages (
		id TEXT PRIMARY KEY,
		room_id TEXT,
		sender TEXT,
		ts_ms INTEGER,
		body TEXT,
		msgtype TEXT,
		raw_json TEXT
	)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	now := time.Now().UnixMilli()
	room := "!testroom:example.com"

	// alice=10 words (rank 1), bob=6 words (rank 2), carol=1 word (rank 3)
	for i := 0; i < 5; i++ {
		_, _ = db.Exec(`INSERT INTO messages(id, room_id, sender, ts_ms, body, msgtype) VALUES (?, ?, ?, ?, ?, ?)`,
			fmt.Sprintf("alice-%d", i), room, "@alice:example.com", now-int64(i*1000), fmt.Sprintf("hello %d", i), "m.text")
	}
	for i := 0; i < 3; i++ {
		_, _ = db.Exec(`INSERT INTO messages(id, room_id, sender, ts_ms, body, msgtype) VALUES (?, ?, ?, ?, ?, ?)`,
			fmt.Sprintf("bob-%d", i), room, "@bob:example.com", now-int64(i*1000), fmt.Sprintf("hey %d", i), "m.text")
	}
	_, _ = db.Exec(`INSERT INTO messages(id, room_id, sender, ts_ms, body, msgtype) VALUES (?, ?, ?, ?, ?, ?)`,
		"carol-0", room, "@carol:example.com", now, "sup", "m.text")

	ctx := context.Background()

	// Bob guesses rank 1 but is actually rank 2.
	ev := &event.Event{
		RoomID: id.RoomID(room),
	}
	ev.Sender = "@bob:example.com"
	result, err := queryTopYappers(ctx, db, nil, ev, "guess 1", "", false)
	if err != nil {
		t.Fatalf("queryYapGuess: %v", err)
	}
	if !strings.Contains(result, "guessed #1") || !strings.Contains(result, "actually #2") {
		t.Errorf("expected bob at #2 with guess #1, got: %s", result)
	}
	if !strings.Contains(result, "1 position(s) higher") {
		t.Errorf("expected 'higher than you thought', got: %s", result)
	}

	// Alice guesses rank 1 — exactly right.
	ev.Sender = "@alice:example.com"
	result, err = queryTopYappers(ctx, db, nil, ev, "guess 1", "", false)
	if err != nil {
		t.Fatalf("queryYapGuess exact: %v", err)
	}
	if !strings.Contains(result, "exactly right") {
		t.Errorf("expected exact match for alice guessing #1, got: %s", result)
	}

	// Carol guesses rank 1 but is actually rank 3.
	ev.Sender = "@carol:example.com"
	result, err = queryTopYappers(ctx, db, nil, ev, "guess 1", "", false)
	if err != nil {
		t.Fatalf("queryYapGuess carol: %v", err)
	}
	if !strings.Contains(result, "guessed #1") || !strings.Contains(result, "actually #3") {
		t.Errorf("expected carol at #3, got: %s", result)
	}

	// Unknown sender has no messages.
	ev.Sender = "@nobody:example.com"
	result, err = queryTopYappers(ctx, db, nil, ev, "guess 1", "", false)
	if err != nil {
		t.Fatalf("queryYapGuess nobody: %v", err)
	}
	if !strings.Contains(result, "no messages") {
		t.Errorf("expected no messages for unknown sender, got: %s", result)
	}
}
