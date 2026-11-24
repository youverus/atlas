package channel

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"pi-go/internal/assembly"
	"pi-go/internal/wire"
)

// Runner executes one prompt for a chat target and returns the answer text.
// The gateway treats it as the whole kernel: it never touches app, wire events
// or the permission gate directly, which is what keeps a chat adapter from
// growing a second opinion on what a turn is.
type Runner interface {
	// Run drives one turn. The returned text is everything the model said;
	// err carries the turn's failure, and the text may still carry partial
	// output alongside it.
	Run(ctx context.Context, t Target, text string) (string, error)
	// Status is what /status says: model, session file, working directory.
	Status(t Target) string
	// Reset drops the chat's slot and, when cwd is not empty, re-points the
	// binding at that directory. The session goes with it: a session is
	// recorded against one directory, and carrying it to another would split
	// what its header and its turns mean.
	Reset(t Target, cwd string)
	Close() error
}

// ApprovalAsker is the one thing a kernel needs from the chat world: someone
// to show a tool approval to, and an answer that comes back from the chat
// rather than from a keyboard or a blocking POST.
type ApprovalAsker interface {
	AskApproval(t Target, q assembly.Ask) (assembly.Answer, error)
}

// AssemblyRunner is the Runner over real kernels: one assembly kernel per
// chat, built on the chat's first turn and reused after. The kernel is heavy
// (config, model, MCP), which is why the binding — not the message — decides
// its lifetime.
type AssemblyRunner struct {
	configPath string
	version    string
	store      *bindingStore
	asker      ApprovalAsker
	logger     *log.Logger

	mu    sync.Mutex
	slots map[string]*assembly.Kernel
}

func NewAssemblyRunner(configPath, version string, store *bindingStore, asker ApprovalAsker, logger *log.Logger) *AssemblyRunner {
	if logger == nil {
		logger = log.New(os.Stderr, "channel: ", log.LstdFlags)
	}
	return &AssemblyRunner{
		configPath: configPath,
		version:    version,
		store:      store,
		asker:      asker,
		logger:     logger,
		slots:      map[string]*assembly.Kernel{},
	}
}

type slotKey = string

func (r *AssemblyRunner) slotFor(ctx context.Context, t Target) (*assembly.Kernel, Binding, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := t.Key()
	if kernel, ok := r.slots[key]; ok {
		return kernel, r.bindingOf(key), nil
	}
	binding := r.store.ensure(key, t.UserName)
	options := assembly.Options{
		ConfigPath:     r.configPath,
		Workdir:        binding.CWD,
		PermissionMode: "", // the config's mode; the gate asks the chat, not the flag
		Version:        r.version,
		Ask: func(q assembly.Ask) assembly.Answer {
			ans, err := r.asker.AskApproval(t, q)
			if err != nil {
				r.logger.Printf("approval ask for %s failed: %v", key, err)
				return assembly.Answer{} // deny: no answer is no permission
			}
			return ans
		},
	}
	if binding.Session != "" {
		options.SessionPath = binding.Session
	}
	kernel, notes, err := assembly.New(ctx, options)
	if err != nil {
		return nil, binding, err
	}
	for _, note := range notes {
		r.logger.Printf("kernel %s: %s", key, note)
	}
	r.slots[key] = kernel
	return kernel, binding, nil
}

// bindingOf reads without ensure; caller holds r.mu.
func (r *AssemblyRunner) bindingOf(key string) Binding {
	b, _ := r.store.get(key)
	return b
}

// Run drives one turn and returns what the model said.
//
// The text is accumulated from the stream's own deltas rather than lifted
// from the result, so what the chat receives is what was actually said in
// order — the same rule the window's fold follows.
func (r *AssemblyRunner) Run(ctx context.Context, t Target, text string) (string, error) {
	kernel, binding, err := r.slotFor(ctx, t)
	if err != nil {
		return "", fmt.Errorf("build kernel: %w", err)
	}
	var answer strings.Builder
	_, askErr := kernel.App.Ask(ctx, wire.UserMessage(text), func(event wire.AgentEvent) error {
		if event.AssistantEvent != nil && event.AssistantEvent.Type == "text_delta" {
			answer.WriteString(event.AssistantEvent.Delta)
		}
		return nil
	})
	// A session file appears on its first message; from then on the binding
	// records it, so the next turn continues instead of starting over.
	if kernel.Manager.Persisted() {
		if file := kernel.Manager.File(); file != "" && file != binding.Session {
			r.store.setSession(t.Key(), file)
		}
	}
	if askErr != nil {
		return answer.String(), askErr
	}
	return answer.String(), nil
}

func (r *AssemblyRunner) Status(t Target) string {
	r.mu.Lock()
	kernel, ok := r.slots[t.Key()]
	r.mu.Unlock()
	binding, _ := r.store.get(t.Key())
	if !ok {
		where := binding.CWD
		if where == "" {
			where = r.store.defaultCWD
		}
		return fmt.Sprintf("还没有对话。工作目录 %s，说一句话就开始。", where)
	}
	session := kernel.Manager.File()
	if session == "" {
		session = "还没落盘"
	}
	return fmt.Sprintf("模型 %s · 会话 %s · 工作目录 %s", kernel.Selection.Name(), session, kernel.Cwd)
}

// Reset closes the chat's kernel and re-opens the binding. cwd empty keeps
// the current one; a new cwd always starts a new session, because the old one
// is recorded against the old directory.
func (r *AssemblyRunner) Reset(t Target, cwd string) {
	key := t.Key()
	r.mu.Lock()
	if kernel, ok := r.slots[key]; ok {
		kernel.Close()
		delete(r.slots, key)
	}
	r.mu.Unlock()
	if cwd != "" {
		r.store.setCWD(key, cwd)
	}
	r.store.setSession(key, "")
}

// Drop closes and forgets an active chat kernel slot by storage key.
func (r *AssemblyRunner) Drop(key string) {
	r.mu.Lock()
	if kernel, ok := r.slots[key]; ok {
		kernel.Close()
		delete(r.slots, key)
	}
	r.mu.Unlock()
}

func (r *AssemblyRunner) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, kernel := range r.slots {
		kernel.Close()
		delete(r.slots, key)
	}
	return nil
}
