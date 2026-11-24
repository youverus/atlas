// Package scheduler persists and executes time-of-day and interval agent tasks.
package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"pi-go/internal/httpapi"
)

const (
	// FileName is the schedule store file written inside the Atlas home directory.
	FileName = "schedules.json"

	defaultPollInterval = 5 * time.Second
	maxSummaryRunes     = 240
)

// RunRequest describes one scheduled task execution handed to the host runner.
type RunRequest struct {
	TaskID      string
	Name        string
	Prompt      string
	Model       string
	CWD         string
	SessionMode string // "new" | "continue"
	LastSession string
}

// RunResult is returned by RunFunc after one scheduled turn finishes.
type RunResult struct {
	SessionFile string
	Summary     string
}

// RunFunc executes one task prompt against an Atlas session and returns the
// resulting session file path and a short summary.
type RunFunc func(ctx context.Context, req RunRequest) (RunResult, error)

// Options configures a scheduler Engine.
type Options struct {
	Path         string
	DefaultCWD   string
	Run          RunFunc
	Now          func() time.Time
	PollInterval time.Duration
	OnFinished   func(task httpapi.ScheduledTask)
}

type storedTask struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Enabled         bool   `json:"enabled"`
	Prompt          string `json:"prompt"`
	Model           string `json:"model,omitempty"`
	CWD             string `json:"cwd,omitempty"`
	SessionMode     string `json:"sessionMode"`
	ScheduleType    string `json:"scheduleType"`
	Hour            int    `json:"hour"`
	Minute          int    `json:"minute"`
	Weekdays        []int  `json:"weekdays,omitempty"`
	IntervalMinutes int    `json:"intervalMinutes,omitempty"`
	LastSession     string `json:"lastSession,omitempty"`
	LastRunAt       string `json:"lastRunAt,omitempty"`
	LastStatus      string `json:"lastStatus,omitempty"`
	LastError       string `json:"lastError,omitempty"`
	LastSummary     string `json:"lastSummary,omitempty"`
	LastDurationMs  int64  `json:"lastDurationMs,omitempty"`
	RunCount        int    `json:"runCount,omitempty"`
	CreatedAt       string `json:"createdAt,omitempty"`
	UpdatedAt       string `json:"updatedAt,omitempty"`
}

// Engine manages persistent scheduled tasks and drives due executions in the
// background while Atlas is running.
type Engine struct {
	path         string
	defaultCWD   string
	run          RunFunc
	now          func() time.Time
	pollInterval time.Duration
	onFinished   func(task httpapi.ScheduledTask)

	mu       sync.Mutex
	tasks    map[string]*storedTask
	order    []string
	nextRuns map[string]time.Time
	running  map[string]bool

	runCtx context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewEngine loads existing tasks from opts.Path (creating none until mutated)
// and prepares the scheduler engine. Call Start to begin the background loop.
func NewEngine(opts Options) (*Engine, error) {
	if opts.Path == "" {
		return nil, errors.New("scheduler: path is required")
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	poll := opts.PollInterval
	if poll <= 0 {
		poll = defaultPollInterval
	}

	e := &Engine{
		path:         opts.Path,
		defaultCWD:   opts.DefaultCWD,
		run:          opts.Run,
		now:          nowFn,
		pollInterval: poll,
		onFinished:   opts.OnFinished,
		tasks:        make(map[string]*storedTask),
		nextRuns:     make(map[string]time.Time),
		running:      make(map[string]bool),
	}
	if err := e.load(); err != nil {
		return nil, err
	}
	e.recomputeAllNextRunsLocked(e.now())
	return e, nil
}

// Start runs the background polling loop until ctx is cancelled or Stop is called.
func (e *Engine) Start(ctx context.Context) {
	e.mu.Lock()
	if e.cancel != nil {
		e.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	e.runCtx = runCtx
	e.cancel = cancel
	e.recomputeAllNextRunsLocked(e.now())
	e.mu.Unlock()

	e.wg.Add(1)
	go e.loop(runCtx)
}

// Stop stops future triggers, cancels in-flight scheduled runs, and waits for
// all active task goroutines to return.
func (e *Engine) Stop() {
	e.mu.Lock()
	cancel := e.cancel
	e.cancel = nil
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	e.wg.Wait()
}

func (e *Engine) loop(ctx context.Context) {
	defer e.wg.Done()
	ticker := time.NewTicker(e.pollInterval)
	defer ticker.Stop()

	e.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.tick(ctx)
		}
	}
}

func (e *Engine) tick(ctx context.Context) {
	now := e.now()
	var due []string

	e.mu.Lock()
	for _, id := range e.order {
		task := e.tasks[id]
		if task == nil || !task.Enabled || e.running[id] {
			continue
		}
		next := e.nextRuns[id]
		if next.IsZero() {
			next = computeNextRun(task, now)
			e.nextRuns[id] = next
		}
		if !next.IsZero() && !now.Before(next) {
			due = append(due, id)
			if task.ScheduleType == "daily" {
				e.nextRuns[id] = computeNextRun(task, next)
			} else {
				e.nextRuns[id] = now.Add(time.Duration(task.IntervalMinutes) * time.Minute)
			}
			e.running[id] = true
		}
	}
	e.mu.Unlock()

	for _, id := range due {
		e.launch(ctx, id, now)
	}
}

// ListSchedules implements httpapi.SchedulerController.
func (e *Engine) ListSchedules() (httpapi.ScheduleList, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]httpapi.ScheduledTask, 0, len(e.order))
	for _, id := range e.order {
		if task := e.tasks[id]; task != nil {
			out = append(out, e.viewLocked(task))
		}
	}
	return httpapi.ScheduleList{
		DefaultCWD: e.defaultCWD,
		Tasks:      out,
	}, nil
}

// CreateSchedule implements httpapi.SchedulerController.
func (e *Engine) CreateSchedule(input httpapi.ScheduleInput) (httpapi.ScheduledTask, error) {
	norm, err := normalizeInput(input)
	if err != nil {
		return httpapi.ScheduledTask{}, err
	}
	id, err := newTaskID()
	if err != nil {
		return httpapi.ScheduledTask{}, err
	}
	now := e.now()
	stamp := now.Format(time.RFC3339)

	task := &storedTask{
		ID:              id,
		Name:            norm.Name,
		Enabled:         norm.Enabled,
		Prompt:          norm.Prompt,
		Model:           norm.Model,
		CWD:             norm.CWD,
		SessionMode:     norm.SessionMode,
		ScheduleType:    norm.ScheduleType,
		Hour:            norm.Hour,
		Minute:          norm.Minute,
		Weekdays:        norm.Weekdays,
		IntervalMinutes: norm.IntervalMinutes,
		CreatedAt:       stamp,
		UpdatedAt:       stamp,
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.tasks[id] = task
	e.order = append(e.order, id)
	if task.Enabled {
		e.nextRuns[id] = computeNextRun(task, now)
	} else {
		delete(e.nextRuns, id)
	}
	if err := e.saveLocked(); err != nil {
		delete(e.tasks, id)
		e.order = e.order[:len(e.order)-1]
		delete(e.nextRuns, id)
		return httpapi.ScheduledTask{}, err
	}
	return e.viewLocked(task), nil
}

// UpdateSchedule implements httpapi.SchedulerController.
func (e *Engine) UpdateSchedule(id string, input httpapi.ScheduleInput) (httpapi.ScheduledTask, error) {
	id = strings.TrimSpace(id)
	norm, err := normalizeInput(input)
	if err != nil {
		return httpapi.ScheduledTask{}, err
	}
	now := e.now()

	e.mu.Lock()
	defer e.mu.Unlock()
	prev, ok := e.tasks[id]
	if !ok || prev == nil {
		return httpapi.ScheduledTask{}, fmt.Errorf("未找到定时任务 %s", id)
	}
	if e.running[id] {
		return httpapi.ScheduledTask{}, errors.New("任务正在运行中，请等待本次完成后再修改")
	}

	next := *prev
	next.Name = norm.Name
	next.Enabled = norm.Enabled
	next.Prompt = norm.Prompt
	next.Model = norm.Model
	next.CWD = norm.CWD
	if prev.CWD != norm.CWD {
		// Working directory changed, so future continues start from the new directory.
		next.LastSession = ""
	}
	next.SessionMode = norm.SessionMode
	next.ScheduleType = norm.ScheduleType
	next.Hour = norm.Hour
	next.Minute = norm.Minute
	next.Weekdays = norm.Weekdays
	next.IntervalMinutes = norm.IntervalMinutes
	next.UpdatedAt = now.Format(time.RFC3339)

	e.tasks[id] = &next
	if next.Enabled {
		e.nextRuns[id] = computeNextRun(&next, now)
	} else {
		delete(e.nextRuns, id)
	}
	if err := e.saveLocked(); err != nil {
		e.tasks[id] = prev
		e.recomputeTaskNextRunLocked(prev, now)
		return httpapi.ScheduledTask{}, err
	}
	return e.viewLocked(&next), nil
}

// DeleteSchedule implements httpapi.SchedulerController.
func (e *Engine) DeleteSchedule(id string) error {
	id = strings.TrimSpace(id)
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.tasks[id]; !ok {
		return fmt.Errorf("未找到定时任务 %s", id)
	}
	if e.running[id] {
		return errors.New("任务正在运行中，暂时无法删除")
	}
	backupTask := e.tasks[id]
	backupOrder := append([]string(nil), e.order...)
	backupNext, hadNext := e.nextRuns[id]

	delete(e.tasks, id)
	delete(e.nextRuns, id)
	filtered := make([]string, 0, len(e.order))
	for _, item := range e.order {
		if item != id {
			filtered = append(filtered, item)
		}
	}
	e.order = filtered
	if err := e.saveLocked(); err != nil {
		e.tasks[id] = backupTask
		e.order = backupOrder
		if hadNext {
			e.nextRuns[id] = backupNext
		}
		return err
	}
	return nil
}

// RunScheduleNow implements httpapi.SchedulerController. It triggers an
// immediate asynchronous execution and returns the updated task with Running=true.
func (e *Engine) RunScheduleNow(id string) (httpapi.ScheduledTask, error) {
	id = strings.TrimSpace(id)
	now := e.now()

	e.mu.Lock()
	task, ok := e.tasks[id]
	if !ok || task == nil {
		e.mu.Unlock()
		return httpapi.ScheduledTask{}, fmt.Errorf("未找到定时任务 %s", id)
	}
	if e.running[id] {
		e.mu.Unlock()
		return httpapi.ScheduledTask{}, errors.New("该任务正在运行中，请等待本次完成")
	}
	if e.run == nil {
		e.mu.Unlock()
		return httpapi.ScheduledTask{}, errors.New("当前构建未启用任务执行器")
	}
	e.running[id] = true
	if task.Enabled && task.ScheduleType == "interval" {
		e.nextRuns[id] = now.Add(time.Duration(task.IntervalMinutes) * time.Minute)
	}
	ctx := e.runCtx
	if ctx == nil {
		ctx = context.Background()
	}
	view := e.viewLocked(task)
	e.mu.Unlock()

	e.launch(ctx, id, now)
	return view, nil
}

func (e *Engine) launch(ctx context.Context, id string, startedAt time.Time) {
	e.mu.Lock()
	task, ok := e.tasks[id]
	if !ok || task == nil || e.run == nil {
		delete(e.running, id)
		e.mu.Unlock()
		return
	}
	cwd := task.CWD
	if cwd == "" {
		cwd = e.defaultCWD
	}
	req := RunRequest{
		TaskID:      task.ID,
		Name:        task.Name,
		Prompt:      task.Prompt,
		Model:       task.Model,
		CWD:         cwd,
		SessionMode: task.SessionMode,
		LastSession: task.LastSession,
	}
	e.mu.Unlock()

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		result, err := e.run(ctx, req)
		finishedAt := e.now()
		durationMs := finishedAt.Sub(startedAt).Milliseconds()
		if durationMs < 0 {
			durationMs = 0
		}

		e.mu.Lock()
		delete(e.running, id)
		var callbackTask httpapi.ScheduledTask
		var notify bool
		if current := e.tasks[id]; current != nil {
			current.RunCount++
			current.LastRunAt = finishedAt.Format(time.RFC3339)
			current.LastDurationMs = durationMs
			current.UpdatedAt = finishedAt.Format(time.RFC3339)
			if result.SessionFile != "" {
				current.LastSession = result.SessionFile
			}
			if err != nil {
				current.LastStatus = "error"
				current.LastError = err.Error()
			} else {
				current.LastStatus = "success"
				current.LastError = ""
				current.LastSummary = clipSummary(result.Summary)
			}
			if current.Enabled && current.ScheduleType == "interval" {
				// Prevent immediate overlap if an interval run took longer than intervalMinutes.
				if next, hasNext := e.nextRuns[id]; !hasNext || !next.After(finishedAt) {
					e.nextRuns[id] = finishedAt.Add(time.Duration(current.IntervalMinutes) * time.Minute)
				}
			}
			_ = e.saveLocked()
			callbackTask = e.viewLocked(current)
			notify = e.onFinished != nil
		}
		e.mu.Unlock()

		if notify {
			e.onFinished(callbackTask)
		}
	}()
}

func (e *Engine) recomputeAllNextRunsLocked(now time.Time) {
	for _, id := range e.order {
		if task := e.tasks[id]; task != nil {
			e.recomputeTaskNextRunLocked(task, now)
		}
	}
}

func (e *Engine) recomputeTaskNextRunLocked(task *storedTask, now time.Time) {
	if !task.Enabled {
		delete(e.nextRuns, task.ID)
		return
	}
	e.nextRuns[task.ID] = computeNextRun(task, now)
}

func (e *Engine) viewLocked(t *storedTask) httpapi.ScheduledTask {
	nextStr := ""
	if t.Enabled {
		if next, ok := e.nextRuns[t.ID]; ok && !next.IsZero() {
			nextStr = next.Format(time.RFC3339)
		}
	}
	return httpapi.ScheduledTask{
		ID:              t.ID,
		Name:            t.Name,
		Enabled:         t.Enabled,
		Prompt:          t.Prompt,
		Model:           t.Model,
		CWD:             t.CWD,
		SessionMode:     t.SessionMode,
		ScheduleType:    t.ScheduleType,
		Hour:            t.Hour,
		Minute:          t.Minute,
		Weekdays:        append([]int(nil), t.Weekdays...),
		IntervalMinutes: t.IntervalMinutes,
		Running:         e.running[t.ID],
		LastSession:     t.LastSession,
		LastRunAt:       t.LastRunAt,
		NextRunAt:       nextStr,
		LastStatus:      t.LastStatus,
		LastError:       t.LastError,
		LastSummary:     t.LastSummary,
		LastDurationMs:  t.LastDurationMs,
		RunCount:        t.RunCount,
		CreatedAt:       t.CreatedAt,
		UpdatedAt:       t.UpdatedAt,
	}
}

func (e *Engine) load() error {
	raw, err := os.ReadFile(e.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var list []*storedTask
	if err := json.Unmarshal(raw, &list); err != nil {
		return fmt.Errorf("schedules %s: %w", e.path, err)
	}
	for _, task := range list {
		if task == nil || task.ID == "" {
			continue
		}
		if task.SessionMode == "" {
			task.SessionMode = "new"
		}
		if task.ScheduleType == "" {
			task.ScheduleType = "daily"
		}
		e.tasks[task.ID] = task
		e.order = append(e.order, task.ID)
	}
	return nil
}

func (e *Engine) saveLocked() error {
	list := make([]*storedTask, 0, len(e.order))
	for _, id := range e.order {
		if task := e.tasks[id]; task != nil {
			list = append(list, task)
		}
	}
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(e.path), 0o700); err != nil {
		return err
	}
	tmp := e.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, e.path)
}

func computeNextRun(t *storedTask, after time.Time) time.Time {
	if !t.Enabled {
		return time.Time{}
	}
	if t.ScheduleType == "interval" {
		mins := t.IntervalMinutes
		if mins < 1 {
			mins = 1
		}
		return after.Add(time.Duration(mins) * time.Minute)
	}

	allowed := make(map[time.Weekday]bool, len(t.Weekdays))
	for _, wd := range t.Weekdays {
		if wd >= 0 && wd <= 6 {
			allowed[time.Weekday(wd)] = true
		}
	}
	loc := after.Location()
	for dayOffset := 0; dayOffset <= 7; dayOffset++ {
		day := after.AddDate(0, 0, dayOffset)
		if len(allowed) > 0 && !allowed[day.Weekday()] {
			continue
		}
		candidate := time.Date(day.Year(), day.Month(), day.Day(), t.Hour, t.Minute, 0, 0, loc)
		if candidate.After(after) {
			return candidate
		}
	}
	return after.AddDate(0, 0, 1)
}

func normalizeInput(input httpapi.ScheduleInput) (httpapi.ScheduleInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return input, errors.New("任务名称不能为空")
	}
	input.Prompt = strings.TrimSpace(input.Prompt)
	if input.Prompt == "" {
		return input, errors.New("任务提示词不能为空")
	}
	input.Model = strings.TrimSpace(input.Model)

	cwd := strings.TrimSpace(input.CWD)
	if cwd != "" {
		expanded, err := expandHome(cwd)
		if err != nil {
			return input, err
		}
		info, err := os.Stat(expanded)
		if err != nil || !info.IsDir() {
			return input, fmt.Errorf("启动目录不存在或不是文件夹: %s", cwd)
		}
		cwd = expanded
	}
	input.CWD = cwd

	switch strings.TrimSpace(input.SessionMode) {
	case "", "new":
		input.SessionMode = "new"
	case "continue":
		input.SessionMode = "continue"
	default:
		return input, fmt.Errorf("无效的会话模式: %s", input.SessionMode)
	}

	switch strings.TrimSpace(input.ScheduleType) {
	case "", "daily":
		input.ScheduleType = "daily"
		if input.Hour < 0 || input.Hour > 23 {
			return input, errors.New("小时必须在 0 到 23 之间")
		}
		if input.Minute < 0 || input.Minute > 59 {
			return input, errors.New("分钟必须在 0 到 59 之间")
		}
		seen := make(map[int]bool)
		var days []int
		for _, d := range input.Weekdays {
			if d < 0 || d > 6 {
				return input, fmt.Errorf("无效的星期数值: %d", d)
			}
			if !seen[d] {
				seen[d] = true
				days = append(days, d)
			}
		}
		sort.Ints(days)
		input.Weekdays = days
		input.IntervalMinutes = 0
	case "interval":
		input.ScheduleType = "interval"
		if input.IntervalMinutes < 1 {
			return input, errors.New("执行间隔至少为 1 分钟")
		}
		input.Weekdays = nil
	default:
		return input, fmt.Errorf("无效的调度类型: %s", input.ScheduleType)
	}

	return input, nil
}

func expandHome(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

func newTaskID() (string, error) {
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "task_" + hex.EncodeToString(raw[:]), nil
}

func clipSummary(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) <= maxSummaryRunes {
		return s
	}
	return string(runes[:maxSummaryRunes]) + "..."
}
