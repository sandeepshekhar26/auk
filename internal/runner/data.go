package runner

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"apitool/internal/core/model"
)

// bomUTF8 is the UTF-8 byte-order mark Excel prepends when it saves a CSV.
const bomUTF8 = "\xef\xbb\xbf"

// Row is one iteration's worth of data-file variables, in a stable order.
type Row struct {
	// Index is the 1-based iteration number this row drives.
	Index int
	// Values are the row's columns as enabled variables, ready to be
	// layered onto the environment.
	Values []model.KeyValue
}

// RowReader streams a data file one row at a time. Next returns io.EOF when
// the file is exhausted — a 10k-row CSV is never held in memory, only the
// row currently being executed.
type RowReader interface {
	Next() ([]model.KeyValue, error)
	Close() error
}

// OpenData opens path as a data file. The format is chosen by extension
// (.csv / .tsv / .json), falling back to sniffing the first non-whitespace
// byte ('[' or '{' means JSON) so an extension-less fixture still works.
func OpenData(path string) (RowReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open data file: %w", err)
	}

	br := bufio.NewReader(f)
	// A UTF-8 BOM (Excel loves emitting one) would otherwise become part of
	// the first column's NAME, so `${id}` would silently resolve to nothing.
	if bom, err := br.Peek(3); err == nil && string(bom) == bomUTF8 {
		_, _ = br.Discard(3)
	}

	format := strings.ToLower(filepath.Ext(path))
	if format != ".csv" && format != ".tsv" && format != ".json" {
		format = sniff(br)
	}

	switch format {
	case ".json":
		r, err := newJSONRows(f, br, path)
		if err != nil {
			f.Close()
			return nil, err
		}
		return r, nil
	case ".tsv":
		return newCSVRows(f, br, path, '\t')
	default:
		return newCSVRows(f, br, path, ',')
	}
}

// sniff peeks at the first non-whitespace byte without consuming it.
func sniff(br *bufio.Reader) string {
	head, _ := br.Peek(512)
	for _, b := range head {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '[', '{':
			return ".json"
		default:
			return ".csv"
		}
	}
	return ".csv"
}

// csvRows streams a CSV/TSV file: the first record is the header, every
// later record is one iteration. encoding/csv handles quoted fields
// containing commas, embedded newlines, and escaped quotes.
type csvRows struct {
	file   *os.File
	reader *csv.Reader
	header []string
	path   string
}

func newCSVRows(f *os.File, br *bufio.Reader, path string, comma rune) (*csvRows, error) {
	r := csv.NewReader(br)
	r.Comma = comma
	// Keep the default strict field count: a row with the wrong number of
	// columns is a broken fixture, and silently padding it would produce a
	// green CI run against variables that were never set.
	r.TrimLeadingSpace = true

	header, err := r.Read()
	if err == io.EOF {
		f.Close()
		return nil, fmt.Errorf("data file %s is empty (expected a header row)", path)
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("read header of data file %s: %w", path, err)
	}
	for i := range header {
		header[i] = strings.TrimSpace(strings.TrimPrefix(header[i], bomUTF8))
	}
	for _, name := range header {
		if name == "" {
			f.Close()
			return nil, fmt.Errorf("data file %s has an empty column name in its header row", path)
		}
	}
	return &csvRows{file: f, reader: r, header: header, path: path}, nil
}

func (c *csvRows) Next() ([]model.KeyValue, error) {
	rec, err := c.reader.Read()
	if err == io.EOF {
		return nil, io.EOF
	}
	if err != nil {
		return nil, fmt.Errorf("read data file %s: %w", c.path, err)
	}
	out := make([]model.KeyValue, 0, len(rec))
	for i, v := range rec {
		if i >= len(c.header) {
			break
		}
		out = append(out, model.KeyValue{Key: c.header[i], Value: v, Enabled: true})
	}
	return out, nil
}

func (c *csvRows) Close() error { return c.file.Close() }

// jsonRows streams a JSON array of objects with encoding/json's token
// reader, so only one object is decoded at a time.
type jsonRows struct {
	file *os.File
	dec  *json.Decoder
	path string
	// keys is the sorted key set of the FIRST object, which every later object
	// must match exactly. A row missing a key another row has would leave that
	// ${variable} unset for the iteration — the same "green build against a
	// variable that was never set" the CSV path refuses ragged rows to prevent.
	keys []string
	row  int
}

func newJSONRows(f *os.File, br *bufio.Reader, path string) (*jsonRows, error) {
	dec := json.NewDecoder(br)
	// Numbers stay as their literal text: an id of 10000000000000000001 or
	// a version of "3.0" must reach the request unchanged, not round-tripped
	// through float64.
	dec.UseNumber()

	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("parse data file %s: %w", path, err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return nil, fmt.Errorf("data file %s must be a JSON array of objects (got %v)", path, tok)
	}
	return &jsonRows{file: f, dec: dec, path: path}, nil
}

func (j *jsonRows) Next() ([]model.KeyValue, error) {
	if !j.dec.More() {
		return nil, io.EOF
	}
	var obj map[string]any
	if err := j.dec.Decode(&obj); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return nil, fmt.Errorf("data file %s: every element must be a JSON object (got %s)", j.path, typeErr.Value)
		}
		return nil, fmt.Errorf("parse data file %s: %w", j.path, err)
	}

	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	if j.row == 0 {
		j.keys = keys
	} else if !sameStrings(j.keys, keys) {
		return nil, fmt.Errorf(
			"data file %s: row %d has keys %v but row 1 has %v — every object must have the same keys, or an iteration would run against an unset variable",
			j.path, j.row+1, keys, j.keys)
	}
	j.row++

	out := make([]model.KeyValue, 0, len(keys))
	for _, k := range keys {
		out = append(out, model.KeyValue{Key: k, Value: jsonScalar(obj[k]), Enabled: true})
	}
	return out, nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (j *jsonRows) Close() error { return j.file.Close() }

// jsonScalar flattens a JSON value to the string a ${variable} expands to:
// strings verbatim, numbers as written, booleans as true/false, null as
// empty, and objects/arrays as compact JSON (so a request body can embed a
// nested fixture with ${payload}).
func jsonScalar(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return string(b)
	}
}

// rowSource unifies the two iteration modes behind one cursor: a data file
// (one iteration per row) and a plain repeat count (--iterations with no
// data file).
type rowSource struct {
	reader  RowReader
	limit   int // max iterations; 0 = unlimited (data file) / handled by repeats
	repeats int // iterations for a non-data run
	emitted int
}

func openRows(opts Options) (*rowSource, error) {
	if opts.DataFile == "" {
		repeats := opts.Iterations
		if repeats < 1 {
			repeats = 1
		}
		return &rowSource{repeats: repeats}, nil
	}
	r, err := OpenData(opts.DataFile)
	if err != nil {
		return nil, err
	}
	return &rowSource{reader: r, limit: opts.Iterations}, nil
}

func (s *rowSource) dataDriven() bool { return s != nil && s.reader != nil }

// next yields the next iteration, or ok=false when the run is complete.
func (s *rowSource) next() (Row, bool, error) {
	if s.reader == nil {
		if s.emitted >= s.repeats {
			return Row{}, false, nil
		}
		s.emitted++
		return Row{Index: s.emitted}, true, nil
	}
	if s.limit > 0 && s.emitted >= s.limit {
		return Row{}, false, nil
	}
	values, err := s.reader.Next()
	if err == io.EOF {
		return Row{}, false, nil
	}
	if err != nil {
		return Row{}, false, err
	}
	s.emitted++
	return Row{Index: s.emitted, Values: values}, true, nil
}

func (s *rowSource) Close() error {
	if s.reader != nil {
		return s.reader.Close()
	}
	return nil
}
