package core

import (
	"context"
	"testing"

	"apitool/internal/core/model"
)

// ---------------------------------------------------------------------------
// The run-scoped variable layer (finding 3), unit-tested at the engine layer.
// The end-to-end proofs (per-iteration reset, response() chaining under --data,
// no-persist, no race) live in internal/runner with the real scripter.
// ---------------------------------------------------------------------------

func kv(pairs ...string) []model.KeyValue {
	out := make([]model.KeyValue, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, model.KeyValue{Key: pairs[i], Value: pairs[i+1], Enabled: true})
	}
	return out
}

func overlayMap(rv *RunScopedVars) map[string]string {
	m := map[string]string{}
	for _, kv := range rv.overlay() {
		m[kv.Key] = kv.Value
	}
	return m
}

// TestRunScopedVarsPrecedenceAndReset covers the layer's core contract: a script
// write beats the data row within an iteration, an unset tombstones the data
// row, and Reset drops every script write so the next iteration starts clean.
func TestRunScopedVarsPrecedenceAndReset(t *testing.T) {
	rv := NewRunScopedVars()
	rv.Reset(kv("user", "row1", "plan", "free"))

	// Data row visible.
	if m := overlayMap(rv); m["user"] != "row1" || m["plan"] != "free" {
		t.Fatalf("data row not visible: %+v", m)
	}

	// Script write beats the data row within the iteration.
	rv.setScript("plan", "pro")
	rv.setScript("token", "tok-1")
	if m := overlayMap(rv); m["plan"] != "pro" || m["token"] != "tok-1" || m["user"] != "row1" {
		t.Fatalf("script write should layer over the data row: %+v", m)
	}

	// Unset tombstones the data-row column for later requests this iteration.
	rv.unsetScript("user")
	if _, ok := overlayMap(rv)["user"]; ok {
		t.Fatalf("unset should hide the data-row column, got %+v", overlayMap(rv))
	}

	// A new iteration: the row swaps and every script write/unset is gone.
	rv.Reset(kv("user", "row2"))
	m := overlayMap(rv)
	if m["user"] != "row2" {
		t.Fatalf("new data row not installed: %+v", m)
	}
	if _, ok := m["token"]; ok {
		t.Fatalf("iteration 1's script write leaked into iteration 2: %+v", m)
	}
	if _, ok := m["plan"]; ok {
		t.Fatalf("iteration 1's script write leaked into iteration 2: %+v", m)
	}
}

// TestMergedEnvironmentConsultsRunScopedLayerOnlyViaContext proves the layer is
// applied only for a request whose context carries it (a data run), never for a
// plain send — and that when applied it outranks the environment.
func TestMergedEnvironmentConsultsRunScopedLayerOnlyViaContext(t *testing.T) {
	store := newRedactingStore()
	const ws = model.ID("ws1")
	store.envs["e"] = model.Environment{
		ID: "e", WorkspaceID: ws, Name: "Env",
		Variables: kv("user", "fromEnv", "plan", "envPlan"),
	}
	engine := newTestEngine(store, &capturingProtocol{})
	req := model.RequestDef{ID: "r", WorkspaceID: ws, Name: "R"}

	// No layer on the context: environment values stand.
	plain, err := engine.mergedEnvironment(context.Background(), req, "e")
	if err != nil {
		t.Fatalf("mergedEnvironment: %v", err)
	}
	if last := lastValue(plain, "user"); last != "fromEnv" {
		t.Fatalf("without a run layer, ${user} should be the env value, got %q", last)
	}

	// Layer attached: the data row wins over the environment.
	rv := NewRunScopedVars()
	rv.Reset(kv("user", "fromData"))
	ctx := WithRunScopedVars(context.Background(), rv)
	withLayer, err := engine.mergedEnvironment(ctx, req, "e")
	if err != nil {
		t.Fatalf("mergedEnvironment: %v", err)
	}
	if last := lastValue(withLayer, "user"); last != "fromData" {
		t.Fatalf("the data row should beat the environment, got %q", last)
	}
	if last := lastValue(withLayer, "plan"); last != "envPlan" {
		t.Fatalf("a column the row omits should fall through to the env, got %q", last)
	}
}

// TestApplyVariableWritesUnderDataRunIsRunScoped proves the persistence
// semantics: in a data run a vars.set is run-scoped — visible to later requests
// in the iteration, but NEVER persisted to the environment or the session
// overlay.
func TestApplyVariableWritesUnderDataRunIsRunScoped(t *testing.T) {
	store := newRedactingStore()
	const ws = model.ID("ws1")
	store.envs["e"] = model.Environment{ID: "e", WorkspaceID: ws, Name: "Env"}
	engine := newTestEngine(store, &capturingProtocol{})
	req := model.RequestDef{ID: "r", WorkspaceID: ws, Name: "R"}

	rv := NewRunScopedVars()
	rv.Reset(nil)
	ctx := WithRunScopedVars(context.Background(), rv)

	env := store.envs["e"]
	engine.applyVariableWrites(ctx, req, "e", &env, map[string]string{"token": "fresh"}, nil)

	if overlayMap(rv)["token"] != "fresh" {
		t.Fatalf("a data-run write should land in the run-scoped layer, got %+v", overlayMap(rv))
	}
	if len(store.puts) != 0 {
		t.Fatalf("a data run must not persist to the environment, got %d PutEnvironment call(s)", len(store.puts))
	}
	if _, ok := engine.scriptVars.snapshot(ws)["token"]; ok {
		t.Fatalf("a data-run write must not pollute the session overlay")
	}
}

func lastValue(env *model.Environment, key string) string {
	out := ""
	if env == nil {
		return out
	}
	for _, kv := range env.Variables {
		if kv.Key == key && kv.Enabled {
			out = kv.Value
		}
	}
	return out
}
