package db

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/polarhive/ash/config"
	"github.com/polarhive/ash/links"
)

//go:embed schema_meta.sql schema_messages.sql
var schemaFS embed.FS

// MetaSyncStore implements mautrix.Storer using the meta SQLite database.
type MetaSyncStore struct {
	DB *sql.DB
}

func (s *MetaSyncStore) LoadNextBatch(ctx context.Context, userID id.UserID) (string, error) {
	return GetMeta(ctx, s.DB, "sync_token")
}
func (s *MetaSyncStore) SaveNextBatch(ctx context.Context, userID id.UserID, token string) error {
	return SetMeta(ctx, s.DB, "sync_token", token)
}
func (s *MetaSyncStore) Close() error { return nil }
func (s *MetaSyncStore) Name() string { return "MetaSyncStore" }

func (s *MetaSyncStore) LoadFilterID(ctx context.Context, userID id.UserID) (string, error) {
	return "", nil
}
func (s *MetaSyncStore) SaveFilterID(ctx context.Context, userID id.UserID, filterID string) error {
	return nil
}
func (s *MetaSyncStore) LoadPresence(ctx context.Context, userID id.UserID) (interface{}, error) {
	return nil, nil
}
func (s *MetaSyncStore) SavePresence(ctx context.Context, userID id.UserID, presence interface{}) error {
	return nil
}
func (s *MetaSyncStore) LoadAccountData(ctx context.Context, userID id.UserID, eventType string) (json.RawMessage, error) {
	return nil, nil
}
func (s *MetaSyncStore) SaveAccountData(ctx context.Context, userID id.UserID, eventType string, content json.RawMessage) error {
	return nil
}
func (s *MetaSyncStore) LoadRoomAccountData(ctx context.Context, userID id.UserID, roomID id.RoomID, eventType string) (json.RawMessage, error) {
	return nil, nil
}
func (s *MetaSyncStore) SaveRoomAccountData(ctx context.Context, userID id.UserID, roomID id.RoomID, eventType string, content json.RawMessage) error {
	return nil
}

// ---------------------------------------------------------------------------
// Database helpers
// ---------------------------------------------------------------------------

// OpenMeta opens (or creates) the meta database and applies its schema.
func OpenMeta(ctx context.Context, path string) (*sql.DB, error) {
	return openWithSchema(ctx, path, "schema_meta.sql")
}

// OpenMessages opens (or creates) the messages database and applies its schema.
func OpenMessages(ctx context.Context, path string) (*sql.DB, error) {
	return openWithSchema(ctx, path, "schema_messages.sql")
}

func openWithSchema(ctx context.Context, path, schemaFile string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := database.ExecContext(ctx, "PRAGMA journal_mode=WAL;"); err != nil {
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	sqlBytes, err := schemaFS.ReadFile(schemaFile)
	if err != nil {
		return nil, fmt.Errorf("read schema: %w", err)
	}
	if _, err := database.ExecContext(ctx, string(sqlBytes)); err != nil {
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return database, nil
}

// GetMeta retrieves a value from the meta key-value table.
func GetMeta(ctx context.Context, database *sql.DB, key string) (string, error) {
	var val string
	if err := database.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&val); err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return val, nil
}

// SetMeta inserts or updates a value in the meta key-value table.
func SetMeta(ctx context.Context, database *sql.DB, key, value string) error {
	_, err := database.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// ---------------------------------------------------------------------------
// Message storage
// ---------------------------------------------------------------------------

// MessageData holds a parsed Matrix message event and its extracted URLs.
type MessageData struct {
	Event *event.Event
	Msg   *event.MessageEventContent
	URLs  []string
}

// ProcessMessageEvent parses a raw event and extracts links.
func ProcessMessageEvent(ev *event.Event) (*MessageData, error) {
	if ev.Content.Raw != nil {
		if err := ev.Content.ParseRaw(ev.Type); err != nil {
			if !strings.Contains(err.Error(), "already parsed") {
				return nil, err
			}
		}
	}
	msg := ev.Content.AsMessage()
	if msg == nil || msg.Body == "" {
		return nil, nil
	}
	urls := links.ExtractLinks(msg.Body)
	return &MessageData{
		Event: ev,
		Msg:   msg,
		URLs:  urls,
	}, nil
}

// StoreMessage persists a message and its links to the database.
func StoreMessage(database *sql.DB, data *MessageData) error {
	rawJSON, _ := json.Marshal(data.Event.Content.Raw)
	_, err := database.Exec(`
		INSERT OR IGNORE INTO messages(id, room_id, sender, ts_ms, body, msgtype, raw_json)
		VALUES (?, ?, ?, ?, ?, ?, ?);
	`, data.Event.ID, data.Event.RoomID, data.Event.Sender, int64(data.Event.Timestamp),
		data.Msg.Body, data.Msg.MsgType, string(rawJSON))
	if err != nil {
		return err
	}
	for idx, u := range data.URLs {
		if _, err := database.Exec(`
			INSERT OR IGNORE INTO links(message_id, url, idx, title, ts_ms)
			VALUES (?, ?, ?, NULL, ?);
		`, data.Event.ID, u, idx, int64(data.Event.Timestamp)); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Link snapshots
// ---------------------------------------------------------------------------

// LinkRow represents a link entry for JSON export.
type LinkRow struct {
	MessageID string `json:"message_id"`
	URL       string `json:"url"`
	TSMillis  int64  `json:"ts_ms"`
	Sender    string `json:"sender"`
}

// ExportAllSnapshots exports all links from monitored rooms to a JSON file.
func ExportAllSnapshots(database *sql.DB, rooms []config.RoomIDEntry, path string) error {
	roomMap := make(map[string]string)
	for _, r := range rooms {
		roomMap[r.ID] = r.Comment
	}
	rows, err := database.Query(`
		SELECT m.room_id, l.message_id, l.url, l.ts_ms, m.sender
		FROM links l
		JOIN messages m ON m.id = l.message_id
		WHERE m.room_id IN (`+strings.Repeat("?,", len(rooms)-1)+`?)
		ORDER BY m.room_id, l.ts_ms ASC, l.message_id, l.idx;
	`, func() []interface{} {
		args := make([]interface{}, len(rooms))
		for i, r := range rooms {
			args[i] = r.ID
		}
		return args
	}()...)
	if err != nil {
		return fmt.Errorf("query links: %w", err)
	}
	defer rows.Close()
	roomLinks := make(map[string][]LinkRow)
	for rows.Next() {
		var roomID string
		var r LinkRow
		if err := rows.Scan(&roomID, &r.MessageID, &r.URL, &r.TSMillis, &r.Sender); err != nil {
			return fmt.Errorf("scan link: %w", err)
		}
		comment := roomMap[roomID]
		roomLinks[comment] = append(roomLinks[comment], r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	payload := struct {
		LastSync time.Time            `json:"last_sync"`
		Rooms    map[string][]LinkRow `json:"rooms"`
	}{
		LastSync: time.Now().UTC(),
		Rooms:    roomLinks,
	}
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create export file: %w", err)
	}
	defer file.Close()
	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(payload); err != nil {
		return fmt.Errorf("encode export: %w", err)
	}
	return nil
}
