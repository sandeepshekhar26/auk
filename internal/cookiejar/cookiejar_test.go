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
