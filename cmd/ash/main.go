package main

import (
	"context"
	"database/sql"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"

	"github.com/polarhive/ash/app"
	"github.com/polarhive/ash/bot"
	"github.com/polarhive/ash/config"
	"github.com/polarhive/ash/db"
	"github.com/polarhive/ash/matrix"
)

// main initializes the application, loads config, sets up databases, and starts the bot.
func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	log.Debug().Msg("starting")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg, err := config.LoadConfig()
	must(err, "load config")
	if cfg.Debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}
	log.Debug().Msg("config loaded")

	metaDB, err := db.OpenMeta(ctx, cfg.MetaDBPath)
	must(err, "open meta db")
	defer metaDB.Close()

	must(matrix.EnsureSecrets(ctx, metaDB, cfg), "ensure secrets")

	messagesDB, err := db.OpenMessages(ctx, cfg.DBPath)
	must(err, "open messages db")
	defer messagesDB.Close()

	_, err = matrix.EnsurePickleKey(ctx, metaDB)
	must(err, "ensure pickle key")

	must(run(ctx, metaDB, messagesDB, cfg), "run")
	log.Debug().Msg("exiting")
}

// run starts the Matrix client, sets up sync, and handles messages.
func run(ctx context.Context, metaDB *sql.DB, messagesDB *sql.DB, cfg *config.Config) error {
	log.Info().Msgf("logging in as %s to %s (E2EE initializing)", cfg.User, cfg.Homeserver)
	var roomNames []string
	for _, r := range cfg.RoomIDs {
		roomNames = append(roomNames, r.Comment)
	}
	log.Info().Msgf("ready: watching rooms: [%s]", strings.Join(roomNames, ", "))

	client, err := matrix.LoadOrCreate(ctx, metaDB, cfg)
	if err != nil {
		return err
	}
	client.SyncPresence = "offline"
	syncer := mautrix.NewDefaultSyncer()
	client.Syncer = syncer
	client.Store = &db.MetaSyncStore{DB: metaDB}

	cryptoHelper, err := matrix.SetupHelper(ctx, client, metaDB, cfg.MetaDBPath)
	if err != nil {
		return err
	}
	client.Crypto = cryptoHelper
	if err := matrix.VerifyWithRecoveryKey(ctx, cryptoHelper.Machine(), cfg.RecoveryKey); err != nil {
		log.Warn().Err(err).Msg("failed to verify session with recovery key")
	}

	// Load bot configuration (optional).
	botCfgPath := cfg.BotConfigPath
	if botCfgPath == "" {
		botCfgPath = "./bot.json"
	}
	botCfg, err := bot.LoadBotConfig(botCfgPath)
	if err != nil {
		log.Warn().Err(err).Str("path", botCfgPath).Msg("failed to load bot config (continuing without)")
	} else {
		log.Info().Str("path", botCfgPath).Msg("loaded bot config")
	}

	// Set yap leaderboard timezone from config (defaults to UTC).
	if cfg.Timezone != "" {
		if tz, err := time.LoadLocation(cfg.Timezone); err != nil {
			log.Warn().Err(err).Str("tz", cfg.Timezone).Msg("invalid TIMEZONE in config, using UTC")
		} else {
			bot.YapTimezone = tz
			log.Info().Str("tz", cfg.Timezone).Msg("yap leaderboard timezone set")
		}
	}

	readyChan := make(chan bool)
	var once sync.Once
	syncer.OnSync(func(_ context.Context, _ *mautrix.RespSync, _ string) bool {
		once.Do(func() { close(readyChan) })
		return true
	})

	a := &app.App{
		Cfg:        cfg,
		MessagesDB: messagesDB,
		BotCfg:     botCfg,
		Client:     client,
		ReadyChan:  readyChan,
		KnockKnock: bot.NewKnockKnockState(),
	}
	syncer.OnEventType(event.EventMessage, a.HandleMessage)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error().Msgf("sync goroutine panic: %v", r)
			}
		}()
		log.Debug().Msg("starting sync")
		if err := client.Sync(); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("sync error")
		}
	}()

	select {
	case <-readyChan:
	case <-ctx.Done():
		return ctx.Err()
	}
	<-ctx.Done()
	log.Debug().Msg("exiting run")
	return ctx.Err()
}

func must(err error, context string) {
	if err != nil {
		log.Fatal().Err(err).Msgf("%s", context)
	}
}
