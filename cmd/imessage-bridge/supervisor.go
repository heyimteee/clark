package main

import (
	"context"
	"database/sql"
	"time"
)

// chat.db is unreadable whenever Full Disk Access is missing, and macOS gives no
// API to request it. Recovering therefore means waiting for a human to act in
// System Settings — so the bridge must keep waiting rather than exit, otherwise
// restoring the grant silently does nothing until someone reloads the launchd
// agent by hand.
const (
	// dbOpenInitialBackoff is the first retry delay after a failed open.
	dbOpenInitialBackoff = 2 * time.Second
	// dbOpenMaxBackoff caps the exponential growth so a long-lived denial does
	// not back off to hours and stay unresponsive to a restored grant.
	dbOpenMaxBackoff = 2 * time.Minute
)

// openChatDBWithRetry retries opening the Messages database until it succeeds or
// ctx ends. onFail runs once per failed attempt, onSuccess once when the handle
// opens. Returns the live handle, or nil with ctx.Err() when cancelled first.
//
// A real chat.db can fail to open for reasons that persist (no such file, a
// corrupt sidecar), not just a missing grant — the ceiling on the backoff is
// what keeps that from becoming a hot loop.
func openChatDBWithRetry(ctx context.Context, path string, initial, max time.Duration, onFail func(error), onSuccess func()) (*sql.DB, error) {
	backoff := initial
	for {
		db, err := openChatDB(path)
		if err == nil {
			if onSuccess != nil {
				onSuccess()
			}
			return db, nil
		}
		if onFail != nil {
			onFail(err)
		}

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}

		if backoff < max {
			backoff *= 2
			if backoff > max {
				backoff = max
			}
		}
	}
}
