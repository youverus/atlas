package fs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"pi-go/internal/tool"
)

func TestRootedTools(t *testing.T) {
	base := newCwd(t, map[string]string{
		"project/docs/.gitignore":     "ignored.txt\n",
		"project/docs/ignored.txt":    "needle",
		"project/docs/visible.txt":    "before\nneedle\nafter",
		"project/Capture d’écran.txt": "screenshot notes",
		"sibling/visible.txt":         "outside",
	})
	project := filepath.Join(base, "project")
	tools := openRootedTools(t, project, filepath.Join(base, "sibling"))
	if len(tools) != 4 {
		t.Fatalf("expected four rooted tools, got %d", len(tools))
	}
	for _, name := range []string{"read", "ls", "find", "grep"} {
		if tools[name] == nil {
			t.Fatalf("rooted tool %s is missing", name)
		}
	}

	for _, test := range []struct {
		name, args, want string
	}{
		{"read", `{"path":"Capture d'écran.txt"}`, "screenshot notes"},
		{"read", rootedPathArgs(filepath.Join(project, "docs", "visible.txt")), "before\nneedle\nafter"},
		{"ls", `{"path":"docs"}`, ".gitignore\nignored.txt\nvisible.txt"},
		{"find", `{"pattern":"*.txt","path":"docs"}`, "visible.txt"},
		{"grep", `{"pattern":"needle","path":"docs","context":1}`, "visible.txt-1- before\nvisible.txt:2: needle\nvisible.txt-3- after"},
	} {
		if got := text(t, run(t, tools[test.name], test.args)); got != test.want {
			t.Errorf("%s %s = %q, want %q", test.name, test.args, got, test.want)
		}
	}
}

func TestRootedToolsRejectOutsidePaths(t *testing.T) {
	base := newCwd(t, map[string]string{
		"project/local.txt":          "local",
		"project-sibling/secret.txt": "needle outside",
	})
	project := filepath.Join(base, "project")
	outside := filepath.Join(base, "project-sibling")
	tools := openRootedTools(t, project, "")
	t.Setenv("HOME", outside)
	t.Setenv("USERPROFILE", outside)

	for name, impl := range tools {
		t.Run(name, func(t *testing.T) {
			for _, path := range []string{"../project-sibling", outside, "~"} {
				if name == "read" {
					path = filepath.Join(path, "secret.txt")
				}
				runError(t, impl, rootedPathArgs(path))
			}
		})
	}
}

func TestRootedToolsContainSymlinksAndGitignore(t *testing.T) {
	base := newCwd(t, map[string]string{
		"project/sub/deep/visible.txt": "before\nneedle\nafter",
		"project/visible.txt":          "needle local",
		"outside/.gitignore":           "visible.txt\n",
		"outside/secret.txt":           "needle outside",
	})
	project := filepath.Join(base, "project")
	outside := filepath.Join(base, "outside")
	testSymlink(t, filepath.Join("..", "outside"), filepath.Join(project, "outside-dir"))
	testSymlink(t, filepath.Join(outside, "secret.txt"), filepath.Join(project, "outside-file.txt"))
	testSymlink(t, filepath.Join("..", "outside", ".gitignore"), filepath.Join(project, ".gitignore"))
	testSymlink(t, filepath.Join(outside, ".gitignore"), filepath.Join(project, "sub", ".gitignore"))
	testSymlink(t, filepath.Join("sub", "deep"), filepath.Join(project, "local-dir"))
	tools := openRootedTools(t, project, "")

	for name, impl := range tools {
		path := "outside-dir"
		if name == "read" {
			path += "/secret.txt"
		}
		runError(t, impl, rootedPathArgs(path))
	}
	for _, name := range []string{"read", "grep"} {
		runError(t, tools[name], rootedPathArgs("outside-file.txt"))
	}
	for _, test := range []struct {
		name, args, want string
	}{
		{"read", `{"path":"local-dir/visible.txt"}`, "before\nneedle\nafter"},
		{"ls", `{}`, "local-dir/\nsub/\nvisible.txt"},
		{"find", `{"pattern":"*.txt"}`, "sub/deep/visible.txt\nvisible.txt"},
		{"grep", `{"pattern":"needle"}`, "sub/deep/visible.txt:2: needle\nvisible.txt:1: needle local"},
	} {
		if got := text(t, run(t, tools[test.name], test.args)); got != test.want {
			t.Errorf("%s %s = %q, want %q", test.name, test.args, got, test.want)
		}
	}

	if runtime.GOOS != "windows" {
		// On Unix these components must be resolved in order, even when an
		// input was absolute and needs to be translated relative to the root.
		for _, name := range []string{"ls", "find", "grep"} {
			runError(t, tools[name], rootedPathArgs(project+"/outside-dir/.."))
		}
		runError(t, tools["read"], rootedPathArgs(project+"/outside-dir/../visible.txt"))
		got := text(t, run(t, tools["grep"], `{"pattern":"needle","path":"local-dir/..","context":1}`))
		if want := "deep/visible.txt-1- before\ndeep/visible.txt:2: needle\ndeep/visible.txt-3- after"; got != want {
			t.Errorf("grep through an in-root symlink and .. = %q, want %q", got, want)
		}
	}
}

func TestRootedToolsUseOpenDirectory(t *testing.T) {
	base := newCwd(t, map[string]string{
		"project/kept.txt":       "needle in project",
		"replacement/secret.txt": "needle outside",
	})
	project := filepath.Join(base, "project")
	tools := openRootedTools(t, project, "")
	if err := os.Rename(project, filepath.Join(base, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(base, "replacement"), project); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name, args, want string
	}{
		{"read", rootedPathArgs(filepath.Join(project, "kept.txt")), "needle in project"},
		{"ls", `{}`, "kept.txt"},
		{"find", `{"pattern":"*.txt"}`, "kept.txt"},
		{"grep", `{"pattern":"needle"}`, "kept.txt:1: needle in project"},
	} {
		if got := text(t, run(t, tools[test.name], test.args)); got != test.want {
			t.Errorf("%s %s = %q, want %q", test.name, test.args, got, test.want)
		}
	}
}

func openRootedTools(t *testing.T, path, cwd string) map[string]tool.Tool {
	t.Helper()
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	tools := make(map[string]tool.Tool)
	for _, impl := range Tools(Options{Cwd: cwd, Root: root}) {
		tools[impl.Name()] = impl
	}
	return tools
}

func rootedPathArgs(path string) string {
	encoded, _ := json.Marshal(path)
	return `{"pattern":"needle","path":` + string(encoded) + `}`
}

func testSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" && os.IsPermission(err) {
			t.Skipf("cannot create symlinks: %v", err)
		}
		t.Fatal(err)
	}
}
