package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// state is a Policy with every path made absolute and free of symlinks.
//
// The symlink step is not tidiness. Seatbelt matches the path the kernel
// resolved, so a rule naming /tmp never matches a write to /private/tmp: on
// macOS /tmp is a symlink, and the rule does nothing at all. The same trap hides
// /var, which is why a writable $TMPDIR spelled /var/folders/... is silently
// useless, and why every directory here goes through EvalSymlinks first.
type state struct {
	writable []string
	deny     []string
	denyRead []string
	network  bool
}

// resolve makes the policy absolute and adds what the scan found inside the
// writable roots.
func resolve(policy Policy) (state, error) {
	writable := make([]string, 0, len(policy.Writable))
	for _, root := range policy.Writable {
		if path, ok := canonical(root); ok {
			writable = append(writable, path)
		}
	}
	if len(writable) == 0 {
		return state{}, fmt.Errorf("sandbox: none of the %d writable roots exist, so a confined command could not write anywhere",
			len(policy.Writable))
	}
	denyRead := make([]string, 0, len(policy.DenyRead))
	for _, raw := range policy.DenyRead {
		if path, ok := canonicalOrParent(raw); ok {
			denyRead = append(denyRead, path)
		}
	}
	depth := policy.Depth
	if depth <= 0 {
		depth = DefaultDepth
	}
	return state{
		writable: writable,
		deny:     unique(scan(writable, depth)),
		denyRead: unique(denyRead),
		network:  policy.Network,
	}, nil
}

// canonicalOrParent resolves a path when it exists, or its parent directory
// when only the parent exists. A read-denied directory such as ~/.ssh may not
// exist on a fresh machine yet, and resolving its parent lets Seatbelt name the
// real /Users/... path before anything creates it.
func canonicalOrParent(path string) (string, bool) {
	if resolved, ok := canonical(path); ok {
		return resolved, true
	}
	if path == "" {
		return "", false
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	parent, ok := canonical(filepath.Dir(absolute))
	if !ok {
		return "", false
	}
	return filepath.Join(parent, filepath.Base(absolute)), true
}

// within reports whether child is parent or sits inside it. Both paths are
// already canonical.
func within(child, parent string) bool {
	if child == parent {
		return true
	}
	if parent == string(filepath.Separator) {
		return strings.HasPrefix(child, parent)
	}
	return strings.HasPrefix(child, parent+string(filepath.Separator))
}

// canonical returns the path the kernel sees, and false when it cannot be
// resolved.
//
// A missing path is not an error. A guarded path that was deleted between two
// commands is not worth failing a command over, and a writable root that is
// missing is caught by the caller, which refuses a policy where nothing is
// writable.
func canonical(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", false
	}
	return resolved, true
}

// unique drops repeated paths while keeping the order, which matters because
// the rules are written in order and a later rule wins.
func unique(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := paths[:0]
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}

// workspacePaths are the names refused at the workspace root, whether or not
// they exist yet. Refusing a path before it exists is what stops a command from
// creating one: a .vscode directory that does not exist is still a directory a
// later task would be read from.
//
// Each of these is a write that becomes a program another process runs: a hook
// runs on the next commit, .mcp.json decides which servers an editor starts,
// .vscode and .idea carry tasks the editor runs, .claude holds commands and
// subagents, and an rc file or .envrc is sourced by the next shell that reads it.
var workspacePaths = []string{
	".mcp.json",
	".vscode",
	".idea",
	".claude/commands",
	".claude/agents",
	".envrc",
	".bashrc",
	".bash_profile",
	".bash_login",
	".profile",
	".zshrc",
	".zshenv",
	".zprofile",
	".zlogin",
}

// skippedDirs are never walked into.
//
// .git holds thousands of object files and nothing the scan wants; .vscode and
// .idea are refused as a whole, so rules for their contents would only repeat the
// parent rule; node_modules is the one that makes the difference between a scan
// that costs a millisecond and one that costs a second on a large project, and a
// repository inside an installed dependency is not the user's to operate.
var skippedDirs = map[string]bool{
	".git":         true,
	".vscode":      true,
	".idea":        true,
	"node_modules": true,
}

// scan collects the guarded paths under each writable root.
//
// The list is computed per command rather than once at startup because the
// workspace changes: a `git init` inside it creates a .git directory, and a list
// captured before that would miss exactly the directory an escape needs.
func scan(roots []string, depth int) []string {
	var out []string
	for _, root := range roots {
		out = append(out, scanRoot(root, depth)...)
	}
	return out
}

// scanRoot walks the directories under root and collects what to refuse.
//
// Repositories are guarded wherever they are, because a nested repository has its
// own hooks directory and the workspace's own .git says nothing about it. The
// workspace-level names are guarded at the root only: a .vscode directory four
// levels down is a directory inside a project, not the editor's configuration.
func scanRoot(root string, depth int) []string {
	var out []string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			// A directory that cannot be read is not a reason to fail the
			// command: the profile is about what may be written, and a path the
			// walk could not see is one it cannot guard anyway.
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		if path != root && skippedDirs[entry.Name()] {
			return filepath.SkipDir
		}
		level := 0
		if path != root {
			level = strings.Count(strings.TrimPrefix(path, root), string(filepath.Separator))
		}
		if level > depth {
			return filepath.SkipDir
		}
		if path == root {
			for _, name := range workspacePaths {
				out = append(out, filepath.Join(path, name))
			}
		}
		out = append(out, repositoryPaths(path)...)
		if level == depth {
			return filepath.SkipDir
		}
		return nil
	})
	return out
}

// repositoryPaths lists what a repository at dir must not have written.
//
// A hooks directory is refused whether or not it exists, because creating it is
// the step before writing a hook, and git only creates it when a template
// directory supplies one — which the wrapper makes sure it does not. The config
// is refused only once it exists, because git cannot initialise a repository
// without writing it: an unconditional refusal would forbid `git init` and
// `git clone` outright. The cost is that a command which creates a repository and
// rewrites its config in the same breath is not covered, and every command after
// it is.
func repositoryPaths(dir string) []string {
	git := filepath.Join(dir, ".git")
	if info, err := os.Lstat(git); err != nil || !info.IsDir() {
		return nil
	}
	out := []string{filepath.Join(git, "hooks")}
	if _, err := os.Lstat(filepath.Join(git, "config")); err == nil {
		out = append(out, filepath.Join(git, "config"))
	}
	return out
}

// render turns the state into a Seatbelt profile.
//
// Seatbelt is a whitelist: (deny default) refuses everything and each rule adds
// one capability back. Later rules win over earlier ones, which is what makes
// "allow the workspace, then take .git/hooks back out of it" work at all — the
// order of these writes is the meaning of the profile, not a formatting choice.
func (s state) render() string {
	var b strings.Builder
	b.WriteString("(version 1)\n")
	fmt.Fprintf(&b, "(deny default (with message %q))\n", logTag)
	b.WriteString(processRules)
	if len(s.denyRead) > 0 {
		for _, path := range s.denyRead {
			fmt.Fprintf(&b, "(deny file-read* (subpath %q))\n", path)
		}
		for _, root := range s.writable {
			for _, denied := range s.denyRead {
				if within(root, denied) {
					fmt.Fprintf(&b, "(allow file-read* (subpath %q))\n", root)
					break
				}
			}
		}
		b.WriteString("(allow file-read-metadata (vnode-type DIRECTORY))\n")
	}
	for _, root := range s.writable {
		fmt.Fprintf(&b, "(allow file-write* (subpath %q))\n", root)
	}
	b.WriteString(deviceRules)
	for _, path := range s.deny {
		fmt.Fprintf(&b, "(deny file-write* (subpath %q))\n", path)
	}
	if s.network {
		b.WriteString("\n(allow network*)\n")
	} else {
		b.WriteString("\n(deny network*)\n")
	}
	return b.String()
}

// bwrap turns the state into a bubblewrap command line for Linux.
//
// Mount order is the whole policy, for the same reason rule order is in
// Seatbelt: --ro-bind / / makes the root read-only first, --bind opens the
// writable roots on top of it, --tmpfs or --ro-bind /dev/null hides the
// read-denied paths, and a final --ro-bind on each guarded path inside a
// writable root locks .git/hooks and its siblings back down.
func (s state) bwrap(executor string, argv []string) []string {
	out := []string{
		executor,
		"--new-session",
		"--die-with-parent",
		"--unshare-user",
		"--cap-drop", "ALL",
		"--proc", "/proc",
		"--dev", "/dev",
		"--unshare-pid",
	}
	if !s.network {
		out = append(out, "--unshare-net")
	}
	out = append(out, "--ro-bind", "/", "/")
	for _, root := range s.writable {
		out = append(out, "--bind", root, root)
	}
	for _, path := range s.denyRead {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.IsDir() {
			out = append(out, "--tmpfs", path)
			for _, root := range s.writable {
				if within(root, path) {
					out = append(out, "--bind", root, root)
				}
			}
		} else {
			out = append(out, "--ro-bind", "/dev/null", path)
		}
	}
	for _, path := range s.deny {
		if _, err := os.Lstat(path); err != nil {
			continue
		}
		out = append(out, "--ro-bind", path, path)
	}
	out = append(out, "--")
	out = append(out, argv...)
	return out
}

// processRules is the part of the profile that is the same for every policy:
// what an ordinary shell needs in order to start at all, and the read decision.
//
// The set follows the Chrome sandbox policy that Seatbelt's own documentation
// points at. Process rules are scoped to this sandbox, so a confined command can
// inspect and signal its own tree and nothing else. Mach services are named one
// by one, because a wildcard there would hand the confined process a way to talk
// to launchd and every other system service, which is the boundary itself. Reads
// are not isolated in this version: a read boundary needs the list of files that
// must stay unreadable — credentials, other projects — and that list does not
// exist yet, so claiming one would be worse than saying so.
const processRules = `
(allow process-exec)
(allow process-fork)
(allow process-info* (target same-sandbox))
(allow signal (target same-sandbox))
(allow mach-priv-task-port (target same-sandbox))
(allow user-preference-read)
(allow sysctl-read)
(allow mach-lookup
    (global-name "com.apple.system.logger")
    (global-name "com.apple.system.notification_center")
    (global-name "com.apple.system.opendirectoryd.libinfo")
    (global-name "com.apple.system.opendirectoryd.membership")
    (global-name "com.apple.cfprefsd.agent")
    (global-name "com.apple.cfprefsd.daemon")
    (global-name "com.apple.SecurityServer")
    (global-name "com.apple.trustd.agent")
    (global-name "com.apple.dnssd.service"))
(allow file-read*)
`

// deviceRules opens the device files a shell and git write to unconditionally.
// Without them `git status` fails with "could not open '/dev/null'" and the tool
// looks broken rather than confined.
const deviceRules = `(allow file-write* (literal "/dev/null") (literal "/dev/tty")
                   (literal "/dev/stdout") (literal "/dev/stderr")
                   (literal "/dev/zero") (literal "/dev/random")
                   (literal "/dev/urandom"))
`
