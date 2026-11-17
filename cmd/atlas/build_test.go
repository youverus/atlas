package main

import (
	"bytes"
	"strings"
	"testing"

	"pi-go/internal/config"
	"pi-go/internal/provider/anthropic"
	"pi-go/internal/provider/openai"
)

func TestWriteModelList(t *testing.T) {
	cfg := &config.Config{
		DefaultModel: "a/one",
		Providers: map[string]config.Provider{
			"a": {API: openai.APIName, BaseURL: "http://a.example/v1", Models: []config.Model{{ID: "one"}}},
			"b": {API: anthropic.APIName, Models: []config.Model{{ID: "two", MaxTokens: 4096}}},
		},
	}
	var out bytes.Buffer
	if err := writeModelList(&out, cfg); err != nil {
		t.Fatalf("writeModelList: %v", err)
	}
	printed := out.String()
	if !strings.Contains(printed, "one (default)") {
		t.Fatalf("output = %q, want the default marked", printed)
	}
	if !strings.Contains(printed, "two") || !strings.Contains(printed, "(adapter default)") {
		t.Fatalf("output = %q", printed)
	}
	// A zero budget is "not declared", not "no budget".
	if !strings.Contains(printed, "-") {
		t.Fatalf("output = %q, want a dash for an undeclared count", printed)
	}
}

func TestParseFlags(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		expect func(t *testing.T, opts options)
		fails  string
	}{
		{
			name: "positional prompt",
			args: []string{"fix", "the", "build"},
			expect: func(t *testing.T, opts options) {
				if opts.prompt != "fix the build" {
					t.Fatalf("prompt = %q", opts.prompt)
				}
			},
		},
		{
			name: "flag prompt",
			args: []string{"-prompt", "hi", "-model", "a/m1", "-thinking", "high"},
			expect: func(t *testing.T, opts options) {
				if opts.prompt != "hi" || opts.model != "a/m1" || opts.thinking != "high" {
					t.Fatalf("options = %+v", opts)
				}
			},
		},
		{
			name:  "prompt twice",
			args:  []string{"-prompt", "hi", "there"},
			fails: "twice",
		},
		{
			name:  "session and continue",
			args:  []string{"-session", "/tmp/s.jsonl", "-continue"},
			fails: "choose one",
		},
		{
			name:  "rpc with a prompt",
			args:  []string{"-rpc", "-prompt", "hi"},
			fails: "reads commands from stdin",
		},
		{
			name:  "demo with config flags",
			args:  []string{"-demo", "-model", "a/m1"},
			fails: "ignores",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			opts, err := parseFlags(testCase.args)
			if testCase.fails != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.fails) {
					t.Fatalf("error = %v, want %q", err, testCase.fails)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseFlags: %v", err)
			}
			testCase.expect(t, opts)
		})
	}
}
