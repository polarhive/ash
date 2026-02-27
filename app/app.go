package app

import (
	"context"
	"database/sql"
	"fmt"
	grand "math/rand"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/polarhive/ash/bot"
	"github.com/polarhive/ash/config"
	"github.com/polarhive/ash/db"
	"github.com/polarhive/ash/links"
	"github.com/polarhive/ash/util"
)

// App holds the runtime dependencies for handling Matrix events.
type App struct {
	Cfg        *config.Config
	MessagesDB *sql.DB
	BotCfg     *bot.BotConfig
	Client     *mautrix.Client
	ReadyChan  <-chan bool
	KnockKnock *bot.KnockKnockState
}

// ResolveReplyLabel returns the reply label with precedence:
// config.BOT_REPLY_LABEL -> bot.json label -> default "> ".
func ResolveReplyLabel(cfg *config.Config, botCfg *bot.BotConfig) string {
	if cfg != nil && cfg.BotReplyLabel != "" {
		return cfg.BotReplyLabel
	}
	if botCfg != nil && botCfg.Label != "" {
		return botCfg.Label
	}
	return "> "
}

// SendBotReply sends a text reply to the given event.
func SendBotReply(ctx context.Context, client *mautrix.Client, roomID id.RoomID, eventID id.EventID, body, cmd string) {
	content := event.MessageEventContent{
		MsgType:   event.MsgText,
		Body:      body,
		RelatesTo: &event.RelatesTo{InReplyTo: &event.InReplyTo{EventID: eventID}},
	}
	if _, err := client.SendMessageEvent(ctx, roomID, event.EventMessage, &content); err != nil {
		log.Error().Err(err).Str("cmd", cmd).Msg("failed to send response")
	} else {
		log.Info().Str("cmd", cmd).Msg("sent bot response")
	}
}

// GenerateHelpMessage creates a help message listing available commands.
func GenerateHelpMessage(botCfg *bot.BotConfig, allowedCommands []string) string {
	var cmds []string
	if len(allowedCommands) > 0 {
		cmds = make([]string, len(allowedCommands))
		copy(cmds, allowedCommands)
	} else {
		for cmd := range botCfg.Commands {
			cmds = append(cmds, cmd)
		}
	}
	sort.Strings(cmds)
	return "Available commands: " + strings.Join(cmds, ", ")
}

// HandleMessage processes an incoming Matrix message event.
func (app *App) HandleMessage(evCtx context.Context, ev *event.Event) {
	currentRoom, ok := app.findRoom(ev.RoomID)
	if len(app.Cfg.RoomIDs) > 0 && !ok {
		return
	}

	msgData, err := db.ProcessMessageEvent(ev)
	if err != nil {
		log.Warn().Err(err).Str("event_id", string(ev.ID)).Msg("failed to parse event")
		return
	}
	if msgData == nil {
		return
	}
	if err := db.StoreMessage(app.MessagesDB, msgData); err != nil {
		log.Error().Err(err).Str("event_id", string(ev.ID)).Msg("store event")
		return
	}
	log.Info().Str("room", currentRoom.Comment).Str("sender", string(ev.Sender)).Msg(util.Truncate(msgData.Msg.Body, 100))

	// Skip messages that contain the bot's own reply label.
	if app.Cfg.BotReplyLabel != "" && strings.Contains(msgData.Msg.Body, app.Cfg.BotReplyLabel) {
		log.Debug().Str("label", app.Cfg.BotReplyLabel).Msg("skipped bot processing due to bot reply label")
		return
	}

	// Check for knock-knock joke reply continuations.
	if app.KnockKnock != nil && msgData.Msg.RelatesTo != nil && msgData.Msg.RelatesTo.InReplyTo != nil {
		if step, ok := app.KnockKnock.Get(msgData.Msg.RelatesTo.InReplyTo.EventID); ok {
			go app.handleKnockKnockReply(evCtx, ev, step, msgData.Msg.RelatesTo.InReplyTo.EventID)
			return
		}
	}

	// Handle bot commands.
	if currentRoom.AllowedCommands != nil && (strings.HasPrefix(msgData.Msg.Body, "/bot") || strings.HasPrefix(msgData.Msg.Body, "@gork")) {
		app.dispatchBotCommand(evCtx, ev, msgData, currentRoom)
		return
	}

	// Handle links.
	app.processLinks(evCtx, ev, msgData, currentRoom)
}

// findRoom returns the RoomIDEntry matching the given room ID.
func (app *App) findRoom(roomID id.RoomID) (config.RoomIDEntry, bool) {
	for _, r := range app.Cfg.RoomIDs {
		if string(roomID) == r.ID {
			return r, true
		}
	}
	return config.RoomIDEntry{}, false
}

// dispatchBotCommand parses and dispatches a bot command.
func (app *App) dispatchBotCommand(evCtx context.Context, ev *event.Event, msgData *db.MessageData, room config.RoomIDEntry) {
	if app.Cfg.DryRun {
		log.Info().Msg("dry run mode: skipping bot command")
		return
	}
	select {
	case <-app.ReadyChan:
	case <-evCtx.Done():
		return
	}

	normalizedBody := msgData.Msg.Body
	if strings.HasPrefix(msgData.Msg.Body, "@gork") {
		normalizedBody = "/bot gork " + strings.TrimSpace(strings.TrimPrefix(msgData.Msg.Body, "@gork"))
	}
	parts := strings.Fields(normalizedBody)
	cmd := "hi"
	if len(parts) >= 2 && parts[1] != "" {
		cmd = parts[1]
	}

	label := ResolveReplyLabel(app.Cfg, app.BotCfg)

	// Check command permissions.
	if len(room.AllowedCommands) > 0 && !util.InSlice(room.AllowedCommands, cmd) && cmd != "hi" {
		SendBotReply(evCtx, app.Client, ev.RoomID, ev.ID, label+"command not allowed in this room", cmd)
		return
	}

	if app.BotCfg == nil {
		SendBotReply(evCtx, app.Client, ev.RoomID, ev.ID, label+"no bot configuration loaded", cmd)
		return
	}

	if cmd == "help" {
		SendBotReply(evCtx, app.Client, ev.RoomID, ev.ID, label+GenerateHelpMessage(app.BotCfg, room.AllowedCommands), cmd)
		return
	}

	cmdCfg, ok := app.BotCfg.Commands[cmd]
	if !ok {
		SendBotReply(evCtx, app.Client, ev.RoomID, ev.ID, label+"Unknown command. "+GenerateHelpMessage(app.BotCfg, room.AllowedCommands), cmd)
		return
	}

	// Handle knockknock specially since it needs conversational state.
	if cmdCfg.Type == "builtin" && cmdCfg.Command == "knockknock" {
		go app.startKnockKnock(evCtx, ev, label)
		return
	}

	// Run the command in a goroutine to avoid blocking other messages.
	go func() {
		resp, err := bot.FetchBotCommand(evCtx, &cmdCfg, app.Cfg.LinkstashURL, ev, app.Client, app.Cfg.GroqAPIKey, label, app.MessagesDB)
		var body string
		if err != nil {
			log.Error().Err(err).Str("cmd", cmd).Msg("failed to execute bot command")
			body = fmt.Sprintf("sorry, couldn't execute %s right now", cmd)
		} else if resp != "" {
			body = resp
		} else {
			return // Command sent its own message (like images).
		}
		SendBotReply(evCtx, app.Client, ev.RoomID, ev.ID, label+body, cmd)
	}()
}

// startKnockKnock begins a knock-knock joke conversation.
func (app *App) startKnockKnock(ctx context.Context, ev *event.Event, label string) {
	joke := bot.KnockKnockJokes[grand.Intn(len(bot.KnockKnockJokes))]

	body := label + "Knock knock! (reply to this message)"
	content := event.MessageEventContent{
		MsgType:   event.MsgText,
		Body:      body,
		RelatesTo: &event.RelatesTo{InReplyTo: &event.InReplyTo{EventID: ev.ID}},
	}
	resp, err := app.Client.SendMessageEvent(ctx, ev.RoomID, event.EventMessage, &content)
	if err != nil {
		log.Error().Err(err).Msg("failed to send knock knock opener")
		return
	}

	app.KnockKnock.Set(resp.EventID, &bot.KnockKnockStep{
		Joke:  joke,
		Step:  0,
		Label: label,
	})

	// Clean up after 5 minutes if no reply.
	go func() {
		time.Sleep(5 * time.Minute)
		app.KnockKnock.Delete(resp.EventID)
	}()
}

// handleKnockKnockReply continues a knock-knock joke conversation.
func (app *App) handleKnockKnockReply(ctx context.Context, ev *event.Event, step *bot.KnockKnockStep, origEventID id.EventID) {
	app.KnockKnock.Delete(origEventID)

	if step.Step == 0 {
		// User replied to "Knock knock!" — send the name.
		body := fmt.Sprintf("%s%s (reply to this message)", step.Label, step.Joke.Name)
		content := event.MessageEventContent{
			MsgType:   event.MsgText,
			Body:      body,
			RelatesTo: &event.RelatesTo{InReplyTo: &event.InReplyTo{EventID: ev.ID}},
		}
		resp, err := app.Client.SendMessageEvent(ctx, ev.RoomID, event.EventMessage, &content)
		if err != nil {
			log.Error().Err(err).Msg("failed to send knock knock name")
			return
		}
		app.KnockKnock.Set(resp.EventID, &bot.KnockKnockStep{
			Joke:  step.Joke,
			Step:  1,
			Label: step.Label,
		})
		// Clean up after 5 minutes.
		go func() {
			time.Sleep(5 * time.Minute)
			app.KnockKnock.Delete(resp.EventID)
		}()
	} else {
		// User replied to the name — send the punchline!
		body := step.Label + step.Joke.Punchline
		SendBotReply(ctx, app.Client, ev.RoomID, ev.ID, body, "knockknock")
	}
}

// processLinks handles link extraction, hooks, and snapshot exports.
func (app *App) processLinks(_ context.Context, ev *event.Event, msgData *db.MessageData, room config.RoomIDEntry) {
	if len(msgData.URLs) == 0 {
		log.Debug().Msg("no links found")
		return
	}

	log.Info().Int("count", len(msgData.URLs)).Msg("found links:")
	for _, u := range msgData.URLs {
		log.Info().Str("url", u).Msg("link")
	}

	if app.Cfg.OptOutTag != "" && strings.Contains(msgData.Msg.Body, app.Cfg.OptOutTag) {
		log.Info().Str("tag", app.Cfg.OptOutTag).Msg("skipped sending hooks due to opt-out tag")
	} else if app.Cfg.DryRun {
		log.Info().Msg("dry run mode: skipping hooks")
	} else {
		blacklist, err := links.LoadBlacklist("blacklist.json")
		if err != nil {
			log.Error().Err(err).Msg("failed to load blacklist")
		}
		if room.Hook != "" {
			for _, u := range msgData.URLs {
				if blacklist != nil && links.IsBlacklisted(u, blacklist) {
					log.Info().Str("url", u).Msg("skipped blacklisted url")
					continue
				}
				go links.SendHook(room.Hook, u, room.Key, string(ev.Sender), room.ID, room.Comment, room.SendUser, room.SendTopic)
			}
		}
	}

	log.Info().Msg("stored to db, exporting snapshot...")
	if err := db.ExportAllSnapshots(app.MessagesDB, app.Cfg.RoomIDs, app.Cfg.LinksPath); err != nil {
		log.Error().Err(err).Msg("export snapshots")
	} else {
		log.Info().Str("path", app.Cfg.LinksPath).Msg("exported")
	}
}
