package channel

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"pi-go/internal/assembly"
)

// Gateway is the process side of the contract: adapters feed it admitted
// messages, it runs turns on a Runner and sends the answers back.
//
// Two kinds of work deliberately bypass the per-chat queue, because both must
// act while a turn is in flight and the worker that runs turns is blocked
// inside it: an approval answer ("y" reaching the gate through the chat) and
// /stop. Everything else — commands, prompts — is serialized per chat, so two
// messages from one chat never race two kernels.
type Gateway struct {
	adapters  map[string]Adapter
	runner    Runner
	store     *bindingStore
	dedupe    *deduper
	approvals *approvals
	queue     chan Inbound
	logger    *log.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	workers map[string]chan Inbound
	turns   map[string]*turnHandle
	started bool
}

type turnHandle struct {
	ctx    context.Context
	cancel context.CancelFunc
}

type Options struct {
	Adapters  []Adapter
	Store     *bindingStore
	Dedupe    *deduper
	QueueSize int
	Logger    *log.Logger
}

func New(opts Options) (*Gateway, error) {
	if len(opts.Adapters) == 0 {
		return nil, errors.New("channel: no adapters configured")
	}
	if opts.Store == nil || opts.Dedupe == nil {
		return nil, errors.New("channel: bindings and dedupe store are required")
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = 256
	}
	byName := make(map[string]Adapter, len(opts.Adapters))
	for _, adapter := range opts.Adapters {
		if _, dup := byName[adapter.Name()]; dup {
			return nil, fmt.Errorf("channel: duplicate adapter %s", adapter.Name())
		}
		byName[adapter.Name()] = adapter
	}
	logger := opts.Logger
	if logger == nil {
		logger = log.New(os.Stderr, "channel: ", log.LstdFlags)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Gateway{
		adapters:  byName,
		store:     opts.Store,
		dedupe:    opts.Dedupe,
		approvals: newApprovals(),
		queue:     make(chan Inbound, opts.QueueSize),
		logger:    logger,
		ctx:       ctx,
		cancel:    cancel,
		workers:   map[string]chan Inbound{},
		turns:     map[string]*turnHandle{},
	}, nil
}

// Start consumes the queue until ctx ends. It returns nil on a clean
// shutdown; the caller Closes the runner afterwards.
func (g *Gateway) Start(ctx context.Context, runner Runner) error {
	if runner == nil {
		return errors.New("channel: no runner")
	}
	g.mu.Lock()
	if g.started {
		g.mu.Unlock()
		return errors.New("channel: already started")
	}
	g.started = true
	g.mu.Unlock()
	g.runner = runner

	for _, adapter := range g.adapters {
		g.wg.Add(1)
		go func(a Adapter) {
			defer g.wg.Done()
			if err := a.Run(ctx, g); err != nil && g.ctx.Err() == nil {
				g.logger.Printf("adapter %s exited: %v", a.Name(), err)
			}
		}(adapter)
	}

	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		for {
			select {
			case <-g.ctx.Done():
				return
			case msg := <-g.queue:
				g.route(msg)
			}
		}
	}()

	<-ctx.Done()
	g.cancel()
	g.Close()
	g.wg.Wait()
	return nil
}

// Inbound implements Sink. The adapter's handler goroutine calls it; a block
// here delays that handler's ack past the platform's redelivery window, which
// the deduper then absorbs — backpressure by way of the platform, and no
// message is silently dropped.
func (g *Gateway) Inbound(msg Inbound) {
	select {
	case g.queue <- msg:
	case <-g.ctx.Done():
	}
}

func (g *Gateway) route(msg Inbound) {
	key := msg.Target.Key()
	// An approval answer is only one while the gate is waiting on it; the
	// same word typed an hour later is prose and goes to the turn.
	if g.approvals.pending(key) {
		if ans, ok := parseApprovalAnswer(msg.Text); ok {
			g.approvals.settle(key, ans)
			g.ackAnswer(msg, ans)
			return
		}
	}
	if cmd, ok := parseCommand(msg.Text); ok && cmd.name == "stop" {
		g.stopTurn(msg, key)
		return
	}
	if !g.dedupe.first(msg.Target.Adapter + ":" + msg.MessageID) {
		g.logger.Printf("dropping duplicate %s %s", msg.Target.Adapter, msg.MessageID)
		return
	}
	g.enqueue(key, msg)
}

// parseApprovalAnswer maps the three words the approval question asked for.
// Anything else stays prose — a message that happens to start with "n" is a
// message, not an answer.
func parseApprovalAnswer(text string) (assembly.Answer, bool) {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "y", "yes":
		return assembly.Answer{Allowed: true}, true
	case "a":
		return assembly.Answer{Allowed: true, Always: true}, true
	case "n", "no":
		return assembly.Answer{}, true
	}
	return assembly.Answer{}, false
}

func (g *Gateway) ackAnswer(msg Inbound, ans assembly.Answer) {
	adapter, ok := g.adapters[msg.Target.Adapter]
	if !ok {
		return
	}
	word := "已拒绝"
	switch {
	case ans.Always:
		word = "已允许，本轮会话内对该工具不再询问"
	case ans.Allowed:
		word = "已允许"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := adapter.Send(ctx, msg.Target, word, ""); err != nil {
		g.logger.Printf("ack to %s failed: %v", msg.Target.Key(), err)
	}
}

func (g *Gateway) enqueue(key string, msg Inbound) {
	g.mu.Lock()
	ch, ok := g.workers[key]
	if !ok {
		ch = make(chan Inbound, 64)
		g.workers[key] = ch
		g.wg.Add(1)
		go g.worker(key, ch)
	}
	g.mu.Unlock()
	select {
	case ch <- msg:
	case <-g.ctx.Done():
	}
}

// worker owns one chat. It runs for the life of the process once the chat
// speaks: a goroutine per conversation is the cheapest correct serialization,
// and idle ones cost nothing.
func (g *Gateway) worker(key string, ch chan Inbound) {
	defer g.wg.Done()
	for {
		select {
		case <-g.ctx.Done():
			return
		case msg := <-ch:
			g.dispatch(msg)
		}
	}
}

func (g *Gateway) dispatch(msg Inbound) {
	if cmd, ok := parseCommand(msg.Text); ok {
		g.command(msg, cmd)
		return
	}
	g.turn(msg)
}

// turn runs one prompt and delivers the answer. The reaction, the chunking
// and the trailing status line are presentation: the kernel's account of the
// turn stays in the session file, this is what the chat sees instead.
func (g *Gateway) turn(msg Inbound) {
	key := msg.Target.Key()
	adapter, ok := g.adapters[msg.Target.Adapter]
	if !ok {
		g.logger.Printf("no adapter %s for %s", msg.Target.Adapter, key)
		return
	}

	turnCtx, cancel := context.WithCancel(g.ctx)
	handle := &turnHandle{ctx: turnCtx, cancel: cancel}
	g.mu.Lock()
	g.turns[key] = handle
	g.mu.Unlock()

	started := time.Now()
	if cycler, ok := adapter.(ReactionLifecycler); ok {
		cycler.ReactStart(turnCtx, msg.Target, msg.MessageID)
	}

	answer, err := g.runner.Run(turnCtx, msg.Target, msg.Text)

	if cycler, ok := adapter.(ReactionLifecycler); ok {
		cycler.ReactEnd(turnCtx, msg.Target, msg.MessageID, err != nil)
	}

	g.mu.Lock()
	delete(g.turns, key)
	g.mu.Unlock()
	cancel()

	out := strings.TrimSpace(answer)
	switch {
	case err != nil && errors.Is(err, context.Canceled):
		if out != "" {
			out += "\n\n"
		}
		out += "（已中止）"
	case err != nil:
		cause := err.Error()
		if len([]rune(cause)) > 200 {
			cause = string([]rune(cause)[:200]) + "…"
		}
		if out != "" {
			out += "\n\n"
		}
		out += "（这一轮出错了：" + cause + "）"
	case out == "":
		out = "（这一轮没有产出文本）"
	default:
		// The receipt rides on the answer instead of being its own message:
		// one bubble per turn is the shape a chat reader expects.
		out += fmt.Sprintf("\n\n—— 耗时 %.1fs", time.Since(started).Seconds())
	}

	caps := adapter.Caps()
	chunks := Chunk(out, caps.MaxRunes)
	for i, chunk := range chunks {
		replyTo := ""
		if i == 0 {
			replyTo = msg.MessageID
		}
		sendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := adapter.Send(sendCtx, msg.Target, chunk, replyTo)
		cancel()
		if err != nil {
			g.logger.Printf("send to %s failed: %v", key, err)
			return
		}
	}
}

// stopTurn runs in the router, not the worker, because the worker is the
// thing that is busy inside the turn being stopped.
func (g *Gateway) stopTurn(msg Inbound, key string) {
	g.mu.Lock()
	handle, running := g.turns[key]
	g.mu.Unlock()
	if running {
		handle.cancel()
	}
	// An approval waiting on this turn dies with it: a cancelled turn never
	// reaches the tool, so an answer arriving now would approve nothing.
	g.approvals.settle(key, assembly.Answer{})
	g.reply(msg, "已中止这一轮")
}

func (g *Gateway) command(msg Inbound, cmd command) {
	switch cmd.name {
	case "new":
		g.runner.Reset(msg.Target, "")
		g.reply(msg, "已开新会话，之前的上下文不再接续。")
	case "status":
		g.reply(msg, g.runner.Status(msg.Target))
	case "cwd":
		g.handleCWD(msg, cmd.arg)
	case "help":
		g.reply(msg, helpText)
	default:
		g.reply(msg, "没有 /"+cmd.name+" 这个命令。\n\n"+helpText)
	}
}

func (g *Gateway) handleCWD(msg Inbound, arg string) {
	if arg == "" {
		g.reply(msg, "/cwd 需要一个目录：/cwd /path/to/project")
		return
	}
	if checker, ok := g.adapters[msg.Target.Adapter].(AdminChecker); ok && !checker.IsAdmin(msg.Target) {
		g.reply(msg, "只有管理员能改工作目录。")
		return
	}
	info, err := os.Stat(arg)
	if err != nil || !info.IsDir() {
		g.reply(msg, "「"+arg+"」不是一个存在的目录。")
		return
	}
	g.runner.Reset(msg.Target, arg)
	g.reply(msg, "工作目录已改为 "+arg+"；会话从这里重新开始。")
}

func (g *Gateway) reply(msg Inbound, text string) {
	adapter, ok := g.adapters[msg.Target.Adapter]
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := adapter.Send(ctx, msg.Target, text, msg.MessageID); err != nil {
		g.logger.Printf("reply to %s failed: %v", msg.Target.Key(), err)
	}
}

// AskApproval implements ApprovalAsker: the kernel's gate parks a tool
// goroutine here while the chat decides. The question is one message; the
// answer is whatever the authorized reader sends back, and silence is a
// denial — the same fallback the window's blocking POST uses.
//
// This runs on the worker goroutine mid-turn, so the answer must arrive
// through the router (which settles pending approvals before enqueueing),
// never through this chat's own queue.
func (g *Gateway) AskApproval(t Target, q assembly.Ask) (assembly.Answer, error) {
	adapter, ok := g.adapters[t.Adapter]
	if !ok {
		return assembly.Answer{}, fmt.Errorf("no adapter %s", t.Adapter)
	}
	key := t.Key()
	question := "需要批准 " + q.Tool + "：" + q.Subject + "\n回答 y（允许）/ a（总是允许）/ n（拒绝）"
	pending := g.approvals.open(key, question)
	defer g.approvals.close(key)

	sendCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := adapter.Send(sendCtx, t, question, ""); err != nil {
		// A question that never reached the chat must not wait five minutes
		// to deny a tool nobody saw — but it is also not the turn's failure.
		g.logger.Printf("approval question to %s failed: %v", key, err)
		return assembly.Answer{}, fmt.Errorf("审批消息发送失败: %w", err)
	}

	var done <-chan struct{}
	g.mu.Lock()
	if handle, ok := g.turns[key]; ok {
		done = handle.ctx.Done()
	}
	g.mu.Unlock()
	return g.approvals.wait(pending, done, 5*time.Minute), nil
}

// Close flushes what must survive a restart and stops the adapters. It is
// called by Start on the way out; calling it twice is harmless.
func (g *Gateway) Close() {
	g.cancel()
	if err := g.dedupe.Close(); err != nil {
		g.logger.Printf("dedupe save: %v", err)
	}
}

// ReactionLifecycler is the optional adapter half of "this is working": a
// per-message mark that appears when the turn starts and settles when it
// ends. It is best-effort everywhere — a reaction that fails is never worth
// the answer it decorates.
type ReactionLifecycler interface {
	ReactStart(ctx context.Context, t Target, messageID string)
	ReactEnd(ctx context.Context, t Target, messageID string, failed bool)
}

// AdminChecker is the optional adapter half of "who may change the chat's
// working directory". Adapters that have no admin concept simply don't
// implement it, and /cwd follows the same rule as everything else.
type AdminChecker interface {
	IsAdmin(t Target) bool
}
