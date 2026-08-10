package cmd

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/mdp/qrterminal"
	"github.com/tristnaja/clark/internal/whatsapp"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
)

func ExecRun(ast *whatsapp.Assistant) error {
	available, err := ast.CheckAst()

	if err != nil {
		return err
	}

	if !available {
		return fmt.Errorf("No assistant is initiated Sir. Do 'clark init' first.")
	}

	if ast.MasterContext == "" {
		return fmt.Errorf("No context yet Sir. Do 'clark ctx -c [context]' first.")
	}

	if ast.Status == false {
		return fmt.Errorf("Clark is not active yet Sir. Do 'clark toggle' to toggle it on.")
	}

	whatsapp.Log("CLARK", whatsapp.SevInfo, "START", "Assistant started", "name", ast.Name)
	whatsapp.Log("CLARK", whatsapp.SevInfo, "CONTEXT", "Master context loaded", "context", ast.MasterContext)

	dbLog := whatsapp.NewWALogger("Database", whatsapp.SevDebug)
	container, err := sqlstore.New(context.Background(), "sqlite3", "file:mystore.db?_foreign_keys=on", dbLog)

	if err != nil {
		return fmt.Errorf("fail to initiate database container: %v", err)
	}

	defer container.Close()

	rawDb, err := sql.Open("sqlite3", "mystore.db")

	if err != nil {
		return fmt.Errorf("fail to open database: %v", err)
	}

	defer rawDb.Close()

	if err := rawDb.Ping(); err != nil {
		return fmt.Errorf("fail to ping database: %v", err)
	}

	deviceStore, err := container.GetFirstDevice(context.Background())

	if err != nil {
		return fmt.Errorf("fail to get device connection: %v", err)
	}

	client := whatsmeow.NewClient(deviceStore, whatsapp.NewWALogger("Client", whatsapp.SevInfo))
	connectedAt := time.Now()

	if latestVer, err := whatsmeow.GetLatestVersion(context.Background(), nil); err == nil {
		store.SetWAVersion(*latestVer)
		whatsapp.Log("WHATSAPP", whatsapp.SevInfo, "VERSION", "Client version detected", "version", latestVer)
	} else {
		whatsapp.Log("WHATSAPP", whatsapp.SevWarn, "VERSION", "Using bundled WhatsApp version", "version", store.GetWAVersion())
	}

	client.AddEventHandler(whatsapp.EventHandler(client, ast, connectedAt))

	if client.Store.ID == nil {
		whatsapp.Log("WHATSAPP", whatsapp.SevNotice, "AUTH", "No session found", "action", "pair")
		qrChan, _ := client.GetQRChannel(context.Background())
		err := client.Connect()

		if err != nil {
			return fmt.Errorf("fail to connect through QR: %v", err)
		}

		for evt := range qrChan {
			if evt.Event == "code" {
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
			}
		}
	} else {
		whatsapp.Log("WHATSAPP", whatsapp.SevNotice, "AUTH", "Existing session found", "action", "reconnect")
		err := client.Connect()

		if err != nil {
			return fmt.Errorf("fail to connect to existing session: %v", err)
		}
	}

	whatsapp.Log("CLARK", whatsapp.SevInfo, "STATUS", "Assistant is online")

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c
	client.Disconnect()
	return nil
}
