package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"pi-go/internal/browser"
)

func TestBrowserCommandAndToolGroup(t *testing.T) {
	if got := toolGroupOf("browser_open"); got != "browser" {
		t.Fatalf("toolGroupOf(browser_open) = %q, want browser", got)
	}
	if !archivedCommands["/browser"] || !sideCommands["/browser"] {
		t.Fatal("expected /browser allowed in archived and side command tables")
	}

	svc := browser.NewService(browser.Options{Executable: "/tmp/fake-chrome"})
	ctx := withBrowserService(context.Background(), svc)
	var out bytes.Buffer
	if err := browserCommand(ctx, nil, &out); err != nil {
		t.Fatalf("browserCommand: %v", err)
	}
	if !strings.Contains(out.String(), "/tmp/fake-chrome") {
		t.Fatalf("unexpected /browser output: %q", out.String())
	}
}
