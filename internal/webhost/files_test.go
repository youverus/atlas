package webhost

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"pi-go/internal/assembly"
	"pi-go/internal/wire"
)

func TestReadDirAndReadFileFollowActiveSessionDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ATLAS_HOME", home)
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{
		"defaultModel": "p1/m1",
		"providers": {
			"p1": {
				"api": "openai-completions",
				"apiKey": "k1",
				"models": [{"id": "m1", "name": "One", "contextWindow": 8000}]
			}
		}
	}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	dirA := t.TempDir()
	dirB := t.TempDir()
	if err := os.WriteFile(filepath.Join(dirA, "alpha.txt"), []byte("from A"), 0o644); err != nil {
		t.Fatalf("write alpha.txt: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dirB, "subB"), 0o755); err != nil {
		t.Fatalf("mkdir subB: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "beta.txt"), []byte("from B"), 0o644); err != nil {
		t.Fatalf("write beta.txt: %v", err)
	}

	kernel, _, err := assembly.New(context.Background(), assembly.Options{
		ConfigPath: cfgPath,
		Workdir:    dirA,
		NoMCP:      true,
	})
	if err != nil {
		t.Fatalf("assembly.New: %v", err)
	}
	defer kernel.Close()

	host := NewWith(kernel, Options{Projects: filepath.Join(t.TempDir(), "projects.json")})
	sessionA := host.CurrentSession()
	if _, err := kernel.App.Session().AppendMessage(wire.UserMessage("hello")); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	listingA, err := host.ReadDir("")
	if err != nil {
		t.Fatalf("ReadDir(A): %v", err)
	}
	absA, _ := filepath.Abs(dirA)
	if listingA.Root != absA {
		t.Fatalf("listingA.Root = %q, want %q", listingA.Root, absA)
	}
	if len(listingA.Entries) != 1 || listingA.Entries[0].Name != "alpha.txt" {
		t.Fatalf("listingA.Entries = %+v, want [alpha.txt]", listingA.Entries)
	}

	if err := host.AddNamedProject(dirB, "Project Beta"); err != nil {
		t.Fatalf("AddNamedProject(B): %v", err)
	}
	if err := host.NewSessionIn(dirB); err != nil {
		t.Fatalf("NewSessionIn(B): %v", err)
	}

	listingB, err := host.ReadDir("")
	if err != nil {
		t.Fatalf("ReadDir(B): %v", err)
	}
	absB, _ := filepath.Abs(dirB)
	if listingB.Root != absB {
		t.Fatalf("listingB.Root = %q, want %q", listingB.Root, absB)
	}
	if len(listingB.Entries) != 2 || listingB.Entries[0].Name != "subB" || listingB.Entries[1].Name != "beta.txt" {
		t.Fatalf("listingB.Entries = %+v, want [subB, beta.txt]", listingB.Entries)
	}
	fileB, err := host.ReadFile("beta.txt")
	if err != nil || fileB.Text != "from B" {
		t.Fatalf("ReadFile(beta.txt) = %+v, %v", fileB, err)
	}
	if _, err := host.ReadFile("alpha.txt"); err == nil {
		t.Fatal("ReadFile(alpha.txt) succeeded under dirB, want error")
	}

	projects, err := host.Projects(false)
	if err != nil {
		t.Fatalf("Projects(false): %v", err)
	}
	if len(projects.Groups) != 2 || !sameDir(projects.Groups[0].Path, dirA) || !projects.Groups[0].Default || projects.Groups[1].Default {
		t.Fatalf("Projects while viewing dirB = %+v, want dirA preserved as sole Default", projects.Groups)
	}
	if projects.Groups[1].Name != "Project Beta" {
		t.Fatalf("Projects[1].Name = %q, want %q", projects.Groups[1].Name, "Project Beta")
	}

	if err := host.OpenSession(sessionA); err != nil {
		t.Fatalf("OpenSession(A): %v", err)
	}
	listingBack, err := host.ReadDir("")
	if err != nil {
		t.Fatalf("ReadDir(back to A): %v", err)
	}
	if listingBack.Root != absA || len(listingBack.Entries) != 1 || listingBack.Entries[0].Name != "alpha.txt" {
		t.Fatalf("listingBack = %+v, want alpha.txt under %q", listingBack, absA)
	}

	browse, err := host.BrowseDirs("", dirB, false)
	if err != nil {
		t.Fatalf("BrowseDirs(dirB): %v", err)
	}
	if browse.Path != absB || len(browse.Entries) != 1 || browse.Entries[0].Name != "subB" {
		t.Fatalf("BrowseDirs = %+v, want [subB] under %q", browse, absB)
	}
	if _, err := host.BrowseDirs("gpu-1", dirB, false); err == nil {
		t.Fatal("BrowseDirs on remote device should fail on local webhost")
	}

	discovered, err := host.DiscoverProjects("")
	if err != nil {
		t.Fatalf("DiscoverProjects: %v", err)
	}
	foundA, foundB := false, false
	for _, item := range discovered {
		if sameDir(item.Path, dirA) && item.SessionCount >= 1 {
			foundA = true
		}
		if sameDir(item.Path, dirB) && item.Added && item.Name == "Project Beta" {
			foundB = true
		}
	}
	if !foundA || !foundB {
		t.Fatalf("DiscoverProjects = %+v, want dirA(sessions>=1) and dirB(added Project Beta)", discovered)
	}
}
