package artifact

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestValidateIDAndKind(t *testing.T) {
	valid := []string{"a", "chart-1", "sales_dashboard_v2", "0-test"}
	for _, id := range valid {
		if err := ValidateID(id); err != nil {
			t.Fatalf("ValidateID(%q) unexpected error: %v", id, err)
		}
	}
	invalid := []string{"", "../escape", "UpperCase", "has space", "-leading-dash", "_leading", "a/b", strings.Repeat("a", MaxIDLength+1)}
	for _, id := range invalid {
		if err := ValidateID(id); err == nil {
			t.Fatalf("ValidateID(%q) expected error", id)
		}
	}
	for _, kind := range []string{KindHTML, KindSVG, KindMarkdown, KindCode, KindCSV} {
		if !ValidKind(kind) {
			t.Fatalf("ValidKind(%q) = false, want true", kind)
		}
	}
	if ValidKind("pdf") || ValidKind("") {
		t.Fatalf("expected unsupported kinds to return false")
	}
}

func TestStorePublishPatchGetDelete(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(t *testing.T) *Store
	}{
		{"disk", func(t *testing.T) *Store { return OpenDir(t.TempDir()) }},
		{"memory", func(t *testing.T) *Store { return NewMemory() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.build(t)

			list, err := store.List()
			if err != nil {
				t.Fatalf("List empty: %v", err)
			}
			if len(list) != 0 {
				t.Fatalf("len(List) = %d, want 0", len(list))
			}

			v1, err := store.Publish(PublishRequest{
				ID:          "sales-chart",
				Kind:        KindHTML,
				Title:       "Sales Chart",
				Description: "Interactive revenue chart",
				Content:     "<div><h1>Sales</h1><p>Q1: 100</p></div>",
				ToolCallID:  "call-1",
			})
			if err != nil {
				t.Fatalf("Publish v1: %v", err)
			}
			if v1.Version != 1 || v1.ActiveVersion != 1 || v1.Title != "Sales Chart" {
				t.Fatalf("unexpected v1 metadata: %+v", v1)
			}

			v2, err := store.Patch(PatchRequest{
				ID:         "sales-chart",
				Summary:    "Update Q1 metric",
				ToolCallID: "call-2",
				Edits: []Edit{
					{OldText: "Q1: 100", NewText: "Q1: 250"},
				},
			})
			if err != nil {
				t.Fatalf("Patch v2: %v", err)
			}
			if v2.Version != 2 || v2.ActiveVersion != 2 || !strings.Contains(v2.Content, "Q1: 250") {
				t.Fatalf("unexpected v2 result: %+v", v2)
			}
			if len(v2.Versions) != 2 || v2.Versions[0].Action != "publish" || v2.Versions[1].Action != "patch" {
				t.Fatalf("unexpected versions history: %+v", v2.Versions)
			}

			// Ensure v1 remained immutable.
			gotV1, err := store.Get("sales-chart", 1)
			if err != nil {
				t.Fatalf("Get v1: %v", err)
			}
			if !strings.Contains(gotV1.Content, "Q1: 100") || gotV1.ActiveVersion != 1 || gotV1.Version != 2 {
				t.Fatalf("v1 corrupted: %+v", gotV1)
			}

			// Patch errors: missing, ambiguous, empty old_text.
			if _, err := store.Patch(PatchRequest{ID: "sales-chart", Edits: []Edit{{OldText: "missing", NewText: "x"}}}); err == nil {
				t.Fatal("expected error for missing old_text")
			}

			if _, err := store.Publish(PublishRequest{
				ID:      "dup-test",
				Kind:    KindCode,
				Content: "alpha\nalpha\nbeta\n",
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Patch(PatchRequest{ID: "dup-test", Edits: []Edit{{OldText: "alpha", NewText: "gamma"}}}); err == nil {
				t.Fatal("expected error for ambiguous match without replace_all")
			}
			patchedDup, err := store.Patch(PatchRequest{ID: "dup-test", Edits: []Edit{{OldText: "alpha", NewText: "gamma", ReplaceAll: true}}})
			if err != nil || patchedDup.Content != "gamma\ngamma\nbeta\n" {
				t.Fatalf("replace_all failed: %v, %q", err, patchedDup.Content)
			}

			list, err = store.List()
			if err != nil || len(list) != 2 {
				t.Fatalf("List = %d, err = %v", len(list), err)
			}

			if err := store.Delete("dup-test"); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if _, err := store.Get("dup-test", 0); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Get deleted = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestStoreLimitsAndPersistence(t *testing.T) {
	dir := t.TempDir()
	store := OpenDir(dir)

	if _, err := store.Publish(PublishRequest{
		ID:      "too-big",
		Kind:    KindMarkdown,
		Content: strings.Repeat("a", MaxContentBytes+1),
	}); err == nil {
		t.Fatal("expected size limit error")
	}

	if _, err := store.Publish(PublishRequest{
		ID:      "bad-utf8",
		Kind:    KindCode,
		Content: string([]byte{0xff, 0xfe}),
	}); err == nil {
		t.Fatal("expected invalid UTF-8 error")
	}

	for i := 1; i <= MaxVersions; i++ {
		if _, err := store.Publish(PublishRequest{
			ID:      "versioned",
			Kind:    KindMarkdown,
			Content: fmt.Sprintf("# v%d", i),
		}); err != nil {
			t.Fatalf("Publish v%d: %v", i, err)
		}
	}
	if _, err := store.Publish(PublishRequest{
		ID:      "versioned",
		Kind:    KindMarkdown,
		Content: "overflow",
	}); err == nil {
		t.Fatal("expected version cap error")
	}

	reopened := OpenDir(dir)
	got, err := reopened.Get("versioned", 0)
	if err != nil {
		t.Fatalf("reopened.Get: %v", err)
	}
	if got.Version != MaxVersions || got.Content != fmt.Sprintf("# v%d", MaxVersions) {
		t.Fatalf("reopened content mismatch: %+v", got)
	}
}

func TestStoreConcurrentPublish(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			s := OpenDir(dir)
			_, err := s.Publish(PublishRequest{
				ID:      "concurrent",
				Kind:    KindCode,
				Content: fmt.Sprintf("item-%d", idx),
			})
			if err != nil {
				t.Errorf("concurrent Publish %d: %v", idx, err)
			}
		}(i)
	}
	wg.Wait()

	got, err := OpenDir(dir).Get("concurrent", 0)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Version != 8 || len(got.Versions) != 8 {
		t.Fatalf("got version %d (%d entries), want 8", got.Version, len(got.Versions))
	}
}

func TestRepoForSession(t *testing.T) {
	repo := NewRepo()
	memA := repo.ForSession("sess-a", "")
	memB := repo.ForSession("sess-b", "")
	if _, err := memA.Publish(PublishRequest{ID: "doc", Kind: KindMarkdown, Content: "A"}); err != nil {
		t.Fatal(err)
	}
	if list, err := memB.List(); err != nil || len(list) != 0 {
		t.Fatalf("expected isolated memory stores, got %d items (err=%v)", len(list), err)
	}

	sessPath := t.TempDir() + "/demo.jsonl"
	disk := repo.ForSession("sess-disk", sessPath)
	if _, err := disk.Publish(PublishRequest{ID: "doc", Kind: KindMarkdown, Content: "D"}); err != nil {
		t.Fatal(err)
	}
	if got, err := OpenDir(sessPath+".artifacts").Get("doc", 0); err != nil || got.Content != "D" {
		t.Fatalf("disk store mismatch: %+v, err=%v", got, err)
	}
}
