package links

import (
	"testing"
)

func TestExtractLinks(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"no links", "hello world", nil},
		{"single http", "check http://example.com out", []string{"http://example.com"}},
		{"single https", "visit https://example.com/page", []string{"https://example.com/page"}},
		{"multiple links", "see https://a.com and http://b.com/path",
			[]string{"https://a.com", "http://b.com/path"}},
		{"link with query", "go to https://example.com/search?q=test&page=1",
			[]string{"https://example.com/search?q=test&page=1"}},
		{"case insensitive", "HTTPS://EXAMPLE.COM", []string{"HTTPS://EXAMPLE.COM"}},
		{"empty string", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractLinks(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("ExtractLinks(%q) = %v, want %v", tt.input, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("ExtractLinks(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestIsBlacklisted(t *testing.T) {
	blacklist, err := LoadBlacklist("../blacklist.json")
	if err != nil {
		t.Skipf("skipping blacklist test (no blacklist.json): %v", err)
	}
	// Just verify it doesn't crash with a normal URL
	_ = IsBlacklisted("https://example.com", blacklist)
}
