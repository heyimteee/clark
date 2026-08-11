package whatsapp

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/heyimteee/clark/internal/gateway"
	"github.com/heyimteee/clark/internal/logging"
	"github.com/heyimteee/clark/internal/tools"
	"github.com/mdp/qrterminal"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
)

// Options wires the transport to the rest of clark.
type Options struct {
	DBPath   string
	Butler   gateway.Butler
	Notifier gateway.Notifier
	Tools    *tools.Registry
	// BypassPhrase is the urgent-alert command word (default "get him to me").
	BypassPhrase string
	// NameToJID resolves a VIP name or number to a full jid for send_message.
	NameToJID func(input string) (string, bool)
}

// Run connects to WhatsApp, wires the handler, and blocks until ctx is done.
func Run(ctx context.Context, opts Options) error {
	dbLog := logging.NewWALogger("Database", logging.SevDebug)
	container, err := sqlstore.New(ctx, "sqlite3", "file:"+opts.DBPath+"?_foreign_keys=on", dbLog)
	if err != nil {
		return fmt.Errorf("fail to initiate database container: %v", err)
	}
	defer container.Close()

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		return fmt.Errorf("fail to get device connection: %v", err)
	}

	client := whatsmeow.NewClient(deviceStore, logging.NewWALogger("Client", logging.SevInfo))

	if latestVer, err := whatsmeow.GetLatestVersion(ctx, nil); err == nil {
		store.SetWAVersion(*latestVer)
		logging.Log("WHATSAPP", logging.SevInfo, "VERSION", "Client version detected", "version", latestVer)
	} else {
		logging.Log("WHATSAPP", logging.SevWarn, "VERSION", "Using bundled WhatsApp version", "version", store.GetWAVersion())
	}

	echo := gateway.NewEchoTracker()
	msgr := NewMessenger(client, echo)
	if opts.Tools != nil && opts.NameToJID != nil {
		registerSendMessageTool(opts.Tools, msgr, opts.NameToJID)
	}
	handler := NewHandler(msgr, opts.Butler, opts.Notifier, echo, time.Now(), opts.BypassPhrase)
	client.AddEventHandler(handler.OnEvent)

	if client.Store.ID == nil {
		logging.Log("WHATSAPP", logging.SevNotice, "AUTH", "No session found", "action", "pair")
		qrChan, _ := client.GetQRChannel(ctx)
		if err := client.Connect(); err != nil {
			return fmt.Errorf("fail to connect through QR: %v", err)
		}

		for evt := range qrChan {
			if evt.Event == "code" {
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
			}
		}
	} else {
		logging.Log("WHATSAPP", logging.SevNotice, "AUTH", "Existing session found", "action", "reconnect")
		if err := client.Connect(); err != nil {
			return fmt.Errorf("fail to connect to existing session: %v", err)
		}
	}

	logging.Log("CLARK", logging.SevInfo, "STATUS", "Assistant is online")
	<-ctx.Done()
	handler.Close()
	client.Disconnect()
	return nil
}

// registerSendMessageTool wires the send_message capability, which lets the
// Master have clark deliver a message to a VIP by name or number.
func registerSendMessageTool(reg *tools.Registry, msgr *WAMessenger, nameToJID func(string) (string, bool)) {
	reg.RegisterFunc(
		"send_message",
		"Send a WhatsApp message to a VIP on the Master's behalf. Only the Master may use this.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"recipient": map[string]any{"type": "string", "description": "A VIP's name or phone number"},
				"message":   map[string]any{"type": "string", "description": "The message text to deliver"},
			},
			"required": []string{"recipient", "message"},
		},
		func(ctx context.Context, args map[string]any) (string, error) {
			if !tools.IsMaster(ctx) {
				return "", fmt.Errorf("forbidden: only the Master may send messages")
			}
			recipient := tools.StringArg(args, "recipient")
			message := tools.StringArg(args, "message")
			if recipient == "" || message == "" {
				return "", fmt.Errorf("recipient and message are required")
			}

			jid, ok := nameToJID(recipient)
			if !ok {
				return "", fmt.Errorf("no VIP found matching %q", recipient)
			}
			if _, err := types.ParseJID(jid); err != nil {
				return "", fmt.Errorf("invalid recipient jid %q: %w", jid, err)
			}
			if err := msgr.Send(ctx, jid, message); err != nil {
				return "", err
			}
			return "Message delivered to " + recipient + ".", nil
		},
	)
}
