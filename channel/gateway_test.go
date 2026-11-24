package channel

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pi-go/internal/assembly"
)

// fakeAdapter records everything the gateway sends and lets the test push
// inbound messages through the real Sink path.
type fakeAdapter struct {
	name  string
	mu    sync.Mutex
	sink  Sink
	sent  []string
	ready chan struct{}
	once  sync.Once
}

func newFakeAdapter(name string) *fakeAdapter {
	return &fakeAdapter{name: name, ready: make(chan struct{})}
}

func (f *fakeAdapter) Name() string       { return f.name }
func (f *fakeAdapter) Caps() Capabilities { return Capabilities{MaxRunes: DefaultChunkRunes} }
func (f *fakeAdapter) Run(ctx context.Context, sink Sink) error {
	f.mu.Lock()
	f.sink = sink
	f.mu.Unlock()
	f.once.Do(func() { close(f.ready) })
	<-ctx.Done()
	return nil
}

func (f *fakeAdapter) Send(ctx context.Context, t Target, text string, replyTo string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, text)
	return nil
}

func (f *fakeAdapter) push(t *testing.T, msg Inbound) {
	t.Helper()
	f.mu.Lock()
	sink := f.sink
	f.mu.Unlock()
	if sink == nil {
		t.Fatal("adapter not started")
	}
	sink.Inbound(msg)
}

func (f *fakeAdapter) sentTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

func (f *fakeAdapter) waitFor(t *testing.T, contains string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, text := range f.sentTexts() {
			if strings.Contains(text, contains) {
				return text
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no sent message containing %q; got %q", contains, f.sentTexts())
	return ""
}

func (f *fakeAdapter) waitReady(t *testing.T) {
	t.Helper()
	select {
	case <-f.ready:
	case <-time.After(2 * time.Second):
		t.Fatal("adapter never started")
	}
}

// fakeRunner is the whole kernel, in one function.
type fakeRunner struct {
	mu      sync.Mutex
	gw      *Gateway // set after construction; onRun reads it
	prompts []string
	resets  []string
	onRun   func(ctx context.Context, t Target, text string) (string, error)
}

func (r *fakeRunner) Run(ctx context.Context, t Target, text string) (string, error) {
	r.mu.Lock()
	r.prompts = append(r.prompts, text)
	r.mu.Unlock()
	if r.onRun != nil {
		return r.onRun(ctx, t, text)
	}
	return "回答：" + text, nil
}

func (r *fakeRunner) Status(t Target) string {
	return "模型 fake · 会话 none · 工作目录 /fake"
}

func (r *fakeRunner) Reset(t Target, cwd string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resets = append(r.resets, cwd)
}

func (r *fakeRunner) Close() error { return nil }

func (r *fakeRunner) promptList() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.prompts...)
}

func (r *fakeRunner) waitPrompt(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		prompts := r.promptList()
		if len(prompts) > 0 {
			return prompts[len(prompts)-1]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no prompt arrived; got %q", r.promptList())
	return ""
}

func newTestGateway(t *testing.T, adapter Adapter, runner Runner) (*Gateway, context.CancelFunc) {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenBindings(filepath.Join(dir, "bindings.json"), "/default")
	if err != nil {
		t.Fatal(err)
	}
	dedupe, err := OpenDedupe("", 64)
	if err != nil {
		t.Fatal(err)
	}
	gw, err := New(Options{Adapters: []Adapter{adapter}, Store: store, Dedupe: dedupe, QueueSize: 16})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- gw.Start(ctx, runner) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("gateway did not stop")
		}
	})
	return gw, cancel
}

func dmTarget() Target {
	return Target{Adapter: "fake", ChatID: "oc_dm", ChatType: ChatDM, UserID: "ou_1", UserName: "ou_1"}
}

func TestGatewayPromptAnswerFlow(t *testing.T) {
	fa := newFakeAdapter("fake")
	runner := &fakeRunner{}
	newTestGateway(t, fa, runner)
	fa.waitReady(t)

	fa.push(t, Inbound{Target: dmTarget(), MessageID: "om_1", Text: "你好"})
	got := fa.waitFor(t, "回答：你好")
	if !strings.Contains(got, "—— 耗时") {
		t.Fatalf("answer missing the receipt line: %q", got)
	}
	if p := runner.waitPrompt(t); p != "你好" {
		t.Fatalf("prompt = %q", p)
	}

	// A redelivered message is not answered twice.
	fa.push(t, Inbound{Target: dmTarget(), MessageID: "om_1", Text: "你好"})
	time.Sleep(100 * time.Millisecond)
	if got := len(fa.sentTexts()); got != 1 {
		t.Fatalf("duplicate answered %d times", got)
	}

	// A fresh id turns again.
	fa.push(t, Inbound{Target: dmTarget(), MessageID: "om_2", Text: "第二条"})
	fa.waitFor(t, "回答：第二条")
}

func TestGatewayApprovalFlow(t *testing.T) {
	fa := newFakeAdapter("fake")
	runner := &fakeRunner{}
	var decision assembly.Answer
	runner.onRun = func(ctx context.Context, t Target, text string) (string, error) {
		ans, err := runner.gw.AskApproval(t, assembly.Ask{Session: "", Tool: "bash", Subject: "rm -rf build"})
		decision = ans
		if err != nil {
			return "", err
		}
		if ans.Allowed {
			return "跑了", nil
		}
		return "没跑", nil
	}
	gw, _ := newTestGateway(t, fa, runner)
	runner.gw = gw
	fa.waitReady(t)

	fa.push(t, Inbound{Target: dmTarget(), MessageID: "om_10", Text: "清理一下"})
	fa.waitFor(t, "需要批准 bash")

	// Someone else's word is prose while the gate waits; the authorized "y"
	// is the answer that settles it.
	fa.push(t, Inbound{Target: dmTarget(), MessageID: "om_11", Text: "y"})
	fa.waitFor(t, "已允许")
	fa.waitFor(t, "跑了")
	if !decision.Allowed {
		t.Fatalf("AskApproval = %+v", decision)
	}
}

func TestGatewayApprovalDeny(t *testing.T) {
	fa := newFakeAdapter("fake")
	runner := &fakeRunner{}
	runner.onRun = func(ctx context.Context, t Target, text string) (string, error) {
		ans, err := runner.gw.AskApproval(t, assembly.Ask{Tool: "write", Subject: "覆盖文件"})
		if err != nil {
			return "", err
		}
		if ans.Allowed {
			return "写了", nil
		}
		return "没写", nil
	}
	gw, _ := newTestGateway(t, fa, runner)
	runner.gw = gw
	fa.waitReady(t)

	fa.push(t, Inbound{Target: dmTarget(), MessageID: "om_20", Text: "写文件"})
	fa.waitFor(t, "需要批准 write")
	fa.push(t, Inbound{Target: dmTarget(), MessageID: "om_21", Text: "n"})
	fa.waitFor(t, "已拒绝")
	fa.waitFor(t, "没写")
}

func TestGatewayStop(t *testing.T) {
	fa := newFakeAdapter("fake")
	runner := &fakeRunner{}
	runner.onRun = func(ctx context.Context, t Target, text string) (string, error) {
		<-ctx.Done()
		return "部分回答", ctx.Err()
	}
	gw, _ := newTestGateway(t, fa, runner)
	runner.gw = gw
	fa.waitReady(t)

	fa.push(t, Inbound{Target: dmTarget(), MessageID: "om_30", Text: "跑个长活"})
	_ = runner.waitPrompt(t)
	fa.push(t, Inbound{Target: dmTarget(), MessageID: "om_31", Text: "/stop"})
	got := fa.waitFor(t, "已中止这一轮")
	fa.waitFor(t, "部分回答")
	if !strings.Contains(strings.Join(fa.sentTexts(), "\n"), "（已中止）") {
		t.Fatalf("cancelled turn missing the marker: %q", got)
	}
}

func TestGatewayCommands(t *testing.T) {
	fa := newFakeAdapter("fake")
	runner := &fakeRunner{}
	gw, _ := newTestGateway(t, fa, runner)
	runner.gw = gw
	fa.waitReady(t)

	fa.push(t, Inbound{Target: dmTarget(), MessageID: "c1", Text: "/new"})
	fa.waitFor(t, "已开新会话")
	if len(runner.resets) != 1 || runner.resets[0] != "" {
		t.Fatalf("/new resets = %v", runner.resets)
	}

	fa.push(t, Inbound{Target: dmTarget(), MessageID: "c2", Text: "/status"})
	fa.waitFor(t, "模型 fake · 会话 none · 工作目录 /fake")

	fa.push(t, Inbound{Target: dmTarget(), MessageID: "c3", Text: "/help"})
	fa.waitFor(t, "/cwd")

	fa.push(t, Inbound{Target: dmTarget(), MessageID: "c4", Text: "/cwd"})
	fa.waitFor(t, "需要一个目录")

	fa.push(t, Inbound{Target: dmTarget(), MessageID: "c5", Text: "/cwd /definitely/not/here"})
	fa.waitFor(t, "不是一个存在的目录")
	if len(runner.resets) != 1 {
		t.Fatalf("bad /cwd reset the binding anyway: %v", runner.resets)
	}

	fa.push(t, Inbound{Target: dmTarget(), MessageID: "c6", Text: "/cwd /tmp"})
	fa.waitFor(t, "工作目录已改为 /tmp")
	if len(runner.resets) != 2 || runner.resets[1] != "/tmp" {
		t.Fatalf("/cwd resets = %v", runner.resets)
	}

	fa.push(t, Inbound{Target: dmTarget(), MessageID: "c7", Text: "/nope"})
	fa.waitFor(t, "没有 /nope")
}

func TestGatewayApprovalWordIsProseWhenNothingPending(t *testing.T) {
	fa := newFakeAdapter("fake")
	runner := &fakeRunner{}
	gw, _ := newTestGateway(t, fa, runner)
	runner.gw = gw
	fa.waitReady(t)

	fa.push(t, Inbound{Target: dmTarget(), MessageID: "p1", Text: "y"})
	fa.waitFor(t, "回答：y")
}

func TestGatewayErrorReachesTheChat(t *testing.T) {
	fa := newFakeAdapter("fake")
	runner := &fakeRunner{}
	runner.onRun = func(ctx context.Context, t Target, text string) (string, error) {
		return "先说了半句", errors.New("upstream exploded")
	}
	gw, _ := newTestGateway(t, fa, runner)
	runner.gw = gw
	fa.waitReady(t)

	fa.push(t, Inbound{Target: dmTarget(), MessageID: "e1", Text: "跑"})
	got := fa.waitFor(t, "先说了半句")
	if !strings.Contains(got, "（这一轮出错了：upstream exploded）") {
		t.Fatalf("error not surfaced: %q", got)
	}
}

func TestGatewayTurnErrorKeepsChatAlive(t *testing.T) {
	fa := newFakeAdapter("fake")
	runner := &fakeRunner{}
	runner.onRun = func(ctx context.Context, t Target, text string) (string, error) {
		return "", fmt.Errorf("boom %s", text)
	}
	gw, _ := newTestGateway(t, fa, runner)
	runner.gw = gw
	fa.waitReady(t)

	fa.push(t, Inbound{Target: dmTarget(), MessageID: "k1", Text: "第一句"})
	fa.waitFor(t, "boom 第一句")
	fa.push(t, Inbound{Target: dmTarget(), MessageID: "k2", Text: "第二句"})
	fa.waitFor(t, "boom 第二句")
}
