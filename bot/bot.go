package bot

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/polarhive/ash/util"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// ---------------------------------------------------------------------------
// Bot config types & loading
// ---------------------------------------------------------------------------

// BotCommand describes a bot command that can return text or images.
type BotCommand struct {
	Type         string                 `json:"type"`
	Method       string                 `json:"method,omitempty"`
	URL          string                 `json:"url,omitempty"`
	Headers      map[string]string      `json:"headers,omitempty"`
	JSONPath     string                 `json:"json_path,omitempty"`
	ResponseType string                 `json:"response_type,omitempty"`
	Command      string                 `json:"command,omitempty"`
	Args         []string               `json:"args,omitempty"`
	InputType    string                 `json:"input_type,omitempty"`
	OutputType   string                 `json:"output_type,omitempty"`
	Model        string                 `json:"model,omitempty"`
	MaxTokens    int                    `json:"max_tokens,omitempty"`
	Prompt       string                 `json:"prompt,omitempty"`
	Response     string                 `json:"response,omitempty"`
	Params       map[string]interface{} `json:"params,omitempty"`
	Mention      bool                   `json:"mention,omitempty"`
}

// BotConfig is the structure of bot.json.
type BotConfig struct {
	Label    string                `json:"label,omitempty"`
	Commands map[string]BotCommand `json:"commands,omitempty"`
}

// LoadBotConfig reads and parses the bot config file.
func LoadBotConfig(path string) (*BotConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	var bc BotConfig
	if err := json.NewDecoder(f).Decode(&bc); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return &bc, nil
}

// ---------------------------------------------------------------------------
// Knock-knock jokes
// ---------------------------------------------------------------------------

// KnockKnockJoke holds a single knock-knock joke.
type KnockKnockJoke struct {
	Name      string
	Punchline string
}

// KnockKnockJokes is the list of available jokes.
var KnockKnockJokes = []KnockKnockJoke{
	{"Lettuce", "Lettuce in, it's cold out here!"},
	{"Atch", "Bless you!"},
	{"Nobel", "Nobel, that's why I knocked!"},
	{"Cow says", "No, a cow says moo!"},
	{"Interrupting cow", "MOO!"},
	{"Who", "‼️ That's the sound of da police ‼️"},
	{"Boo", "Don't cry, it's just a joke!"},
	{"Tank", "You're welcome!"},
	{"Broken pencil", "Never mind, it's pointless."},
	{"Dishes", "Dishes the police, open up!"},
	{"Honey bee", "Honey bee a dear and open the door!"},
	{"Ice cream", "Ice cream every time I see a scary movie!"},
	{"Olive", "Olive you and I don't care who knows it!"},
	{"Harry", "Harry up and answer the door!"},
	{"Canoe", "Canoe help me with my homework?"},
	{"Annie", "Annie thing you can do, I can do better!"},
	{"Woo", "Don't get so excited, it's just a joke!"},
	{"Déja", "Knock knock."},
	{"Spell", "W-H-O"},
	{"Yukon", "Yukon say that again!"},
	{"Alpaca", "Alpaca the suitcase, you load the car!"},
	{"Needle", "Needle little help getting in!"},
	{"Butch", "Butch your arms around me!"},
	{"Mikey", "Mikey doesn't fit in the lock!"},
	{"Iva", "Iva sore hand from knocking!"},
	{"Figs", "Figs the doorbell, it's broken!"},
	{"Ketchup", "Ketchup with me and I'll tell you!"},
	{"Wooden shoe", "Wooden shoe like to hear another joke?"},
	{"Owls say", "Yes, they do!"},
	{"To", "To whom."},
	{"Banana", "Banana split, let's get out of here!"},
	{"Justin", "Justin time for dinner!"},
	{"Water", "Water you doing in my house?"},
	{"Nana", "Nana your business!"},
	{"Doris", "Doris locked, that's why I'm knocking!"},
	{"Europe", "Europe next to open the door!"},
	{"Abby", "Abby birthday to you!"},
	{"Luke", "Luke through the peephole and find out!"},
	{"Ash", "Ash you a question, but you might not like it!"},
	{"Cargo", "Car go beep beep, vroom vroom!"},
	{"Howard", "Howard I know? I forgot!"},
	{"Wendy", "Wendy wind blows the cradle will rock!"},
	{"Noah", "Noah good place to eat around here?"},
	{"Al", "Al give you a hug if you open this door!"},
	{"Cows go", "No they don't, cows go moo!"},
	{"Stopwatch", "Stopwatch you're doing and open the door!"},
	{"Radio", "Radio not, here I come!"},
}

// KnockKnockStep tracks the current step in a knock-knock joke conversation.
type KnockKnockStep struct {
	Joke  KnockKnockJoke
	Step  int // 0 = waiting for "who's there?", 1 = waiting for "<name> who?"
	Label string
}

// KnockKnockState manages pending knock-knock joke conversations.
type KnockKnockState struct {
	mu      sync.Mutex
	pending map[id.EventID]*KnockKnockStep
}

// NewKnockKnockState creates a new KnockKnockState.
func NewKnockKnockState() *KnockKnockState {
	return &KnockKnockState{pending: make(map[id.EventID]*KnockKnockStep)}
}

// Set stores a knock-knock step for the given event ID.
func (s *KnockKnockState) Set(evID id.EventID, step *KnockKnockStep) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[evID] = step
}

// Get retrieves a knock-knock step by event ID.
func (s *KnockKnockState) Get(evID id.EventID) (*KnockKnockStep, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.pending[evID]
	return v, ok
}

// Delete removes a knock-knock step by event ID.
func (s *KnockKnockState) Delete(evID id.EventID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pending, evID)
}

// ---------------------------------------------------------------------------
// Yap leaderboard
// ---------------------------------------------------------------------------

// YapTimezone is the timezone used to determine "start of day" for the yap
// leaderboard. Defaults to UTC. Set via config.json "TIMEZONE" field.
var YapTimezone = time.UTC

// startOfToday returns midnight in the configured YapTimezone as Unix millis.
func startOfToday() int64 {
	now := time.Now().In(YapTimezone)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, YapTimezone).UnixMilli()
}

// QueryTopYappers returns the top N message senders since midnight for the
// current room, excluding messages that start with the bot label (e.g. [BOT]).
func QueryTopYappers(ctx context.Context, db *sql.DB, matrixClient *mautrix.Client, ev *event.Event, args string, replyLabel string, mention bool) (string, error) {
	if db == nil {
		return "", fmt.Errorf("no database available")
	}

	// Handle "guess N" subcommand.
	trimmed := strings.TrimSpace(args)
	if strings.HasPrefix(strings.ToLower(trimmed), "guess") {
		return queryYapGuess(ctx, db, matrixClient, ev, strings.TrimSpace(trimmed[len("guess"):]), replyLabel)
	}

	limit := 5
	if args != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(args)); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 50 {
		limit = 50
	}

	roomID := string(ev.RoomID)
	cutoff := startOfToday()

	rows, err := db.QueryContext(ctx, `
		SELECT sender, SUM(LENGTH(body) - LENGTH(REPLACE(body, ' ', '')) + 1) as word_count
		FROM messages
		WHERE room_id = ?
		  AND ts_ms >= ?
		  AND body NOT LIKE '[BOT]%'
		  AND body NOT LIKE '/bot %'
		  AND msgtype = 'm.text'
		GROUP BY sender
		ORDER BY word_count DESC
		LIMIT ?
	`, roomID, cutoff, limit)
	if err != nil {
		return "", fmt.Errorf("query yappers: %w", err)
	}
	defer rows.Close()

	// Pre-fetch room members for display name resolution.
	displayNames := make(map[string]string)
	if matrixClient != nil {
		if resp, err := matrixClient.JoinedMembers(ctx, ev.RoomID); err == nil {
			for uid, member := range resp.Joined {
				if member.DisplayName != "" {
					displayNames[string(uid)] = member.DisplayName
				}
			}
		}
	}

	type yapEntry struct {
		senderID string
		display  string
		count    int
	}
	var entries []yapEntry
	for rows.Next() {
		var sender string
		var count int
		if err := rows.Scan(&sender, &count); err != nil {
			continue
		}
		display := sender
		if dn, ok := displayNames[sender]; ok {
			display = dn
		} else if strings.HasPrefix(sender, "@") {
			if idx := strings.Index(sender, ":"); idx > 0 {
				display = sender[1:idx]
			}
		}
		entries = append(entries, yapEntry{senderID: sender, display: display, count: count})
	}

	if len(entries) == 0 {
		return "no messages found today", nil
	}

	// Build plain text and HTML versions.
	var plain, html strings.Builder
	plain.WriteString(replyLabel + "top yappers (today):\n")
	html.WriteString(replyLabel + "top yappers (today):<br>")
	for i, e := range entries {
		plain.WriteString(fmt.Sprintf("%d. %s \u2014 %d words\n", i+1, e.display, e.count))
		if mention {
			html.WriteString(fmt.Sprintf("%d. <a href=\"https://matrix.to/#/%s\">%s</a> \u2014 %d words<br>", i+1, e.senderID, e.display, e.count))
		} else {
			html.WriteString(fmt.Sprintf("%d. %s \u2014 %d words<br>", i+1, e.display, e.count))
		}
	}

	// Send the formatted message directly.
	if matrixClient != nil {
		content := event.MessageEventContent{
			MsgType:       event.MsgText,
			Body:          strings.TrimSpace(plain.String()),
			Format:        event.FormatHTML,
			FormattedBody: strings.TrimSuffix(html.String(), "<br>"),
			RelatesTo:     &event.RelatesTo{InReplyTo: &event.InReplyTo{EventID: ev.ID}},
		}
		if _, err := matrixClient.SendMessageEvent(ctx, ev.RoomID, event.EventMessage, &content); err != nil {
			return "", fmt.Errorf("send yap reply: %w", err)
		}
		return "", nil
	}

	// Fallback for tests or when no client is available.
	return strings.TrimSpace(plain.String()), nil
}

// queryYapGuess handles "/bot yap guess N". It looks up the caller's actual
// position on today's (since midnight UTC) word-count leaderboard and reports the difference.
func queryYapGuess(ctx context.Context, db *sql.DB, matrixClient *mautrix.Client, ev *event.Event, guessArg string, replyLabel string) (string, error) {
	guess := 1
	if guessArg != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(guessArg)); err == nil && n > 0 {
			guess = n
		}
	}

	roomID := string(ev.RoomID)
	senderID := string(ev.Sender)
	cutoff := startOfToday()

	rows, err := db.QueryContext(ctx, `
		SELECT sender, SUM(LENGTH(body) - LENGTH(REPLACE(body, ' ', '')) + 1) as word_count
		FROM messages
		WHERE room_id = ?
		  AND ts_ms >= ?
		  AND body NOT LIKE '[BOT]%'
		  AND body NOT LIKE '/bot %'
		  AND msgtype = 'm.text'
		GROUP BY sender
		ORDER BY word_count DESC
	`, roomID, cutoff)
	if err != nil {
		return "", fmt.Errorf("query yap guess: %w", err)
	}
	defer rows.Close()

	actualPos := 0
	totalWords := 0
	rank := 0
	for rows.Next() {
		var sender string
		var count int
		if err := rows.Scan(&sender, &count); err != nil {
			continue
		}
		rank++
		if sender == senderID {
			actualPos = rank
			totalWords = count
		}
	}

	if actualPos == 0 {
		return "you have no messages today!", nil
	}

	diff := guess - actualPos
	var msg string
	if diff == 0 {
		msg = fmt.Sprintf("%syou guessed #%d — that's exactly right! (%d words)", replyLabel, guess, totalWords)
	} else {
		direction := "higher"
		absDiff := diff
		if diff > 0 {
			direction = "lower"
		} else {
			absDiff = -diff
		}
		msg = fmt.Sprintf("%syou guessed #%d but you're actually #%d (%d words) — %d position(s) %s than you thought",
			replyLabel, guess, actualPos, totalWords, absDiff, direction)
	}

	if matrixClient != nil {
		content := event.MessageEventContent{
			MsgType:   event.MsgText,
			Body:      msg,
			RelatesTo: &event.RelatesTo{InReplyTo: &event.InReplyTo{EventID: ev.ID}},
		}
		if _, err := matrixClient.SendMessageEvent(ctx, ev.RoomID, event.EventMessage, &content); err != nil {
			return "", fmt.Errorf("send yap guess reply: %w", err)
		}
		return "", nil
	}
	return msg, nil
}

// ---------------------------------------------------------------------------
// Random quote
// ---------------------------------------------------------------------------

// QueryRandomQuote picks a random message from the room's history (excluding
// bot messages and commands) and formats it as a quote.
func QueryRandomQuote(ctx context.Context, db *sql.DB, matrixClient *mautrix.Client, ev *event.Event, args string, replyLabel string, mention bool) (string, error) {
	if db == nil {
		return "", fmt.Errorf("no database available")
	}

	roomID := string(ev.RoomID)

	// Parse duration argument (default 24h)
	durSec, err := util.ParseDurationArg(args)
	if err != nil {
		durSec = 24 * 3600 // fallback to 24h
	}
	cutoff := time.Now().Unix() - durSec

	row := db.QueryRowContext(ctx, `
		SELECT sender, body, ts_ms
		FROM messages
		WHERE room_id = ?
		  AND body NOT LIKE '[BOT]%'
		  AND body NOT LIKE '/bot %'
		  AND msgtype = 'm.text'
		  AND LENGTH(body) > 5
		  AND ts_ms >= ? * 1000
		ORDER BY RANDOM()
		LIMIT 1
	`, roomID, cutoff)

	var sender, body string
	var tsMs int64
	if err := row.Scan(&sender, &body, &tsMs); err != nil {
		return "no messages found to quote", nil
	}

	// Resolve display name.
	display := sender
	if matrixClient != nil {
		if resp, err := matrixClient.JoinedMembers(ctx, ev.RoomID); err == nil {
			if member, ok := resp.Joined[id.UserID(sender)]; ok && member.DisplayName != "" {
				display = member.DisplayName
			}
		}
	}
	if display == sender && strings.HasPrefix(sender, "@") {
		if idx := strings.Index(sender, ":"); idx > 0 {
			display = sender[1:idx]
		}
	}

	ts := time.UnixMilli(tsMs).In(YapTimezone)
	date := ts.Format("02 Jan 2006")

	plain := fmt.Sprintf("%s> %s\n> \u2014 %s, %s", replyLabel, body, display, date)
	html := fmt.Sprintf("%s<blockquote>%s<br>\u2014 <i>%s, %s</i></blockquote>", replyLabel, body, display, date)

	if matrixClient != nil {
		content := event.MessageEventContent{
			MsgType:       event.MsgText,
			Body:          plain,
			Format:        event.FormatHTML,
			FormattedBody: html,
			RelatesTo:     &event.RelatesTo{InReplyTo: &event.InReplyTo{EventID: ev.ID}},
		}
		if _, err := matrixClient.SendMessageEvent(ctx, ev.RoomID, event.EventMessage, &content); err != nil {
			return "", fmt.Errorf("send quote reply: %w", err)
		}
		return "", nil
	}
	return plain, nil
}

// ---------------------------------------------------------------------------
// UwUify
// ---------------------------------------------------------------------------

// Uwuify transforms text into uwu-speak.
func Uwuify(text string) string {
	replacements := []struct{ old, new string }{
		{"small", "smol"},
		{"cute", "kawaii"},
		{"love", "wuv"},
		{"Love", "Wuv"},
		{"LOVE", "WUV"},
		{"this", "dis"},
		{"This", "Dis"},
		{"the ", "da "},
		{"The ", "Da "},
		{"have", "haz"},
		{"ove", "uv"},
		{"th", "d"},
		{"Th", "D"},
	}

	result := text
	for _, r := range replacements {
		result = strings.ReplaceAll(result, r.old, r.new)
	}

	// Character-level replacements.
	var buf strings.Builder
	buf.Grow(len(result))
	for i := 0; i < len(result); i++ {
		c := result[i]
		switch c {
		case 'r', 'l':
			buf.WriteByte('w')
		case 'R', 'L':
			buf.WriteByte('W')
		default:
			buf.WriteByte(c)
		}
	}
	result = buf.String()

	// Add stutter to some words.
	words := strings.Fields(result)
	if len(words) > 0 {
		for i, w := range words {
			if len(w) > 1 && i%4 == 0 {
				first := strings.ToLower(string(w[0]))
				if first >= "a" && first <= "z" {
					words[i] = string(w[0]) + "-" + w
				}
			}
		}
		result = strings.Join(words, " ")
	}

	// Append a random kaomoji.
	faces := []string{" uwu", " owo", " >w<", " ^w^", " (◕ᴗ◕✿)", " ✧w✧", " ~nyaa"}
	b := make([]byte, 1)
	_, _ = rand.Read(b)
	result += faces[int(b[0])%len(faces)]

	return result
}
