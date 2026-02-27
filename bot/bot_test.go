package bot

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

func TestLoadBotConfig(t *testing.T) {
	cfg, err := LoadBotConfig("../bot.json")
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
			got := Uwuify(tt.input)
			if !tt.check(got) {
				t.Errorf("Uwuify(%q) = %q: %s", tt.input, got, tt.desc)
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

	// Old message — should be excluded (before today UTC).
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
	result, err := QueryTopYappers(ctx, db, nil, ev, "", "", false)
	if err != nil {
		t.Fatalf("QueryTopYappers: %v", err)
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
	result2, err := QueryTopYappers(ctx, db, nil, ev, "2", "", false)
	if err != nil {
		t.Fatalf("QueryTopYappers with limit: %v", err)
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
	result, err := QueryTopYappers(ctx, db, nil, ev, "guess 1", "", false)
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
	result, err = QueryTopYappers(ctx, db, nil, ev, "guess 1", "", false)
	if err != nil {
		t.Fatalf("queryYapGuess exact: %v", err)
	}
	if !strings.Contains(result, "exactly right") {
		t.Errorf("expected exact match for alice guessing #1, got: %s", result)
	}

	// Carol guesses rank 1 but is actually rank 3.
	ev.Sender = "@carol:example.com"
	result, err = QueryTopYappers(ctx, db, nil, ev, "guess 1", "", false)
	if err != nil {
		t.Fatalf("queryYapGuess carol: %v", err)
	}
	if !strings.Contains(result, "guessed #1") || !strings.Contains(result, "actually #3") {
		t.Errorf("expected carol at #3, got: %s", result)
	}

	// Unknown sender has no messages.
	ev.Sender = "@nobody:example.com"
	result, err = QueryTopYappers(ctx, db, nil, ev, "guess 1", "", false)
	if err != nil {
		t.Fatalf("queryYapGuess nobody: %v", err)
	}
	if !strings.Contains(result, "no messages") {
		t.Errorf("expected no messages for unknown sender, got: %s", result)
	}
}

func TestQueryRandomQuote(t *testing.T) {
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

	room := "!testroom:example.com"
	ev := &event.Event{RoomID: id.RoomID(room)}
	ctx := context.Background()

	// Empty room — should return "no messages found".
	result, err := QueryRandomQuote(ctx, db, nil, ev, "", "", false)
	if err != nil {
		t.Fatalf("QueryRandomQuote empty: %v", err)
	}
	if !strings.Contains(result, "no messages") {
		t.Errorf("expected 'no messages' for empty room, got: %s", result)
	}

	// Insert messages: one recent, one old.
	now := time.Now().UnixMilli()
	_, _ = db.Exec(`INSERT INTO messages(id, room_id, sender, ts_ms, body, msgtype) VALUES (?, ?, ?, ?, ?, ?)`,
		"msg-1", room, "@alice:example.com", now, "the quick brown fox jumps", "m.text")
	_, _ = db.Exec(`INSERT INTO messages(id, room_id, sender, ts_ms, body, msgtype) VALUES (?, ?, ?, ?, ?, ?)`,
		"msg-2", room, "@bob:example.com", now-3*86400000, "hello world from 3 days ago", "m.text")

	// Should return only recent message for 1d.
	result, err = QueryRandomQuote(ctx, db, nil, ev, "1d", "", false)
	if err != nil {
		t.Fatalf("QueryRandomQuote 1d: %v", err)
	}
	if !strings.Contains(result, "fox jumps") {
		t.Errorf("expected recent quote, got: %s", result)
	}
	if strings.Contains(result, "3 days ago") {
		t.Errorf("should not quote old message, got: %s", result)
	}

	// Should return either for 1w.
	result, err = QueryRandomQuote(ctx, db, nil, ev, "1w", "", false)
	if err != nil {
		t.Fatalf("QueryRandomQuote 1w: %v", err)
	}
	if !strings.Contains(result, "fox jumps") && !strings.Contains(result, "3 days ago") {
		t.Errorf("expected any quote, got: %s", result)
	}

	// Bot messages should be excluded.
	_, _ = db.Exec(`INSERT INTO messages(id, room_id, sender, ts_ms, body, msgtype) VALUES (?, ?, ?, ?, ?, ?)`,
		"bot-1", room, "@bot:example.com", now, "[BOT] I am a bot message", "m.text")

	result, err = QueryRandomQuote(ctx, db, nil, ev, "1d", "", false)
	if err != nil {
		t.Fatalf("QueryRandomQuote bot: %v", err)
	}
	if strings.Contains(result, "[BOT]") {
		t.Errorf("bot messages should be excluded, got: %s", result)
	}
	if !strings.Contains(result, "> ") || !strings.Contains(result, "\u2014") {
		t.Errorf("expected blockquote format, got: %s", result)
	}
	if !strings.Contains(result, "alice") && !strings.Contains(result, "bob") {
		t.Errorf("expected alice or bob in quote, got: %s", result)
	}
}
