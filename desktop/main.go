// Command atlas-desktop is the windowed shell: a Wails application whose one
// window points at a loopback HTTP server started in the same process.
//
// Why a URL instead of Wails' own asset server and bindings: the frontend then
// has no coupling to the shell at all. It reads three routes over HTTP and one
// SSE stream, which means the same page can be developed in a browser, served
// by a future `atlas web`, or embedded here, with nothing to rewrite — and the
// approval dialog, which has to block a tool goroutine, becomes an ordinary
// blocking POST rather than a bidirectionally framed Wails event.
//
// The turn behind the window is the real one. The assembly package builds the
// same app the CLI runs: same config file, same adapters, same tools, same
// permission engine. The difference between the two front ends is the surface,
// not the agent.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"pi-go/internal/assembly"
	"pi-go/internal/httpapi"
	"pi-go/internal/wire"
)

// version is printed on the page. It tracks the kernel's own version loosely:
// this shell ships separately and does not need to be rebuilt when the model
// adapters change.
const version = "0.2.0"

func main() {
	configPath := flag.String("config", "", "config file (default ~/.atlas/config.json)")
	modelFlag := flag.String("model", "", "model to use: \"provider/model\" or a unique model id")
	thinking := flag.String("thinking", "", "thinking level; fitted to what the model offers")
	workdir := flag.String("cwd", "", "working directory for tools and the session (default the current one)")
	permission := flag.String("permission", "", "permission mode: default, auto, bypassPermissions, off")
	offline := flag.Bool("offline", false, "answer from a script instead of a model (no config, no network)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	directory, _ := os.Getwd()
	shown := "offline"
	var turn httpapi.Turn

	if *offline {
		turn = offlineTurn()
	} else {
		kernel, notes, err := assembly.New(ctx, assembly.Options{
			ConfigPath:     *configPath,
			Model:          *modelFlag,
			Thinking:       *thinking,
			Workdir:        *workdir,
			PermissionMode: *permission,
			Version:        version,
		})
		if err != nil {
			// No config, no key, no network: the window would open onto nothing
			// and say nothing, so say it where the reader launched it.
			fmt.Fprintf(os.Stderr, "atlas-desktop: %v\n", err)
			os.Exit(1)
		}
		defer kernel.Close()
		for _, note := range notes {
			fmt.Fprintln(os.Stderr, "note:", note)
		}
		directory, shown = kernel.Cwd, kernel.Selection.Name()
		turn = kernelTurn(kernel)
	}

	server := httpapi.New(httpapi.Options{
		Turn: turn,
		Facts: httpapi.Facts{
			Version:   version,
			Directory: directory,
			Model:     shown,
		},
	})
	if _, err := server.Listen(); err != nil {
		fmt.Fprintf(os.Stderr, "atlas-desktop: %v\n", err)
		os.Exit(1)
	}
	defer server.Close()

	app := application.New(application.Options{
		Name:        "atlas",
		Description: "atlas desktop",
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:   "main",
		Title:  "atlas",
		Width:  1100,
		Height: 760,
		URL:    server.URL(),
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 0,
			Backdrop:                application.MacBackdropNormal,
		},
	})

	// Ctrl-C in the launching terminal is the second way to quit, because a
	// windowed app started from a shell should stop when that shell interrupts.
	go func() {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
		<-signals
		app.Quit()
	}()

	if err := app.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "atlas-desktop: %v\n", err)
		os.Exit(1)
	}
}

// kernelTurn hands the app's own Ask to the stream. Nothing is translated: the
// events a window draws are the events the kernel emits, which is why the page
// can read them by their wire names.
func kernelTurn(kernel *assembly.Kernel) httpapi.Turn {
	return func(ctx context.Context, prompt string, emit func(wire.AgentEvent) error) error {
		_, err := kernel.App.Ask(ctx, wire.UserMessage(prompt), emit)
		return err
	}
}

// offlineTurn answers without a model. It exists so the window can be run and
// the stream watched end to end with no config file and no API key; every event
// it emits is a real wire.AgentEvent, so the page cannot tell it apart from a
// provider until the text stops being a script.
func offlineTurn() httpapi.Turn {
	return func(ctx context.Context, prompt string, emit func(wire.AgentEvent) error) error {
		if err := emit(wire.AgentEvent{Type: wire.AgentEventStart}); err != nil {
			return err
		}
		if err := emit(wire.AgentEvent{
			Type:    wire.AgentEventMessageStart,
			Message: &wire.Message{Role: "assistant"},
		}); err != nil {
			return err
		}

		// Streamed word by word so the page paints incrementally: a single
		// message would render the same text but prove nothing about the stream.
		reply := "This is the offline path (-offline). " +
			"The transport is real (loopback HTTP plus SSE); the turn behind it is " +
			"a script. Prompt received: " + prompt
		for _, word := range strings.Fields(reply) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			event := wire.AgentEvent{
				Type:           wire.AgentEventMessageUpdate,
				AssistantEvent: &wire.AssistantEvent{Type: wire.EventTextDelta, Delta: word + " "},
			}
			if err := emit(event); err != nil {
				return err
			}
			time.Sleep(18 * time.Millisecond)
		}

		if err := emit(wire.AgentEvent{Type: wire.AgentEventMessageEnd}); err != nil {
			return err
		}
		return emit(wire.AgentEvent{Type: wire.AgentEventEnd})
	}
}
