package runner

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"apitool/internal/core/model"
)

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// readAll drains a RowReader into a comparable [][]KeyValue.
func readAll(t *testing.T, path string) [][]model.KeyValue {
	t.Helper()
	r, err := OpenData(path)
	if err != nil {
		t.Fatalf("OpenData(%s) error = %v", path, err)
	}
	defer r.Close()

	var rows [][]model.KeyValue
	for {
		row, err := r.Next()
		if err == io.EOF {
			return rows
		}
		if err != nil {
			t.Fatalf("Next() error = %v", err)
		}
		rows = append(rows, row)
	}
}

func lookup(row []model.KeyValue, key string) (string, bool) {
	for _, kv := range row {
		if kv.Key == key {
			return kv.Value, true
		}
	}
	return "", false
}

func TestCSVQuotedFieldsCommasAndBOM(t *testing.T) {
	// Leading UTF-8 BOM (what Excel writes), a quoted field containing a
	// comma, an escaped quote, and an embedded newline.
	csv := "\xef\xbb\xbfname,note,qty\r\n" +
		"widget,\"red, large\",2\r\n" +
		"gadget,\"he said \"\"hi\"\"\",3\r\n" +
		"thing,\"line one\nline two\",4\r\n"
	rows := readAll(t, writeFile(t, "data.csv", csv))

	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	// The BOM must not end up in the first column's NAME, or ${name} would
	// silently resolve to nothing.
	if _, ok := lookup(rows[0], "name"); !ok {
		t.Fatalf("first column name = %q, want %q (BOM not stripped)", rows[0][0].Key, "name")
	}
	if got, _ := lookup(rows[0], "note"); got != "red, large" {
		t.Errorf("quoted field with comma = %q, want %q", got, "red, large")
	}
	if got, _ := lookup(rows[1], "note"); got != `he said "hi"` {
		t.Errorf("escaped quotes = %q", got)
	}
	if got, _ := lookup(rows[2], "note"); got != "line one\nline two" {
		t.Errorf("embedded newline = %q", got)
	}
	for _, kv := range rows[0] {
		if !kv.Enabled {
			t.Errorf("data column %q is not Enabled — templating would ignore it", kv.Key)
		}
	}
}

func TestCSVHeaderWhitespaceTrimmed(t *testing.T) {
	rows := readAll(t, writeFile(t, "data.csv", "  id , name \n1,ana\n"))
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if _, ok := lookup(rows[0], "id"); !ok {
		t.Errorf("header names not trimmed: %+v", rows[0])
	}
	if _, ok := lookup(rows[0], "name"); !ok {
		t.Errorf("header names not trimmed: %+v", rows[0])
	}
}

func TestCSVHeaderOnlyYieldsNoRows(t *testing.T) {
	if rows := readAll(t, writeFile(t, "data.csv", "id,name\n")); len(rows) != 0 {
		t.Fatalf("rows = %d, want 0", len(rows))
	}
}

func TestDataFileErrors(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		content string
		want    string
	}{
		{"empty csv", "data.csv", "", "is empty"},
		{"empty header name", "data.csv", "id,,name\n1,x,y\n", "empty column name"},
		{"json object, not array", "data.json", `{"id":1}`, "must be a JSON array"},
		{"malformed json", "data.json", `[{"id":1},`, "parse data file"},
		{"json array of scalars", "data.json", `["a","b"]`, "must be a JSON object"},
		{"missing file", "", "", "open data file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "missing.csv")
			if tc.file != "" {
				path = writeFile(t, tc.file, tc.content)
			}
			r, err := OpenData(path)
			if err == nil {
				// Some failures only surface while streaming rows (a
				// truncated array is well-formed until it isn't).
				for err == nil {
					_, err = r.Next()
				}
				r.Close()
				if err == io.EOF {
					err = nil
				}
			}
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestCSVWrongFieldCountIsAnError: silently padding a short row would run a
// green CI build against variables that were never set.
func TestCSVWrongFieldCountIsAnError(t *testing.T) {
	path := writeFile(t, "data.csv", "id,name\n1,ana\n2\n")
	r, err := OpenData(path)
	if err != nil {
		t.Fatalf("OpenData() error = %v", err)
	}
	defer r.Close()

	if _, err := r.Next(); err != nil {
		t.Fatalf("first row error = %v", err)
	}
	_, err = r.Next()
	if err == nil || err == io.EOF {
		t.Fatalf("second row error = %v, want a field-count error", err)
	}
	if !strings.Contains(err.Error(), "data.csv") {
		t.Errorf("error %q should name the offending file", err)
	}
}

func TestJSONArrayOfObjects(t *testing.T) {
	json := `[
	  {"id": 1, "name": "ana", "admin": true, "nickname": null, "score": 3.50, "big": 10000000000000000001, "tags": ["a","b"], "meta": {"k": "v"}},
	  {"id": 2, "name": "bo", "admin": false, "nickname": "bee", "score": 4, "big": 2, "tags": [], "meta": {}}
	]`
	rows := readAll(t, writeFile(t, "data.json", json))
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}

	checks := map[string]string{
		"id":       "1",
		"name":     "ana",
		"admin":    "true",
		"nickname": "",
		// Numbers keep their literal text — no float64 round-trip.
		"score": "3.50",
		"big":   "10000000000000000001",
		"tags":  `["a","b"]`,
		"meta":  `{"k":"v"}`,
	}
	for key, want := range checks {
		got, ok := lookup(rows[0], key)
		if !ok {
			t.Errorf("column %q missing", key)
			continue
		}
		if got != want {
			t.Errorf("column %q = %q, want %q", key, got, want)
		}
	}
}

// TestDataFormatSniffing: an extension-less (or oddly-named) fixture still
// works — the first non-space byte decides.
func TestDataFormatSniffing(t *testing.T) {
	rows := readAll(t, writeFile(t, "rows.txt", "\n  [{\"id\": 7}]"))
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if got, _ := lookup(rows[0], "id"); got != "7" {
		t.Errorf("id = %q, want 7", got)
	}

	rows = readAll(t, writeFile(t, "rows.dat", "id\n9\n"))
	if got, _ := lookup(rows[0], "id"); got != "9" {
		t.Errorf("csv fallback id = %q, want 9", got)
	}
}

func TestTSV(t *testing.T) {
	rows := readAll(t, writeFile(t, "data.tsv", "id\tname\n1\tana, of course\n"))
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if got, _ := lookup(rows[0], "name"); got != "ana, of course" {
		t.Errorf("name = %q", got)
	}
}

func TestRowSourceIterationModes(t *testing.T) {
	count := func(s *rowSource) int {
		n := 0
		for {
			_, ok, err := s.next()
			if err != nil {
				t.Fatalf("next() error = %v", err)
			}
			if !ok {
				return n
			}
			n++
		}
	}

	t.Run("no data file defaults to one pass", func(t *testing.T) {
		s, err := openRows(Options{})
		if err != nil {
			t.Fatal(err)
		}
		if got := count(s); got != 1 {
			t.Fatalf("iterations = %d, want 1", got)
		}
		if s.dataDriven() {
			t.Error("dataDriven() = true with no data file")
		}
	})

	t.Run("iterations repeats without a data file", func(t *testing.T) {
		s, _ := openRows(Options{Iterations: 4})
		if got := count(s); got != 4 {
			t.Fatalf("iterations = %d, want 4", got)
		}
	})

	t.Run("data file drives one iteration per row", func(t *testing.T) {
		path := writeFile(t, "d.csv", "id\n1\n2\n3\n")
		s, err := openRows(Options{DataFile: path})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if got := count(s); got != 3 {
			t.Fatalf("iterations = %d, want 3 (one per row)", got)
		}
	})

	t.Run("iterations caps the rows consumed", func(t *testing.T) {
		path := writeFile(t, "d.csv", "id\n1\n2\n3\n4\n5\n")
		s, err := openRows(Options{DataFile: path, Iterations: 2})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if got := count(s); got != 2 {
			t.Fatalf("iterations = %d, want 2", got)
		}
	})

	t.Run("iterations larger than the file stops at the file", func(t *testing.T) {
		path := writeFile(t, "d.csv", "id\n1\n2\n")
		s, err := openRows(Options{DataFile: path, Iterations: 10})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if got := count(s); got != 2 {
			t.Fatalf("iterations = %d, want 2", got)
		}
	})

	t.Run("row index is 1-based and increments", func(t *testing.T) {
		path := writeFile(t, "d.csv", "id\n7\n8\n")
		s, _ := openRows(Options{DataFile: path})
		defer s.Close()
		first, _, _ := s.next()
		second, _, _ := s.next()
		if first.Index != 1 || second.Index != 2 {
			t.Fatalf("indexes = %d,%d want 1,2", first.Index, second.Index)
		}
		if got, _ := lookup(second.Values, "id"); got != "8" {
			t.Errorf("second row id = %q, want 8", got)
		}
	})
}

// TestJSONRowsRejectInconsistentKeys guards the false-green: a JSON data file
// whose objects have differing key sets would silently run an iteration
// against an unset variable. Like the CSV path's strict field count, it must
// be a hard error.
func TestJSONRowsRejectInconsistentKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte(`[{"user":"ada","plan":"pro"},{"user":"grace"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, err := OpenData(path)
	if err != nil {
		t.Fatalf("openRows: %v", err)
	}
	defer rows.Close()
	if _, err := rows.Next(); err != nil {
		t.Fatalf("first row should parse: %v", err)
	}
	_, err = rows.Next()
	if err == nil {
		t.Fatal("a row with a different key set must be a hard error, not a silent unset variable")
	}
	if !strings.Contains(err.Error(), "same keys") {
		t.Errorf("error should explain the key mismatch, got %q", err.Error())
	}
}

// Consistent keys across rows are fine.
func TestJSONRowsAcceptConsistentKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.json")
	if err := os.WriteFile(path, []byte(`[{"user":"ada","plan":"pro"},{"user":"grace","plan":"free"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, err := OpenData(path)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for i := 0; i < 2; i++ {
		if _, err := rows.Next(); err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
	}
}
