package main

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"apitool/internal/core/model"
	"apitool/internal/storage"
)

// seedFolderWorkspace writes a two-request folder (one passing, one failing
// depending on failURL) plus a nested subfolder, and returns the workspace
// dir and the folder id.
func seedFolderWorkspace(t *testing.T, serverURL string, assertions []model.Assertion) (dir string, folderID model.ID) {
	t.Helper()
	dir = t.TempDir()
	store, err := storage.NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	ws := model.ID("ws-cli")
	if err := store.PutWorkspace(model.Workspace{ID: ws, Name: "CLI"}); err != nil {
		t.Fatal(err)
	}
	folderID = model.ID("folder-cli")
	if err := store.PutFolder(model.Folder{ID: folderID, WorkspaceID: ws, Name: "Smoke", OrderKey: "a"}); err != nil {
		t.Fatal(err)
	}
	reqs := []model.RequestDef{
		{ID: "cli-a", WorkspaceID: ws, FolderID: &folderID, Name: "first", OrderKey: "a",
			Protocol: model.ProtocolHTTP, Method: "GET", URL: serverURL + "/ok"},
		{ID: "cli-b", WorkspaceID: ws, FolderID: &folderID, Name: "second", OrderKey: "b",
			Protocol: model.ProtocolHTTP, Method: "GET", URL: serverURL + "/ok", Assertions: assertions},
	}
	for _, r := range reqs {
		if err := store.PutRequest(r); err != nil {
			t.Fatal(err)
		}
	}
	return dir, folderID
}

func okServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","count":3}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestRunFolderExitCodes is the CI contract: 0 when every request passes,
// 1 when any check fails, 2 when the run could not start.
func TestRunFolderExitCodes(t *testing.T) {
	srv := okServer(t)

	t.Run("all passing exits 0", func(t *testing.T) {
		dir, folder := seedFolderWorkspace(t, srv.URL, []model.Assertion{
			{Source: model.AssertBody, Path: "status", Operator: model.OpEq, Value: "ok", Enabled: true},
		})
		err := run([]string{"run-folder", folder, "--workspace-dir", dir})
		if err != nil {
			t.Fatalf("run() error = %v, want nil (exit 0)", err)
		}
		if got := exitCode(err); got != 0 {
			t.Fatalf("exit code = %d, want 0", got)
		}
	})

	t.Run("failed assertion exits 1", func(t *testing.T) {
		dir, folder := seedFolderWorkspace(t, srv.URL, []model.Assertion{
			{Source: model.AssertBody, Path: "count", Operator: model.OpGt, Value: "10", Enabled: true},
		})
		err := run([]string{"run-folder", folder, "--workspace-dir", dir})
		if err == nil {
			t.Fatal("a failed assertion must fail the run")
		}
		if got := exitCode(err); got != 1 {
			t.Fatalf("exit code = %d, want 1 (test failure)", got)
		}
	})

	t.Run("unknown folder exits 2", func(t *testing.T) {
		dir, _ := seedFolderWorkspace(t, srv.URL, nil)
		err := run([]string{"run-folder", "no-such-folder", "--workspace-dir", dir})
		if err == nil {
			t.Fatal("expected an error")
		}
		if got := exitCode(err); got != 2 {
			t.Fatalf("exit code = %d, want 2 (the run could not start)", got)
		}
	})

	t.Run("missing folder id exits 2", func(t *testing.T) {
		if got := exitCode(run([]string{"run-folder"})); got != 2 {
			t.Fatalf("exit code = %d, want 2", got)
		}
	})

	t.Run("unknown reporter exits 2", func(t *testing.T) {
		dir, folder := seedFolderWorkspace(t, srv.URL, nil)
		err := run([]string{"run-folder", folder, "--workspace-dir", dir, "--reporter", "xunit"})
		if got := exitCode(err); got != 2 {
			t.Fatalf("exit code = %d, want 2 (bad flag value)", got)
		}
	})
}

func TestRunWorkspaceCommand(t *testing.T) {
	srv := okServer(t)
	dir, _ := seedFolderWorkspace(t, srv.URL, nil)

	// No workspace id: the directory holds exactly one.
	if err := run([]string{"run-workspace", "--workspace-dir", dir}); err != nil {
		t.Fatalf("run-workspace error = %v", err)
	}
	if err := run([]string{"run-workspace", "ws-cli", "--workspace-dir", dir}); err != nil {
		t.Fatalf("run-workspace with an explicit id error = %v", err)
	}
	if got := exitCode(run([]string{"run-workspace", "nope", "--workspace-dir", dir})); got != 2 {
		t.Fatal("an unknown workspace id should exit 2")
	}
}

// TestRunFolderWritesJUnitReport is the end-to-end artifact check: the file
// exists, parses, and its failure count matches the run.
func TestRunFolderWritesJUnitReport(t *testing.T) {
	srv := okServer(t)
	dir, folder := seedFolderWorkspace(t, srv.URL, []model.Assertion{
		{Source: model.AssertBody, Path: "count", Operator: model.OpGt, Value: "10", Enabled: true},
	})
	out := filepath.Join(t.TempDir(), "nested", "results.xml")

	err := run([]string{"run-folder", folder, "--workspace-dir", dir,
		"--reporter", "junit", "--reporter-out", out})
	if err == nil {
		t.Fatal("expected a non-zero exit for the failing assertion")
	}
	if got := exitCode(err); got != 1 {
		t.Fatalf("exit code = %d, want 1", got)
	}

	raw, readErr := os.ReadFile(out)
	if readErr != nil {
		t.Fatalf("report not written: %v", readErr)
	}
	var doc struct {
		XMLName  xml.Name `xml:"testsuites"`
		Tests    int      `xml:"tests,attr"`
		Failures int      `xml:"failures,attr"`
	}
	if err := xml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("report does not parse as XML: %v\n%s", err, raw)
	}
	if doc.Failures != 1 {
		t.Errorf("failures = %d, want 1\n%s", doc.Failures, raw)
	}
	if doc.Tests != 2 {
		t.Errorf("tests = %d, want 2 (one synthetic check + one assertion)\n%s", doc.Tests, raw)
	}
}

func TestRunFolderMultipleReporters(t *testing.T) {
	srv := okServer(t)
	dir, folder := seedFolderWorkspace(t, srv.URL, nil)
	tmp := t.TempDir()
	xmlOut := filepath.Join(tmp, "r.xml")
	jsonOut := filepath.Join(tmp, "r.json")

	err := run([]string{"run-folder", folder, "--workspace-dir", dir,
		"--reporter", "junit", "--reporter-out", xmlOut,
		"--reporter", "json", "--reporter-out", jsonOut})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	for _, path := range []string{xmlOut, jsonOut} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("%s not written: %v", path, err)
		}
		if info.Size() == 0 {
			t.Fatalf("%s is empty", path)
		}
	}
}

func TestBuildSinks(t *testing.T) {
	// spec builds a reporterFlags from a flag stream in the order the flags
	// were given: "r:NAME" is a --reporter, "o:VALUE" is a --reporter-out.
	spec := func(args ...string) reporterFlags {
		var f reporterFlags
		rn := (*reporterName)(&f)
		ro := (*reporterOut)(&f)
		for _, a := range args {
			if v, ok := trimPrefix(a, "r:"); ok {
				_ = rn.Set(v)
			} else if v, ok := trimPrefix(a, "o:"); ok {
				_ = ro.Set(v)
			} else {
				t.Fatalf("bad spec token %q", a)
			}
		}
		return f
	}

	t.Run("defaults to the console reporter", func(t *testing.T) {
		sinks, err := buildSinks(spec(), "cli")
		if err != nil {
			t.Fatal(err)
		}
		if len(sinks) != 1 || sinks[0].reporter.Name() != "cli" || sinks[0].path != "" {
			t.Fatalf("sinks = %+v", sinks)
		}
	})

	t.Run("each --reporter-out binds to the reporter that PRECEDES it", func(t *testing.T) {
		// junit --reporter-out a.xml  json --reporter-out b.json
		sinks, err := buildSinks(spec("r:junit", "o:a.xml", "r:json", "o:b.json"), "cli")
		if err != nil {
			t.Fatal(err)
		}
		if sinks[0].path != "a.xml" || sinks[1].path != "b.json" {
			t.Fatalf("sinks = %+v", sinks)
		}
	})

	t.Run("REGRESSION: the CI recipe writes junit to the file, cli to stdout", func(t *testing.T) {
		// The exact documented recipe:
		//   --reporter cli --reporter junit --reporter-out results.xml
		// The old code bound results.xml to the FIRST path-less reporter (cli),
		// silently putting the console summary into results.xml. The path
		// belongs to the reporter it FOLLOWS — junit.
		sinks, err := buildSinks(spec("r:cli", "r:junit", "o:results.xml"), "cli")
		if err != nil {
			t.Fatal(err)
		}
		if sinks[0].reporter.Name() != "cli" || sinks[0].path != "" {
			t.Fatalf("cli sink should stay on stdout, got %+v", sinks[0])
		}
		if sinks[1].reporter.Name() != "junit" || sinks[1].path != "results.xml" {
			t.Fatalf("junit sink should get results.xml, got %+v", sinks[1])
		}
	})

	t.Run("NAME=PATH binds by name regardless of position", func(t *testing.T) {
		sinks, err := buildSinks(spec("r:junit", "r:json", "o:json=b.json"), "cli")
		if err != nil {
			t.Fatal(err)
		}
		if sinks[0].path != "" || sinks[1].path != "b.json" {
			t.Fatalf("sinks = %+v", sinks)
		}
	})

	t.Run("NAME=PATH for an unselected reporter is an error", func(t *testing.T) {
		if _, err := buildSinks(spec("r:junit", "o:json=b.json"), "cli"); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("two --reporter-out after the same reporter is an error", func(t *testing.T) {
		if _, err := buildSinks(spec("r:junit", "o:a.xml", "o:b.xml"), "cli"); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("unknown reporter name is an error", func(t *testing.T) {
		if _, err := buildSinks(spec("r:tap"), "cli"); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("a windows-style path is not mistaken for NAME=PATH", func(t *testing.T) {
		sinks, err := buildSinks(spec("r:junit", `o:C:\ci\results.xml`), "cli")
		if err != nil {
			t.Fatal(err)
		}
		if sinks[0].path != `C:\ci\results.xml` {
			t.Fatalf("path = %q", sinks[0].path)
		}
	})
}

// trimPrefix is a tiny test helper (strings.CutPrefix isn't in this Go line).
func trimPrefix(s, pfx string) (string, bool) {
	if len(s) >= len(pfx) && s[:len(pfx)] == pfx {
		return s[len(pfx):], true
	}
	return "", false
}

// TestReorderFlagsBoolArity guards the bug a naive reorder introduces: a
// boolean flag must not swallow the id that follows it, or the flag after
// that gets silently dropped.
func TestReorderFlagsBoolArity(t *testing.T) {
	got := reorderFlags([]string{"--bail", "folder-1", "--env=staging"},
		[]string{"env"}, []string{"bail"})
	want := []string{"--bail", "--env=staging", "folder-1"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("reorderFlags = %v, want %v", got, want)
	}
}

func TestRunFolderFlagOrderIndependence(t *testing.T) {
	srv := okServer(t)
	dir, folder := seedFolderWorkspace(t, srv.URL, nil)

	orders := [][]string{
		{"run-folder", folder, "--workspace-dir", dir, "--bail"},
		{"run-folder", "--workspace-dir", dir, folder, "--bail"},
		{"run-folder", "--bail", folder, "--workspace-dir", dir},
		{"run-folder", "--bail", "--workspace-dir=" + dir, folder},
	}
	for _, args := range orders {
		if err := run(args); err != nil {
			t.Fatalf("run(%v) error = %v", args, err)
		}
	}
}

func TestRunSingleRequestWithReporter(t *testing.T) {
	srv := okServer(t)
	dir, _ := seedFolderWorkspace(t, srv.URL, nil)
	out := filepath.Join(t.TempDir(), "one.json")

	if err := run([]string{"run", "cli-a", "--workspace-dir", dir, "--reporter", "json", "--reporter-out", out}); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("report not written: %v", err)
	}
	if !strings.Contains(string(raw), `"requestName": "first"`) {
		t.Errorf("report does not name the request:\n%s", raw)
	}
}

func TestDataDrivenRunThroughCLI(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Query().Get("user"))
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	store, err := storage.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws := model.ID("ws-data")
	if err := store.PutWorkspace(model.Workspace{ID: ws, Name: "Data"}); err != nil {
		t.Fatal(err)
	}
	folder := model.ID("folder-data")
	if err := store.PutFolder(model.Folder{ID: folder, WorkspaceID: ws, Name: "Data", OrderKey: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRequest(model.RequestDef{
		ID: "r-data", WorkspaceID: ws, FolderID: &folder, Name: "echo", OrderKey: "a",
		Protocol: model.ProtocolHTTP, Method: "GET", URL: srv.URL + "/echo?user=${user}",
	}); err != nil {
		t.Fatal(err)
	}

	data := filepath.Join(t.TempDir(), "users.csv")
	if err := os.WriteFile(data, []byte("user\nana\nbo\ncid\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := run([]string{"run-folder", folder, "--workspace-dir", dir, "--data", data, "--iterations", "2"}); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if strings.Join(seen, ",") != "ana,bo" {
		t.Fatalf("requests saw users %v, want [ana bo] (--iterations caps the rows)", seen)
	}
}

// TestBuildSinksRejectsUnboundReporterOut guards two silent-CI-failure modes:
// a bare --reporter-out before any --reporter used to bind to whatever sink
// was first (writing the console summary into results.xml), and with no
// --reporter at all it wrote no file while exiting 0.
func TestBuildSinksRejectsUnboundReporterOut(t *testing.T) {
	mk := func(args ...string) reporterFlags {
		var f reporterFlags
		rn, ro := (*reporterName)(&f), (*reporterOut)(&f)
		for _, a := range args {
			if v, ok := trimPrefix(a, "r:"); ok {
				_ = rn.Set(v)
			} else if v, ok := trimPrefix(a, "o:"); ok {
				_ = ro.Set(v)
			}
		}
		return f
	}
	t.Run("out before any reporter is an error", func(t *testing.T) {
		if _, err := buildSinks(mk("o:results.xml", "r:cli", "r:junit"), "cli"); err == nil {
			t.Fatal("expected an error: the path has no preceding --reporter")
		}
	})
	t.Run("out with no reporter at all is an error", func(t *testing.T) {
		if _, err := buildSinks(mk("o:results.xml"), "cli"); err == nil {
			t.Fatal("expected an error rather than silently writing nothing")
		}
	})
	t.Run("NAME= with an empty path is an error", func(t *testing.T) {
		if _, err := buildSinks(mk("r:junit", "o:junit="), "cli"); err == nil {
			t.Fatal("expected an error rather than silently going to stdout")
		}
	})
	t.Run("the documented recipe still works", func(t *testing.T) {
		sinks, err := buildSinks(mk("r:cli", "r:junit", "o:results.xml"), "cli")
		if err != nil {
			t.Fatal(err)
		}
		if sinks[0].path != "" || sinks[1].path != "results.xml" {
			t.Fatalf("sinks = %+v", sinks)
		}
	})
}
