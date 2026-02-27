package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/sashabaranov/go-openai"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

const defaultContentType = "image/jpeg"

// knockKnockJoke holds a single knock-knock joke.
type knockKnockJoke struct {
	Name      string
	Punchline string
}

var knockKnockJokes = []knockKnockJoke{
	{"Lettuce", "Lettuce in, it's cold out here!"},
	{"Atch", "Bless you!"},
	{"Nobel", "Nobel, that's why I knocked!"},
	{"Cow says", "No, a cow says moo!"},
	{"Interrupting cow", "MOO!"},
	{"Who"," ‼️ That's the sound of da police ‼️ "},
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

// knockKnockStep tracks the current step in a knock-knock joke conversation.
type knockKnockStep struct {
	Joke  knockKnockJoke
	Step  int // 0 = waiting for "who's there?", 1 = waiting for "<name> who?"
	Label string
}

// KnockKnockState manages pending knock-knock joke conversations.
type KnockKnockState struct {
	mu      sync.Mutex
	pending map[id.EventID]*knockKnockStep
}

// NewKnockKnockState creates a new KnockKnockState.
func NewKnockKnockState() *KnockKnockState {
	return &KnockKnockState{pending: make(map[id.EventID]*knockKnockStep)}
}

func (s *KnockKnockState) Set(evID id.EventID, step *knockKnockStep) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[evID] = step
}

func (s *KnockKnockState) Get(evID id.EventID) (*knockKnockStep, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.pending[evID]
	return v, ok
}

func (s *KnockKnockState) Delete(evID id.EventID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pending, evID)
}

// BotCommand describes a bot command that can return text or images.
type BotCommand struct {
	Type         string                 `json:"type"` // "http", "exec", "ai"
	Method       string                 `json:"method,omitempty"`
	URL          string                 `json:"url,omitempty"`
	Headers      map[string]string      `json:"headers,omitempty"`
	JSONPath     string                 `json:"json_path,omitempty"`
	ResponseType string                 `json:"response_type,omitempty"` // "text", "json", "image"
	Command      string                 `json:"command,omitempty"`       // for exec
	Args         []string               `json:"args,omitempty"`          // for exec
	InputType    string                 `json:"input_type,omitempty"`    // "none", "text", "image"
	OutputType   string                 `json:"output_type,omitempty"`   // "text", "image"
	Model        string                 `json:"model,omitempty"`         // for ai
	MaxTokens    int                    `json:"max_tokens,omitempty"`    // for ai
	Prompt       string                 `json:"prompt,omitempty"`        // for ai
	Response     string                 `json:"response,omitempty"`      // static response
	Params       map[string]interface{} `json:"params,omitempty"`
	Mention      bool                   `json:"mention,omitempty"` // tag users in output
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
// Shared helpers
// ---------------------------------------------------------------------------

// sendImageToMatrix uploads and sends an image as a reply to Matrix.
func sendImageToMatrix(ctx context.Context, client *mautrix.Client, roomID id.RoomID, eventID id.EventID, imageData []byte, contentType, body string) error {
	uploadResp, err := client.UploadBytes(ctx, imageData, contentType)
	if err != nil {
		return fmt.Errorf("upload image: %w", err)
	}
	content := event.MessageEventContent{
		MsgType:   event.MsgImage,
		Body:      body,
		URL:       uploadResp.ContentURI.CUString(),
		RelatesTo: &event.RelatesTo{InReplyTo: &event.InReplyTo{EventID: eventID}},
	}
	if _, err := client.SendMessageEvent(ctx, roomID, event.EventMessage, &content); err != nil {
		return fmt.Errorf("send image: %w", err)
	}
	return nil
}

// downloadImageFromMessage extracts the image from a message or its replied-to
// message, returning the message content containing the media URL.
func downloadImageFromMessage(ctx context.Context, client *mautrix.Client, ev *event.Event) (*event.MessageEventContent, error) {
	parseEvent(ev)
	msg := ev.Content.AsMessage()
	if msg == nil {
		return nil, fmt.Errorf("not a message event")
	}

	if isImageMessage(msg) {
		return msg, nil
	}

	// Check replied-to message.
	if msg.RelatesTo == nil || msg.RelatesTo.InReplyTo == nil {
		return nil, fmt.Errorf("no image found")
	}
	original, err := fetchAndDecrypt(ctx, client, ev.RoomID, msg.RelatesTo.InReplyTo.EventID)
	if err != nil {
		return nil, err
	}
	origMsg := original.Content.AsMessage()
	if origMsg != nil && isImageMessage(origMsg) {
		return origMsg, nil
	}
	return nil, fmt.Errorf("no image found")
}

// downloadImageBytes downloads image data from a Matrix content URI, handling
// decryption if the file is encrypted.
func downloadImageBytes(ctx context.Context, client *mautrix.Client, mediaURL id.ContentURIString, encryptedFile *event.EncryptedFileInfo) ([]byte, error) {
	if mediaURL == "" {
		return nil, fmt.Errorf("no media URL")
	}
	parsed, err := id.ParseContentURI(string(mediaURL))
	if err != nil {
		return nil, fmt.Errorf("parse media URL: %w", err)
	}
	data, err := client.DownloadBytes(ctx, parsed)
	if err != nil {
		return nil, fmt.Errorf("download image: %w", err)
	}
	if encryptedFile != nil {
		if err := encryptedFile.PrepareForDecryption(); err != nil {
			return nil, fmt.Errorf("prepare decryption: %w", err)
		}
		data, err = encryptedFile.Decrypt(data)
		if err != nil {
			return nil, fmt.Errorf("decrypt image: %w", err)
		}
	}
	return data, nil
}

// mediaFromMessage returns the media URL and optional encrypted file info from
// a message content.
func mediaFromMessage(msg *event.MessageEventContent) (id.ContentURIString, *event.EncryptedFileInfo, error) {
	if msg.File != nil {
		return msg.File.URL, msg.File, nil
	}
	if msg.URL != "" {
		return msg.URL, nil, nil
	}
	return "", nil, fmt.Errorf("no media URL")
}

// detectImageExtension uses the `file` command to determine the image type.
func detectImageExtension(inputPath string) string {
	out, err := exec.Command("file", inputPath).Output()
	if err != nil {
		return ".png"
	}
	lower := strings.ToLower(string(out))
	switch {
	case strings.Contains(lower, "jpeg") || strings.Contains(lower, "jpg"):
		return ".jpg"
	case strings.Contains(lower, "png"):
		return ".png"
	case strings.Contains(lower, "gif"):
		return ".gif"
	case strings.Contains(lower, "webp") || strings.Contains(lower, "web/p"):
		return ".webp"
	default:
		return ".png"
	}
}

// callGroq sends a prompt to the Groq API and returns the response text.
func callGroq(ctx context.Context, apiKey, model string, maxTokens int, prompt string) (string, error) {
	if apiKey == "" {
		return "", fmt.Errorf("GROQ_API_KEY not set")
	}
	if model == "" {
		model = "openai/gpt-oss-120b"
	}
	if maxTokens == 0 {
		maxTokens = 300
	}
	cfg := openai.DefaultConfig(apiKey)
	cfg.BaseURL = "https://api.groq.com/openai/v1"
	resp, err := openai.NewClientWithConfig(cfg).CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:     model,
		Messages:  []openai.ChatCompletionMessage{{Role: "user", Content: prompt}},
		MaxTokens: maxTokens,
	})
	if err != nil {
		return "", fmt.Errorf("groq api: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from groq")
	}
	return resp.Choices[0].Message.Content, nil
}

// parseEvent safely parses the raw content of an event, ignoring "already
// parsed" errors.
func parseEvent(ev *event.Event) {
	if ev.Content.Raw != nil {
		if err := ev.Content.ParseRaw(ev.Type); err != nil {
			if !strings.Contains(err.Error(), "already parsed") {
				log.Warn().Err(err).Str("event_id", string(ev.ID)).Msg("parse event")
			}
		}
	}
}

// fetchAndDecrypt fetches a Matrix event and decrypts it if encrypted.
func fetchAndDecrypt(ctx context.Context, client *mautrix.Client, roomID id.RoomID, eventID id.EventID) (*event.Event, error) {
	ev, err := client.GetEvent(ctx, roomID, eventID)
	if err != nil {
		return nil, fmt.Errorf("fetch event %s: %w", eventID, err)
	}
	if ev.Content.Raw != nil {
		if err := ev.Content.ParseRaw(ev.Type); err != nil {
			return nil, fmt.Errorf("parse event: %w", err)
		}
	}
	if ev.Type == event.EventEncrypted && client.Crypto != nil {
		decrypted, err := client.Crypto.Decrypt(ctx, ev)
		if err != nil {
			return nil, fmt.Errorf("decrypt event: %w", err)
		}
		return decrypted, nil
	}
	return ev, nil
}

// isImageMessage checks whether a message contains an image.
func isImageMessage(msg *event.MessageEventContent) bool {
	return msg.MsgType == event.MsgImage || msg.MsgType == "m.sticker" || msg.URL != "" || msg.File != nil
}

// truncateText truncates text to roughly fit within a token budget.
func truncateText(text string, tokenLimit int) string {
	estimated := len(text) / 4
	if estimated <= tokenLimit {
		return text
	}
	maxChars := tokenLimit * 4
	if len(text) > maxChars {
		text = text[:maxChars]
	}
	if last := strings.LastIndex(text, "\n"); last > maxChars/2 {
		text = text[:last]
	} else if last := strings.LastIndex(text, " "); last > maxChars/2 {
		text = text[:last]
	}
	return text
}

// downloadExternalImage downloads an image from a URL and returns the bytes
// and content type.
func downloadExternalImage(url string) ([]byte, string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, "", fmt.Errorf("download image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("image download status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read image data: %w", err)
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = defaultContentType
	}
	return data, ct, nil
}

// ---------------------------------------------------------------------------
// FetchBotCommand — main dispatcher
// ---------------------------------------------------------------------------

// FetchBotCommand executes the configured command and returns a string to post.
func FetchBotCommand(ctx context.Context, c *BotCommand, linkstashURL string, ev *event.Event, matrixClient *mautrix.Client, groqAPIKey string, replyLabel string, messagesDB *sql.DB) (string, error) {
	if c.Response != "" {
		return c.Response, nil
	}
	switch c.Type {
	case "http":
		return handleHttpCommand(ctx, c, linkstashURL, ev, matrixClient)
	case "exec":
		return handleExecCommand(ctx, ev, matrixClient, c)
	case "ai":
		return handleAiCommand(ctx, ev, matrixClient, c, groqAPIKey, replyLabel)
	case "builtin":
		return handleBuiltinCommand(ctx, ev, matrixClient, c, messagesDB, replyLabel)
	default:
		return "", fmt.Errorf("unknown command type: %s", c.Type)
	}
}

// ---------------------------------------------------------------------------
// Command handlers
// ---------------------------------------------------------------------------

// handleHttpCommand handles HTTP-based commands.
func handleHttpCommand(ctx context.Context, c *BotCommand, linkstashURL string, ev *event.Event, matrixClient *mautrix.Client) (string, error) {
	method := c.Method
	if method == "" {
		method = "GET"
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL, nil)
	if err != nil {
		return "", err
	}
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	// Try JSON parsing if a path is specified or content-type indicates JSON.
	if c.JSONPath != "" || strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "application/json") {
		var j interface{}
		if err := json.Unmarshal(bodyBytes, &j); err != nil {
			return strings.TrimSpace(string(bodyBytes)), nil
		}
		v := extractJSONPath(j, c.JSONPath)
		if s, ok := v.(string); ok {
			if c.OutputType == "image" {
				go func(url string) {
					defer func() {
						if r := recover(); r != nil {
							log.Error().Interface("panic", r).Msg("panic in http image download")
						}
					}()
					data, ct, err := downloadExternalImage(url)
					if err != nil {
						log.Warn().Err(err).Str("url", url).Msg("image download failed")
						return
					}
					if err := sendImageToMatrix(context.Background(), matrixClient, ev.RoomID, ev.ID, data, ct, "image.jpg"); err != nil {
						log.Warn().Err(err).Msg("send image failed")
					}
				}(s)
				return "", nil
			}
			return strings.TrimSpace(s), nil
		}
		if arr, ok := v.([]interface{}); ok {
			return formatPosts(arr, linkstashURL), nil
		}
		if v != nil {
			b, _ := json.Marshal(v)
			return strings.TrimSpace(string(b)), nil
		}
		return "", fmt.Errorf("no value found at path: %s", c.JSONPath)
	}
	return strings.TrimSpace(string(bodyBytes)), nil
}

// handleExecCommand handles executable commands.
func handleExecCommand(ctx context.Context, ev *event.Event, matrixClient *mautrix.Client, c *BotCommand) (string, error) {
	var inputPath string
	var tmpFiles []string
	defer func() {
		for _, f := range tmpFiles {
			_ = os.Remove(f)
		}
	}()

	if c.InputType == "image" {
		// Use shared helpers to get image data.
		imgMsg, err := downloadImageFromMessage(ctx, matrixClient, ev)
		if err != nil {
			return "reply to an image to use this command", nil
		}
		mediaURL, encFile, err := mediaFromMessage(imgMsg)
		if err != nil {
			return "", err
		}
		data, err := downloadImageBytes(ctx, matrixClient, mediaURL, encFile)
		if err != nil {
			return "", err
		}

		// Write to temp file.
		tmpDir := "data/tmp"
		_ = os.MkdirAll(tmpDir, 0755)
		tmpFile, err := os.CreateTemp(tmpDir, "exec_input_*.tmp")
		if err != nil {
			return "", fmt.Errorf("create temp input: %w", err)
		}
		tmpFiles = append(tmpFiles, tmpFile.Name())
		if _, err := tmpFile.Write(data); err != nil {
			tmpFile.Close()
			return "", fmt.Errorf("write image data: %w", err)
		}
		tmpFile.Close()

		// Detect file type and rename with proper extension.
		ext := detectImageExtension(tmpFile.Name())
		newName := strings.TrimSuffix(tmpFile.Name(), ".tmp") + ext
		if err := os.Rename(tmpFile.Name(), newName); err != nil {
			inputPath = tmpFile.Name()
		} else {
			inputPath = newName
			tmpFiles = append(tmpFiles, newName)
		}
	}

	// Prepare args, replacing {input}/{output} placeholders.
	args := make([]string, len(c.Args))
	var outputPath string
	for i, arg := range c.Args {
		switch arg {
		case "{input}":
			args[i] = inputPath
		case "{output}":
			out, err := os.CreateTemp("data/tmp", "exec_output_*")
			if err != nil {
				return "", fmt.Errorf("create output file: %w", err)
			}
			outputPath = out.Name()
			args[i] = outputPath
			out.Close()
			tmpFiles = append(tmpFiles, outputPath)
		default:
			args[i] = arg
		}
	}

	// Run.
	cmd := exec.Command(c.Command, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("exec failed: %w, stderr: %s", err, stderr.String())
	}

	// Handle output.
	if c.OutputType == "image" {
		data, err := os.ReadFile(outputPath)
		if err != nil {
			return "", fmt.Errorf("read processed image: %w", err)
		}
		if err := sendImageToMatrix(ctx, matrixClient, ev.RoomID, ev.ID, data, defaultContentType, "processed.jpg"); err != nil {
			return "", err
		}
		return "", nil
	}
	return strings.TrimSpace(stdout.String()), nil
}

// handleAiCommand handles AI-based commands using the Groq API.
func handleAiCommand(ctx context.Context, ev *event.Event, matrixClient *mautrix.Client, c *BotCommand, groqAPIKey string, replyLabel string) (string, error) {
	var targetText string
	var originalEventID id.EventID

	if strings.Contains(c.Prompt, "articles") {
		text, err := fetchArticleContents(ctx)
		if err != nil {
			return "", err
		}
		if text == "" {
			return "No articles to summarize.", nil
		}
		targetText = truncateText(text, 6000)
	} else {
		parseEvent(ev)
		msg := ev.Content.AsMessage()
		if msg == nil {
			return "", fmt.Errorf("not a message event")
		}
		if msg.Body == "" {
			return "No message to respond to.", nil
		}

		var originalText string
		if msg.RelatesTo != nil && msg.RelatesTo.InReplyTo != nil {
			original, err := fetchAndDecrypt(ctx, matrixClient, ev.RoomID, msg.RelatesTo.InReplyTo.EventID)
			if err != nil {
				log.Warn().Err(err).Msg("failed to fetch replied-to message")
			} else if om := original.Content.AsMessage(); om != nil {
				originalEventID = original.ID
				originalText = om.Body
			}
		}

		if originalText != "" {
			suffix := stripCommandPrefix(msg.Body)
			if suffix != "" {
				targetText = fmt.Sprintf("respond to: %s, %s", strings.TrimSpace(originalText), suffix)
			} else {
				targetText = fmt.Sprintf("respond to: %s", strings.TrimSpace(originalText))
			}
		} else {
			parts := strings.Fields(msg.Body)
			if len(parts) >= 2 {
				targetText = strings.TrimSpace(strings.TrimPrefix(msg.Body, parts[0]+" "+parts[1]))
			} else {
				targetText = strings.TrimSpace(msg.Body)
			}
		}
		targetText = truncateText(targetText, 2000)
	}

	prompt := c.Prompt + "\n\n" + targetText
	response, err := callGroq(ctx, groqAPIKey, c.Model, c.MaxTokens, prompt)
	if err != nil {
		return "", err
	}

	// If we resolved a replied-to event, reply directly to that event.
	if originalEventID != "" {
		label := replyLabel
		if label == "" {
			label = "> "
		}
		content := event.MessageEventContent{
			MsgType:   event.MsgText,
			Body:      label + response,
			RelatesTo: &event.RelatesTo{InReplyTo: &event.InReplyTo{EventID: originalEventID}},
		}
		if _, err := matrixClient.SendMessageEvent(ctx, ev.RoomID, event.EventMessage, &content); err != nil {
			return "", fmt.Errorf("send reply: %w", err)
		}
		return "", nil
	}
	return response, nil
}

// ---------------------------------------------------------------------------
// Builtin text-transform commands
// ---------------------------------------------------------------------------

// handleBuiltinCommand handles built-in text transformation and query commands.
func handleBuiltinCommand(ctx context.Context, ev *event.Event, matrixClient *mautrix.Client, c *BotCommand, messagesDB *sql.DB, replyLabel string) (string, error) {
	// Check for DB-backed builtins first.
	if dbFn, ok := builtinDBFuncs[c.Command]; ok {
		parseEvent(ev)
		msg := ev.Content.AsMessage()
		if msg == nil {
			return "", fmt.Errorf("not a message event")
		}
		// Extract inline args after "/bot <cmd>".
		var args string
		parts := strings.Fields(msg.Body)
		if len(parts) > 2 {
			args = strings.TrimSpace(strings.Join(parts[2:], " "))
		}
		return dbFn(ctx, messagesDB, matrixClient, ev, args, replyLabel, c.Mention)
	}

	// Text-transform builtins.
	parseEvent(ev)
	msg := ev.Content.AsMessage()
	if msg == nil {
		return "", fmt.Errorf("not a message event")
	}

	var targetText string

	// Check if replying to another message.
	if msg.RelatesTo != nil && msg.RelatesTo.InReplyTo != nil {
		original, err := fetchAndDecrypt(ctx, matrixClient, ev.RoomID, msg.RelatesTo.InReplyTo.EventID)
		if err == nil {
			if om := original.Content.AsMessage(); om != nil {
				targetText = om.Body
			}
		}
	}

	// If no replied-to text, use the inline text after the command.
	if targetText == "" {
		parts := strings.Fields(msg.Body)
		if len(parts) > 2 {
			targetText = strings.TrimSpace(strings.Join(parts[2:], " "))
		}
	}

	if targetText == "" {
		return "uwu~ pwease give me some text to twansfowm!", nil
	}

	// Dispatch to the appropriate builtin function.
	fn, ok := builtinFuncs[c.Command]
	if !ok {
		return "", fmt.Errorf("unknown builtin: %s", c.Command)
	}
	return fn(targetText), nil
}

// builtinFuncs maps builtin command names to their Go functions.
var builtinFuncs = map[string]func(string) string{
	"uwuify": uwuify,
}

// builtinDBFuncs maps builtin command names that need DB access.
var builtinDBFuncs = map[string]func(context.Context, *sql.DB, *mautrix.Client, *event.Event, string, string, bool) (string, error){
	"yap": queryTopYappers,
}

// queryTopYappers returns the top N message senders in the last 24h for the
// current room, excluding messages that start with the bot label (e.g. [BOT]).
// It sends its own HTML-formatted message with user mention pills and returns
// "", nil so the caller does not send a duplicate.
func queryTopYappers(ctx context.Context, db *sql.DB, matrixClient *mautrix.Client, ev *event.Event, args string, replyLabel string, mention bool) (string, error) {
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
	cutoff := time.Now().Add(-24 * time.Hour).UnixMilli()

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
		return "no messages found in the last 24h", nil
	}

	// Build plain text and HTML versions.
	var plain, html strings.Builder
	plain.WriteString(replyLabel + "top yappers (last 24h):\n")
	html.WriteString(replyLabel + "top yappers (last 24h):<br>")
	for i, e := range entries {
		plain.WriteString(fmt.Sprintf("%d. %s \u2014 %d words\n", i+1, e.display, e.count))
		if mention {
			// Matrix user mention pill: <a href="https://matrix.to/#/@user:server">Display</a>
			html.WriteString(fmt.Sprintf("%d. <a href=\"https://matrix.to/#/%s\">%s</a> \u2014 %d words<br>", i+1, e.senderID, e.display, e.count))
		} else {
			html.WriteString(fmt.Sprintf("%d. %s \u2014 %d words<br>", i+1, e.display, e.count))
		}
	}

	// Send the formatted message directly (like image commands).
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
		return "", nil // Message already sent.
	}

	// Fallback for tests or when no client is available.
	return strings.TrimSpace(plain.String()), nil
}

// queryYapGuess handles "/bot yap guess N". It looks up the caller's actual
// position on the 24h word-count leaderboard and reports the difference from
// the guessed position.
func queryYapGuess(ctx context.Context, db *sql.DB, matrixClient *mautrix.Client, ev *event.Event, guessArg string, replyLabel string) (string, error) {
	guess := 1
	if guessArg != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(guessArg)); err == nil && n > 0 {
			guess = n
		}
	}

	roomID := string(ev.RoomID)
	senderID := string(ev.Sender)
	cutoff := time.Now().Add(-24 * time.Hour).UnixMilli()

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
		return replyLabel + "you have no messages in the last 24h!", nil
	}

	diff := guess - actualPos // positive = guessed lower rank (higher number) than actual
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

// uwuify transforms text into uwu-speak.
func uwuify(text string) string {
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

	// Add stutter to some words (first word that starts with a letter).
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

// ---------------------------------------------------------------------------
// AI helpers
// ---------------------------------------------------------------------------

// stripCommandPrefix removes common bot command prefixes from a message body.
func stripCommandPrefix(body string) string {
	s := strings.TrimSpace(body)
	for _, prefix := range []string{"/bot gork ", "/bot gork", "/bot"} {
		s = strings.TrimPrefix(s, prefix)
	}
	if strings.HasPrefix(strings.ToLower(s), "@gork") {
		s = s[len("@gork"):]
	}
	s = strings.TrimLeft(strings.TrimSpace(s), ":, ")
	return strings.TrimSpace(s)
}

// fetchArticleContents fetches and combines article contents from linkstash.
func fetchArticleContents(ctx context.Context) (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://linkstash.hsp-ec.xyz/api/summary", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var data struct {
		Summary []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			URL   string `json:"url"`
		} `json:"summary"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	if len(data.Summary) == 0 {
		return "", nil
	}

	var contents []string
	for _, article := range data.Summary {
		contentURL := fmt.Sprintf("https://linkstash.hsp-ec.xyz/api/content/%s", article.ID)
		req, err := http.NewRequestWithContext(ctx, "GET", contentURL, nil)
		if err != nil {
			log.Warn().Err(err).Str("id", article.ID).Msg("failed to create content request")
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			log.Warn().Err(err).Str("id", article.ID).Msg("failed to fetch content")
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			log.Warn().Int("status", resp.StatusCode).Str("id", article.ID).Msg("bad content response")
			continue
		}
		contents = append(contents, string(body))
	}
	if len(contents) == 0 {
		return "", nil
	}
	return strings.Join(contents, "\n\n---\n\n"), nil
}

// ---------------------------------------------------------------------------
// JSON / formatting helpers
// ---------------------------------------------------------------------------

// extractJSONPath extracts a value from parsed JSON using a dot-separated path.
func extractJSONPath(root interface{}, path string) interface{} {
	if path == "" {
		return root
	}
	cur := root
	for _, p := range strings.Split(path, ".") {
		if m, ok := cur.(map[string]interface{}); ok {
			cur = m[p]
		} else if arr, ok := cur.([]interface{}); ok {
			// Try to parse p as an index if it's an array.
			var idx int
			if _, err := fmt.Sscanf(p, "%d", &idx); err == nil && idx >= 0 && idx < len(arr) {
				cur = arr[idx]
			} else {
				return nil
			}
		} else {
			return nil
		}
	}
	return cur
}

// formatPosts formats an array of post objects into a readable string.
func formatPosts(posts []interface{}, linkstashURL string) string {
	var sb strings.Builder
	limit := 5
	if len(posts) < limit {
		limit = len(posts)
	}
	for i := 0; i < limit; i++ {
		if m, ok := posts[i].(map[string]interface{}); ok {
			title, _ := m["title"].(string)
			url, _ := m["url"].(string)
			if title != "" && url != "" {
				sb.WriteString(fmt.Sprintf("- %s (%s)\n", title, url))
			}
		}
	}
	sb.WriteString(fmt.Sprintf("\nSee full list: %s", linkstashURL))
	return sb.String()
}
