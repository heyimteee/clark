package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	if _, err := loadState(path); err != nil {
		t.Fatalf("missing file should load as zero state: %v", err)
	}

	if err := (state{LastRowID: 42}).save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := loadState(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.LastRowID != 42 {
		t.Errorf("LastRowID = %d, want 42", got.LastRowID)
	}
}

func TestStateSaveCreatesDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "state.json")
	if err := (state{LastRowID: 7}).save(path); err != nil {
		t.Fatalf("save with missing dirs: %v", err)
	}
	got, err := loadState(path)
	if err != nil || got.LastRowID != 7 {
		t.Fatalf("load = %+v/%v, want LastRowID 7", got, err)
	}
}

func TestStateCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadState(path); err == nil {
		t.Fatal("corrupt state file should error")
	}
}

func TestStateAtomicWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := (state{LastRowID: 1}).save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file should be removed after rename, stat err = %v", err)
	}
}
