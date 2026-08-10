package whatsapp

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/heyimteee/clark/internal/logging"
	"github.com/mdp/qrterminal"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
)

// Options wires the transport to the rest of clark.
type Options struct {
	DBPath   string
	Butler   Butler
	Notifier Notifier
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

	echo := NewEchoTracker()
	handler := NewHandler(NewMessenger(client, echo), opts.Butler, opts.Notifier, echo, time.Now())
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
	client.Disconnect()
	return nil
}
