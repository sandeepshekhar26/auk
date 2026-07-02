package templating

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestEncodeURL(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{name: "happy path", args: []string{"a b&c"}, want: "a+b%26c"},
		{name: "missing arg", args: nil, wantErr: true},
	}
	e := New(nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.funcs["encode.url"](tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestJSONGet(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{
			name: "happy path nested field",
			args: []string{`{"a":{"b":[1,2,{"c":"hello"}]}}`, "a.b[2].c"},
			want: "hello",
		},
		{
			name: "top level array index",
			args: []string{`[10,20,30]`, "[1]"},
			want: "20",
		},
		{
			name:    "missing field",
			args:    []string{`{"a":1}`, "b"},
			wantErr: true,
		},
		{
			name:    "invalid json",
			args:    []string{`not json`, "a"},
			wantErr: true,
		},
	}
	e := New(nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.funcs["json.get"](tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRegexMatch(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{name: "happy path", args: []string{"order-4821", `\d+`}, want: "4821"},
		{name: "no match", args: []string{"no digits here", `\d+`}, wantErr: true},
		{name: "invalid pattern", args: []string{"abc", `(unclosed`}, wantErr: true},
	}
	e := New(nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.funcs["regex.match"](tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRegexReplace(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{name: "happy path", args: []string{"foo123bar", `\d+`, "X"}, want: "fooXbar"},
		{name: "invalid pattern", args: []string{"abc", `(unclosed`, "X"}, wantErr: true},
		{name: "missing args", args: []string{"abc"}, wantErr: true},
	}
	e := New(nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.funcs["regex.replace"](tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTimestampOffset(t *testing.T) {
	base := int64(1_700_000_000)
	cases := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{
			name: "happy path plus one hour",
			args: []string{strconv.FormatInt(base, 10), "+1h"},
			want: strconv.FormatInt(base+3600, 10),
		},
		{
			name: "minus thirty minutes",
			args: []string{strconv.FormatInt(base, 10), "-30m"},
			want: strconv.FormatInt(base-1800, 10),
		},
		{
			name:    "invalid offset spec",
			args:    []string{strconv.FormatInt(base, 10), "not-a-duration"},
			wantErr: true,
		},
		{
			name:    "invalid unix seconds",
			args:    []string{"not-a-number", "+1h"},
			wantErr: true,
		},
	}
	e := New(nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.funcs["timestamp.offset"](tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTimestampFormat(t *testing.T) {
	e := New(nil)
	t.Run("happy path", func(t *testing.T) {
		got, err := e.funcs["timestamp.format"]([]string{"0", "2006-01-02T15:04:05Z"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := time.Unix(0, 0).UTC().Format("2006-01-02T15:04:05Z")
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
	t.Run("invalid unix seconds", func(t *testing.T) {
		_, err := e.funcs["timestamp.format"]([]string{"not-a-number", "2006-01-02"})
		if err == nil {
			t.Fatalf("expected error, got none")
		}
	})
}

func TestFsRead(t *testing.T) {
	e := New(nil)
	t.Run("happy path", func(t *testing.T) {
		dir := t.TempDir()
		path := dir + "/hello.txt"
		if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
			t.Fatalf("setup failed: %v", err)
		}
		got, err := e.funcs["fs.read"]([]string{path})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "hello world" {
			t.Fatalf("got %q, want %q", got, "hello world")
		}
	})
	t.Run("missing file", func(t *testing.T) {
		_, err := e.funcs["fs.read"]([]string{"/nonexistent/path/does-not-exist.txt"})
		if err == nil {
			t.Fatalf("expected error, got none")
		}
	})
}

func TestCookie(t *testing.T) {
	e := New(nil)
	t.Run("not wired yet", func(t *testing.T) {
		_, err := e.funcs["cookie"]([]string{"session"})
		if err == nil {
			t.Fatalf("expected error, got none")
		}
		if !strings.Contains(err.Error(), "not wired yet") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})
}

func TestPrompt(t *testing.T) {
	e := New(nil)
	t.Run("not supported headlessly", func(t *testing.T) {
		_, err := e.funcs["prompt"]([]string{"Enter value:"})
		if err == nil {
			t.Fatalf("expected error, got none")
		}
		if !strings.Contains(err.Error(), "interactive UI") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})
}
