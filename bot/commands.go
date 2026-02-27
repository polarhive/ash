package bot

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/sashabaranov/go-openai"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/polarhive/ash/matrix"
	"github.com/polarhive/ash/util"
)

const defaultContentType = "image/jpeg"

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

	if c.JSONPath != "" || strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "application/json") {
		var j interface{}
		if err := json.Unmarshal(bodyBytes, &j); err != nil {
			return strings.TrimSpace(string(bodyBytes)), nil
		}
		v := util.ExtractJSONPath(j, c.JSONPath)
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
					if err := matrix.SendImageToMatrix(context.Background(), matrixClient, ev.RoomID, ev.ID, data, ct, "image.jpg"); err != nil {
						log.Warn().Err(err).Msg("send image failed")
					}
				}(s)
				return "", nil
			}
			return strings.TrimSpace(s), nil
		}
		if arr, ok := v.([]interface{}); ok {
			return util.FormatPosts(arr, linkstashURL), nil
		}
		if v != nil {
			b, _ := json.Marshal(v)
			return strings.TrimSpace(string(b)), nil
		}
		return "", fmt.Errorf("no value found at path: %s", c.JSONPath)
	}
	return strings.TrimSpace(string(bodyBytes)), nil
}

func handleExecCommand(ctx context.Context, ev *event.Event, matrixClient *mautrix.Client, c *BotCommand) (string, error) {
	var inputPath string
	var tmpFiles []string
	defer func() {
		for _, f := range tmpFiles {
			_ = os.Remove(f)
		}
	}()

	if c.InputType == "image" {
		imgMsg, err := matrix.DownloadImageFromMessage(ctx, matrixClient, ev)
		if err != nil {
			return "reply to an image to use this command", nil
		}
		mediaURL, encFile, err := matrix.MediaFromMessage(imgMsg)
		if err != nil {
			return "", err
		}
		data, err := matrix.DownloadImageBytes(ctx, matrixClient, mediaURL, encFile)
		if err != nil {
			return "", err
		}

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

		ext := matrix.DetectImageExtension(tmpFile.Name())
		newName := strings.TrimSuffix(tmpFile.Name(), ".tmp") + ext
		if err := os.Rename(tmpFile.Name(), newName); err != nil {
			inputPath = tmpFile.Name()
		} else {
			inputPath = newName
			tmpFiles = append(tmpFiles, newName)
		}
	}

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

	cmd := exec.Command(c.Command, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("exec failed: %w, stderr: %s", err, stderr.String())
	}

	if c.OutputType == "image" {
		data, err := os.ReadFile(outputPath)
		if err != nil {
			return "", fmt.Errorf("read processed image: %w", err)
		}
		if err := matrix.SendImageToMatrix(ctx, matrixClient, ev.RoomID, ev.ID, data, defaultContentType, "processed.jpg"); err != nil {
			return "", err
		}
		return "", nil
	}
	return strings.TrimSpace(stdout.String()), nil
}

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
		targetText = util.TruncateText(text, 6000)
	} else {
		matrix.ParseEvent(ev)
		msg := ev.Content.AsMessage()
		if msg == nil {
			return "", fmt.Errorf("not a message event")
		}
		if msg.Body == "" {
			return "No message to respond to.", nil
		}

		var originalText string
		if msg.RelatesTo != nil && msg.RelatesTo.InReplyTo != nil {
			original, err := matrix.FetchAndDecrypt(ctx, matrixClient, ev.RoomID, msg.RelatesTo.InReplyTo.EventID)
			if err != nil {
				log.Warn().Err(err).Msg("failed to fetch replied-to message")
			} else if om := original.Content.AsMessage(); om != nil {
				originalEventID = original.ID
				originalText = om.Body
			}
		}

		if originalText != "" {
			suffix := util.StripCommandPrefix(msg.Body)
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
		targetText = util.TruncateText(targetText, 2000)
	}

	prompt := c.Prompt + "\n\n" + targetText
	response, err := callGroq(ctx, groqAPIKey, c.Model, c.MaxTokens, prompt)
	if err != nil {
		return "", err
	}

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

func handleBuiltinCommand(ctx context.Context, ev *event.Event, matrixClient *mautrix.Client, c *BotCommand, messagesDB *sql.DB, replyLabel string) (string, error) {
	if dbFn, ok := builtinDBFuncs[c.Command]; ok {
		matrix.ParseEvent(ev)
		msg := ev.Content.AsMessage()
		if msg == nil {
			return "", fmt.Errorf("not a message event")
		}
		var args string
		parts := strings.Fields(msg.Body)
		if len(parts) > 2 {
			args = strings.TrimSpace(strings.Join(parts[2:], " "))
		}
		return dbFn(ctx, messagesDB, matrixClient, ev, args, replyLabel, c.Mention)
	}

	matrix.ParseEvent(ev)
	msg := ev.Content.AsMessage()
	if msg == nil {
		return "", fmt.Errorf("not a message event")
	}

	var targetText string
	if msg.RelatesTo != nil && msg.RelatesTo.InReplyTo != nil {
		original, err := matrix.FetchAndDecrypt(ctx, matrixClient, ev.RoomID, msg.RelatesTo.InReplyTo.EventID)
		if err == nil {
			if om := original.Content.AsMessage(); om != nil {
				targetText = om.Body
			}
		}
	}

	if targetText == "" {
		parts := strings.Fields(msg.Body)
		if len(parts) > 2 {
			targetText = strings.TrimSpace(strings.Join(parts[2:], " "))
		}
	}

	if targetText == "" {
		return "uwu~ pwease give me some text to twansfowm!", nil
	}

	fn, ok := builtinFuncs[c.Command]
	if !ok {
		return "", fmt.Errorf("unknown builtin: %s", c.Command)
	}
	return fn(targetText), nil
}

// builtinFuncs maps builtin command names to their Go functions.
var builtinFuncs = map[string]func(string) string{
	"uwuify": Uwuify,
}

// builtinDBFuncs maps builtin command names that need DB access.
var builtinDBFuncs = map[string]func(context.Context, *sql.DB, *mautrix.Client, *event.Event, string, string, bool) (string, error){
	"yap": QueryTopYappers,
}

// ---------------------------------------------------------------------------
// AI helpers
// ---------------------------------------------------------------------------

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
