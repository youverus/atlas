package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"pi-go/internal/browser"
)

type browserContextKey struct{}

func withBrowserService(ctx context.Context, svc *browser.Service) context.Context {
	if svc == nil {
		return ctx
	}
	return context.WithValue(ctx, browserContextKey{}, svc)
}

func browserServiceFromContext(ctx context.Context) *browser.Service {
	if ctx == nil {
		return nil
	}
	svc, _ := ctx.Value(browserContextKey{}).(*browser.Service)
	return svc
}

func browserCommand(ctx context.Context, args []string, out io.Writer) error {
	svc := browserServiceFromContext(ctx)
	if svc == nil {
		fmt.Fprintln(out, "managed browser is not initialized in this mode")
		return nil
	}
	if len(args) == 0 {
		return writeBrowserState(out, svc.State())
	}
	switch args[0] {
	case "close":
		if len(args) < 2 {
			return fmt.Errorf("/browser close requires a tab id or \"all\"")
		}
		target := strings.TrimSpace(args[1])
		if target == "all" {
			if err := svc.CloseAllTabs(ctx); err != nil {
				return err
			}
			fmt.Fprintln(out, "closed all managed browser tabs")
			return nil
		}
		if err := svc.CloseTab(ctx, target); err != nil {
			return err
		}
		fmt.Fprintf(out, "closed browser tab %s\n", target)
		return nil
	case "open":
		if len(args) < 2 {
			return fmt.Errorf("/browser open requires a URL or search query")
		}
		tab, err := svc.OpenTab(ctx, strings.Join(args[1:], " "), true, true)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "opened %s (%s)\n", tab.URL, tab.ID)
		return nil
	default:
		tab, err := svc.OpenTab(ctx, strings.Join(args, " "), true, true)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "opened %s (%s)\n", tab.URL, tab.ID)
		return nil
	}
}

func writeBrowserState(out io.Writer, state browser.State) error {
	if !state.Available {
		fmt.Fprintln(out, "no Chrome/Chromium executable found; install Google Chrome or set ATLAS_BROWSER_BIN")
		return nil
	}
	status := "idle (launches on demand)"
	if state.Running {
		status = "running"
	}
	fmt.Fprintf(out, "browser: %s (%s)\n", state.Executable, status)
	if len(state.Tabs) == 0 {
		fmt.Fprintln(out, "no managed tabs open; use /browser <url> to open one")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tCONTROL\tTITLE\tURL")
	for _, tab := range state.Tabs {
		mark := ""
		if tab.Active {
			mark = "*"
		}
		control := tab.Control
		if tab.Paused {
			control += " (paused)"
		}
		fmt.Fprintf(tw, "%s%s\t%s\t%s\t%s\n", mark, tab.ID, control, tab.Title, tab.URL)
	}
	return tw.Flush()
}
