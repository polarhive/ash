# ash

> a minimal [matrix] message watcher and link extractor

# ash

> a minimal [matrix] message watcher and link extractor

## Bot commands (bot.json)

You can configure bot commands in `bot.json`. Commands are fully composable and support three types:

### Command Types

- **`exec`**: Runs arbitrary executables with arguments. Supports `{input}` and `{output}` placeholders for file processing (e.g., image manipulation).
- **`http`**: Makes HTTP requests and returns responses (text or images).
- **`ai`**: Uses Groq AI with custom prompts for intelligent responses.

### Example Commands

```json
{
  "deepfry": {
    "type": "exec",
    "command": "magick",
    "args": ["{input}", "-modulate", "200,200", "-sharpen", "0x3", "{output}"],
    "input_type": "image",
    "output_type": "image"
  },
  "quack": {
    "type": "http",
    "url": "https://random-d.uk/api/v2/random",
    "method": "GET",
    "headers": {"User-Agent": "ash-bot"},
    "output_type": "image"
  },
  "summary": {
    "type": "ai",
    "prompt": "Summarize these articles concisely:",
    "model": "llama3-8b-8192",
    "max_tokens": 500
  }
}
```

### Available Commands

- `/bot deepfry` — Deepfries attached images using ImageMagick
- `/bot quack` — Returns a random duck image
- `/bot meow` — Returns a random cat image
- `/bot summary` — Fetches recent articles from linkstash and summarizes them using Groq AI
- `/bot gork <message>` — Responds to queries using Groq AI (alias: `@gork <message>`)

Add or change commands in `bot.json` and set `BOT_CONFIG_PATH` in `config.json` if you place it elsewhere. The bot will prefix responses using `BOT_REPLY_LABEL` in `config.json` (defaults to `[BOT]\n`).

### Room-specific bot configuration

Bot commands are enabled per room via the `allowedCommands` array in `config.json`:

- `"allowedCommands": []` — Enable bot with all commands allowed
- `"allowedCommands": ["summary", "joke"]` — Enable bot with only specific commands
- Omit `allowedCommands` — Bot disabled in that room

The `hi` command is always allowed in all rooms.

This allows fine-grained control over which commands are available in each room.

pairs nicely with [lava](https://polarhive.net/lava)

> lava is a web clipping tool that can run as a server or daemon to automatically populate your Obsidian clippings directory with fresh parsed md from URLs.

## Setup

1. Install Go 1.25+ and SQLite.
2. Clone the repo.
3. Copy `config.json` and edit with your Matrix credentials.
4. Run `make` to build and run.

## Structure

- `ash.go`: Main application logic
- `bot.go`: Bot command handling
- `db/`: Database schema files
- `data/`: Runtime data (SQLite, exports)
- `config.json`: Configuration file
- `bot.json`: Bot commands configuration

## Configuration

Edit `config.json`:

- `MATRIX_HOMESERVER`: Your Matrix server URL
- `MATRIX_USER`: Your Matrix user ID
- `MATRIX_PASSWORD`: Password
- `MATRIX_RECOVERY_KEY`: For E2EE verification
- `MATRIX_ROOM_ID`: Array of rooms to watch, each with:
  - `id`: Room ID
  - `comment`: Human-readable name
  - `hook`: Optional webhook URL for link processing
  - `key`: Webhook auth key
  - `sendUser`/`sendTopic`: Whether to include user/topic in webhooks
  - `allowedCommands`: Array of allowed bot commands (empty = all, omit = disabled)
- `BOT_REPLY_LABEL`: Bot response prefix (default: `[BOT]\n`)
- `LINKSTASH_URL`: Base URL for linkstash service (used in summary bot)
- `GROQ_API_KEY`: API key for Groq AI (required for summary and gork commands)
- `MATRIX_DEVICE_NAME`: Device name
- `DEBUG`: Enable debug logging

## Usage

- `make`: Build and run
- `make build`: Build only
- `make test`: Run tests (validates bot.json configuration)
- `make clean`: Clean build artifacts

Links are exported to `data/links.json`.