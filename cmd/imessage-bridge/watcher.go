package main

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/heyimteee/clark/internal/imessage"
	"github.com/heyimteee/clark/internal/logging"
)

// inboundClient is the slice of the clark API the watcher needs.
type inboundClient interface {
	PostInbound(ctx context.Context, msg imessage.InboundMessage) error
}

// Watcher scans chat.db for new inbound messages and forwards them to clark,
// persisting a ROWID watermark so nothing is replayed after a restart. Scans are
// driven by filesystem events, with a low-frequency poll as a backstop (#208).
type Watcher struct {
	db        *sql.DB
	dbPath    string
	statePath string
	ownHandle string
	client    inboundClient
	interval  time.Duration
	lastRowID int64
	// lastScanAt is when the previous scan cycle completed. A row that surfaces
	// now but predates it is by definition one this watcher was blind to, so it
	// is flagged as a replay (#206). Initialised at Run entry, which makes every
	// message delivered on the first scan after a restart a replay — correct,
	// since the bridge was not watching while the Mac was down.
	lastScanAt time.Time
	// now is injectable for tests; clock() falls back to time.Now so a
	// directly-constructed Watcher cannot nil-panic on the time source.
	now func() time.Time
	// fs is the kqueue-backed filesystem watcher, nil when unavailable.
	fs *fsnotify.Watcher
	// wake carries debounced scan triggers from watchLoop to the scan loop.
	wake chan struct{}
}

// clock returns the watcher's time source, tolerating a nil injection point.
func (w *Watcher) clock() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

// NewWatcher wires the scanner around a read-only chat.db handle. dbPath is that
// database's filesystem location, used to arm the filesystem watch. interval is
// kept for the caller's configured cadence; the event watcher is the primary
// trigger and the internal fallback poll is the backstop (#208).
func NewWatcher(db *sql.DB, statePath string, ownHandle string, client inboundClient, interval time.Duration, dbPath string) *Watcher {
	return &Watcher{
		db:        db,
		dbPath:    dbPath,
		statePath: statePath,
		ownHandle: ownHandle,
		client:    client,
		interval:  interval,
		now:       time.Now,
		wake:      make(chan struct{}, 1),
	}
}

// Run scans until ctx is cancelled. It bootstraps the watermark on first run
// (or after the state file is lost) so an existing chat history is skipped.
func (w *Watcher) Run(ctx context.Context) error {
	if err := w.bootstrap(ctx); err != nil {
		return err
	}
	w.lastScanAt = w.clock()

	w.startFileWatch(ctx)
	defer w.stopFileWatch()

	w.runScanLoop(ctx)
	return nil
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
		logging.Log("BRIDGE", logging.SevInfo, "SCAN", "Forwarded inbound message", "row", m.RowID, "from", m.Handle, "replay", inbound.Replay)
	}
	// Advance the blind-spot marker only after a clean pass. A cycle that bailed
	// out early has not proven it saw everything, so the earlier timestamp is the
	// conservative thing to compare against next time.
	w.lastScanAt = w.clock()
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
		Replay:    w.isReplay(m),
		MediaType: mediaType,
		Media:     media,
	}
}

// isReplay reports whether this row is backlog the watcher was blind to. A row
// surfacing now whose send time predates the last completed scan could not have
// been seen live — the Mac was asleep, the bridge was down, or the scan failed
// and the row was picked up later (#206).
//
// lastScanAt is set at Run entry, so everything delivered on the first scan
// after a start is a replay. A message that arrives *after* that scan is live
// and is answered normally, which is what keeps ordinary conversation unaffected.
func (w *Watcher) isReplay(m newMessage) bool {
	if w.lastScanAt.IsZero() {
		return false
	}
	return messageTime(m.Date).Before(w.lastScanAt)
}
