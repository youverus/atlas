package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"pi-go/internal/assembly"
	"pi-go/internal/cost"
	"pi-go/internal/httpapi"
	"pi-go/internal/webhost"
	"pi-go/internal/wire"
)

// serveInfoMetadata describes a running `atlas -serve` instance so SSH callers
// can discover and reuse it on reconnect.
type serveInfoMetadata struct {
	PID       int    `json:"pid"`
	Addr      string `json:"addr"`
	Port      int    `json:"port"`
	URL       string `json:"url"`
	Version   string `json:"version"`
	Cwd       string `json:"cwd"`
	StartedAt string `json:"startedAt"`
}

type serveInstance struct {
	server    *httpapi.Server
	kernel    *assembly.Kernel
	addr      string
	port      int
	info      serveInfoMetadata
	closeFunc func()
}

func runServeHTTP(ctx context.Context, opts options, cwd string, stdio streams) error {
	serveCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	inst, err := startServeHTTP(serveCtx, opts, cwd, stdio)
	if err != nil {
		return err
	}
	defer inst.closeFunc()

	encoded, _ := json.Marshal(inst.info)
	fmt.Fprintln(stdio.out, string(encoded))

	<-serveCtx.Done()
	return nil
}

func startServeHTTP(ctx context.Context, opts options, cwd string, stdio streams) (*serveInstance, error) {
	token := strings.TrimSpace(opts.authToken)
	if token == "" {
		token = strings.TrimSpace(os.Getenv("ATLAS_SERVE_TOKEN"))
	}
	if token == "" {
		var buf [24]byte
		if _, err := rand.Read(buf[:]); err == nil {
			token = hex.EncodeToString(buf[:])
		}
	}

	var server *httpapi.Server
	req := opts.assembly()
	req.Workdir = cwd
	req.EnableSubagents = true
	req.Ask = func(question assembly.Ask) assembly.Answer {
		if server == nil {
			return assembly.Answer{}
		}
		decision := server.AskContext(question.Context, httpapi.Question{
			Session: question.Session,
			Subject: question.Subject,
			Tool:    question.Tool,
		})
		return assembly.Answer{Allowed: decision.Allowed, Always: decision.Always}
	}

	kernel, notes, err := assembly.New(ctx, req)
	if err != nil {
		return nil, err
	}
	for _, note := range notes {
		fmt.Fprintln(stdio.err, "warning:", note)
	}
	for _, note := range kernel.Notices() {
		fmt.Fprintln(stdio.err, "note:", note)
	}

	webHost := webhost.New(kernel)
	server = httpapi.New(httpapi.Options{
		AuthToken: token,
		// The same credential admits the browser extension, so a headless
		// instance needs no second token.
		ExtensionToken:        token,
		Turn:                  serveKernelTurn(kernel),
		Compact:               serveKernelCompact(kernel),
		SideTurn:              serveKernelSideTurn(kernel),
		SessionTurn:           serveHostSessionTurn(webHost),
		SessionAttachmentTurn: serveHostSessionAttachmentTurn(webHost),
		SessionCompact:        serveHostSessionCompact(webHost),
		SessionSideTurn:       serveHostSessionSideTurn(webHost),
		Subagents:             webHost,
		UsageHistory:          webHost.UsageHistory,
		Host:                  webHost,
		Artifacts:             webHost,
		Facts: httpapi.Facts{
			Version:   version,
			Directory: kernel.Cwd,
			Model:     kernel.Selection.Name(),
		},
	})
	kernel.SetSubagentObserver(server.PublishSubagent)
	listenAddr := strings.TrimSpace(opts.listenAddr)
	if listenAddr == "" {
		listenAddr = "127.0.0.1:0"
	}
	boundAddr, err := server.ListenOn(listenAddr)
	if err != nil {
		kernel.Close()
		return nil, err
	}

	port := 0
	if _, rawPort, splitErr := net.SplitHostPort(boundAddr); splitErr == nil {
		port, _ = strconv.Atoi(rawPort)
	}
	meta := serveInfoMetadata{
		PID:       os.Getpid(),
		Addr:      boundAddr,
		Port:      port,
		URL:       "http://" + boundAddr,
		Version:   version,
		Cwd:       kernel.Cwd,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}

	infoPath := strings.TrimSpace(opts.serveInfo)
	if infoPath != "" {
		if writeErr := writeServeInfoFile(infoPath, meta); writeErr != nil {
			_ = server.Close()
			kernel.Close()
			return nil, writeErr
		}
	}

	var closeOnce sync.Once
	closeFn := func() {
		closeOnce.Do(func() {
			if infoPath != "" {
				removeOwnServeInfoFile(infoPath, meta)
			}
			_ = server.Close()
			kernel.Close()
		})
	}

	return &serveInstance{
		server:    server,
		kernel:    kernel,
		addr:      boundAddr,
		port:      port,
		info:      meta,
		closeFunc: closeFn,
	}, nil
}

func writeServeInfoFile(path string, meta serveInfoMetadata) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("serve-info mkdir: %w", err)
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("serve-info write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("serve-info rename: %w", err)
	}
	return nil
}

func removeOwnServeInfoFile(path string, expected serveInfoMetadata) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var current serveInfoMetadata
	if err := json.Unmarshal(raw, &current); err != nil {
		return
	}
	if current.PID == expected.PID && current.Addr == expected.Addr {
		_ = os.Remove(path)
	}
}

func serveReceiptFromCost(c cost.Cost, usage wire.Usage) httpapi.Receipt {
	return httpapi.Receipt{
		Fields:       c.Fields(usage),
		EndedAt:      c.Ended.UnixMilli(),
		DurationMs:   c.Took.Milliseconds(),
		TokensPerSec: c.TokensPerSec(usage.Output),
	}
}

func serveKernelTurn(kernel *assembly.Kernel) httpapi.Turn {
	return func(ctx context.Context, prompt string, emit func(wire.AgentEvent) error) (httpapi.Receipt, error) {
		timer := cost.NewTimer()
		result, err := kernel.App.Ask(ctx, wire.UserMessage(prompt), func(event wire.AgentEvent) error {
			timer.Observe(event)
			return emit(event)
		})
		return serveReceiptFromCost(timer.Cost(), result.Usage), err
	}
}

func serveKernelCompact(kernel *assembly.Kernel) httpapi.Compact {
	return func(ctx context.Context, note string, emit func(wire.AgentEvent) error) error {
		_, err := kernel.App.Compact(ctx, note, emit)
		return err
	}
}

func serveKernelSideTurn(kernel *assembly.Kernel) httpapi.Turn {
	return func(ctx context.Context, prompt string, emit func(wire.AgentEvent) error) (httpapi.Receipt, error) {
		side := kernel.App.Side()
		if side == nil {
			return httpapi.Receipt{}, errors.New("no side conversation is open")
		}
		timer := cost.NewTimer()
		result, err := side.Ask(ctx, wire.UserMessage(prompt), func(event wire.AgentEvent) error {
			timer.Observe(event)
			return emit(event)
		})
		return serveReceiptFromCost(timer.Cost(), result.Usage), err
	}
}

func serveHostSessionTurn(host *webhost.Host) httpapi.SessionTurn {
	return func(ctx context.Context, session, prompt string, emit func(wire.AgentEvent) error) (httpapi.Receipt, error) {
		timer := cost.NewTimer()
		usage, err := host.SessionAsk(ctx, session, prompt, func(event wire.AgentEvent) error {
			timer.Observe(event)
			return emit(event)
		})
		return serveReceiptFromCost(timer.Cost(), usage), err
	}
}

func serveHostSessionAttachmentTurn(host *webhost.Host) httpapi.SessionAttachmentTurn {
	return func(ctx context.Context, session, prompt string, attachments []httpapi.Attachment, emit func(wire.AgentEvent) error) (httpapi.Receipt, error) {
		timer := cost.NewTimer()
		usage, err := host.SessionAskWithAttachments(ctx, session, prompt, attachments, func(event wire.AgentEvent) error {
			timer.Observe(event)
			return emit(event)
		})
		return serveReceiptFromCost(timer.Cost(), usage), err
	}
}

func serveHostSessionCompact(host *webhost.Host) httpapi.SessionCompact {
	return func(ctx context.Context, session, note string, emit func(wire.AgentEvent) error) error {
		return host.SessionCompact(ctx, session, note, emit)
	}
}

func serveHostSessionSideTurn(host *webhost.Host) httpapi.SessionTurn {
	return func(ctx context.Context, session, prompt string, emit func(wire.AgentEvent) error) (httpapi.Receipt, error) {
		timer := cost.NewTimer()
		usage, err := host.SessionSideAsk(ctx, session, prompt, func(event wire.AgentEvent) error {
			timer.Observe(event)
			return emit(event)
		})
		return serveReceiptFromCost(timer.Cost(), usage), err
	}
}
