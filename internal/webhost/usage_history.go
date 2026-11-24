package webhost

import (
	"cmp"
	"context"
	"slices"
	"time"

	"pi-go/internal/config"
	"pi-go/internal/httpapi"
	"pi-go/internal/session"
)

// UsageHistory returns usage from local projects and open session files.
func (h *Host) UsageHistory(ctx context.Context, location *time.Location) (httpapi.UsageHistory, error) {
	home, err := config.DefaultHome()
	if err != nil {
		return httpapi.UsageHistory{}, err
	}
	var dirs, files []string
	if dir, err := h.kernel.App.SessionDir(h.startupDir()); err == nil {
		dirs = append(dirs, dir)
	}
	h.mu.RLock()
	for path := range h.slots {
		if path != "" {
			files = append(files, path)
		}
	}
	h.mu.RUnlock()

	history, err := session.ReadUsageHistory(ctx, home, dirs, files, location)
	if err != nil {
		return httpapi.UsageHistory{}, err
	}
	rows := make([]httpapi.ModelUsage, 0, len(history.Models))
	for _, model := range history.Models {
		row := httpapi.ModelUsage{Provider: model.Provider, Model: model.Model, TotalTokens: model.TotalTokens}
		if model.DurationMs > 0 {
			rate := 1000 * float64(model.TimedOutput) / float64(model.DurationMs)
			row.TokensPerSec = &rate
		}
		rows = append(rows, row)
	}
	slices.SortFunc(rows, func(a, b httpapi.ModelUsage) int {
		return cmp.Or(cmp.Compare(b.TotalTokens, a.TotalTokens), cmp.Compare(a.Provider, b.Provider), cmp.Compare(a.Model, b.Model))
	})
	return httpapi.UsageHistory{Models: rows, Days: history.Days}, nil
}
