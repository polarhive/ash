package links

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/rs/zerolog/log"
)

var urlRe = regexp.MustCompile(`(?i)https?://[^\s>]+`)

// ExtractLinks returns all HTTP(S) URLs found in text.
func ExtractLinks(text string) []string {
	return urlRe.FindAllString(text, -1)
}

// SendHook posts a link to the configured webhook URL.
func SendHook(hookURL, link, key, sender, roomID, roomComment string, sendUser, sendTopic bool) {
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

// BlacklistEntry represents a regex pattern and comment from blacklist.json.
type BlacklistEntry struct {
	Pattern string `json:"pattern"`
	Comment string `json:"comment"`
}

// LoadBlacklist loads blacklist.json and compiles regex patterns.
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

// IsBlacklisted checks if a URL matches any blacklist regex.
func IsBlacklisted(url string, blacklist []*regexp.Regexp) bool {
	for _, re := range blacklist {
		if re.MatchString(url) {
			return true
		}
	}
	return false
}
