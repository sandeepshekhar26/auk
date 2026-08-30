package importer

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"apitool/internal/core/model"
)

// har* mirror the subset of the HTTP Archive (HAR 1.2) format we read. A HAR
// is a recording of real traffic (from Chrome/Firefox DevTools, Charles,
// Proxyman, mitmproxy, …); we import each captured REQUEST faithfully. HAR
// also records the RESPONSE of every entry, but response replay is out of
// scope for the importer (AUK keeps its own last-response cache the mock
// server reads) — see the package README/INTEGRATION NOTES.
type harFile struct {
	Log harLog `json:"log"`
}

type harLog struct {
	Version string     `json:"version"`
	Pages   []harPage  `json:"pages"`
	Entries []harEntry `json:"entries"`
}

type harPage struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type harEntry struct {
	PageRef string     `json:"pageref"`
	Request harRequest `json:"request"`
}

type harRequest struct {
	Method      string       `json:"method"`
	URL         string       `json:"url"`
	Headers     []harNV      `json:"headers"`
	QueryString []harNV      `json:"queryString"`
	Cookies     []harCookie  `json:"cookies"`
	PostData    *harPostData `json:"postData"`
}

type harNV struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type harCookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type harPostData struct {
	MimeType string         `json:"mimeType"`
	Text     string         `json:"text"`
	Params   []harPostParam `json:"params"`
}

type harPostParam struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ParseHAR converts a HAR document into an ImportResult: one RequestDef per
// (deduplicated) captured request, grouped into a folder per HAR "page" when
// the recording carries page metadata. Because a HAR usually hits the same
// URL many times, entries are deduplicated by method+url — the first
// occurrence wins, and its Description notes how many identical hits were
// collapsed.
func ParseHAR(data []byte) (ImportResult, error) {
	var f harFile
	if err := json.Unmarshal(data, &f); err != nil {
		return ImportResult{}, fmt.Errorf("parse HAR: %w", err)
	}
	if len(f.Log.Entries) == 0 {
		return ImportResult{}, fmt.Errorf("HAR file has no entries")
	}

	// pageref -> page title, for folder naming.
	pageTitle := map[string]string{}
	for _, p := range f.Log.Pages {
		title := p.Title
		if title == "" {
			title = p.ID
		}
		pageTitle[p.ID] = title
	}

	result := ImportResult{
		WorkspaceName: harWorkspaceName(f),
		Format:        FormatHAR,
	}

	nextOrder := newOrderMinter()

	// First pass: count occurrences per method+url so the imported request can
	// report how many identical hits it stands in for.
	counts := map[string]int{}
	for _, e := range f.Log.Entries {
		counts[dedupeKey(e.Request)]++
	}

	// Folders created lazily, one per pageref that actually owns an emitted
	// request, preserving first-seen order.
	folderIDByPage := map[string]string{}
	folderIDFor := func(pageref string) *string {
		if pageref == "" {
			return nil
		}
		title, ok := pageTitle[pageref]
		if !ok {
			return nil
		}
		if fid, ok := folderIDByPage[pageref]; ok {
			return &fid
		}
		fid := uuid.NewString()
		folderIDByPage[pageref] = fid
		result.Folders = append(result.Folders, model.Folder{
			ID: fid, Name: title, OrderKey: nextOrder(),
		})
		return &fid
	}

	emitted := map[string]bool{}
	for _, e := range f.Log.Entries {
		key := dedupeKey(e.Request)
		if emitted[key] {
			continue
		}
		emitted[key] = true
		folderID := folderIDFor(e.PageRef)
		result.Requests = append(result.Requests, harToRequest(e.Request, folderID, nextOrder(), counts[key]))
	}

	if len(result.Requests) == 0 {
		return ImportResult{}, fmt.Errorf("HAR file has no importable requests")
	}
	return result, nil
}

// dedupeKey is the method+url identity a HAR is deduplicated on.
func dedupeKey(r harRequest) string {
	return strings.ToUpper(r.Method) + " " + r.URL
}

func harToRequest(r harRequest, folderID *string, orderKey string, occurrences int) model.RequestDef {
	method := strings.ToUpper(orDefault(r.Method, "GET"))

	// Query string → Params, stripping the query off the stored URL so the
	// URL bar and Params tab don't both show it. AUK's buildURL reattaches
	// enabled params at send time (internal/protocols/http.buildURL), so the
	// wire request is unchanged. Only strip when we actually captured a
	// queryString array to move.
	rawURL := r.URL
	var params []model.KeyValue
	if len(r.QueryString) > 0 {
		for _, q := range r.QueryString {
			params = append(params, model.KeyValue{Key: q.Name, Value: q.Value, Enabled: true})
		}
		if i := strings.IndexByte(rawURL, '?'); i >= 0 {
			rawURL = rawURL[:i]
		}
	}

	req := model.RequestDef{
		ID:       uuid.NewString(),
		FolderID: folderID,
		Name:     harRequestName(method, r.URL),
		Protocol: model.ProtocolHTTP,
		Method:   method,
		URL:      rawURL,
		Params:   params,
		OrderKey: orderKey,
	}

	// Headers, holding the Authorization value aside for possible auth mapping
	// and skipping HTTP/2 pseudo-headers (":authority", ":method", …) which
	// are not real, sendable headers.
	var authHeader string
	hasCookie := false
	for _, h := range r.Headers {
		if strings.HasPrefix(h.Name, ":") {
			continue
		}
		lname := strings.ToLower(h.Name)
		if lname == "authorization" && authHeader == "" {
			authHeader = h.Value
			continue
		}
		if lname == "cookie" {
			hasCookie = true
		}
		req.Headers = append(req.Headers, model.KeyValue{Key: h.Name, Value: h.Value, Enabled: true})
	}

	// Synthesize a Cookie header from the cookies array only if the request
	// didn't already carry one in its headers.
	if !hasCookie && len(r.Cookies) > 0 {
		var parts []string
		for _, c := range r.Cookies {
			parts = append(parts, c.Name+"="+c.Value)
		}
		req.Headers = append(req.Headers, model.KeyValue{Key: "Cookie", Value: strings.Join(parts, "; "), Enabled: true})
	}

	// Map an obvious Authorization scheme to an AUK auth kind; otherwise keep
	// it as a plain header so nothing is lost.
	if authHeader != "" {
		if auth, ok := authFromHeaderValue(authHeader); ok {
			req.Auth = auth
		} else {
			req.Headers = append(req.Headers, model.KeyValue{Key: "Authorization", Value: authHeader, Enabled: true})
		}
	}

	applyHARBody(&req, r.PostData)

	if occurrences > 1 {
		req.Description = fmt.Sprintf(
			"Imported from HAR. %d identical %s requests were captured for this URL; only the first was imported.",
			occurrences, method)
	}
	return req
}

// authFromHeaderValue maps a raw Authorization header value to an AUK auth
// config for the schemes we can reconstruct losslessly (Bearer, Basic).
// Anything else (Digest, AWS4-HMAC, custom) returns ok=false so the caller
// keeps it as a header.
func authFromHeaderValue(v string) (*model.AuthConfig, bool) {
	scheme, rest, found := strings.Cut(strings.TrimSpace(v), " ")
	if !found {
		return nil, false
	}
	rest = strings.TrimSpace(rest)
	switch strings.ToLower(scheme) {
	case "bearer":
		return &model.AuthConfig{Kind: model.AuthBearer, Bearer: &model.BearerAuth{Token: rest}}, true
	case "basic":
		dec, err := base64.StdEncoding.DecodeString(rest)
		if err != nil {
			return nil, false
		}
		user, pass, _ := strings.Cut(string(dec), ":")
		return &model.AuthConfig{Kind: model.AuthBasic, Basic: &model.BasicAuth{Username: user, Password: pass}}, true
	}
	return nil, false
}

func applyHARBody(req *model.RequestDef, pd *harPostData) {
	if pd == nil {
		return
	}
	mime := strings.ToLower(strings.TrimSpace(pd.MimeType))
	if i := strings.IndexByte(mime, ';'); i >= 0 { // drop ";charset=utf-8"
		mime = strings.TrimSpace(mime[:i])
	}
	switch {
	case strings.Contains(mime, "json"):
		if pd.Text == "" {
			return
		}
		req.Body = &model.RequestBody{Kind: model.BodyJSON, Text: pd.Text}
	case strings.Contains(mime, "x-www-form-urlencoded"), strings.Contains(mime, "multipart/form-data"):
		var fields []model.KeyValue
		if len(pd.Params) > 0 {
			for _, p := range pd.Params {
				fields = append(fields, model.KeyValue{Key: p.Name, Value: p.Value, Enabled: true})
			}
		} else if pd.Text != "" {
			fields = parseFormFields(pd.Text) // shared with curl.go
		}
		req.Body = &model.RequestBody{Kind: model.BodyForm, FormFields: fields}
	default:
		if pd.Text != "" {
			req.Body = &model.RequestBody{Kind: model.BodyText, Text: pd.Text}
		}
	}
}

// harRequestName renders a readable "METHOD /path" label from a captured URL.
func harRequestName(method, rawURL string) string {
	path := rawURL
	if u, err := url.Parse(rawURL); err == nil {
		path = u.Path
		if path == "" {
			path = "/"
		}
	}
	return method + " " + path
}

// harWorkspaceName prefers the host of the first entry ("api.example.com
// (HAR)"), which reads better than a Chrome page title (usually the full page
// URL). Falls back to a generic label.
func harWorkspaceName(f harFile) string {
	if len(f.Log.Entries) > 0 {
		if u, err := url.Parse(f.Log.Entries[0].Request.URL); err == nil && u.Host != "" {
			return u.Host + " (HAR)"
		}
	}
	return "Imported (HAR)"
}
