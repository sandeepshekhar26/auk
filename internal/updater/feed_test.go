package updater

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestParseGitHubRelease(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "github_release_latest.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	rel, err := parseGitHubRelease(data, DefaultAssetPrefix, DefaultAssetSuffix)
	if err != nil {
		t.Fatalf("parseGitHubRelease: %v", err)
	}
	if rel.Version != "0.3.0" {
		t.Errorf("Version = %q, want 0.3.0 (leading v stripped)", rel.Version)
	}
	if rel.AssetName != "AUK-0.3.0.dmg" {
		t.Errorf("AssetName = %q, want AUK-0.3.0.dmg", rel.AssetName)
	}
	if rel.URL != "https://github.com/sandeepshekhar26/auk/releases/download/v0.3.0/AUK-0.3.0.dmg" {
		t.Errorf("URL = %q", rel.URL)
	}
	if rel.SizeBytes != 43867463 {
		t.Errorf("SizeBytes = %d, want 43867463", rel.SizeBytes)
	}
	if rel.SHA256 != "e99cee33a3ddfff37c3be66e461dc9384a6f12d66c7a2e837533ff013b03c05e" {
		t.Errorf("SHA256 = %q, not lifted from body", rel.SHA256)
	}
	if rel.Notes == "" {
		t.Error("Notes should carry the release body")
	}
}

func TestParseGitHubRelease_NoMatchingAsset(t *testing.T) {
	// A release whose only asset is a .zip must be rejected (no DMG to install).
	json := `{"tag_name":"v0.3.0","assets":[{"name":"AUK-0.3.0.zip","size":1,"browser_download_url":"http://x/AUK-0.3.0.zip"}]}`
	if _, err := parseGitHubRelease([]byte(json), DefaultAssetPrefix, DefaultAssetSuffix); err == nil {
		t.Error("expected error when no AUK-*.dmg asset is present")
	}
}

func TestParseGitHubRelease_NoTag(t *testing.T) {
	if _, err := parseGitHubRelease([]byte(`{"assets":[]}`), DefaultAssetPrefix, DefaultAssetSuffix); err == nil {
		t.Error("expected error when tag_name is absent")
	}
}

func TestParseSHA256FromBody(t *testing.T) {
	hash := "e99cee33a3ddfff37c3be66e461dc9384a6f12d66c7a2e837533ff013b03c05e"
	cases := []struct {
		name, body, want string
	}{
		{"labelled code fence", "notes\n```\nSHA-256  " + hash + "\n```\n", hash},
		{"no space", "SHA256:" + hash, hash},
		{"sha256 lower", "checksum sha256 " + hash + " done", hash},
		{"shasum output format", hash + "  AUK-0.3.0.dmg", hash},
		{"uppercase label", "SHA-256 " + "E99CEE33A3DDFFF37C3BE66E461DC9384A6F12D66C7A2E837533FF013B03C05E", hash},
		{"absent", "no checksum here", ""},
		{"too short", "SHA-256 abc123", ""},
	}
	for _, c := range cases {
		if got := parseSHA256FromBody(c.body); got != c.want {
			t.Errorf("%s: parseSHA256FromBody = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestGitHubFeed_Latest(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "github_release_latest.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/sandeepshekhar26/auk/releases/latest" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing User-Agent (GitHub rejects those)")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	feed := GitHubFeed{Repo: "sandeepshekhar26/auk", AssetPrefix: DefaultAssetPrefix, AssetSuffix: DefaultAssetSuffix, apiBase: srv.URL}
	rel, err := feed.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if rel.Version != "0.3.0" {
		t.Errorf("Version = %q", rel.Version)
	}
}

func TestGitHubFeed_RateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // GitHub returns 403 on rate-limit
	}))
	defer srv.Close()

	feed := GitHubFeed{Repo: "x/y", AssetPrefix: DefaultAssetPrefix, AssetSuffix: DefaultAssetSuffix, apiBase: srv.URL}
	if _, err := feed.Latest(context.Background()); err == nil {
		t.Error("expected an error on HTTP 403, so Check can fold it into 'unknown'")
	}
}
