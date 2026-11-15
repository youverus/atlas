package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resolvedTemp is t.TempDir with symlinks resolved, which is what every
// assertion here compares against. On macOS the temporary directory is reached
// through /var, and the policy deliberately names the /private/var path because
// that is the path the kernel matches.
func resolvedTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve %s: %v", dir, err)
	}
	return resolved
}

// workspace builds a directory with a repository in it, which is the shape the
// scan exists for: the writable root is the workspace, and the paths it must
// refuse live inside it.
func workspace(t *testing.T) string {
	t.Helper()
	root := resolvedTemp(t)
	if err := os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[core]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestProfileAllowsRootThenDeniesInsideIt pins the one thing about Seatbelt that
// is easy to get wrong: rules are applied in order and the last match wins, so
// the deny has to come after the allow it carves a hole in.
func TestProfileAllowsRootThenDeniesInsideIt(t *testing.T) {
	root := workspace(t)
	state, err := resolve(Policy{Writable: []string{root}})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	profile := state.render()

	allow := `(allow file-write* (subpath "` + root + `"))`
	denyHooks := `(deny file-write* (subpath "` + filepath.Join(root, ".git", "hooks") + `"))`
	allowAt := strings.Index(profile, allow)
	denyAt := strings.Index(profile, denyHooks)
	if allowAt < 0 {
		t.Fatalf("profile does not allow the writable root:\n%s", profile)
	}
	if denyAt < 0 {
		t.Fatalf("profile does not deny .git/hooks:\n%s", profile)
	}
	if denyAt < allowAt {
		t.Fatalf("deny comes before the allow it carves out, so it has no effect:\n%s", profile)
	}
}

// TestResolveFollowsSymlinks is the bug this package was written around. Seatbelt
// matches the resolved path, so a writable root named through a symlink produces
// a rule that never matches and a sandbox where nothing can be written.
func TestResolveFollowsSymlinks(t *testing.T) {
	real := workspace(t)
	link := filepath.Join(resolvedTemp(t), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	state, err := resolve(Policy{Writable: []string{link}})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(state.writable) != 1 {
		t.Fatalf("got %d writable roots, want 1", len(state.writable))
	}
	if state.writable[0] != real {
		t.Fatalf("writable root = %q, want the resolved path %q", state.writable[0], real)
	}
}

// TestResolveRefusesNothingWritable covers the policy that would otherwise
// produce a profile where every command fails for a reason nothing explains.
func TestResolveRefusesNothingWritable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	if _, err := resolve(Policy{Writable: []string{missing}}); err == nil {
		t.Fatal("resolve accepted a policy with no existing writable root")
	}
}

// TestScanFindsNestedRepositories is why the list is computed rather than
// written down: a repository inside the workspace has its own hooks directory,
// and a static list of the workspace's own .git would miss it.
func TestScanFindsNestedRepositories(t *testing.T) {
	root := workspace(t)
	nested := filepath.Join(root, "sub")
	if err := os.MkdirAll(filepath.Join(nested, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	state, err := resolve(Policy{Writable: []string{root}})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !contains(state.deny, filepath.Join(nested, ".git", "hooks")) {
		t.Fatalf("nested hooks directory is not guarded; deny = %v", state.deny)
	}
}

// TestScanStopsAtDepth keeps the per-command cost bounded. The limit is a
// trade — a repository deeper than this is not guarded — and a test that pins it
// is what stops the limit from drifting silently.
func TestScanStopsAtDepth(t *testing.T) {
	root := resolvedTemp(t)
	deep := filepath.Join(root, "a", "b", "c", "d")
	if err := os.MkdirAll(filepath.Join(deep, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	state, err := resolve(Policy{Writable: []string{root}, Depth: 2})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if contains(state.deny, filepath.Join(deep, ".git", "hooks")) {
		t.Fatalf("scan walked past its depth limit; deny = %v", state.deny)
	}
}

// TestScanGuardsHooksBeforeTheyExist pins the asymmetry in guardedPaths: a hooks
// directory is refused before it exists, because creating it is the step before
// writing a hook, while .git/config is refused only once it exists, because git
// has to create it.
func TestScanGuardsHooksBeforeTheyExist(t *testing.T) {
	root := resolvedTemp(t)
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	state, err := resolve(Policy{Writable: []string{root}})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !contains(state.deny, filepath.Join(root, ".git", "hooks")) {
		t.Fatalf("a hooks directory that does not exist yet is not guarded; deny = %v", state.deny)
	}
	if contains(state.deny, filepath.Join(root, ".git", "config")) {
		t.Fatalf("a config that does not exist yet is guarded, which forbids git init; deny = %v", state.deny)
	}
}

// TestEnvironmentDropsGitAndAddsFixed covers the two halves of the environment:
// nothing the parent set may redirect git, and the fixed set that makes the
// profile's promises hold is present.
func TestEnvironmentDropsGitAndAddsFixed(t *testing.T) {
	confine := New(Policy{Writable: []string{resolvedTemp(t)}})
	env := confine.environment([]string{
		"PATH=/bin",
		"GIT_DIR=/elsewhere/.git",
		"GIT_CONFIG_GLOBAL=/elsewhere/config",
		"GIT_TEMPLATE_DIR=/elsewhere/hooks",
		"HOME=/home/someone",
	})

	for _, entry := range env {
		if strings.HasPrefix(entry, "GIT_DIR=") ||
			strings.HasPrefix(entry, "GIT_CONFIG_GLOBAL=") ||
			strings.HasPrefix(entry, "GIT_TEMPLATE_DIR=/elsewhere") {
			t.Fatalf("inherited variable survived: %s", entry)
		}
	}
	for _, want := range []string{
		"PATH=/bin",
		"HOME=/home/someone",
		Marker + "=1",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_PAGER=cat",
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
	} {
		if !contains(env, want) {
			t.Fatalf("missing %q; env = %v", want, env)
		}
	}
}

// TestWrapIsIdempotent covers the marker: a command started from inside a
// confined tree is already confined, and applying a second profile fails with
// "sandbox_apply: Operation not permitted".
func TestWrapIsIdempotent(t *testing.T) {
	confine := New(Policy{Writable: []string{resolvedTemp(t)}})
	argv := []string{"/bin/sh", "-c", "true"}
	env := []string{Marker + "=1"}

	wrapped, wrappedEnv, err := confine.Wrap(argv, env)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if strings.Join(wrapped, " ") != strings.Join(argv, " ") {
		t.Fatalf("argv was wrapped again: %v", wrapped)
	}
	if strings.Join(wrappedEnv, " ") != strings.Join(env, " ") {
		t.Fatalf("environment was rewritten again: %v", wrappedEnv)
	}
}

// TestWrapRefusesWithoutExecutor is the one failure a sandbox must not have: a
// wrap that cannot be applied has to stop the command rather than return it
// unwrapped.
func TestWrapRefusesWithoutExecutor(t *testing.T) {
	missing := &Sandbox{
		policy:   Policy{Writable: []string{resolvedTemp(t)}},
		executor: filepath.Join(resolvedTemp(t), "sandbox-exec"),
	}
	if _, _, err := missing.Wrap([]string{"/bin/sh", "-c", "true"}, nil); err == nil {
		t.Fatal("Wrap accepted a missing executor")
	} else if !strings.Contains(err.Error(), "remove the sandbox block") {
		t.Fatalf("error does not say what to do about it: %v", err)
	}

	unsupported := &Sandbox{
		policy: Policy{Writable: []string{resolvedTemp(t)}},
		reason: "no executor on plan9",
	}
	if _, _, err := unsupported.Wrap([]string{"/bin/sh", "-c", "true"}, nil); err == nil {
		t.Fatal("Wrap accepted a platform with no executor")
	}
}

// TestProfileDeniesReadAndKeepsDirectoryMetadata pins the read half of the
// Seatbelt profile: a denied directory comes after the broad (allow file-read*),
// directory metadata stays open so ls on the parent does not fail, and a
// writable root inside a denied ancestor is re-opened for reading after the
// deny.
func TestProfileDeniesReadAndKeepsDirectoryMetadata(t *testing.T) {
	parent := resolvedTemp(t)
	root := filepath.Join(parent, "workspace")
	secret := filepath.Join(parent, ".ssh")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	state, err := resolve(Policy{
		Writable: []string{root},
		DenyRead: []string{secret, parent},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	profile := state.render()

	allowAll := strings.Index(profile, "(allow file-read*)")
	denySecret := strings.Index(profile, `(deny file-read* (subpath "`+secret+`"))`)
	denyParent := strings.Index(profile, `(deny file-read* (subpath "`+parent+`"))`)
	reopenRoot := strings.Index(profile, `(allow file-read* (subpath "`+root+`"))`)
	metadata := strings.Index(profile, "(allow file-read-metadata (vnode-type DIRECTORY))")
	if allowAll < 0 || denySecret < 0 || denyParent < 0 || reopenRoot < 0 || metadata < 0 {
		t.Fatalf("missing read rules in profile:\n%s", profile)
	}
	if denySecret < allowAll || reopenRoot < denyParent {
		t.Fatalf("read rules are out of order:\n%s", profile)
	}
}

// TestBwrapOrdersMountsFromBroadToNarrow pins the Linux command line: a read-only
// root first, the writable workspace over it, tmpfs and /dev/null masks over
// denied reads, and a read-only bind over .git/hooks last.
func TestBwrapOrdersMountsFromBroadToNarrow(t *testing.T) {
	root := workspace(t)
	secretDir := filepath.Join(resolvedTemp(t), ".ssh")
	secretFile := filepath.Join(resolvedTemp(t), ".npmrc")
	if err := os.MkdirAll(secretDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretFile, []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	fakeBwrap := filepath.Join(resolvedTemp(t), "bwrap")
	if err := os.WriteFile(fakeBwrap, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	confine := &Sandbox{
		policy: Policy{
			Writable: []string{root},
			DenyRead: []string{secretDir, secretFile},
			Network:  false,
		},
		executor: fakeBwrap,
	}
	wrapped, _, err := confine.Wrap([]string{"/bin/sh", "-c", "true"}, nil)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	joined := strings.Join(wrapped, " ")
	for _, want := range []string{
		"--new-session --die-with-parent",
		"--unshare-user --cap-drop ALL --proc /proc --dev /dev --unshare-pid",
		"--unshare-net",
		"--ro-bind / /",
		"--bind " + root + " " + root,
		"--tmpfs " + secretDir,
		"--ro-bind /dev/null " + secretFile,
		"--ro-bind " + filepath.Join(root, ".git", "hooks") + " " + filepath.Join(root, ".git", "hooks"),
		"-- /bin/sh -c true",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("bwrap argv missing %q:\n%s", want, joined)
		}
	}
	if strings.Index(joined, "--ro-bind / /") > strings.Index(joined, "--bind "+root) {
		t.Fatalf("--ro-bind / / must precede --bind:\n%s", joined)
	}
	if strings.Index(joined, "--bind "+root) > strings.Index(joined, "--ro-bind "+filepath.Join(root, ".git", "hooks")) {
		t.Fatalf("--bind must precede the .git/hooks --ro-bind:\n%s", joined)
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
