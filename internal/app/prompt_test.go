package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"pi-go/internal/provider/faux"
	"pi-go/internal/wire"
)

// The instructions are read for every turn. The files behind them belong to
// the user, who can rewrite them while the session runs, and a session that
// answered with the copy it read at startup would be acting on rules that no
// longer exist.
func TestThePromptIsReadAgainForEveryTurn(t *testing.T) {
	prov := faux.New(faux.Response{Text: "first"}, faux.Response{Text: "second"})
	prompt := "first prompt"
	application := newTestApp(t, newSession(t), prov, func(cfg *Config) {
		cfg.SystemPrompt = func(string) (string, error) { return prompt, nil }
	})

	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	prompt = "second prompt"
	if _, err := application.Ask(context.Background(), wire.UserMessage("again"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}

	requests := prov.Calls()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	if got := requests[0].Messages[0].Content.Text(); got != "first prompt" {
		t.Fatalf("first request = %q, want the prompt of the first turn", got)
	}
	if got := requests[1].Messages[0].Content.Text(); got != "second prompt" {
		t.Fatalf("second request = %q, want the prompt of the second turn", got)
	}
}

// A file that cannot be read stops the turn. The model would otherwise answer
// without the instructions the reader believes it was given, and the mistake
// would surface as a wrong answer instead of a path.
func TestASourceThatFailsStopsTheTurn(t *testing.T) {
	prov := faux.New(faux.Response{Text: "never sent"})
	reads := 0
	application := newTestApp(t, newSession(t), prov, func(cfg *Config) {
		cfg.SystemPrompt = func(string) (string, error) {
			reads++
			if reads > 1 {
				return "", errors.New("context file: open AGENTS.md: permission denied")
			}
			return "the prompt", nil
		}
	})

	_, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil)
	if err == nil {
		t.Fatal("want an error when the instructions cannot be read")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v, want the reason the file could not be read", err)
	}
	if requests := prov.Calls(); len(requests) != 0 {
		t.Fatalf("requests = %d, want none: nothing is sent without the instructions", len(requests))
	}
}

// The same failure stops the session before it starts, when the file is
// already broken at startup.
func TestASourceThatFailsStopsTheSession(t *testing.T) {
	_, err := New(Config{
		Model:        testModel(),
		Provider:     faux.New(),
		SystemPrompt: func(string) (string, error) { return "", errors.New("context file: permission denied") },
	}, newSession(t))
	if err == nil {
		t.Fatal("want an error when the instructions cannot be read")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v, want the reason the file could not be read", err)
	}
}

// The reload is the explicit half of the per-turn read: it says whether the
// instructions changed, and it is the command a reader runs to find out.
func TestReloadSystemPromptSaysWhatChanged(t *testing.T) {
	prompt := "the prompt"
	application := newTestApp(t, newSession(t), faux.New(), func(cfg *Config) {
		cfg.SystemPrompt = func(string) (string, error) { return prompt, nil }
	})

	result, err := application.ReloadSystemPrompt()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !result.Live {
		t.Fatal("live = false, want the instructions to come from a source")
	}
	if result.Changed {
		t.Fatal("changed = true, want the same instructions")
	}
	if result.Prompt != "the prompt" {
		t.Fatalf("prompt = %q, want the instructions in effect", result.Prompt)
	}

	prompt = "another prompt"
	result, err = application.ReloadSystemPrompt()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !result.Changed {
		t.Fatal("changed = false, want the new instructions")
	}
	if got := application.SystemPrompt(); got != "another prompt" {
		t.Fatalf("prompt = %q, want the new ones", got)
	}
}

// A session that was started with a prompt recorded in its file keeps that
// prompt: the same rule the tools follow, and the reason the source is not
// asked again.
func TestASessionThatCarriesItsOwnPromptKeepsIt(t *testing.T) {
	manager := newSession(t)
	mustAppend(t, manager, wire.SystemMessage("prompt from the session"))
	reads := 0
	application := newTestApp(t, manager, faux.New(), func(cfg *Config) {
		cfg.SystemPrompt = func(string) (string, error) {
			reads++
			return "prompt from the source", nil
		}
	})

	if got := application.SystemPrompt(); got != "prompt from the session" {
		t.Fatalf("prompt = %q, want the one the session was started with", got)
	}
	before := reads
	result, err := application.ReloadSystemPrompt()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if result.Live {
		t.Fatal("live = true, want a session that carries its own instructions")
	}
	if reads != before {
		t.Fatalf("reads = %d, want the source left alone", reads-before)
	}
}

// The transcript survives a change of instructions: the rules change, the
// conversation does not.
func TestTheTranscriptSurvivesAChangedPrompt(t *testing.T) {
	prov := faux.New(faux.Response{Text: "first"}, faux.Response{Text: "second"})
	prompt := "first prompt"
	application := newTestApp(t, newSession(t), prov, func(cfg *Config) {
		cfg.SystemPrompt = func(string) (string, error) { return prompt, nil }
	})

	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	prompt = "second prompt"
	if _, err := application.Ask(context.Background(), wire.UserMessage("again"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}

	if got := application.MessageCount(); got != 4 {
		t.Fatalf("messages = %d, want the two turns and the two answers", got)
	}
	requests := prov.Calls()
	if len(requests[1].Messages) != 4 {
		t.Fatalf("second request has %d messages, want the prompt, the first turn and the second question", len(requests[1].Messages))
	}
}
