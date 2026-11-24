package browser

import (
	"context"
	"encoding/json"
	"testing"

	libbrowser "pi-go/internal/browser"
	"pi-go/internal/tool"
)

func TestToolsRegistersAllSixteenCoreTools(t *testing.T) {
	if got := Tools(nil); got != nil {
		t.Fatalf("expected nil tools for nil service, got %d", len(got))
	}
	svc := libbrowser.NewService(libbrowser.Options{})
	tools := Tools(svc)
	want := []string{
		"browser_open",
		"browser_list_tabs",
		"browser_navigate",
		"browser_inspect",
		"browser_snapshot",
		"browser_screenshot",
		"browser_click",
		"browser_click_at",
		"browser_type",
		"browser_press",
		"browser_scroll",
		"browser_wait",
		"browser_back",
		"browser_forward",
		"browser_reload",
		"browser_close",
	}
	if len(tools) != len(want) {
		t.Fatalf("expected %d tools, got %d", len(want), len(tools))
	}
	seen := make(map[string]bool, len(tools))
	for _, item := range tools {
		seen[item.Name()] = true
		if !json.Valid(item.Parameters()) {
			t.Fatalf("tool %s has invalid JSON schema", item.Name())
		}
	}
	for _, name := range want {
		if !seen[name] {
			t.Fatalf("missing expected tool %q", name)
		}
	}
}

func TestListTabsToolWithoutLaunchingChromium(t *testing.T) {
	svc := libbrowser.NewService(libbrowser.Options{})
	var list tool.Tool
	for _, item := range Tools(svc) {
		if item.Name() == "browser_list_tabs" {
			list = item
			break
		}
	}
	res, err := list.Execute(context.Background(), tool.Call{ID: "1", Name: "browser_list_tabs", Arguments: json.RawMessage(`{}`)}, func(tool.Partial) {})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError || len(res.Content) == 0 {
		t.Fatalf("unexpected error result: %+v", res)
	}
}
