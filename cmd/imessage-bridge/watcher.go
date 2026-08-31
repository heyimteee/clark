package main

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/heyimteee/clark/internal/imessage"
	"github.com/heyimteee/clark/internal/logging"
)

// inboundClient is the slice of the clark API the watcher needs.
type inboundClient interface {
	PostInbound(ctx context.Context, msg imessage.InboundMessage) error
}

// Watcher polls chat.db for new inbound messages and forwards them to clark,
// persisting a ROWID watermark so nothing is replayed after a restart.
type Watcher struct {
	db        *sql.DB
	statePath string
	ownHandle string
	client    inboundClient
	interval  time.Duration
	lastRowID int64
}

// NewWatcher wires the poller around a read-only chat.db handle.
func NewWatcher(db *sql.DB, statePath string, ownHandle string, client inboundClient, interval time.Duration) *Watcher {
	return &Watcher{
		db:        db,
		statePath: statePath,
		ownHandle: ownHandle,
		client:    client,
		interval:  interval,
	}
}

// Run scans until ctx is cancelled. It bootstraps the watermark on first run
// (or after the state file is lost) so an existing chat history is skipped.
func (w *Watcher) Run(ctx context.Context) error {
	if err := w.bootstrap(ctx); err != nil {
		return err
	}

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			w.scanOnce(ctx)
		}
	}
}

// bootstrap seeds the watermark from the state file, falling back to the
// current max ROWID when none is recorded (fresh install or lost file).
func (w *Watcher) bootstrap(ctx context.Context) error {
	st, err := loadState(w.statePath)
	if err != nil {
		return err
	}
	if st.LastRowID > 0 {
		w.lastRowID = st.LastRowID
		logging.Log("BRIDGE", logging.SevInfo, "STATE", "Watermark loaded", "row", w.lastRowID)
		return nil
	}

	max, err := maxRowID(w.db)
	if err != nil {
		return err
	}
	w.lastRowID = max
	logging.Log("BRIDGE", logging.SevNotice, "STATE", "No watermark; bootstrapped to current history", "row", max)
	return w.persist(ctx)
}

// scanOnce forwards every qualifying message newer than the watermark,
// advancing it per message only after clark accepts the POST. A failed POST
// leaves the watermark behind so the message is retried next tick (at-least-
// once delivery, matching the plan).
func (w *Watcher) scanOnce(ctx context.Context) {
	msgs, err := queryNewMessages(w.db, w.lastRowID)
	if err != nil {
		logging.Log("BRIDGE", logging.SevErr, "SCAN", "Failed to query new messages", "error", err)
		return
	}

	for _, m := range msgs {
		if ctx.Err() != nil {
			return
		}
		media := collectIMessageMedia(w.db, m)
		inbound := w.toInbound(m, media)
		if err := w.client.PostInbound(ctx, inbound); err != nil {
			logging.Log("BRIDGE", logging.SevErr, "SCAN", "Failed to forward message", "row", m.RowID, "error", err)
			return
		}
		w.lastRowID = m.RowID
		if err := w.persist(ctx); err != nil {
			logging.Log("BRIDGE", logging.SevErr, "SCAN", "Failed to persist watermark", "error", err)
		}
		logging.Log("BRIDGE", logging.SevInfo, "SCAN", "Forwarded inbound message", "row", m.RowID, "from", m.Handle)
	}
}

func (w *Watcher) persist(ctx context.Context) error {
	if err := (state{LastRowID: w.lastRowID}).save(w.statePath); err != nil {
		return fmt.Errorf("fail to save watermark: %w", err)
	}
	return nil
}

// toInbound maps a chat.db row to the clark inbound protocol. The handle may
// be empty when the message has no handle row; clark drops such messages, but
// the watermark still advances so a broken row cannot wedge the poller.
func (w *Watcher) toInbound(m newMessage, media []imessage.InboundMedia) imessage.InboundMessage {
	mediaType := ""
	if len(media) > 0 {
		mediaType = media[0].Type
	}
	return imessage.InboundMessage{
		ID:        strconv.FormatInt(m.RowID, 10),
		Handle:    m.Handle,
		Text:      m.Text,
		IsSelf:    m.Handle != "" && m.Handle == w.ownHandle,
		Timestamp: messageTime(m.Date),
		MediaType: mediaType,
		Media:     media,
	}
}
