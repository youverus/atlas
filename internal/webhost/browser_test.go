package webhost

import (
	"testing"
)

func TestHostBrowserStateDoesNotStartChromium(t *testing.T) {
	host, _, _ := sessionHost(t)

	state, err := host.BrowserState()
	if err != nil {
		t.Fatalf("BrowserState: %v", err)
	}
	if state.Running {
		t.Fatalf("expected lazy browser not running initially")
	}
	if len(state.Tabs) != 0 {
		t.Fatalf("expected zero initial tabs, got %d", len(state.Tabs))
	}
}
