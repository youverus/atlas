package browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// LaunchOptions configures how Atlas spawns the managed Chromium instance.
type LaunchOptions struct {
	Executable string
	ProfileDir string
	Headless   bool
}

type launchedBrowser struct {
	cmd        *exec.Cmd
	executable string
	profileDir string
	wsURL      string
	exited     chan struct{}
}

// FindExecutable locates a Chromium-family browser binary on the host machine.
// ATLAS_BROWSER_BIN takes precedence when set.
func FindExecutable() string {
	if override := strings.TrimSpace(os.Getenv("ATLAS_BROWSER_BIN")); override != "" {
		if info, err := os.Stat(override); err == nil && !info.IsDir() {
			return override
		}
		if found, err := exec.LookPath(override); err == nil {
			return found
		}
	}

	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
			"/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
		}
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			candidates = append(candidates,
				filepath.Join(home, "Applications/Google Chrome.app/Contents/MacOS/Google Chrome"),
				filepath.Join(home, "Applications/Chromium.app/Contents/MacOS/Chromium"),
			)
		}
	case "windows":
		candidates = []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		}
	default:
		for _, bin := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "brave-browser"} {
			if found, err := exec.LookPath(bin); err == nil {
				return found
			}
		}
	}

	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	for _, bin := range []string{"google-chrome", "chromium", "chromium-browser"} {
		if found, err := exec.LookPath(bin); err == nil {
			return found
		}
	}
	return ""
}

// DefaultProfileDir returns the isolated Chromium user-data-dir used by Atlas.
func DefaultProfileDir() string {
	if home := strings.TrimSpace(os.Getenv("ATLAS_HOME")); home != "" {
		return filepath.Join(home, "browser-profiles", "default")
	}
	userHome, err := os.UserHomeDir()
	if err != nil || userHome == "" {
		return filepath.Join(os.TempDir(), "atlas-browser-default")
	}
	return filepath.Join(userHome, ".atlas", "browser-profiles", "default")
}

func launchBrowser(ctx context.Context, opts LaunchOptions) (*launchedBrowser, error) {
	exe := strings.TrimSpace(opts.Executable)
	if exe == "" {
		exe = FindExecutable()
	}
	if exe == "" {
		return nil, errors.New("browser: no Chrome/Chromium executable found; install Google Chrome or set ATLAS_BROWSER_BIN")
	}

	profileDir := strings.TrimSpace(opts.ProfileDir)
	if profileDir == "" {
		profileDir = DefaultProfileDir()
	}
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		return nil, fmt.Errorf("browser: create profile dir: %w", err)
	}

	portFile := filepath.Join(profileDir, "DevToolsActivePort")
	_ = os.Remove(portFile)

	args := buildLaunchArgs(profileDir, opts)

	cmd := exec.Command(exe, args...)
	// Detach stdout/stderr to avoid blocking pipes in long-lived desktop runs.
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("browser: start %s: %w", exe, err)
	}

	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()

	wsURL, err := waitForDevToolsActivePort(ctx, portFile, exited)
	if err != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return nil, err
	}

	return &launchedBrowser{
		cmd:        cmd,
		executable: exe,
		profileDir: profileDir,
		wsURL:      wsURL,
		exited:     exited,
	}, nil
}

func waitForDevToolsActivePort(ctx context.Context, portFile string, exited <-chan struct{}) (string, error) {
	deadline := time.NewTimer(12 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()

	for {
		if data, err := os.ReadFile(portFile); err == nil {
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) >= 2 && strings.TrimSpace(lines[0]) != "" && strings.HasPrefix(strings.TrimSpace(lines[1]), "/") {
				return fmt.Sprintf("ws://127.0.0.1:%s%s", strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])), nil
			}
		}
		select {
		case <-exited:
			return "", errors.New("browser: Chromium exited before exposing DevToolsActivePort (is another instance using this profile?)")
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			return "", errors.New("browser: timed out waiting for Chromium DevToolsActivePort")
		case <-ticker.C:
		}
	}
}

func buildLaunchArgs(profileDir string, opts LaunchOptions) []string {
	args := []string{
		"--remote-debugging-port=0",
		"--user-data-dir=" + profileDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-backgrounding-occluded-windows",
		"--new-window",
	}
	if opts.Headless {
		args = append(args, "--headless=new", "--disable-gpu")
	}
	return append(args, "about:blank")
}

func resolveHeadless(opts Options, goos string, getenv func(string) string) bool {
	if opts.HeadlessOverride != nil {
		return *opts.HeadlessOverride
	}
	if opts.Headless {
		return true
	}
	if getenv != nil {
		if raw := strings.TrimSpace(getenv("ATLAS_BROWSER_HEADLESS")); raw != "" {
			if parsed, err := strconv.ParseBool(raw); err == nil {
				return parsed
			}
		}
	}
	return isHeadlessEnvironment(goos, getenv)
}

func isHeadlessEnvironment(goos string, getenv func(string) string) bool {
	if goos == "darwin" || goos == "windows" {
		return false
	}
	if getenv == nil {
		return true
	}
	return strings.TrimSpace(getenv("DISPLAY")) == "" && strings.TrimSpace(getenv("WAYLAND_DISPLAY")) == ""
}

func (b *launchedBrowser) Close() {
	if b == nil || b.cmd == nil || b.cmd.Process == nil {
		return
	}
	select {
	case <-b.exited:
		return
	default:
	}
	_ = b.cmd.Process.Signal(os.Interrupt)
	select {
	case <-b.exited:
		return
	case <-time.After(1500 * time.Millisecond):
	}
	_ = b.cmd.Process.Kill()
	<-b.exited
}
