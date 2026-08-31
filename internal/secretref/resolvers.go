package secretref

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"apitool/internal/onepassword"
)

// ---- op:// (1Password) --------------------------------------------------

// OnePassword adapts the existing internal/onepassword package to the
// Resolver interface. Behaviour is unchanged — this is the reference
// implementation the interface was shaped around, not a rewrite of it.
type OnePassword struct{}

func (OnePassword) Scheme() string { return "op" }

func (OnePassword) Available() error {
	if onepassword.Available() {
		return nil
	}
	return fmt.Errorf("1Password CLI (op) not found on PATH — install it from https://developer.1password.com/docs/cli")
}

func (OnePassword) Resolve(ctx context.Context, ref string) (string, error) {
	return onepassword.Read(ctx, ref)
}

// ---- env:// (dotenv files) ----------------------------------------------

// DotEnv resolves `env://<path>#<KEY>` against a .env file on disk.
//
// This is the cheapest "AUK already works with my project" moment there is:
// every developer has a .env within arm's reach, already holding the tokens
// they would otherwise re-type into a GUI. Pointing a variable at it means the
// credential keeps living where the project already keeps it — rotated by the
// same process, gitignored by the same rule — and AUK never stores a copy.
//
//	env://.env#STRIPE_KEY              relative to WorkspaceDir
//	env://./config/.env.local#API_KEY  relative paths resolve the same way
//	env:///Users/me/p/.env#TOKEN       absolute path
type DotEnv struct {
	// WorkspaceDir anchors relative paths. Reading a .env is a filesystem
	// capability, so it is scoped: a workspace can reach its own project's
	// files, not wander the disk from an unanchored relative path.
	WorkspaceDir string
}

func (DotEnv) Scheme() string { return "env" }

func (DotEnv) Available() error { return nil } // nothing to install

func (d DotEnv) Resolve(_ context.Context, ref string) (string, error) {
	rest := strings.TrimPrefix(ref, "env://")
	path, key, ok := strings.Cut(rest, "#")
	if !ok || strings.TrimSpace(key) == "" {
		return "", fmt.Errorf("reference %q is missing the #KEY part — use env://.env#MY_KEY", ref)
	}
	if strings.TrimSpace(path) == "" {
		path = ".env"
	}

	if !filepath.IsAbs(path) && d.WorkspaceDir == "" {
		// No anchor: a relative path would resolve against whatever the
		// process's working directory happens to be, which for a GUI app is
		// arbitrary and for a CLI run is the user's shell. Refuse rather than
		// read a file nobody chose.
		return "", fmt.Errorf("relative path %q needs a workspace directory; use an absolute path", path)
	}
	full := path
	if !filepath.IsAbs(path) {
		full = filepath.Join(d.WorkspaceDir, path)
	}
	// Resolve symlinks and `..` before deciding the file is in bounds:
	// checking the literal string would let `env://../../../etc/passwd#x`
	// through on a path that only LOOKS relative.
	clean := filepath.Clean(full)
	if !filepath.IsAbs(path) && d.WorkspaceDir != "" {
		root := filepath.Clean(d.WorkspaceDir)
		if !strings.HasPrefix(clean, root+string(os.PathSeparator)) && clean != root {
			return "", fmt.Errorf("%q escapes the workspace directory; use an absolute path if that is intended", path)
		}
	}

	vars, err := parseDotEnv(clean)
	if err != nil {
		return "", err
	}
	v, found := vars[key]
	if !found {
		return "", fmt.Errorf("%s has no %s", clean, key)
	}
	return v, nil
}

// parseDotEnv reads the subset of dotenv syntax that is actually universal:
// KEY=value, `export KEY=value`, # comments, blank lines, and single- or
// double-quoted values.
//
// It deliberately does NOT do variable interpolation (`${OTHER}` inside a
// value). Two reasons: AUK's own templating already owns `${...}`, so
// interpolating here would create two layers arguing over the same syntax;
// and a .env that relies on interpolation is usually being consumed by a
// specific loader whose exact semantics differ per language anyway.
func parseDotEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no .env file at %s", path)
		}
		return nil, err
	}
	defer f.Close()

	out := map[string]string{}
	sc := bufio.NewScanner(f)
	// .env values (a JWT, a PEM on one line) can be long; the default 64KB
	// token limit would fail on those with an opaque scanner error.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		// Quoted values keep their inner whitespace and may carry a trailing
		// comment outside the quotes; unquoted values stop at an unescaped #.
		switch {
		case len(value) >= 2 && value[0] == '"' && strings.HasSuffix(value, `"`):
			value = value[1 : len(value)-1]
			value = strings.ReplaceAll(value, `\n`, "\n")
		case len(value) >= 2 && value[0] == '\'' && strings.HasSuffix(value, `'`):
			value = value[1 : len(value)-1]
		default:
			if i := strings.Index(value, " #"); i >= 0 {
				value = strings.TrimSpace(value[:i])
			}
		}
		out[key] = value
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return out, nil
}

// Default builds the registry AUK runs with: 1Password plus dotenv, anchored
// at the workspace directory.
func Default(workspaceDir string) *Registry {
	r := New()
	r.Register(OnePassword{})
	r.Register(DotEnv{WorkspaceDir: workspaceDir})
	return r
}
