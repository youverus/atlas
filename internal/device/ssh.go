package device

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// probeResult captures what one single-shot SSH probe discovers on the remote host.
type probeResult struct {
	OS          string
	Arch        string
	Home        string
	AtlasBin    string
	AtlasVer    string
	HasConfig   bool
	ServePID    int
	ServeAddr   string
	ServePort   int
	ServeAlive  bool
	ServeVer    string
	DefaultPath string
}

// TunnelHandle represents one running SSH loopback port-forward process.
type TunnelHandle interface {
	Wait() error
	Close() error
}

// Runner abstracts SSH execution, stdin file upload, and port-forward tunnels
// so Manager can be tested offline without spawning real OpenSSH processes.
type Runner interface {
	Probe(ctx context.Context, dev Device, logFn func(string)) (probeResult, error)
	WriteFile(ctx context.Context, dev Device, remotePath, mode string, data io.Reader, logFn func(string)) error
	StartServe(ctx context.Context, dev Device, binPath, token string, logFn func(string)) (port int, ver string, err error)
	RestartServe(ctx context.Context, dev Device, oldPID int, binPath, token string, logFn func(string)) (port int, ver string, err error)
	OpenTunnel(dev Device, localPort, remotePort int) (TunnelHandle, error)
}

type execRunner struct{}

// BuildSSHBaseArgs constructs safe OpenSSH client flags without executing a shell.
func BuildSSHBaseArgs(dev Device, interactive bool) []string {
	args := []string{}
	if !interactive {
		args = append(args, "-o", "BatchMode=yes")
	}
	args = append(args,
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
	)
	if dev.Port > 0 {
		args = append(args, "-p", strconv.Itoa(dev.Port))
	}
	if ident := expandLocalTilde(dev.IdentityFile); ident != "" {
		args = append(args, "-i", ident, "-o", "IdentitiesOnly=yes")
	}
	args = append(args, sshDestination(dev))
	return args
}

// SSHInteractiveArgs builds the argv for `ssh -t ...` embedded remote terminal
// sessions anchored in remoteDir (or $HOME if remoteDir is empty).
func SSHInteractiveArgs(dev Device, remoteDir string) []string {
	args := []string{}
	if dev.Port > 0 {
		args = append(args, "-p", strconv.Itoa(dev.Port))
	}
	if ident := expandLocalTilde(dev.IdentityFile); ident != "" {
		args = append(args, "-i", ident, "-o", "IdentitiesOnly=yes")
	}
	args = append(args,
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-t",
		sshDestination(dev),
	)
	dir := strings.TrimSpace(remoteDir)
	if dir == "" {
		args = append(args, "exec ${SHELL:-/bin/sh} -l")
	} else {
		args = append(args, fmt.Sprintf("cd %s && exec ${SHELL:-/bin/sh} -l", shellSingleQuote(dir)))
	}
	return args
}

func sshDestination(dev Device) string {
	if dev.User != "" && !strings.Contains(dev.Host, "@") {
		return dev.User + "@" + dev.Host
	}
	return dev.Host
}

func expandLocalTilde(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if p == "~" {
				return home
			}
			return filepath.Join(home, strings.TrimPrefix(p, "~/"))
		}
	}
	return p
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func runSSHCommand(ctx context.Context, dev Device, stdin io.Reader, remoteCmd string) (string, string, error) {
	args := BuildSSHBaseArgs(dev, false)
	args = append(args, remoteCmd)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

const probeScript = `
OS=$(uname -s 2>/dev/null | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m 2>/dev/null)
case "$ARCH" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
esac
ATLAS_HOME_DIR="${ATLAS_HOME:-$HOME/.atlas}"
BIN=""
VER=""
for cand in "$ATLAS_HOME_DIR/bin/atlas" "$(command -v atlas 2>/dev/null || true)"; do
  if [ -n "$cand" ] && [ -x "$cand" ]; then
    BIN="$cand"
    VER=$("$cand" -version 2>/dev/null | head -n1 || true)
    break
  fi
done
HAS_CFG=0
if [ -f "$ATLAS_HOME_DIR/config.json" ]; then HAS_CFG=1; fi
SERVE_JSON=""
SERVE_ALIVE=0
if [ -f "$ATLAS_HOME_DIR/serve.json" ]; then
  SERVE_JSON=$(cat "$ATLAS_HOME_DIR/serve.json" 2>/dev/null | tr -d '\n')
  PID=$(printf '%s' "$SERVE_JSON" | sed -n 's/.*"pid"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p')
  if [ -n "$PID" ] && kill -0 "$PID" 2>/dev/null; then
    SERVE_ALIVE=1
  fi
fi
printf 'OS=%s\nARCH=%s\nHOME=%s\nATLAS_HOME=%s\nBIN=%s\nVER=%s\nHAS_CFG=%s\nSERVE_ALIVE=%s\nSERVE_JSON=%s\n' \
  "$OS" "$ARCH" "$HOME" "$ATLAS_HOME_DIR" "$BIN" "$VER" "$HAS_CFG" "$SERVE_ALIVE" "$SERVE_JSON"
`

func (r *execRunner) Probe(ctx context.Context, dev Device, logFn func(string)) (probeResult, error) {
	if logFn != nil {
		logFn("正在通过 SSH 探测远程环境...")
	}
	script := probeScript
	if custom := strings.TrimSpace(dev.RemoteBin); custom != "" {
		script = fmt.Sprintf(`CUSTOM_BIN=%s
OS=$(uname -s 2>/dev/null | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m 2>/dev/null)
case "$ARCH" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
esac
ATLAS_HOME_DIR="${ATLAS_HOME:-$HOME/.atlas}"
BIN=""
VER=""
if [ -x "$CUSTOM_BIN" ]; then
  BIN="$CUSTOM_BIN"
  VER=$("$CUSTOM_BIN" -version 2>/dev/null | head -n1 || true)
fi
HAS_CFG=0
if [ -f "$ATLAS_HOME_DIR/config.json" ]; then HAS_CFG=1; fi
SERVE_JSON=""
SERVE_ALIVE=0
if [ -f "$ATLAS_HOME_DIR/serve.json" ]; then
  SERVE_JSON=$(cat "$ATLAS_HOME_DIR/serve.json" 2>/dev/null | tr -d '\n')
  PID=$(printf '%%s' "$SERVE_JSON" | sed -n 's/.*"pid"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p')
  if [ -n "$PID" ] && kill -0 "$PID" 2>/dev/null; then
    SERVE_ALIVE=1
  fi
fi
printf 'OS=%%s\nARCH=%%s\nHOME=%%s\nATLAS_HOME=%%s\nBIN=%%s\nVER=%%s\nHAS_CFG=%%s\nSERVE_ALIVE=%%s\nSERVE_JSON=%%s\n' \
  "$OS" "$ARCH" "$HOME" "$ATLAS_HOME_DIR" "$BIN" "$VER" "$HAS_CFG" "$SERVE_ALIVE" "$SERVE_JSON"
`, shellSingleQuote(custom))
	}
	out, errOut, err := runSSHCommand(ctx, dev, strings.NewReader(script), "sh -s")
	if err != nil {
		msg := strings.TrimSpace(errOut)
		if msg == "" {
			msg = err.Error()
		}
		return probeResult{}, fmt.Errorf("device: ssh probe %s: %s", sshDestination(dev), msg)
	}
	var res probeResult
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "OS":
			res.OS = v
		case "ARCH":
			res.Arch = v
		case "HOME":
			res.Home = v
		case "ATLAS_HOME":
			res.DefaultPath = v
		case "BIN":
			res.AtlasBin = v
		case "VER":
			res.AtlasVer = v
		case "HAS_CFG":
			res.HasConfig = v == "1"
		case "SERVE_ALIVE":
			res.ServeAlive = v == "1"
		case "SERVE_JSON":
			if v != "" {
				var meta struct {
					PID     int    `json:"pid"`
					Addr    string `json:"addr"`
					Port    int    `json:"port"`
					Version string `json:"version"`
				}
				if json.Unmarshal([]byte(v), &meta) == nil {
					res.ServePID = meta.PID
					res.ServeAddr = meta.Addr
					res.ServePort = meta.Port
					res.ServeVer = meta.Version
				}
			}
		}
	}
	if res.OS == "" || res.Arch == "" {
		return probeResult{}, fmt.Errorf("device: failed to identify remote OS/arch on %s", sshDestination(dev))
	}
	return res, nil
}

func (r *execRunner) WriteFile(ctx context.Context, dev Device, remotePath, mode string, data io.Reader, logFn func(string)) error {
	if logFn != nil {
		logFn(fmt.Sprintf("正在上传文件到 %s ...", remotePath))
	}
	remoteDir := path.Dir(remotePath)
	tmpPath := fmt.Sprintf("%s.tmp.%d", remotePath, os.Getpid())
	remoteCmd := fmt.Sprintf(
		"umask 077 && mkdir -p %s && cat > %s && chmod %s %s && mv %s %s",
		shellSingleQuote(remoteDir),
		shellSingleQuote(tmpPath),
		mode,
		shellSingleQuote(tmpPath),
		shellSingleQuote(tmpPath),
		shellSingleQuote(remotePath),
	)
	_, errOut, err := runSSHCommand(ctx, dev, data, remoteCmd)
	if err != nil {
		msg := strings.TrimSpace(errOut)
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("device: upload %s: %s", remotePath, msg)
	}
	return nil
}

func (r *execRunner) RestartServe(ctx context.Context, dev Device, oldPID int, binPath, token string, logFn func(string)) (int, string, error) {
	if oldPID > 0 {
		if logFn != nil {
			logFn(fmt.Sprintf("正在重启远程 atlas -serve (旧 PID %d)...", oldPID))
		}
		_, _, _ = runSSHCommand(ctx, dev, nil, fmt.Sprintf("kill %d 2>/dev/null || true; sleep 0.3", oldPID))
	}
	return r.StartServe(ctx, dev, binPath, token, logFn)
}

func (r *execRunner) StartServe(ctx context.Context, dev Device, binPath, token string, logFn func(string)) (int, string, error) {
	if logFn != nil {
		logFn(fmt.Sprintf("正在启动远程服务 %s -serve ...", binPath))
	}
	script := fmt.Sprintf(`
ATLAS_HOME_DIR="${ATLAS_HOME:-$HOME/.atlas}"
mkdir -p "$ATLAS_HOME_DIR"
INFO_FILE="$ATLAS_HOME_DIR/serve.json"
LOG_FILE="$ATLAS_HOME_DIR/serve.log"
rm -f "$INFO_FILE"
ATLAS_SERVE_TOKEN=%s nohup %s -serve -listen 127.0.0.1:0 -serve-info "$INFO_FILE" >"$LOG_FILE" 2>&1 </dev/null &
i=0
while [ $i -lt 40 ]; do
  if [ -s "$INFO_FILE" ]; then
    cat "$INFO_FILE"
    exit 0
  fi
  i=$((i + 1))
  sleep 0.2
done
cat "$LOG_FILE" 2>/dev/null | tail -n 20 >&2
exit 1
`, shellSingleQuote(token), shellSingleQuote(binPath))
	out, errOut, err := runSSHCommand(ctx, dev, strings.NewReader(script), "sh -s")
	if err != nil {
		msg := strings.TrimSpace(errOut)
		if msg == "" {
			msg = err.Error()
		}
		return 0, "", fmt.Errorf("device: start remote atlas -serve: %s", msg)
	}
	var meta struct {
		Port    int    `json:"port"`
		Version string `json:"version"`
	}
	if decErr := json.Unmarshal([]byte(strings.TrimSpace(out)), &meta); decErr != nil || meta.Port <= 0 {
		return 0, "", fmt.Errorf("device: invalid serve metadata from remote: %q", strings.TrimSpace(out))
	}
	return meta.Port, meta.Version, nil
}

type execTunnel struct {
	cmd  *exec.Cmd
	done chan error
}

func (t *execTunnel) Wait() error {
	return <-t.done
}

func (t *execTunnel) Close() error {
	if t.cmd != nil && t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	return nil
}

func (r *execRunner) OpenTunnel(dev Device, localPort, remotePort int) (TunnelHandle, error) {
	args := BuildSSHBaseArgs(dev, false)
	// Insert -o ExitOnForwardFailure=yes -N -L before destination.
	dest := args[len(args)-1]
	tunnelArgs := make([]string, 0, len(args)+4)
	tunnelArgs = append(tunnelArgs, args[:len(args)-1]...)
	tunnelArgs = append(tunnelArgs,
		"-o", "ExitOnForwardFailure=yes",
		"-N",
		"-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", localPort, remotePort),
		dest,
	)
	cmd := exec.Command("ssh", tunnelArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("device: start ssh tunnel: %w", err)
	}
	h := &execTunnel{cmd: cmd, done: make(chan error, 1)}
	go func() {
		waitErr := cmd.Wait()
		if waitErr != nil && stderr.Len() > 0 {
			h.done <- fmt.Errorf("%w: %s", waitErr, strings.TrimSpace(stderr.String()))
		} else {
			h.done <- waitErr
		}
		close(h.done)
	}()
	return h, nil
}

func reserveLocalPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, nil
}

// resolveAtlasBinary finds or cross-compiles an `atlas` CLI binary for the
// target remote OS/arch, storing build artifacts under <home>/bin/<os>-<arch>/.
func resolveAtlasBinary(ctx context.Context, atlasHome, remoteOS, remoteArch, expectedVersion, sourceRoot string, logFn func(string)) (string, error) {
	if remoteOS != "linux" && remoteOS != "darwin" {
		return "", fmt.Errorf("device: unsupported remote OS %q", remoteOS)
	}
	if remoteArch != "amd64" && remoteArch != "arm64" {
		return "", fmt.Errorf("device: unsupported remote architecture %q", remoteArch)
	}

	cachePath := filepath.Join(atlasHome, "bin", remoteOS+"-"+remoteArch, "atlas")
	if expectedVersion != "" {
		cachePath = filepath.Join(atlasHome, "bin", remoteOS+"-"+remoteArch, "atlas-"+expectedVersion)
	}
	if info, err := os.Stat(cachePath); err == nil && !info.IsDir() && info.Size() > 0 {
		return cachePath, nil
	}

	// Same OS/arch host: check sibling binary next to our executable or in PATH.
	if remoteOS == runtime.GOOS && remoteArch == runtime.GOARCH {
		if exe, err := os.Executable(); err == nil {
			cand := filepath.Join(filepath.Dir(exe), "atlas")
			if binaryMatchesVersion(cand, expectedVersion) {
				return cand, nil
			}
			// Packaged macOS .app bundle layout: Contents/Resources/bin/atlas
			resCand := filepath.Join(filepath.Dir(exe), "..", "Resources", "atlas")
			if binaryMatchesVersion(resCand, expectedVersion) {
				return resCand, nil
			}
		}
		if inPath, err := exec.LookPath("atlas"); err == nil {
			if binaryMatchesVersion(inPath, expectedVersion) {
				return inPath, nil
			}
		}
	}

	// Fallback: cross-compile from source root if Go toolchain and cmd/atlas exist.
	goRoot := strings.TrimSpace(sourceRoot)
	if goRoot == "" {
		goRoot = detectSourceRoot()
	}
	if goRoot != "" {
		if _, goErr := exec.LookPath("go"); goErr == nil {
			if logFn != nil {
				logFn(fmt.Sprintf("正在为 %s/%s 交叉编译 atlas 二进制...", remoteOS, remoteArch))
			}
			if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
				return "", err
			}
			buildCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(buildCtx, "go", "build", "-trimpath", "-ldflags=-s -w", "-o", cachePath, "./cmd/atlas")
			cmd.Dir = goRoot
			cmd.Env = append(os.Environ(),
				"CGO_ENABLED=0",
				"GOOS="+remoteOS,
				"GOARCH="+remoteArch,
			)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				return "", fmt.Errorf("device: cross-compile atlas for %s/%s: %v: %s", remoteOS, remoteArch, err, strings.TrimSpace(stderr.String()))
			}
			return cachePath, nil
		}
	}

	return "", fmt.Errorf("device: 无法找到适用于 %s/%s 的 atlas CLI 二进制，请将其置于 %s 或在远程机器预装 ~/.atlas/bin/atlas", remoteOS, remoteArch, cachePath)
}

func binaryMatchesVersion(binPath, expected string) bool {
	info, err := os.Stat(binPath)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return false
	}
	if expected == "" {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binPath, "-version").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == expected
}

func detectSourceRoot() string {
	candidates := []string{}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, wd, filepath.Join(wd, "go"), filepath.Join(wd, ".."))
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates, dir, filepath.Join(dir, ".."), filepath.Join(dir, "..", ".."))
	}
	for _, dir := range candidates {
		if _, err := os.Stat(filepath.Join(dir, "cmd", "atlas", "serve.go")); err == nil {
			return dir
		}
	}
	return ""
}

// errorsIsCanceled distinguishes context cancellation from transport failures.
func errorsIsCanceled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
