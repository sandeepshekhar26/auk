package storage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"apitool/internal/core/model"
)

// appendHistoryLine appends one JSON-encoded HistoryEntry as a line to path,
// creating the file (and its parent directory) if needed. JSON-lines (not a
// single JSON array) so an append is O(1) and a torn write only ever
// corrupts the last line, never the whole file.
func appendHistoryLine(path string, h model.HistoryEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	line, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("marshal history entry: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// readHistoryLines reads every entry back in file order (oldest first). A
// missing file just means "no history yet", not an error. A line that
// fails to parse (e.g. truncated by a crash mid-write) is skipped rather
// than failing the whole read, since history is a debugging aid, not
// source-of-truth data.
func readHistoryLines(path string) ([]model.HistoryEntry, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var out []model.HistoryEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var h model.HistoryEntry
		if err := json.Unmarshal(line, &h); err != nil {
			continue
		}
		out = append(out, h)
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("scan %s: %w", path, err)
	}
	return out, nil
}
