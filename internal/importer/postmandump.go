package importer

// Postman's Settings → Data → Export Data writes ONE JSON file holding every
// collection and every environment in the account at once — the file a
// switcher reaches for when they want to leave, rather than exporting twelve
// collections by hand. This file understands that dump (and the standalone
// environment export, which the single-collection importer never handled),
// and hands each collection to the EXISTING ParsePostman so nothing about
// single-collection fidelity is reimplemented here.
//
// The shape of the dump has drifted across Postman versions (and differs
// again between the desktop dump and an API-driven backup), so the parser is
// deliberately shape-tolerant: it probes the plausible keys, then falls back
// to walking the document for anything that STRUCTURALLY looks like a
// collection or an environment. Being wrong about a key name should not cost
// the user their migration.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"apitool/internal/core/model"
)

// Formats produced by a Postman export that Import()'s single-document
// Detect doesn't classify: the multi-collection data dump, and the
// standalone environment export.
const (
	FormatPostmanDump        = "postman-dump"
	FormatPostmanEnvironment = "environment"
)

// DetectPostmanFile classifies one file dropped into the migration flow:
// FormatPostman (a single collection — same answer Detect gives),
// FormatPostmanDump (an Export Data bundle of many collections and/or
// environments), FormatPostmanEnvironment (a standalone environment export),
// or "" when the file is not Postman data at all.
func DetectPostmanFile(content string) string {
	trimmed := strings.TrimPrefix(strings.TrimSpace(content), "\ufeff")
	if trimmed == "" {
		return ""
	}
	// A single collection is the unambiguous case: it carries info._postman_id
	// (or the getpostman.com schema) at the TOP level. Checked first so a
	// collection is never mistaken for a one-element dump.
	if Detect(trimmed) == FormatPostman {
		return FormatPostman
	}

	var doc any
	if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		return ""
	}
	if m, ok := doc.(map[string]any); ok && looksPostmanEnvironment(m) {
		return FormatPostmanEnvironment
	}
	var probe PostmanDump
	harvestPostmanDump(doc, 0, &probe)
	if len(probe.Collections) > 0 || len(probe.Environments) > 0 {
		return FormatPostmanDump
	}
	return ""
}

// PostmanDump is everything a data dump yielded: the raw JSON of each
// collection (ready for ParsePostman) plus every environment, already mapped
// onto AUK's model.
type PostmanDump struct {
	Collections  []DumpCollection
	Environments []model.Environment
}

// DumpCollection is one collection lifted out of a dump, kept as raw JSON so
// it can go through the ordinary ParsePostman path (and through the script
// scanner, which reads fields ParsePostman ignores).
type DumpCollection struct {
	Name string
	Raw  []byte
}

// ParsePostmanDump reads a Postman "Export Data" file. It never fails on an
// unrecognized surrounding shape — it fails only when the document is not
// JSON, or holds no collections and no environments.
func ParsePostmanDump(data []byte) (PostmanDump, error) {
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return PostmanDump{}, fmt.Errorf("parse Postman data dump: %w", err)
	}
	var dump PostmanDump
	harvestPostmanDump(doc, 0, &dump)
	if len(dump.Collections) == 0 && len(dump.Environments) == 0 {
		return PostmanDump{}, fmt.Errorf("no Postman collections or environments found in this file")
	}
	return dump, nil
}

// maxDumpDepth bounds the structural search. Every real dump shape puts its
// collections within two or three levels of the root ({"collections":[…]},
// {"data":{"collections":[…]}}); the limit stops a pathological document
// from turning detection into a deep walk.
const maxDumpDepth = 5

// dumpPreferredKeys are visited before any other key, in this order, so the
// output order of a well-formed dump is stable and predictable rather than
// Go's randomized map order.
var dumpPreferredKeys = []string{
	"collections", "collection",
	"environments", "environment",
	"globals", "global",
	"data", "workspaces", "workspace",
}

func harvestPostmanDump(node any, depth int, out *PostmanDump) {
	if depth > maxDumpDepth {
		return
	}
	switch v := node.(type) {
	case []any:
		for _, el := range v {
			harvestPostmanDump(el, depth+1, out)
		}
	case map[string]any:
		// Some backups wrap each entry as {"id":…, "collection":{…v2.1…}}.
		if inner, ok := v["collection"].(map[string]any); ok && looksPostmanCollection(inner) {
			out.appendCollection(inner)
			return
		}
		if looksPostmanCollection(v) {
			out.appendCollection(v)
			return
		}
		if looksPostmanEnvironment(v) {
			out.appendEnvironment(v)
			return
		}
		for _, k := range orderedDumpKeys(v) {
			switch child := v[k].(type) {
			case map[string]any:
				harvestPostmanDump(child, depth+1, out)
			case []any:
				harvestPostmanDump(child, depth+1, out)
			}
		}
	}
}

// orderedDumpKeys yields the preferred keys present in m first, then every
// remaining key in sorted order — deterministic regardless of map iteration.
func orderedDumpKeys(m map[string]any) []string {
	seen := make(map[string]bool, len(m))
	keys := make([]string, 0, len(m))
	for _, k := range dumpPreferredKeys {
		if _, ok := m[k]; ok {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	rest := make([]string, 0, len(m))
	for k := range m {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	return append(keys, rest...)
}

func (d *PostmanDump) appendCollection(m map[string]any) {
	raw, err := json.Marshal(m)
	if err != nil {
		return
	}
	name := "Imported Collection"
	if info, ok := m["info"].(map[string]any); ok {
		if n, ok := info["name"].(string); ok && strings.TrimSpace(n) != "" {
			name = n
		}
	}
	d.Collections = append(d.Collections, DumpCollection{Name: name, Raw: raw})
}

func (d *PostmanDump) appendEnvironment(m map[string]any) {
	raw, err := json.Marshal(m)
	if err != nil {
		return
	}
	env, err := ParsePostmanEnvironment(raw)
	if err != nil {
		return
	}
	d.Environments = append(d.Environments, env)
}

// looksPostmanCollection reports whether m is a Postman collection document.
// Strict on purpose: the structural fallback walk relies on this never
// matching a folder or a request item (neither carries an `info` object).
func looksPostmanCollection(m map[string]any) bool {
	info, ok := m["info"].(map[string]any)
	if !ok {
		return false
	}
	if _, ok := info["_postman_id"]; ok {
		return true
	}
	if schema, ok := info["schema"].(string); ok && strings.Contains(schema, "getpostman.com") {
		return true
	}
	if _, hasItem := m["item"]; hasItem {
		if _, hasName := info["name"]; hasName {
			return true
		}
	}
	return false
}

// looksPostmanEnvironment reports whether m is an environment (or globals)
// export: a `values` array of {key,value,…} rows, plus a name or Postman's
// variable-scope marker.
func looksPostmanEnvironment(m map[string]any) bool {
	values, ok := m["values"].([]any)
	if !ok {
		return false
	}
	_, hasName := m["name"]
	_, hasScope := m["_postman_variable_scope"]
	if !hasName && !hasScope {
		return false
	}
	for _, v := range values {
		row, ok := v.(map[string]any)
		if !ok {
			return false
		}
		if _, ok := row["key"]; !ok {
			return false
		}
		return true
	}
	// An environment with no variables at all is still an environment.
	return true
}

// postmanEnvFile is the standalone environment export shape:
// {"name":"Staging","values":[{"key":"host","value":"…","enabled":true,"type":"default"}]}.
type postmanEnvFile struct {
	Name   string            `json:"name"`
	Scope  string            `json:"_postman_variable_scope"`
	Values []postmanEnvValue `json:"values"`
}

type postmanEnvValue struct {
	Key string `json:"key"`
	// Value is RawMessage for the same reason postmanVar.Value is: Postman
	// types it as `any` and real exports carry numbers and booleans.
	Value json.RawMessage `json:"value"`
	// Enabled is a pointer so a missing field means enabled (Postman's
	// default) rather than disabled.
	Enabled *bool `json:"enabled"`
	// Type is "default" or "secret". A secret's VALUE never comes across —
	// see ParsePostmanEnvironment.
	Type string `json:"type"`
}

// ParsePostmanEnvironment converts a Postman environment (or globals) export
// into an AUK environment.
//
// A value Postman marked `"type":"secret"` is imported by NAME ONLY: AUK keeps
// secret values in the OS keychain and never on disk (docs/02-architecture.md
// §7), and a Postman export carries the value in plaintext. Writing it into
// the YAML file would quietly downgrade the user's security posture on their
// first minute in the app, so the name lands in Environment.Secrets with an
// empty row for the user to paste into. The migration reports every one of
// them by name.
func ParsePostmanEnvironment(data []byte) (model.Environment, error) {
	var f postmanEnvFile
	if err := json.Unmarshal(data, &f); err != nil {
		return model.Environment{}, fmt.Errorf("parse Postman environment: %w", err)
	}
	var probe map[string]any
	if err := json.Unmarshal(data, &probe); err != nil || !looksPostmanEnvironment(probe) {
		return model.Environment{}, fmt.Errorf("not a Postman environment export (no \"values\" array)")
	}

	name := strings.TrimSpace(f.Name)
	if name == "" {
		if strings.EqualFold(f.Scope, "globals") {
			name = "Globals"
		} else {
			name = "Imported Environment"
		}
	}

	env := model.Environment{ID: uuid.NewString(), Name: name}
	for _, v := range f.Values {
		if strings.TrimSpace(v.Key) == "" {
			continue
		}
		enabled := true
		if v.Enabled != nil {
			enabled = *v.Enabled
		}
		if strings.EqualFold(v.Type, "secret") {
			// Name only. The row exists so the environment editor shows it
			// (a secret is a Variables row whose key is listed in Secrets),
			// but the value stays out of the file.
			env.Variables = append(env.Variables, model.KeyValue{Key: v.Key, Value: "", Enabled: enabled})
			env.Secrets = append(env.Secrets, v.Key)
			continue
		}
		env.Variables = append(env.Variables, model.KeyValue{
			Key:     v.Key,
			Value:   convertPostmanVars(postmanVar{Value: v.Value}.scalarString()),
			Enabled: enabled,
		})
	}
	return env, nil
}
