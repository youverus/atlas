package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// homeEnv overrides the atlas home directory. It exists so tests and parallel
// checkouts can point at a scratch directory without touching ~/.atlas.
const homeEnv = "ATLAS_HOME"

// homeDirName is the directory atlas owns under the user's home.
const homeDirName = ".atlas"

// DefaultHome returns the atlas home directory: $ATLAS_HOME when set and
// non-empty, otherwise ~/.atlas. The result is absolute, so a relative
// $ATLAS_HOME cannot resolve differently after a chdir.
func DefaultHome() (string, error) {
	if env := strings.TrimSpace(os.Getenv(homeEnv)); env != "" {
		expanded, err := ExpandHome(env)
		if err != nil {
			return "", err
		}
		return filepath.Abs(expanded)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: locate the home directory: %w", err)
	}
	return filepath.Join(home, homeDirName), nil
}

// DefaultPath returns the config file inside DefaultHome.
func DefaultPath() (string, error) {
	home, err := DefaultHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, FileName), nil
}

// ExpandHome replaces a leading "~" with the user's home directory. Every other
// path is returned unchanged, so callers never special-case a relative one.
func ExpandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: locate the home directory: %w", err)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, filepath.FromSlash(path[2:])), nil
}

// WriteTemplate creates home when missing and writes the config template there.
// It reports the path to edit and whether the file was newly created.
//
// An existing config.json is never overwritten: the first run of a fresh
// install and a second run of a configured one take the same code path, and
// only one of them may write.
func WriteTemplate(home string) (string, bool, error) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", false, fmt.Errorf("config: create %s: %w", home, err)
	}
	path := filepath.Join(home, FileName)
	// The appearance file first: config.json names it, so it must never exist
	// while what it points at does not. Writing it is not conditional on
	// anything — an older install that has config.json but no tui.json only
	// reaches this function if the config itself is missing.
	if _, err := writeExclusive(filepath.Join(home, TUIFileName), tuiTemplate); err != nil {
		return path, false, err
	}
	created, err := writeExclusive(path, configTemplate)
	if err != nil {
		return path, false, err
	}
	// The example is a convenience. Failing to write it must not fail the first
	// run, because config.json is the file the user came for.
	_, _ = writeExclusive(filepath.Join(home, ExampleName), exampleTemplate)
	return path, created, nil
}

// writeExclusive writes content only when path does not exist yet, and reports
// whether it did. A partial write is removed rather than left behind, so the
// next run sees a missing file instead of a truncated one.
func writeExclusive(path, content string) (bool, error) {
	// 0600: the file is the intended home of an api key.
	handle, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("config: write %s: %w", path, err)
	}
	if _, err := io.WriteString(handle, content); err != nil {
		handle.Close()
		os.Remove(path)
		return false, fmt.Errorf("config: write %s: %w", path, err)
	}
	if err := handle.Close(); err != nil {
		os.Remove(path)
		return false, fmt.Errorf("config: write %s: %w", path, err)
	}
	return true, nil
}

// configTemplate is the minimum that runs: one provider, one model, and a
// credential read from the environment so the file stays free of secrets. It
// names tui.json rather than carrying the appearance, because the first run
// writes both and the point of the split is that neither file grows into the
// other.
const configTemplate = `{
  "defaultModel": "deepseek/deepseek-chat",
  "tui": "tui.json",
  "providers": {
    "deepseek": {
      "api": "openai-completions",
      "baseUrl": "https://api.deepseek.com/v1",
      "apiKeyEnv": "DEEPSEEK_API_KEY",
      "compat": {"requireReasoningContent": true},
      "models": [
        {
          "id": "deepseek-chat",
          "name": "DeepSeek Chat",
          "contextWindow": 128000,
          "maxTokens": 8192
        }
      ]
    }
  }
}
`

// exampleTemplate shows the fields the minimum template omits: two adapters,
// several models per provider, a model-level baseUrl, reasoning, a declared
// thinking subset, modalities, a literal apiKey for an endpoint that needs no
// real one, and the sandbox block. The appearance is not here at all —
// tuiTemplate is its sample, and pointing at it is what the template does.
const exampleTemplate = `{
  "defaultModel": "deepseek/deepseek-chat",
  "thinkingLevel": "off",
  "tui": "tui.json",
  "sandbox": {"network": true},
  "goal": {"tokenBudget": 100000},
  "update": {"repo": "youverus/atlas"},
  "providers": {
    "deepseek": {
      "api": "openai-completions",
      "baseUrl": "https://api.deepseek.com/v1",
      "apiKeyEnv": "DEEPSEEK_API_KEY",
      "compat": {"requireReasoningContent": true},
      "models": [
        {
          "id": "deepseek-chat",
          "name": "DeepSeek Chat",
          "contextWindow": 128000,
          "maxTokens": 8192
        },
        {
          "id": "deepseek-reasoner",
          "name": "DeepSeek Reasoner",
          "contextWindow": 128000,
          "maxTokens": 8192,
          "reasoning": true,
          "thinkingLevels": ["low", "high", "max"]
        }
      ]
    },
    "anthropic": {
      "api": "anthropic-messages",
      "baseUrl": "https://api.anthropic.com/v1",
      "apiKeyEnv": "ANTHROPIC_API_KEY",
      "models": [
        {
          "id": "claude-sonnet-4-5",
          "name": "Claude Sonnet 4.5",
          "contextWindow": 200000,
          "maxTokens": 8192,
          "reasoning": true,
          "input": ["text", "image"]
        }
      ]
    },
    "local": {
      "api": "openai-completions",
      "baseUrl": "http://localhost:8080/v1",
      "apiKey": "local",
      "models": [
        {
          "id": "qwen3-coder",
          "name": "Qwen3 Coder (llama.cpp)",
          "contextWindow": 131072
        }
      ]
    }
  }
}
`

// tuiTemplate is the appearance file the first run writes. Every key is
// written out at its default on purpose: the file exists to be edited, and a
// reader cannot tune a setting whose name they have never seen. Changing a
// default later therefore does not reach an install that already has this file
// — which is the correct trade for a file whose whole job is to be explicit.
const tuiTemplate = `{
  "theme": "auto",
  "colors": {},
  "blocks": {
    "transcript": {"gap": 1, "frames": true},
    "composer": {"gapAbove": 1, "rows": 8, "frame": true},
    "palette": {"rows": 5},
    "status": {"gapAbove": 0},
    "menu": {"rows": 10, "width": 72},
    "approval": {"width": 64},
    "welcome": {"on": true}
  },
  "roles": {
    "thinking": {"rows": 8}
  }
}
`
