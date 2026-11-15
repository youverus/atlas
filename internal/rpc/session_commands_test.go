package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"pi-go/internal/app"
	"pi-go/internal/provider/faux"
	"pi-go/internal/session"
	"pi-go/internal/tool"
	"pi-go/internal/tool/echo"
	"pi-go/internal/wire"
)

// newDiskHarness mirrors newHarness with a persisted session on disk so tests
// can inspect sessionFile, parentSession, lazy creation, and untouched origin
// files across fork and clone.
func newDiskHarness(t *testing.T, prov *faux.Provider, configure ...func(*app.Config)) *harness {
	t.Helper()

	cfg := app.Config{
		Model:        testModel(),
		Provider:     prov,
		SystemPrompt: app.StaticPrompt("base prompt"),
	}
	for _, fn := range configure {
		fn(&cfg)
	}
	dir := t.TempDir()
	manager, err := session.Create(dir, dir, session.Options{})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	application, err := app.New(cfg, manager)
	if err != nil {
		t.Fatalf("new app: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()

	h := &harness{
		t:      t,
		app:    application,
		prov:   prov,
		frames: newFrameBuffer(),
		in:     inWriter,
		out:    outReader,
		cancel: cancel,
		done:   make(chan struct{}),
	}

	go func() {
		h.serveErr = New(application).Serve(ctx, inReader, outWriter)
		outWriter.Close()
		close(h.done)
	}()
	go func() {
		defer h.frames.close()
		reader := bufio.NewReader(outReader)
		for {
			line, err := reader.ReadBytes('\n')
			if trimmed := bytes.TrimSuffix(line, []byte("\n")); len(bytes.TrimSpace(trimmed)) > 0 {
				h.frames.push(trimmed)
			}
			if err != nil {
				return
			}
		}
	}()

	t.Cleanup(func() {
		inWriter.Close()
		cancel()
		outReader.Close()
		select {
		case <-h.done:
		case <-time.After(readTimeout):
			t.Error("the server did not stop")
		}
		h.frames.close()
	})
	return h
}

func TestServeGetSessionStatsReportsTokensCostAndContextUsage(t *testing.T) {
	prov := faux.New(
		faux.Response{
			ToolCalls: []wire.Content{wire.ToolCallContent("c1", "echo", json.RawMessage(`{"text":"hi"}`))},
			Usage: wire.Usage{
				Input:       100,
				Output:      40,
				CacheRead:   50,
				CacheWrite:  10,
				TotalTokens: 200,
				Cost:        wire.Cost{Total: 0.15},
			},
		},
		faux.Response{
			Text: "done",
			Usage: wire.Usage{
				Input:       220,
				Output:      30,
				CacheRead:   0,
				CacheWrite:  0,
				TotalTokens: 250,
				Cost:        wire.Cost{Total: 0.10},
			},
		},
	)
	h := newDiskHarness(t, prov, func(cfg *app.Config) {
		cfg.Model.ContextWindow = 1000
		cfg.Tools = app.StaticTools([]tool.Tool{echo.New()})
	})

	// Empty session still emits cost: 0 and 0% contextUsage.
	h.send(`{"id":"s0","type":"get_session_stats"}`)
	f0 := h.success("get_session_stats")
	data0 := f0.raw["data"].(map[string]any)
	if _, ok := data0["cost"]; !ok {
		t.Fatalf("stats missing cost key on zero-cost session: %s", f0.Data)
	}
	if _, ok := data0["sessionFile"]; !ok {
		t.Fatalf("disk session missing sessionFile: %s", f0.Data)
	}

	h.sendJSON(map[string]any{"id": "p1", "type": "prompt", "message": "run echo"})
	h.success("prompt")
	h.events("agent_end")

	h.send(`{"id":"s1","type":"get_session_stats"}`)
	var stats struct {
		SessionFile       string  `json:"sessionFile"`
		SessionID         string  `json:"sessionId"`
		UserMessages      int     `json:"userMessages"`
		AssistantMessages int     `json:"assistantMessages"`
		ToolCalls         int     `json:"toolCalls"`
		ToolResults       int     `json:"toolResults"`
		TotalMessages     int     `json:"totalMessages"`
		Cost              float64 `json:"cost"`
		Tokens            struct {
			Input      int `json:"input"`
			Output     int `json:"output"`
			CacheRead  int `json:"cacheRead"`
			CacheWrite int `json:"cacheWrite"`
			Total      int `json:"total"`
		} `json:"tokens"`
		ContextUsage *struct {
			Tokens        *int     `json:"tokens"`
			ContextWindow int      `json:"contextWindow"`
			Percent       *float64 `json:"percent"`
		} `json:"contextUsage"`
	}
	h.success("get_session_stats").data(t, &stats)

	if stats.SessionID != h.app.Session().ID() || stats.SessionFile != h.app.Session().File() {
		t.Fatalf("identity = %q / %q", stats.SessionID, stats.SessionFile)
	}
	if stats.UserMessages != 1 || stats.AssistantMessages != 2 || stats.ToolCalls != 1 ||
		stats.ToolResults != 1 || stats.TotalMessages != 4 {
		t.Fatalf("counts = %+v, want user=1 assistant=2 calls=1 results=1 total=4", stats)
	}
	if stats.Tokens.Input != 320 || stats.Tokens.Output != 70 || stats.Tokens.CacheRead != 50 ||
		stats.Tokens.CacheWrite != 10 || stats.Tokens.Total != 450 {
		t.Fatalf("tokens = %+v, want input=320 output=70 cacheRead=50 cacheWrite=10 total=450", stats.Tokens)
	}
	if stats.Cost < 0.2499 || stats.Cost > 0.2501 {
		t.Fatalf("cost = %v, want 0.25", stats.Cost)
	}
	if stats.ContextUsage == nil || stats.ContextUsage.Tokens == nil || stats.ContextUsage.Percent == nil {
		t.Fatalf("contextUsage = %+v, want populated contextUsage", stats.ContextUsage)
	}
	if *stats.ContextUsage.Tokens != 250 || stats.ContextUsage.ContextWindow != 1000 || *stats.ContextUsage.Percent != 25 {
		t.Fatalf("contextUsage = %+v, want tokens=250 window=1000 percent=25", stats.ContextUsage)
	}
}

func TestServeGetSessionStatsAfterCompactionAndWithoutWindow(t *testing.T) {
	// Without a context window, contextUsage is omitted completely.
	inMem := newHarness(t, faux.New())
	inMem.send(`{"id":"s0","type":"get_session_stats"}`)
	f := inMem.success("get_session_stats")
	data := f.raw["data"].(map[string]any)
	if _, ok := data["contextUsage"]; ok {
		t.Fatalf("contextUsage present without ContextWindow: %s", f.Data)
	}
	if _, ok := data["sessionFile"]; ok {
		t.Fatalf("in-memory session reported sessionFile: %s", f.Data)
	}

	// Immediately after compaction, contextUsage stays present with explicit
	// null tokens and percent while session totals keep pre-compaction +
	// compaction usage.
	h := newHarness(t, faux.New(faux.Response{
		Text:  "HISTORY",
		Usage: wire.Usage{Input: 30, Output: 20, TotalTokens: 50, Cost: wire.Cost{Total: 0.05}},
	}), func(cfg *app.Config) {
		cfg.Model.ContextWindow = 200000
		cfg.Compaction = smallCompaction()
	})
	seedHistory(t, h, 3)

	h.send(`{"id":"c1","type":"compact"}`)
	h.read() // compaction_start
	h.read() // compaction_end
	h.success("compact")

	h.send(`{"id":"s1","type":"get_session_stats"}`)
	f1 := h.success("get_session_stats")
	data1 := f1.raw["data"].(map[string]any)
	cu, ok := data1["contextUsage"].(map[string]any)
	if !ok {
		t.Fatalf("contextUsage missing after compaction: %s", f1.Data)
	}
	if val, has := cu["tokens"]; !has || val != nil {
		t.Fatalf("contextUsage.tokens = %v (has=%v), want explicit null", val, has)
	}
	if val, has := cu["percent"]; !has || val != nil {
		t.Fatalf("contextUsage.percent = %v (has=%v), want explicit null", val, has)
	}
	if cu["contextWindow"] != float64(200000) {
		t.Fatalf("contextWindow = %v, want 200000", cu["contextWindow"])
	}
	tokens := data1["tokens"].(map[string]any)
	if tokens["total"] != float64(50) {
		t.Fatalf("tokens.total = %v, want 50 from compaction", tokens["total"])
	}
}

func TestServeGetForkMessagesAndFork(t *testing.T) {
	prov := faux.New(
		faux.Response{Text: "answer one"},
		faux.Response{Text: "answer two"},
		faux.Response{Text: "answer forked"},
	)
	h := newDiskHarness(t, prov)

	// Empty session returns an empty JSON array, never null.
	h.send(`{"id":"gf0","type":"get_fork_messages"}`)
	emptyFrame := h.success("get_fork_messages")
	if !bytes.Contains(emptyFrame.Data, []byte(`"messages":[]`)) {
		t.Fatalf("empty get_fork_messages = %s, want []", emptyFrame.Data)
	}

	h.sendJSON(map[string]any{"id": "p1", "type": "prompt", "message": "first prompt"})
	h.success("prompt")
	h.events("agent_end")

	h.sendJSON(map[string]any{"id": "p2", "type": "prompt", "message": "second prompt"})
	h.success("prompt")
	h.events("agent_end")

	h.send(`{"id":"gf1","type":"get_fork_messages"}`)
	var forkList struct {
		Messages []struct {
			EntryID string `json:"entryId"`
			Text    string `json:"text"`
		} `json:"messages"`
	}
	h.success("get_fork_messages").data(t, &forkList)
	if len(forkList.Messages) != 2 ||
		forkList.Messages[0].Text != "first prompt" ||
		forkList.Messages[1].Text != "second prompt" {
		t.Fatalf("fork messages = %+v", forkList.Messages)
	}

	originalFile := h.app.Session().File()
	originalBytes, err := os.ReadFile(originalFile)
	if err != nil {
		t.Fatalf("read original file: %v", err)
	}

	// Fork before the second prompt: keeps the first turn and returns the
	// second prompt's text.
	h.sendJSON(map[string]any{"id": "f1", "type": "fork", "entryId": forkList.Messages[1].EntryID})
	forkResp := h.success("fork")
	var forkOut struct {
		Text      string `json:"text"`
		Cancelled *bool  `json:"cancelled"`
	}
	forkResp.data(t, &forkOut)
	if forkOut.Text != "second prompt" || forkOut.Cancelled == nil || *forkOut.Cancelled {
		t.Fatalf("fork data = %s", forkResp.Data)
	}

	h.send(`{"id":"m1","type":"get_messages"}`)
	var messages struct {
		Messages []wire.Message `json:"messages"`
	}
	h.success("get_messages").data(t, &messages)
	if len(messages.Messages) != 2 || messages.Messages[0].Text() != "first prompt" || messages.Messages[1].Text() != "answer one" {
		t.Fatalf("forked messages = %+v, want first turn only", messages.Messages)
	}
	if h.app.Session().Header().ParentSession != originalFile {
		t.Fatalf("parentSession = %q, want %q", h.app.Session().Header().ParentSession, originalFile)
	}

	// Verify the next prompt runs on the forked history and leaves original untouched.
	h.sendJSON(map[string]any{"id": "p3", "type": "prompt", "message": "forked branch"})
	h.success("prompt")
	h.events("agent_end")

	calls := prov.Calls()
	lastReq := calls[len(calls)-1]
	var reqTexts []string
	for _, m := range lastReq.Messages {
		if m.Role != wire.RoleSystem {
			reqTexts = append(reqTexts, m.Text())
		}
	}
	if strings.Join(reqTexts, "|") != "first prompt|answer one|forked branch" {
		t.Fatalf("next turn context = %v", reqTexts)
	}
	afterBytes, err := os.ReadFile(originalFile)
	if err != nil {
		t.Fatalf("read original after fork: %v", err)
	}
	if !bytes.Equal(originalBytes, afterBytes) {
		t.Fatal("original session file was modified after fork")
	}

	// Invalid entryId returns pi's error message.
	h.sendJSON(map[string]any{"id": "f2", "type": "fork", "entryId": "missing"})
	if fail := h.failure("fork"); fail.Error != "Invalid entry ID for forking" {
		t.Fatalf("missing entry error = %q", fail.Error)
	}
}

func TestServeForkAbortsActiveTurnAndHandlesRoot(t *testing.T) {
	block := newBlockTool()
	prov := faux.New(
		faux.Response{Text: "first reply"},
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("call-1", "block", json.RawMessage(`{}`)),
		}},
		faux.Response{Text: "after fork"},
	)
	h := newHarness(t, prov, func(cfg *app.Config) {
		cfg.Tools = app.StaticTools([]tool.Tool{block})
	})

	h.sendJSON(map[string]any{"id": "p1", "type": "prompt", "message": "first"})
	h.success("prompt")
	h.events("agent_end")

	h.send(`{"id":"gf","type":"get_fork_messages"}`)
	var forkList struct {
		Messages []struct {
			EntryID string `json:"entryId"`
		} `json:"messages"`
	}
	h.success("get_fork_messages").data(t, &forkList)
	firstEntry := forkList.Messages[0].EntryID

	h.sendJSON(map[string]any{"id": "p2", "type": "prompt", "message": "blocking"})
	h.success("prompt")
	<-block.started

	// Forking at the first entry while a tool is running aborts the active
	// turn first and leaves the new session completely clean.
	h.sendJSON(map[string]any{"id": "f1", "type": "fork", "entryId": firstEntry})
	var forkOut struct {
		Text      string `json:"text"`
		Cancelled bool   `json:"cancelled"`
	}
	h.success("fork").data(t, &forkOut)
	if forkOut.Text != "first" || forkOut.Cancelled {
		t.Fatalf("fork = %+v", forkOut)
	}

	h.send(`{"id":"st","type":"get_state"}`)
	var state struct {
		IsStreaming  bool `json:"isStreaming"`
		MessageCount int  `json:"messageCount"`
	}
	h.success("get_state").data(t, &state)
	if state.IsStreaming || state.MessageCount != 0 {
		t.Fatalf("state after root fork = %+v, want idle and 0 messages", state)
	}
}

func TestServeCloneValidationAndExecution(t *testing.T) {
	// Empty session rejects clone.
	empty := newHarness(t, faux.New())
	empty.send(`{"id":"c0","type":"clone"}`)
	if fail := empty.failure("clone"); fail.Error != "Cannot clone session: no current entry selected" {
		t.Fatalf("empty clone error = %q", fail.Error)
	}

	// Unsaved disk-backed session (only thinking_level_change) rejects clone.
	unsaved := newDiskHarness(t, faux.New())
	unsaved.send(`{"id":"tl","type":"set_thinking_level","level":"high"}`)
	unsaved.success("set_thinking_level")
	unsaved.send(`{"id":"c1","type":"clone"}`)
	if fail := unsaved.failure("clone"); fail.Error != "This session has not been saved yet. Send a message before cloning or forking it." {
		t.Fatalf("unsaved clone error = %q", fail.Error)
	}

	// Disk-backed session with conversation clones cleanly.
	h := newDiskHarness(t, faux.New(
		faux.Response{Text: "one"},
		faux.Response{Text: "two"},
	))
	h.sendJSON(map[string]any{"id": "p1", "type": "prompt", "message": "hello"})
	h.success("prompt")
	h.events("agent_end")

	oldID := h.app.Session().ID()
	oldFile := h.app.Session().File()
	before, err := os.ReadFile(oldFile)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}

	h.send(`{"id":"c2","type":"clone"}`)
	cloneFrame := h.success("clone")
	var cloneOut struct {
		Cancelled *bool `json:"cancelled"`
	}
	cloneFrame.data(t, &cloneOut)
	if cloneOut.Cancelled == nil || *cloneOut.Cancelled {
		t.Fatalf("clone data = %s, want cancelled=false", cloneFrame.Data)
	}

	h.send(`{"id":"st","type":"get_state"}`)
	var state struct {
		SessionID    string `json:"sessionId"`
		SessionFile  string `json:"sessionFile"`
		MessageCount int    `json:"messageCount"`
	}
	h.success("get_state").data(t, &state)
	if state.SessionID == oldID || state.SessionFile == oldFile || state.MessageCount != 2 {
		t.Fatalf("state after clone = %+v", state)
	}
	if h.app.Session().Header().ParentSession != oldFile {
		t.Fatalf("parentSession = %q, want %q", h.app.Session().Header().ParentSession, oldFile)
	}

	// Prompts after clone write only to the cloned file.
	h.sendJSON(map[string]any{"id": "p2", "type": "prompt", "message": "continued"})
	h.success("prompt")
	h.events("agent_end")

	after, err := os.ReadFile(oldFile)
	if err != nil {
		t.Fatalf("read original after prompt: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("prompt after clone wrote to the original session file")
	}

	// Ensure context cancellation test cleanup is clean even if an unused context
	// is created.
	_ = context.Background()
}
