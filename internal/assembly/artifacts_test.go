package assembly

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"pi-go/internal/tool"
)

func TestKernelRegistersArtifactToolsAndExportsSafely(t *testing.T) {
	t.Setenv("ATLAS_HOME", t.TempDir())

	cfgPath := filepath.Join(t.TempDir(), "config.json")
	writeConfigFile(t, cfgPath, `{
		"defaultModel": "test/m1",
		"providers": {
			"test": {
				"api": "openai-completions",
				"baseUrl": "https://example.com/v1",
				"apiKey": "sk-test",
				"models": [{"id": "m1"}]
			}
		}
	}`)

	cwd := t.TempDir()
	kernel, _, err := New(context.Background(), Options{
		ConfigPath: cfgPath,
		Workdir:    cwd,
		NoMCP:      true,
	})
	if err != nil {
		t.Fatalf("assembly.New: %v", err)
	}
	defer kernel.Close()

	tools := kernel.Tools(cwd)
	names := make([]string, 0, len(tools))
	var publishTool tool.Tool
	for _, impl := range tools {
		names = append(names, impl.Name())
		if impl.Name() == "artifact_publish" {
			publishTool = impl
		}
	}
	for _, want := range []string{"artifact_publish", "artifact_patch", "artifact_list", "artifact_read"} {
		if !slices.Contains(names, want) {
			t.Fatalf("missing tool %q in %v", want, names)
		}
	}
	if publishTool == nil {
		t.Fatal("artifact_publish tool not found")
	}

	sess := kernel.App.Session()
	initial, err := kernel.ListArtifacts(sess.ID(), sess.File())
	if err != nil || len(initial) != 0 {
		t.Fatalf("initial ListArtifacts = %+v, err=%v", initial, err)
	}

	// Execute artifact_publish without SessionContext in ctx to verify kernel fallback resolution.
	res, err := publishTool.Execute(context.Background(), tool.Call{
		ID:        "c1",
		Name:      "artifact_publish",
		Arguments: json.RawMessage(`{"id":"demo","kind":"html","title":"Demo","content":"<h1>Hi</h1>"}`),
	}, func(tool.Partial) {})
	if err != nil || res.IsError {
		t.Fatalf("publishTool.Execute = (%+v, %v)", res, err)
	}

	items, err := kernel.ListArtifacts(sess.ID(), sess.File())
	if err != nil || len(items) != 1 || items[0].ID != "demo" || items[0].Version != 1 {
		t.Fatalf("ListArtifacts = %+v, err=%v", items, err)
	}

	detail, err := kernel.GetArtifact(sess.ID(), sess.File(), "demo", 0)
	if err != nil || detail.Content != "<h1>Hi</h1>" || detail.ActiveVersion != 1 || len(detail.Versions) != 1 {
		t.Fatalf("GetArtifact = %+v, err=%v", detail, err)
	}

	// Export with empty relPath defaults to artifacts/<id>.<ext>.
	exported, err := kernel.ExportArtifact(sess.ID(), sess.File(), cwd, "demo", 0, "")
	if err != nil {
		t.Fatalf("ExportArtifact default: %v", err)
	}
	if exported.Path != "artifacts/demo.html" || exported.Bytes != len("<h1>Hi</h1>") {
		t.Fatalf("exported = %+v", exported)
	}
	gotBytes, err := os.ReadFile(filepath.Join(cwd, "artifacts", "demo.html"))
	if err != nil || string(gotBytes) != "<h1>Hi</h1>" {
		t.Fatalf("exported file = %q, err=%v", string(gotBytes), err)
	}

	// Export path traversal must be rejected.
	if _, err := kernel.ExportArtifact(sess.ID(), sess.File(), cwd, "demo", 0, "../escape.html"); err == nil {
		t.Fatal("expected traversal export path to fail")
	}

	// Export through a symlinked directory escaping workspace must be rejected.
	outside := t.TempDir()
	symDir := filepath.Join(cwd, "linkout")
	if err := os.Symlink(outside, symDir); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := kernel.ExportArtifact(sess.ID(), sess.File(), cwd, "demo", 0, "linkout/out.html"); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected symlink escape error, got %v", err)
	}

	// Export directly onto a symlink file must be rejected.
	symFile := filepath.Join(cwd, "sym.html")
	if err := os.Symlink(filepath.Join(outside, "target.html"), symFile); err != nil {
		t.Fatalf("symlink file: %v", err)
	}
	if _, err := kernel.ExportArtifact(sess.ID(), sess.File(), cwd, "demo", 0, "sym.html"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink target error, got %v", err)
	}

	// Delete removes the artifact from the session store.
	if err := kernel.DeleteArtifact(sess.ID(), sess.File(), "demo"); err != nil {
		t.Fatalf("DeleteArtifact: %v", err)
	}
	afterDelete, err := kernel.ListArtifacts(sess.ID(), sess.File())
	if err != nil || len(afterDelete) != 0 {
		t.Fatalf("afterDelete = %+v, err=%v", afterDelete, err)
	}
}
