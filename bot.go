package main

import (
	"bytes"
	"context"
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
)

// BotCommand describes a bot command that can return text or images
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
	Params       map[string]interface{} `json:"params,omitempty"`        // additional params
}

// BotConfig is the structure of bot.json
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
	dec := json.NewDecoder(f)
	if err := dec.Decode(&bc); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return &bc, nil
}

// FetchBotCommand executes the configured command and returns a string to post.
func FetchBotCommand(ctx context.Context, c *BotCommand, linkstashURL string, ev *event.Event, matrixClient *mautrix.Client, groqAPIKey string) (string, error) {
	if c.Response != "" {
		return c.Response, nil
	}
	// Execute based on command type
	switch c.Type {
	case "http":
		return handleHttpCommand(ctx, c, linkstashURL, ev, matrixClient)
	case "exec":
		return handleExecCommand(ctx, ev, matrixClient, c)
	case "ai":
		return handleAiCommand(ctx, ev, matrixClient, c, groqAPIKey)
	default:
		return "", fmt.Errorf("unknown command type: %s", c.Type)
	}
}

// handleHttpCommand handles HTTP-based commands
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
	// Default User-Agent
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "ash-bot (https://github.com/polarhive/ash)")
	}
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
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
	// If JSON path provided, try to parse
	if c.JSONPath != "" || strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "application/json") {
		var j interface{}
		if err := json.Unmarshal(bodyBytes, &j); err != nil {
			// If parsing fails but we expected json, return raw body as fallback
			return strings.TrimSpace(string(bodyBytes)), nil
		}
		v := extractJSONPath(j, c.JSONPath)
		if s, ok := v.(string); ok {
			if c.OutputType == "image" {
				// Download and upload image asynchronously
				go func(url string) {
					defer func() {
						if r := recover(); r != nil {
							log.Error().Interface("panic", r).Msg("panic in http image download")
						}
					}()
					imageResp, err := http.Get(url)
					if err != nil {
						log.Warn().Err(err).Str("url", url).Msg("failed to download image")
						return
					}
					defer imageResp.Body.Close()
					if imageResp.StatusCode != http.StatusOK {
						log.Warn().Int("status", imageResp.StatusCode).Str("url", url).Msg("image download failed")
						return
					}
					imageData, err := io.ReadAll(imageResp.Body)
					if err != nil {
						log.Warn().Err(err).Str("url", url).Msg("failed to read image data")
						return
					}
					contentType := imageResp.Header.Get("Content-Type")
					if contentType == "" {
						contentType = "image/jpeg"
					}
					uploadResp, err := matrixClient.UploadBytes(context.Background(), imageData, contentType)
					if err != nil {
						log.Warn().Err(err).Str("url", url).Msg("failed to upload image")
						return
					}
					imageContent := event.MessageEventContent{
						MsgType:   event.MsgImage,
						Body:      "image.jpg",
						URL:       uploadResp.ContentURI.CUString(),
						RelatesTo: &event.RelatesTo{InReplyTo: &event.InReplyTo{EventID: ev.ID}},
					}
					_, err = matrixClient.SendMessageEvent(context.Background(), ev.RoomID, event.EventMessage, &imageContent)
					if err != nil {
						log.Warn().Err(err).Msg("failed to send image")
					}
				}(s)
				return "", nil
			} else {
				return strings.TrimSpace(s), nil
			}
		}
		// Check if it's an array of posts (for summary)
		if arr, ok := v.([]interface{}); ok {
			return formatPosts(arr, linkstashURL), nil
		}
		// try to marshal the value to string
		if v != nil {
			b, _ := json.Marshal(v)
			return strings.TrimSpace(string(b)), nil
		}
		return "", fmt.Errorf("no value found at path: %s", c.JSONPath)
	}
	// Default: return body as text
	return strings.TrimSpace(string(bodyBytes)), nil
}

// handleExecCommand handles executable commands
func handleExecCommand(ctx context.Context, ev *event.Event, matrixClient *mautrix.Client, c *BotCommand) (string, error) {
	var inputPath string
	if c.InputType == "image" {
		// Copy image download logic from handleDeepfryCommand
		// Parse the message content
		if ev.Content.Raw != nil {
			if err := ev.Content.ParseRaw(ev.Type); err != nil {
				if !strings.Contains(err.Error(), "already parsed") {
					return "", fmt.Errorf("failed to parse event: %w", err)
				}
			}
		}

		msg := ev.Content.AsMessage()
		if msg == nil {
			return "", fmt.Errorf("not a message event")
		}

		var imageMsg *event.MessageEventContent

		if msg.MsgType == event.MsgImage || msg.MsgType == "m.sticker" || msg.URL != "" || msg.File != nil {
			imageMsg = msg
		} else if msg.RelatesTo != nil && msg.RelatesTo.InReplyTo != nil {
			// This is a reply, fetch the original message
			originalEvent, err := matrixClient.GetEvent(ctx, ev.RoomID, msg.RelatesTo.InReplyTo.EventID)
			if err != nil {
				return "", fmt.Errorf("failed to fetch replied-to message: %w", err)
			}

			// Parse the original event content
			if originalEvent.Content.Raw != nil {
				if err := originalEvent.Content.ParseRaw(originalEvent.Type); err != nil {
					return "", fmt.Errorf("failed to parse original event: %w", err)
				}
			}

			// Decrypt if encrypted
			if originalEvent.Type == event.EventEncrypted && matrixClient.Crypto != nil {
				log.Debug().Str("event_id", string(originalEvent.ID)).Msg("decrypting replied-to encrypted event")
				decryptedEvent, err := matrixClient.Crypto.Decrypt(ctx, originalEvent)
				if err != nil {
					return "", fmt.Errorf("failed to decrypt replied-to message: %w", err)
				}
				originalEvent = decryptedEvent
			}

			originalMsg := originalEvent.Content.AsMessage()
			if originalMsg != nil && (originalMsg.MsgType == event.MsgImage || originalMsg.MsgType == "m.sticker" || originalMsg.URL != "" || originalMsg.File != nil) {
				imageMsg = originalMsg
			}
		}

		if imageMsg == nil {
			return "", fmt.Errorf("no image found")
		}

		// Get media URL
		var mediaURL id.ContentURIString
		var encryptedFile *event.EncryptedFileInfo

		if imageMsg.File != nil {
			mediaURL = imageMsg.File.URL
			encryptedFile = imageMsg.File
		} else if imageMsg.URL != "" {
			mediaURL = imageMsg.URL
		} else {
			return "", fmt.Errorf("no media URL")
		}

		// Download image
		var data []byte
		var err error

		if mediaURL != "" {
			parsedURL, err := id.ParseContentURI(string(mediaURL))
			if err != nil {
				return "", fmt.Errorf("failed to parse media URL: %w", err)
			}
			data, err = matrixClient.DownloadBytes(ctx, parsedURL)
			if err != nil {
				return "", fmt.Errorf("failed to download image: %w", err)
			}

			if encryptedFile != nil {
				err = encryptedFile.PrepareForDecryption()
				if err != nil {
					return "", fmt.Errorf("failed to prepare for decryption: %w", err)
				}
				data, err = encryptedFile.Decrypt(data)
				if err != nil {
					return "", fmt.Errorf("failed to decrypt image: %w", err)
				}
			}
		}

		// Create temp input file
		tmpDir := "data/tmp"
		os.MkdirAll(tmpDir, 0755)
		inputFile, err := os.CreateTemp(tmpDir, "exec_input_*.tmp")
		if err != nil {
			return "", fmt.Errorf("failed to create temp input file: %w", err)
		}

		if _, err := inputFile.Write(data); err != nil {
			return "", fmt.Errorf("failed to write image data: %w", err)
		}
		inputFile.Close()

		// Detect file type and rename
		fileCmd := exec.Command("file", inputFile.Name())
		fileOutput, err := fileCmd.Output()
		if err == nil {
			typeStr := strings.ToLower(strings.TrimSpace(string(fileOutput)))
			var ext string
			if strings.Contains(typeStr, "jpeg") || strings.Contains(typeStr, "jpg") {
				ext = ".jpg"
			} else if strings.Contains(typeStr, "png") {
				ext = ".png"
			} else if strings.Contains(typeStr, "webp") {
				ext = ".webp"
			} else {
				ext = ".png"
			}
			newName := strings.TrimSuffix(inputFile.Name(), ".tmp") + ext
			os.Rename(inputFile.Name(), newName)
			inputPath = newName
		} else {
			inputPath = inputFile.Name()
		}
	}

	// Prepare args, replace placeholders
	args := make([]string, len(c.Args))
	var outputPath string
	for i, arg := range c.Args {
		if arg == "{input}" {
			args[i] = inputPath
		} else if arg == "{output}" {
			outputFile, err := os.CreateTemp("data/tmp", "exec_output_*")
			if err != nil {
				return "", fmt.Errorf("failed to create output file: %w", err)
			}
			outputPath = outputFile.Name()
			args[i] = outputPath
			outputFile.Close()
		} else {
			args[i] = arg
		}
	}

	// Run command
	cmd := exec.Command(c.Command, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("exec command failed: %w, stderr: %s", err, stderr.String())
	}

	// Handle output
	if c.OutputType == "image" {
		// Read and upload the output file
		processedData, err := os.ReadFile(outputPath)
		if err != nil {
			return "", fmt.Errorf("failed to read processed image: %w", err)
		}

		uploadResp, err := matrixClient.UploadBytes(ctx, processedData, "image/jpeg")
		if err != nil {
			return "", fmt.Errorf("failed to upload processed image: %w", err)
		}

		imageContent := event.MessageEventContent{
			MsgType:   event.MsgImage,
			Body:      "processed.jpg",
			URL:       uploadResp.ContentURI.CUString(),
			RelatesTo: &event.RelatesTo{InReplyTo: &event.InReplyTo{EventID: ev.ID}},
		}

		_, err = matrixClient.SendMessageEvent(ctx, ev.RoomID, event.EventMessage, &imageContent)
		if err != nil {
			return "", fmt.Errorf("failed to send processed image: %w", err)
		}

		return "Image processed!", nil
	} else {
		return strings.TrimSpace(stdout.String()), nil
	}
}

// handleAiCommand handles AI-based commands using Groq
func handleAiCommand(ctx context.Context, ev *event.Event, matrixClient *mautrix.Client, c *BotCommand, groqAPIKey string) (string, error) {
	var targetText string

	if strings.Contains(c.Prompt, "articles") {
		// Special for summary: fetch articles from linkstash
		url := "https://linkstash.hsp-ec.xyz/api/summary"
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return "", err
		}
		client := &http.Client{Timeout: 10 * time.Second}
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
			return "No articles to summarize.", nil
		}

		// Fetch content for each article
		var contents []string
		for _, article := range data.Summary {
			contentURL := fmt.Sprintf("https://linkstash.hsp-ec.xyz/api/content/%s", article.ID)
			req, err := http.NewRequestWithContext(ctx, "GET", contentURL, nil)
			if err != nil {
				log.Warn().Err(err).Str("id", article.ID).Msg("failed to create request for content")
				continue
			}
			resp, err := client.Do(req)
			if err != nil {
				log.Warn().Err(err).Str("id", article.ID).Msg("failed to fetch content")
				continue
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				log.Warn().Err(err).Str("id", article.ID).Msg("failed to read content")
				continue
			}
			if resp.StatusCode != http.StatusOK {
				log.Warn().Int("status", resp.StatusCode).Str("id", article.ID).Msg("bad status for content")
				continue
			}
			contents = append(contents, string(body))
		}

		if len(contents) == 0 {
			return "Failed to fetch any article contents.", nil
		}

		// Combine all contents
		targetText = strings.Join(contents, "\n\n---\n\n")

		// Estimate tokens and truncate if necessary
		estimatedTokens := len(targetText) / 4
		tokenLimit := 6000
		if estimatedTokens > tokenLimit {
			maxChars := tokenLimit * 4
			if len(targetText) > maxChars {
				targetText = targetText[:maxChars]
				if lastNewline := strings.LastIndex(targetText, "\n"); lastNewline > maxChars/2 {
					targetText = targetText[:lastNewline]
				}
			}
		}
	} else {
		// For gork: use message text
		if ev.Content.Raw != nil {
			if err := ev.Content.ParseRaw(ev.Type); err != nil {
				if !strings.Contains(err.Error(), "already parsed") {
					return "", fmt.Errorf("failed to parse event: %w", err)
				}
			}
		}

		msg := ev.Content.AsMessage()
		if msg == nil {
			return "", fmt.Errorf("not a message event")
		}

		messageText := msg.Body
		if messageText == "" {
			return "No message to respond to.", nil
		}

		// Remove the command prefix
		targetText = strings.TrimSpace(strings.TrimPrefix(messageText, "/bot "+strings.Split(ev.Content.AsMessage().Body, " ")[0]))

		// Estimate tokens and truncate if necessary
		estimatedTokens := len(targetText) / 4
		tokenLimit := 2000
		if estimatedTokens > tokenLimit {
			maxChars := tokenLimit * 4
			targetText = targetText[:maxChars]
			if lastSpace := strings.LastIndex(targetText, " "); lastSpace > maxChars/2 {
				targetText = targetText[:lastSpace]
			}
		}
	}

	// Prepare prompt
	prompt := c.Prompt + "\n\n" + targetText

	// Use Groq API
	if groqAPIKey == "" {
		return "", fmt.Errorf("GROQ_API_KEY not set")
	}

	config := openai.DefaultConfig(groqAPIKey)
	config.BaseURL = "https://api.groq.com/openai/v1"
	groqClient := openai.NewClientWithConfig(config)

	model := c.Model
	if model == "" {
		model = "openai/gpt-oss-120b"
	}

	maxTokens := c.MaxTokens
	if maxTokens == 0 {
		maxTokens = 300
	}

	groqResp, err := groqClient.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: model,
		Messages: []openai.ChatCompletionMessage{
			{Role: "user", Content: prompt},
		},
		MaxTokens: maxTokens,
	})
	if err != nil {
		return "", fmt.Errorf("groq api: %w", err)
	}

	if len(groqResp.Choices) == 0 {
		return "", fmt.Errorf("no response from groq")
	}

	return groqResp.Choices[0].Message.Content, nil
}

// Very small helper to extract keys separated by '.' from a parsed JSON value.
func extractJSONPath(root interface{}, path string) interface{} {
	if path == "" {
		return root
	}
	parts := strings.Split(path, ".")
	cur := root
	for _, p := range parts {
		switch v := cur.(type) {
		case map[string]interface{}:
			cur = v[p]
		default:
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
		p := posts[i]
		if m, ok := p.(map[string]interface{}); ok {
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

// handleDeepfryCommand processes a deepfry command by finding image attachments and applying deepfry effects
func handleDeepfryCommand(ctx context.Context, ev *event.Event, matrixClient *mautrix.Client, cmd *BotCommand) (string, error) {
	log.Debug().Str("event_id", string(ev.ID)).Str("event_type", ev.Type.String()).Msg("processing deepfry command")

	// Parse the message content
	if ev.Content.Raw != nil {
		if err := ev.Content.ParseRaw(ev.Type); err != nil {
			if !strings.Contains(err.Error(), "already parsed") {
				return "", fmt.Errorf("failed to parse event: %w", err)
			}
		}
	}

	msg := ev.Content.AsMessage()
	if msg == nil {
		return "", fmt.Errorf("not a message event")
	}

	log.Debug().Str("msg_type", string(msg.MsgType)).Interface("relates_to", msg.RelatesTo).Msg("message details")
	var imageMsg *event.MessageEventContent

	if msg.MsgType == event.MsgImage || msg.MsgType == "m.sticker" || msg.URL != "" || msg.File != nil {
		imageMsg = msg
		log.Debug().Interface("image_info", imageMsg.Info).Str("has_file", fmt.Sprintf("%v", imageMsg.File != nil)).Str("has_url", fmt.Sprintf("%v", imageMsg.URL != "")).Msg("image message content")
	} else if msg.RelatesTo != nil && msg.RelatesTo.InReplyTo != nil {
		// This is a reply, fetch the original message
		originalEvent, err := matrixClient.GetEvent(ctx, ev.RoomID, msg.RelatesTo.InReplyTo.EventID)
		if err != nil {
			return "", fmt.Errorf("failed to fetch replied-to message: %w", err)
		}

		// Parse the original event content
		if originalEvent.Content.Raw != nil {
			if err := originalEvent.Content.ParseRaw(originalEvent.Type); err != nil {
				return "", fmt.Errorf("failed to parse original event: %w", err)
			}
		}

		// Decrypt the event if it's encrypted
		if originalEvent.Type == event.EventEncrypted && matrixClient.Crypto != nil {
			log.Debug().Str("event_id", string(originalEvent.ID)).Msg("decrypting replied-to encrypted event")
			decryptedEvent, err := matrixClient.Crypto.Decrypt(ctx, originalEvent)
			if err != nil {
				return "", fmt.Errorf("failed to decrypt replied-to message: %w", err)
			}
			originalEvent = decryptedEvent
			log.Debug().Str("event_id", string(originalEvent.ID)).Msg("successfully decrypted replied-to event")
		} else {
			log.Debug().Str("event_id", string(originalEvent.ID)).Str("event_type", originalEvent.Type.String()).Msg("replied-to event (not encrypted or no crypto)")
		}

		originalMsg := originalEvent.Content.AsMessage()
		if originalMsg != nil && (originalMsg.MsgType == event.MsgImage || originalMsg.MsgType == "m.sticker" || originalMsg.URL != "" || originalMsg.File != nil) {
			imageMsg = originalMsg
			log.Debug().Interface("original_image_info", originalMsg.Info).Str("original_has_file", fmt.Sprintf("%v", originalMsg.File != nil)).Str("original_has_url", fmt.Sprintf("%v", originalMsg.URL != "")).Msg("original image message content")
		}
	}

	if imageMsg == nil {
		return "", fmt.Errorf("no image found - please send an image or reply to one with /bot deepfry")
	}

	// Get the media URL or handle encrypted file
	var mediaURL id.ContentURIString
	var encryptedFile *event.EncryptedFileInfo

	if imageMsg.File != nil {
		// Encrypted media - use the URL from the file info and decrypt later
		mediaURL = imageMsg.File.URL
		encryptedFile = imageMsg.File
		log.Debug().Str("encrypted_media_url", string(mediaURL)).Interface("encrypted_file", imageMsg.File).Msg("using encrypted media URL")
	} else if imageMsg.URL != "" {
		// Unencrypted media - use direct URL
		mediaURL = imageMsg.URL
		log.Debug().Str("media_url", string(mediaURL)).Msg("using direct media URL")
	} else {
		return "", fmt.Errorf("no media URL or encrypted file found")
	}

	// Download the image
	var data []byte
	var err error

	if mediaURL != "" {
		// Download media (may be encrypted or unencrypted)
		parsedURL, err := id.ParseContentURI(string(mediaURL))
		if err != nil {
			return "", fmt.Errorf("failed to parse media URL: %w", err)
		}
		data, err = matrixClient.DownloadBytes(ctx, parsedURL)
		if err != nil {
			return "", fmt.Errorf("failed to download image: %w", err)
		}

		// If this was from an encrypted file, decrypt it
		if encryptedFile != nil {
			log.Debug().Msg("decrypting downloaded encrypted media")
			err = encryptedFile.PrepareForDecryption()
			if err != nil {
				return "", fmt.Errorf("failed to prepare for decryption: %w", err)
			}
			data, err = encryptedFile.Decrypt(data)
			if err != nil {
				return "", fmt.Errorf("failed to decrypt image: %w", err)
			}
			log.Debug().Msg("successfully decrypted media")
		} else {
			log.Debug().Msg("downloaded unencrypted media")
		}
	} else {
		return "", fmt.Errorf("no valid media source found")
	}

	log.Debug().Int("size", len(data)).Msg("downloaded image data")

	// Ensure data/tmp directory exists
	tmpDir := "data/tmp"
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}

	// Create temporary files
	inputFile, err := os.CreateTemp(tmpDir, "deepfry_input_*.tmp")
	if err != nil {
		return "", fmt.Errorf("failed to create temp input file: %w", err)
	}

	outputFile, err := os.CreateTemp(tmpDir, "deepfry_output_*.jpg")
	if err != nil {
		return "", fmt.Errorf("failed to create temp output file: %w", err)
	}

	// Write downloaded data to input file
	if _, err := inputFile.Write(data); err != nil {
		return "", fmt.Errorf("failed to write image data: %w", err)
	}
	inputFile.Close()

	// Try to detect file type using 'file' command
	fileCmd := exec.Command("file", inputFile.Name())
	fileOutput, err := fileCmd.Output()
	inputPath := inputFile.Name()
	if err == nil {
		log.Debug().Str("file_type", strings.TrimSpace(string(fileOutput))).Msg("detected file type")

		// Determine file extension based on detected type
		typeStr := strings.ToLower(strings.TrimSpace(string(fileOutput)))
		var ext string
		if strings.Contains(typeStr, "jpeg") || strings.Contains(typeStr, "jpg") {
			ext = ".jpg"
		} else if strings.Contains(typeStr, "png") {
			ext = ".png"
		} else if strings.Contains(typeStr, "gif") {
			ext = ".gif"
		} else if strings.Contains(typeStr, "webp") || strings.Contains(typeStr, "web/p") {
			ext = ".webp"
		} else {
			ext = ".png" // default to png if unknown
		}
		newName := strings.TrimSuffix(inputFile.Name(), ".tmp") + ext
		if err := os.Rename(inputFile.Name(), newName); err != nil {
			log.Warn().Err(err).Msg("failed to rename input file")
		} else {
			log.Debug().Str("new_name", newName).Msg("renamed input file with extension")
			inputPath = newName
		}
	} else {
		log.Warn().Err(err).Msg("failed to detect file type")
	}

	// Log first 64 bytes as hex for debugging
	if len(data) > 0 {
		hexLen := 64
		if len(data) < hexLen {
			hexLen = len(data)
		}
		hexDump := fmt.Sprintf("%x", data[:hexLen])
		log.Debug().Str("hex_prefix", hexDump).Int("total_size", len(data)).Msg("file header bytes")
	}

	// Check if the file is actually an image by running identify
	var identifyCmd *exec.Cmd
	var identifyStderr bytes.Buffer

	if exec.Command("magick", "identify", inputPath).Run() == nil {
		identifyCmd = exec.Command("magick", "identify", inputPath)
	} else {
		identifyCmd = exec.Command("identify", inputPath)
	}
	identifyCmd.Stderr = &identifyStderr

	if err := identifyCmd.Run(); err != nil {
		log.Warn().Err(err).Str("stderr", identifyStderr.String()).Str("file", inputPath).Msg("identify failed, attempting conversion anyway")
		// Don't return error, try conversion anyway
	}

	// Apply deepfry effects using ImageMagick
	// Try different magick approaches for various image formats

	// Get ImageMagick args from params
	var imagemagickArgs []string
	if cmd.Params != nil {
		if args, ok := cmd.Params["imagemagick_args"].([]interface{}); ok {
			imagemagickArgs = make([]string, len(args))
			for i, a := range args {
				imagemagickArgs[i] = fmt.Sprintf("%v", a)
			}
		}
	}
	if len(imagemagickArgs) == 0 {
		imagemagickArgs = []string{"-modulate", "100,200,100", "-contrast-stretch", "0", "-statistic", "NonPeak", "3", "-sharpen", "0x5"}
	}

	// Apply deepfry effects using ImageMagick
	args := append([]string{inputPath}, imagemagickArgs...)
	args = append(args, outputFile.Name())
	var stderr bytes.Buffer
	execCmd := exec.Command("convert", args...)
	execCmd.Stderr = &stderr

	if err := execCmd.Run(); err != nil {
		return "", fmt.Errorf("failed to run ImageMagick: %w, stderr: %s", err, stderr.String())
	}

	// Read the processed image
	processedData, err := os.ReadFile(outputFile.Name())
	if err != nil {
		return "", fmt.Errorf("failed to read processed image: %w", err)
	}

	// Upload the processed image
	uploadResp, err := matrixClient.UploadBytes(ctx, processedData, "image/jpeg")
	if err != nil {
		return "", fmt.Errorf("failed to upload processed image: %w", err)
	}

	// Send the processed image as a reply
	imageContent := event.MessageEventContent{
		MsgType:   event.MsgImage,
		Body:      "deepfried.jpg",
		URL:       uploadResp.ContentURI.CUString(),
		RelatesTo: &event.RelatesTo{InReplyTo: &event.InReplyTo{EventID: ev.ID}},
	}

	_, err = matrixClient.SendMessageEvent(ctx, ev.RoomID, event.EventMessage, &imageContent)
	if err != nil {
		return "", fmt.Errorf("failed to send processed image: %w", err)
	}

	return "Image deepfried! 🔥", nil
}

// handleQuackCommand fetches a random duck image from random-d.uk API
func handleQuackCommand(ctx context.Context, ev *event.Event, matrixClient *mautrix.Client, cmd *BotCommand) (string, error) {
	// Process image asynchronously
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error().Interface("panic", r).Msg("panic in quack command goroutine")
			}
		}()

		var url string
		if cmd.Params != nil {
			if u, ok := cmd.Params["url"].(string); ok {
				url = u
			}
		}
		if url == "" {
			log.Warn().Msg("no URL configured for quack command")
			return
		}
		// Fetch random duck image URL from API
		req, err := http.NewRequestWithContext(context.Background(), "GET", url, nil)
		if err != nil {
			log.Warn().Err(err).Msg("failed to create duck API request")
			return
		}
		req.Header.Set("User-Agent", "ash-bot (https://github.com/polarhive/ash)")

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			log.Warn().Err(err).Msg("failed to fetch duck API")
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			log.Warn().Int("status", resp.StatusCode).Msg("duck API returned bad status")
			return
		}

		// Parse JSON response
		var apiResp struct {
			Message string `json:"message"`
			URL     string `json:"url"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
			log.Warn().Err(err).Msg("failed to parse duck API response")
			return
		}

		if apiResp.URL == "" {
			log.Warn().Msg("no image URL in duck API response")
			return
		}

		log.Info().Str("image_url", apiResp.URL).Msg("fetched duck image URL")

		// Download the image
		imageResp, err := http.Get(apiResp.URL)
		if err != nil {
			log.Warn().Err(err).Msg("failed to download duck image")
			return
		}
		defer imageResp.Body.Close()

		if imageResp.StatusCode != http.StatusOK {
			log.Warn().Int("status", imageResp.StatusCode).Msg("duck image download failed")
			return
		}

		imageData, err := io.ReadAll(imageResp.Body)
		if err != nil {
			log.Warn().Err(err).Msg("failed to read duck image data")
			return
		}

		log.Info().Int("size", len(imageData)).Msg("downloaded duck image")

		// Determine content type
		contentType := imageResp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "image/jpeg" // default fallback
		}

		// Upload the image to Matrix
		uploadResp, err := matrixClient.UploadBytes(context.Background(), imageData, contentType)
		if err != nil {
			log.Warn().Err(err).Msg("failed to upload duck image")
			return
		}

		// Send the image as a reply
		imageContent := event.MessageEventContent{
			MsgType:   event.MsgImage,
			Body:      "quack.jpg",
			URL:       uploadResp.ContentURI.CUString(),
			RelatesTo: &event.RelatesTo{InReplyTo: &event.InReplyTo{EventID: ev.ID}},
		}

		_, err = matrixClient.SendMessageEvent(context.Background(), ev.RoomID, event.EventMessage, &imageContent)
		if err != nil {
			log.Warn().Err(err).Msg("failed to send duck image")
			return
		}
	}()

	return "", nil
}

// handleMeowCommand fetches a random cat image from The Cat API
func handleMeowCommand(ctx context.Context, ev *event.Event, matrixClient *mautrix.Client, cmd *BotCommand) (string, error) {
	// Process image asynchronously
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error().Interface("panic", r).Msg("panic in meow command goroutine")
			}
		}()

		var url string
		if cmd.Params != nil {
			if u, ok := cmd.Params["url"].(string); ok {
				url = u
			}
		}
		if url == "" {
			log.Warn().Msg("no URL configured for meow command")
			return
		}
		// Fetch random cat image from The Cat API
		req, err := http.NewRequestWithContext(context.Background(), "GET", url, nil)
		if err != nil {
			log.Warn().Err(err).Msg("failed to create cat API request")
			return
		}
		req.Header.Set("User-Agent", "ash-bot (https://github.com/polarhive/ash)")

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			log.Warn().Err(err).Msg("failed to fetch cat API")
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			log.Warn().Int("status", resp.StatusCode).Msg("cat API returned bad status")
			return
		}

		// Parse JSON response - API returns an array of cat objects
		var apiResp []struct {
			ID     string `json:"id"`
			URL    string `json:"url"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
			log.Warn().Err(err).Msg("failed to parse cat API response")
			return
		}

		if len(apiResp) == 0 || apiResp[0].URL == "" {
			log.Warn().Msg("no image URL in cat API response")
			return
		}

		imageURL := apiResp[0].URL
		log.Info().Str("image_url", imageURL).Str("cat_id", apiResp[0].ID).Msg("fetched cat image")

		// Download the image
		imageResp, err := http.Get(imageURL)
		if err != nil {
			log.Warn().Err(err).Msg("failed to download cat image")
			return
		}
		defer imageResp.Body.Close()

		if imageResp.StatusCode != http.StatusOK {
			log.Warn().Int("status", imageResp.StatusCode).Msg("cat image download failed")
			return
		}

		imageData, err := io.ReadAll(imageResp.Body)
		if err != nil {
			log.Warn().Err(err).Msg("failed to read cat image data")
			return
		}

		log.Info().Int("size", len(imageData)).Msg("downloaded cat image")

		// Determine content type
		contentType := imageResp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "image/jpeg" // default fallback
		}

		// Upload the image to Matrix
		uploadResp, err := matrixClient.UploadBytes(context.Background(), imageData, contentType)
		if err != nil {
			log.Warn().Err(err).Msg("failed to upload cat image")
			return
		}

		// Send the image as a reply
		imageContent := event.MessageEventContent{
			MsgType:   event.MsgImage,
			Body:      "meow.jpg",
			URL:       uploadResp.ContentURI.CUString(),
			RelatesTo: &event.RelatesTo{InReplyTo: &event.InReplyTo{EventID: ev.ID}},
		}

		_, err = matrixClient.SendMessageEvent(context.Background(), ev.RoomID, event.EventMessage, &imageContent)
		if err != nil {
			log.Warn().Err(err).Msg("failed to send cat image")
			return
		}
	}()

	return "", nil
}

// handleJokeCommand fetches a random joke from configured API
func handleJokeCommand(ctx context.Context, cmd *BotCommand) (string, error) {
	var url string
	headers := map[string]string{"Accept": "application/json"}
	if cmd.Params != nil {
		if u, ok := cmd.Params["url"].(string); ok {
			url = u
		}
		if h, ok := cmd.Params["headers"].(map[string]interface{}); ok {
			for k, v := range h {
				if vs, ok := v.(string); ok {
					headers[k] = vs
				}
			}
		}
	}
	if url == "" {
		return "", fmt.Errorf("no URL configured for joke command")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("User-Agent", "ash-bot (https://github.com/polarhive/ash)")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}
	var payload struct {
		Joke string `json:"joke"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	return payload.Joke, nil
}

// handleSummaryCommand summarizes the articles from linkstash using Groq
func handleSummaryCommand(ctx context.Context, ev *event.Event, cmd *BotCommand, groqAPIKey string) (string, error) {
	// First, fetch the summary JSON
	url := "https://linkstash.hsp-ec.xyz/api/summary"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "ash-bot (https://github.com/polarhive/ash)")
	client := &http.Client{Timeout: 10 * time.Second}
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
		return "No articles to summarize.", nil
	}

	// Fetch content for each article
	var contents []string
	for _, article := range data.Summary {
		contentURL := fmt.Sprintf("https://linkstash.hsp-ec.xyz/api/content/%s", article.ID)
		req, err := http.NewRequestWithContext(ctx, "GET", contentURL, nil)
		if err != nil {
			log.Warn().Err(err).Str("id", article.ID).Msg("failed to create request for content")
			continue
		}
		req.Header.Set("User-Agent", "ash-bot (https://github.com/polarhive/ash)")
		resp, err := client.Do(req)
		if err != nil {
			log.Warn().Err(err).Str("id", article.ID).Msg("failed to fetch content")
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			log.Warn().Err(err).Str("id", article.ID).Msg("failed to read content")
			continue
		}
		if resp.StatusCode != http.StatusOK {
			log.Warn().Int("status", resp.StatusCode).Str("id", article.ID).Msg("bad status for content")
			continue
		}
		contents = append(contents, string(body))
	}

	if len(contents) == 0 {
		return "Failed to fetch any article contents.", nil
	}

	// Combine all contents
	combined := strings.Join(contents, "\n\n---\n\n")

	// Estimate tokens (rough approximation: 1 token ≈ 4 characters)
	estimatedTokens := len(combined) / 4
	tokenLimit := 6000 // Leave some buffer below the 8000 limit

	if estimatedTokens > tokenLimit {
		// Truncate content to fit within token limit
		maxChars := tokenLimit * 4
		if len(combined) > maxChars {
			combined = combined[:maxChars]
			// Try to cut at a reasonable boundary
			if lastNewline := strings.LastIndex(combined, "\n"); lastNewline > maxChars/2 {
				combined = combined[:lastNewline]
			}
		}
		log.Warn().Int("original_tokens", estimatedTokens).Int("truncated_tokens", len(combined)/4).Msg("truncated content to fit token limit")
	}

	// Prepare prompt
	prompt := cmd.Prompt
	if prompt == "" {
		prompt = "Provide a short 2-line summary of what these articles are about, focusing on the main topics and key insights:" // fallback
	}
	prompt += "\n\n" + combined

	// Use Groq API
	if groqAPIKey == "" {
		return "", fmt.Errorf("GROQ_API_KEY not set")
	}

	config := openai.DefaultConfig(groqAPIKey)
	config.BaseURL = "https://api.groq.com/openai/v1"
	groqClient := openai.NewClientWithConfig(config)

	model := cmd.Model
	if model == "" {
		model = "openai/gpt-oss-120b" // default fallback
	}

	maxTokens := cmd.MaxTokens
	if maxTokens == 0 {
		maxTokens = 600 // default fallback
	}

	groqResp, err := groqClient.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: model,
		Messages: []openai.ChatCompletionMessage{
			{Role: "user", Content: prompt},
		},
		MaxTokens: maxTokens,
	})
	if err != nil {
		return "", fmt.Errorf("groq api: %w", err)
	}

	if len(groqResp.Choices) == 0 {
		return "", fmt.Errorf("no response from groq")
	}

	response := groqResp.Choices[0].Message.Content
	return response + "\n\nRead more: https://linkstash.hsp-ec.xyz/", nil
}

// handleGorkCommand responds to a message using Groq
func handleGorkCommand(ctx context.Context, ev *event.Event, matrixClient *mautrix.Client, cmd *BotCommand, groqAPIKey string) (string, error) {
	// Get the message content
	if ev.Content.Raw != nil {
		if err := ev.Content.ParseRaw(ev.Type); err != nil {
			if !strings.Contains(err.Error(), "already parsed") {
				return "", fmt.Errorf("failed to parse event: %w", err)
			}
		}
	}

	msg := ev.Content.AsMessage()
	if msg == nil {
		return "", fmt.Errorf("not a message event")
	}

	messageText := msg.Body
	if messageText == "" {
		return "No message to respond to.", nil
	}

	// Check if this is a reply and get the original message
	var originalMessageText string
	if msg.RelatesTo != nil && msg.RelatesTo.InReplyTo != nil {
		// This is a reply, fetch the original message
		originalEvent, err := matrixClient.GetEvent(ctx, ev.RoomID, msg.RelatesTo.InReplyTo.EventID)
		if err != nil {
			log.Warn().Err(err).Str("event_id", string(msg.RelatesTo.InReplyTo.EventID)).Msg("failed to fetch replied-to message")
			// Continue without original message
		} else {
			// Parse the original event content
			if originalEvent.Content.Raw != nil {
				if err := originalEvent.Content.ParseRaw(originalEvent.Type); err != nil {
					log.Warn().Err(err).Msg("failed to parse original event")
				} else {
					// Decrypt the event if it's encrypted
					if originalEvent.Type == event.EventEncrypted && matrixClient.Crypto != nil {
						log.Debug().Str("event_id", string(originalEvent.ID)).Msg("decrypting replied-to encrypted event")
						decryptedEvent, err := matrixClient.Crypto.Decrypt(ctx, originalEvent)
						if err != nil {
							log.Warn().Err(err).Msg("failed to decrypt replied-to message")
						} else {
							originalEvent = decryptedEvent
							log.Debug().Str("event_id", string(originalEvent.ID)).Msg("successfully decrypted replied-to event")
						}
					}

					originalMsg := originalEvent.Content.AsMessage()
					if originalMsg != nil {
						originalMessageText = originalMsg.Body
					}
				}
			}
		}
	}

	// Remove the command prefix if present
	targetText := strings.TrimSpace(strings.TrimPrefix(messageText, "/bot gork"))

	if targetText == "" && originalMessageText == "" {
		return "Please provide a message to respond to.", nil
	}

	// Estimate tokens and truncate if necessary
	estimatedTokens := len(targetText) / 4
	tokenLimit := 2000 // Conservative limit for gork responses

	if estimatedTokens > tokenLimit {
		maxChars := tokenLimit * 4
		targetText = targetText[:maxChars]
		// Try to cut at word boundary
		if lastSpace := strings.LastIndex(targetText, " "); lastSpace > maxChars/2 {
			targetText = targetText[:lastSpace]
		}
		log.Warn().Int("original_tokens", estimatedTokens).Int("truncated_tokens", len(targetText)/4).Msg("truncated message to fit token limit")
	}

	// Prepare prompt
	basePrompt := cmd.Prompt
	if basePrompt == "" {
		basePrompt = "Respond as an AI assistant in plain text without any markdown formatting. Be helpful, truthful, and engaging." // fallback
	}

	var prompt string
	if originalMessageText != "" {
		// This is a reply to another message
		prompt = fmt.Sprintf("%s\n\nThe user is asking about this message: \"%s\"\n\nTheir question/comment: %s", basePrompt, originalMessageText, targetText)
	} else {
		// Direct message
		prompt = fmt.Sprintf("%s\n\n%s", basePrompt, targetText)
	}

	// Use Groq API
	if groqAPIKey == "" {
		return "", fmt.Errorf("GROQ_API_KEY not set")
	}

	config := openai.DefaultConfig(groqAPIKey)
	config.BaseURL = "https://api.groq.com/openai/v1"
	groqClient := openai.NewClientWithConfig(config)

	model := cmd.Model
	if model == "" {
		model = "openai/gpt-oss-120b" // default fallback
	}

	maxTokens := cmd.MaxTokens
	if maxTokens == 0 {
		maxTokens = 300 // default fallback
	}

	groqResp, err := groqClient.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: model,
		Messages: []openai.ChatCompletionMessage{
			{Role: "user", Content: prompt},
		},
		MaxTokens: maxTokens,
	})
	if err != nil {
		return "", fmt.Errorf("groq api: %w", err)
	}

	if len(groqResp.Choices) == 0 {
		return "", fmt.Errorf("no response from groq")
	}

	return groqResp.Choices[0].Message.Content, nil
}
