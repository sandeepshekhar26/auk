package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"apitool/internal/core/model"
)

func TestGuessExtension(t *testing.T) {
	cases := map[string]string{
		"application/json":                ".json",
		"application/json; charset=utf-8": ".json",
		"APPLICATION/JSON":                ".json", // case-insensitive
		"application/vnd.api+json":        ".json", // structured suffix
		"text/html":                       ".html",
		"text/html;charset=UTF-8":         ".html",
		"application/xhtml+xml":           ".html",
		"image/png":                       ".png",
		"image/jpeg":                      ".jpg",
		"image/gif":                       ".gif",
		"image/webp":                      ".webp",
		"image/svg+xml":                   ".svg",
		"application/xml":                 ".xml",
		"text/xml":                        ".xml",
		"application/atom+xml":            ".xml", // structured suffix
		"text/csv":                        ".csv",
		"application/pdf":                 ".pdf",
		"text/plain":                      ".txt",
		"application/octet-stream":        ".txt",  // unknown → default
		"":                                ".txt",  // missing → default
		"  text/html ; boundary=x  ":      ".html", // trimmed + param-stripped
	}
	for ct, want := range cases {
		if got := guessExtension(ct); got != want {
			t.Errorf("guessExtension(%q) = %q, want %q", ct, got, want)
		}
	}
}

func TestDefaultResponseFilename(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		want        string
	}{
		{"Get User", "application/json", "get-user.json"},
		{"List Photos", "image/png", "list-photos.png"},
		{"", "application/json", "response.json"},                  // empty name → response
		{"   ", "text/plain", "response.txt"},                      // whitespace-only → response
		{"GET /users/:id", "application/json", "get-usersid.json"}, // slashes/colons dropped
		{"weird!!name@@", "text/html", "weirdname.html"},           // punctuation dropped
		{"report.json", "application/json", "report.json"},         // extension not doubled
		{"Data.JSON", "application/json", "data.json"},             // lowercased, ext not doubled
		{"snapshot", "application/octet-stream", "snapshot.txt"},   // unknown type → .txt
	}
	for _, c := range cases {
		if got := defaultResponseFilename(c.name, c.contentType); got != c.want {
			t.Errorf("defaultResponseFilename(%q, %q) = %q, want %q", c.name, c.contentType, got, c.want)
		}
	}
}

func TestSanitizeBaseName(t *testing.T) {
	cases := map[string]string{
		"Get User":        "get-user",
		"  padded  ":      "padded",
		"a/b\\c":          "abc",
		"--leading--":     "leading",
		"UPPER_case Name": "upper-case-name",
		"!!!":             "",
	}
	for in, want := range cases {
		if got := sanitizeBaseName(in); got != want {
			t.Errorf("sanitizeBaseName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteDecodedBody(t *testing.T) {
	dir := t.TempDir()

	t.Run("round trip", func(t *testing.T) {
		want := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0xff} // PNG-ish binary
		encoded := base64.StdEncoding.EncodeToString(want)
		path := filepath.Join(dir, "out.png")
		if err := writeDecodedBody(path, encoded); err != nil {
			t.Fatalf("writeDecodedBody: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if string(got) != string(want) {
			t.Errorf("bytes round-tripped wrong: got %v want %v", got, want)
		}
	})

	t.Run("invalid base64", func(t *testing.T) {
		path := filepath.Join(dir, "bad.bin")
		if err := writeDecodedBody(path, "not valid base64!!!"); err == nil {
			t.Error("expected an error decoding invalid base64, got nil")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Error("no file should be written when the body fails to decode")
		}
	})
}

func TestHeaderValue(t *testing.T) {
	headers := []model.KeyValue{
		{Key: "Content-Type", Value: "application/json"},
		{Key: "X-Custom", Value: "abc"},
	}
	if got := headerValue(headers, "content-type"); got != "application/json" {
		t.Errorf("case-insensitive lookup = %q, want application/json", got)
	}
	if got := headerValue(headers, "CONTENT-TYPE"); got != "application/json" {
		t.Errorf("upper lookup = %q, want application/json", got)
	}
	if got := headerValue(headers, "Missing"); got != "" {
		t.Errorf("missing header = %q, want empty", got)
	}
	if got := headerValue(nil, "Content-Type"); got != "" {
		t.Errorf("nil headers = %q, want empty", got)
	}
}
