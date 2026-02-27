package util

import (
	"fmt"
	"strings"
)

// InSlice checks whether item is present in slice.
func InSlice(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// Truncate shortens a string to maxLen and appends "..." if truncated.
func Truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// TruncateText truncates text to roughly fit within a token budget.
func TruncateText(text string, tokenLimit int) string {
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

// StripCommandPrefix removes common bot command prefixes from a message body.
func StripCommandPrefix(body string) string {
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

// ExtractJSONPath extracts a value from parsed JSON using a dot-separated path.
func ExtractJSONPath(root interface{}, path string) interface{} {
	if path == "" {
		return root
	}
	cur := root
	for _, p := range strings.Split(path, ".") {
		if m, ok := cur.(map[string]interface{}); ok {
			cur = m[p]
		} else if arr, ok := cur.([]interface{}); ok {
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

// FormatPosts formats an array of post objects into a readable string.
func FormatPosts(posts []interface{}, linkstashURL string) string {
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
