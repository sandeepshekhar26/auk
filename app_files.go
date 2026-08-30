package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"apitool/internal/core/model"
)

// app_files.go holds the file-export bindings that write a single response
// body to disk. It mirrors ExportWorkspace's SaveFileDialog pattern in app.go
// but is kept in its own file so the response-viewer feature can evolve
// without touching the main App surface.
//
// Everything that can be exercised without popping a native panel — the
// decode-and-write, the Content-Type → extension guess, and the default
// filename — is factored into pure helpers with unit tests in
// app_files_test.go. SaveResponseBody itself is the thin, untestable shell
// around wailsruntime.SaveFileDialog (which opens a real macOS panel and
// can't be driven headlessly).

// SaveResponseBody writes a response body to a file the user picks in a
// native save dialog, and returns the saved path (or "" with no error if the
// user cancels — matching ExportWorkspace's contract).
//
// The body and its Content-Type are passed in by the caller rather than read
// back from the store's last-response cache, because those two can legitimately
// disagree: a folder run or an MCP-driven call overwrites the cache for the
// same request id WITHOUT touching what the response pane is showing, so a
// cache-based save could silently write bytes the user never saw. Saving what
// the viewer holds keeps the file and the screen in agreement.
//
// requestID is used only to derive a friendly default filename.
func (a *App) SaveResponseBody(requestID, bodyBase64, contentType string) (string, error) {
	if bodyBase64 == "" {
		return "", fmt.Errorf("response body is empty — nothing to save")
	}

	reqName := ""
	if req, err := a.store.GetRequest(requestID); err == nil {
		reqName = req.Name
	}

	path, err := wailsruntime.SaveFileDialog(a.ctx, wailsruntime.SaveDialogOptions{
		Title:           "Save Response Body",
		DefaultFilename: defaultResponseFilename(reqName, contentType),
		Filters:         responseSaveFilters(contentType),
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil // user cancelled the dialog
	}
	if err := writeDecodedBody(path, bodyBase64); err != nil {
		return "", err
	}
	return path, nil
}

// writeDecodedBody base64-decodes body (StdEncoding, matching how every
// protocol encodes ResponseData.BodyBase64) and writes the raw bytes to path.
// This is the whole non-dialog half of SaveResponseBody, split out so it can
// be unit-tested against a temp file.
func writeDecodedBody(path, body string) error {
	decoded, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return fmt.Errorf("decode response body: %w", err)
	}
	if err := os.WriteFile(path, decoded, 0o644); err != nil {
		return fmt.Errorf("write response file: %w", err)
	}
	return nil
}

// defaultResponseFilename builds the save dialog's suggested name from the
// request name (sanitized to a safe basename) and an extension guessed from
// the response Content-Type. A name that already ends in the guessed
// extension isn't doubled up.
func defaultResponseFilename(requestName, contentType string) string {
	base := sanitizeBaseName(requestName)
	if base == "" {
		base = "response"
	}
	ext := guessExtension(contentType)
	if strings.HasSuffix(strings.ToLower(base), ext) {
		return base
	}
	return base + ext
}

// guessExtension maps a response Content-Type to a file extension. Parameters
// (";charset=…") are ignored, and structured-suffix types (application/…+json,
// …+xml) fall back to the base family. Unknown types default to .txt.
func guessExtension(contentType string) string {
	mime := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = strings.TrimSpace(mime[:i])
	}
	switch mime {
	case "application/json":
		return ".json"
	case "text/html", "application/xhtml+xml":
		return ".html"
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "application/xml", "text/xml":
		return ".xml"
	case "text/csv":
		return ".csv"
	case "application/pdf":
		return ".pdf"
	case "application/javascript", "text/javascript":
		return ".js"
	case "text/css":
		return ".css"
	case "text/plain":
		return ".txt"
	}
	if strings.HasSuffix(mime, "+json") {
		return ".json"
	}
	if strings.HasSuffix(mime, "+xml") {
		return ".xml"
	}
	return ".txt"
}

// responseSaveFilters offers the guessed type first (so the panel defaults to
// it) with an all-files escape hatch.
func responseSaveFilters(contentType string) []wailsruntime.FileFilter {
	ext := strings.TrimPrefix(guessExtension(contentType), ".")
	return []wailsruntime.FileFilter{
		{DisplayName: strings.ToUpper(ext) + " File", Pattern: "*." + ext},
		{DisplayName: "All Files", Pattern: "*.*"},
	}
}

// sanitizeBaseName reduces a request name to a lowercase, filesystem-safe
// basename: alphanumerics kept, spaces/underscores collapsed to '-', dots
// kept (for extensions), everything else (including path separators) dropped.
func sanitizeBaseName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-.")
}

// headerValue does a case-insensitive lookup of the first matching header
// value (HTTP header names are case-insensitive), returning "" if absent.
func headerValue(headers []model.KeyValue, name string) string {
	for _, h := range headers {
		if strings.EqualFold(h.Key, name) {
			return h.Value
		}
	}
	return ""
}
