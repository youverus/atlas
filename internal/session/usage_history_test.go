package session

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadUsageHistoryAcrossSessions(t *testing.T) {
	home := t.TempDir()
	project := DefaultDir(home, home)
	custom := filepath.Join(home, "custom")
	for _, dir := range []string{project, custom} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, contents string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	const timestamp = `"timestamp":"2026-02-01T00:10:00Z"`
	const assistant = `"type":"message","id":"answer",` + timestamp + `,"message":{"role":"assistant","provider":"p","model":"chat","usage":{"input":900,"output":100,"cacheRead":200},"durationMs":2000}`
	const subagent = `{"type":"usage","id":"work","kind":"subagent","note":"task-1","timestamp":"2026-02-02T00:10:00Z","provider":"p","model":"child","usage":{"totalTokens":500}}`
	original := filepath.Join(project, "original.jsonl")
	originalText := `{"type":"session","id":"original"}` + "\n{" + assistant + "}\n" + subagent
	write(original, originalText)
	write(ArchiveMarker(original), "")
	fork := filepath.Join(custom, "fork.jsonl")
	write(fork, `{"type":"session","id":"fork"}`+"\n{"+assistant+`,"parentId":"rewired"}`+"\n"+subagent+"\n"+
		`{"type":"message","id":"second",`+timestamp+`,"message":{"role":"assistant","provider":"p","model":"chat","usage":{"input":800,"output":200,"totalTokens":1000},"durationMs":1000}}`+"\n"+
		`{"type":"message","id":"old",`+timestamp+`,"message":{"role":"assistant","provider":"q","model":"chat","usage":{"output":75}}}`+"\n"+
		`{"type":"compaction","id":"compaction",`+timestamp+`,"provider":"p","model":"chat","usage":{"totalTokens":25}}`+"\n"+
		`{"type":"message","id":"unfinished"`)
	if err := os.Mkdir(SubagentDir(original), 0o700); err != nil {
		t.Fatal(err)
	}
	delivered := filepath.Join(SubagentDir(original), "task-1.json")
	write(delivered, `{"id":"task-1","entryId":"answer","sessionId":"original","provider":"p","model":"child","usage":{"totalTokens":500}}`)
	finished := time.Date(2026, 2, 1, 1, 0, 0, 0, time.UTC)
	if err := os.Chtimes(delivered, finished, finished); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(SubagentDir(original), "task-2.json")
	write(pending, `{"id":"task-2","entryId":"answer","sessionId":"original","provider":"p","model":"child","usage":{"totalTokens":2000},"durationMs":1234}`)
	completed := time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)
	if err := os.Chtimes(pending, completed, completed); err != nil {
		t.Fatal(err)
	}

	location := time.FixedZone("UTC−08", -8*60*60)
	history, err := ReadUsageHistory(context.Background(), home, []string{custom, project}, []string{fork}, location)
	if err != nil {
		t.Fatal(err)
	}
	wantDays := map[string]int64{"2026-01-31": 2800, "2026-02-01": 2000}
	if !maps.Equal(history.Days, wantDays) {
		t.Fatalf("daily usage = %v, want %v", history.Days, wantDays)
	}
	byModel := make(map[[2]string]ModelUsage)
	for _, model := range history.Models {
		byModel[[2]string{model.Provider, model.Model}] = model
	}
	wantModels := map[[2]string]ModelUsage{
		{"p", "chat"}:  {Provider: "p", Model: "chat", TotalTokens: 2225, TimedOutput: 300, DurationMs: 3000},
		{"q", "chat"}:  {Provider: "q", Model: "chat", TotalTokens: 75},
		{"p", "child"}: {Provider: "p", Model: "child", TotalTokens: 2500},
	}
	if !maps.Equal(byModel, wantModels) {
		t.Fatalf("model usage = %+v, want %+v", byModel, wantModels)
	}
	if data, err := os.ReadFile(original); err != nil || string(data) != originalText {
		t.Fatalf("history collection modified the session file: %v", err)
	}
}
