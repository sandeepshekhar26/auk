package updater

// The update FEED: where "what is the latest release?" comes from.
//
// Feed is a deliberate abstraction over the source. v1 answers it from the
// public GitHub Releases API (GitHubFeed), which needs no auth and no
// infrastructure of our own. But the app should not be welded to GitHub: a
// future signed appcast (a small JSON/XML document we host and sign, so the
// "what's the latest" answer is itself tamper-evident — see docs/07) can drop
// in as another Feed implementation without touching the version-compare,
// download, verify, or UI code. Everything downstream consumes a Release, not
// a GitHub payload.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// Release is the source-agnostic description of one published release. It is
// the entire contract between a Feed and the rest of the updater.
type Release struct {
	// Version is the release's SemVer WITHOUT a leading "v" ("0.3.0").
	Version string `json:"version"`
	// Notes is the human-readable release body (Markdown), shown behind
	// "What's new".
	Notes string `json:"notes"`
	// URL is the direct download URL of the DMG asset.
	URL string `json:"url"`
	// AssetName is the DMG's file name ("AUK-0.3.0.dmg").
	AssetName string `json:"assetName"`
	// SizeBytes is the DMG's size, for the "Update (~42 MB)" affordance and as
	// a sanity bound on the download.
	SizeBytes int64 `json:"sizeBytes"`
	// SHA256 is the hex digest parsed from the release notes if one is present
	// (see parseSHA256FromBody), else "". Optional: it hardens the download
	// but is NOT the primary trust anchor — the codesign/notarization/Team-ID
	// check in install.go is (docs/07-auto-update.md).
	SHA256 string `json:"sha256"`
}

// Feed resolves the latest available release. Implementations must be safe to
// call on a background launch check: bounded, timeout-driven, and returning a
// plain error (never a panic) on network failure or rate-limiting.
type Feed interface {
	Latest(ctx context.Context) (Release, error)
}

// GitHubFeed reads the latest release from the GitHub REST API. Repo is
// "owner/name"; AssetPrefix + AssetSuffix bracket the DMG asset name
// ("AUK-" ... ".dmg") so the right asset is picked even alongside a .zip or
// checksum file. HTTPClient is optional (defaults applied by the Service).
type GitHubFeed struct {
	Repo        string
	AssetPrefix string
	AssetSuffix string
	HTTPClient  *http.Client
	// apiBase is overridable in tests; defaults to the public API host.
	apiBase string
}

const defaultGitHubAPIBase = "https://api.github.com"

// feedBodyLimit caps how much of the API response we read. Release bodies are
// a few KB; this is generous headroom without letting a hostile/huge response
// exhaust memory during a background check.
const feedBodyLimit = 1 << 20 // 1 MiB

// Latest queries GET /repos/{repo}/releases/latest and maps it to a Release.
// "latest" excludes drafts and pre-releases on GitHub's side, which is exactly
// what a stable-channel updater wants.
func (g GitHubFeed) Latest(ctx context.Context) (Release, error) {
	base := g.apiBase
	if base == "" {
		base = defaultGitHubAPIBase
	}
	url := fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(base, "/"), g.Repo)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, fmt.Errorf("build release request: %w", err)
	}
	// Ask for the documented media type and identify ourselves; GitHub rejects
	// requests with no User-Agent.
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "AUK-Updater")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	client := g.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("contact release feed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		// Unauthenticated GitHub API calls are rate-limited (~60/hr per IP). A
		// throttled background check is not an error the user should ever see —
		// the caller maps this to "unknown", not a crash or a red banner.
		return Release{}, fmt.Errorf("release feed rate-limited (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("release feed returned HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, feedBodyLimit))
	if err != nil {
		return Release{}, fmt.Errorf("read release feed: %w", err)
	}
	return parseGitHubRelease(data, g.AssetPrefix, g.AssetSuffix)
}

// githubRelease is the subset of the GitHub release JSON we consume.
type githubRelease struct {
	TagName    string `json:"tag_name"`
	Name       string `json:"name"`
	Body       string `json:"body"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name        string `json:"name"`
		Size        int64  `json:"size"`
		DownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// parseGitHubRelease turns a GitHub release payload into a Release, selecting
// the DMG asset by prefix/suffix and lifting a SHA-256 out of the body if one
// is published there. Pure and fixture-testable — no network.
func parseGitHubRelease(data []byte, assetPrefix, assetSuffix string) (Release, error) {
	var gr githubRelease
	if err := json.Unmarshal(data, &gr); err != nil {
		return Release{}, fmt.Errorf("parse release JSON: %w", err)
	}
	if gr.TagName == "" {
		return Release{}, fmt.Errorf("release feed has no tag_name")
	}

	rel := Release{
		Version: strings.TrimPrefix(strings.TrimSpace(gr.TagName), "v"),
		Notes:   gr.Body,
		SHA256:  parseSHA256FromBody(gr.Body),
	}

	for _, a := range gr.Assets {
		if strings.HasPrefix(a.Name, assetPrefix) && strings.HasSuffix(a.Name, assetSuffix) {
			rel.URL = a.DownloadURL
			rel.AssetName = a.Name
			rel.SizeBytes = a.Size
			break
		}
	}
	if rel.URL == "" {
		return Release{}, fmt.Errorf("release %s has no %s*%s asset", gr.TagName, assetPrefix, assetSuffix)
	}
	return rel, nil
}

// sha256LineRE matches a published digest such as the
// "SHA-256  e99cee...c05e" line release.sh prints and the human pastes into
// the release notes. It tolerates "SHA-256", "SHA256", or "sha256", an
// optional colon, and arbitrary whitespace before the 64 hex characters.
var sha256LineRE = regexp.MustCompile(`(?i)sha-?256\s*:?\s*([0-9a-f]{64})`)

// bareSHA256RE is the fallback: a lone 64-hex token (e.g. the left column of
// `shasum -a 256` output) when no "SHA-256" label precedes it.
var bareSHA256RE = regexp.MustCompile(`\b([0-9a-f]{64})\b`)

// parseSHA256FromBody extracts a hex SHA-256 from release notes, preferring a
// labelled "SHA-256 <hash>" line and falling back to any bare 64-hex token.
// Returns "" when the notes carry no digest — that is expected and fine; the
// download still gets the codesign/notarization/Team-ID verification, which is
// the real trust anchor.
func parseSHA256FromBody(body string) string {
	if m := sha256LineRE.FindStringSubmatch(body); m != nil {
		return strings.ToLower(m[1])
	}
	if m := bareSHA256RE.FindStringSubmatch(strings.ToLower(body)); m != nil {
		return m[1]
	}
	return ""
}
