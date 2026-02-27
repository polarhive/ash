package matrix

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/rs/zerolog/log"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/polarhive/ash/config"
	"github.com/polarhive/ash/db"
)

// Credentials holds stored Matrix login credentials.
type Credentials struct {
	UserID      string
	AccessToken string
	DeviceID    string
}

// LoadOrCreate loads stored credentials or performs a fresh login.
func LoadOrCreate(ctx context.Context, database *sql.DB, cfg *config.Config) (*mautrix.Client, error) {
	storedCreds, err := loadStored(ctx, database)
	if err == nil && storedCreds != nil {
		return createClientFromCreds(cfg.Homeserver, storedCreds)
	}
	client, creds, err := loginWithPassword(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := storeCreds(ctx, database, creds); err != nil {
		fmt.Fprintf(os.Stderr, "warning: couldn't store credentials: %v\n", err)
	}
	return client, nil
}

// EnsureSecrets prompts for missing credentials and stores them.
func EnsureSecrets(ctx context.Context, database *sql.DB, cfg *config.Config) error {
	reader := bufio.NewReader(os.Stdin)
	type field struct {
		label   string
		metaKey string
		target  *string
	}
	fields := []field{
		{"Homeserver URL", "homeserver", &cfg.Homeserver},
		{"Matrix user ID", "user_id", &cfg.User},
		{"Password", "password", &cfg.Password},
		{"Recovery key (format: EsXX XXXX ...)", "recovery_key", &cfg.RecoveryKey},
	}
	for i := range fields {
		f := &fields[i]
		if *f.target == "" {
			if val, err := db.GetMeta(ctx, database, f.metaKey); err == nil && val != "" {
				*f.target = val
				continue
			}
		}
		for *f.target == "" {
			fmt.Printf("%s: ", f.label)
			line, err := reader.ReadString('\n')
			if err != nil {
				return fmt.Errorf("read %s: %w", f.label, err)
			}
			*f.target = strings.TrimSpace(line)
		}
		if err := db.SetMeta(ctx, database, f.metaKey, *f.target); err != nil {
			return fmt.Errorf("save %s: %w", f.label, err)
		}
	}
	return nil
}

func loadStored(ctx context.Context, database *sql.DB) (*Credentials, error) {
	userID, _ := db.GetMeta(ctx, database, "user_id")
	token, _ := db.GetMeta(ctx, database, "access_token")
	deviceID, _ := db.GetMeta(ctx, database, "device_id")
	if userID == "" || token == "" || deviceID == "" {
		return nil, fmt.Errorf("incomplete stored credentials")
	}
	return &Credentials{userID, token, deviceID}, nil
}

func createClientFromCreds(homeserver string, creds *Credentials) (*mautrix.Client, error) {
	client, err := mautrix.NewClient(homeserver, id.UserID(creds.UserID), creds.AccessToken)
	if err != nil {
		return nil, err
	}
	client.DeviceID = id.DeviceID(creds.DeviceID)
	return client, nil
}

func loginWithPassword(ctx context.Context, cfg *config.Config) (*mautrix.Client, *Credentials, error) {
	client, err := mautrix.NewClient(cfg.Homeserver, "", "")
	if err != nil {
		return nil, nil, err
	}
	loginReq := mautrix.ReqLogin{
		Type:                     "m.login.password",
		Identifier:               mautrix.UserIdentifier{Type: "m.id.user", User: cfg.User},
		Password:                 cfg.Password,
		InitialDeviceDisplayName: cfg.DeviceName,
		StoreCredentials:         true,
	}
	resp, err := client.Login(ctx, &loginReq)
	if err != nil {
		return nil, nil, err
	}
	client.SetCredentials(resp.UserID, resp.AccessToken)
	client.DeviceID = resp.DeviceID
	return client, &Credentials{string(resp.UserID), resp.AccessToken, string(resp.DeviceID)}, nil
}

func storeCreds(ctx context.Context, database *sql.DB, creds *Credentials) error {
	if err := db.SetMeta(ctx, database, "user_id", creds.UserID); err != nil {
		return err
	}
	if err := db.SetMeta(ctx, database, "access_token", creds.AccessToken); err != nil {
		return err
	}
	return db.SetMeta(ctx, database, "device_id", creds.DeviceID)
}

// EnsurePickleKey generates or retrieves the pickle key for crypto.
func EnsurePickleKey(ctx context.Context, metaDB *sql.DB) (string, error) {
	pickleKey, err := db.GetMeta(ctx, metaDB, "pickle_key")
	if err == nil && pickleKey != "" {
		return pickleKey, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("generate pickle key: %w", err)
	}
	pickleKey = base64.StdEncoding.EncodeToString(key)
	if err := db.SetMeta(ctx, metaDB, "pickle_key", pickleKey); err != nil {
		return "", fmt.Errorf("save pickle key: %w", err)
	}
	return pickleKey, nil
}

// SetupHelper initializes the crypto helper for E2EE.
func SetupHelper(ctx context.Context, client *mautrix.Client, metaDB *sql.DB, metaDBPath string) (*cryptohelper.CryptoHelper, error) {
	pickleKey, err := db.GetMeta(ctx, metaDB, "pickle_key")
	if err != nil {
		return nil, fmt.Errorf("get pickle key: %w", err)
	}
	pickleKeyBytes, err := base64.StdEncoding.DecodeString(pickleKey)
	if err != nil {
		return nil, fmt.Errorf("decode pickle key: %w", err)
	}
	cryptoDBPath := metaDBPath + ".crypto"
	helper, err := cryptohelper.NewCryptoHelper(client, pickleKeyBytes, cryptoDBPath)
	if err != nil {
		if strings.Contains(err.Error(), "mismatching device ID") {
			for _, fname := range []string{cryptoDBPath, cryptoDBPath + "-shm", cryptoDBPath + "-wal"} {
				_ = os.Remove(fname)
			}
			helper, err = cryptohelper.NewCryptoHelper(client, pickleKeyBytes, cryptoDBPath)
			if err != nil {
				return nil, fmt.Errorf("new crypto helper (after cleanup): %w", err)
			}
		} else {
			return nil, fmt.Errorf("new crypto helper: %w", err)
		}
	}
	if err := helper.Init(ctx); err != nil {
		return nil, fmt.Errorf("init crypto helper: %w", err)
	}
	return helper, nil
}

// VerifyWithRecoveryKey verifies the session using a recovery key.
func VerifyWithRecoveryKey(ctx context.Context, machine *crypto.OlmMachine, recoveryKey string) error {
	keyID, keyData, err := machine.SSSS.GetDefaultKeyData(ctx)
	if err != nil {
		return fmt.Errorf("get key data: %w", err)
	}
	key, err := keyData.VerifyRecoveryKey(keyID, recoveryKey)
	if err != nil {
		return fmt.Errorf("verify recovery key: %w", err)
	}
	if err := machine.FetchCrossSigningKeysFromSSSS(ctx, key); err != nil {
		return fmt.Errorf("fetch cross-signing keys: %w", err)
	}
	if err := machine.SignOwnDevice(ctx, machine.OwnIdentity()); err != nil {
		return fmt.Errorf("sign own device: %w", err)
	}
	if err := machine.SignOwnMasterKey(ctx); err != nil {
		return fmt.Errorf("sign own master key: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Event & media helpers
// ---------------------------------------------------------------------------

// ParseEvent safely parses the raw content of an event.
func ParseEvent(ev *event.Event) {
	if ev.Content.Raw != nil {
		if err := ev.Content.ParseRaw(ev.Type); err != nil {
			if !strings.Contains(err.Error(), "already parsed") {
				log.Warn().Err(err).Str("event_id", string(ev.ID)).Msg("parse event")
			}
		}
	}
}

// FetchAndDecrypt fetches a Matrix event and decrypts it if encrypted.
func FetchAndDecrypt(ctx context.Context, client *mautrix.Client, roomID id.RoomID, eventID id.EventID) (*event.Event, error) {
	ev, err := client.GetEvent(ctx, roomID, eventID)
	if err != nil {
		return nil, fmt.Errorf("fetch event %s: %w", eventID, err)
	}
	if ev.Content.Raw != nil {
		if err := ev.Content.ParseRaw(ev.Type); err != nil {
			return nil, fmt.Errorf("parse event: %w", err)
		}
	}
	if ev.Type == event.EventEncrypted && client.Crypto != nil {
		decrypted, err := client.Crypto.Decrypt(ctx, ev)
		if err != nil {
			return nil, fmt.Errorf("decrypt event: %w", err)
		}
		return decrypted, nil
	}
	return ev, nil
}

// IsImageMessage checks whether a message contains an image.
func IsImageMessage(msg *event.MessageEventContent) bool {
	return msg.MsgType == event.MsgImage || msg.MsgType == "m.sticker" || msg.URL != "" || msg.File != nil
}

// SendImageToMatrix uploads and sends an image as a reply.
func SendImageToMatrix(ctx context.Context, client *mautrix.Client, roomID id.RoomID, eventID id.EventID, imageData []byte, contentType, body string) error {
	uploadResp, err := client.UploadBytes(ctx, imageData, contentType)
	if err != nil {
		return fmt.Errorf("upload image: %w", err)
	}
	content := event.MessageEventContent{
		MsgType:   event.MsgImage,
		Body:      body,
		URL:       uploadResp.ContentURI.CUString(),
		RelatesTo: &event.RelatesTo{InReplyTo: &event.InReplyTo{EventID: eventID}},
	}
	if _, err := client.SendMessageEvent(ctx, roomID, event.EventMessage, &content); err != nil {
		return fmt.Errorf("send image: %w", err)
	}
	return nil
}

// DownloadImageFromMessage extracts the image from a message or its replied-to message.
func DownloadImageFromMessage(ctx context.Context, client *mautrix.Client, ev *event.Event) (*event.MessageEventContent, error) {
	ParseEvent(ev)
	msg := ev.Content.AsMessage()
	if msg == nil {
		return nil, fmt.Errorf("not a message event")
	}
	if IsImageMessage(msg) {
		return msg, nil
	}
	if msg.RelatesTo == nil || msg.RelatesTo.InReplyTo == nil {
		return nil, fmt.Errorf("no image found")
	}
	original, err := FetchAndDecrypt(ctx, client, ev.RoomID, msg.RelatesTo.InReplyTo.EventID)
	if err != nil {
		return nil, err
	}
	origMsg := original.Content.AsMessage()
	if origMsg != nil && IsImageMessage(origMsg) {
		return origMsg, nil
	}
	return nil, fmt.Errorf("no image found")
}

// DownloadImageBytes downloads image data from a Matrix content URI.
func DownloadImageBytes(ctx context.Context, client *mautrix.Client, mediaURL id.ContentURIString, encryptedFile *event.EncryptedFileInfo) ([]byte, error) {
	if mediaURL == "" {
		return nil, fmt.Errorf("no media URL")
	}
	parsed, err := id.ParseContentURI(string(mediaURL))
	if err != nil {
		return nil, fmt.Errorf("parse media URL: %w", err)
	}
	data, err := client.DownloadBytes(ctx, parsed)
	if err != nil {
		return nil, fmt.Errorf("download image: %w", err)
	}
	if encryptedFile != nil {
		if err := encryptedFile.PrepareForDecryption(); err != nil {
			return nil, fmt.Errorf("prepare decryption: %w", err)
		}
		data, err = encryptedFile.Decrypt(data)
		if err != nil {
			return nil, fmt.Errorf("decrypt image: %w", err)
		}
	}
	return data, nil
}

// MediaFromMessage returns the media URL and optional encrypted file info.
func MediaFromMessage(msg *event.MessageEventContent) (id.ContentURIString, *event.EncryptedFileInfo, error) {
	if msg.File != nil {
		return msg.File.URL, msg.File, nil
	}
	if msg.URL != "" {
		return msg.URL, nil, nil
	}
	return "", nil, fmt.Errorf("no media URL")
}

// DetectImageExtension uses the `file` command to determine image type.
func DetectImageExtension(inputPath string) string {
	out, err := exec.Command("file", inputPath).Output()
	if err != nil {
		return ".png"
	}
	lower := strings.ToLower(string(out))
	switch {
	case strings.Contains(lower, "jpeg") || strings.Contains(lower, "jpg"):
		return ".jpg"
	case strings.Contains(lower, "png"):
		return ".png"
	case strings.Contains(lower, "gif"):
		return ".gif"
	case strings.Contains(lower, "webp") || strings.Contains(lower, "web/p"):
		return ".webp"
	default:
		return ".png"
	}
}
