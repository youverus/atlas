package webhost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"pi-go/internal/assembly"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

func TestWebHostArtifactsCRUDExportAndSessionIsolation(t *testing.T) {
	t.Setenv("ATLAS_HOME", t.TempDir())
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{
		"defaultModel": "p1/m1",
		"providers": {
			"p1": {
				"api": "openai-completions",
				"apiKey": "k1",
				"models": [{"id": "m1", "name": "One", "contextWindow": 8000}]
			}
		}
	}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	dirA := t.TempDir()
	dirB := t.TempDir()
	kernel, _, err := assembly.New(context.Background(), assembly.Options{
		ConfigPath: cfgPath,
		Workdir:    dirA,
		NoMCP:      true,
	})
	if err != nil {
		t.Fatalf("assembly.New: %v", err)
	}
	defer kernel.Close()

	host := NewWith(kernel, Options{Projects: filepath.Join(t.TempDir(), "projects.json")})
	sessionA := host.CurrentSession()
	if _, err := kernel.App.Session().AppendMessage(wire.UserMessage("hello")); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	var publish tool.Tool
	for _, impl := range kernel.Tools(dirA) {
		if impl.Name() == "artifact_publish" {
			publish = impl
			break
		}
	}
	if publish == nil {
		t.Fatal("missing artifact_publish tool")
	}

	sess := host.activeSlot().live.App.Session()
	ctxA := tool.WithSession(context.Background(), tool.SessionContext{ID: sess.ID(), File: sess.File()})
	if _, err := publish.Execute(ctxA, tool.Call{
		ID:        "c1",
		Name:      "artifact_publish",
		Arguments: json.RawMessage(`{"id":"report","kind":"markdown","title":"Report A","content":"# Session A"}`),
	}, func(tool.Partial) {}); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	if _, err := publish.Execute(ctxA, tool.Call{
		ID:        "c2",
		Name:      "artifact_publish",
		Arguments: json.RawMessage(`{"id":"report","kind":"markdown","title":"Report A2","content":"# Session A v2"}`),
	}, func(tool.Partial) {}); err != nil {
		t.Fatalf("publish v2: %v", err)
	}

	items, err := host.ListArtifacts()
	if err != nil || len(items) != 1 || items[0].ID != "report" || items[0].Version != 2 {
		t.Fatalf("ListArtifacts = (%+v, %v)", items, err)
	}
	v1, err := host.GetArtifact("report", 1)
	if err != nil || v1.Content != "# Session A" || v1.ActiveVersion != 1 || len(v1.Versions) != 2 {
		t.Fatalf("GetArtifact v1 = (%+v, %v)", v1, err)
	}
	exported, err := host.ExportArtifact("report", 2, "docs/out.md")
	if err != nil || exported.Path != "docs/out.md" {
		t.Fatalf("ExportArtifact = (%+v, %v)", exported, err)
	}
	gotFile, err := os.ReadFile(filepath.Join(dirA, "docs", "out.md"))
	if err != nil || string(gotFile) != "# Session A v2" {
		t.Fatalf("exported content = %q, err=%v", string(gotFile), err)
	}

	// Switch to a new session in dirB: artifacts must not leak across sessions.
	if err := host.AddNamedProject(dirB, "Project B"); err != nil {
		t.Fatalf("AddNamedProject: %v", err)
	}
	if err := host.NewSessionIn(dirB); err != nil {
		t.Fatalf("NewSessionIn: %v", err)
	}
	itemsB, err := host.ListArtifacts()
	if err != nil || len(itemsB) != 0 {
		t.Fatalf("ListArtifacts in session B = (%+v, %v), want empty", itemsB, err)
	}

	// Switch back to sessionA: artifact returns, and DeleteArtifact removes it.
	if err := host.OpenSession(sessionA); err != nil {
		t.Fatalf("OpenSession A: %v", err)
	}
	if err := host.DeleteArtifact("report"); err != nil {
		t.Fatalf("DeleteArtifact: %v", err)
	}
	afterDelete, err := host.ListArtifacts()
	if err != nil || len(afterDelete) != 0 {
		t.Fatalf("afterDelete = (%+v, %v)", afterDelete, err)
	}
}
