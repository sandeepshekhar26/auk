package storage

import (
	"os"
	"path/filepath"
	"testing"

	"apitool/internal/core/model"
)

func TestReadHistoryLines_MissingFile(t *testing.T) {
	got, err := readHistoryLines(filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	if err != nil {
		t.Fatalf("readHistoryLines on missing file: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing file, got %+v", got)
	}
}

func TestAppendAndReadHistoryLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")

	entries := []model.HistoryEntry{
		{ID: "h1", RequestID: "r1", RequestName: "first", Method: "GET", URL: "https://a", Status: 200, Timestamp: "2026-01-01T00:00:00Z"},
		{ID: "h2", RequestID: "r2", RequestName: "second", Method: "POST", URL: "https://b", Status: 201, Timestamp: "2026-01-01T00:01:00Z"},
	}
	for _, e := range entries {
		if err := appendHistoryLine(path, e); err != nil {
			t.Fatalf("appendHistoryLine: %v", err)
		}
	}

	got, err := readHistoryLines(path)
	if err != nil {
		t.Fatalf("readHistoryLines: %v", err)
	}
	if len(got) != len(entries) {
		t.Fatalf("got %d entries, want %d", len(got), len(entries))
	}
	for i, want := range entries {
		if got[i] != want {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want)
		}
	}
}

func TestReadHistoryLines_SkipsCorruptedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	if err := appendHistoryLine(path, model.HistoryEntry{ID: "h1", RequestName: "good-one"}); err != nil {
		t.Fatalf("appendHistoryLine: %v", err)
	}
	// Simulate a torn write: append a truncated/invalid JSON line.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for corrupt append: %v", err)
	}
	if _, err := f.WriteString("{\"id\":\"h2\",\"requestNam"); err != nil {
		t.Fatalf("write corrupt line: %v", err)
	}
	f.Close()

	got, err := readHistoryLines(path)
	if err != nil {
		t.Fatalf("readHistoryLines: %v", err)
	}
	if len(got) != 1 || got[0].RequestName != "good-one" {
		t.Errorf("expected only the well-formed entry to survive, got %+v", got)
	}
}
