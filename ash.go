package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

//go:embed db/schema_meta.sql db/schema_messages.sql
var schemaFS embed.FS

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

type RoomIDEntry struct {
	ID              string   `json:"id"`
	Comment         string   `json:"comment"`
	Hook            string   `json:"hook,omitempty"`
	Key             string   `json:"key,omitempty"`
	SendUser        bool     `json:"sendUser,omitempty"`
	SendTopic       bool     `json:"sendTopic,omitempty"`
	AllowedCommands []string `json:"allowedCommands,omitempty"`
}

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

// generateHelpMessage creates a help message listing available commands
func generateHelpMessage(botCfg *BotConfig, allowedCommands []string) string {
	var cmds []string
	if len(allowedCommands) > 0 {
		cmds = make([]string, len(allowedCommands))
		copy(cmds, allowedCommands)
	} else {
		for cmd := range botCfg.Commands {
			cmds = append(cmds, cmd)
		}
	}
	sort.Strings(cmds)
	return "Available commands: " + strings.Join(cmds, ", ")
}

func OpenMeta(ctx context.Context, path string) (*sql.DB, error) {
	return openWithSchema(ctx, path, "db/schema_meta.sql")
}
func OpenMessages(ctx context.Context, path string) (*sql.DB, error) {
	return openWithSchema(ctx, path, "db/schema_messages.sql")
}
func openWithSchema(ctx context.Context, path, schemaFile string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL;"); err != nil {
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	sqlBytes, err := schemaFS.ReadFile(schemaFile)
	if err != nil {
		return nil, fmt.Errorf("read schema: %w", err)
	}
	if _, err := db.ExecContext(ctx, string(sqlBytes)); err != nil {
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return db, nil
}
func GetMeta(ctx context.Context, db *sql.DB, key string) (string, error) {
	var val string
	if err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&val); err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return val, nil
}
func SetMeta(ctx context.Context, db *sql.DB, key, value string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

type Credentials struct {
	UserID      string
	AccessToken string
	DeviceID    string
}

func LoadOrCreate(ctx context.Context, database *sql.DB, cfg *Config) (*mautrix.Client, error) {
	storedCreds, err := loadStored(ctx, database)
	if err == nil && storedCreds != nil {
		return createClientFromCreds(cfg.Homeserver, storedCreds)
	}
	client, creds, err := loginWithPassword(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := storeCreds(ctx, database, creds); err != nil {
		fmt.Fprintf(os.Stderr, "warning: couldn't store credentials: %v\n", err)
	}
	return client, nil
}
func EnsureSecrets(ctx context.Context, database *sql.DB, cfg *Config) error {
	reader := bufio.NewReader(os.Stdin)
	type field struct {
		label   string
		metaKey string
		target  *string
	}
	fields := []field{
		{"Homeserver URL", "homeserver", &cfg.Homeserver},
		{"Matrix user ID", "user_id", &cfg.User},
		{"Password", "password", &cfg.Password},
		{"Recovery key (format: EsXX XXXX ...)", "recovery_key", &cfg.RecoveryKey},
	}
	for i := range fields {
		f := &fields[i]
		if *f.target == "" {
			if val, err := GetMeta(ctx, database, f.metaKey); err == nil && val != "" {
				*f.target = val
				continue
			}
		}
		for *f.target == "" {
			fmt.Printf("%s: ", f.label)
			line, err := reader.ReadString('\n')
			if err != nil {
				return fmt.Errorf("read %s: %w", f.label, err)
			}
			*f.target = strings.TrimSpace(line)
		}
		if err := SetMeta(ctx, database, f.metaKey, *f.target); err != nil {
			return fmt.Errorf("save %s: %w", f.label, err)
		}
	}
	return nil
}

func loadStored(ctx context.Context, database *sql.DB) (*Credentials, error) {
	userID, _ := GetMeta(ctx, database, "user_id")
	token, _ := GetMeta(ctx, database, "access_token")
	deviceID, _ := GetMeta(ctx, database, "device_id")
	if userID == "" || token == "" || deviceID == "" {
		return nil, fmt.Errorf("incomplete stored credentials")
	}
	return &Credentials{userID, token, deviceID}, nil
}
func createClientFromCreds(homeserver string, creds *Credentials) (*mautrix.Client, error) {
	client, err := mautrix.NewClient(homeserver, id.UserID(creds.UserID), creds.AccessToken)
	if err != nil {
		return nil, err
	}
	client.DeviceID = id.DeviceID(creds.DeviceID)
	return client, nil
}
func loginWithPassword(ctx context.Context, cfg *Config) (*mautrix.Client, *Credentials, error) {
	client, err := mautrix.NewClient(cfg.Homeserver, "", "")
	if err != nil {
		return nil, nil, err
	}
	loginReq := mautrix.ReqLogin{
		Type:                     "m.login.password",
		Identifier:               mautrix.UserIdentifier{Type: "m.id.user", User: cfg.User},
		Password:                 cfg.Password,
		InitialDeviceDisplayName: cfg.DeviceName,
		StoreCredentials:         true,
	}
	resp, err := client.Login(ctx, &loginReq)
	if err != nil {
		return nil, nil, err
	}
	client.SetCredentials(resp.UserID, resp.AccessToken)
	client.DeviceID = resp.DeviceID
	return client, &Credentials{string(resp.UserID), resp.AccessToken, string(resp.DeviceID)}, nil
}
func storeCreds(ctx context.Context, database *sql.DB, creds *Credentials) error {
	if err := SetMeta(ctx, database, "user_id", creds.UserID); err != nil {
		return err
	}
	if err := SetMeta(ctx, database, "access_token", creds.AccessToken); err != nil {
		return err
	}
	return SetMeta(ctx, database, "device_id", creds.DeviceID)
}

func EnsurePickleKey(ctx context.Context, metaDB *sql.DB) (string, error) {
	pickleKey, err := GetMeta(ctx, metaDB, "pickle_key")
	if err == nil && pickleKey != "" {
		return pickleKey, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("generate pickle key: %w", err)
	}
	pickleKey = base64.StdEncoding.EncodeToString(key)
	if err := SetMeta(ctx, metaDB, "pickle_key", pickleKey); err != nil {
		return "", fmt.Errorf("save pickle key: %w", err)
	}
	return pickleKey, nil
}
func SetupHelper(ctx context.Context, client *mautrix.Client, metaDB *sql.DB, metaDBPath string) (*cryptohelper.CryptoHelper, error) {
	pickleKey, err := GetMeta(ctx, metaDB, "pickle_key")
	if err != nil {
		return nil, fmt.Errorf("get pickle key: %w", err)
	}
	pickleKeyBytes, err := base64.StdEncoding.DecodeString(pickleKey)
	if err != nil {
		return nil, fmt.Errorf("decode pickle key: %w", err)
	}
	cryptoDBPath := metaDBPath + ".crypto"
	helper, err := cryptohelper.NewCryptoHelper(client, pickleKeyBytes, cryptoDBPath)
	if err != nil {
		if strings.Contains(err.Error(), "mismatching device ID") {
			for _, fname := range []string{cryptoDBPath, cryptoDBPath + "-shm", cryptoDBPath + "-wal"} {
				_ = os.Remove(fname)
			}
			helper, err = cryptohelper.NewCryptoHelper(client, pickleKeyBytes, cryptoDBPath)
			if err != nil {
				return nil, fmt.Errorf("new crypto helper (after cleanup): %w", err)
			}
		} else {
			return nil, fmt.Errorf("new crypto helper: %w", err)
		}
	}
	if err := helper.Init(ctx); err != nil {
		return nil, fmt.Errorf("init crypto helper: %w", err)
	}
	return helper, nil
}
func VerifyWithRecoveryKey(ctx context.Context, machine *crypto.OlmMachine, recoveryKey string) error {
	keyID, keyData, err := machine.SSSS.GetDefaultKeyData(ctx)
	if err != nil {
		return fmt.Errorf("get key data: %w", err)
	}
	key, err := keyData.VerifyRecoveryKey(keyID, recoveryKey)
	if err != nil {
		return fmt.Errorf("verify recovery key: %w", err)
	}
	if err := machine.FetchCrossSigningKeysFromSSSS(ctx, key); err != nil {
		return fmt.Errorf("fetch cross-signing keys: %w", err)
	}
	if err := machine.SignOwnDevice(ctx, machine.OwnIdentity()); err != nil {
		return fmt.Errorf("sign own device: %w", err)
	}
	if err := machine.SignOwnMasterKey(ctx); err != nil {
		return fmt.Errorf("sign own master key: %w", err)
	}
	return nil
}

type MessageData struct {
	Event *event.Event
	Msg   *event.MessageEventContent
	URLs  []string
}

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
	urls := ExtractLinks(msg.Body)
	return &MessageData{
		Event: ev,
		Msg:   msg,
		URLs:  urls,
	}, nil
}
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

var urlRe = regexp.MustCompile(`(?i)https?://[^\s>]+`)

func ExtractLinks(text string) []string {
	return urlRe.FindAllString(text, -1)
}

func sendHook(hookURL, link, key, sender, roomID, roomComment string, sendUser, sendTopic bool) {
	resolvedLink := resolveURL(link)
	payload := map[string]interface{}{
		"link": map[string]interface{}{
			"url": resolvedLink,
		},
	}
	if sendUser {
		payload["link"].(map[string]interface{})["submittedBy"] = sender
	}
	if sendTopic && (roomID != "" || roomComment != "") {
		payload["room"] = map[string]string{
			"id":      roomID,
			"comment": roomComment,
		}
	}
	jsonData, err := json.Marshal(payload)
	if err != nil {
		log.Error().Err(err).Str("hook_url", hookURL).Str("link", link).Msg("failed to marshal hook payload")
		return
	}
	req, err := http.NewRequest("POST", hookURL, bytes.NewBuffer(jsonData))
	if err != nil {
		log.Error().Err(err).Str("hook_url", hookURL).Str("link", link).Msg("failed to create hook request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Error().Err(err).Str("hook_url", hookURL).Str("link", link).Msg("failed to send hook")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Warn().Int("status", resp.StatusCode).Str("hook_url", hookURL).Str("link", link).Msg("hook response not ok")
	} else {
		log.Info().Str("hook_url", hookURL).Str("link", link).Msg("hook sent successfully")
	}
}

func resolveURL(url string) string {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	resp, err := client.Head(url)
	if err != nil {
		return url
	}
	defer resp.Body.Close()
	return resp.Request.URL.String()
}

type LinkRow struct {
	MessageID string `json:"message_id"`
	URL       string `json:"url"`
	TSMillis  int64  `json:"ts_ms"`
	Sender    string `json:"sender"`
}

func ExportAllSnapshots(db *sql.DB, rooms []RoomIDEntry, path string) error {
	// Create a map from roomID to comment
	roomMap := make(map[string]string)
	for _, r := range rooms {
		roomMap[r.ID] = r.Comment
	}
	rows, err := db.Query(`
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

// BlacklistEntry represents a regex pattern and comment from blacklist.json
type BlacklistEntry struct {
	Pattern string `json:"pattern"`
	Comment string `json:"comment"`
}

// LoadBlacklist loads blacklist.json and compiles regex patterns
func LoadBlacklist(path string) ([]*regexp.Regexp, error) {
	var entries []BlacklistEntry
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	dec := json.NewDecoder(file)
	if err := dec.Decode(&entries); err != nil {
		return nil, err
	}
	var regexps []*regexp.Regexp
	for _, entry := range entries {
		re, err := regexp.Compile(entry.Pattern)
		if err != nil {
			return nil, err
		}
		regexps = append(regexps, re)
	}
	return regexps, nil
}

// IsBlacklisted checks if a URL matches any blacklist regex
func IsBlacklisted(url string, blacklist []*regexp.Regexp) bool {
	for _, re := range blacklist {
		if re.MatchString(url) {
			return true
		}
	}
	return false
}

// App holds the runtime dependencies for handling Matrix events.
type App struct {
	Cfg        *Config
	MessagesDB *sql.DB
	BotCfg     *BotConfig
	Client     *mautrix.Client
	ReadyChan  <-chan bool
}

// resolveReplyLabel returns the reply label with precedence:
// config.BOT_REPLY_LABEL -> bot.json label -> default "> ".
func resolveReplyLabel(cfg *Config, botCfg *BotConfig) string {
	if cfg != nil && cfg.BotReplyLabel != "" {
		return cfg.BotReplyLabel
	}
	if botCfg != nil && botCfg.Label != "" {
		return botCfg.Label
	}
	return "> "
}

// sendBotReply sends a text reply to the given event.
func sendBotReply(ctx context.Context, client *mautrix.Client, roomID id.RoomID, eventID id.EventID, body, cmd string) {
	content := event.MessageEventContent{
		MsgType:   event.MsgText,
		Body:      body,
		RelatesTo: &event.RelatesTo{InReplyTo: &event.InReplyTo{EventID: eventID}},
	}
	if _, err := client.SendMessageEvent(ctx, roomID, event.EventMessage, &content); err != nil {
		log.Error().Err(err).Str("cmd", cmd).Msg("failed to send response")
	} else {
		log.Info().Str("cmd", cmd).Msg("sent bot response")
	}
}

// handleMessage processes an incoming Matrix message event.
func (app *App) handleMessage(evCtx context.Context, ev *event.Event) {
	currentRoom, ok := app.findRoom(ev.RoomID)
	if len(app.Cfg.RoomIDs) > 0 && !ok {
		return
	}

	msgData, err := ProcessMessageEvent(ev)
	if err != nil {
		log.Warn().Err(err).Str("event_id", string(ev.ID)).Msg("failed to parse event")
		return
	}
	if msgData == nil {
		return
	}
	if err := StoreMessage(app.MessagesDB, msgData); err != nil {
		log.Error().Err(err).Str("event_id", string(ev.ID)).Msg("store event")
		return
	}
	log.Info().Str("room", currentRoom.Comment).Str("sender", string(ev.Sender)).Msg(truncate(msgData.Msg.Body, 100))

	// Skip messages that contain the bot's own reply label.
	if app.Cfg.BotReplyLabel != "" && strings.Contains(msgData.Msg.Body, app.Cfg.BotReplyLabel) {
		log.Debug().Str("label", app.Cfg.BotReplyLabel).Msg("skipped bot processing due to bot reply label")
		return
	}

	// Handle bot commands.
	if currentRoom.AllowedCommands != nil && (strings.HasPrefix(msgData.Msg.Body, "/bot") || strings.HasPrefix(msgData.Msg.Body, "@gork")) {
		app.dispatchBotCommand(evCtx, ev, msgData, currentRoom)
		return
	}

	// Handle links.
	app.processLinks(evCtx, ev, msgData, currentRoom)
}

// findRoom returns the RoomIDEntry matching the given room ID.
func (app *App) findRoom(roomID id.RoomID) (RoomIDEntry, bool) {
	for _, r := range app.Cfg.RoomIDs {
		if string(roomID) == r.ID {
			return r, true
		}
	}
	return RoomIDEntry{}, false
}

// dispatchBotCommand parses and dispatches a bot command.
func (app *App) dispatchBotCommand(evCtx context.Context, ev *event.Event, msgData *MessageData, room RoomIDEntry) {
	if app.Cfg.DryRun {
		log.Info().Msg("dry run mode: skipping bot command")
		return
	}
	select {
	case <-app.ReadyChan:
	case <-evCtx.Done():
		return
	}

	normalizedBody := msgData.Msg.Body
	if strings.HasPrefix(msgData.Msg.Body, "@gork") {
		normalizedBody = "/bot gork " + strings.TrimSpace(strings.TrimPrefix(msgData.Msg.Body, "@gork"))
	}
	parts := strings.Fields(normalizedBody)
	cmd := "hi"
	if len(parts) >= 2 && parts[1] != "" {
		cmd = parts[1]
	}

	label := resolveReplyLabel(app.Cfg, app.BotCfg)

	// Check command permissions.
	if len(room.AllowedCommands) > 0 && !inSlice(room.AllowedCommands, cmd) && cmd != "hi" {
		sendBotReply(evCtx, app.Client, ev.RoomID, ev.ID, label+"command not allowed in this room", cmd)
		return
	}

	if app.BotCfg == nil {
		sendBotReply(evCtx, app.Client, ev.RoomID, ev.ID, label+"no bot configuration loaded", cmd)
		return
	}

	if cmd == "help" {
		sendBotReply(evCtx, app.Client, ev.RoomID, ev.ID, label+generateHelpMessage(app.BotCfg, room.AllowedCommands), cmd)
		return
	}

	cmdCfg, ok := app.BotCfg.Commands[cmd]
	if !ok {
		sendBotReply(evCtx, app.Client, ev.RoomID, ev.ID, label+"Unknown command. "+generateHelpMessage(app.BotCfg, room.AllowedCommands), cmd)
		return
	}

	// Run the command in a goroutine to avoid blocking other messages.
	go func() {
		resp, err := FetchBotCommand(evCtx, &cmdCfg, app.Cfg.LinkstashURL, ev, app.Client, app.Cfg.GroqAPIKey, label, app.MessagesDB)
		var body string
		if err != nil {
			log.Error().Err(err).Str("cmd", cmd).Msg("failed to execute bot command")
			body = fmt.Sprintf("sorry, couldn't execute %s right now", cmd)
		} else if resp != "" {
			body = resp
		} else {
			return // Command sent its own message (like images).
		}
		sendBotReply(evCtx, app.Client, ev.RoomID, ev.ID, label+body, cmd)
	}()
}

// processLinks handles link extraction, hooks, and snapshot exports.
func (app *App) processLinks(_ context.Context, ev *event.Event, msgData *MessageData, room RoomIDEntry) {
	if len(msgData.URLs) == 0 {
		log.Debug().Msg("no links found")
		return
	}

	log.Info().Int("count", len(msgData.URLs)).Msg("found links:")
	for _, u := range msgData.URLs {
		log.Info().Str("url", u).Msg("link")
	}

	if app.Cfg.OptOutTag != "" && strings.Contains(msgData.Msg.Body, app.Cfg.OptOutTag) {
		log.Info().Str("tag", app.Cfg.OptOutTag).Msg("skipped sending hooks due to opt-out tag")
	} else if app.Cfg.DryRun {
		log.Info().Msg("dry run mode: skipping hooks")
	} else {
		blacklist, err := LoadBlacklist("blacklist.json")
		if err != nil {
			log.Error().Err(err).Msg("failed to load blacklist")
		}
		if room.Hook != "" {
			for _, u := range msgData.URLs {
				if blacklist != nil && IsBlacklisted(u, blacklist) {
					log.Info().Str("url", u).Msg("skipped blacklisted url")
					continue
				}
				go sendHook(room.Hook, u, room.Key, string(ev.Sender), room.ID, room.Comment, room.SendUser, room.SendTopic)
			}
		}
	}

	log.Info().Msg("stored to db, exporting snapshot...")
	if err := ExportAllSnapshots(app.MessagesDB, app.Cfg.RoomIDs, app.Cfg.LinksPath); err != nil {
		log.Error().Err(err).Msg("export snapshots")
	} else {
		log.Info().Str("path", app.Cfg.LinksPath).Msg("exported")
	}
}

// main initializes the application, loads config, sets up databases, and starts the bot.
func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	log.Debug().Msg("starting")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg, err := LoadConfig()
	must(err, "load config")
	if cfg.Debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}
	log.Debug().Msg("config loaded")

	metaDB, err := OpenMeta(ctx, cfg.MetaDBPath)
	must(err, "open meta db")
	defer metaDB.Close()

	must(EnsureSecrets(ctx, metaDB, cfg), "ensure secrets")

	messagesDB, err := OpenMessages(ctx, cfg.DBPath)
	must(err, "open messages db")
	defer messagesDB.Close()

	_, err = EnsurePickleKey(ctx, metaDB)
	must(err, "ensure pickle key")

	must(run(ctx, metaDB, messagesDB, cfg), "run")
	log.Debug().Msg("exiting")
}

// run starts the Matrix client, sets up sync, and handles messages.
func run(ctx context.Context, metaDB *sql.DB, messagesDB *sql.DB, cfg *Config) error {
	log.Info().Msgf("logging in as %s to %s (E2EE initializing)", cfg.User, cfg.Homeserver)
	var roomNames []string
	for _, r := range cfg.RoomIDs {
		roomNames = append(roomNames, r.Comment)
	}
	log.Info().Msgf("ready: watching rooms: [%s]", strings.Join(roomNames, ", "))

	client, err := LoadOrCreate(ctx, metaDB, cfg)
	if err != nil {
		return err
	}
	client.SyncPresence = "offline"
	syncer := mautrix.NewDefaultSyncer()
	client.Syncer = syncer
	client.Store = &MetaSyncStore{DB: metaDB}

	cryptoHelper, err := SetupHelper(ctx, client, metaDB, cfg.MetaDBPath)
	if err != nil {
		return err
	}
	client.Crypto = cryptoHelper
	if err := VerifyWithRecoveryKey(ctx, cryptoHelper.Machine(), cfg.RecoveryKey); err != nil {
		log.Warn().Err(err).Msg("failed to verify session with recovery key")
	}

	// Load bot configuration (optional).
	botCfgPath := cfg.BotConfigPath
	if botCfgPath == "" {
		botCfgPath = "./bot.json"
	}
	botCfg, err := LoadBotConfig(botCfgPath)
	if err != nil {
		log.Warn().Err(err).Str("path", botCfgPath).Msg("failed to load bot config (continuing without)")
	} else {
		log.Info().Str("path", botCfgPath).Msg("loaded bot config")
	}

	readyChan := make(chan bool)
	var once sync.Once
	syncer.OnSync(func(_ context.Context, _ *mautrix.RespSync, _ string) bool {
		once.Do(func() { close(readyChan) })
		return true
	})

	app := &App{
		Cfg:        cfg,
		MessagesDB: messagesDB,
		BotCfg:     botCfg,
		Client:     client,
		ReadyChan:  readyChan,
	}
	syncer.OnEventType(event.EventMessage, app.handleMessage)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error().Msgf("sync goroutine panic: %v", r)
			}
		}()
		log.Debug().Msg("starting sync")
		if err := client.Sync(); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("sync error")
		}
	}()

	select {
	case <-readyChan:
	case <-ctx.Done():
		return ctx.Err()
	}
	<-ctx.Done()
	log.Debug().Msg("exiting run")
	return ctx.Err()
}

// ---------------------------------------------------------------------------
// Utility helpers
// ---------------------------------------------------------------------------

func inSlice(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func must(err error, context string) {
	if err != nil {
		log.Fatal().Err(err).Msgf("%s", context)
	}
}
