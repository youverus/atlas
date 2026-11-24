package assembly

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"pi-go/internal/app"
	"pi-go/internal/config"
	"pi-go/internal/permission"
	"pi-go/internal/provider"
	"pi-go/internal/session"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

const (
	subagentQueued    = "queued"
	subagentRunning   = "running"
	subagentSucceeded = "succeeded"
	subagentFailed    = "failed"
	subagentCanceled  = "canceled"
	subagentLost      = "lost"

	subagentTurnLimit    = 4
	subagentSessionLimit = 2
	subagentProcessLimit = 8
	subagentQueueLimit   = 32
)

var (
	errSubagentStopped = errors.New("stopped by the host")
	errSubagentClosed  = errors.New("subagent runner closed")

	// These limits span kernels in one process, including concurrent live sessions.
	subagentAdmissions = make(chan struct{}, subagentQueueLimit+subagentProcessLimit)
	subagentWorkers    = make(chan struct{}, subagentProcessLimit)
	subagentSessions   = struct {
		sync.Mutex
		byID map[string]*subagentSessionSlots
	}{byID: make(map[string]*subagentSessionSlots)}
)

var _ app.SubagentRuntime = (*subagentRunner)(nil)

type subagentSessionSlots struct {
	slots chan struct{}
	users int
}

func acquireSubagentSession(sessionID string) (*subagentSessionSlots, func()) {
	subagentSessions.Lock()
	defer subagentSessions.Unlock()
	entry := subagentSessions.byID[sessionID]
	if entry == nil {
		entry = &subagentSessionSlots{slots: make(chan struct{}, subagentSessionLimit)}
		subagentSessions.byID[sessionID] = entry
	}
	entry.users++
	return entry, func() {
		subagentSessions.Lock()
		defer subagentSessions.Unlock()
		entry.users--
		if entry.users == 0 {
			delete(subagentSessions.byID, sessionID)
		}
	}
}

type subagentRunner struct {
	mu       sync.Mutex
	wg       sync.WaitGroup
	cfg      *config.Config
	engine   *permission.Engine
	observer func(wire.SubagentUpdate)
	jobs     map[string]*subagentJob
	unstored map[string]*subagentJob
	closed   bool
}

type subagentJob struct {
	update   wire.SubagentUpdate
	model    wire.Model
	provider provider.Provider
	thinking string
	prompt   string
	root     *os.Root
	tools    map[string]bool
	path     string
	lease    *os.File
	ctx      context.Context
	cancel   context.CancelCauseFunc
}

type subagentStartTool struct {
	runner *subagentRunner
	turn   app.SubagentTurn
	roles  []string
	tools  map[string]bool

	// Calls in one parent tool batch can execute in parallel. The runner mutex
	// serializes acceptance with the turn's cleanup and with Commit.
	pending  []string
	accepted int
	closed   bool
}

func newSubagentRunner(cfg *config.Config, engine *permission.Engine) *subagentRunner {
	return &subagentRunner{
		cfg: cfg, engine: engine, jobs: make(map[string]*subagentJob), unstored: make(map[string]*subagentJob),
	}
}

func (r *subagentRunner) configure(cfg *config.Config) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfg = cfg
}

func (r *subagentRunner) Prepare(turn app.SubagentTurn) (tool.Tool, func()) {
	launch := &subagentStartTool{runner: r, turn: turn, tools: make(map[string]bool)}
	for _, role := range []string{wire.SubagentExplorer, wire.SubagentVerifier} {
		if len(turn.Roles) == 0 || slices.Contains(turn.Roles, role) {
			launch.roles = append(launch.roles, role)
		}
	}
	for _, impl := range turn.Tools {
		launch.tools[impl.Name()] = true
	}
	if turn.Override != nil {
		override := *turn.Override
		launch.turn.Override = &override
	}
	return launch, launch.cleanup
}

func (t *subagentStartTool) register(ctx context.Context, call tool.Call, args subagentArgs) (wire.SubagentUpdate, error) {
	r := t.runner
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return wire.SubagentUpdate{}, err
	}
	if r.closed || t.closed {
		return wire.SubagentUpdate{}, errors.New("subagent: this turn no longer accepts delegations")
	}
	if t.accepted >= subagentTurnLimit {
		return wire.SubagentUpdate{}, fmt.Errorf("subagent: this turn has reached its limit of %d delegations", subagentTurnLimit)
	}
	if t.turn.SessionFile == "" || t.turn.SessionID == "" || !filepath.IsAbs(t.turn.Cwd) {
		return wire.SubagentUpdate{}, errors.New("subagent: a persistent parent session and an absolute workspace path are required")
	}
	model, adapter, level, err := subagentModel(r.cfg, t.turn, args.Role)
	if err != nil {
		return wire.SubagentUpdate{}, err
	}
	model.Input = slices.Clone(model.Input)
	select {
	case subagentAdmissions <- struct{}{}:
	default:
		return wire.SubagentUpdate{}, errors.New("subagent: the background queue is full")
	}
	job, err := newSubagentJob(t, call.ID, args, model, adapter, level)
	if err != nil {
		<-subagentAdmissions
		return wire.SubagentUpdate{}, err
	}
	r.jobs[job.update.ID] = job
	t.pending = append(t.pending, job.update.ID)
	t.accepted++
	return job.update, nil
}

func newSubagentJob(t *subagentStartTool, callID string, args subagentArgs, model wire.Model, adapter provider.Provider, level string) (*subagentJob, error) {
	root, err := os.OpenRoot(t.turn.Cwd)
	if err != nil {
		return nil, fmt.Errorf("subagent: open workspace: %w", err)
	}
	id := rand.Text()
	up := wire.SubagentUpdate{
		ID: id, SessionID: t.turn.SessionID, SessionFile: t.turn.SessionFile, ToolCallID: callID,
		Role: args.Role, Description: args.Description, State: subagentQueued, Provider: model.Provider, Model: model.ID,
	}
	path := filepath.Join(session.SubagentDir(up.SessionFile), id+".json")
	lease, err := createSubagentRecord(path, up)
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("subagent: record delegation: %w", err)
	}
	// The parent's tool context ends with its turn. Only the runner, an explicit
	// stop, or the execution deadline can cancel a committed job.
	ctx, cancel := context.WithCancelCause(context.Background())
	return &subagentJob{
		update: up, model: model, provider: adapter, thinking: level, prompt: args.Prompt,
		root: root, tools: t.tools, path: path, lease: lease, ctx: ctx, cancel: cancel,
	}, nil
}

func (t *subagentStartTool) cleanup() {
	r := t.runner
	r.mu.Lock()
	defer r.mu.Unlock()
	t.closed = true
	for _, id := range t.pending {
		job := r.jobs[id]
		if job == nil || job.update.EntryID != "" {
			continue
		}
		delete(r.jobs, id)
		job.cancel(context.Canceled)
		_ = job.root.Close()
		_ = os.Remove(job.path)
		releaseSubagentLease(job.path, job.lease, true)
		<-subagentAdmissions
	}
	t.pending = nil
}

func (r *subagentRunner) Commit(sessionID, taskID, entryID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job := r.jobs[taskID]
	if r.closed || job == nil || job.update.SessionID != sessionID || job.update.EntryID != "" || entryID == "" {
		return
	}
	job.update.EntryID = entryID
	persistErr := writeSubagentRecord(job.path, job.update)
	r.wg.Add(1)
	go r.run(job, persistErr)
}

func (r *subagentRunner) stop(sessionID, taskID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	job := r.jobs[taskID]
	if job == nil || job.update.SessionID != sessionID || job.update.EntryID == "" {
		return fmt.Errorf("subagent: no active task %q in this session", taskID)
	}
	job.cancel(errSubagentStopped)
	return nil
}

func (r *subagentRunner) close() {
	r.mu.Lock()
	r.closed = true
	for id, job := range r.jobs {
		job.cancel(errSubagentClosed)
		if job.update.EntryID == "" {
			// A receipt may be in the process of being persisted by the parent.
			// Leave recovery to match the record against the on-disk receipt.
			delete(r.jobs, id)
			_ = job.root.Close()
			releaseSubagentLease(job.path, job.lease, false)
			<-subagentAdmissions
		}
	}
	r.mu.Unlock()
	r.wg.Wait()

	r.mu.Lock()
	defer r.mu.Unlock()
	for id, job := range r.unstored {
		err := writeSubagentRecord(job.path, job.update)
		releaseSubagentLease(job.path, job.lease, err == nil)
		<-subagentAdmissions
		delete(r.unstored, id)
	}
}

func (r *subagentRunner) setObserver(observer func(wire.SubagentUpdate)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observer = observer
}

func (r *subagentRunner) observe(up wire.SubagentUpdate) {
	r.mu.Lock()
	observer := r.observer
	r.mu.Unlock()
	if observer != nil {
		if up.Usage != nil {
			var usage wire.Usage
			usage.Add(*up.Usage)
			up.Usage = &usage
		}
		observer(up)
	}
}

func (r *subagentRunner) finish(job *subagentJob, up wire.SubagentUpdate) wire.SubagentUpdate {
	_ = job.root.Close()
	job.root, job.tools, job.provider, job.prompt = nil, nil, nil, ""
	r.mu.Lock()
	defer r.mu.Unlock()
	if up.State == subagentSucceeded && job.ctx.Err() != nil {
		up = canceledSubagent(up, job.ctx)
	}
	job.cancel(context.Canceled)
	delete(r.jobs, up.ID)
	if err := writeSubagentRecord(job.path, up); err != nil && !errors.Is(err, os.ErrNotExist) {
		// Hold the lease until the next read can retry. A different process
		// should not declare a locally available completion lost.
		job.update = up
		r.unstored[up.ID] = job
		return up
	}
	releaseSubagentLease(job.path, job.lease, true)
	<-subagentAdmissions
	return up
}

func (r *subagentRunner) Completed(sessionID, sessionFile string) ([]wire.SubagentUpdate, error) {
	if sessionID == "" || sessionFile == "" {
		return nil, nil
	}
	r.mu.Lock()
	var local []wire.SubagentUpdate
	for id, job := range r.unstored {
		up := job.update
		if up.SessionID != sessionID || up.SessionFile != sessionFile {
			continue
		}
		if err := writeSubagentRecord(job.path, up); err != nil && !errors.Is(err, os.ErrNotExist) {
			local = append(local, up)
			continue
		}
		releaseSubagentLease(job.path, job.lease, true)
		<-subagentAdmissions
		delete(r.unstored, id)
	}
	r.mu.Unlock()

	stored, err := completedSubagentRecords(sessionID, sessionFile)
	if err != nil {
		return nil, err
	}
	for _, up := range local {
		if !containsSubagentUpdate(stored, up.ID) {
			stored = append(stored, up)
		}
	}
	return stored, nil
}

func containsSubagentUpdate(updates []wire.SubagentUpdate, id string) bool {
	for _, up := range updates {
		if up.ID == id {
			return true
		}
	}
	return false
}

func subagentReceipt(update wire.SubagentUpdate) tool.Result {
	details, _ := json.Marshal(update)
	return tool.Result{
		Content: []wire.Content{wire.TextContent(fmt.Sprintf("Queued background %s task %s (%s). Its report will arrive in this conversation.", update.Role, update.ID, update.Description))},
		Details: details,
	}
}
