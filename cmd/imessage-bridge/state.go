package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// state is the bridge's persisted watermark: the highest chat.db ROWID that
// has been forwarded to clark. Persisted so a bridge restart never re-sends or
// misses messages.
type state struct {
	LastRowID int64 `json:"last_row_id"`
}

// loadState reads the watermark file. A missing file yields a zero state,
// which signals the watcher to bootstrap to the current max ROWID (no replay).
func loadState(path string) (state, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return state{}, nil
		}
		return state{}, fmt.Errorf("fail to read state file: %w", err)
	}
	var s state
	if err := json.Unmarshal(raw, &s); err != nil {
		return state{}, fmt.Errorf("fail to parse state file: %w", err)
	}
	return s, nil
}

// save atomically persists the watermark (temp file + rename) so a crash can
// never leave a half-written state file.
func (s state) save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("fail to create state dir: %w", err)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("fail to marshal state: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("fail to write state file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("fail to move state file: %w", err)
	}
	return nil
}
