package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"pi-go/internal/httpapi"
)

func TestBuildTerminalEnvSetsTermPathAndUTF8Locale(t *testing.T) {
	env := buildTerminalEnv([]string{
		"PATH=/usr/bin:/bin",
		"LANG=C",
		"TERM=dumb",
	})
	kv := make(map[string]string, len(env))
	for _, item := range env {
		k, v, _ := strings.Cut(item, "=")
		kv[k] = v
	}
	if kv["TERM"] != "xterm-256color" {
		t.Fatalf("TERM = %q, want xterm-256color", kv["TERM"])
	}
	if kv["COLORTERM"] != "truecolor" {
		t.Fatalf("COLORTERM = %q, want truecolor", kv["COLORTERM"])
	}
	if kv["LANG"] != "en_US.UTF-8" || kv["LC_CTYPE"] != "en_US.UTF-8" {
		t.Fatalf("locale = LANG:%q LC_CTYPE:%q", kv["LANG"], kv["LC_CTYPE"])
	}
	if runtime.GOOS == "darwin" && !strings.Contains(kv["PATH"], "/opt/homebrew/bin") {
		t.Fatalf("PATH missing homebrew fallback: %q", kv["PATH"])
	}
}

func TestTerminalManagerCreatesStreamsWritesResizesAndCloses(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin PTY test")
	}

	dir := t.TempDir()
	mgr := newTerminalManager()
	defer mgr.Close()

	info, err := mgr.CreateTerminal(dir, 80, 24)
	if err != nil {
		t.Fatalf("CreateTerminal: %v", err)
	}
	if info.ID == "" || info.CWD != dir {
		t.Fatalf("unexpected info: %+v", info)
	}

	sub, err := mgr.SubscribeTerminal(info.ID)
	if err != nil {
		t.Fatalf("SubscribeTerminal: %v", err)
	}
	defer sub.Cancel()

	if err := mgr.ResizeTerminal(info.ID, 100, 30); err != nil {
		t.Fatalf("ResizeTerminal: %v", err)
	}
	if err := mgr.WriteTerminal(info.ID, []byte("pwd\n")); err != nil {
		t.Fatalf("WriteTerminal: %v", err)
	}

	var buf strings.Builder
	for _, b := range sub.Backlog {
		buf.Write(b)
	}

	deadline := time.After(5 * time.Second)
	for !strings.Contains(buf.String(), filepath.Base(dir)) {
		select {
		case ev, ok := <-sub.Events:
			if !ok {
				t.Fatalf("stream closed early; output=%q", buf.String())
			}
			if ev.Type == "data" {
				buf.Write(ev.Data)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for pwd output; got %q", buf.String())
		}
	}

	if err := mgr.WriteTerminal(info.ID, []byte("exit 0\n")); err != nil {
		t.Fatalf("WriteTerminal exit: %v", err)
	}

	var exited bool
	exitDeadline := time.After(5 * time.Second)
	for !exited {
		select {
		case ev, ok := <-sub.Events:
			if !ok {
				exited = true
			} else if ev.Type == "exit" {
				exited = true
				if ev.ExitCode != 0 {
					t.Fatalf("exitCode = %d, want 0", ev.ExitCode)
				}
			}
		case <-exitDeadline:
			t.Fatalf("timed out waiting for exit event")
		}
	}

	_ = mgr.CloseTerminal(info.ID)
}

func TestTerminalManagerRejectsMissingDirectory(t *testing.T) {
	mgr := newTerminalManager()
	defer mgr.Close()

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := mgr.CreateTerminal(missing, 80, 24); err == nil {
		t.Fatal("expected error for missing directory")
	}
	// File path instead of directory must also fail.
	f := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := mgr.CreateTerminal(f, 80, 24); err == nil {
		t.Fatal("expected error for file target")
	}
	var _ httpapi.TerminalController = mgr
	var _ httpapi.RemoteTerminalController = mgr
}

func TestTerminalManagerCreateRemoteTerminalRequiresResolverAndValidates(t *testing.T) {
	mgr := newTerminalManager()
	defer mgr.Close()

	if _, err := mgr.CreateRemoteTerminal("gpu-1", "/srv/app", 80, 24); err == nil {
		t.Fatal("expected error when remote resolver is not configured")
	}

	var gotDev, gotDir string
	mgr.SetRemoteResolver(func(deviceID, dir string) ([]string, string, error) {
		gotDev = deviceID
		gotDir = dir
		return []string{"-V"}, "GPU 1", nil
	})
	info, err := mgr.CreateRemoteTerminal("gpu-1", "/srv/app", 80, 24)
	if err != nil {
		t.Fatalf("CreateRemoteTerminal(-V): %v", err)
	}
	if gotDev != "gpu-1" || gotDir != "/srv/app" || info.Shell != "ssh:GPU 1" || info.CWD != "/srv/app" {
		t.Fatalf("unexpected remote terminal result: dev=%q dir=%q info=%+v", gotDev, gotDir, info)
	}
	_ = mgr.CloseTerminal(info.ID)
}
