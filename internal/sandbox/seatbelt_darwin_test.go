//go:build darwin

package sandbox

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// confineFor builds a sandbox around one writable directory, or skips the test
// when this machine cannot apply a profile at all.
func confineFor(t *testing.T, writable string, network bool) *Sandbox {
	t.Helper()
	confine := New(Policy{Writable: []string{writable}, Network: network})
	if err := confine.Available(); err != nil {
		t.Skipf("no executor here: %v", err)
	}
	if value, ok := lookupEnv(os.Environ(), Marker); ok && value == "1" {
		// A nested profile cannot be applied, and this test process is already
		// inside one.
		t.Skip("already running inside a sandbox")
	}
	return confine
}

// run executes a script through the sandbox and returns its combined output and
// exit code.
func run(t *testing.T, confine *Sandbox, dir, script string) (string, int) {
	t.Helper()
	argv, env, err := confine.Wrap([]string{"/bin/sh", "-c", script}, os.Environ())
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	command := exec.Command(argv[0], argv[1:]...)
	command.Dir = dir
	command.Env = env
	output, err := command.CombinedOutput()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("run %q: %v", script, err)
	}
	return string(output), code
}

// TestSeatbeltWriteInsideAndOutside is the boundary itself: the writable root is
// writable, and a sibling of it is not, even though the two differ by one path
// element and both belong to the same user.
func TestSeatbeltWriteInsideAndOutside(t *testing.T) {
	parent := t.TempDir()
	inside := filepath.Join(parent, "workspace")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	confine := confineFor(t, inside, true)

	output, code := run(t, confine, inside, "echo hello > inside.txt && cat inside.txt")
	if code != 0 {
		t.Fatalf("write inside the root failed with %d: %s", code, output)
	}

	outside := filepath.Join(parent, "outside.txt")
	output, code = run(t, confine, inside, "echo hello > "+outside)
	if code == 0 {
		t.Fatalf("write outside the root succeeded: %s", output)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("the file exists, so the write was not refused by the kernel")
	}
}

// TestSeatbeltRefusesGitHooks is the criterion that says the boundary is in the
// kernel rather than in our argv handling: the hooks directory sits inside a
// writable root and is refused anyway.
func TestSeatbeltRefusesGitHooks(t *testing.T) {
	root := workspace(t)
	confine := confineFor(t, root, true)

	output, code := run(t, confine, root, "echo evil > .git/hooks/pre-commit")
	if code == 0 {
		t.Fatalf("writing a hook succeeded: %s", output)
	}
	if !strings.Contains(output, "not permitted") {
		t.Fatalf("refusal does not read as a kernel refusal: %s", output)
	}
}

// TestSeatbeltRefusesGitConfig covers the other half of the same idea: a config
// write is how a hook path or a pager becomes a program that runs outside the
// sandbox later.
func TestSeatbeltRefusesGitConfig(t *testing.T) {
	root := workspace(t)
	confine := confineFor(t, root, true)

	output, code := run(t, confine, root, "echo x >> .git/config")
	if code == 0 {
		t.Fatalf("writing .git/config succeeded: %s", output)
	}
}

// TestSeatbeltAllowsDevNull covers the device files: without them `git status`
// fails with "could not open '/dev/null'" and the whole tool looks broken.
func TestSeatbeltAllowsDevNull(t *testing.T) {
	root := workspace(t)
	confine := confineFor(t, root, true)

	output, code := run(t, confine, root, "echo x > /dev/null && echo ok")
	if code != 0 || !strings.Contains(output, "ok") {
		t.Fatalf("writing /dev/null failed with %d: %s", code, output)
	}
}

// TestSeatbeltKeepsGitWorking is the counterweight to the two refusals above.
// Denying .git/config and .git/hooks is only defensible because an ordinary
// repository still works: init, add, commit and status all have to succeed, or
// the sandbox has made the tool useless rather than safe.
func TestSeatbeltKeepsGitWorking(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	confine := confineFor(t, root, true)

	script := "git init -q . && echo a > a.txt && " +
		"git add -A && " +
		git + " -c user.email=a@b -c user.name=a -c commit.gpgsign=false commit -qm x && " +
		"git status --short && echo committed"
	output, code := run(t, confine, root, script)
	if code != 0 {
		t.Fatalf("an ordinary git workflow failed with %d: %s", code, output)
	}
	if !strings.Contains(output, "committed") {
		t.Fatalf("commit did not run: %s", output)
	}
}

// TestSeatbeltDeniesReadAndListsParent verifies the read boundary in the kernel:
// a file inside DenyRead cannot be read, while listing the parent directory
// still succeeds because directory metadata stays open.
func TestSeatbeltDeniesReadAndListsParent(t *testing.T) {
	parent := t.TempDir()
	inside := filepath.Join(parent, "workspace")
	secret := filepath.Join(parent, ".ssh")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(secret, 0o700); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(secret, "id_ed25519")
	if err := os.WriteFile(keyFile, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}

	confine := New(Policy{
		Writable: []string{inside},
		DenyRead: []string{secret},
		Network:  true,
	})
	if err := confine.Available(); err != nil {
		t.Skipf("no executor here: %v", err)
	}
	if value, ok := lookupEnv(os.Environ(), Marker); ok && value == "1" {
		t.Skip("already running inside a sandbox")
	}

	output, code := run(t, confine, inside, "cat "+keyFile)
	if code == 0 || strings.Contains(output, "PRIVATE KEY") {
		t.Fatalf("reading a denied file succeeded: %s", output)
	}
	if output, code := run(t, confine, inside, "ls "+parent); code != 0 {
		t.Fatalf("listing the parent directory failed with %d: %s", code, output)
	}
}
