package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// writeSynthDB creates a valid, readable chat.db-shaped file at path. Used to
// simulate Full Disk Access being restored mid-flight.
func writeSynthDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	if _, err := db.Exec(synthDDL); err != nil {
		db.Close()
		t.Fatalf("create fixture schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture db: %v", err)
	}
}

// TestOpenChatDBWithRetryRecovers proves the bridge needs no manual restart after
// Full Disk Access is restored (#204): a failed open is retried, and the caller
// learns the moment the database becomes readable.
func TestOpenChatDBWithRetryRecovers(t *testing.T) {
	// A real path that does not exist yet, so the first attempt fails exactly the
	// way a revoked FDA grant fails.
	path := filepath.Join(t.TempDir(), "chat.db")
	if _, err := openChatDB(path); err == nil {
		t.Fatal("openChatDB on a missing file succeeded; want an error")
	}

	// Stand in for the user granting Full Disk Access: materialise the database
	// from inside the first failure callback. onFail runs synchronously on the
	// test goroutine, so this stays deterministic and cannot outlive the test.
	fails, successes := 0, 0
	db, err := openChatDBWithRetry(context.Background(), path,
		10*time.Millisecond, 20*time.Millisecond,
		func(error) {
			fails++
			if fails == 1 {
				writeSynthDB(t, path)
			}
		},
		func() { successes++ },
	)
	if err != nil {
		t.Fatalf("openChatDBWithRetry: %v", err)
	}
	defer db.Close()

	if fails != 1 {
		t.Errorf("onFail calls = %d, want exactly 1 (the retry that found the restored grant)", fails)
	}
	if successes != 1 {
		t.Errorf("onSuccess calls = %d, want exactly 1", successes)
	}
	if err := db.Ping(); err != nil {
		t.Errorf("returned handle is not usable: %v", err)
	}
}

// TestOpenChatDBWithRetryStopsOnCancel proves a bridge shut down during a Full
// Disk Access denial exits promptly instead of retrying forever.
func TestOpenChatDBWithRetryStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	db, err := openChatDBWithRetry(ctx, filepath.Join(t.TempDir(), "missing.db"),
		time.Hour, time.Hour, nil, nil)
	if err == nil {
		db.Close()
		t.Fatal("openChatDBWithRetry returned a handle under a cancelled context")
	}
	if db != nil {
		t.Error("handle is non-nil under a cancelled context; want nil")
	}
}

// TestOpenChatDBWithRetryBacksOff proves the delay grows but stops growing at the
// ceiling: a persistently unreadable database must not become a hot loop, and
// must not drift into sleeps long enough to ignore a restored grant.
func TestOpenChatDBWithRetryBacksOff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	attempts := 0
	_, _ = openChatDBWithRetry(ctx, filepath.Join(t.TempDir(), "missing.db"),
		5*time.Millisecond, 10*time.Millisecond,
		func(error) { attempts++ }, nil)

	// Capped at 10ms, 200ms affords ~18 attempts. Uncapped doubling
	// (5, 10, 20, 40, 80, 160…) affords only ~6, so this threshold separates
	// the two behaviours with a wide margin.
	if attempts < 10 {
		t.Errorf("attempts = %d in 200ms with a 10ms ceiling, want >= 10; backoff is not capped", attempts)
	}
}
