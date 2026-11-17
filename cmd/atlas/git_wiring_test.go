package main

import (
	"path/filepath"
	"testing"

	"pi-go/internal/assembly"
	"pi-go/internal/config"
	"pi-go/internal/tool"
)

// The git tools are built per directory like the file tools, because /resume can
// move a session into another project and the repository a tool reads has to be
// the one the session is in. This asserts the wiring rather than the tools: a
// family that ignored its argument would leave them reading the startup
// directory forever, which is the same bug the tools-as-a-function change fixed.
func TestTheGitFamilyIsBuiltForTheDirectory(t *testing.T) {
	var asked []string
	family := func(cwd string) []tool.Tool {
		asked = append(asked, cwd)
		return []tool.Tool{namedTool{"git_status"}}
	}
	set := assembly.ToolSet(&config.Config{}, nil, nil, family, nil, assembly.TaskWiring{})

	dir := filepath.Join(t.TempDir(), "project")
	tools := set(dir)

	if len(asked) != 1 || asked[0] != dir {
		t.Fatalf("the git family was asked for %q, want [%q]", asked, dir)
	}
	var names []string
	for _, candidate := range tools {
		names = append(names, candidate.Name())
	}
	for _, name := range names {
		if name == "git_status" {
			return
		}
	}
	t.Fatalf("the tool set does not hold the git family: %v", names)
}
