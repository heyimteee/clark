package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/heyimteee/clark/internal/imessage"
)

type fakeInboundClient struct {
	mu       sync.Mutex
	posted   []imessage.InboundMessage
	failNext bool
}

func (f *fakeInboundClient) PostInbound(_ context.Context, msg imessage.InboundMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		f.failNext = false
		return errFakeForward
	}
	f.posted = append(f.posted, msg)
	return nil
}

func (f *fakeInboundClient) posts() []imessage.InboundMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]imessage.InboundMessage(nil), f.posted...)
}

var errFakeForward = &fakeErr{}

type fakeErr struct{}

func (*fakeErr) Error() string { return "fake forward failure" }

func TestWatcherBootstrapSkipsHistory(t *testing.T) {
	db := openSynthDB(t)
	addMessage(t, db, "+6281267858909", "old message", false, false, nil)

	fake := &fakeInboundClient{}
	w := &Watcher{db: db, statePath: filepath.Join(t.TempDir(), "state.json"), ownHandle: "", client: fake, interval: time.Second}

	ctx := context.Background()
	if err := w.bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if w.lastRowID != 1 {
		t.Fatalf("lastRowID = %d, want 1 (max existing ROWID)", w.lastRowID)
	}

	w.scanOnce(ctx)
	if got := fake.posts(); len(got) != 0 {
		t.Fatalf("existing history forwarded %d messages, want 0", len(got))
	}
}

func TestWatcherScanForwardsAndPersistsWatermark(t *testing.T) {
	db := openSynthDB(t)
	addMessage(t, db, "+6281267858909", "pre-existing", false, false, nil)

	fake := &fakeInboundClient{}
	statePath := filepath.Join(t.TempDir(), "state.json")
	w := &Watcher{db: db, statePath: statePath, ownHandle: "", client: fake, interval: time.Second}

	ctx := context.Background()
	if err := w.bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	addMessage(t, db, "+6281267858909", "new message", false, false, nil)
	w.scanOnce(ctx)

	posts := fake.posts()
	if len(posts) != 1 {
		t.Fatalf("forwarded %d messages, want 1", len(posts))
	}
	if posts[0].Handle != "+6281267858909" || posts[0].Text != "new message" || posts[0].IsSelf {
		t.Errorf("post = %+v, want new inbound DM", posts[0])
	}

	st, err := loadState(statePath)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if st.LastRowID != 2 {
		t.Errorf("persisted LastRowID = %d, want 2", st.LastRowID)
	}
}

func TestWatcherDoesNotAdvanceOnFailure(t *testing.T) {
	db := openSynthDB(t)
	fake := &fakeInboundClient{}
	w := &Watcher{db: db, statePath: filepath.Join(t.TempDir(), "state.json"), ownHandle: "", client: fake, interval: time.Second}
	ctx := context.Background()
	if err := w.bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	addMessage(t, db, "+6281267858909", "will fail", false, false, nil)
	fake.failNext = true
	w.scanOnce(ctx)

	if w.lastRowID != 0 {
		t.Fatalf("watermark advanced on failed POST: lastRowID = %d, want 0", w.lastRowID)
	}
	st, err := loadState(w.statePath)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if st.LastRowID != 0 {
		t.Errorf("state advanced to %d on failure, want 0", st.LastRowID)
	}

	w.scanOnce(ctx)
	if got := fake.posts(); len(got) != 1 || got[0].Text != "will fail" {
		t.Fatalf("failed message not retried: %+v", got)
	}
}

func TestWatcherLoadsExistingWatermark(t *testing.T) {
	db := openSynthDB(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := (state{LastRowID: 7}).save(statePath); err != nil {
		t.Fatal(err)
	}

	fake := &fakeInboundClient{}
	w := &Watcher{db: db, statePath: statePath, ownHandle: "", client: fake, interval: time.Second}
	if err := w.bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if w.lastRowID != 7 {
		t.Errorf("lastRowID = %d, want 7 from state file", w.lastRowID)
	}
}

func TestWatcherToInboundMapsSelf(t *testing.T) {
	w := &Watcher{ownHandle: "+6281267858909"}
	m := newMessage{RowID: 3, Handle: "+6281267858909", Text: "self note", Date: 0}
	got := w.toInbound(m)
	if !got.IsSelf || got.ID != "3" || got.Text != "self note" {
		t.Errorf("self mapping = %+v", got)
	}
	if !got.Timestamp.Equal(epoch) {
		t.Errorf("timestamp = %v, want epoch", got.Timestamp)
	}

	other := newMessage{RowID: 4, Handle: "+6281111111111", Text: "hi", Date: 0}
	got2 := w.toInbound(other)
	if got2.IsSelf {
		t.Error("stranger should not map as self")
	}
}

func TestWatcherStateDirCreation(t *testing.T) {
	db := openSynthDB(t)
	fake := &fakeInboundClient{}
	statePath := filepath.Join(t.TempDir(), "a", "b", "state.json")
	w := &Watcher{db: db, statePath: statePath, ownHandle: "", client: fake, interval: time.Second}
	ctx := context.Background()
	if err := w.bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Errorf("state file not written: %v", err)
	}
}
