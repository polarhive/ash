package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// BotCommand describes a simple HTTP-backed bot command
type BotCommand struct {
	Method       string            `json:"method,omitempty"`
	URL          string            `json:"url"`
	Headers      map[string]string `json:"headers,omitempty"`
	JSONPath     string            `json:"json_path,omitempty"`
	ResponseType string            `json:"response_type,omitempty"` // "text" or "json" (optional)
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
func FetchBotCommand(ctx context.Context, c *BotCommand, linkstashURL string) (string, error) {
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
			return strings.TrimSpace(s), nil
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
