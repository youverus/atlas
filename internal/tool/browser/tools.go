// Package browser exposes the 16 Stage-1 managed Chromium tools to the agent.
package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	libbrowser "pi-go/internal/browser"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

type browserTool struct {
	name   string
	label  string
	desc   string
	schema json.RawMessage
	run    func(ctx context.Context, raw json.RawMessage) (tool.Result, error)
}

func (t browserTool) Name() string                { return t.name }
func (t browserTool) Label() string               { return t.label }
func (t browserTool) Description() string         { return t.desc }
func (t browserTool) Parameters() json.RawMessage { return t.schema }
func (t browserTool) Execute(ctx context.Context, call tool.Call, _ func(tool.Partial)) (tool.Result, error) {
	res, err := t.run(ctx, call.Arguments)
	if err != nil {
		return tool.Result{
			Content: []wire.Content{wire.TextContent(err.Error())},
			IsError: true,
		}, nil
	}
	return res, nil
}

// Tools returns the 16 core browser read and interaction tools backed by svc.
func Tools(svc *libbrowser.Service) []tool.Tool {
	if svc == nil {
		return nil
	}
	return []tool.Tool{
		openTool(svc),
		listTabsTool(svc),
		navigateTool(svc),
		inspectTool(svc),
		snapshotTool(svc),
		screenshotTool(svc),
		clickTool(svc),
		clickAtTool(svc),
		typeTool(svc),
		pressTool(svc),
		scrollTool(svc),
		waitTool(svc),
		backTool(svc),
		forwardTool(svc),
		reloadTool(svc),
		closeTool(svc),
	}
}

func jsonResult(v any) (tool.Result, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{
		Content:           []wire.Content{wire.TextContent(string(data))},
		StructuredContent: json.RawMessage(data),
	}, nil
}

func openTool(svc *libbrowser.Service) tool.Tool {
	return browserTool{
		name:  "browser_open",
		label: "browser open",
		desc:  "Open an http(s) URL in a managed Chromium browser tab. Launches the visible controlled browser window on demand. For general web search or reading public pages, prefer deepseek_web_search and deepseek_web_fetch first; use browser_* tools only when interaction, login, dynamic rendering, or visual inspection is needed.",
		schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"url": { "type": "string", "description": "Absolute http(s) URL to open" },
				"activate": { "type": "boolean", "description": "Focus the opened tab (default true)" }
			},
			"required": ["url"]
		}`),
		run: func(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
			var in struct {
				URL      string `json:"url"`
				Activate *bool  `json:"activate"`
			}
			if err := tool.Decode(raw, &in); err != nil {
				return tool.Result{}, err
			}
			if strings.TrimSpace(in.URL) == "" {
				return tool.Result{}, fmt.Errorf("url is required")
			}
			activate := true
			if in.Activate != nil {
				activate = *in.Activate
			}
			tab, err := svc.OpenTab(ctx, in.URL, false, activate)
			if err != nil {
				return tool.Result{}, err
			}
			return jsonResult(tab)
		},
	}
}

func listTabsTool(svc *libbrowser.Service) tool.Tool {
	return browserTool{
		name:   "browser_list_tabs",
		label:  "browser list tabs",
		desc:   "List open tabs in the managed Chromium browser.",
		schema: json.RawMessage(`{"type":"object","properties":{}}`),
		run: func(_ context.Context, _ json.RawMessage) (tool.Result, error) {
			return jsonResult(map[string]any{"tabs": svc.ListTabs()})
		},
	}
}

func navigateTool(svc *libbrowser.Service) tool.Tool {
	return browserTool{
		name:  "browser_navigate",
		label: "browser navigate",
		desc:  "Navigate an existing managed browser tab to an http(s) URL.",
		schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"tabId": { "type": "string", "description": "Target browser tab UUID" },
				"url": { "type": "string", "description": "Absolute http(s) URL" }
			},
			"required": ["tabId", "url"]
		}`),
		run: func(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
			var in struct {
				TabID string `json:"tabId"`
				URL   string `json:"url"`
			}
			if err := tool.Decode(raw, &in); err != nil {
				return tool.Result{}, err
			}
			tab, err := svc.Navigate(ctx, in.TabID, in.URL, false)
			if err != nil {
				return tool.Result{}, err
			}
			return jsonResult(tab)
		},
	}
}

func inspectTool(svc *libbrowser.Service) tool.Tool {
	return browserTool{
		name:  "browser_inspect",
		label: "browser inspect",
		desc:  "Inspect a browser tab with compact page text and element refs (e1, e2, ...). Reuse sinceInspectionId on follow-up checks to skip unchanged content.",
		schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"tabId": { "type": "string", "description": "Browser tab UUID (defaults to active tab)" },
				"sinceInspectionId": { "type": "string", "description": "Previous inspectionId to detect changes" },
				"maxNodes": { "type": "integer", "description": "Maximum interactive nodes (default 100)" },
				"maxTextChars": { "type": "integer", "description": "Maximum text characters (default 8000)" },
				"screenshot": {
					"type": "object",
					"properties": {
						"enabled": { "type": "boolean" },
						"format": { "type": "string", "enum": ["png", "jpeg"] },
						"quality": { "type": "integer" }
					}
				}
			}
		}`),
		run: func(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
			var in struct {
				TabID             string      `json:"tabId"`
				SinceInspectionID string      `json:"sinceInspectionId"`
				MaxNodes          tool.Number `json:"maxNodes"`
				MaxTextChars      tool.Number `json:"maxTextChars"`
				Screenshot        *struct {
					Enabled bool        `json:"enabled"`
					Format  string      `json:"format"`
					Quality tool.Number `json:"quality"`
				} `json:"screenshot"`
			}
			if err := tool.Decode(raw, &in); err != nil {
				return tool.Result{}, err
			}
			req := libbrowser.InspectInput{
				TabID:             in.TabID,
				SinceInspectionID: in.SinceInspectionID,
				MaxNodes:          in.MaxNodes.Int(),
				MaxTextChars:      in.MaxTextChars.Int(),
			}
			if in.Screenshot != nil && in.Screenshot.Enabled {
				req.IncludeScreenshot = true
				req.ScreenshotFormat = in.Screenshot.Format
				req.ScreenshotQuality = in.Screenshot.Quality.Int()
			}
			res, shot, err := svc.Inspect(ctx, req)
			if err != nil {
				return tool.Result{}, err
			}
			data, err := json.MarshalIndent(res, "", "  ")
			if err != nil {
				return tool.Result{}, err
			}
			content := []wire.Content{wire.TextContent(string(data))}
			if shot != nil {
				content = append(content, wire.ImageContent(shot.Base64, shot.MIME))
			}
			return tool.Result{
				Content:           content,
				StructuredContent: json.RawMessage(data),
			}, nil
		},
	}
}

func snapshotTool(svc *libbrowser.Service) tool.Tool {
	return browserTool{
		name:  "browser_snapshot",
		label: "browser snapshot",
		desc:  "Capture a standalone snapshot of visible page text and interactive element refs.",
		schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"tabId": { "type": "string", "description": "Browser tab UUID" },
				"maxNodes": { "type": "integer" },
				"maxTextChars": { "type": "integer" }
			},
			"required": ["tabId"]
		}`),
		run: func(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
			var in struct {
				TabID        string      `json:"tabId"`
				MaxNodes     tool.Number `json:"maxNodes"`
				MaxTextChars tool.Number `json:"maxTextChars"`
			}
			if err := tool.Decode(raw, &in); err != nil {
				return tool.Result{}, err
			}
			snap, err := svc.Snapshot(ctx, in.TabID, in.MaxNodes.Int(), in.MaxTextChars.Int())
			if err != nil {
				return tool.Result{}, err
			}
			return jsonResult(snap)
		},
	}
}

func screenshotTool(svc *libbrowser.Service) tool.Tool {
	return browserTool{
		name:  "browser_screenshot",
		label: "browser screenshot",
		desc:  "Capture a viewport, full-page, or element screenshot of a browser tab.",
		schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"tabId": { "type": "string", "description": "Browser tab UUID" },
				"mode": { "type": "string", "enum": ["viewport", "full-page", "element"] },
				"format": { "type": "string", "enum": ["png", "jpeg"] },
				"quality": { "type": "integer" },
				"snapshotId": { "type": "string" },
				"ref": { "type": "string" },
				"generation": { "type": "integer" }
			},
			"required": ["tabId"]
		}`),
		run: func(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
			var in struct {
				TabID      string      `json:"tabId"`
				Mode       string      `json:"mode"`
				Format     string      `json:"format"`
				Quality    tool.Number `json:"quality"`
				SnapshotID string      `json:"snapshotId"`
				Ref        string      `json:"ref"`
				Generation tool.Number `json:"generation"`
			}
			if err := tool.Decode(raw, &in); err != nil {
				return tool.Result{}, err
			}
			shot, err := svc.Screenshot(ctx, libbrowser.ScreenshotInput{
				TabID:      in.TabID,
				Mode:       in.Mode,
				Format:     in.Format,
				Quality:    in.Quality.Int(),
				SnapshotID: in.SnapshotID,
				Ref:        in.Ref,
				Generation: in.Generation.Int(),
			})
			if err != nil {
				return tool.Result{}, err
			}
			summary := fmt.Sprintf("Screenshot of browser tab %s (%dx%d, %s, generation %d)", shot.TabID, shot.Width, shot.Height, shot.Mode, shot.Generation)
			return tool.Result{
				Content: []wire.Content{
					wire.TextContent(summary),
					wire.ImageContent(shot.Base64, shot.MIME),
				},
			}, nil
		},
	}
}

func clickTool(svc *libbrowser.Service) tool.Tool {
	return browserTool{
		name:  "browser_click",
		label: "browser click",
		desc:  "Click an element ref from the latest snapshot on a browser tab.",
		schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"tabId": { "type": "string" },
				"ref": { "type": "string" },
				"snapshotId": { "type": "string" },
				"generation": { "type": "integer" },
				"button": { "type": "string", "enum": ["left", "middle", "right"] },
				"clickCount": { "type": "integer", "enum": [1, 2] },
				"modifiers": { "type": "array", "items": { "type": "string" } }
			},
			"required": ["tabId", "ref", "snapshotId", "generation"]
		}`),
		run: func(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
			var in struct {
				TabID      string      `json:"tabId"`
				Ref        string      `json:"ref"`
				SnapshotID string      `json:"snapshotId"`
				Generation tool.Number `json:"generation"`
				Button     string      `json:"button"`
				ClickCount tool.Number `json:"clickCount"`
				Modifiers  []string    `json:"modifiers"`
			}
			if err := tool.Decode(raw, &in); err != nil {
				return tool.Result{}, err
			}
			res, err := svc.Click(ctx, libbrowser.ClickInput{
				TabID:      in.TabID,
				Ref:        in.Ref,
				SnapshotID: in.SnapshotID,
				Generation: in.Generation.Int(),
				Button:     in.Button,
				ClickCount: in.ClickCount.Int(),
				Modifiers:  in.Modifiers,
			})
			if err != nil {
				return tool.Result{}, err
			}
			return jsonResult(res)
		},
	}
}

func clickAtTool(svc *libbrowser.Service) tool.Tool {
	return browserTool{
		name:  "browser_click_at",
		label: "browser click at",
		desc:  "Click viewport coordinates (x, y) in a browser tab.",
		schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"tabId": { "type": "string" },
				"x": { "type": "number" },
				"y": { "type": "number" },
				"button": { "type": "string", "enum": ["left", "middle", "right"] },
				"clickCount": { "type": "integer", "enum": [1, 2] },
				"modifiers": { "type": "array", "items": { "type": "string" } }
			},
			"required": ["tabId", "x", "y"]
		}`),
		run: func(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
			var in struct {
				TabID      string      `json:"tabId"`
				X          float64     `json:"x"`
				Y          float64     `json:"y"`
				Button     string      `json:"button"`
				ClickCount tool.Number `json:"clickCount"`
				Modifiers  []string    `json:"modifiers"`
			}
			if err := tool.Decode(raw, &in); err != nil {
				return tool.Result{}, err
			}
			res, err := svc.ClickAt(ctx, in.TabID, in.X, in.Y, in.Button, in.ClickCount.Int(), in.Modifiers)
			if err != nil {
				return tool.Result{}, err
			}
			return jsonResult(res)
		},
	}
}

func typeTool(svc *libbrowser.Service) tool.Tool {
	return browserTool{
		name:  "browser_type",
		label: "browser type",
		desc:  "Focus a snapshot element ref and enter text, optionally submitting with Enter.",
		schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"tabId": { "type": "string" },
				"ref": { "type": "string" },
				"snapshotId": { "type": "string" },
				"generation": { "type": "integer" },
				"text": { "type": "string" },
				"submit": { "type": "boolean" }
			},
			"required": ["tabId", "ref", "snapshotId", "generation", "text"]
		}`),
		run: func(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
			var in struct {
				TabID      string      `json:"tabId"`
				Ref        string      `json:"ref"`
				SnapshotID string      `json:"snapshotId"`
				Generation tool.Number `json:"generation"`
				Text       string      `json:"text"`
				Submit     bool        `json:"submit"`
			}
			if err := tool.Decode(raw, &in); err != nil {
				return tool.Result{}, err
			}
			if err := svc.Type(ctx, in.TabID, in.SnapshotID, in.Ref, in.Generation.Int(), in.Text, in.Submit); err != nil {
				return tool.Result{}, err
			}
			return jsonResult(map[string]any{"ok": true, "tabId": in.TabID, "submitted": in.Submit})
		},
	}
}

func pressTool(svc *libbrowser.Service) tool.Tool {
	return browserTool{
		name:  "browser_press",
		label: "browser press",
		desc:  "Send an allowlisted keyboard key (Enter, Tab, Escape, ArrowUp/Down/Left/Right, etc.) to a browser tab.",
		schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"tabId": { "type": "string" },
				"key": { "type": "string" },
				"modifiers": { "type": "array", "items": { "type": "string" } }
			},
			"required": ["tabId", "key"]
		}`),
		run: func(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
			var in struct {
				TabID     string   `json:"tabId"`
				Key       string   `json:"key"`
				Modifiers []string `json:"modifiers"`
			}
			if err := tool.Decode(raw, &in); err != nil {
				return tool.Result{}, err
			}
			if err := svc.Press(ctx, in.TabID, in.Key, in.Modifiers); err != nil {
				return tool.Result{}, err
			}
			return jsonResult(map[string]any{"ok": true, "tabId": in.TabID, "key": in.Key})
		},
	}
}

func scrollTool(svc *libbrowser.Service) tool.Tool {
	return browserTool{
		name:  "browser_scroll",
		label: "browser scroll",
		desc:  "Scroll a browser tab vertically or horizontally via wheel input.",
		schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"tabId": { "type": "string" },
				"deltaX": { "type": "integer" },
				"deltaY": { "type": "integer" },
				"x": { "type": "integer" },
				"y": { "type": "integer" }
			},
			"required": ["tabId", "deltaY"]
		}`),
		run: func(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
			var in struct {
				TabID  string       `json:"tabId"`
				DeltaX tool.Number  `json:"deltaX"`
				DeltaY tool.Number  `json:"deltaY"`
				X      *tool.Number `json:"x"`
				Y      *tool.Number `json:"y"`
			}
			if err := tool.Decode(raw, &in); err != nil {
				return tool.Result{}, err
			}
			var px, py *int
			if in.X != nil {
				v := in.X.Int()
				px = &v
			}
			if in.Y != nil {
				v := in.Y.Int()
				py = &v
			}
			if err := svc.Scroll(ctx, in.TabID, in.DeltaX.Int(), in.DeltaY.Int(), px, py); err != nil {
				return tool.Result{}, err
			}
			return jsonResult(map[string]any{"ok": true, "tabId": in.TabID})
		},
	}
}

func waitTool(svc *libbrowser.Service) tool.Tool {
	return browserTool{
		name:  "browser_wait",
		label: "browser wait",
		desc:  "Wait for page load, network-idle, a CSS selector, or visible text on a browser tab.",
		schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"tabId": { "type": "string" },
				"condition": { "type": "string", "enum": ["load", "network-idle", "selector", "text"] },
				"value": { "type": "string" },
				"timeoutMs": { "type": "integer" }
			},
			"required": ["tabId"]
		}`),
		run: func(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
			var in struct {
				TabID     string      `json:"tabId"`
				Condition string      `json:"condition"`
				Value     string      `json:"value"`
				TimeoutMs tool.Number `json:"timeoutMs"`
			}
			if err := tool.Decode(raw, &in); err != nil {
				return tool.Result{}, err
			}
			tab, err := svc.Wait(ctx, in.TabID, in.Condition, in.Value, in.TimeoutMs.Int())
			if err != nil {
				return tool.Result{}, err
			}
			return jsonResult(tab)
		},
	}
}

func backTool(svc *libbrowser.Service) tool.Tool {
	return tabOnlyTool(svc, "browser_back", "browser back", "Go back in a browser tab's navigation history.", svc.GoBack)
}

func forwardTool(svc *libbrowser.Service) tool.Tool {
	return tabOnlyTool(svc, "browser_forward", "browser forward", "Go forward in a browser tab's navigation history.", svc.GoForward)
}

func reloadTool(svc *libbrowser.Service) tool.Tool {
	return tabOnlyTool(svc, "browser_reload", "browser reload", "Reload a browser tab.", svc.Reload)
}

func closeTool(svc *libbrowser.Service) tool.Tool {
	return browserTool{
		name:   "browser_close",
		label:  "browser close",
		desc:   "Close a managed browser tab.",
		schema: json.RawMessage(`{"type":"object","properties":{"tabId":{"type":"string"}},"required":["tabId"]}`),
		run: func(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
			var in struct {
				TabID string `json:"tabId"`
			}
			if err := tool.Decode(raw, &in); err != nil {
				return tool.Result{}, err
			}
			if err := svc.CloseTab(ctx, in.TabID); err != nil {
				return tool.Result{}, err
			}
			return jsonResult(map[string]any{"ok": true, "closedTabId": in.TabID})
		},
	}
}

func tabOnlyTool(svc *libbrowser.Service, name, label, desc string, fn func(context.Context, string) (libbrowser.TabInfo, error)) tool.Tool {
	return browserTool{
		name:   name,
		label:  label,
		desc:   desc,
		schema: json.RawMessage(`{"type":"object","properties":{"tabId":{"type":"string"}},"required":["tabId"]}`),
		run: func(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
			var in struct {
				TabID string `json:"tabId"`
			}
			if err := tool.Decode(raw, &in); err != nil {
				return tool.Result{}, err
			}
			tab, err := fn(ctx, in.TabID)
			if err != nil {
				return tool.Result{}, err
			}
			return jsonResult(tab)
		},
	}
}
