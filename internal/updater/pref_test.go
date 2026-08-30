package updater

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAutoCheckPref_DefaultsOn(t *testing.T) {
	// Missing file → default ON (opt-out model).
	missing := filepath.Join(t.TempDir(), "AUK", "updater.json")
	if v, present := readAutoCheck(missing); present || !v {
		t.Errorf("readAutoCheck(missing) = (%v, present=%v), want (true, false)", v, present)
	}
}

func TestAutoCheckPref_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AUK", "updater.json")

	if err := writeAutoCheck(path, false); err != nil {
		t.Fatalf("writeAutoCheck(false): %v", err)
	}
	v, present := readAutoCheck(path)
	if !present || v != false {
		t.Errorf("after writing false: readAutoCheck = (%v, present=%v), want (false, true)", v, present)
	}

	if err := writeAutoCheck(path, true); err != nil {
		t.Fatalf("writeAutoCheck(true): %v", err)
	}
	v, present = readAutoCheck(path)
	if !present || v != true {
		t.Errorf("after writing true: readAutoCheck = (%v, present=%v), want (true, true)", v, present)
	}
}

func TestAutoCheckPref_MalformedDefaultsOn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "updater.json")
	if err := os.WriteFile(path, []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if v, present := readAutoCheck(path); present || !v {
		t.Errorf("malformed file: readAutoCheck = (%v, present=%v), want (true, false)", v, present)
	}
}
