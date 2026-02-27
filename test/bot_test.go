package test

import (
	"encoding/json"
	"os"
	"testing"
)

// BotCommand represents a bot command configuration
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

func TestBotConfigValidation(t *testing.T) {
	// Test loading and parsing bot.json directly
	data, err := os.ReadFile("../bot.json")
	if err != nil {
		t.Fatalf("Failed to read bot.json: %v", err)
	}

	var config BotConfig
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("Failed to parse bot.json: %v", err)
	}

	if config.Commands == nil {
		t.Fatal("Commands map is nil")
	}

	// Test each command
	for name, cmd := range config.Commands {
		t.Run("command_"+name, func(t *testing.T) {
			validateCommand(t, name, cmd)
		})
	}
}

func validateCommand(t *testing.T, name string, cmd BotCommand) {
	// Static responses don't need a type
	if cmd.Response != "" {
		return
	}

	// Check that type is specified and valid
	validTypes := map[string]bool{
		"http":    true,
		"exec":    true,
		"ai":      true,
		"builtin": true,
	}

	if cmd.Type == "" {
		t.Errorf("Command %s: type is required", name)
		return
	}

	if !validTypes[cmd.Type] {
		t.Errorf("Command %s: invalid type '%s', must be one of: http, exec, ai, builtin", name, cmd.Type)
		return
	}

	// Validate based on type
	switch cmd.Type {
	case "http":
		validateHttpCommand(t, name, cmd)
	case "exec":
		validateExecCommand(t, name, cmd)
	case "ai":
		validateAiCommand(t, name, cmd)
	case "builtin":
		validateBuiltinCommand(t, name, cmd)
	}

	// Validate input/output types if specified
	if cmd.InputType != "" {
		validIOTypes := map[string]bool{
			"none":  true,
			"text":  true,
			"image": true,
		}
		if !validIOTypes[cmd.InputType] {
			t.Errorf("Command %s: invalid input_type '%s', must be one of: none, text, image", name, cmd.InputType)
		}
	}

	if cmd.OutputType != "" {
		validIOTypes := map[string]bool{
			"text":  true,
			"image": true,
		}
		if !validIOTypes[cmd.OutputType] {
			t.Errorf("Command %s: invalid output_type '%s', must be one of: text, image", name, cmd.OutputType)
		}
	}
}

func validateHttpCommand(t *testing.T, name string, cmd BotCommand) {
	if cmd.URL == "" {
		t.Errorf("Command %s: http type requires url", name)
	}

	if cmd.Method == "" {
		cmd.Method = "GET" // Default method
	}

	validMethods := map[string]bool{
		"GET":    true,
		"POST":   true,
		"PUT":    true,
		"DELETE": true,
		"PATCH":  true,
	}

	if !validMethods[cmd.Method] {
		t.Errorf("Command %s: invalid method '%s'", name, cmd.Method)
	}

	// If output_type is image, json_path should point to a URL
	if cmd.OutputType == "image" && cmd.JSONPath == "" {
		t.Errorf("Command %s: image output_type requires json_path to specify image URL field", name)
	}
}

func validateExecCommand(t *testing.T, name string, cmd BotCommand) {
	if cmd.Command == "" {
		t.Errorf("Command %s: exec type requires command", name)
	}

	if len(cmd.Args) == 0 {
		t.Errorf("Command %s: exec type requires args array", name)
	}

	// Check for placeholder usage
	hasInput := false
	hasOutput := false
	for _, arg := range cmd.Args {
		if arg == "{input}" {
			hasInput = true
		}
		if arg == "{output}" {
			hasOutput = true
		}
	}

	if cmd.InputType == "image" && !hasInput {
		t.Errorf("Command %s: input_type 'image' requires {input} placeholder in args", name)
	}

	if cmd.OutputType == "image" && !hasOutput {
		t.Errorf("Command %s: output_type 'image' requires {output} placeholder in args", name)
	}
}

func validateAiCommand(t *testing.T, name string, cmd BotCommand) {
	if cmd.Prompt == "" {
		t.Errorf("Command %s: ai type requires prompt", name)
	}

	if cmd.Model == "" {
		t.Errorf("Command %s: ai type requires model", name)
	}

	if cmd.MaxTokens <= 0 {
		t.Errorf("Command %s: ai type requires max_tokens > 0", name)
	}

	if cmd.InputType == "" {
		cmd.InputType = "text" // Default for AI
	}

	if cmd.OutputType == "" {
		cmd.OutputType = "text" // Default for AI
	}
}

func validateBuiltinCommand(t *testing.T, name string, cmd BotCommand) {
	if cmd.Command == "" {
		t.Errorf("Command %s: builtin type requires command", name)
	}
}

func TestBotConfigJSONStructure(t *testing.T) {
	// Test that bot.json is valid JSON
	data, err := os.ReadFile("../bot.json")
	if err != nil {
		t.Fatalf("Failed to read bot.json: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("bot.json is not valid JSON: %v", err)
	}

	// Check that it has the expected top-level structure
	if _, ok := raw["commands"]; !ok {
		t.Error("bot.json missing 'commands' key")
	}

	if commands, ok := raw["commands"].(map[string]interface{}); ok {
		if len(commands) == 0 {
			t.Error("bot.json has empty commands map")
		}
	} else {
		t.Error("bot.json 'commands' is not an object")
	}
}

func TestBotConfigRequiredCommands(t *testing.T) {
	data, err := os.ReadFile("../bot.json")
	if err != nil {
		t.Fatalf("Failed to read bot.json: %v", err)
	}

	var config BotConfig
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("Failed to parse bot.json: %v", err)
	}

	requiredCommands := []string{"hi", "summary", "gork"}
	for _, cmdName := range requiredCommands {
		if _, exists := config.Commands[cmdName]; !exists {
			t.Errorf("Required command '%s' not found in bot.json", cmdName)
		}
	}
}
