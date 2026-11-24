package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

const minimal = `{
  "defaultModel": "local/qwen",
  "thinkingLevel": "low",
  "sessionDir": "~/sessions",
  "tui": "tui.json",
  "providers": {
    "local": {
      "api": "openai-completions",
      "baseUrl": "http://localhost:8080/v1",
      "apiKey": "secret",
      "compat": {"thinkingDialect": "deepseek"},
      "models": [
        {"id": "qwen", "name": "Qwen", "contextWindow": 32000, "maxTokens": 2048, "reasoning": true, "input": ["text", "image"]}
      ]
    }
  }
}`

func TestLoadDecodesEveryField(t *testing.T) {
	dir := t.TempDir()
	// The config names the interface file, so the two are read together: a
	// test that wrote only one of them would not be testing the indirection.
	write(t, dir, TUIFileName, `{
  "theme": "atlas-dark",
  "colors": {"accent": "#ff0000"},
  "blocks": {"composer": {"gapAbove": 0}}
}`)
	path := write(t, dir, FileName, minimal)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultModel != "local/qwen" || cfg.ThinkingLevel != "low" || cfg.SessionDir != "~/sessions" {
		t.Fatalf("top-level fields = %+v", cfg)
	}
	if cfg.TUI == nil || *cfg.TUI != TUIFileName {
		t.Fatalf("tui = %v, want %q", cfg.TUI, TUIFileName)
	}
	if cfg.UI.Theme != "atlas-dark" {
		t.Fatalf("theme = %q, want atlas-dark", cfg.UI.Theme)
	}
	if got := cfg.UI.Colors["accent"]; got != "#ff0000" {
		t.Fatalf("colors.accent = %q", got)
	}
	if gap := cfg.UI.Blocks.Composer.GapAbove; gap == nil || *gap != 0 {
		t.Fatalf("blocks.composer.gapAbove = %v, want a written zero", gap)
	}
	provider := cfg.Providers["local"]
	if provider.API != "openai-completions" || provider.BaseURL != "http://localhost:8080/v1" || provider.APIKey != "secret" {
		t.Fatalf("provider = %+v", provider)
	}
	if provider.Compat.ThinkingDialect != "deepseek" {
		t.Fatalf("compat = %+v", provider.Compat)
	}
	model := provider.Models[0]
	if model.ID != "qwen" || model.Name != "Qwen" || model.ContextWindow != 32000 || model.MaxTokens != 2048 || !model.Reasoning {
		t.Fatalf("model = %+v", model)
	}
	if len(model.Input) != 2 || model.Input[1] != "image" {
		t.Fatalf("input = %v", model.Input)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	// A misspelled field must fail loudly: the request would otherwise go to
	// the vendor default with the wrong credential and name neither.
	path := write(t, t.TempDir(), FileName, `{
  "providers": {"local": {"api": "openai-completions", "bseUrl": "http://x/v1", "models": [{"id": "m"}]}}
}`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load accepted an unknown field")
	}
	if !strings.Contains(err.Error(), "bseUrl") {
		t.Fatalf("error = %v, want it to name the unknown field", err)
	}
}

func TestLoadRejectsTrailingData(t *testing.T) {
	path := write(t, t.TempDir(), FileName, minimal+"\n{}\n")
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "trailing data") {
		t.Fatalf("error = %v, want trailing data", err)
	}
}

func TestLoadMissingFileKeepsNotExist(t *testing.T) {
	// The first-run flow tells "no config yet" apart from "broken config" with
	// errors.Is, so Load must not wrap the error into something else.
	_, err := Load(filepath.Join(t.TempDir(), FileName))
	if !os.IsNotExist(err) {
		t.Fatalf("error = %v, want os.ErrNotExist", err)
	}
}

func TestLoadRejectsOversizedFile(t *testing.T) {
	path := write(t, t.TempDir(), FileName, strings.Repeat(" ", maxSize+1))
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v, want a size error", err)
	}
}

func TestLoadBrowserConfig(t *testing.T) {
	path := write(t, t.TempDir(), FileName, `{
  "providers": {"local": {"api": "openai-completions", "models": [{"id": "m"}]}},
  "browser": {"headless": true, "executable": "/usr/bin/chromium"}
}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Browser == nil || cfg.Browser.Headless == nil || !*cfg.Browser.Headless {
		t.Fatalf("Browser = %+v, want headless=true", cfg.Browser)
	}
	if cfg.Browser.Executable != "/usr/bin/chromium" {
		t.Fatalf("Executable = %q, want /usr/bin/chromium", cfg.Browser.Executable)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		cfg    Config
		expect string
	}{
		{
			name:   "no providers",
			cfg:    Config{},
			expect: "providers is empty",
		},
		{
			name:   "empty provider name",
			cfg:    Config{Providers: map[string]Provider{" ": validProvider()}},
			expect: "provider name is empty",
		},
		{
			name:   "slash in provider name",
			cfg:    Config{Providers: map[string]Provider{"a/b": validProvider()}},
			expect: "ambiguous",
		},
		{
			name:   "theme given as a path",
			cfg:    Config{UI: UI{Theme: "../atlas-dark"}, Providers: map[string]Provider{"p": validProvider()}},
			expect: "is a name, not a path",
		},
		{
			name:   "theme given as a bare dot",
			cfg:    Config{UI: UI{Theme: ".."}, Providers: map[string]Provider{"p": validProvider()}},
			expect: "is a name, not a path",
		},
		{
			name: "gap larger than the screen can spare",
			cfg: Config{
				UI:        UI{Layout: Layout{Blocks: Blocks{Composer: ComposerBlock{GapAbove: count(99)}}}},
				Providers: map[string]Provider{"p": validProvider()},
			},
			expect: "blocks.composer.gapAbove",
		},
		{
			name: "composer that cannot show a line",
			cfg: Config{
				UI:        UI{Layout: Layout{Blocks: Blocks{Composer: ComposerBlock{Rows: count(0)}}}},
				Providers: map[string]Provider{"p": validProvider()},
			},
			expect: "blocks.composer.rows",
		},
		{
			name: "menu too narrow for its own columns",
			cfg: Config{
				UI:        UI{Layout: Layout{Blocks: Blocks{Menu: MenuBlock{Width: count(10)}}}},
				Providers: map[string]Provider{"p": validProvider()},
			},
			expect: "blocks.menu.width",
		},
		{
			name: "status bar pushed off the bottom",
			cfg: Config{
				UI:        UI{Layout: Layout{Blocks: Blocks{Status: StatusBlock{GapAbove: count(9)}}}},
				Providers: map[string]Provider{"p": validProvider()},
			},
			expect: "blocks.status.gapAbove",
		},
		{
			name: "reasoning taller than the transcript",
			cfg: Config{
				UI:        UI{Layout: Layout{Roles: Roles{Thinking: ThinkingRole{Rows: count(99)}}}},
				Providers: map[string]Provider{"p": validProvider()},
			},
			expect: "roles.thinking.rows",
		},
		{
			name:   "missing api",
			cfg:    Config{Providers: map[string]Provider{"p": withProvider(func(p *Provider) { p.API = "" })}},
			expect: "api is required",
		},
		{
			name: "relative base url",
			cfg: Config{Providers: map[string]Provider{"p": withProvider(func(p *Provider) {
				p.BaseURL = "/v1"
			})}},
			expect: "must start with http",
		},
		{
			name:   "no models",
			cfg:    Config{Providers: map[string]Provider{"p": withProvider(func(p *Provider) { p.Models = nil })}},
			expect: "models is empty",
		},
		{
			name: "empty model id",
			cfg: Config{Providers: map[string]Provider{"p": withProvider(func(p *Provider) {
				p.Models = []Model{{ID: ""}}
			})}},
			expect: "id is required",
		},
		{
			name: "duplicate model id",
			cfg: Config{Providers: map[string]Provider{"p": withProvider(func(p *Provider) {
				p.Models = []Model{{ID: "m"}, {ID: "m"}}
			})}},
			expect: "duplicate id",
		},
		{
			name: "unknown modality",
			cfg: Config{Providers: map[string]Provider{"p": withProvider(func(p *Provider) {
				p.Models = []Model{{ID: "m", Input: []string{"images"}}}
			})}},
			expect: "is not \"text\" or \"image\"",
		},
		{
			name: "unknown max tokens field",
			cfg: Config{Providers: map[string]Provider{"p": withProvider(func(p *Provider) {
				p.Compat.MaxTokensField = "max_output_tokens"
			})}},
			expect: "maxTokensField",
		},
		{
			name: "unknown thinking dialect",
			cfg: Config{Providers: map[string]Provider{"p": withProvider(func(p *Provider) {
				p.Compat.ThinkingDialect = "openrouter"
			})}},
			expect: "thinkingDialect",
		},
		{
			name: "unknown thinking level",
			cfg: Config{
				ThinkingLevel: "hihg",
				Providers:     map[string]Provider{"p": validProvider()},
			},
			expect: "not one of",
		},
		{
			name: "default model names nothing",
			cfg: Config{
				DefaultModel: "p/missing",
				Providers:    map[string]Provider{"p": validProvider()},
			},
			expect: "has no model",
		},
		{
			name: "unknown gear in a model declaration",
			cfg: Config{Providers: map[string]Provider{"p": withProvider(func(p *Provider) {
				p.Models = []Model{{ID: "m", Reasoning: true, ThinkingLevels: []string{"hihg"}}}
			})}},
			expect: "thinkingLevels",
		},
		{
			name: "duplicate gear",
			cfg: Config{Providers: map[string]Provider{"p": withProvider(func(p *Provider) {
				p.Models = []Model{{ID: "m", Reasoning: true, ThinkingLevels: []string{"low", "low"}}}
			})}},
			expect: "duplicate",
		},
		{
			name: "off declared as a gear",
			cfg: Config{Providers: map[string]Provider{"p": withProvider(func(p *Provider) {
				p.Models = []Model{{ID: "m", Reasoning: true, ThinkingLevels: []string{"off", "low"}}}
			})}},
			expect: "always offered",
		},
		{
			name: "gears on a model that cannot think",
			cfg: Config{Providers: map[string]Provider{"p": withProvider(func(p *Provider) {
				p.Models = []Model{{ID: "m", ThinkingLevels: []string{"low"}}}
			})}},
			expect: "reasoning is false",
		},
		{
			name: "default level the default model does not offer",
			cfg: Config{
				DefaultModel:  "p/m",
				ThinkingLevel: "max",
				Providers: map[string]Provider{"p": withProvider(func(p *Provider) {
					p.Models = []Model{{ID: "m", Reasoning: true, ThinkingLevels: []string{"low", "high"}}}
				})},
			},
			expect: "is not offered by p/m",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.cfg.Validate()
			if err == nil {
				t.Fatalf("Validate accepted %+v", testCase.cfg)
			}
			if !strings.Contains(err.Error(), testCase.expect) {
				t.Fatalf("error = %v, want %q", err, testCase.expect)
			}
		})
	}
}

func TestValidateAcceptsMinimal(t *testing.T) {
	cfg := Config{Providers: map[string]Provider{"p": validProvider()}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// TestAClaimedLevelMustBeOneTheModelOffers is the whole point of the
// declaration: a config that names a default the model cannot send is a typo,
// and the menu would otherwise open on a level the request drops.
func TestAClaimedLevelMustBeOneTheModelOffers(t *testing.T) {
	cfg := Config{
		DefaultModel:  "p/m",
		ThinkingLevel: "high",
		Providers: map[string]Provider{"p": withProvider(func(p *Provider) {
			p.Models = []Model{{ID: "m", Reasoning: true, ThinkingLevels: []string{"low", "high"}}}
		})},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// TestGearsListsTheDeclarationInProtocolOrder: the declaration is a set, and
// the axis it drives has one canonical order.
func TestGearsListsTheDeclarationInProtocolOrder(t *testing.T) {
	cases := []struct {
		name   string
		model  Model
		gears  []string
		allows []string
		denies []string
	}{
		{
			name:   "undeclared reasoning model takes every gear",
			model:  Model{ID: "m", Reasoning: true},
			gears:  []string{"off", "low", "medium", "high", "xhigh", "max", "ultra"},
			allows: []string{"low", "ultra"},
		},
		{
			name:   "declared gears come back in protocol order",
			model:  Model{ID: "m", Reasoning: true, ThinkingLevels: []string{"max", "low", "high"}},
			gears:  []string{"off", "low", "high", "max"},
			allows: []string{"off", "low", "high", "max"},
			denies: []string{"medium", "xhigh", "ultra"},
		},
		{
			name:   "off is a switch every reasoning model can throw",
			model:  Model{ID: "m", Reasoning: true, ThinkingLevels: []string{"high"}},
			gears:  []string{"off", "high"},
			allows: []string{"off", "high"},
			denies: []string{"low", "max"},
		},
		{
			name:   "a model without thinking has no axis",
			model:  Model{ID: "m"},
			gears:  nil,
			allows: []string{"off"},
			denies: []string{"low", "high"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			gears := testCase.model.Gears()
			if len(gears) != len(testCase.gears) {
				t.Fatalf("Gears() = %v, want %v", gears, testCase.gears)
			}
			for i, gear := range gears {
				if gear != testCase.gears[i] {
					t.Fatalf("Gears() = %v, want %v", gears, testCase.gears)
				}
			}
			for _, level := range testCase.allows {
				if !testCase.model.AllowsGear(level) {
					t.Fatalf("AllowsGear(%q) = false, want true", level)
				}
			}
			for _, level := range testCase.denies {
				if testCase.model.AllowsGear(level) {
					t.Fatalf("AllowsGear(%q) = true, want false", level)
				}
			}
		})
	}
}

func TestKeyPrefersTheEnvironment(t *testing.T) {
	t.Setenv("ATLAS_TEST_KEY", "from-env")
	provider := Provider{APIKey: "from-file", APIKeyEnv: "ATLAS_TEST_KEY"}
	if key := provider.Key(); key != "from-env" {
		t.Fatalf("Key() = %q, want the environment value", key)
	}

	// An empty variable must not blank out the file value: shells routinely
	// export a name before a value is filled in.
	t.Setenv("ATLAS_TEST_KEY", "")
	if key := provider.Key(); key != "from-file" {
		t.Fatalf("Key() = %q, want the file value", key)
	}

	plain := Provider{APIKey: "only-file"}
	if key := plain.Key(); key != "only-file" {
		t.Fatalf("Key() = %q", key)
	}
	if key := (Provider{}).Key(); key != "" {
		t.Fatalf("Key() = %q, want empty", key)
	}

	// Copy-pasted credentials often carry leading/trailing whitespace or newlines;
	// whitespace-only env values must behave like an unset variable.
	t.Setenv("ATLAS_TEST_KEY", "  \t\n")
	padded := Provider{APIKey: "  sk-pasted\n", APIKeyEnv: " ATLAS_TEST_KEY "}
	if key := padded.Key(); key != "sk-pasted" {
		t.Fatalf("Key() = %q, want trimmed file key when env is blank", key)
	}
	t.Setenv("ATLAS_TEST_KEY", " sk-from-env \n")
	if key := padded.Key(); key != "sk-from-env" {
		t.Fatalf("Key() = %q, want trimmed env key", key)
	}
}

func validProvider() Provider {
	return Provider{API: "openai-completions", Models: []Model{{ID: "m"}}}
}

func withProvider(mutate func(*Provider)) Provider {
	provider := validProvider()
	mutate(&provider)
	return provider
}

// count and flag build the pointers the layout's optional fields need. The
// pointer is the whole contract: a test that could not write an explicit zero
// would be testing a type that cannot express what the file says.
func count(value int) *int  { return &value }
func flag(value bool) *bool { return &value }

// minimalWithWeb is the smallest file that also declares the web block. The
// providers stay minimal because the point is that neither depends on the
// other.
const minimalWithWeb = `{
  "providers": {"local": {"api": "openai-completions", "models": [{"id": "qwen"}]}},
  "web": {"baseUrl": "https://api.example.com", "apiKey": "literal", "apiKeyEnv": "ATLAS_TEST_WEB_KEY"}
}`

// TestTheWebBlockHasItsOwnCredential pins the split: the key that reads a page
// has nothing to do with the key that answers the prompt, so it lives in its
// own block and follows the same env-wins rule.
func TestTheWebBlockHasItsOwnCredential(t *testing.T) {
	t.Setenv("ATLAS_TEST_WEB_KEY", "from-env")
	cfg, err := Load(write(t, t.TempDir(), FileName, minimalWithWeb))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Web == nil {
		t.Fatal("web block was not decoded")
	}
	if got := cfg.Web.BaseURL; got != "https://api.example.com" {
		t.Fatalf("web.baseUrl = %q", got)
	}
	if got := cfg.Web.Key(); got != "from-env" {
		t.Fatalf("Key() = %q, want the variable to win", got)
	}
	// An unset variable falls back to the literal, exactly as a provider does:
	// two spellings of "which key do I use" is a difference nobody finds until
	// a request fails.
	t.Setenv("ATLAS_TEST_WEB_KEY", "")
	if got := cfg.Web.Key(); got != "literal" {
		t.Fatalf("Key() = %q, want the literal", got)
	}
}

// TestTheWebBaseURLIsAnOrigin covers the one mistake this field invites: the
// provider blocks above it end in /v1, and this one must not, because the tool
// paths already start there.
func TestTheWebBaseURLIsAnOrigin(t *testing.T) {
	for _, baseURL := range []string{"https://api.example.com/v1", "https://api.example.com/v1/"} {
		path := write(t, t.TempDir(), FileName, `{
  "providers": {"local": {"api": "openai-completions", "models": [{"id": "qwen"}]}},
  "web": {"baseUrl": "`+baseURL+`"}
}`)
		_, err := Load(path)
		if err == nil {
			t.Fatalf("%s was accepted", baseURL)
		}
		if !strings.Contains(err.Error(), "origin") {
			t.Fatalf("error = %v, want it to say the value must be an origin", err)
		}
	}
}

// TestNoWebBlockMeansNoWebTools: the block is a pointer precisely so that an
// omitted one is a statement rather than an empty credential.
func TestNoWebBlockMeansNoWebTools(t *testing.T) {
	cfg, err := Load(write(t, t.TempDir(), FileName, `{
  "providers": {"local": {"api": "openai-completions", "models": [{"id": "qwen"}]}}
}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Web != nil {
		t.Fatalf("web = %+v, want nil when the block is absent", cfg.Web)
	}
}

// TestTheSandboxBlockIsOffUnlessAskedFor covers the two statements the pointer
// separates: no block runs commands with the process's own permissions, and an
// empty block is a reader who asked for confinement.
func TestTheSandboxBlockIsOffUnlessAskedFor(t *testing.T) {
	absent, err := Load(write(t, t.TempDir(), FileName, `{
  "providers": {"local": {"api": "openai-completions", "models": [{"id": "qwen"}]}}
}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if absent.Sandbox != nil {
		t.Fatalf("sandbox = %+v, want nil when the block is absent", absent.Sandbox)
	}

	empty, err := Load(write(t, t.TempDir(), FileName, `{
  "providers": {"local": {"api": "openai-completions", "models": [{"id": "qwen"}]}},
  "sandbox": {}
}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if empty.Sandbox == nil {
		t.Fatal("an empty sandbox block was dropped")
	}
	// Network defaults to allowed: a build that cannot fetch a module fails in a
	// way that reads as a broken toolchain rather than as a policy decision.
	if !empty.Sandbox.AllowsNetwork() {
		t.Fatal("network defaults to refused, which would break a build out of the box")
	}

	offline, err := Load(write(t, t.TempDir(), FileName, `{
  "providers": {"local": {"api": "openai-completions", "models": [{"id": "qwen"}]}},
  "sandbox": {"network": false}
}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if offline.Sandbox.AllowsNetwork() {
		t.Fatal(`"network": false was ignored`)
	}
}

func TestSandboxDenyReadAndPermissionBlock(t *testing.T) {
	cfg, err := Load(write(t, t.TempDir(), FileName, `{
  "providers": {"local": {"api": "openai-completions", "models": [{"id": "qwen"}]}},
  "sandbox": {"denyRead": ["~/.ssh", "/etc/secrets"]},
  "permission": {"mode": "default", "autoAllowBashIfSandboxed": true}
}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Sandbox.DenyRead) != 2 || cfg.Sandbox.DenyRead[0] != "~/.ssh" {
		t.Fatalf("sandbox.denyRead = %v", cfg.Sandbox.DenyRead)
	}
	if cfg.Permission == nil || cfg.Permission.EffectiveMode() != "default" || !cfg.Permission.AutoAllowBashIfSandboxed {
		t.Fatalf("permission = %+v", cfg.Permission)
	}

	if _, err := Load(write(t, t.TempDir(), FileName, `{
  "providers": {"local": {"api": "openai-completions", "models": [{"id": "qwen"}]}},
  "permission": {"mode": "approve"}
}`)); err == nil || !strings.Contains(err.Error(), "bypassPermissions") {
		t.Fatalf("unknown mode error = %v, want the four mode names", err)
	}

	// Every one of the four postures loads, and "auto" is the one that needs a
	// classifier key to point somewhere.
	for _, mode := range []string{"default", "auto", "bypassPermissions", "off"} {
		auto, err := Load(write(t, t.TempDir(), FileName, fmt.Sprintf(`{
  "providers": {"local": {"api": "openai-completions", "models": [{"id": "qwen"}]}},
  "permission": {"mode": %q}
}`, mode)))
		if err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
		if auto.Permission.EffectiveMode() != mode {
			t.Fatalf("mode %q read back as %q", mode, auto.Permission.EffectiveMode())
		}
	}

	if _, err := Load(write(t, t.TempDir(), FileName, `{
  "providers": {"local": {"api": "openai-completions", "models": [{"id": "qwen"}]}},
  "permission": {"mode": "auto", "classifier": "qwen"}
}`)); err == nil || !strings.Contains(err.Error(), "provider/model") {
		t.Fatalf("classifier without a slash error = %v, want provider/model", err)
	}

	if _, err := Load(write(t, t.TempDir(), FileName, `{
  "providers": {"local": {"api": "openai-completions", "models": [{"id": "qwen"}]}},
  "sandbox": {"denyRead": ["  "]}
}`)); err == nil || !strings.Contains(err.Error(), "denyRead[0]") {
		t.Fatalf("empty denyRead error = %v, want denyRead[0]", err)
	}
}
