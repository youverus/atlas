package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/provider/faux"
	"pi-go/internal/session"
	"pi-go/internal/tool"
)

// bucketFor is the policy a test uses: one store per directory, named after it,
// so a conversation started in another project visibly lands somewhere else.
func bucketFor(root string) SessionDirFunc {
	return func(cwd string) (string, error) {
		return filepath.Join(root, filepath.Base(cwd)), nil
	}
}

func TestNewSessionInMovesTheConversation(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	root := t.TempDir()
	manager, err := session.Create(first, filepath.Join(root, filepath.Base(first)), session.Options{})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	application := newTestApp(t, manager, faux.New(), func(cfg *Config) {
		cfg.SessionDir = bucketFor(root)
	})

	if err := application.NewSessionIn(second); err != nil {
		t.Fatalf("new session in %s: %v", second, err)
	}
	if got := application.Session().Cwd(); got != second {
		t.Fatalf("cwd = %q, want %q", got, second)
	}
	// The manager reserves a name in the new project's bucket, and the file
	// appears with the first entry: a conversation nobody talked to must not
	// litter the project.
	file := application.Session().File()
	if want := filepath.Join(root, filepath.Base(second)); !strings.HasPrefix(file, want+string(filepath.Separator)) {
		t.Fatalf("file = %q, want it under %q", file, want)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("stat %q = %v, want no file before the first message", file, err)
	}
	if messages := application.Messages(); len(messages) != 0 {
		t.Fatalf("messages = %d, want a fresh conversation", len(messages))
	}
}

// The tools and the instructions belong to the directory the conversation runs
// in, which is the whole point of starting one in a project.
func TestNewSessionInAsksForThatDirectorysToolsAndPrompt(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	root := t.TempDir()
	manager, err := session.Create(first, filepath.Join(root, filepath.Base(first)), session.Options{})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	asked := ""
	toolDirs := []string{}
	application := newTestApp(t, manager, faux.New(), func(cfg *Config) {
		cfg.SessionDir = bucketFor(root)
		cfg.Tools = func(cwd string) []tool.Tool {
			toolDirs = append(toolDirs, cwd)
			return nil
		}
		cfg.SystemPrompt = func(cwd string) (string, error) {
			asked = cwd
			return "instructions for " + filepath.Base(cwd), nil
		}
	})

	if err := application.NewSessionIn(second); err != nil {
		t.Fatalf("new session in %s: %v", second, err)
	}
	if asked != second {
		t.Fatalf("prompt asked for %q, want %q", asked, second)
	}
	if want := "instructions for " + filepath.Base(second); application.SystemPrompt() != want {
		t.Fatalf("prompt = %q, want %q", application.SystemPrompt(), want)
	}
	// The tool set is asked for by directory too: a set built for the folder
	// the conversation left would run that folder's tools here.
	if len(toolDirs) == 0 || toolDirs[len(toolDirs)-1] != second {
		t.Fatalf("tools asked for %v, want the last one to be %q", toolDirs, second)
	}
}

// A directory that is gone cannot host a tool call, so the move is refused
// before anything is swapped.
func TestNewSessionInRefusesWhatCannotHostIt(t *testing.T) {
	root := t.TempDir()
	manager, err := session.Create(t.TempDir(), filepath.Join(root, "bucket"), session.Options{})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	application := newTestApp(t, manager, faux.New(), func(cfg *Config) {
		cfg.SessionDir = bucketFor(root)
	})
	before := application.Session().Cwd()

	cases := map[string]string{
		"empty":   "",
		"missing": filepath.Join(root, "gone"),
		"file":    filepath.Join(root, "not-a-dir"),
	}
	file := filepath.Join(root, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	for name, path := range cases {
		if err := application.NewSessionIn(path); err == nil {
			t.Fatalf("%s: starting a conversation in %q succeeded", name, path)
		}
		if got := application.Session().Cwd(); got != before {
			t.Fatalf("%s: cwd moved to %q after a refusal", name, got)
		}
	}
}

// Without the policy there is no honest answer: the default layout and a store
// configured by path disagree, and a guess would list one project's
// conversations under another.
func TestSessionDirNeedsThePolicy(t *testing.T) {
	manager, err := session.Create(t.TempDir(), t.TempDir(), session.Options{})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	application := newTestApp(t, manager, faux.New())
	if _, err := application.SessionDir(t.TempDir()); err == nil {
		t.Fatal("SessionDir answered without a policy")
	}
}
