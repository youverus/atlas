package fs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"pi-go/internal/tool"
)

// newCwd builds a temporary directory tree from a name -> content map and
// returns its absolute path. A key ending in "/" creates a directory.
func newCwd(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if name[len(name)-1] == '/' {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", name, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

// pick returns the tool with the given name.
func pick(t *testing.T, cwd, name string) tool.Tool {
	t.Helper()
	for _, candidate := range Tools(Options{Cwd: cwd}) {
		if candidate.Name() == name {
			return candidate
		}
	}
	t.Fatalf("tool %q not registered", name)
	return nil
}

// run executes a tool with JSON arguments.
func run(t *testing.T, impl tool.Tool, args string) tool.Result {
	t.Helper()
	result, err := impl.Execute(context.Background(), tool.Call{
		ID:        "call-1",
		Name:      impl.Name(),
		Arguments: json.RawMessage(args),
	}, func(tool.Partial) {})
	if err != nil {
		t.Fatalf("%s returned error: %v", impl.Name(), err)
	}
	return result
}

// runError executes a tool and returns the error it produced. Tool failures are
// errors here; the agent turns them into isError results.
func runError(t *testing.T, impl tool.Tool, args string) error {
	t.Helper()
	_, err := impl.Execute(context.Background(), tool.Call{
		ID:        "call-1",
		Name:      impl.Name(),
		Arguments: json.RawMessage(args),
	}, func(tool.Partial) {})
	if err == nil {
		t.Fatalf("%s was expected to fail with %s", impl.Name(), args)
	}
	return err
}

// text returns the single text block of a result.
func text(t *testing.T, result tool.Result) string {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Content))
	}
	if result.Content[0].Type != "text" {
		t.Fatalf("expected a text block, got %q", result.Content[0].Type)
	}
	return result.Content[0].Text
}

// readFile reads a file relative to dir.
func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}
