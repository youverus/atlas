package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"pi-go/internal/wire"
)

// ModelUsage summarizes recorded tokens and the subset with provider timing.
type ModelUsage struct {
	Provider    string
	Model       string
	TotalTokens int64
	TimedOutput int64
	DurationMs  int64
}

// UsageHistory groups recorded tokens by model and local calendar date.
type UsageHistory struct {
	Models []ModelUsage
	Days   map[string]int64
}

type usageModel struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type usageEntry struct {
	usageModel
	Type      string      `json:"type"`
	ID        string      `json:"id"`
	Timestamp string      `json:"timestamp"`
	Kind      string      `json:"kind"`
	Note      string      `json:"note"`
	Usage     *wire.Usage `json:"usage"`
	Message   *struct {
		usageModel
		Role       wire.Role   `json:"role"`
		Timestamp  int64       `json:"timestamp"`
		DurationMs int64       `json:"durationMs"`
		Usage      *wire.Usage `json:"usage"`
	} `json:"message"`
}

type usageKey struct {
	id, timestamp, kind string
	model               usageModel
	tokens              int64
}

// ReadUsageHistory scans active and archived session files without modifying them.
func ReadUsageHistory(ctx context.Context, agentDir string, dirs, files []string, location *time.Location) (UsageHistory, error) {
	root := filepath.Join(agentDir, "sessions")
	buckets, err := os.ReadDir(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return UsageHistory{}, err
	}
	dirs = slices.Clone(dirs)
	for _, bucket := range buckets {
		if bucket.IsDir() || bucket.Type()&os.ModeSymlink != 0 {
			dirs = append(dirs, filepath.Join(root, bucket.Name()))
		}
	}
	files = slices.Clone(files)
	seenDirs := make(map[string]bool)
	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return UsageHistory{}, err
		}
		dir, err := filepath.EvalSymlinks(dir)
		if err != nil || seenDirs[dir] {
			continue
		}
		seenDirs[dir] = true
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
				files = append(files, filepath.Join(dir, entry.Name()))
			}
		}
	}

	history := UsageHistory{Days: make(map[string]int64)}
	models := make(map[usageModel]*ModelUsage)
	add := func(model usageModel, date time.Time, total, output, durationMs int64) {
		row := models[model]
		if row == nil {
			row = &ModelUsage{Provider: model.Provider, Model: model.Model}
			models[model] = row
		}
		row.TotalTokens += total
		if output > 0 && durationMs > 0 {
			row.TimedOutput += output
			row.DurationMs += durationMs
		}
		if !date.IsZero() {
			history.Days[date.In(location).Format(time.DateOnly)] += total
		}
	}

	seenFiles := make(map[string]string)
	seenEntries := make(map[usageKey]bool)
	type taskUsage struct {
		model usageModel
		date  time.Time
		total int64
	}
	tasks := make(map[string]taskUsage)
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return UsageHistory{}, err
		}
		file, err := filepath.EvalSymlinks(file)
		if _, seen := seenFiles[file]; err != nil || seen {
			continue
		}
		info, err := os.Stat(file)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		seenFiles[file] = scanUsageFile(ctx, file, func(entry usageEntry) {
			model, usage := entry.usageModel, entry.Usage
			date, _ := time.Parse(time.RFC3339Nano, entry.Timestamp)
			var durationMs int64
			switch entry.Type {
			case TypeMessage:
				msg := entry.Message
				if msg == nil || (msg.Role != wire.RoleAssistant && msg.Role != wire.RoleToolResult) {
					return
				}
				model, usage = msg.usageModel, msg.Usage
				if msg.Timestamp > 0 {
					date = time.UnixMilli(msg.Timestamp)
				}
				if msg.Role == wire.RoleAssistant {
					durationMs = msg.DurationMs
				}
			case TypeUsage, TypeCompaction, TypeBranchSummary:
			default:
				return
			}
			total := usageTokens(usage)
			if total <= 0 {
				return
			}
			if entry.Type == TypeUsage && entry.Kind == wire.SubagentToolName && entry.Note != "" {
				if previous := tasks[entry.Note]; previous.date.IsZero() || (!date.IsZero() && date.Before(previous.date)) {
					tasks[entry.Note] = taskUsage{model, date, total}
				}
				return
			}
			// Forks preserve the entry ID and timestamp even when reparenting it.
			key := usageKey{entry.ID, entry.Timestamp, entry.Type, model, total}
			if entry.ID != "" {
				if seenEntries[key] {
					return
				}
				seenEntries[key] = true
			}
			add(model, date, total, int64(usage.Output), durationMs)
		})
	}

	// The task record predates delivery to the session and retains the completion date.
	for file, sessionID := range seenFiles {
		if err := ctx.Err(); err != nil {
			return UsageHistory{}, err
		}
		if sessionID == "" {
			continue
		}
		entries, _ := os.ReadDir(SubagentDir(file))
		for _, entry := range entries {
			if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			var task wire.SubagentUpdate
			date := readUsageTask(filepath.Join(SubagentDir(file), entry.Name()), &task)
			if task.ID+".json" != entry.Name() || task.SessionID != sessionID || task.EntryID == "" || date.IsZero() {
				continue
			}
			if total := usageTokens(task.Usage); total > 0 {
				tasks[task.ID] = taskUsage{usageModel{task.Provider, task.Model}, date, total}
			}
		}
	}
	for _, task := range tasks {
		add(task.model, task.date, task.total, 0, 0)
	}
	if err := ctx.Err(); err != nil {
		return UsageHistory{}, err
	}
	for _, model := range models {
		history.Models = append(history.Models, *model)
	}
	return history, nil
}

func scanUsageFile(ctx context.Context, path string, visit func(usageEntry)) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	var sessionID string
	for ctx.Err() == nil {
		line, readErr := reader.ReadBytes('\n')
		var entry usageEntry
		if json.Unmarshal(line, &entry) == nil {
			if sessionID == "" {
				if entry.Type != "session" || entry.ID == "" {
					return ""
				}
				sessionID = entry.ID
			} else {
				visit(entry)
			}
		}
		if readErr != nil {
			break
		}
	}
	return sessionID
}

func readUsageTask(path string, task *wire.SubagentUpdate) time.Time {
	file, err := os.Open(path)
	if err != nil {
		return time.Time{}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || json.NewDecoder(io.LimitReader(file, 128<<10)).Decode(task) != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func usageTokens(usage *wire.Usage) int64 {
	if usage == nil {
		return 0
	}
	if usage.TotalTokens > 0 {
		return int64(usage.TotalTokens)
	}
	return int64(usage.Input) + int64(usage.Output) + int64(usage.CacheRead) + int64(usage.CacheWrite)
}
