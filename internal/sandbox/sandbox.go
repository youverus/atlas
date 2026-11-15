// Package sandbox confines the process tree a command starts.
//
// atlas does not implement a sandbox; the kernel does. What lives here is the
// policy — which directories a confined command may write and whether it may
// reach the network — the compiler that turns that policy into a platform
// profile, and the choice of where the profile is applied. On macOS the profile
// is a Seatbelt policy handed to /usr/bin/sandbox-exec, which applies it and
// then execs the command, so the pid the caller started is the pid that runs
// and the process group the shell tool kills is still the right one.
//
// The scope is one process tree, not the whole process. atlas's own write and
// edit tools run inside this process and are therefore outside the profile;
// they are bounded by the tool's path rules instead. Confining the whole
// process would mean re-executing the interface inside the profile, and the
// price — the terminal, signal handling, the exit code — is larger than the hole
// it closes. go/docs/sandbox.md records the trade and what is left open.
package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// Marker is the environment variable a wrapped command exports. A confined
	// process that starts another atlas command would otherwise apply a second
	// profile, and Seatbelt refuses that with "sandbox_apply: Operation not
	// permitted" — a failure that reads as a broken command rather than as a
	// nested sandbox. A nested wrap therefore returns the command as it is: it
	// is already confined by the profile that started it.
	Marker = "ATLAS_SANDBOXED"

	// DefaultDepth bounds the scan for the paths where a write becomes a
	// program another process runs. Three levels reaches the workspace's own
	// .git and a nested repository one directory down, which is what the scan
	// exists for. Deeper spends time on every command for a case that barely
	// happens.
	DefaultDepth = 3

	// logTag is what a refused operation is tagged with in the system log,
	// which is where Seatbelt records violations. Reading that log is not part
	// of this version; the tag is here so that a message a reader finds later
	// can be traced back to this profile.
	logTag = "atlas-sandbox"
)

// Policy is what a confined process tree may touch.
//
// The zero value confines everything, which is why a caller has to name at least
// one writable directory: a policy with no writable root is refused rather than
// silently producing a sandbox where every command fails.
type Policy struct {
	// Writable are the directories a confined command may write inside. They
	// are also the directories scanned for paths that must stay unwritable
	// even though they sit inside one.
	Writable []string

	// DenyRead are the paths a confined command may not read, even though the
	// rest of the filesystem stays readable. On macOS each entry becomes a
	// (deny file-read* (subpath ...)) after the broad read grant, paired with
	// (allow file-read-metadata (vnode-type DIRECTORY)) so listing a parent
	// directory does not fail on the existence of a hidden entry. On Linux a
	// directory is covered by --tmpfs and a file by --ro-bind /dev/null. A
	// writable root that sits inside a denied ancestor is re-opened for reading
	// after the deny, because a directory a command cannot read is one it
	// cannot work in either.
	DenyRead []string

	// Network allows outbound connections. It is all or nothing: Seatbelt's
	// network rules match * or localhost and never a host name or a port list,
	// so a domain allowlist would have to be a proxy running outside the
	// sandbox. This version does not have one.
	Network bool

	// Depth bounds the scan. Zero means DefaultDepth.
	Depth int
}

// Sandbox is a Policy bound to this platform's executor.
type Sandbox struct {
	policy   Policy
	executor string
	reason   string
}

// New binds a policy to this platform's executor.
//
// It does not check that the executor exists. The check happens on every wrap,
// so installing the missing program does not need a restart, and a machine
// without it reports the missing program on the command that needed it instead
// of refusing to start atlas. Available is the same check, for a caller that
// wants to say it once at startup.
func New(policy Policy) *Sandbox {
	executor, reason := defaultExecutor()
	return &Sandbox{policy: policy, executor: executor, reason: reason}
}

// Available reports whether a command can actually be confined here.
//
// It depends on the platform and the executor, not on the policy, so a caller
// that wants to warn about it once at startup can ask without building a
// Sandbox: atlas builds one confinement per workspace, and only the first of
// them would ever carry the warning.
func Available() error {
	executor, reason := defaultExecutor()
	return available(executor, reason)
}

// Available reports whether a command can actually be confined here, with this
// sandbox's executor.
func (s *Sandbox) Available() error { return available(s.executor, s.reason) }

func available(executor, reason string) error {
	if executor == "" {
		return fmt.Errorf("sandbox: %s", reason)
	}
	if _, err := os.Stat(executor); err != nil {
		return fmt.Errorf("sandbox: %s is not usable: %w", executor, err)
	}
	return nil
}

// Wrap returns the argv and the environment that run the command confined.
//
// An error means the command must not run: the caller reports it and the command
// does not happen. Returning the original argv instead would turn a sandbox that
// could not be applied into a silent grant of the process's full permissions,
// which is the one failure a sandbox must not have.
func (s *Sandbox) Wrap(argv []string, env []string) ([]string, []string, error) {
	if len(argv) == 0 {
		return nil, nil, errors.New("sandbox: nothing to run")
	}
	if value, ok := lookupEnv(env, Marker); ok && value == "1" {
		return argv, env, nil
	}
	if err := s.Available(); err != nil {
		return nil, nil, fmt.Errorf("%w; remove the sandbox block from config.json to run bash without it", err)
	}
	resolved, err := resolve(s.policy)
	if err != nil {
		return nil, nil, err
	}
	if filepath.Base(s.executor) == "bwrap" {
		return resolved.bwrap(s.executor, argv), s.environment(env), nil
	}
	wrapped := make([]string, 0, len(argv)+3)
	wrapped = append(wrapped, s.executor, "-p", resolved.render())
	wrapped = append(wrapped, argv...)
	return wrapped, s.environment(env), nil
}

// profile resolves the policy and renders it as a Seatbelt profile.
//
// Both steps happen per command rather than once at construction because the
// workspace changes underneath a session: a `git init` creates a .git/hooks
// directory, and a list captured before that would miss exactly the directory an
// escape needs.
func (s *Sandbox) profile() (string, error) {
	state, err := resolve(s.policy)
	if err != nil {
		return "", err
	}
	return state.render(), nil
}

// environment is the environment a confined command runs with.
//
// Two things happen here. Every GIT_* variable is dropped, because each one
// redirects git somewhere the policy did not account for: GIT_DIR names a
// repository outside the workspace, GIT_CONFIG_GLOBAL a config that names a
// pager, GIT_TEMPLATE_DIR a template that carries hooks. Then a fixed set is
// added: the marker that keeps a nested wrap from applying a second profile, the
// three settings that stop git from prompting or from taking an index lock the
// profile would refuse, LC_ALL=C so a refusal reads the same in every locale, and
// an empty template directory so `git init` and `git clone` never create a hooks
// directory — the profile refuses to write one, and git would otherwise fail the
// whole command on it.
func (s *Sandbox) environment(env []string) []string {
	out := make([]string, 0, len(env)+6)
	for _, entry := range env {
		if key, _, ok := strings.Cut(entry, "="); ok && strings.HasPrefix(key, "GIT_") {
			continue
		}
		out = append(out, entry)
	}
	out = append(out,
		Marker+"=1",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_PAGER=cat",
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
	)
	if dir := templateDir(); dir != "" {
		out = append(out, "GIT_TEMPLATE_DIR="+dir)
	}
	return out
}

// templateDir is a directory that holds no git templates.
//
// git copies its template directory into every repository it creates, and the
// sample hooks are what that directory carries. Pointing git at an empty one is
// what lets the profile refuse .git/hooks unconditionally: with templates,
// `git init` tries to create the directory and fails the whole command; without
// them it never touches it. The path is fixed rather than per-run so that
// repeated sessions do not leave one directory behind each.
func templateDir() string {
	templateOnce.Do(func() {
		cache, err := os.UserCacheDir()
		if err != nil {
			return
		}
		dir := filepath.Join(cache, "atlas", "git-template")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return
		}
		templatePath = dir
	})
	return templatePath
}

var (
	templateOnce sync.Once
	templatePath string
)

// lookupEnv finds one variable in a raw environment list. It does not consult
// the process environment: the caller's list is the whole environment the
// command will see, and a fallback would make the answer depend on how atlas was
// started.
func lookupEnv(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, prefix); ok {
			return value, true
		}
	}
	return "", false
}
