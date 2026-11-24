package httpapi

import (
	"context"
	"net/http"
	"time"
)

// UsageHistorySource provides recorded usage on the server's own machine.
type UsageHistorySource func(context.Context, *time.Location) (UsageHistory, error)

// UsageHistory reports historical model totals and nonzero daily totals.
type UsageHistory struct {
	Models []ModelUsage     `json:"models"`
	Days   map[string]int64 `json:"days"`
}

// ModelUsage gives the token total and rate for one provider and model pair.
type ModelUsage struct {
	Provider     string   `json:"provider"`
	Model        string   `json:"model"`
	TotalTokens  int64    `json:"totalTokens"`
	TokensPerSec *float64 `json:"tokensPerSec"`
}

func (s *Server) serveUsageHistory(w http.ResponseWriter, r *http.Request) {
	if s.usageHistory == nil {
		http.Error(w, "httpapi: no usage history source configured", http.StatusNotImplemented)
		return
	}
	location := time.Local
	if zone := r.URL.Query().Get("timeZone"); zone != "" {
		var err error
		location, err = time.LoadLocation(zone)
		if err != nil {
			http.Error(w, "httpapi: invalid time zone", http.StatusBadRequest)
			return
		}
	}
	usage, err := s.usageHistory(r.Context(), location)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, usage)
}
