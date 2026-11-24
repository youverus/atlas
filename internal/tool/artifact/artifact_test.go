package artifact

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/artifact"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

func toolByName(tools []tool.Tool, name string) tool.Tool {
	for _, t := range tools {
		if t.Name() == name {
			return t
		}
	}
	return nil
}

func TestToolsPublishPatchListReadAndSourceConfinement(t *testing.T) {
	cwd := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "chart.svg"), []byte(`<svg viewBox="0 0 10 10"><rect width="10" height="10"/></svg>`), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink(outside, filepath.Join(cwd, "escape.txt"))

	repo := artifact.NewRepo()
	tools := Tools(Options{
		Cwd: cwd,
		ResolveStore: func(ctx context.Context) (*artifact.Store, error) {
			sess, ok := tool.SessionFromContext(ctx)
			if !ok {
				return repo.ForSession("fallback", ""), nil
			}
			return repo.ForSession(sess.ID, sess.File), nil
		},
	})

	sessFile := filepath.Join(t.TempDir(), "sess.jsonl")
	ctx := tool.WithSession(context.Background(), tool.SessionContext{ID: "s1", File: sessFile, Cwd: cwd})
	noop := func(tool.Partial) {}

	publish := toolByName(tools, wire.ArtifactPublishToolName)
	patch := toolByName(tools, wire.ArtifactPatchToolName)
	list := toolByName(tools, wire.ArtifactListToolName)
	read := toolByName(tools, wire.ArtifactReadToolName)

	// Publish inline HTML.
	res, err := publish.Execute(ctx, tool.Call{
		ID:        "c1",
		Name:      wire.ArtifactPublishToolName,
		Arguments: json.RawMessage(`{"id":"calc","kind":"html","title":"Calculator","content":"<div>1 + 1</div>"}`),
	}, noop)
	if err != nil {
		t.Fatalf("publish inline: %v", err)
	}
	var ref wire.ArtifactRef
	if err := json.Unmarshal(res.Details, &ref); err != nil {
		t.Fatalf("unmarshal details: %v", err)
	}
	if ref.ID != "calc" || ref.Version != 1 || ref.Action != "publish" {
		t.Fatalf("unexpected ref: %+v", ref)
	}

	// Patch HTML to v2.
	res2, err := patch.Execute(ctx, tool.Call{
		ID:        "c2",
		Name:      wire.ArtifactPatchToolName,
		Arguments: json.RawMessage(`{"id":"calc","summary":"update calc","edits":[{"old_text":"1 + 1","new_text":"2 + 2"}]}`),
	}, noop)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if err := json.Unmarshal(res2.Details, &ref); err != nil || ref.Version != 2 || ref.Action != "patch" {
		t.Fatalf("unexpected patch ref: %+v (err=%v)", ref, err)
	}

	// Publish from valid confined source_path.
	if _, err := publish.Execute(ctx, tool.Call{
		ID:        "c3",
		Name:      wire.ArtifactPublishToolName,
		Arguments: json.RawMessage(`{"id":"logo","kind":"svg","source_path":"chart.svg"}`),
	}, noop); err != nil {
		t.Fatalf("publish source_path: %v", err)
	}

	// Reject path traversal and symlink escape.
	for _, bad := range []string{`../secret.txt`, outside, `escape.txt`} {
		args, _ := json.Marshal(map[string]string{"id": "bad", "kind": "code", "source_path": bad})
		if _, err := publish.Execute(ctx, tool.Call{ID: "c-bad", Arguments: args}, noop); err == nil {
			t.Fatalf("expected error for source_path %q", bad)
		}
	}

	// List & Read verify session isolation too.
	listRes, err := list.Execute(ctx, tool.Call{ID: "c4", Arguments: json.RawMessage(`{}`)}, noop)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	listText := listRes.Content[0].Text
	if !strings.Contains(listText, "calc (html, v2") || !strings.Contains(listText, "logo (svg, v1") {
		t.Fatalf("unexpected list output: %s", listText)
	}

	readRes, err := read.Execute(ctx, tool.Call{ID: "c5", Arguments: json.RawMessage(`{"id":"calc","version":1}`)}, noop)
	if err != nil || !strings.Contains(readRes.Content[0].Text, "1 + 1") {
		t.Fatalf("read v1: %v, %+v", err, readRes.Content)
	}

	// A different session sees no artifacts from s1.
	ctx2 := tool.WithSession(context.Background(), tool.SessionContext{ID: "s2", File: filepath.Join(t.TempDir(), "s2.jsonl"), Cwd: cwd})
	list2, err := list.Execute(ctx2, tool.Call{ID: "c6", Arguments: json.RawMessage(`{}`)}, noop)
	if err != nil || !strings.Contains(list2.Content[0].Text, "No artifacts") {
		t.Fatalf("expected isolated store for s2, got %v, %+v", err, list2.Content)
	}
}
