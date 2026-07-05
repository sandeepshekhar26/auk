package cookiejar

import (
	"testing"

	"apitool/internal/core/model"
)

func TestJar_CaptureAndGet(t *testing.T) {
	j := New()
	j.Capture("ws-1", []model.KeyValue{
		{Key: "Set-Cookie", Value: "session=abc123; Path=/; HttpOnly"},
		{Key: "Content-Type", Value: "application/json"},
	})

	v, ok := j.Get("ws-1", "session")
	if !ok || v != "abc123" {
		t.Fatalf("got (%q, %v), want (\"abc123\", true)", v, ok)
	}
}

func TestJar_MultipleSetCookieHeaders(t *testing.T) {
	j := New()
	j.Capture("ws-1", []model.KeyValue{
		{Key: "Set-Cookie", Value: "a=1"},
		{Key: "Set-Cookie", Value: "b=2"},
	})
	if v, ok := j.Get("ws-1", "a"); !ok || v != "1" {
		t.Fatalf("cookie a: got (%q, %v)", v, ok)
	}
	if v, ok := j.Get("ws-1", "b"); !ok || v != "2" {
		t.Fatalf("cookie b: got (%q, %v)", v, ok)
	}
}

func TestJar_LastResponseWins(t *testing.T) {
	j := New()
	j.Capture("ws-1", []model.KeyValue{{Key: "Set-Cookie", Value: "session=old"}})
	j.Capture("ws-1", []model.KeyValue{{Key: "Set-Cookie", Value: "session=new"}})
	v, ok := j.Get("ws-1", "session")
	if !ok || v != "new" {
		t.Fatalf("got (%q, %v), want (\"new\", true)", v, ok)
	}
}

func TestJar_WorkspaceIsolation(t *testing.T) {
	j := New()
	j.Capture("ws-1", []model.KeyValue{{Key: "Set-Cookie", Value: "session=ws1val"}})
	j.Capture("ws-2", []model.KeyValue{{Key: "Set-Cookie", Value: "session=ws2val"}})

	v1, _ := j.Get("ws-1", "session")
	v2, _ := j.Get("ws-2", "session")
	if v1 != "ws1val" || v2 != "ws2val" {
		t.Fatalf("cross-workspace leak: ws-1=%q ws-2=%q", v1, v2)
	}
}

func TestJar_GetMissing(t *testing.T) {
	j := New()
	if _, ok := j.Get("unknown-ws", "whatever"); ok {
		t.Fatalf("expected ok=false for unknown workspace")
	}
	j.Capture("ws-1", []model.KeyValue{{Key: "Set-Cookie", Value: "a=1"}})
	if _, ok := j.Get("ws-1", "nonexistent"); ok {
		t.Fatalf("expected ok=false for unknown cookie name")
	}
}

func TestJar_NoSetCookieHeaders(t *testing.T) {
	j := New()
	j.Capture("ws-1", []model.KeyValue{{Key: "Content-Type", Value: "text/plain"}})
	if _, ok := j.Get("ws-1", "anything"); ok {
		t.Fatalf("expected no cookies captured when there are no Set-Cookie headers")
	}
}

func TestJar_ListSortedByName(t *testing.T) {
	j := New()
	j.Capture("ws-1", []model.KeyValue{
		{Key: "Set-Cookie", Value: "zebra=1"},
		{Key: "Set-Cookie", Value: "apple=2"},
		{Key: "Set-Cookie", Value: "mango=3"},
	})
	got := j.List("ws-1")
	if len(got) != 3 {
		t.Fatalf("got %d cookies, want 3", len(got))
	}
	wantOrder := []string{"apple", "mango", "zebra"}
	for i, name := range wantOrder {
		if got[i].Key != name {
			t.Errorf("position %d: got %q, want %q", i, got[i].Key, name)
		}
	}
}

func TestJar_ListEmptyWorkspace(t *testing.T) {
	j := New()
	got := j.List("never-touched-ws")
	if len(got) != 0 {
		t.Fatalf("got %d cookies, want 0 for a workspace with none captured", len(got))
	}
}

func TestJar_ListIsolatedPerWorkspace(t *testing.T) {
	j := New()
	j.Capture("ws-1", []model.KeyValue{{Key: "Set-Cookie", Value: "a=1"}})
	j.Capture("ws-2", []model.KeyValue{{Key: "Set-Cookie", Value: "b=2"}})
	if got := j.List("ws-1"); len(got) != 1 || got[0].Key != "a" {
		t.Fatalf("ws-1 list leaked or missing: %+v", got)
	}
	if got := j.List("ws-2"); len(got) != 1 || got[0].Key != "b" {
		t.Fatalf("ws-2 list leaked or missing: %+v", got)
	}
}

func TestJar_SetAddsAndOverwrites(t *testing.T) {
	j := New()
	j.Set("ws-1", "session", "manually-set-value")
	if v, ok := j.Get("ws-1", "session"); !ok || v != "manually-set-value" {
		t.Fatalf("got (%q, %v), want (\"manually-set-value\", true)", v, ok)
	}
	// A real captured response after a manual edit still wins (last-write,
	// same as two real responses) — manual Set isn't sticky/pinned.
	j.Capture("ws-1", []model.KeyValue{{Key: "Set-Cookie", Value: "session=from-server"}})
	if v, _ := j.Get("ws-1", "session"); v != "from-server" {
		t.Fatalf("got %q, want the real response to override the manual edit", v)
	}
}

func TestJar_SetOnUntouchedWorkspaceCreatesIt(t *testing.T) {
	j := New()
	j.Set("brand-new-ws", "a", "1")
	if v, ok := j.Get("brand-new-ws", "a"); !ok || v != "1" {
		t.Fatalf("got (%q, %v), want (\"1\", true) — Set must work even for a workspace with no prior Capture", v, ok)
	}
}

func TestJar_Delete(t *testing.T) {
	j := New()
	j.Capture("ws-1", []model.KeyValue{
		{Key: "Set-Cookie", Value: "a=1"},
		{Key: "Set-Cookie", Value: "b=2"},
	})
	j.Delete("ws-1", "a")
	if _, ok := j.Get("ws-1", "a"); ok {
		t.Fatal("expected cookie a to be gone after Delete")
	}
	if v, ok := j.Get("ws-1", "b"); !ok || v != "2" {
		t.Fatalf("Delete must not touch other cookies: got (%q, %v)", v, ok)
	}
}

func TestJar_DeleteMissingIsNoop(t *testing.T) {
	j := New()
	j.Delete("never-touched-ws", "whatever") // must not panic
}
