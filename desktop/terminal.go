package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"pi-go/internal/httpapi"
)

const (
	terminalBacklogLimit = 512 * 1024
	terminalSubscriberQ  = 256
)

var darwinGUIFallbackPaths = []string{
	"/opt/homebrew/bin",
	"/opt/homebrew/sbin",
	"/usr/local/bin",
	"/usr/local/sbin",
	"/usr/bin",
	"/bin",
	"/usr/sbin",
	"/sbin",
}

// terminalManager owns interactive PTY sessions for the desktop shell.
type terminalManager struct {
	mu             sync.Mutex
	closed         bool
	sessions       map[string]*terminalSession
	seq            atomic.Uint64
	remoteResolver func(deviceID, dir string) ([]string, string, error)
}

func newTerminalManager() *terminalManager {
	return &terminalManager{
		sessions: make(map[string]*terminalSession),
	}
}

// SetRemoteResolver registers how CreateRemoteTerminal constructs `ssh -t`
// command arguments for a remote device and directory.
func (m *terminalManager) SetRemoteResolver(fn func(deviceID, dir string) ([]string, string, error)) {
	m.mu.Lock()
	m.remoteResolver = fn
	m.mu.Unlock()
}

func (m *terminalManager) CreateTerminal(dir string, cols, rows int) (httpapi.TerminalSessionInfo, error) {
	cwd, err := resolveTerminalCwd(dir)
	if err != nil {
		return httpapi.TerminalSessionInfo{}, err
	}
	shellPath, err := resolveTerminalShell()
	if err != nil {
		return httpapi.TerminalSessionInfo{}, err
	}
	cmd := exec.Command(shellPath)
	cmd.Dir = cwd
	cmd.Env = buildTerminalEnv(os.Environ())
	return m.spawnPTYCommand(cmd, shellPath, cwd, cols, rows)
}

// CreateRemoteTerminal spawns an interactive `ssh -t` shell inside the active
// remote project directory.
func (m *terminalManager) CreateRemoteTerminal(deviceID, dir string, cols, rows int) (httpapi.TerminalSessionInfo, error) {
	m.mu.Lock()
	resolver := m.remoteResolver
	m.mu.Unlock()
	if resolver == nil {
		return httpapi.TerminalSessionInfo{}, errors.New("atlas-desktop: remote SSH terminal is not configured")
	}
	sshArgs, devName, err := resolver(deviceID, dir)
	if err != nil {
		return httpapi.TerminalSessionInfo{}, err
	}
	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		return httpapi.TerminalSessionInfo{}, fmt.Errorf("atlas-desktop: ssh binary not found: %w", err)
	}
	cmd := exec.Command(sshBin, sshArgs...)
	if home, hErr := os.UserHomeDir(); hErr == nil && home != "" {
		cmd.Dir = home
	}
	cmd.Env = buildTerminalEnv(os.Environ())
	label := "ssh"
	if devName != "" {
		label = "ssh:" + devName
	}
	return m.spawnPTYCommand(cmd, label, dir, cols, rows)
}

func (m *terminalManager) spawnPTYCommand(cmd *exec.Cmd, shellLabel, cwd string, cols, rows int) (httpapi.TerminalSessionInfo, error) {
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return httpapi.TerminalSessionInfo{}, errors.New("atlas-desktop: terminal manager is closed")
	}
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}

	ptm, err := startPTYCommand(cmd, cols, rows)
	if err != nil {
		return httpapi.TerminalSessionInfo{}, fmt.Errorf("atlas-desktop: start terminal: %w", err)
	}

	id := "term-" + strconv.FormatUint(m.seq.Add(1), 10)
	sess := &terminalSession{
		id:    id,
		shell: shellLabel,
		cwd:   cwd,
		cmd:   cmd,
		ptm:   ptm,
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		sess.close()
		return httpapi.TerminalSessionInfo{}, errors.New("atlas-desktop: terminal manager is closed")
	}
	m.sessions[id] = sess
	m.mu.Unlock()

	sess.startIO()
	return httpapi.TerminalSessionInfo{
		ID:    id,
		Shell: shellLabel,
		CWD:   cwd,
	}, nil
}

func (m *terminalManager) session(id string) (*terminalSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess := m.sessions[id]
	if sess == nil {
		return nil, fmt.Errorf("atlas-desktop: terminal %q not found", id)
	}
	return sess, nil
}

func (m *terminalManager) WriteTerminal(id string, data []byte) error {
	sess, err := m.session(id)
	if err != nil {
		return err
	}
	return sess.write(data)
}

func (m *terminalManager) ResizeTerminal(id string, cols, rows int) error {
	sess, err := m.session(id)
	if err != nil {
		return err
	}
	return sess.resize(cols, rows)
}

func (m *terminalManager) CloseTerminal(id string) error {
	m.mu.Lock()
	sess := m.sessions[id]
	if sess != nil {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if sess == nil {
		return fmt.Errorf("atlas-desktop: terminal %q not found", id)
	}
	sess.close()
	return nil
}

func (m *terminalManager) SubscribeTerminal(id string) (httpapi.TerminalSubscription, error) {
	sess, err := m.session(id)
	if err != nil {
		return httpapi.TerminalSubscription{}, err
	}
	return sess.subscribe(), nil
}

func (m *terminalManager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	list := make([]*terminalSession, 0, len(m.sessions))
	for _, sess := range m.sessions {
		list = append(list, sess)
	}
	m.sessions = make(map[string]*terminalSession)
	m.mu.Unlock()

	for _, sess := range list {
		sess.close()
	}
}

type terminalSession struct {
	id    string
	shell string
	cwd   string
	cmd   *exec.Cmd
	ptm   *os.File

	mu           sync.Mutex
	closed       bool
	exited       bool
	exitCode     int
	backlog      [][]byte
	backlogBytes int
	sub          chan httpapi.TerminalEvent
}

func (s *terminalSession) startIO() {
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 16*1024)
		for {
			n, err := s.ptm.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				s.emitData(chunk)
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					// Normal when the slave closes or session is closed.
				}
				return
			}
		}
	}()

	go func() {
		waitErr := s.cmd.Wait()
		code := 0
		if waitErr != nil {
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				code = exitErr.ExitCode()
			} else {
				code = 1
			}
		}
		_ = s.ptm.Close()
		<-readDone
		s.emitExit(code)
	}()
}

func (s *terminalSession) emitData(chunk []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if s.sub != nil {
		select {
		case s.sub <- httpapi.TerminalEvent{Type: "data", Data: chunk}:
		default:
			// Drop oldest chunk if subscriber fell behind to keep PTY flowing.
			select {
			case <-s.sub:
			default:
			}
			select {
			case s.sub <- httpapi.TerminalEvent{Type: "data", Data: chunk}:
			default:
			}
		}
		return
	}
	s.backlog = append(s.backlog, chunk)
	s.backlogBytes += len(chunk)
	for s.backlogBytes > terminalBacklogLimit && len(s.backlog) > 0 {
		s.backlogBytes -= len(s.backlog[0])
		s.backlog = s.backlog[1:]
	}
}

func (s *terminalSession) emitExit(code int) {
	s.mu.Lock()
	s.exited = true
	s.exitCode = code
	sub := s.sub
	s.sub = nil
	s.mu.Unlock()

	if sub != nil {
		select {
		case sub <- httpapi.TerminalEvent{Type: "exit", ExitCode: code}:
		default:
		}
		close(sub)
	}
}

func (s *terminalSession) subscribe() httpapi.TerminalSubscription {
	s.mu.Lock()
	backlog := s.backlog
	s.backlog = nil
	s.backlogBytes = 0
	if s.exited || s.closed {
		code := s.exitCode
		s.mu.Unlock()
		return httpapi.TerminalSubscription{
			Backlog:  backlog,
			Exited:   true,
			ExitCode: code,
			Cancel:   func() {},
		}
	}
	if s.sub != nil {
		close(s.sub)
	}
	ch := make(chan httpapi.TerminalEvent, terminalSubscriberQ)
	s.sub = ch
	s.mu.Unlock()

	cancel := func() {
		s.mu.Lock()
		if s.sub == ch {
			s.sub = nil
			close(ch)
		}
		s.mu.Unlock()
	}
	return httpapi.TerminalSubscription{
		Backlog: backlog,
		Events:  ch,
		Cancel:  cancel,
	}
}

func (s *terminalSession) write(data []byte) error {
	s.mu.Lock()
	closed := s.closed || s.exited
	s.mu.Unlock()
	if closed {
		return errors.New("atlas-desktop: terminal session has exited")
	}
	_, err := s.ptm.Write(data)
	return err
}

func (s *terminalSession) resize(cols, rows int) error {
	s.mu.Lock()
	closed := s.closed || s.exited
	s.mu.Unlock()
	if closed {
		return nil
	}
	return resizePTY(s.ptm, cols, rows)
}

func (s *terminalSession) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	sub := s.sub
	s.sub = nil
	s.mu.Unlock()

	if sub != nil {
		close(sub)
	}
	terminatePTYProcess(s.cmd)
	_ = s.ptm.Close()
}

func resolveTerminalCwd(dir string) (string, error) {
	trimmed := strings.TrimSpace(dir)
	if trimmed != "" {
		abs, err := filepath.Abs(trimmed)
		if err != nil {
			return "", fmt.Errorf("atlas-desktop: resolve directory %q: %w", dir, err)
		}
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			return abs, nil
		}
		return "", fmt.Errorf("atlas-desktop: directory %q is unavailable", abs)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home, nil
	}
	return "/", nil
}

func resolveTerminalShell() (string, error) {
	candidates := []string{os.Getenv("SHELL"), "/bin/zsh", "/bin/bash", "/bin/sh"}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", errors.New("atlas-desktop: no executable shell found")
}

func buildTerminalEnv(base []string) []string {
	envMap := make(map[string]string, len(base)+8)
	keys := make([]string, 0, len(base)+8)
	set := func(k, v string) {
		if _, ok := envMap[k]; !ok {
			keys = append(keys, k)
		}
		envMap[k] = v
	}
	for _, entry := range base {
		k, v, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		set(k, v)
	}

	if runtime.GOOS == "darwin" {
		set("PATH", mergePathEntries(envMap["PATH"], darwinGUIFallbackPaths))
	}
	set("TERM", "xterm-256color")
	if strings.TrimSpace(envMap["COLORTERM"]) == "" {
		set("COLORTERM", "truecolor")
	}
	if strings.TrimSpace(envMap["TERM_PROGRAM"]) == "" {
		set("TERM_PROGRAM", "Atlas")
	}
	fallbackLocale := resolveFallbackUTF8Locale(envMap)
	if isMissingOrCLocale(envMap["LANG"]) {
		set("LANG", fallbackLocale)
	}
	if isMissingOrCLocale(envMap["LC_CTYPE"]) {
		set("LC_CTYPE", fallbackLocale)
	}
	if v, ok := envMap["LC_ALL"]; ok && isMissingOrCLocale(v) {
		set("LC_ALL", fallbackLocale)
	}

	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+envMap[k])
	}
	return out
}

func mergePathEntries(current string, extras []string) string {
	seen := make(map[string]struct{})
	var merged []string
	add := func(part string) {
		part = strings.TrimSpace(part)
		if part == "" {
			return
		}
		if _, ok := seen[part]; ok {
			return
		}
		seen[part] = struct{}{}
		merged = append(merged, part)
	}
	for _, part := range strings.Split(current, string(os.PathListSeparator)) {
		add(part)
	}
	for _, part := range extras {
		add(part)
	}
	return strings.Join(merged, string(os.PathListSeparator))
}

func isMissingOrCLocale(val string) bool {
	upper := strings.ToUpper(strings.TrimSpace(val))
	return upper == "" || upper == "C" || upper == "POSIX"
}

func resolveFallbackUTF8Locale(env map[string]string) string {
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if strings.Contains(strings.ToLower(env[k]), "utf-8") || strings.Contains(strings.ToLower(env[k]), "utf8") {
			return env[k]
		}
	}
	return "en_US.UTF-8"
}
