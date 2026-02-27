package util

import (
	"fmt"
	"strings"
	"testing"
)

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
			got := ExtractJSONPath(root, tt.path)
			if tt.want == nil && got != nil {
				t.Errorf("ExtractJSONPath(_, %q) = %v, want nil", tt.path, got)
			} else if tt.want != nil {
				if s, ok := tt.want.(string); ok {
					if gs, ok := got.(string); !ok || gs != s {
						t.Errorf("ExtractJSONPath(_, %q) = %v, want %q", tt.path, got, s)
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
	result := FormatPosts(posts, "https://linkstash.example.com")
	if result == "" {
		t.Error("FormatPosts returned empty string")
	}
	if !strings.Contains(result, "Post 1") || !strings.Contains(result, "Post 2") {
		t.Errorf("FormatPosts missing post titles: %s", result)
	}
	if !strings.Contains(result, "https://linkstash.example.com") {
		t.Errorf("FormatPosts missing linkstash URL: %s", result)
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
	result := FormatPosts(posts, "https://linkstash.example.com")
	// Count lines with "- " prefix (capped at 5)
	lines := 0
	for _, line := range strings.Split(result, "\n") {
		if len(line) > 0 && line[0] == '-' {
			lines++
		}
	}
	if lines != 5 {
		t.Errorf("FormatPosts should cap at 5 posts, got %d", lines)
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
			got := TruncateText(tt.text, tt.tokenLimit)
			if len(got) > tt.wantMax+10 { // small buffer for boundary rounding
				t.Errorf("TruncateText len = %d, want <= %d", len(got), tt.wantMax)
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
			got := StripCommandPrefix(tt.input)
			if got != tt.want {
				t.Errorf("StripCommandPrefix(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestInSlice(t *testing.T) {
	slice := []string{"a", "b", "c"}
	if !InSlice(slice, "b") {
		t.Error("InSlice should find 'b'")
	}
	if InSlice(slice, "d") {
		t.Error("InSlice should not find 'd'")
	}
	if InSlice(nil, "a") {
		t.Error("InSlice should return false for nil slice")
	}
}

func TestTruncate(t *testing.T) {
	if Truncate("hello", 10) != "hello" {
		t.Error("Truncate should not truncate short string")
	}
	got := Truncate("hello world", 5)
	if got != "hello..." {
		t.Errorf("Truncate = %q, want %q", got, "hello...")
	}
}

// Silence unused import warning in case fmt is needed for future tests.
var _ = fmt.Sprintf
