package scheduler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pi-go/internal/httpapi"
)

func TestSchedulerCRUDPersistenceAndNextRun(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, FileName)
	workDir := filepath.Join(dir, "proj")
	otherDir := filepath.Join(dir, "other")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Fixed Wednesday 2026-11-18 10:00:00 local time.
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, loc) // Wednesday (3)

	engine, err := NewEngine(Options{
		Path:       storePath,
		DefaultCWD: workDir,
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	// Reject empty name/prompt or invalid CWD.
	if _, err := engine.CreateSchedule(httpapi.ScheduleInput{Name: "", Prompt: "hi"}); err == nil {
		t.Fatal("expected error for empty name")
	}
	if _, err := engine.CreateSchedule(httpapi.ScheduleInput{Name: "t", Prompt: "hi", CWD: filepath.Join(dir, "missing")}); err == nil {
		t.Fatal("expected error for missing CWD")
	}

	// Daily on Friday (5) at 09:30 -> 2026-11-20 09:30:00.
	created, err := engine.CreateSchedule(httpapi.ScheduleInput{
		Name:         "周五巡检",
		Enabled:      true,
		Prompt:       "检查仓库状态",
		Model:        "openai/gpt-5",
		CWD:          workDir,
		SessionMode:  "continue",
		ScheduleType: "daily",
		Hour:         9,
		Minute:       30,
		Weekdays:     []int{5, 1, 5}, // duplicates sorted to [1,5]
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	wantNext := time.Date(2026, 11, 20, 9, 30, 0, 0, loc).Format(time.RFC3339)
	if created.NextRunAt != wantNext {
		t.Fatalf("NextRunAt = %q, want %q", created.NextRunAt, wantNext)
	}
	if len(created.Weekdays) != 2 || created.Weekdays[0] != 1 || created.Weekdays[1] != 5 {
		t.Fatalf("Weekdays = %v, want [1 5]", created.Weekdays)
	}

	// Simulate prior run saved on task, then change CWD -> LastSession resets.
	engine.mu.Lock()
	engine.tasks[created.ID].LastSession = "/tmp/old-session.jsonl"
	engine.mu.Unlock()

	updated, err := engine.UpdateSchedule(created.ID, httpapi.ScheduleInput{
		Name:            "每小时巡检",
		Enabled:         true,
		Prompt:          "检查日志",
		Model:           "openai/gpt-5",
		CWD:             otherDir,
		SessionMode:     "continue",
		ScheduleType:    "interval",
		IntervalMinutes: 60,
	})
	if err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}
	if updated.LastSession != "" {
		t.Fatalf("expected LastSession cleared on CWD change, got %q", updated.LastSession)
	}

	// Re-open engine from disk and check persisted fields.
	reloaded, err := NewEngine(Options{
		Path:       storePath,
		DefaultCWD: workDir,
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("reload NewEngine: %v", err)
	}
	list, err := reloaded.ListSchedules()
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	if len(list.Tasks) != 1 || list.Tasks[0].Name != "每小时巡检" || list.Tasks[0].IntervalMinutes != 60 {
		t.Fatalf("unexpected reloaded tasks: %+v", list.Tasks)
	}

	if err := reloaded.DeleteSchedule(created.ID); err != nil {
		t.Fatalf("DeleteSchedule: %v", err)
	}
	list, _ = reloaded.ListSchedules()
	if len(list.Tasks) != 0 {
		t.Fatalf("expected 0 tasks after delete, got %d", len(list.Tasks))
	}
}

func TestSchedulerTickRunNowAndSessionContinuity(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, FileName)

	var clockMu sync.Mutex
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	getNow := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		now = now.Add(d)
		clockMu.Unlock()
	}

	var callsMu sync.Mutex
	var calls []RunRequest
	doneCh := make(chan struct{}, 10)
	var failNext atomic.Bool

	engine, err := NewEngine(Options{
		Path:         storePath,
		DefaultCWD:   dir,
		PollInterval: 15 * time.Millisecond,
		Now:          getNow,
		Run: func(_ context.Context, req RunRequest) (RunResult, error) {
			callsMu.Lock()
			calls = append(calls, req)
			n := len(calls)
			callsMu.Unlock()
			if failNext.Load() {
				return RunResult{SessionFile: req.LastSession}, errors.New("boom")
			}
			sessionFile := req.LastSession
			if req.SessionMode == "new" || sessionFile == "" {
				sessionFile = filepath.Join(dir, "sessions", req.TaskID+"_"+string(rune('0'+n))+".jsonl")
			}
			return RunResult{SessionFile: sessionFile, Summary: "已完成检查"}, nil
		},
		OnFinished: func(_ httpapi.ScheduledTask) {
			doneCh <- struct{}{}
		},
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	contTask, err := engine.CreateSchedule(httpapi.ScheduleInput{
		Name:            "连续任务",
		Enabled:         true,
		Prompt:          "继续分析",
		Model:           "anthropic/claude",
		SessionMode:     "continue",
		ScheduleType:    "interval",
		IntervalMinutes: 10,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Disabled task should still support RunScheduleNow.
	newTask, err := engine.CreateSchedule(httpapi.ScheduleInput{
		Name:            "手动新会话",
		Enabled:         false,
		Prompt:          "新建会话测试",
		SessionMode:     "new",
		ScheduleType:    "daily",
		Hour:            8,
		Minute:          0,
		IntervalMinutes: 0,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine.Start(ctx)
	defer engine.Stop()

	// Run 1 of disabled task via RunScheduleNow.
	if _, err := engine.RunScheduleNow(newTask.ID); err != nil {
		t.Fatalf("RunScheduleNow: %v", err)
	}
	<-doneCh

	// Trigger due interval task twice by advancing clock past 10m each time.
	advance(11 * time.Minute)
	<-doneCh
	advance(11 * time.Minute)
	<-doneCh

	// Verify failure recording on RunScheduleNow.
	failNext.Store(true)
	if _, err := engine.RunScheduleNow(newTask.ID); err != nil {
		t.Fatalf("RunScheduleNow fail run: %v", err)
	}
	<-doneCh

	callsMu.Lock()
	if len(calls) != 4 {
		callsMu.Unlock()
		t.Fatalf("expected 4 total runs, got %d", len(calls))
	}
	// Run 2 was contTask run #1 (no LastSession), Run 3 was contTask run #2 (inherited LastSession).
	firstCont := calls[1]
	secondCont := calls[2]
	callsMu.Unlock()

	if firstCont.LastSession != "" || firstCont.CWD != dir || firstCont.Model != "anthropic/claude" {
		t.Fatalf("unexpected first continue call: %+v", firstCont)
	}
	if secondCont.LastSession == "" {
		t.Fatalf("expected second continue call to inherit LastSession, got %+v", secondCont)
	}

	list, err := engine.ListSchedules()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range list.Tasks {
		switch item.ID {
		case contTask.ID:
			if item.RunCount != 2 || item.LastStatus != "success" || item.LastSummary != "已完成检查" {
				t.Fatalf("unexpected contTask stats: %+v", item)
			}
		case newTask.ID:
			if item.RunCount != 2 || item.LastStatus != "error" || item.LastError != "boom" {
				t.Fatalf("unexpected newTask stats: %+v", item)
			}
		}
	}
}
