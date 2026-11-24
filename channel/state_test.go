package channel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBindingEnsureAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindings.json")
	store, err := OpenBindings(path, "/default/cwd")
	if err != nil {
		t.Fatal(err)
	}
	b := store.ensure("feishu:oc_1", "张三")
	if b.CWD != "/default/cwd" {
		t.Fatalf("default cwd = %q", b.CWD)
	}
	if b.Name != "张三" || b.Created == "" {
		t.Fatalf("binding not seeded: %+v", b)
	}
	// ensure twice is the same record, not two
	b2 := store.ensure("feishu:oc_1", "张三")
	if b2 != b {
		t.Fatalf("ensure is not idempotent: %+v vs %+v", b2, b)
	}

	// The file exists and round-trips through a second store.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("bindings file missing: %v", err)
	}
	var m map[string]Binding
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("bindings file unreadable: %v", err)
	}
	if _, ok := m["feishu:oc_1"]; !ok {
		t.Fatalf("key feishu:oc_1 not persisted: %s", raw)
	}
	reloaded, err := OpenBindings(path, "/default/cwd")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := reloaded.get("feishu:oc_1"); got.CWD != "/default/cwd" {
		t.Fatalf("reloaded cwd = %q", got.CWD)
	}
}

func TestBindingSessionAndCWD(t *testing.T) {
	store, err := OpenBindings(filepath.Join(t.TempDir(), "bindings.json"), "/d")
	if err != nil {
		t.Fatal(err)
	}
	store.ensure("k", "")
	store.setSession("k", "/sessions/a.json")
	store.setCWD("k", "/new/cwd")
	got, _ := store.get("k")
	if got.Session != "/sessions/a.json" || got.CWD != "/new/cwd" {
		t.Fatalf("mutations lost: %+v", got)
	}
}

func TestDedupeFirstAndRing(t *testing.T) {
	d, err := OpenDedupe("", 3)
	if err != nil {
		t.Fatal(err)
	}
	if !d.first("a") || d.first("a") {
		t.Fatal("first is not first")
	}
	d.first("b")
	d.first("c")
	// The ring evicts on insert, not on check: "a" is still seen until a
	// fourth id pushes it out.
	if d.first("a") {
		t.Fatal("still-seen id reported fresh")
	}
	d.first("d")
	if !d.first("a") {
		t.Fatal("ring did not evict the oldest id")
	}
}

func TestDedupePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dedupe.json")
	d, err := OpenDedupe(path, 16)
	if err != nil {
		t.Fatal(err)
	}
	d.first("feishu:om_1")
	d.first("feishu:om_2")
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := OpenDedupe(path, 16)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.first("feishu:om_1") {
		t.Fatal("persisted id forgotten")
	}
	if !reloaded.first("feishu:om_9") {
		t.Fatal("fresh id reported as seen")
	}
}

func TestDedupeTruncatesOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dedupe.json")
	raw := `["a","b","c","d","e"]`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := OpenDedupe(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	// The cap keeps the newest tail: a/b are gone, e is still known.
	if !d.first("a") {
		t.Fatal("truncated id not reportable again")
	}
	if d.first("e") {
		t.Fatal("newest id lost")
	}
}

func TestAllowlistReloadOnChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allowlist.json")
	a, err := OpenAllowlist(path)
	if err != nil {
		t.Fatal(err)
	}
	if a.Has("feishu", "ou_1") {
		t.Fatal("empty allowlist admits")
	}
	if err := a.Add("feishu", "ou_1"); err != nil {
		t.Fatal(err)
	}
	if !a.Has("feishu", "ou_1") {
		t.Fatal("added id not admitted")
	}
	if a.Has("feishu", "ou_2") || a.Has("qq", "ou_1") {
		t.Fatal("admission leaked across user or adapter")
	}

	// A second process (approve CLI) writes the file while this one runs.
	other, err := OpenAllowlist(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Add("feishu", "ou_2"); err != nil {
		t.Fatal(err)
	}
	if !a.Has("feishu", "ou_2") {
		t.Fatal("external change not picked up")
	}
}

func TestPairingCodeLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pairing.json")
	p, err := OpenPairing(path)
	if err != nil {
		t.Fatal(err)
	}
	code, err := p.NewCode("feishu", "ou_1", "oc_1")
	if err != nil || len(code) != 6 {
		t.Fatalf("code = %q err=%v", code, err)
	}
	// Same user re-asking mid-window reuses the code.
	again, err := p.NewCode("feishu", "ou_1", "oc_1")
	if err != nil || again != code {
		t.Fatalf("reissue = %q, want %q", again, code)
	}

	// Approve pops the code exactly once.
	adapter, userID, err := p.Approve(code)
	if err != nil || adapter != "feishu" || userID != "ou_1" {
		t.Fatalf("approve = %s/%s err=%v", adapter, userID, err)
	}
	if _, _, err := p.Approve(code); err == nil {
		t.Fatal("approved code reusable")
	}
}
